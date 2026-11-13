package guard

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"
)

// The guard benchmark of the P4e entry conditions: p50 and p99 of one CheckBody call on real text (benchcorpus_test.go)
// at 32 KB, 128 KB and 1 MB, all three checks on, against the absolute targets and against the JS on the same machine
// (scripts/bench-guard.js, the same slices). It runs only when asked:
//
//	node scripts/bench-guard.js --json /tmp/js.json
//	ZP_GUARD_BENCH=1 ZP_GUARD_BENCH_JS=/tmp/js.json go -C gw test -run TestGuardBench -count=1 -v ./guard/
//
// A size that misses a target is measured again, up to three attempts, and passes when one attempt meets every target
// (CI runners are noisy). Without ZP_GUARD_BENCH_JS only the absolute targets are checked.

// benchPlan: the sizes, the slices per size and the rounds over them; scripts/bench-guard.js uses the same plan.
var benchPlan = []struct{ size, offsets, rounds int }{
	{32 << 10, 100, 3},
	{128 << 10, 50, 3},
	{1 << 20, 20, 2},
}

// benchTargets: p50 and p99 bounds per size (0: none), and the bound on Go over JS.
var benchTargets = map[int]struct{ p50, p99 time.Duration }{
	32 << 10:  {5 * time.Millisecond, 15 * time.Millisecond},
	128 << 10: {30 * time.Millisecond, 60 * time.Millisecond},
	1 << 20:   {0, 300 * time.Millisecond},
}

const benchMaxRatio = 3.0

// pct is the nearest-rank percentile of sorted samples.
func pct(sorted []time.Duration, p float64) time.Duration {
	i := int(float64(len(sorted))*p+0.999999999) - 1
	return sorted[max(0, min(i, len(sorted)-1))]
}

type jsBench struct {
	Runtime string `json:"runtime"`
	Sizes   map[string]struct {
		P50 float64 `json:"p50"`
		P99 float64 `json:"p99"`
	} `json:"sizes"`
}

func measure(t *testing.T, c []byte, size, k, rounds int) (p50, p99 time.Duration) {
	t.Helper()
	offs := benchOffsets(len(c), size, k)
	bodies := make([]jsv.Value, len(offs))
	for i, o := range offs {
		bodies[i] = benchBody(benchText(c, o, size))
	}
	check := func(b jsv.Value) {
		if _, err := CheckBody(b, benchPolicy, "openai-chat"); err != nil {
			t.Fatalf("%d bytes: %v", size, err)
		}
	}
	for _, b := range bodies[:min(5, len(bodies))] {
		check(b) // warm-up, as in the JS
	}
	var ds []time.Duration
	for range rounds {
		for _, b := range bodies {
			start := time.Now()
			check(b)
			ds = append(ds, time.Since(start))
		}
	}
	slices.Sort(ds)
	return pct(ds, 0.5), pct(ds, 0.99)
}

func ms(d time.Duration) float64 { return float64(d) / 1e6 }

