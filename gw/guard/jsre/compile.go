package jsre

import (
	"errors"
	"sync"
)

type opcode uint8

const (
	opChar     opcode = iota // one code unit against atom
	opStar                   // a repeat of one character, run without backtracking frames per character
	opSplit                  // try x, then y
	opJmp                    // go to x
	opAlt                    // try alts in order, skipping those whose first character cannot match
	opSave                   // caps[n] = pos
	opClear                  // reset caps of groups capFirst..capLast (a new iteration of a repeat)
	opRegSet                 // regs[n] = pos (start of a repeat iteration)
	opRegCheck               // fail when an iteration matched nothing (ECMA-262 RepeatMatcher)
	opBegin
	opEnd
	opWordB
	opNotWordB
	opLook // lookahead or lookbehind: sub
	opBackref
	opMatch
)

type inst struct {
	op                  opcode
	atom                *node
	x, y, n             int
	alts                []int
	firsts              []*bitset // per alt; nil when the alt can match without consuming a character
	min, max            int
	greedy, neg, behind bool
	sub                 *prog
	capFirst, capLast   int
	set                 *bitset     // opChar, opStar: the code units the atom matches (i flag applied)
	follow              *bitset     // opStar: what must come next, or nil when unknown
	ascii               *[128][]int // opAlt: the viable alternatives for each ASCII code unit
	atEnd               []int       // opAlt: the viable alternatives at the end of the input
}

type prog struct {
	insts   []inst
	nregs   int
	hasCaps bool // the program sets or clears a capture
}

// maxInsts bounds the expansion of counted repeats of groups.
const maxInsts = 200000

type compiler struct {
	follow *bitset // set by kSeq for the repeat it generates next
	p      *prog
	icase  bool
	firsts firstCache
	err    error
}

func (c *compiler) emit(i inst) int {
	if len(c.p.insts) >= maxInsts {
		c.err = errors.New("jsre: pattern too large after expanding counted repeats")
	}
	c.p.insts = append(c.p.insts, i)
	return len(c.p.insts) - 1
}

func compileProg(n *node, icase bool, firsts firstCache) (*prog, error) {
	c := &compiler{p: &prog{}, icase: icase, firsts: firsts}
	c.gen(n)
	c.emit(inst{op: opMatch})
	for _, in := range c.p.insts {
		c.p.hasCaps = c.p.hasCaps || in.op == opSave || in.op == opClear
	}
	return c.p, c.err
}

func (c *compiler) gen(n *node) {
	if c.err != nil {
		return
	}
	follow := c.follow // meant for this node only, never for what it contains
	c.follow = nil
	switch n.kind {
	case kChar, kSet, kClass:
		c.emit(inst{op: opChar, atom: n, set: c.atomSet(n)})
	case kSeq:
		for i, s := range n.subs {
			// A single-character repeat followed by something that must start with a known character can skip the
			// counts after which that character is absent (the VM still tries them in JS order; skipped ones fail).
			if s.kind == kRep && i+1 < len(n.subs) {
				c.follow = c.followOf(n.subs[i+1:])
			}
			c.gen(s)
			c.follow = nil
		}
	case kAlt:
		at := c.emit(inst{op: opAlt})
		var alts, jumps []int
		var firsts []*bitset
		for _, s := range n.subs {
			alts = append(alts, len(c.p.insts))
			f, empty := c.firsts.of(s, c.icase)
			if empty {
				f = nil
			}
			firsts = append(firsts, f)
			c.gen(s)
			jumps = append(jumps, c.emit(inst{op: opJmp}))
		}
		end := len(c.p.insts)
		for _, j := range jumps {
			c.p.insts[j].x = end
		}
		in := &c.p.insts[at]
		in.alts, in.firsts = alts, firsts
		in.ascii = new([128][]int)
		for u := 0; u < 128; u++ {
			for k, f := range firsts {
				if f == nil || f.has(uint16(u)) {
					in.ascii[u] = append(in.ascii[u], k)
				}
			}
		}
		for k, f := range firsts {
			if f == nil {
				in.atEnd = append(in.atEnd, k)
			}
		}
	case kGroup:
		c.emit(inst{op: opSave, n: 2 * n.idx})
		c.gen(n.subs[0])
		c.emit(inst{op: opSave, n: 2*n.idx + 1})
	case kRep:
		c.genRep(n, follow)
	case kBackref:
		c.emit(inst{op: opBackref, n: n.idx})
	case kBegin:
		c.emit(inst{op: opBegin})
	case kEnd:
		c.emit(inst{op: opEnd})
	case kWordB:
		c.emit(inst{op: opWordB})
	case kNotWordB:
		c.emit(inst{op: opNotWordB})
	case kLookahead, kLookbehind:
		sub, err := compileProg(n.subs[0], c.icase, c.firsts)
		if err != nil {
			c.err = err
			return
		}
		c.emit(inst{op: opLook, sub: sub, neg: n.neg, behind: n.kind == kLookbehind, min: n.min, max: n.max})
	}
}

