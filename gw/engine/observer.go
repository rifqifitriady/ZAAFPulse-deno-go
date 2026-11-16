package engine

import (
	"regexp"
	"strings"
)

// observerMax is lib/otel.js MAX_BODY: 1 MiB, counted in UTF-16 units of the decoded text.
const observerMax = 1024 * 1024

// Observed is responseObserver's result without the text: content capture keeps the go classes on js (P4f D10).
type Observed struct {
	ID      string
	Reasons []string
}

// Observer is lib/otel.js responseObserver(maxChars, false) (P4f D5): it reads what the engine writes back (JSON, or
// server-sent events, in any of the four formats: OpenAI chat, Anthropic Messages, Responses, Gemini) for the response
// id and the finish reasons. Known deviations, pinned by observer_test.go: an id that is not a string is ignored (JS
// keeps it, then loses the whole client span to a TypeError in finishModelSpan); a Gemini finishReason is lower-cased
// with strings.ToLower (JS uses full Unicode special casing); a JSON text the engine's parser refuses (an escaped lone
// surrogate, nesting deeper than 512) is skipped.
type Observer struct {
	pending []byte // the bytes of a character split across feeds (StringDecoder('utf8') holds them back)
	mode    byte   // 0 unknown, 'j' JSON, 's' server-sent events
	buf     strings.Builder
	size    int  // JSON mode: UTF-16 units seen
	over    bool // JSON mode: over observerMax, nothing more is read
	id      string
	reasons []string
}

// NewObserver starts an observer for one answer.
func NewObserver() *Observer { return &Observer{} }

// Feed is responseObserver's feed(chunk).
func (o *Observer) Feed(p []byte) {
	if o.over || len(p) == 0 {
		return
	}
	s := o.decode(p)
	if o.mode == 0 {
		head := strings.TrimLeftFunc(s, jsSpace)
		if head == "" {
			return
		}
		o.mode = 's'
		if head[0] == '{' || head[0] == '[' {
			o.mode = 'j'
		}
	}
	if o.mode == 'j' {
		o.size += utf16Len(s)
		if o.size > observerMax {
			o.over = true
			o.buf.Reset()
			return
		}
		o.buf.WriteString(s)
		return
	}
	o.buf.WriteString(s)
	text := o.buf.String()
	for {
		i := strings.IndexByte(text, '\n')
		if i < 0 {
			break
		}
		o.line(text[:i])
		text = text[i+1:]
	}
	o.buf.Reset()
	if utf16Len(text) <= observerMax {
		o.buf.WriteString(text)
	}
}

// decode is StringDecoder('utf8').write: an incomplete sequence at the end waits for the next feed; invalid bytes are
// replaced as the engine's TextDecoder port does.
func (o *Observer) decode(p []byte) string {
	b := p
	if len(o.pending) > 0 {
		b = append(append([]byte(nil), o.pending...), p...)
		o.pending = nil
	}
	if n := incompleteTail(b); n > 0 {
		o.pending = append([]byte(nil), b[len(b)-n:]...)
		b = b[:len(b)-n]
	}
	return validText(b)
}

// Result is responseObserver's result(): the id, and the finish reasons once each in first-seen order.
func (o *Observer) Result() *Observed {
	if o.buf.Len() > 0 {
		switch o.mode {
		case 'j':
			if v, err := Parse([]byte(o.buf.String())); err == nil {
				items := []*Value{v}
				if v.Kind == Array {
					items = v.A
				}
				for _, x := range items {
					if !o.absorb(x) {
						break // a TypeError inside absorb ends the loop: JS's try covers the whole for
					}
				}
			}
		case 's':
			o.line(o.buf.String())
		}
		o.buf.Reset()
	}
	seen := map[string]bool{}
	var rs []string
	for _, r := range o.reasons {
		if !seen[r] {
			seen[r] = true
			rs = append(rs, r)
		}
	}
	return &Observed{ID: o.id, Reasons: rs}
}

func (o *Observer) line(l string) {
	t := jsTrim(l)
	if !strings.HasPrefix(t, "data:") {
		return
	}
	d := jsTrim(t[5:])
	if d == "" || d == "[DONE]" {
		return
	}
	if v, err := Parse([]byte(d)); err == nil {
		o.absorb(v)
	}
}

func (o *Observer) reason(v *Value) {
	if v != nil && v.Kind == String && v.S != "" {
		o.reasons = append(o.reasons, v.S)
	}
}

// takeID is `st.id = st.id || x` for an id that is a non-empty string (Known deviation: a non-string id is ignored).
func (o *Observer) takeID(v *Value) {
	if o.id == "" && v != nil && v.Kind == String {
		o.id = v.S
	}
}

var responseEndRe = regexp.MustCompile(`^response\.(completed|incomplete|failed)$`)

