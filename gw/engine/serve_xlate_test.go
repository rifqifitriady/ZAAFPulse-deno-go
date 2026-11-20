package engine

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/front"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/guard"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"
)

// xlateRig serves the P4d classes through Engine.Serve with every sub-class on go (unless modes says otherwise), its
// providers at one test server, trace spans on, and a recording call journal (P4f): what a served call leaves.
type xlateRig struct {
	url, key string
	e        *Engine
	mu       sync.Mutex
	lines    []recordedLine
	opened   []int64
	ended    []int64
	spans    []ipc.TraceSpanBody
	js       int         // requests handed to the control plane
	onLine   func()      // called as each usage line is emitted, before it is kept (tests of the order)
	written  atomic.Bool // the gateway wrote the head or a byte of the current answer
}

// markWriter marks the rig's written flag on the first head or byte the engine writes to the client.
type markWriter struct {
	http.ResponseWriter
	written *atomic.Bool
}

func (m *markWriter) WriteHeader(code int) { m.written.Store(true); m.ResponseWriter.WriteHeader(code) }
func (m *markWriter) Write(b []byte) (int, error) {
	m.written.Store(true)
	return m.ResponseWriter.Write(b)
}
func (m *markWriter) Unwrap() http.ResponseWriter { return m.ResponseWriter }

type recordedLine struct {
	call int64
	rec  ipc.UsageRecord
}

func (g *xlateRig) Open(ts int64, base ipc.UsageRecord) int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	id := int64(len(g.opened) + 1)
	g.opened = append(g.opened, id)
	return id
}

func (g *xlateRig) End(id int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ended = append(g.ended, id)
}

func newXlateRig(t *testing.T, provider http.Handler, modes func(sub string) (bool, bool)) *xlateRig {
	t.Helper()
	up := httptest.NewServer(provider)
	t.Cleanup(up.Close)
	c := xlateBase(t)
	s := xlateStateOf(t, c)
	for i := range s.Gateway.Providers {
		s.Gateway.Providers[i].BaseURL = up.URL + "/" + s.Gateway.Providers[i].ID
	}
	s.spans = true
	s = s.WithSnapshot(&ipc.BudgetSnapshot{At: NowMs() + 3600_000, KeyCenter: map[string]string{}})
	g := &xlateRig{key: c.Request.Headers["x-api-key"]}
	if g.key == "" {
		g.key = strings.TrimPrefix(c.Request.Headers["authorization"], "Bearer ")
	}
	g.e = New(NewUpstream(func(string) string { return "" }), func(r *ipc.UsageReport) (int64, error) {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.lines = append(g.lines, recordedLine{rec: r.Record})
		return 0, nil
	}, t.Logf)
	g.e.SetState(s)
	if modes == nil {
		modes = func(string) (bool, bool) { return true, false }
	}
	g.e.SetModes(modes)
	g.e.SetCalls(g, func(r *ipc.UsageReport, call int64) (int64, error) {
		if g.onLine != nil {
			g.onLine()
		}
		g.mu.Lock()
		defer g.mu.Unlock()
		g.lines = append(g.lines, recordedLine{call: call, rec: r.Record})
		return 0, nil
	})
	g.e.SetSpanEmitter(func(b *ipc.TraceSpanBody) error {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.spans = append(g.spans, *b)
		return nil
	})
	js := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.js++
		g.mu.Unlock()
		w.WriteHeader(299)
	})
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := front.RawPath(r.RequestURI)
		g.written.Store(false)
		g.e.Serve(front.Classify(raw), &markWriter{ResponseWriter: w, written: &g.written}, r, raw, js)
	}))
	t.Cleanup(gw.Close)
	g.url = gw.URL
	return g
}

// post sends body to path with the fixture key; ctx may cancel it.
func (g *xlateRig) post(ctx context.Context, t *testing.T, path, body string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, "POST", g.url+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", g.key)
	req.Header.Set("Traceparent", tp)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err.Error()
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, string(b)
}

// settle waits until n usage lines are in (or 5 s), then 300 ms more for any extra one.
func (g *xlateRig) settle(t *testing.T, n int) []recordedLine {
	t.Helper()
	end := time.Now().Add(5 * time.Second)
	for {
		g.mu.Lock()
		c := len(g.lines)
		g.mu.Unlock()
		if c >= n || time.Now().After(end) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]recordedLine(nil), g.lines...)
}

