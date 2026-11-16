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

// postChat is one call through the main port; a transport error is status -1 (it runs in goroutines: no t.Fatal).
func postChat(t *testing.T, port int, model string) (int, string) {
	req, _ := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", port),
		strings.NewReader(`{"model":"`+model+`","messages":[]}`))
	req.Header.Set("Authorization", "Bearer zp_hangTestSecret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return -1, err.Error()
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode, string(b)
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
	var wg sync.WaitGroup
	var mu sync.Mutex
	codes := map[int]int{}
	go func() {
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				code, _ := postChat(t, port, "pv/llama-9")
				mu.Lock()
				codes[code]++
				mu.Unlock()
			}()
			if i%20 == 19 {
				time.Sleep(5 * time.Millisecond)
			}
		}
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("calls still waiting after 10 s while the control plane did not read (answered %v); stderr %s",
			codes, r.stderr)
	}
	if codes[200] != n {
		t.Fatalf("answers %v, want %d × 200", codes, n)
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
	var wg sync.WaitGroup
	var bad []string
	var mu sync.Mutex
	start := time.Now()
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if code, body := postChat(t, port, "pv/llama-9"); code != 200 {
				mu.Lock()
				bad = append(bad, fmt.Sprintf("%d %s", code, body))
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(bad) > 0 || time.Since(start) > 10*time.Second {
		t.Fatalf("%d calls failed (%v) or took %s; stderr %s", len(bad), bad[:min(3, len(bad))], time.Since(start), r.stderr)
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
	}
}
