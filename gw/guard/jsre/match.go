package jsre

import (
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
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

// Prepare compiles the pattern's program and start-position filter now instead of on first use (tens of
// milliseconds for a large pattern), so the first call under a deadline does not pay for it.
func (re *Regexp) Prepare() {
	re.program()
	re.filter()
}

// MatchAt runs the pattern anchored at pos. It returns the capture offsets (2 per group, group 0 first; -1 unset)
// or nil.
func (re *Regexp) MatchAt(s []uint16, pos int) []int { return re.newVM(s, nil).matchAt(pos) }

// Exec is RegExp.prototype.exec with lastIndex = from: the first match at or after from, or nil.
func (re *Regexp) Exec(s []uint16, from int) []int { return re.ExecB(s, from, nil) }

// ExecB is Exec counting its steps against b (nil: no limit). Past the limit it panics with ErrBudget; run it inside
// Budget.Run.
func (re *Regexp) ExecB(s []uint16, from int, b *Budget) []int {
	var m *vm
	c := re.exec(&m, s, from, b)
	if c != nil {
		c = append([]int(nil), c...)
	}
	re.release(m)
	return c
}

// noFilter turns the start-position filters off (tests only).
var noFilter bool

// exec is ExecB with a VM that is made on the first start position worth trying and kept in *mp for the next call on
// the same text. The returned captures are the VM's own: valid until its next run.
func (re *Regexp) exec(mp **vm, s []uint16, from int, b *Budget) []int {
	pre := re.filter()
	if pre.anchored {
		if from != 0 {
			return nil
		}
		return re.vmFor(mp, s, b).matchAt(0)
	}
	if noFilter || !pre.ok {
		for p := from; p <= len(s); p++ {
			if c := re.vmFor(mp, s, b).matchAt(p); c != nil {
				return c
			}
		}
		return nil
	}
	if pre.run != nil {
		return re.execRun(mp, s, from, b)
	}
	// The scan for a start position: positions are counted as steps in bulk, when a candidate is tried and every
	// clockEvery positions, so the deadline holds over a long text with no candidate.
	for p := from; p < len(s); {
		end := min(len(s), p+clockEvery)
		q := pre.next(s, p, end)
		b.count(q - p)
		if q == end {
			p = end
			continue
		}
		if c := re.vmFor(mp, s, b).matchAt(q); c != nil {
			return c
		}
		p = q + 1
	}
	return nil
}

// execRun is exec for a runThen pattern: it looks for the units that can end a run, and tries the starts of the run
// before each one (in order, so the first match found is the leftmost).
func (re *Regexp) execRun(mp **vm, s []uint16, from int, b *Budget) []int {
	pre := &re.pre
	p := from
	for q := from; q < len(s); q++ {
		if q-p >= clockEvery {
			b.count(q - p)
			p = q
		}
		if !pre.then.has(s[q]) {
			continue
		}
		r := q
		for r > from && pre.run.has(s[r-1]) {
			r--
		}
		b.count(q - p)
		p = q
		for c := r; c < q; c++ {
			if c > 0 && pre.prevNot != nil && pre.prevNot.has(s[c-1]) {
				continue
			}
			if m := re.vmFor(mp, s, b).matchAt(c); m != nil {
				return m
			}
		}
	}
	b.count(len(s) - p)
	return nil
}

func (re *Regexp) vmFor(mp **vm, s []uint16, b *Budget) *vm {
	if *mp == nil {
		if m, ok := re.vms.Get().(*vm); ok {
			if int64(len(s)) > 1<<31-1 {
				panic(ErrStack)
			}
			m.s, m.b = s, b
			*mp = m
		} else {
			*mp = re.newVM(s, b)
		}
	}
	return *mp
}

// release returns an idle VM to the pool, without the text, the budget or a deep stack.
func (re *Regexp) release(m *vm) {
	if m == nil {
		return
	}
	m.s, m.b = nil, nil
	m.stack.chunks = nil
	if cap(m.stack.small) > 4096 {
		m.stack.small = nil
	}
	re.vms.Put(m)
}

// count adds n steps to a budget that may be nil.
func (b *Budget) count(n int) {
	if b != nil && n > 0 {
		b.add(int64(n))
	}
}

// Test is RegExp.prototype.test from the start.
func (re *Regexp) Test(s []uint16) bool { return re.ExecB(s, 0, nil) != nil }

// TestB is Test under a budget.
func (re *Regexp) TestB(s []uint16, b *Budget) bool {
	var m *vm
	ok := re.exec(&m, s, 0, b) != nil
	re.release(m)
	return ok
}

// EachB calls f with each match of a g pattern in s (as FindAll finds them: an empty one advances by one), under b.
// The captures passed to f are valid only during the call.
func (re *Regexp) EachB(s []uint16, b *Budget, f func(caps []int)) {
	var m *vm
	for p := 0; p <= len(s); {
		c := re.exec(&m, s, p, b)
		if c == nil {
			re.release(m)
			return
		}
		c0, c1 := c[0], c[1]
		f(c)
		p = c1
		if c1 == c0 {
			p++
		}
	}
}

// FindAll is String.prototype.matchAll for a g pattern: each match, an empty one advancing by one.
func (re *Regexp) FindAll(s []uint16) [][]int { return re.FindAllB(s, nil) }

// FindAllB is FindAll under a budget.
func (re *Regexp) FindAllB(s []uint16, b *Budget) [][]int {
	var out [][]int
	re.EachB(s, b, func(c []int) { out = append(out, append([]int(nil), c...)) })
	return out
}

// ReplaceAll is String.prototype.replace with a g pattern and a function.
func (re *Regexp) ReplaceAll(s []uint16, f func(caps []int) []uint16) []uint16 {
	return re.ReplaceAllB(s, f, nil)
}

// ReplaceAllB is ReplaceAll under a budget. With no match it returns s itself (no copy).
func (re *Regexp) ReplaceAllB(s []uint16, f func(caps []int) []uint16, b *Budget) []uint16 {
	return re.ReplaceAllInto(nil, s, f, b)
}

// ReplaceAllInto is ReplaceAllB writing into dst's storage when it is large enough (dst must not overlap s): a caller
// that replaces in several passes can alternate two buffers instead of allocating one per pass. With no match it
// returns s itself.
func (re *Regexp) ReplaceAllInto(dst, s []uint16, f func(caps []int) []uint16, b *Budget) []uint16 {
	var out []uint16
	started := false
	last := 0
	re.EachB(s, b, func(c []int) {
		if !started {
			started = true
			if cap(dst) >= len(s) {
				out = dst[:0]
			} else {
				out = make([]uint16, 0, len(s)+len(s)/16+16)
			}
		}
		c0, c1 := c[0], c[1]
		out = append(out, s[last:c0]...)
		out = append(out, f(c)...)
		last = c1
	})
	if !started {
		return s
	}
	return append(out, s[last:]...)
}

// U16 is a Go string as JS code units, without an intermediate rune slice.
func U16(s string) []uint16 {
	n := 0
	for _, r := range s {
		n++
		if r >= 0x10000 {
			n++
		}
	}
	out := make([]uint16, 0, n)
	for _, r := range s {
		out = utf16.AppendRune(out, r)
	}
	return out
}

// Str is code units as a Go string (a lone surrogate becomes U+FFFD), encoded straight to UTF-8 without a rune slice.
func Str(u []uint16) string {
	n := 0
	for i := 0; i < len(u); i++ {
		c := u[i]
		switch {
		case c < 0x80:
			n++
		case c < 0x800:
			n += 2
		case utf16.IsSurrogate(rune(c)) && c < 0xdc00 && i+1 < len(u) && u[i+1] >= 0xdc00 && u[i+1] <= 0xdfff:
			n += 4
			i++
		default:
			n += 3 // a BMP character, or U+FFFD for a lone surrogate
		}
	}
	var sb strings.Builder
	sb.Grow(n)
	for i := 0; i < len(u); i++ {
		c := rune(u[i])
		switch {
		case c < 0x80:
			sb.WriteByte(byte(c))
			continue
		case utf16.IsSurrogate(c):
			if c < 0xdc00 && i+1 < len(u) && u[i+1] >= 0xdc00 && u[i+1] <= 0xdfff {
				c = utf16.DecodeRune(c, rune(u[i+1]))
				i++
			} else {
				c = utf8.RuneError
			}
		}
		sb.WriteRune(c)
	}
	return sb.String()
}
