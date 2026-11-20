package engine

import (
	"testing"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/guard/jsre"
)

func TestTrim16(t *testing.T) {
	in := "\ufeff\u00a0\u2028\t x y \u3000\u200a\n"
	if got := jsre.Str(trim16(u16(in))); got != "x y" {
		t.Fatalf("%q", got)
	}
	if got := jsre.Str(trim16(u16("\u0085x\u0085"))); got != "\u0085x\u0085" { // NEL is not JS whitespace
		t.Fatalf("%q", got)
	}
}

func TestHintLower(t *testing.T) {
	for in, want := range map[string]string{"GiT DIFF": "git diff", "\u0130git": "i̇git", "ma\u212ae": "make",
		"\uff27IT": "\uff27it", "\u03a3X": "\u03a3x"} {
		if got := jsre.Str(hintLower(u16(in))); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestJSToString(t *testing.T) {
	ok := map[string]string{`null`: "null", `true`: "true", `-0`: "0", `1e21`: "1e+21", `1e400`: "Infinity",
		`-1e400`: "-Infinity", `"s"`: "s", `[1,null,[2,[3]],"a"]`: "1,,2,3,a", `[]`: "", `{"a":1}`: "[object Object]",
		`{"valueOf":1}`: "[object Object]", `[{}]`: "[object Object]"}
	for in, want := range ok {
		v, err := Parse([]byte(in))
		if err != nil {
			t.Fatal(err)
		}
		got, good := jsToString(v)
		if !good || jsre.Str(got) != want {
			t.Errorf("%s: %q %v, want %q", in, jsre.Str(got), good, want)
		}
	}
	if s, _ := jsToString(nil); jsre.Str(s) != "undefined" {
		t.Error("undefined")
	}
	for _, in := range []string{`{"toString":1}`, `[1,{"toString":null}]`} {
		v, _ := Parse([]byte(in))
		if _, good := jsToString(v); good {
			t.Errorf("%s: JS throws a TypeError", in)
		}
	}
}

func TestMapKey(t *testing.T) {
	k := func(s string) string {
		if s == "" {
			key, _ := mapKey(nil)
			return key
		}
		v, _ := Parse([]byte(s))
		key, ok := mapKey(v)
		if !ok {
			return "object"
		}
		return key
	}
	if k(`1`) == k(`"1"`) || k(`-0`) != k(`0`) || k(`1.0`) != k(`1`) || k(``) == k(`null`) || k(`{}`) != "object" {
		t.Fatal("SameValueZero")
	}
}

func TestSplitJoin(t *testing.T) {
	ls := splitNL(u16("a\n\nb\n"))
	if len(ls) != 4 || jsre.Str(joinNL(ls)) != "a\n\nb\n" || units("a\U0001F600") != 3 {
		t.Fatal(ls)
	}
}
