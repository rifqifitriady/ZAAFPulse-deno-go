package engine

// Translated calls on the go engine (P4d, slice S5): /v1/messages (Anthropic Messages), /v1/responses (OpenAI
// Responses) and the Gemini generateContent calls, translated to an OpenAI chat body with gw/translate and served as
// lib/gateway/index.js translated() → proxyChat → callChat serves them. The translated body enters the chat pipeline
// as the bytes JSON.stringify would send (P4d Decision 7), so the stream-usage merge, the guardrails and the provider
// body are the chat path's.

import (
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"
)

// translatedBuild is callChat's OpenAI request for a translated body: { ...body, model: t.model } through adaptChat,
// which drops a reasoning_effort the translation added for a model that does not take it (P4d Decision 11).
func translatedBuild(p *Plan, b *Value) []byte {
	b.Set("model", Str(p.Target.Model))
	var dropped *[]string
	if p.Tagged {
		dropped = &p.Dropped
	}
	n := len(p.Dropped)
	AdaptChatTagged(p.Target.Provider, p.Target.Model, b, p.Soft, dropped)
	p.dropAdded = len(p.Dropped) > n
	return b.Stringify()
}

// xlateSpec is what a translated call keeps of its client request: the client's model name, the translation's tags,
// the client's format and its refusal shape.
type xlateSpec struct {
	clientModel   string
	tagged        bool
	soft, dropped []string
	custom        []string
	format        string
	shape         Shape
}

// translatedPlan is translated() → proxyChat for a request already translated to chat (J2): one target, or a route's
// legs (P4h).
func (s *State) translatedPlan(r *Request, key *ipc.GatewayKeyAuth, oaiBody *jsv.Object, x xlateSpec) Plan {
	text, ok := jsv.Stringify(oaiBody)
	if !ok {
		return handoff("translate: the request cannot be translated")
	}
	oai, err := Parse([]byte(text)) // what JSON.stringify sends (P4d Decision 7)
	if err != nil || oai.Kind != Object {
		return handoff("translate: the request cannot be translated")
	}
	m := oai.Get("model")
	if m == nil || m.Kind != String || m.S == "" {
		return handoff("model: missing")
	}
	model, ok := s.MapModel(m.S)
	if !ok {
		return handoff("model: mapping needs the model list")
	}
	if Authorize(key, "llm", model, "") != "" {
		return handoff("key: not allowed")
	}
	if rt := s.routeOf(model); rt != nil {
		return s.translatedRoute(r, key, oai, model, m.S, rt, x)
	}
	t, why := s.target(model, key)
	if why != "" {
		return handoff(why)
	}
	p, why := s.xlateLeg(r, oai, key, t, model, m.S, x, s.tokenSaverOn(r))
	if why != "" {
		return handoff(why)
	}
	return p
}

// translatedRoute is proxyChat over a route for a translated body: one leg per target, built from the translated body;
// the token saver, when on, scans that body once (Save shortens it and builds the legs again), as planChatRoute.
func (s *State) translatedRoute(r *Request, key *ipc.GatewayKeyAuth, oai *Value, model, asked string,
	rt *ipc.RouteConfig, x xlateSpec) Plan {
	build := func(b *Value) ([]*Plan, string) {
		return s.routeLegs(rt, key, func(t *Target) (Plan, string) { return s.xlateLeg(r, b, key, t, model, asked, x, false) })
	}
	legs, why := build(oai)
	if why != "" {
		return handoff(RouteHandoff + why)
	}
	var job *saverJob
	var pending *Value
	if s.tokenSaverOn(r) {
		pending = oai.Clone()
		if job, why = scanSaver(pending, "openai"); why != "" {
			return handoff(why)
		}
		if job != nil && objectToolIDs(pending) {
			return handoff(saverIDs)
		}
		if job == nil {
			pending = nil
		}
	}
	first := firstLeg(legs)
	return Plan{Key: key, Model: model, ClientModel: x.clientModel, Route: rt, Legs: legs, Takes: 1,
		Stream: first.Stream, Format: x.format, Shape: x.shape, Upstream: UpChat, GuardFormat: "openai-chat",
		Env: translate.Default, saver: job, pending: pending, relegs: build}
}

