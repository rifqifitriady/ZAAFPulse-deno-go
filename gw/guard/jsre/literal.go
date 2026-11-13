package jsre

import "sort"

// A literal-prefix filter: when every match begins with one of a small set of literal strings (an alternation of
// words, say), a start position is tried only where the text begins with one of them, read through a trie. Under the i
// flag both sides compare by canon, as the matcher does. It runs only at positions the other filters let through.

const (
	maxLits   = 512
	maxLitLen = 8
)

type litSet struct {
	strs     [][]uint16
	complete bool // each string is a whole match of the node, not just its start
}

// literals returns the literal starts of the matches of n (nil: none known, every start possible).
func literals(n *node, icase bool, fc firstCache) *litSet {
	cu := func(u uint16) uint16 {
		if icase {
			return canon(u)
		}
		return u
	}
	switch n.kind {
	case kChar:
		return &litSet{strs: [][]uint16{{cu(n.ch)}}, complete: true}
	case kSet, kClass:
		f, _ := fc.of(n, icase)
		seen := map[uint16]bool{}
		for i, w := range f {
			for ; w != 0; w &= w - 1 {
				u := cu(uint16(i<<6 + trailing(w)))
				seen[u] = true
				if len(seen) > 8 {
					return nil
				}
			}
		}
		out := &litSet{complete: true}
		for u := range seen {
			out.strs = append(out.strs, []uint16{u})
		}
		return out
	case kGroup:
		return literals(n.subs[0], icase, fc)
	case kAlt:
		out := &litSet{complete: true}
		for _, s := range n.subs {
			l := literals(s, icase, fc)
			if l == nil {
				return nil
			}
			out.strs = append(out.strs, l.strs...)
			out.complete = out.complete && l.complete
			if len(out.strs) > maxLits {
				return nil
			}
		}
		return out
	case kRep:
		if n.min == 0 {
			return nil
		}
		l := literals(n.subs[0], icase, fc)
		if l == nil {
			return nil
		}
		return &litSet{strs: l.strs, complete: l.complete && n.min == 1 && n.max == 1}
	case kSeq:
		cur := &litSet{strs: [][]uint16{{}}, complete: true}
		for _, s := range n.subs {
			switch s.kind {
			case kLookahead, kLookbehind, kWordB, kNotWordB, kBegin:
				continue
			}
			l := literals(s, icase, fc)
			if l == nil || len(cur.strs)*len(l.strs) > maxLits {
				cur.complete = false
				break
			}
			var next [][]uint16
			long := false
			for _, a := range cur.strs {
				for _, b := range l.strs {
					c := append(append(make([]uint16, 0, len(a)+len(b)), a...), b...)
					if len(c) >= maxLitLen {
						c, long = c[:maxLitLen], true
					}
					next = append(next, c)
				}
			}
			cur = &litSet{strs: next, complete: l.complete && !long}
			if !cur.complete {
				break
			}
		}
		for _, s := range cur.strs {
			if len(s) == 0 {
				return nil
			}
		}
		return cur
	}
	return nil
}

func trailing(w uint64) int {
	n := 0
	for w&1 == 0 {
		w >>= 1
		n++
	}
	return n
}

// trie of literal starts: node i has sorted edges; end marks a literal that ends there (a match may start).
type trie struct {
	nodes []trieNode
}

type trieNode struct {
	units []uint16
	next  []int32
	end   bool
}

func newTrie(strs [][]uint16) *trie {
	t := &trie{nodes: []trieNode{{}}}
	for _, s := range strs {
		at := 0
		for _, u := range s {
			nd := &t.nodes[at]
			i := sort.Search(len(nd.units), func(i int) bool { return nd.units[i] >= u })
			if i < len(nd.units) && nd.units[i] == u {
				at = int(nd.next[i])
				continue
			}
			t.nodes = append(t.nodes, trieNode{})
			nd = &t.nodes[at]
			nd.units = append(nd.units[:i], append([]uint16{u}, nd.units[i:]...)...)
			nd.next = append(nd.next[:i], append([]int32{int32(len(t.nodes) - 1)}, nd.next[i:]...)...)
			at = len(t.nodes) - 1
		}
		t.nodes[at].end = true
	}
	return t
}

// starts reports whether s[p:] begins with one of the literals (units compared through cu).
func (t *trie) starts(s []uint16, p int, icase bool) bool {
	at := 0
	for i := p; ; i++ {
		nd := &t.nodes[at]
		if nd.end {
			return true
		}
		if i >= len(s) {
			return false
		}
		u := s[i]
		if icase {
			if u < 128 {
				if u >= 'a' && u <= 'z' {
					u -= 32
				}
			} else {
				u = canon(u)
			}
		}
		j := -1
		for k, x := range nd.units {
			if x == u {
				j = k
				break
			}
			if x > u {
				break
			}
		}
		if j < 0 {
			return false
		}
		at = int(nd.next[j])
	}
}

// runThen detects a pattern whose every match is a run of one class C (a greedy, unbounded repeat, at least once)
// followed by one unit of a class L that shares nothing with C, lookarounds before it aside: the email address
// pattern, [a-z0-9._%+-]+@…. Such a match ends its run exactly at an L unit, so only the runs that end at one need
// trying. It returns C and L, or nil.
func runThen(n *node, icase bool, fc firstCache) (run, then *bitset) {
	for n.kind == kGroup {
		n = n.subs[0]
	}
	if n.kind != kSeq {
		return nil, nil
	}
	i := 0
	for i < len(n.subs) && (n.subs[i].kind == kLookbehind || n.subs[i].kind == kLookahead) {
		i++
	}
	if i+1 >= len(n.subs) {
		return nil, nil
	}
	r, l := n.subs[i], n.subs[i+1]
	single := func(x *node) bool { return x.kind == kChar || x.kind == kSet || x.kind == kClass }
	if r.kind != kRep || r.min < 1 || r.max >= 0 || !r.greedy || !single(r.subs[0]) || !single(l) {
		return nil, nil
	}
	c, _ := fc.of(r.subs[0], icase)
	t, _ := fc.of(l, icase)
	for k := range c {
		if c[k]&t[k] != 0 {
			return nil, nil
		}
	}
	return c, t
}
