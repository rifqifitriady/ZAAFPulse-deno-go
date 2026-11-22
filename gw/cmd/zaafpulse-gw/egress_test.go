package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/egress"
)

// egressGateway runs the gateway through the handshake with the given config.push extra (`,"egress":{...}` or "")
// and returns the ready body and a channel of every later line it writes.
func egressGateway(t *testing.T, extra string) (map[string]any, chan string) {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	stderr := &syncBuffer{}
	code := make(chan int, 1)
	go func() {
		code <- run([]string{"--outbox", t.TempDir(), "--health-ms", "50"}, inR, outW, stderr)
		_ = outW.Close()
		_ = inR.Close()
	}()
	t.Cleanup(func() {
		_ = inW.Close()
		select {
		case <-code:
		case <-time.After(10 * time.Second):
			t.Error("the gateway did not exit after stdin closed")
		}
	})
	br := bufio.NewReader(outR)
	if line, err := br.ReadString('\n'); err != nil || !strings.Contains(line, `"type":"hello"`) {
		t.Fatalf("no hello: %q %v", line, err)
	}
	lines := []string{
		`{"v":1,"type":"hello.ok","seq":1,"ts":0,"re":"h1","body":{"protocol":1,"cpVersion":"test"}}`,
		`{"v":1,"type":"config.push","seq":2,"ts":0,"body":{"gen":1,"gateway":{"providers":[],"routes":[],` +
			`"guardrails":{"scope":"external","secrets":"off","pii":"off","injection":"off"},"defaults":{"model":"",` +
			`"plannerModel":"","codeModel":""},"tokenSaver":false,"modelMap":[]},"keys":[],"pricing":{}` + extra + `}}`,
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
	out := make(chan string, 256)
	go func() {
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				close(out)
				return
			}
			select {
			case out <- line:
			default: // health lines nobody reads
			}
		}
	}()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case line, ok := <-out:
			if !ok {
				t.Fatalf("no ready; stderr %s", stderr)
			}
			if !strings.Contains(line, `"type":"ready"`) {
				continue
			}
			var msg struct {
				Body map[string]any `json:"body"`
			}
			if err := json.Unmarshal([]byte(line), &msg); err != nil {
				t.Fatal(err)
			}
			return msg.Body, out
		case c := <-code:
			t.Fatalf("exit %d before ready; stderr %s", c, stderr)
		case <-deadline:
			t.Fatalf("no ready; stderr %s", stderr)
		}
	}
}

func readyPort(t *testing.T, ready map[string]any) int {
	t.Helper()
	e, ok := ready["egress"].(map[string]any)
	if !ok {
		t.Fatalf("ready has no egress: %v", ready)
	}
	return int(e["port"].(float64))
}

// Without an egress section there is no endpoint and ready names none.
func TestReadyHasNoEgressWithoutTheSection(t *testing.T) {
	ready, _ := egressGateway(t, "")
	if _, ok := ready["egress"]; ok {
		t.Fatalf("ready %v", ready)
	}
}

// With the section, ready names the endpoint's port; a call through it reaches the target and its record reaches the
// control plane as an egress.event from the outbox (with a seq).
func TestEgressEndpointOpensBeforeReadyAndRecordsThroughTheOutbox(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "hello")
	}))
	defer target.Close()
	ready, out := egressGateway(t, `,"egress":{"allow":[],"allowList":"report"}`)
	port := readyPort(t, ready)
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", port), nil)
	req.Header.Set("X-ZP-Egress-Token", egress.DeriveToken(frontToken))
	req.Header.Set("X-ZP-Egress-URL", target.URL+"/x")
	req.Header.Set("X-ZP-Egress-Purpose", "connectors:test")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || string(b) != "hello" {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	// The allow-list is empty in report mode: a would-refuse record, then the call's own.
	want := map[string]bool{"would-refuse": false, "ok": false}
	deadline := time.After(10 * time.Second)
	for !want["would-refuse"] || !want["ok"] {
		select {
		case line := <-out:
			if !strings.Contains(line, `"type":"egress.event"`) {
				continue
			}
			var msg struct {
				Seq  int64             `json:"seq"`
				Body ipcEgressEventRaw `json:"body"`
			}
			if err := json.Unmarshal([]byte(line), &msg); err != nil {
				t.Fatal(err)
			}
			if msg.Seq < 1 || msg.Body.Purpose != "connectors:test" || msg.Body.Via != "proxy" || msg.Body.Host != "127.0.0.1" {
				t.Fatalf("event %s", line)
			}
			if msg.Body.Outcome == "ok" && (msg.Body.Status != 200 || msg.Body.BytesIn != 5) {
				t.Fatalf("event %s", line)
			}
			want[msg.Body.Outcome] = true
		case <-deadline:
			t.Fatalf("records seen %v", want)
		}
	}
}

type ipcEgressEventRaw struct {
	Purpose, Host, Outcome, Via string
	Status                      int
	BytesIn                     int64 `json:"bytesIn"`
}

// A wrong token gets no reply: the connection is closed.
func TestEgressEndpointClosesOnABadToken(t *testing.T) {
	ready, _ := egressGateway(t, `,"egress":{"allow":[],"allowList":"report"}`)
	port := readyPort(t, ready)
	c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = io.WriteString(c, "GET / HTTP/1.1\r\nHost: x\r\nX-ZP-Egress-Token: wrong\r\nX-ZP-Egress-URL: http://127.0.0.1:1/\r\n\r\n")
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	b, err := io.ReadAll(c)
	if len(b) != 0 || err != nil {
		t.Fatalf("got %q, %v", b, err)
	}
}
