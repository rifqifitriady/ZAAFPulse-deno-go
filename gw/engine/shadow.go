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
)

// Capture is what the control plane recorded of one request it served.
type Capture struct {
	ProviderRequests []struct {
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
		Body    *string           `json:"body"`
	} `json:"providerRequests"`
	ProviderResponse *struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
	} `json:"providerResponse"`
	Client *struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
	} `json:"client"`
	Usage map[string]any `json:"usage"`
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
		a := s.Respond(p, c.ProviderResponse.Status, []byte(c.ProviderResponse.Body), 0)
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

// NewShadowID is the id of one shadowed request.
func NewShadowID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Own is the front's call to the control plane's internal port as the gateway itself (X-ZP-Own).
type Own func(method, path string, body []byte) (int, []byte, error)

// ShadowHeader marks a request the engine shadows.
const ShadowHeader = "X-Zp-Shadow"

// shadowSlots bounds the compares running at once; a request past it is not compared.
var shadowSlots = make(chan struct{}, 8)

// Shadow forwards a request of a class on js to the control plane marked for the compare, then compares in the
// background once the client has its answer.
func (e *Engine) Shadow(class string, w http.ResponseWriter, r *http.Request, rawPath string, js http.Handler,
	own func(method, path string, body []byte) (int, []byte, error)) {
	s := e.state.Load()
	if s == nil || class != "chat" {
		js.ServeHTTP(w, r)
		return
	}
	req := ReadRequest(r, rawPath)
	p := s.PlanChat(req, e.now())
	// The engine counts what it would serve, as when it serves (the control plane counts every request of the key).
	var wait int64
	if p.Handoff == "" {
		wait = e.limits.Take(p.Key.ID, p.Key.RPM, e.now())
	}
	id := NewShadowID()
	r.Header.Set(ShadowHeader, id)
	js.ServeHTTP(w, r)
	select {
	case shadowSlots <- struct{}{}:
	default:
		return
	}
	go func() {
		defer func() { <-shadowSlots }()
		var c *Capture
		if code, body, err := own(http.MethodGet, "/_zp/shadow/"+id, nil); err == nil && code == http.StatusOK {
			c = &Capture{}
			if json.Unmarshal(body, c) != nil {
				c = nil
			}
		}
		l := s.Compare(class, &p, c)
		if wait > 0 {
			// The engine would have answered 429 itself.
			l = ShadowLine{Class: class, Outcome: "same", Status: l.Status}
			if l.Status != http.StatusTooManyRequests {
				l.Outcome, l.Diffs = "diff", []Diff{{Field: "rateLimit", JS: l.Status, Go: http.StatusTooManyRequests}}
			}
		}
		line, _ := json.Marshal(l)
		if _, _, err := own(http.MethodPost, "/_zp/shadow", line); err != nil {
			e.logf("shadow: the compare line could not be posted: %v", err)
		}
	}()
}
