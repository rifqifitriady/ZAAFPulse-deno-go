package engine

// Native passthroughs on the go engine (P4d Decision 12): lib/gateway/index.js anthropicPassthrough (a /v1/messages
// call whose mapped model is an Anthropic-format provider's own name) and responsesPassthrough (a /v1/responses call
// to a provider that takes /responses as it is). The client's body goes upstream with only the model changed; the
// answer comes back with the client's model name in it.

import (
	"io"
	"net/http"
	"regexp"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"
)

// passBuild sends the checked body as it is (JSON.stringify of { ...body, model: t.model }).
func passBuild(_ *Plan, b *Value) []byte { return b.Stringify() }

// nativePlan is what both passthroughs share once their target is known.
func (s *State) nativePlan(r *Request, key *ipc.GatewayKeyAuth, body *Value, name, model string, t *Target,
	up, format, path, guardFormat string, sh Shape, saverFormat string) Plan {
	if Authorize(key, "llm", model, "") != "" {
		return handoff("key: not allowed")
	}
	cred, auth, why := s.credential(t)
	if why != "" {
		return handoff(why)
	}
	stream := body.Get("stream").Truthy()
	p := Plan{Key: key, Target: t, Model: model, ClientModel: name, Stream: stream, Format: format, Shape: sh,
		Upstream: up, GuardFormat: guardFormat, Env: translate.Default, URL: t.Provider.BaseURL + path,
		Header: providerHeader(t, cred), Base: s.base(key, t, model, r), build: passBuild, auth: auth}
	f := format
	p.Base.Format = &f
	if stream {
		v := true
		p.Base.Stream = &v
	}
	out := body.Clone()
	out.Set("model", Str(t.Model))
	if saverFormat != "" && s.tokenSaverOn(r) {
		// anthropicPassthrough: saveTokens(body, 'anthropic') before guard({...body, model: t.model}) (rtk.js R3).
		job, why := scanSaver(out, saverFormat)
		if why != "" {
			return handoff(why)
		}
		p.saver, p.keepSaved = job, true // its guard lines spread base, savedTokens included (R9)
	}
	if pol := PolicyFor(s.Gateway.Guardrails, key); checksApply(pol, ScopeTier(t.Provider, t.Model)) {
		p.Policy, p.pending = &pol, out
	} else if p.saver != nil {
		p.pending = out // State.Save builds Body
	} else {
		p.Body = out.Stringify()
	}
	return p
}

// planAnthropicNative: the passthrough when the mapped model's target is an Anthropic-format provider named by it
// (messagesApi, J4). done false: translate instead.
func (s *State) planAnthropicNative(r *Request, key *ipc.GatewayKeyAuth, body *Value, name string) (Plan, bool) {
	model, ok := s.MapModel(name)
	if !ok {
		return handoff("model: mapping needs the model list"), true
	}
	first, why := s.target(model, key)
	if why == "model: a route" {
		return handoff(why), true
	}
	if first == nil || FormatOf(first.Provider) != "anthropic" || model != first.Name {
		return Plan{}, false
	}
	p := s.nativePlan(r, key, body, name, model, first, UpAnthropicNative, "anthropic", "/messages", "anthropic",
		ShapeAnthropic, "anthropic")
	if p.Handoff != "" {
		return p, true
	}
	addAnthropicHeaders(&p, r)
	req := jsSlice(name, 80) // anthropicPassthrough names the requested model on every line (Decision 27)
	p.Base.Requested = &req
	p.Class = SubMessages
	if p.Stream {
		p.Class = SubMessagesStream
	}
	return p, true
}

// planResponsesNative: the passthrough when the target takes /responses (responsesApi, J5). done false: translate.
func (s *State) planResponsesNative(r *Request, key *ipc.GatewayKeyAuth, body *Value, name string) (Plan, bool) {
	model, ok := s.MapModel(name)
	if !ok {
		return handoff("model: mapping needs the model list"), true
	}
	t, why := s.target(model, key)
	if why == "model: a route" {
		return handoff(why), true
	}
	if t == nil || !NativeResponses(t.Provider) {
		return Plan{}, false
	}
	p := s.nativePlan(r, key, body, name, model, t, UpResponsesNative, "responses", "/responses", "responses",
		ShapeOpenAI, "") // responsesPassthrough has no token saver (R3)
	if p.Handoff != "" {
		return p, true
	}
	if model != name {
		req := jsSlice(name, 80)
		p.Base.Requested = &req
	}
	p.Class = SubResponses
	if p.Stream {
		p.Class = SubResponsesStream
	}
	return p, true
}

