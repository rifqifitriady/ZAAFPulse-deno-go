package jsre

import (
	"errors"
	"time"
)

// The matcher is iterative: backtracking state lives on a heap-allocated stack, so Go's stack depth never depends on
// the input (a repeat of a group used to recurse once per iteration and overflow on about 1 MB). Only lookarounds
// nest, and their depth is the pattern's.

// ErrBudget is what a run past its Budget reports.
var ErrBudget = errors.New("jsre: step budget exceeded")

// ErrStack is what a run reports when its backtrack stack would pass MaxFrames. It is raised whether or not a Budget
// is given (a nil-budget call panics with it); Budget.Run turns it into an error like ErrBudget.
var ErrStack = errors.New("jsre: backtrack stack limit exceeded")

// MaxFrames caps the backtrack stack of one run: 2,097,152 frames of 20 bytes, 40 MiB. V8 caps its own backtrack
// stack too (it throws a RangeError). No fixture or legitimate text measured comes near it; a crafted repeat of a
// group over about two million iterations (a 4 MB run of "a a a …") does, and then fails closed.
const MaxFrames = 2 << 20

// ErrDeadline is what a run past its Budget's Deadline reports.
var ErrDeadline = errors.New("jsre: deadline exceeded")

// clockEvery is how many steps pass between two reads of the clock (about 0.1 ms of matching).
const clockEvery = 1 << 16

// Budget counts matcher steps (one per instruction, one per character a repeat consumes, one per start position the
// filter skips) across every call that is given it, and holds an optional wall-clock deadline. Past Limit the
// matcher panics with ErrBudget, at or past Deadline (read every clockEvery steps) with ErrDeadline; Run turns either
// back into an error. Limit may be raised between runs (Extend), never lowered.
type Budget struct {
	Steps, Limit int64
	Deadline     time.Time        // zero: none
	Now          func() time.Time // nil: time.Now
	next         int64            // the step count at which the slow check runs next
}

// Extend raises Limit by n, up to max (never above it, never lowering Limit).
func (b *Budget) Extend(n, max int64) {
	if l := min(b.Limit+n, max); l > b.Limit {
		b.Limit = l
	}
}

// Expired reports whether the deadline has passed (reads the clock).
func (b *Budget) Expired() bool {
	if b.Deadline.IsZero() {
		return false
	}
	now := time.Now
	if b.Now != nil {
		now = b.Now
	}
	return !now().Before(b.Deadline)
}

// add counts n steps; the slow path checks the limit and, every clockEvery steps, the clock.
func (b *Budget) add(n int64) {
	b.Steps += n
	if b.Steps > b.next {
		b.slow()
	}
}

func (b *Budget) slow() {
	if b.Steps > b.Limit {
		panic(ErrBudget)
	}
	if b.Deadline.IsZero() {
		b.next = b.Limit
		return
	}
	if b.Expired() {
		panic(ErrDeadline)
	}
	b.next = min(b.Limit, b.Steps+clockEvery)
}

// IsLimit reports whether a recovered panic value is one of the limits (ErrBudget, ErrStack, ErrDeadline).
func IsLimit(r any) bool { return r == ErrBudget || r == ErrStack || r == ErrDeadline }

