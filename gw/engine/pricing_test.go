package engine

import (
	"encoding/json"
	"testing"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

// Every case of gw/testdata/pricing (lib/pricing.js and lib/gateway/usage.js, scripts/fixtures/gen-pricing.js).
func TestPricingFixtures(t *testing.T) {
	var table map[string]ipc.PriceEntry
	cases := loadFixtures(t, "pricing")
	for _, c := range cases {
		if c.Fn == "table" {
			if err := json.Unmarshal(c.Out, &table); err != nil {
				t.Fatal(err)
			}
		}
	}
	p := NewPricer(table)
	for _, c := range cases {
		switch c.Fn {
		case "table":
		case "normalize":
			var m string
			mustDecode(t, c, &m)
			sameJSON(t, c.Name, NormalizeModel(m), c.Out)
		case "key":
			var m string
			mustDecode(t, c, &m)
			var got any
			if k := p.Key(m); k != "" {
				got = k
			}
			sameJSON(t, c.Name, got, c.Out)
		case "cost":
			var in struct {
				Model                 string
				Input, Output, Cached float64
			}
			mustDecode(t, c, &in)
			sameJSON(t, c.Name, p.Cost(in.Model, in.Input, in.Output, in.Cached), c.Out)
		default:
			t.Errorf("%s: unknown fn %q", c.Name, c.Fn)
		}
	}
}
