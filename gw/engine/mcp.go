package engine

// /mcp tools/call on the go engine (P4g, slice S9): lib/gateway/index.js route's /mcp branch, mcpFor and guardedTool,
// and lib/mcp/protocol.js handle, for one JSON-RPC tools/call request per POST. PlanMCP decides, without side effects,
// whether the engine serves the request; everything else on /mcp (other methods, batches, notifications, responses,
// invalid requests, initialize, ping, tools/list, a bad version header or body) is handed to the control plane, which
// answers before any tool or guardrail runs (Decision 1). The tool itself runs in the control plane (Decision 2): the
// engine checks the arguments (CheckToolArgs), sends them, and builds the reply and the usage line from the tool's
// answer (ToolReply). Both are pure and proven by gw/testdata/mcp (scripts/fixtures/gen-mcp.js).

import (
	"errors"
	"fmt"
	"strings"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/guard"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"
)

// SubMCP is the /mcp sub-class (Subs["mcp"]).
const SubMCP = "mcp"

// UpTool: a plan whose call is the control plane's tool endpoint.
const UpTool = "tool"

// MCPVersions is lib/mcp/protocol.js SUPPORTED_VERSIONS (pinned by gw/testdata/mcp/versions.json).
var MCPVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

// checkText is guard.CheckText; a test replaces it to prove the failure path.
var checkText = guard.CheckText

// ToolState is what the arguments check found: its hits and action, and whether a check in log mode failed.
type ToolState struct {
	Hits   []guard.Hit
	Action string
	Failed bool
}

// PlanMCP decides for a request to /mcp, at now (epoch ms). It reads and changes nothing: the rate limit is the
// caller's (Take when serving).
func (s *State) PlanMCP(r *Request, now int64) Plan {
	key, msg, why := s.admit(r, now, "mcp")
	if why != "" {
		return handoff(why)
	}
	// Node joins a repeated header of this name with ", " (req.headers); present and not supported: JS's 400.
	if vals := r.Header.Values("Mcp-Protocol-Version"); len(vals) > 0 {
		v := strings.Join(vals, ", ")
		ok := false
		for _, x := range MCPVersions {
			ok = ok || x == v
		}
		if !ok {
			return handoff("mcp: protocol version not supported")
		}
	}
	// handle(msg): a response, an invalid request and a notification are the control plane's.
	if j := msg.Get("jsonrpc"); j == nil || j.Kind != String || j.S != "2.0" {
		return handoff("mcp: not a JSON-RPC 2.0 request")
	}
	m := msg.Get("method")
	if m == nil || m.Kind != String {
		return handoff("mcp: a response or an invalid request")
	}
	id := msg.Get("id")
	if id == nil {
		return handoff("mcp: a notification")
	}
	if m.S != "tools/call" {
		return handoff("mcp: not tools/call")
	}
	params := msg.Get("params")
	if params == nil || params.Kind != Object {
		return handoff("mcp: params not an object with a name") // null or absent: {} has no name; else -32602
	}
	name := params.Get("name")
	if name == nil || name.Kind != String {
		return handoff("mcp: params.name not a string")
	}
	args := params.Get("arguments")
	switch {
	case args == nil || args.Kind == Null:
		args = NewObject()
	case args.Kind != Object:
		return handoff("mcp: params.arguments not an object")
	}
	tier := "external"
	if strings.HasPrefix(name.S, "acc_") {
		tier = "private"
	}
	kind := "mcp"
	provider := name.S
	if i := strings.IndexByte(name.S, '_'); i > 0 {
		provider = name.S[:i]
	}
	p := Plan{Key: key, Class: SubMCP, Upstream: UpTool, Shape: ShapeOpenAI, Format: "openai", Model: name.S,
		RPCID: id, Tool: name.S, ToolArgs: args, ToolTier: tier,
		Base: ipc.UsageRecord{KeyID: key.ID, Provider: provider, Model: name.S, Kind: &kind}}
	// guardedTool: resolvePolicy then inScope. Every check off still takes the in-scope path (its result text is read,
	// and reading it can throw), so the scope alone decides.
	if pol := PolicyFor(s.Gateway.Guardrails, key); pol.Scope == "all" || tier != "private" {
		p.Policy = &pol
	}
	return p
}

