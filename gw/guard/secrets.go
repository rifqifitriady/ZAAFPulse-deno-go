package guard

import (
	"sort"
	"unicode/utf16"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/guard/jsre"
)

// Span is where one live secret sits (lib/signals.js secretSpans), in UTF-16 offsets. For a database URL only the
// password is the span; MatchStart is where the whole match begins.
type Span struct {
	Start, End int
	Label      string
	MatchStart int
}

var (
	reAWSExample = jsre.MustCompile(`EXAMPLE$`, "")
	reMasked3    = jsre.MustCompile(`[•*]{3}`, "")
)

// isLiveSecret: documentation placeholders and low-variety strings are not live keys.
func isLiveSecret(label string, s []uint16, m []int, b *jsre.Budget) bool {
	if label == "Private key" {
		return true
	}
	if label == "Database URL with password" {
		pw := s[m[2]:m[3]]
		return !rePlaceholderPassword.TestB(pw, b) && !reMasked3.TestB(pw, b)
	}
	v := s[m[0]:m[1]]
	if rePlaceholder.TestB(v, b) {
		return false
	}
	if label == "AWS access key" && reAWSExample.TestB(v, b) {
		return false
	}
	tail := v[min(8, max(0, len(v)-16)):]
	distinct := map[rune]bool{}
	for _, r := range utf16.Decode(tail) { // new Set(string) iterates code points
		distinct[r] = true
	}
	return len(distinct) >= 10
}

// SecretSpans is lib/signals.js secretSpans: the live secrets in text, overlapping ones merged (first label kept). It
// runs under b like ScanInjection.
func SecretSpans(text string, b *jsre.Budget) (spans []Span, err error) {
	u := jsre.U16(text)
	b = orBudget(b, len(u))
	err = b.Run(func() { spans = secretSpans(u, b) })
	return spans, err
}

func secretSpans(s []uint16, b *jsre.Budget) []Span {
	if !reSecretHint.TestB(s, b) {
		return nil
	}
	var found []Span
	for _, p := range secretPatterns {
		for last := 0; ; {
			m := p.re.ExecB(s, last, b)
			if m == nil {
				break
			}
			last = m[1]
			if !isLiveSecret(p.label, s, m, b) {
				continue
			}
			if p.label == "Database URL with password" {
				whole := s[m[0]:m[1]]
				needle := append(append([]uint16{':'}, s[m[2]:m[3]]...), '@')
				at := m[0] + indexOfSeq(whole, needle, indexOfSeq(whole, jsre.U16("://"), 0)+3) + 1
				found = append(found, Span{Start: at, End: at + m[3] - m[2], Label: p.label, MatchStart: m[0]})
			} else {
				found = append(found, Span{Start: m[0], End: m[1], Label: p.label, MatchStart: m[0]})
			}
		}
	}
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].Start != found[j].Start {
			return found[i].Start < found[j].Start
		}
		return found[i].End > found[j].End
	})
	var out []Span
	for _, f := range found {
		if n := len(out); n > 0 && f.Start < out[n-1].End {
			out[n-1].End = max(out[n-1].End, f.End)
			out[n-1].MatchStart = min(out[n-1].MatchStart, f.MatchStart)
		} else {
			out = append(out, f)
		}
	}
	return out
}

// indexOfSeq is s.indexOf(sub, from).
func indexOfSeq(s, sub []uint16, from int) int {
	for i := clamp(from, 0, len(s)); i+len(sub) <= len(s); i++ {
		ok := true
		for j := range sub {
			if s[i+j] != sub[j] {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}
