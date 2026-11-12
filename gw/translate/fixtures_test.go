package translate

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"
)

const fixtureDir = "../testdata/translate"

// fixedEnv is the generator's determinism: Date.now() 1767225600000, randomBytes(n) = n bytes of a counter that
// starts at 1 for each case.
func fixedEnv() *Env {
	n := 0
	return &Env{
		Now: func() int64 { return 1767225600000 },
		Random: func(k int) []byte {
			n++
			b := make([]byte, k)
			for i := range b {
				b[i] = byte(n)
			}
			return b
		},
	}
}

type fixtureFn func(e *Env, args []jsv.Value) (jsv.Value, error)

// fixtureFns maps each generator fn name to its Go port.
var fixtureFns = map[string]fixtureFn{}

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
			checkFixture(t, fx, func() (jsv.Value, error) { return fn(fixedEnv(), fx.In) })
		})
	}
}

func checkFixture(t *testing.T, fx jsv.Fixture, run func() (jsv.Value, error)) {
	t.Helper()
	got, err := run()
	if fx.Threw {
		if err == nil {
			t.Fatalf("want error %q, got %s", fx.Throws, jsv.Canonical(got))
		}
		if err.Error() != fx.Throws {
			t.Fatalf("error\n got: %s\nwant: %s", err, fx.Throws)
		}
		if bad, ok := err.(*BadRequest); ok != (fx.Status == 400) || (ok && bad.Status() != 400) {
			t.Fatalf("status: error %T, fixture status %v", err, fx.Status)
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

func TestFixturesFolderIsNotEmpty(t *testing.T) {
	if _, err := os.Stat(fixtureDir); err != nil {
		t.Fatal(err)
	}
}
