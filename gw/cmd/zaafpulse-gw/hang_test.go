package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var seqRe = regexp.MustCompile(`"seq":(\d+)`)

// hungGateway starts a gateway with chat on go against provider, hands it config, secrets and a snapshot, waits for
// ready, and returns the run and the main port. Nothing reads stdout afterwards until the test does: a hung control
// plane (P4e Decision 21a).
func hungGateway(t *testing.T, provider string, snapshot string, extra ...string) (*gwRun, int) {
	t.Helper()
	cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "control plane")
	}))
	t.Cleanup(cp.Close)
	port := freePort(t)
	r := drive(t, extra...)
	r.next(t, "hello")
	r.send(`{"v":1,"type":"hello.ok","seq":1,"ts":0,"re":"h1","body":{"protocol":1,"cpVersion":"test"}}`)
	cfg := chatConfig(2, 1, port, portOf(cp.URL), provider+"/v1", "zp_hangTestSecret")
	cfg = strings.Replace(cfg, `"rpm":1,`, `"rpm":1000000,`, 1)
	r.send(cfg)
	r.send(secretsLine(3, 1))
	r.send(snapshot)
	r.next(t, "ready")
	return r, port
}

func snapshotLine(seq int, at int64, budgets string) string {
	return fmt.Sprintf(`{"v":1,"type":"budget.snapshot","seq":%d,"ts":%d,"body":{"at":%d,"budgets":[%s],"keyCenter":{}}}`,
		seq, at, at, budgets)
}

// hangClient is the tests' client: at most 20 connections to the gateway, each kept and reused. The property under test
// is that no call waits for the control plane, not how an OS takes a burst of new dials: with
// http.DefaultClient (2 idle connections per host) 300 calls opened up to 300 connections at once, and Windows' listen
// backlog refused some of them (connectex: actively refused; seen on windows-latest and windows-11-arm).
var hangClient = &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
	MaxConnsPerHost: 20, MaxIdleConns: 20, MaxIdleConnsPerHost: 20, IdleConnTimeout: time.Minute}}

// postChat is one call through the main port; a transport error is status -1 with its text (it runs in goroutines:
// no t.Fatal).
func postChat(t *testing.T, port int, model string) (int, string) {
	req, _ := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", port),
		strings.NewReader(`{"model":"`+model+`","messages":[]}`))
	req.Header.Set("Authorization", "Bearer zp_hangTestSecret")
	resp, err := hangClient.Do(req)
	if err != nil {
		return -1, err.Error()
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode, string(b)
}

// callAll makes n calls to model through 20 clients and returns the answers by status and the first failures' text.
func callAll(t *testing.T, port, n int, model string) (map[int]int, []string) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	codes := map[int]int{}
	var bad []string
	next := make(chan struct{}, n)
	for range n {
		next <- struct{}{}
	}
	close(next)
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range next {
				code, body := postChat(t, port, model)
				mu.Lock()
				codes[code]++
				if code != 200 && len(bad) < 3 {
					bad = append(bad, fmt.Sprintf("%d %s", code, body))
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return codes, bad
}

// TestCallsAnswerWhileTheControlPlaneHangs: 300 go-served calls answer while nothing reads the gateway's stdout; once
// it reads again, the 300 usage reports arrive in seq order, each once.
func TestCallsAnswerWhileTheControlPlaneHangs(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"id":"c1","usage":{"prompt_tokens":10,"completion_tokens":5}}`)
	}))
	defer provider.Close()
	now := time.Now().UnixMilli()
	// --stale-ms keeps the snapshot fresh for this task (Task 5 runs the stale variant).
	r, port := hungGateway(t, provider.URL, snapshotLine(4, now, ""), "--stale-ms", "600000")
	const n = 300
	done := make(chan struct{})
	var codes map[int]int
	var bad []string
	go func() {
		codes, bad = callAll(t, port, n, "pv/llama-9")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("calls still waiting after 10 s while the control plane did not read; stderr %s", r.stderr)
	}
	if codes[200] != n {
		t.Fatalf("answers %v, want %d × 200: %v", codes, n, bad)
	}
	// The control plane reads again: every report, in order, once.
	var last int64
	for i := 0; i < n; i++ {
		l := r.next(t, "usage.report")
		m := seqRe.FindStringSubmatch(l)
		seq, _ := strconv.ParseInt(m[1], 10, 64)
		if last != 0 && seq != last+1 {
			t.Fatalf("report seq %d after %d", seq, last)
		}
		last = seq
	}
}

// TestHungControlPlaneBudgetsFailOpen is S7's kill-IPC test at the process level (all four OS): the control plane
// stops reading and sending; once the snapshot is older than --stale-ms, metered calls still answer, a model whose
// budget the last snapshot shows exceeded is still refused, and when the control plane reads again every call's report
// is there, in order, once, marked "unchecked".
func TestHungControlPlaneBudgetsFailOpen(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"id":"c1","usage":{"prompt_tokens":10,"completion_tokens":5}}`)
	}))
	defer provider.Close()
	now := time.Now().UnixMilli()
	exceeded := `{"id":"b1","name":"Spent","scope":{"dim":"model","value":"llama-x"},"period":"month","currency":"USD",` +
		`"measure":"list","enforce":"block","amount":1,"from":"","to":"","state":"exceeded","spentUSD":2,` +
		`"amountUSD":1,"reason":null}`
	r, port := hungGateway(t, provider.URL, snapshotLine(4, now, exceeded), "--stale-ms", "300")
	time.Sleep(400 * time.Millisecond) // the snapshot is now stale, and no new one comes
	const n = 300
	start := time.Now()
	codes, bad := callAll(t, port, n, "pv/llama-9")
	if codes[200] != n || time.Since(start) > 10*time.Second {
		t.Fatalf("answers %v (%v) in %s; stderr %s", codes, bad, time.Since(start), r.stderr)
	}
	if code, body := postChat(t, port, "pv/llama-x"); code != 402 ||
		body != `{"error":{"message":"budget \"Spent\" is used up for this month","type":"insufficient_quota"}}` {
		t.Fatalf("the exceeded budget: %d %s", code, body)
	}
	var last int64
	for i := 0; i < n; i++ {
		l := r.next(t, "usage.report")
		if !strings.Contains(l, `"budget":"unchecked"`) {
			t.Fatalf("report %d not marked: %s", i, l)
		}
		seq, _ := strconv.ParseInt(seqRe.FindStringSubmatch(l)[1], 10, 64)
		if last != 0 && seq != last+1 {
			t.Fatalf("report seq %d after %d", seq, last)
		}
		last = seq
		if strings.Contains(l, `"model":"pv/llama-x"`) {
			t.Fatalf("the refused call wrote a line: %s", l)
		}
	}
	// The 402 wrote no usage line (allowedTargets throws before any record): nothing more comes (review m6). A line
	// for it would follow the 300, since it was answered after them.
	deadline := time.After(time.Second)
	for {
		select {
		case l, ok := <-r.lines:
			if !ok {
				return
			}
			if strings.Contains(l, `"type":"usage.report"`) {
				t.Fatalf("a usage line after the %d calls: %s", n, l)
			}
		case <-deadline:
			return
		}
	}
}
