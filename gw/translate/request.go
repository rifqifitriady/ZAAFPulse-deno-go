package translate

import (
	"strings"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"
)

// BadRequest is the JS `bad(message)`: a request the gateway cannot translate faithfully (400).
type BadRequest struct{ Message string }

func (e *BadRequest) Error() string { return e.Message }

// Status is the HTTP status, 400.
func (e *BadRequest) Status() int { return 400 }

// Type is the OpenAI error type, invalid_request_error.
func (e *BadRequest) Type() string { return "invalid_request_error" }

// Tagged is a translated request with the JS symbol-keyed fields: Soft lists fields the gateway added that a target may
// not accept (nil when the JS sets none), Dropped lists request features nothing upstream can serve.
type Tagged struct {
	Body    *jsv.Object
	Soft    []string
	Dropped []string
}

// Value is the fixture form { body, soft, dropped }.
func (t Tagged) Value() *jsv.Object {
	var soft V
	if t.Soft != nil {
		soft = strs(t.Soft)
	}
	return obj("body", t.Body, "soft", soft, "dropped", strs(t.Dropped))
}

// Stop-reason tables: finish_reason ↔ stop_reason ↔ Gemini finishReason.
var (
	ToAnthropicStop = map[string]string{
		"stop": "end_turn", "length": "max_tokens", "tool_calls": "tool_use", "function_call": "tool_use",
		"content_filter": "refusal",
	}
	FromAnthropicStop = map[string]string{
		"end_turn": "stop", "stop_sequence": "stop", "max_tokens": "length", "tool_use": "tool_calls",
		"refusal": "content_filter", "pause_turn": "stop",
	}
	ToGeminiFinish = map[string]string{"stop": "STOP", "length": "MAX_TOKENS", "tool_calls": "STOP", "content_filter": "SAFETY"}
)

// Anthropic thinking budget ↔ OpenAI reasoning effort.
var effortBudget = map[string]float64{"low": 2048, "medium": 8192, "high": 16384, "xhigh": 32000}

var efforts = map[string]bool{"none": true, "minimal": true, "low": true, "medium": true, "high": true, "xhigh": true}

func isEffort(v V) bool { s, ok := v.(string); return ok && efforts[s] }

func effortOfBudget(n float64) string {
	switch {
	case n < 4096:
		return "low"
	case n < 16384:
		return "medium"
	}
	return "high"
}

/* ======================= Anthropic Messages → OpenAI chat ======================= */

func imageURLOf(block V) V {
	s := or(get(block, "source"), jsv.NewObject())
	if eq(get(s, "type"), "base64") {
		return "data:" + str(get(s, "media_type")) + ";base64," + str(get(s, "data"))
	}
	return or(get(s, "url"), "")
}

// hasDigitsAfter is the JS /^<prefix>\d+/ test on String(v).
func hasDigitsAfter(v V, prefix string) bool {
	s := str(v)
	return strings.HasPrefix(s, prefix) && len(s) > len(prefix) && s[len(prefix)] >= '0' && s[len(prefix)] <= '9'
}

// Anthropic-defined client tools have a fixed schema; the client still runs them, so any model can call them.
type clientTool struct {
	prefix, description string
	parameters          func() *jsv.Object
}

var anthropicClientTools = []clientTool{
	{"bash_", "Run a shell command.", func() *jsv.Object {
		return obj("type", "object", "properties", obj("command", obj("type", "string"), "restart", obj("type", "boolean")))
	}},
	{"text_editor_", "View, create and edit files: commands view, create, str_replace and insert.", func() *jsv.Object {
		return obj("type", "object", "properties", obj(
			"command", obj("type", "string", "enum", jsv.Arr("view", "create", "str_replace", "insert", "undo_edit")),
			"path", obj("type", "string"),
			"file_text", obj("type", "string"),
			"old_str", obj("type", "string"),
			"new_str", obj("type", "string"),
			"insert_line", obj("type", "integer"),
			"insert_text", obj("type", "string"),
			"view_range", obj("type", "array", "items", obj("type", "integer")),
		), "required", jsv.Arr("command", "path"))
	}},
}

// AnthropicToOpenAI translates an Anthropic Messages request into an OpenAI chat request.
func (e *Env) AnthropicToOpenAI(b V) (out Tagged, err error) {
	err = catch(func() { out = anthropicToOpenAI(b) })
	return
}

