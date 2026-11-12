package jsre

import "errors"

// The matcher is iterative: backtracking state lives on a heap-allocated stack, so Go's stack depth never depends on
// the input (a repeat of a group used to recurse once per iteration and overflow on about 1 MB). Only lookarounds
// nest, and their depth is the pattern's.

// ErrBudget is what a run past its Budget reports.
var ErrBudget = errors.New("jsre: step budget exceeded")

// Budget counts matcher steps (one per instruction, one per character a repeat consumes) across every call that is
// given it. Past Limit the matcher panics with ErrBudget; Run turns that back into an error.
type Budget struct {
	Steps, Limit int64
}

// Run calls f and returns ErrBudget if a matcher inside ran past the budget. Other panics propagate.
func (b *Budget) Run(f func()) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if r == ErrBudget {
				err = ErrBudget
				return
			}
			panic(r)
		}
	}()
	f()
	return nil
}

const (
	fBranch uint8 = iota // resume at pc, pos
	fCap                 // undo: caps[a] = b
	fReg                 // undo: regs[a] = b
	fStarG               // greedy repeat at base pos: retry with a-1 characters (b = min)
	fStarL               // lazy repeat at base pos: retry with a+1 characters (b = longest run)
	fAlt                 // retry alternative index b of the opAlt at a
)

type frame struct {
	kind    uint8
	pc, pos int
	a, b    int
}

type vm struct {
	s     []uint16
	caps  []int
	stack []frame
	b     *Budget
	icase bool
	prog  *prog
	regs  []int
}

func (re *Regexp) newVM(s []uint16, b *Budget) *vm {
	p := re.program()
	return &vm{s: s, caps: make([]int, 2*(re.ncap+1)), b: b, icase: re.icase, prog: p, regs: make([]int, p.nregs)}
}

func (m *vm) step(n int) {
	if m.b != nil {
		m.b.Steps += int64(n)
		if m.b.Steps > m.b.Limit {
			panic(ErrBudget)
		}
	}
}

func (m *vm) matchAt(pos int) []int {
	for i := range m.caps {
		m.caps[i] = -1
	}
	m.stack = m.stack[:0]
	end, ok := m.run(m.prog, m.regs, pos, -1)
	if !ok {
		return nil
	}
	m.caps[0], m.caps[1] = pos, end
	return m.caps
}

func (m *vm) wordAt(i int) bool { return i >= 0 && i < len(m.s) && isWord(m.s[i]) }

// altFrom returns the first alternative at index >= k whose first character can match at pos, or -1.
func (m *vm) altFrom(in *inst, k, pos int) int {
	var list []int
	switch {
	case pos >= len(m.s):
		list = in.atEnd
	case m.s[pos] < 128:
		list = in.ascii[m.s[pos]]
	default:
		for ; k < len(in.alts); k++ {
			if f := in.firsts[k]; f == nil || f.has(m.s[pos]) {
				return k
			}
		}
		return -1
	}
	for _, x := range list {
		if x >= k {
			return x
		}
	}
	return -1
}

// followDown is the largest count c in [lo, hi] after which the repeat's follow set can match (all counts when the
// follow set is unknown), or -1. A count it skips would fail at once on the next instruction.
func (m *vm) followDown(in *inst, base, hi, lo int) int {
	for c := hi; c >= lo; c-- {
		if in.follow == nil || (base+c < len(m.s) && in.follow.has(m.s[base+c])) {
			return c
		}
	}
	return -1
}

// followUp is followDown for a lazy repeat: the smallest count in [lo, hi].
func (m *vm) followUp(in *inst, base, lo, hi int) int {
	for c := lo; c <= hi; c++ {
		if in.follow == nil || (base+c < len(m.s) && in.follow.has(m.s[base+c])) {
			return c
		}
	}
	return -1
}

