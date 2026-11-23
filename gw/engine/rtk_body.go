package engine

// The token saver's walks (lib/gateway/rtk.js saveTokens; spec D3, D7, D8). scanSaver is the cheap part, run while
// planning: it builds the hints, finds every body on which the JS throws (the engine hands those off: JS answers),
// and lists the texts the saver will look at, as pointers into the plan's pending body. run shortens them in place.

import (
	"fmt"
	"strings"
	"time"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/guard/jsre"
)

// SaverSwitch is the token saver's engine switch (spec D13): not a path class, read for every call the saver looks at.
const SaverSwitch = "token-saver"

const (
	saverThrows     = "token saver: a message it cannot read"
	saverArgs       = "token saver: a tool call the engine cannot read"
	saverOverBudget = "token saver: over its step budget"
	saverIDs        = "token saver: a tool call id the engine cannot match"
	saverPanicked   = "token saver: the engine's saver panicked"
)

// saverHook, when set (tests only), runs before each text the saver shortens: a test injects a panic through it.
var saverHook func()

// objectToolIDs: a tool call id or a tool message's tool_call_id is an object or an array. JS's Map finds such an id by
// identity, and a translator may put one object in both places (lib/gateway/translate.js geminiToOpenAI), which the
// engine's JSON copy of the translated body cannot see: the call is handed off (Tasks 8-9 review I1).
func objectToolIDs(body *Value) bool {
	msgs := body.Get("messages")
	if msgs == nil || msgs.Kind != Array {
		return false
	}
	isObj := func(v *Value) bool { return v != nil && (v.Kind == Object || v.Kind == Array) }
	for _, m := range msgs.A {
		if isObj(m.Get("tool_call_id")) {
			return true
		}
		if tcs := m.Get("tool_calls"); tcs != nil && tcs.Kind == Array {
			for _, tc := range tcs.A {
				if isObj(tc.Get("id")) {
					return true
				}
			}
		}
	}
	return false
}

// saverTarget is one text the saver looks at: holder[key] is a string; hint is its tool call's "<name> <arg>".
type saverTarget struct {
	holder *Value
	key    string
	hint   []uint16
}

type saverJob struct{ targets []saverTarget }

// candidate: compressText can change it (a string of 600 to 10 MiB units that does not start with [error]).
func candidate(v *Value) bool {
	if v == nil || v.Kind != String || strings.HasPrefix(v.S, "[error]") {
		return false
	}
	n := units(v.S)
	return n >= rtkMinChars && n <= rtkMaxChars
}

// firstTruthy is `a.k1 || a.k2 || …` on a JSON value (a primitive or an array has none of these properties); nil when
// every one is falsy.
func firstTruthy(a *Value, keys ...string) *Value {
	for _, k := range keys {
		if v := a.Get(k); v.Truthy() {
			return v
		}
	}
	return nil
}

// hintOf is `${name} ${val || ”}`; ok is false when either ToString throws.
func hintOf(name, val *Value) ([]uint16, bool) {
	n, ok := jsToString(name)
	if !ok {
		return nil, false
	}
	rest := []uint16{}
	if val != nil {
		if rest, ok = jsToString(val); !ok {
			return nil, false
		}
	}
	return cat(n, u16(" "), rest), true
}

// scanSaver walks the body as saveTokens does. A nil job means nothing can change (JS then returns null). why is a
// hand-off reason.
func scanSaver(body *Value, format string) (*saverJob, string) {
	msgs := body.Get("messages")
	if msgs == nil || msgs.Kind != Array {
		return nil, ""
	}
	j := &saverJob{}
	hints := map[string][]uint16{}
	lookup := func(id *Value) []uint16 {
		if k, ok := mapKey(id); ok {
			return hints[k]
		}
		return nil
	}
	remember := func(id *Value, h []uint16) {
		if k, ok := mapKey(id); ok {
			hints[k] = h
		}
	}
	// texts: content as a string, or the text of each {type: "text"} item; a null item throws (c.type).
	texts := func(holder *Value, hint []uint16) bool {
		c := holder.Get("content")
		switch {
		case c != nil && c.Kind == String:
			if candidate(c) {
				j.targets = append(j.targets, saverTarget{holder, "content", hint})
			}
		case c != nil && c.Kind == Array:
			for _, x := range c.A {
				if x == nil || x.Kind == Null {
					return false
				}
				if ty := x.Get("type"); ty != nil && ty.Kind == String && ty.S == "text" && candidate(x.Get("text")) {
					j.targets = append(j.targets, saverTarget{x, "text", hint})
				}
			}
		}
		return true
	}
	for _, m := range msgs.A {
		if m == nil || m.Kind == Null {
			return nil, saverThrows
		}
		if format == "anthropic" {
			c := m.Get("content")
			if c == nil || c.Kind != Array {
				continue
			}
			for _, bl := range c.A {
				if bl == nil || bl.Kind == Null {
					return nil, saverThrows
				}
				ty := bl.Get("type")
				isType := func(s string) bool { return ty != nil && ty.Kind == String && ty.S == s }
				if isType("tool_use") {
					var val *Value
					if in := bl.Get("input"); in.Truthy() {
						val = firstTruthy(in, "command", "pattern", "file_path", "path")
					}
					h, ok := hintOf(bl.Get("name"), val)
					if !ok {
						return nil, saverThrows
					}
					remember(bl.Get("id"), h)
				}
				if !isType("tool_result") || bl.Get("is_error").Truthy() {
					continue
				}
				if !texts(bl, lookup(bl.Get("tool_use_id"))) {
					return nil, saverThrows
				}
			}
			continue
		}
		// OpenAI: for (const tc of m.tool_calls || []). A truthy non-array throws: a non-empty string iterates
		// characters, which have no .function; anything else is not iterable.
		if tcs := m.Get("tool_calls"); tcs.Truthy() {
			if tcs.Kind != Array {
				return nil, saverThrows
			}
			for _, tc := range tcs.A {
				if tc == nil || tc.Kind != Object {
					return nil, saverThrows // tc.function.arguments on null, a primitive or an array
				}
				f := tc.Get("function")
				if f == nil || f.Kind == Null {
					return nil, saverThrows
				}
				a := NewObject() // let a = {}; a parse error leaves it
				if text, parse := argsText(f.Get("arguments")); parse {
					v, syntax, unsupported := jsonParse([]byte(text))
					switch {
					case unsupported:
						return nil, saverArgs
					case !syntax:
						a = v
					}
				}
				if a.Kind == Null {
					return nil, saverThrows // a.command outside the try
				}
				h, ok := hintOf(f.Get("name"), firstTruthy(a, "command", "cmd", "pattern", "file_path", "path", "input"))
				if !ok {
					return nil, saverThrows
				}
				remember(tc.Get("id"), h)
			}
		}
		if r := m.Get("role"); r == nil || r.Kind != String || r.S != "tool" {
			continue
		}
		if !texts(m, lookup(m.Get("tool_call_id"))) {
			return nil, saverThrows
		}
	}
	if len(j.targets) == 0 {
		return nil, ""
	}
	return j, ""
}

