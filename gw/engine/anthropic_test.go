package engine

import (
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

// xlateBase is the first xlate fixture: its config, secrets and key serve the unit tests (no config hand-written).
func xlateBase(t *testing.T) xlateCase {
	t.Helper()
	return loadXlate(t)[0].In
}

func xlateState(t *testing.T) *State { return xlateStateOf(t, xlateBase(t)) }

// xlateReq is a loopback request with the fixture key to path.
func xlateReq(t *testing.T, path, body string) *Request {
	c := xlateBase(t)
	c.Request.Path, c.Request.Body = path, body
	return xlateRequestOf(c)
}

// A chat call to an Anthropic-format target is the chat-anthropic leg (P4d): no hand-off, its own sub-class.
func TestChatAnthropicSubClass(t *testing.T) {
	s := xlateState(t)
	p := s.PlanChat(xlateReq(t, "/v1/chat/completions", `{"model":"an/claude-x","messages":[{"role":"user","content":"hi"}]}`), fixtureNow)
	if p.Handoff != "" || p.Upstream != UpAnthropic || SubClass("chat", &p, nil) != SubChatAnthropic {
		t.Fatalf("plan %q %q %q", p.Handoff, p.Upstream, p.Class)
	}
	p = s.PlanChat(xlateReq(t, "/v1/chat/completions", `{"model":"an/claude-x","stream":true,"messages":[]}`), fixtureNow)
	if p.Class != SubChatAnthropicStream {
		t.Fatal(p.Class)
	}
	// A hand-off is filed under the sub-class its body asks for.
	h := handoff("x")
	if SubClass("chat", &h, xlateReq(t, "/v1/chat/completions", `{"stream":true}`)) != SubChatStream {
		t.Fatal("hand-off sub-class")
	}
}

// OAuth2 and service accounts stay with the control plane, on every leg, when the state has no token registry (one no
// engine installed; P4g: SetState gives it the engine's).
func TestCredentialHandsOffOAuth(t *testing.T) {
	s := xlateState(t)
	for i := range s.Gateway.Providers {
		if s.Gateway.Providers[i].ID == "an" {
			s.Gateway.Providers[i].Auth = &ipc.ProviderAuth{Type: "oauth2"}
		}
	}
	p := s.PlanChat(xlateReq(t, "/v1/chat/completions", `{"model":"an/claude-x","messages":[]}`), fixtureNow)
	if p.Handoff != "provider: OAuth2 or a service account (P4g)" {
		t.Fatal(p.Handoff)
	}
}

// The synthesized stream: one data line per converted chunk, [DONE] after a clean end; a bad event is skipped, and
// a cut ends it without [DONE] (callChat's catch).
func TestAnthropicToChatText(t *testing.T) {
	start := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"usage\":{\"input_tokens\":3}}}\n\n"
	r := AnthropicToChatText("an/claude-x", iotest.OneByteReader(strings.NewReader(start+"event: x\ndata: {bad\n\n")), counterEnv())
	b, _ := io.ReadAll(r)
	if !strings.HasPrefix(string(b), "data: {") || !strings.HasSuffix(string(b), "data: [DONE]\n\n") {
		t.Fatalf("%q", b)
	}
	r = AnthropicToChatText("an/claude-x", io.MultiReader(strings.NewReader(start), iotest.ErrReader(ErrCut)), counterEnv())
	b, _ = io.ReadAll(r)
	if strings.Contains(string(b), "[DONE]") {
		t.Fatalf("%q", b)
	}
	// Closing the reader stops the conversion: nothing blocks.
	r = AnthropicToChatText("an/claude-x", strings.NewReader(strings.Repeat(start, 1000)), counterEnv())
	_ = r.Close()
}

func mustParse(t *testing.T, s string) *Value {
	t.Helper()
	v, err := Parse([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return v
}