// guarded runs a check; a panic out of it is a failed check (fail closed, as Plan.check).
func guarded[T any](f func() (T, error)) (res T, err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("guardrails: %v", v)
		}
	}()
	return f()
}

// CheckToolArgs runs the arguments check when p.Policy is set. refusal != nil: the reply to write and its line.
// Otherwise args is the JSON text to send the tool (JSON.stringify of the checked arguments) and st what the check
// found (for the result step).
func (s *State) CheckToolArgs(p *Plan, ms float64) (args []byte, st ToolState, refusal *Answer) {
	if p.Policy == nil {
		return p.ToolArgs.Stringify(), ToolState{}, nil
	}
	pol := *p.Policy
	res, err := guarded(func() (guard.Result, error) { return checkBody(toJSV(p.ToolArgs), pol, "arguments") })
	if err != nil {
		var b *guard.Block
		if errors.As(err, &b) {
			a := rpcReply(p, rpcError(-32602, b.Error()))
			a.Usage, a.NoLine = toolLine(p, 400, ms, "blocked by guardrails", outcome("blocked", b.Hits, false)), false
			return nil, ToolState{}, &a
		}
		a := rpcReply(p, rpcError(-32603, "guardrails failed"))
		a.Usage, a.NoLine = toolLine(p, 500, ms, "guardrails failed", nil), false
		return nil, ToolState{}, &a
	}
	return []byte(jsv.MustStringify(res.Body)), ToolState{Hits: res.Hits, Action: res.Action, Failed: res.Error}, nil
}

// ToolReply builds the reply from the tool endpoint's answer ({"r": …} or {"thrown": true}; nil: the call failed) and
// the line JS writes (Answer.NoLine when none): guardedTool after callTool, then handle's tools/call reply.
func (s *State) ToolReply(p *Plan, st ToolState, endpoint []byte, ms float64) Answer {
	internal := func() Answer { a := rpcReply(p, rpcError(-32603, "Internal error")); a.NoLine = true; return a }
	if endpoint == nil {
		return internal() // the control plane gone mid-call (known difference 1)
	}
	v, err := jsv.Parse(endpoint)
	o := jsv.Obj(v)
	if err != nil || o == nil || jsv.Truthy(o.Get("thrown")) {
		return internal() // callTool threw: handle's catch
	}
	r := o.Get("r")
	var line *ipc.UsageRecord
	if p.Policy != nil {
		var reply jsv.Value
		reply, line = s.resultCheck(p, st, r, ms)
		if reply != nil {
			a := rpcReply(p, jsv.NewObject().Set("result", reply))
			a.Usage, a.NoLine = *line, false
			return a
		}
	}
	// handle: r.error ? fail(id, r.error.code, r.error.message) : ok(id, r.result); r null or undefined throws.
	var a Answer
	switch {
	case jsv.IsNullish(r):
		a = internal()
	case jsv.Truthy(jsv.Get(r, "error")):
		e := jsv.Get(r, "error")
		x := jsv.NewObject().Set("code", jsv.Get(e, "code")).Set("message", jsv.Get(e, "message"))
		a = rpcReply(p, jsv.NewObject().Set("error", x))
	default:
		a = rpcReply(p, jsv.NewObject().Set("result", jsv.Get(r, "result")))
	}
	if line != nil {
		a.Usage, a.NoLine = *line, false
	}
	return a
}

