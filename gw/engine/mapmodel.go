package engine

// Model names in the translated formats (P4d): lib/gateway/index.js mapModel up to its modelMap step, and the Gemini
// route's model, decoded from the path as decodeURIComponent does.

import (
	"strings"
	"unicode/utf8"
)

// MapModel is mapModel (P4d Decision 8): the name when it is a route or resolves, else the first modelMap entry whose
// match allows it. ok false: the model cache or the defaults would decide; the caller hands the request off.
func (s *State) MapModel(name string) (string, bool) {
	if name != "" {
		for _, rt := range s.Gateway.Routes {
			if rt.Name == name {
				return name, true
			}
		}
		if Resolve(s.Gateway.Providers, name) != nil {
			return name, true
		}
	}
	for _, m := range s.Gateway.ModelMap {
		if m.Match != "" && m.To != "" && Allows([]string{m.Match}, name) {
			return m.To, true
		}
	}
	return "", false
}

// decodeURIComponent is the JS function on a path segment: every %XX decoded and the bytes valid UTF-8 (no surrogate,
// no overlong form). ok false where JS throws a URIError.
func decodeURIComponent(s string) (string, bool) {
	if !strings.Contains(s, "%") {
		return s, true
	}
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '%' {
			b = append(b, s[i])
			continue
		}
		if i+2 >= len(s) || !isHex(s[i+1]) || !isHex(s[i+2]) {
			return "", false
		}
		b = append(b, unhex(s[i+1])<<4|unhex(s[i+2]))
		i += 2
	}
	if !utf8.Valid(b) {
		return "", false
	}
	return string(b), true
}

func isHex(c byte) bool { return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }

func unhex(c byte) byte {
	switch {
	case c <= '9':
		return c - '0'
	case c >= 'a':
		return c - 'a' + 10
	}
	return c - 'A' + 10
}

var geminiActions = []string{"generateContent", "streamGenerateContent", "countTokens"}

// geminiPathShape is the route's /^\/v1beta\/models\/(.+):(generateContent|streamGenerateContent|countTokens)$/: the
// greedy (.+) makes the action the text after the last ':'. The model is not decoded.
func geminiPathShape(raw string) (model, action string, ok bool) {
	const pre = "/v1beta/models/"
	if !strings.HasPrefix(raw, pre) {
		return "", "", false
	}
	rest := raw[len(pre):]
	for _, a := range geminiActions {
		if strings.HasSuffix(rest, ":"+a) && len(rest) > len(a)+1 {
			return rest[:len(rest)-len(a)-1], a, true
		}
	}
	return "", "", false
}

// geminiPath is geminiPathShape with the model decoded; ok false also when it does not decode.
func geminiPath(raw string) (model, action string, ok bool) {
	m, a, ok := geminiPathShape(raw)
	if !ok {
		return "", "", false
	}
	m, ok = decodeURIComponent(m)
	return m, a, ok
}