// run executes p from pos. With target >= 0 a match must end exactly there (lookbehind). Frames below the stack
// height at entry belong to the caller and are never popped here.
func (m *vm) run(p *prog, regs []int, pos, target int) (int, bool) {
	base := len(m.stack)
	pc := 0
	s := m.s
	for {
		m.step(1)
		in := &p.insts[pc]
		ok := true
		switch in.op {
		case opChar:
			if pos < len(s) && in.set.has(s[pos]) {
				pos++
				pc++
			} else {
				ok = false
			}
		case opStar:
			r := 0
			for (in.max < 0 || r < in.max) && pos+r < len(s) && in.set.has(s[pos+r]) {
				r++
			}
			m.step(r)
			if r < in.min {
				ok = false
				break
			}
			var c int
			if in.greedy {
				c = m.followDown(in, pos, r, in.min)
			} else {
				c = m.followUp(in, pos, in.min, r)
			}
			if c < 0 {
				ok = false
				break
			}
			if in.greedy && c > in.min {
				m.stack = append(m.stack, frame{kind: fStarG, pc: pc + 1, pos: pos, a: c, b: in.min})
			} else if !in.greedy && c < r {
				m.stack = append(m.stack, frame{kind: fStarL, pc: pc + 1, pos: pos, a: c, b: r})
			}
			pos += c
			pc++
		case opSplit:
			m.stack = append(m.stack, frame{kind: fBranch, pc: in.y, pos: pos})
			pc = in.x
		case opJmp:
			pc = in.x
		case opAlt:
			k := m.altFrom(in, 0, pos)
			if k < 0 {
				ok = false
				break
			}
			if next := m.altFrom(in, k+1, pos); next >= 0 {
				m.stack = append(m.stack, frame{kind: fAlt, pos: pos, a: pc, b: next})
			}
			pc = in.alts[k]
		case opSave:
			m.stack = append(m.stack, frame{kind: fCap, a: in.n, b: m.caps[in.n]})
			m.caps[in.n] = pos
			pc++
		case opClear:
			for i := 2 * in.capFirst; i < 2*in.capLast+2; i++ {
				if m.caps[i] != -1 {
					m.stack = append(m.stack, frame{kind: fCap, a: i, b: m.caps[i]})
					m.caps[i] = -1
				}
			}
			pc++
		case opRegSet:
			m.stack = append(m.stack, frame{kind: fReg, a: in.n, b: regs[in.n]})
			regs[in.n] = pos
			pc++
		case opRegCheck:
			if regs[in.n] == pos {
				ok = false
			} else {
				pc++
			}
		case opBegin:
			ok = pos == 0
			pc++
		case opEnd:
			ok = pos == len(s)
			pc++
		case opWordB:
			ok = m.wordAt(pos-1) != m.wordAt(pos)
			pc++
		case opNotWordB:
			ok = m.wordAt(pos-1) == m.wordAt(pos)
			pc++
		case opLook:
			ok = m.look(in, pos)
			pc++
		case opBackref:
			a, b := m.caps[2*in.n], m.caps[2*in.n+1]
			if a >= 0 && b >= 0 {
				l := b - a
				if pos+l > len(s) {
					ok = false
					break
				}
				for x := 0; x < l; x++ {
					u, v := s[a+x], s[pos+x]
					if u != v && !(m.icase && canon(u) == canon(v)) {
						ok = false
						break
					}
				}
				if ok {
					pos += l
				}
			}
			pc++
		case opMatch:
			if target < 0 || pos == target {
				return pos, true
			}
			ok = false
		}
		if ok {
			continue
		}
		// Backtrack: undo state changes until a frame that resumes matching.
		for resumed := false; !resumed; {
			if len(m.stack) == base {
				return 0, false
			}
			f := m.stack[len(m.stack)-1]
			m.stack = m.stack[:len(m.stack)-1]
			switch f.kind {
			case fCap:
				m.caps[f.a] = f.b
			case fReg:
				regs[f.a] = f.b
			case fBranch:
				pc, pos, resumed = f.pc, f.pos, true
			case fStarG:
				c := m.followDown(&p.insts[f.pc-1], f.pos, f.a-1, f.b)
				if c < 0 {
					continue
				}
				if c > f.b {
					m.stack = append(m.stack, frame{kind: fStarG, pc: f.pc, pos: f.pos, a: c, b: f.b})
				}
				pc, pos, resumed = f.pc, f.pos+c, true
			case fStarL:
				c := m.followUp(&p.insts[f.pc-1], f.pos, f.a+1, f.b)
				if c < 0 {
					continue
				}
				if c < f.b {
					m.stack = append(m.stack, frame{kind: fStarL, pc: f.pc, pos: f.pos, a: c, b: f.b})
				}
				pc, pos, resumed = f.pc, f.pos+c, true
			case fAlt:
				in := &p.insts[f.a]
				if next := m.altFrom(in, f.b+1, f.pos); next >= 0 {
					m.stack = append(m.stack, frame{kind: fAlt, pos: f.pos, a: f.a, b: next})
				}
				pc, pos, resumed = in.alts[f.b], f.pos, true
			}
		}
	}
}

// look runs a lookaround. Its own frames are dropped once it is decided (JS never backtracks into a lookaround); a
// positive one keeps the captures it set, recorded so the caller can still undo them.
func (m *vm) look(in *inst, pos int) bool {
	var saved []int
	if in.sub.hasCaps {
		saved = append([]int(nil), m.caps...)
	}
	height := len(m.stack)
	var regs []int
	if in.sub.nregs > 0 {
		regs = make([]int, in.sub.nregs)
	}
	found := false
	if !in.behind {
		_, found = m.run(in.sub, regs, pos, -1)
	} else {
		// The ported patterns hold no capture inside a lookbehind, so a forward match from each possible start finds
		// the same answer as JS's backward match. Compile keeps unbounded lookbehinds out (they would be O(n) here).
		lo := pos - in.max
		if lo < 0 {
			lo = 0
		}
		for j := pos - in.min; j >= lo && !found; j-- {
			_, found = m.run(in.sub, regs, j, pos)
			m.stack = m.stack[:height]
		}
	}
	m.stack = m.stack[:height]
	if saved == nil {
		return found != in.neg
	}
	if found && !in.neg {
		for i, v := range saved {
			if m.caps[i] != v {
				m.stack = append(m.stack, frame{kind: fCap, a: i, b: v})
			}
		}
		return true
	}
	copy(m.caps, saved)
	return found != in.neg
}
