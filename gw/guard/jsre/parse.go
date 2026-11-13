// Package jsre runs JavaScript regular expressions (ECMAScript, non-unicode mode, with the Annex B syntax V8 accepts)
// with JavaScript's backtracking semantics, over UTF-16 code units. The guardrail detectors (lib/signals.js,
// lib/evals/pii.js) use lookbehind, lookahead, lazy quantifiers and a backreference, which Go's RE2-based regexp
// cannot express; running the same pattern sources here keeps the Go port provably equal (gw/testdata/guard).
//
// Supported: alternation, groups (capturing, (?:), (?=), (?!), (?<=), (?<!)), quantifiers * + ? {n} {n,} {n,m} and
// their lazy forms, character classes with ranges and the \d \w \s escapes, ., ^, $, \b, \B, backreferences \1-\9,
// escapes \n \r \t \v \f \0 \xHH \uHHHH \cX, and the flags i and g (g is the caller's business: Exec takes a start).
// Not supported (not used by the ported patterns): the u, y, m, s flags, named groups, \k, \p.
package jsre

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
)

type parser struct {
	src    []rune
	pos    int
	ncap   int // groups opened so far
	total  int // capturing groups in the pattern
	icase  bool
	errMsg string
}

// Regexp is a compiled pattern.
type Regexp struct {
	src   string
	flags string
	root  *node
	ncap  int
	icase bool
	pre   prefilter
	prog  lazyProg
	vms   sync.Pool // idle *vm, reused across calls (ExecB, TestB, EachB)
}

// String is the pattern source.
func (re *Regexp) String() string { return re.src }

// Flags is the flag string the pattern was compiled with.
func (re *Regexp) Flags() string { return re.flags }

// NumCap is the number of capturing groups.
func (re *Regexp) NumCap() int { return re.ncap }

// Compile parses a pattern with flags ("", "i", "g", "gi").
func Compile(src, flags string) (*Regexp, error) {
	for _, f := range flags {
		if f != 'i' && f != 'g' {
			return nil, fmt.Errorf("jsre: flag %q not supported", f)
		}
	}
	p := &parser{src: []rune(src), icase: strings.ContainsRune(flags, 'i')}
	p.total = countGroups(p.src)
	n, err := p.disjunction()
	if err == nil && p.pos < len(p.src) {
		err = fmt.Errorf("jsre: unmatched ) at %d in /%s/", p.pos, src)
	}
	if err != nil {
		return nil, err
	}
	re := &Regexp{src: src, flags: flags, root: n, ncap: p.total, icase: p.icase}
	if progSize(n) > maxInsts {
		return nil, fmt.Errorf("jsre: /%s/ is too large after expanding counted repeats", src)
	}
	return re, nil
}

// MustCompile is Compile for constant patterns.
func MustCompile(src, flags string) *Regexp {
	re, err := Compile(src, flags)
	if err != nil {
		panic(err)
	}
	return re
}

func countGroups(src []rune) int {
	n, inClass := 0, false
	for i := 0; i < len(src); i++ {
		switch c := src[i]; {
		case c == '\\':
			i++
		case inClass:
			inClass = c != ']'
		case c == '[':
			inClass = true
		case c == '(' && (i+1 >= len(src) || src[i+1] != '?'):
			n++
		}
	}
	return n
}

func (p *parser) fail(format string, args ...any) error {
	return fmt.Errorf("jsre: "+format+" in /%s/", append(args, string(p.src))...)
}

func (p *parser) peek() rune {
	if p.pos < len(p.src) {
		return p.src[p.pos]
	}
	return -1
}

func (p *parser) eat(s string) bool {
	r := []rune(s)
	if p.pos+len(r) > len(p.src) {
		return false
	}
	for i, c := range r {
		if p.src[p.pos+i] != c {
			return false
		}
	}
	p.pos += len(r)
	return true
}

func (p *parser) disjunction() (*node, error) {
	var alts []*node
	for {
		a, err := p.alternative()
		if err != nil {
			return nil, err
		}
		alts = append(alts, a)
		if !p.eat("|") {
			break
		}
	}
	if len(alts) == 1 {
		return alts[0], nil
	}
	return &node{kind: kAlt, subs: alts}, nil
}

func (p *parser) alternative() (*node, error) {
	var terms []*node
	for p.pos < len(p.src) && p.peek() != '|' && p.peek() != ')' {
		t, err := p.term()
		if err != nil {
			return nil, err
		}
		terms = append(terms, t)
	}
	return &node{kind: kSeq, subs: terms}, nil
}

