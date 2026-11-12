package jsre

import (
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
	once sync.Once
	ok   bool // false: every position must be tried
	set  bitset
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
	case kSet, kClass:
		m := &machine{s: make([]uint16, 1), icase: icase}
		for u := 0; u < 1<<16; u++ {
			m.s[0] = uint16(u)
			if m.charOK(n, 0) {
				b.add(uint16(u))
			}
		}
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
	})
	return &re.pre
}
