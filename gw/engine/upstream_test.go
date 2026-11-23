package engine

import (
	"context"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/egress"
)

func planTo(url string) *Plan {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Authorization", "Bearer sk-test")
	return &Plan{URL: url, Header: h, Body: []byte(`{"model":"m1"}`)}
}

// The provider gets the plan's method, headers and body; its status and body come back as they are.
func TestUpstreamCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer sk-test" || string(b) != `{"model":"m1"}` ||
			r.Header.Get("Accept-Encoding") != "" {
			t.Errorf("%s %v %s", r.Method, r.Header, b)
		}
		w.WriteHeader(418)
		_, _ = io.WriteString(w, "teapot")
	}))
	defer srv.Close()
	res := NewUpstream(func(string) string { return "" }).Call(context.Background(), planTo(srv.URL+"/v1/chat/completions"))
	if res.Status != 418 || string(res.Body) != "teapot" || res.Why != "" {
		t.Fatalf("%+v", res)
	}
}

// A redirect is followed (same method and body) to the same origin only; another host is refused with the JS text.
func TestUpstreamRedirects(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("followed") }))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a":
			http.Redirect(w, r, "/b", http.StatusFound)
		case "/b":
			b, _ := io.ReadAll(r.Body)
			_, _ = io.WriteString(w, r.Method+" "+string(b))
		case "/away":
			http.Redirect(w, r, other.URL+"/x", http.StatusTemporaryRedirect)
		}
	}))
	defer srv.Close()
	up := NewUpstream(func(string) string { return "" })
	if res := up.Call(context.Background(), planTo(srv.URL+"/a")); string(res.Body) != `POST {"model":"m1"}` {
		t.Fatalf("same origin: %+v", res)
	}
	res := up.Call(context.Background(), planTo(srv.URL+"/away"))
	want := "refused a redirect from " + strings.TrimPrefix(srv.URL, "http://") + " to " +
		strings.TrimPrefix(other.URL, "http://") + "; credentials are not forwarded to another host"
	if res.Why != want {
		t.Fatalf("%q, want %q", res.Why, want)
	}
}

// No answer started within the timeout: "timed out"; nothing listening: ECONNREFUSED; a connection closed with no
// answer: UND_ERR_SOCKET (the names Node's fetch gives, which the JS gateway puts in its 502).
func TestUpstreamFailures(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer slow.Close()
	up := NewUpstream(func(string) string { return "" })
	up.timeout = 200 * time.Millisecond
	if res := up.Call(context.Background(), planTo(slow.URL)); res.Why != "timed out" {
		t.Errorf("slow: %+v", res)
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	_ = ln.Close()
	if res := up.Call(context.Background(), planTo("http://"+addr+"/")); res.Why != "ECONNREFUSED" {
		t.Errorf("refused: %+v", res)
	}
	reset, _ := net.Listen("tcp", "127.0.0.1:0")
	defer reset.Close()
	go func() {
		for {
			c, err := reset.Accept()
			if err != nil {
				return
			}
			_, _ = c.Read(make([]byte, 4096))
			_ = c.Close()
		}
	}()
	if res := up.Call(context.Background(), planTo("http://"+reset.Addr().String()+"/")); res.Why != "UND_ERR_SOCKET" {
		t.Errorf("reset: %+v", res)
	}
}

// A provider with a private CA is trusted once NODE_EXTRA_CA_CERTS (or DENO_CERT, SSL_CERT_FILE) names its certificate,
// as Node and Deno trust it; without it the call fails with Node's name for an unknown issuer.
func TestUpstreamExtraCA(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") }))
	defer srv.Close()
	if res := NewUpstream(func(string) string { return "" }).Call(context.Background(), planTo(srv.URL)); res.Why == "" {
		t.Fatalf("an unknown CA was trusted: %+v", res)
	}
	f := filepath.Join(t.TempDir(), "ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(f, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"NODE_EXTRA_CA_CERTS", "DENO_CERT", "SSL_CERT_FILE"} {
		env := func(k string) string {
			if k == name {
				return f
			}
			return ""
		}
		if res := NewUpstream(env).Call(context.Background(), planTo(srv.URL)); res.Why != "" || string(res.Body) != "ok" {
			t.Errorf("%s: %+v", name, res)
		}
	}
}

// Windows names a refused or reset connection by its Winsock code; the engine names them as Node does on Windows
// (re-review N4), on every OS this test runs on.
func TestWindowsSocketErrorsHaveNodeNames(t *testing.T) {
	for code, want := range map[syscall.Errno]string{10061: "ECONNREFUSED", 10054: "ECONNRESET", 10060: "ETIMEDOUT",
		10065: "EHOSTUNREACH", 10051: "ENETUNREACH"} {
		err := &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connectex", code)}
		if got := ErrorCode(err); got != want {
			t.Errorf("errno %d: %q, want %q", code, got, want)
		}
	}
}

// Start returns once the headers are in: the body is read by the caller, and closing it ends the call (the provider
// sees its request cancelled). The provider gets a Content-Length, as fetch sends one for a string body.
func TestUpstreamStart(t *testing.T) {
	gone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength != int64(len(`{"model":"m1"}`)) {
			t.Errorf("content-length %d", r.ContentLength)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "data: 1\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(gone)
	}))
	defer srv.Close()
	p := planTo(srv.URL)
	resp, why := NewUpstream(func(string) string { return "" }).Start(context.Background(), p)
	if why != "" || resp.StatusCode != 200 {
		t.Fatalf("%q %v", why, resp)
	}
	buf := make([]byte, 64)
	n, _ := resp.Body.Read(buf)
	if string(buf[:n]) != "data: 1\n\n" {
		t.Fatalf("%q", buf[:n])
	}
	_ = resp.Body.Close()
	select {
	case <-gone:
	case <-time.After(5 * time.Second):
		t.Fatal("closing the body did not end the call")
	}
}