// withModel is JSON.stringify({ ...o, [k]: { ...o[k], model } }) when o[k].model is a string (k "" : o itself).
func withModel(o jsv.Value, k, model string) (string, bool) {
	obj := jsv.Obj(o)
	if obj == nil {
		return "", false
	}
	inner := obj
	if k != "" {
		if inner = jsv.Obj(obj.Get(k)); inner == nil {
			return "", false
		}
	}
	if _, ok := inner.Get("model").(string); !ok {
		return "", false
	}
	in := inner.Clone()
	in.Set("model", model)
	out := in
	if k != "" {
		out = obj.Clone()
		out.Set(k, in)
	}
	b, ok := jsv.Stringify(out)
	return b, ok
}

// responsesUsage is index.js responsesUsage(u).
func responsesUsage(u jsv.Value) (int64, int64, int64) {
	n := func(v jsv.Value) int64 {
		f, ok := v.(float64)
		if !ok || f < 0 || f != float64(int64(f)) {
			return 0
		}
		return int64(f)
	}
	return n(field(u, "input_tokens")), n(field(u, "output_tokens")),
		n(field(field(u, "input_tokens_details"), "cached_tokens"))
}

// anthropicTokens is usageOf({ usage: anthropicUsageToOpenAI(u) }).
func anthropicTokens(u jsv.Value) (int64, int64, int64) {
	o, err := translate.AnthropicUsageToOpenAI(u)
	if err != nil {
		return 0, 0, 0
	}
	return tokensJSV(jsv.NewObject().Set("usage", o))
}

// RespondNative is a passthrough's whole answer (or its answer to a stream request the provider refused).
func (s *State) RespondNative(p *Plan, status int, raw []byte, ms float64) Answer {
	text := decodeWHATWG(raw)
	j, err := jsv.Parse([]byte(text))
	if err != nil {
		j = nil
	}
	ok := ok2xx(status)
	rec := p.Base
	rec.Status, rec.Ms = int64(status), ms
	set := func(in, out, cached int64) { rec.Input, rec.Output, rec.Cached = &in, &out, &cached }
	if p.Upstream == UpAnthropicNative {
		if u := field(j, "usage"); jsv.Truthy(j) && jsv.Truthy(u) {
			set(anthropicTokens(u))
		}
		if !ok {
			m := s.mask(jsSlice(text, 200))
			rec.Error = &m
		}
	} else {
		if jsv.Truthy(j) && ok {
			set(responsesUsage(field(j, "usage")))
		}
		if !ok {
			var msg jsv.Value = text
			if e := field(j, "error"); jsv.Truthy(j) && jsv.Truthy(e) && jsv.Truthy(field(e, "message")) {
				msg = field(e, "message")
			}
			m := s.mask(jsSlice(jsv.ToString(msg), 200))
			rec.Error = &m
		}
	}
	rec.Cost = s.costOf(&rec)
	body := text
	switch {
	case ok:
		if b, done := withModel(j, "", p.ClientModel); done {
			body = b
		}
	default:
		body = s.mask(text)
	}
	return Answer{Status: status, Header: answerHeader(), Body: []byte(body), Usage: rec}
}

var responsesDelta = regexp.MustCompile(`^response\.(output_text|function_call_arguments|custom_tool_call_input)\.delta$`)

