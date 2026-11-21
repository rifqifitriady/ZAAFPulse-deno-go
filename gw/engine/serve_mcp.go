package engine

// Serving /mcp tools/call (P4g, slice S9, Decisions 2, 4 and 5; spec D2): after the key and the rate limit (Serve), the
// arguments check, the tool run by the control plane over the internal port (POST /_zp/mcp/call, lib/front/tools.js),
// the result check and the reply (mcp.go), the usage line when JS writes one, and the spans as lib/otel.js records them
// for a request that is no model endpoint: the server span only when something happened inside the request (a usage
// line's client span, or a span the tool started under it in the control plane), never on its own. No budget (JS has
// none on /mcp) and no call journal entry (Decision 5: JS writes the line only at the end and only sometimes).

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"
)

// ToolCall calls the control plane's tool endpoint with a JSON body and gives its status and answer.
type ToolCall func(ctx context.Context, body []byte) (status int, answer []byte, err error)

// SetTools gives the engine the front's call to the control plane's tool endpoint (the internal port, as the gateway
// itself); without it a tools/call answers -32603 Internal error, with no line.
func (e *Engine) SetTools(call func(ctx context.Context, body []byte) (status int, answer []byte, err error)) {
	f := ToolCall(call)
	e.tools.Store(&f)
}

// serveMCP serves a planned tools/call after its rate limit.
func (e *Engine) serveMCP(w http.ResponseWriter, r *http.Request, s *State, p *Plan, started time.Time, tr *callTrace) {
	elapsed := func() float64 { return float64(time.Since(started).Microseconds()) / 1000 }
	child := false
	args, st, refusal := s.CheckToolArgs(p, elapsed())
	var a Answer
	if refusal != nil {
		a = *refusal
	} else {
		var endpoint []byte
		endpoint, child = e.callTool(r, s, p, args, tr)
		a = s.ToolReply(p, st, endpoint, elapsed())
	}
	var rec *ipc.UsageRecord
	if !a.NoLine {
		line := a.Usage
		line.Ts = e.now()
		if tr != nil {
			line.TraceID = &tr.tc.TraceID
		}
		e.emitLine(line, false, 0) // no budget on /mcp: never marked unchecked
		rec = &line
	}
	if r.Context().Err() == nil {
		write(w, a)
	}
	if tr != nil && (rec != nil || child) {
		flushAnswer(w)
		var prov *ipc.ProviderSpan
		if rec != nil {
			prov = toolProvSpan(s, rec.Provider)
		}
		e.spans(s, tr, a.Status, started, time.Now(), rec, prov, nil) // /mcp has no observer (otel.js llmFormat)
	}
}

// callTool sends the checked arguments to the tool endpoint with a context the client's leaving does not cancel and no
// time limit of its own (Decision 2: the tool's own timeouts apply). endpoint is nil when the call failed (no endpoint,
// a transport error, a status but 200); child is whether the control plane started a span under this request.
func (e *Engine) callTool(r *http.Request, s *State, p *Plan, args []byte, tr *callTrace) (endpoint []byte, child bool) {
	f := e.tools.Load()
	if f == nil {
		return nil, false
	}
	status, answer, err := (*f)(context.WithoutCancel(r.Context()), toolBody(p, args, e.toolTrace(r, s, tr)))
	if err != nil || status != http.StatusOK {
		if err != nil {
			e.logf("go engine: mcp: the tool endpoint failed: %v", err)
		} else {
			e.logf("go engine: mcp: the tool endpoint answered %d", status)
		}
		return nil, false
	}
	var flag struct {
		Child bool `json:"child"`
	}
	_ = json.Unmarshal(answer, &flag)
	return answer, flag.Child
}

// toolTrace is the trace the tool runs under (lib/otel.js within): the request's span when the engine traces it; for
// a request whose traceparent is not sampled, that trace unsampled, so the tool starts no span (as otel.js toolCall
// under an unsampled request); nil when spans are off.
func (e *Engine) toolTrace(r *http.Request, s *State, tr *callTrace) *TraceCtx {
	if tr != nil {
		return &tr.tc
	}
	if !s.Spans() || e.emitSpan == nil {
		return nil
	}
	tc := StartTrace(r.Header, e.ids)
	return &tc // a sampled one is traced (tr), so this is the unsampled case
}

// toolBody is the endpoint's request: {"name", "arguments" (the checked arguments' JSON text), "keyId" (the call runs
// as the key, for the integrations' audit), "trace"?}.
func toolBody(p *Plan, args []byte, tc *TraceCtx) []byte {
	var b strings.Builder
	b.WriteString(`{"name":`)
	jsv.Quote(&b, p.Tool)
	b.WriteString(`,"arguments":`)
	b.Write(args)
	b.WriteString(`,"keyId":`)
	jsv.Quote(&b, p.Key.ID)
	if tc != nil {
		b.WriteString(`,"trace":{"traceId":`)
		jsv.Quote(&b, tc.TraceID)
		b.WriteString(`,"spanId":`)
		jsv.Quote(&b, tc.SpanID)
		if tc.Sampled {
			b.WriteString(`,"sampled":true`)
		} else {
			b.WriteString(`,"sampled":false`)
		}
		if tc.TraceState != "" {
			b.WriteString(`,"traceState":`)
			jsv.Quote(&b, tc.TraceState)
		}
		b.WriteByte('}')
	}
	b.WriteByte('}')
	return []byte(b.String())
}

// providerNames is lib/otel.js PROVIDER_NAME.
var providerNames = map[string]string{"openai": "openai", "anthropic": "anthropic", "anthropic-openai": "anthropic",
	"gemini": "gcp.gemini", "vertex": "gcp.vertex_ai", "bedrock": "aws.bedrock", "azure-openai": "azure.ai.openai",
	"mistral": "mistral", "cohere": "cohere", "xai": "x_ai", "deepseek": "deepseek", "perplexity": "perplexity",
	"groq": "groq", "moonshot": "moonshot_ai"}

// toolProvSpan is otel.js modelSpan's providerSpanAttrs for an mcp line, whose provider is the tool name's prefix: the
// gateway provider of that id when there is one (its pushed span attributes), else the name PROVIDER_NAME gives the id
// (nil: the id itself, as ClientSpan names it).
func toolProvSpan(s *State, id string) *ipc.ProviderSpan {
	for i := range s.Gateway.Providers {
		if p := &s.Gateway.Providers[i]; p.ID == id && p.Span != nil {
			return p.Span
		}
	}
	if n, ok := providerNames[id]; ok {
		return &ipc.ProviderSpan{Name: n}
	}
	return nil
}
