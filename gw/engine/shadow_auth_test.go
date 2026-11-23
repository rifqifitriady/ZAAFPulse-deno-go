package engine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

// authFixtureNamed is the auth fixture name (gw/testdata/auth, the JS gateway's own exchanges).
func authFixtureNamed(t *testing.T, name string) authFixture {
	t.Helper()
	for _, f := range loadAuth(t) {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("no %s", name)
	return authFixture{}
}

// authCapture is the control plane's capture of a fixture's first request, built from what the JS sent and was
// answered: its provider requests (the token endpoint left out, as providers.js notes only provider requests), every
// provider answer, the last one, the client's answer and its usage line (cost in USD, as the JS writes it).
func authCapture(t *testing.T, f authFixture) *Capture {
	t.Helper()
	c := &Capture{}
	for i, x := range f.Out.Exchanges {
		if strings.HasSuffix(x.URL, "/token") {
			continue
		}
		c.ProviderRequests = append(c.ProviderRequests, struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
			Body    *string           `json:"body"`
		}{x.URL, x.Headers, x.Body})
		q := f.In.Queue[i]
		c.Upstream = append(c.Upstream, ProviderAnswer{Status: q.Status, ContentType: q.Headers["content-type"],
			Body: q.Body, Ended: "eof"})
	}
	last := c.Upstream[len(c.Upstream)-1]
	c.ProviderResponse = &last
	cl := f.Out.Clients[0]
	c.Client = &CaptureClient{Status: cl.Status, Body: cl.Body, Headers: map[string]string{}}
	for k, v := range cl.Headers {
		if v != nil {
			c.Client.Headers[k] = *v
		}
	}
	if err := json.Unmarshal(f.Out.Usage[0], &c.Usage); err != nil {
		t.Fatal(err)
	}
	if n, ok := c.Usage["cost"].(float64); ok {
		c.Usage["cost"] = n / 1e9
	}
	return c
}

// authPlan plans a fixture's first request on a state with a token registry whose transport fails the test: a plan
// never fetches a token.
func authPlan(t *testing.T, f authFixture) (*State, Plan) {
	t.Helper()
	s := authState(t, f)
	s.tokens = NewTokens(&http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		t.Errorf("a token request while planning: %s", r.URL)
		return answer(500, "{}"), nil
	})}, func() *State { return s }, fixedClock)
	rq := f.In.Requests[0]
	h := http.Header{}
	for k, v := range rq.Headers {
		h.Set(k, v)
	}
	r := &Request{Method: rq.Method, RawPath: rq.Path, Header: h, Host: "127.0.0.1:4777", Peer: "127.0.0.1:50000",
		Body: []byte(rq.Body)}
	p := s.PlanFor(classOfPath(rq.Path), r, fixtureNow)
	if p.Handoff != "" || !p.TokenAuth() {
		t.Fatalf("%s: plan %q, token auth %v", f.Name, p.Handoff, p.TokenAuth())
	}
	return s, p
}

// The provider-auth switch on the serving path (spec Decision 14): a token-auth chat call is handed to js while
// provider-auth is on js and chat on go, and served when both are on go.
func TestServeProviderAuthSwitch(t *testing.T) {
	f := authFixtureNamed(t, "az-chat-whole.json")
	for _, c := range []struct {
		name string
		auth bool
	}{{"provider-auth on js", false}, {"provider-auth on go", true}} {
		t.Run(c.name, func(t *testing.T) {
			want := f.Out.Exchanges
			if !c.auth {
				want = nil
			}
			rt := &replay{t: t, want: want, queue: f.In.Queue}
			var mu sync.Mutex
			var lines []ipc.UsageRecord
			e := authEngine(t, rt, authState(t, f), &lines, &mu)
			e.SetModes(func(sub string) (bool, bool) { return sub == "chat" || (sub == AuthSwitch && c.auth), false })
			toJS := 0
			js := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { toJS++; w.WriteHeader(299) })
			rq := f.In.Requests[0]
			req := httptest.NewRequest(rq.Method, "http://127.0.0.1:4777"+rq.Path, strings.NewReader(rq.Body))
			req.Host, req.RemoteAddr = "127.0.0.1:4777", "127.0.0.1:50000"
			for k, v := range rq.Headers {
				req.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			e.Serve("chat", w, req, rq.Path, js)
			if !c.auth {
				if w.Code != 299 || toJS != 1 || rt.count() != 0 {
					t.Fatalf("status %d, to js %d, exchanges %d", w.Code, toJS, rt.count())
				}
				return
			}
			if w.Code != f.Out.Clients[0].Status || w.Body.String() != f.Out.Clients[0].Body || toJS != 0 ||
				rt.count() != len(f.Out.Exchanges) {
				t.Fatalf("status %d, to js %d, exchanges %d", w.Code, toJS, rt.count())
			}
		})
	}
}

