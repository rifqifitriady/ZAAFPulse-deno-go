// Package provauth gets the short-lived tokens of providers that do not take a fixed API key (P4g): OAuth2 with the
// client_credentials or refresh_token grant (Azure Entra ID and others) and a Google service account, whose RS256 JWT
// is exchanged for a token (Vertex AI). It is the Go port of lib/gateway/providers.js refreshToken and accessToken,
// which stay the reference until G3; gw/testdata/provauth holds fixtures generated from that JS
// (scripts/fixtures/gen-provauth.js), and fixtures_test.go replays them.
//
// A token is cached in memory until Lead before it expires (the JS TOKEN_LEAD_MS). Concurrent callers share one
// refresh, which runs on its own 20 s timeout: a caller whose context ends stops waiting, the refresh goes on for the
// others. A failed refresh keeps the cached token, and the next call tries again. The secret is read through a Secret
// function at every refresh (the wiring reads the latest secrets.handoff; never the environment), and every error
// message goes through the mask (the handoff's mask set). A redirect from the token endpoint is refused, never
// followed: the request body holds a client secret or a signed assertion.
package provauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Source gives a provider token and the time it expires.
type Source interface {
	Token(ctx context.Context) (string, time.Time, error)
}

// Secret returns the provider's stored credential: the client secret, JSON {"client_secret", "refresh_token"}, or a
// service account's JSON. "" means none is stored.
type Secret func() (string, error)

// Clock returns the current time (nil: time.Now).
type Clock func() time.Time

// Option configures a source.
type Option func(*Cached)

// WithID sets the provider id that prefixes the messages ("<id>: token refresh failed (401)"), as in the JS.
func WithID(id string) Option { return func(c *Cached) { c.id = id } }

// WithMask sets the function every error message goes through before it is returned.
func WithMask(mask func(string) string) Option {
	return func(c *Cached) {
		if mask != nil {
			c.mask = mask
		}
	}
}

// MaskValues replaces each of values in a text with "***", as lib/secrets.js mask does with the secrets it resolved
// (the secrets.handoff mask list). Empty values are skipped.
func MaskValues(values []string) func(string) string {
	vs := make([]string, 0, len(values))
	for _, v := range values {
		if v != "" {
			vs = append(vs, v)
		}
	}
	return func(s string) string {
		for _, v := range vs {
			s = strings.ReplaceAll(s, v, "***")
		}
		return s
	}
}

const (
	// Lead: a token that expires within Lead is refreshed before use (providers.js TOKEN_LEAD_MS).
	Lead = 5 * time.Minute
	// DefaultTimeout bounds one token request (providers.js: 20 s).
	DefaultTimeout = 20 * time.Second
	// maxExpiry caps an expires_in the JS would keep as a huge number or Infinity (about 31 years).
	maxExpiry = time.Duration(1e9) * time.Second
	maxBody   = 1 << 20
)

type field struct{ name, value string }

// formFunc builds a token request: the endpoint and the form fields in order. refresh is the cached refresh token
// ("" when none), now the clock at the start of the refresh.
type formFunc func(refresh string, now time.Time) (endpoint string, fields []field, err error)

// formError is a message of this package; it gets the provider id prefix. Other errors (a Secret's) are returned as
// they are.
type formError string

func (e formError) Error() string { return string(e) }

type token struct {
	access  string
	exp     time.Time
	refresh string
}

type flight struct {
	done chan struct{}
	tok  token
	err  error
}

// Cached is a Source with the token cache and the shared refresh.
type Cached struct {
	id      string
	form    formFunc
	client  *http.Client
	clock   Clock
	mask    func(string) string
	timeout time.Duration

	mu     sync.Mutex
	tok    *token
	flight *flight
}

