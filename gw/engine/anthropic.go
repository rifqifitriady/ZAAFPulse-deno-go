package engine

// The Anthropic provider leg (P4d): lib/gateway/index.js callChat for a provider whose format is "anthropic". The chat
// body goes to <base>/messages through openAIToAnthropicRequest; a whole answer comes back through
// anthropicResponseToOpenAI and a stream through anthropicEventsToOpenAI as a synthesized OpenAI event stream. chat,
// messages, responses and Gemini calls to such a provider all take this leg.

import (
	"bytes"
	"errors"
	"io"
	"net/http"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"
)

// anthropicBuild is callChat's request: { ...rest, model: t.model } without stream_options, through
// openAIToAnthropicRequest, as JSON.stringify writes it (no adaptChat). nil when the translation throws.
func anthropicBuild(p *Plan, body *Value) []byte {
	b, err := anthropicTranslate(p.Target, body)
	if err != nil {
		p.buildErr = err // the guardrails' redacted body does not translate: answered without a provider call (m3)
	}
	return b
}

// anthropicRequest is anthropicBuild for a target (nil when the translation throws).
func anthropicRequest(t *Target, body *Value) []byte {
	b, _ := anthropicTranslate(t, body)
	return b
}

func anthropicTranslate(t *Target, body *Value) ([]byte, error) {
	out := body.Clone()
	out.Delete("stream_options")
	out.Set("model", Str(t.Model))
	req, err := translate.OpenAIToAnthropicRequest(toJSV(out))
	if err != nil {
		return nil, err
	}
	b, ok := jsv.Stringify(req)
	if !ok {
		return nil, errors.New("the request cannot be written as JSON")
	}
	return []byte(b), nil
}

// buildRefusal is callChat's throw when the body cannot be translated for the provider (review m3): a translation 400
// is rethrown by proxyChat (the route's 400 in the client's shape, no line); any other error is a failed target (the 599
// line with the error's text, then the 502 with it masked).
func (s *State) buildRefusal(p *Plan, ms float64) Answer {
	var bad *translate.BadRequest
	if errors.As(p.buildErr, &bad) {
		return shapeError(p.Shape, http.StatusBadRequest, bad.Message, "invalid_request_error", "", "")
	}
	msg := p.buildErr.Error()
	rec := p.Base
	rec.RoutedTo, rec.Guard, rec.Requested, rec.SavedTokens = nil, nil, nil, nil
	rec.Status, rec.Ms, rec.Error = 599, ms, &msg
	rec.Input, rec.Output, rec.Cached, rec.Cost = nil, nil, nil, 0
	a := shapeError(p.Shape, http.StatusBadGateway, s.mask(msg), "upstream_error", "", "")
	a.Usage, a.NoLine = rec, false
	return a
}

// planAnthropicLeg turns a plan's provider request into the Anthropic leg. The translation is tried once here: one
// that throws would be callChat's throw (a 599 line and a 502); it is handed off instead, and JS answers it.
func planAnthropicLeg(p *Plan, cred string, pending *Value) string {
	if anthropicRequest(p.Target, pending.Clone()) == nil {
		return "translate: the request cannot be translated for an Anthropic provider"
	}
	p.Upstream = UpAnthropic
	p.URL = p.Target.Provider.BaseURL + "/messages"
	p.Header = providerHeader(p.Target, cred)
	p.build = anthropicBuild
	return ""
}

// anthropicWhole is callChat's whole answer: the provider's JSON through anthropicResponseToOpenAI(j, body.model), as
// the Response it becomes (JSON.stringify). ok false: r.json() or the conversion throws (callChat throws: 599, 502).
func anthropicWhole(env *translate.Env, raw []byte, clientModel string) (out []byte, why string) {
	defer func() {
		if v := recover(); v != nil {
			out, why = nil, panicText(v) // callChat's throw: the 599 line and the 502 (review I3)
		}
	}()
	j, err := jsv.Parse([]byte(decodeWHATWG(raw)))
	if err != nil {
		return nil, "the provider's answer is not JSON"
	}
	if translateHook != nil {
		translateHook("anthropic-whole")
	}
	o, err := env.AnthropicResponseToOpenAI(j, clientModel)
	if err != nil {
		return nil, err.Error()
	}
	b, _ := jsv.Stringify(o)
	return []byte(b), ""
}

// AnthropicToChatText is callChat's synthesized stream: readSse over the provider's events, each parsed (one that does
// not parse is skipped) and converted, one "data: <JSON>\n\n" per chunk, then "data: [DONE]\n\n" after a clean end. Any
// error (the provider breaking off, a conversion that throws) ends it without [DONE] and without an error, as the JS
// catch does. The reader must be closed:
// closing it stops the conversion (its next write fails).
func AnthropicToChatText(model string, body io.Reader, env *translate.Env) io.ReadCloser {
	pr, pw := io.Pipe()
	conv := env.NewAnthropicEvents(model) // now: its id is drawn when callChat returns, before the client's writer
	go func() {
		// A panic in the conversion (a port bug) ends the stream as callChat's catch does: no [DONE], no error; it
		// must not take the gateway down from this goroutine (review I3).
		defer func() {
			if v := recover(); v != nil {
				_ = pw.Close()
			}
		}()
		err := translate.ReadSSE(body, func(_, data string) error {
			o, err := jsv.Parse([]byte(data))
			if err != nil {
				return nil
			}
			if translateHook != nil {
				translateHook("anthropic-event")
			}
			chunks, err := conv.Next(o)
			if err != nil {
				return err
			}
			for _, c := range chunks {
				s, _ := jsv.Stringify(c)
				if _, werr := io.WriteString(pw, "data: "+s+"\n\n"); werr != nil {
					return werr
				}
			}
			return nil
		})
		if err == nil {
			_, _ = io.WriteString(pw, "data: [DONE]\n\n") // only after a clean end: a throw skips it
		}
		_ = pw.Close()
	}()
	return pr
}

// noBodyStream is callChat's synthesized stream for a 2xx without a body (204, 205): readSse(null) throws inside its
// try, so the stream closes empty, without [DONE] (review m2).
func noBodyStream() io.ReadCloser { return io.NopCloser(bytes.NewReader(nil)) }

// anthropicText is the synthesized stream for a provider's answer of status.
func anthropicText(status int, model string, body io.Reader, env *translate.Env) io.ReadCloser {
	if !StreamsBody(status) {
		return noBodyStream()
	}
	return AnthropicToChatText(model, body, env)
}

// decodeWHATWG is Response.text(): a leading byte order mark dropped, each invalid sequence one U+FFFD per maximal
// subpart (validText, P4c).
func decodeWHATWG(b []byte) string { return validText(bytes.TrimPrefix(b, utf8BOM)) }
