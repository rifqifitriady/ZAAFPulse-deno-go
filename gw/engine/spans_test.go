package engine

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

// counterIDs is scripts/fixtures/lib.js crypto.randomBytes: n bytes all equal to a counter that starts at 1 per case
// (lib/otel.js newId draws again for an all-zero id).
func counterIDs() SpanIDs {
	c := 1
	return func(n int) string {
		for {
			b := bytes.Repeat([]byte{byte(c & 255)}, n)
			c++
			if b[0] != 0 {
				return hex.EncodeToString(b)
			}
		}
	}
}

type spanCase struct {
	Request struct {
		Method      string            `json:"method"`
		Path        string            `json:"path"`
		HTTPVersion string            `json:"httpVersion"`
		Headers     map[string]string `json:"headers"`
		TLS         bool              `json:"tls"`
	} `json:"request"`
	Answer struct {
		Status int               `json:"status"`
		Chunks []json.RawMessage `json:"chunks"`
	} `json:"answer"`
	Usage        map[string]any    `json:"usage"`
	UsageWhen    string            `json:"usageWhen"`
	Ends         string            `json:"ends"`
	ProviderSpan *ipc.ProviderSpan `json:"providerSpan"`
}

type spanOut struct {
	TraceID *string           `json:"traceId"`
	Spans   []json.RawMessage `json:"spans"`
}

// usageOf is the fixture's usage entry (float USD, as JS records it) as the engine's record (nano-USD, as IPC carries it).
func usageOf(t *testing.T, m map[string]any) *ipc.UsageRecord {
	t.Helper()
	c := map[string]any{}
	for k, v := range m {
		c[k] = v
	}
	if f, ok := c["cost"].(float64); ok {
		c["cost"] = int64(jsRound(f * 1e9))
	}
	b, _ := json.Marshal(c)
	var r ipc.UsageRecord
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return &r
}

// replaySpans is what the engine emits for the fixture's request, in lib/otel.js order: the server span when the
// response ends, and the client span of the usage line either before it (it ends at once: not a chat call on a model
// endpoint, e.g. embeddings) or after it (a chat call's span waits for the response's id and finish reasons).
func replaySpans(t *testing.T, in *spanCase) (*string, []ipc.TraceSpanBody) {
	t.Helper()
	method, path, proto := in.Request.Method, in.Request.Path, in.Request.HTTPVersion
	if method == "" {
		method = "POST"
	}
	if path == "" {
		path = "/v1/chat/completions"
	}
	if proto == "" {
		proto = "1.1"
	}
	h := http.Header{}
	for k, v := range in.Request.Headers {
		h.Set(k, v)
	}
	ids := counterIDs()
	tc := StartTrace(h, ids)
	if !tc.Sampled {
		return nil, nil
	}
	format := LLMFormat(method, path)
	var obs *Observer
	if format != "" && format != "embeddings" {
		obs = NewObserver()
	}
	for _, c := range in.Answer.Chunks {
		var s string
		if json.Unmarshal(c, &s) != nil {
			var b struct {
				B64 string `json:"b64"`
			}
			if err := json.Unmarshal(c, &b); err != nil {
				t.Fatal(err)
			}
			raw, _ := base64.StdEncoding.DecodeString(b.B64)
			s = string(raw)
		}
		if obs != nil {
			obs.Feed([]byte(s))
		}
	}
	traceID := tc.TraceID
	var client *ipc.TraceSpanBody
	chatPending := false
	if in.Usage != nil {
		rec := usageOf(t, in.Usage)
		chatPending = format != "" && rec.Kind != nil && *rec.Kind == "chat"
		var o *Observed
		if chatPending {
			o = obs.Result()
		}
		cs := ClientSpan(tc, ids(8), rec, in.ProviderSpan, o, nil)
		client = &cs
	}
	ua := ""
	if v, ok := in.Request.Headers["user-agent"]; ok {
		ua = v
	}
	server := ServerSpan(tc, ServerFacts{Method: method, Path: path, Proto: proto, TLS: in.Request.TLS, UserAgent: ua,
		Status: in.Answer.Status})
	var out []ipc.TraceSpanBody
	if client != nil && !chatPending {
		out = append(out, *client)
	}
	out = append(out, server)
	if client != nil && chatPending {
		out = append(out, *client)
	}
	if in.Usage == nil {
		return &traceID, out
	}
	return &traceID, out
}

