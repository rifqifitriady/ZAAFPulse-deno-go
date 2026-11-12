// Package jsv holds JSON values with JavaScript semantics, for Go ports of the gateway's JS libraries that must match
// them byte for byte: objects keep their key order (JSON.stringify output reaches the wire inside tool arguments and
// SSE data), undefined is a value, and property reads on null or undefined throw the TypeError V8 throws.
//
// A Value is one of: nil (null), Undefined, bool, float64, string, *Object, *Array.
package jsv

import (
	"fmt"
	"runtime"
	"sort"
	"strconv"
)

// Value is any JS value that JSON can hold, plus Undefined.
type Value = any

type undefinedType struct{}

// Undefined is JavaScript's undefined.
var Undefined = undefinedType{}

func (undefinedType) String() string { return "undefined" }

// Object is a JS plain object: string keys in insertion order.
type Object struct {
	keys []string
	vals map[string]Value
}

// NewObject returns an empty object.
func NewObject() *Object { return &Object{vals: map[string]Value{}} }

// Get returns the own property k, or Undefined.
func (o *Object) Get(k string) Value {
	if v, ok := o.vals[k]; ok {
		return v
	}
	return Undefined
}

// Has reports whether k is an own property.
func (o *Object) Has(k string) bool { _, ok := o.vals[k]; return ok }

// Set assigns k; a new key goes last, an existing one keeps its place (JS semantics). It returns o.
func (o *Object) Set(k string, v Value) *Object {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
	return o
}

// Delete removes k.
func (o *Object) Delete(k string) {
	if _, ok := o.vals[k]; !ok {
		return
	}
	delete(o.vals, k)
	for i, x := range o.keys {
		if x == k {
			o.keys = append(o.keys[:i:i], o.keys[i+1:]...)
			break
		}
	}
}

// Keys returns the keys in JS property order (Object.keys): array-index keys ("0", "17") first in ascending numeric
// order, then the other keys in insertion order.
func (o *Object) Keys() []string {
	var idx, rest []string
	for _, k := range o.keys {
		if _, ok := arrayIndex(k); ok {
			idx = append(idx, k)
		} else {
			rest = append(rest, k)
		}
	}
	if len(idx) == 0 {
		return rest
	}
	sort.Slice(idx, func(i, j int) bool { a, _ := arrayIndex(idx[i]); b, _ := arrayIndex(idx[j]); return a < b })
	return append(idx, rest...)
}

// arrayIndex reports whether k is a canonical array index (0 .. 2^32-2).
func arrayIndex(k string) (uint64, bool) {
	if k == "" || len(k) > 10 || (len(k) > 1 && k[0] == '0') {
		return 0, false
	}
	n, err := strconv.ParseUint(k, 10, 64)
	return n, err == nil && n < 1<<32-1
}

// Len is the number of keys.
func (o *Object) Len() int { return len(o.keys) }

// Clone is a shallow copy, like {...o}.
func (o *Object) Clone() *Object {
	c := &Object{keys: append([]string(nil), o.keys...), vals: make(map[string]Value, len(o.vals))}
	for k, v := range o.vals {
		c.vals[k] = v
	}
	return c
}

// Assign copies every own property of src onto o, like {...o, ...src}. It returns o.
func (o *Object) Assign(src *Object) *Object {
	if src == nil {
		return o
	}
	for _, k := range src.keys {
		o.Set(k, src.vals[k])
	}
	return o
}

// Array is a JS array. A pointer, so a pushed item is seen by every holder, as in JS.
type Array struct{ Items []Value }

// Arr returns an array of the given items.
func Arr(items ...Value) *Array { return &Array{Items: items} }

// Push appends items.
func (a *Array) Push(items ...Value) { a.Items = append(a.Items, items...) }

// Len is the length.
func (a *Array) Len() int { return len(a.Items) }

// TypeError is the error a JS TypeError becomes.
type TypeError struct{ Message string }

func (e *TypeError) Error() string { return e.Message }

// Throw panics with a TypeError; Catch turns it back into an error.
func Throw(format string, args ...any) { panic(&TypeError{Message: fmt.Sprintf(format, args...)}) }

// Catch runs f and returns the TypeError (or other error value) it panicked with. A Go runtime error and any
// non-error panic propagate.
func Catch(f func()) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if _, bug := r.(runtime.Error); bug {
				panic(r)
			}
			if e, ok := r.(error); ok {
				err = e
				return
			}
			panic(r)
		}
	}()
	f()
	return nil
}

// Get is JS `v[key]` for a string key: it throws on null and undefined like V8, reads own properties of objects,
// `length` and indices of arrays and strings (UTF-16), and is undefined on any other primitive.
func Get(v Value, key string) Value {
	switch x := v.(type) {
	case nil:
		Throw("Cannot read properties of null (reading '%s')", key)
	case undefinedType:
		Throw("Cannot read properties of undefined (reading '%s')", key)
	case *Object:
		return x.Get(key)
	case *Array:
		if key == "length" {
			return float64(len(x.Items))
		}
		if i, ok := index(key); ok && i < len(x.Items) {
			return x.Items[i]
		}
	case string:
		u := toUTF16(x)
		if key == "length" {
			return float64(len(u))
		}
		if i, ok := index(key); ok && i < len(u) {
			return fromUTF16(u[i : i+1])
		}
	}
	return Undefined
}

func index(key string) (int, bool) {
	if key == "" || (len(key) > 1 && key[0] == '0') {
		return 0, false
	}
	n, err := strconv.Atoi(key)
	return n, err == nil && n >= 0
}

// Obj returns v as an object, or nil when it is not one.
func Obj(v Value) *Object { o, _ := v.(*Object); return o }

// Ary returns v as an array, or nil when it is not one.
func Ary(v Value) *Array { a, _ := v.(*Array); return a }

// Str returns v as a string and whether it is one (typeof v === 'string').
func Str(v Value) (string, bool) { s, ok := v.(string); return s, ok }

// IsNullish is v == null.
func IsNullish(v Value) bool { return v == nil || v == Undefined }

// Or is JS `a || b`.
func Or(a, b Value) Value {
	if Truthy(a) {
		return a
	}
	return b
}

// Nullish is JS `a ?? b`.
func Nullish(a, b Value) Value {
	if IsNullish(a) {
		return b
	}
	return a
}
