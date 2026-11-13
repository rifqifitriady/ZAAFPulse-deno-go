package jsre

import (
	"math/bits"
	"sync"
	"unicode"
)

// A first-character filter: Exec skips start positions whose code unit cannot begin a match. It changes no result,
// only the cost of scanning long text.

type bitset [1024]uint64

func (b *bitset) add(u uint16)      { b[u>>6] |= 1 << (u & 63) }
func (b *bitset) has(u uint16) bool { return b[u>>6]&(1<<(u&63)) != 0 }
func (b *bitset) union(o *bitset) {
	for i := range b {
		b[i] |= o[i]
	}
}

type prefilter struct {
	once     sync.Once
	ok       bool // false: every position must be tried
	set      bitset
	anchored bool      // every match begins with ^ (no m flag): only position 0 can match
	wordOnly bool      // every match begins with \b before a word character: s[p-1] must not be one
	pairs    bool      // two holds the two-unit filter
	ascii    [128]bool // set, for ASCII
	icase    bool
	tab      *[2][129][130]bool // [prev is an ASCII word unit][sym s[p]][sym s[p+1], 129 at the end]: may start
	first    [2][129]bool       // tab's union over the next unit: rejects most positions from a small table
	lits     *trie              // the literal starts of every match, or nil
	run      *bitset            // with then: every match is a run of run units ended by a then unit (runThen)
	then     *bitset
	prevNot  *bitset // code units that cannot precede a match (a leading negative lookbehind), or nil
	one      symset
	two      [129]symset
}

// next is the first position in [q, end) where a match can start, or end. One table lookup per position decides
// from the code unit there, the one after (or the end) and whether the one before is an ASCII word character; a
// position the table accepts is then checked against the full first set (for non-ASCII) and prevNot.
func (f *prefilter) next(s []uint16, q, end int) int {
	t := f.tab
	pw := 0
	if q > 0 && s[q-1] < 128 && wordTab[s[q-1]] {
		pw = 1
	}
	for ; q < end; q++ {
		u := s[q]
		a := sym(u)
		hit := false
		if f.first[pw][a] {
			nb := 129
			if q+1 < len(s) {
				nb = sym(s[q+1])
			}
			hit = t[pw][a][nb]
		}
		pw = 0
		if u < 128 && wordTab[u] {
			pw = 1
		}
		if !hit {
			continue
		}
		if u >= 128 && !f.set.has(u) {
			continue
		}
		if q > 0 && f.prevNot != nil && f.prevNot.has(s[q-1]) {
			continue
		}
		if f.lits != nil && !f.lits.starts(s, q, f.icase) {
			continue
		}
		return q
	}
	return end
}

// wordTab is isWord for ASCII.
var wordTab = func() (t [128]bool) {
	for u := range t {
		t[u] = isWord(uint16(u))
	}
	return t
}()

// anchoredAt reports whether every match of n must begin with ^.
func anchoredAt(n *node) bool {
	switch n.kind {
	case kBegin:
		return true
	case kGroup:
		return anchoredAt(n.subs[0])
	case kSeq:
		return len(n.subs) > 0 && anchoredAt(n.subs[0])
	case kAlt:
		for _, s := range n.subs {
			if !anchoredAt(s) {
				return false
			}
		}
		return len(n.subs) > 0
	}
	return false
}

// firstOf returns the code units n can start with and whether n can match without consuming one (then the units of
// what follows count too).
func firstOf(n *node, icase bool) (*bitset, bool) {
	b := new(bitset)
	switch n.kind {
	case kChar:
		b.add(n.ch)
		if icase {
			addFolds(b, n.ch)
		}
		return b, false
	case kSet:
		for u := 0; u < 1<<16; u++ {
			if n.set(uint16(u)) {
				b.add(uint16(u))
			}
		}
		return b, false
	case kClass:
		classSet(b, n.cls, icase)
		return b, false
	case kSeq:
		for _, s := range n.subs {
			f, empty := firstOf(s, icase)
			b.union(f)
			if !empty {
				return b, false
			}
		}
		return b, true
	case kAlt:
		empty := false
		for _, s := range n.subs {
			f, e := firstOf(s, icase)
			b.union(f)
			empty = empty || e
		}
		return b, empty
	case kGroup:
		return firstOf(n.subs[0], icase)
	case kRep:
		f, e := firstOf(n.subs[0], icase)
		return f, e || n.min == 0
	case kBackref:
		for i := range b {
			b[i] = ^uint64(0)
		}
		return b, true
	}
	return b, true // assertions consume nothing
}

