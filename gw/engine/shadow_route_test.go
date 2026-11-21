package engine

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/front"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"
)

// shadowSeq is gw/testdata/router/shadow-*.json (scripts/fixtures/gen-router.js): the sequence seq-* again with every
// call shadowed, and the capture lib/gateway/shadow.js kept of each.
type shadowSeq struct {
	In struct {
		Seq string `json:"seq"`
	} `json:"in"`
	Out struct {
		Steps []struct {
			Capture json.RawMessage `json:"capture"`
		} `json:"steps"`
	} `json:"out"`
}

// loadShadowSeq reads the captures of sequence seq with the secrets put back (as loadRouteSeqs does).
func loadShadowSeq(t *testing.T, seq string) shadowSeq {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "testdata", "router", strings.Replace(seq, "seq-", "shadow-", 1)))
	if err != nil {
		t.Fatalf("%v (run node scripts/fixtures/gen-router.js)", err)
	}
	var head struct {
		Secrets []string `json:"secrets"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		t.Fatal(err)
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
	var out shadowSeq
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatal(err)
	}
	if out.In.Seq+".json" != seq {
		t.Fatalf("%s: captures of %s", seq, out.In.Seq)
	}
	return out
}

// routeCaptureOf decodes a step's capture, the generator's used-body marker replaced by the runtime text given.
func routeCaptureOf(t *testing.T, raw json.RawMessage, bodyUsed string) *Capture {
	t.Helper()
	if string(raw) == "null" {
		return nil
	}
	c := &Capture{}
	if err := json.Unmarshal(raw, c); err != nil {
		t.Fatal(err)
	}
	if c.Client != nil {
		c.Client.Body = strings.ReplaceAll(c.Client.Body, "<<runtime: body already used>>", bodyUsed)
	}
	return c
}

// routeStep is one route call of a sequence, planned as the shadow plans it, with JS's capture of it.
type routeStep struct {
	name string
	sub  string
	s    *State
	plan func() *Plan
	cap  func(bodyUsed string) *Capture
	now  int64
}

// routeSteps gives every route call of every sequence the engine would compare (not handed off).
func routeSteps(t *testing.T) []routeStep {
	t.Helper()
	var out []routeStep
	for _, seq := range loadRouteSeqs(t) {
		sh := loadShadowSeq(t, seq.Name)
		if len(sh.Out.Steps) != len(seq.In.Steps) {
			t.Fatalf("%s: %d captures for %d steps", seq.Name, len(sh.Out.Steps), len(seq.In.Steps))
		}
		g := newRouteRig(t, &seq, nil)
		s := g.e.State()
		for i, st := range seq.In.Steps {
			now := seq.Out.Steps[i].Now
			req := xlateRequestOf(xlateCase{})
			req.Body = []byte(st.Request.Body)
			req.RawPath = front.RawPath(st.Request.Path)
			for k, v := range st.Request.Headers {
				req.Header.Set(k, v)
			}
			class := front.Classify(req.RawPath)
			probe := s.PlanFor(class, req, now)
			if probe.Route == nil || probe.Handoff != "" {
				continue
			}
			raw := sh.Out.Steps[i].Capture
			out = append(out, routeStep{
				name: seq.Name + " step " + strconv.Itoa(i),
				sub:  SubClass(class, &probe, req),
				s:    s,
				plan: func() *Plan { p := s.PlanFor(class, req, now); return &p },
				cap:  func(bodyUsed string) *Capture { return routeCaptureOf(t, raw, bodyUsed) },
				now:  now,
			})
		}
	}
	return out
}

// Every route call JS answered in the sequences, captured as the control plane captures a shadowed call, is compared by
// the engine with no diff (P4h spec Decision 11): JS's orders recomputed from its tape, the legs replayed in JS's order
// against its provider answers (a call that threw included), every provider request, the client's answer and every
// line. The used-body text of the fallback-then-throw quirk is either runtime's (spec Decision 15f).
func TestShadowRouteSequencesSame(t *testing.T) {
	steps := routeSteps(t)
	if len(steps) < 40 {
		t.Fatalf("only %d route calls in the sequences", len(steps))
	}
	sequenceEnv(t, func() int64 { return 0 })
	for _, st := range steps {
		for _, used := range []string{BodyUsedText, NodeBodyUsedText} {
			p, c := st.plan(), st.cap(used)
			l := st.s.compareCapturedAt(st.sub, p, c, StaleMs, func() int64 { return st.now })
			if l.Outcome != "same" || !l.Route {
				b, _ := json.Marshal(l)
				t.Errorf("%s (%s): %s", st.name, used, b)
			}
		}
	}
}

// A deliberate one-byte change in JS's fallback line is a diff (lines.<i>.error), as is an order JS gave that its own
// counter does not give (route.order), a used-body text of neither runtime (client.body), and a provider request JS
// sent that the replay does not send (providerRequests).
func TestShadowRouteDiffs(t *testing.T) {
	steps := routeSteps(t)
	sequenceEnv(t, func() int64 { return 0 })
	find := func(name string) routeStep {
		for _, st := range steps {
			if st.name == name {
				return st
			}
		}
		t.Fatalf("no step %s", name)
		return routeStep{}
	}
	run := func(st routeStep, edit func(c *Capture)) ShadowLine {
		p, c := st.plan(), st.cap(BodyUsedText)
		edit(c)
		return st.s.compareCapturedAt(st.sub, p, c, StaleMs, func() int64 { return st.now })
	}
	has := func(l ShadowLine, field string) bool {
		return l.Outcome == "diff" && slices.ContainsFunc(l.Diffs, func(d Diff) bool { return d.Field == field })
	}

	fb := find("seq-chat-fallback-429.json step 0")
	l := run(fb, func(c *Capture) {
		e := c.Lines[0]["error"].(string)
		c.Lines[0]["error"] = e[:len(e)-1] + "T" // "fell back to the next route targeT"
	})
	if !has(l, "lines.0.error") || len(l.Diffs) != 1 {
		t.Errorf("a one-byte change in the fallback line: %+v", l)
	}
	l = run(fb, func(c *Capture) { c.Lines[0]["routedTo"] = "ob/m1" })
	if !has(l, "lines.0.routedTo") {
		t.Errorf("the fallback line's routedTo: %+v", l)
	}
	l = run(fb, func(c *Capture) { c.Lines = c.Lines[1:] })
	if !has(l, "lines") {
		t.Errorf("a line JS did not write: %+v", l)
	}

	rr := find("seq-chat-rr3.json step 1")
	l = run(rr, func(c *Capture) { c.Routes[0].N = (c.Routes[0].N + 1) % 3 })
	if !has(l, "route.order") || len(l.Diffs) != 1 {
		t.Errorf("an order the counter does not give: %+v", l)
	}
	l = run(rr, func(c *Capture) { c.Routes = append(c.Routes, c.Routes[0]) })
	if !has(l, "route.takes") {
		t.Errorf("a second take: %+v", l)
	}

	quirk := find("seq-chat-fallback-then-throw.json step 0")
	l = run(quirk, func(c *Capture) {
		c.Client.Body = strings.Replace(c.Client.Body, BodyUsedText, "Body used", 1)
	})
	if !has(l, "client.body") {
		t.Errorf("a used-body text of neither runtime: %+v", l)
	}
	l = run(quirk, func(c *Capture) { c.Upstream[1] = ProviderAnswer{Thrown: "ob: ECONNRESET"} })
	if !has(l, "lines.1.error") {
		t.Errorf("a different failure replayed: %+v", l)
	}

	// A leg whose token fetch threw in the control plane: each engine holds its own token.
	l = run(quirk, func(c *Capture) { c.Upstream[1] = ProviderAnswer{Thrown: "ob: token refresh failed", Token: true} })
	if l.Outcome != "skipped" || l.Reason != authOwnFailure || !l.Route {
		t.Errorf("a token failure: %+v", l)
	}
	// No route take in the capture: JS answered before resolveAll.
	l = run(fb, func(c *Capture) { c.Routes, c.Client.Status = nil, http.StatusTooManyRequests })
	if l.Outcome != "skipped" {
		t.Errorf("JS's rate limit: %+v", l)
	}
	l = run(fb, func(c *Capture) { c.Routes = nil })
	if !has(l, "served") {
		t.Errorf("no take: %+v", l)
	}
}

// The engine's own view of a route at plan time (routeView, spec Decision 11): its counter and cooling set, read with
// Peek (no take), against JS's first resolveAll.
func TestShadowRouteView(t *testing.T) {
	steps := routeSteps(t)
	var st routeStep
	for _, x := range steps {
		if x.name == "seq-chat-rr3.json step 1" {
			st = x
		}
	}
	c := st.cap(BodyUsedText)
	v := &routeView{n: c.Routes[0].N, cooling: slices.Clone(c.Routes[0].Cooling)}
	if got := v.against(c); got != "same" {
		t.Fatalf("view %s", got)
	}
	v.n++
	if got := v.against(c); got != "differs" {
		t.Fatalf("view %s", got)
	}
	v.n--
	v.cooling = append(v.cooling, "zz")
	if got := v.against(c); got != "differs" {
		t.Fatalf("view %s", got)
	}
	if got := v.against(&Capture{}); got != "" {
		t.Fatalf("no tape: %q", got)
	}
}

// Through Engine.Shadow: a shadowed route call is forwarded, the engine takes no rotation and notes nothing for it (JS
// takes and tells; spec Decision 3), compares JS's capture and posts a line with route and routeView.
func TestShadowRouteCallEndToEnd(t *testing.T) {
	var seq routeSeq
	for _, s := range loadRouteSeqs(t) {
		if s.Name == "seq-chat-rr3.json" {
			seq = s
		}
	}
	sh := loadShadowSeq(t, seq.Name)
	g := newRouteRig(t, &seq, func(sub string) (bool, bool) { return false, sub == RouteSwitch })
	g.clock.Store(seq.Out.Steps[0].Now)
	before := g.e.Routes().Snapshot()
	lines := make(chan ShadowLine, 1)
	own := func(method, path string, body []byte) (int, []byte, error) {
		if method == http.MethodGet {
			return http.StatusOK, sh.Out.Steps[0].Capture, nil
		}
		var l ShadowLine
		_ = json.Unmarshal(body, &l)
		lines <- l
		return http.StatusNoContent, nil, nil
	}
	st := seq.In.Steps[0]
	r := httptest.NewRequest(st.Request.Method, st.Request.Path, strings.NewReader(st.Request.Body))
	for k, v := range st.Request.Headers {
		r.Header.Set(k, v)
	}
	r.Host, r.RemoteAddr = "127.0.0.1:4777", "127.0.0.1:50000"
	w := httptest.NewRecorder()
	js := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "js") })
	g.e.Shadow(front.Classify(front.RawPath(r.RequestURI)), w, r, front.RawPath(r.RequestURI), js, own)
	select {
	case l := <-lines:
		if l.Outcome != "same" || !l.Route || l.RouteView != "same" || l.Class != "chat" {
			t.Fatalf("line %+v", l)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no shadow line")
	}
	if w.Body.String() != "js" {
		t.Fatalf("answered %q", w.Body.String())
	}
	sameJSON(t, "the engine's route state", g.e.Routes().Snapshot(), mustJSON(before))
	if _, ev := g.take(); len(ev) != 0 || len(g.rt.sent) != 0 {
		t.Fatalf("the shadow took or noted (%d messages) or called a provider (%d)", len(ev), len(g.rt.sent))
	}
}
