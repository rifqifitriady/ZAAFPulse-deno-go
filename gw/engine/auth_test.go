package engine

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"
)

// authExchange is one outbound request the JS made (a token endpoint or a provider), in order.
type authExchange struct {
	URL     string            `json:"url"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers"`
	Body    *string           `json:"body"`
}

// authAnswer is the recorded answer to exchange i.
type authAnswer struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
	Stream  bool              `json:"stream"`
	Throws  string            `json:"throws"` // fetch rejected (a refused connection): the transport fails
}

type authFixture struct {
	Name string
	Fn   string `json:"fn"`
	In   struct {
		Note        string              `json:"note"`
		ConfigPush  ipc.ConfigPush      `json:"configPush"`
		SecretsPush ipc.SecretsHandoff  `json:"secretsPush"`
		Snapshot    *ipc.BudgetSnapshot `json:"snapshot"`
		Requests    []struct {
			Method  string            `json:"method"`
			Path    string            `json:"path"`
			Headers map[string]string `json:"headers"`
			Body    string            `json:"body"`
		} `json:"requests"`
		Queue []authAnswer `json:"queue"`
	} `json:"in"`
	Out struct {
		Exchanges []authExchange `json:"exchanges"`
		Clients   []struct {
			Status  int                `json:"status"`
			Headers map[string]*string `json:"headers"`
			Body    string             `json:"body"`
		} `json:"clients"`
		Usage []json.RawMessage `json:"usage"`
	} `json:"out"`
}

// loadAuth reads gw/testdata/auth (scripts/fixtures/gen-auth.js), sorted by name. Their `in` is an object, so
// jsv.LoadFixture cannot read them: the secrets are put back the same way (JSON-escaped into the text), then the text is
// read into authFixture.
func loadAuth(t *testing.T) []authFixture {
	t.Helper()
	dir := filepath.Join("..", "testdata", "auth")
	files, err := os.ReadDir(dir)
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures in %s (run node scripts/fixtures/gen-auth.js): %v", dir, err)
	}
	var out []authFixture
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(dir, f.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var head struct {
			Secrets []string `json:"secrets"`
		}
		if err := json.Unmarshal(data, &head); err != nil {
			t.Fatalf("%s: %v", f.Name(), err)
		}
		text := string(data)
		for i, s := range head.Secrets {
			raw, err := base64.StdEncoding.DecodeString(s)
			if err != nil {
				t.Fatalf("%s: secret %d: %v", f.Name(), i, err)
			}
			var b strings.Builder
			jsv.Quote(&b, string(raw))
			q := b.String()
			text = strings.ReplaceAll(text, "<<ZPSECRET:"+strconv.Itoa(i)+">>", q[1:len(q)-1])
		}
		x := authFixture{Name: f.Name()}
		if err := json.Unmarshal([]byte(text), &x); err != nil {
			t.Fatalf("%s: %v", f.Name(), err)
		}
		out = append(out, x)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// replay is the transport the provider client and the token sources share: request i must be exchanges[i] (URL,
// method, headers, body) and gets queue[i]. Headers are compared whole, lower-cased: a RoundTripper sees only the
// headers the engine set (net/http's transport adds User-Agent, Accept-Encoding and Content-Length when it writes the
// request, as fetch adds its own; neither side records those), so nothing is ignored.
type replay struct {
	t     *testing.T
	want  []authExchange
	queue []authAnswer

	mu sync.Mutex
	n  int
}

func (r *replay) RoundTrip(req *http.Request) (*http.Response, error) {
	var body *string
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		_ = req.Body.Close()
		s := string(b)
		body = &s
	}
	r.mu.Lock()
	i := r.n
	r.n++
	r.mu.Unlock()
	if i >= len(r.want) || i >= len(r.queue) {
		r.t.Errorf("exchange %d: %s %s, the JS made only %d", i, req.Method, req.URL, len(r.want))
		return nil, errors.New("no recorded exchange")
	}
	w := r.want[i]
	if req.URL.String() != w.URL || req.Method != w.Method {
		r.t.Errorf("exchange %d: %s %s, want %s %s", i, req.Method, req.URL, w.Method, w.URL)
	}
	got := map[string]string{}
	for k := range req.Header {
		got[strings.ToLower(k)] = strings.Join(req.Header.Values(k), ", ")
	}
	sameJSON(r.t, fmt.Sprintf("exchange %d headers", i), got, mustJSON(w.Headers))
	if (body == nil) != (w.Body == nil) || (body != nil && *body != *w.Body) {
		r.t.Errorf("exchange %d body\n got %v\nwant %v", i, ptrText(body), ptrText(w.Body))
	}
	a := r.queue[i]
	if a.Throws != "" {
		return nil, errors.New("dial tcp 127.0.0.1:1: connect: connection refused")
	}
	h := http.Header{}
	for k, v := range a.Headers {
		h.Set(k, v)
	}
	return &http.Response{StatusCode: a.Status, Status: strconv.Itoa(a.Status) + " " + http.StatusText(a.Status),
		Header: h, Body: io.NopCloser(strings.NewReader(a.Body)), Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Request: req}, nil
}

