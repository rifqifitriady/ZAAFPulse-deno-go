package guard

import (
	"strings"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/guard/jsre"
)

// The personal-data masking of lib/evals/pii.js maskPii, with the guardrail's options: home false (home-folder paths
// are left alone) and counts. Only that path is ported.

var keepIP = map[string]bool{"127.0.0.1": true, "0.0.0.0": true, "::1": true, "::": true}

var (
	reHasDigit = jsre.MustCompile(`\d`, "")
	reDateHead = jsre.MustCompile(`^\d{4}-\d{2}-\d{2}\b`, "")
)

// ipv6: a "::" form needs a digit and either three groups or one long group.
func ipv6(m []uint16, b *jsre.Budget) []uint16 {
	s := jsre.Str(m)
	if keepIP[strings.ToLower(s)] || !reHasDigit.TestB(m, b) {
		return m
	}
	if !strings.Contains(s, "::") {
		return jsre.U16("‹ip›")
	}
	n, long := 0, false
	for _, g := range strings.Split(s, ":") {
		if g != "" {
			n++
			long = long || len(g) >= 3
		}
	}
	if n >= 3 || long {
		return jsre.U16("‹ip›")
	}
	return m
}

func phone(m []uint16, b *jsre.Budget) []uint16 {
	if reDateHead.TestB(m, b) {
		return m
	}
	n := 0
	for _, c := range m {
		if c >= '0' && c <= '9' {
			n++
		}
	}
	if n >= 9 && n <= 15 {
		return jsre.U16("‹phone›")
	}
	return m
}

// sameSlice reports whether a and b are the same slice of the same storage.
func sameSlice(a, b []uint16) bool {
	return len(a) == len(b) && cap(a) == cap(b) && (cap(a) == 0 || &a[:1][0] == &b[:1][0])
}

func equalU16(a, b []uint16) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// MaskPII masks emails, IP addresses and phone numbers and counts each kind masked (a kind appears once seen). It runs
// under b like ScanInjection.
func MaskPII(text string, b *jsre.Budget) (masked string, counts map[string]int, err error) {
	u := jsre.U16(text)
	b = orBudget(b, len(u))
	var s []uint16
	if err = b.Run(func() { s, counts = maskPII(u, true, nil, b) }); err != nil {
		return "", nil, err
	}
	return jsre.Str(s), counts, nil
}

// maskPII masks s. With disposable the passes may write over s once they no longer read it, and over spare (which
// must not overlap s); otherwise s is never written. The result may be either storage.
func maskPII(s []uint16, disposable bool, spare []uint16, b *jsre.Budget) ([]uint16, map[string]int) {
	counts := map[string]int{}
	seen := func(kind string, f func(m []uint16) []uint16) func([]int) []uint16 {
		return func(c []int) []uint16 {
			m := s[c[0]:c[1]]
			out := f(m)
			if !equalU16(out, m) {
				counts[kind]++
			}
			return out
		}
	}
	email := jsre.U16("‹email›")
	ip := jsre.U16("‹ip›")
	// Each pass reads s and writes a new text only when it replaces something; the passes alternate between two
	// buffers, so a long text costs at most two copies, never one per pass; with disposable, s and spare are those two.
	in := s
	pass := func(re *jsre.Regexp, f func([]int) []uint16) {
		out := re.ReplaceAllInto(spare, s, f, b)
		if sameSlice(out, s) {
			return
		}
		if disposable || !sameSlice(s, in) {
			spare = s
		}
		s = out
	}
	pass(reEmail, seen("email", func([]uint16) []uint16 { return email }))
	pass(reIPV4, seen("ip", func(m []uint16) []uint16 {
		if keepIP[jsre.Str(m)] {
			return m
		}
		return ip
	}))
	pass(reIPV6, seen("ip", func(m []uint16) []uint16 { return ipv6(m, b) }))
	pass(rePhone, seen("phone", func(m []uint16) []uint16 { return phone(m, b) }))
	return s, counts
}
