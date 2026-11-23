package engine

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/guard"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

// Engine serves the route classes on the go engine. Its State is swapped whole on each generation and budget snapshot.
type Engine struct {
	state    atomic.Pointer[State]
	limits   *RateLimiter
	upstream *Upstream
	emit     func(*ipc.UsageReport) (int64, error)
	now      func() int64
	logf     func(string, ...any)
	modes    atomic.Pointer[func(sub string) (onGo, shadowed bool)]
	own      atomic.Pointer[func(method, path string, body []byte) (int, []byte, error)]
	// the control plane's tool endpoint (P4g, serve_mcp.go)
	tools    atomic.Pointer[ToolCall]
	streams  sync.WaitGroup // streams being served, until their usage line is handed to emit
	closeMu  sync.Mutex     // closing and streams.Add: no Add once Wait has begun (final review m5)
	closing  bool
	failMu   sync.Mutex
	failAt   map[string]time.Time // when each check's log-mode failure was last logged
	spend    *Spend               // the engine's own lines since the newest snapshot (P4e)
	staleMs  int64                // the age at which a budget snapshot no longer checks a call
	calls    Calls                // the call journal (P4f D14); nil: none
	emitCall func(r *ipc.UsageReport, call int64) (int64, error)
	emitSpan func(*ipc.TraceSpanBody) error // the trace spans (P4f D1); nil: none
	ids      SpanIDs
	onTake   func(keyID string, at int64) // a request admitted under a key's window, for the control plane (P4f D16)
	tokens   *Tokens                      // the provider token registry (P4g Decision 10), outside the generations
	// routes is the routing state (P4h design D1): rotation counters and cooldowns, outside the generations (a config
	// push keeps it, as JS keeps its maps), replicated both ways with the control plane (Decision 3).
	routes *RouteState
}

// Routes is the engine's routing state.
func (e *Engine) Routes() *RouteState { return e.routes }

// SetRouteSend gives the engine where to tell each provider answer it reads (provider.note) and each rotation it takes
// (route.take), for the control plane (P4h Decision 3). send runs under the route state's mutex: it must not block
// (RouteState). Call it before serving.
func (e *Engine) SetRouteSend(send func(typ string, body any)) { e.routes.SetSend(send) }

// RouteControl applies a routing message from the control plane (P4h Decision 3): a provider answer it read, a rotation
// it took (with this engine's target count of the route, Decision 4; nothing when the route is not a round-robin of
// more than one target here), or the seed of a new session. A remote note or take is never sent back.
func (e *Engine) RouteControl(body any) {
	switch b := body.(type) {
	case *ipc.ProviderNote:
		h := make(http.Header, len(b.Headers))
		for k, v := range b.Headers {
			h.Set(k, v)
		}
		e.routes.Note(b.ProviderID, b.Status, h, b.At, true)
	case *ipc.RouteTake:
		s := e.State()
		if s == nil {
			return
		}
		for _, rt := range s.Gateway.Routes {
			if rt.Name == b.Name {
				if rt.Strategy != nil && *rt.Strategy == "round-robin" {
					e.routes.Take(rt.Name, len(rt.Targets), b.At, true)
				}
				return
			}
		}
	case *ipc.RouteState:
		e.routes.Seed(*b)
	}
}

// SetOnTake gives the engine where to tell each request it admits under a key's window (P4f D16); call it before
// serving.
func (e *Engine) SetOnTake(f func(keyID string, at int64)) { e.onTake = f }

// NoteTake counts a request the control plane admitted under key id's window (P4f D16).
func (e *Engine) NoteTake(id string, at int64) { e.limits.Note(id, at) }

// SeedWindows replaces the windows with the control plane's (a new session, before ready; P4f D16).
func (e *Engine) SeedWindows(ws []ipc.RateWindow) {
	for _, w := range ws {
		e.limits.Seed(w.KeyID, w.At)
	}
}

// SetSpanEmitter gives the engine the outbox for its trace spans (P4f D1); call it before serving.
func (e *Engine) SetSpanEmitter(f func(*ipc.TraceSpanBody) error) { e.emitSpan = f }

// SetIDs replaces the span id source (tests).
func (e *Engine) SetIDs(ids SpanIDs) { e.ids = ids }

