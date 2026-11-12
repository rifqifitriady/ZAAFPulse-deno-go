package engine

// The chat route class on the go engine (P4b, slice S2): POST /v1/chat/completions, not streaming, to one OpenAI-format
// provider with a plain API key (or none). PlanChat decides, without side effects, whether the engine serves a request
// and builds the provider request; everything it does not serve is handed to the control plane, which answers exactly
// as in js mode (P4b decision: hand-off). Respond and Failed turn the provider's answer (or the failed call) into the
// client's answer and the usage line, as lib/gateway/index.js chatCompletion and proxyChat do.

import (
	"bytes"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

// MaxBody is lib/gateway/index.js MAX_BODY: a bigger body is the control plane's 413.
const MaxBody = 20 << 20

// StaleMs: a budget snapshot older than this cannot tell the engine that no blocking budget applies.
const StaleMs = ipc.StaleAfterMs

// CORS are the headers of every gateway answer (lib/gateway/index.js CORS).
var CORS = [][2]string{
	{"Access-Control-Allow-Origin", "*"},
	{"Access-Control-Allow-Headers", "authorization, content-type, x-api-key, x-goog-api-key, x-zp-session, x-zp-token-saver, anthropic-version, anthropic-beta, a2a-version, a2a-extensions, mcp-protocol-version, mcp-session-id, last-event-id"},
	{"Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS"},
	{"Access-Control-Expose-Headers", "x-zp-session, x-zp-dropped, retry-after, mcp-session-id, www-authenticate"},
}

// State is one generation of what the control plane pushed: built when a config.push and its secrets.handoff are both
// in, swapped whole, never changed after.
type State struct {
	Gen        int64
	Gateway    ipc.GatewayConfig
	Keys       *Keys
	Secrets    map[string]string // provider id -> API key
	Mask       []string
	Pricer     *Pricer
	Snapshot   *ipc.BudgetSnapshot
	SnapshotAt int64 // epoch ms the snapshot says it was taken
}

// NewState builds a generation from config.push and secrets.handoff (same gen).
func NewState(c *ipc.ConfigPush, s *ipc.SecretsHandoff) *State {
	st := &State{Gen: c.Gen, Gateway: c.Gateway, Keys: NewKeys(c.Keys), Secrets: map[string]string{},
		Mask: append([]string(nil), s.Mask...), Pricer: NewPricer(c.Pricing)}
	for _, p := range s.Providers {
		if p.Value != nil && *p.Value != "" {
			st.Secrets[p.ID] = *p.Value
		}
	}
	return st
}

// WithSnapshot is the same generation with a newer budget snapshot.
func (s *State) WithSnapshot(b *ipc.BudgetSnapshot) *State {
	c := *s
	c.Snapshot, c.SnapshotAt = b, b.At
	return &c
}

// Request is what the engine reads of a client request.
type Request struct {
	Method  string
	RawPath string
	Query   string
	Header  http.Header
	Host    string // the Host header ("" when none)
	Peer    string // the client's address, "ip:port"
	Body    []byte
	TooBig  bool // the body was over MaxBody
}

// Plan is the engine's decision for one request.
type Plan struct {
	Handoff string // why the control plane serves it ("" when the engine does)
	Key     *ipc.GatewayKeyAuth
	Target  *Target
	Model   string // the model the client asked for
	URL     string
	Header  http.Header // the provider request's headers, with the credential
	Body    []byte      // the provider request's body, as JSON.stringify writes it
	Base    ipc.UsageRecord
}

func handoff(why string) Plan { return Plan{Handoff: why} }

// proxyHeaders: any of these means a proxy may stand between the client and the gateway (lib/gateway/client.js
// PROXY_HEADERS, server.js's no-sign-in refusal and lib/auth.js originAllowed): the control plane decides.
var proxyHeaders = []string{"Forwarded", "X-Forwarded-For", "X-Real-Ip", "Cf-Connecting-Ip", "True-Client-Ip",
	"X-Forwarded-Host", "X-Forwarded-Proto", "X-Forwarded-Port", "Via"}

// PlanChat decides for a POST to the exact path /v1/chat/completions, at now (epoch ms). It reads and changes nothing:
// the rate limit is the caller's (Take when serving, Wait when comparing).
func (s *State) PlanChat(r *Request, now int64) Plan {
	// The server's checks before the gateway (server.js handler): only a request every one of them passes in every
	// auth mode is the engine's, a loopback Host from a loopback client with no browser or proxy header.
	if !loopbackName(r.Host) || !loopbackPeer(r.Peer) {
		return handoff("access: not a loopback host and client")
	}
	for _, h := range append([]string{"Origin", "Referer", "Sec-Fetch-Site"}, proxyHeaders...) {
		if _, ok := r.Header[h]; ok {
			return handoff("access: a browser or proxy header")
		}
	}
	if r.Method != http.MethodPost {
		return handoff("method")
	}
	secret, ok := RequestSecret(r.Header, r.RawPath, r.Query)
	key, why := s.Keys.Authenticate(secret, ok, now)
	if key == nil {
		return handoff("key: " + why)
	}
	if r.TooBig || len(r.Body) == 0 {
		return handoff("body: empty or over 20 MB")
	}
	body, err := Parse(r.Body)
	if err != nil || body.Kind != Object {
		return handoff("body: not a JSON object the engine serves")
	}
	m := body.Get("model")
	if m == nil || m.Kind != String || m.S == "" {
		return handoff("model: missing")
	}
	model := m.S
	if strings.HasPrefix(model, "agent/") || strings.HasPrefix(model, "collab/") {
		return handoff("model: an agent or a collaboration")
	}
	if body.Get("stream").Truthy() {
		return handoff("stream: P4c")
	}
	if Authorize(key, "llm", model, "") != "" {
		return handoff("key: not allowed")
	}
	for _, rt := range s.Gateway.Routes {
		if rt.Name == model {
			return handoff("model: a route")
		}
	}
	t := Resolve(s.Gateway.Providers, model)
	if t == nil || (key.PrivateOnly && TierOf(t.Provider) != "private") {
		return handoff("model: no usable target")
	}
	if FormatOf(t.Provider) != "openai" {
		return handoff("provider: not the OpenAI format (P4d)")
	}
	cred := ""
	if a := t.Provider.Auth; a != nil {
		if a.Type != "key" {
			return handoff("provider: OAuth2 or a service account (P4g)")
		}
		if cred = s.Secrets[t.Provider.ID]; cred == "" {
			return handoff("provider: no credential")
		}
	}
	if key.BudgetUSD != nil {
		return handoff("budget: the key has a monthly budget")
	}
	if s.Snapshot == nil || now-s.SnapshotAt > StaleMs {
		return handoff("budget: no fresh snapshot")
	}
	if s.budgetApplies(key.ID, model, t.Provider.ID) {
		return handoff("budget: a blocking budget applies (P4e)")
	}
	if GuardActive(s.Gateway.Guardrails, key, ScopeTier(t.Provider, t.Model)) {
		return handoff("guardrails: a check applies (P4e)")
	}
	if s.Gateway.TokenSaver && !strings.EqualFold(strings.Join(r.Header.Values("X-Zp-Token-Saver"), ", "), "off") &&
		hasToolMessage(body) {
		return handoff("token saver: tool results")
	}
	out := body.Clone()
	out.Set("model", Str(t.Model))
	AdaptChat(t.Provider, t.Model, out)
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "application/json")
	if cred != "" {
		h.Set("Authorization", "Bearer "+cred)
	}
	return Plan{Key: key, Target: t, Model: model, URL: t.Provider.BaseURL + "/chat/completions", Header: h,
		Body: out.Stringify(), Base: s.base(key, t, model, r)}
}

