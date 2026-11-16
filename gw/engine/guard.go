package engine

// Guardrails on the go engine (P4e, slice S6): lib/gateway/index.js guard() for the chat route. The checks run on the
// body proxyChat sends upstream (the client's body with the stream usage merge), before the model is replaced, with
// resolvePolicy's policy, when inScope says the target's tier is checked. A block is a 400 guardrail_blocked and a
// check that failed any other way a 500 (fail closed: entry condition 4); either writes a usage line and sends
// nothing. A redacted body goes upstream instead of the client's; the outcome goes in the call's usage line.

import (
	"errors"
	"net/http"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/guard"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"
)

// checkBody is guard.CheckBody; a test replaces it to prove the fail-closed path.
var checkBody = guard.CheckBody

// PolicyFor is guardrails.js resolvePolicy(gateway.guardrails, key): a mode the key sets wins, one it leaves out comes
// from the global setting; the scope is global only (the key's is ignored). The global policy was cleaned by
// lib/front/push.js globalPolicy and the key's modes by keyAuth.
func PolicyFor(global ipc.GuardrailsPolicy, key *ipc.GatewayKeyAuth) guard.Policy {
	p := guard.Policy{Scope: global.Scope, Secrets: global.Secrets, PII: global.PII, Injection: global.Injection}
	if key == nil {
		return p
	}
	own := key.Guardrails
	if own.Secrets != nil {
		p.Secrets = *own.Secrets
	}
	if own.PII != nil {
		p.PII = *own.PII
	}
	if own.Injection != nil {
		p.Injection = *own.Injection
	}
	return p
}

// checksApply is inScope(policy, tier) with at least one check on. With every check off, checkBody would return the
// body unchanged and no outcome, so skipping it is the same answer.
func checksApply(p guard.Policy, tier string) bool {
	return (p.Scope == "all" || tier != "private") && (p.Secrets != "off" || p.PII != "off" || p.Injection != "off")
}

// finishBody is callChat's provider body: { ...body, model: t.model } through adaptChat, as JSON.stringify writes it.
func finishBody(t *Target, out *Value) []byte {
	out.Set("model", Str(t.Model))
	AdaptChat(t.Provider, t.Model, out)
	return out.Stringify()
}

// toJSV is the engine's value as the guard reads it. Strings are shared, not copied.
func toJSV(v *Value) jsv.Value {
	switch v.Kind {
	case Null:
		return nil
	case Bool:
		return v.B
	case Number:
		return v.N
	case String:
		return v.S
	case Array:
		a := &jsv.Array{Items: make([]jsv.Value, len(v.A))}
		for i, x := range v.A {
			a.Items[i] = toJSV(x)
		}
		return a
	}
	o := jsv.NewObject()
	for _, k := range v.Keys() {
		o.Set(k, toJSV(v.Get(k)))
	}
	return o
}

// fromJSV is the guard's checked value back as the engine's (the guard never makes undefined).
func fromJSV(x jsv.Value) *Value {
	switch t := x.(type) {
	case bool:
		return &Value{Kind: Bool, B: t}
	case float64:
		return Num(t)
	case string:
		return Str(t)
	case *jsv.Array:
		v := &Value{Kind: Array, A: make([]*Value, len(t.Items))}
		for i, it := range t.Items {
			v.A[i] = fromJSV(it)
		}
		return v
	case *jsv.Object:
		o := NewObject()
		for _, k := range t.Keys() {
			o.Set(k, fromJSV(t.Get(k)))
		}
		return o
	}
	return &Value{Kind: Null}
}

func outcome(action string, hits []guard.Hit, failed bool) *ipc.GuardrailOutcome {
	o := &ipc.GuardrailOutcome{Action: action, Hits: make([]ipc.GuardrailHit, 0, len(hits))}
	for _, h := range hits {
		o.Hits = append(o.Hits, ipc.GuardrailHit{Check: h.Check, Label: h.Label, Count: int64(h.Count)})
	}
	if failed {
		t := true
		o.Error = &t
	}
	return o
}

// errorBody is lib/gateway/index.js fail's body: { error: { message, type, code? } }.
func errorBody(msg, typ, code string) []byte {
	b, e := NewObject(), NewObject()
	e.Set("message", Str(msg))
	e.Set("type", Str(typ))
	if code != "" {
		e.Set("code", Str(code))
	}
	b.Set("error", e)
	return b.Stringify()
}

// refusal is guard()'s rec(...) then throw: the line proxyChat's rec writes (the call's base without routedTo, no
// tokens, cost 0) and the client's answer.
func refusal(p *Plan, status int, msg, typ, code string, ms float64, lineErr string, g *ipc.GuardrailOutcome) *Answer {
	rec := p.Base
	rec.RoutedTo, rec.Guard = nil, g
	rec.Status, rec.Ms, rec.Cost = int64(status), ms, 0
	rec.Input, rec.Output, rec.Cached = nil, nil, nil
	rec.Error = &lineErr
	return &Answer{Status: status, Header: answerHeader(), Body: errorBody(msg, typ, code), Usage: rec}
}

// Guard runs the checks of a plan that has them and finishes its provider body; it returns the client's answer when
// the call is refused (its Usage is the line to emit), nil when the call goes on. ms is the time since the start.
func (s *State) Guard(p *Plan, ms float64) *Answer {
	if p.Policy == nil || p.pending == nil {
		return nil
	}
	res, err := checkBody(toJSV(p.pending), *p.Policy, "openai-chat")
	if err != nil {
		p.pending = nil
		var b *guard.Block
		if errors.As(err, &b) {
			p.Overrun = b.Overrun
			return refusal(p, http.StatusBadRequest, b.Error(), "invalid_request_error", "guardrail_blocked", ms,
				"blocked by guardrails", outcome("blocked", b.Hits, false))
		}
		return refusal(p, http.StatusInternalServerError, "guardrails failed", "server_error", "", ms,
			"guardrails failed", nil)
	}
	body := p.pending
	if res.Action == "redacted" {
		body = fromJSV(res.Body)
	}
	p.pending = nil
	p.Body = finishBody(p.Target, body)
	p.Failures = res.Failures
	if res.Action != "" {
		p.Base.Guard = outcome(res.Action, res.Hits, res.Error)
	}
	return nil
}

// Budget is a placeholder until P4e Task 4: no refusal, checked.
func (s *State) Budget(p *Plan, now, staleMs int64, own []any) (*Answer, bool) { return nil, false }
