package front

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The route fixtures (scripts/fixtures/gen-route.js): the pathname the control plane routes on for each raw path. A
// path the gateway calls canonical must have that pathname unchanged, so the gateway and the control plane classify
// the same text (P4b entry condition 2).
func TestCanonicalPathsAreTheirOwnPathname(t *testing.T) {
	dir := filepath.Join("..", "testdata", "route")
	files, err := os.ReadDir(dir)
	if err != nil || len(files) == 0 {
		t.Fatalf("no route fixtures in %s (run node scripts/fixtures/gen-route.js): %v", dir, err)
	}
	canonical := 0
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(dir, f.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var c struct{ Fn, In, Out string }
		if err := json.Unmarshal(b, &c); err != nil || c.Fn != "pathname" {
			t.Fatalf("%s: %v %q", f.Name(), err, c.Fn)
		}
		if !Canonical(c.In) {
			if Classify(c.In) != "" {
				t.Errorf("%s: %q is not canonical but has class %q", f.Name(), c.In, Classify(c.In))
			}
			continue
		}
		canonical++
		if c.In != c.Out {
			t.Errorf("%s: canonical %q has pathname %q", f.Name(), c.In, c.Out)
		}
	}
	if canonical < 10 {
		t.Errorf("only %d canonical fixtures", canonical)
	}
}

