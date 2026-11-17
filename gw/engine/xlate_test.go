package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"
)

// xlateProvider is the stubbed provider of one case: a whole answer, a stream of chunks (cut: it breaks off after the
// last), or a call that throws (a code, or "timeout"). nil: the case makes no provider call.
type xlateProvider struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
	Chunks  []string          `json:"chunks"`
	Cut     bool              `json:"cut"`
	Throw   string            `json:"throw"`
}

type xlateCase struct {
	Config      ipc.ConfigPush      `json:"config"`
	Secrets     ipc.SecretsHandoff  `json:"secrets"`
	Snapshot    *ipc.BudgetSnapshot `json:"snapshot"`
	FailChecks  bool                `json:"failChecks"`
	RateLimited bool                `json:"rateLimited"`
	Request     struct {
		Path    string            `json:"path"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	} `json:"request"`
	Provider *xlateProvider `json:"provider"`
}

type xlateRequest struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    *string           `json:"body"`
}

type xlateOut struct {
	Requests []xlateRequest `json:"requests"`
	Client   struct {
		Status  int                `json:"status"`
		Headers map[string]*string `json:"headers"`
		Body    string             `json:"body"`
	} `json:"client"`
	Usage json.RawMessage `json:"usage"`
}

type xlateFixture struct {
	Name string
	Fn   string
	In   xlateCase
	Out  xlateOut
}

// loadXlate reads gw/testdata/xlate (scripts/fixtures/gen-xlate.js) with the secrets put back, sorted by name.
func loadXlate(t *testing.T) []xlateFixture {
	t.Helper()
	dir := filepath.Join("..", "testdata", "xlate")
	files, err := os.ReadDir(dir)
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures in %s (run node scripts/fixtures/gen-xlate.js): %v", dir, err)
	}
	var out []xlateFixture
	for _, f := range files {
		fx, err := jsv.LoadFixture(filepath.Join(dir, f.Name()))
		if err != nil {
			t.Fatalf("%s: %v", f.Name(), err)
		}
		x := xlateFixture{Name: f.Name(), Fn: fx.Fn}
		if err := json.Unmarshal([]byte(jsv.MustStringify(fx.In[0])), &x.In); err != nil {
			t.Fatalf("%s: %v", f.Name(), err)
		}
		if err := json.Unmarshal([]byte(jsv.MustStringify(fx.Out)), &x.Out); err != nil {
			t.Fatalf("%s: %v", f.Name(), err)
		}
		out = append(out, x)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// counterEnv is the generator's determinism (scripts/fixtures/lib.js): Random gives n bytes equal to a counter that
// starts at 1 per case, Now the fixed clock.
func counterEnv() *translate.Env {
	n := 0
	return &translate.Env{
		Now: func() int64 { return fixtureNow },
		Random: func(k int) []byte {
			n++
			b := make([]byte, k)
			for i := range b {
				b[i] = byte(n)
			}
			return b
		},
	}
}

// xlatePorted replays the cases of one fixture function; a function not in it is not ported yet.
var xlatePorted = map[string]func(t *testing.T, f xlateFixture){}

// Every case of gw/testdata/xlate (the JS gateway's own answers): the engine plans it without a hand-off, sends the
// provider the same request, and gives the client the same status, headers and bytes and the same usage line.
func TestXlateFixtures(t *testing.T) {
	for _, f := range loadXlate(t) {
		t.Run(f.Name, func(t *testing.T) {
			run, ok := xlatePorted[f.Fn]
			if !ok {
				t.Skipf("%s: not ported yet", f.Fn)
			}
			run(t, f)
		})
	}
}
