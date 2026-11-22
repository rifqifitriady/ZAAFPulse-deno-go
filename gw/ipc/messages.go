package ipc

import (
	"encoding/json"
	"errors"
	"fmt"
)

// The bodies of the v1 messages (ipc/v1/*.schema.json). Field rules: see shape.go. Money fields are int64 nano-USD.

// Message type names (D3).
const (
	TypeHello          = "hello"
	TypeHelloOK        = "hello.ok"
	TypeHelloRefused   = "hello.refused"
	TypeConfigPush     = "config.push"
	TypeSecretsHandoff = "secrets.handoff"
	TypeBudgetSnapshot = "budget.snapshot"
	TypeShutdown       = "shutdown"
	TypeAck            = "ack"
	TypeReady          = "ready"
	TypeHealth         = "health"
	TypeUsageReport    = "usage.report"
	TypeAuditEvent     = "audit.event"
	TypeTraceSpan      = "trace.span"
	TypeError          = "error"
	// P4f D16: one rate-limit window across the engines (control messages, not outbox events).
	TypeRateLimitTake   = "ratelimit.take"
	TypeRateLimitWindow = "ratelimit.window"
	// P4h Decisions 3, 14: the routing state replicated across the engines (control messages, not outbox events).
	TypeProviderNote = "provider.note"
	TypeRouteTake    = "route.take"
	TypeRouteState   = "route.state"
	// P5 Decision 9: one outbound connection the gateway made or refused (an outbox event, as usage.report).
	TypeEgressEvent = "egress.event"
)

// Hello is the gateway's first message: the protocols it speaks, its version, its durable event stream and the
// first event the control plane has not acknowledged.
type Hello struct {
	Protocol   []int  `json:"protocol" ipc:"minitems=1"`
	GwVersion  string `json:"gwVersion" ipc:"minlen=1"`
	Stream     string `json:"stream" ipc:"pattern=hex32"`
	OutboxFrom int64  `json:"outboxFrom" ipc:"min=1"`
}

// HelloOK is the control plane's answer: the protocol both speak.
type HelloOK struct {
	Protocol  int    `json:"protocol" ipc:"min=1"`
	CpVersion string `json:"cpVersion" ipc:"minlen=1"`
}

// HelloRefused ends the handshake; the gateway exits 2.
type HelloRefused struct {
	Reason string `json:"reason" ipc:"minlen=1"`
}

// ConfigPush is the gateway section of config.json (no keys, no secret references), the keys with their hashes and
// the price table in nano-USD per 1M tokens.
type ConfigPush struct {
	Gen     int64                 `json:"gen" ipc:"min=1"`
	Gateway GatewayConfig         `json:"gateway"`
	Keys    []GatewayKeyAuth      `json:"keys"`
	Pricing map[string]PriceEntry `json:"pricing"`
	Front   *FrontConfig          `json:"front,omitzero"`  // P4a: present when the gateway holds the main port
	Trace   *TraceSwitch          `json:"trace,omitzero"`  // P4f D8: whether a span is recorded anywhere
	Egress  *EgressConfig         `json:"egress,omitzero"` // P5 Decision 19: the egress endpoint's allow-list
}

// EgressConfig is the egress endpoint's allow-list (P5): AllowList "report" records a connection the list does not
// allow as would-refuse and makes it; "enforce" refuses it. Classes, each caller class's switch, is informational.
type EgressConfig struct {
	Allow     []EgressRule            `json:"allow"`
	AllowList string                  `json:"allowList" ipc:"enum=report|enforce"`
	Classes   map[string]EgressSwitch `json:"classes,omitzero"`
}

// EgressRule allows a host (a name, an address or "*.suffix") on one port, or on any port when Port is 0 (absent).
type EgressRule struct {
	Host string `json:"host" ipc:"minlen=1"`
	Port int    `json:"port,omitzero" ipc:"min=1,max=65535"`
}

// EgressSwitch is a caller class's switch: "direct", "check" or "go".
type EgressSwitch string

func (s *EgressSwitch) UnmarshalJSON(b []byte) error {
	var v string
	if err := json.Unmarshal(b, &v); err != nil || (v != "direct" && v != "check" && v != "go") {
		return fmt.Errorf("egress switch %s is not \"direct\", \"check\" or \"go\"", b)
	}
	*s = EgressSwitch(v)
	return nil
}

