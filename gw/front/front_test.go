package front

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const token = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// upstream stands for the control plane's internal listener: an HTTP server on 127.0.0.1 that records the
// X-Zp-Client header of each request.
type upstream struct {
	srv     *httptest.Server
	mu      sync.Mutex
	clients []string
}

func (u *upstream) port() int { return u.srv.Listener.Addr().(*net.TCPAddr).Port }

func (u *upstream) seen() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.clients...)
}

func startUpstream(t *testing.T, h http.HandlerFunc) *upstream {
	t.Helper()
	u := &upstream{}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.clients = append(u.clients, r.Header.Get(ClientHeader))
		u.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func startFront(t *testing.T, upstreamPort int, mod func(*Config)) *Server {
	t.Helper()
	cfg := Config{Host: "127.0.0.1", Port: 0, Upstream: upstreamPort, Token: token, Engines: map[string]string{"chat": "js"}}
	if mod != nil {
		mod(&cfg)
	}
	s, err := Listen(cfg, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Serve() }()
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func frontURL(s *Server) string { return "http://" + s.Addr().String() }

// rawRequest sends text on a new connection to addr and returns the whole answer (the connection closes after it)
// and the client's own address.
func rawRequest(t *testing.T, addr, text string) (string, net.Addr) {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(c, text); err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(c)
	if err != nil {
		t.Fatal(err)
	}
	return string(b), c.LocalAddr()
}

func TestClassify(t *testing.T) {
	for p, want := range map[string]string{
		"/v1/chat/completions":         "chat",
		"/v1/messages":                 "messages",
		"/v1/messages/count_tokens":    "messages",
		"/v1/responses":                "responses",
		"/v1/embeddings":               "embeddings",
		"/v1/models":                   "models",
		"/v1beta/models/x:generate":    "gemini",
		"/mcp":                         "mcp",
		"/a2a":                         "a2a",
		"/.well-known/agent-card.json": "a2a",
		"/skills/zaafpulse/SKILL.md":   "skills",
		"/v1/collaborate":              "agents",
		"/v1/traces":                   "",
		"/api/gateway/status":          "",
		"/":                            "",
		"/mcp/extra":                   "",
	} {
		if got := Classify(p); got != want {
			t.Errorf("Classify(%q) = %q, want %q", p, got, want)
		}
	}
}

// The header value: the client, then an HMAC the control plane checks (lib/front/client.js); zones dropped,
// IPv4-mapped addresses written as IPv4.
func TestClientValue(t *testing.T) {
	for _, c := range []struct {
		addr string
		tls  bool
		want string
	}{
		{"127.0.0.1:51234", false, "127.0.0.1 51234 0 "},
		{"[::1]:443", true, "::1 443 1 "},
		{"[fe80::1%en0]:7", false, "fe80::1 7 0 "},
		{"[::ffff:203.0.113.7]:9", false, "203.0.113.7 9 0 "},
	} {
		got := ClientValue(token, c.addr, c.tls)
		if !strings.HasPrefix(got, c.want) || len(got) != len(c.want)+64 {
			t.Errorf("ClientValue(%q) = %q", c.addr, got)
		}
	}
	// The vector lib/front/client.js computes: crypto.createHmac('sha256', token).update('127.0.0.1 51234 0').
	const js = "127.0.0.1 51234 0 a62228dc0c4ed9931e85579b843130b03bab9753bb2f298c63f931609e754171"
	if got := ClientValue(token, "127.0.0.1:51234", false); got != js {
		t.Fatalf("got %q, want %q", got, js)
	}
}

// The request reaches the upstream as the client sent it: method, path, query, Host and body kept; hop-by-hop headers
// (and those Connection names) removed; X-Forwarded-For and Forwarded exactly as sent, none added; a client's own
// X-Zp-Client replaced by the front's, which names the client's address and port.
func TestRequestGoesThroughAsSentWithTheClientHeader(t *testing.T) {
	type seen struct{ method, uri, host, body, secret, xff, fwd, conn string }
	got := make(chan seen, 1)
	u := startUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- seen{r.Method, r.RequestURI, r.Host, string(b), r.Header.Get("X-Secret"),
			strings.Join(r.Header.Values("X-Forwarded-For"), "|"), r.Header.Get("Forwarded"), r.Header.Get("Connection")}
		w.WriteHeader(201)
	})
	s := startFront(t, u.port(), nil)
	resp, local := rawRequest(t, s.Addr().String(), "POST /api/x?a=1 HTTP/1.1\r\nHost: dash.test:4777\r\n"+
		"Connection: close, X-Secret\r\nX-Secret: s\r\nX-Forwarded-For: 203.0.113.9\r\nForwarded: for=203.0.113.9\r\n"+
		"X-ZP-Client: 203.0.113.9 1 0 forged\r\nContent-Length: 5\r\n\r\nhello")
	if !strings.HasPrefix(resp, "HTTP/1.1 201") {
		t.Fatalf("answer %q", resp)
	}
	g := <-got
	if g != (seen{"POST", "/api/x?a=1", "dash.test:4777", "hello", "", "203.0.113.9", "for=203.0.113.9", ""}) {
		t.Fatalf("upstream saw %+v", g)
	}
	want := ClientValue(token, local.String(), false)
	if c := u.seen(); len(c) != 1 || c[0] != want {
		t.Fatalf("client headers %q, want %q", c, want)
	}
}

