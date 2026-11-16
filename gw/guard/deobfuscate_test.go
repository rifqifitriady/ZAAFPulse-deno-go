package guard

import (
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/guard/jsre"
)

// deobfuscateRef is deobfuscate as it was before the heap work (a flag and an offset per code unit, ReplaceAll for
// leetspeak): the reference the new one must equal.
func deobfuscateRef(text []uint16, windows [][2]int) ([]uint16, []int) {
	var near []uint16
	for i, w := range windows {
		if i > 0 {
			near = append(near, '\n')
		}
		near = append(near, text[w[0]:w[1]]...)
	}
	isLeet := reHasLeet.Test(near)
	if !reHasZeroWidth.Test(text) && !isLeet && !reHasSplit.Test(near) {
		return nil, nil
	}
	drop := make([]bool, len(text))
	for _, m := range reZeroWidth.FindAll(text) {
		drop[m[0]] = true
	}
	for _, m := range reSplitLetters.FindAll(text) {
		for i := m[0]; i < m[1]; i++ {
			if c := text[i] | 0x20; c < 'a' || c > 'z' {
				drop[i] = true
			}
		}
	}
	var s []uint16
	var idx []int
	for i, c := range text {
		if !drop[i] {
			s = append(s, c)
			idx = append(idx, i)
		}
	}
	if isLeet {
		s = reLeetWord.ReplaceAll(s, func(m []int) []uint16 {
			w := s[m[0]:m[1]]
			if !reDigit.Test(w) {
				return w
			}
			return reLeetChar.ReplaceAll(w, func(c []int) []uint16 { return []uint16{leet[w[c[0]]]} })
		})
	}
	return s, idx
}

func TestDeobfuscateMatchesReference(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 9))
	parts := []string{"i-g-n-o-r-e", " ", "\n", "1gn0r3", "pr3v10us", "h@ck", "$ecret", "a b c d", "x​y", "­",
		"instructions", "4ll", "token", "t.o.k.e.n", "@", "$", "0", "7h3", "ünï", "😀", "_", "a_b_c_d", "ſ", "K"}
	for n := range 3000 {
		var sb strings.Builder
		for range 1 + r.IntN(30) {
			sb.WriteString(parts[r.IntN(len(parts))])
		}
		text := jsre.U16(sb.String())
		windows := [][2]int{{0, len(text)}}
		if n%3 == 1 && len(text) > 4 {
			windows = [][2]int{{0, len(text) / 3}, {len(text) / 2, len(text)}}
		}
		got, km := deobfuscate(text, windows, nil)
		want, idx := deobfuscateRef(text, windows)
		if (got == nil) != (want == nil) || !slices.Equal(got, want) {
			t.Fatalf("%q: got %q, want %q", sb.String(), jsre.Str(got), jsre.Str(want))
		}
		for i := range want {
			o := i
			if km != nil {
				o = km.orig(i)
			}
			if o != idx[i] {
				t.Fatalf("%q: offset %d maps to %d, want %d", sb.String(), i, o, idx[i])
			}
		}
		if jsre.Str(text) != sb.String() {
			t.Fatalf("input changed: %q", sb.String())
		}
	}
}
