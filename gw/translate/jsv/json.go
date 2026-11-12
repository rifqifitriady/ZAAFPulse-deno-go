package jsv

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Parse is JSON.parse: objects keep their key order, numbers are float64 (an overflow is ±Infinity, as in V8). A
// lone surrogate escape becomes U+FFFD (Go strings are UTF-8), where JS keeps it.
func Parse(data []byte) (Value, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := parseValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("jsv: trailing data after JSON value")
	}
	return v, nil
}

func parseValue(dec *json.Decoder) (Value, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	return fromToken(dec, tok)
}

func fromToken(dec *json.Decoder, tok json.Token) (Value, error) {
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			o := NewObject()
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				k, _ := kt.(string)
				v, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				o.Set(k, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return o, nil
		case '[':
			a := &Array{Items: []Value{}}
			for dec.More() {
				v, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				a.Items = append(a.Items, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return a, nil
		}
		return nil, errors.New("jsv: unexpected delimiter")
	case json.Number:
		f, err := strconv.ParseFloat(string(t), 64)
		if err != nil && !math.IsInf(f, 0) {
			return nil, err
		}
		return f, nil
	case string, bool, nil:
		return t, nil
	}
	return nil, errors.New("jsv: unexpected token")
}

// MustParse parses or panics; for tests and constant data.
func MustParse(s string) Value {
	v, err := Parse([]byte(s))
	if err != nil {
		panic(err)
	}
	return v
}

// Stringify is JSON.stringify(v). ok is false when v is undefined (JSON.stringify returns undefined).
func Stringify(v Value) (string, bool) {
	if v == Undefined {
		return "", false
	}
	var b strings.Builder
	write(&b, v, false)
	return b.String(), true
}

// MustStringify is Stringify for a value known not to be undefined.
func MustStringify(v Value) string { s, _ := Stringify(v); return s }

// Canonical is Stringify with every object's keys sorted: the form fixtures are compared in.
func Canonical(v Value) string {
	var b strings.Builder
	write(&b, v, true)
	return b.String()
}

func write(b *strings.Builder, v Value, sorted bool) {
	switch x := v.(type) {
	case nil, undefinedType:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			b.WriteString("null")
		} else {
			b.WriteString(NumberString(x))
		}
	case string:
		Quote(b, x)
	case *Array:
		b.WriteByte('[')
		for i, it := range x.Items {
			if i > 0 {
				b.WriteByte(',')
			}
			write(b, it, sorted)
		}
		b.WriteByte(']')
	case *Object:
		keys := x.Keys()
		if sorted {
			sort.Strings(keys)
		}
		b.WriteByte('{')
		first := true
		for _, k := range keys {
			val := x.vals[k]
			if val == Undefined {
				continue
			}
			if !first {
				b.WriteByte(',')
			}
			first = false
			Quote(b, k)
			b.WriteByte(':')
			write(b, val, sorted)
		}
		b.WriteByte('}')
	default:
		panic("jsv: not a JS value")
	}
}

const hexDigits = "0123456789abcdef"

// Quote writes s as JSON.stringify writes a string.
func Quote(b *strings.Builder, s string) {
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
				b.WriteString(`\u00`)
				b.WriteByte(hexDigits[r>>4])
				b.WriteByte(hexDigits[r&15])
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}
