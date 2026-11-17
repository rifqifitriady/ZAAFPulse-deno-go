package engine

// The go engine's side of the shadow compare (P4b): while a class runs on js, the front marks a request, the control
// plane serves it and keeps a capture (lib/gateway/shadow.js), and the engine turns the same provider answer into its
// own answer and usage line and compares. Only the control plane calls the provider. The line it posts back
// (lib/front/shadow-log.js) holds no prompt or answer text: a text that differs is named by its length, a hash prefix
// and the first byte that differs.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/front"
)

// Capture is what the control plane recorded of one request it served.
type Capture struct {
	ProviderRequests []struct {
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
		Body    *string           `json:"body"`
	} `json:"providerRequests"`
	ProviderResponse *ProviderAnswer `json:"providerResponse"`
	Client           *struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
	} `json:"client"`
	Usage map[string]any `json:"usage"`
}

// ProviderAnswer is the provider's answer in a capture.
type ProviderAnswer struct {
	Status    int    `json:"status"`
	Body      string `json:"body"`
	Ended     string `json:"ended"`     // a stream (P4c): "eof" or "cut"
	Truncated bool   `json:"truncated"` // a stream: the capture passed its cap
}

// Diff is one field that differs.
type Diff struct {
	Field string `json:"field"`
	JS    any    `json:"js"`
	Go    any    `json:"go"`
}

// ShadowLine is the line of one compared request (lib/front/shadow-log.js).
type ShadowLine struct {
	Class   string `json:"class"`
	Outcome string `json:"outcome"` // same, diff, handoff, skipped
	Reason  string `json:"reason,omitzero"`
	Status  int    `json:"status,omitzero"` // the control plane's answer
	Diffs   []Diff `json:"diffs,omitzero"`
}

// budgetLag is the reason a budget disagreement is not a diff: the control plane decides on its live spend, the engine
// on its snapshot, up to a second behind (P4e Decision 18).
const budgetLag = "budget: the go engine decided on its snapshot"

// budgetRefusal is a budget's 402 (`budget "<name>" is used up for this <period>`), the only refusal the snapshot's lag
// explains; the price rule's 402 and every other answer are compared (review m2).
func budgetRefusal(status int, body string) bool {
	return status == http.StatusPaymentRequired && strings.Contains(body, `"message":"budget \"`)
}

// usageFields are the usage-line fields compared; ts, ms, ttft, traceId and ipc differ by nature, cost is compared
// in nano-USD, error as a text.
var usageFields = []string{"keyId", "provider", "model", "requested", "routedTo", "kind", "format", "stream", "status",
	"input", "output", "cached", "client", "local", "savedTokens", "guard"}