func anthropicToOpenAI(b V) Tagged {
	messages := []V{}
	dropped := []string{}
	var system V = ""
	switch sys := get(b, "system"); {
	case isStr(sys):
		system = sys
	case jsv.Ary(sys) != nil:
		parts := []V{}
		for _, x := range jsv.Ary(sys).Items {
			parts = append(parts, or(get(x, "text"), ""))
		}
		system = join(parts, "\n\n")
	}
	if truthy(system) {
		messages = append(messages, obj("role", "system", "content", system))
	}
	for _, m := range listOr(get(b, "messages")) {
		var blocks []V
		if c := get(m, "content"); isStr(c) {
			blocks = []V{obj("type", "text", "text", c)}
		} else {
			blocks = listOr(c)
		}
		if eq(get(m, "role"), "assistant") {
			texts := []V{}
			calls := []V{}
			for _, x := range blocks {
				if eq(get(x, "type"), "text") {
					texts = append(texts, get(x, "text"))
				}
			}
			// Server tool calls ran at Anthropic and have no tool result in this history.
			for _, x := range blocks {
				if eq(get(x, "type"), "tool_use") {
					calls = append(calls, obj("id", get(x, "id"), "type", "function", "function",
						obj("name", get(x, "name"), "arguments", stringify(or(get(x, "input"), jsv.NewObject())))))
				}
			}
			msg := obj("role", "assistant", "content", textOrEmpty(join(texts, ""), len(calls) > 0))
			if len(calls) > 0 {
				msg.Set("tool_calls", arr(calls))
			}
			messages = append(messages, msg)
			continue
		}
		// A user turn: tool results first, then the rest. Images from a tool result go into the user message.
		images := []V{}
		parts := []V{}
		for _, x := range blocks {
			switch t := get(x, "type"); {
			case eq(t, "tool_result"):
				var items []V
				if c := get(x, "content"); isStr(c) {
					items = []V{obj("type", "text", "text", c)}
				} else {
					items = listOr(c)
				}
				outs := []V{}
				for _, c := range items {
					ct := get(c, "type")
					switch {
					case eq(ct, "text"):
						outs = append(outs, get(c, "text"))
					case !eq(ct, "image"):
						outs = append(outs, "")
					default:
						images = append(images, obj("type", "image_url", "image_url", obj("url", imageURLOf(c))))
						outs = append(outs, "[image: attached to the next user message]")
					}
				}
				out := join(outs, "\n")
				var content V = out
				if truthy(get(x, "is_error")) {
					content = "[error] " + out
				}
				messages = append(messages, obj("role", "tool", "tool_call_id", get(x, "tool_use_id"), "content", content))
			case eq(t, "text"):
				parts = append(parts, obj("type", "text", "text", get(x, "text")))
			case eq(t, "image"):
				parts = append(parts, obj("type", "image_url", "image_url", obj("url", imageURLOf(x))))
			case eq(t, "document") && truthy(get(x, "source")) && eq(get(get(x, "source"), "type"), "text"):
				parts = append(parts, obj("type", "text", "text", or(get(get(x, "source"), "data"), "")))
			}
		}
		parts = append(images, parts...)
		if len(parts) > 0 {
			messages = append(messages, obj("role", "user", "content", textOrParts(parts)))
		}
	}
	tools := []V{}
	toolNames := []V{}
	for _, t := range listOr(get(b, "tools")) {
		tt := get(t, "type")
		var fixed *clientTool
		if truthy(tt) {
			for i := range anthropicClientTools {
				if hasDigitsAfter(tt, anthropicClientTools[i].prefix) {
					fixed = &anthropicClientTools[i]
					break
				}
			}
		}
		switch {
		case truthy(get(t, "input_schema")) || eq(tt, "custom") || !truthy(tt):
			tools = append(tools, obj("type", "function", "function", obj("name", get(t, "name"),
				"description", or(get(t, "description"), ""),
				"parameters", or(get(t, "input_schema"), obj("type", "object", "properties", jsv.NewObject())))))
			toolNames = append(toolNames, get(t, "name"))
		case fixed != nil:
			tools = append(tools, obj("type", "function", "function", obj("name", get(t, "name"),
				"description", fixed.description, "parameters", fixed.parameters())))
			toolNames = append(toolNames, get(t, "name"))
		default:
			// Server tools and computer use run inside Anthropic's API.
			dropped = append(dropped, "tool "+str(or(get(t, "name"), tt)))
		}
	}
	tc := get(b, "tool_choice")
	if truthy(tc) && eq(get(tc, "type"), "tool") {
		for _, t := range listOr(get(b, "tools")) {
			if !eq(get(t, "name"), get(tc, "name")) {
				continue
			}
			served := false
			for _, n := range toolNames {
				served = served || eq(n, get(t, "name"))
			}
			if !served {
				throwBad("tool_choice names \"" + str(get(tc, "name")) + "\", a tool only Anthropic's own API can run")
			}
		}
	}
	var toolChoice V = jsv.Undefined
	if truthy(tc) {
		switch tct := get(tc, "type"); {
		case eq(tct, "any"):
			toolChoice = "required"
		case eq(tct, "tool"):
			toolChoice = obj("type", "function", "function", obj("name", get(tc, "name")))
		case eq(tct, "none"):
			toolChoice = "none"
		default:
			toolChoice = "auto"
		}
	}
	// Extended thinking and effort → reasoning_effort, sent only to models known to take it (SOFT).
	oc := or(get(b, "output_config"), jsv.NewObject())
	th := or(get(b, "thinking"), jsv.NewObject())
	var effort V
	switch {
	case eq(get(th, "type"), "enabled"):
		n := jsv.ToNumber(get(th, "budget_tokens"))
		if !truthy(n) {
			n = 0
		}
		effort = effortOfBudget(n)
	case eq(get(th, "type"), "adaptive"):
		effort = "medium"
	}
	if oe := get(oc, "effort"); truthy(oe) {
		if eq(oe, "max") || eq(oe, "xhigh") {
			effort = "high"
		} else {
			effort = oe
		}
	}
	if truthy(effort) && !isEffort(effort) {
		effort = nil
	}
	var format V
	if f := get(oc, "format"); truthy(f) && eq(get(f, "type"), "json_schema") {
		format = obj("type", "json_schema", "json_schema",
			obj("name", "output", "schema", or(get(f, "schema"), jsv.NewObject()), "strict", true))
	}
	body := obj("model", get(b, "model"), "messages", arr(messages))
	if mt := get(b, "max_tokens"); truthy(mt) {
		body.Set("max_tokens", mt)
	}
	if t := get(b, "temperature"); !jsv.IsNullish(t) {
		body.Set("temperature", t)
	}
	if t := get(b, "top_p"); !jsv.IsNullish(t) {
		body.Set("top_p", t)
	}
	if ss := get(b, "stop_sequences"); truthy(ss) && truthy(get(ss, "length")) {
		body.Set("stop", ss)
	}
	if len(tools) > 0 {
		body.Set("tools", arr(tools))
		if truthy(toolChoice) {
			body.Set("tool_choice", toolChoice)
		}
		if truthy(tc) && truthy(get(tc, "disable_parallel_tool_use")) {
			body.Set("parallel_tool_calls", false)
		}
	}
	var soft []string
	if truthy(effort) {
		body.Set("reasoning_effort", effort)
		soft = []string{"reasoning_effort"}
	}
	if truthy(format) {
		body.Set("response_format", format)
	}
	body.Set("stream", truthy(get(b, "stream")))
	return Tagged{Body: body, Soft: soft, Dropped: dropped}
}

