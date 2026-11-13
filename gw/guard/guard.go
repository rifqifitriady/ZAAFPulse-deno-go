// Package guard is the Go port of lib/gateway/guardrails.js (P4e): content checks on what the gateway sends to a
// provider or an integration. Three checks: secrets and pii scan the whole body, injection only the new input. Pure: a
// body and a policy in, a new body, the action and the hits out; no I/O, no clock. A hit is { check, label, count },
// never the matched text. Redaction is deterministic. The fixtures in gw/testdata/guard
// (scripts/fixtures/gen-guard.js) prove it equal to the JS.
package guard

import (
	"errors"
	"fmt"
	"strings"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/guard/jsre"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"
)

// Checks, in the order block messages name them.
var Checks = []string{"secrets", "pii", "injection"}

// Modes allowed per check.
var Modes = map[string][]string{
	"secrets":   {"off", "log", "redact", "block"},
	"pii":       {"off", "log", "redact", "block"},
	"injection": {"off", "log", "block"},
}

// Scopes allowed.
var Scopes = []string{"external", "all"}

// Defaults is the policy when config.json sets none.
var Defaults = Policy{Scope: "external", Secrets: "redact", PII: "off", Injection: "log"}

// Formats of a checked body.
var Formats = []string{"openai-chat", "anthropic", "responses", "embeddings", "arguments"}

var word = map[string]string{"secrets": "secret", "pii": "personal data", "injection": "prompt injection"}

var piiKinds = []string{"email", "phone", "ip"}

var (
	structural = set("model", "role", "type", "id", "name", "tool_choice", "stop")
	binary     = set("b64_json", "image_url", "inline_data", "signature", "encrypted_content")
	opaque     = set("thinking", "redacted_thinking", "reasoning")
	modelItems = set("function_call", "custom_tool_call", "reasoning", "web_search_call", "file_search_call",
		"computer_call", "code_interpreter_call", "image_generation_call", "local_shell_call", "mcp_call")
	reBase64URL = jsre.MustCompile(`^data:[\w.+-]+\/[\w.+-]+(?:;[\w=.+-]+)*;base64,[A-Za-z0-9+/=\s]*$`, "i")
	rePEMEnd    = jsre.MustCompile(`-----END [A-Z ]*PRIVATE KEY-----`, "")
)

func set(xs ...string) map[string]bool {
	m := map[string]bool{}
	for _, x := range xs {
		m[x] = true
	}
	return m
}

func contains(list []string, v jsv.Value) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Policy is a guardrail policy. An empty mode behaves like a mode the JS policy object leaves out.
type Policy struct{ Scope, Secrets, PII, Injection string }

func (p Policy) mode(check string) string {
	switch check {
	case "secrets":
		return p.Secrets
	case "pii":
		return p.PII
	}
	return p.Injection
}

func (p *Policy) set(check, mode string) {
	switch check {
	case "secrets":
		p.Secrets = mode
	case "pii":
		p.PII = mode
	default:
		p.Injection = mode
	}
}

// Value is the policy as the JS object.
func (p Policy) Value() *jsv.Object {
	return jsv.NewObject().Set("scope", p.Scope).Set("secrets", p.Secrets).Set("pii", p.PII).Set("injection", p.Injection)
}

// PolicyOf reads a JS policy object (a missing or non-string field is empty).
func PolicyOf(v jsv.Value) Policy {
	o := jsv.Obj(v)
	if o == nil {
		return Policy{}
	}
	s := func(k string) string { x, _ := o.Get(k).(string); return x }
	return Policy{Scope: s("scope"), Secrets: s("secrets"), PII: s("pii"), Injection: s("injection")}
}

// CleanPolicy is the global policy from config.json gateway.guardrails: a missing or disallowed field takes its default.
func CleanPolicy(cfg jsv.Value) Policy {
	c := jsv.Obj(cfg)
	if c == nil {
		c = jsv.NewObject()
	}
	out := Policy{Scope: Defaults.Scope}
	if contains(Scopes, c.Get("scope")) {
		out.Scope = c.Get("scope").(string)
	}
	for _, k := range Checks {
		out.set(k, Defaults.mode(k))
		if v := c.Get(k); contains(Modes[k], v) {
			out.set(k, v.(string))
		}
	}
	return out
}

