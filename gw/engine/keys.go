// Package engine is the Go engine of the gateway (P4b, docs/superpowers/specs/2026-11-12-p4-stage1-design.md stream
// A): the route classes the gateway serves itself instead of forwarding them to the control plane. It decides from the
// state the control plane pushes (config.push, secrets.handoff, budget.snapshot) with the rules of the JS gateway,
// each one checked against fixtures generated from the JS code (scripts/fixtures/gen-*.js, gw/testdata/*).
package engine

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"unicode"
	"unicode/utf16"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

// The answers of lib/gateway/keys.js authenticate.
const (
	MsgMalformed = "missing or malformed API key (Authorization: Bearer zp_…)"
	MsgUnknown   = "unknown API key"
	MsgRevoked   = "this API key was revoked"
	MsgExpired   = "this API key has expired"
)

// Keys are the API keys of one config.push, looked up by the SHA-256 of a secret (keys.json stores only that).
type Keys struct {
	list   []ipc.GatewayKeyAuth
	hashes [][]byte
}

// NewKeys keeps the keys in the order config.push lists them (the first key with a hash wins, as in JS).
func NewKeys(list []ipc.GatewayKeyAuth) *Keys {
	k := &Keys{list: list, hashes: make([][]byte, len(list))}
	for i, key := range list {
		k.hashes[i], _ = hex.DecodeString(key.Hash)
	}
	return k
}

// Authenticate is lib/gateway/keys.js authenticate: the key of secret at now (epoch ms), or nil with the 401 message.
// It never writes: lastUsedAt is the control plane's, from the usage lines (lib/gateway/index.js ingestUsage).
func (k *Keys) Authenticate(secret string, ok bool, now int64) (*ipc.GatewayKeyAuth, string) {
	if !ok || !strings.HasPrefix(secret, "zp_") {
		return nil, MsgMalformed
	}
	sum := sha256.Sum256([]byte(secret))
	for i := range k.list {
		if len(k.hashes[i]) == len(sum) && subtle.ConstantTimeCompare(k.hashes[i], sum[:]) == 1 {
			key := &k.list[i]
			if key.RevokedAt != nil && *key.RevokedAt != 0 {
				return nil, MsgRevoked
			}
			if key.ExpiresAt != nil && *key.ExpiresAt != 0 && now > *key.ExpiresAt {
				return nil, MsgExpired
			}
			return key, ""
		}
	}
	return nil, MsgUnknown
}