func hasToolMessage(body *Value) bool {
	msgs := body.Get("messages")
	if msgs == nil || msgs.Kind != Array {
		return false
	}
	for _, m := range msgs.A {
		if r := m.Get("role"); r != nil && r.Kind == String && r.S == "tool" {
			return true
		}
	}
	return false
}

// budgetApplies: a blocking budget of the snapshot reaches this call (lib/cost/guard.js applies, source "gateway"),
// whatever its state: the engine leaves every budgeted call to the control plane until P4e.
func (s *State) budgetApplies(keyID, model, provider string) bool {
	nm := NormalizeModel(model)
	for _, b := range s.Snapshot.Budgets {
		if b.Enforce != "block" {
			continue
		}
		if b.Scope.Global {
			return true
		}
		switch v := b.Scope.Value; b.Scope.Dim {
		case "key", "source":
			if b.Scope.Dim == "key" && keyID == v || b.Scope.Dim == "source" && v == "gateway" {
				return true
			}
		case "center":
			if c, ok := s.Snapshot.KeyCenter[keyID]; ok && c == v {
				return true
			}
		case "model":
			if nm != "" && nm == v {
				return true
			}
		case "provider":
			if provider == v {
				return true
			}
		}
	}
	return false
}

func (s *State) base(key *ipc.GatewayKeyAuth, t *Target, model string, r *Request) ipc.UsageRecord {
	kind, format, stream := "chat", "openai", false
	rec := ipc.UsageRecord{KeyID: key.ID, Provider: t.Provider.ID, Model: model, Kind: &kind, Format: &format,
		Stream: &stream}
	if t.Name != model {
		rt := t.Name
		rec.RoutedTo = &rt
	}
	// lib/gateway/client.js clientTag: the tool by its User-Agent; local when the client is loopback and no proxy
	// header came (the engine serves no request with one).
	if c := clientOf(r.Header.Get("User-Agent")); c != "" {
		rec.Client = &c
	}
	local := true
	rec.Local = &local
	return rec
}

func clientOf(ua string) string {
	l := strings.ToLower(ua)
	switch {
	case strings.HasPrefix(l, "claude-cli/"):
		return "claude-code"
	case strings.HasPrefix(l, "codex_cli_rs/"), strings.HasPrefix(l, "codex/"):
		return "codex"
	}
	return ""
}

