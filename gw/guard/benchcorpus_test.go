package guard

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"
)

// The real-text corpus of the guard benchmark: the files testdata/bench-corpus.txt names (repository paths), read in
// order and joined with a newline. scripts/bench-guard.js builds the same bytes and cuts the same slices, so the Go
// and JS timings are on identical text.

var benchCorpus struct {
	once sync.Once
	data []byte
	err  error
}

func loadBenchCorpus(t testing.TB) []byte {
	t.Helper()
	benchCorpus.once.Do(func() {
		list, err := os.ReadFile(filepath.Join("testdata", "bench-corpus.txt"))
		if err != nil {
			benchCorpus.err = err
			return
		}
		var out bytes.Buffer
		sc := bufio.NewScanner(bytes.NewReader(list))
		first := true
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			b, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(line)))
			if err != nil {
				benchCorpus.err = err
				return
			}
			if !first {
				out.WriteByte('\n')
			}
			first = false
			out.Write(b)
		}
		benchCorpus.data = out.Bytes()
	})
	if benchCorpus.err != nil {
		t.Fatalf("bench corpus: %v", benchCorpus.err)
	}
	return benchCorpus.data
}

// benchText is n bytes of the corpus from off (the corpus repeated as needed), with both ends moved back to a UTF-8
// character boundary.
func benchText(c []byte, off, n int) string {
	for len(c) < off+n {
		c = append(append(c[:len(c):len(c)], '\n'), c...)
	}
	start, end := off, off+n
	for start > 0 && c[start]&0xc0 == 0x80 {
		start--
	}
	for end > start && end < len(c) && c[end]&0xc0 == 0x80 {
		end--
	}
	return string(c[start:end])
}

// benchOffsets spreads k slices of n bytes evenly over the corpus.
func benchOffsets(total, n, k int) []int {
	span := max(total-n, 0)
	out := make([]int, k)
	for i := range out {
		if k > 1 {
			out[i] = span * i / (k - 1)
		}
	}
	return out
}

func benchBody(text string) jsv.Value {
	return jsv.NewObject().Set("model", "m").Set("messages",
		jsv.Arr(jsv.NewObject().Set("role", "user").Set("content", text)))
}

// benchPolicy turns every check on: secrets and pii redact (so the redacted copies are timed too), injection logs.
var benchPolicy = Policy{Scope: "all", Secrets: "redact", PII: "redact", Injection: "log"}

func benchmarkSize(b *testing.B, n int) {
	c := loadBenchCorpus(b)
	offs := benchOffsets(len(c), n, 16)
	bodies := make([]jsv.Value, len(offs))
	for i, o := range offs {
		bodies[i] = benchBody(benchText(c, o, n))
	}
	b.SetBytes(int64(n))
	for i := 0; b.Loop(); i++ {
		if _, err := CheckBody(bodies[i%len(bodies)], benchPolicy, "openai-chat"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCheckBody32K(b *testing.B)  { benchmarkSize(b, 32<<10) }
func BenchmarkCheckBody128K(b *testing.B) { benchmarkSize(b, 128<<10) }
func BenchmarkCheckBody1M(b *testing.B)   { benchmarkSize(b, 1<<20) }
