package engine

// count_tokens on the go engine (P4d): lib/gateway/index.js countTokens for POST /v1/messages/count_tokens (the
// provider's own count for a native Anthropic model, else an estimate) and the Gemini countTokens estimate. Neither
// writes a usage line; neither checks a budget.

import (
	"io"
	"math"
	"net/http"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"
)

// estimateAnswer is json(res, 200, body) for an estimate: no usage line.
func estimateAnswer(key string, n int) *Answer {
	b := NewObject()
	b.Set(key, Num(float64(n)))
	return &Answer{Status: http.StatusOK, Header: answerHeader(), Body: b.Stringify(), NoLine: true}
}

// countEstimate is countTokens' estimate(): { input_tokens: estimateTokens({ system, messages, tools }) }.
func countEstimate(b *Value) *Answer {
	o := jsv.NewObject()
	for _, k := range []string{"system", "messages", "tools"} {
		if v := b.Get(k); v != nil {
			o.Set(k, toJSV(v)) // an absent field stays out, as JSON.stringify drops undefined
		}
	}
	return estimateAnswer("input_tokens", translate.EstimateTokens(o))
}

// PlanCount is /v1/messages/count_tokens (J7).
func (s *State) PlanCount(r *Request, now int64) Plan {
	key, b, why := s.admit(r, now, "llm")
	if why != "" {
		return handoff(why)
	}
	self := Plan{Key: key, Upstream: UpNone, Class: SubMessagesCount, Format: "anthropic", Shape: ShapeAnthropic,
		Env: translate.Default, Self: countEstimate(b)}
	m := b.Get("model")
	if m == nil || m.Kind != String || m.S == "" {
		return self
	}
	model, ok := s.MapModel(m.S)
	if !ok {
		return handoff("model: mapping needs the model list")
	}
	t, why := s.target(model, key)
	if why == "model: a route" {
		return handoff(why) // resolveAll(...)[0] of a route depends on its rotation and cooldowns
	}
	if t == nil || FormatOf(t.Provider) != "anthropic" || Authorize(key, "llm", model, "") != "" {
		return self
	}
	cred, why := s.credential(t)
	if why != "" {
		return handoff(why)
	}
	body := NewObject()
	body.Set("model", Str(t.Model))
	for _, f := range []string{"messages", "system", "tools", "tool_choice", "thinking"} {
		if v := b.Get(f); v != nil {
			body.Set(f, v)
		}
	}
	p := Plan{Key: key, Target: t, Model: model, ClientModel: m.S, Upstream: UpCount, Class: SubMessagesCount,
		Format: "anthropic", Shape: ShapeAnthropic, GuardFormat: "anthropic", Env: translate.Default,
		URL: t.Provider.BaseURL + "/messages/count_tokens", Header: providerHeader(t, cred), Timeout: CountTimeout,
		Self: self.Self, Base: s.base(key, t, model, r), build: passBuild}
	addAnthropicHeaders(&p, r)
	if pol := PolicyFor(s.Gateway.Guardrails, key); checksApply(pol, ScopeTier(t.Provider, t.Model)) {
		p.Policy, p.pending = &pol, body
	} else {
		p.Body = body.Stringify()
	}
	return p
}

// planGeminiCount is the Gemini countTokens route: { totalTokens: estimateTokens(body.contents ||
// body.generateContentRequest || body) }.
func (s *State) planGeminiCount(r *Request, now int64) Plan {
	key, b, why := s.admit(r, now, "llm")
	if why != "" {
		return handoff(why)
	}
	v := b.Get("contents")
	if !v.Truthy() {
		v = b.Get("generateContentRequest")
	}
	if !v.Truthy() {
		v = b
	}
	return Plan{Key: key, Upstream: UpNone, Class: SubGeminiCount, Format: "gemini", Shape: ShapeGemini,
		Env: translate.Default, Self: estimateAnswer("totalTokens", translate.EstimateTokens(toJSV(v)))}
}

// deliverCount: a finite input_tokens in a 2xx JSON answer is returned re-serialized; anything else (a failed call
// included) is the estimate.
func (s *State) deliverCount(p *Plan, res ProviderResult, w ClientWriter) *ipc.UsageRecord {
	if res.Why == "" && ok2xx(res.Status) {
		raw, _ := io.ReadAll(res.Body)
		if j, err := jsv.Parse([]byte(decodeWHATWG(raw))); err == nil && jsv.Truthy(j) {
			if n, ok := field(j, "input_tokens").(float64); ok && !math.IsInf(n, 0) && !math.IsNaN(n) {
				b, _ := jsv.Stringify(j)
				return writeAnswer(w, Answer{Status: http.StatusOK, Header: answerHeader(), Body: []byte(b), NoLine: true})
			}
		}
	}
	return writeAnswer(w, *p.Self)
}
