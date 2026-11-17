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
