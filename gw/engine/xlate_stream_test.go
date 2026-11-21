package engine

import (
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

// Every split of the provider's bytes gives the same client stream: the fixtures again, one byte per read.
func TestTranslatedStreamSplits(t *testing.T) {
	n := 0
	for _, f := range loadXlate(t) {
		pr := f.In.Provider
		if pr == nil || pr.Chunks == nil || !ok2xx(pr.Status) {
			continue
		}
		t.Run(f.Name, func(t *testing.T) {
			w := deliverXlate(t, f, func(r io.Reader) io.Reader { return iotest.OneByteReader(r) })
			if w.body.String() != f.Out.Client.Body {
				t.Errorf("one byte per read differs:\n got %q\nwant %q", w.body.String(), f.Out.Client.Body)
			}
		})
		n++
	}
	if n < 10 {
		t.Fatalf("only %d stream fixtures", n)
	}
}

// After an error event the line has no time to the first token and no tokens, as JS keeps its first result.
func TestTranslatedStreamErrorLine(t *testing.T) {
	for _, f := range loadXlate(t) {
		if f.Name != "msg-stream-error-chunk.json" && f.Name != "msg-stream-cut.json" {
			continue
		}
		s, p := planXlate(t, f)
		w := &recordWriter{}
		line := s.Deliver(&p, providerOf(f.In, nil), func() float64 { return 1 }, w)
		if line.TTFT.Set && !line.TTFT.Null || *line.Input != 0 || !strings.Contains(w.body.String(), "event: error") {
			t.Errorf("%s: %+v %q", f.Name, line, w.body.String())
		}
	}
}

// A client that leaves (a write fails) ends the stream at once, with no error event and one line.
func TestTranslatedStreamClientGone(t *testing.T) {
	for _, f := range loadXlate(t) {
		if f.Name != "msg-oa-stream.json" {
			continue
		}
		s, p := planXlate(t, f)
		w := &goneWriter{after: 2}
		line := s.Deliver(&p, providerOf(f.In, nil), func() float64 { return 1 }, w)
		if line == nil || line.Status != 200 || w.writes != 3 {
			t.Fatalf("%+v writes %d", line, w.writes)
		}
	}
}

// goneWriter fails every write after the first `after`.
type goneWriter struct {
	recordWriter
	after, writes int
}

func (w *goneWriter) Write(b []byte) error {
	w.writes++
	if w.writes > w.after {
		return io.ErrClosedPipe
	}
	return w.recordWriter.Write(b)
}

// eventWriter records the order of what Deliver does: the head, each write, the pinger's start and stop.
type eventWriter struct {
	recordWriter
	events []string
}

func (w *eventWriter) Write(b []byte) error {
	w.events = append(w.events, "write:"+strings.SplitN(string(b), "\n", 2)[0])
	return w.recordWriter.Write(b)
}
func (w *eventWriter) Pinger(func() string) { w.events = append(w.events, "pinger") }
func (w *eventWriter) StopPings()           { w.events = append(w.events, "stop") }

// Review m1: the pings start after the opening event (JS writes open before any interval fires) and stop before the
// final event (JS clears the interval as the stream ends), on the clean end and on an error.
func TestPingerAfterOpenAndStoppedBeforeTheEnd(t *testing.T) {
	for _, name := range []string{"msg-oa-stream.json", "msg-stream-error-chunk.json"} {
		var f xlateFixture
		for _, x := range loadXlate(t) {
			if x.Name == name {
				f = x
			}
		}
		s, p := planXlate(t, f)
		w := &eventWriter{}
		s.Deliver(&p, providerOf(f.In, nil), func() float64 { return 1 }, w)
		ev := w.events
		last := -1
		for i, e := range ev {
			if strings.HasPrefix(e, "write:") {
				last = i
			}
		}
		if len(ev) < 4 || ev[0] != "write:event: message_start" || ev[1] != "pinger" || last < 1 || ev[last-1] != "stop" {
			t.Fatalf("%s: %v", name, ev)
		}
	}
}
