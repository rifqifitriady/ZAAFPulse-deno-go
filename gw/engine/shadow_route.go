package engine

// The shadow compare of a route call (P4h spec Decision 11, design D5): the control plane served it and kept its route
// tape (each resolveAll: the name, the counter n before the take, the cooling provider ids, the order it gave), every
// usage line it wrote, and every provider answer as read, a call that threw included ({ thrown }). The engine
// recomputes each order from (n, cooling) and compares it with JS's (route.order), then replays the legs in JS's order
// through serveRoute against the captured answers, on a throwaway engine (no take, no note, no line reaches the real
// state), and compares every provider request, the client's answer and every line. Separately, the line reports the
// engine's own view of the route when it planned the call (routeView), outside the diff gate.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

// CaptureRoute is one resolveAll of a captured route call (lib/gateway/providers.js shadow.noteRoute): the counter before
// the take (0 when the route does not rotate), the providers cooling down among its targets, and the target names in
// the order it gave.
type CaptureRoute struct {
	Name    string   `json:"name"`
	N       int      `json:"n"`
	Cooling []string `json:"cooling"`
	Order   []string `json:"order"`
}

// NodeBodyUsedText is the TypeError text Node's Response.text() throws for a body already cancelled; the engine writes
// Deno's (BodyUsedText, the control plane's runtime). The compare is exact: under Node the quirk's answer is a
// client.body diff, a known difference (spec Decision 15f); the capture's runtime says which runtime answered.
const NodeBodyUsedText = "Body is unusable: Body has already been read"

// replayFailure is a provider call that threw in the control plane, replayed: ErrorCode names it by the text JS named it
// with (the part after "<provider id>: "), so the replayed 599 line is built from the same failure.
type replayFailure string

func (r replayFailure) Error() string { return string(r) }

// routeView is the engine's own view of a route when it planned a forwarded call: its counter and the providers cooling
// down among the route's legs, read without a take (spec Decision 11).
type routeView struct {
	n       int
	cooling []string
}

// peekRoute reads the engine's view of plan p's route at now.
func (e *Engine) peekRoute(p *Plan, now int64) *routeView {
	v := &routeView{n: e.routes.Peek(p.Route.Name, len(p.Route.Targets))}
	for _, l := range p.Legs {
		if l != nil && e.routes.Cooling(l.Target.Provider.ID, now) && !slices.Contains(v.cooling, l.Target.Provider.ID) {
			v.cooling = append(v.cooling, l.Target.Provider.ID)
		}
	}
	return v
}

// against is "same" when the view equals JS's first resolveAll of the call (the counter and the cooling set), else
// "differs"; "" when the capture has no tape.
func (v *routeView) against(c *Capture) string {
	if v == nil || c == nil || len(c.Routes) == 0 {
		return ""
	}
	js := c.Routes[0]
	a, b := slices.Clone(v.cooling), slices.Clone(js.Cooling)
	slices.Sort(a)
	slices.Sort(b)
	a, b = slices.Compact(a), slices.Compact(b)
	if v.n == js.N && slices.Equal(a, b) {
		return "same"
	}
	return "differs"
}

// tapeRequest is a provider request the replay sent.
type tapeRequest struct {
	url     string
	headers map[string]string
	body    string
}

// tapeTransport answers the replay's provider requests with the captured answers, in order, and keeps each request.
type tapeTransport struct {
	mu      sync.Mutex
	answers []ProviderAnswer
	sent    []tapeRequest
}

func (t *tapeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
		_ = req.Body.Close()
	}
	h := map[string]string{}
	for k := range req.Header {
		h[strings.ToLower(k)] = req.Header.Get(k)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sent = append(t.sent, tapeRequest{url: req.URL.String(), headers: h, body: string(body)})
	if len(t.answers) == 0 {
		return nil, replayFailure("no captured answer left")
	}
	a := t.answers[0]
	t.answers = t.answers[1:]
	if a.Thrown != "" {
		_, why, ok := strings.Cut(a.Thrown, ": ")
		if !ok {
			why = a.Thrown
		}
		return nil, replayFailure(why)
	}
	hd := http.Header{}
	if a.ContentType != "" {
		hd.Set("Content-Type", a.ContentType)
	}
	return &http.Response{StatusCode: a.Status, Header: hd, Body: io.NopCloser(cutReader(a)), Request: req,
		Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1}, nil
}

// replayWriter keeps the replay's answer to the client.
type replayWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *replayWriter) Header() http.Header { return w.header }
func (w *replayWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *replayWriter) Write(b []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.body.Write(b)
}
func (w *replayWriter) Flush() {}

