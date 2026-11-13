package engine

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

const fixtureNow = 1767225600000

type chatCase struct {
	Config  ipc.ConfigPush     `json:"config"`
	Secrets ipc.SecretsHandoff `json:"secrets"`
	Request struct {
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	} `json:"request"`
	Provider struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
		Throw  string `json:"throw"`
	} `json:"provider"`
}

type chatOut struct {
	Request *struct {
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	} `json:"request"`
	Client struct {
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	} `json:"client"`
	Usage json.RawMessage `json:"usage"`
}

func stateFor(t *testing.T, c chatCase) *State {
	t.Helper()
	// A copy of its own: a test that edits the state must not change the next one's.
	b, _ := json.Marshal(c)
	c = chatCase{}
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	c.Config.Gen, c.Secrets.Gen = 1, 1
	return NewState(&c.Config, &c.Secrets).WithSnapshot(&ipc.BudgetSnapshot{At: fixtureNow, KeyCenter: map[string]string{}})
}

func requestFor(c chatCase) *Request {
	h := http.Header{}
	for k, v := range c.Request.Headers {
		h.Set(k, v)
	}
	return &Request{Method: "POST", RawPath: "/v1/chat/completions", Header: h, Host: "127.0.0.1:4777",
		Peer: "127.0.0.1:50000", Body: []byte(c.Request.Body)}
}