func chatWhole(w http.ResponseWriter, r *http.Request) {
	_, _ = io.WriteString(w, `{"id":"chatcmpl-9","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
}

func sseChunk(content string) string {
	b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": content}}}})
	return "data: " + string(b) + "\n\n"
}

// The engine serves each class by its sub-class's mode: messages on go, messages-stream on js, so a stream goes to the
// control plane; count_tokens' estimate and embeddings are served; a route is handed off.
func TestServeDispatchByClass(t *testing.T) {
	g := newXlateRig(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/embeddings") {
			_, _ = io.WriteString(w, `{"data":[],"usage":{"prompt_tokens":4}}`)
			return
		}
		chatWhole(w, r)
	}), func(sub string) (bool, bool) { return sub != SubMessagesStream, false })
	resp, body := g.post(context.Background(), t, "/v1/messages", `{"model":"oa/m1","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != 200 || !strings.Contains(body, `"type":"message"`) || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	resp, _ = g.post(context.Background(), t, "/v1/messages", `{"model":"oa/m1","stream":true,"messages":[]}`)
	if resp.StatusCode != 299 {
		t.Fatalf("a stream on js was served: %d", resp.StatusCode)
	}
	resp, body = g.post(context.Background(), t, "/v1/messages/count_tokens", `{"messages":[{"role":"user","content":"hello"}]}`)
	if resp.StatusCode != 200 || !strings.HasPrefix(body, `{"input_tokens":`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	resp, body = g.post(context.Background(), t, "/v1/embeddings", `{"model":"oa/e1","input":"x"}`)
	if resp.StatusCode != 200 || body != `{"data":[],"usage":{"prompt_tokens":4}}` || resp.Header.Get("Cache-Control") != "" {
		t.Fatalf("%d %s %v", resp.StatusCode, body, resp.Header)
	}
	resp, _ = g.post(context.Background(), t, "/v1/messages", `{"model":"router/x","messages":[]}`)
	if resp.StatusCode != 299 {
		t.Fatalf("a route was served: %d", resp.StatusCode)
	}
	lines := g.settle(t, 2)
	if len(lines) != 2 || *lines[0].rec.Format != "anthropic" || *lines[1].rec.Kind != "embeddings" || g.js != 2 {
		t.Fatalf("%+v js %d", lines, g.js)
	}
}

// P4f's hooks on a P4d call: one journal entry closed by its usage line, the line naming the trace, the server span
// then the client span. A count estimate: the server span only, no journal, no line. A native Anthropic call that
// fails: its journal entry ends with no line, the server span only.
func TestTranslatedCallJournalAndSpans(t *testing.T) {
	g := newXlateRig(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/an/") {
			hj, _ := w.(http.Hijacker)
			c, _, _ := hj.Hijack()
			_ = c.Close() // the provider call fails
			return
		}
		chatWhole(w, r)
	}), nil)
	g.post(context.Background(), t, "/v1/messages", `{"model":"oa/m1","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`)
	lines := g.settle(t, 1)
	if len(lines) != 1 || lines[0].call != 1 || lines[0].rec.TraceID == nil || *lines[0].rec.TraceID != "0af7651916cd43dd8448eb211c80319c" {
		t.Fatalf("%+v", lines)
	}
	g.mu.Lock()
	if len(g.spans) != 2 || g.spans[0].Span.K != "server" || g.spans[1].Span.K != "client" || len(g.opened) != 1 || len(g.ended) != 0 {
		t.Fatalf("spans %d journal %v %v", len(g.spans), g.opened, g.ended)
	}
	g.spans = nil
	g.mu.Unlock()
	g.post(context.Background(), t, "/v1/messages/count_tokens", `{"messages":[]}`)
	g.post(context.Background(), t, "/v1/messages", `{"model":"an/claude-x","max_tokens":5,"messages":[]}`)
	lines = g.settle(t, 1)
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(lines) != 1 || len(g.spans) != 2 || g.spans[0].Span.K != "server" || g.spans[1].Span.K != "server" ||
		len(g.opened) != 2 || len(g.ended) != 1 || g.ended[0] != 2 {
		t.Fatalf("lines %d spans %+v journal %v %v", len(lines), g.spans, g.opened, g.ended)
	}
}

// The Anthropic pings go out every PingEvery while the provider is quiet, after the opening event.
func TestTranslatedStreamPings(t *testing.T) {
	old := PingEvery
	PingEvery = 20 * time.Millisecond
	defer func() { PingEvery = old }()
	g := newXlateRig(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		time.Sleep(150 * time.Millisecond)
		_, _ = io.WriteString(w, sseChunk("hi")+"data: [DONE]\n\n")
	}), nil)
	resp, body := g.post(context.Background(), t, "/v1/messages", `{"model":"oa/m1","stream":true,"max_tokens":5,"messages":[{"role":"user","content":"x"}]}`)
	if resp.StatusCode != 200 || !strings.HasPrefix(body, "event: message_start\n") || strings.Count(body, anthropicPing) < 3 ||
		!strings.HasSuffix(body, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n") {
		t.Fatalf("%d %q", resp.StatusCode, body)
	}
	if lines := g.settle(t, 1); len(lines) != 1 || lines[0].rec.Status != 200 {
		t.Fatalf("%+v", lines)
	}
}

