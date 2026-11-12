package provauth

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// encodeForm is URLSearchParams.toString(): application/x-www-form-urlencoded in the WHATWG serializer's byte set
// (alphanumerics and *-._ stay, a space is +, every other byte is %XX in upper case), fields in the given order.
func encodeForm(fields []field) string {
	var b strings.Builder
	for i, f := range fields {
		if i > 0 {
			b.WriteByte('&')
		}
		formEscape(&b, f.name)
		b.WriteByte('=')
		formEscape(&b, f.value)
	}
	return b.String()
}

func formEscape(b *strings.Builder, s string) {
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == ' ':
			b.WriteByte('+')
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9', c == '*', c == '-', c == '.', c == '_':
			b.WriteByte(c)
		default:
			fmt.Fprintf(b, "%%%02X", c)
		}
	}
}

// jsString is JSON.stringify of a string: ", \ and the control characters are escaped (\b \f \n \r \t, else \u00xx
// in lower case); everything else, U+2028 and U+2029 included, is written as it is. encoding/json differs: it escapes
// <, >, & and U+2028/U+2029 by default.
func jsString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

var decimal = regexp.MustCompile(`^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?$`)

// jsNumber is Number(v) for a value decoded from JSON: numbers as they are, true 1, false 0, a string by the
// StringToNumber grammar (trimmed; "" is 0; decimal, Infinity, and 0x/0o/0b integers), anything else NaN (null
// included: Number(null) is 0 and Number(undefined) NaN, but the only caller treats both as missing).
func jsNumber(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case bool:
		if x {
			return 1
		}
		return 0
	case string:
		s := strings.TrimFunc(x, func(r rune) bool { return unicode.IsSpace(r) || r == '\uFEFF' })
		switch s {
		case "":
			return 0
		case "Infinity", "+Infinity":
			return math.Inf(1)
		case "-Infinity":
			return math.Inf(-1)
		}
		if len(s) > 2 && s[0] == '0' {
			base := map[byte]int{'x': 16, 'X': 16, 'o': 8, 'O': 8, 'b': 2, 'B': 2}[s[1]]
			if base != 0 {
				if n, err := strconv.ParseUint(s[2:], base, 64); err == nil && !strings.Contains(s, "_") {
					return float64(n)
				}
				return math.NaN()
			}
		}
		if !decimal.MatchString(s) {
			return math.NaN()
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil && !math.IsInf(f, 0) {
			return math.NaN()
		}
		return f
	}
	return math.NaN()
}