// TraceSwitch says whether the control plane records gateway spans (the local trace store on, or OTLP export on;
// lib/otel.js live()). Without it, or with spans false, the engine emits no span and no usage line names a trace.
type TraceSwitch struct {
	Spans bool `json:"spans"`
}

// FrontConfig puts the gateway in front (P4a spec D1): it listens on the main port and forwards each route class to
// the engine named in Engines; "js" is the control plane's internal port on 127.0.0.1.
type FrontConfig struct {
	Listen   FrontListen       `json:"listen"`
	TLS      *FrontTLS         `json:"tls,omitzero"`
	Upstream FrontUpstream     `json:"upstream"`
	Engines  map[string]Engine `json:"engines"`
	Shadow   FrontShadow       `json:"shadow"`
}

type FrontListen struct {
	Host string `json:"host" ipc:"minlen=1"`
	Port int64  `json:"port" ipc:"min=1,max=65535"`
}

type FrontTLS struct {
	CertPath string `json:"certPath" ipc:"minlen=1"`
	KeyPath  string `json:"keyPath" ipc:"minlen=1"`
}

type FrontUpstream struct {
	Port int64 `json:"port" ipc:"min=1,max=65535"`
}

// FrontShadow names the route classes on js whose requests the gateway compares with the go engine (P4b; empty in
// P4a).
type FrontShadow struct {
	Classes []RouteClass `json:"classes,omitzero"`
}

// RouteClass is one of the gateway's route classes (gw/front Classes), or a sub-class: chat-stream (P4c), and the P4d
// sub-classes (gw/front Subs); or token-saver, the token saver's switch (rtk spec D13); or provider-auth, the switch of
// token-auth provider calls (P4g spec Decisions 14, 17); or routes, the switch of route calls (P4h spec Decision 2).
type RouteClass string

func (c *RouteClass) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("route class %s is not a string", b)
	}
	switch s {
	case "chat", "chat-stream", "chat-anthropic", "chat-anthropic-stream", "messages", "messages-stream", "messages-count",
		"responses", "responses-stream", "embeddings", "models", "gemini", "gemini-stream", "gemini-count", "mcp", "a2a",
		"skills", "agents", "token-saver", "provider-auth", "routes":
		*c = RouteClass(s)
		return nil
	}
	return fmt.Errorf("route class %q is not one of the gateway's", s)
}

// Engine is "js" or "go".
type Engine string

func (e *Engine) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil || (s != "js" && s != "go") {
		return fmt.Errorf("engine %s is not \"js\" or \"go\"", b)
	}
	*e = Engine(s)
	return nil
}

type GatewayConfig struct {
	Providers  []ProviderConfig `json:"providers"`
	Routes     []RouteConfig    `json:"routes"`
	Guardrails GuardrailsPolicy `json:"guardrails"`
	Defaults   Defaults         `json:"defaults"`
	TokenSaver bool             `json:"tokenSaver"`
	ModelMap   []ModelMapEntry  `json:"modelMap"`
}

type ProviderConfig struct {
	ID         string            `json:"id" ipc:"minlen=1"`
	Preset     string            `json:"preset"`
	BaseURL    string            `json:"baseUrl"`
	Models     []string          `json:"models,omitzero"`
	Aliases    map[string]string `json:"aliases,omitzero"`
	Enabled    *bool             `json:"enabled,omitzero"`
	Tier       *string           `json:"tier,omitzero" ipc:"enum=private|external"`
	Approved   *bool             `json:"approved,omitzero"`
	Auth       *ProviderAuth     `json:"auth,omitzero"`
	Format     *string           `json:"format,omitzero" ipc:"enum=openai|anthropic"`
	Responses  *bool             `json:"responses,omitzero"`
	Reasoning  *bool             `json:"reasoning,omitzero"`
	TokenParam *string           `json:"tokenParam,omitzero" ipc:"enum=max_tokens|max_completion_tokens"`
	Span       *ProviderSpan     `json:"span,omitzero"` // P4f D4: lib/otel.js providerSpanAttrs, computed by the control plane
}

