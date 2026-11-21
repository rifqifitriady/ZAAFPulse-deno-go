package engine

import (
	"regexp"
	"slices"
	"strings"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

// Preset is what the engine needs of a lib/gateway/providers.js preset (the table is checked against the JS one by
// gw/testdata/providers, scripts/fixtures/gen-providers.js).
type Preset struct {
	Local               bool   `json:"local,omitzero"`
	Forwards            bool   `json:"forwards,omitzero"`
	MaxCompletionTokens bool   `json:"maxCompletionTokens,omitzero"`
	Format              string `json:"format,omitzero"`
	StreamUsage         *bool  `json:"streamUsage,omitzero"` // false: the provider refuses stream_options
}

// Presets are the presets with a flag the engine reads; any other preset name has none.
var Presets = map[string]Preset{
	"ollama": {Local: true}, "lmstudio": {Local: true}, "vllm": {Local: true}, "tgi": {Local: true},
	"llamacpp": {Local: true}, "jan": {Local: true}, "localai": {Local: true},
	"litellm":          {Local: true, Forwards: true},
	"9router":          {Local: true, Forwards: true},
	"openai":           {MaxCompletionTokens: true},
	"anthropic":        {Format: "anthropic"},
	"azure-openai":     {MaxCompletionTokens: true},
	"anthropic-openai": {StreamUsage: boolPtr(false)},
}

func boolPtr(b bool) *bool { return &b }

// StreamUsageOf is providers.js streamUsage: the preset does not say streamUsage: false and the format is not anthropic
// (the gateway then asks the provider for a usage chunk at the end of a stream).
func StreamUsageOf(p *ipc.ProviderConfig) bool {
	return !isFalse(Presets[p.Preset].StreamUsage) && FormatOf(p) != "anthropic"
}

// Target is one provider and the model it is asked for (lib/gateway/providers.js resolve).
type Target struct {
	Provider *ipc.ProviderConfig
	Model    string // the provider's own model name
	Name     string // the name it was asked by: "<provider>/<model>" or an alias
}

func isTrue(b *bool) bool  { return b != nil && *b }
func isFalse(b *bool) bool { return b != nil && !*b }

// TierOf is providers.js tierOf: the tier the owner set, else private for a local preset.
func TierOf(p *ipc.ProviderConfig) string {
	if p.Tier != nil && *p.Tier != "" {
		return *p.Tier
	}
	if Presets[p.Preset].Local {
		return "private"
	}
	return "external"
}

// FormatOf is providers.js formatOf.
func FormatOf(p *ipc.ProviderConfig) string {
	if p.Format != nil && *p.Format != "" {
		return *p.Format
	}
	if f := Presets[p.Preset].Format; f != "" {
		return f
	}
	return "openai"
}

// Usable is providers.js usable: enabled, and private or approved.
func Usable(p *ipc.ProviderConfig) bool {
	return !isFalse(p.Enabled) && (TierOf(p) == "private" || !isFalse(p.Approved))
}

// Resolve is providers.js resolve: an alias of a usable provider (in list order), else "<id>/<model>" of the first
// usable provider with that id; nil when nothing matches.
func Resolve(providers []ipc.ProviderConfig, name string) *Target {
	if name == "" {
		return nil
	}
	for i := range providers {
		p := &providers[i]
		if Usable(p) {
			if m, ok := p.Aliases[name]; ok && m != "" {
				return &Target{Provider: p, Model: m, Name: name}
			}
		}
	}
	if i := strings.IndexByte(name, '/'); i > 0 {
		for j := range providers {
			p := &providers[j]
			if p.ID == name[:i] && Usable(p) {
				return &Target{Provider: p, Model: name[i+1:], Name: name}
			}
		}
	}
	return nil
}

var openAIReasoning = regexp.MustCompile(`(?i)(^|/)(o[1-9]|gpt-5)`)

// TokenParamOf is providers.js tokenParamOf.
func TokenParamOf(p *ipc.ProviderConfig, model string) string {
	if p.TokenParam != nil && (*p.TokenParam == "max_tokens" || *p.TokenParam == "max_completion_tokens") {
		return *p.TokenParam
	}
	if Presets[p.Preset].MaxCompletionTokens || openAIReasoning.MatchString(model) {
		return "max_completion_tokens"
	}
	return "max_tokens"
}

// AdaptChat is providers.js adaptChat for a body from a client (no field the gateway derived itself): max_tokens
// becomes max_completion_tokens, at the end, for a target that takes only that. body is changed in place.
func AdaptChat(p *ipc.ProviderConfig, model string, body *Value) {
	mt := body.Get("max_tokens")
	mct := body.Get("max_completion_tokens")
	if mt != nil && mt.Kind != Null && (mct == nil || mct.Kind == Null) && TokenParamOf(p, model) == "max_completion_tokens" {
		body.Set("max_completion_tokens", mt)
		body.Delete("max_tokens")
	}
}

var cloudModel = regexp.MustCompile(`(?i)(^|[:-])cloud$`)

// ScopeTier is lib/gateway/guardrails.js scopeTier: an Ollama cloud model and a local router that forwards (without a
// tier of its own) count as external.
func ScopeTier(p *ipc.ProviderConfig, model string) string {
	if cloudModel.MatchString(model) {
		return "external"
	}
	if (p.Tier == nil || *p.Tier == "") && Presets[p.Preset].Forwards {
		return "external"
	}
	return TierOf(p)
}

// GuardActive is lib/gateway/guardrails.js resolvePolicy then inScope: whether any check would look at a call to a
// target of this tier. The engine serves only calls no check looks at; the checks themselves are P4e.
func GuardActive(global ipc.GuardrailsPolicy, key *ipc.GatewayKeyAuth, tier string) bool {
	return checksApply(PolicyFor(global, key), tier)
}

var takesReasoningEffort = regexp.MustCompile(`(?i)(^|/)(o[1-9]|gpt-5|gpt-oss)`)
var geminiReasoning = regexp.MustCompile(`gemini-(2\.5|[3-9])`)

// TakesReasoning is providers.js takesReasoning: the provider's reasoning flag, else a model known to take
// reasoning_effort (o-series, gpt-5+, gpt-oss, Gemini 2.5+ on the gemini preset).
func TakesReasoning(p *ipc.ProviderConfig, model string) bool {
	if p.Reasoning != nil {
		return *p.Reasoning
	}
	return takesReasoningEffort.MatchString(model) || (p.Preset == "gemini" && geminiReasoning.MatchString(model))
}

// AdaptChatTagged is adaptChat for a translated body (P4d): AdaptChat, then a reasoning_effort the translation added
// (soft) is removed for a model that does not take it, and "reasoning" is added once to dropped (nil: the body had no
// dropped list, as Gemini's).
func AdaptChatTagged(p *ipc.ProviderConfig, model string, body *Value, soft []string, dropped *[]string) {
	AdaptChat(p, model, body)
	for _, f := range soft {
		re := body.Get("reasoning_effort")
		if f != "reasoning_effort" || re == nil || re.Kind == Null || TakesReasoning(p, model) {
			continue
		}
		body.Delete("reasoning_effort")
		if dropped != nil && !slices.Contains(*dropped, "reasoning") {
			*dropped = append(*dropped, "reasoning")
		}
	}
}

// NativeResponses is providers.js nativeResponses: an OpenAI-format provider that takes /responses as it is (its
// responses flag, else the openai and azure-openai presets).
func NativeResponses(p *ipc.ProviderConfig) bool {
	if FormatOf(p) != "openai" {
		return false
	}
	if p.Responses != nil {
		return *p.Responses
	}
	return p.Preset == "openai" || p.Preset == "azure-openai"
}