func TestGuardBench(t *testing.T) {
	if os.Getenv("ZP_GUARD_BENCH") == "" {
		t.Skip("set ZP_GUARD_BENCH=1 to run the guard benchmark")
	}
	var js *jsBench
	if f := os.Getenv("ZP_GUARD_BENCH_JS"); f != "" {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		js = &jsBench{}
		if err := json.Unmarshal(raw, js); err != nil {
			t.Fatal(err)
		}
	}
	c := loadBenchCorpus(t)
	var table strings.Builder
	table.WriteString("| Size | Go p50 | Go p99 | JS p50 | JS p99 | Go/JS p50 | Go/JS p99 | Targets | Attempts |\n")
	table.WriteString("|---|---|---|---|---|---|---|---|---|\n")
	for _, pl := range benchPlan {
		tg := benchTargets[pl.size]
		var jp50, jp99 float64
		if js != nil {
			s, ok := js.Sizes[strconv.Itoa(pl.size)]
			if !ok {
				t.Fatalf("no JS numbers for %d bytes", pl.size)
			}
			jp50, jp99 = s.P50, s.P99
		}
		var p50, p99 time.Duration
		var misses []string
		attempt := 0
		for attempt < 3 {
			attempt++
			p50, p99 = measure(t, c, pl.size, pl.offsets, pl.rounds)
			misses = nil
			if tg.p50 > 0 && p50 > tg.p50 {
				misses = append(misses, fmt.Sprintf("p50 %.2f ms > %v", ms(p50), tg.p50))
			}
			if tg.p99 > 0 && p99 > tg.p99 {
				misses = append(misses, fmt.Sprintf("p99 %.2f ms > %v", ms(p99), tg.p99))
			}
			if js != nil && ms(p50) > benchMaxRatio*jp50 {
				misses = append(misses, fmt.Sprintf("p50 %.1fx the JS", ms(p50)/jp50))
			}
			if js != nil && ms(p99) > benchMaxRatio*jp99 {
				misses = append(misses, fmt.Sprintf("p99 %.1fx the JS", ms(p99)/jp99))
			}
			if len(misses) == 0 {
				break
			}
			t.Logf("%d KB, attempt %d: %s", pl.size>>10, attempt, strings.Join(misses, "; "))
		}
		verdict := "met"
		if len(misses) > 0 {
			verdict = "MISSED: " + strings.Join(misses, "; ")
			t.Errorf("%d KB: %s", pl.size>>10, strings.Join(misses, "; "))
		}
		ratio := func(g time.Duration, j float64) string {
			if js == nil {
				return "-"
			}
			return fmt.Sprintf("%.2fx", ms(g)/j)
		}
		fmt.Fprintf(&table, "| %d KB | %.2f ms | %.2f ms | %.2f ms | %.2f ms | %s | %s | %s | %d |\n", pl.size>>10,
			ms(p50), ms(p99), jp50, jp99, ratio(p50, jp50), ratio(p99, jp99), verdict, attempt)
	}
	t.Log("\n" + table.String())
	if f := os.Getenv("GITHUB_STEP_SUMMARY"); f != "" {
		if out, err := os.OpenFile(f, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644); err == nil {
			runtime := "JS not measured"
			if js != nil {
				runtime = "JS: " + js.Runtime
			}
			fmt.Fprintf(out, "### Guard benchmark (%s)\n\n%s\n", runtime, table.String())
			out.Close()
		}
	}
}

// TestBenchCorpusNeverOverruns is P4e entry condition 3: the real-text corpus never reaches a limit (step budget or
// deadline) at any size up to MAX_BODY (20 MB), so the limits cause no false block. The step budget does not depend on
// the machine and is checked on every run; the 2 s deadline does, so it is checked only in the benchmark job
// (ZP_GUARD_BENCH, the Ubuntu runner the targets are set on) and widened elsewhere.
func TestBenchCorpusNeverOverruns(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("20 MB inputs: not under -short or the race detector")
	}
	if os.Getenv("ZP_GUARD_BENCH") == "" {
		saved := requestDeadline
		t.Cleanup(func() { requestDeadline = saved })
		requestDeadline = time.Hour
	}
	c := loadBenchCorpus(t)
	for _, size := range []int{2 << 20, 5 << 20, 20 << 20} {
		for _, off := range benchOffsets(len(c), min(size, len(c)), 3) {
			body := benchBody(benchText(c, off, size))
			start := time.Now()
			var err error
			peak := peakHeapDuring(func() { _, err = CheckBody(body, benchPolicy, "openai-chat") })
			d := time.Since(start)
			if peak > maxHeapGrowth {
				t.Errorf("%d MB at %d: heap grew by %d MB, bound %d MB", size>>20, off, peak>>20, maxHeapGrowth>>20)
			}
			var b *Block
			if errors.As(err, &b) && b.Overrun != "" {
				t.Errorf("%d MB at %d: false block after %v: %v", size>>20, off, d, err)
			} else if err != nil {
				t.Errorf("%d MB at %d: %v", size>>20, off, err)
			}
			t.Logf("%d MB at %d: %v, %d MB heap", size>>20, off, d.Round(time.Millisecond), peak>>20)
		}
	}
}
