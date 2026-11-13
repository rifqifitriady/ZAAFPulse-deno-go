package jsre

import (
	"strings"
	"testing"
	"time"
)

// I4 (P4e): a budget's deadline stops a run that has steps left, and the clock is read only every few thousand steps.
func TestDeadline(t *testing.T) {
	re := MustCompile(`(?:a|aa)*b`, "")
	s := U16(strings.Repeat("a", 40))
	now := time.Unix(1000, 0)
	reads := 0
	b := &Budget{Limit: 1 << 62, Deadline: now.Add(time.Second), Now: func() time.Time {
		reads++
		now = now.Add(100 * time.Millisecond)
		return now
	}}
	err := b.Run(func() { re.ExecB(s, 0, b) })
	if err != ErrDeadline {
		t.Fatalf("got %v after %d steps", err, b.Steps)
	}
	if reads != 10 || b.Steps < 9*clockEvery {
		t.Fatalf("%d clock reads in %d steps", reads, b.Steps)
	}
	// A zero deadline is no deadline.
	b = &Budget{Limit: 1 << 40, Now: func() time.Time { t.Fatal("clock read without a deadline"); return now }}
	if err := b.Run(func() { re.ExecB(U16(strings.Repeat("a", 22)), 0, b) }); err != nil {
		t.Fatal(err)
	}
}

// The scan for a start position counts too: a long text with no candidate still spends steps, so a deadline holds
// over it.
func TestSkippedPositionsCount(t *testing.T) {
	re := MustCompile(`zq`, "")
	s := U16(strings.Repeat("a", 1<<20))
	b := &Budget{Limit: 1 << 62}
	if m := re.ExecB(s, 0, b); m != nil {
		t.Fatal("matched")
	}
	if b.Steps < 1<<20/skipWeight {
		t.Fatalf("%d steps for 1M skipped positions", b.Steps)
	}
	b = &Budget{Limit: 1 << 19 / skipWeight}
	if err := b.Run(func() { re.ExecB(s, 0, b) }); err != ErrBudget {
		t.Fatalf("got %v", err)
	}
}

// A pattern that begins with ^ (no m flag) can match only at 0: no other start is tried.
func TestAnchoredTriesOnlyTheStart(t *testing.T) {
	re := MustCompile(`^(?:data|x):\w+$`, "i")
	s := U16(strings.Repeat("data:", 1<<18))
	b := &Budget{Limit: 1 << 62}
	if re.ExecB(s, 0, b) != nil || b.Steps > 1<<20 {
		t.Fatalf("%d steps", b.Steps)
	}
	if m := re.Exec(U16("DATA:abc"), 0); m == nil || m[1] != 8 {
		t.Fatalf("got %v", m)
	}
	if re.Exec(U16("DATA:abc"), 1) != nil {
		t.Fatal("matched past the start")
	}
}
