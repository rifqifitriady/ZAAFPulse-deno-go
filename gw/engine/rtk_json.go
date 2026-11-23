package engine

// The token saver's json filter: JSON.stringify(cut(JSON.parse(t))) in one pass over engine.Parse's tree (spec D6).
// Arrays keep their first 20 items and a "… N more items" string; objects below depth 12 are rebuilt (keys in JS
// order); deeper ones are written whole; a string over 2000 UTF-16 units becomes its first 2000 and "… (N chars)".

import (
	"strconv"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/guard/jsre"
)

func rtkJSON(v *Value) []uint16 { return u16(string(cutWrite(nil, v, 0))) }

func cutWrite(b []byte, v *Value, depth int) []byte {
	switch v.Kind {
	case Array:
		b = append(b, '[')
		n := len(v.A)
		for i := 0; i < min(n, 20); i++ {
			if i > 0 {
				b = append(b, ',')
			}
			b = cutWrite(b, v.A[i], depth+1)
		}
		if n > 20 {
			b = append(b, ',')
			b = QuoteJS(b, "… "+strconv.Itoa(n-20)+" more items")
		}
		return append(b, ']')
	case Object:
		if depth >= 12 {
			return append(b, v.Stringify()...)
		}
		b = append(b, '{')
		for i, k := range v.Keys() {
			if i > 0 {
				b = append(b, ',')
			}
			b = QuoteJS(b, k)
			b = append(b, ':')
			b = cutWrite(b, v.Get(k), depth+1)
		}
		return append(b, '}')
	case String:
		if units(v.S) > 2000 {
			return cutString(b, v.S)
		}
	}
	return append(b, v.Stringify()...)
}

// cutString writes `${s.slice(0, 2000)}… (${s.length} chars)` as JSON.stringify writes it: a surrogate pair cut in
// half leaves a lone high surrogate, written \udXXX in lower-case hex.
func cutString(b []byte, s string) []byte {
	us := u16(s)
	head := us[:2000]
	var lone uint16
	if c := head[1999]; c >= 0xd800 && c <= 0xdbff {
		lone, head = c, head[:1999]
	}
	b = QuoteJS(b, jsre.Str(head))
	b = b[:len(b)-1] // reopen the string
	if lone != 0 {
		const hexd = "0123456789abcdef"
		b = append(b, '\\', 'u', hexd[lone>>12], hexd[lone>>8&15], hexd[lone>>4&15], hexd[lone&15])
	}
	tail := QuoteJS(nil, "… ("+strconv.Itoa(len(us))+" chars)")
	return append(b, tail[1:]...)
}
