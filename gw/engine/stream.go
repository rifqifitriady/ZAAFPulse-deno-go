package engine

// The streaming chat route on the go engine (P4c, slice S3): lib/gateway/index.js chatCompletion's stream branch for an
// OpenAI-format provider. The provider's stream is split into lines on the byte \n (never part of a UTF-8 sequence) and
// each line is rewritten as chatCompletion's line() does: a data: line holding a JSON object or array is written again
// with the requested model name; without the client's include_usage, a usage-only chunk is dropped with the blank line
// after it and "usage": null is removed; every other line passes as it came, its \r dropped. Usage and the time to the
// first token are read on the way. The output does not depend on how the provider's bytes were split into reads.

import (
	"bytes"
	"errors"
	"strings"
	"unicode/utf8"
)

// MaxStreamLine: a provider line longer than this ends the stream as a cut does (chatCompletion has no limit).
const MaxStreamLine = 16 << 20

// ErrStreamLine is Feed's error for a line longer than MaxStreamLine.
var ErrStreamLine = errors.New("engine: a provider stream line is longer than 16 MiB")

var utf8BOM = []byte{0xef, 0xbb, 0xbf}

// ChatStream rewrites one provider stream for the client.
type ChatStream struct {
	model      string
	wantsUsage bool
	elapsed    func() float64
	buf        []byte // the bytes of a line not yet complete
	bomDone    bool
	skipBlank  bool

	TTFT                  *float64 // ms from the call's start to the first content or tool-call delta
	Input, Output, Cached int64    // the last usage chunk's tokens (tokens(): 0 when not a whole number)
	Unsupported           int      // data: lines JSON.parse reads and this engine does not (passed unchanged)
}

// NewChatStream starts a stream for the client's requested model name; wantsUsage is the client's
// stream_options.include_usage; elapsed gives ms since the call's start.
func NewChatStream(model string, wantsUsage bool, elapsed func() float64) *ChatStream {
	return &ChatStream{model: model, wantsUsage: wantsUsage, elapsed: elapsed}
}

// Feed takes the provider's next bytes and returns what the client gets now (nil: nothing yet). p is not kept: only
// an incomplete last line is copied, so a stream between events holds no buffer (P4c Decision 11).
func (s *ChatStream) Feed(p []byte) ([]byte, error) {
	data := p
	if len(s.buf) > 0 {
		s.buf = append(s.buf, p...)
		data = s.buf
	}
	if !s.bomDone {
		// TextDecoder drops one byte order mark at the start of the stream, also when it arrives in pieces.
		if len(data) < len(utf8BOM) && bytes.HasPrefix(utf8BOM, data) {
			s.buf = append(s.buf[:0], data...)
			return nil, nil
		}
		data = bytes.TrimPrefix(data, utf8BOM)
		s.bomDone = true
	}
	var out []byte
	start := 0
	for {
		nl := bytes.IndexByte(data[start:], '\n')
		if nl < 0 {
			break
		}
		raw := data[start : start+nl]
		start += nl + 1
		if s.skipBlank && jsBlank(raw) {
			s.skipBlank = false
			continue
		}
		s.skipBlank = false
		if w, keep := s.line(raw); keep {
			out = append(out, w...)
		} else {
			s.skipBlank = true
		}
	}
	rest := data[start:]
	switch {
	case len(rest) > MaxStreamLine:
		return out, ErrStreamLine
	case len(rest) == 0:
		s.buf = nil
	case len(s.buf) > 0 && cap(s.buf) <= 64<<10:
		s.buf = append(s.buf[:0], rest...) // rest lies in s.buf: copy moves it to the front
	default:
		s.buf = append([]byte(nil), rest...) // out of p, or out of a big line's buffer, which goes
	}
	return out, nil
}

// End is the provider's clean end: the last line if it had no \n, rewritten with one (chatCompletion's
// `if (buf) res.write(line(buf) || ”)`). Not called after a cut, which writes no partial line.
func (s *ChatStream) End() []byte {
	if !s.bomDone {
		return nil // at most a partial byte order mark, which TextDecoder holds and never flushes
	}
	rest := s.buf[:len(s.buf)-incompleteTail(s.buf)]
	s.buf = nil
	if len(rest) == 0 {
		return nil
	}
	if w, keep := s.line(rest); keep {
		return w
	}
	return nil
}