// ProviderSpan is a provider's model-span attributes (P4f D4): gen_ai.provider.name, server.address and server.port, as
// lib/otel.js providerSpanAttrs computes them from the provider's preset and its baseUrl (WHATWG URL).
type ProviderSpan struct {
	Name    string  `json:"name" ipc:"minlen=1"`
	Address *string `json:"address,omitzero"`
	Port    *int64  `json:"port,omitzero" ipc:"min=0"`
}

type ProviderAuth struct {
	Type     string  `json:"type" ipc:"minlen=1"`
	TokenURL *string `json:"tokenUrl,omitzero"`
	ClientID *string `json:"clientId,omitzero"`
	Scope    *string `json:"scope,omitzero"`
	Grant    *string `json:"grant,omitzero" ipc:"enum=client_credentials|refresh_token"`
}

type RouteConfig struct {
	Name        string   `json:"name" ipc:"minlen=1"`
	Targets     []string `json:"targets"`
	Strategy    *string  `json:"strategy,omitzero" ipc:"enum=fallback|round-robin"`
	Description *string  `json:"description,omitzero"`
}

type GuardrailsPolicy struct {
	Scope     string `json:"scope" ipc:"enum=external|all"`
	Secrets   string `json:"secrets" ipc:"enum=off|log|redact|block"`
	PII       string `json:"pii" ipc:"enum=off|log|redact|block"`
	Injection string `json:"injection" ipc:"enum=off|log|block"`
}

// GuardrailsPartial is a key's own guardrails: each setting overrides the gateway's when present.
type GuardrailsPartial struct {
	Scope     *string `json:"scope,omitzero" ipc:"enum=external|all"`
	Secrets   *string `json:"secrets,omitzero" ipc:"enum=off|log|redact|block"`
	PII       *string `json:"pii,omitzero" ipc:"enum=off|log|redact|block"`
	Injection *string `json:"injection,omitzero" ipc:"enum=off|log|block"`
}

type Defaults struct {
	Model        string `json:"model"`
	PlannerModel string `json:"plannerModel"`
	CodeModel    string `json:"codeModel"`
}

type ModelMapEntry struct {
	Match string `json:"match"`
	To    string `json:"to"`
}

// GatewayKeyAuth is a key as the gateway enforces it, with the SHA-256 hex of its secret (never the secret).
type GatewayKeyAuth struct {
	ID             string            `json:"id" ipc:"minlen=1"`
	Name           string            `json:"name"`
	Scopes         []string          `json:"scopes"`
	Models         []string          `json:"models"`
	Agents         []string          `json:"agents"`
	Knowledge      []string          `json:"knowledge"`
	BudgetUSD      *float64          `json:"budgetUSD"`
	RPM            int64             `json:"rpm" ipc:"min=0"`
	PrivateOnly    bool              `json:"privateOnly"`
	ExpiresAt      *int64            `json:"expiresAt"`
	RevokedAt      *int64            `json:"revokedAt"`
	Guardrails     GuardrailsPartial `json:"guardrails"`
	Prefix         string            `json:"prefix"`
	ApproveOwnRuns bool              `json:"approveOwnRuns"`
	CreatedAt      int64             `json:"createdAt" ipc:"min=0"`
	LastUsedAt     *int64            `json:"lastUsedAt"`
	Hash           string            `json:"hash" ipc:"pattern=hex64"`
}

// PriceEntry is one model's list price in nano-USD per 1M tokens (WebSearch: per search).
type PriceEntry struct {
	Input        int64       `json:"input" ipc:"min=0"`
	Output       int64       `json:"output" ipc:"min=0"`
	CacheRead    *int64      `json:"cacheRead,omitzero" ipc:"min=0"`
	CacheWrite5m *int64      `json:"cacheWrite5m,omitzero" ipc:"min=0"`
	CacheWrite1h *int64      `json:"cacheWrite1h,omitzero" ipc:"min=0"`
	WebSearch    *int64      `json:"webSearch,omitzero" ipc:"min=0"`
	Tiers        []PriceTier `json:"tiers,omitzero"`
	Estimate     *bool       `json:"estimate,omitzero"`
}

