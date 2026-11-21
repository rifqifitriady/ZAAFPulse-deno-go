package engine

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

// fixNum is the generator's enc of a number: NaN and ±Infinity as their String(), -0 as 0.
func fixNum(f float64) any {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	case f == 0:
		return 0.0
	}
	return f
}

func fixReading(q *quotaReading) any {
	if q == nil {
		return nil
	}
	kind := func(k *quotaKind) any {
		if k == nil {
			return nil
		}
		m := map[string]any{"limit": nil, "remaining": nil, "resetAt": nil}
		if k.HasLimit {
			m["limit"] = fixNum(k.Limit)
		}
		if k.HasRemaining {
			m["remaining"] = fixNum(k.Remaining)
		}
		if k.HasReset {
			m["resetAt"] = fixNum(k.ResetAt)
		}
		return m
	}
	return map[string]any{"requests": kind(q.Requests), "tokens": kind(q.Tokens)}
}

// headerOf builds the provider answer's headers from the fixture's (lower-case names, as Headers.get read them).
func headerOf(m map[string]string) http.Header {
	h := http.Header{}
	for k, v := range m {
		h.Set(k, v)
	}
	return h
}

// liveCooldowns is quota()'s cooldown per provider: the entry while until > now, else null.
func liveCooldowns(r *RouteState, providers []ipc.ProviderConfig, now int64) map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]any{}
	for _, p := range providers {
		c, ok := r.cool[p.ID]
		if ok && c.Until > float64(now) {
			out[p.ID] = map[string]any{"until": c.Until, "strikes": c.Strikes, "reason": c.Reason}
		} else {
			out[p.ID] = nil
		}
	}
	return out
}

func TestRouterResetInFixtures(t *testing.T) {
	f := routerFixture(t, "fn-resetin.json")
	var in struct {
		Now    int64     `json:"now"`
		Values []*string `json:"values"`
	}
	decodeFix(t, f.In, &in)
	got := make([]any, len(in.Values))
	for i, v := range in.Values {
		s := ""
		if v != nil {
			s = *v
		}
		if ms, ok := ResetIn(s, v != nil, in.Now); ok {
			got[i] = fixNum(ms)
		}
	}
	var want []json.RawMessage
	decodeFix(t, f.Out, &want)
	if len(want) != len(got) {
		t.Fatalf("%d outputs for %d values", len(want), len(got))
	}
	for i := range got {
		v := "null"
		if in.Values[i] != nil {
			v = *in.Values[i]
		}
		sameJSON(t, "resetIn("+jsonQuote(v)+")", got[i], want[i])
	}
}

func TestRouterReadQuotaFixtures(t *testing.T) {
	f := routerFixture(t, "fn-readquota.json")
	var in struct {
		Now     int64               `json:"now"`
		Headers []map[string]string `json:"headers"`
	}
	decodeFix(t, f.In, &in)
	var want []json.RawMessage
	decodeFix(t, f.Out, &want)
	if len(want) != len(in.Headers) {
		t.Fatalf("%d outputs for %d header sets", len(want), len(in.Headers))
	}
	for i, h := range in.Headers {
		b, _ := json.Marshal(h)
		sameJSON(t, "readQuota("+string(b)+")", fixReading(readQuota(headerOf(h), float64(in.Now))), want[i])
	}
}

type routerStep struct {
	At          int64             `json:"at"`
	Call        *string           `json:"call"`
	PrivateOnly bool              `json:"privateOnly"`
	Provider    string            `json:"provider"`
	Status      int               `json:"status"`
	Headers     map[string]string `json:"headers"`
}

type routerSequence struct {
	Note  string       `json:"note"`
	Steps []routerStep `json:"steps"`
}

