package egress

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDeriveToken(t *testing.T) {
	// node -e "crypto.createHmac('sha256','0123abcd').update('zaafpulse egress v1').digest('hex')"
	const want = "d219e2f4118b5dfcb931bba186ad6f85159169831918c83cf48b9f2125d76794"
	if got := DeriveToken("0123abcd"); got != want {
		t.Fatalf("DeriveToken = %s, want %s", got, want)
	}
}

// rawExchange writes req on a fresh connection to the endpoint and returns every byte it answers before closing.
func rawExchange(t *testing.T, base, req string) []byte {
	t.Helper()
	c, err := net.Dial("tcp", strings.TrimSuffix(strings.TrimPrefix(base, "http://"), "/"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(c, req); err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(c)
	if err != nil {
		t.Fatalf("read: %v (got %q)", err, b)
	}
	return b
}

func TestBadTokenGetsNoBytes(t *testing.T) {
	hits := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer ts.Close()
	s, base, rec := startEgress(t, allowAll)
	url := "X-ZP-Egress-URL: " + ts.URL + "/\r\n"
	for name, req := range map[string]string{
		"no token":          "GET / HTTP/1.1\r\nHost: x\r\n" + url + "\r\n",
		"wrong token":       "GET / HTTP/1.1\r\nHost: x\r\nX-ZP-Egress-Token: " + strings.Repeat("0", 64) + "\r\n" + url + "\r\n",
		"short token":       "GET / HTTP/1.1\r\nHost: x\r\nX-ZP-Egress-Token: " + testToken[:10] + "\r\n" + url + "\r\n",
		"check no token":    "POST /check HTTP/1.1\r\nHost: x\r\nContent-Length: 0\r\n" + url + "\r\n",
		"connect no token":  "CONNECT " + strings.TrimPrefix(ts.URL, "http://") + " HTTP/1.1\r\nHost: x\r\n\r\n",
		"connect bad token": "CONNECT " + strings.TrimPrefix(ts.URL, "http://") + " HTTP/1.1\r\nHost: x\r\nX-ZP-Egress-Token: nope\r\n\r\n",
	} {
		if got := rawExchange(t, base, req); len(got) != 0 {
			t.Errorf("%s: answered %q", name, got)
		}
	}
	// No token set yet: everything is dropped, the right token included.
	s.token.Store(nil)
	if got := rawExchange(t, base, "GET / HTTP/1.1\r\nHost: x\r\nX-ZP-Egress-Token: "+testToken+"\r\n"+url+"\r\n"); len(got) != 0 {
		t.Errorf("unset token: answered %q", got)
	}
	if hits != 0 {
		t.Fatalf("the target was reached %d times", hits)
	}
	rec.none(t, 100*time.Millisecond)
}

func TestForwardHeadersBodyAndRecord(t *testing.T) {
	type seen struct {
		method, uri, host, body string
		h                       http.Header
	}
	got := make(chan seen, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- seen{r.Method, r.RequestURI, r.Host, string(b), r.Header.Clone()}
		w.Header().Set("Connection", "X-Bar")
		w.Header().Set("X-Bar", "1")
		w.Header().Set("Proxy-Foo", "z")
		w.Header().Set("X-Ok", "yes")
		w.WriteHeader(201)
		_, _ = io.WriteString(w, "created!")
	}))
	defer ts.Close()
	_, base, rec := startEgress(t, allowAll)
	target := "http://A.test:" + portOf(t, ts.URL) + "/p/a?q=secret#frag"
	resp := send(t, base, "POST", target, strings.NewReader("hello"), map[string]string{
		"Connection":          "X-Foo",
		"X-Foo":               "1",
		"Keep-Alive":          "timeout=5",
		"Proxy-Authorization": "Basic eA==",
		"Te":                  "trailers",
		"Accept-Encoding":     "br",
		"Authorization":       "Bearer k",
		"Content-Type":        "text/plain",
		"User-Agent":          "zp-test",
		"X-Custom":            "y",
	})
	body := readAll(t, resp)
	s := <-got
	if s.method != "POST" || s.uri != "/p/a?q=secret" || s.body != "hello" || s.host != "a.test:"+portOf(t, ts.URL) {
		t.Fatalf("target saw %+v", s)
	}
	for _, k := range []string{"X-Foo", "Keep-Alive", "Proxy-Authorization", "Te", "X-Zp-Egress-Token",
		"X-Zp-Egress-Url", "X-Zp-Egress-Purpose"} {
		if v := s.h.Values(k); len(v) != 0 {
			t.Errorf("target got %s: %q", k, v)
		}
	}
	for k, want := range map[string]string{"Accept-Encoding": "gzip", "Authorization": "Bearer k",
		"Content-Type": "text/plain", "User-Agent": "zp-test", "X-Custom": "y"} {
		if v := s.h.Get(k); v != want {
			t.Errorf("target got %s %q, want %q", k, v, want)
		}
	}
	if resp.StatusCode != 201 || body != "created!" {
		t.Fatalf("answer %d %q", resp.StatusCode, body)
	}
	if resp.Header.Get("X-Bar") != "" || resp.Header.Get("Proxy-Foo") != "" || resp.Header.Get("X-Ok") != "yes" {
		t.Errorf("answer headers %v", resp.Header)
	}
	if u := resp.Header.Get("X-ZP-Egress-URL"); u != "http://a.test:"+portOf(t, ts.URL)+"/p/a?q=secret" {
		t.Errorf("X-ZP-Egress-URL %q", u)
	}
	if resp.Header.Get("X-ZP-Egress-Redirected") != "" {
		t.Error("redirected set without a redirect")
	}
	r := rec.wait(t, 1)[0]
	port, _ := strconv.Atoi(portOf(t, ts.URL))
	want := Record{TS: r.TS, Purpose: "connectors:test", Method: "POST", Scheme: "http", Host: "a.test", Port: port,
		Status: 201, Outcome: "ok", BytesOut: 5, BytesIn: 8, MS: r.MS, Via: "proxy"}
	if r != want {
		t.Fatalf("record %+v\nwant   %+v", r, want)
	}
	if r.TS < time.Now().Add(-time.Minute).UnixMilli() || r.MS <= 0 {
		t.Errorf("ts %d ms %v", r.TS, r.MS)
	}
	j, _ := json.Marshal(r)
	for _, leak := range []string{"/p/a", "secret", "frag", "Bearer", "zp-test"} {
		if strings.Contains(string(j), leak) {
			t.Errorf("record holds %q: %s", leak, j)
		}
	}
}