func TestRawPath(t *testing.T) {
	for in, want := range map[string]string{
		"/v1/chat/completions":         "/v1/chat/completions",
		"/v1/chat/completions?x=1":     "/v1/chat/completions",
		"/v1/chat%2Fcompletions?a=%2F": "/v1/chat%2Fcompletions",
		"/a#b":                         "/a",
		"*":                            "*",
	} {
		if got := RawPath(in); got != want {
			t.Errorf("RawPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// With chat on the go engine, only the exact spelling is the gateway's: an encoded or dot-segment spelling goes to the
// control plane, which routes it as js mode does (before P4b, Go decoded %2F and called it chat).
func TestOnlyTheCanonicalSpellingIsClassified(t *testing.T) {
	up := startUpstream(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "js "+r.RequestURI) })
	s := startFront(t, up.port(), func(c *Config) { c.Engines = map[string]string{"chat": "go"} })
	for _, p := range []string{"/v1/chat%2Fcompletions", "/v1/./chat/completions", "/v1/x/../chat/completions"} {
		out, _ := rawRequest(t, s.Addr().String(), "POST "+p+" HTTP/1.1\r\nHost: 127.0.0.1\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		if !strings.Contains(out, "200 OK") || !strings.Contains(out, "js "+p) {
			t.Errorf("%s: not forwarded as sent: %q", p, out)
		}
	}
}

// Headers past Node's limit get 431 from the front, as from Node, never the 502 of a closed upstream connection.
func TestOversizedHeadersAre431(t *testing.T) {
	up := startUpstream(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") })
	s := startFront(t, up.port(), nil)
	ask := func(n int) string {
		out, _ := rawRequest(t, s.Addr().String(), "GET /x HTTP/1.1\r\nHost: 127.0.0.1\r\nX-Big: "+
			strings.Repeat("a", n)+"\r\nConnection: close\r\n\r\n")
		return out
	}
	if out := ask(8 * 1024); !strings.Contains(out, "200 OK") {
		t.Errorf("8 KiB header: %q", out)
	}
	if out := ask(20 * 1024); !strings.HasPrefix(out, "HTTP/1.1 431") {
		t.Errorf("20 KiB header: %q", out)
	}
}

// CONNECT gets no answer: the connection is closed, as Node does without a connect listener.
func TestConnectIsClosedWithoutAnAnswer(t *testing.T) {
	up := startUpstream(t, func(w http.ResponseWriter, r *http.Request) { t.Error("CONNECT reached the control plane") })
	s := startFront(t, up.port(), nil)
	if out, _ := rawRequest(t, s.Addr().String(), "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n"); out != "" {
		t.Errorf("CONNECT answered: %q", out)
	}
}

// A noisy kind of line is logged once a minute with a count of the ones left out; any other line goes through.
func TestQuietLoggerRateLimitsNoisyLines(t *testing.T) {
	var buf bytes.Buffer
	now := time.Unix(1000, 0)
	q := QuietLogger(log.New(&buf, "", 0), func() time.Time { return now })
	for range 5 {
		q.Print("http: TLS handshake error from 1.2.3.4:5: EOF")
	}
	q.Print("ready")
	now = now.Add(QuietEvery)
	q.Print("http: TLS handshake error from 1.2.3.4:6: EOF")
	want := "http: TLS handshake error from 1.2.3.4:5: EOF\nready\n" +
		"http: TLS handshake error from 1.2.3.4:6: EOF (4 more like this in the last minute were not logged)\n"
	if buf.String() != want {
		t.Errorf("log %q, want %q", buf.String(), want)
	}
}

// The front forwards the request target exactly as the client sent it (P4b review I2): a backslash, a "|" or a ";" in
// the query reach the control plane unchanged, so js and front mode route the same text.
func TestRequestTargetIsForwardedAsSent(t *testing.T) {
	got := make(chan string, 8)
	up := startUpstream(t, func(w http.ResponseWriter, r *http.Request) { got <- r.RequestURI })
	s := startFront(t, up.port(), nil)
	for _, target := range []string{`/v1\chat/completions`, "/v1/a|b", "/api/x?q=a;b&c=1", "/v1/a^b?x=%zz&y=\\",
		"/v1/chat%2Fcompletions?a=%2F", "//v1/chat/completions", "///a", "//v1/x?q=a;b", "//host/x%2Fy"} {
		out, _ := rawRequest(t, s.Addr().String(), "GET "+target+" HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n")
		if !strings.Contains(out, "200 OK") {
			t.Errorf("%s: %q", target, out)
			continue
		}
		if r := <-got; r != target {
			t.Errorf("forwarded %q, sent %q", r, target)
		}
	}
}

// A client's own X-Zp-Shadow or X-Zp-Own never reaches the control plane: only the gateway marks a request.
func TestClientShadowMarksAreRemoved(t *testing.T) {
	got := make(chan http.Header, 1)
	up := startUpstream(t, func(w http.ResponseWriter, r *http.Request) { got <- r.Header.Clone() })
	s := startFront(t, up.port(), nil)
	req, _ := http.NewRequest("POST", frontURL(s)+"/v1/chat/completions", strings.NewReader("{}"))
	req.Header.Set(ShadowHeader, "0123456789abcdef")
	req.Header.Set(OwnHeader, "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	h := <-got
	if h.Get(ShadowHeader) != "" || h.Get(OwnHeader) != "" {
		t.Fatalf("forwarded %v", h)
	}
}

// Node and Deno disagree on /v1/a^b (Node's pathname is /v1/a%5Eb, Deno's /v1/a^b), so it is not in the route corpus:
// the front never classifies it, and the control plane routes it whatever its runtime does.
func TestCaretPathIsNotCanonical(t *testing.T) {
	if Canonical("/v1/a^b") || Classify("/v1/a^b") != "" {
		t.Fatal("/v1/a^b is classified")
	}
}

// fakeShadow marks every chat request as the go engine does.
type fakeShadow struct{}

func (fakeShadow) Serve(string, http.ResponseWriter, *http.Request, string, http.Handler) {}
func (fakeShadow) Shadow(_ string, w http.ResponseWriter, r *http.Request, _ string, js http.Handler,
	_ func(string, string, []byte) (int, []byte, error)) {
	js.ServeHTTP(w, WithShadowID(r, "0123456789abcdef"))
}

// Final review m9': the mark is set in the proxy's Rewrite, after the hop-by-hop headers are gone, so a client that
// names X-Zp-Shadow in Connection cannot strip it.
func TestShadowMarkSurvivesAConnectionHeader(t *testing.T) {
	got := make(chan string, 1)
	up := startUpstream(t, func(w http.ResponseWriter, r *http.Request) { got <- r.Header.Get(ShadowHeader) })
	s := startFront(t, up.port(), nil)
	s.SetGoEngine(fakeShadow{})
	s.SetShadow([]string{"chat"})
	out, _ := rawRequest(t, s.Addr().String(), "POST /v1/chat/completions HTTP/1.1\r\nHost: 127.0.0.1\r\n"+
		"Connection: close, X-Zp-Shadow\r\nContent-Length: 0\r\n\r\n")
	if !strings.Contains(out, "200 OK") {
		t.Fatalf("%q", out)
	}
	if id := <-got; id != "0123456789abcdef" {
		t.Fatalf("mark %q", id)
	}
}

// recEngine records which entry the front called and the modes it was given.
type recEngine struct {
	calls chan string
	modes func(string) (bool, bool)
}

func (e *recEngine) Serve(string, http.ResponseWriter, *http.Request, string, http.Handler) {
	e.calls <- "serve"
}
func (e *recEngine) Shadow(string, http.ResponseWriter, *http.Request, string, http.Handler,
	func(string, string, []byte) (int, []byte, error)) {
	e.calls <- "shadow"
}
func (e *recEngine) SetModes(f func(string) (bool, bool)) { e.modes = f }

// P4c: chat reaches the engine when chat or its sub-class chat-stream is on go (Serve) or shadowed (Shadow); the
// engine reads the per-sub-class modes from the front.
func TestChatStreamSubClassDispatch(t *testing.T) {
	up := startUpstream(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "js") })
	for _, c := range []struct {
		engines map[string]string
		shadow  []string
		want    string
	}{
		{map[string]string{"chat": "js"}, nil, "js"},
		{map[string]string{"chat": "js", "chat-stream": "go"}, nil, "serve"},
		{map[string]string{"chat": "go", "chat-stream": "js"}, nil, "serve"},
		{map[string]string{"chat": "js", "chat-stream": "js"}, []string{"chat-stream"}, "shadow"},
		{map[string]string{"chat": "js"}, []string{"chat"}, "shadow"},
	} {
		s := startFront(t, up.port(), func(cfg *Config) { cfg.Engines = c.engines })
		e := &recEngine{calls: make(chan string, 1)}
		s.SetGoEngine(e)
		s.SetShadow(c.shadow)
		resp, err := http.Post(frontURL(s)+"/v1/chat/completions", "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		got := "js"
		select {
		case got = <-e.calls:
		default:
		}
		if got != c.want {
			t.Errorf("%v %v: %s, want %s", c.engines, c.shadow, got, c.want)
		}
		if e.modes == nil {
			t.Fatal("the engine got no modes")
		}
		onGo, shadowed := e.modes("chat-stream")
		if onGo != (c.engines["chat-stream"] == "go") || shadowed != (c.engines["chat-stream"] != "go" && slices.Contains(c.shadow, "chat-stream")) {
			t.Errorf("%v %v: modes(chat-stream) = %v %v", c.engines, c.shadow, onGo, shadowed)
		}
	}
}

// P4c M4 root cause, front side: requests forwarded to the control plane that end together put their connections back
// at once; past the proxy transport's idle limit they were closed and the next wave dialled the internal port again.
// A second wave of 300 concurrent requests must reuse the first wave's connections.
func TestForwardedWavesReuseUpstreamConnections(t *testing.T) {
	const n = 300
	var conns atomic.Int64
	var release atomic.Pointer[chan struct{}]
	first := make(chan struct{})
	release.Store(&first)
	up := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-*release.Load()
		_, _ = io.WriteString(w, "done")
	}))
	up.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	up.Start()
	t.Cleanup(up.Close)
	s := startFront(t, up.Listener.Addr().(*net.TCPAddr).Port, nil)
	tr := &http.Transport{MaxIdleConnsPerHost: n}
	client := &http.Client{Transport: tr}
	defer tr.CloseIdleConnections()
	wave := func() {
		errs := make(chan error, n)
		started := make(chan struct{}, n)
		for i := 0; i < n; i++ {
			go func() {
				resp, err := client.Get(frontURL(s) + "/api/x")
				if err != nil {
					errs <- err
					return
				}
				started <- struct{}{}
				_, err = io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				errs <- err
			}()
		}
		for i := 0; i < n; i++ {
			<-started
		}
		close(*release.Load())
		for i := 0; i < n; i++ {
			if err := <-errs; err != nil {
				t.Fatal(err)
			}
		}
	}
	wave()
	opened := conns.Load()
	second := make(chan struct{})
	release.Store(&second)
	wave()
	if again := conns.Load() - opened; again > n/10 {
		t.Fatalf("the second wave opened %d new upstream connections (first wave %d)", again, opened)
	}
}
