package engine

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/front"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"
)

// routeSeq is one gw/testdata/router/seq-*.json (scripts/fixtures/gen-router.js, Task 3 report "Fixture format"):
// client calls sharing one JS gateway, so rotation and cooldowns carry from call to call.
type routeSeq struct {
	Name string
	In   struct {
		Note        string              `json:"note"`
		ConfigPush  ipc.ConfigPush      `json:"configPush"`
		SecretsPush ipc.SecretsHandoff  `json:"secretsPush"`
		Snapshot    *ipc.BudgetSnapshot `json:"snapshot"`
		Start       int64               `json:"start"`
		Steps       []struct {
			AdvanceMs int64 `json:"advanceMs"`
			Request   struct {
				Method  string            `json:"method"`
				Path    string            `json:"path"`
				Headers map[string]string `json:"headers"`
				Body    string            `json:"body"`
			} `json:"request"`
			Answers []xlateProvider `json:"answers"`
		} `json:"steps"`
	} `json:"in"`
	Out struct {
		Steps []struct {
			Now      int64 `json:"now"`
			Requests []struct {
				URL     string            `json:"url"`
				Method  string            `json:"method"`
				Headers map[string]string `json:"headers"`
				Body    *string           `json:"body"`
			} `json:"requests"`
			Client struct {
				Status  int                `json:"status"`
				Headers map[string]*string `json:"headers"`
				Body    string             `json:"body"`
			} `json:"client"`
			Usage  []json.RawMessage `json:"usage"`
			Events []struct {
				Type string          `json:"type"`
				Body json.RawMessage `json:"body"`
			} `json:"events"`
			State json.RawMessage `json:"state"`
		} `json:"steps"`
	} `json:"out"`
}

// loadRouteSeqs reads every seq-*.json with the secrets put back (as jsv.LoadFixture does; "in" is an object here).
func loadRouteSeqs(t *testing.T) []routeSeq {
	t.Helper()
	dir := filepath.Join("..", "testdata", "router")
	files, _ := filepath.Glob(filepath.Join(dir, "seq-*.json"))
	if len(files) == 0 {
		t.Fatalf("no sequences in %s (run node scripts/fixtures/gen-router.js)", dir)
	}
	var out []routeSeq
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var head struct {
			Secrets []string `json:"secrets"`
		}
		if err := json.Unmarshal(data, &head); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		text := string(data)
		for i, s := range head.Secrets {
			raw, err := base64.StdEncoding.DecodeString(s)
			if err != nil {
				t.Fatal(err)
			}
			var b strings.Builder
			jsv.Quote(&b, string(raw))
			q := b.String()
			text = strings.ReplaceAll(text, "<<ZPSECRET:"+strconv.Itoa(i)+">>", q[1:len(q)-1])
		}
		seq := routeSeq{Name: filepath.Base(f)}
		if err := json.Unmarshal([]byte(text), &seq); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if len(seq.In.Steps) != len(seq.Out.Steps) {
			t.Fatalf("%s: %d steps in, %d out", f, len(seq.In.Steps), len(seq.Out.Steps))
		}
		out = append(out, seq)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// sentRequest is a provider request as the replay transport saw it.
type sentRequest struct {
	url, method string
	headers     map[string]string
	body        string
}

// replayTransport answers the engine's provider requests with a step's answers, in order, and records each request.
type replayTransport struct {
	mu      sync.Mutex
	answers []xlateProvider
	sent    []sentRequest
}

func (rt *replayTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
		_ = req.Body.Close()
	}
	h := map[string]string{}
	for k := range req.Header {
		h[strings.ToLower(k)] = req.Header.Get(k)
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.sent = append(rt.sent, sentRequest{url: req.URL.String(), method: req.Method, headers: h, body: string(body)})
	if len(rt.answers) == 0 {
		return nil, errors.New("replay: no answer left for this request")
	}
	a := rt.answers[0]
	rt.answers = rt.answers[1:]
	if a.Throw != "" {
		// fetch rejects as Node's does for a refused connection: err.cause.code ECONNREFUSED.
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
	}
	hd := http.Header{}
	for k, v := range a.Headers {
		hd.Set(k, v)
	}
	var rb io.Reader = strings.NewReader(a.Body)
	if a.Chunks != nil {
		cr := &chunkReader{cut: a.Cut}
		for _, ch := range a.Chunks {
			cr.chunks = append(cr.chunks, []byte(ch))
		}
		rb = cr
	}
	return &http.Response{StatusCode: a.Status, Header: hd, Body: io.NopCloser(rb), Request: req, Proto: "HTTP/1.1",
		ProtoMajor: 1, ProtoMinor: 1}, nil
}

// routeRig is one engine for one sequence: the replay transport, a clock the test sets per step, the usage lines and
// the routing messages it sends.
type routeRig struct {
	e      *Engine
	rt     *replayTransport
	clock  atomic.Int64
	mu     sync.Mutex
	lines  []ipc.UsageRecord
	marks  []*string // each line's budget mark (unchecked or none)
	events []map[string]any
	js     int
}

func newRouteRig(t *testing.T, seq *routeSeq, modes func(string) (bool, bool)) *routeRig {
	t.Helper()
	g := &routeRig{rt: &replayTransport{}}
	g.clock.Store(seq.In.Start)
	up := NewUpstream(func(string) string { return "" })
	up.client.Transport = g.rt
	g.e = New(up, func(r *ipc.UsageReport) (int64, error) {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.lines = append(g.lines, r.Record)
		g.marks = append(g.marks, r.Budget)
		return 0, nil
	}, t.Logf)
	g.e.now = g.clock.Load
	g.e.SetRouteSend(func(typ string, body any) {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.events = append(g.events, map[string]any{"type": typ, "body": body})
	})
	c, sp := seq.In.ConfigPush, seq.In.SecretsPush
	c.Gen, sp.Gen = 1, 1
	s := NewState(&c, &sp)
	if seq.In.Snapshot != nil {
		s = s.WithSnapshot(seq.In.Snapshot)
	}
	g.e.SetState(s)
	if modes != nil {
		g.e.SetModes(modes)
	}
	return g
}

// serve sends one client request through Engine.Serve from a loopback client; handed is true when it went to js.
func (g *routeRig) serve(method, path string, headers map[string]string, body string) (*httptest.ResponseRecorder, bool) {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	r.Host, r.RemoteAddr = "127.0.0.1:4777", "127.0.0.1:50000"
	w := httptest.NewRecorder()
	handed := false
	js := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handed = true
		g.js++
		w.WriteHeader(299)
	})
	raw := front.RawPath(r.RequestURI)
	g.e.Serve(front.Classify(raw), w, r, raw, js)
	g.e.streams.Wait() // a stream's line goes out after its last byte
	return w, handed
}