// Compare is the line for request plan p (built before the control plane answered) and its capture c (nil: the control
// plane made no provider call for it).
func (s *State) Compare(class string, p *Plan, c *Capture) ShadowLine {
	l := ShadowLine{Class: class}
	if c != nil && c.Client != nil {
		l.Status = c.Client.Status
	}
	if p.Handoff != "" {
		l.Outcome, l.Reason = "handoff", p.Handoff
		return l
	}
	d := func(field string, js, gov any) { l.Diffs = append(l.Diffs, Diff{field, js, gov}) }
	dt := func(field, js, gov string) { a, b := digest(js, gov); d(field, a, b) }
	switch {
	case c != nil && c.ProviderResponse == nil && len(c.ProviderRequests) > 0:
		l.Outcome, l.Reason = "skipped", "the provider call failed: no answer to compare"
		return l
	case c != nil && c.ProviderResponse == nil && c.Client != nil && c.Client.Status == http.StatusTooManyRequests:
		l.Outcome, l.Reason = "skipped", "the control plane's rate limit answered (each engine counts its own)"
		return l
	case c != nil && c.ProviderResponse == nil && c.Client != nil && budgetRefusal(c.Client.Status, c.Client.Body):
		// The control plane's budget refused on its live spend, the engine's snapshot did not (yet): a lag, not a diff.
		l.Outcome, l.Reason = "skipped", budgetLag
		return l
	case c == nil || c.ProviderResponse == nil || c.Client == nil:
		js := "no capture"
		if c != nil && c.Client != nil {
			js = "answered " + NumberString(float64(c.Client.Status)) + " without a provider call"
		}
		d("served", js, "the engine would call the provider")
	case len(c.ProviderRequests) != 1:
		d("providerRequests", len(c.ProviderRequests), 1)
	default:
		pr := c.ProviderRequests[0]
		if pr.URL != p.URL {
			dt("providerRequest.url", pr.URL, p.URL)
		}
		goH := map[string]string{}
		for k := range p.Header {
			goH[strings.ToLower(k)] = p.Header.Get(k)
		}
		if a, b := headerSet(pr.Headers), headerSet(goH); a != b {
			dt("providerRequest.headers", a, b)
		}
		jsBody := ""
		if pr.Body != nil {
			jsBody = *pr.Body
		}
		if jsBody != string(p.Body) {
			dt("providerRequest.body", jsBody, string(p.Body))
		}
		var a Answer
		if p.Stream && StreamsBody(c.ProviderResponse.Status) {
			// A stream (P4c): the provider's text as the control plane decoded it, replayed through the rewriter; the
			// last partial line only after a clean end.
			if c.ProviderResponse.Truncated {
				l.Outcome, l.Reason = "skipped", "the stream's capture is over its limit"
				return l
			}
			cs := NewChatStream(p.Model, p.WantsUsage, func() float64 { return 0 })
			out, _ := cs.Feed([]byte(c.ProviderResponse.Body))
			if c.ProviderResponse.Ended == "eof" {
				out = append(out, cs.End()...)
			}
			a = Answer{Status: http.StatusOK, Body: out, Usage: s.StreamLine(p, cs, 0)}
		} else {
			a = s.Respond(p, c.ProviderResponse.Status, []byte(c.ProviderResponse.Body), 0)
		}
		if a.Status != c.Client.Status {
			d("client.status", c.Client.Status, a.Status)
		}
		if string(a.Body) != c.Client.Body {
			dt("client.body", c.Client.Body, string(a.Body))
		}
		var goU map[string]any
		b, _ := json.Marshal(a.Usage)
		_ = json.Unmarshal(b, &goU)
		for _, f := range usageFields {
			js, gov := canonical(c.Usage[f]), canonical(goU[f])
			if js != gov {
				d("usage."+f, c.Usage[f], goU[f])
			}
		}
		if js, gov := text(c.Usage["error"]), text(goU["error"]); js != gov {
			dt("usage.error", js, gov)
		}
		jsCost, _ := c.Usage["cost"].(float64)
		if n := math.Round(jsCost * 1e9); int64(n) != a.Usage.Cost {
			d("usage.cost", int64(n), a.Usage.Cost)
		}
	}
	l.Outcome = "same"
	if len(l.Diffs) > 0 {
		l.Outcome = "diff"
	}
	return l
}

// CompareRefusal is the line for a request the engine would refuse itself, without a provider call (ref: a 402, or a
// guardrail 400/500): the control plane's status, its error body (when its capture has one) and the refusal's usage
// line. A budget disagreement is the snapshot's lag, not a diff (P4e Decision 18); an overrun the JS has no limit for
// is a real diff (Decision 9).
func (s *State) CompareRefusal(class string, p *Plan, ref *Answer, c *Capture) ShadowLine {
	l := ShadowLine{Class: class}
	if c != nil && c.Client != nil {
		l.Status = c.Client.Status
	}
	d := func(field string, js, gov any) { l.Diffs = append(l.Diffs, Diff{field, js, gov}) }
	refused := "refused " + NumberString(float64(ref.Status))
	switch {
	case c == nil || c.Client == nil:
		d("served", "no capture", refused)
	case len(c.ProviderRequests) > 0 || c.ProviderResponse != nil:
		if budgetRefusal(ref.Status, string(ref.Body)) {
			l.Outcome, l.Reason = "skipped", budgetLag
			return l
		}
		d("served", "called the provider", refused)
	default:
		if budgetRefusal(c.Client.Status, c.Client.Body) && budgetRefusal(ref.Status, string(ref.Body)) &&
			c.Client.Body != string(ref.Body) {
			l.Outcome, l.Reason = "skipped", budgetLag // two budgets, each side's newest view
			return l
		}
		if c.Client.Status != ref.Status {
			if budgetRefusal(c.Client.Status, c.Client.Body) || budgetRefusal(ref.Status, string(ref.Body)) {
				l.Outcome, l.Reason = "skipped", budgetLag
				return l
			}
			d("client.status", c.Client.Status, ref.Status)
		}
		if c.Client.Body != "" && c.Client.Body != string(ref.Body) {
			a, b := digest(c.Client.Body, string(ref.Body))
			d("client.body", a, b)
		}
		switch {
		case ref.NoLine && c.Usage != nil:
			d("usage", "a line", "none")
		case !ref.NoLine && c.Usage == nil:
			d("usage", "none", "a line")
		case !ref.NoLine:
			var goU map[string]any
			b, _ := json.Marshal(ref.Usage)
			_ = json.Unmarshal(b, &goU)
			for _, f := range usageFields {
				if canonical(c.Usage[f]) != canonical(goU[f]) {
					d("usage."+f, c.Usage[f], goU[f])
				}
			}
			if js, gov := text(c.Usage["error"]), text(goU["error"]); js != gov {
				a, b := digest(js, gov)
				d("usage.error", a, b)
			}
		}
	}
	l.Outcome = "same"
	if len(l.Diffs) > 0 {
		l.Outcome = "diff"
	}
	return l
}

