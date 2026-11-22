package egress

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const testToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// recorder collects records.
type recorder struct {
	mu   sync.Mutex
	recs []Record
	ch   chan Record
}

func newRecorder() *recorder { return &recorder{ch: make(chan Record, 256)} }

func (r *recorder) add(rec Record) {
	r.mu.Lock()
	r.recs = append(r.recs, rec)
	r.mu.Unlock()
	r.ch <- rec
}

// wait returns the next n records (waiting up to 5 s).
func (r *recorder) wait(t *testing.T, n int) []Record {
	t.Helper()
	var out []Record
	for len(out) < n {
		select {
		case rec := <-r.ch:
			out = append(out, rec)
		case <-time.After(5 * time.Second):
			t.Fatalf("waited for %d records, got %d: %+v", n, len(out), out)
		}
	}
	return out
}

// none fails when a record arrives within d.
func (r *recorder) none(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case rec := <-r.ch:
		t.Fatalf("unexpected record %+v", rec)
	case <-time.After(d):
	}
}

// fakeLookup is the tests' resolver: names under .test are 127.0.0.1, .invalid does not exist, anything else fails
// (no test reaches real DNS).
func fakeLookup(_ context.Context, host string) ([]net.IP, error) {
	switch {
	case strings.HasSuffix(host, ".test"):
		return []net.IP{net.IPv4(127, 0, 0, 1)}, nil
	case strings.HasSuffix(host, ".invalid"):
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	return nil, errors.New("test resolver: " + host + " is not a test name")
}

// startEgress starts an endpoint on a loopback port with the fake resolver, a client without proxy and the given
// policy; it returns the server, its base URL and the recorder.
func startEgress(t *testing.T, pol Policy) (*Server, string, *recorder) {
	t.Helper()
	rec := newRecorder()
	s := New(&http.Client{Transport: &http.Transport{}}, rec.add)
	s.lookup = fakeLookup
	s.SetToken(testToken)
	s.SetPolicy(pol)
	port, err := s.Listen()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(time.Second) })
	return s, "http://127.0.0.1:" + strconv.Itoa(port) + "/", rec
}

// portOf is the port of an httptest URL.
func portOf(t *testing.T, raw string) string {
	t.Helper()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(strings.TrimPrefix(raw, "http://"), "https://"))
	if err != nil {
		t.Fatal(err)
	}
	return port
}

// allowAll is an enforce-mode policy that lists the test names and the loopback address.
var allowAll = Policy{Mode: ModeEnforce, Allow: []Rule{{Host: "*.test"}, {Host: "*.invalid"}, {Host: "127.0.0.1"}}}

// send makes one request through the endpoint.
func send(t *testing.T, base, method, target string, body io.Reader, h map[string]string) *http.Response {
	t.Helper()
	resp, err := sendErr(context.Background(), base, method, target, body, h)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func sendErr(ctx context.Context, base, method, target string, body io.Reader, h map[string]string) (*http.Response,
	error) {
	req, err := http.NewRequestWithContext(ctx, method, base, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-ZP-Egress-Token", testToken)
	req.Header.Set("X-ZP-Egress-URL", target)
	req.Header.Set("X-ZP-Egress-Purpose", "connectors:test")
	for k, v := range h {
		req.Header.Set(k, v)
	}
	// The client's own transport: no proxy, no gzip of its own (it must not decode for the endpoint).
	return (&http.Client{Transport: &http.Transport{DisableCompression: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
