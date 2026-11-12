package jsre

import (
	"unicode"
	"unicode/utf16"
)

type kind uint8

const (
	kChar kind = iota
	kSet
	kClass
	kSeq
	kAlt
	kGroup
	kRep
	kBackref
	kBegin
	kEnd
	kWordB
	kNotWordB
	kLookahead
	kLookbehind
)

type node struct {
	kind              kind
	ch                uint16
	set               func(uint16) bool
	cls               *charClass
	subs              []*node
	idx               int  // group or backreference number
	neg               bool // negative lookaround
	min, max          int  // repetition bounds, or lookbehind widths (-1: unbounded)
	greedy            bool
	capFirst, capLast int // captures inside a repeated atom, reset on each iteration
}

type classItem struct {
	lo, hi uint16
	set    func(uint16) bool
}

type charClass struct {
	items []classItem
	neg   bool
}

func (c *charClass) raw(u uint16) bool {
	for _, it := range c.items {
		if it.set != nil {
			if it.set(u) {
				return true
			}
		} else if u >= it.lo && u <= it.hi {
			return true
		}
	}
	return false
}

func isDigit(u uint16) bool { return u >= '0' && u <= '9' }

func isWord(u uint16) bool {
	return u >= 'a' && u <= 'z' || u >= 'A' && u <= 'Z' || u >= '0' && u <= '9' || u == '_'
}

