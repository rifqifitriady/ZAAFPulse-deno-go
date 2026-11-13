package guard

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"
)

const fixtureDir = "../testdata/guard"

type fixtureFn func(args []jsv.Value) (jsv.Value, error)

var fixtureFns = map[string]fixtureFn{
	"scanInjection": func(a []jsv.Value) (jsv.Value, error) {
		r, err := ScanInjection(jsv.ToString(jsv.Arg(a, 0)), nil)
		if err != nil {
			return nil, err
		}
		if r == nil {
			return nil, nil
		}
		return jsv.NewObject().Set("id", r.ID).Set("label", r.Label).Set("at", float64(r.At)).Set("match", r.Match).
			Set("categories", strArr(r.Categories)).Set("labels", strArr(r.Labels)), nil
	},
	"secretSpans": func(a []jsv.Value) (jsv.Value, error) {
		spans, err := SecretSpans(jsv.ToString(jsv.Arg(a, 0)), nil)
		if err != nil {
			return nil, err
		}
		out := jsv.Arr()
		for _, s := range spans {
			out.Push(jsv.NewObject().Set("start", float64(s.Start)).Set("end", float64(s.End)).
				Set("label", s.Label).Set("matchStart", float64(s.MatchStart)))
		}
		return out, nil
	},
	"maskPii": func(a []jsv.Value) (jsv.Value, error) {
		text, counts, err := MaskPII(jsv.ToString(jsv.Arg(a, 0)), nil)
		if err != nil {
			return nil, err
		}
		c := jsv.NewObject()
		for k, n := range counts {
			c.Set(k, float64(n))
		}
		return jsv.NewObject().Set("text", text).Set("counts", c), nil
	},
}

// normalizeFixture adjusts a fixture's expected output before the comparison (per fn).
var normalizeFixture = map[string]func(jsv.Value) jsv.Value{}

func strArr(ss []string) *jsv.Array {
	a := jsv.Arr()
	for _, s := range ss {
		a.Push(s)
	}
	return a
}

func TestFixtures(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(fixtureDir, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures in %s: %v", fixtureDir, err)
	}
	sort.Strings(files)
	for _, file := range files {
		name := strings.TrimSuffix(filepath.Base(file), ".json")
		t.Run(name, func(t *testing.T) {
			fx, err := jsv.LoadFixture(file)
			if err != nil {
				t.Fatal(err)
			}
			fn, ok := fixtureFns[fx.Fn]
			if !ok {
				t.Fatalf("no Go port registered for %s", fx.Fn)
			}
			if norm := normalizeFixture[fx.Fn]; norm != nil && fx.Out != jsv.Undefined {
				fx.Out = norm(fx.Out)
			}
			got, err := fn(fx.In)
			checkFixture(t, fx, got, err)
		})
	}
}

func checkFixture(t *testing.T, fx jsv.Fixture, got jsv.Value, err error) {
	t.Helper()
	if fx.Threw {
		if err == nil {
			t.Fatalf("want error %q, got %s", fx.Throws, jsv.Canonical(got))
		}
		if err.Error() != fx.Throws {
			t.Fatalf("error\n got: %s\nwant: %s", err, fx.Throws)
		}
		if fx.Hits != jsv.Undefined {
			b, ok := err.(*Block)
			if !ok {
				t.Fatalf("want a *Block, got %T", err)
			}
			if g, w := jsv.Canonical(hitsValue(b.Hits)), jsv.Canonical(fx.Hits); g != w {
				t.Fatalf("hits\n got: %s\nwant: %s", g, w)
			}
		}
		return
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if g, w := jsv.Canonical(got), jsv.Canonical(fx.Out); g != w {
		t.Fatalf("output differs\n got: %s\nwant: %s", g, w)
	}
}