// textOrEmpty is `text || (calls ? null : ”)`.
func textOrEmpty(text string, calls bool) V {
	switch {
	case text != "":
		return text
	case calls:
		return nil
	}
	return ""
}

// textOrParts: parts that are all text become one string joined by \n, otherwise the parts.
func textOrParts(parts []V) V {
	texts := make([]V, len(parts))
	for i, p := range parts {
		if !eq(get(p, "type"), "text") {
			return arr(parts)
		}
		texts[i] = get(p, "text")
	}
	return join(texts, "\n")
}

// OpenAIToAnthropic turns an OpenAI chat answer into an Anthropic message.
func (e *Env) OpenAIToAnthropic(j, model V) (out *jsv.Object, err error) {
	err = catch(func() {
		choice, msg := firstChoice(j)
		content := []V{}
		if c := get(msg, "content"); truthy(c) {
			content = append(content, obj("type", "text", "text", textOf(c)))
		}
		calls := listOr(get(msg, "tool_calls"))
		for _, tc := range calls {
			id := get(tc, "id")
			if !truthy(id) {
				id = e.rid("toolu_")
			}
			fn := get(tc, "function")
			content = append(content, obj("type", "tool_use", "id", id, "name", get(fn, "name"),
				"input", parseJSON(get(fn, "arguments"), jsv.NewObject())))
		}
		u := usageParts(get(j, "usage"))
		stop := lookup(ToAnthropicStop, get(choice, "finish_reason"))
		if !truthy(stop) {
			stop = "end_turn"
			if len(calls) > 0 {
				stop = "tool_use"
			}
		}
		out = obj("id", e.rid("msg_"), "type", "message", "role", "assistant", "model", model,
			"content", arr(content), "stop_reason", stop, "stop_sequence", nil,
			"usage", obj("input_tokens", jsMax(0.0, sub(u.prompt, u.cached)), "output_tokens", u.output,
				"cache_read_input_tokens", u.cached, "cache_creation_input_tokens", 0.0))
	})
	return
}

// firstChoice is `(j.choices && j.choices[0]) || {}` and its `.message || {}`.
func firstChoice(j V) (choice, msg V) {
	choice = get(j, "choices")
	if truthy(choice) {
		choice = get(choice, "0")
	}
	choice = or(choice, jsv.NewObject())
	return choice, or(get(choice, "message"), jsv.NewObject())
}

/* ======================= OpenAI chat → Anthropic Messages (native providers) ======================= */

func imageBlockOf(url V) *jsv.Object {
	s := str(or(url, ""))
	// /^data:([^;]+);base64,(.*)$/: `.` stops at line terminators, so the data must hold none.
	if rest, ok := strings.CutPrefix(s, "data:"); ok {
		if i := strings.Index(rest, ";base64,"); i > 0 && !strings.Contains(rest[:i], ";") {
			data := rest[i+len(";base64,"):]
			if !strings.ContainsAny(data, "\n\r\u2028\u2029") {
				return obj("type", "image", "source", obj("type", "base64", "media_type", rest[:i], "data", data))
			}
		}
	}
	return obj("type", "image", "source", obj("type", "url", "url", url))
}

// blocksOf: OpenAI content (a string or parts) → Anthropic text and image blocks.
func blocksOf(content V) []V {
	if s, ok := content.(string); ok {
		if s == "" {
			return []V{}
		}
		return []V{obj("type", "text", "text", s)}
	}
	out := []V{}
	if a := jsv.Ary(content); a != nil {
		for _, p := range a.Items {
			switch t := get(p, "type"); {
			case eq(t, "text") && truthy(get(p, "text")):
				out = append(out, obj("type", "text", "text", get(p, "text")))
			case eq(t, "image_url"):
				iu := get(p, "image_url")
				var url V = iu
				if truthy(iu) {
					url = get(iu, "url")
				}
				out = append(out, imageBlockOf(or(url, "")))
			}
		}
	}
	return out
}

// OpenAIToAnthropicRequest translates an OpenAI chat request for a provider that speaks Anthropic natively.
func (e *Env) OpenAIToAnthropicRequest(b V) (out *jsv.Object, err error) {
	err = catch(func() { out = openAIToAnthropicRequest(b) })
	return
}