// Authorize is lib/gateway/keys.js authorize: the refusal for a scope, a model or an agent (each checked when not
// empty), or "".
func Authorize(k *ipc.GatewayKeyAuth, scope, model, agent string) string {
	switch {
	case scope != "" && !contains(k.Scopes, scope):
		return `this key does not have the "` + scope + `" scope`
	case model != "" && !Allows(k.Models, model):
		return `this key may not use model "` + model + `"`
	case agent != "" && !Allows(k.Agents, agent):
		return `this key may not use agent "` + agent + `"`
	}
	return ""
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Allows is lib/gateway/keys.js allows: no patterns allow everything; else a pattern must match the whole value, where
// "*" is any run of characters except a line terminator and the rest is literal, compared case-insensitively the way
// a JavaScript /i regular expression without the u flag does (per UTF-16 code unit; a non-ASCII character never
// matches an ASCII one).
func Allows(patterns []string, value string) bool {
	if len(patterns) == 0 {
		return true
	}
	v := utf16.Encode([]rune(value))
	for _, p := range patterns {
		if globMatch(utf16.Encode([]rune(p)), v) {
			return true
		}
	}
	return false
}

func globMatch(p, v []uint16) bool {
	// Iterative wildcard match with one back-tracking point (the last "*").
	pi, vi, star, mark := 0, 0, -1, 0
	for vi < len(v) {
		switch {
		case pi < len(p) && p[pi] == '*':
			star, mark = pi, vi
			pi++
		case pi < len(p) && canon(p[pi]) == canon(v[vi]):
			pi++
			vi++
		case star >= 0 && !lineTerminator(v[mark]):
			mark++
			pi, vi = star+1, mark
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}

func lineTerminator(u uint16) bool { return u == '\n' || u == '\r' || u == 0x2028 || u == 0x2029 }

// canon is ECMAScript Canonicalize for a non-unicode, ignoreCase pattern: the upper case of a code unit, unless that
// is not one code unit or would turn a non-ASCII unit into an ASCII one.
func canon(u uint16) uint16 {
	if u >= 0xd800 && u <= 0xdfff {
		return u
	}
	r := rune(u)
	up := unicode.ToUpper(r)
	if up > 0xffff || specialUpper[r] || divergentFold[r] {
		return u
	}
	if r >= 128 && up < 128 {
		return u
	}
	return uint16(up)
}

// divergentFold are the units Node 24 upper-cases and Deno 2.9.8 does not (Unicode 17 letters); the engine keeps
// Deno's answer, the v2 runtime's.
var divergentFold = map[rune]bool{0xa7cf: true, 0xa7d3: true, 0xa7d5: true}

// specialUpper are the BMP code units whose JavaScript toUpperCase is more than one unit (SpecialCasing.txt: the 102 the
// keys fixture lists through gen-keys.js "canonicalize"), so Canonicalize leaves them as they are, though Go's simple
// mapping folds some of them (U+1F80 to U+1F88, P4b review I3).
var specialUpper = map[rune]bool{
	0xdf: true, 0x149: true, 0x1f0: true, 0x390: true, 0x3b0: true, 0x587: true, 0x1e96: true, 0x1e97: true,
	0x1e98: true, 0x1e99: true, 0x1e9a: true, 0x1f50: true, 0x1f52: true, 0x1f54: true, 0x1f56: true,
	0x1f80: true, 0x1f81: true, 0x1f82: true, 0x1f83: true, 0x1f84: true, 0x1f85: true, 0x1f86: true,
	0x1f87: true, 0x1f88: true, 0x1f89: true, 0x1f8a: true, 0x1f8b: true, 0x1f8c: true, 0x1f8d: true,
	0x1f8e: true, 0x1f8f: true, 0x1f90: true, 0x1f91: true, 0x1f92: true, 0x1f93: true, 0x1f94: true,
	0x1f95: true, 0x1f96: true, 0x1f97: true, 0x1f98: true, 0x1f99: true, 0x1f9a: true, 0x1f9b: true,
	0x1f9c: true, 0x1f9d: true, 0x1f9e: true, 0x1f9f: true, 0x1fa0: true, 0x1fa1: true, 0x1fa2: true,
	0x1fa3: true, 0x1fa4: true, 0x1fa5: true, 0x1fa6: true, 0x1fa7: true, 0x1fa8: true, 0x1fa9: true,
	0x1faa: true, 0x1fab: true, 0x1fac: true, 0x1fad: true, 0x1fae: true, 0x1faf: true, 0x1fb2: true,
	0x1fb3: true, 0x1fb4: true, 0x1fb6: true, 0x1fb7: true, 0x1fbc: true, 0x1fc2: true, 0x1fc3: true,
	0x1fc4: true, 0x1fc6: true, 0x1fc7: true, 0x1fcc: true, 0x1fd2: true, 0x1fd3: true, 0x1fd6: true,
	0x1fd7: true, 0x1fe2: true, 0x1fe3: true, 0x1fe4: true, 0x1fe6: true, 0x1fe7: true, 0x1ff2: true,
	0x1ff3: true, 0x1ff4: true, 0x1ff6: true, 0x1ff7: true, 0x1ffc: true, 0xfb00: true, 0xfb01: true,
	0xfb02: true, 0xfb03: true, 0xfb04: true, 0xfb05: true, 0xfb06: true, 0xfb13: true, 0xfb14: true,
	0xfb15: true, 0xfb16: true, 0xfb17: true,
}

// RequestSecret is lib/gateway/keys.js requestSecret over Go's view of a request: the header values as Node joins
// them (", " between repeats; Authorization keeps its first) and the raw path and query. ok is false when there is none.
func RequestSecret(h http.Header, rawPath, rawQuery string) (string, bool) {
	if a := h.Values("Authorization"); len(a) > 0 && strings.HasPrefix(a[0], "Bearer ") {
		return strings.TrimFunc(a[0][7:], jsSpace), true
	}
	for _, name := range []string{"X-Api-Key", "X-Goog-Api-Key"} {
		if v := strings.Join(h.Values(name), ", "); v != "" {
			return v, true
		}
	}
	if strings.HasPrefix(rawPath, "/v1beta/") {
		if q, err := url.ParseQuery(rawQuery); err == nil || q != nil {
			if v := q.Get("key"); v != "" {
				return v, true
			}
		}
	}
	return "", false
}

// jsSpace is what String.prototype.trim removes: WhiteSpace and LineTerminator.
func jsSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// RateLimiter is lib/gateway/keys.js rateLimit: a sliding one-minute window per key, in this process only.
type RateLimiter struct {
	mu      sync.Mutex
	windows map[string][]int64
}

func NewRateLimiter() *RateLimiter { return &RateLimiter{windows: map[string][]int64{}} }

// Take counts a request of key id at now (epoch ms) and returns 0, or the seconds to wait when rpm (0: 60) requests
// were made in the last minute; a refused request is not counted.
func (r *RateLimiter) Take(id string, rpm, now int64) int64 { return r.take(id, rpm, now, true) }

// Wait is Take without counting: what the shadow compare asks.
func (r *RateLimiter) Wait(id string, rpm, now int64) int64 { return r.take(id, rpm, now, false) }

func (r *RateLimiter) take(id string, rpm, now int64, count bool) int64 {
	if rpm <= 0 {
		rpm = 60
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var w []int64
	for _, t := range r.windows[id] {
		if now-t < 60000 {
			w = append(w, t)
		}
	}
	if int64(len(w)) >= rpm {
		r.windows[id] = w
		return (60000 - (now - w[0]) + 999) / 1000
	}
	if count {
		w = append(w, now)
	}
	r.windows[id] = w
	return 0
}
