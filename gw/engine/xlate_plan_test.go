package engine

import (
	"strings"
	"testing"
)

func TestMapModel(t *testing.T) {
	s := xlateState(t) // modelMap: claude-* → oa/m1, gpt-codex* → op/gpt-6.1, gemini-* → gm/gemini-2.5-pro
	for _, c := range []struct {
		in, out string
		ok      bool
	}{
		{"oa/m1", "oa/m1", true}, {"fast", "fast", true}, {"router/x", "router/x", true},
		{"claude-sonnet-9", "oa/m1", true}, {"CLAUDE-x", "oa/m1", true}, // allows() folds case as keys.js does
		{"zz/none", "", false}, // no such provider: the model cache decides, so the call is handed off
		{"llama3", "", false},
	} {
		got, ok := s.MapModel(c.in)
		if got != c.out || ok != c.ok {
			t.Errorf("%q: %q %v", c.in, got, ok)
		}
	}
}

func TestDecodeURIComponent(t *testing.T) {
	for in, want := range map[string]string{"oa%2Fm1": "oa/m1", "a%3Ab": "a:b", "caf%C3%A9": "café", "plain": "plain"} {
		if got, ok := decodeURIComponent(in); !ok || got != want {
			t.Errorf("%q: %q %v", in, got, ok)
		}
	}
	for _, bad := range []string{"%", "%2", "%zz", "%C3", "%ED%A0%80", "%C0%AF"} {
		if _, ok := decodeURIComponent(bad); ok {
			t.Errorf("%q decoded", bad)
		}
	}
}

func TestGeminiPath(t *testing.T) {
	m, a, ok := geminiPath("/v1beta/models/pv/llama3:8b:streamGenerateContent")
	if !ok || m != "pv/llama3:8b" || a != "streamGenerateContent" {
		t.Fatal(m, a, ok)
	}
	if m, a, ok := geminiPath("/v1beta/models/x:generateContent:countTokens"); !ok || m != "x:generateContent" || a != "countTokens" {
		t.Fatal(m, a, ok)
	}
	for _, p := range []string{"/v1beta/models/:generateContent", "/v1beta/models/x:embed", "/v1beta/models"} {
		if _, _, ok := geminiPath(p); ok {
			t.Errorf("%s matched", p)
		}
	}
}

func TestAdaptChatSoftDrop(t *testing.T) {
	s := xlateState(t)
	var pv, gm *Target
	for i := range s.Gateway.Providers {
		switch s.Gateway.Providers[i].ID {
		case "pv":
			pv = &Target{Provider: &s.Gateway.Providers[i]}
		case "gm":
			gm = &Target{Provider: &s.Gateway.Providers[i]}
		}
	}
	body := mustParse(t, `{"model":"llama-9","reasoning_effort":"high","max_tokens":5}`)
	dropped := []string{"tool web_search"}
	AdaptChatTagged(pv.Provider, "llama-9", body, []string{"reasoning_effort"}, &dropped)
	if string(body.Stringify()) != `{"model":"llama-9","max_tokens":5}` || strings.Join(dropped, ",") != "tool web_search,reasoning" {
		t.Fatalf("%s %v", body.Stringify(), dropped)
	}
	AdaptChatTagged(pv.Provider, "llama-9", mustParse(t, `{"reasoning_effort":"low"}`), []string{"reasoning_effort"}, &dropped)
	if len(dropped) != 2 {
		t.Fatalf("reasoning named twice: %v", dropped)
	}
	for _, c := range []struct {
		t     *Target
		model string
	}{{pv, "gpt-oss-20b"}, {gm, "gemini-2.5-pro"}, {pv, "o3-mini"}} {
		b := mustParse(t, `{"reasoning_effort":"high"}`)
		AdaptChatTagged(c.t.Provider, c.model, b, []string{"reasoning_effort"}, nil)
		if b.Get("reasoning_effort") == nil {
			t.Errorf("%s takes reasoning_effort", c.model)
		}
	}
	// A reasoning_effort the client sent itself (not soft) is never dropped.
	b := mustParse(t, `{"reasoning_effort":"high"}`)
	AdaptChatTagged(pv.Provider, "llama-9", b, nil, &dropped)
	if b.Get("reasoning_effort") == nil {
		t.Fatal("the client's own field was dropped")
	}
}

// Every hand-off the translated planners keep (P4d Decision 14): nothing is sent, JS answers.
func TestTranslatedHandoffs(t *testing.T) {
	s := xlateState(t)
	for _, c := range []struct{ path, body, why string }{
		{"/v1/messages", `{"model":"oa/m1"}`, "body: messages is not an array"},
		{"/v1/messages", `{"model":7,"messages":[]}`, "model: missing"},
		{"/v1/messages", `{"model":"llama3","messages":[]}`, "model: mapping needs the model list"},
		{"/v1/messages", `{"model":"router/x","messages":[]}`, "model: a route"},
		{"/v1/messages", `{"model":"zz/none","messages":[]}`, "model: mapping needs the model list"},
		{"/v1/messages", `{"model":"oa/m1","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"x"}]}]}`, "token saver: tool results"},
		{"/v1/messages", `{"model":"oa/m1","messages":[],"tool_choice":{"type":"tool","name":"web_search"},"tools":[{"type":"web_search_20250305","name":"web_search"}]}`, "translate: the request cannot be translated"},
		{"/v1/responses", `{"model":"oa/m1","input":"hi","previous_response_id":"r1"}`, "translate: the request cannot be translated"},
		{"/v1/responses", `{"input":"hi"}`, "model: missing"},
		{"/v1beta/models/oa%2Gm1:generateContent", `{"contents":[]}`, "model: the path does not decode"},
		{"/v1beta/models", `{}`, "path: not a Gemini model call"},
		{"/v1/messages/x", `{}`, "path: not a messages call"},
	} {
		p := s.PlanFor(classOfPath(c.path), xlateReq(t, c.path, c.body), fixtureNow)
		if p.Handoff != c.why {
			t.Errorf("%s %s: %q", c.path, c.body, p.Handoff)
		}
	}
	// The token saver off by header: translated and served.
	r := xlateReq(t, "/v1/messages", `{"model":"oa/m1","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"x"}]}]}`)
	r.Header.Set("X-Zp-Token-Saver", "off")
	if p := s.PlanFor("messages", r, fixtureNow); p.Handoff != "" {
		t.Fatal(p.Handoff)
	}
}

// The refusal shapes the engine writes itself: a 429 in each client's format (the Google shape has no Retry-After).
func TestRefusalShapes(t *testing.T) {
	a := shapeError(ShapeAnthropic, 429, "slow", "rate_limit_error", "", "60")
	if a.Header.Get("Retry-After") != "60" || string(a.Body) != `{"type":"error","error":{"type":"rate_limit_error","message":"slow"}}` {
		t.Fatalf("%v %s", a.Header, a.Body)
	}
	g := shapeError(ShapeGemini, 400, "blocked", "invalid_request_error", "guardrail_blocked", "")
	if want := `{"error":{"code":400,"message":"blocked","status":"INVALID_ARGUMENT","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"guardrail_blocked","domain":"zaafpulse"}]}}`; string(g.Body) != want {
		t.Fatalf("%s", g.Body)
	}
	if g = shapeError(ShapeGemini, 429, "slow", "", "", "60"); g.Header.Get("Retry-After") != "" {
		t.Fatal("a Gemini 429 has no Retry-After")
	}
}
