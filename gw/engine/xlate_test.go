package engine

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/front"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/guard"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"
)

// xlateProvider is the stubbed provider of one case: a whole answer, a stream of chunks (cut: it breaks off after the
// last), or a call that throws (a code, or "timeout"). nil: the case makes no provider call.
type xlateProvider struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
	Chunks  []string          `json:"chunks"`
	Cut     bool              `json:"cut"`
	Throw   string            `json:"throw"`
}

type xlateCase struct {
	Config      ipc.ConfigPush      `json:"config"`
	Secrets     ipc.SecretsHandoff  `json:"secrets"`
	Snapshot    *ipc.BudgetSnapshot `json:"snapshot"`
	FailChecks  bool                `json:"failChecks"`
	RateLimited bool                `json:"rateLimited"`
	Request     struct {
		Path    string            `json:"path"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	} `json:"request"`
	Provider *xlateProvider `json:"provider"`
}

type xlateRequest struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    *string           `json:"body"`
}

type xlateOut struct {
	Requests []xlateRequest `json:"requests"`
	Client   struct {
		Status  int                `json:"status"`
		Headers map[string]*string `json:"headers"`
		Body    string             `json:"body"`
	} `json:"client"`
	Usage json.RawMessage `json:"usage"`
}

type xlateFixture struct {
	Name string
	Fn   string
	In   xlateCase
	Out  xlateOut
}

// loadXlate reads gw/testdata/xlate (scripts/fixtures/gen-xlate.js) with the secrets put back, sorted by name.
func loadXlate(t *testing.T) []xlateFixture {
	t.Helper()
	dir := filepath.Join("..", "testdata", "xlate")
	files, err := os.ReadDir(dir)
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures in %s (run node scripts/fixtures/gen-xlate.js): %v", dir, err)
	}
	var out []xlateFixture
	for _, f := range files {
		fx, err := jsv.LoadFixture(filepath.Join(dir, f.Name()))
		if err != nil {
			t.Fatalf("%s: %v", f.Name(), err)
		}
		x := xlateFixture{Name: f.Name(), Fn: fx.Fn}
		if err := json.Unmarshal([]byte(jsv.MustStringify(fx.In[0])), &x.In); err != nil {
			t.Fatalf("%s: %v", f.Name(), err)
		}
		if err := json.Unmarshal([]byte(jsv.MustStringify(fx.Out)), &x.Out); err != nil {
			t.Fatalf("%s: %v", f.Name(), err)
		}
		out = append(out, x)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// counterEnv is the generator's determinism (scripts/fixtures/lib.js): Random gives n bytes equal to a counter that
// starts at 1 per case, Now the fixed clock.
func counterEnv() *translate.Env {
	n := 0
	return &translate.Env{
		Now: func() int64 { return fixtureNow },
		Random: func(k int) []byte {
			n++
			b := make([]byte, k)
			for i := range b {
				b[i] = byte(n)
			}
			return b
		},
	}
}

// xlatePorted replays the cases of one fixture function; a function not in it is not ported yet.
var xlatePorted = map[string]func(t *testing.T, f xlateFixture){}

// Every case of gw/testdata/xlate (the JS gateway's own answers): the engine plans it without a hand-off, sends the
// provider the same request, and gives the client the same status, headers and bytes and the same usage line.
func TestXlateFixtures(t *testing.T) {
	for _, f := range loadXlate(t) {
		t.Run(f.Name, func(t *testing.T) {
			run, ok := xlatePorted[f.Fn]
			if !ok {
				t.Skipf("%s: not ported yet", f.Fn)
			}
			run(t, f)
		})
	}
}

// chunkReader gives one chunk per Read, then io.EOF, or ErrCut when the provider breaks off.
type chunkReader struct {
	chunks [][]byte
	cut    bool
}

func (c *chunkReader) Read(p []byte) (int, error) {
	if len(c.chunks) == 0 {
		if c.cut {
			return 0, ErrCut
		}
		return 0, io.EOF
	}
	n := copy(p, c.chunks[0])
	if n == len(c.chunks[0]) {
		c.chunks = c.chunks[1:]
	} else {
		c.chunks[0] = c.chunks[0][n:]
	}
	return n, nil
}

// providerOf is the fixture's provider as a call result.
func providerOf(c xlateCase, wrap func(io.Reader) io.Reader) ProviderResult {
	pr := c.Provider
	if pr == nil {
		return ProviderResult{Why: "no provider in this case"}
	}
	if pr.Throw != "" {
		why := pr.Throw
		if why == "timeout" {
			why = "timed out"
		}
		return ProviderResult{Why: why}
	}
	h := http.Header{}
	for k, v := range pr.Headers {
		h.Set(k, v)
	}
	var r io.Reader = strings.NewReader(pr.Body)
	if pr.Chunks != nil {
		cr := &chunkReader{cut: pr.Cut}
		for _, ch := range pr.Chunks {
			cr.chunks = append(cr.chunks, []byte(ch))
		}
		r = cr
	}
	if wrap != nil {
		r = wrap(r)
	}
	return ProviderResult{Status: pr.Status, Header: h, Body: io.NopCloser(r)}
}

// recordWriter keeps what Deliver writes.
type recordWriter struct {
	status int
	header http.Header
	body   strings.Builder
	heads  int
}

func (w *recordWriter) Head(status int, h http.Header) error {
	w.status, w.header = status, h
	w.heads++
	return nil
}
func (w *recordWriter) Write(b []byte) error { w.body.Write(b); return nil }
func (w *recordWriter) Pinger(func() string) {}

// xlateRequestOf is the fixture's client request, from a loopback client to a loopback Host.
func xlateRequestOf(c xlateCase) *Request {
	h := http.Header{}
	for k, v := range c.Request.Headers {
		h.Set(k, v)
	}
	raw, q, _ := strings.Cut(c.Request.Path, "?")
	return &Request{Method: "POST", RawPath: raw, Query: q, Header: h, Host: "127.0.0.1:4777", Peer: "127.0.0.1:50000",
		Body: []byte(c.Request.Body)}
}

func xlateStateOf(t *testing.T, c xlateCase) *State {
	t.Helper()
	b, _ := json.Marshal(c) // a copy of its own: a test that edits the state must not change the next one's
	var cp xlateCase
	if err := json.Unmarshal(b, &cp); err != nil {
		t.Fatal(err)
	}
	cp.Config.Gen, cp.Secrets.Gen = 1, 1
	s := NewState(&cp.Config, &cp.Secrets)
	if cp.Snapshot != nil {
		s = s.WithSnapshot(cp.Snapshot)
	}
	return s
}

// usageOfRecord is a line as the generator writes it: no ts or ms, ttft "absent", null or "set".
func usageOfRecord(rec *ipc.UsageRecord) any {
	if rec == nil {
		return nil
	}
	var m map[string]any
	b, _ := json.Marshal(rec)
	_ = json.Unmarshal(b, &m)
	delete(m, "ts")
	delete(m, "ms")
	switch v, ok := m["ttft"]; {
	case !ok:
		m["ttft"] = "absent"
	case v != nil:
		m["ttft"] = "set"
	}
	return m
}

// planXlate plans a fixture's request with the generator's determinism; it fails the test on a hand-off.
func planXlate(t *testing.T, f xlateFixture) (*State, Plan) {
	t.Helper()
	s := xlateStateOf(t, f.In)
	r := xlateRequestOf(f.In)
	p := s.PlanFor(front.Classify(r.RawPath), r, fixtureNow)
	if p.Handoff != "" {
		t.Fatalf("handed off: %s", p.Handoff)
	}
	p.Env = counterEnv()
	return s, p
}

// refusalOf is the answer the engine gives before any provider call (the rate limit, its own answer, the budget, the
// guardrails), or nil when the call goes to the provider.
func refusalOf(t *testing.T, s *State, p *Plan, c xlateCase) *Answer {
	t.Helper()
	if c.RateLimited {
		l := NewRateLimiter()
		l.Take(p.Key.ID, p.Key.RPM, fixtureNow)
		a := RateLimited(p.Key, l.Take(p.Key.ID, p.Key.RPM, fixtureNow), p.Shape)
		return &a
	}
	if p.Upstream == UpNone {
		return p.Self
	}
	if p.Upstream != UpCount {
		if ref, _ := s.Budget(p, fixtureNow, ipc.StaleAfterMs, nil); ref != nil {
			return ref
		}
	}
	was := checkBody
	defer func() { checkBody = was }()
	if c.FailChecks {
		checkBody = func(jsv.Value, guard.Policy, string) (guard.Result, error) {
			return guard.Result{}, errors.New("a check failed")
		}
	}
	return s.Guard(p, 1)
}

// runXlate replays one fixture: the refusal or the provider request, then the client's answer and the line.
func runXlate(t *testing.T, f xlateFixture) {
	got := deliverXlate(t, f, nil)
	want := f.Out
	if got.status != want.Client.Status || got.body.String() != want.Client.Body {
		t.Errorf("client\n got %d %s\nwant %d %s", got.status, got.body.String(), want.Client.Status, want.Client.Body)
	}
	for k, v := range want.Client.Headers {
		g := got.header.Get(k)
		if (v == nil) != (g == "") || (v != nil && *v != g) {
			t.Errorf("header %s: got %q, want %v", k, g, ptrText(v))
		}
	}
}

func ptrText(v *string) string {
	if v == nil {
		return "absent"
	}
	return strconv.Quote(*v)
}

// deliverXlate plans and answers a fixture, checking the provider request and the usage line; the client's answer is
// returned for the caller to compare.
func deliverXlate(t *testing.T, f xlateFixture, wrap func(io.Reader) io.Reader) *recordWriter {
	t.Helper()
	s, p := planXlate(t, f)
	w := &recordWriter{}
	var line *ipc.UsageRecord
	if a := refusalOf(t, s, &p, f.In); a != nil {
		if len(f.Out.Requests) > 0 {
			t.Fatalf("the engine refused (%d) where the JS called the provider", a.Status)
		}
		line = writeAnswer(w, *a)
	} else {
		if len(f.Out.Requests) != 1 {
			t.Fatalf("the engine would call the provider; the JS made %d calls", len(f.Out.Requests))
		}
		want := f.Out.Requests[0]
		if p.URL != want.URL || want.Body == nil || string(p.Body) != *want.Body {
			t.Errorf("provider request\n got %s %s\nwant %s %v", p.URL, p.Body, want.URL, ptrText(want.Body))
		}
		gotH := map[string]string{}
		for k := range p.Header {
			gotH[strings.ToLower(k)] = p.Header.Get(k)
		}
		sameJSON(t, "provider headers", gotH, mustJSON(want.Headers))
		line = s.Deliver(&p, providerOf(f.In, wrap), func() float64 { return 1 }, w)
	}
	sameJSON(t, "usage", usageOfRecord(line), f.Out.Usage)
	if w.heads != 1 {
		t.Errorf("%d heads written", w.heads)
	}
	return w
}

func init() {
	xlatePorted["chatAnthropic"] = runXlate
}
