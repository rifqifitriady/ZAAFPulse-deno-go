package engine

import (
	"strings"
	"testing"
	"time"
)

// The client's anthropic-version and anthropic-beta reach the provider as anthropicHeaders keeps them: after the
// provider's own headers, at most 1000 characters, no CR or LF.
func TestAnthropicHeadersKept(t *testing.T) {
	s := xlateState(t)
	r := xlateReq(t, "/v1/messages", `{"model":"an/claude-x","messages":[]}`)
	r.Header.Set("Anthropic-Beta", "context-1m")
	r.Header.Set("Anthropic-Version", strings.Repeat("x", 1001))
	p := s.PlanMessages(r, fixtureNow)
	if p.Upstream != UpAnthropicNative || p.Header.Get("Anthropic-Beta") != "context-1m" ||
		p.Header.Get("Anthropic-Version") != "2023-06-01" || p.Header.Get("X-Api-Key") == "" {
		t.Fatalf("%q %v", p.Handoff, p.Header)
	}
	r.Header.Set("Anthropic-Version", "2024-01-01")
	if p = s.PlanMessages(r, fixtureNow); p.Header.Get("Anthropic-Version") != "2024-01-01" {
		t.Fatal(p.Header)
	}
}

// A passthrough event's data goes out as it came unless its JSON is the one rewritten; a falsy or bad JSON value is
// passed on and read for nothing.
func TestPassthroughEventRewrite(t *testing.T) {
	s := xlateState(t)
	p := s.PlanMessages(xlateReq(t, "/v1/messages", `{"model":"an/claude-x","stream":true,"messages":[]}`), fixtureNow)
	in := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"up\",\"usage\":{\"input_tokens\":2}}}\n\n" +
		"event: ping\ndata: {\"type\":\"ping\"}\n\n" + "event: x\ndata: not json\n\n" + "event: y\ndata: null\n\n"
	w := &recordWriter{}
	line := s.Deliver(&p, ProviderResult{Status: 200, Header: map[string][]string{}, Body: nopBody(in)}, func() float64 { return 1 }, w)
	want := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"an/claude-x\",\"usage\":{\"input_tokens\":2}}}\n\n" +
		"event: ping\ndata: {\"type\":\"ping\"}\n\n" + "event: x\ndata: not json\n\n" + "event: y\ndata: null\n\n"
	if w.body.String() != want || *line.Input != 2 || line.TTFT.Set && !line.TTFT.Null {
		t.Fatalf("%q %+v", w.body.String(), line)
	}
}

// The token saver on, a long tool result in an Anthropic passthrough: planned with the saver, savedTokens kept on every
// line (rtk spec D12); a message saveTokens cannot read is handed off (JS answers).
func TestPassthroughTokenSaver(t *testing.T) {
	s := xlateState(t)
	long := `{"model":"an/claude-x","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":` +
		string(mustJSON(strings.Repeat("2026-11-17 INFO step\n", 300))) + `}]}]}`
	p := s.PlanMessages(xlateReq(t, "/v1/messages", long), fixtureNow)
	if p.Handoff != "" || p.saver == nil || !p.keepSaved || len(p.Body) != 0 {
		t.Fatalf("handoff %q, saver %v, keepSaved %v, body %d", p.Handoff, p.saver != nil, p.keepSaved, len(p.Body))
	}
	if why := s.Save(&p, time.Now()); why != "" || p.Base.SavedTokens == nil || len(p.Body) == 0 {
		t.Fatalf("save %q, savedTokens %v, body %d", why, p.Base.SavedTokens, len(p.Body))
	}
	p = s.PlanMessages(xlateReq(t, "/v1/messages", `{"model":"an/claude-x","messages":[null]}`), fixtureNow)
	if p.Handoff != saverThrows {
		t.Fatal(p.Handoff)
	}
}
