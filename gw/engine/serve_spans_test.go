package engine

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

// spanRig is a stream rig with trace spans on (config.push trace.spans), recording the spans the engine emits.
func spanRig(t *testing.T, provider http.Handler, on bool) (*streamRig, func(n int) []ipc.TraceSpanBody) {
	t.Helper()
	rig := newStreamRig(t, provider)
	st := *rig.e.State()
	st.spans = on
	rig.e.SetState(&st)
	var mu sync.Mutex
	var got []ipc.TraceSpanBody
	rig.e.SetSpanEmitter(func(b *ipc.TraceSpanBody) error {
		mu.Lock()
		got = append(got, *b)
		mu.Unlock()
		return nil
	})
	wait := func(n int) []ipc.TraceSpanBody {
		end := time.Now().Add(5 * time.Second)
		for {
			mu.Lock()
			c := append([]ipc.TraceSpanBody(nil), got...)
			mu.Unlock()
			if len(c) >= n || time.Now().After(end) {
				time.Sleep(100 * time.Millisecond) // and no more
				mu.Lock()
				defer mu.Unlock()
				return append([]ipc.TraceSpanBody(nil), got...)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	return rig, wait
}

const tp = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"

func (g *streamRig) postTraced(t *testing.T, body, traceparent string) *http.Response {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), "POST", g.url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", g.key)
	if traceparent != "" {
		req.Header.Set("Traceparent", traceparent)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp
}

func wholeProvider(w http.ResponseWriter, r *http.Request) {
	_, _ = io.WriteString(w, `{"id":"chatcmpl-9","choices":[{"message":{"content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
}

// A served call: the usage line names the trace; the server span (parent: the caller's span), then the client span
// under it with the answer's id and finish reason.
func TestServedCallEmitsServerThenClientSpan(t *testing.T) {
	rig, spans := spanRig(t, http.HandlerFunc(wholeProvider), true)
	if r := rig.postTraced(t, `{"model":"oa/m1","messages":[{"role":"user","content":"hi"}]}`, tp); r.StatusCode != 200 {
		t.Fatalf("status %d", r.StatusCode)
	}
	line := rig.oneLine(t)
	if line.TraceID == nil || *line.TraceID != "0af7651916cd43dd8448eb211c80319c" {
		t.Fatalf("usage traceId %v", line.TraceID)
	}
	got := spans(2)
	if len(got) != 2 || got[0].Span.K != "server" || got[1].Span.K != "client" {
		t.Fatalf("spans %+v", got)
	}
	if got[0].Span.P == nil || *got[0].Span.P != "b7ad6b7169203331" || got[1].Span.P == nil || *got[1].Span.P != got[0].Span.S {
		t.Fatalf("parents %+v", got)
	}
	for _, want := range []string{`"gen_ai.response.id":"chatcmpl-9"`, `"gen_ai.response.finish_reasons":["stop"]`,
		`"gen_ai.provider.name":"oa"`, `"server.port":`} {
		if !bytes.Contains(got[1].Span.A, []byte(want)) {
			t.Fatalf("client attributes %s lack %s", got[1].Span.A, want)
		}
	}
	if got[0].Span.St > got[1].Span.St+1000 || got[0].OTLP == nil || got[0].OTLP.Remote == nil {
		t.Fatalf("server %+v", got[0])
	}
}

func TestUnsampledCallEmitsNoSpanAndNoTraceID(t *testing.T) {
	rig, spans := spanRig(t, http.HandlerFunc(wholeProvider), true)
	rig.postTraced(t, `{"model":"oa/m1","messages":[{"role":"user","content":"hi"}]}`, strings.TrimSuffix(tp, "01")+"00")
	if l := rig.oneLine(t); l.TraceID != nil {
		t.Fatalf("traceId %v", *l.TraceID)
	}
	if got := spans(1); len(got) != 0 {
		t.Fatalf("spans %+v", got)
	}
}

func TestSpansOffEmitsNothing(t *testing.T) {
	rig, spans := spanRig(t, http.HandlerFunc(wholeProvider), false)
	rig.postTraced(t, `{"model":"oa/m1","messages":[{"role":"user","content":"hi"}]}`, tp)
	if l := rig.oneLine(t); l.TraceID != nil {
		t.Fatalf("traceId %v", *l.TraceID)
	}
	if got := spans(1); len(got) != 0 {
		t.Fatalf("spans %+v", got)
	}
}

// A call the rate limit refuses has the server span only (JS: authed throws 429 before any usage line).
func TestRateLimitedCallEmitsTheServerSpanOnly(t *testing.T) {
	rig, spans := spanRig(t, http.HandlerFunc(wholeProvider), true)
	st := *rig.e.State()
	keys := append([]ipc.GatewayKeyAuth(nil), st.Keys.list...)
	for i := range keys {
		keys[i].RPM = 1
	}
	st.Keys = NewKeys(keys)
	rig.e.SetState(&st)
	rig.postTraced(t, `{"model":"oa/m1","messages":[{"role":"user","content":"hi"}]}`, "")
	rig.oneLine(t)
	if r := rig.postTraced(t, `{"model":"oa/m1","messages":[{"role":"user","content":"hi"}]}`, ""); r.StatusCode != 429 {
		t.Fatalf("status %d", r.StatusCode)
	}
	got := spans(3)
	if len(got) != 3 || got[2].Span.K != "server" || !bytes.Contains(got[2].Span.A, []byte(`"http.response.status_code":429`)) {
		t.Fatalf("spans %+v", got)
	}
}

// A stream: the usage line, then the server span (200), then the client span with the stream's id and reason.
func TestStreamSpansFollowTheUsageLine(t *testing.T) {
	rig, spans := spanRig(t, sseProvider([]string{
		`data: {"id":"s9","model":"m1","choices":[{"delta":{"content":"a"}}]}` + "\n\n",
		`data: {"id":"s9","model":"m1","choices":[{"delta":{},"finish_reason":"length"}]}` + "\n\n",
		"data: [DONE]\n\n",
	}, nil), true)
	rig.postTraced(t, streamBody, tp)
	if l := rig.oneLine(t); l.TraceID == nil {
		t.Fatal("no traceId")
	}
	got := spans(2)
	if len(got) != 2 || got[0].Span.K != "server" || got[1].Span.K != "client" ||
		!bytes.Contains(got[1].Span.A, []byte(`"gen_ai.response.id":"s9"`)) ||
		!bytes.Contains(got[1].Span.A, []byte(`"gen_ai.response.finish_reasons":["length"]`)) ||
		!bytes.Contains(got[1].Span.A, []byte(`"gen_ai.request.stream":true`)) {
		t.Fatalf("spans %+v", got)
	}
}

// A call the guardrails block: its line, and both spans (JS records the blocked call's usage line and its span).
func TestBlockedCallEmitsBothSpans(t *testing.T) {
	rig, spans := spanRig(t, http.HandlerFunc(wholeProvider), true)
	st := *rig.e.State()
	st.Gateway.Guardrails.Secrets = "block"
	rig.e.SetState(&st)
	rig.postTraced(t, `{"model":"oa/m1","messages":[{"role":"user","content":"key `+antKey+`"}]}`, "")
	if l := rig.oneLine(t); l.Status != 400 || l.TraceID == nil {
		t.Fatalf("line %+v", l)
	}
	got := spans(2)
	if len(got) != 2 || got[1].Span.OK || !bytes.Contains(got[1].Span.A, []byte(`"error.type":"400"`)) {
		t.Fatalf("spans %+v", got)
	}
}

// Final review I4 (D1: spans after the last byte): the client has the whole answer before the span work runs. The
// span emitter waits until the client has read the body; if the answer were held in net/http's buffer until the handler
// returns, the client would wait for the emitter and the emitter for the client.
func TestClientHasTheAnswerBeforeTheSpans(t *testing.T) {
	for _, c := range []struct {
		name, body string
		rpm        int64
	}{
		{"served", `{"model":"oa/m1","messages":[{"role":"user","content":"hi"}]}`, 0},
		{"blocked", `{"model":"oa/m1","messages":[{"role":"user","content":"key ` + antKey + `"}]}`, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			rig, _ := spanRig(t, http.HandlerFunc(wholeProvider), true)
			if c.name == "blocked" {
				st := *rig.e.State()
				st.Gateway.Guardrails.Secrets = "block"
				rig.e.SetState(&st)
			}
			read := make(chan struct{})
			waited := make(chan bool, 4)
			rig.e.SetSpanEmitter(func(*ipc.TraceSpanBody) error {
				select {
				case <-read:
					waited <- true
				case <-time.After(3 * time.Second):
					waited <- false
				}
				return nil
			})
			go func() {
				resp := rig.postTraced(t, c.body, "")
				_ = resp
				close(read)
			}()
			select {
			case ok := <-waited:
				if !ok {
					t.Fatal("the span work ran before the client had the answer")
				}
			case <-time.After(6 * time.Second):
				t.Fatal("no span")
			}
			rig.oneLine(t)
		})
	}
}