// ResolvePolicy is the policy for one call: a mode the key sets wins, one it leaves out comes from the global setting.
func ResolvePolicy(global, key jsv.Value) Policy {
	out := CleanPolicy(global)
	own := jsv.NewObject()
	if jsv.Truthy(key) {
		if g := jsv.Obj(jsv.Get(key, "guardrails")); g != nil {
			own = g
		}
	}
	for _, k := range Checks {
		if v := own.Get(k); contains(Modes[k], v) {
			out.set(k, v.(string))
		}
	}
	return out
}

// Hit is one finding: the check, a label and how often; never the matched text.
type Hit struct {
	Check, Label string
	Count        int
}

// BlockMessage names the checks and labels that blocked, never a value.
func BlockMessage(hits []Hit) string {
	var parts []string
	for _, c := range Checks {
		var labels []string
		for _, h := range hits {
			if h.Check == c {
				labels = append(labels, h.Label)
			}
		}
		if len(labels) > 0 {
			parts = append(parts, word[c]+" ("+strings.Join(labels, ", ")+")")
		}
	}
	return "Blocked by ZAAFPulse guardrails: " + strings.Join(parts, "; ") + ". Remove it, or ask the owner to change the policy."
}

// Block is the GuardrailBlock error: a check in block mode found something (Hits are only those checks' hits), or,
// in Go only, a check ran past its step budget (Overrun names the check; Hits is empty). Either way nothing is sent.
type Block struct {
	Hits    []Hit
	Overrun string
}

func (b *Block) Error() string {
	if b.Overrun != "" {
		name, ok := word[b.Overrun]
		switch {
		case b.Overrun == dataURLCheck:
			name = "data URL"
		case !ok:
			name = "content"
		}
		return "Blocked by ZAAFPulse guardrails: the " + name + " check did not finish within its limit. " +
			"Send a shorter input, or ask the owner to change the policy."
	}
	return BlockMessage(b.Hits)
}

// Detectors, replaceable in tests to prove the fail-closed path.
var (
	detectSecrets   = secretSpans
	detectPII       = maskPII
	detectInjection = scanInjection
)

// The step budget of one check on one string: a base plus a share per code unit, capped. A check past it fails the
// call closed in every mode, log included (the JS has no budget: Go-only, spec deviation 12). Sized from the corpus
// and the adversarial inputs of the review: about 20x the steps the heaviest legitimate text measured needs.
var (
	budgetBase    int64 = 20_000_000
	budgetPerUnit int64 = 600
	budgetCap     int64 = 3_000_000_000
)

func budgetFor(n int) *jsre.Budget {
	return &jsre.Budget{Limit: min(budgetBase+budgetPerUnit*int64(n), budgetCap)}
}

// errCheck is a check that failed (a detector panicked): in redact or block mode the call fails closed.
type errCheck struct{ err error }

// errOverrun is a check past its step budget.
type errOverrun struct{ check string }

type run struct {
	policy   Policy
	hits     []*Hit
	index    map[string]*Hit
	redacted bool
	failed   bool
}

func newRun(p Policy) *run { return &run{policy: p, index: map[string]*Hit{}} }

func (r *run) add(check, label string, n int) {
	k := check + "|" + label
	if h := r.index[k]; h != nil {
		h.Count += n
		return
	}
	h := &Hit{Check: check, Label: label, Count: n}
	r.index[k] = h
	r.hits = append(r.hits, h)
}

// guarded runs a check; a panic is marked in log mode and fails the call otherwise.
func (r *run) guarded(check string, f func() []uint16, fallback []uint16) (out []uint16) {
	defer func() {
		if p := recover(); p != nil {
			if p == jsre.ErrBudget || p == jsre.ErrStack {
				panic(errOverrun{check})
			}
			if r.policy.mode(check) != "log" {
				panic(errCheck{fmt.Errorf("%v", p)})
			}
			r.failed = true
			out = fallback
		}
	}()
	return f()
}

func privateKeyEnd(text []uint16, from int, b *jsre.Budget) int {
	if m := rePEMEnd.ExecB(text[from:], 0, b); m != nil {
		return from + m[1]
	}
	return len(text)
}

func (r *run) secretsIn(text []uint16) []uint16 {
	b := budgetFor(len(text))
	spans := detectSecrets(text, b)
	if len(spans) == 0 {
		return text
	}
	var out []uint16
	at := 0
	for _, sp := range spans {
		if sp.Start < at {
			continue // inside a private key block already replaced
		}
		r.add("secrets", sp.Label, 1)
		out = append(out, text[at:sp.Start]...)
		out = append(out, jsre.U16("‹secret: "+sp.Label+"›")...)
		if sp.Label == "Private key" {
			at = privateKeyEnd(text, sp.End, b)
		} else {
			at = sp.End
		}
	}
	if r.policy.Secrets != "redact" {
		return text
	}
	r.redacted = true
	return append(out, text[at:]...)
}

