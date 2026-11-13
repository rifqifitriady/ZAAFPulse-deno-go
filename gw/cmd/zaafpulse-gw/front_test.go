package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const frontToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// syncBuffer is an io.Writer several goroutines may write to.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// handshake runs the gateway with the given front section (JSON) and drives the handshake up to ready (or to the exit).
// It returns the exit code channel, stderr, and a function that closes stdin.
func handshake(t *testing.T, frontJSON string) (chan int, *syncBuffer, func()) {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	stderr := &syncBuffer{}
	code := make(chan int, 1)
	go func() {
		code <- run([]string{"--outbox", t.TempDir(), "--health-ms", "50"}, inR, outW, stderr)
		_ = outW.Close()
		_ = inR.Close() // a write still pending fails instead of blocking
	}()
	br := bufio.NewReader(outR)
	if line, err := br.ReadString('\n'); err != nil || !strings.Contains(line, `"type":"hello"`) {
		t.Fatalf("no hello: %q %v", line, err)
	}
	// The writes run on their own: a gateway that exits early stops reading, and a pipe write would block.
	lines := []string{
		`{"v":1,"type":"hello.ok","seq":1,"ts":0,"re":"h1","body":{"protocol":1,"cpVersion":"test"}}`,
		`{"v":1,"type":"config.push","seq":2,"ts":0,"body":{"gen":1,"gateway":{"providers":[],"routes":[],` +
			`"guardrails":{"scope":"external","secrets":"off","pii":"off","injection":"off"},"defaults":{"model":"",` +
			`"plannerModel":"","codeModel":""},"tokenSaver":false,"modelMap":[]},"keys":[],"pricing":{},"front":` + frontJSON + `}}`,
		`{"v":1,"type":"secrets.handoff","seq":3,"ts":0,"body":{"gen":1,"providers":[],"mask":[],"frontToken":"` + frontToken + `"}}`,
		`{"v":1,"type":"budget.snapshot","seq":4,"ts":0,"body":{"at":0,"budgets":[],"keyCenter":{}}}`,
	}
	go func() {
		for _, l := range lines {
			if _, err := io.WriteString(inW, l+"\n"); err != nil {
				return
			}
		}
	}()
	ready := make(chan struct{})
	go func() {
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			if strings.Contains(line, `"type":"ready"`) {
				close(ready)
			}
		}
	}()
	select {
	case <-ready:
	case c := <-code:
		code <- c
	case <-time.After(10 * time.Second):
		t.Fatalf("no ready; stderr %s", stderr)
	}
	stop := func() {
		_ = inW.Close()
	}
	t.Cleanup(stop)
	return code, stderr, stop
}

func frontJSON(port, upstream int, engines string) string {
	return fmt.Sprintf(`{"listen":{"host":"127.0.0.1","port":%d},"upstream":{"port":%d},"engines":%s,"shadow":{}}`,
		port, upstream, engines)
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestFrontPortInUseExits4(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	code, stderr, _ := handshake(t, frontJSON(held.Addr().(*net.TCPAddr).Port, 1, `{"chat":"js"}`))
	select {
	case c := <-code:
		if c != 4 || !strings.Contains(stderr.String(), "fatal: port ") {
			t.Fatalf("exit %d, stderr %s", c, stderr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no exit")
	}
}

// P4b: only chat may be on the go engine; any other class there is a config this gateway cannot apply.
func TestFrontGoEngineIsRefusedWithExit2(t *testing.T) {
	code, stderr, _ := handshake(t, frontJSON(freePort(t), 1, `{"chat":"go","messages":"go"}`))
	select {
	case c := <-code:
		if c != 2 || !strings.Contains(stderr.String(), `fatal: config.push: route class "messages" is on engine "go"`) {
			t.Fatalf("exit %d, stderr %s", c, stderr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no exit")
	}
}

// Ready means the main port is open: a request through it reaches the upstream with the client header, the token
// never reaches the log, and closing stdin closes the port.
func TestFrontServesOnceReadyAndStopsWithStdin(t *testing.T) {
	client := make(chan string, 1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		client <- r.Header.Get("X-Zp-Client")
		_, _ = io.WriteString(w, "ok")
	}))
	defer up.Close()
	port := freePort(t)
	code, stderr, stop := handshake(t, frontJSON(port, up.Listener.Addr().(*net.TCPAddr).Port, `{"chat":"js"}`))
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/login.html", port))
	if err != nil {
		t.Fatalf("%v; stderr %s", err, stderr)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || string(b) != "ok" {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	if h := <-client; !strings.HasPrefix(h, "127.0.0.1 ") || len(strings.Fields(h)) != 4 {
		t.Fatalf("client header %q", h)
	}
	if strings.Contains(stderr.String(), frontToken) {
		t.Fatal("the front token reached the log")
	}
	stop()
	select {
	case c := <-code:
		if c != 0 {
			t.Fatalf("exit %d", c)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no exit after stdin closed")
	}
	if _, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second); err == nil {
		t.Fatal("the main port is still open after the exit")
	}
}
