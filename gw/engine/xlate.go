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
	AdaptChatTagged(p.Target.Provider, p.Target.Model, b, p.Soft, dropped)
	return b.Stringify()
}

// translatedPlan is translated() → proxyChat for a request already translated to chat (J2). format is the client's
// format, sh its refusal shape; clientModel the name the client asked for.
func (s *State) translatedPlan(r *Request, key *ipc.GatewayKeyAuth, clientModel string, oaiBody *jsv.Object,
	tagged bool, soft, dropped, custom []string, format string, sh Shape) Plan {
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
	t, why := s.target(model, key)
	if why != "" {
		return handoff(why)
	}
	cred, why := s.credential(t)
	if why != "" {
		return handoff(why)
	}
	stream := oai.Get("stream").Truthy()
	out, why := upstreamBody(oai, t, stream)
	if why != "" {
		return handoff(why)
	}
	var job *saverJob
	if s.tokenSaverOn(r) {
		// proxyChat's saveTokens(body, 'openai') on the translated chat body (rtk.js R2, spec D10).
		if job, why = scanSaver(out, "openai"); why != "" {
			return handoff(why)
		}
	}
	p := Plan{Key: key, Target: t, Model: model, ClientModel: clientModel, Stream: stream, Format: format, Shape: sh,
		URL: t.Provider.BaseURL + "/chat/completions", Header: providerHeader(t, cred), Upstream: UpChat,
		GuardFormat: "openai-chat", Env: translate.Default, Tagged: tagged, Soft: soft,
		Dropped: append([]string(nil), dropped...), Custom: custom, Base: s.base(key, t, model, r),
		build: translatedBuild, saver: job}
	if tagged && p.Dropped == nil {
		p.Dropped = []string{}
	}
	f := format
	p.Base.Format = &f
	if model != m.S {
		req := jsSlice(m.S, 80)
		p.Base.Requested = &req
	}
	if stream {
		v := true
		p.Base.Stream = &v
	}
	if FormatOf(t.Provider) == "anthropic" {
		if why := planAnthropicLeg(&p, cred, out); why != "" {
			return handoff(why)
		}
	}
	if pol := PolicyFor(s.Gateway.Guardrails, key); checksApply(pol, ScopeTier(t.Provider, t.Model)) {
		p.Policy, p.pending = &pol, out
	} else if job != nil {
		p.pending = out // State.Save builds Body
	} else {
		p.Body = p.build(&p, out)
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
	p := s.translatedPlan(r, key, m.S, tg.Body, true, tg.Soft, tg.Dropped, nil, "anthropic", ShapeAnthropic)
	p.Class = SubMessages
	if p.Stream {
		p.Class = SubMessagesStream
	}
	return p
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
	p := s.translatedPlan(r, key, m.S, tg.Body, true, tg.Soft, tg.Dropped, custom, "responses", ShapeOpenAI)
	p.Class = SubResponses
	if p.Stream {
		p.Class = SubResponsesStream
	}
	return p
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
	p := s.translatedPlan(r, key, model, o, false, nil, nil, nil, "gemini", ShapeGemini)
	p.Class = SubGemini
	if stream {
		p.Class = SubGeminiStream
	}
	return p
}
