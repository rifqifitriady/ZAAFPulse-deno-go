package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

type streamRig struct {
	url   string
	key   string
	lines chan ipc.UsageRecord
	e     *Engine
}

// newStreamRig: the go engine serving chat (and its streams) against provider, on 127.0.0.1.
func newStreamRig(t *testing.T, provider http.Handler) *streamRig {
	t.Helper()
	up := httptest.NewServer(provider)
	t.Cleanup(up.Close)
	return newStreamRigAt(t, up.URL)
}

// newStreamRigAt: the same against a provider already listening at providerURL.
func newStreamRigAt(t *testing.T, providerURL string) *streamRig {
	t.Helper()
	var base chatCase
	for _, f := range loadFixtures(t, "chat") {
		if strings.HasSuffix(f.Name, "-basic.json") && f.Fn == "chat" {
			_ = json.Unmarshal(f.In, &base)
		}
	}
	s := stateFor(t, base)
	for i := range s.Gateway.Providers {
		if s.Gateway.Providers[i].ID == "oa" {
			s.Gateway.Providers[i].BaseURL = providerURL + "/v1"
		}
	}
	// A snapshot dated an hour ahead stays fresh for the whole test (PlanChat refuses one older than StaleMs).
	s = s.WithSnapshot(&ipc.BudgetSnapshot{At: NowMs() + 3600_000, KeyCenter: map[string]string{}})
	lines := make(chan ipc.UsageRecord, 1024)
	e := New(NewUpstream(func(string) string { return "" }), func(r *ipc.UsageReport) error {
		lines <- r.Record
		return nil
	}, t.Logf)
	e.SetState(s)
	e.SetModes(func(string) (bool, bool) { return true, false }) // both sub-classes on go
	js := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Errorf("handed off: %s", r.URL) })
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.Serve("chat", w, r, "/v1/chat/completions", js)
	}))
	t.Cleanup(gw.Close)
	return &streamRig{url: gw.URL + "/v1/chat/completions", key: base.Request.Headers["authorization"], lines: lines, e: e}
}

func (g *streamRig) post(ctx context.Context, t *testing.T, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, "POST", g.url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", g.key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// oneLine waits for exactly one usage line: a second within 300 ms fails.
func (g *streamRig) oneLine(t *testing.T) ipc.UsageRecord {
	t.Helper()
	var rec ipc.UsageRecord
	select {
	case rec = <-g.lines:
	case <-time.After(5 * time.Second):
		t.Fatal("no usage line")
	}
	select {
	case again := <-g.lines:
		t.Fatalf("a second usage line: %+v", again)
	case <-time.After(300 * time.Millisecond):
	}
	return rec
}

const streamBody = `{"model":"oa/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`

// sseProvider writes the events one by one, waiting on step before each one after the first.
func sseProvider(events []string, step <-chan struct{}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		for i, ev := range events {
			if i > 0 && step != nil {
				select {
				case <-step:
				case <-r.Context().Done():
					return
				}
			}
			_, _ = io.WriteString(w, ev)
			w.(http.Flusher).Flush()
		}
	}
}

// Each provider read reaches the client before the next one is sent (flush per read), with the J7 headers and no
// Connection header; the line has the usage of the last chunk and a ttft.
func TestStreamFlushesEachRead(t *testing.T) {
	step := make(chan struct{})
	g := newStreamRig(t, sseProvider([]string{
		`data: {"model":"m1","choices":[{"delta":{"content":"a"}}]}` + "\n\n",
		`data: {"model":"m1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2}}` + "\n\n",
		"data: [DONE]\n\n",
	}, step))
	resp := g.post(context.Background(), t, streamBody)
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" ||
		resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("Access-Control-Allow-Origin") != "*" ||
		resp.Header.Get("Connection") != "" {
		t.Fatalf("%d %v", resp.StatusCode, resp.Header)
	}
	rd := bufio.NewReader(resp.Body)
	first, _ := rd.ReadString('\n')
	if !strings.Contains(first, `"model":"oa/m1"`) {
		t.Fatalf("%q", first)
	}
	close(step) // the rest may come now
	rest, _ := io.ReadAll(rd)
	if !strings.HasSuffix(string(rest), "data: [DONE]\n\n") || strings.Contains(string(rest), "usage") {
		t.Fatalf("%q", rest)
	}
	rec := g.oneLine(t)
	if rec.Status != 200 || *rec.Input != 10 || *rec.Output != 2 || !rec.TTFT.Set || rec.TTFT.Null || !*rec.Stream {
		t.Fatalf("%+v", rec)
	}
}

