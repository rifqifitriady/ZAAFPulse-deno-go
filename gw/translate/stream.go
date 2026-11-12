package translate

import (
	"bytes"
	"errors"
	"io"
	"strings"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"
)

// sse is `event: ${event}\ndata: ${JSON.stringify(data)}\n\n`.
func sse(event string, data *jsv.Object) string {
	return "event: " + event + "\ndata: " + jsv.MustStringify(data) + "\n\n"
}

// mapKey is a JS Map key for a value (SameValueZero on the primitives the streams use).
func mapKey(v V) string { return jsv.TypeOf(v) + ":" + str(v) }

// streamIndex is `tc.index ?? 0`.
func streamIndex(tc V) string { return mapKey(jsv.Nullish(get(tc, "index"), 0.0)) }

// fnField is `(tc.function && tc.function.<k>)`.
func fnField(tc V, k string) V {
	fn := get(tc, "function")
	if !truthy(fn) {
		return fn
	}
	return get(fn, k)
}

// firstStreamChoice is `c.choices && c.choices[0]`.
func firstStreamChoice(c V) V {
	ch := get(c, "choices")
	if truthy(ch) {
		ch = get(ch, "0")
	}
	return ch
}

/* ---------- OpenAI chunks → Anthropic SSE events ---------- */

type anthropicTool struct {
	id, up V
	index  float64
}

// AnthropicStream turns OpenAI chat chunks into Anthropic SSE events (the JS anthropicStream). Each method returns
// the text to write.
type AnthropicStream struct {
	env     *Env
	model   V
	id      string
	started bool
	open    string // "", "text" or "tool"
	index   float64
	stop    V
	usage   V
	tools   map[string]*anthropicTool
	out     []string
}

// NewAnthropicStream starts a stream; it draws the message id at once, as the JS does.
func (e *Env) NewAnthropicStream(model V) *AnthropicStream {
	return &AnthropicStream{env: e, model: model, id: e.rid("msg_"), index: -1, stop: nil, usage: jsv.NewObject(),
		tools: map[string]*anthropicTool{}}
}

// NewAnthropicStream on the Default env.
func NewAnthropicStream(model V) *AnthropicStream { return Default.NewAnthropicStream(model) }

func (s *AnthropicStream) start() {
	if s.started {
		return
	}
	s.started = true
	s.out = append(s.out,
		sse("message_start", obj("type", "message_start", "message", obj("id", s.id, "type", "message",
			"role", "assistant", "model", s.model, "content", jsv.Arr(), "stop_reason", nil, "stop_sequence", nil,
			"usage", obj("input_tokens", 0.0, "output_tokens", 0.0)))),
		sse("ping", obj("type", "ping")))
}

func (s *AnthropicStream) close() {
	if s.open != "" {
		s.out = append(s.out, sse("content_block_stop", obj("type", "content_block_stop", "index", s.index)))
		s.open = ""
	}
}

func (s *AnthropicStream) drain() string {
	t := strings.Join(s.out, "")
	s.out = nil
	return t
}

