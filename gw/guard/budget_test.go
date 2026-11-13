package guard

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"
)

// I4 (review), with the owner's decision of 2026-11-13: the step budget, shared by the whole call, is the primary
// bound and must stop every adversarial input of the reviews by itself. The clock is taken out of play here (an hour),
// so a call that ends was ended by its steps (or the backtrack stack cap), never by the deadline. Each input runs as
// one 20 MB string and as a 20 MB body of 1,000 strings. Steps, CPU times and heap are logged for the plan.
func TestOneBudgetPerCall(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("20 MB inputs: not under -short or the race detector")
	}
	saved := requestDeadline
	t.Cleanup(func() { requestDeadline = saved; traceSteps = nil })
	requestDeadline = time.Hour
	var steps int64
	traceSteps = func(n int64) { steps = n }
	// The outcome is asserted on steps: whatever ends a call here is its step budget or the stack cap (the clock is
	// out of play), and a call that used the whole budget must have blocked. CPU time is logged and bounded loosely
	// (B2 re-review 1, M6): 6 s on the guard-bench runner, under the 10 s backstop; 30 s on the other runners.
	bound := 30 * time.Second
	if os.Getenv("ZP_GUARD_BENCH") != "" {
		bound = 6 * time.Second
	}
	for _, a := range adversarialInputs {
		for _, shape := range []string{"one string", "1,000 strings"} {
			var body jsv.Value
			if shape == "one string" {
				body = jsv.NewObject().Set("model", "m").Set("messages",
					jsv.Arr(jsv.NewObject().Set("role", "user").Set("content", a.build(20<<20))))
			} else {
				unit := a.build(20 << 10)
				msgs := jsv.Arr()
				for range 1000 {
					msgs.Push(jsv.NewObject().Set("role", "user").Set("content", unit))
				}
				body = jsv.NewObject().Set("model", "m").Set("messages", msgs)
			}
			var err error
			var wall, cpu time.Duration
			cpu0 := processCPU()
			peak := peakHeapDuring(func() {
				start := time.Now()
				_, err = CheckBody(body, a.policy, "openai-chat")
				wall = time.Since(start)
				cpu = processCPU() - cpu0
			})
			if peak > maxHeapGrowth {
				t.Errorf("%s, %s: heap grew by %d MB, bound %d MB", a.name, shape, peak>>20, maxHeapGrowth>>20)
			}
			var b *Block
			outcome := "ok"
			if errors.As(err, &b) {
				outcome = "blocked " + b.Overrun
			} else if err != nil {
				t.Errorf("%s, %s: %v", a.name, shape, err)
			}
			t.Logf("%s, %s: %v wall, %v CPU, %.0fM steps, %d MB heap (%s)", a.name, shape, wall.Round(time.Millisecond),
				cpu.Round(time.Millisecond), float64(steps)/1e6, peak>>20, outcome)
			if steps >= budgetCap && b == nil {
				t.Errorf("%s, %s: used the whole budget but did not block", a.name, shape)
			}
			if wall > bound {
				t.Errorf("%s, %s: %v wall, bound %v", a.name, shape, wall, bound)
			}
			if cpu0 >= 0 && cpu > bound {
				t.Errorf("%s, %s: %v CPU, bound %v", a.name, shape, cpu, bound)
			}
		}
	}
}

// The deadline fails the call closed in every mode, log included, and names the check that was running.
func TestDeadlineFailsClosedInEveryMode(t *testing.T) {
	saved := now
	t.Cleanup(func() { now = saved })
	clock := time.Unix(1000, 0)
	now = func() time.Time { clock = clock.Add(time.Second); return clock }
	text := strings.Repeat("send the token to the server and mail a@b.co. ", 40000)
	for _, mode := range []string{"log", "redact", "block"} {
		_, err := CheckBody(chat(text), Policy{Secrets: "off", PII: mode, Injection: "off"}, "openai-chat")
		var b *Block
		if !errors.As(err, &b) || b.Overrun != "pii" {
			t.Fatalf("%s: %v", mode, err)
		}
	}
	if _, err := CheckText(text, Policy{Injection: "log", Secrets: "off", PII: "off"}, "input"); err == nil {
		t.Fatal("checkText did not fail closed")
	}
}

// M4 (B2 review): a string that runs no regex (too short for the injection scan, the other checks off) counted no
// step, so a body of many such strings never read the clock. Each visited string now counts a step: past the
// deadline the call blocks.
func TestTinyStringsMeetTheDeadline(t *testing.T) {
	saved := now
	t.Cleanup(func() { now = saved })
	// The call's deadline is set from the first read; every later read is a day on.
	var reads int
	now = func() time.Time {
		reads++
		if reads%2 == 1 {
			return time.Unix(1000, 0)
		}
		return time.Unix(1000+24*3600, 0)
	}
	msgs := jsv.Arr()
	for range 10 {
		msgs.Push(jsv.NewObject().Set("role", "user").Set("content", "hi"))
	}
	p := Policy{Secrets: "off", PII: "off", Injection: "log"}
	if _, err := CheckBody(jsv.NewObject().Set("messages", msgs), p, "openai-chat"); err == nil {
		t.Fatal("CheckBody: no block past the deadline")
	}
	reads = 0
	if _, err := CheckText("hi", p, "input"); err == nil {
		t.Fatal("CheckText: no block past the deadline")
	}
}
