package engine

// /v1/embeddings on the go engine (P4d): lib/gateway/index.js embeddings. The provider's bytes go back as they came
// (masked when not 2xx); the line has the input tokens, no format, no stream and no error text; a failed call writes
// no line (the route's 502). The call is not cancelled when the client leaves.

import (
	"io"
	"net/http"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate"
)

// PlanEmbeddings is /v1/embeddings (J8).
func (s *State) PlanEmbeddings(r *Request, now int64) Plan {
	key, body, why := s.admit(r, now, "llm")
	if why != "" {
		return handoff(why)
	}
	m := body.Get("model")
	if m == nil || m.Kind != String || m.S == "" {
		return handoff("model: missing")
	}
	model := m.S
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
	kind := "embeddings"
	p := Plan{Key: key, Target: t, Model: model, ClientModel: model, Upstream: UpEmbeddings, Class: SubEmbeddings,
		Format: "openai", Shape: ShapeOpenAI, GuardFormat: "embeddings", Env: translate.Default, Detached: true,
		URL: t.Provider.BaseURL + "/embeddings", Header: providerHeader(t, cred), build: passBuild,
		Base: ipc.UsageRecord{KeyID: key.ID, Provider: t.Provider.ID, Model: model, Kind: &kind}}
	b := s.base(key, t, model, r) // the client tag and local flag
	p.Base.Client, p.Base.Local = b.Client, b.Local
	out := body.Clone()
	out.Set("model", Str(t.Model))
	if pol := PolicyFor(s.Gateway.Guardrails, key); checksApply(pol, ScopeTier(t.Provider, t.Model)) {
		p.Policy, p.pending = &pol, out
	} else {
		p.Body = out.Stringify()
	}
	return p
}

// RespondEmbeddings is embeddings' answer for the provider's status and body.
func (s *State) RespondEmbeddings(p *Plan, status int, raw []byte, ms float64) Answer {
	text := decodeWHATWG(raw)
	rec := p.Base
	rec.Status, rec.Ms = int64(status), ms
	var in int64
	if j, err := Parse([]byte(text)); err == nil {
		in, _, _ = tokens(j)
	}
	out := int64(0)
	rec.Input, rec.Output = &in, &out
	rec.Cost = s.costOf(&rec)
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	for _, c := range CORS {
		h.Set(c[0], c[1])
	}
	body := text
	if !ok2xx(status) {
		body = s.mask(text)
	}
	return Answer{Status: status, Header: h, Body: []byte(body), Usage: rec}
}

// deliverEmbeddings: a failed call is the route's 502 with no line.
func (s *State) deliverEmbeddings(p *Plan, res ProviderResult, elapsed func() float64, w ClientWriter) *ipc.UsageRecord {
	if res.Why != "" {
		return writeAnswer(w, shapeError(ShapeOpenAI, http.StatusBadGateway, s.mask(p.Target.Provider.ID+": "+res.Why),
			"upstream_error", "", ""))
	}
	raw, _ := io.ReadAll(res.Body)
	return writeAnswer(w, s.RespondEmbeddings(p, res.Status, raw, elapsed()))
}
