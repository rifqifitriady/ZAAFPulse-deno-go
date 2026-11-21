package engine

// Routes (P4h spec Decision 6, design D1): the exact port of lib/gateway/providers.js's routing functions. resetIn and
// the part of readQuota a cooldown reads (JS Number on the header values, resetAt), noteResponse's cooldown arm,
// coolingDown, and resolveAll's rotation and order. RouteState holds what routing reads (the round-robin counters and
// the cooldowns); the quota records stay in JS (Decision 5). gw/testdata/router/fn-*.json (scripts/fixtures/
// gen-router.js) proves each against the real JS.

import (
	"math"
	"math/big"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

// CooldownMaxMs is providers.js COOLDOWN_MAX_MS: the longest a provider cools down.
const CooldownMaxMs = 5 * 60 * 1000

var (
	resetDigits   = regexp.MustCompile(`^\d+(\.\d+)?$`)
	resetDateLike = regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)
	resetPart     = regexp.MustCompile(`(\d+(?:\.\d+)?)(ms|h|m|s)`)
	resetUnit     = map[string]float64{"ms": 1, "s": 1000, "m": 60000, "h": 3600000}
)

// ResetIn is providers.js resetIn: seconds, a date-time (ms until it, at least 0), or a sum of "6m0s"-style parts, in
// milliseconds from now. present=false is null or undefined; ok=false is null.
func ResetIn(v string, present bool, now int64) (ms float64, ok bool) {
	if !present || v == "" {
		return 0, false
	}
	s := jsTrim(v)
	if resetDigits.MatchString(s) {
		return jsToNumber(s) * 1000, true
	}
	if t, ok := jsDateParse(s); ok && resetDateLike.MatchString(s) {
		return math.Max(0, t-float64(now)), true
	}
	found := false
	for _, m := range resetPart.FindAllStringSubmatch(s, -1) {
		found = true
		ms += jsToNumber(m[1]) * resetUnit[m[2]]
	}
	return ms, found
}

// quotaKind is one kind ("requests" or "tokens") of providers.js readQuota; a Has flag false is null.
type quotaKind struct {
	Limit, Remaining, ResetAt        float64
	HasLimit, HasRemaining, HasReset bool
}

// quotaReading is providers.js readQuota's answer; nil is null, a nil kind is null.
type quotaReading struct{ Requests, Tokens *quotaKind }

// headerGet is Headers.get: the values of a name joined with ", ", present when the answer carried the name at all.
func headerGet(h http.Header, name string) (string, bool) {
	vs := h.Values(name)
	if len(vs) == 0 {
		return "", false
	}
	return strings.Join(vs, ", "), true
}

// readQuota is providers.js readQuota: the OpenAI-style headers first, else Anthropic's, per kind.
func readQuota(h http.Header, now float64) *quotaReading {
	pick := func(kind string) *quotaKind {
		get := func(x, a string) (string, bool) {
			if v, ok := headerGet(h, "x-ratelimit-"+x+"-"+kind); ok {
				return v, true
			}
			return headerGet(h, "anthropic-ratelimit-"+kind+"-"+a)
		}
		limit, hasLimit := get("limit", "limit")
		remaining, hasRemaining := get("remaining", "remaining")
		reset, hasReset := get("reset", "reset")
		if !hasLimit && !hasRemaining {
			return nil
		}
		k := &quotaKind{HasLimit: hasLimit, HasRemaining: hasRemaining}
		if hasLimit {
			k.Limit = jsToNumber(limit)
		}
		if hasRemaining {
			k.Remaining = jsToNumber(remaining)
		}
		if r, ok := ResetIn(reset, hasReset, int64(now)); ok {
			k.ResetAt, k.HasReset = now+r, true
		}
		return k
	}
	q := &quotaReading{Requests: pick("requests"), Tokens: pick("tokens")}
	if q.Requests == nil && q.Tokens == nil {
		return nil
	}
	return q
}

// noteHeaderNames are the headers noteResponse reads: readQuota's twelve and retry-after.
var noteHeaderNames = []string{
	"x-ratelimit-limit-requests", "x-ratelimit-remaining-requests", "x-ratelimit-reset-requests",
	"x-ratelimit-limit-tokens", "x-ratelimit-remaining-tokens", "x-ratelimit-reset-tokens",
	"anthropic-ratelimit-requests-limit", "anthropic-ratelimit-requests-remaining", "anthropic-ratelimit-requests-reset",
	"anthropic-ratelimit-tokens-limit", "anthropic-ratelimit-tokens-remaining", "anthropic-ratelimit-tokens-reset",
	"retry-after",
}