func (r *run) piiIn(text []uint16) []uint16 {
	masked, counts := detectPII(text, budgetFor(len(text)))
	for _, kind := range piiKinds {
		if counts[kind] > 0 {
			r.add("pii", kind, counts[kind])
		}
	}
	if r.policy.PII != "redact" || equalU16(masked, text) {
		return text
	}
	r.redacted = true
	return masked
}

func (r *run) text(s []uint16, fresh bool) []uint16 {
	t := s
	if r.policy.Secrets != "off" {
		t = r.guarded("secrets", func() []uint16 { return r.secretsIn(t) }, t)
	}
	if r.policy.PII != "off" {
		t = r.guarded("pii", func() []uint16 { return r.piiIn(t) }, t)
	}
	if fresh && r.policy.Injection != "off" {
		r.guarded("injection", func() []uint16 {
			if res := detectInjection(s, budgetFor(len(s))); res != nil {
				for _, l := range res.Labels {
					r.add("injection", l, 1)
				}
			}
			return nil
		}, nil)
	}
	return t
}

// visitStr runs the checks on a Go string and returns the (possibly new) string.
func (r *run) visitStr(s string, fresh bool) string {
	u := jsre.U16(s)
	out := r.text(u, fresh)
	if equalU16(out, u) {
		return s
	}
	return jsre.Str(out)
}

func (r *run) finish() (action string, hits []Hit, err error) {
	var blocking []Hit
	for _, h := range r.hits {
		hits = append(hits, *h)
		if r.policy.mode(h.Check) == "block" {
			blocking = append(blocking, *h)
		}
	}
	if len(blocking) > 0 {
		return "", nil, &Block{Hits: blocking}
	}
	switch {
	case r.redacted:
		action = "redacted"
	case len(hits) > 0 || r.failed:
		action = "logged"
	}
	if hits == nil {
		hits = []Hit{}
	}
	return action, hits, nil
}

// isBase64URL is BASE64_URL.test(s) under its own step budget; past it the call fails closed like a check.
func isBase64URL(s string) bool {
	u := jsre.U16(s)
	b := budgetFor(len(u))
	var ok bool
	if err := b.Run(func() { ok = reBase64URL.TestB(u, b) }); err != nil {
		panic(errOverrun{dataURLCheck})
	}
	return ok
}

// dataURLCheck names the walker's data: URL test in an overrun Block.
const dataURLCheck = "data-url"

func isStructuralKey(key string, hasKey bool) bool {
	return hasKey && (structural[key] || strings.HasSuffix(key, "_id"))
}

// walk visits every string leaf, copy-on-write: an object or array holding a changed string is copied. With
// structural false (MCP arguments) no key is skipped. It returns the value and whether it changed.
func (r *run) walk(v jsv.Value, key string, hasKey bool, fresh, structural bool) (jsv.Value, bool) {
	switch x := v.(type) {
	case string:
		if (structural && isStructuralKey(key, hasKey)) || isBase64URL(x) {
			return x, false
		}
		out := r.visitStr(x, fresh)
		return out, out != x
	case *jsv.Array:
		var out *jsv.Array
		for i, it := range x.Items {
			nv, changed := r.walk(it, key, hasKey, fresh, structural) // a list of strings keeps its key (stop: ['\n'])
			if changed {
				if out == nil {
					out = &jsv.Array{Items: append([]jsv.Value(nil), x.Items...)}
				}
				out.Items[i] = nv
			}
		}
		if out == nil {
			return x, false
		}
		return out, true
	case *jsv.Object:
		if t, ok := x.Get("type").(string); structural && ok && opaque[t] {
			return x, false
		}
		var out *jsv.Object
		for _, k := range x.Keys() {
			if structural && (binary[k] || (k == "data" && x.Get("type") == "base64")) {
				continue
			}
			nv, changed := r.walk(x.Get(k), k, true, fresh, structural)
			if changed {
				if out == nil {
					out = x.Clone()
				}
				out.Set(k, nv)
			}
		}
		if out == nil {
			return x, false
		}
		return out, true
	}
	return v, false
}

