package engine

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/guard"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/guard/jsre"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

// The gateway's trace spans as lib/otel.js builds them and lib/trace/recorder.js toRecord stores them (P4f D1–D7):
// the SERVER span of a request the engine answered and the CLIENT span of each usage line, byte-equal to the JS records
// by the fixtures scripts/fixtures/gen-spans.js writes from the real lib/otel.js (gw/testdata/spans).

// maxAttrValue is lib/otel.js MAX_VALUE: cleanValue cuts a longer string (in UTF-16 units) and marks it.
const maxAttrValue = 8192

// ServerFacts is what the server span reads of a request the engine answered (otel.js requestContext's done).
type ServerFacts struct {
	Method, Path string
	Proto        string // "1.1" or "1.0": Node's req.httpVersion (the front speaks HTTP/1.1 only)
	TLS          bool
	UserAgent    string // the first User-Agent value; "" adds no attribute
	Status       int
	StartUs      int64
	EndUs        int64
}

// LLMFormat is lib/otel.js llmFormat: the model endpoint a request is, or "" (a request that is not one gets a server
// span only when something happened inside it, which the engine never answers).
func LLMFormat(method, path string) string {
	if method != "POST" {
		return ""
	}
	switch path {
	case "/v1/chat/completions":
		return "openai"
	case "/v1/messages":
		return "anthropic"
	case "/v1/responses":
		return "responses"
	case "/v1/embeddings":
		return "embeddings"
	}
	if geminiPathRe.MatchString(path) {
		return "gemini"
	}
	return ""
}

var geminiPathRe = regexp.MustCompile(`^/v1beta/models/.+:(generateContent|streamGenerateContent)$`)

// ServerSpan is the SERVER span "{method} {route}" of a request the engine answered.
func ServerSpan(tc TraceCtx, f ServerFacts) ipc.TraceSpanBody {
	route := routeOf(f.Path)
	a := NewObject()
	setStr(a, "http.request.method", f.Method)
	setStr(a, "http.route", route)
	setStr(a, "url.path", f.Path)
	scheme := "http"
	if f.TLS {
		scheme = "https"
	}
	setStr(a, "url.scheme", scheme)
	setStr(a, "network.protocol.version", f.Proto)
	a.Set("http.response.status_code", Num(float64(f.Status)))
	if f.UserAgent != "" {
		setStr(a, "user_agent.original", jsSlice(f.UserAgent, 256))
	}
	ok := true
	if f.Status >= 500 {
		setStr(a, "error.type", strconv.Itoa(f.Status)) // span.fail(String(status)): no message
		ok = false
	}
	parent := ""
	if tc.Remote {
		parent = tc.ParentID
	}
	return record(tc, tc.SpanID, parent, "server", jsSlice(f.Method+" "+route, 256), f.StartUs, f.EndUs, a, ok, "", tc.Remote)
}

