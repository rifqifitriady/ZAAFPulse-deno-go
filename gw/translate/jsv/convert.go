package jsv

import (
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// Truthy is JS truthiness.
func Truthy(v Value) bool {
	switch x := v.(type) {
	case nil, undefinedType:
		return false
	case bool:
		return x
	case float64:
		return x != 0 && !math.IsNaN(x)
	case string:
		return x != ""
	}
	return true
}

// TypeOf is JS typeof for the values jsv holds ("object" for null, objects and arrays).
func TypeOf(v Value) string {
	switch v.(type) {
	case undefinedType:
		return "undefined"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case string:
		return "string"
	}
	return "object"
}

// StrictEqual is JS ===: by value for primitives, by identity for objects and arrays.
func StrictEqual(a, b Value) bool {
	switch x := a.(type) {
	case *Object:
		y, ok := b.(*Object)
		return ok && x == y
	case *Array:
		y, ok := b.(*Array)
		return ok && x == y
	case float64:
		y, ok := b.(float64)
		return ok && x == y
	}
	return a == b
}

// ToString is JS String(v).
func ToString(v Value) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case undefinedType:
		return "undefined"
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		return NumberString(x)
	case string:
		return x
	case *Array:
		return Join(x, ",")
	}
	return "[object Object]"
}

// Join is JS Array.prototype.join: null and undefined become "".
func Join(a *Array, sep string) string {
	var b strings.Builder
	for i, it := range a.Items {
		if i > 0 {
			b.WriteString(sep)
		}
		if !IsNullish(it) {
			b.WriteString(ToString(it))
		}
	}
	return b.String()
}

// NumberString is ECMA-262 Number::toString(10).
func NumberString(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case f == 0:
		return "0"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	case f < 0:
		return "-" + NumberString(-f)
	}
	e := strconv.FormatFloat(f, 'e', -1, 64) // d.ddde±XX, shortest round-trip digits
	mant, exp, _ := strings.Cut(e, "e")
	digits := strings.Replace(mant, ".", "", 1)
	x, _ := strconv.Atoi(exp)
	k, n := len(digits), x+1
	switch {
	case k <= n && n <= 21:
		return digits + strings.Repeat("0", n-k)
	case 0 < n && n <= 21:
		return digits[:n] + "." + digits[n:]
	case -6 < n && n <= 0:
		return "0." + strings.Repeat("0", -n) + digits
	}
	sign := "+"
	if n-1 < 0 {
		sign = "-"
	}
	ex := strconv.Itoa(abs(n - 1))
	if k == 1 {
		return digits + "e" + sign + ex
	}
	return digits[:1] + "." + digits[1:] + "e" + sign + ex
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// IsJSSpace reports whether r is JS WhiteSpace or LineTerminator (what String.prototype.trim and \s remove).
func IsJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// ToNumber is JS Number(v).
func ToNumber(v Value) float64 {
	switch x := v.(type) {
	case nil:
		return 0
	case undefinedType:
		return math.NaN()
	case bool:
		if x {
			return 1
		}
		return 0
	case float64:
		return x
	case string:
		return stringToNumber(x)
	case *Array:
		return stringToNumber(ToString(x))
	}
	return math.NaN()
}

func stringToNumber(s string) float64 {
	s = strings.TrimFunc(s, IsJSSpace)
	if s == "" {
		return 0
	}
	if len(s) > 2 && s[0] == '0' {
		base := 0
		switch s[1] {
		case 'x', 'X':
			base = 16
		case 'o', 'O':
			base = 8
		case 'b', 'B':
			base = 2
		}
		if base != 0 {
			f := 0.0
			for _, c := range s[2:] {
				d := digitVal(c)
				if d < 0 || d >= base {
					return math.NaN()
				}
				f = f*float64(base) + float64(d)
			}
			return f
		}
	}
	body := s
	if body[0] == '+' || body[0] == '-' {
		body = body[1:]
	}
	if body == "Infinity" {
		if s[0] == '-' {
			return math.Inf(-1)
		}
		return math.Inf(1)
	}
	if !decimalLiteral(body) {
		return math.NaN()
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil && !math.IsInf(f, 0) {
		return math.NaN()
	}
	return f
}

func digitVal(c rune) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'z':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'Z':
		return int(c-'A') + 10
	}
	return -1
}

// decimalLiteral: (digits [. digits?] | . digits) ([eE] [+-]? digits)?
func decimalLiteral(s string) bool {
	i, n := 0, len(s)
	d1 := 0
	for i < n && s[i] >= '0' && s[i] <= '9' {
		i, d1 = i+1, d1+1
	}
	d2 := 0
	if i < n && s[i] == '.' {
		i++
		for i < n && s[i] >= '0' && s[i] <= '9' {
			i, d2 = i+1, d2+1
		}
	}
	if d1+d2 == 0 {
		return false
	}
	if i < n && (s[i] == 'e' || s[i] == 'E') {
		i++
		if i < n && (s[i] == '+' || s[i] == '-') {
			i++
		}
		d3 := 0
		for i < n && s[i] >= '0' && s[i] <= '9' {
			i, d3 = i+1, d3+1
		}
		if d3 == 0 {
			return false
		}
	}
	return i == n
}

// UTF16Len is the JS length of s.
func UTF16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

func toUTF16(s string) []uint16 { return utf16.Encode([]rune(s)) }

func fromUTF16(u []uint16) string { return string(utf16.Decode(u)) }

// ToUTF16 returns the code units of s (a JS string's view of it).
func ToUTF16(s string) []uint16 { return toUTF16(s) }

// FromUTF16 turns code units back into a Go string; a lone surrogate becomes U+FFFD.
func FromUTF16(u []uint16) string { return fromUTF16(u) }

// ToLowerASCII etc. are not needed: JS toLowerCase on the values the ports lower is plain Unicode lower case.
// ToLower is JS String.prototype.toLowerCase for BMP text without special casing (İ, final sigma).
func ToLower(s string) string {
	if isASCII(s) {
		return strings.ToLower(s)
	}
	var b strings.Builder
	for _, r := range s {
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}
