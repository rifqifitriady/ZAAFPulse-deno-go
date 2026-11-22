package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A count_tokens call waits 15 s at most for the provider (AbortSignal.timeout(15000)), then answers the estimate.
func TestCountTimeout(t *testing.T) {
	s := xlateState(t)
	p := s.PlanCount(xlateReq(t, "/v1/messages/count_tokens", `{"model":"an/claude-x","messages":[]}`), fixtureNow)
	if p.Handoff != "" || p.Upstream != UpCount || p.Timeout != CountTimeout {
		t.Fatalf("%q %q %v", p.Handoff, p.Upstream, p.Timeout)
	}
	hold := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-hold:
		case <-r.Context().Done():
		}
	}))
	defer slow.Close()
	defer close(hold) // first: the held handler returns, then the server closes
	p.URL, p.Timeout = slow.URL, 50*time.Millisecond
	resp, why := NewUpstream(func(string) string { return "" }).Start(context.Background(), &p)
	if resp != nil || why != "timed out" {
		t.Fatalf("%v %q", resp, why)
	}
	w := &recordWriter{}
	if line := s.Deliver(&p, ProviderResult{Why: why}, func() float64 { return 1 }, w); line != nil ||
		!strings.HasPrefix(w.body.String(), `{"input_tokens":`) {
		t.Fatalf("%v %q", line, w.body.String())
	}
}

// count_tokens without a model, or with a model whose target is not native Anthropic, is the engine's own estimate.
func TestCountEstimatePlans(t *testing.T) {
	s := xlateState(t)
	for _, body := range []string{`{"messages":[]}`, `{"model":"oa/m1","messages":[]}`, `{"model":"","messages":[]}`} {
		p := s.PlanCount(xlateReq(t, "/v1/messages/count_tokens", body), fixtureNow)
		if p.Handoff != "" || p.Upstream != UpNone || p.Self == nil || !p.Self.NoLine {
			t.Errorf("%s: %+v", body, p)
		}
	}
	// A route (P4h): one take, then its first leg in order decides; oa/m1 is an estimate leg, op/gpt-6.1 too.
	p := s.PlanCount(xlateReq(t, "/v1/messages/count_tokens", `{"model":"router/x"}`), fixtureNow)
	if p.Handoff != "" || p.Route == nil || p.Takes != 1 || p.Self == nil || len(p.Legs) != 2 ||
		p.Legs[0].Upstream != UpNone || p.Legs[0].Target == nil {
		t.Fatalf("%+v", p)
	}
}

// /v1/embeddings does not follow the client's context (J8), and a model that is not a string is the control plane's.
func TestEmbeddingsPlan(t *testing.T) {
	s := xlateState(t)
	p := s.PlanEmbeddings(xlateReq(t, "/v1/embeddings", `{"model":"oa/e1","input":"x"}`), fixtureNow)
	if p.Handoff != "" || !p.Detached || p.Base.Format != nil || p.Base.Stream != nil || *p.Base.Kind != "embeddings" {
		t.Fatalf("%+v", p)
	}
	// A route (P4h): one take, the first allowed leg, no fallback.
	if p := s.PlanEmbeddings(xlateReq(t, "/v1/embeddings", `{"model":"router/x","input":"x"}`), fixtureNow); p.Handoff != "" ||
		p.Route == nil || p.Takes != 1 || len(p.Legs) != 2 || !p.Legs[0].Detached || p.Legs[0].Base.RoutedTo != nil {
		t.Fatalf("%+v", p)
	}
	for _, body := range []string{`{"input":"x"}`, `{"model":["oa/e1"]}`} {
		if p := s.PlanEmbeddings(xlateReq(t, "/v1/embeddings", body), fixtureNow); p.Handoff == "" {
			t.Errorf("%s served", body)
		}
	}
}