// Every case of gw/testdata/chat (the JS gateway's own answers, scripts/fixtures/gen-chat.js): the engine serves it,
// sends the provider the same request, and gives the client the same answer and the same usage line.
func TestChatFixtures(t *testing.T) {
	for _, f := range loadFixtures(t, "chat") {
		if f.Fn == "presets" {
			sameJSON(t, f.Name, Presets, f.Out)
			continue
		}
		if f.Fn == "chatStream" {
			continue // TestChatStreamFixtures
		}
		var c chatCase
		var want chatOut
		if err := json.Unmarshal(f.In, &c); err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		if err := json.Unmarshal(f.Out, &want); err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		s := stateFor(t, c)
		p := s.PlanChat(requestFor(c), fixtureNow)
		if p.Handoff != "" {
			t.Errorf("%s: handed off: %s", f.Name, p.Handoff)
			continue
		}
		got := map[string]string{}
		for k := range p.Header {
			got[strings.ToLower(k)] = p.Header.Get(k)
		}
		if p.URL != want.Request.URL || string(p.Body) != want.Request.Body {
			t.Errorf("%s: provider request\n got %s %s\nwant %s %s", f.Name, p.URL, p.Body, want.Request.URL, want.Request.Body)
		}
		sameJSON(t, f.Name+" provider headers", got, mustJSON(want.Request.Headers))
		var a Answer
		if c.Provider.Throw != "" {
			why := c.Provider.Throw
			if why == "timeout" {
				why = "timed out"
			}
			a = s.Failed(&p, why, 1)
		} else {
			a = s.Respond(&p, c.Provider.Status, []byte(c.Provider.Body), 1)
		}
		if a.Status != want.Client.Status || string(a.Body) != want.Client.Body {
			t.Errorf("%s: client answer\n got %d %s\nwant %d %s", f.Name, a.Status, a.Body, want.Client.Status, want.Client.Body)
		}
		for _, h := range []string{"content-type", "cache-control", "access-control-allow-origin"} {
			if a.Header.Get(h) != want.Client.Headers[h] {
				t.Errorf("%s: %s %q, want %q", f.Name, h, a.Header.Get(h), want.Client.Headers[h])
			}
		}
		if a.Header.Get("Retry-After") != "" {
			t.Errorf("%s: the provider's retry-after reached the client", f.Name)
		}
		u := a.Usage
		u.Ms = 0
		var rec map[string]any
		b, _ := json.Marshal(u)
		_ = json.Unmarshal(b, &rec)
		delete(rec, "ts")
		delete(rec, "ms")
		sameJSON(t, f.Name+" usage", rec, want.Usage)
	}
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// The engine leaves to the control plane every request it does not serve as the JS gateway would, and says why.
func TestChatHandoffs(t *testing.T) {
	var base chatCase
	for _, f := range loadFixtures(t, "chat") {
		if strings.HasSuffix(f.Name, "-basic.json") {
			_ = json.Unmarshal(f.In, &base)
		}
	}
	for _, c := range []struct {
		name, why string
		edit      func(s *State, r *Request)
	}{
		{"remote client", "access:", func(s *State, r *Request) { r.Peer = "10.0.0.2:5" }},
		{"public host", "access:", func(s *State, r *Request) { r.Host = "zaaf.example" }},
		{"browser", "access:", func(s *State, r *Request) { r.Header.Set("Origin", "http://127.0.0.1:4777") }},
		{"proxy", "access:", func(s *State, r *Request) { r.Header.Set("X-Forwarded-For", "1.2.3.4") }},
		{"no key", "key: " + MsgMalformed, func(s *State, r *Request) { r.Header.Del("Authorization") }},
		{"bad json", "body:", func(s *State, r *Request) { r.Body = []byte(`{"model":`) }},
		{"array body", "body:", func(s *State, r *Request) { r.Body = []byte(`[]`) }},
		{"empty body", "body:", func(s *State, r *Request) { r.Body = nil }},
		{"no model", "model: missing", func(s *State, r *Request) { r.Body = []byte(`{"messages":[]}`) }},
		{"agent", "model: an agent", func(s *State, r *Request) { r.Body = []byte(`{"model":"agent/auto"}`) }},
		{"stream_options a string", "stream: stream_options", func(s *State, r *Request) {
			r.Body = []byte(`{"model":"oa/m1","stream":true,"stream_options":"x"}`)
		}},
		{"unknown model", "model: no usable", func(s *State, r *Request) { r.Body = []byte(`{"model":"zz/m1"}`) }},
		{"route", "model: a route", func(s *State, r *Request) {
			s.Gateway.Routes = []ipc.RouteConfig{{Name: "router/r", Targets: []string{"oa/m1"}}}
			r.Body = []byte(`{"model":"router/r"}`)
		}},
		{"anthropic format", "provider: not the OpenAI", func(s *State, r *Request) {
			f := "anthropic"
			s.Gateway.Providers[0].Format = &f
		}},
		{"oauth2", "provider: OAuth2", func(s *State, r *Request) { s.Gateway.Providers[0].Auth = &ipc.ProviderAuth{Type: "oauth2"} }},
		{"credential missing", "provider: no credential", func(s *State, r *Request) { delete(s.Secrets, "oa") }},
		{"key budget", "budget: the key", func(s *State, r *Request) {
			b := 5.0
			s.Keys.list[0].BudgetUSD = &b
		}},
		{"stale snapshot", "budget: no fresh", func(s *State, r *Request) { s.SnapshotAt = fixtureNow - StaleMs - 1 }},
		{"global budget", "budget: a blocking", func(s *State, r *Request) {
			s.Snapshot = &ipc.BudgetSnapshot{At: fixtureNow, Budgets: []ipc.BudgetView{{ID: "b", Enforce: "block",
				Scope: ipc.BudgetScope{Global: true}, State: "ok"}}}
		}},
		{"model budget", "budget: a blocking", func(s *State, r *Request) {
			s.Snapshot = &ipc.BudgetSnapshot{At: fixtureNow, Budgets: []ipc.BudgetView{{ID: "b", Enforce: "block",
				Scope: ipc.BudgetScope{Dim: "model", Value: "m1"}, State: "ok"}}}
		}},
		{"guardrails", "guardrails:", func(s *State, r *Request) { s.Gateway.Guardrails.Secrets = "redact" }},
		{"key guardrails", "guardrails:", func(s *State, r *Request) {
			m := "log"
			s.Keys.list[0].Guardrails.PII = &m
		}},
		{"tool results", "token saver:", func(s *State, r *Request) {
			s.Gateway.TokenSaver = true
			r.Body = []byte(`{"model":"oa/m1","messages":[{"role":"tool","tool_call_id":"c","content":"x"}]}`)
		}},
	} {
		s := stateFor(t, base)
		r := requestFor(base)
		c.edit(s, r)
		if p := s.PlanChat(r, fixtureNow); !strings.HasPrefix(p.Handoff, c.why) {
			t.Errorf("%s: handoff %q, want %q", c.name, p.Handoff, c.why)
		}
	}
	// The token saver off by header: served.
	s := stateFor(t, base)
	s.Gateway.TokenSaver = true
	r := requestFor(base)
	r.Body = []byte(`{"model":"oa/m1","messages":[{"role":"tool","tool_call_id":"c","content":"x"}]}`)
	r.Header.Set("X-Zp-Token-Saver", "OFF")
	if p := s.PlanChat(r, fixtureNow); p.Handoff != "" {
		t.Errorf("token saver off: %s", p.Handoff)
	}
}

func TestRateLimitedAnswer(t *testing.T) {
	a := RateLimited(&ipc.GatewayKeyAuth{RPM: 2}, 7)
	if a.Status != 429 || a.Header.Get("Retry-After") != "7" ||
		string(a.Body) != `{"error":{"message":"rate limit of 2 requests per minute reached","type":"rate_limit_error"}}` {
		t.Fatalf("%d %v %s", a.Status, a.Header, a.Body)
	}
}

type streamCase struct {
	chatCase
	Provider struct {
		Status int      `json:"status"`
		Body   string   `json:"body"`
		Throw  string   `json:"throw"`
		Chunks []string `json:"chunks"`
		B64    [][]byte `json:"b64"` // encoding/json reads base64 into []byte
		Cut    bool     `json:"cut"`
	} `json:"provider"`
}

// Every streamed case of gw/testdata/chat (the JS chatCompletion's own output): the same provider request, the same
// client bytes, the same usage line (ttft only as set or null).
func TestChatStreamFixtures(t *testing.T) {
	n := 0
	for _, f := range loadFixtures(t, "chat") {
		if f.Fn != "chatStream" {
			continue
		}
		n++
		var c streamCase
		var want chatOut
		if err := json.Unmarshal(f.In, &c); err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		if err := json.Unmarshal(f.Out, &want); err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		s := stateFor(t, c.chatCase)
		p := s.PlanChat(requestFor(c.chatCase), fixtureNow)
		if p.Handoff != "" || !p.Stream {
			t.Errorf("%s: handed off (%s) or not a stream", f.Name, p.Handoff)
			continue
		}
		if p.URL != want.Request.URL || string(p.Body) != want.Request.Body {
			t.Errorf("%s: provider request\n got %s %s\nwant %s %s", f.Name, p.URL, p.Body, want.Request.URL, want.Request.Body)
		}
		var status int
		var body string
		var rec ipc.UsageRecord
		switch {
		case c.Provider.Throw != "":
			a := s.Failed(&p, c.Provider.Throw, 1)
			status, body, rec = a.Status, string(a.Body), a.Usage
		case c.Provider.Status < 200 || c.Provider.Status > 299:
			a := s.Respond(&p, c.Provider.Status, []byte(c.Provider.Body), 1)
			status, body, rec = a.Status, string(a.Body), a.Usage
		default:
			pieces := c.Provider.B64
			if pieces == nil {
				for _, ch := range c.Provider.Chunks {
					pieces = append(pieces, []byte(ch))
				}
			}
			cs := NewChatStream(p.Model, p.WantsUsage, func() float64 { return 1 })
			var out []byte
			for _, pc := range pieces {
				w, err := cs.Feed(pc)
				if err != nil {
					t.Fatalf("%s: %v", f.Name, err)
				}
				out = append(out, w...)
			}
			if !c.Provider.Cut {
				out = append(out, cs.End()...)
			}
			status, body, rec = 200, string(out), s.StreamLine(&p, cs, 1)
		}
		if status != want.Client.Status || body != want.Client.Body {
			t.Errorf("%s: client\n got %d %q\nwant %d %q", f.Name, status, body, want.Client.Status, want.Client.Body)
		}
		var got map[string]any
		b, _ := json.Marshal(rec)
		_ = json.Unmarshal(b, &got)
		delete(got, "ts")
		delete(got, "ms")
		switch v, ok := got["ttft"]; {
		case !ok:
			got["ttft"] = "absent"
		case v != nil:
			got["ttft"] = "set"
		}
		sameJSON(t, f.Name+" usage", got, want.Usage)
	}
	if n < 15 {
		t.Fatalf("only %d stream fixtures", n)
	}
}
