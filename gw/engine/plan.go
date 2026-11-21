package engine

// The planning pieces every gateway path shares (P4d): the admission checks of PlanChat, a model's single target, the
// provider's credential and request headers, and the dispatch by path class. Each planner is pure: it reads the
// request and the generation and decides, without I/O, whether the engine serves the call and how.

import (
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

// Sub-classes (P4d Decision 2): each has its own engine entry, shadow entry and switch-rule record.
const (
	SubChat                = "chat"
	SubChatStream          = "chat-stream"
	SubChatAnthropic       = "chat-anthropic"
	SubChatAnthropicStream = "chat-anthropic-stream"
	SubMessages            = "messages"
	SubMessagesStream      = "messages-stream"
	SubMessagesCount       = "messages-count"
	SubResponses           = "responses"
	SubResponsesStream     = "responses-stream"
	SubGemini              = "gemini"
	SubGeminiStream        = "gemini-stream"
	SubGeminiCount         = "gemini-count"
	SubEmbeddings          = "embeddings"
)

// Subs lists each path class's sub-classes.
var Subs = map[string][]string{
	"chat":       {SubChat, SubChatStream, SubChatAnthropic, SubChatAnthropicStream},
	"messages":   {SubMessages, SubMessagesStream, SubMessagesCount},
	"responses":  {SubResponses, SubResponsesStream},
	"gemini":     {SubGemini, SubGeminiStream, SubGeminiCount},
	"embeddings": {SubEmbeddings},
	"mcp":        {SubMCP},
}

// Shape is how the engine writes a refusal: lib/gateway/index.js fail (OpenAI), anthropicError, or the Google error.
type Shape int

const (
	ShapeOpenAI Shape = iota
	ShapeAnthropic
	ShapeGemini
)

// Upstream kinds of a plan: what the provider is asked, and so how its answer is turned into the client's.
const (
	UpChat            = "chat"             // <base>/chat/completions, OpenAI format
	UpAnthropic       = "anthropic"        // <base>/messages, a chat body translated (callChat's Anthropic branch)
	UpAnthropicNative = "anthropic-native" // <base>/messages, the client's body (anthropicPassthrough)
	UpResponsesNative = "responses-native" // <base>/responses, the client's body (responsesPassthrough)
	UpEmbeddings      = "embeddings"       // <base>/embeddings
	UpCount           = "count"            // <base>/messages/count_tokens, 15 s
	UpNone            = "none"             // answered by the engine itself (Plan.Self)
)

// CountTimeout is countTokens' AbortSignal.timeout(15000): it bounds the whole call, its body included (a variable:
// tests shorten it).
var CountTimeout = 15 * time.Second

// admit is the checks every gateway path makes before it reads the model (PlanChat's first lines, P4b): a loopback Host
// and client, no browser or proxy header, POST, a key, a JSON object body of at most 20 MB. scope "llm" is
// authed(req, 'llm'); "" is chat's authed(req, null), whose scope is checked with the model later.
func (s *State) admit(r *Request, now int64, scope string) (*ipc.GatewayKeyAuth, *Value, string) {
	if !loopbackName(r.Host) || !loopbackPeer(r.Peer) {
		return nil, nil, "access: not a loopback host and client"
	}
	for _, h := range append([]string{"Origin", "Referer", "Sec-Fetch-Site"}, proxyHeaders...) {
		if _, ok := r.Header[h]; ok {
			return nil, nil, "access: a browser or proxy header"
		}
	}
	if r.Method != http.MethodPost {
		return nil, nil, "method"
	}
	secret, ok := RequestSecret(r.Header, r.RawPath, r.Query)
	key, why := s.Keys.Authenticate(secret, ok, now)
	if key == nil {
		return nil, nil, "key: " + why
	}
	if scope != "" && Authorize(key, scope, "", "") != "" {
		return nil, nil, "key: not allowed"
	}
	if r.TooBig || len(r.Body) == 0 {
		return nil, nil, "body: empty or over 20 MB"
	}
	body, err := Parse(r.Body)
	if err != nil || body.Kind != Object {
		return nil, nil, "body: not a JSON object the engine serves"
	}
	return key, body, ""
}

// target is resolveAll's one target for a model the key may use: a route is handed off (P4d Decision 9).
func (s *State) target(model string, key *ipc.GatewayKeyAuth) (*Target, string) {
	for _, rt := range s.Gateway.Routes {
		if rt.Name == model {
			return nil, "model: a route"
		}
	}
	t := Resolve(s.Gateway.Providers, model)
	if t == nil || (key.PrivateOnly && TierOf(t.Provider) != "private") {
		return nil, "model: no usable target"
	}
	return t, ""
}

// credential is the provider's API key ("" for none); OAuth2 and service accounts are P4g's (P4d Decision 17).
func (s *State) credential(t *Target) (string, string) {
	a := t.Provider.Auth
	if a == nil {
		return "", ""
	}
	if a.Type != "key" {
		return "", "provider: OAuth2 or a service account (P4g)"
	}
	if c := s.Secrets[t.Provider.ID]; c != "" {
		return c, ""
	}
	return "", "provider: no credential"
}

// providerHeader is providers.js headers(p): x-api-key and anthropic-version for the Anthropic format, else a Bearer.
func providerHeader(t *Target, cred string) http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "application/json")
	if FormatOf(t.Provider) == "anthropic" {
		if cred != "" {
			h.Set("X-Api-Key", cred)
		}
		h.Set("Anthropic-Version", "2023-06-01")
	} else if cred != "" {
		h.Set("Authorization", "Bearer "+cred)
	}
	return h
}

