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

// keepSpentMs is how long the engine keeps its own lines: past it every snapshot holds them (the ingest stores a line a
// few milliseconds after it is sent), or the snapshot is stale and the call is unchecked anyway.
const keepSpentMs = 10_000

// Spent is one line the engine emitted: when, what it reached, its list cost in USD, and the spent of each budget it
// counts in as the snapshot of that moment had it (Base), before this line.
type Spent struct {
	Ts   int64
	In   ipc.CheckInput
	USD  float64
	Base map[string]float64
}

// Spend holds the engine's own recent lines (P4e Decision 15).
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

// Recent drops the lines older than keepSpentMs at now and returns a copy of the rest.
func (sp *Spend) Recent(now int64) []Spent {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	keep := sp.lines[:0]
	for _, l := range sp.lines {
		if l.Ts > now-keepSpentMs {
			keep = append(keep, l)
		}
	}
	sp.lines = keep
	return append([]Spent(nil), keep...)
}

// spentOf is a line's share of the budgets: the model it was billed at (routedTo, else model), normalized as the
// ledger does, its list cost, and the spent of each budget of snap it counts in (nil snap: none known).
func spentOf(rec ipc.UsageRecord, snap *ipc.BudgetSnapshot) (Spent, bool) {
	if rec.Cost <= 0 {
		return Spent{}, false
	}
	m := rec.Model
	if rec.RoutedTo != nil {
		m = *rec.RoutedTo
	}
	x := Spent{Ts: rec.Ts, In: ipc.CheckInput{KeyID: rec.KeyID, Model: NormalizeModel(m), Provider: rec.Provider},
		USD: float64(rec.Cost) / 1e9, Base: map[string]float64{}}
	if snap != nil {
		for _, b := range snap.Budgets {
			if b.SpentUSD != nil && ipc.AppliesTo(b, x.In, snap.KeyCenter) {
				x.Base[b.ID] = *b.SpentUSD
			}
		}
	}
	return x, true
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

// ownRefusal: a budget the engine's own recent lines used up. For each budget the estimate is the larger of the
// snapshot's spent and the spent the oldest of those lines saw plus all of them: when the snapshot already holds the
// lines the two agree (no double count), when it does not yet (the ingest lags), the second one counts them. A line
// whose base is above today's spent belongs to an earlier period and is left out.
func (s *State) ownRefusal(in ipc.CheckInput, own []Spent) string {
	if s.Snapshot == nil || len(own) == 0 {
		return ""
	}
	for _, b := range s.Snapshot.Budgets {
		if b.SpentUSD == nil || b.AmountUSD == nil || !ipc.AppliesTo(b, in, s.Snapshot.KeyCenter) {
			continue
		}
		sum, base, any := 0.0, 0.0, false
		for _, l := range own {
			v, ok := l.Base[b.ID]
			if !ok || v > *b.SpentUSD+1e-12 {
				continue
			}
			if !any || v < base {
				base = v
			}
			sum += l.USD
			any = true
		}
		if any && max(*b.SpentUSD, base+sum) >= *b.AmountUSD {
			return `budget "` + b.Name + `" is used up for this ` + b.Period
		}
	}
	return ""
}
