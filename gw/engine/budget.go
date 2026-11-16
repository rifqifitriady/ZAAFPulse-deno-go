package engine

// Budgets on the go engine (P4e, slice S7): lib/gateway/index.js targetsFor's price rule and allowedTargets with
// lib/cost/guard.js, for the chat route's one target, on the newest budget.snapshot. Budgets fail open (owner decision
// 2026-11-11): no snapshot or one older than staleMs lets the call through, marked "unchecked"; a budget the snapshot
// shows as exceeded is refused even then. Between snapshots the engine adds the cost of its own lines, so the call
// after the one that used a budget up is refused, as the JS refuses it (spec Decision 15).

import (
	"net/http"
	"sync"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

// maxSpent bounds the lines kept while no snapshot comes (a hung control plane): the oldest go first.
const maxSpent = 65536

// Spent is one line the engine emitted: when, what it reached, its list cost in USD.
type Spent struct {
	Ts  int64
	In  ipc.CheckInput
	USD float64
}

// Spend holds the engine's own lines newer than the newest snapshot.
type Spend struct {
	mu    sync.Mutex
	lines []Spent
}

// Add keeps one line.
func (sp *Spend) Add(x Spent) {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if len(sp.lines) >= maxSpent {
		sp.lines = append(sp.lines[:0], sp.lines[len(sp.lines)-maxSpent/2:]...)
	}
	sp.lines = append(sp.lines, x)
}

// Since drops the lines at or before at (the snapshot taken at at holds them) and returns a copy of the rest.
func (sp *Spend) Since(at int64) []Spent {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	keep := sp.lines[:0]
	for _, l := range sp.lines {
		if l.Ts > at {
			keep = append(keep, l)
		}
	}
	sp.lines = keep
	return append([]Spent(nil), keep...)
}

// spentOf is a line's share of the budgets: the model it was billed at (routedTo, else model), normalized as the
// ledger does, and its list cost.
func spentOf(rec ipc.UsageRecord) (Spent, bool) {
	if rec.Cost <= 0 {
		return Spent{}, false
	}
	m := rec.Model
	if rec.RoutedTo != nil {
		m = *rec.RoutedTo
	}
	return Spent{Ts: rec.Ts, In: ipc.CheckInput{KeyID: rec.KeyID, Model: NormalizeModel(m), Provider: rec.Provider},
		USD: float64(rec.Cost) / 1e9}, true
}

func quota(msg string) *Answer {
	return &Answer{Status: http.StatusPaymentRequired, Header: answerHeader(), Body: errorBody(msg, "insufficient_quota", ""),
		NoLine: true}
}

// limited is proxyChat's limitedKey: a key budget (budgetUSD), or a blocking budget scoped to the key or its center.
func (s *State) limited(k *ipc.GatewayKeyAuth) bool {
	if k.BudgetUSD != nil {
		return true
	}
	if s.Snapshot == nil {
		return false
	}
	for _, b := range s.Snapshot.Budgets {
		if b.Enforce != "block" || b.Scope.Global {
			continue
		}
		if b.Scope.Dim == "key" && b.Scope.Value == k.ID {
			return true
		}
		if c, ok := s.Snapshot.KeyCenter[k.ID]; b.Scope.Dim == "center" && ok && c == b.Scope.Value {
			return true
		}
	}
	return false
}

// Budget decides the plan's budget at now: a 402 (no usage line goes with it), or nil and whether no fresh snapshot
// checked the call. own is the engine's lines since the snapshot (Spend.Since).
func (s *State) Budget(p *Plan, now, staleMs int64, own []Spent) (*Answer, bool) {
	if s.limited(p.Key) && TierOf(p.Target.Provider) != "private" && s.Pricer.Key(p.Target.Name) == "" {
		return quota("this key has a budget, and " + p.Model + " has no known price; add one under Settings → Model prices"), false
	}
	in := ipc.CheckInput{KeyID: p.Key.ID, Model: NormalizeModel(p.Target.Name), Provider: p.Target.Provider.ID}
	d := ipc.Decide(s.Snapshot, now, staleMs, in)
	if d.Action == ipc.Refuse {
		return quota(d.Message), false
	}
	if m := s.ownRefusal(in, own); m != "" {
		return quota(m), false
	}
	return nil, d.Action == ipc.Unchecked
}

// ownRefusal: a budget the snapshot shows under its amount that the engine's own lines since then used up.
func (s *State) ownRefusal(in ipc.CheckInput, own []Spent) string {
	if s.Snapshot == nil || len(own) == 0 {
		return ""
	}
	for _, b := range s.Snapshot.Budgets {
		if b.SpentUSD == nil || b.AmountUSD == nil || !ipc.AppliesTo(b, in, s.Snapshot.KeyCenter) {
			continue
		}
		extra := 0.0
		for _, l := range own {
			if ipc.AppliesTo(b, l.In, s.Snapshot.KeyCenter) {
				extra += l.USD
			}
		}
		if extra > 0 && *b.SpentUSD+extra >= *b.AmountUSD {
			return `budget "` + b.Name + `" is used up for this ` + b.Period
		}
	}
	return ""
}