// addAnthropicHeaders is anthropicHeaders(req) passed as request()'s extra headers: the client's anthropic-version and
// anthropic-beta (a string of at most 1000 characters without CR or LF) after the provider's own.
func addAnthropicHeaders(p *Plan, r *Request) {
	for _, k := range []string{"Anthropic-Version", "Anthropic-Beta"} {
		// Node joins a repeated header of these names with ", " (req.headers).
		if v := strings.Join(r.Header.Values(k), ", "); v != "" && utf16Units(v) <= 1000 && !strings.ContainsAny(v, "\r\n") {
			p.Header.Set(k, v)
		}
	}
}

// utf16Units is a string's JavaScript length.
func utf16Units(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// recovered logs a panic a translation step recovered from, with its value and stack, so a port bug is not silent
// (P4d re-review 3); the call itself ends as JS's catch ends it.
func (s *State) recovered(where string, v any) {
	if s != nil && s.logf != nil {
		s.logf("go engine: a panic in %s was recovered; the call ended as JS's catch ends it: %v\n%s", where, v, debug.Stack())
	}
}

// onPanic is recovered for one place, as a callback.
func (s *State) onPanic(where string) func(any) { return func(v any) { s.recovered(where, v) } }

// tokenSaverOn is lib/gateway/index.js tokenSaverOn.
func (s *State) tokenSaverOn(r *Request) bool {
	return s.Gateway.TokenSaver && !strings.EqualFold(strings.Join(r.Header.Values("X-Zp-Token-Saver"), ", "), "off")
}

// PlanFor plans a request of a path class (gw/front Classify).
func (s *State) PlanFor(class string, r *Request, now int64) Plan {
	switch class {
	case "chat":
		return s.PlanChat(r, now)
	case "messages":
		if r.RawPath == "/v1/messages/count_tokens" {
			return s.PlanCount(r, now)
		}
		if r.RawPath != "/v1/messages" {
			return handoff("path: not a messages call")
		}
		return s.PlanMessages(r, now)
	case "responses":
		if r.RawPath != "/v1/responses" {
			return handoff("path: not a responses call")
		}
		return s.PlanResponses(r, now)
	case "gemini":
		return s.PlanGemini(r, now)
	case "embeddings":
		return s.PlanEmbeddings(r, now)
	case "mcp":
		return s.PlanMCP(r, now)
	}
	return handoff("class: not on the go engine")
}

// SubClass is the sub-class a request of a path class belongs to. A plan the engine would serve says so itself; for a
// hand-off the request is read again, so its shadow line is filed under the sub-class it asked for.
func SubClass(class string, p *Plan, r *Request) string {
	if p != nil && p.Handoff == "" && p.Class != "" {
		return p.Class
	}
	switch class {
	case "chat":
		stream := p != nil && p.Stream
		if (p == nil || p.Handoff != "") && r != nil {
			stream = bodyStreams(r)
		}
		anth := p != nil && p.Upstream == UpAnthropic
		switch {
		case anth && stream:
			return SubChatAnthropicStream
		case anth:
			return SubChatAnthropic
		case stream:
			return SubChatStream
		}
		return SubChat
	case "messages":
		if r != nil && r.RawPath == "/v1/messages/count_tokens" {
			return SubMessagesCount
		}
		if bodyStreams(r) {
			return SubMessagesStream
		}
		return SubMessages
	case "responses":
		if bodyStreams(r) {
			return SubResponsesStream
		}
		return SubResponses
	case "gemini":
		if r != nil {
			switch _, a, _ := geminiPathShape(r.RawPath); a {
			case "countTokens":
				return SubGeminiCount
			case "streamGenerateContent":
				return SubGeminiStream
			}
		}
		return SubGemini
	}
	return class
}

func bodyStreams(r *Request) bool {
	if r == nil {
		return false
	}
	b, err := Parse(r.Body)
	return err == nil && b.Get("stream").Truthy()
}
