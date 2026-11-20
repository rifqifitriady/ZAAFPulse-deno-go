//go:build !race

package engine

// The race detector allocates for its own bookkeeping: allocation bounds run without it (as guard_heap_test.go).

import (
	"runtime"
	"strings"
	"testing"
)

// Spec D19: Save on a 10 MiB tool result allocates at most 8x its UTF-8 size: a log (dedupe) and a grep output (one
// group per file, most lines not printed).
func TestSaverMemory10MiB(t *testing.T) {
	for name, line := range map[string]string{"log": "2026-11-17T10:00:00Z INFO request done ok\n",
		"grep": "src/dir/file.go:999: match here\n"} {
		text := strings.Repeat(line, 10*1024*1024/len(line))
		body := toolBody(text, "x")
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		res, why := SaveTokens(body, "openai", nil)
		runtime.ReadMemStats(&after)
		if why != "" || res == nil {
			t.Fatalf("%s: %v %q", name, res, why)
		}
		got := after.TotalAlloc - before.TotalAlloc
		t.Logf("%s: allocated %.2fx the text", name, float64(got)/float64(len(text)))
		if got > 8*uint64(len(text)) {
			t.Fatalf("%s: allocated %d bytes for a %d-byte text", name, got, len(text))
		}
	}
}
