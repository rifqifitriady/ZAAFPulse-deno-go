package engine

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

// Every case of gw/testdata/keys (scripts/fixtures/gen-keys.js, the real lib/gateway/keys.js) gives the same answer here.
func TestKeysFixtures(t *testing.T) {
	seen := map[string]int{}
	for _, c := range loadFixtures(t, "keys") {
		seen[c.Fn]++
		switch c.Fn {
		case "secret":
			var in struct {
				Headers map[string]string `json:"headers"`
				URL     string            `json:"url"`
			}
			mustDecode(t, c, &in)
			h := http.Header{}
			for k, v := range in.Headers {
				h.Set(k, v)
			}
			path, query, _ := strings.Cut(in.URL, "?")
			var got any
			if s, ok := RequestSecret(h, path, query); ok {
				got = s
			}
			sameJSON(t, c.Name, got, c.Out)
		case "allows":
			var in struct {
				Patterns []string `json:"patterns"`
				Value    string   `json:"value"`
			}
			mustDecode(t, c, &in)
			sameJSON(t, c.Name, Allows(in.Patterns, in.Value), c.Out)
		case "authenticate":
			var in struct {
				Keys   []ipc.GatewayKeyAuth `json:"keys"`
				Secret *string              `json:"secret"`
				Now    int64                `json:"now"`
			}
			mustDecode(t, c, &in)
			secret, ok := "", in.Secret != nil
			if ok {
				secret = *in.Secret
			}
			k, msg := NewKeys(in.Keys).Authenticate(secret, ok, in.Now)
			if k != nil {
				sameJSON(t, c.Name, map[string]string{"key": k.ID}, c.Out)
			} else {
				sameJSON(t, c.Name, map[string]any{"status": 401, "error": msg}, c.Out)
			}
		case "authorize":
			var in struct {
				Key  ipc.GatewayKeyAuth `json:"key"`
				Want struct{ Scope, Model, Agent string }
			}
			mustDecode(t, c, &in)
			var got any
			if why := Authorize(&in.Key, in.Want.Scope, in.Want.Model, in.Want.Agent); why != "" {
				got = why
			}
			sameJSON(t, c.Name, got, c.Out)
		case "rateLimit":
			var in struct {
				RPM   int64   `json:"rpm"`
				Times []int64 `json:"times"`
			}
			mustDecode(t, c, &in)
			r := NewRateLimiter()
			var got []int64
			for _, dt := range in.Times {
				got = append(got, r.Take("k", in.RPM, 1767225600000+dt))
			}
			sameJSON(t, c.Name, got, c.Out)
		case "canonicalize":
			// Every BMP code unit folds as JavaScript's /i folds it (the fixture lists the ones that change).
			var want map[string]string
			if err := json.Unmarshal(c.Out, &want); err != nil {
				t.Fatal(err)
			}
			bad := 0
			for u := 0; u < 0x10000; u++ {
				if (u >= 0xd800 && u <= 0xdfff) || divergentFold[rune(u)] {
					continue
				}
				exp := uint16(u)
				if v, ok := want[strconv.FormatInt(int64(u), 16)]; ok {
					n, _ := strconv.ParseUint(v, 16, 16)
					exp = uint16(n)
				}
				if got := canon(uint16(u)); got != exp {
					bad++
					if bad <= 10 {
						t.Errorf("canon(U+%04X) = U+%04X, JS U+%04X", u, got, exp)
					}
				}
			}
			if bad > 0 {
				t.Errorf("%d code units fold differently", bad)
			}
		default:
			t.Errorf("%s: unknown fn %q", c.Name, c.Fn)
		}
	}
	for _, fn := range []string{"secret", "allows", "authenticate", "authorize", "rateLimit", "canonicalize"} {
		if seen[fn] == 0 {
			t.Errorf("no %s fixtures", fn)
		}
	}
}

// Wait answers as Take would without counting the request (the shadow compare must not use up a key's limit).
func TestRateLimiterWaitDoesNotCount(t *testing.T) {
	r := NewRateLimiter()
	for range 3 {
		if w := r.Wait("k", 1, 0); w != 0 {
			t.Fatalf("Wait %d", w)
		}
	}
	if r.Take("k", 1, 0) != 0 || r.Wait("k", 1, 1) != 60 {
		t.Fatal("Take after Wait")
	}
}

func mustDecode(t *testing.T, c fixture, v any) {
	t.Helper()
	if err := json.Unmarshal(c.In, v); err != nil {
		t.Fatalf("%s: %v", c.Name, err)
	}
}

// Node and Deno upper-case three units differently (gen-keys.js leaves them out of the corpus): the v2 product runs on
// Deno, which does not fold them, so neither does the engine.
func TestUnitsTheRuntimesFoldDifferently(t *testing.T) {
	for _, u := range []uint16{0xa7cf, 0xa7d3, 0xa7d5} {
		if got := canon(u); got != u {
			t.Errorf("canon(U+%04X) = U+%04X, Deno keeps it", u, got)
		}
	}
	if Allows([]string{"oa/\ua7cf"}, "oa/\ua7ce") {
		t.Error("U+A7CF matched U+A7CE")
	}
}

// P4f D16: the control plane's takes count in the window, in time order, as keys.js note does.
func TestNoteCountsTheOtherEnginesTakes(t *testing.T) {
	r := NewRateLimiter()
	if w := r.Take("k", 3, 1_000_000); w != 0 {
		t.Fatal(w)
	}
	r.Note("k", 1_000_005)
	r.Note("k", 999_990)
	if w := r.Take("k", 3, 1_000_020); w != 60 {
		t.Fatalf("wait %d, want 60 (the oldest take is 30 ms old)", w)
	}
	r.mu.Lock()
	got := append([]int64(nil), r.windows["k"]...)
	r.mu.Unlock()
	if len(got) != 3 || got[0] != 999_990 || got[1] != 1_000_000 || got[2] != 1_000_005 {
		t.Fatalf("window %v", got)
	}
}

// A new session's window replaces the key's: a seeded full window refuses the next take with JS's wait.
func TestSeedThenTake(t *testing.T) {
	r := NewRateLimiter()
	r.Take("k", 2, 50) // an old take of a previous session, replaced by the seed
	r.Seed("k", []int64{2_000, 1_000})
	if w := r.Take("k", 2, 1_500); w != 60 {
		t.Fatalf("wait %d", w)
	}
	if w := r.Take("k", 2, 61_001); w != 0 {
		t.Fatalf("after the first seeded take aged out: wait %d", w)
	}
}

// Final review I3: a key the engine never takes for (its class on js, a hand-off) gets only notes; they are pruned to
// the last minute and capped at 10,000 (the largest rpm), not kept for the life of the gateway.
func TestNotesArePrunedAndCapped(t *testing.T) {
	r := NewRateLimiter()
	for i := int64(0); i < 100_000; i++ {
		r.Note("k", 1_000_000+i*1000) // one a second for 27 h
	}
	r.mu.Lock()
	n := len(r.windows["k"])
	r.mu.Unlock()
	if n > 60 {
		t.Fatalf("%d notes kept, want at most a minute's worth (60)", n)
	}
	for i := int64(0); i < 100_000; i++ {
		r.Note("burst", 5_000_000+i/10) // ten a millisecond: inside one minute
	}
	r.mu.Lock()
	n = len(r.windows["burst"])
	r.mu.Unlock()
	if n != maxTakes {
		t.Fatalf("%d notes kept in one minute, want the cap %d", n, maxTakes)
	}
}
