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

// anchorWindows: spans worth matching, around each anchor, widened to line starts and merged when they overlap. Both
// anchor patterns run in one scan (anchorMulti); each goes on from where the JS loop would take it next.
func anchorWindows(text []uint16, b *jsre.Budget) [][2]int {
	Prepare()
	var spans [][2]int
	var st jsre.MultiState
	defer anchorMulti.Release(&st)
	anchorMulti.AllB(&st, text, 3, b, func(_ int, m []int) int {
		last := m[1]
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
			return len(text) + 1 // stop this pattern, as the JS loop breaks
		}
		return last
	})
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

// injMulti finds the first match of every injection pattern in a window in one scan (set up by Prepare); injFirst[c]
// is the index of category c's first pattern in it.
var (
	injMulti *jsre.Multi
	injFirst []int
)

var anchorMulti *jsre.Multi

func setupInjMulti() {
	anchorMulti = jsre.NewMulti([]*jsre.Regexp{reInjAnchor, reInjAnchorMark})
	var res []*jsre.Regexp
	for _, c := range injection {
		injFirst = append(injFirst, len(res))
		res = append(res, c.res...)
	}
	injMulti = jsre.NewMulti(res)
}

// matchInjection finds, for each category in order, the first window holding an accepted match of one of its
// patterns (the patterns in order, each at its first match in the window), like the JS. It goes window by window and
// finds the first match of every pattern of the categories still open in one scan of the window: the same hits as
// category by category, since each category's answer depends only on its own patterns.
func matchInjection(text []uint16, offsetOf func(int) int, windows [][2]int, b *jsre.Budget) []injHit {
	if len(windows) == 0 {
		return nil
	}
	Prepare()
	sc := newScan(text, b)
	found := make([]*injHit, len(injection))
	open := len(injection)
	var st jsre.MultiState
	defer injMulti.Release(&st)
	first := make([][]int, injFirst[len(injFirst)-1]+len(injection[len(injection)-1].res))
	for _, w := range windows {
		var active uint64
		for ci, p := range injection {
			if found[ci] == nil {
				active |= (uint64(1)<<len(p.res) - 1) << injFirst[ci]
			}
		}
		part := text[w[0]:w[1]]
		injMulti.FirstB(&st, part, active, b, first)
		for ci, p := range injection {
			if found[ci] != nil {
				continue
			}
			for j := range p.res {
				m := first[injFirst[ci]+j]
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
				found[ci] = &injHit{id: p.id, label: p.label, at: offsetOf(at), match: text[at : at+n], supporting: p.supporting}
				open--
				break
			}
		}
		if open == 0 {
			break
		}
	}
	var hits []injHit
	for _, h := range found {
		if h != nil {
			hits = append(hits, *h)
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

// deobfuscate undoes spelled-out (i-g-n-o-r-e), zero-width-split and leetspeak words. It returns the new text and
// the map from its offsets to offsets in text; nil when there is nothing to undo. It keeps one bit per dropped code
// unit, not a flag and an offset per unit, and rewrites leetspeak in place on its own copy.
func deobfuscate(text []uint16, windows [][2]int, b *jsre.Budget) ([]uint16, *keptMap) {
	var near []uint16
	if len(windows) == 1 {
		near = text[windows[0][0]:windows[0][1]]
	} else {
		n := len(windows)
		for _, w := range windows {
			n += w[1] - w[0]
		}
		near = make([]uint16, 0, n)
		for i, w := range windows {
			if i > 0 {
				near = append(near, '\n')
			}
			near = append(near, text[w[0]:w[1]]...)
		}
	}
	isLeet := reHasLeet.TestB(near, b)
	if !reHasZeroWidth.TestB(text, b) && !isLeet && !reHasSplit.TestB(near, b) {
		return nil, nil
	}
	km := &keptMap{drop: make([]uint64, len(text)/64+1)}
	dropped := 0
	mark := func(i int) {
		if km.drop[i>>6]&(1<<(i&63)) == 0 {
			km.drop[i>>6] |= 1 << (i & 63)
			dropped++
		}
	}
	reZeroWidth.EachB(text, b, func(m []int) { mark(m[0]) })
	reSplitLetters.EachB(text, b, func(m []int) {
		for i := m[0]; i < m[1]; i++ {
			if c := text[i] | 0x20; c < 'a' || c > 'z' { // !/[a-z]/i.test(ch)
				mark(i)
			}
		}
	})
	s := text
	if dropped > 0 {
		// near is no longer needed: its storage holds the shortened text when it is large enough (it usually is
		// when the windows cover most of a long text, the case where a second copy would cost the most).
		if multi := len(windows) > 1; multi && cap(near) >= len(text)-dropped {
			s = near[:0]
		} else {
			s = make([]uint16, 0, len(text)-dropped)
		}
		km.before = make([]int32, len(km.drop)+1)
		for blk, word := range km.drop {
			lo, hi := blk*64, min(blk*64+64, len(text))
			for i := lo; i < hi; i++ {
				if word&(1<<(i-lo)) == 0 {
					s = append(s, text[i])
				}
			}
			km.before[blk+1] = int32(len(s))
		}
	} else {
		km = nil
	}
	if len(s) == 0 {
		return nil, nil // everything dropped (the JS has an empty string: nothing to match either way)
	}
	if isLeet {
		s = leetInPlace(s, dropped > 0, b) // s is a new buffer exactly when something was dropped
	}
	return s, km
}

// leetInPlace is s.replace(LEET_WORD, w => /\d/.test(w) ? w.replace(/[013457@$]/g, c => LEET[c]) : w). A replacement
// keeps the length, so it is written over s (or over a copy, made at the first change, when s is not ours). A word is
// rewritten only after the next one is found: the search for the next word reads the last code unit of the previous
// one (\b), which must still be the original, as in the JS, which matches on the unchanged string.
func leetInPlace(s []uint16, own bool, b *jsre.Budget) []uint16 {
	src, out := s, s
	pending := [2]int{-1, -1}
	apply := func() {
		a, z := pending[0], pending[1]
		pending = [2]int{-1, -1}
		if a < 0 || !reDigit.TestB(src[a:z], b) {
			return
		}
		for i := a; i < z; i++ {
			if c, ok := leet[src[i]]; ok {
				if !own {
					out = append([]uint16(nil), src...)
					own = true
				}
				out[i] = c
			}
		}
	}
	// Only a word with a digit changes, and a word lies inside one run of [A-Za-z0-9@$]; the search for words starts
	// afresh at each run (the code unit before a run is outside every word and never rewritten). So only the runs
	// holding a digit are searched, which skips most of a long text.
	for i := 0; i < len(src); i++ {
		if src[i] < '0' || src[i] > '9' {
			continue
		}
		r0, r1 := i, i+1
		for r0 > 0 && leetUnit(src[r0-1]) {
			r0--
		}
		for r1 < len(src) && leetUnit(src[r1]) {
			r1++
		}
		for p := r0; p <= r1; {
			m := reLeetWord.ExecB(src, p, b)
			if m == nil || m[0] >= r1 {
				break
			}
			apply()
			pending = [2]int{m[0], m[1]}
			p = m[1]
			if m[1] == m[0] {
				p++
			}
		}
		apply()
		i = r1
	}
	return out
}

// leetUnit reports whether u is in [A-Za-z0-9@$] (what LEET_WORD's words are made of under the i flag).
func leetUnit(u uint16) bool {
	return u >= 'a' && u <= 'z' || u >= 'A' && u <= 'Z' || u >= '0' && u <= '9' || u == '@' || u == '$'
}

// keptMap maps an offset in a text with some code units dropped back to the offset in the original: a bit per
// original code unit (set: dropped) and, per block of 64, how many were kept before it.
type keptMap struct {
	drop   []uint64
	before []int32
}

// orig is the offset in the original text of code unit i of the shortened one.
func (k *keptMap) orig(i int) int {
	blk := sort.Search(len(k.drop), func(j int) bool { return int(k.before[j+1]) > i })
	if blk >= len(k.drop) {
		return -1
	}
	n := i - int(k.before[blk]) // the n-th kept unit of this block
	word := ^k.drop[blk]
	for bit := 0; bit < 64; bit++ {
		if word&(1<<bit) != 0 {
			if n == 0 {
				return blk*64 + bit
			}
			n--
		}
	}
	return -1
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
		if d, km := deobfuscate(s, windows, b); d != nil {
			hits = matchInjection(d, func(i int) int {
				if i >= len(d) {
					return 0
				}
				if km == nil {
					return i
				}
				return km.orig(i)
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