func lastAssistant(list []jsv.Value, format string) int {
	isModel := func(m jsv.Value) bool {
		o := jsv.Obj(m)
		if o == nil {
			return false
		}
		if o.Get("role") == "assistant" {
			return true
		}
		t, ok := o.Get("type").(string)
		return format == "responses" && ok && modelItems[t]
	}
	for i := len(list) - 1; i >= 0; i-- {
		if isModel(list[i]) && i < len(list)-1 {
			return i
		}
	}
	return -1
}

// Result of CheckBody. Body is a new value (the input is never changed) that shares every unchanged nested value
// with the input; Action is "redacted", "logged" or "" (JS null); Error is true when a check in log mode failed.
type Result struct {
	Body   jsv.Value
	Action string
	Hits   []Hit
	Error  bool
}

// catchCheck turns a failed check into the returned error.
func catchCheck(err *error) {
	if p := recover(); p != nil {
		switch e := p.(type) {
		case errCheck:
			*err = e.err
		case errOverrun:
			*err = &Block{Overrun: e.check}
		case error:
			// A limit reached outside a check (no path is known to do so): still fail closed, never panic out.
			if errors.Is(e, jsre.ErrBudget) || errors.Is(e, jsre.ErrStack) {
				*err = &Block{Overrun: "limit"}
				return
			}
			panic(p)
		default:
			panic(p)
		}
	}
}

// CheckBody checks the body that will be sent upstream. format is its shape: openai-chat, anthropic, responses,
// embeddings or arguments (MCP tool arguments: secrets and pii only, no key is structural). It returns a *Block
// when a check in block mode found something, and a check's own failure in redact or block mode (fail closed).
func CheckBody(body jsv.Value, p Policy, format string) (res Result, err error) {
	if !contains(Formats, format) {
		return Result{}, errors.New(`unknown body format "` + format + `"`)
	}
	defer catchCheck(&err)
	if format == "arguments" {
		p.Injection = "off"
	}
	r := newRun(p)
	structural := format != "arguments"
	o := jsv.Obj(body)
	var out jsv.Value
	if o == nil {
		out, _ = r.walk(body, "", false, true, structural)
	} else {
		listKey := ""
		switch format {
		case "responses":
			listKey = "input"
		case "openai-chat", "anthropic":
			listKey = "messages"
		}
		var list *jsv.Array
		if listKey != "" {
			list = jsv.Ary(o.Get(listKey))
		}
		last := -1
		if list != nil {
			last = lastAssistant(list.Items, format)
		}
		copyObj := o.Clone()
		for _, k := range o.Keys() {
			if structural && binary[k] {
				continue
			}
			if list != nil && k == listKey {
				var items *jsv.Array
				for i, it := range list.Items {
					nv, changed := r.walk(it, "", false, i > last, structural)
					if changed {
						if items == nil {
							items = &jsv.Array{Items: append([]jsv.Value(nil), list.Items...)}
						}
						items.Items[i] = nv
					}
				}
				if items != nil {
					copyObj.Set(k, items)
				}
				continue
			}
			nv, _ := r.walk(o.Get(k), k, true, last < 0, structural)
			copyObj.Set(k, nv)
		}
		out = copyObj
	}
	action, hits, ferr := r.finish()
	if ferr != nil {
		return Result{}, ferr
	}
	return Result{Body: out, Action: action, Hits: hits, Error: r.failed}, nil
}

// TextResult of CheckText.
type TextResult struct {
	Text   string
	Action string
	Hits   []Hit
	Error  bool
}

// CheckText checks one text: part "arguments" (secrets and pii), "result" (an MCP tool result: injection only) or
// "input" (all three; also any other value, as in the JS). text is String(text ?? ”).
func CheckText(text jsv.Value, p Policy, part string) (res TextResult, err error) {
	defer catchCheck(&err)
	switch part {
	case "result":
		p.Secrets, p.PII = "off", "off"
	case "arguments":
		p.Injection = "off"
	}
	r := newRun(p)
	out := r.visitStr(jsv.ToString(jsv.Nullish(text, "")), true)
	action, hits, ferr := r.finish()
	if ferr != nil {
		return TextResult{}, ferr
	}
	return TextResult{Text: out, Action: action, Hits: hits, Error: r.failed}, nil
}

func hitsValue(hits []Hit) *jsv.Array {
	a := jsv.Arr()
	for _, h := range hits {
		a.Push(jsv.NewObject().Set("check", h.Check).Set("label", h.Label).Set("count", float64(h.Count)))
	}
	return a
}