// take empties what the step left: its usage lines and routing messages.
func (g *routeRig) take() ([]ipc.UsageRecord, []map[string]any) {
	g.mu.Lock()
	defer g.mu.Unlock()
	l, ev := g.lines, g.events
	g.lines, g.events = nil, nil
	return l, ev
}

// sequenceEnv makes translate.Default the generator's determinism for one sequence (scripts/fixtures/lib.js): random
// bytes from a counter that starts at 1 per sequence and runs across its steps, Date.now the step's clock.
func sequenceEnv(t *testing.T, clock func() int64) {
	was := *translate.Default
	n := 0
	*translate.Default = translate.Env{Now: clock, Random: func(k int) []byte {
		n++
		b := make([]byte, k)
		for i := range b {
			b[i] = byte(n)
		}
		return b
	}}
	t.Cleanup(func() { *translate.Default = was })
}

// Every chat call sequence the JS gateway recorded (P4h spec Decision 12) replayed through Engine.Serve with the chat
// sub-classes, the token saver and routes on go, one engine per sequence: the same provider requests (order, URL,
// headers, bytes), the same client answer, every usage line, the same provider.note and route.take messages, and the
// same routing state as JS after each step. A step the engine hands off (the 404 of a route with no usable target) is
// answered by JS; its messages are applied as the control plane sends them.
func TestRouteChatSequences(t *testing.T) {
	for _, seq := range loadRouteSeqs(t) {
		t.Run(seq.Name, func(t *testing.T) {
			if !strings.HasPrefix(seq.Name, "seq-chat-") {
				t.Skip("a sequence of the P4d classes: replayed by P4h Task 5")
			}
			g := newRouteRig(t, &seq, func(string) (bool, bool) { return true, false })
			sequenceEnv(t, g.clock.Load)
			for i, st := range seq.In.Steps {
				want := seq.Out.Steps[i]
				g.clock.Store(want.Now)
				g.rt.mu.Lock()
				g.rt.answers, g.rt.sent = append([]xlateProvider(nil), st.Answers...), nil
				g.rt.mu.Unlock()
				w, handed := g.serve(st.Request.Method, st.Request.Path, st.Request.Headers, st.Request.Body)
				lines, events := g.take()
				name := seq.Name + " step " + strconv.Itoa(i)
				if handed {
					// Only targetsFor's 404 is handed off in these sequences; JS answered it and told its take.
					if want.Client.Status != http.StatusNotFound || len(want.Requests) != 0 || len(lines) != 0 || len(events) != 0 {
						t.Fatalf("%s: handed off (js %d, %d lines, %d messages)", name, want.Client.Status, len(lines), len(events))
					}
					// The engine hands off targetsFor's 404 with its reason.
					req := xlateRequestOf(xlateCase{})
					req.Body = []byte(st.Request.Body)
					for k, v := range st.Request.Headers {
						req.Header.Set(k, v)
					}
					if p := g.e.State().PlanChat(req, want.Now); p.Handoff != RouteHandoff+"no usable target" {
						t.Errorf("%s: handoff %q, want the 404's", name, p.Handoff)
					}
					for _, ev := range want.Events {
						applyControl(t, g.e, ev.Type, ev.Body)
					}
				} else {
					checkRouteStep(t, name, g, w, lines, events, want.Requests, want.Client.Status, want.Client.Headers,
						want.Client.Body, want.Usage, func() []json.RawMessage {
							var raw []json.RawMessage
							for _, ev := range want.Events {
								b, _ := json.Marshal(map[string]any{"type": ev.Type, "body": ev.Body})
								raw = append(raw, b)
							}
							return raw
						}())
				}
				sameJSON(t, name+" state", g.e.Routes().Snapshot(), want.State)
				g.rt.mu.Lock()
				left := len(g.rt.answers)
				g.rt.mu.Unlock()
				if left != 0 && !handed {
					t.Errorf("%s: %d answers left unused", name, left)
				}
			}
		})
	}
}

