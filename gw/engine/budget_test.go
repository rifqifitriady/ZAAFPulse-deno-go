package engine

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

const T = int64(fixtureNow)

func basicState(t *testing.T) (*State, *Request) {
	t.Helper()
	var base chatCase
	for _, f := range loadFixtures(t, "chat") {
		if strings.HasSuffix(f.Name, "-basic.json") {
			_ = json.Unmarshal(f.In, &base)
		}
	}
	return stateFor(t, base), requestFor(base)
}

func f64(v float64) *float64 { return &v }

func keyBudget(state string, spent, amount float64) ipc.BudgetView {
	return ipc.BudgetView{ID: "kb", Name: "Key k1", Scope: ipc.BudgetScope{Dim: "key", Value: "k1"}, Period: "month",
		Enforce: "block", State: state, SpentUSD: f64(spent), AmountUSD: f64(amount)}
}

func plan(t *testing.T, s *State, r *Request) Plan {
	t.Helper()
	p := s.PlanChat(r, T)
	if p.Handoff != "" {
		t.Fatalf("handed off: %s", p.Handoff)
	}
	return p
}

// The call after the one that used a budget up is refused at once, from the engine's own line; the next snapshot holds
// that spend and refuses through Decide, and the own line is dropped (P4e Decision 15).
func TestOwnSpendRefusesTheNextCall(t *testing.T) {
	s, r := basicState(t)
	s = s.WithSnapshot(&ipc.BudgetSnapshot{At: T, Budgets: []ipc.BudgetView{keyBudget("ok", 0, 0.002)}})
	p := plan(t, s, r)
	sp := &Spend{}
	if a, _ := s.Budget(&p, T+5, StaleMs, sp.Since(T)); a != nil {
		t.Fatalf("refused before any spend: %s", a.Body)
	}
	rec := p.Base
	rec.Ts, rec.Cost = T+10, 2_000_000
	x, ok := spentOf(rec)
	if !ok {
		t.Fatal("no spend from a priced line")
	}
	sp.Add(x)
	a, _ := s.Budget(&p, T+20, StaleMs, sp.Since(T))
	if a == nil || a.Status != 402 || !a.NoLine ||
		string(a.Body) != `{"error":{"message":"budget \"Key k1\" is used up for this month","type":"insufficient_quota"}}` {
		t.Fatalf("got %+v", a)
	}
	s2 := s.WithSnapshot(&ipc.BudgetSnapshot{At: T + 30, Budgets: []ipc.BudgetView{keyBudget("exceeded", 0.002, 0.002)}})
	if own := sp.Since(T + 30); len(own) != 0 {
		t.Fatalf("own lines kept past the snapshot: %v", own)
	}
	if a, _ := s2.Budget(&p, T+40, StaleMs, sp.Since(T+30)); a == nil || a.Status != 402 {
		t.Fatalf("the exceeded snapshot did not refuse: %+v", a)
	}
}

// A snapshot older than staleMs passes the call, unchecked; exactly staleMs old is still fresh.
func TestStaleSnapshotPassesUnchecked(t *testing.T) {
	s, r := basicState(t)
	s = s.WithSnapshot(&ipc.BudgetSnapshot{At: T, Budgets: []ipc.BudgetView{keyBudget("ok", 0, 1)}})
	p := plan(t, s, r)
	if a, u := s.Budget(&p, T+5001, 5000, nil); a != nil || !u {
		t.Fatalf("stale: %v %v", a, u)
	}
	if a, u := s.Budget(&p, T+5000, 5000, nil); a != nil || u {
		t.Fatalf("fresh: %v %v", a, u)
	}
}

// A budget the snapshot shows exceeded is refused even when the snapshot is stale (S7).
func TestExceededRefusedEvenWhenStale(t *testing.T) {
	s, r := basicState(t)
	s = s.WithSnapshot(&ipc.BudgetSnapshot{At: T, Budgets: []ipc.BudgetView{keyBudget("exceeded", 2, 1)}})
	p := plan(t, s, r)
	if a, _ := s.Budget(&p, T+60_000, 5000, nil); a == nil || a.Status != 402 {
		t.Fatalf("got %+v", a)
	}
}

// No snapshot yet: unchecked; a key with budgetUSD still needs a priced or private model.
func TestNoSnapshotIsUnchecked(t *testing.T) {
	s, r := basicState(t)
	s.Snapshot, s.SnapshotAt = nil, 0
	p := plan(t, s, r)
	if a, u := s.Budget(&p, T, StaleMs, nil); a != nil || !u {
		t.Fatalf("got %v %v", a, u)
	}
	b := 5.0
	s.Keys.list[0].BudgetUSD = &b
	r.Body = []byte(`{"model":"oa/nopr","messages":[]}`)
	p = plan(t, s, r)
	a, _ := s.Budget(&p, T, StaleMs, nil)
	if a == nil || a.Status != 402 || !strings.Contains(string(a.Body), "oa/nopr has no known price") {
		t.Fatalf("got %+v", a)
	}
}

func TestSpendIsBounded(t *testing.T) {
	sp := &Spend{}
	for i := range 70000 {
		sp.Add(Spent{Ts: int64(i) + 1, USD: 1})
	}
	if n := len(sp.Since(0)); n > maxSpent {
		t.Fatalf("%d lines kept", n)
	}
}

// The budget rows that used to hand a call to the control plane are the engine's now.
func TestBudgetsNoLongerHandOff(t *testing.T) {
	for _, edit := range []func(s *State){
		func(s *State) { b := 5.0; s.Keys.list[0].BudgetUSD = &b },
		func(s *State) { s.SnapshotAt = T - StaleMs - 1 },
		func(s *State) {
			s.Snapshot = &ipc.BudgetSnapshot{At: T, Budgets: []ipc.BudgetView{{ID: "b", Enforce: "block",
				Scope: ipc.BudgetScope{Global: true}, State: "ok"}}}
		},
	} {
		s, r := basicState(t)
		edit(s)
		if p := s.PlanChat(r, T); p.Handoff != "" {
			t.Errorf("handed off: %s", p.Handoff)
		}
	}
}