func (c *compiler) genRep(n *node, follow *bitset) {
	a := n.subs[0]
	if (a.kind == kChar || a.kind == kSet || a.kind == kClass) && n.capLast < 0 {
		c.emit(inst{op: opStar, atom: a, set: c.atomSet(a), follow: follow, min: n.min, max: n.max, greedy: n.greedy})
		return
	}
	iteration := func(check bool) {
		if n.capLast >= 0 {
			c.emit(inst{op: opClear, capFirst: n.capFirst, capLast: n.capLast})
		}
		if !check {
			c.gen(a)
			return
		}
		r := c.p.nregs
		c.p.nregs++
		c.emit(inst{op: opRegSet, n: r})
		c.gen(a)
		c.emit(inst{op: opRegCheck, n: r})
	}
	// The empty-iteration check (and its register frame) is only needed when the atom can match nothing.
	if lo, _ := width(a); lo > 0 {
		check := iteration
		iteration = func(bool) { check(false) }
	}
	for k := 0; k < n.min && c.err == nil; k++ {
		iteration(false) // a mandatory iteration may match empty
	}
	split := func() int { return c.emit(inst{op: opSplit}) }
	setSplit := func(at, body, exit int) {
		if n.greedy {
			c.p.insts[at].x, c.p.insts[at].y = body, exit
		} else {
			c.p.insts[at].x, c.p.insts[at].y = exit, body
		}
	}
	if n.max < 0 {
		loop := split()
		iteration(true)
		c.emit(inst{op: opJmp, x: loop})
		setSplit(loop, loop+1, len(c.p.insts))
		return
	}
	var splits []int
	for k := n.min; k < n.max && c.err == nil; k++ {
		splits = append(splits, split())
		iteration(true)
	}
	end := len(c.p.insts)
	for _, s := range splits {
		setSplit(s, s+1, end)
	}
}

// followOf is the first-character set of a sequence tail, or nil when the tail can match empty.
func (c *compiler) followOf(rest []*node) *bitset {
	f, empty := firstOf(&node{kind: kSeq, subs: rest}, c.icase)
	if empty {
		return nil
	}
	return f
}

// atomSet is the set of code units a one-character node matches; the first-character sets are the same thing.
func (c *compiler) atomSet(n *node) *bitset {
	f, _ := c.firsts.of(n, c.icase)
	return f
}

// program compiles the pattern on first use.
type lazyProg struct {
	once sync.Once
	p    *prog
	err  error
}

func (re *Regexp) program() *prog {
	re.prog.once.Do(func() {
		re.prog.p, re.prog.err = compileProg(re.root, re.icase, firstCache{})
	})
	if re.prog.err != nil {
		panic(re.prog.err)
	}
	return re.prog.p
}

// progSize is an upper bound of the instructions n compiles to.
func progSize(n *node) int {
	switch n.kind {
	case kSeq, kAlt:
		t := 2
		for _, s := range n.subs {
			t += progSize(s) + 1
			if t > maxInsts {
				return t
			}
		}
		return t
	case kGroup:
		return progSize(n.subs[0]) + 2
	case kRep:
		a := progSize(n.subs[0]) + 4
		copies := n.min + 1
		if n.max > n.min {
			copies = n.max
		}
		if a*copies > maxInsts {
			return maxInsts + 1
		}
		return a * copies
	case kLookahead, kLookbehind:
		return progSize(n.subs[0]) + 2
	}
	return 1
}