// routeOrder is the target names of legs in order.
func routeOrder(legs []*Plan) []string {
	out := make([]string, 0, len(legs))
	for _, l := range legs {
		out = append(out, l.Target.Name)
	}
	return out
}

// CompareRoute is the line of a route call: p was planned before the control plane answered (and the token saver has
// run on it); c is its capture. staleMs and now are the engine's snapshot age limit and clock (the replay's budget).
func (s *State) CompareRoute(class string, p *Plan, c *Capture, staleMs int64, now func() int64) ShadowLine {
	l := ShadowLine{Class: class, Route: true}
	if c != nil && c.Client != nil {
		l.Status = c.Client.Status
	}
	if p.Handoff != "" {
		l.Outcome, l.Reason = "handoff", p.Handoff
		return l
	}
	d := func(field string, js, gov any) { l.Diffs = append(l.Diffs, Diff{field, js, gov}) }
	dt := func(field, js, gov string) { a, b := digest(js, gov); d(field, a, b) }
	done := func() ShadowLine {
		l.Outcome = "same"
		if len(l.Diffs) > 0 {
			l.Outcome = "diff"
		}
		return l
	}
	skip := func(why string) ShadowLine {
		l.Outcome, l.Reason, l.Diffs = "skipped", why, nil
		return l
	}
	if c == nil || c.Client == nil {
		d("served", "no capture", "the engine would route the call")
		return done()
	}
	if c.Client.Truncated || slices.ContainsFunc(c.Upstream, func(a ProviderAnswer) bool { return a.Truncated }) {
		return skip("the capture is over its limit")
	}
	if slices.ContainsFunc(c.Upstream, func(a ProviderAnswer) bool { return a.Token }) {
		return skip(authOwnFailure) // a leg's token fetch threw in the control plane: each engine holds its own token
	}
	if p.TokenAuth() && slices.ContainsFunc(c.Upstream, func(a ProviderAnswer) bool {
		return a.Thrown == "" && a.Status == http.StatusUnauthorized
	}) {
		return skip(authReplay)
	}
	if len(c.Routes) == 0 {
		// JS answered before resolveAll: its rate limit (each engine counts its own), or a refusal the engine would not
		// make (a diff).
		if c.Client.Status == http.StatusTooManyRequests {
			return skip("the control plane's rate limit answered (each engine counts its own)")
		}
		d("served", "answered "+NumberString(float64(c.Client.Status))+" without a route take",
			"the engine would route the call")
		return done()
	}
	if len(c.Routes) != p.Takes {
		d("route.takes", len(c.Routes), p.Takes)
	}
	// Each order JS gave, recomputed from its counter and cooling set.
	rr := p.Route.Strategy != nil && *p.Route.Strategy == "round-robin"
	for i, t := range c.Routes {
		goOrder := routeOrder(Order(p.Legs, t.N, rr, func(id string) bool { return slices.Contains(t.Cooling, id) }))
		if t.Name != p.Route.Name || !slices.Equal(t.Order, goOrder) {
			field := "route.order"
			if len(c.Routes) > 1 {
				field += "." + strconv.Itoa(i)
			}
			d(field, t.Name+" "+strings.Join(t.Order, ","), p.Route.Name+" "+strings.Join(goOrder, ","))
		}
	}
	// The legs in JS's order (its last resolveAll orders the call).
	jsOrder := c.Routes[len(c.Routes)-1].Order
	legs := make([]*Plan, 0, len(jsOrder))
	used := make([]bool, len(p.Legs))
	for _, name := range jsOrder {
		at := slices.IndexFunc(p.Legs, func(x *Plan) bool { return x != nil && x.Target.Name == name })
		for at >= 0 && used[at] {
			next := slices.IndexFunc(p.Legs[at+1:], func(x *Plan) bool { return x != nil && x.Target.Name == name })
			if next < 0 {
				at = -1
				break
			}
			at += 1 + next
		}
		if at < 0 {
			d("route.leg", name, "no such leg")
			return done()
		}
		used[at] = true
		legs = append(legs, p.Legs[at])
	}
	answer, sent, lines, check := s.replayRoute(p, legs, c, staleMs, now)

	jsBody := c.Client.Body
	if (budgetRefusalAny(c.Client.Status, c.Client.Body) || budgetRefusalAny(answer.status, answer.body.String())) &&
		(c.Client.Status != answer.status || jsBody != answer.body.String()) {
		return skip(budgetLag) // each side's own view of the budgets (P4e Decision 18)
	}
	if len(sent) != len(c.ProviderRequests) {
		d("providerRequests", len(c.ProviderRequests), len(sent))
	}
	for i := 0; i < len(sent) && i < len(c.ProviderRequests); i++ {
		js, gov := c.ProviderRequests[i], sent[i]
		at := "providerRequests." + strconv.Itoa(i)
		if js.URL != gov.url {
			dt(at+".url", js.URL, gov.url)
		}
		jh, gh := js.Headers, gov.headers
		if strings.Contains(gh["authorization"], tokenMark) || strings.Contains(gh["x-api-key"], tokenMark) {
			jh, gh = schemeOnly(jh), schemeOnly(gh) // a token-auth leg: compared by name and scheme (P4g Decision 15)
		}
		if a, b := headerSet(jh), headerSet(gh); a != b {
			dt(at+".headers", a, b)
		}
		jb := ""
		if js.Body != nil {
			jb = *js.Body
		}
		if jb != gov.body {
			dt(at+".body", jb, gov.body)
		}
	}
	if answer.status != c.Client.Status {
		d("client.status", c.Client.Status, answer.status)
	}
	for _, k := range []string{"content-type", "x-zp-dropped"} {
		if js, gov := c.Client.Headers[k], answer.header.Get(k); js != gov {
			d("client.headers."+k, js, gov)
		}
	}
	if js, gov := stripPings(jsBody), stripPings(answer.body.String()); js != gov {
		dt("client.body", js, gov)
	}
	if why := check(); why != "" {
		d("tape", why, "")
	}
	if len(lines) != len(c.Lines) {
		d("lines", len(c.Lines), len(lines))
	}
	for i := 0; i < len(lines) && i < len(c.Lines); i++ {
		js, at := c.Lines[i], "lines."+strconv.Itoa(i)+"."
		var goU map[string]any
		b, _ := json.Marshal(lines[i])
		_ = json.Unmarshal(b, &goU)
		for _, f := range usageFields {
			if canonical(js[f]) != canonical(goU[f]) {
				d(at+f, js[f], goU[f])
			}
		}
		if a, b := text(js["error"]), text(goU["error"]); a != b {
			dt(at+"error", a, b)
		}
		jsCost, _ := js["cost"].(float64)
		if n := math.Round(jsCost * 1e9); int64(n) != lines[i].Cost {
			d(at+"cost", int64(n), lines[i].Cost)
		}
	}
	return done()
}