func (r *replay) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

func fixedClock() time.Time { return time.UnixMilli(fixtureNow) }

// authEngine is a fresh engine (and token registry) over rt with the fixed clock, every sub-class and switch on go.
func authEngine(t *testing.T, rt http.RoundTripper, s *State, lines *[]ipc.UsageRecord, mu *sync.Mutex) *Engine {
	t.Helper()
	up := &Upstream{client: &http.Client{Transport: rt, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}, timeout: Timeout}
	e := New(up, func(r *ipc.UsageReport) (int64, error) {
		mu.Lock()
		defer mu.Unlock()
		*lines = append(*lines, r.Record)
		return 0, nil
	}, t.Logf)
	e.now = func() int64 { return fixtureNow }
	e.tokens = NewTokens(up.Client(), e.State, fixedClock)
	e.SetModes(func(string) (bool, bool) { return true, false })
	e.SetState(s)
	return e
}

func authState(t *testing.T, f authFixture) *State {
	t.Helper()
	c, sp := f.In.ConfigPush, f.In.SecretsPush
	c.Gen, sp.Gen = 1, 1
	s := NewState(&c, &sp)
	if f.In.Snapshot != nil {
		s = s.WithSnapshot(f.In.Snapshot)
	}
	return s
}

// Every case of gw/testdata/auth (the JS gateway's own exchanges): served through Engine.Serve one request after
// another on a fresh engine, the engine makes the same token and provider requests in the same order, and gives each
// client the same status, headers and bytes and the same usage line.
func TestAuthFixtures(t *testing.T) {
	fs := loadAuth(t)
	if len(fs) < 16 {
		t.Fatalf("%d auth fixtures", len(fs))
	}
	was := *translate.Default
	defer func() { *translate.Default = was }()
	for _, f := range fs {
		t.Run(f.Name, func(t *testing.T) {
			*translate.Default = *counterEnv() // the generator's per-case random counter and fixed clock
			rt := &replay{t: t, want: f.Out.Exchanges, queue: f.In.Queue}
			var mu sync.Mutex
			var lines []ipc.UsageRecord
			e := authEngine(t, rt, authState(t, f), &lines, &mu)
			js := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Errorf("handed to the control plane: %s", r.URL.Path)
				w.WriteHeader(599)
			})
			if len(f.Out.Clients) != len(f.In.Requests) || len(f.Out.Usage) != len(f.In.Requests) {
				t.Fatalf("%d requests, %d clients, %d usage", len(f.In.Requests), len(f.Out.Clients), len(f.Out.Usage))
			}
			for j, rq := range f.In.Requests {
				req := httptest.NewRequest(rq.Method, "http://127.0.0.1:4777"+rq.Path, strings.NewReader(rq.Body))
				req.Host, req.RemoteAddr = "127.0.0.1:4777", "127.0.0.1:50000"
				for k, v := range rq.Headers {
					req.Header.Set(k, v)
				}
				raw, _, _ := strings.Cut(rq.Path, "?")
				mu.Lock()
				before := len(lines)
				mu.Unlock()
				w := httptest.NewRecorder()
				e.Serve(classOfPath(raw), w, req, raw, js)
				e.streams.Wait() // a stream's line is handed to emit after its last byte
				want := f.Out.Clients[j]
				if w.Code != want.Status || w.Body.String() != want.Body {
					t.Errorf("client %d\n got %d %s\nwant %d %s", j, w.Code, w.Body.String(), want.Status, want.Body)
				}
				for k, v := range want.Headers {
					g := w.Header().Get(k)
					if (v == nil) != (g == "") || (v != nil && *v != g) {
						t.Errorf("client %d header %s: got %q, want %v", j, k, g, ptrText(v))
					}
				}
				mu.Lock()
				got := append([]ipc.UsageRecord(nil), lines[before:]...)
				mu.Unlock()
				var line any
				switch len(got) {
				case 0:
				case 1:
					line = usageOfRecord(&got[0])
				default:
					t.Fatalf("request %d: %d usage lines", j, len(got))
				}
				sameJSON(t, fmt.Sprintf("usage %d", j), line, f.Out.Usage[j])
			}
			if n := rt.count(); n != len(f.Out.Exchanges) {
				t.Errorf("%d exchanges, the JS made %d", n, len(f.Out.Exchanges))
			}
		})
	}
}