// NoteHeaders is the headers of a provider.note (spec Decision 4): the names noteResponse reads, lower case, each
// present one's values joined with ", " as Headers.get joins them.
func NoteHeaders(h http.Header) map[string]string {
	out := map[string]string{}
	for _, k := range noteHeaderNames {
		if v, ok := headerGet(h, k); ok {
			out[k] = v
		}
	}
	return out
}

// Cooldown is one provider's entry of providers.js's cooldown map. Until is a float: a fractional retry-after or reset
// ("1.0005", "12.345678s") gives a fractional time in JS too.
type Cooldown struct {
	Until   float64
	Strikes int
	Reason  string
}

// RouteState is the routing state of one engine (design D1): providers.js's rr and cooldown maps. One mutex guards
// both. send, when set, tells the other engine of every local note and take (never of a remote one, spec Decision 3).
//
// send runs with the mutex held, so the messages leave in the order the state changed: strikes and rotations are
// order-sensitive, and two local notes sent after the unlock could reach the other engine swapped. send must therefore
// not block and must not call back into the RouteState: the gateway's send is an enqueue on a buffered channel that
// drops a message when the queue is full (gw/cmd/zaafpulse-gw, as ratelimit.take).
type RouteState struct {
	mu   sync.Mutex
	rr   map[string]int
	cool map[string]Cooldown
	send func(typ string, body any)
}

// NewRouteState makes an empty state; send may be nil.
func NewRouteState(send func(typ string, body any)) *RouteState {
	return &RouteState{rr: map[string]int{}, cool: map[string]Cooldown{}, send: send}
}

// Note is noteResponse's cooldown arm for one provider answer read at `at` (ms): a 429, 503 or 529 cools the provider
// down for retry-after, else the reset of a kind with nothing remaining, else 1 s doubled per strike; at most 5
// minutes, at least 1 s. A 2xx clears the entry. A local note (remote false) is sent on.
func (r *RouteState) Note(providerID string, status int, h http.Header, at int64, remote bool) {
	now := float64(at)
	r.mu.Lock()
	switch {
	case status == 429 || status == 503 || status == 529:
		c := r.cool[providerID]
		q := readQuota(h, now)
		retryV, hasRetry := headerGet(h, "retry-after")
		retry, hasRetryMs := ResetIn(retryV, hasRetry, at)
		var resetAt float64
		if q != nil {
			for _, k := range []*quotaKind{q.Requests, q.Tokens} {
				// x && x.remaining === 0 && x.resetAt: -0 is 0; a resetAt of 0 or NaN is falsy.
				if k != nil && k.HasRemaining && k.Remaining == 0 && k.HasReset && k.ResetAt != 0 && !math.IsNaN(k.ResetAt) {
					resetAt = k.ResetAt
					break
				}
			}
		}
		var wait float64
		switch {
		case hasRetryMs:
			wait = retry
		case resetAt != 0:
			wait = resetAt - now
		default:
			wait = 1000 * math.Pow(2, float64(c.Strikes))
		}
		wait = math.Min(CooldownMaxMs, wait)
		r.cool[providerID] = Cooldown{Until: now + math.Max(1000, wait), Strikes: c.Strikes + 1, Reason: "HTTP " + strconv.Itoa(status)}
	case status >= 200 && status <= 299:
		delete(r.cool, providerID)
	}
	if r.send != nil && !remote {
		r.send(ipc.TypeProviderNote, ipc.ProviderNote{ProviderID: providerID, Status: status, At: at, Headers: NoteHeaders(h)})
	}
	r.mu.Unlock()
}

// Cooling is providers.js coolingDown as a test: the provider has an entry whose until is after now.
func (r *RouteState) Cooling(providerID string, now int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.cool[providerID]
	return ok && c.Until > float64(now)
}

