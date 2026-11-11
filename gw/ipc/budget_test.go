package ipc

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type budgetFixture struct {
	Snapshot json.RawMessage `json:"snapshot"`
	Now      int64           `json:"now"`
	Input    CheckInput      `json:"input"`
	Expect   Decision        `json:"expect"`
}

// ipc/v1/fixtures/budget-decide: the same decisions as lib/cost/guard.js (test/ipc-budget.test.js runs them there).
func TestBudgetDecideFixtures(t *testing.T) {
	dir := filepath.Join(fixtureRoot, "budget-decide")
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 20 {
		t.Fatalf("only %d budget fixtures", len(files))
	}
	for _, f := range files {
		t.Run(strings.TrimSuffix(f.Name(), ".json"), func(t *testing.T) {
			data, _ := os.ReadFile(filepath.Join(dir, f.Name()))
			dec := json.NewDecoder(bytes.NewReader(data))
			dec.DisallowUnknownFields()
			var fx budgetFixture
			if err := dec.Decode(&fx); err != nil {
				t.Fatal(err)
			}
			var snap *BudgetSnapshot
			if string(fx.Snapshot) != "null" {
				snap = new(BudgetSnapshot)
				if err := decodeBody(fx.Snapshot, snap, true, "snapshot"); err != nil {
					t.Fatal(err)
				}
			}
			if got := Decide(snap, fx.Now, StaleAfterMs, fx.Input); got != fx.Expect {
				t.Fatalf("got %+v, want %+v", got, fx.Expect)
			}
			if !strings.HasPrefix(f.Name(), fx.Expect.Action+"-") {
				t.Fatalf("the file name starts with the expected action %q", fx.Expect.Action)
			}
		})
	}
}