func (s *AnthropicStream) push(c V) string {
	e := s.env
	s.start()
	if u := get(c, "usage"); truthy(u) {
		s.usage = u
	}
	ch := firstStreamChoice(c)
	if !truthy(ch) {
		return s.drain()
	}
	d := or(get(ch, "delta"), jsv.NewObject())
	if content := get(d, "content"); truthy(content) {
		if s.open != "text" {
			s.close()
			s.index++
			s.open = "text"
			s.out = append(s.out, sse("content_block_start", obj("type", "content_block_start", "index", s.index,
				"content_block", obj("type", "text", "text", ""))))
		}
		s.out = append(s.out, sse("content_block_delta", obj("type", "content_block_delta", "index", s.index,
			"delta", obj("type", "text_delta", "text", content))))
	}
	for _, tc := range listOr(get(d, "tool_calls")) {
		k := streamIndex(tc)
		id := get(tc, "id")
		// A new id at a known index is a new call: some providers send parallel calls all at index 0.
		if t, ok := s.tools[k]; !ok || (truthy(id) && truthy(t.up) && !eq(t.up, id)) {
			s.close()
			s.index++
			tid := id
			if !truthy(tid) {
				tid = e.rid("toolu_")
			}
			t = &anthropicTool{id: tid, up: id, index: s.index}
			s.tools[k] = t
			s.open = "tool"
			s.out = append(s.out, sse("content_block_start", obj("type", "content_block_start", "index", s.index,
				"content_block", obj("type", "tool_use", "id", t.id, "name", or(fnField(tc, "name"), ""), "input", jsv.NewObject()))))
		}
		if args := fnField(tc, "arguments"); truthy(args) {
			s.out = append(s.out, sse("content_block_delta", obj("type", "content_block_delta",
				"index", s.tools[k].index, "delta", obj("type", "input_json_delta", "partial_json", args))))
		}
	}
	if fr := get(ch, "finish_reason"); truthy(fr) {
		s.stop = fr
	}
	return s.drain()
}

// Push takes one OpenAI chunk and returns the events to write.
func (s *AnthropicStream) Push(c V) (out string, err error) {
	err = catch(func() { out = s.push(c) })
	return
}

// Finish closes the open block and writes message_delta and message_stop.
func (s *AnthropicStream) Finish() (out string, err error) {
	err = catch(func() {
		s.start()
		s.close()
		u := usageParts(s.usage)
		stop := lookup(ToAnthropicStop, s.stop)
		if !truthy(stop) {
			stop = "end_turn"
			if len(s.tools) > 0 {
				stop = "tool_use"
			}
		}
		s.out = append(s.out,
			sse("message_delta", obj("type", "message_delta", "delta", obj("stop_reason", stop, "stop_sequence", nil),
				"usage", obj("input_tokens", jsMax(0.0, sub(u.prompt, u.cached)), "output_tokens", u.output,
					"cache_read_input_tokens", u.cached))),
			sse("message_stop", obj("type", "message_stop")))
		out = s.drain()
	})
	return
}

// Open writes message_start (and its ping): it must be the first event a client sees.
func (s *AnthropicStream) Open() string {
	s.start()
	return s.drain()
}

// Ping is one ping event.
func (s *AnthropicStream) Ping() string { return sse("ping", obj("type", "ping")) }

// Error is an error event.
func (s *AnthropicStream) Error(message V) string {
	return sse("error", obj("type", "error", "error", obj("type", "api_error", "message", message)))
}

/* ---------- Anthropic SSE events → OpenAI chunks ---------- */

// AnthropicEvents turns parsed Anthropic SSE data objects into OpenAI chunks (the JS anthropicEventsToOpenAI).
type AnthropicEvents struct {
	model   V
	id      string
	created float64
	usage   *jsv.Object
	blocks  map[string]float64
	toolN   float64
}

// NewAnthropicEvents starts a translation; the chunk id and created time are drawn at once, as the JS does.
func (e *Env) NewAnthropicEvents(model V) *AnthropicEvents {
	return &AnthropicEvents{model: model, id: e.rid("chatcmpl-"), created: e.seconds(), usage: jsv.NewObject(),
		blocks: map[string]float64{}, toolN: -1}
}

// NewAnthropicEvents on the Default env.
func NewAnthropicEvents(model V) *AnthropicEvents { return Default.NewAnthropicEvents(model) }

func (a *AnthropicEvents) chunk(delta *jsv.Object, finish V) *jsv.Object {
	return obj("id", a.id, "object", "chat.completion.chunk", "created", a.created, "model", a.model,
		"choices", jsv.Arr(obj("index", 0.0, "delta", delta, "finish_reason", finish)))
}

// Next takes one event and returns the chunks for it (often none).
func (a *AnthropicEvents) Next(ev V) (out []V, err error) {
	err = catch(func() { out = a.next(ev) })
	return
}

