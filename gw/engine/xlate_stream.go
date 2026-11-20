package engine

// Streams of translated calls (P4d): lib/gateway/index.js translated()'s stream branch. The provider's OpenAI chunks
// (its SSE, a whole JSON answer as chunks, or the Anthropic leg's synthesized stream) are turned into the client's
// events by gw/translate's stream writers. Pure over the provider's body: the caller writes the head, sends the pings
// and emits the line.

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"
)

// translateHook, when set (tests only), runs at a translation step ("push": each chunk of a stream; "whole": a whole
// answer's translation): a panic there shows the call still ends with its line (review I3).
var translateHook func(stage string)

// PingEvery is translated()'s ping interval for Anthropic clients (10 s); tests shorten it.
var PingEvery = 10 * time.Second

// clientStream is the writer of the client's format.
type clientStream interface {
	Push(c jsv.Value) (string, error)
	Finish() (string, error)
	Error(message jsv.Value) string
}

// TranslatedStream is one translated stream.
type TranslatedStream struct {
	p       *Plan
	s       *State
	out     clientStream
	open    func() string
	ping    func() string
	elapsed func() float64
	// beforeEnd runs before the stream's last event (the finish or an error): the pings stop there (review m1).
	beforeEnd func()
	// The line's facts: set only when the provider's answer was read to its end (JS keeps its first result
	// otherwise: no ttft and no tokens after an error).
	ttft           *float64
	in, ou, cached int64
}

// NewTranslatedStream starts the client's stream for a plan; elapsed gives ms since the call's start.
func (s *State) NewTranslatedStream(p *Plan, elapsed func() float64) *TranslatedStream {
	t := &TranslatedStream{p: p, s: s, elapsed: elapsed}
	switch p.Format {
	case "anthropic":
		a := p.Env.NewAnthropicStream(p.ClientModel)
		t.out, t.open, t.ping = a, a.Open, a.Ping
	case "responses":
		r := p.Env.NewResponsesStream(p.ClientModel, p.Custom)
		t.out, t.open = r, r.Open
	default:
		t.out = p.Env.NewGeminiStream(p.ClientModel)
	}
	return t
}

// Open is the opening event (message_start, response.created), "" for Gemini.
func (t *TranslatedStream) Open() string {
	if t.open == nil {
		return ""
	}
	return t.open()
}

// Ping is the keep-alive event, nil when the format has none.
func (t *TranslatedStream) Ping() func() string { return t.ping }

// errWrite marks a failed client write: the client is gone.
var errWrite = errors.New("engine: the client's write failed")