func TestNoUserAgentWhenCallerSentNone(t *testing.T) {
	got := make(chan []string, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Values("User-Agent")
	}))
	defer ts.Close()
	_, base, _ := startEgress(t, allowAll)
	req, _ := http.NewRequest("GET", base, nil)
	req.Header.Set("X-ZP-Egress-Token", testToken)
	req.Header.Set("X-ZP-Egress-URL", ts.URL)
	req.Header["User-Agent"] = []string{""} // the test client sends none
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	readAll(t, resp)
	if ua := <-got; len(ua) != 0 {
		t.Fatalf("target got User-Agent %q", ua)
	}
}

func TestGzipDecoded(t *testing.T) {
	const text = "a decoded body, a decoded body, a decoded body"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept-Encoding") != "gzip" {
			t.Errorf("Accept-Encoding %q", r.Header.Get("Accept-Encoding"))
		}
		w.Header().Set("Content-Encoding", "gzip")
		zw := gzip.NewWriter(w)
		_, _ = io.WriteString(zw, text)
		_ = zw.Close()
	}))
	defer ts.Close()
	_, base, rec := startEgress(t, allowAll)
	resp := send(t, base, "GET", ts.URL+"/z", nil, map[string]string{"Accept-Encoding": "br, deflate"})
	if body := readAll(t, resp); body != text {
		t.Fatalf("body %q", body)
	}
	if ce := resp.Header.Get("Content-Encoding"); ce != "" {
		t.Errorf("Content-Encoding %q passed on", ce)
	}
	if r := rec.wait(t, 1)[0]; r.BytesIn != int64(len(text)) {
		t.Errorf("bytesIn %d, want the decoded %d", r.BytesIn, len(text))
	}
}