func TestRouterNoteFixtures(t *testing.T) {
	f := routerFixture(t, "fn-note.json")
	var in struct {
		Providers []ipc.ProviderConfig `json:"providers"`
		Sequences []routerSequence     `json:"sequences"`
	}
	decodeFix(t, f.In, &in)
	var want [][]struct {
		Reading  json.RawMessage `json:"reading"`
		Cooldown json.RawMessage `json:"cooldown"`
	}
	decodeFix(t, f.Out, &want)
	if len(want) != len(in.Sequences) {
		t.Fatalf("%d outputs for %d sequences", len(want), len(in.Sequences))
	}
	for si, seq := range in.Sequences {
		r := NewRouteState(nil)
		for i, st := range seq.Steps {
			name := seq.Note + " step " + itoa(i)
			var reading any
			if st.Provider != "" {
				h := headerOf(st.Headers)
				r.Note(st.Provider, st.Status, h, st.At, false)
				reading = fixReading(readQuota(h, float64(st.At)))
			}
			sameJSON(t, name+" reading", reading, want[si][i].Reading)
			sameJSON(t, name+" cooldown", liveCooldowns(r, in.Providers, st.At), want[si][i].Cooldown)
		}
	}
}

// resolveAll is providers.js resolveAll composed from the port: the take on the raw names (a round-robin route of more
// than one target), one leg per raw name (nil when it does not resolve or a private-only key may not use it), Order.
func resolveAll(r *RouteState, gw *ipc.GatewayConfig, name string, privateOnly bool, at int64) []*Plan {
	var route *ipc.RouteConfig
	for i := range gw.Routes {
		if gw.Routes[i].Name == name {
			route = &gw.Routes[i]
			break
		}
	}
	names := []string{name}
	rr := false
	if route != nil {
		names = route.Targets
		rr = route.Strategy != nil && *route.Strategy == "round-robin" && len(names) > 1
	}
	n := 0
	if rr {
		n = r.Take(name, len(names), at, false)
	}
	legs := make([]*Plan, len(names))
	for i, nm := range names {
		if t := Resolve(gw.Providers, nm); t != nil && (!privateOnly || TierOf(t.Provider) == "private") {
			legs[i] = &Plan{Target: t}
		}
	}
	return Order(legs, n, rr, func(id string) bool { return r.Cooling(id, at) })
}

func TestRouterResolveFixtures(t *testing.T) {
	f := routerFixture(t, "fn-resolve.json")
	var in struct {
		Providers []ipc.ProviderConfig `json:"providers"`
		Routes    []ipc.RouteConfig    `json:"routes"`
		Sequences []routerSequence     `json:"sequences"`
	}
	decodeFix(t, f.In, &in)
	gw := &ipc.GatewayConfig{Providers: in.Providers, Routes: in.Routes}
	var want [][]struct {
		Order    json.RawMessage `json:"order"`
		Cooldown json.RawMessage `json:"cooldown"`
	}
	decodeFix(t, f.Out, &want)
	if len(want) != len(in.Sequences) {
		t.Fatalf("%d outputs for %d sequences", len(want), len(in.Sequences))
	}
	for si, seq := range in.Sequences {
		r := NewRouteState(nil)
		for i, st := range seq.Steps {
			name := seq.Note + " step " + itoa(i)
			if st.Provider != "" {
				r.Note(st.Provider, st.Status, headerOf(st.Headers), st.At, false)
				sameJSON(t, name+" cooldown", liveCooldowns(r, in.Providers, st.At), want[si][i].Cooldown)
				continue
			}
			order := []any{}
			for _, l := range resolveAll(r, gw, *st.Call, st.PrivateOnly, st.At) {
				order = append(order, map[string]any{"name": l.Target.Name, "provider": l.Target.Provider.ID, "model": l.Target.Model})
			}
			sameJSON(t, name+" "+*st.Call+" order", order, want[si][i].Order)
		}
	}
}

