package jsre

// A "required within K" filter: every match holds one of a set R of code units among its first K units (an IPv6
// address has a ':' within its first five, an IPv4 address a '.' within its first four). A start position is tried
// only when one of the K units from it is in R. R is taken from the punctuation the pattern consumes, and K is the
// smallest that the analysis below proves; numbers in CSV or code then cost no matcher run.

const maxWithin = 8

// lenSet is a set of lengths 0..maxWithin, maxWithin standing for "maxWithin or more".
type lenSet uint16

func (s lenSet) shift(o lenSet) lenSet { // {a+b}
	var out lenSet
	for a := 0; a <= maxWithin; a++ {
		if s&(1<<a) == 0 {
			continue
		}
		for b := 0; b <= maxWithin; b++ {
			if o&(1<<b) != 0 {
				out |= 1 << min(a+b, maxWithin)
			}
		}
	}
	return out
}

// hitFree describes the strings of a node against R: hit holds the index of the first R unit of each string that has
// one, free the length of each string with none.
type hitFree struct{ hit, free lenSet }

func seqHF(a, b hitFree) hitFree {
	return hitFree{hit: a.hit | a.free.shift(b.hit), free: a.free.shift(b.free)}
}

const allLens = lenSet(1<<(maxWithin+1) - 1)

func withinOf(n *node, r *bitset, icase bool, fc firstCache) hitFree {
	switch n.kind {
	case kChar, kSet, kClass:
		f, _ := fc.of(n, icase)
		in, out := false, false
		for i := range f {
			in = in || f[i]&r[i] != 0
			out = out || f[i]&^r[i] != 0
		}
		var h hitFree
		if in {
			h.hit = 1
		}
		if out {
			h.free = 1 << 1
		}
		return h
	case kSeq:
		h := hitFree{free: 1}
		for _, s := range n.subs {
			h = seqHF(h, withinOf(s, r, icase, fc))
		}
		return h
	case kAlt:
		var h hitFree
		for _, s := range n.subs {
			x := withinOf(s, r, icase, fc)
			h.hit |= x.hit
			h.free |= x.free
		}
		return h
	case kGroup:
		return withinOf(n.subs[0], r, icase, fc)
	case kRep:
		atom := withinOf(n.subs[0], r, icase, fc)
		h := hitFree{free: 1}
		for range n.min {
			h = seqHF(h, atom)
			if h.free == 0 && h.free|h.hit == h.hit { // nothing free is left to extend
				break
			}
		}
		acc := h
		for i := n.min; n.max < 0 || i < n.max; i++ {
			next := seqHF(h, atom)
			next.hit |= acc.hit
			next.free |= acc.free
			if next == acc {
				break
			}
			acc, h = next, seqHF(h, atom)
			if i > n.min+2*maxWithin {
				break // lengths are capped: no new length can appear any more
			}
		}
		return acc
	case kBackref:
		return hitFree{hit: allLens, free: allLens}
	}
	return hitFree{free: 1} // assertions and lookarounds consume nothing
}

// consumedPunct is the ASCII punctuation the pattern consumes (lookarounds aside): the candidates for R.
func consumedPunct(n *node, icase bool, fc firstCache, out *bitset) {
	switch n.kind {
	case kLookahead, kLookbehind:
		return
	case kChar, kSet, kClass:
		f, _ := fc.of(n, icase)
		cnt := 0
		for u := range 128 {
			if f.has(uint16(u)) {
				cnt++
			}
		}
		if cnt > 8 {
			return
		}
		for u := range 128 {
			c := uint16(u)
			if f.has(c) && !isWord(c) && c > ' ' {
				out.add(c)
			}
		}
		return
	}
	for _, s := range n.subs {
		consumedPunct(s, icase, fc, out)
	}
}

// requiredWithin finds R and the smallest K (2..maxWithin-1) such that every match holds a unit of R among its
// first K, or K = 0.
func requiredWithin(root *node, icase bool, fc firstCache) (*bitset, int) {
	r := new(bitset)
	consumedPunct(root, icase, fc, r)
	empty := true
	for i := range r {
		empty = empty && r[i] == 0
	}
	if empty {
		return nil, 0
	}
	h := withinOf(root, r, icase, fc)
	if h.free != 0 {
		return nil, 0 // some match has no unit of R at all
	}
	for k := 2; k < maxWithin; k++ {
		if h.hit&^(1<<k-1) == 0 { // every first hit is at an index below k
			return r, k
		}
	}
	return nil, 0
}

// hasWithin reports whether one of s[p : p+k] is in r.
func hasWithin(s []uint16, p, k int, r *bitset) bool {
	for i := p; i < len(s) && i < p+k; i++ {
		if r.has(s[i]) {
			return true
		}
	}
	return false
}