// applyControl applies one routing message from the control plane, as the gateway's reader does.
func applyControl(t *testing.T, e *Engine, typ string, body json.RawMessage) {
	t.Helper()
	switch typ {
	case ipc.TypeProviderNote:
		var b ipc.ProviderNote
		if err := json.Unmarshal(body, &b); err != nil {
			t.Fatal(err)
		}
		e.RouteControl(&b)
	case ipc.TypeRouteTake:
		var b ipc.RouteTake
		if err := json.Unmarshal(body, &b); err != nil {
			t.Fatal(err)
		}
		e.RouteControl(&b)
	default:
		t.Fatalf("unknown routing message %q", typ)
	}
}

// checkRouteStep compares one served step with what JS did.
func checkRouteStep(t *testing.T, name string, g *routeRig, w *httptest.ResponseRecorder, lines []ipc.UsageRecord,
	events []map[string]any, reqs []struct {
		URL     string            `json:"url"`
		Method  string            `json:"method"`
		Headers map[string]string `json:"headers"`
		Body    *string           `json:"body"`
	}, status int, headers map[string]*string, body string, usage []json.RawMessage, wantEvents []json.RawMessage) {
	t.Helper()
	g.rt.mu.Lock()
	sent := append([]sentRequest(nil), g.rt.sent...)
	g.rt.mu.Unlock()
	if len(sent) != len(reqs) {
		t.Errorf("%s: %d provider requests, want %d", name, len(sent), len(reqs))
	}
	for j := 0; j < len(sent) && j < len(reqs); j++ {
		got, want := sent[j], reqs[j]
		if got.url != want.URL || got.method != want.Method || want.Body == nil || got.body != *want.Body {
			t.Errorf("%s: provider request %d\n got %s %s %s\nwant %s %s %s", name, j, got.method, got.url, got.body,
				want.Method, want.URL, ptrText(want.Body))
		}
		sameJSON(t, name+" provider headers "+strconv.Itoa(j), got.headers, mustJSON(want.Headers))
	}
	// The generator writes the runtime's text for a used body as a marker (Node and Deno differ); the engine writes
	// Deno's, the control plane's runtime.
	gotBody := strings.ReplaceAll(w.Body.String(), BodyUsedText, "<<runtime: body already used>>")
	if w.Code != status || gotBody != body {
		t.Errorf("%s: client\n got %d %s\nwant %d %s", name, w.Code, gotBody, status, body)
	}
	for k, v := range headers {
		gv := w.Header().Get(k)
		if (v == nil) != (gv == "") || (v != nil && *v != gv) {
			t.Errorf("%s: header %s: got %q, want %v", name, k, gv, ptrText(v))
		}
	}
	got := make([]any, len(lines))
	for j := range lines {
		got[j] = usageOfRecord(&lines[j])
	}
	sameJSON(t, name+" usage", got, mustJSON(usage))
	sameJSON(t, name+" events", events, mustJSON(wantEvents))
}

