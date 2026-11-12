package engine

import (
	"encoding/json"
	"net/http"
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
		default:
			t.Errorf("%s: unknown fn %q", c.Name, c.Fn)
		}
	}
	for _, fn := range []string{"secret", "allows", "authenticate", "authorize", "rateLimit"} {
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