type PriceTier struct {
	Above        int64  `json:"above" ipc:"min=0"`
	Input        int64  `json:"input" ipc:"min=0"`
	Output       int64  `json:"output" ipc:"min=0"`
	CacheRead    *int64 `json:"cacheRead,omitzero" ipc:"min=0"`
	CacheWrite5m *int64 `json:"cacheWrite5m,omitzero" ipc:"min=0"`
	CacheWrite1h *int64 `json:"cacheWrite1h,omitzero" ipc:"min=0"`
}

// SecretsHandoff carries resolved provider secrets. They live in memory only: String and GoString hide them from every
// log, and Decode zeroes the envelope's raw body (E6).
type SecretsHandoff struct {
	Gen        int64            `json:"gen" ipc:"min=1"`
	Providers  []ProviderSecret `json:"providers"`
	Mask       []string         `json:"mask"`
	FrontToken string           `json:"frontToken,omitzero" ipc:"pattern=hex64"` // P4a: the key of the X-ZP-Client header's HMAC
}

func (s SecretsHandoff) String() string {
	return fmt.Sprintf("SecretsHandoff{gen %d, %d providers, %d masked values}", s.Gen, len(s.Providers), len(s.Mask))
}

// GoString keeps %#v from printing the fields (Mask holds the secret values themselves).
func (s SecretsHandoff) GoString() string { return s.String() }

type ProviderSecret struct {
	ID    string      `json:"id" ipc:"minlen=1"`
	Value *string     `json:"value,omitzero"`
	Token *OAuthToken `json:"token,omitzero"`
}

func (p ProviderSecret) String() string { return "ProviderSecret{" + p.ID + ", <redacted>}" }

func (p ProviderSecret) GoString() string { return p.String() }

type OAuthToken struct {
	Value     string `json:"value" ipc:"minlen=1"`
	ExpiresAt int64  `json:"expiresAt" ipc:"min=0"`
}

func (t OAuthToken) String() string {
	return fmt.Sprintf("OAuthToken{<redacted>, expires %d}", t.ExpiresAt)
}

func (t OAuthToken) GoString() string { return t.String() }

// BudgetSnapshot is the budgets as the guard enforces them, with each key's cost center.
type BudgetSnapshot struct {
	At        int64             `json:"at" ipc:"min=0"`
	Budgets   []BudgetView      `json:"budgets"`
	KeyCenter map[string]string `json:"keyCenter"`
}

type BudgetView struct {
	ID        string      `json:"id" ipc:"minlen=1"`
	Name      string      `json:"name"`
	Scope     BudgetScope `json:"scope"`
	Period    string      `json:"period" ipc:"enum=day|week|month"`
	Currency  string      `json:"currency"`
	Measure   string      `json:"measure" ipc:"enum=list|billed"`
	Enforce   string      `json:"enforce" ipc:"enum=none|block|pause"`
	Amount    float64     `json:"amount"`
	From      string      `json:"from"`
	To        string      `json:"to"`
	State     string      `json:"state" ipc:"minlen=1"`
	SpentUSD  *float64    `json:"spentUSD"`
	AmountUSD *float64    `json:"amountUSD"`
	Reason    *string     `json:"reason"`
}

// BudgetScope is "global" or {"dim", "value"}.
type BudgetScope struct {
	Global bool
	Dim    string
	Value  string
}

func (s BudgetScope) MarshalJSON() ([]byte, error) {
	if s.Global {
		return []byte(`"global"`), nil
	}
	return json.Marshal(struct {
		Dim   string `json:"dim"`
		Value string `json:"value"`
	}{s.Dim, s.Value})
}

func (s *BudgetScope) UnmarshalJSON(b []byte) error {
	var word string
	if err := json.Unmarshal(b, &word); err == nil {
		if word != "global" {
			return fmt.Errorf("budget scope %q is not \"global\" or an object", word)
		}
		*s = BudgetScope{Global: true}
		return nil
	}
	var o map[string]any
	if err := json.Unmarshal(b, &o); err != nil || o == nil {
		return errors.New(`budget scope must be "global" or {"dim", "value"}`)
	}
	dim, _ := o["dim"].(string)
	value, _ := o["value"].(string)
	if dim == "" || value == "" {
		return errors.New("budget scope needs a non-empty dim and value")
	}
	*s = BudgetScope{Dim: dim, Value: value}
	return nil
}