// callTrace is one traced request: its trace context and what the server span reads of it, on the heap and only when
// traced, so a held stream's frame stays as small as before P4f (the per-stream memory budget, P4c Decision 11).
type callTrace struct {
	tc    TraceCtx
	facts reqFacts
	prior []priorLine // a route call's lines before the one that ends it (P4h Decision 10)
}

// priorLine is a route leg's line written before the call ended (a 599, a fallback): its client span is emitted with the
// call's, after the server span, in the order the lines were written, as lib/otel.js finishes the pending spans.
type priorLine struct {
	rec  ipc.UsageRecord
	prov *ipc.ProviderSpan
}

// trace starts a request's trace context when the engine answers it and a span is recorded somewhere (config.push
// trace.spans, P4f D8); nil: no span and no trace id on the line (tracing off, or an unsampled traceparent).
func (e *Engine) trace(s *State, r *http.Request, path string) *callTrace {
	if !s.Spans() || e.emitSpan == nil {
		return nil
	}
	tc := StartTrace(r.Header, e.ids)
	if !tc.Sampled {
		return nil
	}
	return &callTrace{tc: tc, facts: factsOf(r, path)}
}

// reqFacts is what the server span reads of the request, copied before the handler returns (net/http may reuse r).
type reqFacts struct {
	method, path, proto, ua string
	tls                     bool
}

func factsOf(r *http.Request, path string) reqFacts {
	f := reqFacts{method: r.Method, path: path, proto: strconv.Itoa(r.ProtoMajor) + "." + strconv.Itoa(r.ProtoMinor),
		tls: r.TLS != nil}
	if v := r.Header.Values("User-Agent"); len(v) > 0 {
		f.ua = v[0] // Node keeps the first User-Agent
	}
	return f
}

// spans emits a request's server span and, for a usage line, its client span (P4f D1), as lib/otel.js records them
// when the response ends: the server span first (a chat call's client span waits for the answer's id and finish
// reasons), each through the outbox after the usage line. A route call's earlier lines (t.prior) get theirs first, in
// order, then the last line's (P4h Decision 10): one client span per usage line, as otel.js makes one per record.
func (e *Engine) spans(s *State, t *callTrace, status int, started, ended time.Time, rec *ipc.UsageRecord,
	prov *ipc.ProviderSpan, obs *Observer) {
	tc, f := t.tc, t.facts
	out := []ipc.TraceSpanBody{ServerSpan(tc, ServerFacts{Method: f.method, Path: f.path, Proto: f.proto, TLS: f.tls,
		UserAgent: f.ua, Status: status, StartUs: started.UnixMicro(), EndUs: ended.UnixMicro()})}
	var o *Observed
	if obs != nil && (rec != nil || len(t.prior) > 0) {
		o = obs.Result()
	}
	for i := range t.prior {
		out = append(out, ClientSpan(tc, e.ids(8), &t.prior[i].rec, t.prior[i].prov, o, s.mask))
	}
	if rec != nil {
		out = append(out, ClientSpan(tc, e.ids(8), rec, prov, o, s.mask))
	}
	for i := range out {
		if err := e.emitSpan(&out[i]); err != nil {
			e.logf("go engine: a trace span could not be stored: %v", err)
		}
	}
}

func provSpan(p *Plan) *ipc.ProviderSpan {
	if p.Target == nil || p.Target.Provider == nil {
		return nil
	}
	return p.Target.Provider.Span
}

// Calls is the call journal (gw/ipc CallJournal, P4f D14): a served call is opened before its provider call, and its
// usage line is emitted with its id, so the outbox notes the line's seq first and a gateway that dies during the call
// leaves exactly one line for it (written at the next start).
type Calls interface {
	Open(ts int64, base ipc.UsageRecord) int64
	End(id int64) // the call ends with no usage line
}

// SetCalls gives the engine the call journal and the emit that notes a call's close under the outbox's send lock;
// call it before serving.
func (e *Engine) SetCalls(c Calls, emit func(r *ipc.UsageReport, call int64) (int64, error)) {
	e.calls, e.emitCall = c, emit
}