func (a *AnthropicEvents) next(ev V) []V {
	switch t := get(ev, "type"); {
	case eq(t, "message_start"):
		m := get(ev, "message")
		var u V = m
		if truthy(m) {
			u = get(m, "usage")
		}
		a.usage = jsv.NewObject()
		if o := jsv.Obj(u); o != nil {
			a.usage = o.Clone()
		}
		return []V{a.chunk(obj("role", "assistant", "content", ""), nil)}
	case eq(t, "content_block_start") && truthy(get(ev, "content_block")) && eq(get(get(ev, "content_block"), "type"), "tool_use"):
		cb := get(ev, "content_block")
		a.toolN++
		a.blocks[mapKey(get(ev, "index"))] = a.toolN
		return []V{a.chunk(obj("tool_calls", jsv.Arr(obj("index", a.toolN, "id", get(cb, "id"), "type", "function",
			"function", obj("name", get(cb, "name"), "arguments", "")))), nil)}
	case eq(t, "content_block_delta"):
		d := or(get(ev, "delta"), jsv.NewObject())
		switch dt := get(d, "type"); {
		case eq(dt, "text_delta"):
			return []V{a.chunk(obj("content", get(d, "text")), nil)}
		case eq(dt, "thinking_delta"):
			return []V{a.chunk(obj("reasoning_content", get(d, "thinking")), nil)}
		case eq(dt, "input_json_delta"):
			if n, ok := a.blocks[mapKey(get(ev, "index"))]; ok {
				return []V{a.chunk(obj("tool_calls", jsv.Arr(obj("index", n, "function",
					obj("arguments", get(d, "partial_json"))))), nil)}
			}
		}
		return []V{}
	case eq(t, "message_delta"):
		if u := jsv.Obj(get(ev, "usage")); u != nil {
			a.usage = a.usage.Clone().Assign(u)
		}
		d := get(ev, "delta")
		var sr V = d
		if truthy(d) {
			sr = get(d, "stop_reason")
		}
		return []V{a.chunk(jsv.NewObject(), or(lookup(FromAnthropicStop, sr), "stop"))}
	case eq(t, "message_stop"):
		return []V{obj("id", a.id, "object", "chat.completion.chunk", "created", a.created, "model", a.model,
			"choices", jsv.Arr(), "usage", anthropicUsageToOpenAI(a.usage))}
	case eq(t, "error"):
		return []V{obj("error", or(get(ev, "error"), obj("message", "upstream error")))}
	}
	return []V{}
}

/* ---------- OpenAI chunks → Responses streaming events ---------- */

type responsesText struct {
	id  string
	oi  float64
	buf V
}

type responsesTool struct {
	oi           float64
	id, up, name V
	args         V
	itemID       V
}

// ResponsesStream turns OpenAI chat chunks into Responses streaming events (the JS responsesStream).
type ResponsesStream struct {
	env     *Env
	model   V
	custom  *customSet
	id      string
	created float64
	seq     float64
	started bool
	usage   V
	finish  V
	output  []V
	text    *responsesText
	tools   map[string]*responsesTool
	calls   []*responsesTool
	out     []string
}

// NewResponsesStream starts a stream; id and created are drawn at once, as the JS does.
func (e *Env) NewResponsesStream(model V, custom []string) *ResponsesStream {
	return &ResponsesStream{env: e, model: model, custom: newCustomSet(custom), id: e.rid("resp_"),
		created: e.seconds(), usage: jsv.NewObject(), tools: map[string]*responsesTool{}}
}

// NewResponsesStream on the Default env.
func NewResponsesStream(model V, custom []string) *ResponsesStream {
	return Default.NewResponsesStream(model, custom)
}

func (s *ResponsesStream) ev(typ string, data *jsv.Object) {
	o := obj("type", typ, "sequence_number", s.seq)
	s.seq++
	s.out = append(s.out, sse(typ, o.Assign(data)))
}

func (s *ResponsesStream) base(status string) *jsv.Object {
	return obj("id", s.id, "object", "response", "created_at", s.created, "status", status, "model", s.model,
		"output", jsv.Arr())
}

