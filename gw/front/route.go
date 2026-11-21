package front

import "strings"

// Classes are the gateway's route classes (P4a spec D2). The engines map of config.push picks js or go for each one;
// a path that is in no class (the dashboard, /api/*, /v1/traces) always goes to the control plane.
var Classes = []string{"chat", "messages", "responses", "embeddings", "models", "gemini", "mcp", "a2a", "skills", "agents"}

// Classify returns the route class of a request path, or "" when it is not a gateway route. The prefixes are those of
// lib/gateway/index.js handle(); /v1/traces is the control plane's own OTLP return channel (server.js).
//
// p is the raw path as the client sent it (RawPath of the request target, before "?"), never Go's decoded form: the
// control plane routes on the WHATWG pathname of the same text (new URL(req.url, base).pathname), which keeps percent
// escapes and resolves dot segments and backslashes (P4a final review T4-d, P4b entry condition 2). A path the WHATWG
// parser would change (Canonical false) is in no class, so the control plane routes it, exactly as in js mode.
func Classify(p string) string {
	if !Canonical(p) {
		return ""
	}
	switch {
	case p == "/v1/chat/completions":
		return "chat"
	case p == "/v1/messages" || strings.HasPrefix(p, "/v1/messages/"):
		return "messages"
	case p == "/v1/responses" || strings.HasPrefix(p, "/v1/responses/"):
		return "responses"
	case p == "/v1/embeddings":
		return "embeddings"
	case p == "/v1/models" || strings.HasPrefix(p, "/v1/models/"):
		return "models"
	case strings.HasPrefix(p, "/v1beta/"):
		return "gemini"
	case p == "/mcp":
		return "mcp"
	case p == "/a2a" || p == "/.well-known/agent-card.json" || p == "/.well-known/agent.json":
		return "a2a"
	case p == "/skills" || strings.HasPrefix(p, "/skills/"):
		return "skills"
	case p == "/v1/traces":
		return ""
	case strings.HasPrefix(p, "/v1/"):
		return "agents"
	}
	return ""
}

// Canonical reports whether the WHATWG URL parser leaves path p as it is: it starts with one "/" (not "//", which
// would be read as a host), and has no backslash, dot segment, malformed percent escape or character the parser
// escapes. A well-formed %XX is kept as it is (P4d Decision 23), but a segment that is a dot segment once %2e reads as
// "." is resolved by the parser. Only such a path can be classified without the parser: its pathname is then p itself
// (gw/testdata/route fixtures).
func Canonical(p string) bool {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") {
		return false
	}
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("-._~!$&'()*+,;=:@/", c) >= 0:
		case c == '%' && i+2 < len(p) && isHex(p[i+1]) && isHex(p[i+2]):
			i += 2
		default:
			return false
		}
	}
	for _, seg := range strings.Split(p, "/") {
		if d := strings.ReplaceAll(strings.ToLower(seg), "%2e", "."); d == "." || d == ".." {
			return false
		}
	}
	return true
}

func isHex(c byte) bool { return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }

// RawPath is the path of a request target as the client sent it: the text before "?" (and "#").
func RawPath(requestURI string) string {
	if i := strings.IndexAny(requestURI, "?#"); i >= 0 {
		return requestURI[:i]
	}
	return requestURI
}