// open records a call about to reach its provider (0: no journal, or it could not be written).
func (e *Engine) open(p *Plan) int64 {
	if e.calls == nil || e.emitCall == nil {
		return 0
	}
	base := p.Base
	if !p.keepSaved {
		base.SavedTokens = nil // a recovered 599 line carries no savedTokens, as Failed's (rtk spec D12)
	}
	return e.calls.Open(e.now(), base)
}

// New is an engine that calls providers through up and hands each usage line to emit (the outbox, then the control
// plane: lib/ipc/ingest.js stores it through the JS gateway's own writer).
func New(up *Upstream, emit func(*ipc.UsageReport) (int64, error), logf func(string, ...any)) *Engine {
	e := &Engine{limits: NewRateLimiter(), upstream: up, emit: emit, now: NowMs, logf: logf,
		failAt: map[string]time.Time{}, spend: &Spend{}, staleMs: StaleMs, ids: RandomIDs, routes: NewRouteState(nil)}
	e.tokens = NewTokens(up.Client(), e.State, nil)
	// Every provider answer the engine reads is noted at header time (P4h Decisions 3, 5): its cooldown here, and a
	// provider.note for the control plane's cooldowns and quota readings, whatever serves the call's class.
	up.SetOnAnswer(func(p *Plan, status int, h http.Header) {
		if p.Target != nil && p.Target.Provider != nil {
			e.routes.Note(p.Target.Provider.ID, status, h, e.now(), false)
		}
	})
	return e
}

// Tokens is the engine's provider token registry; the gateway runs its background refresh (Tokens.Run).
func (e *Engine) Tokens() *Tokens { return e.tokens }

// SetStaleMs sets the age at which a budget snapshot is stale (--stale-ms); call it before serving.
func (e *Engine) SetStaleMs(ms int64) { e.staleMs = ms }

// SetState installs a generation (nil: none yet; every request is handed to the control plane).
func (e *Engine) SetState(s *State) {
	if s != nil {
		s.logf = e.logf     // the generation logs recovered panics through the engine's log (P4d re-review 3)
		s.tokens = e.tokens // token-auth targets get their source from the engine's registry (P4g)
	}
	e.state.Store(s)
}

// State is the current generation, or nil.
func (e *Engine) State() *State { return e.state.Load() }

// streamFeedHook, when set (tests only), runs before each read is rewritten: a panic there shows the stream's usage
// line is still written once.
var streamFeedHook func()

// StreamsBody is whether chatCompletion streams a provider answer of this status: a 2xx with a body. fetch gives no
// body for 204 and 205, and chatCompletion then answers whole (`!r.body`; final review m3).
func StreamsBody(status int) bool {
	return status >= 200 && status <= 299 && status != http.StatusNoContent && status != http.StatusResetContent
}

// StreamReadSize is how much of the provider's stream is read at once (P4c Decision 11; 4 KiB, the size of the
// transport's own read buffer, after the memory gate of 2026-11-13).
const StreamReadSize = 4 << 10

// SetOwn gives the engine the front's call to the control plane's internal port (the shadow's capture and line) for
// requests it reads in Serve but shadows (P4c: chat on go, chat-stream shadowed).
func (e *Engine) SetOwn(own func(method, path string, body []byte) (int, []byte, error)) {
	e.own.Store(&own)
}

func (e *Engine) ownFn() func(method, path string, body []byte) (int, []byte, error) {
	if f := e.own.Load(); f != nil {
		return *f
	}
	return nil
}

// SetModes tells the engine, per sub-class ("chat", "chat-stream"), whether it serves it and whether it shadows it
// (the front's engines and shadow classes; Task 3).
func (e *Engine) SetModes(f func(sub string) (onGo, shadowed bool)) { e.modes.Store(&f) }

func (e *Engine) mode(sub string) (bool, bool) {
	if f := e.modes.Load(); f != nil {
		return (*f)(sub)
	}
	return sub == "chat", false // P4b: chat on go, streams handed off
}

// track counts a stream about to be served, unless the shutdown already waits for the streams (false: hand it off).
func (e *Engine) track() bool {
	e.closeMu.Lock()
	defer e.closeMu.Unlock()
	if e.closing {
		return false
	}
	e.streams.Add(1)
	return true
}

