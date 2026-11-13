package jsv

import (
	"encoding/base64"
	"errors"
	"os"
	"strconv"
	"strings"
)

// Fixture is one file written by scripts/fixtures/lib.js: { fn, in, out } or { fn, in, throws, status?, hits? }.
type Fixture struct {
	Fn     string
	In     []Value
	Out    Value // Undefined when the case threw
	Throws string
	Threw  bool
	Status float64
	Hits   Value
}

// LoadFixture reads a fixture file, putting back the secret values the generator replaced with <<ZPSECRET:i>>.
func LoadFixture(path string) (Fixture, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Fixture{}, err
	}
	v, err := Parse(data)
	if err != nil {
		return Fixture{}, err
	}
	o := Obj(v)
	if o == nil {
		return Fixture{}, errors.New("fixture is not an object")
	}
	if secrets := Ary(o.Get("secrets")); secrets != nil {
		text := string(data)
		for i, s := range secrets.Items {
			raw, err := base64.StdEncoding.DecodeString(ToString(s))
			if err != nil {
				return Fixture{}, err
			}
			var b strings.Builder
			Quote(&b, string(raw))
			q := b.String()
			text = strings.ReplaceAll(text, "<<ZPSECRET:"+strconv.Itoa(i)+">>", q[1:len(q)-1])
		}
		if v, err = Parse([]byte(text)); err != nil {
			return Fixture{}, err
		}
		o = Obj(v)
	}
	f := Fixture{Fn: ToString(o.Get("fn")), Out: o.Get("out"), Hits: o.Get("hits")}
	if in := Ary(o.Get("in")); in != nil {
		f.In = in.Items
	}
	if t, ok := o.Get("throws").(string); ok {
		f.Throws, f.Threw = t, true
	}
	if s, ok := o.Get("status").(float64); ok {
		f.Status = s
	}
	return f, nil
}

// Arg returns args[i], or Undefined past the end (a JS call with fewer arguments).
func Arg(args []Value, i int) Value {
	if i < len(args) {
		return args[i]
	}
	return Undefined
}
