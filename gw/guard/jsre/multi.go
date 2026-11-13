package jsre

import "math/bits"

// Multi finds the first match of each of up to 64 patterns in one scan of a text, as ExecB(s, 0, b) on each would:
// at each position a table gives the patterns whose start-position filter accepts it (from the code unit there, the
// next one and whether the one before is a word character), and only those run. A guard text is scanned once instead
// of once per pattern.
type Multi struct {
	res   []*Regexp
	scan  uint64 // the patterns found through the table; the others (no filter, or anchored) run on their own
	tab   [2][129][130]uint64
	first [2][129]uint64 // the union over the next code unit: a small table that rejects most positions
}

// NewMulti prepares the patterns (at most 64).
func NewMulti(res []*Regexp) *Multi {
	if len(res) > 64 {
		panic("jsre: NewMulti takes at most 64 patterns")
	}
	mu := &Multi{res: res}
	for k, re := range res {
		re.Prepare()
		f := re.filter()
		if !f.ok || f.anchored {
			continue
		}
		bit := uint64(1) << k
		mu.scan |= bit
		for pw := range 2 {
			if pw == 1 && f.wordOnly {
				continue
			}
			for a := range 129 {
				if a < 128 && !f.ascii[a] {
					continue
				}
				for nb := range 130 {
					if !f.pairs || f.one.has(a) || (nb < 129 && f.two[a].has(nb)) {
						mu.tab[pw][a][nb] |= bit
						mu.first[pw][a] |= bit
					}
				}
			}
		}
	}
	return mu
}

// next is the first position in [q, end) where the table accepts one of want, with those patterns, or end.
func (mu *Multi) next(s []uint16, q, end int, want uint64) (int, uint64) {
	pw := 0
	if q > 0 && s[q-1] < 128 && wordTab[s[q-1]] {
		pw = 1
	}
	for ; q < end; q++ {
		u := s[q]
		a := sym(u)
		if mu.first[pw][a]&want != 0 {
			nb := 129
			if q+1 < len(s) {
				nb = sym(s[q+1])
			}
			if mask := mu.tab[pw][a][nb] & want; mask != 0 {
				return q, mask
			}
		}
		pw = 0
		if u < 128 && wordTab[u] {
			pw = 1
		}
	}
	return end, 0
}

// MultiState keeps one VM per pattern across the calls of one text; Release returns them.
type MultiState struct {
	vms []*vm
}

// Release returns the state's VMs to their patterns' pools.
func (mu *Multi) Release(st *MultiState) {
	for k, m := range st.vms {
		if m != nil {
			mu.res[k].release(m)
			st.vms[k] = nil
		}
	}
}

// FirstB sets out[k] to the first match of pattern k in s (a copy of its captures, or nil) for each k in active, and
// leaves the others alone. Steps count against b as in ExecB.
func (mu *Multi) FirstB(st *MultiState, s []uint16, active uint64, b *Budget, out [][]int) {
	if st.vms == nil {
		st.vms = make([]*vm, len(mu.res))
	}
	for rest := active &^ mu.scan; rest != 0; rest &= rest - 1 {
		k := bits.TrailingZeros64(rest)
		out[k] = mu.res[k].ExecB(s, 0, b)
	}
	want := active & mu.scan
	for k := range mu.res {
		if want&(1<<k) != 0 {
			out[k] = nil
			if st.vms[k] != nil {
				st.vms[k].s, st.vms[k].b = s, b
			}
		}
	}
	for p := 0; p < len(s) && want != 0; {
		end := min(len(s), p+clockEvery)
		q, mask := mu.next(s, p, end, want)
		b.count(q - p)
		if q == end {
			p = end
			continue
		}
		for ; mask != 0; mask &= mask - 1 {
			k := bits.TrailingZeros64(mask)
			re := mu.res[k]
			if (s[q] >= 128 && !re.pre.set.has(s[q])) || (q > 0 && re.pre.prevNot != nil && re.pre.prevNot.has(s[q-1])) ||
				(re.pre.lits != nil && !re.pre.lits.starts(s, q, re.icase)) {
				continue
			}
			m := st.vms[k]
			if m == nil {
				m = re.vmFor(&st.vms[k], s, b)
			}
			if c := m.matchAt(q); c != nil {
				out[k] = append([]int(nil), c...)
				want &^= 1 << k
			}
		}
		p = q + 1
	}
}

// AllB calls f(k, caps) for every match of each pattern k in active, as repeated ExecB from the position f returns
// would find them, in order of position across patterns (returning caps[1], or caps[1]+1 for an empty match, is
// EachB; a position past the end stops that pattern). The captures are valid only during the call. f must not change
// s.
func (mu *Multi) AllB(st *MultiState, s []uint16, active uint64, b *Budget, f func(k int, caps []int) int) {
	if st.vms == nil {
		st.vms = make([]*vm, len(mu.res))
	}
	for rest := active &^ mu.scan; rest != 0; rest &= rest - 1 {
		k := bits.TrailingZeros64(rest)
		for p := 0; p <= len(s); {
			c := mu.res[k].ExecB(s, p, b)
			if c == nil {
				break
			}
			p = f(k, c)
		}
	}
	want := active & mu.scan
	for k := range mu.res {
		if want&(1<<k) != 0 && st.vms[k] != nil {
			st.vms[k].s, st.vms[k].b = s, b
		}
	}
	var next [64]int // pattern k may start no earlier than next[k]
	for p := 0; p < len(s) && want != 0; {
		// Jump to where the earliest pattern may start again (an anchor match moves its pattern on by a window).
		lo := len(s)
		for w := want; w != 0; w &= w - 1 {
			lo = min(lo, next[bits.TrailingZeros64(w)])
		}
		if lo > p {
			b.count(lo - p)
			p = lo
			if p >= len(s) {
				break
			}
		}
		end := min(len(s), p+clockEvery)
		q, mask := mu.next(s, p, end, want)
		b.count(q - p)
		if q == end {
			p = end
			continue
		}
		for ; mask != 0; mask &= mask - 1 {
			k := bits.TrailingZeros64(mask)
			re := mu.res[k]
			if q < next[k] || (s[q] >= 128 && !re.pre.set.has(s[q])) ||
				(q > 0 && re.pre.prevNot != nil && re.pre.prevNot.has(s[q-1])) ||
				(re.pre.lits != nil && !re.pre.lits.starts(s, q, re.icase)) {
				continue
			}
			m := st.vms[k]
			if m == nil {
				m = re.vmFor(&st.vms[k], s, b)
			}
			if c := m.matchAt(q); c != nil {
				if next[k] = f(k, c); next[k] > len(s) {
					want &^= 1 << k
				}
			}
		}
		p = q + 1
	}
}
