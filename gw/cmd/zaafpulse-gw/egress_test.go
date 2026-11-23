package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/egress"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

// egressGateway runs the gateway through the handshake with the given config.push extra (`,"egress":{...}` or "")
// and returns the ready body and a channel of every later line it writes.
func egressGateway(t *testing.T, extra string) (map[string]any, chan string) {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	stderr := &syncBuffer{}
	code := make(chan int, 1)
	// The outbox folder is made before the cleanup below is registered, so its removal runs after the gateway has
	// exited: cleanups run last first, and on Windows the outbox's open .lock cannot be removed.
	dir := t.TempDir()
	go func() {
		code <- run([]string{"--outbox", dir, "--health-ms", "50"}, inR, outW, stderr)
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

// TestEgressPolicyGetter: the engine's own calls are checked against the endpoint's allow-list, none without one.
func TestEgressPolicyGetter(t *testing.T) {
	g := &gateway{}
	if g.egressPolicy() != nil {
		t.Fatal("a policy without an endpoint")
	}
	srv := egress.New(nil, nil)
	srv.SetPolicy(egress.Policy{Mode: egress.ModeEnforce, Allow: []egress.Rule{{Host: "api.example.com"}}})
	g.egressNow.Store(srv)
	if p := g.egressPolicy(); p == nil || p.Mode != egress.ModeEnforce || !p.Allows("api.example.com", 443) {
		t.Fatalf("policy %+v", p)
	}
	srv.SetPolicy(egress.Policy{Mode: egress.ModeReport})
	if p := g.egressPolicy(); p.Mode != egress.ModeReport {
		t.Fatalf("a pushed policy is not seen: %+v", p)
	}
}

// TestRecordEgressDropsAnInvalidRecord: a record that breaks egress.event's schema is never stored (the control plane
// would refuse it on every replay and close the channel); each cause is logged once.
func TestRecordEgressDropsAnInvalidRecord(t *testing.T) {
	valid := egress.Record{TS: 1, Purpose: "gateway", Method: "GET", Scheme: "https", Host: "api.example.com",
		Port: 443, Status: 200, Outcome: "ok", MS: 1, Via: "engine"}
	for name, mut := range map[string]func(*egress.Record){
		"port 0":      func(r *egress.Record) { r.Port = 0 },
		"port 99999":  func(r *egress.Record) { r.Port = 99999 },
		"no host":     func(r *egress.Record) { r.Host = "" },
		"no purpose":  func(r *egress.Record) { r.Purpose = "" },
		"no method":   func(r *egress.Record) { r.Method = "" },
		"scheme":      func(r *egress.Record) { r.Scheme = "ftp" },
		"status 99":   func(r *egress.Record) { r.Status = 99 },
		"status 1000": func(r *egress.Record) { r.Status = 1000 },
		"outcome":     func(r *egress.Record) { r.Outcome = "maybe" },
		"via":         func(r *egress.Record) { r.Via = "carrier pigeon" },
		"bytes":       func(r *egress.Record) { r.BytesIn = -1 },
		"ms":          func(r *egress.Record) { r.MS = -1 },
	} {
		r := valid
		mut(&r)
		ev := ipc.EgressEvent{TS: r.TS, Purpose: r.Purpose, Method: r.Method, Scheme: r.Scheme, Host: r.Host,
			Port: r.Port, Status: r.Status, Outcome: r.Outcome, BytesOut: r.BytesOut, BytesIn: r.BytesIn, MS: r.MS,
			Via: r.Via}
		if invalidEgress(&ev) == "" {
			t.Errorf("%s: passed", name)
		}
	}
	ev := ipc.EgressEvent{TS: 1, Purpose: "gateway", Method: "GET", Scheme: "https", Host: "h", Port: 443, Outcome: "ok",
		Via: "engine"}
	if why := invalidEgress(&ev); why != "" {
		t.Fatalf("a valid record (no status) refused: %s", why)
	}
	// Dropped before the outbox (the gateway here has none: storing would panic), logged once per cause.
	var buf bytes.Buffer
	g := &gateway{log: log.New(&buf, "", 0)}
	bad := valid
	bad.Port = 0
	g.recordEgress(bad)
	g.recordEgress(bad)
	bad.Host = ""
	g.recordEgress(bad) // the port is checked first: the same cause
	bad.Port = 443
	g.recordEgress(bad)
	if n := strings.Count(buf.String(), "dropped"); n != 2 {
		t.Fatalf("logged %d times:\n%s", n, buf.String())
	}
}
