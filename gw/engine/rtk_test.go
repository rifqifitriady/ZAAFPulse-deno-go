package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/guard/jsre"
)

func rtkOut(text []uint16, hit *rtkHit) any {
	if hit == nil {
		return nil
	}
	return map[string]any{"text": jsre.Str(text), "filter": hit.Filter, "saved": hit.Saved}
}

func buildRecipe(t *testing.T, raw json.RawMessage) string {
	var recipe [][2]json.RawMessage
	if err := json.Unmarshal(raw, &recipe); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, r := range recipe {
		var s string
		var n int
		_ = json.Unmarshal(r[0], &s)
		_ = json.Unmarshal(r[1], &n)
		b.WriteString(strings.Repeat(s, n))
	}
	return b.String()
}

// Every compressText, pickFilter and compressBig case of gw/testdata/rtk (scripts/fixtures/gen-rtk.js).
func TestRtkTextFixtures(t *testing.T) {
	seen := map[string]int{}
	for _, c := range loadFixtures(t, "rtk") {
		if c.Fn == "saveTokens" {
			continue
		}
		seen[c.Fn]++
		t.Run(c.Name, func(t *testing.T) {
			var in []json.RawMessage
			if err := json.Unmarshal(c.In, &in); err != nil {
				t.Fatal(err)
			}
			var hint string
			_ = json.Unmarshal(in[1], &hint)
			handoff := strings.HasPrefix(c.Name, "goHandoff-")
			switch c.Fn {
			case "compressText":
				raw, err := Parse(in[0])
				if err != nil {
					t.Fatal(err)
				}
				var got any
				if raw.Kind == String {
					text, hit, why := compressText(u16(raw.S), u16(hint), nil)
					if handoff {
						if why == "" {
							t.Fatal("the engine must hand this off")
						}
						return
					}
					if why != "" {
						t.Fatalf("handed off: %s", why)
					}
					got = rtkOut(text, hit)
				}
				sameJSON(t, c.Name, got, c.Out)
			case "pickFilter":
				var text string
				_ = json.Unmarshal(in[0], &text)
				name, _, why := pickFilter(u16(text), u16(hint), nil)
				if why != "" {
					t.Fatalf("handed off: %s", why)
				}
				var got any
				if name != "" {
					got = name
				}
				sameJSON(t, c.Name, got, c.Out)
			case "compressBig":
				text, hit, why := compressText(u16(buildRecipe(t, in[0])), u16(hint), nil)
				if why != "" {
					t.Fatalf("handed off: %s", why)
				}
				var got any
				if hit != nil {
					s := jsre.Str(text)
					sum := sha256.Sum256([]byte(s))
					got = map[string]any{"filter": hit.Filter, "saved": hit.Saved, "length": len(text),
						"sha256": hex.EncodeToString(sum[:])}
				}
				sameJSON(t, c.Name, got, c.Out)
			default:
				t.Fatalf("unknown fn %q", c.Fn)
			}
		})
	}
	if seen["compressText"] < 250 || seen["pickFilter"] < 30 || seen["compressBig"] < 6 {
		t.Fatalf("fixtures missing: %v (run node scripts/fixtures/gen-rtk.js)", seen)
	}
}