// Requests of two client connections share the upstream pool, and each carries its own client.
func TestEachRequestNamesItsOwnClient(t *testing.T) {
	u := startUpstream(t, func(w http.ResponseWriter, r *http.Request) {})
	s := startFront(t, u.port(), nil)
	var want []string
	for range 2 {
		c, err := net.Dial("tcp", s.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		br := bufio.NewReader(c)
		for range 2 {
			_, _ = io.WriteString(c, "GET / HTTP/1.1\r\nHost: localhost\r\n\r\n")
			resp, err := http.ReadResponse(br, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			want = append(want, ClientValue(token, c.LocalAddr().String(), false))
		}
		_ = c.Close()
	}
	if got := u.seen(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("client headers\n%q\nwant\n%q", got, want)
	}
}

// Each SSE event reaches the client before the upstream writes the next one.
func TestSSEFlushesEachEvent(t *testing.T) {
	next := make(chan struct{})
	u := startUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 1; i <= 3; i++ {
			fmt.Fprintf(w, "data: %d\n\n", i)
			w.(http.Flusher).Flush()
			select {
			case <-next:
			case <-time.After(10 * time.Second):
				return
			}
		}
	})
	s := startFront(t, u.port(), nil)
	resp, err := http.Get(frontURL(s) + "/v1/chat/completions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	for i := 1; i <= 3; i++ {
		line, err := br.ReadString('\n')
		if err != nil || line != fmt.Sprintf("data: %d\n", i) {
			t.Fatalf("event %d: %q %v", i, line, err)
		}
		_, _ = br.ReadString('\n')
		next <- struct{}{} // the upstream writes the next event only now
	}
}

// A WebSocket upgrade passes: 101, then bytes both ways.
func TestWebSocketUpgradePasses(t *testing.T) {
	u := startUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "websocket" {
			w.WriteHeader(400)
			return
		}
		c, brw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		_ = brw.Flush()
		line, _ := brw.ReadString('\n')
		_, _ = brw.WriteString("echo " + line)
		_ = brw.Flush()
	})
	s := startFront(t, u.port(), nil)
	c, err := net.Dial("tcp", s.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	_, _ = io.WriteString(c, "GET /ws HTTP/1.1\r\nHost: localhost\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != 101 {
		t.Fatalf("upgrade: %v %v", resp, err)
	}
	_, _ = io.WriteString(c, "ping\n")
	if line, err := br.ReadString('\n'); err != nil || line != "echo ping\n" {
		t.Fatalf("after the upgrade: %q %v", line, err)
	}
}

// Nothing listens upstream: 502, a JSON body and Retry-After: 2.
func TestUpstreamDownIs502(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	s := startFront(t, port, nil)
	resp, err := http.Get(frontURL(s) + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 502 || resp.Header.Get("Retry-After") != "2" ||
		string(b) != `{"error":"the control plane is not answering"}` ||
		resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("%d %v %s", resp.StatusCode, resp.Header, b)
	}
}

func TestPortInUse(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	_, err = Listen(Config{Host: "127.0.0.1", Port: held.Addr().(*net.TCPAddr).Port, Upstream: 1, Token: token},
		log.New(io.Discard, "", 0))
	if !errors.Is(err, ErrAddrInUse) {
		t.Fatalf("got %v", err)
	}
}

// writeCert writes a self-signed certificate for 127.0.0.1 and its key, and returns their paths and the pool that
// trusts it.
func writeCert(t *testing.T) (string, string, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	_ = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	_ = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600)
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return certPath, keyPath, pool
}