func (p *parser) term() (*node, error) {
	start := p.pos
	switch c := p.peek(); c {
	case '^':
		p.pos++
		return &node{kind: kBegin}, nil
	case '$':
		p.pos++
		return &node{kind: kEnd}, nil
	case '*', '+', '?':
		return nil, p.fail("nothing to repeat at %d", start)
	case '{':
		if _, _, ok := p.quantBounds(); ok {
			return nil, p.fail("nothing to repeat at %d", start)
		}
	case '\\':
		if p.eat(`\b`) {
			return &node{kind: kWordB}, nil
		}
		if p.eat(`\B`) {
			return &node{kind: kNotWordB}, nil
		}
	case '(':
		if p.eat("(?=") || p.eat("(?!") {
			neg := p.src[p.pos-1] == '!'
			sub, err := p.group()
			if err != nil {
				return nil, err
			}
			// Annex B allows a quantifier after a lookahead; none of the ported patterns uses one.
			return &node{kind: kLookahead, neg: neg, subs: []*node{sub}}, nil
		}
		if p.eat("(?<=") || p.eat("(?<!") {
			neg := p.src[p.pos-1] == '!'
			sub, err := p.group()
			if err != nil {
				return nil, err
			}
			lo, hi := width(sub)
			if hi < 0 {
				return nil, p.fail("unbounded lookbehind at %d is not supported", start)
			}
			return &node{kind: kLookbehind, neg: neg, subs: []*node{sub}, min: lo, max: hi}, nil
		}
	}
	atom, err := p.atom()
	if err != nil {
		return nil, err
	}
	return p.quantifier(atom)
}

func (p *parser) group() (*node, error) {
	n, err := p.disjunction()
	if err != nil {
		return nil, err
	}
	if !p.eat(")") {
		return nil, p.fail("missing )")
	}
	return n, nil
}

func (p *parser) atom() (*node, error) {
	c := p.peek()
	switch c {
	case '.':
		p.pos++
		return &node{kind: kSet, set: anyChar}, nil
	case '[':
		p.pos++
		return p.class()
	case '(':
		if p.eat("(?:") {
			return p.group()
		}
		if p.eat("(?") {
			return nil, p.fail("group syntax not supported at %d", p.pos)
		}
		p.pos++
		p.ncap++
		idx := p.ncap
		sub, err := p.group()
		if err != nil {
			return nil, err
		}
		return &node{kind: kGroup, idx: idx, subs: []*node{sub}}, nil
	case ')':
		return nil, p.fail("unmatched ) at %d", p.pos)
	case '\\':
		p.pos++
		return p.atomEscape()
	}
	p.pos++
	return p.lit(uint16(c)), nil
}

func (p *parser) lit(c uint16) *node { return &node{kind: kChar, ch: c} }

func (p *parser) atomEscape() (*node, error) {
	c := p.peek()
	if c >= '1' && c <= '9' {
		start := p.pos
		for p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
			p.pos++
		}
		n, _ := strconv.Atoi(string(p.src[start:p.pos]))
		if n <= p.total {
			return &node{kind: kBackref, idx: n}, nil
		}
		return nil, p.fail("octal escapes are not supported")
	}
	if s := classEscape(c); s != nil {
		p.pos++
		return &node{kind: kSet, set: s}, nil
	}
	ch, err := p.charEscape()
	if err != nil {
		return nil, err
	}
	return p.lit(ch), nil
}

// charEscape reads the escape after a backslash (not a class escape) and returns its code unit.
func (p *parser) charEscape() (uint16, error) {
	c := p.peek()
	if c < 0 {
		return 0, p.fail("\\ at end of pattern")
	}
	p.pos++
	switch c {
	case 'n':
		return '\n', nil
	case 'r':
		return '\r', nil
	case 't':
		return '\t', nil
	case 'v':
		return '\v', nil
	case 'f':
		return '\f', nil
	case '0':
		if d := p.peek(); d >= '0' && d <= '9' {
			return 0, p.fail("octal escapes are not supported")
		}
		return 0, nil
	case 'x', 'u':
		n := 2
		if c == 'u' {
			n = 4
		}
		if p.pos+n <= len(p.src) {
			if v, err := strconv.ParseUint(string(p.src[p.pos:p.pos+n]), 16, 16); err == nil {
				p.pos += n
				return uint16(v), nil
			}
		}
		return uint16(c), nil // Annex B: an incomplete escape is the letter itself
	case 'c':
		if d := p.peek(); (d >= 'a' && d <= 'z') || (d >= 'A' && d <= 'Z') {
			p.pos++
			return uint16(d % 32), nil
		}
		p.pos--
		return '\\', nil // Annex B: \c without a letter is a backslash, then c
	}
	return uint16(c), nil // identity escape
}

func (p *parser) class() (*node, error) {
	neg := p.eat("^")
	var items []classItem
	for {
		if p.pos >= len(p.src) {
			return nil, p.fail("missing ]")
		}
		if p.eat("]") {
			break
		}
		a, aSet, err := p.classAtom()
		if err != nil {
			return nil, err
		}
		if aSet == nil && p.peek() == '-' && p.pos+1 < len(p.src) && p.src[p.pos+1] != ']' {
			save := p.pos
			p.pos++
			b, bSet, err := p.classAtom()
			if err != nil {
				return nil, err
			}
			if bSet == nil {
				if b < a {
					return nil, p.fail("range out of order in character class")
				}
				items = append(items, classItem{lo: a, hi: b})
				continue
			}
			p.pos = save // Annex B: a range with a class escape is a literal '-'
		}
		if aSet != nil {
			items = append(items, classItem{set: aSet})
		} else {
			items = append(items, classItem{lo: a, hi: a})
		}
	}
	return &node{kind: kClass, cls: &charClass{items: items, neg: neg}}, nil
}

