package engine

// Route calls on the go engine (P4h spec Decisions 7–9): after the key's rate-limit take, the route's rotation is taken
// (resolveAll, as many times as the JS path calls it), the legs are ordered (rotated, cooling providers last), the price
// and budget filters run over them (targetsFor, allowedTargets), and proxyChat's loop tries them in order: the
// guardrails of each leg's tier, the call, a 599 line for a call that threw, a "fell back" line for a retryable answer
// that is not the last target's, then the next. The leg that answered goes on through today's answer path (whole,
// stream, Anthropic leg); no leg answered is the 502 with the last error. Every byte to the client comes after the loop.

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

// RouteSwitch is the routes switch (spec Decision 2, as SaverSwitch and AuthSwitch): not a path class; a route call is
// served on go only when its sub-class and this switch are on go, and compared when either is shadowed.
const RouteSwitch = "routes"

// BodyUsedText is the TypeError text Deno's Response.text() throws for a body already cancelled (the control plane runs
// on Deno; Node writes "Body is unusable: Body has already been read", spec Decision 15f).
const BodyUsedText = "Body already consumed"

// retryable is lib/gateway/index.js retryable: an answer worth trying the next route target for.
func retryable(status int) bool { return status == 408 || status == 429 || status >= 500 }

// fellBack is proxyChat's line for a target whose retryable answer sent the call on to the next one: the attempt's
// fields with routedTo, its status, no tokens, no guardrail outcome, no savedTokens (responsesPassthrough's keeps
// requested).
func (s *State) fellBack(p *Plan, status int, ms float64) ipc.UsageRecord {
	rec := p.Base
	name := p.Target.Name
	msg := "fell back to the next route target"
	rec.RoutedTo, rec.Guard, rec.SavedTokens = &name, nil, nil
	if p.Upstream != UpResponsesNative { // responsesPassthrough's rec keeps requested (R9)
		rec.Requested = nil
	}
	rec.Input, rec.Output, rec.Cached = nil, nil, nil
	rec.Status, rec.Ms, rec.Cost, rec.Error = int64(status), ms, 0, &msg
	return rec
}

// budgetLegs is allowedTargets over the ordered legs: a leg a budget refuses (the snapshot, or the engine's own lines
// since it) is dropped; none left is the 402 of the first refusal. unchecked: no fresh snapshot checked a leg kept.
// model is the model every leg is checked under (embeddings' modelOf: () => body.model); "" checks each leg's target.
func (s *State) budgetLegs(legs []*Plan, model string, now, staleMs int64, own []Spent, sh Shape) ([]*Plan, *Answer, bool) {
	var ok []*Plan
	refusal := ""
	unchecked := false
	for _, l := range legs {
		m := model
		if m == "" {
			m = l.Target.Name
		}
		in := ipc.CheckInput{KeyID: l.Key.ID, Model: NormalizeModel(m), Provider: l.Target.Provider.ID}
		d := ipc.Decide(s.Snapshot, now, staleMs, in)
		why := d.Message
		if d.Action != ipc.Refuse {
			why = s.ownRefusal(in, own)
		}
		if why != "" {
			if refusal == "" {
				refusal = why
			}
			continue
		}
		unchecked = unchecked || d.Action == ipc.Unchecked
		ok = append(ok, l)
	}
	if len(ok) == 0 {
		return nil, quota(refusal, sh), false
	}
	return ok, nil, unchecked
}

