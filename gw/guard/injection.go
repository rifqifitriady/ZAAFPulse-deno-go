package guard

import (
	"sort"
	"strings"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/guard/jsre"
)

// The prompt-injection scan of lib/signals.js (scanInjection), on UTF-16 code units like the JS: every offset is a JS
// string index.

const (
	windowBefore = 260
	windowAfter  = 320
)

// Injection is the scanInjection result: the most severe category (id, label, at, match) and every category that
// matched. Label joins all of them with " + ".
type Injection struct {
	ID         string
	Label      string
	At         int
	Match      string
	Categories []string
	Labels     []string
}

type injHit struct {
	id, label  string
	at         int
	match      []uint16
	supporting bool
}

func clamp(i, lo, hi int) int {
	if i < lo {
		return lo
	}
	if i > hi {
		return hi
	}
	return i
}

// slice is String.prototype.slice(a, b) for non-negative a and b.
func slice(s []uint16, a, b int) []uint16 {
	a, b = clamp(a, 0, len(s)), clamp(b, 0, len(s))
	if a >= b {
		return nil
	}
	return s[a:b]
}

// lastIndexOf is s.lastIndexOf(c, from).
func lastIndexOf(s []uint16, c uint16, from int) int {
	for i := clamp(from, 0, len(s)-1); i >= 0; i-- {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// indexOf is s.indexOf(c, from).
func indexOf(s []uint16, c uint16, from int) int {
	for i := clamp(from, 0, len(s)); i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

func inDocContext(text []uint16, at, n int) bool {
	if reQuoteBefore.Test(slice(text, at-2, at)) {
		return true
	}
	ls := lastIndexOf(text, '\n', at) + 1
	le := indexOf(text, '\n', at+n)
	if le < 0 {
		le = len(text)
	}
	line := slice(text, ls, le)
	var prev []uint16
	if ls > 1 {
		prev = slice(text, lastIndexOf(text, '\n', ls-2)+1, ls-1)
	}
	around := append(append(append([]uint16{}, prev...), '\n'), line...)
	if k := n + 400; k < len(around) {
		around = around[len(around)-k:]
	}
	around = reMasked.ReplaceAll(around, func([]int) []uint16 { return []uint16{' '} })
	if reDocContext.Test(around) {
		return true
	}
	return reCodeLine.Test(line) || reStringItem.Test(line)
}

var reMasked = jsre.MustCompile(`\S+@\S+|https?:\/\/\S+`, "g")

// anchorWindows: spans worth matching, around each anchor, widened to line starts and merged when they overlap.
func anchorWindows(text []uint16) [][2]int {
	var spans [][2]int
	for _, re := range []*jsre.Regexp{reInjAnchor, reInjAnchorMark} {
		last := 0
		for {
			m := re.Exec(text, last)
			if m == nil {
				break
			}
			last = m[1]
			hi := max(0, m[0]-windowBefore)
			lo := max(0, m[0]-windowBefore-200)
			nl := -1
			for i := hi; i >= lo; i-- {
				if text[i] == '\n' {
					nl = i
					break
				}
			}
			from := hi
			if nl >= 0 {
				from = nl
			}
			to := min(len(text), m[1]+windowAfter)
			spans = append(spans, [2]int{from, to})
			last = max(last, to-200)
			if last > len(text) {
				break
			}
		}
	}
	sort.SliceStable(spans, func(i, j int) bool { return spans[i][0] < spans[j][0] })
	var out [][2]int
	for _, sp := range spans {
		if n := len(out); n > 0 && sp[0] <= out[n-1][1] {
			out[n-1][1] = max(out[n-1][1], sp[1])
		} else {
			out = append(out, sp)
		}
	}
	return out
}

func matchInjection(text []uint16, offsetOf func(int) int, windows [][2]int) []injHit {
	if len(windows) == 0 {
		return nil
	}
	var hits []injHit
	for _, p := range injection {
		var hit *injHit
		for _, w := range windows {
			part := text[w[0]:w[1]]
			for _, re := range p.res {
				m := re.Exec(part, 0)
				if m == nil {
					continue
				}
				at, n := w[0]+m[0], m[1]-m[0]
				if p.carries && !reInjCarries.Test(slice(text, at+n, at+200)) {
					continue
				}
				if inDocContext(text, at, n) {
					continue
				}
				hit = &injHit{id: p.id, label: p.label, at: offsetOf(at), match: text[at : at+n], supporting: p.supporting}
				break
			}
			if hit != nil {
				break
			}
		}
		if hit != nil {
			hits = append(hits, *hit)
		}
	}
	for _, h := range hits {
		if !h.supporting {
			return hits
		}
	}
	return nil
}

var leet = map[uint16]uint16{'0': 'o', '1': 'i', '3': 'e', '4': 'a', '5': 's', '7': 't', '@': 'a', '$': 's'}

var (
	reLeetWord   = jsre.MustCompile(`\b[a-z0-9@$]*[a-z][a-z0-9@$]*\b`, "gi")
	reDigit      = jsre.MustCompile(`\d`, "")
	reLeetChar   = jsre.MustCompile(`[013457@$]`, "g")
	reLetterOnly = jsre.MustCompile(`[a-z]`, "i")
)

// deobfuscate undoes spelled-out (i-g-n-o-r-e), zero-width-split and leetspeak words. It returns the new text and,
// for each of its code units, the offset in text; nil when there is nothing to undo.
func deobfuscate(text []uint16, windows [][2]int) ([]uint16, []int) {
	var near []uint16
	for i, w := range windows {
		if i > 0 {
			near = append(near, '\n')
		}
		near = append(near, text[w[0]:w[1]]...)
	}
	isLeet := reHasLeet.Test(near)
	if !reHasZeroWidth.Test(text) && !isLeet && !reHasSplit.Test(near) {
		return nil, nil
	}
	drop := make([]bool, len(text))
	for _, m := range reZeroWidth.FindAll(text) {
		drop[m[0]] = true
	}
	for _, m := range reSplitLetters.FindAll(text) {
		for i := m[0]; i < m[1]; i++ {
			if !reLetterOnly.Test(text[i : i+1]) {
				drop[i] = true
			}
		}
	}
	var s []uint16
	var idx []int
	for i, c := range text {
		if !drop[i] {
			s = append(s, c)
			idx = append(idx, i)
		}
	}
	if isLeet {
		s = reLeetWord.ReplaceAll(s, func(m []int) []uint16 {
			w := s[m[0]:m[1]]
			if !reDigit.Test(w) {
				return w
			}
			return reLeetChar.ReplaceAll(w, func(c []int) []uint16 { return []uint16{leet[w[c[0]]]} })
		})
	}
	return s, idx
}

// ScanInjection is lib/signals.js scanInjection: nil, or the categories that matched.
func ScanInjection(text string) *Injection {
	s := jsre.U16(text)
	if len(s) < 8 {
		return nil
	}
	windows := anchorWindows(s)
	hits := matchInjection(s, func(i int) int { return i }, windows)
	if len(hits) == 0 && (len(windows) > 0 || reHasZeroWidth.Test(s)) {
		if d, idx := deobfuscate(s, windows); d != nil {
			hits = matchInjection(d, func(i int) int {
				if i < len(idx) {
					return idx[i]
				}
				return 0
			}, anchorWindows(d))
		}
	}
	if len(hits) == 0 {
		return nil
	}
	r := &Injection{ID: hits[0].id, At: hits[0].at, Match: jsre.Str(hits[0].match)}
	for _, h := range hits {
		r.Categories = append(r.Categories, h.id)
		r.Labels = append(r.Labels, h.label)
	}
	r.Label = strings.Join(r.Labels, " + ")
	return r
}