func (s *ResponsesStream) start() {
	if s.started {
		return
	}
	s.started = true
	s.ev("response.created", obj("response", s.base("in_progress")))
	s.ev("response.in_progress", obj("response", s.base("in_progress")))
}

func (s *ResponsesStream) closeText() {
	t := s.text
	if t == nil {
		return
	}
	part := obj("type", "output_text", "text", t.buf, "annotations", jsv.Arr())
	item := obj("type", "message", "id", t.id, "status", "completed", "role", "assistant", "content", jsv.Arr(part))
	s.ev("response.output_text.done", obj("item_id", t.id, "output_index", t.oi, "content_index", 0.0, "text", t.buf))
	s.ev("response.content_part.done", obj("item_id", t.id, "output_index", t.oi, "content_index", 0.0, "part", part))
	s.ev("response.output_item.done", obj("output_index", t.oi, "item", item))
	s.output[int(t.oi)] = item
	s.text = nil
}

func (s *ResponsesStream) drain() string {
	t := strings.Join(s.out, "")
	s.out = nil
	return t
}

// Push takes one OpenAI chunk and returns the events to write.
func (s *ResponsesStream) Push(c V) (out string, err error) {
	err = catch(func() { out = s.push(c) })
	return
}

func (s *ResponsesStream) push(c V) string {
	s.start()
	if u := get(c, "usage"); truthy(u) {
		s.usage = u
	}
	ch := firstStreamChoice(c)
	if !truthy(ch) {
		return s.drain()
	}
	d := or(get(ch, "delta"), jsv.NewObject())
	if content := get(d, "content"); truthy(content) {
		if s.text == nil {
			s.text = &responsesText{id: s.env.rid("msg_"), oi: float64(len(s.output)), buf: ""}
			s.output = append(s.output, nil)
			s.ev("response.output_item.added", obj("output_index", s.text.oi, "item", obj("type", "message",
				"id", s.text.id, "status", "in_progress", "role", "assistant", "content", jsv.Arr())))
			s.ev("response.content_part.added", obj("item_id", s.text.id, "output_index", s.text.oi,
				"content_index", 0.0, "part", obj("type", "output_text", "text", "", "annotations", jsv.Arr())))
		}
		s.text.buf = add(s.text.buf, content)
		s.ev("response.output_text.delta", obj("item_id", s.text.id, "output_index", s.text.oi,
			"content_index", 0.0, "delta", content))
	}
	for _, tc := range listOr(get(d, "tool_calls")) {
		k := streamIndex(tc)
		id := get(tc, "id")
		// A new id at a known index is a new call (parallel calls that all arrive at index 0).
		if t, ok := s.tools[k]; !ok || (truthy(id) && truthy(t.up) && !eq(t.up, id)) {
			s.closeText()
			tid := id
			if !truthy(tid) {
				tid = s.env.rid("call_")
			}
			t = &responsesTool{oi: float64(len(s.output)), id: tid, up: id, name: or(fnField(tc, "name"), ""), args: ""}
			s.tools[k] = t
			s.calls = append(s.calls, t)
			s.output = append(s.output, nil)
			item := s.env.toolItem(obj("id", t.id, "function", obj("name", t.name, "arguments", "")), s.custom, "in_progress")
			t.itemID = item.Get("id")
			var shown V = item
			if s.custom.contains(t.name) {
				shown = item.Clone().Set("input", "")
			}
			s.ev("response.output_item.added", obj("output_index", t.oi, "item", shown))
		}
		t := s.tools[k]
		a := or(fnField(tc, "arguments"), "")
		if n := fnField(tc, "name"); truthy(get(tc, "function")) && truthy(n) && !truthy(t.name) {
			t.name = n
		}
		if truthy(a) {
			t.args = add(t.args, a)
			if !s.custom.contains(t.name) {
				s.ev("response.function_call_arguments.delta", obj("item_id", t.itemID, "output_index", t.oi, "delta", a))
			}
		}
	}
	if fr := get(ch, "finish_reason"); truthy(fr) {
		s.finish = fr
	}
	return s.drain()
}