// ClientSpan is modelSpan and finishModelSpan for one usage line: the CLIENT span "chat {model}" (or "embeddings
// {model}"), child of the server span. prov is the pushed provider's span attributes (nil: the provider is unknown);
// obs is what the answer showed (nil: no observer, as for embeddings); mask is the handoff's mask list.
func ClientSpan(tc TraceCtx, spanID string, rec *ipc.UsageRecord, prov *ipc.ProviderSpan, obs *Observed, mask func(string) string) ipc.TraceSpanBody {
	op := "chat"
	if rec.Kind != nil && *rec.Kind == "embeddings" {
		op = "embeddings"
	}
	a := NewObject()
	setStr(a, "gen_ai.operation.name", op)
	name := rec.Provider
	if name == "" {
		name = "_OTHER"
	}
	if prov != nil {
		name = prov.Name
	}
	setStr(a, "gen_ai.provider.name", name)
	setStr(a, "gen_ai.request.model", rec.Model)
	resp := rec.Model
	if rec.RoutedTo != nil && *rec.RoutedTo != "" {
		resp = *rec.RoutedTo
	}
	setStr(a, "gen_ai.response.model", resp)
	if prov != nil && prov.Address != nil && prov.Port != nil {
		setStr(a, "server.address", *prov.Address)
		a.Set("server.port", Num(float64(*prov.Port)))
	}
	if rec.Stream != nil {
		a.Set("gen_ai.request.stream", &Value{Kind: Bool, B: *rec.Stream})
	}
	in, out, cached := deref(rec.Input), deref(rec.Output), deref(rec.Cached)
	if in != 0 || out != 0 || rec.Status < 400 {
		a.Set("gen_ai.usage.input_tokens", Num(float64(in)))
		a.Set("gen_ai.usage.output_tokens", Num(float64(out)))
	}
	if cached != 0 {
		a.Set("gen_ai.usage.cache_read.input_tokens", Num(float64(cached)))
	}
	if rec.TTFT.Set && !rec.TTFT.Null && rec.Stream != nil && *rec.Stream { // e.ttft != null && e.stream
		a.Set("gen_ai.response.time_to_first_chunk", Num(rec.TTFT.V/1000))
	}
	if rec.SessionID != nil && *rec.SessionID != "" {
		setStr(a, "gen_ai.conversation.id", *rec.SessionID)
	}
	if rec.Agent != nil && *rec.Agent != "" {
		setStr(a, "gen_ai.agent.id", *rec.Agent)
	}
	a.Set("zaafpulse.cost.usd", Num(float64(rec.Cost)/1e9)) // lib/money.js fromNano
	key := rec.KeyID
	if key == "" {
		key = "dashboard"
	}
	setStr(a, "zaafpulse.api_key.id", key)
	setStr(a, "zaafpulse.provider.id", rec.Provider)
	kind := "chat"
	if rec.Kind != nil && *rec.Kind != "" {
		kind = *rec.Kind
	}
	setStr(a, "zaafpulse.request.kind", kind)
	if rec.Format != nil && *rec.Format != "" {
		setStr(a, "zaafpulse.request.format", *rec.Format)
	}
	if rec.Requested != nil && *rec.Requested != "" {
		setStr(a, "zaafpulse.request.model_alias", *rec.Requested)
	}
	if rec.SavedTokens != nil && *rec.SavedTokens != 0 {
		a.Set("zaafpulse.token_saver.saved_tokens", Num(float64(*rec.SavedTokens)))
	}
	failed := rec.Status >= 400 || (rec.Error != nil && *rec.Error != "")
	msg := ""
	if failed {
		typ := "_OTHER"
		switch {
		case rec.Status == 599:
			typ = "network_error"
		case rec.Status != 0:
			typ = strconv.FormatInt(rec.Status, 10)
		}
		setStr(a, "error.type", typ)
		text := "HTTP " + strconv.FormatInt(rec.Status, 10)
		if rec.Error != nil && *rec.Error != "" {
			text = *rec.Error
		}
		if mask != nil {
			text = mask(text)
		}
		msg = jsSlice(scrub(text), 500)
	}
	if obs != nil && rec.Status < 400 {
		if obs.ID != "" {
			setStr(a, "gen_ai.response.id", jsSlice(obs.ID, 256))
		}
		if len(obs.Reasons) > 0 {
			rs := obs.Reasons
			if len(rs) > 16 {
				rs = rs[:16]
			}
			arr := &Value{Kind: Array}
			for _, r := range rs {
				arr.A = append(arr.A, Str(cleanString(r)))
			}
			a.Set("gen_ai.response.finish_reasons", arr)
		}
	}
	end := float64(rec.Ts)
	start := jsRound((end - rec.Ms) * 1000) // otel.js msToNs(end - ms), in microseconds
	endUs := jsRound(end * 1000)
	return record(tc, spanID, tc.SpanID, "client", jsSlice(op+" "+rec.Model, 256), int64(start), int64(endUs), a, !failed, msg, false)
}

// record is lib/trace/recorder.js toRecord, plus the OTLP extras (P4f D11).
func record(tc TraceCtx, id, parent, kind, name string, startUs, endUs int64, a *Value, ok bool, msg string, remote bool) ipc.TraceSpanBody {
	if endUs < startUs {
		endUs = startUs
	}
	r := ipc.SpanRecord{T: tc.TraceID, S: id, N: name, K: kind, St: startUs, D: endUs - startUs, OK: ok,
		Src: "gateway", Svc: "zaafpulse", A: json.RawMessage(a.Stringify())}
	if parent != "" {
		p := parent
		r.P = &p
	}
	if !ok {
		e := "error"
		if msg != "" {
			e = maskErr(msg)
		}
		r.Err = &e
	}
	b := ipc.TraceSpanBody{Span: r}
	var x ipc.SpanOTLP
	if tc.TraceState != "" {
		ts := tc.TraceState
		x.TraceState = &ts
	}
	if remote {
		t := true
		x.Remote = &t
	}
	if !ok && msg != "" {
		m := msg
		x.Message = &m
	}
	if x.TraceState != nil || x.Remote != nil || x.Message != nil {
		b.OTLP = &x
	}
	return b
}

