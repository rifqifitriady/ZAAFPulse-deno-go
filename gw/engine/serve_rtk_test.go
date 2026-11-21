package engine

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// The token-saver switch (spec D13, owner answer 22): a chat call the saver looks at is served on go only when chat
// and token-saver are both on go; otherwise it goes to js, and the provider sees nothing from the engine.
func TestServeTokenSaverSwitch(t *testing.T) {
	diff := "diff --git a/f.js b/f.js\n@@ -1,90 +1,90 @@\n" + strings.Repeat("+ line\n", 90)
	body := `{"model":"oa/m1","messages":[{"role":"user","content":"go"},` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"Bash","arguments":"{\"command\":\"git diff\"}"}}]},` +
		`{"role":"tool","tool_call_id":"c1","content":` + string(mustJSON(diff)) + `}]}`
	for _, c := range []struct {
		name  string
		saver bool
	}{{"token-saver on js", false}, {"token-saver on go", true}} {
		t.Run(c.name, func(t *testing.T) {
			rig, sent := guardRig(t, func(s *State) { s.Gateway.TokenSaver = true })
			rig.e.SetModes(func(sub string) (bool, bool) { return sub == "chat" || (sub == SaverSwitch && c.saver), false })
			var toJS atomic.Int32
			js := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { toJS.Add(1); w.WriteHeader(299) })
			gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				rig.e.Serve("chat", w, r, "/v1/chat/completions", js)
			}))
			defer gw.Close()
			rig.url = gw.URL + "/v1/chat/completions"
			resp := rig.post(context.Background(), t, body)
			_, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if !c.saver {
				if resp.StatusCode != 299 || toJS.Load() != 1 || len(sent()) != 0 {
					t.Fatalf("status %d, to js %d, provider calls %d", resp.StatusCode, toJS.Load(), len(sent()))
				}
				return
			}
			if resp.StatusCode != 200 || toJS.Load() != 0 || len(sent()) != 1 {
				t.Fatalf("status %d, to js %d, provider calls %d", resp.StatusCode, toJS.Load(), len(sent()))
			}
			if up := sent()[0]; !strings.Contains(up, "rest of hunk omitted") {
				t.Fatalf("upstream body not shortened: %.200s", up)
			}
			if l := rig.oneLine(t); l.SavedTokens == nil || *l.SavedTokens <= 0 {
				t.Fatalf("line %+v", l)
			}
		})
	}
}

// A Save hand-off forwards to js before the rate limit counts the call (spec D9): a tool result whose filters would
// run past the step budget.
func TestServeSaverOverBudgetGoesToJS(t *testing.T) {
	rig, sent := guardRig(t, func(s *State) { s.Gateway.TokenSaver = true })
	rig.e.SetModes(func(string) (bool, bool) { return true, false })
	var toJS atomic.Int32
	js := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		toJS.Add(1)
		b, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(b), `"role":"tool"`) {
			t.Errorf("js got a different body: %.100s", b)
		}
		w.WriteHeader(299)
	})
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rig.e.Serve("chat", w, r, "/v1/chat/completions", js)
	}))
	defer gw.Close()
	rig.url = gw.URL + "/v1/chat/completions"
	body := `{"model":"oa/m1","messages":[{"role":"tool","tool_call_id":"c","content":` +
		string(mustJSON(strings.Repeat("\x1b]", 300000))) + `}]}`
	resp := rig.post(context.Background(), t, body)
	_ = resp.Body.Close()
	if resp.StatusCode != 299 || toJS.Load() != 1 || len(sent()) != 0 {
		t.Fatalf("status %d, to js %d, provider calls %d", resp.StatusCode, toJS.Load(), len(sent()))
	}
}
