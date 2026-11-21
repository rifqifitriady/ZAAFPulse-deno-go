package engine

// Provider auth on the go engine (P4g S10, spec Decisions 8 to 13): a provider whose auth.type is oauth2 or google-sa
// is called with a short-lived token the engine gets itself through gw/provauth, as lib/gateway/providers.js headers,
// accessToken and refreshToken do. The registry holds one token source per provider, outside the state generations
// (Decision 10); a plan carries the source, never the token (Decision 11): Upstream.Start gets the token, and replays
// the call once after a 401 (Decision 12). A 60 s ticker refreshes tokens close to expiry (Decision 13).

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/provauth"
)

// AuthSwitch is the provider-auth switch (spec Decision 14, as the token saver's SaverSwitch): not a path class, read
// for every call to a token-auth target. Such a call is on go only when its sub-class and this switch are on go, and is
// compared when this switch is shadowed.
const AuthSwitch = "provider-auth"

const (
	// TickEvery is the background refresh's period (providers.js setInterval 60000).
	TickEvery = 60 * time.Second
	// TickLead: the background refresh renews a token that expires within it (providers.js TOKEN_LEAD_MS * 2).
	TickLead = 2 * provauth.Lead
)

// Tokens is the engine's token registry: a source per provider id, kept while the provider's auth settings stay the
// same.
type Tokens struct {
	client *http.Client
	state  func() *State
	clock  provauth.Clock

	mu      sync.Mutex
	sources map[string]tokenEntry
}

type tokenEntry struct {
	key string // authKey: a change makes a new source
	src *provauth.Cached
}

// NewTokens is a registry whose sources call token endpoints through client (the provider client: proxy env, trusted
// roots; provauth copies it and refuses redirects) and read the secret and the mask set from state() (the newest
// generation) at each refresh. clock nil: time.Now.
func NewTokens(client *http.Client, state func() *State, clock provauth.Clock) *Tokens {
	if clock == nil {
		clock = time.Now
	}
	return &Tokens{client: client, state: state, clock: clock, sources: map[string]tokenEntry{}}
}

// tokenAuth is whether a provider is called with a token the engine gets: oauth2 with the client_credentials grant, or
// google-sa. A refresh_token grant provider is not (refreshGrant).
func tokenAuth(p *ipc.ProviderConfig) bool {
	return p != nil && p.Auth != nil && (p.Auth.Type == "oauth2" || p.Auth.Type == "google-sa") && !refreshGrant(p)
}

// refreshGrant is whether a provider uses the OAuth2 refresh_token grant. Its calls stay on the control plane (spec
// Decision 19): a rotating IdP issues a new refresh token at each refresh, and two engines each keeping their own
// rotated copy would present a refresh token the IdP already replaced, which its reuse detection answers by revoking
// the whole token family. The registry never makes a source for it.
func refreshGrant(p *ipc.ProviderConfig) bool {
	return p != nil && p.Auth != nil && p.Auth.Type == "oauth2" && strOf(p.Auth.Grant) == "refresh_token"
}

// HandRefreshGrant is the hand-off reason of a call to a refresh_token grant provider.
const HandRefreshGrant = "provider: refresh-token grant stays on the control plane (P4g Decision 19)"

func strOf(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// authKey is what a source depends on besides the secret: the provider id and its auth settings.
func authKey(p *ipc.ProviderConfig) string {
	a := p.Auth
	return p.ID + "\x00" + a.Type + "\x00" + strOf(a.TokenURL) + "\x00" + strOf(a.ClientID) + "\x00" + strOf(a.Scope) +
		"\x00" + strOf(a.Grant)
}

// Source is the token source of a provider: the same one while its id and auth settings stay the same, a new one
// after a change (spec known difference 3). nil for a provider that is not token-auth, or a nil registry.
func (t *Tokens) Source(p *ipc.ProviderConfig) *provauth.Cached {
	if t == nil || !tokenAuth(p) {
		return nil
	}
	key := authKey(p)
	t.mu.Lock()
	defer t.mu.Unlock()
	if e, ok := t.sources[p.ID]; ok && e.key == key {
		return e.src
	}
	src := t.newSource(p)
	t.sources[p.ID] = tokenEntry{key: key, src: src}
	return src
}

func (t *Tokens) newSource(p *ipc.ProviderConfig) *provauth.Cached {
	id := p.ID
	secret := func() (string, error) {
		if s := t.state(); s != nil {
			return s.Secrets[id], nil
		}
		return "", nil
	}
	mask := func(text string) string {
		if s := t.state(); s != nil {
			return s.mask(text)
		}
		return text
	}
	opts := []provauth.Option{provauth.WithID(id), provauth.WithMask(mask)}
	a := p.Auth
	if a.Type == "google-sa" {
		return provauth.GoogleServiceAccount(secret, strOf(a.Scope), t.client, t.clock, opts...)
	}
	return provauth.OAuth2(provauth.OAuth2Config{TokenURL: strOf(a.TokenURL), ClientID: strOf(a.ClientID),
		Scope: strOf(a.Scope), Grant: strOf(a.Grant)}, secret, t.client, t.clock, opts...)
}

// Tick is one background refresh pass (providers.js's 60 s timer): every source of a token-auth provider that is
// usable in the newest state, has a cached token, and expires within TickLead is refreshed, all at once; Tick returns
// when they are done (each is bounded by provauth's own timeout). A failure keeps the cached token, as in JS. Sources
// whose provider is gone or whose auth settings changed are dropped.
func (t *Tokens) Tick(now time.Time) {
	s := t.state()
	if s == nil {
		return
	}
	live := map[string]string{}
	var due []*provauth.Cached
	t.mu.Lock()
	for i := range s.Gateway.Providers {
		p := &s.Gateway.Providers[i]
		if !tokenAuth(p) {
			continue
		}
		key := authKey(p)
		if _, seen := live[p.ID]; !seen {
			live[p.ID] = key
		}
		e, ok := t.sources[p.ID]
		if !ok || e.key != key || !Usable(p) {
			continue
		}
		if exp, ok := e.src.Expiry(); ok && exp.Sub(now) < TickLead {
			due = append(due, e.src)
		}
	}
	for id, e := range t.sources {
		if live[id] != e.key {
			delete(t.sources, id)
		}
	}
	t.mu.Unlock()
	var wg sync.WaitGroup
	for _, src := range due {
		wg.Go(func() { _, _, _ = src.Refresh(context.Background()) })
	}
	wg.Wait()
}

// Run calls Tick every TickEvery until stop is closed.
func (t *Tokens) Run(stop <-chan struct{}) {
	tk := time.NewTicker(TickEvery)
	defer tk.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tk.C:
			t.Tick(t.clock())
		}
	}
}

// TokenAuth is whether the plan's target is called with a token the engine gets (the provider-auth switch applies).
func (p *Plan) TokenAuth() bool { return p.auth != nil }

// setToken puts the token on a provider request's headers: x-api-key for the Anthropic format, else a Bearer
// (providers.js headers).
func setToken(h http.Header, t *Target, tok string) {
	if tok == "" {
		return
	}
	if FormatOf(t.Provider) == "anthropic" {
		h.Set("X-Api-Key", tok)
	} else {
		h.Set("Authorization", "Bearer "+tok)
	}
}