// serveRoute serves a route call after the key's rate-limit take and the token saver (spec D3).
func (e *Engine) serveRoute(w http.ResponseWriter, r *http.Request, s *State, p *Plan, req *Request, started time.Time,
	tr *callTrace, js http.Handler) {
	tracked := false
	if p.Stream {
		if !e.track() {
			js.ServeHTTP(w, r) // the gateway is shutting down: before any take, so JS takes as it serves
			return
		}
		tracked = true
	}
	defer func() {
		if tracked {
			e.streams.Done()
		}
	}()
	elapsed := func() float64 { return float64(time.Since(started).Microseconds()) / 1000 }
	stamp := func(rec ipc.UsageRecord) ipc.UsageRecord {
		rec.Ts = e.now()
		if tr != nil {
			rec.TraceID = &tr.tc.TraceID
		}
		return rec
	}
	// A leg's line that does not end the call (a 599, a fallback): written now, through the leg's journal entry, and
	// kept for its client span, emitted with the call's spans when the call ends (P4h Decision 10).
	attempt := func(l *Plan, rec ipc.UsageRecord, unchecked bool, call int64) {
		rec = stamp(rec)
		e.emitLine(rec, unchecked, call)
		if tr != nil {
			tr.prior = append(tr.prior, priorLine{rec: rec, prov: provSpan(l)})
		}
	}

	// resolveAll: the take (once per resolveAll of the JS path; the last one's n orders), then the order.
	now := e.now()
	rr := p.Route.Strategy != nil && *p.Route.Strategy == "round-robin"
	legs := p.replay
	if legs == nil {
		legs = e.routes.TakeOrder(p.Route.Name, len(p.Route.Targets), p.Takes, rr, p.Legs, now)
	}
	if p.Upstream == UpCount { // countTokens: the first leg decides; no price rule, no budget, no fallback
		e.serveRouteCount(w, r, s, p, legs, started, tr, js)
		return
	}

	// targetsFor's price rule for a key with a budget, then allowedTargets: 402s without a line.
	if s.limited(p.Key) {
		var priced []*Plan
		for _, l := range legs {
			if TierOf(l.Target.Provider) == "private" || s.Pricer.Key(l.Target.Name) != "" {
				priced = append(priced, l)
			}
		}
		if len(priced) == 0 {
			e.answerOwn(w, r, s, *quota("this key has a budget, and "+p.Model+
				" has no known price; add one under Settings → Model prices", p.Shape), started, tr)
			return
		}
		legs = priced
	}
	byModel := ""
	if p.Upstream == UpEmbeddings {
		byModel = p.Model
	}
	legs, ref, unchecked := s.budgetLegs(legs, byModel, now, e.staleMs, e.spend.Live(), p.Shape)
	if ref != nil {
		e.answerOwn(w, r, s, *ref, started, tr)
		return
	}
	if p.Upstream == UpEmbeddings { // embeddings: the first allowed leg answers, with no fallback (R8)
		l := legs[0]
		l.Unchecked = unchecked
		if l.Policy != nil {
			if a := s.Guard(l, elapsed()); a != nil {
				e.guardRefused(w, r, s, l, a, started, tr)
				return
			}
			e.noteFailures(l.Failures)
		}
		e.serveDeliver(w, r, s, l, started, tr, js)
		return
	}

	// proxyChat's loop (responsesPassthrough's for a route of native Responses targets: the same rules, R9).
	failed := func(l *Plan, why string) Answer {
		if l.Upstream == UpResponsesNative {
			return s.failedNative(l, why, elapsed())
		}
		return s.Failed(l, why, elapsed())
	}
	var last *Answer // the 502 of the last call that threw, with its 599 line
	fell := false    // a leg fell back: proxyChat keeps it as its target, with its body cancelled
	drop := false    // a leg tried so far dropped reasoning_effort: adaptChat pushed "reasoning" to the shared list
	for i, l := range legs {
		l.Unchecked = unchecked
		if l.Policy != nil {
			if a := s.Guard(l, elapsed()); a != nil {
				e.guardRefused(w, r, s, l, a, started, tr) // a block or a failure ends the call (400, 500)
				return
			}
			e.noteFailures(l.Failures)
		}
		call := e.open(l)
		if l.buildErr != nil { // callChat throws before its fetch (review m3)
			a := s.buildRefusal(l, elapsed())
			if a.NoLine { // a translation 400 is rethrown: the call ends, no line
				if call != 0 {
					e.calls.End(call)
				}
				e.answerOwn(w, r, s, a, started, tr)
				return
			}
			attempt(l, a.Usage, unchecked, call)
			last = &a
			continue
		}
		resp, why := e.upstream.Start(r.Context(), l)
		drop = drop || l.dropAdded
		if why != "" {
			a := failed(l, why)
			attempt(l, a.Usage, unchecked, call)
			last = &a
			continue
		}
		if drop && l.Tagged && !slices.Contains(l.Dropped, "reasoning") {
			l.Dropped = append(l.Dropped, "reasoning")
		}
		status := resp.StatusCode
		if !ok2xx(status) && retryable(status) && i < len(legs)-1 {
			_ = resp.Body.Close()
			attempt(l, s.fellBack(l, status, elapsed()), unchecked, call)
			fell = true
			continue
		}
		if l.Upstream == UpAnthropic && !l.Stream && ok2xx(status) {
			// callChat converts a whole Anthropic answer inside proxyChat's try: a conversion that throws is a failed
			// target (a 599 line, then the next one), not the answer.
			// A read error leaves raw short: anthropicWhole then fails on it, as r.json() rejects in JS.
			raw, _ := io.ReadAll(cutBody{resp.Body})
			_ = resp.Body.Close()
			conv, why := anthropicWhole(l.Env, raw, l.ClientModel, s.onPanic("the Anthropic leg's whole answer"))
			if why != "" {
				a := s.Failed(l, why, elapsed())
				attempt(l, a.Usage, unchecked, call)
				last = &a
				continue
			}
			if l.Format == "openai" {
				e.answerWhole(w, r, s, l, s.Respond(l, 200, conv, elapsed()), call, started, tr)
				return
			}
			l.converted = true // a translated call: translated() answers from the converted body
			e.deliverCall(w, r, s, l, started, tr, call, func(context.Context) ProviderResult {
				return ProviderResult{Status: 200, Header: resp.Header, Body: io.NopCloser(bytes.NewReader(conv))}
			})
			return
		}
		switch {
		case l.Upstream == UpChat && l.Format == "openai" && l.Stream:
			tracked = false // serveStream ends the stream
			e.serveStream(w, r, s, l, req, started, tr, call, resp, "")
		case l.Upstream == UpChat && l.Format == "openai":
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			e.answerWhole(w, r, s, l, s.Respond(l, status, body, elapsed()), call, started, tr)
		default:
			e.deliverCall(w, r, s, l, started, tr, call, func(context.Context) ProviderResult {
				return ProviderResult{Status: status, Header: resp.Header, Body: cutBody{resp.Body}}
			})
		}
		return
	}
	if fell {
		// Copied quirk (spec Decision 7): proxyChat (or responsesPassthrough) returns the last target that fell back,
		// whose body it cancelled; chatCompletion, translated() or the passthrough reads that body (the !r.ok answer),
		// the runtime throws, and handle's catch answers 502 upstream_error with the runtime's text in the client's
		// shape. No line beyond the attempts' own.
		e.answerOwn(w, r, s, shapeError(p.Shape, http.StatusBadGateway, s.mask(BodyUsedText), "upstream_error", "", ""),
			started, tr)
		return
	}
	// No target answered: the 502 with the last error, masked (its 599 line is out, and its span is with the others).
	a := *last
	if r.Context().Err() == nil {
		write(w, a)
	}
	if tr != nil {
		flushAnswer(w)
		obs := NewObserver()
		obs.Feed(a.Body)
		e.spans(s, tr, a.Status, started, time.Now(), nil, nil, obs)
	}
}

// serveRouteCount is countTokens on a route: resolveAll(model)[0] (taken and ordered by the caller) decides; an
// estimate leg, or no leg at all, is the estimate; an Anthropic leg is counted by its provider, its guardrails first (a
// block or a failure is the estimate).
func (e *Engine) serveRouteCount(w http.ResponseWriter, r *http.Request, s *State, p *Plan, legs []*Plan,
	started time.Time, tr *callTrace, js http.Handler) {
	if len(legs) == 0 || legs[0].Upstream == UpNone {
		e.answerOwn(w, r, s, *p.Self, started, tr)
		return
	}
	l := legs[0]
	if l.Policy != nil {
		if a := s.Guard(l, float64(time.Since(started).Microseconds())/1000); a != nil {
			e.answerOwn(w, r, s, *a, started, tr) // count's Guard answers the estimate, with no line
			return
		}
		e.noteFailures(l.Failures)
	}
	e.serveDeliver(w, r, s, l, started, tr, js)
}