func openAIToAnthropicRequest(b V) *jsv.Object {
	system := []V{}
	messages := []*jsv.Object{}
	push := func(role string, blocks []V) {
		if n := len(messages); n > 0 && eq(messages[n-1].Get("role"), role) {
			jsv.Ary(messages[n-1].Get("content")).Push(blocks...)
			return
		}
		messages = append(messages, obj("role", role, "content", arr(append([]V{}, blocks...))))
	}
	for _, m := range listOr(get(b, "messages")) {
		role := get(m, "role")
		if eq(role, "system") || eq(role, "developer") {
			system = append(system, textOf(get(m, "content")))
			continue
		}
		if eq(role, "tool") {
			// Anthropic tool results take images as well as text.
			blocks := blocksOf(get(m, "content"))
			var content V = textOf(get(m, "content"))
			for _, x := range blocks {
				if eq(get(x, "type"), "image") {
					content = arr(blocks)
					break
				}
			}
			push("user", []V{obj("type", "tool_result", "tool_use_id", get(m, "tool_call_id"), "content", content)})
			continue
		}
		blocks := blocksOf(get(m, "content"))
		if eq(role, "assistant") {
			for _, tc := range listOr(get(m, "tool_calls")) {
				fn := get(tc, "function")
				blocks = append(blocks, obj("type", "tool_use", "id", get(tc, "id"), "name", get(fn, "name"),
					"input", parseJSON(get(fn, "arguments"), jsv.NewObject())))
			}
		}
		if len(blocks) > 0 {
			r := "user"
			if eq(role, "assistant") {
				r = "assistant"
			}
			push(r, blocks)
		}
	}
	tools := []V{}
	for _, t := range listOr(get(b, "tools")) {
		if !eq(get(t, "type"), "function") {
			continue
		}
		fn := get(t, "function")
		tools = append(tools, obj("name", get(fn, "name"), "description", or(get(fn, "description"), ""),
			"input_schema", or(get(fn, "parameters"), obj("type", "object", "properties", jsv.NewObject()))))
	}
	tc := get(b, "tool_choice")
	var toolChoice *jsv.Object
	switch {
	case eq(tc, "required"):
		toolChoice = obj("type", "any")
	case eq(tc, "none"):
		toolChoice = obj("type", "none")
	case truthy(tc) && truthy(get(tc, "function")):
		toolChoice = obj("type", "tool", "name", get(get(tc, "function"), "name"))
	case eq(tc, "auto"):
		toolChoice = obj("type", "auto")
	}
	if len(tools) > 0 && eq(get(b, "parallel_tool_calls"), false) && (toolChoice == nil || !eq(toolChoice.Get("type"), "none")) {
		if toolChoice == nil {
			toolChoice = obj("type", "auto")
		} else {
			toolChoice = toolChoice.Clone()
		}
		toolChoice.Set("disable_parallel_tool_use", true)
	}
	// Structured output: json_schema maps to output_config.format. Anthropic has no schema-less JSON mode.
	rf := get(b, "response_format")
	var outputFormat *jsv.Object
	if truthy(rf) && eq(get(rf, "type"), "json_object") {
		throwBad("response_format json_object is not supported by Anthropic providers; send a json_schema")
	}
	if truthy(rf) && eq(get(rf, "type"), "json_schema") {
		js := get(rf, "json_schema")
		var schema V = js
		if truthy(js) {
			schema = get(js, "schema")
		}
		if !truthy(schema) || jsv.TypeOf(schema) != "object" {
			throwBad("response_format json_schema needs json_schema.schema")
		}
		outputFormat = obj("type", "json_schema", "schema", schema)
	}
	requested := or(get(b, "max_completion_tokens"), get(b, "max_tokens"))
	maxTokens := or(requested, 8192.0)
	// reasoning_effort → an extended-thinking budget, left off with a forced tool or a tool loop in progress.
	inToolLoop := false
	if n := len(messages); n > 1 && eq(messages[n-1].Get("role"), "user") {
		for _, x := range jsv.Ary(messages[n-1].Get("content")).Items {
			if eq(get(x, "type"), "tool_result") {
				inToolLoop = true
				break
			}
		}
	}
	var thinking *jsv.Object
	budget, hasBudget := effortBudget[key(get(b, "reasoning_effort"))]
	if hasBudget && !inToolLoop && (toolChoice == nil || eq(toolChoice.Get("type"), "auto") || eq(toolChoice.Get("type"), "none")) {
		if !truthy(requested) {
			maxTokens = jsMax(maxTokens, budget+8192)
		}
		room := jsMin(budget, sub(maxTokens, 1024.0))
		if room >= 1024 {
			thinking = obj("type", "enabled", "budget_tokens", room)
		}
	}
	// OpenAI temperature runs 0-2, Anthropic's 0-1; thinking takes neither temperature nor top_p.
	var temperature V
	if t := get(b, "temperature"); !jsv.IsNullish(t) {
		n := jsv.ToNumber(t)
		if !truthy(n) {
			n = 0
		}
		temperature = jsMin(1.0, jsMax(0.0, n))
	}
	out := obj("model", get(b, "model"), "max_tokens", maxTokens, "messages", arrObjs(messages))
	if len(system) > 0 {
		out.Set("system", join(system, "\n\n"))
	}
	if thinking == nil && temperature != nil {
		out.Set("temperature", temperature)
	}
	if thinking == nil && temperature == nil && !jsv.IsNullish(get(b, "top_p")) {
		out.Set("top_p", get(b, "top_p"))
	}
	if stop := get(b, "stop"); truthy(stop) {
		if a := jsv.Ary(stop); a != nil {
			out.Set("stop_sequences", arr(append([]V{}, a.Items...)))
		} else {
			out.Set("stop_sequences", jsv.Arr(stop))
		}
	}
	if len(tools) > 0 {
		out.Set("tools", arr(tools))
		if toolChoice != nil {
			out.Set("tool_choice", toolChoice)
		}
	}
	if thinking != nil {
		out.Set("thinking", thinking)
	}
	if outputFormat != nil {
		out.Set("output_config", obj("format", outputFormat))
	}
	if truthy(get(b, "stream")) {
		out.Set("stream", true)
	}
	return out
}

