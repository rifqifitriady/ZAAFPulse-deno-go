package egress

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// P7 review L1: GET /hello?n=<nonce> proves the endpoint holds the egress token without the caller sending it: the
// answer is hex HMAC-SHA256(token, "zaafpulse egress hello <listening port> <nonce>"). mcp.js checks it before it
// sends the token (lib/egress-discovery.js); a process that squats the port after a crash never held the token, and
// one that relays the nonce to the real endpoint gets a proof for the real endpoint's port, not its own.

func TestHelloProofVector(t *testing.T) {
	// node -e "crypto.createHmac('sha256', TOKEN).update('zaafpulse egress hello 4242 ' + 'ab'.repeat(32)).digest('hex')"
	const want = "6a48143237facb291e083f006d1c8938dea62308bc35739292c7089b5bfb0a68"
	if got := HelloProof(testToken, 4242, strings.Repeat("ab", 32)); got != want {
		t.Fatalf("HelloProof = %s, want %s", got, want)
	}
}

func TestHelloAnswersWithoutTheToken(t *testing.T) {
	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer ts.Close()
	_, base, rec := startEgress(t, allowAll)
	port, _ := strconv.Atoi(strings.TrimSuffix(base[strings.LastIndex(base, ":")+1:], "/"))
	nonce := strings.Repeat("0f", 32)
	resp, err := http.Get(base + "hello?n=" + nonce)
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, resp)
	if resp.StatusCode != 200 || body != HelloProof(testToken, port, nonce) {
		t.Fatalf("%d %q", resp.StatusCode, body)
	}
	if resp.Header.Get("Cache-Control") != "no-store" || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") {
		t.Errorf("headers %v", resp.Header)
	}
	// A URL header does not turn /hello into a forward, with or without the token.
	for _, tok := range []string{"", testToken} {
		req, _ := http.NewRequest(http.MethodGet, base+"hello?n="+nonce, nil)
		req.Header.Set("X-ZP-Egress-URL", ts.URL+"/")
		if tok != "" {
			req.Header.Set("X-ZP-Egress-Token", tok)
		}
		resp, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		if b := readAll(t, resp); b != HelloProof(testToken, port, nonce) {
			t.Errorf("token %q: %q", tok, b)
		}
	}
	if hits.Load() != 0 {
		t.Fatal("/hello reached a target")
	}
	rec.none(t, 100*time.Millisecond)
}

func TestHelloRefusesABadNonce(t *testing.T) {
	_, base, _ := startEgress(t, allowAll)
	for _, q := range []string{"", "?n=", "?n=abc", "?n=" + strings.Repeat("AB", 32), "?n=" + strings.Repeat("ab", 33),
		"?n=" + strings.Repeat("zz", 32), "?n=" + strings.Repeat("ab", 32) + "&n=" + strings.Repeat("cd", 32)} {
		resp, err := http.Get(base + "hello" + q)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != 400 || len(b) > 200 || strings.Contains(string(b), testToken) {
			t.Errorf("%q: %d %q", q, resp.StatusCode, b)
		}
	}
	// Only GET: any other method on /hello is the token-checked path (dropped without the token).
	if got := rawExchange(t, base, "POST /hello?n="+strings.Repeat("ab", 32)+" HTTP/1.1\r\nHost: x\r\nContent-Length: 0\r\n\r\n"); len(got) != 0 {
		t.Errorf("POST /hello answered %q", got)
	}
}

func TestHelloWithoutATokenIsDropped(t *testing.T) {
	s, base, _ := startEgress(t, allowAll)
	s.token.Store(nil)
	if got := rawExchange(t, base, "GET /hello?n="+strings.Repeat("ab", 32)+" HTTP/1.1\r\nHost: x\r\n\r\n"); len(got) != 0 {
		t.Fatalf("answered %q", got)
	}
}
