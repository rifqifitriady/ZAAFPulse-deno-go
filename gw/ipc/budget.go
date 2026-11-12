package ipc

// The gateway's budget check against the newest budget.snapshot (P3 D3), the same rules as lib/cost/guard.js:
// a budget applies when its enforce is "block" and its scope matches the call; the first applicable budget whose
// state is "exceeded" refuses the call (402 insufficient_quota with Message). Budgets fail open (owner decision
// 2026-11-11): with no snapshot, or one older than staleMs, a call no exceeded budget refuses passes "unchecked" and
// its usage record says so. A budget the newest snapshot shows as exceeded is refused even when the snapshot is stale.
// Shared decisions: ipc/v1/fixtures/budget-decide (budget_test.go, test/ipc-budget.test.js).

// StaleAfterMs: a snapshot older than this (now - at > StaleAfterMs) no longer checks a call.
const StaleAfterMs int64 = 5000

// Decision actions.
const (
	Pass      = "pass"
	Refuse    = "refuse"
	Unchecked = "unchecked"
)

// CheckInput is what the gateway knows about a call before it goes to a route target. Model is already normalized
// (lib/pricing.js normalizeModel: no provider path, lower case); P4 ports the normalizer with its own fixtures.
type CheckInput struct {
	KeyID    string `json:"keyId,omitzero"`
	Model    string `json:"model,omitzero"`
	Provider string `json:"provider,omitzero"`
	Agent    string `json:"agent,omitzero"`
	RunID    string `json:"runId,omitzero"`
}

// Decision is the outcome; Budget and Message are set on a refusal.
type Decision struct {
	Action  string `json:"action"`
	Budget  string `json:"budget,omitzero"`
	Message string `json:"message,omitzero"`
}

// Decide checks one call against snapshot s (nil: none yet) at time now (epoch ms).
func Decide(s *BudgetSnapshot, now, staleMs int64, in CheckInput) Decision {
	if s == nil {
		return Decision{Action: Unchecked}
	}
	keyID := in.KeyID
	if keyID == "dashboard" {
		keyID = ""
	}
	source := "gateway"
	switch {
	case in.RunID != "":
		source = "collab"
	case in.Agent == "judge":
		source = "judge"
	case in.Agent == "replay":
		source = "replay"
	}
	for _, b := range s.Budgets {
		if applies(b, keyID, in, source, s.KeyCenter) && b.State == "exceeded" {
			return Decision{Action: Refuse, Budget: b.ID, Message: "budget \"" + b.Name + "\" is used up for this " + b.Period}
		}
	}
	if now-s.At > staleMs {
		return Decision{Action: Unchecked}
	}
	return Decision{Action: Pass}
}

func applies(b BudgetView, keyID string, in CheckInput, source string, centers map[string]string) bool {
	if b.Enforce != "block" {
		return false
	}
	if b.Scope.Global {
		return true
	}
	switch b.Scope.Dim {
	case "key":
		return keyID != "" && keyID == b.Scope.Value
	case "center":
		c, ok := centers[keyID]
		return keyID != "" && ok && c == b.Scope.Value
	case "model":
		return in.Model != "" && in.Model == b.Scope.Value
	case "provider":
		return in.Provider != "" && in.Provider == b.Scope.Value
	case "source":
		return keyID != "" && b.Scope.Value == "gateway" && source == "gateway"
	}
	return false
}