// rtFunc is a RoundTripper from a function.
type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func answer(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(body))}
}

// authUnitFixture is az-chat-whole: the auth fixtures' state (providers az, rt, vx, ax) and a client request.
func authUnitFixture(t *testing.T) authFixture {
	t.Helper()
	for _, f := range loadAuth(t) {
		if f.Name == "az-chat-whole.json" {
			return f
		}
	}
	t.Fatal("no az-chat-whole.json")
	return authFixture{}
}

func authUnitState(t *testing.T) *State { t.Helper(); return authState(t, authUnitFixture(t)) }

func providerOfState(s *State, id string) *ipc.ProviderConfig {
	for i := range s.Gateway.Providers {
		if s.Gateway.Providers[i].ID == id {
			return &s.Gateway.Providers[i]
		}
	}
	return nil
}

// withProvider is s with provider id changed by edit (a deep copy of the provider list and the secrets).
func withProvider(s *State, id string, edit func(p *ipc.ProviderConfig)) *State {
	c := *s
	b, _ := json.Marshal(s.Gateway.Providers)
	c.Gateway.Providers = nil
	_ = json.Unmarshal(b, &c.Gateway.Providers)
	c.Secrets = map[string]string{}
	for k, v := range s.Secrets {
		c.Secrets[k] = v
	}
	if edit != nil {
		edit(providerOfState(&c, id))
	}
	return &c
}

// A source per provider id and auth settings: the same settings give the same source, a changed token URL a new one;
// a key provider has none.
func TestTokensSourcePerAuthSettings(t *testing.T) {
	s := authUnitState(t)
	tk := NewTokens(nil, func() *State { return s }, fixedClock)
	a := tk.Source(providerOfState(s, "az"))
	if a == nil || tk.Source(providerOfState(withProvider(s, "az", nil), "az")) != a {
		t.Fatal("the same settings should give the same source")
	}
	if tk.Source(providerOfState(s, "rt")) == a {
		t.Fatal("another provider shares the source")
	}
	moved := withProvider(s, "az", func(p *ipc.ProviderConfig) {
		u := "https://login.example.test/other/token"
		p.Auth.TokenURL = &u
	})
	b := tk.Source(providerOfState(moved, "az"))
	if b == nil || b == a {
		t.Fatal("a changed token URL should give a new source")
	}
	if tk.Source(&ipc.ProviderConfig{ID: "k", Auth: &ipc.ProviderAuth{Type: "key"}}) != nil ||
		tk.Source(&ipc.ProviderConfig{ID: "n"}) != nil {
		t.Fatal("a key provider has no source")
	}
	var nilTokens *Tokens
	if nilTokens.Source(providerOfState(s, "az")) != nil {
		t.Fatal("a nil registry has no source")
	}
}