// Finish closes every item and writes response.completed (or response.incomplete).
func (s *ResponsesStream) Finish() (out string, err error) {
	err = catch(func() {
		s.start()
		s.closeText()
		for _, t := range s.calls {
			item := s.env.toolItem(obj("id", t.id, "function", obj("name", t.name, "arguments", or(t.args, "{}"))), s.custom, "completed")
			item = item.Clone().Set("id", t.itemID)
			if eq(item.Get("type"), "function_call") {
				s.ev("response.function_call_arguments.done", obj("item_id", t.itemID, "output_index", t.oi,
					"arguments", item.Get("arguments")))
			}
			s.ev("response.output_item.done", obj("output_index", t.oi, "item", item))
			s.output[int(t.oi)] = item
		}
		incomplete := eq(s.finish, "length")
		status := "completed"
		if incomplete {
			status = "incomplete"
		}
		kept := []V{}
		for _, o := range s.output {
			if truthy(o) {
				kept = append(kept, o)
			}
		}
		response := s.base(status).Set("output", arr(kept)).Set("usage", responsesUsage(s.usage))
		if incomplete {
			response.Set("incomplete_details", obj("reason", "max_output_tokens"))
			s.ev("response.incomplete", obj("response", response))
		} else {
			s.ev("response.completed", obj("response", response))
		}
		out = s.drain()
	})
	return
}

// Open writes response.created at once, so the client sees the first byte before the model answers.
func (s *ResponsesStream) Open() string {
	s.start()
	return s.drain()
}

// Error is a response.failed event.
func (s *ResponsesStream) Error(message V) string {
	o := obj("type", "response.failed", "sequence_number", s.seq, "response",
		s.base("failed").Set("error", obj("code", "upstream_error", "message", message)))
	s.seq++
	return sse("response.failed", o)
}

/* ---------- OpenAI chunks → Gemini stream ---------- */

type geminiTool struct{ name, args, up V }

// GeminiStream turns OpenAI chat chunks into Gemini streamGenerateContent data lines (the JS geminiStream).
type GeminiStream struct {
	model  V
	tools  map[string]*geminiTool
	calls  []*geminiTool
	finish V
	usage  V
}

// NewGeminiStream starts a stream.
func (e *Env) NewGeminiStream(model V) *GeminiStream {
	return &GeminiStream{model: model, tools: map[string]*geminiTool{}}
}

// NewGeminiStream on the Default env.
func NewGeminiStream(model V) *GeminiStream { return Default.NewGeminiStream(model) }

func geminiData(o *jsv.Object) string { return "data: " + jsv.MustStringify(o) + "\n\n" }

// Push takes one OpenAI chunk and returns the data to write (text only; calls come at Finish).
func (s *GeminiStream) Push(c V) (out string, err error) {
	err = catch(func() {
		if u := get(c, "usage"); truthy(u) {
			s.usage = u
		}
		ch := firstStreamChoice(c)
		if !truthy(ch) {
			return
		}
		d := or(get(ch, "delta"), jsv.NewObject())
		if content := get(d, "content"); truthy(content) {
			out += geminiData(obj("candidates", jsv.Arr(obj("content", obj("role", "model",
				"parts", jsv.Arr(obj("text", content))), "index", 0.0)), "modelVersion", s.model))
		}
		for _, tc := range listOr(get(d, "tool_calls")) {
			k := streamIndex(tc)
			id := get(tc, "id")
			t := s.tools[k]
			if t == nil || (truthy(id) && truthy(t.up) && !eq(t.up, id)) {
				t = &geminiTool{name: "", args: "", up: id}
				s.tools[k] = t
				s.calls = append(s.calls, t)
			}
			if n := fnField(tc, "name"); truthy(n) {
				t.name = n
			}
			if a := fnField(tc, "arguments"); truthy(a) {
				t.args = add(t.args, a)
			}
		}
		if fr := get(ch, "finish_reason"); truthy(fr) {
			s.finish = fr
		}
	})
	return
}