func TestRouterOrder(t *testing.T) {
	p := func(id string) *Plan {
		return &Plan{Target: &Target{Provider: &ipc.ProviderConfig{ID: id}, Name: id + "/m"}}
	}
	names := func(ls []*Plan) string {
		var s []string
		for _, l := range ls {
			s = append(s, l.Target.Name)
		}
		return strings.Join(s, ",")
	}
	none := func(string) bool { return false }
	legs := []*Plan{p("a"), nil, p("b"), p("c")}
	cases := []struct {
		n    int
		rr   bool
		cool func(string) bool
		want string
	}{
		{0, false, none, "a/m,b/m,c/m"},
		{2, false, none, "a/m,b/m,c/m"}, // a fallback route never rotates
		{1, true, none, "b/m,c/m,a/m"},
		{2, true, none, "b/m,c/m,a/m"},
		{3, true, none, "c/m,a/m,b/m"},
		{5, true, none, "b/m,c/m,a/m"}, // n is taken modulo the leg count
		{0, true, func(id string) bool { return id == "a" || id == "c" }, "b/m,a/m,c/m"},
		{3, true, func(id string) bool { return id == "c" }, "a/m,b/m,c/m"},
	}
	for _, c := range cases {
		if got := names(Order(legs, c.n, c.rr, c.cool)); got != c.want {
			t.Errorf("Order(n=%d, rr=%v) = %s, want %s", c.n, c.rr, got, c.want)
		}
	}
	if got := Order([]*Plan{nil, nil}, 1, true, none); len(got) != 0 {
		t.Errorf("all-nil legs: %d legs", len(got))
	}
	one := []*Plan{p("a")}
	if got := names(Order(one, 7, true, none)); got != "a/m" {
		t.Errorf("one leg: %s", got)
	}
	if legs[0].Target.Name != "a/m" || legs[1] != nil {
		t.Errorf("Order changed its input")
	}
}

func TestRouterTakePeek(t *testing.T) {
	r := NewRouteState(nil)
	if n := r.Peek("router/x", 3); n != 0 {
		t.Fatalf("peek before any take: %d", n)
	}
	for i, want := range []int{0, 1, 2, 0, 1} {
		if n := r.Take("router/x", 3, 1, false); n != want {
			t.Fatalf("take %d: %d, want %d", i, n, want)
		}
	}
	if n := r.Peek("router/x", 3); n != 2 {
		t.Fatalf("peek: %d, want 2", n)
	}
	// The target count shrank (a config push): the counter is taken modulo the new count, as JS does.
	if n := r.Take("router/x", 2, 1, false); n != 0 {
		t.Fatalf("take after shrink: %d, want 0", n)
	}
	if n := r.Take("router/x", 2, 1, false); n != 1 {
		t.Fatalf("take after shrink: %d, want 1", n)
	}
	// One target or none: JS never rotates, the counter is left as it is.
	if n := r.Take("router/x", 1, 1, false); n != 0 {
		t.Fatalf("take of one target: %d", n)
	}
	if n := r.Peek("router/x", 2); n != 0 {
		t.Fatalf("peek after a one-target take: %d, want 0 (rr 2 %% 2)", n)
	}
	if n := r.Take("router/x", 0, 1, false); n != 0 {
		t.Fatalf("take of no target: %d", n)
	}
}

func TestRouterSendEchoFree(t *testing.T) {
	type sent struct {
		typ  string
		body any
	}
	var got []sent
	r := NewRouteState(func(typ string, body any) { got = append(got, sent{typ, body}) })
	h := http.Header{}
	h.Add("Retry-After", "3")
	h.Add("Retry-After", "4")
	h.Set("X-Ratelimit-Remaining-Requests", "0")
	h.Set("Content-Type", "application/json")
	r.Note("a", 429, h, 100, false)
	r.Note("b", 429, h, 100, true)
	r.Take("router/x", 2, 101, false)
	r.Take("router/x", 2, 102, true)
	if len(got) != 2 {
		t.Fatalf("sent %d messages, want 2 (remote notes and takes are not sent back): %+v", len(got), got)
	}
	b, _ := json.Marshal(got[0].body)
	if got[0].typ != "provider.note" ||
		string(b) != `{"providerId":"a","status":429,"at":100,"headers":{"retry-after":"3, 4","x-ratelimit-remaining-requests":"0"}}` {
		t.Errorf("note: %s %s", got[0].typ, b)
	}
	b, _ = json.Marshal(got[1].body)
	if got[1].typ != "route.take" || string(b) != `{"name":"router/x","at":101}` {
		t.Errorf("take: %s %s", got[1].typ, b)
	}
	// A remote note and take are applied all the same.
	if !r.Cooling("b", 100) {
		t.Errorf("remote note not applied")
	}
	if n := r.Peek("router/x", 2); n != 0 {
		t.Errorf("remote take not applied: peek %d, want 0", n)
	}
}