// The secret and the mask set are read from the newest state at each refresh.
func TestTokensReadTheNewestState(t *testing.T) {
	s := authUnitState(t)
	var mu sync.Mutex
	cur := s
	var forms []string
	rt := rtFunc(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		forms = append(forms, string(b))
		n := len(forms)
		mu.Unlock()
		if n == 3 {
			return answer(401, `{"error":"bad newer-secret-0123"}`), nil
		}
		return answer(200, `{"access_token":"tok-`+strconv.Itoa(n)+`","expires_in":3600}`), nil
	})
	tk := NewTokens(&http.Client{Transport: rt}, func() *State { mu.Lock(); defer mu.Unlock(); return cur }, fixedClock)
	src := tk.Source(providerOfState(s, "az"))
	if _, _, err := src.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	newer := withProvider(s, "az", nil)
	newer.Secrets["az"] = "newer-secret-0123"
	newer.Mask = append(append([]string(nil), s.Mask...), "newer-secret-0123")
	mu.Lock()
	cur = newer
	mu.Unlock()
	if _, _, err := src.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(forms[0], "client_secret="+s.Secrets["az"]) ||
		!strings.Contains(forms[1], "client_secret=newer-secret-0123") {
		t.Fatalf("forms %q", forms)
	}
	_, _, err := src.Refresh(context.Background())
	if err == nil || err.Error() != "az: token refresh failed (401 bad ***)" {
		t.Fatalf("masked with the newest state's set: %v", err)
	}
}

// Tick force-refreshes a cached token that expires within 10 minutes, only when its provider is usable; one with more
// time left, or none cached, is left alone.
func TestTokensTick(t *testing.T) {
	s := authUnitState(t)
	var mu sync.Mutex
	cur := s
	calls := map[string]int{}
	rt := rtFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		calls[r.URL.Path]++
		n := calls[r.URL.Path]
		mu.Unlock()
		return answer(200, fmt.Sprintf(`{"access_token":"t%d","expires_in":3600}`, n)), nil
	})
	tk := NewTokens(&http.Client{Transport: rt}, func() *State { mu.Lock(); defer mu.Unlock(); return cur }, fixedClock)
	az := tk.Source(providerOfState(s, "az"))
	_ = tk.Source(providerOfState(s, "ax")) // no token cached: never refreshed by Tick
	if _, _, err := az.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	azPath := "/t/oauth2/v2.0/token"
	now := fixedClock()
	tk.Tick(now.Add(49 * time.Minute)) // 11 minutes left
	if calls[azPath] != 1 {
		t.Fatalf("refreshed with 11 minutes left: %v", calls)
	}
	mu.Lock()
	cur = withProvider(s, "az", func(p *ipc.ProviderConfig) { f := false; p.Approved = &f })
	mu.Unlock()
	tk.Tick(now.Add(51 * time.Minute))
	if calls[azPath] != 1 {
		t.Fatalf("refreshed a provider that is not usable: %v", calls)
	}
	mu.Lock()
	cur = s
	mu.Unlock()
	tk.Tick(now.Add(51 * time.Minute)) // 9 minutes left, usable
	if calls[azPath] != 2 || len(calls) != 1 {
		t.Fatalf("not refreshed with 9 minutes left: %v", calls)
	}
	if tok, _, _ := az.Token(context.Background()); tok != "t2" {
		t.Fatalf("the refreshed token is not used: %q", tok)
	}
	// A provider that is gone drops its source.
	mu.Lock()
	cur = withProvider(s, "az", nil)
	cur.Gateway.Providers = cur.Gateway.Providers[1:]
	mu.Unlock()
	tk.Tick(now)
	if tk.Source(providerOfState(s, "az")) == az {
		t.Fatal("the source of a provider that is gone was kept")
	}
}

