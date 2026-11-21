package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/guard"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"
)

// noJournal fails the test on any call journal entry: /mcp opens none (Decision 5).
type noJournal struct{ t *testing.T }

func (j noJournal) Open(int64, ipc.UsageRecord) int64 {
	j.t.Error("a call journal entry was opened for /mcp")
	return 1
}
func (j noJournal) End(int64) { j.t.Error("a call journal entry was ended for /mcp") }

// mcpRig is the go engine serving /mcp from a fixture case's state, with a fake tool endpoint.
type mcpRig struct {
	url     string
	headers map[string]string
	lines   chan ipc.UsageRecord
	e       *Engine
	mu      sync.Mutex
	bodies  []string // what the tool endpoint was sent
	handed  int      // requests handed to js
}

func (g *mcpRig) sent() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.bodies...)
}

func newMCPRig(t *testing.T, c mcpCase, tool func(ctx context.Context, body []byte) (int, []byte, error)) *mcpRig {
	t.Helper()
	g := &mcpRig{headers: c.Request.Headers, lines: make(chan ipc.UsageRecord, 64)}
	e := New(NewUpstream(func(string) string { return "" }), func(r *ipc.UsageReport) (int64, error) {
		g.lines <- r.Record
		return 0, nil
	}, t.Logf)
	e.SetState(mcpStateOf(t, c))
	e.SetModes(func(string) (bool, bool) { return true, false })
	e.SetCalls(noJournal{t}, func(*ipc.UsageReport, int64) (int64, error) {
		t.Error("a line was emitted with a call id")
		return 0, nil
	})
	if tool != nil {
		e.SetTools(func(ctx context.Context, body []byte) (int, []byte, error) {
			g.mu.Lock()
			g.bodies = append(g.bodies, string(body))
			g.mu.Unlock()
			return tool(ctx, body)
		})
	}
	js := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.handed++
		g.mu.Unlock()
		w.WriteHeader(299)
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.Serve("mcp", w, r, "/mcp", js)
	}))
	t.Cleanup(srv.Close)
	g.url, g.e = srv.URL+"/mcp", e
	return g
}

func (g *mcpRig) postCtx(ctx context.Context, t *testing.T, body string, h map[string]string) (*http.Response, string, error) {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, "POST", g.url, strings.NewReader(body))
	for k, v := range g.headers {
		req.Header.Set(k, v)
	}
	for k, v := range h {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, string(b), nil
}

func (g *mcpRig) post(t *testing.T, body string, h map[string]string) (*http.Response, string) {
	t.Helper()
	resp, b, err := g.postCtx(context.Background(), t, body, h)
	if err != nil {
		t.Fatal(err)
	}
	return resp, b
}

// line is the next usage line, or nil when none comes within wait.
func (g *mcpRig) line(wait time.Duration) *ipc.UsageRecord {
	select {
	case l := <-g.lines:
		return &l
	case <-time.After(wait):
		return nil
	}
}

// endpointTool answers every call with a fixed endpoint body.
func endpointTool(body []byte) func(context.Context, []byte) (int, []byte, error) {
	return func(context.Context, []byte) (int, []byte, error) { return 200, body, nil }
}

