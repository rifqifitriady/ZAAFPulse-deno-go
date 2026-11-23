package jsv

import (
	"math"
	"strings"
	"testing"
)

func mustParse(t *testing.T, s string) Value {
	t.Helper()
	v, err := Parse([]byte(s))
	if err != nil {
		t.Fatalf("parse %s: %v", s, err)
	}
	return v
}

func TestParseKeepsOrderAndDuplicatePosition(t *testing.T) {
	o := mustParse(t, `{"b":1,"a":2,"b":3}`).(*Object)
	if got := strings.Join(o.Keys(), ","); got != "b,a" {
		t.Fatalf("keys %q", got)
	}
	if o.Get("b") != 3.0 {
		t.Fatalf("b = %v", o.Get("b"))
	}
	if s, _ := Stringify(o); s != `{"b":3,"a":2}` {
		t.Fatalf("stringify %s", s)
	}
}

func TestStringify(t *testing.T) {
	o := NewObject().Set("a", Undefined).Set("b", Arr(Undefined, nil, true))
	if s, _ := Stringify(o); s != `{"b":[null,null,true]}` {
		t.Fatalf("got %s", s)
	}
	if _, ok := Stringify(Undefined); ok {
		t.Fatal("undefined stringifies to nothing")
	}
	cases := map[string]Value{
		"\"<\u2028é\\u0001\\n\\\"\\\\/\"": "<\u2028é\x01\n\"\\/",
		`1e+21`:                           1e21,
		`1e-7`:                            1e-7,
		`0.000001`:                        0.000001,
		`0`:                               math.Copysign(0, -1),
		`123456789012345680000`:           123456789012345678901.0,
		`0.30000000000000004`:             0.30000000000000004,
		`null`:                            math.NaN(),
		`-1.5e-10`:                        -1.5e-10,
		`100`:                             100.0,
		`1.7976931348623157e+308`:         math.MaxFloat64,
		`5e-324`:                          5e-324,
	}
	for want, v := range cases {
		if s, _ := Stringify(v); s != want {
			t.Errorf("Stringify(%v) = %s, want %s", v, s, want)
		}
	}
}

func TestToStringAndNumber(t *testing.T) {
	strs := []struct {
		v    Value
		want string
	}{
		{mustParse(t, `[1,[2,null]]`), "1,2,"},
		{NewObject(), "[object Object]"},
		{Undefined, "undefined"},
		{nil, "null"},
		{1e21, "1e+21"},
		{true, "true"},
		{math.Inf(-1), "-Infinity"},
	}
	for _, c := range strs {
		if got := ToString(c.v); got != c.want {
			t.Errorf("ToString(%v) = %q, want %q", c.v, got, c.want)
		}
	}
	nums := []struct {
		v    Value
		want float64
	}{
		{" 12 ", 12}, {"", 0}, {"0x1f", 31}, {true, 1}, {nil, 0}, {Arr(5.0), 5}, {Arr(), 0}, {"1e3", 1000},
		{" -2.5\n", -2.5}, {"Infinity", math.Inf(1)},
	}
	for _, c := range nums {
		if got := ToNumber(c.v); got != c.want {
			t.Errorf("ToNumber(%#v) = %v, want %v", c.v, got, c.want)
		}
	}
	for _, v := range []Value{"abc", NewObject(), Undefined, "1_000", Arr(1.0, 2.0)} {
		if !math.IsNaN(ToNumber(v)) {
			t.Errorf("ToNumber(%#v) not NaN", v)
		}
	}
}

func TestTruthy(t *testing.T) {
	for _, v := range []Value{nil, Undefined, false, 0.0, "", math.NaN()} {
		if Truthy(v) {
			t.Errorf("%#v truthy", v)
		}
	}
	for _, v := range []Value{true, 1.0, "0", NewObject(), Arr()} {
		if !Truthy(v) {
			t.Errorf("%#v falsy", v)
		}
	}
}

func TestGetThrowsLikeV8(t *testing.T) {
	err := Catch(func() { Get(Undefined, "name") })
	if err == nil || err.Error() != "Cannot read properties of undefined (reading 'name')" {
		t.Fatalf("got %v", err)
	}
	err = Catch(func() { Get(nil, "x") })
	if err == nil || err.Error() != "Cannot read properties of null (reading 'x')" {
		t.Fatalf("got %v", err)
	}
	if Get("str", "x") != Undefined || Get(1.0, "x") != Undefined {
		t.Fatal("primitive property should be undefined")
	}
	if Get("héllo", "length") != 5.0 || Get(Arr(1.0, 2.0), "length") != 2.0 || Get(Arr(1.0, 2.0), "1") != 2.0 {
		t.Fatal("length/index")
	}
}

func TestCanonicalAndUTF16Len(t *testing.T) {
	v := mustParse(t, `{"b":{"y":1,"x":[{"d":1,"c":2}]},"a":"é"}`)
	if got := Canonical(v); got != `{"a":"é","b":{"x":[{"c":2,"d":1}],"y":1}}` {
		t.Fatalf("got %s", got)
	}
	if UTF16Len("a😀é") != 4 {
		t.Fatal("utf16 len")
	}
}

func TestIndexKeysComeFirst(t *testing.T) {
	o := mustParse(t, `{"b":1,"10":2,"2":3,"01":4}`).(*Object)
	if got := strings.Join(o.Keys(), ","); got != "2,10,b,01" {
		t.Fatalf("keys %q", got)
	}
}