// A key provider's call does not read provider-auth: chat on go serves it while provider-auth is on js.
func TestServeKeyProviderIgnoresProviderAuth(t *testing.T) {
	rig, sent := guardRig(t, func(*State) {})
	rig.e.SetModes(func(sub string) (bool, bool) { return sub == "chat", false })
	resp := rig.post(t.Context(), t, `{"model":"oa/m1","messages":[{"role":"user","content":"hi"}]}`)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || len(sent()) != 1 {
		t.Fatalf("status %d, provider calls %d", resp.StatusCode, len(sent()))
	}
}

// The shadow of a token-auth call (spec Decisions 14, 15): with provider-auth shadowed, a chat call whose sub-class is
// on js (Shadow) or on go (Serve) is forwarded to js and compared against the JS capture of az-chat-whole; the line is
// "same" and carries auth: true, and the engine makes no token or provider request.
func TestShadowProviderAuth(t *testing.T) {
	f := authFixtureNamed(t, "az-chat-whole.json")
	capture, _ := json.Marshal(authCapture(t, f))
	for _, c := range []struct {
		name string
		chat bool // chat on go
	}{{"chat on js", false}, {"chat on go", true}} {
		t.Run(c.name, func(t *testing.T) {
			rt := &replay{t: t}
			var mu sync.Mutex
			var lines []ipc.UsageRecord
			e := authEngine(t, rt, authState(t, f), &lines, &mu)
			e.SetModes(func(sub string) (bool, bool) { return sub == "chat" && c.chat, sub == AuthSwitch })
			posted := make(chan string, 2)
			e.SetOwn(func(method, path string, body []byte) (int, []byte, error) {
				if method == http.MethodPost {
					posted <- string(body)
					return http.StatusNoContent, nil, nil
				}
				return http.StatusOK, capture, nil
			})
			js := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(299) })
			rq := f.In.Requests[0]
			req := httptest.NewRequest(rq.Method, "http://127.0.0.1:4777"+rq.Path, strings.NewReader(rq.Body))
			req.Host, req.RemoteAddr = "127.0.0.1:4777", "127.0.0.1:50000"
			for k, v := range rq.Headers {
				req.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			if c.chat {
				e.Serve("chat", w, req, rq.Path, js)
			} else {
				e.Shadow("chat", w, req, rq.Path, js, e.ownFn())
			}
			if w.Code != 299 {
				t.Fatalf("status %d", w.Code)
			}
			select {
			case l := <-posted:
				var got ShadowLine
				_ = json.Unmarshal([]byte(l), &got)
				if got.Class != "chat" || got.Outcome != "same" || !got.Auth {
					t.Fatalf("line %s", l)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("no shadow line")
			}
			if rt.count() != 0 {
				t.Fatalf("%d exchanges", rt.count())
			}
		})
	}
}

