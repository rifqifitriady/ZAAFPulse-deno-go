package engine

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

// Engine serves the route classes on the go engine. Its State is swapped whole on each generation and budget snapshot.
type Engine struct {
	state    atomic.Pointer[State]
	limits   *RateLimiter
	upstream *Upstream
	emit     func(*ipc.UsageReport) error
	now      func() int64
	logf     func(string, ...any)
	modes    atomic.Pointer[func(sub string) (onGo, shadowed bool)]
	own      atomic.Pointer[func(method, path string, body []byte) (int, []byte, error)]
	streams  sync.WaitGroup // streams being served, until their usage line is handed to emit
}

// New is an engine that calls providers through up and hands each usage line to emit (the outbox, then the control
// plane: lib/ipc/ingest.js stores it through the JS gateway's own writer).
func New(up *Upstream, emit func(*ipc.UsageReport) error, logf func(string, ...any)) *Engine {
	return &Engine{limits: NewRateLimiter(), upstream: up, emit: emit, now: NowMs, logf: logf}
}

// SetState installs a generation (nil: none yet; every request is handed to the control plane).
func (e *Engine) SetState(s *State) { e.state.Store(s) }

// State is the current generation, or nil.
func (e *Engine) State() *State { return e.state.Load() }

// StreamReadSize is how much of the provider's stream is read at once (P4c Decision 11; 4 KiB, the size of the
// transport's own read buffer, after the memory gate of 2026-11-13).
const StreamReadSize = 4 << 10

// SetOwn gives the engine the front's call to the control plane's internal port (the shadow's capture and line) for
// requests it reads in Serve but shadows (P4c: chat on go, chat-stream shadowed).
func (e *Engine) SetOwn(own func(method, path string, body []byte) (int, []byte, error)) {
	e.own.Store(&own)
}

func (e *Engine) ownFn() func(method, path string, body []byte) (int, []byte, error) {
	if f := e.own.Load(); f != nil {
		return *f
	}
	return nil
}

// SetModes tells the engine, per sub-class ("chat", "chat-stream"), whether it serves it and whether it shadows it
// (the front's engines and shadow classes; Task 3).
func (e *Engine) SetModes(f func(sub string) (onGo, shadowed bool)) { e.modes.Store(&f) }

func (e *Engine) mode(sub string) (bool, bool) {
	if f := e.modes.Load(); f != nil {
		return (*f)(sub)
	}
	return sub == "chat", false // P4b: chat on go, streams handed off
}

// Wait waits at most d for the streams in flight and their usage lines (the gateway's shutdown, before the outbox
// closes); false when d passed first.
func (e *Engine) Wait(d time.Duration) bool {
	done := make(chan struct{})
	go func() { e.streams.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// ReadRequest reads what the engine needs of r: at most MaxBody + 1 bytes of its body. The body r keeps is the whole
// one again (what was read, then the rest), so a hand-off forwards it unchanged.
func ReadRequest(r *http.Request, rawPath string) *Request {
	buf, _ := io.ReadAll(io.LimitReader(r.Body, MaxBody+1))
	r.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(buf), r.Body), r.Body}
	q := ""
	if i := len(rawPath); i < len(r.RequestURI) && r.RequestURI[i] == '?' {
		q = r.RequestURI[i+1:]
	}
	return &Request{Method: r.Method, RawPath: rawPath, Query: q, Header: r.Header, Host: r.Host, Peer: r.RemoteAddr,
		Body: buf, TooBig: len(buf) > MaxBody}
}