func arrObjs(os []*jsv.Object) *jsv.Array {
	out := make([]V, len(os))
	for i, o := range os {
		out[i] = o
	}
	return arr(out)
}

// AnthropicUsageToOpenAI turns Anthropic usage into OpenAI usage. u undefined is {}.
func AnthropicUsageToOpenAI(u V) (out *jsv.Object, err error) {
	err = catch(func() { out = anthropicUsageToOpenAI(u) })
	return
}

func anthropicUsageToOpenAI(u V) *jsv.Object {
	if u == jsv.Undefined {
		u = jsv.NewObject()
	}
	cached := or(get(u, "cache_read_input_tokens"), 0.0)
	prompt := add(add(or(get(u, "input_tokens"), 0.0), cached), or(get(u, "cache_creation_input_tokens"), 0.0))
	return obj("prompt_tokens", prompt, "completion_tokens", or(get(u, "output_tokens"), 0.0),
		"total_tokens", add(prompt, or(get(u, "output_tokens"), 0.0)),
		"prompt_tokens_details", obj("cached_tokens", cached))
}

// AnthropicResponseToOpenAI turns an Anthropic message into an OpenAI chat completion.
func (e *Env) AnthropicResponseToOpenAI(j, model V) (out *jsv.Object, err error) {
	err = catch(func() {
		content := listOr(get(j, "content"))
		texts, thinking, calls := []V{}, []V{}, []V{}
		for _, c := range content {
			if eq(get(c, "type"), "text") {
				texts = append(texts, get(c, "text"))
			}
		}
		for _, c := range content {
			if eq(get(c, "type"), "tool_use") {
				calls = append(calls, obj("id", get(c, "id"), "type", "function", "function",
					obj("name", get(c, "name"), "arguments", stringify(or(get(c, "input"), jsv.NewObject())))))
			}
		}
		for _, c := range content {
			if eq(get(c, "type"), "thinking") {
				thinking = append(thinking, or(get(c, "thinking"), ""))
			}
		}
		text, think := join(texts, ""), join(thinking, "")
		id := get(j, "id")
		if !truthy(id) {
			id = e.rid("chatcmpl-")
		}
		var c V = text
		if text == "" {
			c = nil
		}
		msg := obj("role", "assistant", "content", c)
		if think != "" {
			msg.Set("reasoning_content", think)
		}
		if len(calls) > 0 {
			msg.Set("tool_calls", arr(calls))
		}
		out = obj("id", id, "object", "chat.completion", "created", e.seconds(), "model", model,
			"choices", jsv.Arr(obj("index", 0.0, "message", msg,
				"finish_reason", or(lookup(FromAnthropicStop, get(j, "stop_reason")), "stop"))),
			"usage", anthropicUsageToOpenAI(get(j, "usage")))
	})
	return
}

/* ======================= OpenAI Responses → OpenAI chat ======================= */

// customSet is the JS Set of custom tool names, in insertion order.
type customSet struct {
	names []string
	has   map[string]bool
}

func newCustomSet(names []string) *customSet {
	s := &customSet{names: []string{}, has: map[string]bool{}}
	for _, n := range names {
		s.add(n)
	}
	return s
}

func (s *customSet) add(v V) {
	n := str(v)
	if !s.has[n] {
		s.has[n] = true
		s.names = append(s.names, n)
	}
}

func (s *customSet) contains(v V) bool { return s.has[str(v)] }

// ResponsesToOpenAI translates an OpenAI Responses request into a chat request; custom lists the custom (freeform)
// tool names, whose calls turn back into custom calls in the answer.
func (e *Env) ResponsesToOpenAI(b V) (out Tagged, custom []string, err error) {
	err = catch(func() { out, custom = responsesToOpenAI(b) })
	return
}