// The engine hands a route call to js when it does not serve it: the routes switch on js (also with no modes
// installed: routes defaults to js), a route with no usable target (targetsFor's 404), and a leg it would hand off;
// none of these takes a rotation or calls a provider.
func TestRouteHandoffs(t *testing.T) {
	var seq routeSeq
	for _, s := range loadRouteSeqs(t) {
		if s.Name == "seq-chat-rr3.json" {
			seq = s
		}
	}
	st := seq.In.Steps[0]
	body := func(model string) string { return strings.Replace(st.Request.Body, "router/rr3", model, 1) }
	for _, c := range []struct {
		name  string
		modes func(string) (bool, bool)
		model string
		edit  func(s *State)
	}{
		{"routes on js", func(sub string) (bool, bool) { return sub != RouteSwitch, false }, "router/rr3", nil},
		{"no modes installed", nil, "router/rr3", nil},
		{"no usable target", nil, "router/none", func(s *State) {
			s.Gateway.Routes = append(s.Gateway.Routes, ipc.RouteConfig{Name: "router/none", Targets: []string{"zz/m1"}})
		}},
		{"a leg without a credential", nil, "router/rr3", func(s *State) { delete(s.Secrets, "op") }},
	} {
		g := newRouteRig(t, &seq, c.modes)
		if c.modes == nil && c.name != "no modes installed" {
			g.e.SetModes(func(string) (bool, bool) { return true, false })
		}
		if c.edit != nil {
			s := *g.e.State()
			s.Gateway.Routes = append([]ipc.RouteConfig(nil), s.Gateway.Routes...)
			s.Secrets = map[string]string{}
			for k, v := range g.e.State().Secrets {
				s.Secrets[k] = v
			}
			c.edit(&s)
			g.e.SetState(&s)
		}
		_, handed := g.serve(st.Request.Method, st.Request.Path, st.Request.Headers, body(c.model))
		_, events := g.take()
		if !handed || len(g.rt.sent) != 0 || len(events) != 0 {
			t.Errorf("%s: handed %v, %d provider requests, %d messages", c.name, handed, len(g.rt.sent), len(events))
		}
		if snap := g.e.Routes().Snapshot(); len(snap.RR) != 0 {
			t.Errorf("%s: a rotation was taken: %v", c.name, snap.RR)
		}
	}
	// The plan says why: a leg's reason, prefixed.
	s := NewState(&seq.In.ConfigPush, &seq.In.SecretsPush)
	delete(s.Secrets, "op")
	r := xlateRequestOf(xlateCase{})
	r.Body = []byte(body("router/rr3"))
	for k, v := range st.Request.Headers {
		r.Header.Set(k, v)
	}
	if p := s.PlanChat(r, fixtureNow); p.Handoff != "route: provider: no credential" {
		t.Errorf("handoff %q", p.Handoff)
	}
}

// Without a fresh budget snapshot every line of a route call is marked unchecked (spec Decision 9): the fallback line
// and the answer's line alike; the 599 line too.
func TestRouteLinesUnchecked(t *testing.T) {
	var seq routeSeq
	for _, s := range loadRouteSeqs(t) {
		if s.Name == "seq-chat-fallback-500.json" {
			seq = s
		}
	}
	seq.In.Snapshot.At = seq.In.Start - 24*3600_000 // stale
	g := newRouteRig(t, &seq, func(string) (bool, bool) { return true, false })
	st := seq.In.Steps[0]
	g.rt.answers = []xlateProvider{{Throw: "ECONNREFUSED"}, st.Answers[0], st.Answers[1]}
	s := *g.e.State()
	s.Gateway.Routes = append(append([]ipc.RouteConfig(nil), s.Gateway.Routes...),
		ipc.RouteConfig{Name: "router/fb3", Targets: []string{"op/gpt-6.1", "oa/m1", "ob/m1"}})
	g.e.SetState(&s)
	w, handed := g.serve(st.Request.Method, st.Request.Path, st.Request.Headers,
		strings.Replace(st.Request.Body, "router/fb", "router/fb3", 1))
	if handed || w.Code != 200 {
		t.Fatalf("handed %v, status %d", handed, w.Code)
	}
	if len(g.lines) != 3 {
		t.Fatalf("%d lines", len(g.lines))
	}
	for i, m := range g.marks {
		if m == nil || *m != ipc.Unchecked {
			t.Errorf("line %d (status %d): budget mark %v, want unchecked", i, g.lines[i].Status, m)
		}
	}
}
