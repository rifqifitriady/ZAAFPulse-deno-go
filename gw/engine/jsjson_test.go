package engine

import (
	"encoding/json"
	"errors"
	"regexp"
	"testing"
)

// A text that holds an escaped lone surrogate once JavaScript has written it again.
var loneSurrogate = regexp.MustCompile(`\\ud[89a-f][0-9a-f]{2}`)

// Every case of gw/testdata/jsjson (JSON.stringify(JSON.parse(text)) from scripts/fixtures/gen-jsjson.js): the same
// bytes, the same refusal, or ErrUnsupported only where JavaScript would keep a lone surrogate.
func TestJSJSONFixtures(t *testing.T) {
	for _, c := range loadFixtures(t, "jsjson") {
		var in string
		var out *string
		if err := json.Unmarshal(c.In, &in); err != nil {
			t.Fatal(err)
		}
		_ = json.Unmarshal(c.Out, &out)
		v, err := Parse([]byte(in))
		switch {
		case out == nil:
			if !errors.Is(err, ErrSyntax) {
				t.Errorf("%s: JSON.parse throws, Go: %v", c.Name, err)
			}
		case errors.Is(err, ErrUnsupported):
			if !loneSurrogate.MatchString(*out) {
				t.Errorf("%s: unsupported, but JavaScript gives %q", c.Name, *out)
			}
		case err != nil:
			t.Errorf("%s: %v, JavaScript gives %q", c.Name, err, *out)
		default:
			if got := string(v.Stringify()); got != *out {
				t.Errorf("%s:\n got %s\nwant %s", c.Name, got, *out)
			}
		}
	}
}

func TestDeepNestingIsUnsupported(t *testing.T) {
	deep := make([]byte, 0, 2*MaxDepth+2)
	for range MaxDepth + 1 {
		deep = append(deep, '[')
	}
	for range MaxDepth + 1 {
		deep = append(deep, ']')
	}
	if _, err := Parse(deep); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("%v", err)
	}
}

// Set keeps a key's place, appends a new one; Delete removes it; JavaScript's order applies when written.
func TestObjectEdits(t *testing.T) {
	v, err := Parse([]byte(`{"model":"a","max_tokens":5,"7":1}`))
	if err != nil {
		t.Fatal(err)
	}
	v.Set("model", Str("b"))
	v.Set("max_completion_tokens", v.Get("max_tokens"))
	v.Delete("max_tokens")
	if got := string(v.Stringify()); got != `{"7":1,"model":"b","max_completion_tokens":5}` {
		t.Fatal(got)
	}
}