func canonical(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func text(v any) string {
	s, _ := v.(string)
	return s
}

// headerSet is a header map as one text, the credential replaced by its hash prefix.
func headerSet(h map[string]string) string {
	low := make(map[string]string, len(h))
	keys := make([]string, 0, len(h))
	for k, v := range h {
		low[strings.ToLower(k)] = v
		keys = append(keys, strings.ToLower(k))
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		v := low[k]
		if k == "authorization" || k == "x-api-key" {
			v = hashPrefix(v)
		}
		b.WriteString(k + ": " + v + "\n")
	}
	return b.String()
}

// digest names two different texts without their content: length, hash prefix, and where they first differ.
func digest(js, gov string) (any, any) {
	at := 0
	for at < len(js) && at < len(gov) && js[at] == gov[at] {
		at++
	}
	one := func(s string) map[string]any { return map[string]any{"len": len(s), "sha": hashPrefix(s), "at": at} }
	return one(js), one(gov)
}

func hashPrefix(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:6])
}

// rateLimitedLine is the line of a call the engine's own rate limit would have answered: nothing of the serving path
// was compared, so both sides at 429 is "skipped", which the switch rule does not count (final review I3); the server
// answering anything else is a diff.
func rateLimitedLine(class string, status int) ShadowLine {
	l := ShadowLine{Class: class, Outcome: "skipped", Reason: "rate limit: both engines answered 429", Status: status}
	if status != http.StatusTooManyRequests {
		l.Outcome, l.Reason = "diff", ""
		l.Diffs = []Diff{{Field: "rateLimit", JS: status, Go: http.StatusTooManyRequests}}
	}
	return l
}

// NewShadowID is the id of one shadowed request.
func NewShadowID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Own is the front's call to the control plane's internal port as the gateway itself (X-ZP-Own).
type Own func(method, path string, body []byte) (int, []byte, error)

// shadowSlots bounds the compares running at once; a request past it is not compared.
var shadowSlots = make(chan struct{}, 8)

// Shadow forwards a chat request whose sub-class is on js to the control plane, marked for the compare when that
// sub-class is shadowed, the engine would serve it and a compare slot is free, then compares in the background once
// the client has its answer. A request the engine would hand off is not marked (the control plane keeps no capture for
// it) and gets a "handoff" line; a request past the slots is forwarded unmarked and gets no line (final review m9':
// captures only for compares that run).
func (e *Engine) Shadow(class string, w http.ResponseWriter, r *http.Request, rawPath string, js http.Handler,
	own func(method, path string, body []byte) (int, []byte, error)) {
	s := e.state.Load()
	if s == nil || class != "chat" {
		js.ServeHTTP(w, r)
		return
	}
	req := ReadRequest(r, rawPath)
	p := s.PlanChat(req, e.now())
	sub := subOf(&p, req)
	_, shadowed := e.mode(sub)
	if e.modes.Load() == nil {
		shadowed = sub == "chat" // P4b: the front calls Shadow for a shadowed chat
	}
	if !shadowed {
		js.ServeHTTP(w, r)
		return
	}
	e.shadowRead(sub, w, r, s, &p, js, own)
}