// resultCheck is guardedTool's in-scope tail: the injection check of the result's text and the line. reply is the
// tool error that replaces a blocked or failed result (nil: r goes on); line the usage line (nil: none).
func (s *State) resultCheck(p *Plan, st ToolState, r jsv.Value, ms float64) (jsv.Value, *ipc.UsageRecord) {
	refused := func(err error) (jsv.Value, *ipc.UsageRecord) {
		msg := "guardrails failed"
		rec := toolLine(p, 500, ms, msg, nil)
		var b *guard.Block
		if errors.As(err, &b) {
			msg = b.Error()
			rec = toolLine(p, 400, ms, "blocked by guardrails",
				outcome("blocked", append(append([]guard.Hit(nil), st.Hits...), b.Hits...), false))
		}
		part := jsv.NewObject().Set("type", "text").Set("text", msg)
		return jsv.NewObject().Set("content", jsv.Arr(part)).Set("isError", true), &rec
	}
	var content []jsv.Value
	if jsv.Truthy(r) {
		if res := jsv.Get(r, "result"); jsv.Truthy(res) {
			if c := jsv.Ary(jsv.Get(res, "content")); c != nil {
				content = c.Items
			}
		}
	}
	// Every text the caller's model will read: text parts and the text of embedded resources, joined by "\n".
	parts := make([]string, len(content))
	for i, c := range content {
		if !jsv.Truthy(c) {
			continue
		}
		switch t, _ := jsv.Get(c, "type").(string); t {
		case "text":
			str, err := toStringJS(jsv.Get(c, "text"))
			if err != nil {
				return refused(err)
			}
			parts[i] = str
		case "resource":
			if res := jsv.Get(c, "resource"); jsv.Truthy(res) {
				parts[i], _ = jsv.Get(res, "text").(string)
			}
		}
	}
	pol := *p.Policy
	text := strings.Join(parts, "\n")
	out, err := guarded(func() (guard.TextResult, error) { return checkText(text, pol, "result") })
	if err != nil {
		return refused(err)
	}
	hits := append(append([]guard.Hit(nil), st.Hits...), out.Hits...)
	if len(hits) == 0 && !st.Failed && !out.Error {
		return nil, nil
	}
	status, failure := 200, ""
	switch {
	case jsv.Truthy(r) && jsv.Truthy(jsv.Get(r, "error")):
		status, failure = 502, "integration error"
	case jsv.Truthy(r) && jsv.Truthy(jsv.Get(r, "result")) && jsv.Truthy(jsv.Get(jsv.Get(r, "result"), "isError")):
		failure = "tool error"
	}
	action := "logged"
	if st.Action == "redacted" {
		action = "redacted"
	}
	rec := toolLine(p, status, ms, failure, outcome(action, hits, st.Failed || out.Error))
	return nil, &rec
}

// errNoPrimitive is String(v)'s TypeError for an object whose own toString is not a function (a JSON key "toString"):
// OrdinaryToPrimitive then reaches valueOf, which gives the object back or is not callable either.
var errNoPrimitive = errors.New("TypeError: Cannot convert object to primitive value")

// toStringJS is String(v) for a parsed JSON value, with its TypeError.
func toStringJS(v jsv.Value) (string, error) {
	switch x := v.(type) {
	case *jsv.Object:
		if x.Has("toString") {
			return "", errNoPrimitive
		}
		return "[object Object]", nil
	case *jsv.Array:
		var b strings.Builder
		for i, it := range x.Items {
			if i > 0 {
				b.WriteByte(',')
			}
			if jsv.IsNullish(it) {
				continue
			}
			s, err := toStringJS(it)
			if err != nil {
				return "", err
			}
			b.WriteString(s)
		}
		return b.String(), nil
	}
	return jsv.ToString(v), nil
}

// rpcError is { error: { code, message } }.
func rpcError(code float64, message string) *jsv.Object {
	return jsv.NewObject().Set("error", jsv.NewObject().Set("code", code).Set("message", message))
}

// rpcReply is json(res, 200, { jsonrpc: '2.0', id, ...tail }).
func rpcReply(p *Plan, tail *jsv.Object) Answer {
	o := jsv.NewObject().Set("jsonrpc", "2.0").Set("id", toJSV(p.RPCID))
	for _, k := range tail.Keys() {
		o.Set(k, tail.Get(k))
	}
	return Answer{Status: 200, Header: answerHeader(), Body: []byte(jsv.MustStringify(o)), NoLine: true}
}

// toolLine is guardedTool's rec(...): the plan's base, status, ms, cost 0, the error text ("" none) and the outcome.
func toolLine(p *Plan, status int, ms float64, errText string, g *ipc.GuardrailOutcome) ipc.UsageRecord {
	rec := p.Base
	rec.Status, rec.Ms, rec.Cost, rec.Guard = int64(status), ms, 0, g
	if errText != "" {
		rec.Error = &errText
	}
	return rec
}
