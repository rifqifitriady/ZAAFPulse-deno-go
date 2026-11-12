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

// MaskPII masks emails, IP addresses and phone numbers and counts each kind masked (a kind appears once seen).
func MaskPII(text string) (string, map[string]int) {
	s, counts := maskPII(jsre.U16(text), nil)
	return jsre.Str(s), counts
}

func maskPII(s []uint16, b *jsre.Budget) ([]uint16, map[string]int) {
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
	s = reEmail.ReplaceAllB(s, seen("email", func([]uint16) []uint16 { return email }), b)
	s = reIPV4.ReplaceAllB(s, seen("ip", func(m []uint16) []uint16 {
		if keepIP[jsre.Str(m)] {
			return m
		}
		return ip
	}), b)
	s = reIPV6.ReplaceAllB(s, seen("ip", func(m []uint16) []uint16 { return ipv6(m, b) }), b)
	s = rePhone.ReplaceAllB(s, seen("phone", func(m []uint16) []uint16 { return phone(m, b) }), b)
	return s, counts
}