// xlateLeg is proxyChat's work for one target t of a translated body: the upstream body, the Anthropic leg, the
// guardrails' policy for the target's tier and the provider request. model is the mapped model, asked the translated
// body's own; save scans for the token saver (a single-target call).
func (s *State) xlateLeg(r *Request, oai *Value, key *ipc.GatewayKeyAuth, t *Target, model, asked string, x xlateSpec,
	save bool) (Plan, string) {
	cred, auth, why := s.credential(t)
	if why != "" {
		return Plan{}, why
	}
	stream := oai.Get("stream").Truthy()
	out, why := upstreamBody(oai, t, stream)
	if why != "" {
		return Plan{}, why
	}
	var job *saverJob
	if save {
		// proxyChat's saveTokens(body, 'openai') on the translated chat body (rtk.js R2, spec D10).
		if job, why = scanSaver(out, "openai"); why != "" {
			return Plan{}, why
		}
		if job != nil && objectToolIDs(out) {
			return Plan{}, saverIDs
		}
	}
	p := Plan{Key: key, Target: t, Model: model, ClientModel: x.clientModel, Stream: stream, Format: x.format,
		Shape: x.shape, URL: t.Provider.BaseURL + "/chat/completions", Header: providerHeader(t, cred), Upstream: UpChat,
		GuardFormat: "openai-chat", Env: translate.Default, Tagged: x.tagged, Soft: x.soft,
		Dropped: append([]string(nil), x.dropped...), Custom: x.custom, Base: s.base(key, t, model, r),
		build: translatedBuild, saver: job, auth: auth}
	if x.tagged && p.Dropped == nil {
		p.Dropped = []string{}
	}
	f := x.format
	p.Base.Format = &f
	if model != asked {
		req := jsSlice(asked, 80)
		p.Base.Requested = &req
	}
	if stream {
		v := true
		p.Base.Stream = &v
	}
	if FormatOf(t.Provider) == "anthropic" {
		if why := planAnthropicLeg(&p, cred, out); why != "" {
			return Plan{}, why
		}
	}
	if pol := PolicyFor(s.Gateway.Guardrails, key); checksApply(pol, ScopeTier(t.Provider, t.Model)) {
		p.Policy, p.pending = &pol, out
	} else if job != nil {
		p.pending = out // State.Save builds Body
	} else {
		p.Body = p.build(&p, out)
	}
	return p, ""
}

// xlateClass sets a translated plan's sub-class (and its legs'), and how many times the JS path takes the route.
func xlateClass(p Plan, whole, stream string, takes int) Plan {
	c := whole
	if p.Stream {
		c = stream
	}
	p.Class = c
	if p.Route != nil {
		p.Takes = takes
	}
	return p
}

// PlanMessages is messagesApi: the Anthropic passthrough, or the request translated (J1).
func (s *State) PlanMessages(r *Request, now int64) Plan {
	key, body, why := s.admit(r, now, "llm")
	if why != "" {
		return handoff(why)
	}
	if msgs := body.Get("messages"); msgs == nil || msgs.Kind != Array {
		return handoff("body: messages is not an array")
	}
	m := body.Get("model")
	if m == nil || m.Kind != String || m.S == "" {
		return handoff("model: missing")
	}
	if p, done := s.planAnthropicNative(r, key, body, m.S); done {
		return p
	}
	tg, err := translate.AnthropicToOpenAI(toJSV(body))
	if err != nil {
		return handoff("translate: the request cannot be translated")
	}
	// messagesApi's resolveAll(model)[0] for the passthrough test, then proxyChat's: a route takes twice (Decision 7).
	p := s.translatedPlan(r, key, tg.Body, xlateSpec{clientModel: m.S, tagged: true, soft: tg.Soft,
		dropped: tg.Dropped, format: "anthropic", shape: ShapeAnthropic})
	return xlateClass(p, SubMessages, SubMessagesStream, 2)
}

// PlanResponses is responsesApi: the native passthrough when the target takes /responses, or the request translated
// (J5).
func (s *State) PlanResponses(r *Request, now int64) Plan {
	key, body, why := s.admit(r, now, "llm")
	if why != "" {
		return handoff(why)
	}
	m := body.Get("model")
	if m == nil || m.Kind != String || m.S == "" {
		return handoff("model: missing")
	}
	if p, done := s.planResponsesNative(r, key, body, m.S); done {
		return p
	}
	tg, custom, err := translate.ResponsesToOpenAI(toJSV(body))
	if err != nil {
		return handoff("translate: the request cannot be translated")
	}
	// responsesApi's resolveAll for the every-native test, then proxyChat's: a route takes twice (Decision 7).
	p := s.translatedPlan(r, key, tg.Body, xlateSpec{clientModel: m.S, tagged: true, soft: tg.Soft,
		dropped: tg.Dropped, custom: custom, format: "responses", shape: ShapeOpenAI})
	return xlateClass(p, SubResponses, SubResponsesStream, 2)
}

// PlanGemini is geminiApi for generateContent and streamGenerateContent, and the countTokens estimate (J6).
func (s *State) PlanGemini(r *Request, now int64) Plan {
	if _, _, shaped := geminiPathShape(r.RawPath); !shaped {
		return handoff("path: not a Gemini model call")
	}
	model, action, ok := geminiPath(r.RawPath)
	if !ok {
		return handoff("model: the path does not decode")
	}
	if action == "countTokens" {
		return s.planGeminiCount(r, now)
	}
	key, body, why := s.admit(r, now, "llm")
	if why != "" {
		return handoff(why)
	}
	stream := action == "streamGenerateContent"
	o, err := translate.GeminiToOpenAI(toJSV(body), model, stream)
	if err != nil {
		return handoff("translate: the request cannot be translated")
	}
	p := s.translatedPlan(r, key, o, xlateSpec{clientModel: model, format: "gemini", shape: ShapeGemini})
	return xlateClass(p, SubGemini, SubGeminiStream, 1)
}
