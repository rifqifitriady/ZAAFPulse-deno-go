package guard

import (
	"sync"
	"testing"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"
)

// The shared parts (compiled patterns, their VM pools, the multi-pattern tables, Prepare) are safe for concurrent
// calls: run under -race, the same bodies checked at once give the results they give one at a time.
func TestConcurrentCallsAgree(t *testing.T) {
	c := loadBenchCorpus(t)
	offs := benchOffsets(len(c), 16<<10, 16)
	want := make([]string, len(offs))
	for i, o := range offs {
		r, err := CheckBody(benchBody(benchText(c, o, 16<<10)), benchPolicy, "openai-chat")
		if err != nil {
			t.Fatal(err)
		}
		want[i] = jsv.Canonical(r.Body) + jsv.Canonical(hitsValue(r.Hits))
	}
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := range offs {
				i := (k + g) % len(offs)
				r, err := CheckBody(benchBody(benchText(c, offs[i], 16<<10)), benchPolicy, "openai-chat")
				if err != nil {
					t.Error(err)
					return
				}
				if got := jsv.Canonical(r.Body) + jsv.Canonical(hitsValue(r.Hits)); got != want[i] {
					t.Errorf("slice %d differs when run concurrently", i)
				}
			}
		}()
	}
	wg.Wait()
}
