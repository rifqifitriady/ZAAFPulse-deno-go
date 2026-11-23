package engine

import (
	"encoding/json"
	"strings"
	"testing"
)

// Every saveTokens case of gw/testdata/rtk: the body after and the result, a hand-off where the JS throws, and a
// hand-off for the goHandoff-* cases (spec D6, D7).
func TestRtkSaveFixtures(t *testing.T) {
	n := 0
	for _, c := range loadFixtures(t, "rtk") {
		if c.Fn != "saveTokens" {
			continue
		}
		n++
		t.Run(c.Name, func(t *testing.T) {
			var in [2]string
			if err := json.Unmarshal(c.In, &in); err != nil {
				t.Fatal(err)
			}
			handoff := strings.HasPrefix(c.Name, "goHandoff-")
			body, err := Parse([]byte(in[0]))
			if err != nil {
				if handoff {
					return // the engine does not serve this body at all (P4b)
				}
				t.Fatal(err)
			}
			res, why := SaveTokens(body, in[1], nil)
			switch {
			case c.Throws != "":
				if why != saverThrows {
					t.Fatalf("the JS throws %q; the engine: %q", c.Throws, why)
				}
				return
			case handoff:
				if why == "" {
					t.Fatal("the engine must hand this off")
				}
				return
			case why != "":
				t.Fatalf("handed off: %s", why)
			}
			var want struct {
				Body   json.RawMessage `json:"body"`
				Result json.RawMessage `json:"result"`
			}
			if err := json.Unmarshal(c.Out, &want); err != nil {
				t.Fatal(err)
			}
			wb, err := Parse(want.Body)
			if err != nil {
				t.Fatal(err)
			}
			if got, exp := string(body.Stringify()), string(wb.Stringify()); got != exp {
				t.Errorf("body\n got %.300s\nwant %.300s", got, exp)
			}
			var got any
			if res != nil {
				hits := make([]map[string]any, len(res.Hits))
				for i, h := range res.Hits {
					hits[i] = map[string]any{"filter": h.Filter, "saved": h.Saved}
				}
				got = map[string]any{"savedChars": res.SavedChars, "savedTokens": res.SavedTokens, "hits": hits}
			}
			sameJSON(t, c.Name, got, want.Result)
		})
	}
	if n < 50 {
		t.Fatalf("%d saveTokens fixtures (run node scripts/fixtures/gen-rtk.js)", n)
	}
}

// The scan alone: no job when nothing can change, and the hand-off reasons by name.
func TestScanSaver(t *testing.T) {
	cases := []struct {
		body, format, why string
		job               bool
	}{
		{`{"messages":[{"role":"tool","content":"short"}]}`, "openai", "", false},
		{`{"messages":"x"}`, "openai", "", false},
		{`{"messages":[{"role":"tool","content":"` + strings.Repeat("x", 600) + `"}]}`, "openai", "", true},
		{`{"messages":[{"role":"tool","content":"[error]` + strings.Repeat("x", 600) + `"}]}`, "openai", "", false},
		{`{"messages":[null]}`, "openai", saverThrows, false},
		{`{"messages":[{"tool_calls":[{"id":1,"function":{"arguments":"{\"command\":\"\\ud800\"}"}}]}]}`, "openai", saverArgs, false},
		{`{"messages":[{"role":"user","content":[{"type":"tool_result","content":"` + strings.Repeat("y", 700) + `"}]}]}`, "anthropic", "", true},
		{`{"messages":[{"role":"user","content":[{"type":"tool_result","is_error":true,"content":"` + strings.Repeat("y", 700) + `"}]}]}`, "anthropic", "", false},
	}
	for _, c := range cases {
		body, err := Parse([]byte(c.body))
		if err != nil {
			t.Fatal(err)
		}
		job, why := scanSaver(body, c.format)
		if why != c.why || (job != nil) != c.job {
			t.Errorf("%s: job %v why %q, want %v %q", c.body[:min(len(c.body), 60)], job != nil, why, c.job, c.why)
		}
	}
}