type Shutdown struct {
	GraceMs int64 `json:"graceMs" ipc:"min=0"`
}

type Ack struct {
	Stream string `json:"stream" ipc:"pattern=hex32"`
	UpTo   int64  `json:"upTo" ipc:"min=0"`
}

type Ready struct {
	Gen    int64        `json:"gen" ipc:"min=1"`
	Egress *ReadyEgress `json:"egress,omitzero"` // P5: the egress endpoint, when it listens
}

// ReadyEgress is the port of the gateway's egress endpoint on 127.0.0.1 (P5).
type ReadyEgress struct {
	Port int `json:"port" ipc:"min=1,max=65535"`
}

type Health struct {
	Inflight      int64  `json:"inflight" ipc:"min=0"`
	OutboxDepth   int64  `json:"outboxDepth" ipc:"min=0"`
	UptimeMs      int64  `json:"uptimeMs" ipc:"min=0"`
	Gen           int64  `json:"gen" ipc:"min=0"`
	SnapshotAgeMs *int64 `json:"snapshotAgeMs" ipc:"min=0"`
}

// UsageReport is one gateway call; Budget is "unchecked" when no fresh snapshot could check it.
type UsageReport struct {
	Record UsageRecord `json:"record"`
	Budget *string     `json:"budget,omitzero" ipc:"enum=unchecked"`
}

type UsageRecord struct {
	Ts          int64             `json:"ts" ipc:"min=0"`
	KeyID       string            `json:"keyId" ipc:"minlen=1"`
	Provider    string            `json:"provider"`
	Model       string            `json:"model"`
	Requested   *string           `json:"requested,omitzero"`
	RoutedTo    *string           `json:"routedTo,omitzero"`
	Kind        *string           `json:"kind,omitzero" ipc:"enum=chat|agent|embeddings|mcp"`
	Format      *string           `json:"format,omitzero" ipc:"enum=openai|anthropic|responses|gemini"`
	Stream      *bool             `json:"stream,omitzero"`
	Status      int64             `json:"status" ipc:"min=0"`
	Ms          float64           `json:"ms" ipc:"min=0"`
	TTFT        Nullable[float64] `json:"ttft,omitzero"`
	Input       *int64            `json:"input,omitzero" ipc:"min=0"`
	Output      *int64            `json:"output,omitzero" ipc:"min=0"`
	Cached      *int64            `json:"cached,omitzero" ipc:"min=0"`
	Cost        int64             `json:"cost" ipc:"min=0"`
	Error       *string           `json:"error,omitzero"`
	Guard       *GuardrailOutcome `json:"guard,omitzero"`
	Client      *string           `json:"client,omitzero" ipc:"enum=claude-code|codex"`
	Local       *bool             `json:"local,omitzero" ipc:"enum=true"`
	SavedTokens *int64            `json:"savedTokens,omitzero" ipc:"min=0"`
	Agent       *string           `json:"agent,omitzero"`
	SessionID   *string           `json:"sessionId,omitzero"`
	RunID       *string           `json:"runId,omitzero"`
	TraceID     *string           `json:"traceId,omitzero"`
}

type GuardrailOutcome struct {
	Action string         `json:"action" ipc:"enum=redacted|logged|blocked"`
	Hits   []GuardrailHit `json:"hits"`
	Error  *bool          `json:"error,omitzero"`
}

type GuardrailHit struct {
	Check string `json:"check" ipc:"enum=secrets|pii|injection"`
	Label string `json:"label"`
	Count int64  `json:"count" ipc:"min=0"`
}

// AuditEventBody is a governance entry (log "governance") or an MCP tool call (log "mcp"). Event keeps the exact
// JSON; Governance() and MCP() give the decoded entry of its kind after Decode.
type AuditEventBody struct {
	Log        string          `json:"log" ipc:"enum=governance|mcp"`
	Event      json.RawMessage `json:"event"`
	governance *AuditEvent
	mcp        *MCPAuditEntry
}

