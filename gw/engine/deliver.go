package engine

// Deliver turns a provider's answer into the client's answer and the call's usage line for the P4d paths, the same way
// for the fixtures (a recording writer) and for serving (an HTTP writer that flushes and sends pings). It is pure apart
// from reading the provider's body and writing to w.

import (
	"errors"
	"io"
	"net/http"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

// ProviderResult is a provider call: its status, headers and body (unread), or Why it failed (the text after
// "<provider id>: ", as Upstream gives it). The body is closed by Deliver.
type ProviderResult struct {
	Status int
	Header http.Header
	Body   io.ReadCloser
	Why    string
}

// ClientWriter is where Deliver writes: the head once, then the bytes (each Write is flushed). Pinger starts the
// keep-alive events of a stream (ping gives the text): the writer sends one every PingEvery between writes until
// StopPings, which Deliver calls before the stream's last event; a writer that does not ping ignores both.
type ClientWriter interface {
	Head(status int, h http.Header) error
	Write(b []byte) error
	Pinger(ping func() string)
	StopPings()
}

// errNotServed: Deliver was given a plan it has no answer builder for (a bug: the planner and Deliver disagree).
var errNotServed = errors.New("engine: no answer builder for this plan")

// Deliver writes the client's answer for a served call and returns its usage line (nil: the JS writes none).
func (s *State) Deliver(p *Plan, res ProviderResult, elapsed func() float64, w ClientWriter) *ipc.UsageRecord {
	if res.Body != nil {
		defer res.Body.Close()
	}
	switch {
	case p.Upstream == UpAnthropic && p.Format == "openai":
		return s.deliverChatAnthropic(p, res, elapsed, w)
	case p.Upstream == UpChat || p.Upstream == UpAnthropic:
		if !p.Stream || res.Why != "" || !ok2xx(res.Status) {
			return s.deliverTranslated(p, res, elapsed, w)
		}
		return s.deliverTranslatedStream(p, res, elapsed, w)
	case p.Upstream == UpAnthropicNative || p.Upstream == UpResponsesNative:
		return s.deliverNative(p, res, elapsed, w)
	case p.Upstream == UpCount:
		return s.deliverCount(p, res, w)
	case p.Upstream == UpEmbeddings:
		return s.deliverEmbeddings(p, res, elapsed, w)
	}
	panic(errNotServed)
}

// writeAnswer writes a whole answer and returns its line (nil when it has none).
func writeAnswer(w ClientWriter, a Answer) *ipc.UsageRecord {
	if w.Head(a.Status, a.Header) == nil {
		_ = w.Write(a.Body)
	}
	if a.NoLine {
		return nil
	}
	u := a.Usage
	return &u
}

func ok2xx(status int) bool { return status >= 200 && status <= 299 }

// sseHeader is the head of every gateway event stream: text/event-stream, no-store, CORS.
func sseHeader() http.Header {
	h := http.Header{}
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	for _, c := range CORS {
		h.Set(c[0], c[1])
	}
	return h
}

// deliverChatAnthropic is chatCompletion over callChat's Anthropic branch: a failed call is proxyChat's 502; a non-2xx
// answer goes back as the provider gave it; a whole answer is converted; a stream is the synthesized OpenAI stream
// through chatCompletion's line rewriter.
func (s *State) deliverChatAnthropic(p *Plan, res ProviderResult, elapsed func() float64, w ClientWriter) *ipc.UsageRecord {
	if res.Why != "" {
		return writeAnswer(w, s.Failed(p, res.Why, elapsed()))
	}
	if !p.Stream || !ok2xx(res.Status) {
		raw, _ := io.ReadAll(res.Body)
		if !ok2xx(res.Status) {
			return writeAnswer(w, s.Respond(p, res.Status, raw, elapsed()))
		}
		conv, why := anthropicWhole(p.Env, raw, p.ClientModel)
		if why != "" {
			return writeAnswer(w, s.Failed(p, why, elapsed())) // P4d Decision 26a: JS writes V8's text
		}
		return writeAnswer(w, s.Respond(p, 200, conv, elapsed()))
	}
	cs := NewChatStream(p.Model, p.WantsUsage, elapsed)
	if w.Head(200, sseHeader()) != nil {
		return lineOf(s.StreamLine(p, cs, elapsed()))
	}
	text := anthropicText(res.Status, p.ClientModel, res.Body, p.Env)
	defer text.Close()
	buf := make([]byte, StreamReadSize)
	for {
		n, err := text.Read(buf)
		if n > 0 {
			out, ferr := cs.Feed(buf[:n])
			if len(out) > 0 && w.Write(out) != nil {
				break
			}
			if ferr != nil {
				break
			}
		}
		if errors.Is(err, io.EOF) {
			if out := cs.End(); len(out) > 0 {
				_ = w.Write(out)
			}
			break
		}
		if err != nil {
			break
		}
	}
	return lineOf(s.StreamLine(p, cs, elapsed()))
}

func lineOf(rec ipc.UsageRecord) *ipc.UsageRecord { return &rec }
