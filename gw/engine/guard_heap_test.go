//go:build !race

package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

// heapPeak runs f and returns the highest HeapAlloc above the level before it (sampled every millisecond) and the
// garbage collections it took.
func heapPeak(f func()) (uint64, uint32) {
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	base, gcs := ms.HeapAlloc, ms.NumGC
	var peak uint64
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		var m runtime.MemStats
		for {
			runtime.ReadMemStats(&m)
			if m.HeapAlloc > base && m.HeapAlloc-base > peak {
				peak = m.HeapAlloc - base
			}
			select {
			case <-stop:
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()
	f()
	close(stop)
	<-done
	runtime.ReadMemStats(&ms)
	return peak, ms.NumGC - gcs
}

// bigBody is a chat body whose one user message is text repeated up to n bytes.
func bigBody(t *testing.T, text string, n int) []byte {
	t.Helper()
	var b strings.Builder
	for b.Len() < n {
		b.WriteString(text)
	}
	msg, _ := json.Marshal(b.String()[:n])
	return []byte(`{"model":"oa/m1","messages":[{"role":"user","content":` + string(msg) + `}]}`)
}

// A guarded 20 MB chat call through PlanChat and Guard (the default policy: secrets redact, injection log) stays under
// the checks' 256 MB plus the engine's own trees of the body; the run is logged with and without the gateway's
// default 64 MiB soft memory limit, for owner question 2 (P4e Task 7). An adversarial 20 MB body is refused by its
// step budget (an overrun), not by memory.
func TestGuardedBigBody(t *testing.T) {
	corpus, err := os.ReadFile(filepath.Join("..", "guard", "testdata", "bench-corpus.txt"))
	if err != nil {
		t.Fatal(err)
	}
	s, r := basicState(t)
	s.Gateway.Guardrails.Secrets, s.Gateway.Guardrails.Injection = "redact", "log"
	const n = 20 << 20
	for _, limit := range []int64{0, 64 << 20, 128 << 20, 256 << 20} {
		r.Body = bigBody(t, string(corpus), n)
		old := debug.SetMemoryLimit(-1)
		if limit > 0 {
			debug.SetMemoryLimit(limit)
		}
		var p Plan
		var ref *Answer
		start := time.Now()
		peak, gcs := heapPeak(func() {
			p = s.PlanChat(r, T)
			ref = s.Guard(&p, 0)
		})
		d := time.Since(start)
		debug.SetMemoryLimit(old)
		if p.Handoff != "" || ref != nil {
			t.Fatalf("handoff %q or refused %v", p.Handoff, ref)
		}
		t.Logf("20 MB corpus body, soft limit %d MiB: peak extra heap %d MB, %d GCs, %s", limit>>20, peak>>20, gcs, d)
		if bound := uint64(256<<20 + 3*n); peak > bound {
			t.Errorf("peak extra heap %d MB, bound %d MB", peak>>20, bound>>20)
		}
	}
	// All three checks on, as the guard's own adversarial tests run it (TestOneBudgetPerCall).
	s.Gateway.Guardrails.PII = "redact"
	r.Body = bigBody(t, "the ai said you ", n)
	var p Plan
	var ref *Answer
	start := time.Now()
	peak, gcs := heapPeak(func() {
		p = s.PlanChat(r, T)
		ref = s.Guard(&p, 0)
	})
	t.Logf("20 MB adversarial body: peak extra heap %d MB, %d GCs, %s, overrun %q", peak>>20, gcs, time.Since(start), p.Overrun)
	if ref == nil || ref.Status != 400 || p.Overrun == "" {
		t.Fatalf("not refused by an overrun: %+v %q", ref, p.Overrun)
	}
}