// tokenPlan is a planned chat call to az through an engine whose registry calls rt.
func tokenPlan(t *testing.T, rt http.RoundTripper) (*Upstream, Plan) {
	t.Helper()
	f := authUnitFixture(t)
	s := authState(t, f)
	up := &Upstream{client: &http.Client{Transport: rt}, timeout: Timeout}
	s.tokens = NewTokens(up.Client(), func() *State { return s }, fixedClock)
	h := http.Header{}
	for k, v := range f.In.Requests[0].Headers {
		h.Set(k, v)
	}
	r := &Request{Method: "POST", RawPath: "/v1/chat/completions", Header: h, Host: "127.0.0.1:4777",
		Peer: "127.0.0.1:50000", Body: []byte(f.In.Requests[0].Body)}
	p := s.PlanChat(r, fixtureNow)
	if p.Handoff != "" || !p.TokenAuth() {
		t.Fatalf("plan: %q, token auth %v", p.Handoff, p.TokenAuth())
	}
	if p.Header.Get("Authorization") != "" || p.Header.Get("X-Api-Key") != "" {
		t.Fatalf("the plan holds a credential: %v", p.Header)
	}
	if p.Body == nil {
		p.Body = []byte(`{}`) // built by Save or Guard when serving; the body does not matter here
	}
	return up, p
}

// A provider that answers 401 again after the replay: the call is replayed once only, and the second 401 is the
// answer.
func TestTokenReplayOnce(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	rt := rtFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/token") {
			seen = append(seen, "token")
			return answer(200, fmt.Sprintf(`{"access_token":"tok-%d","expires_in":3600}`, len(seen))), nil
		}
		seen = append(seen, "provider "+r.Header.Get("Authorization"))
		return answer(401, `{"error":"no"}`), nil
	})
	up, p := tokenPlan(t, rt)
	res := up.Call(context.Background(), &p)
	if res.Why != "" || res.Status != 401 {
		t.Fatalf("%+v", res)
	}
	want := []string{"token", "provider Bearer tok-1", "token", "provider Bearer tok-3"}
	if strings.Join(seen, "|") != strings.Join(want, "|") {
		t.Fatalf("exchanges %q", seen)
	}
	if p.Header.Get("Authorization") != "" {
		t.Fatal("the token was set on the plan's headers")
	}
}

// A token error fails the call with the source's message (the line and the client then read "az: az: …").
func TestTokenErrorIsTheSourceMessage(t *testing.T) {
	providerCalls := 0
	rt := rtFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			return answer(401, `{"error":"invalid_client"}`), nil
		}
		providerCalls++
		return answer(200, `{}`), nil
	})
	up, p := tokenPlan(t, rt)
	res := up.Call(context.Background(), &p)
	if res.Why != "az: token refresh failed (401 invalid_client)" || providerCalls != 0 {
		t.Fatalf("%+v, %d provider calls", res, providerCalls)
	}
}

// A state that no engine installed has no registry: a token-auth target is still handed off.
func TestCredentialWithoutRegistry(t *testing.T) {
	s := authUnitState(t)
	if _, src, why := s.credential(&Target{Provider: providerOfState(s, "az")}); src != nil ||
		why != "provider: OAuth2 or a service account (P4g)" {
		t.Fatalf("%v %q", src, why)
	}
	s.tokens = NewTokens(nil, func() *State { return s }, fixedClock)
	delete(s.Secrets, "az")
	if _, src, why := s.credential(&Target{Provider: providerOfState(s, "az")}); src != nil ||
		why != "provider: no credential" {
		t.Fatalf("%v %q", src, why)
	}
}
