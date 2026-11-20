package engine

// JavaScript string semantics the token saver's port needs (spec D3, D5), on UTF-16 code units: String.length,
// split('\n'), trim, the part of toLowerCase the hint patterns can see, a template literal's ToString of a JSON value,
// and Map keys under SameValueZero.

import (
	"math"
	"strconv"
	"unicode/utf16"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/guard/jsre"
)

func u16(s string) []uint16 { return jsre.U16(s) }

// units is String.prototype.length of a Go string.
func units(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

func cat(parts ...[]uint16) []uint16 {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	out := make([]uint16, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// splitNL is String.prototype.split('\n'): n separators give n+1 parts.
func splitNL(t []uint16) [][]uint16 {
	out := make([][]uint16, 0, 16)
	start := 0
	for i, c := range t {
		if c == '\n' {
			out = append(out, t[start:i:i])
			start = i + 1
		}
	}
	return append(out, t[start:len(t):len(t)])
}

// eachLine calls f with each part of split('\n') in order, without building the list (spec D19).
func eachLine(t []uint16, f func(l []uint16)) {
	start := 0
	for i, c := range t {
		if c == '\n' {
			f(t[start:i:i])
			start = i + 1
		}
	}
	f(t[start:len(t):len(t)])
}

// joinNL is Array.prototype.join('\n').
func joinNL(ls [][]uint16) []uint16 {
	n := 0
	for _, l := range ls {
		n += len(l) + 1
	}
	out := make([]uint16, 0, n)
	for i, l := range ls {
		if i > 0 {
			out = append(out, '\n')
		}
		out = append(out, l...)
	}
	return out
}

// trim16 is String.prototype.trim.
func trim16(t []uint16) []uint16 {
	i, j := 0, len(t)
	for i < j && jsre.IsSpace(t[i]) {
		i++
	}
	for j > i && jsre.IsSpace(t[j-1]) {
		j--
	}
	return t[i:j]
}

func blank(t []uint16) bool { return len(trim16(t)) == 0 }

// hasPrefix16: p is ASCII.
func hasPrefix16(t []uint16, p string) bool {
	if len(t) < len(p) {
		return false
	}
	for i := 0; i < len(p); i++ {
		if t[i] != uint16(p[i]) {
			return false
		}
	}
	return true
}

func equal16(a, b []uint16) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func lastIndex16(t []uint16, c uint16) int {
	for i := len(t) - 1; i >= 0; i-- {
		if t[i] == c {
			return i
		}
	}
	return -1
}

func itoa16(n int) []uint16 { return u16(strconv.Itoa(n)) }

// key16 is an exact map key for a text (lone surrogates included).
func key16(t []uint16) string {
	b := make([]byte, 2*len(t))
	for i, c := range t {
		b[2*i], b[2*i+1] = byte(c>>8), byte(c)
	}
	return string(b)
}

// hintLower is String.prototype.toLowerCase as far as the hint patterns can tell (spec D5): they test ASCII letters,
// \b (ASCII word characters) and [^|;&]. Only A-Z, U+212A KELVIN SIGN (to k) and U+0130 (to i U+0307) lower-case into
// ASCII; every other mapping goes from one non-ASCII unit to another, which no hint pattern tells apart, so those are
// kept as they are.
func hintLower(t []uint16) []uint16 {
	out := make([]uint16, 0, len(t)+1)
	for _, c := range t {
		switch {
		case c >= 'A' && c <= 'Z':
			out = append(out, c+('a'-'A'))
		case c == 0x212a:
			out = append(out, 'k')
		case c == 0x130:
			out = append(out, 'i', 0x307)
		default:
			out = append(out, c)
		}
	}
	return out
}

// jsNumber is Number.prototype.toString, Infinity included (a template literal writes it; JSON writes null).
func jsNumber(n float64) string {
	switch {
	case math.IsInf(n, 1):
		return "Infinity"
	case math.IsInf(n, -1):
		return "-Infinity"
	case n == 0:
		return "0"
	}
	return NumberString(n)
}

// jsToString is a template literal's ToString of a JSON value (nil is undefined). ok is false where JavaScript throws a
// TypeError: an object with an own toString key (OrdinaryToPrimitive finds no callable method that gives a primitive),
// also inside an array (Array.prototype.join converts each item).
func jsToString(v *Value) ([]uint16, bool) {
	if v == nil {
		return u16("undefined"), true
	}
	switch v.Kind {
	case Null:
		return u16("null"), true
	case Bool:
		if v.B {
			return u16("true"), true
		}
		return u16("false"), true
	case Number:
		return u16(jsNumber(v.N)), true
	case String:
		return u16(v.S), true
	case Array:
		out := []uint16{}
		for i, x := range v.A {
			if i > 0 {
				out = append(out, ',')
			}
			if x == nil || x.Kind == Null {
				continue
			}
			s, ok := jsToString(x)
			if !ok {
				return nil, false
			}
			out = append(out, s...)
		}
		return out, true
	}
	if v.Get("toString") != nil {
		return nil, false
	}
	return u16("[object Object]"), true
}

// mapKey is a Map key under SameValueZero. ok is false for an object or an array: a parsed value is never the same
// object as another, so such a key is never found.
func mapKey(v *Value) (string, bool) {
	if v == nil {
		return "u", true
	}
	switch v.Kind {
	case Null:
		return "n", true
	case Bool:
		if v.B {
			return "t", true
		}
		return "f", true
	case Number:
		return "d" + jsNumber(v.N), true
	case String:
		return "s" + v.S, true
	}
	return "", false
}
