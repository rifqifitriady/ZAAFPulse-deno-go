package front

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
