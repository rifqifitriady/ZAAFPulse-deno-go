package engine

import (
	"bytes"
	"io"
	"net/http"
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
	if wait := e.limits.Take(p.Key.ID, p.Key.RPM, e.now()); wait > 0 {
		write(w, RateLimited(p.Key, wait))
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
