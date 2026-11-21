package engine

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/guard"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"
)

// guardFixture plans a chat-guard fixture: its state, plan, the JS's output and the provider's answer.
func guardFixture(t *testing.T, name string) (*State, Plan, guardOut, guardCase) {
	t.Helper()
	io, ok := loadGuardFixtures(t)[name+".json"]
	if !ok {
		t.Fatalf("no fixture %s", name)
	}
	var c guardCase
	var want guardOut
	_ = json.Unmarshal(io[0], &c)
	_ = json.Unmarshal(io[1], &want)
	c.Config.Gen, c.Secrets.Gen = 1, 1
	s := NewState(&c.Config, &c.Secrets).WithSnapshot(c.Snapshot)
	h := http.Header{}
	for k, v := range c.Request.Headers {
		h.Set(k, v)
	}
	p := s.PlanChat(&Request{Method: "POST", RawPath: "/v1/chat/completions", Header: h, Host: "127.0.0.1:4777",
		Peer: "127.0.0.1:50000", Body: []byte(c.Request.Body)}, fixtureNow)
	if p.Handoff != "" {
		t.Fatalf("%s handed off: %s", name, p.Handoff)
	}
	return s, p, want, c
}

// jsCapture is what the control plane's capture holds for a fixture's JS answer (cost in USD, as the usage log has it).
func jsCapture(t *testing.T, want guardOut, prov *ipc.UsageRecord, c guardCase) *Capture {
	t.Helper()
	cp := &Capture{Client: &CaptureClient{Status: want.Client.Status, Body: want.Client.Body}}
	if string(want.Usage) != "null" {
		_ = json.Unmarshal(want.Usage, &cp.Usage)
		if n, ok := cp.Usage["cost"].(float64); ok {
			cp.Usage["cost"] = n / 1e9
		}
	}
	if want.Request != nil {
		body := want.Request.Body
		cp.ProviderRequests = append(cp.ProviderRequests, struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
			Body    *string           `json:"body"`
		}{want.Request.URL, want.Request.Headers, &body})
		cp.ProviderResponse = &ProviderAnswer{Status: c.Provider.Status, Body: c.Provider.Body}
	}
	return cp
}

func TestCompareRefusalSameAndDiff(t *testing.T) {
	s, p, want, c := guardFixture(t, "secrets-block")
	ref := s.Guard(&p, 0)
	if ref == nil {
		t.Fatal("not refused")
	}
	cp := jsCapture(t, want, nil, c)
	if l := s.CompareRefusal("chat", &p, ref, cp); l.Outcome != "same" {
		t.Fatalf("got %+v", l)
	}
	cp.Client.Body = `{"error":{"message":"other"}}`
	if l := s.CompareRefusal("chat", &p, ref, cp); l.Outcome != "diff" || l.Diffs[0].Field != "client.body" {
		t.Fatalf("got %+v", l)
	}
	// An older control plane captures no body: status and line only.
	cp.Client.Body = ""
	if l := s.CompareRefusal("chat", &p, ref, cp); l.Outcome != "same" {
		t.Fatalf("got %+v", l)
	}
}

func TestCompareRefusalBudget(t *testing.T) {
	s, p, want, c := guardFixture(t, "budget-model-exceeded")
	ref, _ := s.Budget(&p, fixtureNow, ipc.StaleAfterMs, nil)
	if ref == nil {
		t.Fatal("not refused")
	}
	if l := s.CompareRefusal("chat", &p, ref, jsCapture(t, want, nil, c)); l.Outcome != "same" {
		t.Fatalf("402 same: %+v", l)
	}
	// The control plane called the provider where the engine's snapshot refused: a lag.
	_, p2, want2, c2 := guardFixture(t, "budget-model-ok")
	if l := s.CompareRefusal("chat", &p2, ref, jsCapture(t, want2, nil, c2)); l.Outcome != "skipped" || l.Reason != budgetLag {
		t.Fatalf("lag: %+v", l)
	}
	// The control plane refused 402 where the engine would call the provider: a lag too.
	s3, p3, _, _ := guardFixture(t, "budget-model-ok")
	cp := jsCapture(t, want, nil, c)
	if l := s3.Compare("chat", &p3, cp); l.Outcome != "skipped" || l.Reason != budgetLag {
		t.Fatalf("reverse lag: %+v", l)
	}
}

// An overrun (Go-only) where the JS called the provider is a real diff.
func TestCompareRefusalOverrunIsADiff(t *testing.T) {
	was := checkBody
	checkBody = func(jsv.Value, guard.Policy, string) (guard.Result, error) {
		return guard.Result{}, &guard.Block{Overrun: "injection"}
	}
	t.Cleanup(func() { checkBody = was })
	s, p, want, c := guardFixture(t, "secrets-redact")
	ref := s.Guard(&p, 0)
	if l := s.CompareRefusal("chat", &p, ref, jsCapture(t, want, nil, c)); l.Outcome != "diff" {
		t.Fatalf("got %+v", l)
	}
}

// A redacted call is compared like any other once Guard has built its body.
func TestCompareRedactedSame(t *testing.T) {
	s, p, want, c := guardFixture(t, "secrets-redact")
	if ref := s.Guard(&p, 0); ref != nil {
		t.Fatal("refused")
	}
	if l := s.Compare("chat", &p, jsCapture(t, want, nil, c)); l.Outcome != "same" {
		t.Fatalf("got %+v", l)
	}
}

// Only a budget's 402 is the snapshot's lag; a price-rule 402 the other side did not answer is a diff (review m2).
func TestPriceRuleDisagreementIsADiff(t *testing.T) {
	s, p, want, c := guardFixture(t, "price-rule-refuses-unpriced")
	ref, _ := s.Budget(&p, fixtureNow, ipc.StaleAfterMs, nil)
	if ref == nil || ref.Status != 402 {
		t.Fatal("not refused")
	}
	// Go's price 402, JS called the provider.
	_, _, served, sc := guardFixture(t, "price-rule-priced-passes")
	if l := s.CompareRefusal("chat", &p, ref, jsCapture(t, served, nil, sc)); l.Outcome != "diff" {
		t.Fatalf("go price 402 vs js served: %+v", l)
	}
	// JS's price 402, Go would serve.
	s2, p2, _, _ := guardFixture(t, "price-rule-priced-passes")
	if ref2 := s2.Guard(&p2, 0); ref2 != nil {
		t.Fatal("refused")
	}
	if l := s2.Compare("chat", &p2, jsCapture(t, want, nil, c)); l.Outcome != "diff" {
		t.Fatalf("js price 402 vs go served: %+v", l)
	}
}