// subOf is the sub-class of chat a request belongs to: chat-stream when it asks for a stream (P4c). A plan the engine
// would serve says so itself; for a hand-off the body is read again, so its hand-off line is filed under the sub-class
// it asked for (final review m1).
func subOf(p *Plan, r *Request) string {
	if p.Handoff == "" {
		if p.Stream {
			return "chat-stream"
		}
		return "chat"
	}
	if b, err := Parse(r.Body); err == nil && b.Get("stream").Truthy() {
		return "chat-stream"
	}
	return "chat"
}

// shadowRead is Shadow after the request is read and planned: sub is the line's class.
func (e *Engine) shadowRead(sub string, w http.ResponseWriter, r *http.Request, s *State, p *Plan, js http.Handler,
	own func(method, path string, body []byte) (int, []byte, error)) {
	if own == nil {
		js.ServeHTTP(w, r)
		return
	}
	select {
	case shadowSlots <- struct{}{}:
	default:
		js.ServeHTTP(w, r)
		return
	}
	// The engine's decision on the key's window for a call the server answers. With one window across the engines
	// (P4f D16) the server's take of this call reaches the window as a note, so the engine only looks (Wait): counting it
	// too would count the call twice. Without the shared window (no SetOnTake: tests) it counts what it would serve.
	var wait int64
	id := ""
	if p.Handoff == "" {
		if e.onTake != nil {
			wait = e.limits.Wait(p.Key.ID, p.Key.RPM, e.now())
		} else {
			wait = e.limits.Take(p.Key.ID, p.Key.RPM, e.now())
		}
		// The budget, decided now on the snapshot (cheap); the guardrail checks run after the answer, off this path.
		if ref, unchecked := s.Budget(p, e.now(), e.staleMs, nil); ref != nil {
			p.refused = ref
		} else {
			p.Unchecked = unchecked
		}
		id = NewShadowID()
		r = front.WithShadowID(r, id) // set by the proxy's Rewrite, after the hop-by-hop headers are gone
	}
	// The front's ReverseProxy panics with http.ErrAbortHandler when the client leaves mid-answer (the stop button of
	// a stream). The slot is released and a "skipped" line posted on that path too, then the panic goes on to net/http
	// (final review I1: otherwise 8 leaves held every slot and the shadow compared nothing more).
	compared := false
	defer func() {
		if compared {
			return
		}
		v := recover()
		go func() {
			defer func() { <-shadowSlots }()
			l := ShadowLine{Class: sub, Outcome: "skipped", Reason: "the client left before the answer ended"}
			line, _ := json.Marshal(l)
			if _, _, err := own(http.MethodPost, "/_zp/shadow", line); err != nil {
				e.logf("shadow: the compare line could not be posted: %v", err)
			}
		}()
		if v != nil {
			panic(v)
		}
	}()
	js.ServeHTTP(w, r)
	compared = true
	go func() {
		defer func() { <-shadowSlots }()
		var c *Capture
		if id != "" {
			// The server stores a refusal's capture when its handler's promise settles, which on Deno can come a moment
			// after the answer reached the gateway: a 404 is asked again, a few times, before the line goes without one
			// (seen in CI as a rateLimit diff with the server's status 0).
			for try := 0; try < 5; try++ {
				code, body, err := own(http.MethodGet, "/_zp/shadow/"+id, nil)
				if err == nil && code == http.StatusOK {
					c = &Capture{}
					if json.Unmarshal(body, c) != nil {
						c = nil
					}
					break
				}
				if err != nil || code != http.StatusNotFound {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
		}
		var l ShadowLine
		ref := p.refused
		if ref == nil {
			ref = s.Guard(p, 0)
		}
		if ref != nil {
			l = s.CompareRefusal(sub, p, ref, c)
		} else {
			l = s.Compare(sub, p, c)
		}
		if wait > 0 {
			l = rateLimitedLine(sub, l.Status) // the engine would have answered 429 itself
		}
		line, _ := json.Marshal(l)
		if _, _, err := own(http.MethodPost, "/_zp/shadow", line); err != nil {
			e.logf("shadow: the compare line could not be posted: %v", err)
		}
	}()
}