func TestSSEChunkByChunk(t *testing.T) {
	next := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 1; i <= 3; i++ {
			_, _ = fmt.Fprintf(w, "data: %d\n\n", i)
			w.(http.Flusher).Flush()
			select {
			case <-next:
			case <-r.Context().Done():
				return
			case <-time.After(5 * time.Second):
				t.Error("the caller never read chunk", i)
				return
			}
		}
	}))
	defer ts.Close()
	_, base, rec := startEgress(t, allowAll)
	resp := send(t, base, "GET", ts.URL+"/sse", nil, nil)
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	for i := 1; i <= 3; i++ {
		// Each event arrives while the target still holds the next one back.
		line, err := br.ReadString('\n')
		if err != nil || line != fmt.Sprintf("data: %d\n", i) {
			t.Fatalf("event %d: %q %v", i, line, err)
		}
		_, _ = br.ReadString('\n')
		next <- struct{}{}
	}
	if rest, _ := io.ReadAll(br); len(rest) != 0 {
		t.Fatalf("rest %q", rest)
	}
	if r := rec.wait(t, 1)[0]; r.BytesIn != int64(3*len("data: 1\n\n")) || r.Outcome != "ok" {
		t.Fatalf("record %+v", r)
	}
}

// redirector answers /r/<status>/<n> with a redirect to /r/<status>/<n-1> (absolute when the query asks), and
// /final with what it received.
func redirector(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/final" {
			b, _ := io.ReadAll(r.Body)
			w.Header().Set("X-Method", r.Method)
			w.Header().Set("X-Body", string(b))
			w.Header().Set("X-Content-Type", r.Header.Get("Content-Type"))
			w.Header().Set("X-Auth", r.Header.Get("Authorization"))
			_, _ = io.WriteString(w, "final")
			return
		}
		var status, n int
		if _, err := fmt.Sscanf(r.URL.Path, "/r/%d/%d", &status, &n); err != nil {
			http.NotFound(w, r)
			return
		}
		_, _ = io.ReadAll(r.Body)
		loc := "/final"
		if n > 1 {
			loc = fmt.Sprintf("/r/%d/%d", status, n-1)
		}
		if to := r.URL.Query().Get("to"); to != "" && n == 1 {
			loc = to + "/final"
		}
		w.Header().Set("Location", loc)
		w.WriteHeader(status)
	}))
}

func TestRedirectMatrix(t *testing.T) {
	ts := redirector(t)
	defer ts.Close()
	_, base, rec := startEgress(t, allowAll)
	for _, status := range []int{301, 302, 303, 307, 308} {
		for _, method := range []string{"GET", "POST", "PUT", "HEAD"} {
			name := fmt.Sprintf("%d %s", status, method)
			var body io.Reader
			if method == "POST" || method == "PUT" {
				body = strings.NewReader("payload")
			}
			resp := send(t, base, method, fmt.Sprintf("%s/r/%d/1", ts.URL, status), body,
				map[string]string{"Content-Type": "text/plain"})
			readAll(t, resp)
			wantMethod, wantBody, wantCT := method, "", "text/plain"
			if body != nil {
				wantBody = "payload"
			}
			if (status == 303 && method != "HEAD") || ((status == 301 || status == 302) && method == "POST") {
				wantMethod, wantBody, wantCT = "GET", "", ""
			}
			if resp.StatusCode != 200 || resp.Header.Get("X-Method") != wantMethod ||
				resp.Header.Get("X-Body") != wantBody || resp.Header.Get("X-Content-Type") != wantCT {
				t.Errorf("%s: %d method %q body %q ct %q; want %s %q %q", name, resp.StatusCode,
					resp.Header.Get("X-Method"), resp.Header.Get("X-Body"), resp.Header.Get("X-Content-Type"),
					wantMethod, wantBody, wantCT)
			}
			if resp.Header.Get("X-ZP-Egress-Redirected") != "1" || resp.Header.Get("X-ZP-Egress-URL") != ts.URL+"/final" {
				t.Errorf("%s: redirected %q url %q", name, resp.Header.Get("X-ZP-Egress-Redirected"),
					resp.Header.Get("X-ZP-Egress-URL"))
			}
			recs := rec.wait(t, 2)
			if recs[0].Status != status || recs[0].Method != method || recs[1].Status != 200 ||
				recs[1].Method != wantMethod {
				t.Errorf("%s: records %+v", name, recs)
			}
			if body != nil && recs[0].BytesOut != 7 {
				t.Errorf("%s: first hop bytesOut %d", name, recs[0].BytesOut)
			}
		}
	}
}