func TestRouterNoteHeaders(t *testing.T) {
	h := http.Header{}
	for _, k := range noteHeaderNames {
		h.Set(k, "v-"+k)
	}
	h.Add("X-Ratelimit-Limit-Requests", "second")
	h.Set("X-Other", "x")
	got := NoteHeaders(h)
	if len(got) != len(noteHeaderNames) {
		t.Fatalf("%d headers, want %d: %v", len(got), len(noteHeaderNames), got)
	}
	if got["x-ratelimit-limit-requests"] != "v-x-ratelimit-limit-requests, second" {
		t.Errorf("joined: %q", got["x-ratelimit-limit-requests"])
	}
	if _, ok := got["x-other"]; ok {
		t.Errorf("an unread header was kept")
	}
	if got := NoteHeaders(http.Header{}); len(got) != 0 {
		t.Errorf("no headers: %v", got)
	}
}

// 64 goroutines take, note and read at once: -race finds no data race, and the counter ends where the same number of
// takes in a row would leave it.
func TestRouterConcurrent(t *testing.T) {
	var sends atomic.Int64
	r := NewRouteState(func(string, any) { sends.Add(1) })
	const workers, each = 64, 100
	var counts [3]atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			h := http.Header{}
			h.Set("Retry-After", "1")
			for i := 0; i < each; i++ {
				counts[r.Take("router/x", 3, int64(i), false)].Add(1)
				r.Note([]string{"a", "b"}[w%2], []int{429, 503, 200}[i%3], h, int64(i), w%4 == 0)
				r.Cooling("a", int64(i))
				r.Peek("router/x", 3)
			}
		}(w)
	}
	wg.Wait()
	total := workers * each
	if c := [3]int64{counts[0].Load(), counts[1].Load(), counts[2].Load()}; c != [3]int64{2134, 2133, 2133} {
		t.Errorf("takes per position %v, want [2134 2133 2133]", c)
	}
	if n := r.Peek("router/x", 3); n != total%3 {
		t.Errorf("peek %d, want %d", n, total%3)
	}
	if want := int64(total + total*3/4); sends.Load() != want {
		t.Errorf("sent %d, want %d", sends.Load(), want)
	}
}

func routerFixture(t *testing.T, file string) fixture {
	t.Helper()
	for _, f := range loadFixtures(t, "router") {
		if f.Name == file {
			return f
		}
	}
	t.Fatalf("no gw/testdata/router/%s (run node scripts/fixtures/gen-router.js)", file)
	return fixture{}
}

func decodeFix(t *testing.T, b json.RawMessage, v any) {
	t.Helper()
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}

func jsonQuote(s string) string { b, _ := json.Marshal(s); return string(b) }

// sentMsg is one control message the route state handed to send.
type sentMsg struct {
	typ  string
	body any
}

// A local note sends exactly one provider.note; a remote one sends none. The same for takes.
func TestRouterNoteSendsOnce(t *testing.T) {
	var got []sentMsg
	r := NewRouteState(func(typ string, body any) { got = append(got, sentMsg{typ, body}) })
	h := http.Header{}
	h.Set("Retry-After", "2")
	r.Note("a", 429, h, 100, false)
	if len(got) != 1 || got[0].typ != ipc.TypeProviderNote {
		t.Fatalf("local note: %+v", got)
	}
	if b, ok := got[0].body.(ipc.ProviderNote); !ok || b.ProviderID != "a" || b.Status != 429 || b.At != 100 ||
		b.Headers["retry-after"] != "2" {
		t.Fatalf("local note body: %#v", got[0].body)
	}
	r.Note("a", 200, http.Header{}, 101, true)
	if len(got) != 1 {
		t.Fatalf("a remote note was sent back: %+v", got)
	}
	if r.Cooling("a", 101) {
		t.Fatal("the remote 200 did not clear the cooldown")
	}
	r.Take("router/x", 2, 102, true)
	if len(got) != 1 {
		t.Fatalf("a remote take was sent back: %+v", got)
	}
	r.Take("router/x", 2, 103, false)
	if len(got) != 2 || got[1].typ != ipc.TypeRouteTake || got[1].body != (ipc.RouteTake{Name: "router/x", At: 103}) {
		t.Fatalf("local take: %+v", got)
	}
}