// replayRoute serves plan p's legs in the given order through serveRoute on a throwaway engine whose provider is the
// capture's answers: the client's answer, the provider requests sent, the usage lines written in order, and how the
// translation used the capture's tape.
func (s *State) replayRoute(p *Plan, legs []*Plan, c *Capture, staleMs int64, now func() int64) (*replayWriter, []tapeRequest,
	[]ipc.UsageRecord, func() string) {
	tt := &tapeTransport{answers: slices.Clone(c.Upstream)}
	up := &Upstream{client: &http.Client{Transport: tt, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}, timeout: Timeout}
	var mu sync.Mutex
	var lines []ipc.UsageRecord
	logf := s.logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	x := New(up, func(r *ipc.UsageReport) (int64, error) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, r.Record)
		return 0, nil
	}, logf)
	x.state.Store(s) // not SetState: the generation is shared with the serving engine and stays as it is
	x.staleMs, x.now = staleMs, now
	env, check := tapeEnv(c)
	q := *p
	q.Env = env
	q.replay = make([]*Plan, len(legs))
	for i, l := range legs {
		lc := *l // the replay edits its own copy of each leg; the planned legs stay as they are
		lc.Env = env
		if lc.auth != nil {
			// A token-auth leg: the replay sends a marked token (each engine holds its own; compared by scheme).
			lc.Header = lc.Header.Clone()
			setToken(lc.Header, lc.Target, tokenMark)
			lc.auth = nil
		}
		q.replay[i] = &lc
	}
	r, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://127.0.0.1/", http.NoBody)
	w := &replayWriter{header: http.Header{}}
	x.serveRoute(w, r, s, &q, &Request{}, time.Now(), nil, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot) // serveRoute hands nothing off once it runs; a shutting-down engine is not this one
	}))
	x.streams.Wait() // a stream's line goes out after its last byte
	tt.mu.Lock()
	sent := slices.Clone(tt.sent)
	tt.mu.Unlock()
	mu.Lock()
	defer mu.Unlock()
	return w, sent, slices.Clone(lines), check
}