// line is chatCompletion's line(): the bytes for the client and true, or false for a dropped usage-only chunk.
func (s *ChatStream) line(raw []byte) ([]byte, bool) {
	raw = bytes.TrimSuffix(raw, []byte{'\r'})
	text := validText(raw)
	pass := func() ([]byte, bool) { return append([]byte(text), '\n'), true }
	// "data: [DONE]" needs no test of its own: JSON.parse("[DONE]") throws, and that passes the line as it is too.
	if !strings.HasPrefix(text, "data:") {
		return pass()
	}
	c, err := Parse([]byte(text[5:]))
	if err != nil {
		if errors.Is(err, ErrUnsupported) {
			s.Unsupported++
		}
		return pass()
	}
	if c.Kind != Object && c.Kind != Array {
		return pass()
	}
	d := firstElem(c.Get("choices")).Get("delta")
	if s.TTFT == nil && d.Truthy() && (d.Get("content").Truthy() || d.Get("tool_calls").Truthy()) {
		t := s.elapsed()
		s.TTFT = &t
	}
	if c.Get("usage").Truthy() {
		s.Input, s.Output, s.Cached = tokens(c)
	}
	if !s.wantsUsage {
		ch := c.Get("choices")
		if ch != nil && ch.Kind == Array && len(ch.A) == 0 && c.Get("usage").Truthy() && !c.Get("error").Truthy() {
			return nil, false
		}
		if u := c.Get("usage"); u != nil && u.Kind == Null {
			c.Delete("usage")
		}
	}
	if m := c.Get("model"); m != nil && m.Kind == String {
		c.Set("model", Str(s.model))
	}
	return append(append([]byte("data: "), c.Stringify()...), '\n'), true
}

// firstElem is `c.choices && c.choices[0]`: an array's first element or an object's key "0".
func firstElem(v *Value) *Value {
	if !v.Truthy() {
		return nil
	}
	switch v.Kind {
	case Array:
		if len(v.A) > 0 {
			return v.A[0]
		}
	case Object:
		return v.Get("0")
	}
	return nil
}

// validText decodes a line as the WHATWG UTF-8 decoder (TextDecoder) does: each maximal subpart of an invalid sequence
// becomes one U+FFFD (P4b's decodeUTF8 writes one per run; the stream path matches JS exactly).
func validText(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var out strings.Builder
	for i := 0; i < len(b); {
		c := b[i]
		if c < 0x80 {
			out.WriteByte(c)
			i++
			continue
		}
		need, lo, hi := seqShape(c)
		if need == 0 {
			out.WriteString("\uFFFD")
			i++
			continue
		}
		j := i + 1
		for ; j < i+need && j < len(b); j++ {
			l, h := byte(0x80), byte(0xbf)
			if j == i+1 {
				l, h = lo, hi
			}
			if b[j] < l || b[j] > h {
				break
			}
		}
		if j == i+need {
			out.Write(b[i:j])
		} else {
			out.WriteString("\uFFFD") // the bytes read so far are one maximal subpart; b[j] starts afresh
		}
		i = j
	}
	return out.String()
}

// seqShape is the length of the UTF-8 sequence lead byte c starts (0: not a lead byte) and the range of its second byte.
func seqShape(c byte) (need int, lo, hi byte) {
	lo, hi = 0x80, 0xbf
	switch {
	case c >= 0xc2 && c <= 0xdf:
		need = 2
	case c >= 0xe0 && c <= 0xef:
		need = 3
	case c >= 0xf0 && c <= 0xf4:
		need = 4
	}
	switch c {
	case 0xe0:
		lo = 0xa0
	case 0xed:
		hi = 0x9f
	case 0xf0:
		lo = 0x90
	case 0xf4:
		hi = 0x8f
	}
	return need, lo, hi
}

// jsBlank is `!raw.trim()` of the decoded line: only JavaScript white space and line terminators (jsSpace, keys.go).
func jsBlank(b []byte) bool {
	for len(b) > 0 {
		r, n := utf8.DecodeRune(b)
		if r == utf8.RuneError && n <= 1 || !jsSpace(r) {
			return false
		}
		b = b[n:]
	}
	return true
}

// incompleteTail is the length of an incomplete but valid UTF-8 sequence at the end of b, which TextDecoder in stream
// mode holds back (0 when the end is complete or already invalid).
func incompleteTail(b []byte) int {
	for n := 1; n <= 3 && n <= len(b); n++ {
		i := len(b) - n
		c := b[i]
		if c < 0x80 {
			return 0
		}
		if c < 0xc0 {
			continue // a continuation byte: its lead is further back
		}
		need, lo, hi := seqShape(c)
		if need == 0 || n >= need {
			return 0
		}
		if n >= 2 && (b[i+1] < lo || b[i+1] > hi) {
			return 0
		}
		return n
	}
	return 0
}
