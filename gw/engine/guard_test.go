package engine

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/guard"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"
)

// guardedPlan is the basic chat fixture's plan with the secrets check in redact mode.
func guardedPlan(t *testing.T) (*State, Plan) {
	t.Helper()
	var base chatCase
	for _, f := range loadFixtures(t, "chat") {
		if strings.HasSuffix(f.Name, "-basic.json") {
			_ = json.Unmarshal(f.In, &base)
		}
	}
	s := stateFor(t, base)
	s.Gateway.Guardrails.Secrets = "redact"
	p := s.PlanChat(requestFor(base), fixtureNow)
	if p.Handoff != "" || p.Policy == nil {
		t.Fatalf("not a guarded plan: %q", p.Handoff)
	}
	return s, p
}

func withCheckBody(t *testing.T, f func(jsv.Value, guard.Policy, string) (guard.Result, error)) {
	t.Helper()
	was := checkBody
	checkBody = f
	t.Cleanup(func() { checkBody = was })
}

// Any error out of the checks that is not a block is a 500 refusal, never a pass (P4e entry condition 4).
func TestGuardFailsClosed(t *testing.T) {
	withCheckBody(t, func(jsv.Value, guard.Policy, string) (guard.Result, error) { return guard.Result{}, errors.New("boom") })
	s, p := guardedPlan(t)
	a := s.Guard(&p, 1)
	if a == nil || a.Status != 500 || string(a.Body) != `{"error":{"message":"guardrails failed","type":"server_error"}}` {
		t.Fatalf("got %+v", a)
	}
	if a.Usage.Status != 500 || a.Usage.Error == nil || *a.Usage.Error != "guardrails failed" || a.Usage.Guard != nil {
		t.Fatalf("line %+v", a.Usage)
	}
	if p.Body != nil {
		t.Fatal("a body was built for a refused call")
	}
}

// An overrun is a 400 guardrail_blocked with no hits, and the plan names the check (Go-only deviation, notes 13/26).
func TestGuardOverrunBlocks(t *testing.T) {
	withCheckBody(t, func(jsv.Value, guard.Policy, string) (guard.Result, error) {
		return guard.Result{}, &guard.Block{Overrun: "injection"}
	})
	s, p := guardedPlan(t)
	a := s.Guard(&p, 1)
	if a == nil || a.Status != 400 || !strings.Contains(string(a.Body), `"code":"guardrail_blocked"`) ||
		!strings.Contains(string(a.Body), "did not finish within its limit") {
		t.Fatalf("got %+v", a)
	}
	if g := a.Usage.Guard; g == nil || g.Action != "blocked" || g.Hits == nil || len(g.Hits) != 0 {
		t.Fatalf("line guard %+v", a.Usage.Guard)
	}
	if b, _ := json.Marshal(a.Usage.Guard); !strings.Contains(string(b), `"hits":[]`) {
		t.Fatalf("hits not an empty list: %s", b)
	}
	if p.Overrun != "injection" {
		t.Fatalf("overrun %q", p.Overrun)
	}
}

// A check that failed in log mode: the call goes on, its line says error, and the failures are kept for the log.
func TestGuardLogFailuresKept(t *testing.T) {
	withCheckBody(t, func(v jsv.Value, _ guard.Policy, _ string) (guard.Result, error) {
		return guard.Result{Body: v, Action: "logged", Hits: []guard.Hit{}, Error: true,
			Failures: []guard.Failure{{Check: "secrets", Count: 2, Err: "boom"}}}, nil
	})
	s, p := guardedPlan(t)
	if a := s.Guard(&p, 1); a != nil {
		t.Fatalf("refused: %+v", a)
	}
	if g := p.Base.Guard; g == nil || g.Action != "logged" || g.Error == nil || !*g.Error {
		t.Fatalf("guard %+v", p.Base.Guard)
	}
	if len(p.Failures) != 1 || p.Failures[0].Count != 2 || p.Body == nil {
		t.Fatalf("failures %+v body %s", p.Failures, p.Body)
	}
}

// A failed provider call's line carries no guardrail outcome; the answered call's line does (J6).
func TestFailedLineHasNoGuard(t *testing.T) {
	s, p := guardedPlan(t)
	p.pending.Get("messages").A[0].Set("content", Str("key "+strings.Join([]string{"sk", "ant", "api03",
		"AbCdEfGhIjKlMnOpQrStUvWxYz0123456789abcd"}, "-")))
	if a := s.Guard(&p, 1); a != nil {
		t.Fatalf("refused: %+v", a)
	}
	if p.Base.Guard == nil || p.Base.Guard.Action != "redacted" {
		t.Fatalf("guard %+v", p.Base.Guard)
	}
	if s.Failed(&p, "connect: refused", 1).Usage.Guard != nil {
		t.Fatal("the 599 line carries the outcome")
	}
	if s.Respond(&p, 200, []byte(`{"id":"c1"}`), 1).Usage.Guard == nil {
		t.Fatal("the answered line lacks the outcome")
	}
}

// The conversion to the guard's values and back writes every fixture body as it was.
func TestConversionRoundTrip(t *testing.T) {
	n := 0
	for _, f := range loadFixtures(t, "chat") {
		var c struct {
			Request struct{ Body string } `json:"request"`
		}
		if json.Unmarshal(f.In, &c) != nil || c.Request.Body == "" {
			continue
		}
		v, err := Parse([]byte(c.Request.Body))
		if err != nil {
			continue
		}
		if got := fromJSV(toJSV(v)).Stringify(); string(got) != string(v.Stringify()) {
			t.Errorf("%s: %s, want %s", f.Name, got, v.Stringify())
		}
		n++
	}
	for name, io := range loadGuardFixtures(t) {
		var c guardCase
		_ = json.Unmarshal(io[0], &c)
		v, err := Parse([]byte(c.Request.Body))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := fromJSV(toJSV(v)).Stringify(); string(got) != string(v.Stringify()) {
			t.Errorf("%s: %s, want %s", filepath.Base(name), got, v.Stringify())
		}
		n++
	}
	if n < 40 {
		t.Fatalf("only %d bodies", n)
	}
}