func responsesToOpenAI(b V) (Tagged, []string) {
	// Conversation state lives with OpenAI; a chat provider sees only what this request carries.
	if truthy(get(b, "previous_response_id")) {
		throwBad("previous_response_id needs a provider with the native Responses API; send the full input instead")
	}
	messages := []*jsv.Object{}
	custom := newCustomSet(nil)
	dropped := []string{}
	if in := get(b, "instructions"); truthy(in) {
		messages = append(messages, obj("role", "system", "content", in))
	}
	var items []V
	if in := get(b, "input"); isStr(in) {
		items = []V{obj("role", "user", "content", in)}
	} else {
		items = listOr(in)
	}
	var pending *jsv.Object // an assistant message that collects text and consecutive tool calls
	flush := func() {
		if pending != nil {
			messages = append(messages, pending)
			pending = nil
		}
	}
	assistant := func() *jsv.Object {
		if pending == nil {
			pending = obj("role", "assistant", "content", nil, "tool_calls", jsv.Arr())
		}
		return pending
	}
	for _, it := range items {
		typ := or(get(it, "type"), "message")
		switch {
		case eq(typ, "message"):
			var parts []V
			if c := get(it, "content"); isStr(c) {
				parts = []V{obj("type", "input_text", "text", c)}
			} else {
				parts = listOr(c)
			}
			texts := []V{}
			images := []V{}
			for _, p := range parts {
				if strings.HasSuffix(str(or(get(p, "type"), "text")), "text") {
					texts = append(texts, or(get(p, "text"), ""))
				}
			}
			for _, p := range parts {
				if eq(get(p, "type"), "input_image") {
					images = append(images, p)
				}
			}
			text := join(texts, "")
			if eq(get(it, "role"), "assistant") {
				a := assistant()
				a.Set("content", add(or(a.Get("content"), ""), text))
				continue
			}
			flush()
			role := "user"
			if r := get(it, "role"); eq(r, "developer") || eq(r, "system") {
				role = "system"
			}
			var content V = text
			if len(images) > 0 && role == "user" {
				cs := []V{}
				if text != "" {
					cs = append(cs, obj("type", "text", "text", text))
				}
				for _, p := range images {
					cs = append(cs, obj("type", "image_url", "image_url", obj("url", or(get(p, "image_url"), ""))))
				}
				content = arr(cs)
			}
			messages = append(messages, obj("role", role, "content", content))
		case eq(typ, "function_call") || eq(typ, "custom_tool_call"):
			isCustom := eq(typ, "custom_tool_call")
			if isCustom {
				custom.add(get(it, "name"))
			}
			var args V
			if isCustom {
				args = stringify(obj("input", or(get(it, "input"), "")))
			} else {
				args = or(get(it, "arguments"), "{}")
			}
			jsv.Ary(assistant().Get("tool_calls")).Push(obj("id", get(it, "call_id"), "type", "function",
				"function", obj("name", get(it, "name"), "arguments", args)))
		case eq(typ, "function_call_output") || eq(typ, "custom_tool_call_output"):
			flush()
			var content V
			switch o := get(it, "output"); {
			case isStr(o):
				content = o
			case jsv.Ary(o) != nil:
				ts := []V{}
				for _, x := range jsv.Ary(o).Items {
					ts = append(ts, or(get(x, "text"), ""))
				}
				content = join(ts, "\n")
			default:
				content = stringify(or(o, ""))
			}
			messages = append(messages, obj("role", "tool", "tool_call_id", get(it, "call_id"), "content", content))
		case eq(typ, "item_reference"):
			throwBad("item_reference needs a provider with the native Responses API; send the item itself")
		}
		// reasoning and other item types carry nothing a chat model can use
	}
	flush()
	for _, m := range messages {
		if tcs := m.Get("tool_calls"); truthy(tcs) && !truthy(get(tcs, "length")) {
			m.Delete("tool_calls")
		}
	}
	// Custom (freeform) tools become functions with one string argument.
	tools := []V{}
	for _, t := range listOr(get(b, "tools")) {
		switch tt := get(t, "type"); {
		case eq(tt, "function"):
			tools = append(tools, obj("type", "function", "function", obj("name", get(t, "name"),
				"description", or(get(t, "description"), ""),
				"parameters", or(get(t, "parameters"), obj("type", "object", "properties", jsv.NewObject())))))
		case eq(tt, "custom"):
			custom.add(get(t, "name"))
			desc := trimJS(str(or(get(t, "description"), "")) + ` Put the whole raw input in the "input" string.`)
			tools = append(tools, obj("type", "function", "function", obj("name", get(t, "name"),
				"description", desc,
				"parameters", obj("type", "object", "properties", obj("input", obj("type", "string")),
					"required", jsv.Arr("input")))))
		default:
			// Built-in tools (web_search, file_search, code_interpreter, …) run inside OpenAI's API.
			dropped = append(dropped, "tool "+str(tt))
		}
	}
	tc := get(b, "tool_choice")
	if truthy(tc) && jsv.TypeOf(tc) == "object" && !truthy(get(tc, "name")) {
		if t := get(tc, "type"); !eq(t, "auto") && !eq(t, "none") && !eq(t, "required") {
			throwBad("tool_choice " + str(t) + " needs a provider with the native Responses API")
		}
	}
	var effort V
	if r := get(b, "reasoning"); truthy(r) && isEffort(get(r, "effort")) {
		effort = get(r, "effort")
	}
	var f V
	if t := get(b, "text"); truthy(t) {
		f = get(t, "format")
	}
	var format V
	switch {
	case truthy(f) && eq(get(f, "type"), "json_schema"):
		js := obj("name", or(get(f, "name"), "output"), "schema", or(get(f, "schema"), jsv.NewObject()))
		if s := get(f, "strict"); !jsv.IsNullish(s) {
			js.Set("strict", s)
		}
		if d := get(f, "description"); truthy(d) {
			js.Set("description", d)
		}
		format = obj("type", "json_schema", "json_schema", js)
	case truthy(f) && eq(get(f, "type"), "json_object"):
		format = obj("type", "json_object")
	}
	body := obj("model", get(b, "model"), "messages", arrObjs(messages))
	if m := get(b, "max_output_tokens"); truthy(m) {
		body.Set("max_tokens", m)
	}
	if effort != nil {
		body.Set("reasoning_effort", effort)
	}
	if format != nil {
		body.Set("response_format", format)
	}
	if t := get(b, "temperature"); !jsv.IsNullish(t) {
		body.Set("temperature", t)
	}
	if t := get(b, "top_p"); !jsv.IsNullish(t) {
		body.Set("top_p", t)
	}
	if len(tools) > 0 {
		body.Set("tools", arr(tools))
		switch {
		case truthy(tc) && isStr(tc):
			body.Set("tool_choice", tc)
		case truthy(tc) && truthy(get(tc, "name")):
			body.Set("tool_choice", obj("type", "function", "function", obj("name", get(tc, "name"))))
		}
		if p := get(b, "parallel_tool_calls"); !jsv.IsNullish(p) {
			body.Set("parallel_tool_calls", p)
		}
	}
	body.Set("stream", truthy(get(b, "stream")))
	var soft []string
	if effort != nil {
		soft = []string{"reasoning_effort"}
	}
	return Tagged{Body: body, Soft: soft, Dropped: dropped}, custom.names
}

