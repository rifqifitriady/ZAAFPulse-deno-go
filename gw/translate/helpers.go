package translate

import (
	"math"
	"strings"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"
)

// V is a JS value (see package jsv).
type V = jsv.Value

var (
	get    = jsv.Get
	truthy = jsv.Truthy
	or     = jsv.Or
	str    = jsv.ToString
)

// obj builds an object from key, value pairs in order.
func obj(kv ...V) *jsv.Object {
	o := jsv.NewObject()
	for i := 0; i+1 < len(kv); i += 2 {
		o.Set(kv[i].(string), kv[i+1])
	}
	return o
}

// list is `for (const x of v)` / `v.map` over an array value; anything else that is not an array throws (the JS throws
// for objects and numbers; it would iterate a string's characters, which no translated shape relies on).
func list(v V) []V {
	if a := jsv.Ary(v); a != nil {
		return a.Items
	}
	jsv.Throw("%s is not iterable", jsv.TypeOf(v))
	return nil
}

// listOr is `v || []` iterated.
func listOr(v V) []V {
	if !truthy(v) {
		return nil
	}
	return list(v)
}

func isStr(v V) bool { _, ok := v.(string); return ok }

// eq is ===.
func eq(a, b V) bool { return jsv.StrictEqual(a, b) }

// add is JS `a + b`.
func add(a, b V) V {
	_, as := a.(string)
	_, bs := b.(string)
	ao, bo := isObjectish(a), isObjectish(b)
	if as || bs || ao || bo {
		return str(a) + str(b)
	}
	return jsv.ToNumber(a) + jsv.ToNumber(b)
}

func isObjectish(v V) bool {
	switch v.(type) {
	case *jsv.Object, *jsv.Array:
		return true
	}
	return false
}

// sub is JS `a - b`.
func sub(a, b V) float64 { return jsv.ToNumber(a) - jsv.ToNumber(b) }

// jsMax and jsMin are Math.max / Math.min of two values.
func jsMax(a, b V) float64 { return math.Max(jsv.ToNumber(a), jsv.ToNumber(b)) }
func jsMin(a, b V) float64 { return math.Min(jsv.ToNumber(a), jsv.ToNumber(b)) }

// join is `.map(f).join(sep)` with JS conversion (null and undefined become "").
func join(items []V, sep string) string { return jsv.Join(jsv.Arr(items...), sep) }

// arr turns a Go slice into an array value.
func arr(items []V) *jsv.Array {
	if items == nil {
		items = []V{}
	}
	return &jsv.Array{Items: items}
}

func strs(ss []string) *jsv.Array {
	out := make([]V, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return arr(out)
}

// stringify is JSON.stringify for a value that is not undefined.
func stringify(v V) V {
	s, ok := jsv.Stringify(v)
	if !ok {
		return jsv.Undefined
	}
	return s
}

// parseJSON is the JS `json(s, fallback)`: an object passes through, otherwise JSON.parse(s || '{}') or the fallback.
func parseJSON(s V, fallback V) V {
	if truthy(s) && jsv.TypeOf(s) == "object" {
		return s
	}
	v, err := jsv.Parse([]byte(str(or(s, "{}"))))
	if err != nil {
		return fallback
	}
	return v
}

// textOf is the JS textOf: a string, or the text of each part joined.
func textOf(content V) string {
	if s, ok := content.(string); ok {
		return s
	}
	if a := jsv.Ary(content); a != nil {
		out := make([]V, len(a.Items))
		for i, c := range a.Items {
			if s, ok := c.(string); ok {
				out[i] = s
			} else {
				out[i] = or(get(c, "text"), "")
			}
		}
		return join(out, "")
	}
	return ""
}

// key is the property key JS uses for v in `TABLE[v]`.
func key(v V) string { return str(v) }

// lookup is `TABLE[v]` on a string table: undefined when absent.
func lookup(t map[string]string, v V) V {
	if s, ok := t[key(v)]; ok {
		return s
	}
	return jsv.Undefined
}

// trimJS is String.prototype.trim.
func trimJS(s string) string { return strings.TrimFunc(s, jsv.IsJSSpace) }

// catch runs f, turning a thrown TypeError or BadRequest into the returned error.
func catch(f func()) error { return jsv.Catch(f) }

func throwBad(msg string) { panic(&BadRequest{Message: msg}) }

// usage is the JS usageParts result.
type usage struct{ prompt, cached, output, reasoning V }

// usageParts: OpenAI usage → the numbers each format reports. u undefined is {} (the default parameter).
func usageParts(u V) usage {
	if u == jsv.Undefined {
		u = jsv.NewObject()
	}
	ptd := get(u, "prompt_tokens_details")
	ctd := get(u, "completion_tokens_details")
	cached, reasoning := V(0.0), V(0.0)
	if truthy(ptd) {
		cached = or(get(ptd, "cached_tokens"), 0.0)
	}
	if truthy(ctd) {
		reasoning = or(get(ctd, "reasoning_tokens"), 0.0)
	}
	return usage{prompt: or(get(u, "prompt_tokens"), 0.0), cached: cached, output: or(get(u, "completion_tokens"), 0.0), reasoning: reasoning}
}
