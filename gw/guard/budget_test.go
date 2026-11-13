package guard

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"
)

// I4 (review): the budget was per string and per check, so a 20 MB body split into 1,000 crafted strings ran 17–18 s
// with no overrun. One budget and a 2 s deadline per call now bound it: each body below finishes or blocks within
// 2.5 s of wall time and, where the OS reports it, 2.5 s of process CPU.
func TestOneBudgetPerCall(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("20 MB inputs: not under -short or the race detector")
	}
	const strs, each = 1000, 20 << 10
	for _, a := range adversarialInputs {
		unit := a.build(each)
		msgs := jsv.Arr()
		for range strs {
			msgs.Push(jsv.NewObject().Set("role", "user").Set("content", unit))
		}
		body := jsv.NewObject().Set("model", "m").Set("messages", msgs)
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
			t.Errorf("%s: heap grew by %d MB, bound %d MB", a.name, peak>>20, maxHeapGrowth>>20)
		}
		var b *Block
		outcome := "ok"
		if errors.As(err, &b) {
			outcome = "blocked " + b.Overrun
		} else if err != nil {
			t.Errorf("%s: %v", a.name, err)
		}
		t.Logf("%s ×%d: %v wall, %v CPU, %d MB heap (%s)", a.name, strs, wall.Round(time.Millisecond),
			cpu.Round(time.Millisecond), peak>>20, outcome)
		if wall > 2500*time.Millisecond {
			t.Errorf("%s: %v wall, bound 2.5 s", a.name, wall)
		}
		if cpu0 >= 0 && cpu > 2500*time.Millisecond {
			t.Errorf("%s: %v CPU, bound 2.5 s", a.name, cpu)
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