func TestRedirectReplaysAStreamedBody(t *testing.T) {
	ts := redirector(t)
	defer ts.Close()
	_, base, _ := startEgress(t, allowAll)
	pr, pw := io.Pipe()
	go func() {
		_, _ = io.WriteString(pw, "part one, ")
		time.Sleep(20 * time.Millisecond)
		_, _ = io.WriteString(pw, "part two")
		_ = pw.Close()
	}()
	resp := send(t, base, "POST", ts.URL+"/r/307/2", pr, nil) // chunked: no Content-Length
	readAll(t, resp)
	if resp.Header.Get("X-Body") != "part one, part two" {
		t.Fatalf("replayed body %q", resp.Header.Get("X-Body"))
	}
}

func TestRedirectBodyTooLargeToReplay(t *testing.T) {
	ts := redirector(t)
	defer ts.Close()
	s, base, _ := startEgress(t, allowAll)
	s.maxReplay = 4
	resp := send(t, base, "POST", ts.URL+"/r/308/1", strings.NewReader("too long"), nil)
	readAll(t, resp)
	if resp.StatusCode != 502 || resp.Header.Get("X-ZP-Egress-Error") != "EGRESS_REDIRECT" {
		t.Fatalf("%d %q", resp.StatusCode, resp.Header.Get("X-ZP-Egress-Error"))
	}
}

func TestAuthorizationDroppedCrossOriginOnly(t *testing.T) {
	a, b := redirector(t), redirector(t)
	defer a.Close()
	defer b.Close()
	_, base, _ := startEgress(t, allowAll)
	h := map[string]string{"Authorization": "Bearer k", "X-Api-Key": "kept"}
	same := send(t, base, "GET", a.URL+"/r/302/2", nil, h)
	readAll(t, same)
	if same.Header.Get("X-Auth") != "Bearer k" {
		t.Errorf("same origin: Authorization %q", same.Header.Get("X-Auth"))
	}
	cross := send(t, base, "GET", a.URL+"/r/302/1?to="+b.URL, nil, h)
	readAll(t, cross)
	if cross.Header.Get("X-Auth") != "" || cross.Header.Get("X-ZP-Egress-URL") != b.URL+"/final" {
		t.Errorf("cross origin: Authorization %q url %q", cross.Header.Get("X-Auth"), cross.Header.Get("X-ZP-Egress-URL"))
	}
	// Another port on the same host is another origin.
	host := strings.Replace(b.URL, "127.0.0.1", "b.test", 1)
	byName := send(t, base, "GET", a.URL+"/r/307/1?to="+host, nil, h)
	readAll(t, byName)
	if byName.Header.Get("X-Auth") != "" {
		t.Errorf("by name: Authorization %q", byName.Header.Get("X-Auth"))
	}
}

func TestRedirectLimit(t *testing.T) {
	ts := redirector(t)
	defer ts.Close()
	_, base, rec := startEgress(t, allowAll)
	ok := send(t, base, "GET", ts.URL+"/r/302/20", nil, nil)
	if readAll(t, ok); ok.StatusCode != 200 {
		t.Fatalf("20 redirects: %d", ok.StatusCode)
	}
	rec.wait(t, 21)
	over := send(t, base, "GET", ts.URL+"/r/302/21", nil, nil)
	body := readAll(t, over)
	if over.StatusCode != 502 || over.Header.Get("X-ZP-Egress-Error") != "EGRESS_REDIRECTS" ||
		!strings.Contains(body, `"code":"EGRESS_REDIRECTS"`) {
		t.Fatalf("21 redirects: %d %q %s", over.StatusCode, over.Header.Get("X-ZP-Egress-Error"), body)
	}
	if recs := rec.wait(t, 21); recs[20].Status != 302 {
		t.Fatalf("each hop is recorded: %+v", recs[20])
	}
}