func responsesUsage(u V) *jsv.Object {
	p := usageParts(u)
	return obj("input_tokens", p.prompt, "input_tokens_details", obj("cached_tokens", p.cached),
		"output_tokens", p.output, "output_tokens_details", obj("reasoning_tokens", p.reasoning),
		"total_tokens", add(p.prompt, p.output))
}

// toolItem is the JS toolItem: a chat tool call as a Responses output item.
func (e *Env) toolItem(tc V, custom *customSet, status string) *jsv.Object {
	fn := get(tc, "function")
	name := get(fn, "name")
	if custom.contains(name) {
		id := e.rid("ctc_")
		input := jsv.Nullish(jsv.Nullish(get(parseJSON(get(fn, "arguments"), jsv.NewObject()), "input"), get(fn, "arguments")), "")
		return obj("type", "custom_tool_call", "id", id, "call_id", get(tc, "id"), "name", name,
			"input", str(input), "status", status)
	}
	id := e.rid("fc_")
	callID := get(tc, "id")
	if !truthy(callID) {
		callID = e.rid("call_")
	}
	return obj("type", "function_call", "id", id, "call_id", callID, "name", name,
		"arguments", or(get(fn, "arguments"), "{}"), "status", status)
}

// OpenAIToResponses turns an OpenAI chat answer into a Responses answer.
func (e *Env) OpenAIToResponses(j, model V, custom []string) (out *jsv.Object, err error) {
	err = catch(func() {
		cs := newCustomSet(custom)
		choice, msg := firstChoice(j)
		output := []V{}
		if c := get(msg, "content"); truthy(c) {
			output = append(output, obj("type", "message", "id", e.rid("msg_"), "status", "completed",
				"role", "assistant", "content", jsv.Arr(obj("type", "output_text", "text", textOf(c), "annotations", jsv.Arr()))))
		}
		for _, tc := range listOr(get(msg, "tool_calls")) {
			output = append(output, e.toolItem(tc, cs, "completed"))
		}
		incomplete := eq(get(choice, "finish_reason"), "length")
		status := "completed"
		if incomplete {
			status = "incomplete"
		}
		out = obj("id", e.rid("resp_"), "object", "response", "created_at", e.seconds(), "status", status)
		if incomplete {
			out.Set("incomplete_details", obj("reason", "max_output_tokens"))
		}
		out.Set("model", model).Set("output", arr(output)).Set("output_text", textOf(get(msg, "content"))).
			Set("usage", responsesUsage(get(j, "usage")))
	})
	return
}

/* ======================= Gemini generateContent → OpenAI chat ======================= */

func lowerTypes(s V) V {
	switch x := s.(type) {
	case *jsv.Array:
		out := make([]V, len(x.Items))
		for i, it := range x.Items {
			out[i] = lowerTypes(it)
		}
		return arr(out)
	case *jsv.Object:
		o := jsv.NewObject()
		for _, k := range x.Keys() {
			v := x.Get(k)
			if sv, ok := v.(string); ok && k == "type" {
				o.Set(k, jsv.ToLower(sv))
			} else {
				o.Set(k, lowerTypes(v))
			}
		}
		return o
	}
	return s
}

// GeminiToOpenAI translates a Gemini generateContent request into an OpenAI chat request.
func (e *Env) GeminiToOpenAI(b, model, stream V) (out *jsv.Object, err error) {
	err = catch(func() { out = geminiToOpenAI(b, model, stream) })
	return
}