func (a *AuditEventBody) Governance() *AuditEvent { return a.governance }
func (a *AuditEventBody) MCP() *MCPAuditEntry     { return a.mcp }

func (a *AuditEventBody) afterDecode(strict bool) error {
	switch a.Log {
	case "governance":
		a.governance = new(AuditEvent)
		return decodeBody(a.Event, a.governance, strict, "body.event")
	case "mcp":
		a.mcp = new(MCPAuditEntry)
		return decodeBody(a.Event, a.mcp, strict, "body.event")
	}
	return fmt.Errorf("body.log: %q is not governance or mcp", a.Log)
}

// GovernanceEvent and MCPEvent build an audit.event body.
func GovernanceEvent(e AuditEvent) (*AuditEventBody, error) {
	raw, err := json.Marshal(e)
	return &AuditEventBody{Log: "governance", Event: raw, governance: &e}, err
}

func MCPEvent(e MCPAuditEntry) (*AuditEventBody, error) {
	raw, err := json.Marshal(e)
	return &AuditEventBody{Log: "mcp", Event: raw, mcp: &e}, err
}

type AuditEvent struct {
	Action  string        `json:"action" ipc:"minlen=1"`
	Target  *AuditTarget  `json:"target,omitzero"`
	Outcome *string       `json:"outcome,omitzero" ipc:"enum=ok|refused|error"`
	Status  *int64        `json:"status,omitzero"`
	Changes []AuditChange `json:"changes,omitzero"`
	Actor   *AuditActor   `json:"actor,omitzero"`
}

type AuditTarget struct {
	Type string  `json:"type" ipc:"minlen=1"`
	ID   *string `json:"id,omitzero"`
	Name *string `json:"name,omitzero"`
}

type AuditChange struct {
	Path string          `json:"path"`
	From json.RawMessage `json:"from,omitzero"`
	To   json.RawMessage `json:"to,omitzero"`
}

type AuditActor struct {
	Via   string  `json:"via" ipc:"minlen=1"`
	KeyID *string `json:"keyId,omitzero"`
	User  *string `json:"user,omitzero"`
	Rule  *string `json:"rule,omitzero"`
}

// MCPAuditEntry is one MCP tool call as lib/integrations.js audits it: argument names, never values.
type MCPAuditEntry struct {
	Ts          int64    `json:"ts" ipc:"min=0"`
	Integration string   `json:"integration" ipc:"minlen=1"`
	Tool        string   `json:"tool" ipc:"minlen=1"`
	OK          bool     `json:"ok"`
	Ms          float64  `json:"ms" ipc:"min=0"`
	Args        []string `json:"args"`
	KeyID       *string  `json:"keyId,omitzero"`
}

// TraceSpanBody is one finished span as the local trace store keeps it, and what OTLP export needs beyond it (P4f D11).
type TraceSpanBody struct {
	Span SpanRecord `json:"span"`
	OTLP *SpanOTLP  `json:"otlp,omitzero"`
}

// SpanOTLP is what OTLP export needs beyond the stored record (P4f D11): the span's tracestate, whether its parent is
// remote (a server span continuing an incoming traceparent), and its status message (up to 500 UTF-16 units; the
// stored err keeps 200).
type SpanOTLP struct {
	TraceState *string `json:"traceState,omitzero"`
	Remote     *bool   `json:"remote,omitzero" ipc:"enum=true"`
	Message    *string `json:"message,omitzero"`
}

type SpanRecord struct {
	T   string  `json:"t" ipc:"pattern=hex32"`
	S   string  `json:"s" ipc:"pattern=hex16"`
	P   *string `json:"p" ipc:"pattern=hex16"`
	N   string  `json:"n" ipc:"minlen=1"`
	K   string  `json:"k" ipc:"enum=internal|server|client"`
	St  int64   `json:"st" ipc:"min=0"`
	D   int64   `json:"d" ipc:"min=0"`
	OK  bool    `json:"ok"`
	Err *string `json:"err,omitzero"`
	Src string  `json:"src"`
	Svc string  `json:"svc"`
	// A holds the attributes' exact JSON, in the order they were built (P4f D3): encoding/json sorts a map's keys, while
	// the JS store keeps insertion order, so a stored line from Go is byte-equal to the JS one only with the raw bytes.
	A json.RawMessage `json:"a" ipc:"object"`
}