// Sends leave in the order the state changed (send runs under the mutex): with 32 goroutines noting one provider, the
// strikes of the sent notes, replayed in order on a second state, end where the first one is.
func TestRouterSendOrder(t *testing.T) {
	var mu sync.Mutex
	var notes []ipc.ProviderNote
	a := NewRouteState(func(typ string, body any) {
		if !mu.TryLock() {
			t.Error("send blocked: the mutex was not free (send must not need another lock here)")
			return
		}
		notes = append(notes, body.(ipc.ProviderNote))
		mu.Unlock()
	})
	var wg sync.WaitGroup
	for w := 0; w < 32; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				h := http.Header{}
				status := []int{429, 503, 200}[(w+i)%3]
				a.Note("p", status, h, int64(1000*(w*50+i)), false)
			}
		}(w)
	}
	wg.Wait()
	b := NewRouteState(nil)
	for _, n := range notes {
		h := http.Header{}
		for k, v := range n.Headers {
			h.Set(k, v)
		}
		b.Note(n.ProviderID, n.Status, h, n.At, true)
	}
	if sa, sb := a.Snapshot(), b.Snapshot(); !reflectEqualJSON(sa, sb) {
		t.Fatalf("replayed in send order: %+v, applied: %+v", sb, sa)
	}
}

func reflectEqualJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// Seed replaces the state; a Take after it continues the seeded counter; Snapshot gives it back.
func TestRouterSeedThenTake(t *testing.T) {
	r := NewRouteState(nil)
	r.Take("router/old", 2, 1, false)
	r.Note("old", 429, http.Header{}, 1, false)
	seed := ipc.RouteState{
		RR:       []ipc.RouteRR{{Name: "router/x", N: 7}},
		Cooldown: []ipc.RouteCooldown{{ProviderID: "a", Until: 5000.5, Strikes: 2, Reason: "HTTP 429"}},
	}
	r.Seed(seed)
	if got := r.Snapshot(); !reflectEqualJSON(got, seed) {
		t.Fatalf("snapshot after the seed: %+v", got)
	}
	if n := r.Take("router/x", 3, 2, false); n != 1 { // 7 % 3
		t.Fatalf("take after the seed: %d, want 1", n)
	}
	if n := r.Take("router/x", 3, 3, false); n != 2 { // 2 % 3
		t.Fatalf("second take: %d, want 2", n)
	}
	if !r.Cooling("a", 5000) || r.Cooling("a", 5001) {
		t.Fatal("the seeded cooldown")
	}
	// The next 429 doubles from the seeded strikes: 1000 * 2^2.
	r.Note("a", 429, http.Header{}, 10000, false)
	if c := r.Snapshot().Cooldown[0]; c.Until != 14000 || c.Strikes != 3 {
		t.Fatalf("after a 429: %+v", c)
	}
}

// The upstream notes every provider answer once its headers are in: the last of a redirect chain only, and both
// answers of a 401 replay (providers.js request calls noteResponse on each).
func TestUpstreamNotesEveryAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/a" {
			http.Redirect(w, r, "/b", http.StatusFound)
			return
		}
		w.Header().Set("X-Ratelimit-Remaining-Requests", "0")
		w.WriteHeader(429)
	}))
	defer srv.Close()
	up := NewUpstream(func(string) string { return "" })
	var got []int
	up.SetOnAnswer(func(p *Plan, status int, h http.Header) {
		got = append(got, status)
		if h.Get("X-Ratelimit-Remaining-Requests") != "0" {
			t.Errorf("headers: %v", h)
		}
	})
	if res := up.Call(context.Background(), planTo(srv.URL+"/a")); res.Status != 429 {
		t.Fatalf("%+v", res)
	}
	if len(got) != 1 || got[0] != 429 {
		t.Fatalf("notes %v, want [429]", got)
	}

	var statuses []int
	rt := rtFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			return answer(200, `{"access_token":"tok","expires_in":3600}`), nil
		}
		if len(statuses) == 0 {
			return answer(401, `{}`), nil
		}
		return answer(200, `{}`), nil
	})
	tup, p := tokenPlan(t, rt)
	tup.SetOnAnswer(func(p *Plan, status int, h http.Header) { statuses = append(statuses, status) })
	if res := tup.Call(context.Background(), &p); res.Status != 200 {
		t.Fatalf("%+v", res)
	}
	if len(statuses) != 2 || statuses[0] != 401 || statuses[1] != 200 {
		t.Fatalf("notes %v, want [401 200]", statuses)
	}
}

