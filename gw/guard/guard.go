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
	"sync"
	"time"
	"unicode/utf16"

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
// in Go only, the call ran past its step budget or its deadline (Overrun names the check that was running; Hits is
// empty). Either way nothing is sent.
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

// The step budget of one call (CheckBody, CheckText), shared by every string and every check in it (I4), is the
// primary bound (owner's decision, 2026-11-13): it does not depend on the machine or its load. A base, plus a share
// per code unit each time a check runs on a string, capped per call. Steps are weighted to cost about the same time
// whatever the input: a matcher instruction, a character a repeat consumes or an alternation choice is one step, a
// start attempt four (jsre.attemptSteps), a start position the filters skip half of one (jsre.skipWeight), a visited
// string stringSteps. One step then costs 3–7 ns on an M-series Mac and about 2–2.5 times that on the Ubuntu runner,
// so the cap of 250M steps bounds a call to about 2 s of CPU there (idle). Legitimate 20 MB bodies measured need 78M
// (the corpus), 112M (code) and 121M steps (numeric CSV, the heaviest): at least 2x below the cap. A coarse
// wall-clock backstop (requestDeadline) runs with it. Past either the call fails closed in every mode, log included
// (the JS has no limit: Go-only, spec deviation 12).
var (
	budgetBase    int64 = 20_000_000
	budgetPerUnit int64 = 600
	budgetCap     int64 = 250_000_000
)

// stringSteps is what visiting one string costs in steps beyond its matching (walking, conversions).
const stringSteps = 16

// traceSteps, when set (tests), receives the steps each call used.
var traceSteps func(int64)

// requestDeadline is the wall-clock backstop of one call: 10 s, far above what the step budget allows on an idle
// machine, so load on the host (other requests, garbage collection) never blocks legitimate text.
var requestDeadline = 10 * time.Second

// now is the clock of the deadline (replaceable in tests).
var now = time.Now

// NewBudget is the budget of one call that checks units code units once: the step limit for them and the deadline.
func NewBudget(units int) *jsre.Budget {
	b := newCallBudget()
	b.Extend(budgetPerUnit*int64(units), budgetCap)
	return b
}

// orBudget is b, or a budget for one check on n code units when b is nil (after Prepare, so compiling is never
// counted against b).
func orBudget(b *jsre.Budget, n int) *jsre.Budget {
	Prepare()
	if b == nil {
		return NewBudget(n)
	}
	return b
}

// allPatterns lists every regex of the package, for Prepare.
func allPatterns() []*jsre.Regexp {
	out := []*jsre.Regexp{reBase64URL, rePEMEnd, reInjCarries, reInjAnchor, reInjAnchorMark, reDocContext, reCodeLine,
		reStringItem, reQuoteBefore, reZeroWidth, reSplitLetters, reHasZeroWidth, reHasSplit, reHasLeet, reSecretHint,
		rePlaceholder, rePlaceholderPassword, reEmail, reIPV4, reIPV6, rePhone, reMasked, reLeetWord, reDigit, reLeetChar,
		reAWSExample, reMasked3, reHasDigit, reDateHead}
	for _, c := range injection {
		out = append(out, c.res...)
	}
	for _, p := range secretPatterns {
		out = append(out, p.re)
	}
	return out
}

var prepared sync.Once

// Prepare compiles every pattern's program and filters (about half a second, once). The checks call it before their
// first deadline starts, so compiling never counts against a request; the gateway may call it at startup.
func Prepare() {
	prepared.Do(func() {
		for _, re := range allPatterns() {
			re.Prepare()
		}
		setupInjMulti()
		setupSecretMulti()
	})
}

// newCallBudget is the budget a call starts with: the base steps and the deadline from now.
func newCallBudget() *jsre.Budget {
	Prepare()
	return &jsre.Budget{Limit: min(budgetBase, budgetCap), Deadline: now().Add(requestDeadline), Now: now}
}