func TestRedirectManualAndError(t *testing.T) {
	ts := redirector(t)
	defer ts.Close()
	_, base, rec := startEgress(t, allowAll)
	m := send(t, base, "GET", ts.URL+"/r/301/1", nil, map[string]string{"X-ZP-Egress-Redirect": "manual"})
	readAll(t, m)
	if m.StatusCode != 301 || m.Header.Get("Location") != "/final" || m.Header.Get("X-ZP-Egress-Redirected") != "" ||
		m.Header.Get("X-ZP-Egress-URL") != ts.URL+"/r/301/1" {
		t.Fatalf("manual: %d %v", m.StatusCode, m.Header)
	}
	rec.wait(t, 1)
	e := send(t, base, "GET", ts.URL+"/r/307/1", nil, map[string]string{"X-ZP-Egress-Redirect": "error"})
	readAll(t, e)
	if e.StatusCode != 502 || e.Header.Get("X-ZP-Egress-Error") != "EGRESS_REDIRECT" {
		t.Fatalf("error: %d %v", e.StatusCode, e.Header)
	}
	if r := rec.wait(t, 1)[0]; r.Outcome != "error" || r.Error != "EGRESS_REDIRECT" || r.Status != 307 {
		t.Fatalf("record %+v", r)
	}
}

func expectError(t *testing.T, resp *http.Response, status int, code string) {
	t.Helper()
	body := readAll(t, resp)
	var j struct{ Code, Message string }
	if err := json.Unmarshal([]byte(body), &j); err != nil {
		t.Fatalf("body %q: %v", body, err)
	}
	if resp.StatusCode != status || resp.Header.Get("X-ZP-Egress-Error") != code || j.Code != code || j.Message == "" {
		t.Fatalf("got %d %q %+v, want %d %s", resp.StatusCode, resp.Header.Get("X-ZP-Egress-Error"), j, status, code)
	}
}

func TestConnectionErrors(t *testing.T) {
	_, base, rec := startEgress(t, allowAll)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := ln.Addr().String()
	_ = ln.Close()
	expectError(t, send(t, base, "GET", "http://"+closed+"/", nil, nil), 502, "ECONNREFUSED")
	if r := rec.wait(t, 1)[0]; r.Outcome != "error" || r.Error != "ECONNREFUSED" || r.Status != 0 {
		t.Fatalf("record %+v", r)
	}
	expectError(t, send(t, base, "GET", "http://nowhere.invalid/", nil, nil), 502, "ENOTFOUND")
	rec.wait(t, 1)
	tlsTS := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("an untrusted server was sent the request")
	}))
	tlsTS.Config.ErrorLog = nil
	defer tlsTS.Close()
	expectError(t, send(t, base, "GET", tlsTS.URL+"/", nil, nil), 502, "CERT_INVALID")
	if r := rec.wait(t, 1)[0]; r.Scheme != "https" || r.Error != "CERT_INVALID" {
		t.Fatalf("record %+v", r)
	}
}

func TestConnectTimeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer ts.Close()
	s, base, rec := startEgress(t, allowAll)
	s.connectTimeout = time.Nanosecond // the limit is over before the connect starts
	expectError(t, send(t, base, "GET", ts.URL+"/", nil, nil), 502, "ETIMEDOUT")
	if r := rec.wait(t, 1)[0]; r.Error != "ETIMEDOUT" {
		t.Fatalf("record %+v", r)
	}
}

func TestPublicModeRefusesANameWithAnyPrivateAddress(t *testing.T) {
	s, base, _ := startEgress(t, allowAll)
	s.lookup = func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("203.0.113.7"), net.ParseIP("10.0.0.1")}, nil
	}
	// Refused before any connect: 203.0.113.7 (TEST-NET-3) is never dialled.
	expectError(t, send(t, base, "GET", "http://mixed.test/", nil, map[string]string{"X-ZP-Egress-Public": "1"}),
		502, "EGRESS_PRIVATE")
}

func TestBadRequests(t *testing.T) {
	_, base, rec := startEgress(t, allowAll)
	for name, h := range map[string]map[string]string{
		"relative":     {"X-ZP-Egress-URL": "/just/a/path"},
		"ftp":          {"X-ZP-Egress-URL": "ftp://a.test/"},
		"userinfo":     {"X-ZP-Egress-URL": "http://u:p@a.test/"},
		"user only":    {"X-ZP-Egress-URL": "http://u@a.test/"},
		"no host":      {"X-ZP-Egress-URL": "http:///x"},
		"bad port":     {"X-ZP-Egress-URL": "http://a.test:99999/"},
		"bad redirect": {"X-ZP-Egress-Redirect": "sometimes"},
	} {
		target := "http://a.test/"
		if v, ok := h["X-ZP-Egress-URL"]; ok {
			target = v
		}
		resp := send(t, base, "GET", target, nil, h)
		t.Run(name, func(t *testing.T) { expectError(t, resp, 400, "EGRESS_BAD_REQUEST") })
	}
	rec.none(t, 50*time.Millisecond)
}