// The client goes away mid-stream: the provider's request is cancelled and one line is written.
func TestStreamClientGoneOneLine(t *testing.T) {
	cancelled := make(chan struct{})
	g := newStreamRig(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"content":"a"}}]}`+"\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(cancelled)
	}))
	ctx, cancel := context.WithCancel(context.Background())
	resp := g.post(ctx, t, streamBody)
	_, _ = bufio.NewReader(resp.Body).ReadString('\n')
	cancel()
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the provider's request was not cancelled")
	}
	if rec := g.oneLine(t); rec.Status != 200 || *rec.Input != 0 {
		t.Fatalf("%+v", rec)
	}
}

// The provider cuts off mid-line: the client gets the complete lines and a clean end (no partial line), one line.
func TestStreamProviderCutOneLine(t *testing.T) {
	g := newStreamRig(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, brw, _ := http.NewResponseController(w).Hijack()
		_, _ = brw.WriteString("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Length: 1000\r\n\r\n" +
			"data: {\"model\":\"m1\"}\n\ndata: {\"mod")
		_ = brw.Flush()
		_ = c.Close()
	}))
	resp := g.post(context.Background(), t, streamBody)
	b, err := io.ReadAll(resp.Body)
	if err != nil || string(b) != "data: {\"model\":\"oa/m1\"}\n\n" {
		t.Fatalf("%q %v", b, err)
	}
	if rec := g.oneLine(t); rec.Status != 200 || rec.Error != nil {
		t.Fatalf("%+v", rec)
	}
}

// A client that reads nothing stops the provider's writes (backpressure): far less than what the provider tries to
// send is ever accepted from it.
func TestStreamBackpressure(t *testing.T) {
	var sent atomic.Int64
	line := `data: {"choices":[{"delta":{"content":"` + strings.Repeat("x", 1000) + `"}}]}` + "\n\n"
	g := newStreamRig(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		for i := 0; i < 256*1024; i++ { // 256 MB if nothing pushed back
			if _, err := io.WriteString(w, line); err != nil {
				return
			}
			sent.Add(int64(len(line)))
		}
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp := g.post(ctx, t, streamBody)
	defer resp.Body.Close()
	time.Sleep(2 * time.Second) // the client reads nothing
	if n := sent.Load(); n > 64<<20 {
		t.Fatalf("the provider wrote %d bytes to a client that read none", n)
	}
}

// A non-2xx answer to a stream is the P4b whole answer, with stream: true in its line.
func TestStreamUpstreamError(t *testing.T) {
	g := newStreamRig(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = io.WriteString(w, `{"error":{"message":"upstream exploded"}}`)
	}))
	resp := g.post(context.Background(), t, streamBody)
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 500 || !strings.Contains(string(b), "upstream exploded") {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	if rec := g.oneLine(t); rec.Status != 500 || !*rec.Stream {
		t.Fatalf("%+v", rec)
	}
}

// Wait returns once every stream's line is out; a stream still open holds it.
func TestStreamWait(t *testing.T) {
	hold := make(chan struct{})
	g := newStreamRig(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		select {
		case <-hold:
		case <-r.Context().Done():
		}
	}))
	resp := g.post(context.Background(), t, streamBody)
	defer resp.Body.Close()
	if g.e.Wait(200 * time.Millisecond) {
		t.Fatal("Wait returned while a stream was open")
	}
	close(hold)
	_, _ = io.ReadAll(resp.Body)
	if !g.e.Wait(5 * time.Second) {
		t.Fatal("Wait did not return after the stream ended")
	}
	g.oneLine(t)
}

// 200 concurrent streams, each 20 events 10 ms apart: all complete, 200 lines, 0 errors (every OS, -race included).
func TestTwoHundredStreams(t *testing.T) {
	g := newStreamRig(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		for i := 0; i < 20; i++ {
			_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"content":"x"}}]}`+"\n\n")
			w.(http.Flusher).Flush()
			time.Sleep(10 * time.Millisecond)
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	tr := &http.Transport{MaxConnsPerHost: 0, MaxIdleConnsPerHost: 256, DialContext: (&net.Dialer{}).DialContext}
	client := &http.Client{Transport: tr}
	defer tr.CloseIdleConnections()
	errs := make(chan error, 200)
	for i := 0; i < 200; i++ {
		go func() {
			req, _ := http.NewRequest("POST", g.url, strings.NewReader(streamBody))
			req.Header.Set("Authorization", g.key)
			resp, err := client.Do(req)
			if err != nil {
				errs <- err
				return
			}
			b, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err == nil && !strings.HasSuffix(string(b), "data: [DONE]\n\n") {
				err = io.ErrUnexpectedEOF
			}
			errs <- err
		}()
	}
	for i := 0; i < 200; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 200; i++ {
		select {
		case <-g.lines:
		case <-time.After(5 * time.Second):
			t.Fatalf("%d lines", i)
		}
	}
}

