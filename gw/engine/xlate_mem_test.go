//go:build !race

package engine

import (
	"io"
	"runtime"
	"testing"
	"time"
)

// The translated part of a held stream (P4d Decision 24): the client's stream writer, the SSE reader's buffer and
// the plan, measured on heap for 200 streams held after their first event. A held chat stream measured ~67 KiB with
// its HTTP buffers and goroutines (P4c); the 80 KiB budget leaves 13 KiB for the translation. The whole held stream
// through the serving path is measured again once it is wired (P4d Task 8).
func TestMemoryPerHeldTranslatedStream(t *testing.T) {
	var f xlateFixture
	for _, x := range loadXlate(t) {
		if x.Name == "msg-oa-stream.json" {
			f = x
		}
	}
	const streams = 200
	first := f.In.Provider.Chunks[0] + f.In.Provider.Chunks[1]
	var writers []*io.PipeWriter
	started := make(chan struct{}, streams)
	measure := func() uint64 {
		time.Sleep(100 * time.Millisecond)
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return m.HeapInuse
	}
	plans := make([]Plan, streams)
	states := make([]*State, streams)
	for i := range plans {
		states[i], plans[i] = planXlate(t, f)
	}
	before := measure()
	for i := 0; i < streams; i++ {
		pr, pw := io.Pipe()
		writers = append(writers, pw)
		s, p := states[i], &plans[i]
		go func() {
			w := &recordWriter{}
			n := 0
			s.Deliver(p, ProviderResult{Status: 200, Header: map[string][]string{"Content-Type": {"text/event-stream"}}, Body: pr},
				func() float64 { return 1 }, &signalWriter{recordWriter: w, n: &n, ready: started})
		}()
		_, _ = io.WriteString(pw, first)
		<-started
	}
	after := measure()
	per := (after - before) / streams
	t.Logf("translated heap per held stream: %d bytes", per)
	for _, w := range writers {
		_ = w.Close()
	}
	if per > 13<<10 {
		t.Fatalf("%d bytes per held translated stream, over the 13 KiB left in the 80 KiB budget", per)
	}
}

// signalWriter signals once its first event after the opening one was written, and keeps no text.
type signalWriter struct {
	*recordWriter
	n     *int
	ready chan struct{}
}

func (w *signalWriter) Write(b []byte) error {
	*w.n++
	if *w.n == 2 {
		w.ready <- struct{}{}
	}
	return nil
}