// jsCanon re-encodes JSON as JSON.stringify writes it, keys in their order (engine.Value keeps it).
func jsCanon(t *testing.T, b []byte) string {
	t.Helper()
	v, err := Parse(b)
	if err != nil {
		t.Fatalf("parse %s: %v", b, err)
	}
	return string(v.Stringify())
}

func TestSpansMatchTheJSFixtures(t *testing.T) {
	for _, c := range loadFixtures(t, "spans") {
		t.Run(c.Name, func(t *testing.T) {
			var in spanCase
			if err := json.Unmarshal(c.In, &in); err != nil {
				t.Fatal(err)
			}
			var want spanOut
			if err := json.Unmarshal(c.Out, &want); err != nil {
				t.Fatal(err)
			}
			traceID, got := replaySpans(t, &in)
			// The usage line names the trace only when a span is recorded (otel.js usageTraceId).
			wantTrace, gotTrace := "", ""
			if want.TraceID != nil {
				wantTrace = *want.TraceID
			}
			if traceID != nil && in.Usage != nil {
				gotTrace = *traceID
			}
			if gotTrace != wantTrace {
				t.Fatalf("usage traceId: got %q, want %q", gotTrace, wantTrace)
			}
			if len(got) != len(want.Spans) {
				t.Fatalf("got %d spans, want %d", len(got), len(want.Spans))
			}
			for i := range got {
				b, err := json.Marshal(got[i])
				if err != nil {
					t.Fatal(err)
				}
				if g, w := jsCanon(t, b), jsCanon(t, want.Spans[i]); g != w {
					t.Fatalf("span %d differs\n got: %s\nwant: %s", i, g, w)
				}
			}
		})
	}
}

// Known deviation (plan, "Known deviations"): JavaScript's slice keeps the high half of a surrogate pair it cuts, and
// the store then holds a lone surrogate; the engine cuts the pair whole. Pinned here, out of the corpus.
func TestCutPairIsDroppedWhole(t *testing.T) {
	ua := "claude-cli/" + strings.Repeat("x", 244) + "😀"
	s := ServerSpan(TraceCtx{TraceID: strings.Repeat("1", 32), SpanID: strings.Repeat("2", 16), Sampled: true},
		ServerFacts{Method: "POST", Path: "/v1/chat/completions", Proto: "1.1", UserAgent: ua, Status: 200})
	if !bytes.Contains(s.Span.A, []byte(`"user_agent.original":"claude-cli/`+strings.Repeat("x", 244)+`"`)) {
		t.Fatalf("attributes: %s", s.Span.A)
	}
}

func TestJSRoundTiesUpAndIsExact(t *testing.T) {
	for _, c := range []struct{ in, want float64 }{
		{0.5, 1}, {1.5, 2}, {-0.5, 0}, {-1.5, -1}, {0.49999999999999994, 0}, {2.4999999, 2}, {1767225599987654.5, 1767225599987655},
	} {
		if got := jsRound(c.in); got != c.want {
			t.Errorf("jsRound(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestRouteOf(t *testing.T) {
	for in, want := range map[string]string{
		"/v1/chat/completions":                          "/v1/chat/completions",
		"/v1beta/models/gemini-2.5-pro:generateContent": "/v1beta/models/{model}:generateContent",
		"/v1/runs/run_abc123/resume":                    "/v1/runs/{id}/resume",
		"/v1/x/0af7651916cd":                            "/v1/x/{id}",
		"/v1/files/file-2026":                           "/v1/files/{id}",
	} {
		if got := routeOf(in); got != want {
			t.Errorf("routeOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestScrubIsJavaScriptSemantics(t *testing.T) {
	// \s in JavaScript includes U+00A0; RE2's does not.
	if got := scrub("Bearer abcdefgh12345678"); got != "Bearer ***" {
		t.Fatalf("scrub: %q", got)
	}
	if got := scrub("password = hunter2hunter2, ok"); got != "password = ***, ok" {
		t.Fatalf("scrub: %q", got)
	}
}
