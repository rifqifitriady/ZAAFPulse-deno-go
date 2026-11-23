package jsre

import (
	"math/rand/v2"
	"testing"
	"unicode/utf16"
)

// Str encodes UTF-8 itself (no rune slice); it must equal string(utf16.Decode(u)), lone surrogates included.
func TestStrMatchesDecode(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	pick := []uint16{'a', 0x7f, 0x80, 0x7ff, 0x800, 0xd7ff, 0xd800, 0xdbff, 0xdc00, 0xdfff, 0xe000, 0xfffd, 0xffff}
	for range 20000 {
		u := make([]uint16, r.IntN(8))
		for i := range u {
			if r.IntN(2) == 0 {
				u[i] = pick[r.IntN(len(pick))]
			} else {
				u[i] = uint16(r.IntN(1 << 16))
			}
		}
		if got, want := Str(u), string(utf16.Decode(u)); got != want {
			t.Fatalf("%x: %q, want %q", u, got, want)
		}
	}
}

// ReplaceAll with no match returns its input without a copy; with matches it never writes into the input.
func TestReplaceAllNoMatchNoCopy(t *testing.T) {
	re := MustCompile(`zq`, "g")
	s := U16("abc abc")
	if out := re.ReplaceAll(s, func([]int) []uint16 { return nil }); &out[0] != &s[0] {
		t.Fatal("copied")
	}
	re = MustCompile(`b`, "g")
	dst := make([]uint16, 0, 64)
	out := re.ReplaceAllInto(dst, s, func([]int) []uint16 { return U16("XY") }, nil)
	if Str(out) != "aXYc aXYc" || Str(s) != "abc abc" || &out[0] != &dst[:1][0] {
		t.Fatalf("%q %q", Str(out), Str(s))
	}
}