// deliverNative is a passthrough's answer: a failed call (Anthropic: no line, the route's 502; Responses: the 599 line
// and the 502), a whole answer, or the provider's events re-emitted with the client's model name.
func (s *State) deliverNative(p *Plan, res ProviderResult, elapsed func() float64, w ClientWriter) *ipc.UsageRecord {
	if res.Why != "" {
		msg := s.mask(p.Target.Provider.ID + ": " + res.Why)
		a := shapeError(p.Shape, http.StatusBadGateway, msg, "upstream_error", "", "")
		if p.Upstream == UpResponsesNative {
			rec := p.Base // responsesPassthrough's rec(t, { status: 599, error }): requested and routedTo kept
			rec.Status, rec.Ms, rec.Error, rec.Guard = 599, elapsed(), &msg, nil
			a.Usage, a.NoLine = rec, false
		}
		return writeAnswer(w, a)
	}
	// anthropicPassthrough streams any 2xx (its body may be null: the read throws inside its catch, an empty stream);
	// responsesPassthrough answers whole when fetch gives no body (`!r.body`: 204, 205; review m2).
	whole := !p.Stream || !ok2xx(res.Status) || (p.Upstream == UpResponsesNative && !StreamsBody(res.Status))
	if whole {
		raw, _ := io.ReadAll(res.Body)
		return writeAnswer(w, s.RespondNative(p, res.Status, raw, elapsed()))
	}
	var ttft *float64
	var in, out, cached int64
	au := jsv.NewObject()
	if w.Head(http.StatusOK, sseHeader()) == nil {
		// A panic in reading the events (a port bug) ends the stream as the passthrough's catch does: the line with
		// what was seen (review I3).
		readEvents := func(onEvent func(ev, data string) error) {
			defer func() {
				if v := recover(); v != nil {
					s.recovered("a passthrough's event reader", v)
				}
			}()
			_ = translate.ReadSSE(res.Body, onEvent)
		}
		readEvents(func(ev, data string) error {
			if translateHook != nil {
				translateHook("native-event")
			}
			o, perr := jsv.Parse([]byte(data))
			if perr != nil || !jsv.Truthy(o) {
				o = nil // JSON.parse failed, or a falsy value: passed on as it came, nothing read from it
			}
			var text string
			if p.Upstream == UpAnthropicNative {
				rewritten, done := "", false
				if field(o, "type") == "message_start" {
					rewritten, done = withModel(o, "message", p.ClientModel)
				}
				if done {
					text = "event: " + ev + "\ndata: " + rewritten + "\n\n"
				} else {
					text = "event: " + ev + "\ndata: " + data + "\n\n"
				}
			} else {
				head := "event: " + ev + "\n"
				if ev == "message" {
					head = ""
				}
				if b, done := withModel(o, "response", p.ClientModel); done {
					text = head + "data: " + b + "\n\n"
				} else {
					text = head + "data: " + data + "\n\n"
				}
			}
			if w.Write([]byte(text)) != nil {
				return errWrite
			}
			if o == nil {
				return nil
			}
			if p.Upstream == UpAnthropicNative {
				_ = jsv.Catch(func() {
					switch jsv.Get(o, "type") {
					case "message_start":
						au = spreadObject(jsv.Get(jsv.Get(o, "message"), "usage"))
					case "message_delta":
						au = au.Clone().Assign(spreadObject(jsv.Get(o, "usage")))
					}
					if ttft == nil && jsv.Get(o, "type") == "content_block_delta" {
						ms := elapsed()
						ttft = &ms
					}
				})
				return nil
			}
			if ttft == nil && responsesDelta.MatchString(jsv.ToString(field(o, "type"))) {
				ms := elapsed()
				ttft = &ms
			}
			if r := field(o, "response"); jsv.Truthy(r) && jsv.Truthy(field(r, "usage")) {
				in, out, cached = responsesUsage(field(r, "usage"))
			}
			return nil
		})
	}
	rec := p.Base
	rec.Status, rec.Ms = 200, elapsed()
	if p.Upstream == UpAnthropicNative {
		in, out, cached = anthropicTokens(au)
	}
	rec.Input, rec.Output, rec.Cached = &in, &out, &cached
	rec.TTFT = ipc.Null[float64]()
	if ttft != nil {
		rec.TTFT = ipc.Some(*ttft)
	}
	rec.Cost = s.costOf(&rec)
	return &rec
}

// spreadObject is { ...v } for the values a usage field holds: an object's own keys, nothing for anything else (a
// string's indexed characters are left out: no usage field is read from them).
func spreadObject(v jsv.Value) *jsv.Object {
	if o := jsv.Obj(v); o != nil {
		return o.Clone()
	}
	return jsv.NewObject()
}