// Serve answers one chat request, or hands it to js (the control plane) unchanged.
func (e *Engine) Serve(class string, w http.ResponseWriter, r *http.Request, rawPath string, js http.Handler) {
	s := e.state.Load()
	if s == nil || class != "chat" {
		js.ServeHTTP(w, r)
		return
	}
	req := ReadRequest(r, rawPath)
	started := time.Now()
	p := s.PlanChat(req, e.now())
	if p.Handoff != "" {
		js.ServeHTTP(w, r)
		return
	}
	// The sub-class (P4c): a stream is chat-stream, with its own engine entry.
	sub := "chat"
	if p.Stream {
		sub = "chat-stream"
	}
	if onGo, shadowed := e.mode(sub); !onGo {
		if shadowed {
			e.shadowRead(sub, w, r, s, &p, js, e.ownFn())
			return
		}
		js.ServeHTTP(w, r)
		return
	}
	if wait := e.limits.Take(p.Key.ID, p.Key.RPM, e.now()); wait > 0 {
		write(w, RateLimited(p.Key, wait))
		return
	}
	if p.Stream {
		e.serveStream(w, r, s, &p, req, started)
		return
	}
	res := e.upstream.Call(r.Context(), &p)
	ms := float64(time.Since(started).Microseconds()) / 1000
	var a Answer
	switch {
	case res.Why == "client gone":
		a = s.Respond(&p, res.Status, res.Body, ms) // the line is still written, as JS records the call
	case res.Why != "":
		a = s.Failed(&p, res.Why, ms)
	default:
		a = s.Respond(&p, res.Status, res.Body, ms)
	}
	// The usage line goes out before the answer: the control plane has it on its way when the client reads the answer
	// (it is stored a few milliseconds later, through the outbox and the ingest), as the JS gateway records before it ends.
	rec := a.Usage
	rec.Ts = e.now()
	if err := e.emit(&ipc.UsageReport{Record: rec}); err != nil {
		e.logf("go engine: the usage line could not be stored: %v", err)
	}
	if r.Context().Err() == nil {
		write(w, a)
	}
}

func write(w http.ResponseWriter, a Answer) {
	for k, v := range a.Header {
		w.Header()[k] = v
	}
	w.Header().Set("Content-Length", itoa(len(a.Body)))
	w.WriteHeader(a.Status)
	_, _ = w.Write(a.Body)
}

func itoa(n int) string { return NumberString(float64(n)) }

// serveStream answers a planned stream (P4c D3): the provider's headers, then each read rewritten and flushed at once,
// written synchronously (a slow client slows the provider read), and one usage line when it ends, whatever ended it,
// handed to emit after the response's last byte.
func (e *Engine) serveStream(w http.ResponseWriter, r *http.Request, s *State, p *Plan, req *Request, started time.Time) {
	e.streams.Add(1)
	elapsed := func() float64 { return float64(time.Since(started).Microseconds()) / 1000 }
	var line func() ipc.UsageRecord
	defer func() {
		if line == nil {
			e.streams.Done()
			return
		}
		rec := line()
		rec.Ts = e.now()
		go func() {
			defer e.streams.Done()
			if err := e.emit(&ipc.UsageReport{Record: rec}); err != nil {
				e.logf("go engine: the usage line could not be stored: %v", err)
			}
		}()
	}()
	resp, why := e.upstream.Start(r.Context(), p)
	if why != "" {
		a := s.Failed(p, why, elapsed())
		line = func() ipc.UsageRecord { return a.Usage }
		if r.Context().Err() == nil {
			write(w, a)
		}
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		a := s.Respond(p, resp.StatusCode, body, elapsed())
		line = func() ipc.UsageRecord { return a.Usage }
		if r.Context().Err() == nil {
			write(w, a)
		}
		return
	}
	defer resp.Body.Close()
	// The prompt is no longer needed: let it go for the length of the stream (Decision 11).
	p.Body, req.Body, r.Body = nil, nil, http.NoBody
	cs := NewChatStream(p.Model, p.WantsUsage, elapsed)
	line = func() ipc.UsageRecord { return s.StreamLine(p, cs, elapsed()) }
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	for _, c := range CORS {
		h.Set(c[0], c[1])
	}
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	if rc.Flush() != nil {
		return
	}
	buf := make([]byte, StreamReadSize)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			out, ferr := cs.Feed(buf[:n])
			if len(out) > 0 {
				if _, werr := w.Write(out); werr != nil || rc.Flush() != nil {
					return
				}
			}
			if ferr != nil {
				return // a line past 16 MiB: ended as a cut
			}
		}
		if errors.Is(err, io.EOF) {
			if out := cs.End(); len(out) > 0 {
				_, _ = w.Write(out)
				_ = rc.Flush()
			}
			return
		}
		if err != nil {
			return // the provider broke off, or the client left (its context cancelled the call)
		}
	}
}