// Every fixture case served through Engine.Serve: the client gets the JS bytes, the tool is sent the checked arguments
// (or never called on a refusal), the line goes out exactly when JS writes one, and no call journal entry is opened.
func TestServeMCPFixtures(t *testing.T) {
	for _, f := range loadMCP(t) {
		if f.In.RateLimited {
			continue // TestServeMCPRateLimited
		}
		t.Run(f.Name, func(t *testing.T) {
			wasBody, wasText := checkBody, checkText
			defer func() { checkBody, checkText = wasBody, wasText }()
			if f.In.FailBody {
				checkBody = func(jsv.Value, guard.Policy, string) (guard.Result, error) {
					return guard.Result{}, errors.New("a check failed")
				}
			}
			if f.In.FailText {
				checkText = func(jsv.Value, guard.Policy, string) (guard.TextResult, error) {
					return guard.TextResult{}, errors.New("a check failed")
				}
			}
			g := newMCPRig(t, f.In, endpointTool(endpointOf(f.In)))
			resp, body := g.post(t, f.In.Request.Body, nil)
			if resp.StatusCode != f.Out.Client.Status || body != f.Out.Client.Body {
				t.Errorf("client\n got %d %s\nwant %d %s", resp.StatusCode, body, f.Out.Client.Status, f.Out.Client.Body)
			}
			for k, v := range f.Out.Client.Headers {
				if got := resp.Header.Get(k); (v == nil) != (got == "") || (v != nil && *v != got) {
					t.Errorf("header %s: %q, want %v", k, got, ptrText(v))
				}
			}
			sent := g.sent()
			if len(sent) != len(f.Out.ToolCalls) {
				t.Fatalf("%d tool calls, JS made %d", len(sent), len(f.Out.ToolCalls))
			}
			if len(sent) == 1 {
				var b struct {
					Name      string          `json:"name"`
					Arguments json.RawMessage `json:"arguments"`
					KeyID     string          `json:"keyId"`
					Trace     json.RawMessage `json:"trace"`
				}
				if err := json.Unmarshal([]byte(sent[0]), &b); err != nil {
					t.Fatalf("tool body %s: %v", sent[0], err)
				}
				want := f.Out.ToolCalls[0]
				if b.Name != want.Name || string(b.Arguments) != want.Args || b.KeyID == "" || b.Trace != nil {
					t.Errorf("tool body %s, want %s %s", sent[0], want.Name, want.Args)
				}
			}
			line := g.line(200 * time.Millisecond)
			if (line == nil) != (string(f.Out.Usage) == "null") {
				t.Fatalf("line %v, want %s", line, f.Out.Usage)
			}
			if line != nil {
				sameJSON(t, "usage", usageOfRecord(line), f.Out.Usage)
				if line.Ts == 0 || line.TraceID != nil {
					t.Errorf("line ts %d trace %v", line.Ts, line.TraceID)
				}
			}
		})
	}
}

// The rate limit answers 429 in the OpenAI shape before any check or tool; no line.
func TestServeMCPRateLimited(t *testing.T) {
	for _, f := range loadMCP(t) {
		if !f.In.RateLimited {
			continue
		}
		g := newMCPRig(t, f.In, endpointTool(endpointOf(f.In)))
		g.post(t, f.In.Request.Body, nil) // the key's one request of this minute
		g.line(200 * time.Millisecond)
		n := len(g.sent())
		resp, body := g.post(t, f.In.Request.Body, nil)
		if resp.StatusCode != f.Out.Client.Status || body != f.Out.Client.Body {
			t.Errorf("client\n got %d %s\nwant %d %s", resp.StatusCode, body, f.Out.Client.Status, f.Out.Client.Body)
		}
		if got := resp.Header.Get("Retry-After"); f.Out.Client.Headers["retry-after"] == nil || got != *f.Out.Client.Headers["retry-after"] {
			t.Errorf("retry-after %q", got)
		}
		if len(g.sent()) != n || g.line(100*time.Millisecond) != nil {
			t.Error("the 429 called the tool or wrote a line")
		}
		return
	}
	t.Fatal("no rate-limited fixture")
}