// classAtom returns a code unit, or a set for \d \w \s and their negations.
func (p *parser) classAtom() (uint16, func(uint16) bool, error) {
	c := p.peek()
	if c != '\\' {
		p.pos++
		return uint16(c), nil, nil
	}
	p.pos++
	d := p.peek()
	if s := classEscape(d); s != nil {
		p.pos++
		return 0, s, nil
	}
	if d == 'b' {
		p.pos++
		return '\b', nil, nil
	}
	if d == '-' {
		p.pos++
		return '-', nil, nil
	}
	if d >= '1' && d <= '9' {
		return 0, nil, p.fail("octal escapes are not supported")
	}
	ch, err := p.charEscape()
	return ch, nil, err
}

func classEscape(c rune) func(uint16) bool {
	switch c {
	case 'd':
		return isDigit
	case 'D':
		return func(u uint16) bool { return !isDigit(u) }
	case 'w':
		return isWord
	case 'W':
		return func(u uint16) bool { return !isWord(u) }
	case 's':
		return isSpace
	case 'S':
		return func(u uint16) bool { return !isSpace(u) }
	}
	return nil
}

// quantBounds parses {n}, {n,} or {n,m} at the current position without consuming it.
func (p *parser) quantBounds() (lo, hi int, ok bool) {
	i := p.pos
	if i >= len(p.src) || p.src[i] != '{' {
		return 0, 0, false
	}
	i++
	num := func() (int, bool) {
		s := i
		for i < len(p.src) && p.src[i] >= '0' && p.src[i] <= '9' {
			i++
		}
		if i == s {
			return 0, false
		}
		n, err := strconv.Atoi(string(p.src[s:i]))
		return n, err == nil
	}
	lo, ok = num()
	if !ok {
		return 0, 0, false
	}
	hi = lo
	if i < len(p.src) && p.src[i] == ',' {
		i++
		hi = -1
		if i < len(p.src) && p.src[i] != '}' {
			if hi, ok = num(); !ok {
				return 0, 0, false
			}
		}
	}
	if i >= len(p.src) || p.src[i] != '}' {
		return 0, 0, false
	}
	return lo, hi, true
}

func (p *parser) quantifier(atom *node) (*node, error) {
	lo, hi := 0, 0
	switch c := p.peek(); c {
	case '*':
		p.pos++
		lo, hi = 0, -1
	case '+':
		p.pos++
		lo, hi = 1, -1
	case '?':
		p.pos++
		lo, hi = 0, 1
	case '{':
		var ok bool
		if lo, hi, ok = p.quantBounds(); !ok {
			return atom, nil // Annex B: a literal {
		}
		for p.src[p.pos] != '}' {
			p.pos++
		}
		p.pos++
		if hi >= 0 && hi < lo {
			return nil, p.fail("numbers out of order in {} quantifier")
		}
	default:
		return atom, nil
	}
	greedy := !p.eat("?")
	first, last := groupRange(atom)
	return &node{kind: kRep, subs: []*node{atom}, min: lo, max: hi, greedy: greedy, capFirst: first, capLast: last}, nil
}

// groupRange is the range of capture indices inside n (0, -1 when none).
func groupRange(n *node) (int, int) {
	first, last := 0, -1
	var walk func(*node)
	walk = func(x *node) {
		if x.kind == kGroup {
			if first == 0 || x.idx < first {
				first = x.idx
			}
			if x.idx > last {
				last = x.idx
			}
		}
		for _, s := range x.subs {
			walk(s)
		}
	}
	walk(n)
	return first, last
}

// width is the least and greatest number of code units n can match (-1: unbounded).
func width(n *node) (int, int) {
	switch n.kind {
	case kChar, kSet, kClass:
		return 1, 1
	case kSeq:
		lo, hi := 0, 0
		for _, s := range n.subs {
			a, b := width(s)
			lo += a
			if hi >= 0 {
				if b < 0 {
					hi = -1
				} else {
					hi += b
				}
			}
		}
		return lo, hi
	case kAlt:
		lo, hi := -1, 0
		for _, s := range n.subs {
			a, b := width(s)
			if lo < 0 || a < lo {
				lo = a
			}
			if hi >= 0 && (b < 0 || b > hi) {
				hi = b
			}
		}
		return lo, hi
	case kGroup:
		return width(n.subs[0])
	case kRep:
		a, b := width(n.subs[0])
		hi := -1
		if n.max >= 0 && b >= 0 {
			hi = n.max * b
		}
		return n.min * a, hi
	case kBackref:
		return 0, -1
	}
	return 0, 0 // assertions
}