// Take is resolveAll's rotation for a round-robin route of count raw targets: n = (rr || 0) % count, rr = n + 1; it
// returns n. JS rotates only a round-robin route of more than one target: with count < 2 nothing changes and n is 0.
// A local take (remote false) is sent on; a remote one is applied with this engine's count (spec Decision 4).
func (r *RouteState) Take(name string, count int, at int64, remote bool) int {
	if count < 2 {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.take(name, count, at, remote)
}

// take is Take with the mutex held.
func (r *RouteState) take(name string, count int, at int64, remote bool) int {
	n := r.rr[name] % count
	r.rr[name] = n + 1
	if r.send != nil && !remote {
		r.send(ipc.TypeRouteTake, ipc.RouteTake{Name: name, At: at})
	}
	return n
}

// TakeOrder is resolveAll for a route call (spec Decision 6): takes local takes of a round-robin route of count raw
// targets (the last one's n rotates), then Order with the cooling test at `at`, all under one hold of the mutex, so no
// note or take lands between the take and the cooling test.
func (r *RouteState) TakeOrder(name string, count, takes int, roundRobin bool, legs []*Plan, at int64) []*Plan {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	if roundRobin && count >= 2 {
		for range takes {
			n = r.take(name, count, at, false)
		}
	}
	return Order(legs, n, roundRobin, func(id string) bool {
		c, ok := r.cool[id]
		return ok && c.Until > float64(at)
	})
}

// SetSend replaces where local notes and takes go (nil: nowhere); see RouteState for what send may do.
func (r *RouteState) SetSend(send func(typ string, body any)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.send = send
}

// Seed replaces the state with the control plane's (route.state: a new session, before ready; spec Decision 3). Every
// cooldown entry comes, expired ones too: their strikes set the next back-off until a 2xx clears them (Decision 4).
func (r *RouteState) Seed(b ipc.RouteState) {
	rr := make(map[string]int, len(b.RR))
	for _, x := range b.RR {
		rr[x.Name] = x.N
	}
	cool := make(map[string]Cooldown, len(b.Cooldown))
	for _, c := range b.Cooldown {
		cool[c.ProviderID] = Cooldown{Until: c.Until, Strikes: c.Strikes, Reason: c.Reason}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rr, r.cool = rr, cool
}

// Snapshot is the whole state as route.state carries it (every cooldown entry, expired ones too), sorted by name and
// provider id (tests).
func (r *RouteState) Snapshot() ipc.RouteState {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := ipc.RouteState{RR: []ipc.RouteRR{}, Cooldown: []ipc.RouteCooldown{}}
	for name, n := range r.rr {
		out.RR = append(out.RR, ipc.RouteRR{Name: name, N: n})
	}
	for id, c := range r.cool {
		out.Cooldown = append(out.Cooldown, ipc.RouteCooldown{ProviderID: id, Until: c.Until, Strikes: c.Strikes, Reason: c.Reason})
	}
	slices.SortFunc(out.RR, func(a, b ipc.RouteRR) int { return strings.Compare(a.Name, b.Name) })
	slices.SortFunc(out.Cooldown, func(a, b ipc.RouteCooldown) int { return strings.Compare(a.ProviderID, b.ProviderID) })
	return out
}

// Peek is the n the next Take would return, without taking (the shadow's own view, spec Decision 11).
func (r *RouteState) Peek(name string, count int) int {
	if count < 2 {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rr[name] % count
}

// Order is resolveAll after the take: the raw legs (one per route target, nil for one that does not resolve or that a
// private-only key may not use) rotated by n for a round-robin route of more than one target, the nil legs dropped,
// then the legs whose provider is cooling down moved to the back, both parts in order. legs is not changed.
func Order(legs []*Plan, n int, roundRobin bool, cooling func(id string) bool) []*Plan {
	rot := legs
	if roundRobin && len(legs) > 1 {
		n %= len(legs)
		rot = append(append(make([]*Plan, 0, len(legs)), legs[n:]...), legs[:n]...)
	}
	var ready, cool []*Plan
	for _, l := range rot {
		if l == nil {
			continue
		}
		if cooling(l.Target.Provider.ID) {
			cool = append(cool, l)
		} else {
			ready = append(ready, l)
		}
	}
	return append(append(make([]*Plan, 0, len(ready)+len(cool)), ready...), cool...)
}

var (
	jsDecimal = regexp.MustCompile(`^[+-]?(Infinity|\d+\.?\d*([eE][+-]?\d+)?|\.\d+([eE][+-]?\d+)?)$`)
	jsRadix   = regexp.MustCompile(`^0([xX][0-9a-fA-F]+|[oO][0-7]+|[bB][01]+)$`)
)

// jsToNumber is JavaScript's Number(string) (StringToNumber): trimmed, "" is 0, a decimal literal (an exponent and
// Infinity included, with a sign), an unsigned 0x, 0o or 0b integer, else NaN.
func jsToNumber(v string) float64 {
	s := jsTrim(v)
	if s == "" {
		return 0
	}
	if jsDecimal.MatchString(s) {
		if strings.HasSuffix(s, "Infinity") {
			if s[0] == '-' {
				return math.Inf(-1)
			}
			return math.Inf(1)
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil && !isRange(err) {
			return math.NaN()
		}
		return f
	}
	if jsRadix.MatchString(s) {
		base := map[byte]int{'x': 16, 'o': 8, 'b': 2}[s[1]|0x20]
		i, ok := new(big.Int).SetString(s[2:], base)
		if !ok {
			return math.NaN()
		}
		f, _ := new(big.Float).SetInt(i).Float64() // round to nearest, ties to even
		return f
	}
	return math.NaN()
}

func isRange(err error) bool {
	ne, ok := err.(*strconv.NumError)
	return ok && ne.Err == strconv.ErrRange
}

var (
	// The ECMAScript date-time string format (a date with a day; the year may be expanded), lower-case t and z too.
	isoDate = regexp.MustCompile(`^(\d{4}|[+-]\d{6})-(\d{2})-(\d{2})(?:[Tt](\d{2}):(\d{2})(?::(\d{2})(?:\.(\d+))?)?([Zz]|[+-]\d{2}:\d{2})?)?$`)
	// V8's legacy parser on "YYYY-MM-DD HH:mm[:ss[.fff]]" with an optional Z or ±HH:mm (spec Decision 6).
	spaceDate = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2}) (\d{2}):(\d{2})(?::(\d{2})(?:\.(\d+))?)?([Zz]|[+-]\d{2}:\d{2})?$`)
)

// maxTime is ECMAScript TimeClip's bound: 8.64e15 ms either side of the epoch.
const maxTime = 8.64e15

// jsDateParse is V8's Date.parse for the forms of spec Decision 6: the ECMAScript date-time string format (a date only
// is UTC, a date-time without an offset local time) and V8's "YYYY-MM-DD HH:mm[:ss]" variant for the years 0100-9999.
// ok=false is NaN, also for every other string (a known difference where V8's legacy parser reads more, spec Decision
// 15a: other zones and spacings, a +24:00 offset, the years 0000-0099 of the variant).
func jsDateParse(s string) (float64, bool) {
	m := isoDate.FindStringSubmatch(s)
	space := false
	if m == nil {
		if m = spaceDate.FindStringSubmatch(s); m == nil {
			return 0, false
		}
		space = true
	}
	if m[1] == "-000000" {
		return 0, false
	}
	year, _ := strconv.Atoi(m[1])
	if space && year < 100 {
		return 0, false // V8's legacy parser reads 0000-0099 as a guessed day, month and two-digit year: not ported
	}
	month, _ := strconv.Atoi(m[2])
	day, _ := strconv.Atoi(m[3])
	if month < 1 || month > 12 || day < 1 || day > 31 {
		return 0, false
	}
	timed := m[4] != ""
	var hour, minute, sec, ms int
	if timed {
		hour, _ = strconv.Atoi(m[4])
		minute, _ = strconv.Atoi(m[5])
		if m[6] != "" {
			sec, _ = strconv.Atoi(m[6])
		}
		if f := m[7]; f != "" {
			ms, _ = strconv.Atoi((f + "00")[:3])
			if hour == 24 && !space && strings.Trim(f, "0") != "" {
				return 0, false // the ISO parser refuses any fraction at 24:00; V8's legacy one only a whole millisecond
			}
		}
		if hour > 24 || minute > 59 || sec > 59 || (hour == 24 && (minute != 0 || sec != 0 || ms != 0)) {
			return 0, false
		}
	}
	loc := time.UTC
	offset := 0
	switch z := m[8]; {
	case z == "Z" || z == "z":
	case z != "":
		oh, _ := strconv.Atoi(z[1:3])
		om, _ := strconv.Atoi(z[4:6])
		if oh > 23 || om > 59 {
			return 0, false
		}
		offset = (oh*60 + om) * 60000
		if z[0] == '-' {
			offset = -offset
		}
	case timed:
		loc = time.Local // a date-time without an offset is local time
	}
	t := float64(time.Date(year, time.Month(month), day, hour, minute, sec, ms*int(time.Millisecond), loc).UnixMilli()) -
		float64(offset)
	if math.Abs(t) > maxTime {
		return 0, false
	}
	return t, true
}
