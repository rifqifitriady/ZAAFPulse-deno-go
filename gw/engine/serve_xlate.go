package engine

// Serving the P4d paths (translated calls, the Anthropic leg of chat, the native passthroughs, count_tokens and
// embeddings): the provider call, State.Deliver's answer written to the client, and P4f's hooks exactly as chat uses
// them: the call journal around a call that writes a usage line, the usage line through emitLine with the call's id,
// and the server and client spans after the answer's last byte.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

// answerOwn writes an answer the engine gives without a provider (a count estimate): no usage line, the server span.
func (e *Engine) answerOwn(w http.ResponseWriter, r *http.Request, s *State, a Answer, started time.Time, tr *callTrace) {
	if r.Context().Err() == nil {
		write(w, a)
	}
	if tr != nil {
		flushAnswer(w)
		e.spans(s, tr, a.Status, started, time.Now(), nil, nil, nil)
	}
}

// cutBody reads the provider's answer; any read error but the end reads as the provider breaking off (ErrCut, whose
// text is the JS stream's "terminated").
type cutBody struct{ io.ReadCloser }

func (c cutBody) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		err = ErrCut
	}
	return n, err
}

// httpWriter is Deliver's writer for a client: a whole answer is held until its usage line is out (the JS gateway
// records before it answers), a stream is written and flushed per write; pings go out between writes under the same
// lock; every byte written is fed to the span observer.
type httpWriter struct {
	w      http.ResponseWriter
	rc     *http.ResponseController
	obs    *Observer
	mu     sync.Mutex
	status int
	header http.Header
	stream bool
	whole  []byte
	failed bool
	stop   chan struct{}
	pinged sync.WaitGroup
}

func (h *httpWriter) Head(status int, hd http.Header) error {
	h.status, h.header = status, hd
	h.stream = strings.HasPrefix(hd.Get("Content-Type"), "text/event-stream")
	if !h.stream {
		return nil
	}
	for k, v := range hd {
		h.w.Header()[k] = v
	}
	h.w.WriteHeader(status)
	if h.rc.Flush() != nil {
		h.failed = true
		return errWrite
	}
	return nil
}

func (h *httpWriter) Write(b []byte) error {
	if !h.stream {
		h.whole = append(h.whole, b...)
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.send(b)
}

// send writes and flushes one piece of a stream (the lock held).
func (h *httpWriter) send(b []byte) error {
	if h.failed {
		return errWrite
	}
	if h.obs != nil {
		h.obs.Feed(b)
	}
	if _, err := h.w.Write(b); err != nil || h.rc.Flush() != nil {
		h.failed = true
		return errWrite
	}
	return nil
}

// Pinger sends ping() every PingEvery until the stream ends (translated()'s setInterval for Anthropic clients).
func (h *httpWriter) Pinger(ping func() string) {
	h.stop = make(chan struct{})
	h.pinged.Add(1)
	go func() {
		defer h.pinged.Done()
		t := time.NewTicker(PingEvery)
		defer t.Stop()
		for {
			select {
			case <-h.stop:
				return
			case <-t.C:
				h.mu.Lock()
				_ = h.send([]byte(ping()))
				h.mu.Unlock()
			}
		}
	}()
}

// StopPings stops the pings and waits for a ping being written; calling it again does nothing.
func (h *httpWriter) StopPings() {
	if h.stop != nil {
		close(h.stop)
		h.pinged.Wait()
		h.stop = nil
	}
}

// end stops the pings (a stream that ended without its last event).
func (h *httpWriter) end() { h.StopPings() }

// commit writes a held whole answer.
func (h *httpWriter) commit(gone bool) {
	if h.stream || h.status == 0 {
		return
	}
	if h.obs != nil {
		h.obs.Feed(h.whole) // what JS's observer sees: the body res.end writes, also to a client that left
	}
	if !gone {
		write(h.w, Answer{Status: h.status, Header: h.header, Body: h.whole})
	}
}

// serveDeliver serves a planned P4d call after its rate limit, budget and guardrail checks.
func (e *Engine) serveDeliver(w http.ResponseWriter, r *http.Request, s *State, p *Plan, started time.Time,
	tr *callTrace, js http.Handler) {
	elapsed := func() float64 { return float64(time.Since(started).Microseconds()) / 1000 }
	if p.Stream {
		if !e.track() {
			js.ServeHTTP(w, r) // the gateway is shutting down
			return
		}
		defer e.streams.Done()
	}
	// The journal: a call that writes a usage line is opened before its provider call (P4f D14). count_tokens writes
	// none, so a gateway that dies during one has nothing to recover.
	var call int64
	if p.Upstream != UpCount {
		call = e.open(p)
	}
	hw := &httpWriter{w: w, rc: http.NewResponseController(w)}
	if tr != nil {
		hw.obs = NewObserver()
	}
	var line *ipc.UsageRecord
	done := false
	finish := func() {
		hw.end()
		if line == nil {
			if call != 0 {
				e.calls.End(call) // it ends with no line
			}
		} else {
			line.Ts = e.now()
			if tr != nil {
				line.TraceID = &tr.tc.TraceID
			}
			e.emitLine(*line, p.Unchecked, call)
		}
		hw.commit(r.Context().Err() != nil)
		if tr != nil {
			flushAnswer(w)
			status := hw.status
			if status == 0 {
				status = http.StatusOK
			}
			e.spans(s, tr, status, started, time.Now(), line, provSpan(p), hw.obs)
		}
	}
	defer func() {
		if !done {
			finish() // a panic in Deliver: the call still ends with what it has, then the panic goes on
		}
	}()
	ctx := r.Context()
	if p.Detached {
		ctx = context.WithoutCancel(ctx) // embeddings: the client's leaving does not cancel the call (J8)
	}
	res := ProviderResult{}
	resp, why := e.upstream.Start(ctx, p)
	if why != "" {
		res.Why = why
	} else {
		res.Status, res.Header, res.Body = resp.StatusCode, resp.Header, cutBody{resp.Body}
	}
	if p.Stream || p.Upstream == UpChat || p.Upstream == UpAnthropic {
		// The client's body and the plan's copies are no longer needed once the call is under way (P4c Decision 11).
		r.Body = http.NoBody
	}
	line = s.Deliver(p, res, elapsed, hw)
	done = true
	finish()
}