// With TLS the front terminates it and says so in the client header (tls 1).
func TestTLSIsTerminatedAndNamed(t *testing.T) {
	u := startUpstream(t, func(w http.ResponseWriter, r *http.Request) {})
	certPath, keyPath, pool := writeCert(t)
	s := startFront(t, u.port(), func(c *Config) { c.CertFile, c.KeyFile = certPath, keyPath })
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	defer client.CloseIdleConnections()
	resp, err := client.Get("https://" + s.Addr().String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	c := u.seen()
	if len(c) != 1 || strings.Fields(c[0])[2] != "1" {
		t.Fatalf("client headers %q", c)
	}
}

func TestBadTLSFilesAreAnError(t *testing.T) {
	_, err := Listen(Config{Host: "127.0.0.1", CertFile: filepath.Join(t.TempDir(), "none.pem"), KeyFile: "x"},
		log.New(io.Discard, "", 0))
	if err == nil || errors.Is(err, ErrAddrInUse) || !strings.Contains(err.Error(), "TLS") {
		t.Fatalf("got %v", err)
	}
}

// I1 (P4a final review): a request without a Host (HTTP/1.0) reaches the control plane flagged as host-less, so its
// host check sees no Host, as on the main port in js mode; never the internal 127.0.0.1:<port>. A client's own copy of
// the flag is removed.
func TestHostlessRequestIsFlagged(t *testing.T) {
	type seen struct{ host, flag string }
	got := make(chan seen, 2)
	u := startUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		got <- seen{r.Host, strings.Join(r.Header.Values(NoHostHeader), "|")}
	})
	s := startFront(t, u.port(), nil)
	resp, _ := rawRequest(t, s.Addr().String(), "GET /api/platform HTTP/1.0\r\n\r\n")
	if !strings.HasPrefix(resp, "HTTP/1.0 200") {
		t.Fatalf("answer %q", resp)
	}
	if g := <-got; g.flag != "1" {
		t.Fatalf("a host-less request reached the control plane as %+v, want the %s flag", g, NoHostHeader)
	}
	resp, _ = rawRequest(t, s.Addr().String(), "GET / HTTP/1.1\r\nHost: dash.test\r\n"+NoHostHeader+": 1\r\nConnection: close\r\n\r\n")
	if !strings.HasPrefix(resp, "HTTP/1.1 200") {
		t.Fatalf("answer %q", resp)
	}
	if g := <-got; g != (seen{"dash.test", ""}) {
		t.Fatalf("a request with a Host reached the control plane as %+v", g)
	}
}

// I1: an absolute-form target ("GET http://evil.example/ HTTP/1.1") is refused with 400, as the control plane refuses
// it on its own port: net/http drops the Host header of such a request, so the front cannot hand on the Host the
// control plane would check.
func TestAbsoluteFormIsRefused(t *testing.T) {
	u := startUpstream(t, func(w http.ResponseWriter, r *http.Request) {})
	s := startFront(t, u.port(), nil)
	for _, target := range []string{"http://evil.example/", "http://127.0.0.1/api/platform", "HTTPS://dash.test/x"} {
		resp, _ := rawRequest(t, s.Addr().String(), "GET "+target+" HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n")
		if !strings.HasPrefix(resp, "HTTP/1.1 400") || !strings.Contains(resp, AbsoluteFormError) {
			t.Fatalf("%s: answer %q", target, resp)
		}
	}
	if c := u.seen(); len(c) != 0 {
		t.Fatalf("the control plane saw %d requests", len(c))
	}
}

// m1 (P4a final review): the proxy headers the control plane looks for pass through even when the client names them
// in Connection (Node keeps them), and X-Forwarded-Host and X-Forwarded-Proto pass through as sent (T4-c).
func TestProxyHeadersPassThrough(t *testing.T) {
	names := []string{"Via", "X-Real-Ip", "X-Forwarded-Port", "Cf-Connecting-Ip", "True-Client-Ip",
		"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Forwarded"}
	got := make(chan http.Header, 2)
	u := startUpstream(t, func(w http.ResponseWriter, r *http.Request) { got <- r.Header.Clone() })
	s := startFront(t, u.port(), nil)
	for _, nominate := range []bool{false, true} {
		req := "GET / HTTP/1.1\r\nHost: dash.test\r\n"
		if nominate {
			req += "Connection: close, " + strings.Join(names, ", ") + "\r\n"
		} else {
			req += "Connection: close\r\n"
		}
		for i, n := range names {
			req += fmt.Sprintf("%s: v%d\r\n", n, i)
		}
		resp, _ := rawRequest(t, s.Addr().String(), req+"\r\n")
		if !strings.HasPrefix(resp, "HTTP/1.1 200") {
			t.Fatalf("answer %q", resp)
		}
		h := <-got
		for i, n := range names {
			if v := h.Values(n); len(v) != 1 || v[0] != fmt.Sprintf("v%d", i) {
				t.Errorf("nominated in Connection %v: %s reached the control plane as %q", nominate, n, v)
			}
		}
	}
}
