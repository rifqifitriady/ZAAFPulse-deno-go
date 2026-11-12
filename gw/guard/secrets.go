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
func isLiveSecret(label string, s []uint16, m []int) bool {
	if label == "Private key" {
		return true
	}
	if label == "Database URL with password" {
		pw := s[m[2]:m[3]]
		return !rePlaceholderPassword.Test(pw) && !reMasked3.Test(pw)
	}
	v := s[m[0]:m[1]]
	if rePlaceholder.Test(v) {
		return false
	}
	if label == "AWS access key" && reAWSExample.Test(v) {
		return false
	}
	tail := v[min(8, max(0, len(v)-16)):]
	distinct := map[rune]bool{}
	for _, r := range utf16.Decode(tail) { // new Set(string) iterates code points
		distinct[r] = true
	}
	return len(distinct) >= 10
}

// SecretSpans is lib/signals.js secretSpans: the live secrets in text, overlapping ones merged (first label kept).
func SecretSpans(text string) []Span { return secretSpans(jsre.U16(text)) }

func secretSpans(s []uint16) []Span {
	if !reSecretHint.Test(s) {
		return nil
	}
	var found []Span
	for _, p := range secretPatterns {
		for last := 0; ; {
			m := p.re.Exec(s, last)
			if m == nil {
				break
			}
			last = m[1]
			if !isLiveSecret(p.label, s, m) {
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