// A tool call outlives a client that leaves: the endpoint's context is not cancelled with the request's, the call
// ends, and its line still goes out.
func TestServeMCPToolOutlivesTheClient(t *testing.T) {
	var base mcpCase
	for _, f := range loadMCP(t) {
		if f.Name == "call-redact-args.json" { // in scope with a hit: a line
			base = f.In
		}
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	ctxErr := make(chan error, 1)
	g := newMCPRig(t, base, func(ctx context.Context, _ []byte) (int, []byte, error) {
		close(entered)
		<-release
		ctxErr <- ctx.Err()
		return 200, endpointOf(base), nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = g.postCtx(ctx, t, base.Request.Body, nil)
	}()
	<-entered
	cancel()
	<-done
	time.Sleep(50 * time.Millisecond) // the server sees the client gone
	close(release)
	if err := <-ctxErr; err != nil {
		t.Fatalf("the tool's context ended with the client: %v", err)
	}
	if l := g.line(2 * time.Second); l == nil || l.Guard == nil || l.Guard.Action != "redacted" {
		t.Fatalf("line %+v", l)
	}
}

// No tool endpoint, a transport error, a status but 200, or an answer that is not the endpoint's: -32603 Internal
// error, and no line even where the arguments check hit (known difference 1).
func TestServeMCPToolEndpointFails(t *testing.T) {
	var base mcpCase
	for _, f := range loadMCP(t) {
		if f.Name == "call-redact-args.json" {
			base = f.In
		}
	}
	const want = `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"Internal error"}}`
	for name, tool := range map[string]func(context.Context, []byte) (int, []byte, error){
		"unset": nil,
		"error": func(context.Context, []byte) (int, []byte, error) { return 0, nil, errors.New("connection refused") },
		"400":   func(context.Context, []byte) (int, []byte, error) { return 400, nil, nil },
		"500":   func(context.Context, []byte) (int, []byte, error) { return 500, []byte(`{"r":{"result":{}}}`), nil },
		"bytes": endpointTool([]byte(`not json`)),
	} {
		t.Run(name, func(t *testing.T) {
			g := newMCPRig(t, base, tool)
			resp, body := g.post(t, base.Request.Body, nil)
			if resp.StatusCode != 200 || body != want {
				t.Fatalf("%d %s", resp.StatusCode, body)
			}
			if l := g.line(150 * time.Millisecond); l != nil {
				t.Fatalf("a line: %+v", l)
			}
		})
	}
}

// A request on /mcp the engine does not serve is handed to js unchanged.
func TestServeMCPHandsOff(t *testing.T) {
	base := mcpBase(t)
	g := newMCPRig(t, base, endpointTool([]byte(`{"r":{}}`)))
	resp, _ := g.post(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, nil)
	if resp.StatusCode != 299 || g.handed != 1 || len(g.sent()) != 0 {
		t.Fatalf("%d handed %d sent %d", resp.StatusCode, g.handed, len(g.sent()))
	}
}

// mcpSpanRig is an mcpRig with trace spans on, recording them.
func mcpSpanRig(t *testing.T, c mcpCase, tool func(context.Context, []byte) (int, []byte, error)) (*mcpRig, func(n int) []ipc.TraceSpanBody) {
	t.Helper()
	g := newMCPRig(t, c, tool)
	st := *g.e.State()
	st.spans = true
	g.e.SetState(&st)
	var mu sync.Mutex
	var got []ipc.TraceSpanBody
	g.e.SetSpanEmitter(func(b *ipc.TraceSpanBody) error {
		mu.Lock()
		got = append(got, *b)
		mu.Unlock()
		return nil
	})
	return g, func(n int) []ipc.TraceSpanBody {
		end := time.Now().Add(3 * time.Second)
		for {
			mu.Lock()
			k := len(got)
			mu.Unlock()
			if k >= n || time.Now().After(end) {
				time.Sleep(100 * time.Millisecond) // and no more
				mu.Lock()
				defer mu.Unlock()
				return append([]ipc.TraceSpanBody(nil), got...)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func mcpCaseNamed(t *testing.T, name string) mcpCase {
	t.Helper()
	for _, f := range loadMCP(t) {
		if f.Name == name {
			return f.In
		}
	}
	t.Fatalf("no %s", name)
	return mcpCase{}
}

// Spans as lib/otel.js records them for /mcp, which is no model endpoint (llmFormat null): a call with a line gets the
// server span and the line's client span ("chat <tool>", kind mcp); the tool runs under the server span (its trace is
// sent); a call with neither a line nor a span inside gets none.
func TestServeMCPSpans(t *testing.T) {
	t.Run("a line", func(t *testing.T) {
		c := mcpCaseNamed(t, "call-redact-args.json")
		g, spans := mcpSpanRig(t, c, endpointTool(endpointOf(c)))
		g.post(t, c.Request.Body, map[string]string{"Traceparent": tp})
		l := g.line(time.Second)
		if l == nil || l.TraceID == nil || *l.TraceID != "0af7651916cd43dd8448eb211c80319c" {
			t.Fatalf("line %+v", l)
		}
		got := spans(2)
		if len(got) != 2 || got[0].Span.K != "server" || got[1].Span.K != "client" {
			t.Fatalf("spans %+v", got)
		}
		if got[0].Span.N != "POST /mcp" || got[1].Span.N != "chat dt_query" || *got[1].Span.P != got[0].Span.S {
			t.Fatalf("names %q %q", got[0].Span.N, got[1].Span.N)
		}
		for _, want := range []string{`"zaafpulse.request.kind":"mcp"`, `"gen_ai.provider.name":"dt"`, `"zaafpulse.cost.usd":0`} {
			if !bytes.Contains(got[1].Span.A, []byte(want)) {
				t.Errorf("client attributes %s lack %s", got[1].Span.A, want)
			}
		}
		want := `"trace":{"traceId":"0af7651916cd43dd8448eb211c80319c","spanId":"` + got[0].Span.S + `","sampled":true}`
		if s := g.sent(); len(s) != 1 || !strings.Contains(s[0], want) {
			t.Errorf("tool body %v lacks %s", s, want)
		}
	})
	t.Run("no line, nothing inside", func(t *testing.T) {
		c := mcpCaseNamed(t, "call-clean.json")
		g, spans := mcpSpanRig(t, c, endpointTool(endpointOf(c)))
		g.post(t, c.Request.Body, nil)
		if got := spans(1); len(got) != 0 {
			t.Fatalf("spans %+v", got)
		}
		if s := g.sent(); len(s) != 1 || !strings.Contains(s[0], `"sampled":true`) {
			t.Errorf("tool body %v", s)
		}
	})
	t.Run("no line, a tool span inside", func(t *testing.T) {
		c := mcpCaseNamed(t, "call-clean.json")
		g, spans := mcpSpanRig(t, c, endpointTool([]byte(`{"r":{"result":{"content":[]}},"child":true}`)))
		g.post(t, c.Request.Body, nil)
		got := spans(1)
		if len(got) != 1 || got[0].Span.K != "server" || !bytes.Contains(got[0].Span.A, []byte(`"http.response.status_code":200`)) {
			t.Fatalf("spans %+v", got)
		}
	})
	t.Run("unsampled", func(t *testing.T) {
		c := mcpCaseNamed(t, "call-redact-args.json")
		g, spans := mcpSpanRig(t, c, endpointTool([]byte(`{"r":{"result":{"content":[]}},"child":true}`)))
		g.post(t, c.Request.Body, map[string]string{"Traceparent": strings.TrimSuffix(tp, "01") + "00"})
		if l := g.line(time.Second); l == nil || l.TraceID != nil {
			t.Fatalf("line %+v", l)
		}
		if got := spans(1); len(got) != 0 {
			t.Fatalf("spans %+v", got)
		}
		want := `"trace":{"traceId":"0af7651916cd43dd8448eb211c80319c",`
		if s := g.sent(); len(s) != 1 || !strings.Contains(s[0], want) || !strings.Contains(s[0], `"sampled":false`) {
			t.Errorf("tool body %v", s)
		}
	})
	t.Run("429", func(t *testing.T) {
		var c mcpCase
		for _, f := range loadMCP(t) {
			if f.In.RateLimited {
				c = f.In
			}
		}
		g, spans := mcpSpanRig(t, c, endpointTool(endpointOf(c)))
		g.post(t, c.Request.Body, nil)
		before := len(spans(0))
		if resp, _ := g.post(t, c.Request.Body, nil); resp.StatusCode != 429 {
			t.Fatalf("status %d", resp.StatusCode)
		}
		if got := spans(before + 1); len(got) != before {
			t.Fatalf("the 429 has spans: %+v", got[before:])
		}
	})
}

// providerNames is lib/otel.js PROVIDER_NAME, read from the source.
func TestProviderNamesMatchOtel(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "lib", "otel.js"))
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)const PROVIDER_NAME = \{(.*?)\n\};`).FindSubmatch(src)
	if block == nil {
		t.Fatal("no PROVIDER_NAME in lib/otel.js")
	}
	got := map[string]string{}
	for _, m := range regexp.MustCompile(`(?m)^\s*'?([a-z-]+)'?: '([a-z._]+)',$`).FindAllSubmatch(block[1], -1) {
		got[string(m[1])] = string(m[2])
	}
	if len(got) == 0 || len(got) != strings.Count(string(block[1]), "\n") {
		t.Fatalf("parsed %d of %q", len(got), block[1])
	}
	if a, b := fmt.Sprint(got), fmt.Sprint(providerNames); a != b {
		t.Fatalf("otel.js %s, engine %s", a, b)
	}
}

// An mcp line's client span names the provider as otel.js providerSpanAttrs: a gateway provider of the tool prefix's
// id gives its pushed attributes; else PROVIDER_NAME's name for the id; else the id.
func TestToolProvSpan(t *testing.T) {
	s := mcpStateOf(t, mcpBase(t))
	if toolProvSpan(s, "dt") != nil || toolProvSpan(s, "gemini").Name != "gcp.gemini" {
		t.Fatal("names")
	}
	addr, port := "h", int64(1)
	st := *s
	st.Gateway.Providers = append([]ipc.ProviderConfig(nil), ipc.ProviderConfig{ID: "dt", Span: &ipc.ProviderSpan{Name: "x", Address: &addr, Port: &port}})
	if p := toolProvSpan(&st, "dt"); p == nil || p.Name != "x" {
		t.Fatalf("%+v", p)
	}
}
