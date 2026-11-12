package provauth

import (
	"encoding/json"
	"net/http"
	"time"
)

// OAuth2Config is a provider's auth section (providers.js cleanProvider, type "oauth2").
type OAuth2Config struct {
	TokenURL string
	ClientID string
	Scope    string // "" sends no scope
	Grant    string // "refresh_token", else client_credentials
}

// OAuth2 is the client_credentials or refresh_token grant. The stored secret is the client secret, or JSON
// {"client_secret", "refresh_token"} (for the refresh-token grant). The form fields go in the JS order: grant_type,
// client_id, client_secret (when there is one), scope (when set), refresh_token (refresh grant: the one the last
// answer returned, else the stored one).
func OAuth2(cfg OAuth2Config, secret Secret, hc *http.Client, clock Clock, opts ...Option) *Cached {
	grant := "client_credentials"
	if cfg.Grant == "refresh_token" {
		grant = "refresh_token"
	}
	return newCached(func(cachedRefresh string, _ time.Time) (string, []field, error) {
		raw, err := secret()
		if err != nil {
			return "", nil, err
		}
		if raw == "" {
			return "", nil, formError("no credential stored for oauth2")
		}
		clientSecret, storedRefresh := storedSecret(raw)
		fields := []field{{"grant_type", grant}, {"client_id", cfg.ClientID}}
		if clientSecret != "" {
			fields = append(fields, field{"client_secret", clientSecret})
		}
		if cfg.Scope != "" {
			fields = append(fields, field{"scope", cfg.Scope})
		}
		if grant == "refresh_token" {
			rt := cachedRefresh
			if rt == "" {
				rt = storedRefresh
			}
			if rt == "" {
				return "", nil, formError("no refresh_token in the stored credential")
			}
			fields = append(fields, field{"refresh_token", rt})
		}
		return cfg.TokenURL, fields, nil
	}, hc, clock, opts)
}

// storedSecret reads the stored credential as the JS does: text that is not JSON is the client secret; a JSON object
// gives its string client_secret and refresh_token; other JSON (a number, a string, null) gives neither.
func storedSecret(raw string) (clientSecret, refresh string) {
	var v any
	if json.Unmarshal([]byte(raw), &v) != nil {
		return raw, ""
	}
	if m, ok := v.(map[string]any); ok {
		clientSecret, _ = m["client_secret"].(string)
		refresh, _ = m["refresh_token"].(string)
	}
	return clientSecret, refresh
}