func geminiToOpenAI(b, model, stream V) *jsv.Object {
	messages := []V{}
	pending := map[string]*[]V{}
	pkey := func(v V) string { return jsv.TypeOf(v) + ":" + str(v) }
	n := 0.0
	if sys := or(get(b, "systemInstruction"), get(b, "system_instruction")); truthy(sys) {
		ts := []V{}
		for _, p := range listOr(get(sys, "parts")) {
			ts = append(ts, or(get(p, "text"), ""))
		}
		messages = append(messages, obj("role", "system", "content", join(ts, "\n")))
	}
	for _, c := range listOr(get(b, "contents")) {
		parts := listOr(get(c, "parts"))
		if eq(get(c, "role"), "model") {
			texts, calls := []V{}, []V{}
			for _, p := range parts {
				if truthy(get(p, "text")) && !truthy(get(p, "thought")) {
					texts = append(texts, get(p, "text"))
				}
			}
			for _, p := range parts {
				fc := get(p, "functionCall")
				if !truthy(fc) {
					continue
				}
				id := get(fc, "id")
				if !truthy(id) {
					id = "call_" + jsv.NumberString(n) + "_" + str(get(fc, "name"))
					n++
				}
				k := pkey(get(fc, "name"))
				if pending[k] == nil {
					pending[k] = &[]V{}
				}
				*pending[k] = append(*pending[k], id)
				calls = append(calls, obj("id", id, "type", "function", "function", obj("name", get(fc, "name"),
					"arguments", stringify(or(get(fc, "args"), jsv.NewObject())))))
			}
			msg := obj("role", "assistant", "content", textOrEmpty(join(texts, ""), len(calls) > 0))
			if len(calls) > 0 {
				msg.Set("tool_calls", arr(calls))
			}
			messages = append(messages, msg)
			continue
		}
		user := []V{}
		for _, p := range parts {
			switch {
			case truthy(get(p, "functionResponse")):
				fr := get(p, "functionResponse")
				id := get(fr, "id")
				if !truthy(id) {
					if q := pending[pkey(get(fr, "name"))]; q != nil && len(*q) > 0 {
						id = (*q)[0]
						*q = (*q)[1:]
					}
				}
				if !truthy(id) {
					id = "call_" + str(get(fr, "name"))
				}
				messages = append(messages, obj("role", "tool", "tool_call_id", id,
					"content", stringify(jsv.Nullish(get(fr, "response"), jsv.NewObject()))))
			case truthy(get(p, "text")):
				user = append(user, obj("type", "text", "text", get(p, "text")))
			case truthy(get(p, "inlineData")) || truthy(get(p, "inline_data")):
				d := or(get(p, "inlineData"), get(p, "inline_data"))
				user = append(user, obj("type", "image_url", "image_url", obj("url",
					"data:"+str(or(get(d, "mimeType"), get(d, "mime_type")))+";base64,"+str(get(d, "data")))))
			}
		}
		if len(user) > 0 {
			messages = append(messages, obj("role", "user", "content", textOrParts(user)))
		}
	}
	decls := []V{}
	for _, t := range listOr(get(b, "tools")) {
		d := or(or(get(t, "functionDeclarations"), get(t, "function_declarations")), jsv.Arr())
		if a := jsv.Ary(d); a != nil {
			decls = append(decls, a.Items...)
		} else {
			decls = append(decls, d)
		}
	}
	tools := []V{}
	for _, f := range decls {
		params := or(or(get(f, "parameters"), get(f, "parametersJsonSchema")), obj("type", "object", "properties", jsv.NewObject()))
		tools = append(tools, obj("type", "function", "function", obj("name", get(f, "name"),
			"description", or(get(f, "description"), ""), "parameters", lowerTypes(params))))
	}
	g := or(or(get(b, "generationConfig"), get(b, "generation_config")), jsv.NewObject())
	out := obj("model", model, "messages", arr(messages))
	if m := get(g, "maxOutputTokens"); truthy(m) {
		out.Set("max_tokens", m)
	}
	if t := get(g, "temperature"); !jsv.IsNullish(t) {
		out.Set("temperature", t)
	}
	if t := get(g, "topP"); !jsv.IsNullish(t) {
		out.Set("top_p", t)
	}
	if s := get(g, "stopSequences"); truthy(s) && truthy(get(s, "length")) {
		out.Set("stop", s)
	}
	if len(tools) > 0 {
		out.Set("tools", arr(tools))
	}
	out.Set("stream", truthy(stream))
	return out
}

func geminiUsage(u V) *jsv.Object {
	p := usageParts(u)
	o := obj("promptTokenCount", p.prompt, "candidatesTokenCount", p.output, "totalTokenCount", add(p.prompt, p.output))
	if truthy(p.cached) {
		o.Set("cachedContentTokenCount", p.cached)
	}
	return o
}

// OpenAIToGemini turns an OpenAI chat answer into a Gemini answer.
func (e *Env) OpenAIToGemini(j, model V) (out *jsv.Object, err error) {
	err = catch(func() {
		choice, msg := firstChoice(j)
		parts := []V{}
		if c := get(msg, "content"); truthy(c) {
			parts = append(parts, obj("text", textOf(c)))
		}
		for _, tc := range listOr(get(msg, "tool_calls")) {
			fn := get(tc, "function")
			parts = append(parts, obj("functionCall", obj("name", get(fn, "name"),
				"args", parseJSON(get(fn, "arguments"), jsv.NewObject()))))
		}
		if len(parts) == 0 {
			parts = []V{obj("text", "")}
		}
		out = obj("candidates", jsv.Arr(obj("content", obj("role", "model", "parts", arr(parts)),
			"finishReason", or(lookup(ToGeminiFinish, get(choice, "finish_reason")), "STOP"), "index", 0.0)),
			"usageMetadata", geminiUsage(get(j, "usage")), "modelVersion", model)
	})
	return
}

// EstimateTokens is the rough token count (about 4 characters per token) for count_tokens endpoints.
func EstimateTokens(body V) int {
	s, _ := jsv.Stringify(or(body, jsv.NewObject()))
	n := (jsv.UTF16Len(s) + 3) / 4
	if n < 1 {
		return 1
	}
	return n
}

// Package-level forms on Default.

// AnthropicToOpenAI on the Default env.
func AnthropicToOpenAI(b V) (Tagged, error) { return Default.AnthropicToOpenAI(b) }

// OpenAIToAnthropic on the Default env.
func OpenAIToAnthropic(j, model V) (*jsv.Object, error) { return Default.OpenAIToAnthropic(j, model) }

// OpenAIToAnthropicRequest on the Default env.
func OpenAIToAnthropicRequest(b V) (*jsv.Object, error) { return Default.OpenAIToAnthropicRequest(b) }

// AnthropicResponseToOpenAI on the Default env.
func AnthropicResponseToOpenAI(j, model V) (*jsv.Object, error) {
	return Default.AnthropicResponseToOpenAI(j, model)
}

// ResponsesToOpenAI on the Default env.
func ResponsesToOpenAI(b V) (Tagged, []string, error) { return Default.ResponsesToOpenAI(b) }

// OpenAIToResponses on the Default env.
func OpenAIToResponses(j, model V, custom []string) (*jsv.Object, error) {
	return Default.OpenAIToResponses(j, model, custom)
}

// GeminiToOpenAI on the Default env.
func GeminiToOpenAI(b, model, stream V) (*jsv.Object, error) {
	return Default.GeminiToOpenAI(b, model, stream)
}

// OpenAIToGemini on the Default env.
func OpenAIToGemini(j, model V) (*jsv.Object, error) { return Default.OpenAIToGemini(j, model) }