// chat on go and chat-stream on js and shadowed: a whole call is served, a stream goes to the control plane and gets a
// chat-stream shadow line; with chat-stream neither on go nor shadowed, a stream goes to the control plane plainly.
func TestStreamSubClassModes(t *testing.T) {
	g := newStreamRig(t, sseProvider([]string{"data: [DONE]\n\n"}, nil))
	lines := make(chan string, 4)
	g.e.SetOwn(func(method, path string, body []byte) (int, []byte, error) {
		if method == http.MethodPost {
			lines <- string(body)
		}
		return http.StatusNotFound, nil, nil
	})
	forwarded := make(chan struct{}, 4)
	js := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded <- struct{}{}
		_, _ = io.WriteString(w, "js")
	})
	for _, shadowed := range []bool{true, false} {
		g.e.SetModes(func(sub string) (bool, bool) { return sub == "chat", sub == "chat-stream" && shadowed })
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(streamBody))
		r.Host, r.RemoteAddr = "127.0.0.1:4777", "127.0.0.1:5000"
		r.Header.Set("Authorization", g.key)
		g.e.Serve("chat", w, r, "/v1/chat/completions", js)
		select {
		case <-forwarded:
		case <-time.After(2 * time.Second):
			t.Fatal("the stream was not forwarded")
		}
		if shadowed {
			select {
			case l := <-lines:
				if !strings.Contains(l, `"class":"chat-stream"`) {
					t.Fatalf("%s", l)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("no shadow line")
			}
		} else {
			select {
			case l := <-lines:
				t.Fatalf("a line for a stream not shadowed: %s", l)
			case <-time.After(200 * time.Millisecond):
			}
		}
	}
}

// P4c M4 root cause: streams that end together (a provider that paces every stream alike) put many connections back
// in the idle pool at once; past the pool's per-host limit they were closed, and the next wave dialled them again
// (handshakes, TIME_WAIT, bursts past the listen backlog: 1 s SYN retries). A second wave of 150 concurrent streams
// must reuse the first wave's connections.
func TestStreamWavesReuseProviderConnections(t *testing.T) {
	const n = 150 // within Windows' accept backlog (about 200)
	var conns atomic.Int64
	var release atomic.Pointer[chan struct{}]
	first := make(chan struct{})
	release.Store(&first)
	up := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"content":"x"}}]}`+"\n\n")
		w.(http.Flusher).Flush()
		<-*release.Load() // every stream of a wave ends at the same moment
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	up.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	up.Start()
	t.Cleanup(up.Close)
	g := newStreamRigAt(t, up.URL)
	tr := &http.Transport{MaxIdleConnsPerHost: n}
	client := &http.Client{Transport: tr}
	defer tr.CloseIdleConnections()
	wave := func() {
		errs := make(chan error, n)
		ready := make(chan error, n) // nil: the answer started; else why it did not
		for i := 0; i < n; i++ {
			go func() {
				req, _ := http.NewRequest("POST", g.url, strings.NewReader(streamBody))
				req.Header.Set("Authorization", g.key)
				resp, err := client.Do(req)
				if err != nil {
					ready <- err
					return
				}
				ready <- nil
				_, err = io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				errs <- err
			}()
		}
		failed := 0
		for i := 0; i < n; i++ {
			if err := <-ready; err != nil {
				t.Error(err)
				failed++
			}
		}
		close(*release.Load())
		for i := 0; i < n-failed; i++ {
			if err := <-errs; err != nil {
				t.Error(err)
			}
		}
		if failed > 0 {
			t.FailNow()
		}
		for i := 0; i < n; i++ {
			<-g.lines
		}
	}
	wave()
	opened := conns.Load()
	second := make(chan struct{})
	release.Store(&second)
	wave()
	if again := conns.Load() - opened; again > n/10 {
		t.Fatalf("the second wave opened %d new provider connections (first wave %d)", again, opened)
	}
}