// Wait waits at most d for the streams in flight and their usage lines (the gateway's shutdown, before the outbox
// closes); false when d passed first.
func (e *Engine) Wait(d time.Duration) bool {
	e.closeMu.Lock()
	e.closing = true // from now on a new stream goes to the control plane (track)
	e.closeMu.Unlock()
	done := make(chan struct{})
	go func() { e.streams.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// ReadRequest reads what the engine needs of r: at most MaxBody + 1 bytes of its body. The body r keeps is the whole
// one again (what was read, then the rest), so a hand-off forwards it unchanged.
//
// A body read to its end is given again from buf alone, never from r's own body: net/http closes that body as the
// handler's answer starts, and a hand-off's proxy transport reads its body once more after the last byte to find the
// end. On the closed body that read failed ("invalid Read on closed Body"), the transport closed the control plane's
// connection mid-answer and cut the client's stream (a shadowed stream was then filed as a client that left).
func ReadRequest(r *http.Request, rawPath string) *Request {
	buf, _ := io.ReadAll(io.LimitReader(r.Body, MaxBody+1))
	body := io.Reader(bytes.NewReader(buf))
	if len(buf) > MaxBody { // the rest is still on the wire; with a known length, nothing is read past its end
		rest := io.Reader(r.Body)
		if r.ContentLength > 0 {
			rest = io.LimitReader(r.Body, r.ContentLength-int64(len(buf)))
		}
		body = io.MultiReader(body, rest)
	}
	r.Body = struct {
		io.Reader
		io.Closer
	}{body, r.Body}
	q := ""
	if i := len(rawPath); i < len(r.RequestURI) && r.RequestURI[i] == '?' {
		q = r.RequestURI[i+1:]
	}
	return &Request{Method: r.Method, RawPath: rawPath, Query: q, Header: r.Header, Host: r.Host, Peer: r.RemoteAddr,
		Body: buf, TooBig: len(buf) > MaxBody}
}

// Serve answers one request of a class the go engine has (chat, and the P4d classes messages, responses, gemini and
// embeddings), or hands it to js (the control plane) unchanged.
func (e *Engine) Serve(class string, w http.ResponseWriter, r *http.Request, rawPath string, js http.Handler) {
	s := e.state.Load()
	if s == nil || Subs[class] == nil {
		js.ServeHTTP(w, r)
		return
	}
	req := ReadRequest(r, rawPath)
	started := time.Now()
	p := s.PlanFor(class, req, e.now())
	// The sub-class (P4c, P4d): each has its own engine entry.
	sub := SubClass(class, &p, req)
	onGo, shadowed := e.mode(sub)
	if p.saver != nil { // a call the token saver looks at also needs its switch (spec D13, owner answer 22)
		g, sh := e.mode(SaverSwitch)
		onGo, shadowed = onGo && g, shadowed || sh
	}
	if p.TokenAuth() { // a call to a token-auth target also needs the provider-auth switch (P4g spec Decision 14)
		g, sh := e.mode(AuthSwitch)
		onGo, shadowed = onGo && g, shadowed || sh
	}
	if p.Route != nil { // a route call also needs the routes switch (P4h spec Decision 2)
		g, sh := e.mode(RouteSwitch)
		onGo, shadowed = onGo && g, shadowed || sh
	}
	if p.Handoff != "" && onGo {
		js.ServeHTTP(w, r)
		return
	}
	if !onGo {
		// Its sub-class is on js: forwarded, compared when shadowed (a hand-off gets its line there; final review m2).
		if shadowed {
			e.shadowRead(sub, w, r, s, &p, js, e.ownFn())
			return
		}
		js.ServeHTTP(w, r)
		return
	}
	// The token saver runs before the rate limit takes the call, so that its hand-off is exact (spec D9, D11). A key
	// already at its limit is answered 429 below without the saver's work, as JS refuses it before saveTokens.
	if p.saver != nil && e.limits.Wait(p.Key.ID, p.Key.RPM, e.now()) == 0 {
		if why := s.Save(&p, time.Now()); why != "" {
			e.logSaverPanic(&p)
			js.ServeHTTP(w, r)
			return
		}
	}
	tr := e.trace(s, r, rawPath)
	takeAt := e.now()
	if wait := e.limits.Take(p.Key.ID, p.Key.RPM, takeAt); wait > 0 {
		a := RateLimited(p.Key, wait, p.Shape)
		write(w, a)
		if tr != nil && p.Upstream != UpTool { // /mcp is no model endpoint: no span without a line (serve_mcp.go)
			flushAnswer(w)
			e.spans(s, tr, a.Status, started, time.Now(), nil, nil, nil) // no usage line: the server span only
		}
		return
	}
	if e.onTake != nil {
		e.onTake(p.Key.ID, takeAt) // the control plane's copy of the window (P4f D16)
	}
	// The window freed between the look above and the take: the saver runs now. A hand-off here is rare and leaves
	// this take counted (JS counts the call once more).
	if why := s.Save(&p, time.Now()); why != "" {
		e.logSaverPanic(&p)
		js.ServeHTTP(w, r)
		return
	}
	if p.Route != nil { // a route call: the take, the order, the filters and the fallback loop (serve_route.go)
		e.serveRoute(w, r, s, &p, req, started, tr, js)
		return
	}
	if p.Upstream == UpTool { // /mcp tools/call (P4g): no budget, the tool run by the control plane
		e.serveMCP(w, r, s, &p, started, tr)
		return
	}
	if p.Upstream == UpNone { // an answer of the engine's own (a count estimate, P4d): no budget, no line
		e.answerOwn(w, r, s, *p.Self, started, tr)
		return
	}
	// The budget (P4e Decision 13): the price rule, the snapshot, and the engine's own spend since it. count_tokens
	// checks none (P4d).
	var ref *Answer
	var unchecked bool
	if p.Upstream != UpCount {
		ref, unchecked = s.Budget(&p, e.now(), e.staleMs, e.spend.Live())
	}
	if ref != nil {
		if r.Context().Err() == nil {
			write(w, *ref) // allowedTargets throws before any record: no line
		}
		if tr != nil {
			flushAnswer(w)
			e.spans(s, tr, ref.Status, started, time.Now(), nil, nil, nil)
		}
		return
	}
	p.Unchecked = unchecked
	elapsed := func() float64 { return float64(time.Since(started).Microseconds()) / 1000 }
	if p.Policy != nil {
		if a := s.Guard(&p, elapsed()); a != nil {
			if a.NoLine { // count_tokens: a block or a failure answers the estimate (P4d)
				e.answerOwn(w, r, s, *a, started, tr)
				return
			}
			e.guardRefused(w, r, s, &p, a, started, tr)
			return
		}
		e.noteFailures(p.Failures)
	}
	if p.Upstream != UpChat || p.Format != "openai" {
		e.serveDeliver(w, r, s, &p, started, tr, js) // the P4d paths (serve_xlate.go)
		return
	}
	if p.Stream {
		if !e.track() {
			js.ServeHTTP(w, r) // the gateway is shutting down
			return
		}
		// Until serveStream takes the stream over, a panic (in Start, its answer hook included) still ends the
		// stream the gateway's shutdown waits for, and the call's journal entry.
		var call int64
		owned := false
		defer func() {
			if !owned {
				if call != 0 {
					e.calls.End(call)
				}
				e.streams.Done()
			}
		}()
		call = e.open(&p)
		resp, why := e.upstream.Start(r.Context(), &p)
		owned = true // serveStream ends the stream
		e.serveStream(w, r, s, &p, req, started, tr, call, resp, why)
		return
	}
	call := e.open(&p)
	res := e.upstream.Call(r.Context(), &p)
	ms := float64(time.Since(started).Microseconds()) / 1000
	var a Answer
	switch {
	case res.Why == "client gone":
		a = s.Respond(&p, res.Status, res.Body, ms) // the line is still written, as JS records the call
	case res.Why != "":
		a = s.Failed(&p, res.Why, ms)
	default:
		a = s.Respond(&p, res.Status, res.Body, ms)
	}
	e.answerWhole(w, r, s, &p, a, call, started, tr)
}

// answerWhole ends a call answered whole: its usage line, then the answer, then its spans. The usage line goes out
// before the answer: the control plane has it on its way when the client reads the answer (it is stored a few
// milliseconds later, through the outbox and the ingest), as the JS gateway records before it ends.
func (e *Engine) answerWhole(w http.ResponseWriter, r *http.Request, s *State, p *Plan, a Answer, call int64,
	started time.Time, tr *callTrace) {
	rec := a.Usage
	rec.Ts = e.now()
	if tr != nil {
		rec.TraceID = &tr.tc.TraceID
	}
	e.emitLine(rec, p.Unchecked, call)
	if r.Context().Err() == nil {
		write(w, a)
	}
	if tr != nil {
		flushAnswer(w)
		obs := NewObserver()
		obs.Feed(a.Body) // what JS's observer sees: the body res.end writes, also to a client that left
		e.spans(s, tr, a.Status, started, time.Now(), &rec, provSpan(p), obs)
	}
}

// guardRefused answers a call the guardrails refused (a block, or checks that failed closed): its line, then the answer.
func (e *Engine) guardRefused(w http.ResponseWriter, r *http.Request, s *State, p *Plan, a *Answer, started time.Time,
	tr *callTrace) {
	if p.Overrun != "" {
		e.logf("go engine: guardrails: the %s check ran past its limit; the call was refused", p.Overrun)
	}
	if p.GuardPanic != "" {
		e.logf("go engine: guardrails: the checks panicked (%s); the call was refused with 500", p.GuardPanic)
	}
	rec := a.Usage
	rec.Ts = e.now()
	if tr != nil {
		rec.TraceID = &tr.tc.TraceID
	}
	e.emitLine(rec, false, 0) // a refusal was not metered: no unchecked mark (P4e Decision 14)
	if r.Context().Err() == nil {
		write(w, *a)
	}
	if tr != nil {
		flushAnswer(w)
		obs := NewObserver()
		obs.Feed(a.Body)
		e.spans(s, tr, a.Status, started, time.Now(), &rec, provSpan(p), obs)
	}
}

// emitLine hands one usage line to the outbox, marked when no fresh snapshot checked the call; with a journalled call,
// the outbox notes its close first (P4f D14).
func (e *Engine) emitLine(rec ipc.UsageRecord, unchecked bool, call int64) {
	rep := &ipc.UsageReport{Record: rec}
	if unchecked {
		u := ipc.Unchecked
		rep.Budget = &u
	}
	var snap *ipc.BudgetSnapshot
	if st := e.state.Load(); st != nil {
		snap = st.Snapshot
	}
	sp, priced := spentOf(rec, snap) // the bases before the line goes out
	var seq int64
	var err error
	if call != 0 {
		seq, err = e.emitCall(rep, call)
	} else {
		seq, err = e.emit(rep)
	}
	if err != nil {
		e.logf("go engine: the usage line could not be stored: %v", err)
	}
	// Counted as soon as it is stored (it goes out without waiting), so the next call sees it (P4e Decision 15).
	if priced {
		sp.Seq = seq
		e.spend.Add(sp)
	}
}

// Acked tells the engine the control plane stored every event up to seq (the outbox ack).
func (e *Engine) Acked(seq int64) { e.spend.Acked(seq) }

// SnapshotIn tells the engine a budget snapshot arrived (after SetState with it): its own lines acknowledged before it
// are in it.
func (e *Engine) SnapshotIn() { e.spend.SnapshotIn() }

// noteFailures logs checks that failed in log mode: the check and how many strings, at most once a minute per check,
// never the detector's text (P4e Decision 10).
func (e *Engine) noteFailures(fs []guard.Failure) {
	for _, f := range fs {
		e.failMu.Lock()
		now := time.Now()
		if last, ok := e.failAt[f.Check]; ok && now.Sub(last) < time.Minute {
			e.failMu.Unlock()
			continue
		}
		e.failAt[f.Check] = now
		e.failMu.Unlock()
		e.logf("go engine: guardrails: the %s check failed on %d string(s) in log mode; the call went on, marked",
			f.Check, f.Count)
	}
}

func write(w http.ResponseWriter, a Answer) {
	for k, v := range a.Header {
		w.Header()[k] = v
	}
	w.Header().Set("Content-Length", itoa(len(a.Body)))
	w.WriteHeader(a.Status)
	_, _ = w.Write(a.Body)
}

// flushAnswer sends what the handler wrote before the span work runs, so the client has the whole answer first (P4f D1,
// final review I4: net/http holds a small body in its buffer until the handler returns). A client that left: no-op.
func flushAnswer(w http.ResponseWriter) { _ = http.NewResponseController(w).Flush() }

func itoa(n int) string { return NumberString(float64(n)) }

// serveStream answers a planned stream (P4c D3) whose provider call started (resp, or why it failed; call is its
// journal entry): the provider's headers, then each read rewritten and flushed at once, written synchronously (a slow
// client slows the provider read), and one usage line when it ends, whatever ended it, handed to emit after the
// response's last byte. It ends the stream the caller tracked (streams.Done).
func (e *Engine) serveStream(w http.ResponseWriter, r *http.Request, s *State, p *Plan, req *Request, started time.Time,
	tr *callTrace, call int64, resp *http.Response, why string) {
	elapsed := func() float64 { return float64(time.Since(started).Microseconds()) / 1000 }
	var line func() ipc.UsageRecord
	var obs *Observer // what the client was sent, for the client span's id and finish reasons (P4f D5)
	if tr != nil {
		obs = NewObserver()
	}
	status := http.StatusOK
	defer func() {
		if line == nil {
			if call != 0 {
				e.calls.End(call) // it ends with no line
			}
			e.streams.Done()
			return
		}
		rec := line()
		rec.Ts = e.now()
		if tr != nil {
			rec.TraceID = &tr.tc.TraceID
		}
		ended := time.Now()
		go func() {
			defer e.streams.Done()
			e.emitLine(rec, p.Unchecked, call)
			if tr != nil {
				e.spans(s, tr, status, started, ended, &rec, provSpan(p), obs)
			}
		}()
	}()
	if why != "" {
		a := s.Failed(p, why, elapsed())
		line = func() ipc.UsageRecord { return a.Usage }
		status = a.Status
		if obs != nil {
			obs.Feed(a.Body)
		}
		if r.Context().Err() == nil {
			write(w, a)
		}
		return
	}
	if !StreamsBody(resp.StatusCode) {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		a := s.Respond(p, resp.StatusCode, body, elapsed())
		line = func() ipc.UsageRecord { return a.Usage }
		status = a.Status
		if obs != nil {
			obs.Feed(a.Body)
		}
		if r.Context().Err() == nil {
			write(w, a)
		}
		return
	}
	defer resp.Body.Close()
	// The client's body and the plan's copies are no longer needed (Decision 11); the provider request keeps its own
	// in-memory body until the call ends (net/http writes headers and body in one go only for such a body).
	p.Body, req.Body, r.Body = nil, nil, http.NoBody
	cs := NewChatStream(p.Model, p.WantsUsage, elapsed)
	line = func() ipc.UsageRecord { return s.StreamLine(p, cs, elapsed()) }
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	for _, c := range CORS {
		h.Set(c[0], c[1])
	}
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	if rc.Flush() != nil {
		return
	}
	buf := make([]byte, StreamReadSize)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if streamFeedHook != nil {
				streamFeedHook()
			}
			out, ferr := cs.Feed(buf[:n])
			if len(out) > 0 {
				if obs != nil {
					obs.Feed(out)
				}
				if _, werr := w.Write(out); werr != nil || rc.Flush() != nil {
					return
				}
			}
			if ferr != nil {
				return // a line past 16 MiB: ended as a cut
			}
		}
		if errors.Is(err, io.EOF) {
			if out := cs.End(); len(out) > 0 {
				if obs != nil {
					obs.Feed(out)
				}
				_, _ = w.Write(out)
				_ = rc.Flush()
			}
			return
		}
		if err != nil {
			return // the provider broke off, or the client left (its context cancelled the call)
		}
	}
}

// logSaverPanic logs a panic out of the token saver (the value only, no prompt text), as the guardrails' GuardPanic.
func (e *Engine) logSaverPanic(p *Plan) {
	if p.SaverPanic != "" {
		e.logf("go engine: token saver: panicked (%s); the call was handed to the control plane", p.SaverPanic)
	}
}