func TestPublicMode(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("public mode reached a loopback target")
	}))
	defer ts.Close()
	// Enforce with an empty list: public mode does not consult the list, so the refusal is the address, not the list.
	_, base, rec := startEgress(t, Policy{Mode: ModeEnforce})
	pub := map[string]string{"X-ZP-Egress-Public": "1"}
	expectError(t, send(t, base, "GET", ts.URL+"/", nil, pub), 502, "EGRESS_PRIVATE")
	expectError(t, send(t, base, "GET", "http://[::1]:"+portOf(t, ts.URL)+"/", nil, pub), 502, "EGRESS_PRIVATE")
	// A name that resolves to a private address is refused at connect.
	expectError(t, send(t, base, "GET", "http://rebind.test:"+portOf(t, ts.URL)+"/", nil, pub), 502, "EGRESS_PRIVATE")
	for _, r := range rec.wait(t, 3) {
		if r.Outcome != "error" || r.Error != "EGRESS_PRIVATE" {
			t.Errorf("record %+v", r)
		}
	}
}

func TestPublicModeRefusesARedirectToPrivate(t *testing.T) {
	// The first hop is reachable only through a test dialer that pretends 127.0.0.1 is public; the redirect's name
	// resolves to a private address and is refused at connect.
	ts := redirector(t)
	defer ts.Close()
	s, base, rec := startEgress(t, allowAll)
	s.public.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if strings.HasPrefix(addr, "first.test:") {
			return (&net.Dialer{}).DialContext(ctx, network, "127.0.0.1"+strings.TrimPrefix(addr, "first.test"))
		}
		return s.dial(ctx, network, addr, true)
	}
	port := portOf(t, ts.URL)
	expectError(t, send(t, base, "GET", "http://first.test:"+port+"/r/302/1?to=http://second.test:"+port, nil,
		map[string]string{"X-ZP-Egress-Public": "1"}), 502, "EGRESS_PRIVATE")
	recs := rec.wait(t, 2)
	if recs[0].Status != 302 || recs[1].Host != "second.test" || recs[1].Error != "EGRESS_PRIVATE" {
		t.Fatalf("records %+v", recs)
	}
}