func newCached(form formFunc, hc *http.Client, clock Clock, opts []Option) *Cached {
	var cp http.Client
	if hc != nil {
		cp = *hc
	}
	cp.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if clock == nil {
		clock = time.Now
	}
	c := &Cached{form: form, client: &cp, clock: clock, mask: func(s string) string { return s }, timeout: DefaultTimeout}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Token returns the cached token while more than Lead is left before it expires, else a new one.
func (c *Cached) Token(ctx context.Context) (string, time.Time, error) { return c.get(ctx, false) }

// Refresh gets a new token even when the cached one is still good (the replay after a 401, the background refresh:
// providers.js accessToken(p, true)). A refresh already running is shared.
func (c *Cached) Refresh(ctx context.Context) (string, time.Time, error) { return c.get(ctx, true) }

func (c *Cached) get(ctx context.Context, force bool) (string, time.Time, error) {
	c.mu.Lock()
	if !force && c.tok != nil && c.tok.exp.Sub(c.clock()) > Lead {
		t := *c.tok
		c.mu.Unlock()
		return t.access, t.exp, nil
	}
	f := c.flight
	if f == nil {
		f = &flight{done: make(chan struct{})}
		c.flight = f
		refresh := ""
		if c.tok != nil {
			refresh = c.tok.refresh
		}
		go c.run(f, refresh)
	}
	c.mu.Unlock()
	select {
	case <-f.done:
		if f.err != nil {
			return "", time.Time{}, f.err
		}
		return f.tok.access, f.tok.exp, nil
	case <-ctx.Done():
		return "", time.Time{}, ctx.Err()
	}
}

func (c *Cached) run(f *flight, refresh string) {
	tok, err := c.fetch(refresh)
	c.mu.Lock()
	if err == nil {
		if tok.refresh == "" && c.tok != nil {
			tok.refresh = c.tok.refresh // an answer without a refresh_token keeps the one we have
		}
		c.tok = &tok
	}
	f.tok, f.err = tok, err
	c.flight = nil
	c.mu.Unlock()
	close(f.done)
}

func (c *Cached) fail(err error) error {
	msg := err.Error()
	var fe formError
	if errors.As(err, &fe) && c.id != "" {
		msg = c.id + ": " + msg
	}
	return errors.New(c.mask(msg))
}

func (c *Cached) fetch(refresh string) (token, error) {
	endpoint, fields, err := c.form(refresh, c.clock())
	if err != nil {
		return token{}, c.fail(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	timedOut := func(err error) error {
		if ctx.Err() == context.DeadlineExceeded {
			return c.fail(formError("token endpoint timed out"))
		}
		return c.fail(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(encodeForm(fields)))
	if err != nil {
		return token{}, c.fail(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := c.client.Do(req)
	if err != nil {
		return token{}, timedOut(err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 && res.StatusCode < 400 {
		return token{}, c.fail(formError(fmt.Sprintf(
			"the token endpoint answered with a redirect (%d); it is not followed", res.StatusCode)))
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxBody))
	if err != nil {
		return token{}, timedOut(err)
	}
	var j map[string]any
	if json.Unmarshal(body, &j) != nil {
		j = nil // not JSON (or not an object): read as {}, as r.json().catch(() => ({}))
	}
	access, _ := j["access_token"].(string)
	if res.StatusCode < 200 || res.StatusCode > 299 || access == "" {
		msg := fmt.Sprintf("token refresh failed (%d", res.StatusCode)
		if e, ok := j["error"].(string); ok && e != "" {
			msg += " " + e
		}
		return token{}, c.fail(formError(msg + ")"))
	}
	secs := jsNumber(j["expires_in"])
	if secs == 0 || math.IsNaN(secs) {
		secs = 3600 // Number(j.expires_in) || 3600
	}
	d := maxExpiry
	switch {
	case secs <= -maxExpiry.Seconds():
		d = -maxExpiry
	case secs < maxExpiry.Seconds():
		d = time.Duration(math.Round(secs * float64(time.Second)))
	}
	rt, _ := j["refresh_token"].(string)
	return token{access: access, exp: c.clock().Add(d), refresh: rt}, nil
}
