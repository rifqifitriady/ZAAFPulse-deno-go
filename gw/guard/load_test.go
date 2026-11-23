package guard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// codeCorpus is n bytes of this repository's own code (lib/, lib/gateway/, gw/guard/, gw/translate/, scripts/; .js
// and .go, in sorted order, repeated as needed): real text with no injection hit, which takes the deobfuscation path.
func codeCorpus(t *testing.T, n int) string {
	t.Helper()
	var files []string
	for _, dir := range []string{"../../lib", "../../lib/gateway", ".", "../translate", "../../scripts"} {
		for _, ext := range []string{"*.js", "*.go"} {
			fs, err := filepath.Glob(filepath.Join(dir, ext))
			if err != nil {
				t.Fatal(err)
			}
			files = append(files, fs...)
		}
	}
	slices.Sort(files)
	var sb strings.Builder
	for sb.Len() < n {
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			sb.Write(b)
			sb.WriteByte('\n')
		}
	}
	return benchText([]byte(sb.String()), 0, n)
}

// securityText is n bytes of legitimate text dense in injection and secret terms, repeated from this repository's
// guardrail tests (tests: true) and/or lib/signals.js (signals: true; the detectors' own patterns and examples).
func securityText(t *testing.T, n int, tests, signals bool) string {
	t.Helper()
	var files []string
	if tests {
		fs, err := filepath.Glob("../../test/guardrails*.js")
		if err != nil || len(fs) == 0 {
			t.Fatalf("no guardrail tests: %v", err)
		}
		slices.Sort(fs)
		files = fs
	}
	if signals {
		files = append(files, "../../lib/signals.js")
	}
	var sb strings.Builder
	for sb.Len() < n {
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			sb.Write(b)
			sb.WriteByte('\n')
		}
	}
	return benchText([]byte(sb.String()), 0, n)
}

// csvNumbers is n bytes of numeric CSV, the heaviest legitimate text measured (the pii patterns try most digits).
func csvNumbers(n int) string {
	var sb strings.Builder
	for i := 0; sb.Len() < n; i++ {
		fmt.Fprintf(&sb, "%d,%d.%02d,2026-11-%02d,%d-%d-%d\n", i, i*7, i%100, i%28+1, i%1000, i%9999, i*13%99999)
	}
	return sb.String()[:n]
}

// I1 (B2 review, owner's decision of 2026-11-13): the step budget is the primary bound and does not depend on load,
// and the wall clock is a 10 s backstop. Legitimate 20 MB bodies (real code, the corpus, numeric CSV, and text dense in
// security terms: the guardrail tests and lib/signals.js) checked two at a time while GOMAXPROCS goroutines burn CPU
// next to them must never block. The step budget is checked on every
// run; the 10 s backstop is in play only in the guard-bench job (ZP_GUARD_BENCH), widened elsewhere.
func TestLegitimateBodiesUnderLoad(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("20 MB inputs: not under -short or the race detector")
	}
	if os.Getenv("ZP_GUARD_BENCH") == "" {
		saved := requestDeadline
		t.Cleanup(func() { requestDeadline = saved })
		requestDeadline = time.Hour
	}
	c := loadBenchCorpus(t)
	bodies := map[string]string{
		"code":            codeCorpus(t, 20<<20),
		"corpus":          benchText(c, 0, 20<<20),
		"csv":             csvNumbers(20 << 20),
		"security":        securityText(t, 20<<20, true, true),
		"guardrail tests": securityText(t, 20<<20, true, false),
		"signals.js":      securityText(t, 20<<20, false, true),
	}
	// Steps are deterministic: measured once, alone, with the headroom under the call's cap (B2 re-review 1, I2).
	var steps int64
	traceSteps = func(n int64) { steps = n }
	for _, name := range []string{"corpus", "code", "csv", "security", "guardrail tests", "signals.js"} {
		_, err := CheckBody(benchBody(bodies[name]), benchPolicy, "openai-chat")
		var b *Block
		if errors.As(err, &b) && b.Overrun != "" {
			t.Errorf("%s: false block at %.0fM steps: %v", name, float64(steps)/1e6, err)
		}
		t.Logf("%s: %.0fM steps, headroom %.2fx under the %dM cap", name, float64(steps)/1e6,
			float64(budgetCap)/float64(steps), budgetCap/1_000_000)
	}
	traceSteps = nil
	var stop atomic.Bool
	var burners sync.WaitGroup
	for range runtime.GOMAXPROCS(0) {
		burners.Add(1)
		go func() {
			defer burners.Done()
			x := 1
			for !stop.Load() {
				for i := range 100000 {
					x = x*31 + i
				}
			}
			_ = x
		}()
	}
	defer func() { stop.Store(true); burners.Wait() }()
	for name, text := range bodies {
		var wg sync.WaitGroup
		for k := range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				start := time.Now()
				_, err := CheckBody(benchBody(text), benchPolicy, "openai-chat")
				d := time.Since(start)
				var b *Block
				if errors.As(err, &b) && b.Overrun != "" {
					t.Errorf("%s #%d: false block after %v under load: %v", name, k, d, err)
				} else if err != nil {
					t.Errorf("%s #%d: %v", name, k, err)
				}
				t.Logf("%s #%d: %v under load", name, k, d.Round(time.Millisecond))
			}()
		}
		wg.Wait()
	}
}
