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

// node is the parsed pattern; compile.go turns it into a program for the iterative matcher (vm.go).
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

// charMatch tests one code unit against a one-character node.
func charMatch(n *node, u uint16, icase bool) bool {
	switch n.kind {
	case kChar:
		return u == n.ch || (icase && canon(u) == canon(n.ch))
	case kSet:
		return n.set(u)
	case kClass:
		return classMatch(n.cls, u, icase)
	}
	return false
}

// MatchAt runs the pattern anchored at pos. It returns the capture offsets (2 per group, group 0 first; -1 unset)
// or nil.
func (re *Regexp) MatchAt(s []uint16, pos int) []int { return re.newVM(s, nil).matchAt(pos) }

// Exec is RegExp.prototype.exec with lastIndex = from: the first match at or after from, or nil.
func (re *Regexp) Exec(s []uint16, from int) []int { return re.ExecB(s, from, nil) }

// ExecB is Exec counting its steps against b (nil: no limit). Past the limit it panics with ErrBudget; run it inside
// Budget.Run.
func (re *Regexp) ExecB(s []uint16, from int, b *Budget) []int {
	m := re.newVM(s, b)
	pre := re.filter()
	for p := from; p <= len(s); p++ {
		if pre.ok && (p == len(s) || !pre.set.has(s[p])) {
			continue
		}
		if c := m.matchAt(p); c != nil {
			return c
		}
	}
	return nil
}

// Test is RegExp.prototype.test from the start.
func (re *Regexp) Test(s []uint16) bool { return re.ExecB(s, 0, nil) != nil }

// TestB is Test under a budget.
func (re *Regexp) TestB(s []uint16, b *Budget) bool { return re.ExecB(s, 0, b) != nil }

// FindAll is String.prototype.matchAll for a g pattern: each match, an empty one advancing by one.
func (re *Regexp) FindAll(s []uint16) [][]int { return re.FindAllB(s, nil) }

// FindAllB is FindAll under a budget.
func (re *Regexp) FindAllB(s []uint16, b *Budget) [][]int {
	var out [][]int
	for p := 0; p <= len(s); {
		c := re.ExecB(s, p, b)
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
	return re.ReplaceAllB(s, f, nil)
}

// ReplaceAllB is ReplaceAll under a budget.
func (re *Regexp) ReplaceAllB(s []uint16, f func(caps []int) []uint16, b *Budget) []uint16 {
	var out []uint16
	last := 0
	for _, c := range re.FindAllB(s, b) {
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
