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

// maxSpent bounds the lines kept while no snapshot holds them (a hung control plane): the oldest go first.
const maxSpent = 65536

// Spent is one line the engine emitted: its outbox seq (0: not stored), when, what it reached, its list cost in USD,
// and the spent of each budget it counts in as the snapshot of that moment had it (Base), before this line.
type Spent struct {
	Seq  int64
	Ts   int64
	In   ipc.CheckInput
	USD  float64
	Base map[string]float64
}

// Spend holds the engine's own lines until a snapshot provably holds them (P4e Decision 15, review m1): the control
// plane acknowledges a seq only after storing it (and the budget service counts a line when it is stored), and the
// gateway reads its messages in order, so a snapshot that arrives after an ack of seq N holds every line up to N.
type Spend struct {
	mu      sync.Mutex
	lines   []Spent // in Add order
	acked   int64   // the newest ack's seq
	covered int64   // the ack in force when the newest snapshot arrived: lines up to it are in that snapshot
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

// Acked records an ack of every event up to seq.
func (sp *Spend) Acked(seq int64) {
	sp.mu.Lock()
	sp.acked = max(sp.acked, seq)
	sp.mu.Unlock()
}

// SnapshotIn records that a snapshot arrived: it holds every line acknowledged before it.
func (sp *Spend) SnapshotIn() {
	sp.mu.Lock()
	sp.covered = sp.acked
	sp.mu.Unlock()
}

// Live drops the lines the newest snapshot holds and returns a copy of the rest, in Add order.
func (sp *Spend) Live() []Spent {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	keep := sp.lines[:0]
	for _, l := range sp.lines {
		if l.Seq == 0 || l.Seq > sp.covered {
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

// ownRefusal: a budget the engine's own lines, not yet in the snapshot, used up. For each budget the estimate is the
// largest of the snapshot's spent and, for every own line i, the spent line i saw before it (base_i) plus line i and
// every own line after it (review I1): base_i was read before line i was emitted, so it holds none of those lines and
// the sum never counts one twice, while spend from elsewhere that landed before line i is kept. A line whose base is
// above today's spent belongs to an earlier period and is left out.
func (s *State) ownRefusal(in ipc.CheckInput, own []Spent) string {
	if s.Snapshot == nil || len(own) == 0 {
		return ""
	}
	for _, b := range s.Snapshot.Budgets {
		if b.SpentUSD == nil || b.AmountUSD == nil || !ipc.AppliesTo(b, in, s.Snapshot.KeyCenter) {
			continue
		}
		est, suffix := *b.SpentUSD, 0.0
		for i := len(own) - 1; i >= 0; i-- {
			v, ok := own[i].Base[b.ID]
			if !ok || v > *b.SpentUSD+1e-12 {
				continue
			}
			suffix += own[i].USD
			est = max(est, v+suffix)
		}
		if est > *b.SpentUSD && est >= *b.AmountUSD {
			return `budget "` + b.Name + `" is used up for this ` + b.Period
		}
	}
	return ""
}