// An engine notes the provider answer of a call it serves (its own cooldown, and one provider.note at the engine's
// clock), and applies the control plane's notes, takes (with its own target count) and seed without sending them back.
func TestEngineRouteReplication(t *testing.T) {
	rt := rtFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			return answer(200, `{"access_token":"tok","expires_in":3600}`), nil
		}
		a := answer(429, `{"error":{"message":"slow down"}}`)
		a.Header.Set("Retry-After", "7")
		a.Header.Set("X-Ratelimit-Remaining-Tokens", "0")
		return a, nil
	})
	f := authUnitFixture(t)
	var mu sync.Mutex
	var lines []ipc.UsageRecord
	s := authState(t, f)
	rr := "round-robin"
	s.Gateway.Routes = append(s.Gateway.Routes, ipc.RouteConfig{Name: "router/two", Targets: []string{"a/x", "b/y"}, Strategy: &rr},
		ipc.RouteConfig{Name: "router/fb", Targets: []string{"a/x", "b/y"}})
	e := authEngine(t, rt, s, &lines, &mu)
	var got []sentMsg
	e.SetRouteSend(func(typ string, body any) { got = append(got, sentMsg{typ, body}) })
	rq := f.In.Requests[0]
	req := httptest.NewRequest(rq.Method, "http://127.0.0.1:4777"+rq.Path, strings.NewReader(rq.Body))
	req.Host, req.RemoteAddr = "127.0.0.1:4777", "127.0.0.1:50000"
	for k, v := range rq.Headers {
		req.Header.Set(k, v)
	}
	raw, _, _ := strings.Cut(rq.Path, "?")
	e.Serve(classOfPath(raw), httptest.NewRecorder(), req, raw, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handed off")
	}))
	e.streams.Wait()
	want := ipc.ProviderNote{ProviderID: "az", Status: 429, At: fixtureNow,
		Headers: map[string]string{"retry-after": "7", "x-ratelimit-remaining-tokens": "0"}}
	if len(got) != 1 || got[0].typ != ipc.TypeProviderNote || !reflectEqualJSON(got[0].body, want) {
		t.Fatalf("sent %+v, want one %+v", got, want)
	}
	if !e.Routes().Cooling("az", fixtureNow+6999) || e.Routes().Cooling("az", fixtureNow+7000) {
		t.Fatal("the engine's own cooldown of az")
	}

	e.RouteControl(&ipc.ProviderNote{ProviderID: "az", Status: 200, At: fixtureNow + 1, Headers: map[string]string{}})
	if e.Routes().Cooling("az", fixtureNow+1) {
		t.Fatal("a remote 200 did not clear the cooldown")
	}
	e.RouteControl(&ipc.RouteTake{Name: "router/two", At: 1})
	e.RouteControl(&ipc.RouteTake{Name: "router/fb", At: 1})      // not round-robin: nothing
	e.RouteControl(&ipc.RouteTake{Name: "router/missing", At: 1}) // not here: nothing
	if n := e.Routes().Peek("router/two", 2); n != 1 {
		t.Fatalf("after a remote take: peek %d, want 1", n)
	}
	if n := e.Routes().Peek("router/fb", 2); n != 0 {
		t.Fatalf("a fallback route was rotated: %d", n)
	}
	e.RouteControl(&ipc.RouteState{RR: []ipc.RouteRR{{Name: "router/two", N: 4}}, Cooldown: []ipc.RouteCooldown{}})
	if n := e.Routes().Take("router/two", 2, 2, false); n != 0 {
		t.Fatalf("take after the seed: %d, want 0", n)
	}
	if len(got) != 2 || got[1].typ != ipc.TypeRouteTake {
		t.Fatalf("remote messages were sent back, or the local take was not sent: %+v", got)
	}
}
