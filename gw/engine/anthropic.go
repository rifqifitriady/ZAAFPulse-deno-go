package engine

// The Anthropic provider leg (P4d): lib/gateway/index.js callChat for a provider whose format is "anthropic". The chat
// body goes to <base>/messages through openAIToAnthropicRequest; a whole answer comes back through
// anthropicResponseToOpenAI and a stream through anthropicEventsToOpenAI as a synthesized OpenAI event stream. chat,
// messages, responses and Gemini calls to such a provider all take this leg.

import (
	"bytes"
	"io"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"
)

// anthropicBuild is callChat's request: { ...rest, model: t.model } without stream_options, through
// openAIToAnthropicRequest, as JSON.stringify writes it (no adaptChat). nil when the translation throws.
func anthropicBuild(t *Target, body *Value) []byte {
	out := body.Clone()
	out.Delete("stream_options")
	out.Set("model", Str(t.Model))
	req, err := translate.OpenAIToAnthropicRequest(toJSV(out))
	if err != nil {
		return nil
	}
	b, ok := jsv.Stringify(req)
	if !ok {
		return nil
	}
	return []byte(b)
}

// planAnthropicLeg turns a plan's provider request into the Anthropic leg. The translation is tried once here: one
// that throws would be callChat's throw (a 599 line and a 502); it is handed off instead, and JS answers it.
func planAnthropicLeg(p *Plan, cred string, pending *Value) string {
	if anthropicBuild(p.Target, pending.Clone()) == nil {
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
func anthropicWhole(env *translate.Env, raw []byte, clientModel string) ([]byte, string) {
	j, err := jsv.Parse([]byte(decodeWHATWG(raw)))
	if err != nil {
		return nil, "the provider's answer is not JSON"
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
	go func() {
		conv := env.NewAnthropicEvents(model)
		err := translate.ReadSSE(body, func(_, data string) error {
			o, err := jsv.Parse([]byte(data))
			if err != nil {
				return nil
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

// decodeWHATWG is Response.text(): a leading byte order mark dropped, each invalid sequence one U+FFFD per maximal
// subpart (validText, P4c).
func decodeWHATWG(b []byte) string { return validText(bytes.TrimPrefix(b, utf8BOM)) }