// Run calls f and returns ErrBudget, ErrStack or ErrDeadline if a matcher inside ran past a limit. Other panics
// propagate.
func (b *Budget) Run(f func()) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if IsLimit(r) {
				err = r.(error)
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

// packed is a frame as the stack holds it: 20 bytes instead of 40 (offsets, counts and program counters all fit in 32
// bits: newVM refuses a text of 2^31 code units or more).
type packed struct {
	pc, pos, a, b int32
	kind          uint8
}

func (f frame) pack() packed {
	return packed{kind: f.kind, pc: int32(f.pc), pos: int32(f.pos), a: int32(f.a), b: int32(f.b)}
}

func (p packed) unpack() frame {
	return frame{kind: p.kind, pc: int(p.pc), pos: int(p.pos), a: int(p.a), b: int(p.b)}
}

// frames is the backtrack stack: the first chunkFrames frames in a slice grown by append (most runs push few), the
// rest in chunks of chunkFrames, so a deep run grows without copying and leaves no discarded smaller copies behind (a
// slice grown by append to two million frames left as much garbage again as it held).
type frames struct {
	small  []packed
	chunks [][]packed
	n      int
}

const chunkShift = 14 // 16,384 frames, 320 KiB per chunk

const chunkFrames = 1 << chunkShift

func (st *frames) push(f packed) {
	if st.n < chunkFrames {
		if st.n < len(st.small) {
			st.small[st.n] = f
		} else {
			st.small = append(st.small, f)
		}
		st.n++
		return
	}
	c := st.n>>chunkShift - 1
	if c == len(st.chunks) {
		st.chunks = append(st.chunks, make([]packed, chunkFrames))
	}
	st.chunks[c][st.n&(chunkFrames-1)] = f
	st.n++
}

func (st *frames) pop() packed {
	st.n--
	if st.n < chunkFrames {
		return st.small[st.n]
	}
	return st.chunks[st.n>>chunkShift-1][st.n&(chunkFrames-1)]
}

type vm struct {
	s     []uint16
	caps  []int
	stack frames
	b     *Budget
	icase bool
	prog  *prog
	regs  []int
}

func (re *Regexp) newVM(s []uint16, b *Budget) *vm {
	if int64(len(s)) > 1<<31-1 {
		panic(ErrStack) // the stack's 32-bit offsets cannot address it: refused like a run past the stack cap
	}
	p := re.program()
	return &vm{s: s, caps: make([]int, 2*(re.ncap+1)), b: b, icase: re.icase, prog: p, regs: make([]int, p.nregs)}
}

func (m *vm) step(n int) {
	if m.b != nil {
		m.b.add(int64(n))
	}
}

// push adds a backtrack frame; past MaxFrames the run stops with ErrStack.
func (m *vm) push(f frame) {
	if m.stack.n >= MaxFrames {
		panic(ErrStack)
	}
	m.stack.push(f.pack())
}

func (m *vm) matchAt(pos int) []int {
	for i := range m.caps {
		m.caps[i] = -1
	}
	m.stack.n = 0
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
	base := m.stack.n
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
				m.push(frame{kind: fStarG, pc: pc + 1, pos: pos, a: c, b: in.min})
			} else if !in.greedy && c < r {
				m.push(frame{kind: fStarL, pc: pc + 1, pos: pos, a: c, b: r})
			}
			pos += c
			pc++
		case opSplit:
			m.push(frame{kind: fBranch, pc: in.y, pos: pos})
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
				m.push(frame{kind: fAlt, pos: pos, a: pc, b: next})
			}
			pc = in.alts[k]
		case opSave:
			m.push(frame{kind: fCap, a: in.n, b: m.caps[in.n]})
			m.caps[in.n] = pos
			pc++
		case opClear:
			for i := 2 * in.capFirst; i < 2*in.capLast+2; i++ {
				if m.caps[i] != -1 {
					m.push(frame{kind: fCap, a: i, b: m.caps[i]})
					m.caps[i] = -1
				}
			}
			pc++
		case opRegSet:
			m.push(frame{kind: fReg, a: in.n, b: regs[in.n]})
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
			if m.stack.n == base {
				return 0, false
			}
			f := m.stack.pop().unpack()
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
					m.push(frame{kind: fStarG, pc: f.pc, pos: f.pos, a: c, b: f.b})
				}
				pc, pos, resumed = f.pc, f.pos+c, true
			case fStarL:
				c := m.followUp(&p.insts[f.pc-1], f.pos, f.a+1, f.b)
				if c < 0 {
					continue
				}
				if c < f.b {
					m.push(frame{kind: fStarL, pc: f.pc, pos: f.pos, a: c, b: f.b})
				}
				pc, pos, resumed = f.pc, f.pos+c, true
			case fAlt:
				in := &p.insts[f.a]
				if next := m.altFrom(in, f.b+1, f.pos); next >= 0 {
					m.push(frame{kind: fAlt, pos: f.pos, a: f.a, b: next})
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
	height := m.stack.n
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
			m.stack.n = height
		}
	}
	m.stack.n = height
	if saved == nil {
		return found != in.neg
	}
	if found && !in.neg {
		for i, v := range saved {
			if m.caps[i] != v {
				m.push(frame{kind: fCap, a: i, b: v})
			}
		}
		return true
	}
	copy(m.caps, saved)
	return found != in.neg
}