// Run reads the provider's answer (for the Anthropic leg, AnthropicToChatText's stream, made before the stream:
// callChat draws its id first) and writes the client's events through write. It returns when the answer ends (the
// finish event written), or after an error event: the provider broke off, it sent an error chunk, or a translation
// step threw. A failed write ends it at once.
func (t *TranslatedStream) Run(body io.Reader, contentType string, write func(string) error) {
	put := func(s string) error {
		if s == "" {
			return nil
		}
		if write(s) != nil {
			return errWrite
		}
		return nil
	}
	// A panic in a translation step (a port bug) ends the stream as JS's catch ends a TypeError: an error event, and
	// the line without a time to the first token or tokens (review I3). The call stays metered.
	end := func() {
		if t.beforeEnd != nil {
			t.beforeEnd()
		}
	}
	defer func() {
		if v := recover(); v != nil {
			t.ttft, t.in, t.ou, t.cached = nil, 0, 0, 0
			end()
			_ = put(t.out.Error(t.s.mask(panicText(v))))
		}
	}()
	onChunk := func(c jsv.Value) error {
		if e := jsv.Get(c, "error"); jsv.Truthy(e) {
			return errors.New(jsv.ToString(jsv.Or(jsv.Get(e, "message"), "upstream error")))
		}
		if translateHook != nil {
			translateHook("push")
		}
		w, err := t.out.Push(c)
		if err != nil {
			return err
		}
		return put(w)
	}
	var ttft *float64
	var in, ou, cached int64
	var err error
	if strings.Contains(contentType, "json") {
		err = jsv.Catch(func() {
			raw, rerr := io.ReadAll(body)
			if rerr != nil {
				panic(&jsv.TypeError{Message: readError(rerr)})
			}
			j, perr := jsv.Parse([]byte(decodeWHATWG(raw)))
			if perr != nil {
				panic(&jsv.TypeError{Message: "the provider's answer is not JSON"}) // P4d Decision 26a
			}
			chunks, cerr := translate.WholeToChunks(j)
			if cerr != nil {
				panic(cerr)
			}
			for _, c := range chunks {
				if e := onChunk(c); e != nil {
					panic(e)
				}
			}
			ms := t.elapsed()
			ttft = &ms
			in, ou, cached = tokensJSV(j)
		})
	} else {
		err = translate.ReadSSE(body, func(_, data string) error {
			if data == "[DONE]" {
				return nil
			}
			c, perr := jsv.Parse([]byte(data))
			if perr != nil {
				return nil
			}
			return jsv.Catch(func() {
				var d jsv.Value = jsv.Get(c, "choices")
				if jsv.Truthy(d) {
					d = jsv.Get(d, "0")
				}
				if jsv.Truthy(d) {
					d = jsv.Get(d, "delta")
				}
				if ttft == nil && jsv.Truthy(d) && (jsv.Truthy(jsv.Get(d, "content")) || jsv.Truthy(jsv.Get(d, "tool_calls"))) {
					ms := t.elapsed()
					ttft = &ms
				}
				if jsv.Truthy(jsv.Get(c, "usage")) {
					in, ou, cached = tokensJSV(c)
				}
				if e := onChunk(c); e != nil {
					panic(e)
				}
			})
		})
	}
	if errors.Is(err, errWrite) {
		return
	}
	if err != nil {
		end()
		_ = put(t.out.Error(t.s.mask(readError(err))))
		return
	}
	t.ttft, t.in, t.ou, t.cached = ttft, in, ou, cached
	f, ferr := t.out.Finish()
	end()
	if ferr != nil {
		_ = put(t.out.Error(t.s.mask(ferr.Error())))
		return
	}
	_ = put(f)
}

// panicText is a recovered panic's message: an error's text, or the value as text.
func panicText(v any) string {
	if err, ok := v.(error); ok {
		return readError(err)
	}
	return fmt.Sprint(v)
}

// readError is an error's message as the JS catch reads it; a provider that breaks off reads "terminated".
func readError(err error) string {
	var te *jsv.TypeError
	switch {
	case errors.As(err, &te):
		return te.Message
	case errors.Is(err, ErrCut), errors.Is(err, io.ErrUnexpectedEOF):
		return ErrCut.Error()
	}
	return err.Error()
}

// Line is translated()'s line after a stream: status 200, the time to the first token, the tokens of the last usage
// seen (none after an error).
func (t *TranslatedStream) Line(ms float64) ipc.UsageRecord {
	rec := t.p.Base
	rec.Status, rec.Ms = 200, ms
	in, out, cached := t.in, t.ou, t.cached
	rec.Input, rec.Output, rec.Cached = &in, &out, &cached
	rec.TTFT = ipc.Null[float64]()
	if t.ttft != nil {
		rec.TTFT = ipc.Some(*t.ttft)
	}
	rec.Cost = t.s.costOf(&rec)
	return rec
}

// deliverTranslatedStream is translated()'s stream branch for a 2xx answer.
func (s *State) deliverTranslatedStream(p *Plan, res ProviderResult, elapsed func() float64, w ClientWriter) *ipc.UsageRecord {
	body, contentType := io.Reader(res.Body), res.Header.Get("Content-Type")
	if p.Upstream == UpAnthropic {
		r := AnthropicToChatText(p.ClientModel, res.Body, p.Env)
		defer r.Close()
		body, contentType = r, "text/event-stream"
	}
	ts := s.NewTranslatedStream(p, elapsed)
	if w.Head(http.StatusOK, droppedHeader(sseHeader(), p)) == nil {
		write := func(s string) error { return w.Write([]byte(s)) }
		// The opening event first, then the pings (JS's interval cannot fire before open is written); they stop before
		// the last event (review m1).
		if open := ts.Open(); open == "" || write(open) == nil {
			if ping := ts.Ping(); ping != nil {
				w.Pinger(ping)
				ts.beforeEnd = w.StopPings
			}
			ts.Run(body, contentType, write)
		}
		w.StopPings()
	}
	return lineOf(ts.Line(elapsed()))
}
