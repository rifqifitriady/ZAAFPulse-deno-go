package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// gwRun is a gateway driven over pipes: send writes control-plane lines, lines carries what it writes back.
type gwRun struct {
	code   chan int
	stderr *syncBuffer
	send   func(string)
	lines  chan string
}

const emptyGateway = `{"providers":[],"routes":[],"guardrails":{"scope":"external","secrets":"off","pii":"off",` +
	`"injection":"off"},"defaults":{"model":"","plannerModel":"","codeModel":""},"tokenSaver":false,"modelMap":[]}`

// configLine is a config.push of generation gen, with the front section front ("" for none).
func configLine(seq, gen int, front string) string {
	f := ""
	if front != "" {
		f = `,"front":` + front
	}
	return fmt.Sprintf(`{"v":1,"type":"config.push","seq":%d,"ts":0,"body":{"gen":%d,"gateway":%s,"keys":[],"pricing":{}%s}}`,
		seq, gen, emptyGateway, f)
}

func secretsLine(seq, gen int) string {
	return fmt.Sprintf(`{"v":1,"type":"secrets.handoff","seq":%d,"ts":0,"body":{"gen":%d,"providers":[],"mask":[],"frontToken":"%s"}}`,
		seq, gen, frontToken)
}

func drive(t *testing.T, extra ...string) *gwRun {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	r := &gwRun{code: make(chan int, 1), stderr: &syncBuffer{}, lines: make(chan string, 64)}
	dir := t.TempDir() // before the cleanup below, so it is removed after the gateway has exited
	go func() {
		r.code <- run(append([]string{"--outbox", dir, "--health-ms", "60000"}, extra...), inR, outW, r.stderr)
		_ = outW.Close()
		_ = inR.Close()
	}()
	go func() {
		br := bufio.NewReader(outR)
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				close(r.lines)
				return
			}
			r.lines <- line
		}
	}()
	writes := make(chan string, 64)
	go func() {
		for l := range writes {
			if _, err := io.WriteString(inW, l+"\n"); err != nil {
				return
			}
		}
	}()
	r.send = func(l string) { writes <- l }
	// Closing stdin ends the gateway; wait for it before t.TempDir's cleanup removes the outbox, whose lock file it holds
	// open (Windows refuses to remove an open file: P4b review I4).
	t.Cleanup(func() {
		close(writes)
		_ = inW.Close()
		select {
		case <-r.code:
		case <-time.After(10 * time.Second):
			t.Error("the gateway did not exit within 10 s of stdin closing")
		}
	})
	return r
}

// next waits for the next line of type typ and returns it.
func (r *gwRun) next(t *testing.T, typ string) string {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case l, ok := <-r.lines:
			if !ok {
				t.Fatalf("the gateway stopped before a %s; stderr %s", typ, r.stderr)
			}
			if strings.Contains(l, `"type":"`+typ+`"`) {
				return l
			}
		case <-deadline:
			t.Fatalf("no %s within 10 s; stderr %s", typ, r.stderr)
		}
	}
}

func (r *gwRun) ready(t *testing.T, front string) {
	t.Helper()
	r.next(t, "hello")
	r.send(`{"v":1,"type":"hello.ok","seq":1,"ts":0,"re":"h1","body":{"protocol":1,"cpVersion":"test"}}`)
	r.send(configLine(2, 1, front))
	r.send(secretsLine(3, 1))
	r.send(`{"v":1,"type":"budget.snapshot","seq":4,"ts":0,"body":{"at":0,"budgets":[],"keyCenter":{}}}`)
	r.next(t, "ready")
}

// A new generation is answered with a health that names it as soon as its config and secrets are both applied, so the
// control plane can wait for it (lib/ipc/supervisor.js synced) instead of the next health tick.
func TestHealthNamesAGenerationOnceItsSecretsAreApplied(t *testing.T) {
	r := drive(t)
	r.ready(t, "")
	r.send(configLine(5, 2, ""))
	r.send(secretsLine(6, 2))
	if h := r.next(t, "health"); !strings.Contains(h, `"gen":2`) {
		t.Fatalf("health %s", h)
	}
}

// The front section is fixed for a gateway's life (P4b entry condition 3): a gateway started without one opens no port
// when a later config.push carries one, and says that it applies after a restart.
func TestALateFrontSectionAppliesAfterARestart(t *testing.T) {
	r := drive(t)
	r.ready(t, "")
	port := freePort(t)
	r.send(configLine(5, 2, frontJSON(port, 1, `{"chat":"js"}`)))
	r.send(secretsLine(6, 2))
	r.next(t, "health")
	if !strings.Contains(r.stderr.String(), "config.push: a front section after the start applies after a restart") {
		t.Fatalf("stderr %s", r.stderr)
	}
	if c, err := dialQuick(port); err == nil {
		_ = c.Close()
		t.Fatal("a late front section opened the main port")
	}
}

func dialQuick(port int) (io.Closer, error) {
	return net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
}

// A health between a generation's config.push and its secrets.handoff names the generation before it (P4b review m2):
// synced() must not resolve before the gateway has the new credentials too.
func TestHealthNeverNamesAGenerationWithoutItsSecrets(t *testing.T) {
	r := drive(t, "--health-on-snapshot")
	r.ready(t, "")
	r.send(configLine(5, 2, ""))
	r.send(`{"v":1,"type":"budget.snapshot","seq":6,"ts":0,"body":{"at":0,"budgets":[],"keyCenter":{}}}`)
	if h := r.next(t, "health"); !strings.Contains(h, `"gen":1`) {
		t.Fatalf("health before the secrets: %s", h)
	}
	r.send(secretsLine(7, 2))
	if h := r.next(t, "health"); !strings.Contains(h, `"gen":2`) {
		t.Fatalf("health after the secrets: %s", h)
	}
}
