package jsre

// A two-code-unit filter: for each start position, the code unit there and the next one must be able to begin a
// match. Code units are reduced to 129 symbols (ASCII, and one for everything from 128 up), and assertions and
// lookarounds count as matching nothing, so the sets are a superset of the truth: the filter skips only positions
// where no match can start. Most guard patterns open with an alternation of words, whose first letters cover half
// the alphabet but whose first two letters are rare pairs.

const symOther = 128

type symset [3]uint64 // 129 bits

func (s *symset) add(x int)       { s[x>>6] |= 1 << (x & 63) }
func (s *symset) has(x int) bool  { return s[x>>6]&(1<<(x&63)) != 0 }
func (s *symset) union(o *symset) { s[0] |= o[0]; s[1] |= o[1]; s[2] |= o[2] }
func (s *symset) empty() bool     { return s[0]|s[1]|s[2] == 0 }
func sym(u uint16) int            { return min(int(u), symOther) }
func symsOf(b *bitset) (out symset) { // the symbols of a set of code units
	for u := range 128 {
		if b.has(uint16(u)) {
			out.add(u)
		}
	}
	for i := 2; i < len(b); i++ {
		if b[i] != 0 {
			out.add(symOther)
			break
		}
	}
	return out
}

// pre2 describes the first two code units of the strings a node matches: one holds the symbols of the strings of
// exactly one unit, two[a] the symbols that can follow a in the strings of two or more, nullable whether it matches
// the empty string; any means no description (a backreference).
type pre2 struct {
	one      symset
	two      [129]symset
	nullable bool
	any      bool
}

func (p *pre2) firsts() (out symset) {
	out = p.one
	for a := range p.two {
		if !p.two[a].empty() {
			out.add(a)
		}
	}
	return out
}

func (p *pre2) unionWith(o *pre2) {
	p.any = p.any || o.any
	p.nullable = p.nullable || o.nullable
	p.one.union(&o.one)
	for a := range p.two {
		p.two[a].union(&o.two[a])
	}
}

// concat is a followed by b.
func concat(a, b *pre2) *pre2 {
	out := &pre2{any: a.any || b.any, nullable: a.nullable && b.nullable}
	if out.any {
		return out
	}
	if b.nullable {
		out.one.union(&a.one)
	}
	if a.nullable {
		out.one.union(&b.one)
	}
	for x := range out.two {
		out.two[x] = a.two[x]
	}
	fb := b.firsts()
	for x := range 129 {
		if a.one.has(x) {
			out.two[x].union(&fb)
		}
	}
	if a.nullable {
		for x := range out.two {
			out.two[x].union(&b.two[x])
		}
	}
	return out
}

// prefix2 computes the description of n; memo holds those already computed (a repeat looks at its atom twice, so
// without it nested repeats cost 2^depth). A returned value is shared: never modify it.
func prefix2(n *node, icase bool, fc firstCache, memo map[*node]*pre2) *pre2 {
	if p, ok := memo[n]; ok {
		return p
	}
	p := prefix2Of(n, icase, fc, memo)
	memo[n] = p
	return p
}

func prefix2Of(n *node, icase bool, fc firstCache, memo map[*node]*pre2) *pre2 {
	switch n.kind {
	case kChar, kSet, kClass:
		f, _ := fc.of(n, icase)
		return &pre2{one: symsOf(f)}
	case kSeq:
		out := &pre2{nullable: true}
		for _, s := range n.subs {
			out = concat(out, prefix2(s, icase, fc, memo))
		}
		return out
	case kAlt:
		out := &pre2{}
		for _, s := range n.subs {
			out.unionWith(prefix2(s, icase, fc, memo))
		}
		return out
	case kGroup:
		return prefix2(n.subs[0], icase, fc, memo)
	case kRep:
		if n.max == 0 {
			return &pre2{nullable: true}
		}
		atom := prefix2(n.subs[0], icase, fc, memo)
		first := *atom
		first.nullable = first.nullable || n.min == 0
		if n.max == 1 {
			return &first
		}
		// Two copies give every two-unit prefix of any number of copies (each copy draws from the same sets).
		rest := *atom
		rest.nullable = true
		return concat(&first, &rest)
	case kBackref:
		return &pre2{any: true, nullable: true}
	}
	return &pre2{nullable: true} // assertions and lookarounds consume nothing
}

// wordStart reports whether every match begins with \b (lookarounds before it aside).
func wordStart(n *node) bool {
	switch n.kind {
	case kWordB:
		return true
	case kGroup:
		return wordStart(n.subs[0])
	case kAlt:
		for _, s := range n.subs {
			if !wordStart(s) {
				return false
			}
		}
		return len(n.subs) > 0
	case kSeq:
		for _, s := range n.subs {
			switch s.kind {
			case kLookahead, kLookbehind:
				continue
			}
			return wordStart(s)
		}
	}
	return false
}

// prevNot is the set of code units that must not come right before a match: every match begins (before consuming
// anything) with a negative lookbehind one of whose alternatives is a single character; past such a character the
// lookbehind fails. nil when there is none.
func prevNot(n *node, icase bool, fc firstCache) *bitset {
	switch n.kind {
	case kGroup:
		return prevNot(n.subs[0], icase, fc)
	case kSeq:
		var out *bitset
		for _, s := range n.subs {
			if s.kind != kLookahead && s.kind != kLookbehind && s.kind != kWordB && s.kind != kNotWordB {
				break
			}
			if s.kind == kLookbehind && s.neg {
				if b := singleAlts(s.subs[0], icase, fc); b != nil {
					if out == nil {
						out = new(bitset)
					}
					out.union(b)
				}
			}
		}
		return out
	}
	return nil
}

// singleAlts is the union of the alternatives of n that are exactly one character, or nil.
func singleAlts(n *node, icase bool, fc firstCache) *bitset {
	switch n.kind {
	case kGroup:
		return singleAlts(n.subs[0], icase, fc)
	case kChar, kSet, kClass:
		f, _ := fc.of(n, icase)
		return f
	case kSeq:
		if len(n.subs) == 1 {
			return singleAlts(n.subs[0], icase, fc)
		}
	case kAlt:
		var out *bitset
		for _, s := range n.subs {
			if b := singleAlts(s, icase, fc); b != nil {
				if out == nil {
					out = new(bitset)
				}
				out.union(b)
			}
		}
		return out
	}
	return nil
}