func TestAllowListEnforceAndReport(t *testing.T) {
	hits := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path == "/hop" {
			http.Redirect(w, r, strings.Replace(r.URL.Query().Get("to"), "X", r.Host[strings.LastIndex(r.Host, ":")+1:], 1), 302)
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer ts.Close()
	port := portOf(t, ts.URL)
	s, base, rec := startEgress(t, Policy{Mode: ModeEnforce, Allow: []Rule{{Host: "listed.test"}}})
	expectError(t, send(t, base, "GET", "http://other.test:"+port+"/", nil, nil), 502, "EGRESS_REFUSED")
	if r := rec.wait(t, 1)[0]; r.Outcome != "refused" || r.Error != "EGRESS_REFUSED" || r.Host != "other.test" {
		t.Fatalf("record %+v", r)
	}
	if hits != 0 {
		t.Fatal("a refused host was reached")
	}
	// Each hop is checked: a listed host redirecting off the list is refused at the second hop.
	expectError(t, send(t, base, "GET", "http://listed.test:"+port+"/hop?to=http://other.test:X/", nil, nil), 502,
		"EGRESS_REFUSED")
	if recs := rec.wait(t, 2); recs[0].Outcome != "ok" || recs[0].Status != 302 || recs[1].Outcome != "refused" {
		t.Fatalf("records %+v", recs)
	}
	// Report mode: the miss is recorded and the call goes on (and gets its own record).
	s.SetPolicy(Policy{Mode: ModeReport, Allow: []Rule{{Host: "listed.test"}}})
	resp := send(t, base, "GET", "http://other.test:"+port+"/", nil, nil)
	if body := readAll(t, resp); body != "ok" {
		t.Fatalf("report mode: %d %q", resp.StatusCode, body)
	}
	recs := rec.wait(t, 2)
	if recs[0].Outcome != "would-refuse" || recs[0].Error != "EGRESS_REFUSED" || recs[1].Outcome != "ok" ||
		recs[1].Status != 200 {
		t.Fatalf("records %+v", recs)
	}
}

func TestCheck(t *testing.T) {
	_, base, rec := startEgress(t, Policy{Mode: ModeReport, Allow: []Rule{{Host: "listed.test", Port: 443}}})
	check := func(target string, h map[string]string) *http.Response {
		return send(t, strings.TrimSuffix(base, "/")+"/check", "POST", target, strings.NewReader("never sent"), h)
	}
	ok := check("https://listed.test/v1?k=s", map[string]string{"X-ZP-Egress-Method": "put"})
	if readAll(t, ok); ok.StatusCode != 204 {
		t.Fatalf("listed: %d", ok.StatusCode)
	}
	if r := rec.wait(t, 1)[0]; r.Outcome != "checked" || r.Method != "PUT" || r.Host != "listed.test" ||
		r.Port != 443 || r.Purpose != "connectors:test" || r.Status != 0 {
		t.Fatalf("record %+v", r)
	}
	expectError(t, check("http://listed.test/", nil), 403, "EGRESS_REFUSED") // port 80 is not listed
	if r := rec.wait(t, 1)[0]; r.Outcome != "would-refuse" || r.Method != "GET" {
		t.Fatalf("record %+v", r)
	}
	expectError(t, check("http://10.0.0.1/", map[string]string{"X-ZP-Egress-Public": "1"}), 403, "EGRESS_PRIVATE")
	if r := rec.wait(t, 1)[0]; r.Outcome != "would-refuse" || r.Error != "EGRESS_PRIVATE" {
		t.Fatalf("record %+v", r)
	}
	// Public mode does not consult the list; a name is only known at connect, so it checks out.
	pub := check("http://anything.test/", map[string]string{"X-ZP-Egress-Public": "1"})
	if readAll(t, pub); pub.StatusCode != 204 {
		t.Fatalf("public name: %d", pub.StatusCode)
	}
	rec.wait(t, 1)
	get, _ := sendErr(context.Background(), strings.TrimSuffix(base, "/")+"/check", "GET", "http://listed.test/", nil, nil)
	expectError(t, get, 405, "EGRESS_BAD_REQUEST")
}

func TestCallerAbortCancelsTheTarget(t *testing.T) {
	gone := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "data: 1\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(gone)
	}))
	defer ts.Close()
	_, base, rec := startEgress(t, allowAll)
	ctx, cancel := context.WithCancel(context.Background())
	resp, err := sendErr(ctx, base, "GET", ts.URL+"/", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bufio.NewReader(resp.Body).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-gone:
	case <-time.After(5 * time.Second):
		t.Fatal("the target request was not cancelled")
	}
	if r := rec.wait(t, 1)[0]; r.Outcome != "error" || r.Error != "ABORTED" || r.Status != 200 {
		t.Fatalf("record %+v", r)
	}
}

func TestStreamedRequestBody(t *testing.T) {
	got := make(chan string, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- string(b)
		_, _ = io.WriteString(w, "ok")
	}))
	defer ts.Close()
	_, base, rec := startEgress(t, allowAll)
	payload := bytes.Repeat([]byte("0123456789"), 100_000) // 1 MB, chunked
	resp := send(t, base, "PUT", ts.URL+"/", io.MultiReader(bytes.NewReader(payload)), nil)
	readAll(t, resp)
	if b := <-got; b != string(payload) {
		t.Fatalf("target got %d bytes", len(b))
	}
	if r := rec.wait(t, 1)[0]; r.BytesOut != int64(len(payload)) {
		t.Fatalf("bytesOut %d", r.BytesOut)
	}
}

func TestPolicyChangesUnderLoad(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ts.Close()
	s, base, rec := startEgress(t, allowAll)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			s.SetPolicy(Policy{Mode: ModeReport, Allow: []Rule{{Host: "127.0.0.1"}}})
			s.SetToken(testToken)
		}
	}()
	for i := 0; i < 20; i++ {
		readAll(t, send(t, base, "GET", ts.URL+"/", nil, nil))
	}
	<-done
	rec.wait(t, 20)
}