// errCheck is a check that failed (a detector panicked): in redact or block mode the call fails closed.
type errCheck struct{ err error }

// errOverrun is a check past its step budget.
type errOverrun struct{ check string }

type run struct {
	b        *jsre.Budget // the call's budget and deadline
	policy   Policy
	hits     []*Hit
	index    map[string]*Hit
	redacted bool
	failed   bool
	failures []Failure
}

// Failure is a check whose detector failed in log mode during one call (N1): the call passed (as in the JS) and the
// wiring should log or count it. Count is how many strings it failed on, Err the first failure.
type Failure struct {
	Check string
	Count int
	Err   string
}

func (r *run) fail(check string, p any) {
	r.failed = true
	for i := range r.failures {
		if r.failures[i].Check == check {
			r.failures[i].Count++
			return
		}
	}
	r.failures = append(r.failures, Failure{Check: check, Count: 1, Err: fmt.Sprint(p)})
}

func newRun(p Policy) *run { return &run{b: newCallBudget(), policy: p, index: map[string]*Hit{}} }

// budget grants the call's budget the allowance of one check on n code units and returns it.
func (r *run) budget(n int) *jsre.Budget {
	r.b.Extend(budgetPerUnit*int64(n), budgetCap)
	return r.b
}

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
			if jsre.IsLimit(p) {
				panic(errOverrun{check})
			}
			if r.policy.mode(check) != "log" {
				panic(errCheck{fmt.Errorf("%v", p)})
			}
			r.fail(check, p)
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
	b := r.budget(len(text))
	spans := detectSecrets(text, b)
	if len(spans) == 0 {
		return text
	}
	redact := r.policy.Secrets == "redact"
	var out []uint16
	if redact {
		out = make([]uint16, 0, len(text)+64)
	}
	at := 0
	for _, sp := range spans {
		if sp.Start < at {
			continue // inside a private key block already replaced
		}
		r.add("secrets", sp.Label, 1)
		if redact {
			out = append(out, text[at:sp.Start]...)
			out = append(out, jsre.U16("‹secret: "+sp.Label+"›")...)
		}
		if sp.Label == "Private key" {
			at = privateKeyEnd(text, sp.End, b)
		} else {
			at = sp.End
		}
	}
	if !redact {
		return text
	}
	r.redacted = true
	return append(out, text[at:]...)
}

// piiIn masks personal data in text. In redact mode the passes may write into spare and over text itself (the caller
// no longer needs either); a replacement always changes the text (no placeholder can equal what it replaces), so
// "something was masked" is "a count went up".
func (r *run) piiIn(text, spare []uint16) []uint16 {
	redact := r.policy.PII == "redact"
	if !redact {
		spare = nil
	}
	masked, counts := detectPII(text, redact, spare, r.budget(len(text)))
	for _, kind := range piiKinds {
		if counts[kind] > 0 {
			r.add("pii", kind, counts[kind])
		}
	}
	if !redact {
		return text
	}
	if len(counts) > 0 {
		r.redacted = true
	}
	return masked
}

// text runs the checks on s, which belongs to the call (a check may overwrite it), and returns the new text and
// whether a check replaced something. Injection, which only reads, runs first so that s is free for the masking passes;
// its hits are still added after the others, in the JS order (secrets, pii, injection).
func (r *run) text(s []uint16, fresh bool) ([]uint16, bool) {
	var injected []string
	if fresh && r.policy.Injection != "off" {
		r.guarded("injection", func() []uint16 {
			if res := detectInjection(s, r.budget(len(s))); res != nil {
				injected = res.Labels
			}
			return nil
		}, nil)
	}
	// "Changed" is whether a check redacted something in this text (r.redacted, set by the check that does it), never
	// whether the result sits in other storage: the masking passes may write the result over s itself (C1).
	before := r.redacted
	r.redacted = false
	t := s
	if r.policy.Secrets != "off" {
		t = r.guarded("secrets", func() []uint16 { return r.secretsIn(t) }, t)
	}
	secretsCopied := r.redacted // secretsIn redacted into a new buffer, so s is free for the masking passes
	if r.policy.PII != "off" {
		var spare []uint16
		if secretsCopied {
			spare = s
		}
		t = r.guarded("pii", func() []uint16 { return r.piiIn(t, spare) }, t)
	}
	for _, l := range injected {
		r.add("injection", l, 1)
	}
	changed := r.redacted
	r.redacted = before || changed
	return t, changed
}