// A shadowed provider-auth alone leaves a key provider's chat call (chat on js) straight to js: no compare, no line.
func TestShadowProviderAuthSkipsKeyProviders(t *testing.T) {
	rig, _ := guardRig(t, func(*State) {})
	rig.e.SetModes(func(sub string) (bool, bool) { return false, sub == AuthSwitch })
	posted := 0
	rig.e.SetOwn(func(string, string, []byte) (int, []byte, error) { posted++; return http.StatusNoContent, nil, nil })
	js := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(299) })
	req := httptest.NewRequest("POST", "http://127.0.0.1:4777/v1/chat/completions",
		strings.NewReader(`{"model":"oa/m1","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", rig.key)
	w := httptest.NewRecorder()
	rig.e.Shadow("chat", w, req, "/v1/chat/completions", js, rig.e.ownFn())
	if w.Code != 299 || posted != 0 {
		t.Fatalf("status %d, posted %d", w.Code, posted)
	}
}

// The credential compare of a token-auth plan (spec Decision 15): by name and scheme, never by value; a missing header
// or another scheme is a diff. A key plan still compares the value.
func TestProviderHeadersByScheme(t *testing.T) {
	for _, c := range []struct {
		fixture, name, value string
	}{{"az-chat-whole.json", "authorization", "Bearer az-tok-1"}, {"ax-chat.json", "x-api-key", "ax-tok-1"}} {
		f := authFixtureNamed(t, c.fixture)
		_, p := authPlan(t, f)
		js := map[string]string{}
		for k, v := range authCapture(t, f).ProviderRequests[0].Headers {
			js[k] = v
		}
		if js[c.name] != c.value {
			t.Fatalf("%s: the JS sent %q", c.fixture, js[c.name])
		}
		same := func(h map[string]string) bool { a, b := providerHeaders(&p, h); return a == b }
		if !same(js) {
			a, b := providerHeaders(&p, js)
			t.Fatalf("%s: the JS headers differ\n%s\n%s", c.fixture, a, b)
		}
		other := map[string]string{}
		for k, v := range js {
			other[k] = v
		}
		other[c.name] = strings.Replace(c.value, "-tok-1", "-tok-7", 1) // another token: the same
		if !same(other) {
			t.Fatalf("%s: another token value differs", c.fixture)
		}
		missing := map[string]string{}
		for k, v := range js {
			if k != c.name {
				missing[k] = v
			}
		}
		if same(missing) {
			t.Fatalf("%s: a missing credential is the same", c.fixture)
		}
		if c.name == "authorization" {
			other[c.name] = "Basic az-tok-1"
			if same(other) {
				t.Fatalf("%s: another scheme is the same", c.fixture)
			}
			other[c.name] = "az-tok-1"
			if same(other) {
				t.Fatalf("%s: no scheme is the same", c.fixture)
			}
		} else {
			other["authorization"] = "Bearer ax-tok-1" // the Anthropic format sends no Bearer
			delete(other, "x-api-key")
			if same(other) {
				t.Fatalf("%s: a Bearer for an x-api-key provider is the same", c.fixture)
			}
		}
	}
}

// A JS capture in which the provider answered 401 and the JS replayed with a refreshed token (two provider requests)
// is skipped (spec Decision 15), for chat and for a translated class; the line still carries auth: true.
func TestShadowTokenReplaySkipped(t *testing.T) {
	f := authFixtureNamed(t, "az-replay-ok.json")
	s, p := authPlan(t, f)
	c := authCapture(t, f)
	if len(c.ProviderRequests) != 2 || c.Upstream[0].Status != 401 {
		t.Fatalf("capture: %d requests", len(c.ProviderRequests))
	}
	l := s.compareCaptured("chat", &p, c)
	if l.Outcome != "skipped" || l.Reason != "a token refresh replay: each engine holds its own token" || !l.Auth {
		t.Fatalf("%+v", l)
	}
	if l := s.CompareTranslated("chat-anthropic", &p, c); l.Outcome != "skipped" || l.Reason != authReplay {
		t.Fatalf("translated: %+v", l)
	}
	// The same capture's first call answered 401 with one provider request (no replay): not this skip.
	one := *c
	one.ProviderRequests = one.ProviderRequests[:1]
	one.Upstream = one.Upstream[:1]
	if l := s.Compare("chat", &p, &one); l.Reason == authReplay {
		t.Fatalf("one request: %+v", l)
	}
	// A key plan with the same capture is not skipped as a replay.
	q := p
	q.auth = nil
	if l := s.Compare("chat", &q, c); l.Reason == authReplay {
		t.Fatalf("key plan: %+v", l)
	}
}

// The line of a key provider's call has no auth mark.
func TestShadowLineAuthOnlyForTokenPlans(t *testing.T) {
	f := authFixtureNamed(t, "az-chat-whole.json")
	s, p := authPlan(t, f)
	c := authCapture(t, f)
	if l := s.compareCaptured("chat", &p, c); !l.Auth || l.Outcome != "same" {
		t.Fatalf("%+v", l)
	}
	q := p
	q.auth = nil
	if l := s.compareCaptured("chat", &q, c); l.Auth {
		t.Fatalf("%+v", l)
	}
	b, _ := json.Marshal(ShadowLine{Class: "chat", Outcome: "same"})
	if strings.Contains(string(b), "auth") {
		t.Fatalf("%s", b)
	}
}

// tokenFailCapture is the control plane's capture of a call whose own token fetch failed (az-token-fetch-failed: no
// provider request, 502 "az: fetch failed").
func tokenFailCapture(t *testing.T) *Capture {
	t.Helper()
	f := authFixtureNamed(t, "az-token-fetch-failed.json")
	cl := f.Out.Clients[0]
	if len(f.Out.Exchanges) != 1 || cl.Status < 500 {
		t.Fatalf("fixture: %d exchanges, status %d", len(f.Out.Exchanges), cl.Status)
	}
	return &Capture{Client: &CaptureClient{Status: cl.Status, Body: cl.Body}}
}

// The control plane's token fetch failed before any provider request (P4g final review I3 case a): not comparable,
// for chat and for a translated class; the line still carries auth: true.
func TestShadowOwnTokenFetchFailedSkipped(t *testing.T) {
	s, p := authPlan(t, authFixtureNamed(t, "az-chat-whole.json"))
	c := tokenFailCapture(t)
	want := "the control plane's token fetch failed: each engine holds its own token"
	if l := s.compareCaptured("chat", &p, c); l.Outcome != "skipped" || l.Reason != want || !l.Auth {
		t.Fatalf("%+v", l)
	}
	if l := s.CompareTranslated("chat-anthropic", &p, c); l.Outcome != "skipped" || l.Reason != want {
		t.Fatalf("translated: %+v", l)
	}
}

// The first provider answer was 401 and the control plane made no replay (its refresh failed): case b, skipped.
func TestShadowOwnRefreshAfter401Skipped(t *testing.T) {
	f := authFixtureNamed(t, "az-replay-ok.json")
	s, p := authPlan(t, f)
	c := authCapture(t, f)
	one := *c
	one.ProviderRequests = one.ProviderRequests[:1]
	one.Upstream = one.Upstream[:1]
	first := one.Upstream[0]
	one.ProviderResponse = &first
	one.Client = &CaptureClient{Status: 502, Body: `{"error":{"message":"az: az: token refresh failed (401 invalid_client)","type":"upstream_error"}}`}
	want := "the control plane's token fetch failed: each engine holds its own token"
	if l := s.Compare("chat", &p, &one); l.Outcome != "skipped" || l.Reason != want {
		t.Fatalf("%+v", l)
	}
	if l := s.CompareTranslated("chat-anthropic", &p, &one); l.Outcome != "skipped" || l.Reason != want {
		t.Fatalf("translated: %+v", l)
	}
}

// A key plan with the same captures still diffs: the skip is for token-auth plans only.
func TestShadowKeyPlanTokenFailureStillDiffs(t *testing.T) {
	s, p := authPlan(t, authFixtureNamed(t, "az-chat-whole.json"))
	q := p
	q.auth = nil
	if l := s.Compare("chat", &q, tokenFailCapture(t)); l.Outcome != "diff" {
		t.Fatalf("%+v", l)
	}
	f := authFixtureNamed(t, "az-replay-ok.json")
	c := authCapture(t, f)
	one := *c
	one.ProviderRequests = one.ProviderRequests[:1]
	one.Upstream = one.Upstream[:1]
	first := one.Upstream[0]
	one.ProviderResponse = &first
	one.Client = &CaptureClient{Status: 502, Body: `{}`}
	if l := s.Compare("chat", &q, &one); l.Outcome != "diff" {
		t.Fatalf("one 401: %+v", l)
	}
}

// A Bearer with an empty token is not normalised to a Bearer with a token (P4g final review m18): the JS sending
// "Bearer " is a diff against the engine's Bearer <token>.
func TestSchemeOnlyEmptyBearer(t *testing.T) {
	if got := schemeOnly(map[string]string{"authorization": "Bearer "})["authorization"]; got != "Bearer " {
		t.Fatalf("got %q", got)
	}
	if got := schemeOnly(map[string]string{"Authorization": "Bearer abc"})["Authorization"]; got != "Bearer "+tokenMark {
		t.Fatalf("got %q", got)
	}
	f := authFixtureNamed(t, "az-chat-whole.json")
	_, p := authPlan(t, f)
	js := map[string]string{}
	for k, v := range authCapture(t, f).ProviderRequests[0].Headers {
		js[k] = v
	}
	js["authorization"] = "Bearer "
	if a, b := providerHeaders(&p, js); a == b {
		t.Fatal("an empty Bearer token compares the same")
	}
}