// routeOf is lib/otel.js routeOf: a low-cardinality route, ids and model names as placeholders.
func routeOf(path string) string {
	path = geminiRouteRe.ReplaceAllString(path, "/v1beta/models/{model}:$1")
	segs := strings.Split(path, "/")
	for i, s := range segs {
		if routeIDRe.MatchString(s) || routeHexRe.MatchString(s) || routeDigitsRe.MatchString(s) {
			segs[i] = "{id}"
		}
	}
	return strings.Join(segs, "/")
}

var (
	geminiRouteRe = regexp.MustCompile(`^/v1beta/models/.+:([A-Za-z]+)$`)
	routeIDRe     = regexp.MustCompile(`^[a-z]+_[A-Za-z0-9]{6,}$`)
	routeHexRe    = regexp.MustCompile(`^[0-9a-fA-F-]{8,}$`)
	routeDigitsRe = regexp.MustCompile(`[0-9]{4,}`)
)

// genericSecrets is lib/otel.js GENERIC_SECRETS, run with JavaScript's regular expression semantics (\s, \b and the
// i flag differ in RE2): credentials that may show up in upstream error text.
var genericSecrets = []struct {
	re    *jsre.Regexp
	group bool // keep group 1 before the mask
	to    string
}{
	{jsre.MustCompile(`\b(Bearer|Basic|Api-Token)\s+[A-Za-z0-9._~+/=-]{8,}`, "gi"), true, " ***"},
	{jsre.MustCompile(`\b(?:sk|pk|rk)-[A-Za-z0-9_-]{12,}`, "g"), false, "***"},
	{jsre.MustCompile(`\bdt0[a-z]\d{2}\.[A-Za-z0-9._-]{16,}`, "g"), false, "***"},
	{jsre.MustCompile(`((?:api[-_]?key|token|secret|password)["']?\s*[:=]\s*["']?)[^\s"',;&]{6,}`, "gi"), true, "***"},
}

// scrub is lib/otel.js scrub.
func scrub(text string) string {
	u := jsre.U16(text)
	for _, g := range genericSecrets {
		to := jsre.U16(g.to)
		u = g.re.ReplaceAll(u, func(c []int) []uint16 {
			if !g.group || c[2] < 0 {
				return to
			}
			return append(append([]uint16(nil), u[c[2]:c[3]]...), to...)
		})
	}
	return jsre.Str(u)
}

// maskErr is lib/trace/recorder.js maskErr: each live secret replaced by ‹label hidden› (lib/trace/model.js
// maskSecrets), cut to 200 UTF-16 units, never ending on half a surrogate pair. A text the scanner could not finish (its
// step budget, impossible for 500 units) is hidden whole: no unscanned text is stored.
func maskErr(text string) string {
	spans, err := guard.SecretSpans(text, nil)
	if err != nil {
		return "‹secret hidden›"
	}
	u := jsre.U16(text)
	var out []uint16
	last := 0
	for _, sp := range spans {
		out = append(out, u[last:sp.Start]...)
		out = append(out, jsre.U16("‹"+sp.Label+" hidden›")...)
		last = sp.End
	}
	out = append(out, u[last:]...)
	if len(out) > 200 {
		out = out[:200]
	}
	if n := len(out); n > 0 && out[n-1] >= 0xd800 && out[n-1] <= 0xdbff {
		out = out[:n-1]
	}
	return jsre.Str(out)
}

// setStr sets a string attribute as otel.js cleanValue keeps it.
func setStr(a *Value, k, v string) { a.Set(k, Str(cleanString(v))) }

// cleanString is otel.js cleanValue for a string: over 8192 UTF-16 units, cut and marked with "…".
func cleanString(v string) string {
	if utf16Len(v) > maxAttrValue {
		return jsSlice(v, maxAttrValue) + "…"
	}
	return v
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// jsRound is Math.round: the nearest integer, ties toward +Infinity (exact where math.Floor(x+0.5) is not).
func jsRound(x float64) float64 {
	f := math.Floor(x)
	if x-f >= 0.5 {
		f++
	}
	return f
}

// utf16Len is a string's length in UTF-16 code units (JavaScript's .length).
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if w := utf16.RuneLen(r); w > 0 {
			n += w
		} else {
			n++
		}
	}
	return n
}