// loopbackName is lib/auth.js isLoopback of the Host without its port (server.js).
func loopbackName(host string) bool {
	h := strings.ToLower(host)
	if i := strings.LastIndexByte(h, ':'); i >= 0 && i < len(h)-1 && allDigits(h[i+1:]) {
		h = h[:i]
	}
	return h == "127.0.0.1" || h == "localhost" || h == "::1" || h == "[::1]"
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

// loopbackPeer is lib/gateway/client.js isLoopback of the client's address.
func loopbackPeer(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback() && (ip.To4() != nil || host == "::1")
}

// Answer is the client's answer and the usage line of one served call.
type Answer struct {
	Status int
	Header http.Header
	Body   []byte
	Usage  ipc.UsageRecord
}

func answerHeader() http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	for _, c := range CORS {
		h.Set(c[0], c[1])
	}
	return h
}

// Respond is chatCompletion's non-streaming answer for the provider's status and body, at ms after the start.
func (s *State) Respond(p *Plan, status int, raw []byte, ms float64) Answer {
	text := decodeUTF8(raw)
	j, err := Parse([]byte(text))
	if err != nil {
		j = nil
	}
	ok := status >= 200 && status <= 299
	in, out, cached := tokens(j)
	rec := p.Base
	rec.Status, rec.Ms = int64(status), ms
	rec.Input, rec.Output, rec.Cached = &in, &out, &cached
	if !ok {
		msg := text
		if e := j.Get("error"); e.Truthy() {
			if m := e.Get("message"); m.Truthy() && m.Kind == String {
				msg = m.S
			}
		}
		masked := s.mask(jsSlice(msg, 200))
		rec.Error = &masked
	}
	model := rec.Model
	if rec.RoutedTo != nil {
		model = *rec.RoutedTo
	}
	rec.Cost = s.Pricer.Cost(model, float64(in), float64(out), float64(cached))
	body := []byte(text)
	if j.Truthy() {
		if ok && j.Kind == Object {
			j.Set("model", Str(p.Model))
		}
		body = j.Stringify()
	}
	return Answer{Status: status, Header: answerHeader(), Body: body, Usage: rec}
}

// Failed is proxyChat's answer when the call to the only target threw: 502 upstream_error with the provider's id and
// what failed (masked), and a usage line with status 599.
func (s *State) Failed(p *Plan, why string, ms float64) Answer {
	msg := s.mask(p.Target.Provider.ID + ": " + why)
	rec := p.Base
	rec.RoutedTo = nil
	rec.Status, rec.Ms, rec.Error = 599, ms, &msg
	b := NewObject()
	e := NewObject()
	e.Set("message", Str(msg))
	e.Set("type", Str("upstream_error"))
	b.Set("error", e)
	return Answer{Status: http.StatusBadGateway, Header: answerHeader(), Body: b.Stringify(), Usage: rec}
}

// RateLimited is authed's 429 for a key past its requests per minute.
func RateLimited(key *ipc.GatewayKeyAuth, wait int64) Answer {
	rpm := key.RPM
	b := NewObject()
	e := NewObject()
	e.Set("message", Str("rate limit of "+NumberString(float64(rpm))+" requests per minute reached"))
	e.Set("type", Str("rate_limit_error"))
	b.Set("error", e)
	h := answerHeader()
	h.Set("Retry-After", NumberString(float64(wait)))
	return Answer{Status: http.StatusTooManyRequests, Header: h, Body: b.Stringify()}
}

// tokens is usageOf: prompt, completion and cached tokens, 0 when missing; a count that is not a whole number of zero
// or more is 0 here (the IPC record holds integers; the JS gateway would store it as it came).
func tokens(j *Value) (int64, int64, int64) {
	u := j.Get("usage")
	n := func(v *Value) int64 {
		if v == nil || v.Kind != Number || v.N < 0 || v.N != float64(int64(v.N)) {
			return 0
		}
		return int64(v.N)
	}
	return n(u.Get("prompt_tokens")), n(u.Get("completion_tokens")), n(u.Get("prompt_tokens_details").Get("cached_tokens"))
}

// mask is lib/secrets.js mask: every known secret value becomes ***.
func (s *State) mask(text string) string {
	for _, v := range s.Mask {
		if v != "" {
			text = strings.ReplaceAll(text, v, "***")
		}
	}
	return text
}

// jsSlice is String.prototype.slice(0, n): the first n UTF-16 code units (a pair cut in half loses its high half here;
// JavaScript would keep it as a lone surrogate).
func jsSlice(s string, n int) string {
	units := 0
	for i, r := range s {
		w := utf16.RuneLen(r)
		if units+w > n {
			return s[:i]
		}
		units += w
	}
	return s
}

// decodeUTF8 is Response.text(): a leading byte order mark dropped, invalid bytes each a U+FFFD.
func decodeUTF8(b []byte) string {
	b = bytes.TrimPrefix(b, []byte{0xef, 0xbb, 0xbf})
	if utf8.Valid(b) {
		return string(b)
	}
	return strings.ToValidUTF8(string(b), "�")
}

// NowMs is the engine's clock in epoch ms.
func NowMs() int64 { return time.Now().UnixMilli() }