type countingRT struct {
	rt http.RoundTripper
	n  int
}

func (c *countingRT) RoundTrip(r *http.Request) (*http.Response, error) {
	c.n++
	return c.rt.RoundTrip(r)
}

// P5: WrapTransport puts a wrapper around every provider call (the gateway's recording RoundTripper), Transport stays the
// unwrapped *http.Transport (the egress endpoint copies it), and the token sources' copy of the client keeps the wrapper.
func TestUpstreamWrapTransport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()
	up := NewUpstream(func(string) string { return "" })
	base := up.Transport()
	if base == nil || up.Client().Transport != base {
		t.Fatalf("transport %v", base)
	}
	c := &countingRT{}
	up.WrapTransport(func(rt http.RoundTripper) http.RoundTripper { c.rt = rt; return c })
	if up.Transport() != base || c.rt != base {
		t.Fatal("Transport is not the unwrapped one")
	}
	if res := up.Call(context.Background(), planTo(srv.URL)); res.Status != 200 || c.n != 1 {
		t.Fatalf("%+v, %d round trips seen", res, c.n)
	}
	e := New(up, nil, t.Logf)
	if e.Tokens().client.Transport != c {
		t.Fatal("the token sources do not see the wrapped client")
	}
	if (&Upstream{}).Transport() != nil {
		t.Fatal("an upstream not built by NewUpstream has no transport")
	}
}

// A provider call the gateway's egress allow-list refuses (gw/egress RecordingTransport in enforce mode, P5 Decision
// 13) is an ordinary failed call named EGRESS_REFUSED: nothing reaches the provider.
func TestUpstreamEgressRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("a refused call reached the provider")
	}))
	defer srv.Close()
	var recs []egress.Record
	up := NewUpstream(func(string) string { return "" })
	pol := &egress.Policy{Mode: egress.ModeEnforce, Allow: []egress.Rule{{Host: "api.example.com"}}}
	up.WrapTransport(func(rt http.RoundTripper) http.RoundTripper {
		return egress.RecordingTransport(rt, "gateway", func() *egress.Policy { return pol },
			func(r egress.Record) { recs = append(recs, r) })
	})
	// An off-list name: a loopback provider is never checked (P7), and a refused call is never dialled.
	off := strings.Replace(srv.URL, "127.0.0.1", "offlist.example", 1)
	res := up.Call(context.Background(), planTo(off+"/v1/chat/completions"))
	if res.Why != "EGRESS_REFUSED" || res.Status != 0 {
		t.Fatalf("%+v", res)
	}
	if len(recs) != 1 || recs[0].Outcome != egress.OutcomeRefused || recs[0].Via != egress.ViaEngine {
		t.Fatalf("records %+v", recs)
	}
}
