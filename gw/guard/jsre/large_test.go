package jsre

import (
	"strings"
	"testing"
	"time"
)

// C1 (review): a repeat of a group must not recurse once per iteration. Each of these used to overflow the goroutine
// stack (a fatal error no recover() catches) on about 1 MB; at 20 MB each must finish within the bound.
func TestLargeInputsDoNotRecurse(t *testing.T) {
	if testing.Short() {
		t.Skip("20 MB inputs")
	}
	const mb20 = 20 << 20
	cases := []struct {
		name, src, flags string
		text             string
	}{
		{"split letters", `\b(?:[a-z][-_.*·•\s]){3,}[a-z]\b`, "gi", "token " + strings.Repeat("a ", mb20/2)},
		{"ipv6", `(?<![\w:.])(?:(?:[0-9a-f]{1,4}:){7}[0-9a-f]{1,4}|(?:[0-9a-f]{1,4}(?::[0-9a-f]{1,4})*)?::(?:[0-9a-f]{1,4}(?::[0-9a-f]{1,4})*)?)(?![\w:])`, "gi", " " + strings.Repeat("1:", mb20/2) + "x"},
		{"email", `(?<![a-z0-9._%+-])[a-z0-9._%+-]+@[a-z0-9-]+(?:\.[a-z0-9-]+)*\.(?!(?:png|jpe?g|svg|webp|gif|ico|bmp|avif|tiff?)\b)[a-z]{2,}\b(?!\.[a-z0-9])`, "gi", " x@" + strings.Repeat("a.", mb20/2) + "1"},
	}
	for _, c := range cases {
		re := MustCompile(c.src, c.flags)
		s := U16(c.text)
		start := time.Now()
		n := len(re.FindAll(s))
		d := time.Since(start)
		t.Logf("%s: %d matches in %v", c.name, n, d)
		if d > 60*time.Second {
			t.Errorf("%s: %v over the 60 s bound", c.name, d)
		}
	}
}

// I1: a step budget stops a run and reports it instead of running on.
func TestBudget(t *testing.T) {
	re := MustCompile(`(?:a|aa)*b`, "")
	s := U16(strings.Repeat("a", 40))
	b := &Budget{Limit: 100000}
	err := b.Run(func() { re.ExecB(s, 0, b) })
	if err != ErrBudget {
		t.Fatalf("got %v after %d steps", err, b.Steps)
	}
	b = &Budget{Limit: 1 << 40}
	if err := b.Run(func() { re.ExecB(U16("aab"), 0, b) }); err != nil || b.Steps == 0 {
		t.Fatalf("err %v, steps %d", err, b.Steps)
	}
}