// charge counts stringSteps for a visited string, so a body of strings that run no regex still uses its budget and
// reads the clock (M4); past a limit the call fails closed like a check.
func (r *run) charge() {
	if err := r.b.Run(func() { r.b.Charge(stringSteps) }); err != nil {
		panic(errOverrun{"limit"})
	}
}

// visitStr runs the checks on a Go string and returns the (possibly new) string: the original when nothing was
// redacted or the result has the same content (compared by content, not by storage).
func (r *run) visitStr(s string, fresh bool) string {
	out, changed := r.text(jsre.U16(s), fresh)
	if !changed || equalU16String(out, s) {
		return s
	}
	return jsre.Str(out)
}

// equalU16String reports whether u equals jsre.U16(s), without converting s.
func equalU16String(u []uint16, s string) bool {
	i := 0
	for _, c := range s {
		if c >= 0x10000 {
			hi, lo := utf16.EncodeRune(c)
			if i+1 >= len(u) || u[i] != uint16(hi) || u[i+1] != uint16(lo) {
				return false
			}
			i += 2
			continue
		}
		if i >= len(u) || u[i] != uint16(c) {
			return false
		}
		i++
	}
	return i == len(u)
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

// isBase64URL is BASE64_URL.test(s) under the call's budget; past it the call fails closed like a check. The pattern
// begins with ^data: under the i flag (which never maps a non-ASCII character to ASCII), so any other string is
// answered without converting it.
func (r *run) isBase64URL(s string) bool {
	if len(s) < 5 || !strings.EqualFold(s[:5], "data:") {
		return false
	}
	u := jsre.U16(s)
	b := r.budget(len(u))
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
		r.charge() // every string leaf, skipped ones too (structural keys, data: URLs)
		if (structural && isStructuralKey(key, hasKey)) || r.isBase64URL(x) {
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
// with the input; Action is "redacted", "logged" or "" (JS null); Error is true when a check in log mode failed, and
// Failures says which (Go only, N1).
type Result struct {
	Body     jsv.Value
	Action   string
	Hits     []Hit
	Error    bool
	Failures []Failure
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
			if errors.Is(e, jsre.ErrBudget) || errors.Is(e, jsre.ErrStack) || errors.Is(e, jsre.ErrDeadline) {
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
	if traceSteps != nil {
		defer func() { traceSteps(r.b.Steps) }()
	}
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
	return Result{Body: out, Action: action, Hits: hits, Error: r.failed, Failures: r.failures}, nil
}

// TextResult of CheckText.
type TextResult struct {
	Text     string
	Action   string
	Hits     []Hit
	Error    bool
	Failures []Failure
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
	if traceSteps != nil {
		defer func() { traceSteps(r.b.Steps) }()
	}
	r.charge()
	out := r.visitStr(jsv.ToString(jsv.Nullish(text, "")), true)
	action, hits, ferr := r.finish()
	if ferr != nil {
		return TextResult{}, ferr
	}
	return TextResult{Text: out, Action: action, Hits: hits, Error: r.failed, Failures: r.failures}, nil
}

func hitsValue(hits []Hit) *jsv.Array {
	a := jsv.Arr()
	for _, h := range hits {
		a.Push(jsv.NewObject().Set("check", h.Check).Set("label", h.Label).Set("count", float64(h.Count)))
	}
	return a
}