func isSpace(u uint16) bool {
	switch u {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return u >= 0x2000 && u <= 0x200a
}

func isLineTerminator(u uint16) bool { return u == '\n' || u == '\r' || u == 0x2028 || u == 0x2029 }

func anyChar(u uint16) bool { return !isLineTerminator(u) }

// canon is ECMA-262 Canonicalize for the non-unicode i flag: simple upper case, except that a non-ASCII character
// never maps to an ASCII one.
func canon(u uint16) uint16 {
	if u < 128 {
		if u >= 'a' && u <= 'z' {
			return u - 32
		}
		return u
	}
	if u >= 0xd800 && u <= 0xdfff {
		return u
	}
	up := unicode.ToUpper(rune(u))
	if up < 128 || up > 0xffff {
		return u
	}
	return uint16(up)
}

// classMatch tests u against a class, under i: any character with the same canonical form as u counts.
func classMatch(c *charClass, u uint16, icase bool) bool {
	found := c.raw(u)
	if !found && icase {
		cu := canon(u)
		if u < 128 {
			if u >= 'a' && u <= 'z' || u >= 'A' && u <= 'Z' {
				found = c.raw(u ^ 0x20)
			}
		} else if u < 0xd800 || u > 0xdfff {
			for r := unicode.SimpleFold(rune(u)); r != rune(u); r = unicode.SimpleFold(r) {
				if r <= 0xffff && canon(uint16(r)) == cu && c.raw(uint16(r)) {
					found = true
					break
				}
			}
		}
	}
	return found != c.neg
}

type machine struct {
	s     []uint16
	caps  []int
	icase bool
}

func (m *machine) wordAt(i int) bool { return i >= 0 && i < len(m.s) && isWord(m.s[i]) }

func (m *machine) charOK(n *node, i int) bool {
	if i >= len(m.s) {
		return false
	}
	u := m.s[i]
	switch n.kind {
	case kChar:
		return u == n.ch || (m.icase && canon(u) == canon(n.ch))
	case kSet:
		return n.set(u)
	case kClass:
		return classMatch(n.cls, u, m.icase)
	}
	return false
}

// match runs n at i and calls k with each end position, in JS backtracking order, until k returns true.
func (m *machine) match(n *node, i int, k func(int) bool) bool {
	switch n.kind {
	case kChar, kSet, kClass:
		return m.charOK(n, i) && k(i+1)
	case kSeq:
		return m.seq(n.subs, i, k)
	case kAlt:
		for _, s := range n.subs {
			if m.match(s, i, k) {
				return true
			}
		}
		return false
	case kGroup:
		return m.match(n.subs[0], i, func(j int) bool {
			a, b := m.caps[2*n.idx], m.caps[2*n.idx+1]
			m.caps[2*n.idx], m.caps[2*n.idx+1] = i, j
			if k(j) {
				return true
			}
			m.caps[2*n.idx], m.caps[2*n.idx+1] = a, b
			return false
		})
	case kRep:
		if a := n.subs[0]; (a.kind == kChar || a.kind == kSet || a.kind == kClass) && n.capLast < 0 {
			return m.repSingle(n, a, i, k)
		}
		return m.rep(n, i, 0, k)
	case kBackref:
		a, b := m.caps[2*n.idx], m.caps[2*n.idx+1]
		if a < 0 || b < 0 {
			return k(i)
		}
		l := b - a
		if i+l > len(m.s) {
			return false
		}
		for x := 0; x < l; x++ {
			p, q := m.s[a+x], m.s[i+x]
			if p != q && !(m.icase && canon(p) == canon(q)) {
				return false
			}
		}
		return k(i + l)
	case kBegin:
		return i == 0 && k(i)
	case kEnd:
		return i == len(m.s) && k(i)
	case kWordB:
		return m.wordAt(i-1) != m.wordAt(i) && k(i)
	case kNotWordB:
		return m.wordAt(i-1) == m.wordAt(i) && k(i)
	case kLookahead:
		saved := append([]int(nil), m.caps...)
		ok := m.match(n.subs[0], i, func(int) bool { return true })
		if n.neg {
			copy(m.caps, saved)
			return !ok && k(i)
		}
		if ok && k(i) {
			return true
		}
		copy(m.caps, saved)
		return false
	case kLookbehind:
		saved := append([]int(nil), m.caps...)
		ok := m.behind(n, i)
		if n.neg {
			copy(m.caps, saved)
			return !ok && k(i)
		}
		if ok && k(i) {
			return true
		}
		copy(m.caps, saved)
		return false
	}
	return false
}

// behind reports whether n's body matches a span that ends exactly at i. The ported patterns hold no capture inside a
// lookbehind, so a forward match from each possible start finds the same answer as JS's backward match.
func (m *machine) behind(n *node, i int) bool {
	lo := 0
	if n.max >= 0 {
		lo = i - n.max
	}
	if lo < 0 {
		lo = 0
	}
	for j := i - n.min; j >= lo; j-- {
		if m.match(n.subs[0], j, func(e int) bool { return e == i }) {
			return true
		}
	}
	return false
}

func (m *machine) seq(subs []*node, i int, k func(int) bool) bool {
	if len(subs) == 0 {
		return k(i)
	}
	return m.match(subs[0], i, func(j int) bool { return m.seq(subs[1:], j, k) })
}

// repSingle repeats a one-character atom without recursion: greedy tries the longest run first, lazy the shortest.
func (m *machine) repSingle(n, a *node, i int, k func(int) bool) bool {
	run := 0
	for (n.max < 0 || run < n.max) && m.charOK(a, i+run) {
		run++
	}
	if run < n.min {
		return false
	}
	if n.greedy {
		for c := run; c >= n.min; c-- {
			if k(i + c) {
				return true
			}
		}
		return false
	}
	for c := n.min; c <= run; c++ {
		if k(i + c) {
			return true
		}
	}
	return false
}

// rep is the ECMA-262 RepeatMatcher: captures inside the atom are cleared before each iteration, and an iteration
// that matched nothing once the minimum is reached fails.
func (m *machine) rep(n *node, i, count int, k func(int) bool) bool {
	if n.max >= 0 && count >= n.max {
		return k(i)
	}
	more := func() bool {
		var saved []int
		if n.capLast >= 0 {
			saved = append([]int(nil), m.caps[2*n.capFirst:2*n.capLast+2]...)
			for x := 2 * n.capFirst; x < 2*n.capLast+2; x++ {
				m.caps[x] = -1
			}
		}
		ok := m.match(n.subs[0], i, func(j int) bool {
			if j == i && count >= n.min {
				return false
			}
			return m.rep(n, j, count+1, k)
		})
		if !ok && saved != nil {
			copy(m.caps[2*n.capFirst:], saved)
		}
		return ok
	}
	if count < n.min {
		return more()
	}
	if n.greedy {
		return more() || k(i)
	}
	return k(i) || more()
}

// MatchAt runs the pattern anchored at pos. It returns the capture offsets (2 per group, group 0 first; -1 unset)
// or nil.
func (re *Regexp) MatchAt(s []uint16, pos int) []int {
	return re.matchAt(&machine{s: s, caps: make([]int, 2*(re.ncap+1)), icase: re.icase}, pos)
}

func (re *Regexp) matchAt(m *machine, pos int) []int {
	for x := range m.caps {
		m.caps[x] = -1
	}
	end := -1
	if !m.match(re.root, pos, func(e int) bool { end = e; return true }) {
		return nil
	}
	m.caps[0], m.caps[1] = pos, end
	return m.caps
}

// Exec is RegExp.prototype.exec with lastIndex = from: the first match at or after from, or nil.
func (re *Regexp) Exec(s []uint16, from int) []int {
	m := &machine{s: s, caps: make([]int, 2*(re.ncap+1)), icase: re.icase}
	for p := from; p <= len(s); p++ {
		if c := re.matchAt(m, p); c != nil {
			return c
		}
	}
	return nil
}

// Test is RegExp.prototype.test from the start.
func (re *Regexp) Test(s []uint16) bool { return re.Exec(s, 0) != nil }

// FindAll is String.prototype.matchAll for a g pattern: each match, an empty one advancing by one.
func (re *Regexp) FindAll(s []uint16) [][]int {
	var out [][]int
	for p := 0; p <= len(s); {
		c := re.Exec(s, p)
		if c == nil {
			break
		}
		out = append(out, c)
		p = c[1]
		if c[1] == c[0] {
			p++
		}
	}
	return out
}

// ReplaceAll is String.prototype.replace with a g pattern and a function.
func (re *Regexp) ReplaceAll(s []uint16, f func(caps []int) []uint16) []uint16 {
	var out []uint16
	last := 0
	for _, c := range re.FindAll(s) {
		out = append(out, s[last:c[0]]...)
		out = append(out, f(c)...)
		last = c[1]
	}
	return append(out, s[last:]...)
}

// U16 is a Go string as JS code units.
func U16(s string) []uint16 { return utf16.Encode([]rune(s)) }

// Str is code units as a Go string (a lone surrogate becomes U+FFFD).
func Str(u []uint16) string { return string(utf16.Decode(u)) }
