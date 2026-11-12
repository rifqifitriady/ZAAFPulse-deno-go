package provauth

import (
	"math"
	"testing"
)

// The values below were measured with Node 24 and Deno 2.9.8 (URLSearchParams, JSON.stringify, Number).
func TestEncodeFormMatchesURLSearchParams(t *testing.T) {
	got := encodeForm([]field{{"a", "x y~*-._!'()é+&="}, {"b c", ""}})
	want := "a=x+y%7E*-._%21%27%28%29%C3%A9%2B%26%3D&b+c="
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestJSStringMatchesJSONStringify(t *testing.T) {
	got := jsString("\u2028\u001f<>&é\"\\\n\t\b\f\r")
	want := "\"\u2028\\u001f<>&é\\\"\\\\\\n\\t\\b\\f\\r\""
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestJSNumber(t *testing.T) {
	cases := []struct {
		in   any
		want float64
	}{
		{" 60 ", 60}, {"0x10", 16}, {"0X1f", 31}, {"0b101", 5}, {"0o17", 15}, {"1e3", 1000}, {"", 0}, {"  ", 0},
		{".5", 0.5}, {"5.", 5}, {"+7", 7}, {"-7", -7}, {"Infinity", math.Inf(1)}, {"-Infinity", math.Inf(-1)},
		{true, 1}, {false, 0}, {3.5, 3.5}, {"1e400", math.Inf(1)},
	}
	for _, c := range cases {
		if got := jsNumber(c.in); got != c.want {
			t.Errorf("jsNumber(%#v) = %v, want %v", c.in, got, c.want)
		}
	}
	for _, in := range []any{nil, "60s", "inf", "infinity", "0x", "-0x10", "1_000", "NaN", "e5", []any{}, map[string]any{}} {
		if got := jsNumber(in); !math.IsNaN(got) {
			t.Errorf("jsNumber(%#v) = %v, want NaN", in, got)
		}
	}
}