type ErrorBody struct {
	Code    string `json:"code" ipc:"minlen=1"`
	Message string `json:"message"`
}

// RateLimitTake is one request admitted under a key's requests-per-minute window, for the other engine (P4f D16; both
// directions, a control message: a lost take changes a window, never a ledger).
type RateLimitTake struct {
	KeyID string `json:"keyId" ipc:"minlen=1"`
	At    int64  `json:"at" ipc:"min=0"`
}

// RateLimitWindow seeds a new gateway with the last minute's takes per key (P4f D16), before budget.snapshot.
type RateLimitWindow struct {
	Windows []RateWindow `json:"windows"`
}

// RateWindow is one key's takes, epoch ms.
type RateWindow struct {
	KeyID string  `json:"keyId" ipc:"minlen=1"`
	At    []int64 `json:"at"`
}

// ProviderNote is one provider answer an engine read, for the other engine's cooldowns and the control plane's quota
// readings (P4h Decisions 3, 4; both directions, a control message). At is the observer's clock (epoch ms); Headers
// holds the names noteResponse reads plus retry-after, lower case, values joined with ", ".
type ProviderNote struct {
	ProviderID string            `json:"providerId" ipc:"minlen=1"`
	Status     int               `json:"status" ipc:"min=100,max=599"`
	At         int64             `json:"at" ipc:"min=0"`
	Headers    map[string]string `json:"headers"`
}

// RouteTake is one round-robin rotation an engine took on a route (P4h Decision 3; both directions).
type RouteTake struct {
	Name string `json:"name" ipc:"minlen=1"`
	At   int64  `json:"at" ipc:"min=0"`
}

// RouteState seeds a new gateway with the control plane's rotation counters and every cooldown entry (P4h Decision 3), after
// ratelimit.window and before budget.snapshot.
type RouteState struct {
	RR       []RouteRR       `json:"rr"`
	Cooldown []RouteCooldown `json:"cooldown"`
}

// RouteRR is one route's rotation counter: providers.js rr (the n of the next take is N % the target count).
type RouteRR struct {
	Name string `json:"name" ipc:"minlen=1"`
	N    int    `json:"n" ipc:"min=0"`
}

// RouteCooldown is one provider's cooldown entry, expired ones too (their strikes set the next back-off). Until is epoch ms and may be fractional (a fractional retry-after or
// reset gives a fractional until in JS too; P4h plan deviation of Task 1).
type RouteCooldown struct {
	ProviderID string  `json:"providerId" ipc:"minlen=1"`
	Until      float64 `json:"until" ipc:"min=0"`
	Strikes    int     `json:"strikes" ipc:"min=0"`
	Reason     string  `json:"reason"`
}

// EgressEvent is one outbound connection the gateway made or refused (P5 Decision 9): Outcome ok (an answer came, any
// status), error (a failure code in Error), refused (allow-list, enforce), would-refuse (allow-list, report) or checked;
// Via proxy, tunnel, engine (the engine's own provider calls) or check (a /check verdict). Status 0 is none. Bytes are body bytes. No path, query
// or header is recorded.
type EgressEvent struct {
	TS       int64   `json:"ts" ipc:"min=0"`
	Purpose  string  `json:"purpose" ipc:"minlen=1"`
	Method   string  `json:"method" ipc:"minlen=1"`
	Scheme   string  `json:"scheme" ipc:"enum=http|https|tcp"`
	Host     string  `json:"host" ipc:"minlen=1"`
	Port     int     `json:"port" ipc:"min=1,max=65535"`
	Status   int     `json:"status,omitzero" ipc:"min=100,max=999"`
	Outcome  string  `json:"outcome" ipc:"enum=ok|error|refused|would-refuse|checked"`
	Error    string  `json:"error,omitzero" ipc:"minlen=1"`
	BytesOut int64   `json:"bytesOut" ipc:"min=0"`
	BytesIn  int64   `json:"bytesIn" ipc:"min=0"`
	MS       float64 `json:"ms" ipc:"min=0"`
	Via      string  `json:"via" ipc:"enum=proxy|tunnel|engine|check"`
}