// Finish writes the last candidate: the calls, the finish reason and the usage.
func (s *GeminiStream) Finish() (out string, err error) {
	err = catch(func() {
		parts := []V{}
		for _, t := range s.calls {
			parts = append(parts, obj("functionCall", obj("name", t.name, "args", parseJSON(t.args, jsv.NewObject()))))
		}
		if len(parts) == 0 {
			parts = []V{obj("text", "")}
		}
		o := obj("candidates", jsv.Arr(obj("content", obj("role", "model", "parts", arr(parts)),
			"finishReason", or(lookup(ToGeminiFinish, s.finish), "STOP"), "index", 0.0)))
		if truthy(s.usage) {
			o.Set("usageMetadata", geminiUsage(s.usage))
		}
		out = geminiData(o.Set("modelVersion", s.model))
	})
	return
}

// Error is a Gemini error line.
func (s *GeminiStream) Error(message V) string {
	return geminiData(obj("error", obj("code", 502.0, "message", message, "status", "UNAVAILABLE")))
}

/* ---------- whole answers and SSE reading ---------- */

// WholeToChunks: a provider that answered a streaming request with one JSON body, as stream chunks.
func WholeToChunks(j V) (out []V, err error) {
	err = catch(func() {
		ch, m := firstChoice(j)
		delta := obj("role", "assistant")
		if c := get(m, "content"); truthy(c) {
			delta.Set("content", textOf(c))
		}
		if tcs := get(m, "tool_calls"); truthy(tcs) {
			items := list(tcs)
			calls := make([]V, len(items))
			for i, tc := range items {
				o := obj("index", float64(i))
				if src := jsv.Obj(tc); src != nil {
					o.Assign(src)
				}
				calls[i] = o
			}
			delta.Set("tool_calls", arr(calls))
		}
		out = []V{
			obj("choices", jsv.Arr(obj("index", 0.0, "delta", delta))),
			obj("choices", jsv.Arr(obj("index", 0.0, "delta", jsv.NewObject(), "finish_reason", or(get(ch, "finish_reason"), "stop")))),
		}
		if u := get(j, "usage"); truthy(u) {
			out = append(out, obj("choices", jsv.Arr(), "usage", u))
		}
	})
	return
}

// eventEnd finds the first /\r?\n\r?\n/ in b: its start and length, or -1.
func eventEnd(b []byte) (int, int) {
	for i := range b {
		for _, sep := range []string{"\r\n\r\n", "\r\n\n", "\n\r\n", "\n\n"} {
			if bytes.HasPrefix(b[i:], []byte(sep)) {
				return i, len(sep)
			}
		}
	}
	return -1, 0
}

// ReadSSE reads an SSE byte stream and calls onEvent(event, data) for each complete event (the JS readSse): `event:`
// is trimmed, one space after `data:` is dropped, data lines are joined by \n, an event without data is skipped and
// a partial event at the end is dropped. An error from onEvent stops the read and is returned.
func ReadSSE(r io.Reader, onEvent func(event, data string) error) error {
	var buf []byte
	chunk := make([]byte, 32*1024)
	for {
		n, err := r.Read(chunk)
		buf = append(buf, chunk[:n]...)
		for {
			i, l := eventEnd(buf)
			if i < 0 {
				break
			}
			raw := string(buf[:i])
			buf = buf[i+l:]
			event := "message"
			var data []string
			for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
				switch {
				case strings.HasPrefix(line, "event:"):
					event = trimJS(line[6:])
				case strings.HasPrefix(line, "data:"):
					data = append(data, strings.TrimPrefix(line[5:], " "))
				}
			}
			if len(data) > 0 {
				if cbErr := onEvent(event, strings.Join(data, "\n")); cbErr != nil {
					return cbErr
				}
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
