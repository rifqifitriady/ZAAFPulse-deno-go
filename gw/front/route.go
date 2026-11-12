package front

import "strings"

// Classes are the gateway's route classes (P4a spec D2). The engines map of config.push picks js or go for each one;
// a path that is in no class (the dashboard, /api/*, /v1/traces) always goes to the control plane.
var Classes = []string{"chat", "messages", "responses", "embeddings", "models", "gemini", "mcp", "a2a", "skills", "agents"}

// Classify returns the route class of a request path, or "" when it is not a gateway route. The prefixes are those of
// lib/gateway/index.js handle(); /v1/traces is the control plane's own OTLP return channel (server.js).
func Classify(p string) string {
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
