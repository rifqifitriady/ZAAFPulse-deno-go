package engine

// JSON as JavaScript reads and writes it (JSON.parse, then JSON.stringify), so that what the Go engine sends and
// answers is byte for byte what the JS gateway sends and answers: numbers are doubles written the way Number#toString
// writes them (1.0 is 1, 1e21 is 1e+21, 1e400 is null), an object keeps its keys in JavaScript's order (array-index keys
// first, ascending, then the others in the order they first appeared; a repeated key keeps its first place and its
// last value), and strings are escaped as JSON.stringify escapes them (", \, and control characters only).
// Fixtures: scripts/fixtures/gen-jsjson.js. A text JSON.parse accepts but this cannot hold faithfully (an escaped lone
// surrogate, nesting deeper than MaxDepth) is ErrUnsupported: the engine then hands the request to the control plane.

import (
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// MaxDepth is the deepest nesting the engine parses.
const MaxDepth = 512

var (
	// ErrSyntax: JSON.parse would throw.
	ErrSyntax = errors.New("jsjson: not valid JSON")
	// ErrUnsupported: JSON.parse would accept it, but this engine does not serve it.
	ErrUnsupported = errors.New("jsjson: valid JSON the engine does not serve")
)

// Kind is a JSON value's type.
type Kind int

const (
	Null Kind = iota
	Bool
	Number
	String
	Array
	Object
)

// Value is one parsed JSON value.
type Value struct {
	Kind Kind
	B    bool
	N    float64
	S    string
	A    []*Value
	keys []string
	vals map[string]*Value
}

// NewObject is an empty object.
func NewObject() *Value { return &Value{Kind: Object, vals: map[string]*Value{}} }

// Str is a string value.
func Str(s string) *Value { return &Value{Kind: String, S: s} }

// Num is a number value.
func Num(n float64) *Value { return &Value{Kind: Number, N: n} }

// Get is the value of an object's key, or nil.
func (v *Value) Get(key string) *Value {
	if v == nil || v.Kind != Object {
		return nil
	}
	return v.vals[key]
}

// Set gives an object's key a value; a new key goes last (JavaScript order is applied when written).
func (v *Value) Set(key string, x *Value) {
	if _, ok := v.vals[key]; !ok {
		v.keys = append(v.keys, key)
	}
	v.vals[key] = x
}

// Delete removes an object's key.
func (v *Value) Delete(key string) {
	if _, ok := v.vals[key]; !ok {
		return
	}
	delete(v.vals, key)
	for i, k := range v.keys {
		if k == key {
			v.keys = append(v.keys[:i:i], v.keys[i+1:]...)
			break
		}
	}
}

// Keys are an object's keys in JavaScript's order.
func (v *Value) Keys() []string {
	var idx, rest []string
	for _, k := range v.keys {
		if arrayIndex(k) {
			idx = append(idx, k)
		} else {
			rest = append(rest, k)
		}
	}
	sort.Slice(idx, func(i, j int) bool {
		a, _ := strconv.ParseUint(idx[i], 10, 64)
		b, _ := strconv.ParseUint(idx[j], 10, 64)
		return a < b
	})
	return append(idx, rest...)
}

// Clone copies a value deeply.
func (v *Value) Clone() *Value {
	c := *v
	if v.A != nil {
		c.A = make([]*Value, len(v.A))
		for i, x := range v.A {
			c.A[i] = x.Clone()
		}
	}
	if v.Kind == Object {
		c.keys = append([]string(nil), v.keys...)
		c.vals = make(map[string]*Value, len(v.vals))
		for k, x := range v.vals {
			c.vals[k] = x.Clone()
		}
	}
	return &c
}

// Truthy is JavaScript's truthiness.
func (v *Value) Truthy() bool {
	if v == nil {
		return false
	}
	switch v.Kind {
	case Null:
		return false
	case Bool:
		return v.B
	case Number:
		return v.N != 0 && !math.IsNaN(v.N)
	case String:
		return v.S != ""
	}
	return true
}

// arrayIndex: a canonical decimal integer below 2^32 - 1 (an array index, which JavaScript orders first).
func arrayIndex(k string) bool {
	if k == "" || len(k) > 10 || (len(k) > 1 && k[0] == '0') {
		return false
	}
	for i := 0; i < len(k); i++ {
		if k[i] < '0' || k[i] > '9' {
			return false
		}
	}
	n, err := strconv.ParseUint(k, 10, 64)
	return err == nil && n < 1<<32-1
}

// Parse reads one JSON text as JSON.parse does.
func Parse(b []byte) (*Value, error) {
	if !utf8.Valid(b) {
		return nil, ErrUnsupported // the control plane decodes it with replacement characters first
	}
	p := &parser{b: b}
	p.ws()
	v, err := p.value(0)
	if err != nil {
		return nil, err
	}
	p.ws()
	if p.i != len(p.b) {
		return nil, ErrSyntax
	}
	return v, nil
}

type parser struct {
	b []byte
	i int
}

func (p *parser) ws() {
	for p.i < len(p.b) && (p.b[p.i] == ' ' || p.b[p.i] == '\t' || p.b[p.i] == '\n' || p.b[p.i] == '\r') {
		p.i++
	}
}

func (p *parser) lit(s string) bool {
	if strings.HasPrefix(string(p.b[p.i:min(len(p.b), p.i+len(s))]), s) {
		p.i += len(s)
		return true
	}
	return false
}

func (p *parser) value(depth int) (*Value, error) {
	if p.i >= len(p.b) {
		return nil, ErrSyntax
	}
	switch c := p.b[p.i]; {
	case c == '{':
		if depth >= MaxDepth {
			return nil, ErrUnsupported
		}
		p.i++
		o := NewObject()
		p.ws()
		if p.i < len(p.b) && p.b[p.i] == '}' {
			p.i++
			return o, nil
		}
		for {
			p.ws()
			if p.i >= len(p.b) || p.b[p.i] != '"' {
				return nil, ErrSyntax
			}
			k, err := p.str()
			if err != nil {
				return nil, err
			}
			p.ws()
			if p.i >= len(p.b) || p.b[p.i] != ':' {
				return nil, ErrSyntax
			}
			p.i++
			p.ws()
			x, err := p.value(depth + 1)
			if err != nil {
				return nil, err
			}
			o.Set(k, x)
			p.ws()
			if p.i < len(p.b) && p.b[p.i] == ',' {
				p.i++
				continue
			}
			if p.i < len(p.b) && p.b[p.i] == '}' {
				p.i++
				return o, nil
			}
			return nil, ErrSyntax
		}
	case c == '[':
		if depth >= MaxDepth {
			return nil, ErrUnsupported
		}
		p.i++
		a := &Value{Kind: Array, A: []*Value{}}
		p.ws()
		if p.i < len(p.b) && p.b[p.i] == ']' {
			p.i++
			return a, nil
		}
		for {
			p.ws()
			x, err := p.value(depth + 1)
			if err != nil {
				return nil, err
			}
			a.A = append(a.A, x)
			p.ws()
			if p.i < len(p.b) && p.b[p.i] == ',' {
				p.i++
				continue
			}
			if p.i < len(p.b) && p.b[p.i] == ']' {
				p.i++
				return a, nil
			}
			return nil, ErrSyntax
		}
	case c == '"':
		s, err := p.str()
		if err != nil {
			return nil, err
		}
		return Str(s), nil
	case c == 't' && p.lit("true"):
		return &Value{Kind: Bool, B: true}, nil
	case c == 'f' && p.lit("false"):
		return &Value{Kind: Bool}, nil
	case c == 'n' && p.lit("null"):
		return &Value{Kind: Null}, nil
	case c == '-' || (c >= '0' && c <= '9'):
		return p.num()
	}
	return nil, ErrSyntax
}

func (p *parser) num() (*Value, error) {
	start := p.i
	digits := func() int {
		n := 0
		for p.i < len(p.b) && p.b[p.i] >= '0' && p.b[p.i] <= '9' {
			p.i++
			n++
		}
		return n
	}
	if p.b[p.i] == '-' {
		p.i++
	}
	if p.i < len(p.b) && p.b[p.i] == '0' {
		p.i++
	} else if digits() == 0 {
		return nil, ErrSyntax
	}
	if p.i < len(p.b) && p.b[p.i] == '.' {
		p.i++
		if digits() == 0 {
			return nil, ErrSyntax
		}
	}
	if p.i < len(p.b) && (p.b[p.i] == 'e' || p.b[p.i] == 'E') {
		p.i++
		if p.i < len(p.b) && (p.b[p.i] == '+' || p.b[p.i] == '-') {
			p.i++
		}
		if digits() == 0 {
			return nil, ErrSyntax
		}
	}
	f, err := strconv.ParseFloat(string(p.b[start:p.i]), 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return nil, ErrSyntax
	}
	return Num(f), nil // out of range: ±Inf (written as null) or ±0, as JavaScript reads it
}

func (p *parser) str() (string, error) {
	p.i++ // the opening quote
	var sb strings.Builder
	for {
		if p.i >= len(p.b) {
			return "", ErrSyntax
		}
		c := p.b[p.i]
		switch {
		case c == '"':
			p.i++
			return sb.String(), nil
		case c < 0x20:
			return "", ErrSyntax
		case c != '\\':
			sb.WriteByte(c)
			p.i++
			continue
		}
		p.i++
		if p.i >= len(p.b) {
			return "", ErrSyntax
		}
		e := p.b[p.i]
		p.i++
		switch e {
		case '"', '\\', '/':
			sb.WriteByte(e)
		case 'b':
			sb.WriteByte('\b')
		case 'f':
			sb.WriteByte('\f')
		case 'n':
			sb.WriteByte('\n')
		case 'r':
			sb.WriteByte('\r')
		case 't':
			sb.WriteByte('\t')
		case 'u':
			u, ok := p.hex4()
			if !ok {
				return "", ErrSyntax
			}
			r := rune(u)
			if utf16.IsSurrogate(r) {
				// Only a high surrogate followed by an escaped low one is a character here.
				if u >= 0xdc00 || p.i+6 > len(p.b) || p.b[p.i] != '\\' || p.b[p.i+1] != 'u' {
					return "", ErrUnsupported
				}
				p.i += 2
				lo, ok := p.hex4()
				if !ok {
					return "", ErrSyntax
				}
				if lo < 0xdc00 || lo > 0xdfff {
					return "", ErrUnsupported
				}
				r = utf16.DecodeRune(r, rune(lo))
			}
			sb.WriteRune(r)
		default:
			return "", ErrSyntax
		}
	}
}

func (p *parser) hex4() (uint16, bool) {
	if p.i+4 > len(p.b) {
		return 0, false
	}
	n, err := strconv.ParseUint(string(p.b[p.i:p.i+4]), 16, 16)
	if err != nil {
		return 0, false
	}
	p.i += 4
	return uint16(n), true
}

// Stringify writes v as JSON.stringify(v) does (no indentation).
func (v *Value) Stringify() []byte { return v.append(nil) }

func (v *Value) append(b []byte) []byte {
	switch v.Kind {
	case Null:
		return append(b, "null"...)
	case Bool:
		return strconv.AppendBool(b, v.B)
	case Number:
		return append(b, NumberString(v.N)...)
	case String:
		return QuoteJS(b, v.S)
	case Array:
		b = append(b, '[')
		for i, x := range v.A {
			if i > 0 {
				b = append(b, ',')
			}
			b = x.append(b)
		}
		return append(b, ']')
	}
	b = append(b, '{')
	for i, k := range v.Keys() {
		if i > 0 {
			b = append(b, ',')
		}
		b = QuoteJS(b, k)
		b = append(b, ':')
		b = v.vals[k].append(b)
	}
	return append(b, '}')
}

// QuoteJS appends s as JSON.stringify writes a string.
func QuoteJS(b []byte, s string) []byte {
	const hexd = "0123456789abcdef"
	b = append(b, '"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			b = append(b, '\\', '"')
		case c == '\\':
			b = append(b, '\\', '\\')
		case c == '\b':
			b = append(b, '\\', 'b')
		case c == '\f':
			b = append(b, '\\', 'f')
		case c == '\n':
			b = append(b, '\\', 'n')
		case c == '\r':
			b = append(b, '\\', 'r')
		case c == '\t':
			b = append(b, '\\', 't')
		case c < 0x20:
			b = append(b, '\\', 'u', '0', '0', hexd[c>>4], hexd[c&15])
		default:
			b = append(b, c)
		}
	}
	return append(b, '"')
}

// NumberString is Number.prototype.toString(10) of a finite double, as JSON.stringify writes it; a non-finite one is
// "null".
func NumberString(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "null"
	}
	if f == 0 {
		return "0"
	}
	sign := ""
	if f < 0 {
		sign, f = "-", -f
	}
	e := strconv.FormatFloat(f, 'e', -1, 64) // d.ddde±XX, the shortest digits that read back as f
	mant, exp, _ := strings.Cut(e, "e")
	digits := strings.Replace(mant, ".", "", 1)
	x, _ := strconv.Atoi(exp)
	k, n := len(digits), x+1
	switch {
	case k <= n && n <= 21:
		return sign + digits + strings.Repeat("0", n-k)
	case 0 < n && n <= 21:
		return sign + digits[:n] + "." + digits[n:]
	case -6 < n && n <= 0:
		return sign + "0." + strings.Repeat("0", -n) + digits
	}
	m := digits[:1]
	if k > 1 {
		m += "." + digits[1:]
	}
	es := "+"
	if n-1 < 0 {
		es = "-"
	}
	return sign + m + "e" + es + strconv.Itoa(abs(n-1))
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