// classSet fills b with the code units classMatch accepts, without testing each of the 65,536 under the i flag: the
// raw members, then (with i) the ASCII letter of the other case and the non-ASCII members of each member's case-fold
// orbit with the same canonical form (classMatch's rule, which is symmetric), then the negation.
func classSet(b *bitset, c *charClass, icase bool) {
	raw := new(bitset)
	for _, it := range c.items {
		if it.set != nil {
			for u := 0; u < 1<<16; u++ {
				if it.set(uint16(u)) {
					raw.add(uint16(u))
				}
			}
		} else {
			for u := int(it.lo); u <= int(it.hi); u++ {
				raw.add(uint16(u))
			}
		}
	}
	*b = *raw
	if icase {
		for i, w := range raw {
			for ; w != 0; w &= w - 1 {
				r := uint16(i<<6 + bits.TrailingZeros64(w))
				if r < 128 && (r|0x20 >= 'a' && r|0x20 <= 'z') {
					b.add(r ^ 0x20)
				}
				if r >= 0xd800 && r <= 0xdfff {
					continue
				}
				cr := canon(r)
				for x := unicode.SimpleFold(rune(r)); x != rune(r); x = unicode.SimpleFold(x) {
					if x >= 128 && x <= 0xffff && (x < 0xd800 || x > 0xdfff) && canon(uint16(x)) == cr {
						b.add(uint16(x))
					}
				}
			}
		}
	}
	if c.neg {
		for i := range b {
			b[i] = ^b[i]
		}
	}
}

func addFolds(b *bitset, u uint16) {
	c := canon(u)
	for r := unicode.SimpleFold(rune(u)); r != rune(u); r = unicode.SimpleFold(r) {
		if r <= 0xffff && canon(uint16(r)) == c {
			b.add(uint16(r))
		}
	}
}

func (re *Regexp) filter() *prefilter {
	re.pre.once.Do(func() {
		f, empty := firstOf(re.root, re.icase)
		re.pre.ok = !empty
		re.pre.set = *f
		re.pre.anchored = anchoredAt(re.root)
		for u := range re.pre.ascii {
			re.pre.ascii[u] = f.has(uint16(u))
		}
		if !re.pre.ok {
			return
		}
		words := true
		for u := 0; u < 1<<16; u++ {
			if f.has(uint16(u)) && (u > 127 || !isWord(uint16(u))) {
				words = false
				break
			}
		}
		re.pre.wordOnly = words && wordStart(re.root)
		fc := firstCache{}
		re.pre.prevNot = prevNot(re.root, re.icase, fc)
		re.pre.icase = re.icase
		if l := literals(re.root, re.icase, fc); l != nil {
			re.pre.lits = newTrie(l.strs)
		}
		re.pre.run, re.pre.then = runThen(re.root, re.icase, fc)
		if p2 := prefix2(re.root, re.icase, fc, map[*node]*pre2{}); !p2.any && !p2.nullable {
			re.pre.pairs, re.pre.one, re.pre.two = true, p2.one, p2.two
		}
		pf := &re.pre
		pf.tab = new([2][129][130]bool)
		for pw := range 2 {
			if pw == 1 && pf.wordOnly {
				continue
			}
			for a := range 129 {
				if a < 128 && !pf.ascii[a] {
					continue
				}
				for nb := range 130 {
					pf.tab[pw][a][nb] = !pf.pairs || pf.one.has(a) || (nb < 129 && pf.two[a].has(nb))
					pf.first[pw][a] = pf.first[pw][a] || pf.tab[pw][a][nb]
				}
			}
		}
	})
	return &re.pre
}

// firstCache memoises firstOf per node while one program is compiled (classes cost 65,536 tests each).
type firstCache map[*node]struct {
	set   *bitset
	empty bool
}

func (c firstCache) of(n *node, icase bool) (*bitset, bool) {
	if v, ok := c[n]; ok {
		return v.set, v.empty
	}
	s, e := firstOf(n, icase)
	c[n] = struct {
		set   *bitset
		empty bool
	}{s, e}
	return s, e
}
