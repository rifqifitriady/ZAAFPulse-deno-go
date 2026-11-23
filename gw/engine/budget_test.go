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

// The call after the one that used a budget up is refused at once, from the engine's own line (P4e Decision 15).
func TestOwnSpendRefusesTheNextCall(t *testing.T) {
	s, r := basicState(t)
	s = s.WithSnapshot(&ipc.BudgetSnapshot{At: T, Budgets: []ipc.BudgetView{keyBudget("ok", 0, 0.002)}})
	p := plan(t, s, r)
	sp := &Spend{}
	if a, _ := s.Budget(&p, T+5, StaleMs, sp.Live()); a != nil {
		t.Fatalf("refused before any spend: %s", a.Body)
	}
	rec := p.Base
	rec.Ts, rec.Cost = T+10, 2_000_000
	x, ok := spentOf(rec, s.Snapshot)
	if !ok || x.Base["kb"] != 0 {
		t.Fatalf("spent %+v %v", x, ok)
	}
	sp.Add(x)
	a, _ := s.Budget(&p, T+20, StaleMs, sp.Live())
	if a == nil || a.Status != 402 || !a.NoLine ||
		string(a.Body) != `{"error":{"message":"budget \"Key k1\" is used up for this month","type":"insufficient_quota"}}` {
		t.Fatalf("got %+v", a)
	}
}

// A snapshot taken after the line but before the ingest stored it (the lag the Windows runner hit) still refuses;
// one that holds the line does not count it twice.
func TestOwnSpendSurvivesTheIngestLag(t *testing.T) {
	s, r := basicState(t)
	s = s.WithSnapshot(&ipc.BudgetSnapshot{At: T, Budgets: []ipc.BudgetView{keyBudget("ok", 0, 0.003)}})
	p := plan(t, s, r)
	sp := &Spend{}
	rec := p.Base
	rec.Ts, rec.Cost = T+10, 2_000_000
	x, _ := spentOf(rec, s.Snapshot)
	sp.Add(x)
	lag := s.WithSnapshot(&ipc.BudgetSnapshot{At: T + 1000, Budgets: []ipc.BudgetView{keyBudget("ok", 0, 0.002)}})
	if a, _ := lag.Budget(&p, T+1010, StaleMs, sp.Live()); a == nil {
		t.Fatal("the line missing from the snapshot was not counted")
	}
	// The snapshot holds the line (spent 0.002 of 0.003): 0.002, not 0.004, so the call passes.
	held := s.WithSnapshot(&ipc.BudgetSnapshot{At: T + 2000, Budgets: []ipc.BudgetView{keyBudget("ok", 0.002, 0.003)}})
	if a, _ := held.Budget(&p, T+2010, StaleMs, sp.Live()); a != nil {
		t.Fatalf("counted twice: %s", a.Body)
	}
	// A new period (spent back to 0, below the line's base of an earlier one) leaves the old line out.
	x2 := x
	x2.Base = map[string]float64{"kb": 0.5}
	sp2 := &Spend{}
	sp2.Add(x2)
	if a, _ := s.Budget(&p, T+20, StaleMs, sp2.Live()); a != nil {
		t.Fatalf("an earlier period's line counted: %s", a.Body)
	}
}

// Own lines stay until a snapshot provably holds them (review m1): an ack alone, or a snapshot before the ack, keeps
// them; a snapshot after the ack drops them. Time alone never does.
func TestOwnLinesLeaveOnceASnapshotHoldsThem(t *testing.T) {
	sp := &Spend{}
	sp.Add(Spent{Seq: 5, USD: 1})
	sp.Add(Spent{Seq: 6, USD: 1})
	sp.Add(Spent{Seq: 0, USD: 1}) // not stored: kept until the cap
	sp.SnapshotIn()               // a snapshot before any ack holds nothing of ours
	if n := len(sp.Live()); n != 3 {
		t.Fatalf("%d lines, want 3", n)
	}
	sp.Acked(5)
	if n := len(sp.Live()); n != 3 {
		t.Fatalf("an ack alone dropped lines: %d", n)
	}
	sp.SnapshotIn()
	got := sp.Live()
	if len(got) != 2 || got[0].Seq != 6 || got[1].Seq != 0 {
		t.Fatalf("got %+v", got)
	}
}

// The review's mixed-spend case (I1): spend from elsewhere lands on the budget between two go-served lines; the second
// line is not in the snapshot yet. The next call must be refused, as JS refuses it on live spend.
func TestOwnSpendKeepsSpendFromElsewhere(t *testing.T) {
	s, r := basicState(t)
	b := func(spent float64) *State {
		v := keyBudget("ok", spent, 0.90)
		return s.WithSnapshot(&ipc.BudgetSnapshot{At: T, Budgets: []ipc.BudgetView{v}})
	}
	s0 := b(0)
	p := plan(t, s0, r)
	sp := &Spend{}
	add := func(st *State, seq int64, usd float64) {
		rec := p.Base
		rec.Ts, rec.Cost = T, int64(usd*1e9)
		x, _ := spentOf(rec, st.Snapshot)
		x.Seq = seq
		sp.Add(x)
	}
	add(s0, 1, 0.40) // L1 at base 0
	s1 := b(0.70)    // the snapshot holds L1 and 0.30 served by JS
	add(s1, 2, 0.25) // L2 at base 0.70; real spend 0.95, not stored yet
	if a, _ := s1.Budget(&p, T, StaleMs, sp.Live()); a == nil || a.Status != 402 {
		t.Fatalf("passed at 0.95 of 0.90: %+v", a)
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
	if n := len(sp.Live()); n > maxSpent {
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