// argsText is the text JSON.parse(tc.function.arguments || '{}') reads. parse is false when String(arguments) throws
// inside the try (an object with an own toString key): a stays {}.
func argsText(args *Value) (string, bool) {
	switch {
	case !args.Truthy():
		return "{}", true
	case args.Kind == String:
		return args.S, true
	}
	s, ok := jsToString(args)
	if !ok {
		return "", false
	}
	return jsre.Str(s), true
}

// budget is spec D11's limit for this job.
func (j *saverJob) budget(now time.Time) *jsre.Budget {
	var n int64
	for _, t := range j.targets {
		n += int64(units(t.holder.Get(t.key).S))
	}
	return &jsre.Budget{Limit: 64*n + 1<<24, Deadline: now.Add(2 * time.Second)}
}

// run shortens every target in place. A hand-off reason leaves the body half done: the plan is dropped then.
func (j *saverJob) run(b *jsre.Budget) (hits []rtkHit, why string) {
	work := func() {
		for _, t := range j.targets {
			if saverHook != nil {
				saverHook()
			}
			text, hit, w := compressText(u16(t.holder.Get(t.key).S), t.hint, b)
			if w != "" {
				why = w
				return
			}
			if hit != nil {
				t.holder.Set(t.key, Str(jsre.Str(text)))
				hits = append(hits, *hit)
			}
		}
	}
	if b == nil {
		work()
		return hits, why
	}
	if err := b.Run(work); err != nil {
		return nil, saverOverBudget
	}
	return hits, why
}

// SaveResult is saveTokens's result: savedTokens is Math.round(savedChars / 4).
type SaveResult struct {
	SavedChars, SavedTokens int
	Hits                    []rtkHit
}

// SaveTokens is saveTokens(body, format) in place: nil when nothing was shortened.
func SaveTokens(body *Value, format string, b *jsre.Budget) (*SaveResult, string) {
	j, why := scanSaver(body, format)
	if why != "" || j == nil {
		return nil, why
	}
	hits, why := j.run(b)
	if why != "" || len(hits) == 0 {
		return nil, why
	}
	chars := 0
	for _, h := range hits {
		chars += h.Saved
	}
	return &SaveResult{SavedChars: chars, SavedTokens: (chars + 2) / 4, Hits: hits}, ""
}

// Save is saveTokens on the plan's pending body (spec D8-D12): it shortens the texts scanSaver found, puts savedTokens
// in the plan's base line, and builds the provider body unless a guardrail check will (Guard builds it from the same
// pending body). It runs once per plan. why is a hand-off reason: the control plane answers the request.
func (s *State) Save(p *Plan, now time.Time) (handoffWhy string) {
	if p.saver == nil || p.saverDone {
		return p.saverWhy
	}
	p.saverDone = true
	// A panic out of the filters is a bug of the port, never an answer JS gives: the call is handed to the control
	// plane (exact), and the caller logs it. The shadow's compare goroutine has no recover of its own (final review I1).
	defer func() {
		if v := recover(); v != nil {
			p.SaverPanic = fmt.Sprint(v)
			p.saverWhy, handoffWhy = saverPanicked, saverPanicked
		}
	}()
	hits, why := p.saver.run(p.saver.budget(now))
	if why != "" {
		p.saverWhy = why // the body is half done: the plan is not served
		return why
	}
	if len(hits) > 0 {
		chars := 0
		for _, h := range hits {
			chars += h.Saved
		}
		n := int64((chars + 2) / 4)
		p.Base.SavedTokens = &n
		p.saverHits = len(hits)
	}
	if p.Route != nil { // a route call: every leg sends the shortened client body (P4h spec Decision 9)
		body := p.pending
		p.pending = nil
		if len(hits) == 0 {
			return ""
		}
		legs, why := p.relegs(body)
		if why != "" {
			p.saverWhy = RouteHandoff + why
			return p.saverWhy
		}
		for _, l := range legs {
			if l != nil {
				l.Base.SavedTokens = p.Base.SavedTokens // only the answer's line keeps it (proxyChat's base)
			}
		}
		p.Legs = legs
		return ""
	}
	if p.Policy == nil && p.pending != nil {
		body := p.pending
		p.pending = nil
		if p.build != nil {
			p.Body = p.build(p, body)
		} else {
			p.Body = finishBody(p.Target, body)
		}
	}
	return ""
}
