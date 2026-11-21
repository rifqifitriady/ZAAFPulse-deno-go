package engine

// Whole answers of translated calls (P4d): lib/gateway/index.js translated() after proxyChat, when the client asked
// for no stream or the provider answered with an error.

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"
)

// tokensJSV is usageOf over a jsv value: prompt, completion and cached tokens, 0 when missing or not a whole number of
// zero or more (the IPC record holds integers, as tokens()).
func tokensJSV(j jsv.Value) (int64, int64, int64) {
	n := func(v jsv.Value) int64 {
		f, ok := v.(float64)
		if !ok || f < 0 || f != float64(int64(f)) {
			return 0
		}
		return int64(f)
	}
	u := field(j, "usage")
	return n(field(u, "prompt_tokens")), n(field(u, "completion_tokens")),
		n(field(field(u, "prompt_tokens_details"), "cached_tokens"))
}

// field is v?.[k]: Undefined for null, undefined and non-objects (no throw).
func field(v jsv.Value, k string) jsv.Value {
	if o := jsv.Obj(v); o != nil {
		return o.Get(k)
	}
	return jsv.Undefined
}

// costOf is the line's cost at list price of the routed model.
func (s *State) costOf(rec *ipc.UsageRecord) int64 {
	model := rec.Model
	if rec.RoutedTo != nil {
		model = *rec.RoutedTo
	}
	var in, out, cached int64
	if rec.Input != nil {
		in = *rec.Input
	}
	if rec.Output != nil {
		out = *rec.Output
	}
	if rec.Cached != nil {
		cached = *rec.Cached
	}
	return s.Pricer.Cost(model, float64(in), float64(out), float64(cached))
}

// providerErrorText is translated()'s message for a provider's non-2xx answer: (j && j.error && (j.error.message ||
// j.error)) || text || `HTTP ${status}`, as a string, its first 300 UTF-16 units, masked.
func (s *State) providerErrorText(status int, text string) string {
	var msg jsv.Value = jsv.Undefined
	if j, err := jsv.Parse([]byte(text)); err == nil && jsv.Truthy(j) {
		if e := field(j, "error"); jsv.Truthy(e) {
			msg = jsv.Or(field(e, "message"), e)
		}
	}
	if !jsv.Truthy(msg) {
		msg = text
	}
	if !jsv.Truthy(msg) {
		msg = "HTTP " + NumberString(float64(status))
	}
	return s.mask(jsSlice(jsv.ToString(msg), 300))
}

// droppedHeader adds x-zp-dropped when the translation left features out.
func droppedHeader(h http.Header, p *Plan) http.Header {
	if len(p.Dropped) > 0 {
		h.Set("X-Zp-Dropped", jsSlice(strings.Join(p.Dropped, ", "), 500))
	}
	return h
}

// RespondTranslated is translated()'s whole answer for the provider's status and body (for the Anthropic leg, the
// body callChat made of it), at ms after the start.
func (s *State) RespondTranslated(p *Plan, status int, raw []byte, ms float64) Answer {
	rec := p.Base
	rec.Status, rec.Ms = int64(status), ms
	if !ok2xx(status) {
		msg := s.providerErrorText(status, decodeWHATWG(raw))
		rec.Error = &msg
		rec.Cost = s.costOf(&rec)
		return Answer{Status: status, Header: answerHeader(), Body: providerError(p.Format, status, msg), Usage: rec}
	}
	rec.Status = 200
	j, err := jsv.Parse([]byte(decodeWHATWG(raw)))
	if err != nil {
		j = jsv.NewObject() // r.json().catch(() => ({}))
	}
	in, out, cached := tokensJSV(j)
	rec.Input, rec.Output, rec.Cached = &in, &out, &cached
	rec.Cost = s.costOf(&rec)
	whole, err := s.wholeAnswer(p, j)
	if err != nil {
		// out.whole threw after the line was written: the route's 502 with the error's text, in the client's shape.
		a := shapeError(p.Shape, http.StatusBadGateway, s.mask(err.Error()), "upstream_error", "", "")
		a.Usage, a.NoLine = rec, false
		return a
	}
	b, _ := jsv.Stringify(whole)
	return Answer{Status: 200, Header: droppedHeader(answerHeader(), p), Body: []byte(b), Usage: rec}
}

// deliverTranslated is translated() for a whole answer, or a provider error on a stream request.
func (s *State) deliverTranslated(p *Plan, res ProviderResult, elapsed func() float64, w ClientWriter) *ipc.UsageRecord {
	if res.Why != "" {
		return writeAnswer(w, s.Failed(p, res.Why, elapsed()))
	}
	raw, _ := io.ReadAll(res.Body)
	if ok2xx(res.Status) && p.Upstream == UpAnthropic && !p.converted {
		conv, why := anthropicWhole(p.Env, raw, p.ClientModel, s.onPanic("the Anthropic leg's whole answer"))
		if why != "" {
			return writeAnswer(w, s.Failed(p, why, elapsed())) // P4d Decision 26a
		}
		raw = conv
	}
	return writeAnswer(w, s.RespondTranslated(p, res.Status, raw, elapsed()))
}

// wholeAnswer is out.whole(j); a panic in it (a port bug) is an error, as JS's TypeError reaches the route's catch
// after the line was written (review I3).
func (s *State) wholeAnswer(p *Plan, j jsv.Value) (whole *jsv.Object, err error) {
	defer func() {
		if v := recover(); v != nil {
			s.recovered("a translated whole answer", v)
			whole, err = nil, fmt.Errorf("%s", panicText(v))
		}
	}()
	if translateHook != nil {
		translateHook("whole")
	}
	switch p.Format {
	case "anthropic":
		return p.Env.OpenAIToAnthropic(j, p.ClientModel)
	case "responses":
		return p.Env.OpenAIToResponses(j, p.ClientModel, p.Custom)
	}
	return p.Env.OpenAIToGemini(j, p.ClientModel)
}