// One usage line however a translated stream ends: the provider's end, the provider breaking off, the client leaving,
// and a client write that fails.
func TestTranslatedStreamOneLine(t *testing.T) {
	const body = `{"model":"oa/m1","stream":true,"max_tokens":5,"messages":[{"role":"user","content":"x"}]}`
	cases := []struct {
		name   string
		end    string // "eof", "cut", "client", "write"
		wantEv string // an event the client must have seen, "" for none
	}{
		{"provider end", "eof", "event: message_stop"},
		{"provider cut", "cut", "event: error"},
		{"client leaves", "client", ""},
		{"write fails", "write", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			leave := make(chan struct{})
			g := newXlateRig(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(200)
				_, _ = io.WriteString(w, sseChunk("a"))
				w.(http.Flusher).Flush()
				switch c.end {
				case "eof":
					_, _ = io.WriteString(w, sseChunk("b")+"data: [DONE]\n\n")
				case "cut":
					hj, _ := w.(http.Hijacker)
					conn, buf, _ := hj.Hijack()
					_ = buf.Flush()
					_ = conn.Close()
				default:
					close(leave)
					select {
					case <-r.Context().Done():
					case <-time.After(5 * time.Second):
					}
				}
			}), nil)
			if c.end == "write" {
				// The client stays; the gateway's writes fail after the head (P4c's failingWriter).
				js := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					g.e.Serve("messages", &failingWriter{ResponseWriter: w}, r, "/v1/messages", js)
				}))
				defer srv.Close()
				g.url = srv.URL
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if c.end == "client" {
				go func() { <-leave; time.Sleep(50 * time.Millisecond); cancel() }()
			}
			_, got := g.post(ctx, t, "/v1/messages", body)
			if c.wantEv != "" && !strings.Contains(got, c.wantEv) {
				t.Fatalf("%q", got)
			}
			lines := g.settle(t, 1)
			if len(lines) != 1 || lines[0].rec.Status != 200 || lines[0].call == 0 {
				t.Fatalf("%+v", lines)
			}
		})
	}
}

// /v1/embeddings does not cancel its provider call when the client leaves (J8): the provider's answer comes later and
// the call's line is written.
func TestEmbeddingsLineWhenClientLeaves(t *testing.T) {
	gone := make(chan struct{})
	g := newXlateRig(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-gone:
		case <-time.After(5 * time.Second):
		}
		time.Sleep(50 * time.Millisecond)
		if r.Context().Err() != nil {
			return // the call was cancelled: no answer, and the test fails on the line
		}
		_, _ = io.WriteString(w, `{"data":[],"usage":{"prompt_tokens":7}}`)
	}), nil)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel(); close(gone) }()
	g.post(ctx, t, "/v1/embeddings", `{"model":"oa/e1","input":"x"}`)
	lines := g.settle(t, 1)
	if len(lines) != 1 || lines[0].rec.Status != 200 || *lines[0].rec.Input != 7 {
		t.Fatalf("%+v", lines)
	}
}

// The front's sub-class table is the engine's.
func TestFrontSubsMatchTheEngine(t *testing.T) {
	a, _ := json.Marshal(front.Subs)
	b, _ := json.Marshal(Subs)
	if string(a) != string(b) {
		t.Fatalf("front %s, engine %s", a, b)
	}
}

// Review I1: count_tokens' 15 s bounds the whole call, as JS's AbortSignal.timeout does: a provider that sends its head
// and then stalls the body gets the estimate at the limit.
func TestCountBodyStallFallsBackToTheEstimate(t *testing.T) {
	old := CountTimeout
	CountTimeout = 300 * time.Millisecond
	defer func() { CountTimeout = old }()
	release := make(chan struct{})
	defer close(release)
	g := newXlateRig(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, `{"input_tok`)
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	t0 := time.Now()
	resp, body := g.post(ctx, t, "/v1/messages/count_tokens", `{"model":"an/claude-x","messages":[{"role":"user","content":"hello"}]}`)
	if resp == nil || resp.StatusCode != 200 || !strings.HasPrefix(body, `{"input_tokens":`) || time.Since(t0) > 3*time.Second {
		t.Fatalf("%v %q after %v", resp, body, time.Since(t0))
	}
}