// absorb is responseObserver's absorb(o); false where JavaScript would throw a TypeError (a property of null, or a for
// over a value that is not iterable), which ends the caller's try.
func (o *Observer) absorb(v *Value) bool {
	if v == nil || v.Kind != Object {
		return true // !o || typeof o !== 'object'; an array has none of the properties read below
	}
	typ := v.Get("type")
	if id := v.Get("id"); id != nil && id.Kind == String && o.id == "" && !typ.Truthy() {
		o.id = id.S
	}
	if r := v.Get("responseId"); r != nil && r.Kind == String && o.id == "" {
		o.id = r.S
	}
	choices, ok := iterate(v.Get("choices"))
	if !ok {
		return false
	}
	for _, c := range choices {
		if _, ok := prop(c, "message"); !ok {
			return false
		}
		fr, _ := prop(c, "finish_reason")
		o.reason(fr)
	}
	cands, ok := iterate(v.Get("candidates"))
	if !ok {
		return false
	}
	for _, c := range cands {
		content, ok := prop(c, "content")
		if !ok {
			return false
		}
		var parts *Value
		if content.Truthy() {
			parts, _ = prop(content, "parts")
		}
		ps, ok := iterate(parts)
		if !ok {
			return false
		}
		for _, p := range ps {
			if _, ok := prop(p, "text"); !ok {
				return false
			}
		}
		if fr, _ := prop(c, "finishReason"); fr.Truthy() {
			o.reason(Str(strings.ToLower(jsString(fr))))
		}
	}
	isStr := func(x *Value, s string) bool { return x != nil && x.Kind == String && x.S == s }
	content := v.Get("content")
	if isStr(typ, "message") || (content != nil && content.Kind == Array && isStr(v.Get("role"), "assistant")) {
		o.takeID(v.Get("id"))
		cs, ok := iterate(content)
		if !ok {
			return false
		}
		for _, c := range cs {
			if _, ok := prop(c, "type"); !ok {
				return false
			}
		}
		o.reason(v.Get("stop_reason"))
	}
	if msg := v.Get("message"); isStr(typ, "message_start") && msg.Truthy() {
		id, _ := prop(msg, "id")
		if id != nil && id.Kind == String && id.S != "" {
			o.id = id.S
		}
	}
	if d := v.Get("delta"); isStr(typ, "message_delta") && d.Truthy() {
		sr, _ := prop(d, "stop_reason")
		o.reason(sr)
	}
	var resp *Value
	switch {
	case isStr(v.Get("object"), "response"):
		resp = v
	case responseEndRe.MatchString(jsString(typ)):
		resp = v.Get("response")
	}
	if resp.Truthy() {
		if id, _ := prop(resp, "id"); id != nil && id.Kind == String && id.S != "" {
			o.id = id.S
		}
		if isStr(v.Get("object"), "response") {
			out, _ := prop(resp, "output")
			items, ok := iterate(out)
			if !ok {
				return false
			}
			for _, it := range items {
				ic, ok := prop(it, "content")
				if !ok {
					return false
				}
				cs, ok := iterate(ic)
				if !ok {
					return false
				}
				for _, c := range cs {
					if _, ok := prop(c, "type"); !ok {
						return false
					}
				}
			}
		}
		status, _ := prop(resp, "status")
		switch {
		case isStr(status, "completed"):
			o.reason(Str("stop"))
		case isStr(status, "incomplete"):
			var r *Value
			if d, _ := prop(resp, "incomplete_details"); d.Truthy() {
				r, _ = prop(d, "reason")
			}
			if !r.Truthy() {
				r = Str("length")
			}
			o.reason(r)
		case isStr(status, "failed"):
			o.reason(Str("error"))
		}
	}
	return true
}

// iterate is `for (const c of x || [])`: a falsy value gives nothing, an array its items, a string one item per code
// point; any other value is not iterable (false: a TypeError).
func iterate(x *Value) ([]*Value, bool) {
	if !x.Truthy() {
		return nil, true
	}
	switch x.Kind {
	case Array:
		return x.A, true
	case String:
		var out []*Value
		for _, r := range x.S {
			out = append(out, Str(string(r)))
		}
		return out, true
	}
	return nil, false
}

// prop is `c[k]` on an item: null throws (false); a primitive or an array has none of the keys read here (nil).
func prop(c *Value, k string) (*Value, bool) {
	if c == nil {
		return nil, true // undefined is never an item here; a missing property reads as undefined
	}
	switch c.Kind {
	case Null:
		return nil, false
	case Object:
		return c.Get(k), true
	}
	return nil, true
}

// jsString is String(v) for a JSON value; undefined (nil) is "undefined".
func jsString(v *Value) string {
	if v == nil {
		return "undefined"
	}
	switch v.Kind {
	case Null:
		return "null"
	case Bool:
		if v.B {
			return "true"
		}
		return "false"
	case Number:
		return NumberString(v.N)
	case String:
		return v.S
	case Array:
		parts := make([]string, len(v.A))
		for i, x := range v.A {
			if x != nil && x.Kind != Null {
				parts[i] = jsString(x) // Array.prototype.join writes null and undefined as ""
			}
		}
		return strings.Join(parts, ",")
	}
	return "[object Object]"
}
