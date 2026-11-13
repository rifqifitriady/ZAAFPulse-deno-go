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

// scan holds one text being scanned: its newline offsets (so line bounds cost a binary search, not a scan back to
// the start of a long line) and the line tests already run. The JS runs CODE_LINE and STRING_ITEM over the whole line
// for every candidate; on one long line with many candidates that is quadratic. A line's answer never changes, so
// it is computed once: the same results in linear time.
type scan struct {
	text []uint16
	nl   []int
	line map[[2]int]bool
	b    *jsre.Budget
}

func newScan(text []uint16, b *jsre.Budget) *scan {
	sc := &scan{text: text, line: map[[2]int]bool{}, b: b}
	for i, c := range text {
		if c == '\n' {
			sc.nl = append(sc.nl, i)
		}
	}
	return sc
}

// lastNL is text.lastIndexOf('\n', from); nextNL is text.indexOf('\n', from).
func (sc *scan) lastNL(from int) int {
	if from < 0 {
		from = 0 // JS clamps a negative fromIndex to 0
	}
	i := sort.SearchInts(sc.nl, from+1) - 1
	if i < 0 {
		return -1
	}
	return sc.nl[i]
}

func (sc *scan) nextNL(from int) int {
	i := sort.SearchInts(sc.nl, max(from, 0))
	if i >= len(sc.nl) {
		return -1
	}
	return sc.nl[i]
}

func (sc *scan) inDocContext(at, n int) bool {
	text, b := sc.text, sc.b
	if reQuoteBefore.TestB(slice(text, at-2, at), b) {
		return true
	}
	ls := sc.lastNL(at) + 1
	le := sc.nextNL(at + n)
	if le < 0 {
		le = len(text)
	}
	line := slice(text, ls, le)
	var prev []uint16
	if ls > 1 {
		prev = slice(text, sc.lastNL(ls-2)+1, ls-1)
	}
	// `${prev}\n${line}`.slice(-(n + 400)), built without copying a long line.
	k := n + 400
	var around []uint16
	switch total := len(prev) + 1 + len(line); {
	case k >= total:
		around = append(append(append([]uint16{}, prev...), '\n'), line...)
	case k <= len(line):
		around = append([]uint16{}, line[len(line)-k:]...)
	default:
		around = append(append(append([]uint16{}, prev[len(prev)-(k-len(line)-1):]...), '\n'), line...)
	}
	around = reMasked.ReplaceAllB(around, func([]int) []uint16 { return []uint16{' '} }, b)
	if reDocContext.TestB(around, b) {
		return true
	}
	key := [2]int{ls, le}
	code, ok := sc.line[key]
	if !ok {
		code = reCodeLine.TestB(line, b) || reStringItem.TestB(line, b)
		sc.line[key] = code
	}
	return code
}

var reMasked = jsre.MustCompile(`\S+@\S+|https?:\/\/\S+`, "g")

// anchorWindows: spans worth matching, around each anchor, widened to line starts and merged when they overlap.
func anchorWindows(text []uint16, b *jsre.Budget) [][2]int {
	var spans [][2]int
	for _, re := range []*jsre.Regexp{reInjAnchor, reInjAnchorMark} {
		last := 0
		for {
			m := re.ExecB(text, last, b)
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

func matchInjection(text []uint16, offsetOf func(int) int, windows [][2]int, b *jsre.Budget) []injHit {
	if len(windows) == 0 {
		return nil
	}
	sc := newScan(text, b)
	var hits []injHit
	for _, p := range injection {
		var hit *injHit
		for _, w := range windows {
			part := text[w[0]:w[1]]
			for _, re := range p.res {
				m := re.ExecB(part, 0, b)
				if m == nil {
					continue
				}
				at, n := w[0]+m[0], m[1]-m[0]
				if p.carries && !reInjCarries.TestB(slice(text, at+n, at+200), b) {
					continue
				}
				if sc.inDocContext(at, n) {
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
	reLeetWord = jsre.MustCompile(`\b[a-z0-9@$]*[a-z][a-z0-9@$]*\b`, "gi")
	reDigit    = jsre.MustCompile(`\d`, "")
	reLeetChar = jsre.MustCompile(`[013457@$]`, "g")
)

// deobfuscate undoes spelled-out (i-g-n-o-r-e), zero-width-split and leetspeak words. It returns the new text and,
// for each of its code units, the offset in text; nil when there is nothing to undo.
func deobfuscate(text []uint16, windows [][2]int, b *jsre.Budget) ([]uint16, []int) {
	var near []uint16
	for i, w := range windows {
		if i > 0 {
			near = append(near, '\n')
		}
		near = append(near, text[w[0]:w[1]]...)
	}
	isLeet := reHasLeet.TestB(near, b)
	if !reHasZeroWidth.TestB(text, b) && !isLeet && !reHasSplit.TestB(near, b) {
		return nil, nil
	}
	drop := make([]bool, len(text))
	for _, m := range reZeroWidth.FindAllB(text, b) {
		drop[m[0]] = true
	}
	for _, m := range reSplitLetters.FindAllB(text, b) {
		for i := m[0]; i < m[1]; i++ {
			if c := text[i] | 0x20; c < 'a' || c > 'z' { // !/[a-z]/i.test(ch)
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
		s = reLeetWord.ReplaceAllB(s, func(m []int) []uint16 {
			w := s[m[0]:m[1]]
			if !reDigit.TestB(w, b) {
				return w
			}
			return reLeetChar.ReplaceAllB(w, func(c []int) []uint16 { return []uint16{leet[w[c[0]]]} }, b)
		}, b)
	}
	return s, idx
}

// ScanInjection is lib/signals.js scanInjection: nil, or the categories that matched. It runs under b (nil: NewBudget
// for the text, with the 2 s deadline); a limit comes back as jsre.ErrBudget, ErrStack or ErrDeadline, never a panic.
func ScanInjection(text string, b *jsre.Budget) (r *Injection, err error) {
	u := jsre.U16(text)
	b = orBudget(b, len(u))
	err = b.Run(func() { r = scanInjection(u, b) })
	return r, err
}

func scanInjection(s []uint16, b *jsre.Budget) *Injection {
	if len(s) < 8 {
		return nil
	}
	windows := anchorWindows(s, b)
	hits := matchInjection(s, func(i int) int { return i }, windows, b)
	if len(hits) == 0 && (len(windows) > 0 || reHasZeroWidth.TestB(s, b)) {
		if d, idx := deobfuscate(s, windows, b); d != nil {
			hits = matchInjection(d, func(i int) int {
				if i < len(idx) {
					return idx[i]
				}
				return 0
			}, anchorWindows(d, b), b)
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