// Review I2 (deviation 19): a native count_tokens call writes no usage line, so it opens no journal entry.
func TestNativeCountHasNoJournalEntry(t *testing.T) {
	g := newXlateRig(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/messages/count_tokens") {
			t.Errorf("unexpected provider call %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"input_tokens":42}`)
	}), nil)
	resp, body := g.post(context.Background(), t, "/v1/messages/count_tokens", `{"model":"an/claude-x","messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != 200 || body != `{"input_tokens":42}` {
		t.Fatalf("%d %q", resp.StatusCode, body)
	}
	lines := g.settle(t, 0)
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(lines) != 0 || len(g.opened) != 0 || len(g.ended) != 0 {
		t.Fatalf("lines %d journal %v %v", len(lines), g.opened, g.ended)
	}
}

// Review I2 (deviation 18): a whole answer's usage line goes out before the engine writes the client a byte, as chat's
// does.
func TestWholeAnswerLineBeforeTheAnswer(t *testing.T) {
	g := newXlateRig(t, http.HandlerFunc(chatWhole), nil)
	var before atomic.Bool
	g.onLine = func() { before.Store(!g.written.Load()) }
	resp, _ := g.post(context.Background(), t, "/v1/messages", `{"model":"oa/m1","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != 200 || len(g.settle(t, 1)) != 1 || !before.Load() {
		t.Fatalf("%d: the usage line went out after the answer was written", resp.StatusCode)
	}
}

// Review I3: a panic inside a translated answer still ends the call as JS's catch does: on a stream an error event and
// the 200 line; on a whole answer the 200 line and the route's 502. The call is metered either way.
func TestTranslatedPanicStillWritesTheLine(t *testing.T) {
	defer func() { translateHook = nil }()
	stream := newXlateRig(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sseChunk("a")+sseChunk("b")+"data: [DONE]\n\n")
	}), nil)
	translateHook = func(stage string) {
		if stage == "push" {
			panic("a port bug")
		}
	}
	resp, body := stream.post(context.Background(), t, "/v1/messages", `{"model":"oa/m1","stream":true,"max_tokens":5,"messages":[{"role":"user","content":"x"}]}`)
	if resp == nil || resp.StatusCode != 200 || !strings.Contains(body, "event: error\n") {
		t.Fatalf("%v %q", resp, body)
	}
	if lines := stream.settle(t, 1); len(lines) != 1 || lines[0].rec.Status != 200 || lines[0].call == 0 {
		t.Fatalf("%+v", lines)
	}
	whole := newXlateRig(t, http.HandlerFunc(chatWhole), nil)
	translateHook = func(stage string) {
		if stage == "whole" {
			panic("a port bug")
		}
	}
	resp, body = whole.post(context.Background(), t, "/v1/messages", `{"model":"oa/m1","max_tokens":5,"messages":[{"role":"user","content":"x"}]}`)
	if resp == nil || resp.StatusCode != 502 || !strings.Contains(body, `"type":"api_error"`) {
		t.Fatalf("%v %q", resp, body)
	}
	if lines := whole.settle(t, 1); len(lines) != 1 || lines[0].rec.Status != 200 || lines[0].call == 0 {
		t.Fatalf("%+v", lines)
	}
}

// Review m3: when the Anthropic leg cannot translate the body the guardrails redacted, nothing is sent (never an empty
// POST) and the client gets what callChat's throw gives in JS: a translation 400 in its shape with no line, or for any
// other error the 599 line and the 502.
func TestAnthropicLegUntranslatableAfterRedaction(t *testing.T) {
	for _, c := range []struct {
		name, add string
		status    int
		line      bool
	}{
		{"a translation 400", `{"type":"json_object"}`, 400, false},
		{"a TypeError", "", 502, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := newXlateRig(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Errorf("the provider was called: %s", r.URL.Path)
			}), nil)
			st := *g.e.State()
			st.Gateway.Guardrails = ipc.GuardrailsPolicy{Scope: "all", Secrets: "redact", PII: "off", Injection: "off"}
			g.e.SetState(&st)
			was := checkBody
			defer func() { checkBody = was }()
			checkBody = func(b jsv.Value, p guard.Policy, f string) (guard.Result, error) {
				o := jsv.Obj(b).Clone()
				if c.add != "" {
					o.Set("response_format", jsv.MustParse(c.add))
				} else {
					o.Set("messages", jsv.MustParse(`[{"role":"assistant","tool_calls":[{"id":"a"}]}]`))
				}
				return guard.Result{Body: o, Action: "redacted", Hits: []guard.Hit{{Check: "secrets", Label: "x", Count: 1}}}, nil
			}
			resp, body := g.post(context.Background(), t, "/v1/chat/completions", `{"model":"an/claude-x","messages":[{"role":"user","content":"hi"}]}`)
			if resp == nil || resp.StatusCode != c.status {
				t.Fatalf("%v %q", resp, body)
			}
			lines := g.settle(t, 0)
			if c.line != (len(lines) == 1) || (c.line && lines[0].rec.Status != 599) {
				t.Fatalf("%+v", lines)
			}
		})
	}
}
