# P4h: routes (`router/*`), cooldowns and provider quota readings on the Go engine: design

Date: 2026-11-21. Repo `ZAAFPulse-deno-go` (public). Branch `migrate/p4h-router`, cut from `migrate/deno-go` at
`1fab143` ("Merge pull request #14 from rifqifitriady/migrate/p4g-mcp-auth"). P4a–P4g and the token saver slice are
merged. This is the last prerequisite of gate G3. Documents only: nothing is implemented here.

Inputs:
- Program plan `docs/migration/2026-11-10-deno-go-migration-plan.md` §3 (design rules), §5 P4 (row S5 and the G3
  prerequisites: "routes (`router/*`: round-robin and cooldowns from provider headers) move to go, with the provider
  quota readings; until then every route call is handed to js"), §8, §9 (Go standard library only), §11.
- P4d spec Decisions 9 and 29 (routes handed off until G3), its owner question 3 (owner, single owner or replicate),
  Decision 31 (JS quirks copied now, fixed in both engines after G3).
- P4f spec D16 (one rate-limit window, replicated both ways, `ratelimit.take` and `ratelimit.window`; owner answer 3
  accepted a few calls over `rpm` at the boundary), D14 (the call journal), D1 (spans).
- Token saver spec D13/D14 and P4g spec Decision 14: a switch of its own that a call needs on top of its sub-class.
- Code at `1fab143`: `lib/gateway/providers.js` (`resetIn`, `readQuota`, `noteResponse`, `coolingDown`, `resolveAll`,
  `whyNot`, `request`, `quota`), `lib/gateway/index.js` (`targetsFor`, `allowedTargets`, `retryable`, `proxyChat`,
  `messagesApi`, `countTokens`, `responsesApi`, `responsesPassthrough`, `geminiApi`, `embeddings`, `llm`, the
  `/v1/models` list), `lib/gateway/shadow.js`, `lib/front/{index,push,shadow-log}.js`, `lib/ipc/{session,supervisor,
  schema}.js`, `server.js` (the `ratelimit.take` wiring), `gw/engine/{plan,chat,serve,serve_xlate,shadow,upstream,
  count,embeddings,passthrough,xlate}.go`, `gw/front/front.go`, `gw/ipc/messages.go`, `ipc/v1/*.schema.json`.

All Deno work follows the `deno` and `migrate-to-deno` skills; all Go work follows `go-stack-defaults` with the
program's rule on top: **Go is standard library only**. The owner's standing instruction is to take the recommended
option; each decision below is "recommended, pending owner". The questions the owner must answer are at the end.

## What the JS gateway does today (the reference)

| # | Fact | Where |
|---|---|---|
| R1 | A route is a config entry `{ name: router/<1-40 [a-z0-9-]>, targets: [≤ 8 names], strategy: fallback \| round-robin, description }`. A target name is resolved like any model (`resolve`: an alias of a usable provider, else `<id>/<model>` of a usable provider). | `cleanRoute`, `resolve` |
| R2 | `resolveAll(name, {privateOnly})`: for a round-robin route with more than one target, `n = (rr[name] \|\| 0) % targets.length`, `rr[name] = n + 1`, the raw names rotated by `n`; then each name resolved (unresolvable dropped), private-only filtered, and the targets **cooling down** moved to the back (stable order in both parts). The counter rotates on **every** call, including calls that are refused later (403 is before; 404, 402 are after). A plain model gives its one target. | `resolveAll` |
| R3 | `noteResponse(p, r)` runs on **every** provider answer `providers.request` reads (headers received; both answers of a 401 replay; `/models`, balance and test calls too): `readQuota(headers, now)` reads `x-ratelimit-{limit,remaining,reset}-{requests,tokens}` or `anthropic-ratelimit-{requests,tokens}-{limit,remaining,reset}`; with a reading or a 429 the provider's quota record is merged (`at`, `status`, `retryAfterMs` from `retry-after`). On 429, 503 or 529 the provider cools down: `wait = min(5 min, retry-after ?? (resetAt of a kind with remaining 0) - now ?? 1000·2^strikes)`, `until = now + max(1000, wait)`, `strikes + 1`, `reason: "HTTP <status>"`. A 2xx clears the cooldown. | `noteResponse`, `readQuota`, `resetIn` |
| R4 | `resetIn(v)`: digits → seconds; a string with `\d{4}-\d{2}-\d{2}` that `Date.parse` reads → ms until it (≥ 0); else the sum of `(\d+(\.\d+)?)(ms\|h\|m\|s)` parts; else null. `Number()` semantics on the limit and remaining values (`""` is 0). | `resetIn`, `readQuota` |
| R5 | The dashboard reads `providers.quota()` (Gateway → Model router → "Quota & cooldown"): per provider the merged quota record, the live cooldown, the token and the balance. | `quota`, `public/gatewayview.js` |
| R6 | `targetsFor(model, key)`: `resolveAll` (the take), 404 `whyNot` when empty, then for a key with a budget the priced filter (private or a known price of `t.name`), 402 naming the route when none is left. `allowedTargets` then drops the targets a budget refuses (model `t.name`, provider id); 402 with the first refusal when none is left. | `targetsFor`, `allowedTargets` |
| R7 | `proxyChat`'s loop (chat, and every translated call): the token saver once on the client body, then per target: the upstream body (`stream_options` merged when the target's preset reports stream usage), the guardrails for that target's tier (a block or failure records its line with that provider and throws 400/500), the call. A throw (fetch failure, header timeout) records a 599 line (no `routedTo`, no `requested`) and tries the next; a non-2xx that is retryable (408, 429, ≥ 500) and not the last target records a "fell back to the next route target" line (with `routedTo: t.name`) and discards the body. No target answered → 502 with the last error masked. Every byte to the client comes after the loop. | `proxyChat`, `retryable` |
| R8 | Takes per client call: chat 1; `/v1/messages` 2 (`messagesApi`'s `resolveAll(model)[0]` for the passthrough test, then `proxyChat`'s); `count_tokens` 1 (its first target decides provider count or estimate); `/v1/responses` 2 (`responsesApi`'s every-target-native test, then `responsesPassthrough`'s `targetsFor` or `proxyChat`'s); Gemini generate 1; Gemini `countTokens` 0; embeddings 1 (first allowed target, no fallback); `GET /v1/models` 1 per route the key may see; agents' `llm` 1. | `index.js` |
| R9 | `responsesPassthrough` has its own fallback loop with the same rules (per-target guard `responses`, 599, fallback line, `routedTo` when `t.name !== model`). | `responsesPassthrough` |
| R10 | The rotation counter, the cooldowns and the quota records are JS memory of one `createProviders`. Since P4b, provider answers the go engine reads never reach them: a provider used through go shows no quota reading, and a JS route call can pick a provider go just saw rate limited. | `providers.js`, `gw/engine` |
| R11 | The go engine hands off every route call today (`target`: "model: a route"; `count.go`: the first target of a route). | `gw/engine/plan.go`, `chat.go`, `count.go` |

## Decisions

Each is the recommended option, pending owner, with the evidence that settled it.

1. **Scope: every route call the served classes see moves, with the cooldowns and the quota readings** (recommended,
   pending owner). Chat (all four sub-classes), messages, messages-count, responses (passthrough and translated),
   gemini and embeddings serve a model that is, or maps to, a route. `gemini-count` takes nothing (R8) and is already
   served. `GET /v1/models` and `/v1beta/models` stay on js (P4d answer 30, the model cache). A route with no usable
   target (R6's 404, `whyNot`) and every other refusal the engine does not answer today are handed off (P4d
   Decision 14). Evidence: the G3 prerequisite; R8, R11.
2. **A switch of its own, `routes`, as `token-saver` and `provider-auth`** (recommended, pending owner). A route call is
   served on go only when its sub-class **and** `routes` are on go; it is compared when either is shadowed; default
   `js`. Shadow lines of route calls carry `route: true` and count for the `routes` record too; hand-offs whose reason
   starts with `route: ` count there. Evidence: classes already on go (`chat`, the P4d classes once switched) would
   otherwise start routing without one compare (rtk D13, P4g Decision 14 for the same reason).
3. **The state is replicated both ways, as the rate-limit window is (P4f D16)** (recommended, pending owner; **owner
   question 1**). Each engine decides synchronously from its own copy of the rotation counters and the cooldowns, as
   today, and tells the other:
   - `provider.note { providerId, status, at, headers }` for every provider answer it reads (R3), both directions;
   - `route.take { name, at }` for every rotation it takes (R2), both directions;
   - `route.state { rr: [{name, n}], cooldown: [{providerId, until, strikes, reason}] }`, control plane → gateway, to
     seed a new gateway session after `ratelimit.window` and before `budget.snapshot` (so before `ready`).
   A note or take applied from the other side is never sent back. Rejected: **a single owner** (JS as owner puts the
   event loop on the engine's hot path; Go as owner makes `resolveAll` and its twelve callers asynchronous, the D16
   argument); **no sharing** (R10 stays: a call routed by one engine to a provider the other saw at 429 — the exact
   failure the slice must prevent). With replication, a call can still pick a provider whose 429 the other engine read
   less than one IPC hop earlier (P3 measured p95 0.09 ms): the same as two concurrent calls in one JS process, where a
   call routed before the 429 arrived also picks it.
4. **Notes carry the observer's clock and raw headers; each side runs the same function** (recommended, pending
   owner). The receiver applies `noteResponse` with `now = at`, so `until`, `resetAt` and the quota's `at` are equal on
   both sides. `headers` holds only the names R3 reads plus `retry-after`, lower case, values joined with `", "` as
   `Headers.get` joins them; `ok` is `200 ≤ status ≤ 299`. A take is applied as JS applies it (`n = (rr || 0) % len;
   rr = n + 1`, with the receiver's current target count); takes commute, so both counters agree once both have seen
   the same takes.
5. **Quota readings: JS stays the store and the dashboard's source; Go keeps only what routing reads** (recommended,
   pending owner). Go reads the quota headers of every answer it receives (it needs `remaining`/`resetAt` for the
   cooldown wait) and sends them in the note; JS merges them into its quota records with `at`. The Go engine keeps
   cooldowns (until, strikes, reason) and rotation counters, nothing else. This closes R10's gap for every class
   already on go, whatever the `routes` switch says: notes flow from the first commit that wires them.
6. **Exact Go port of the routing functions in `gw/engine/routes.go`** (recommended, pending owner): `resetIn`, the
   parts of `readQuota` the cooldown reads (JS `Number` on strings, `resetAt`), `noteResponse`'s cooldown arm,
   `coolingDown`, `resolveAll` (rotation on the raw names, then resolve, private-only, cooling to the back, stable).
   `Date.parse` is ported for the ECMAScript date-time string format (date-only is UTC, date-time without an offset is
   local time) and V8's `YYYY-MM-DD HH:mm[:ss]` variant; any other string that has the date shape is a known
   difference (Decision 15). One mutex guards the state; `resolveAll` holds it for the take and the cooling test.
7. **Takes exactly where JS takes (R8), quirks copied** (recommended, pending owner; P4d answer 31). The engine takes
   after its rate-limit `Take` and before the price and budget 402s, as `targetsFor` does: chat 1, messages 2,
   messages-count 1, responses 2, gemini 1, embeddings 1. Copied quirk, to be fixed in both engines after G3: a
   `/v1/messages` or `/v1/responses` call takes twice, so a round-robin route of two targets serves those paths from
   its second target every time, and each `GET /v1/models` rotates every route (JS, replicated to go by Decision 3).
8. **Planning stays pure; the take happens in `Serve`** (recommended, pending owner; P4e Decision 4). A route plan holds
   one leg per raw target, each a complete single-target `Plan` built by today's planner for that target (nil for a
   name that does not resolve or a private-only key's external target). `Serve` takes, rotates the legs, drops the nil
   ones and moves cooling legs to the back. A leg that would be handed off (a credential missing, a refresh-grant
   provider, a body the engine cannot build, an Anthropic leg's translation error) hands off the **whole** call at plan
   time, before any take: JS takes and answers as today. A route whose every name is nil is the 404, handed off.
9. **The fallback loop is `proxyChat`'s (R7) and `responsesPassthrough`'s (R9), line for line** (recommended, pending
   owner): price filter (402 naming the route), budget filter (first refusal 402; the snapshot's `unchecked` marks
   every line of the call), the token saver once on the client body before the legs are built (every leg sends the
   shortened text; `savedTokens` from that one run), then per leg in order: guardrails for that leg's tier, the call,
   599 or fallback line, next. Streams pick their leg before the first byte, as JS. Embeddings and count use the first
   leg only, with no fallback (R8).
10. **Every line is journalled and gets its span** (recommended, pending owner; P4f D14, D1): the journal opens before
    each leg's call and closes with that leg's line (599, fallback or the answer), so a crash leaves one recovered
    line for the attempt in flight; each line becomes a model span as JS's exporter makes one per `usage.record`,
    proven by route cases in `gen-spans.js`.
11. **Shadow: JS's tape, replayed; Go's own view, reported** (recommended, pending owner). The JS capture gains `routes`
    (each `resolveAll` of the request: the name, `n` before the take, the cooling provider ids, the order it gave),
    `lines` (every usage line of the request, in order) and thrown calls in `upstream` (`{ thrown: <masked message> }`).
    The engine recomputes each order from (`n`, cooling) and compares it with JS's (diff `route.order`), then replays
    the legs in JS's order against the captured answers and compares the provider requests, the client answer and every
    line. The diff gate of the switch rule rests on that compare. Separately, the engine records its **own** view when
    it plans the forwarded call (its counter and cooling set, read without a take) and the line says
    `routeView: "same" | "differs"` against JS's first tape entry; `shadow-log.js` counts it per class, outside the
    diff gate (two calls in flight on one route legitimately see different counters), and the results doc reports it.
12. **Proof by fixtures from the real JS, two layers** (recommended, pending owner). `scripts/fixtures/gen-router.js` →
    `gw/testdata/router/`: function level (`resetIn`, `readQuota`, sequences of `noteResponse` with the state after
    each, sequences of `resolveAll` over rotation, cooling and private-only, with a clock that advances per step) and
    route level (`createGateway` in process as `gen-xlate.js`: **sequences** of client calls sharing one gateway, so
    rotation and cooldown carry from call to call; per class; fallback on 408/429/5xx/throw, last-target non-2xx,
    guard block on the second leg, price and budget filters, private-only, streams, double takes, the Responses
    passthrough loop, count and embeddings on a route). The Go test replays each sequence through `Engine.Serve` over a
    replay transport with one engine per sequence and compares every provider request, client answer and line byte for
    byte. Generators stay pure under the fixtures job's deny flags.
13. **The contract proof of the shared state** (recommended, pending owner): `test/contract/go-routes.test.js` runs the
    server with `chat` and `routes` on go and `messages` on js against the fake upstream: (a) a go-served chat call
    gets 429 from provider A; the next js-served `/v1/messages` route call tries B first; (b) the reverse: a js-served
    call gets A's 429; the next go-served route call tries B first; (c) the quota readings (`quota` of `GET /api/gateway`)
    after a go-served call equals the one after the same call in js mode; (d) a gateway restart keeps the cooldown and
    the counter (`route.state`); (e) round-robin rotation over consecutive go calls equals js mode. Under
    `ZP_ENGINE=shadow` the same calls end with diff 0 for the `routes` record.
14. **IPC: additive only** (recommended, pending owner): three new message types (`provider.note` both ways,
    `route.take` both ways, `route.state` cp → gw) with schemas and fixtures in `ipc/v1`, and the value `routes` in the
    `RouteClass` enum of `config.push`. No other message changes.
15. **Known differences, recorded, not fixed** (recommended, pending owner): (a) a reset or `retry-after` value with a
    date shape outside Decision 6's formats: V8's legacy parser reads more forms than the port; (b) the one-hop races
    of Decision 3 (a take or a note in flight while the other engine routes); (c) `strikes` when both engines note 429s
    of one provider within one hop (applied in another order, the back-off of the next 429 may differ by one doubling);
    (d) a route's target count changed by a config push between a take and its echo (the counters may differ by one
    position until the next take). The shadow reports (a) as a diff and (b)–(d) as `routeView: differs`.
16. **M3/M4 stay owed** to the program's final quiet-machine step: the results doc adds "chat on a two-target
    round-robin route, whole and 500 streams" to the owed list, every number marked "not quiet-confirmed".
17. **Implementation order, riskiest first** (recommended, pending owner): (1) function fixtures and the Go port;
    (2) replication (IPC, both sides, seed), which fixes R10 on its own; (3) route-level fixtures; (4) chat route plans
    and the fallback loop with the switch; (5) the P4d classes; (6) journal and spans; (7) shadow; (8) contract;
    (9) results and the program plan.

## Design

### D1. State (`gw/engine/routes.go`)

```go
type RouteState struct { mu sync.Mutex; rr map[string]int; cool map[string]Cooldown; send func(kind string, body any) }
type Cooldown struct { Until int64; Strikes int; Reason string }
func (r *RouteState) Note(providerID string, status int, h http.Header, at int64, remote bool)
func (r *RouteState) Take(route ipc.GatewayRoute, at int64, remote bool) int   // returns n before the take
func (r *RouteState) Peek(route ipc.GatewayRoute) (n int, cooling map[string]bool, now int64) // the shadow's view
func (r *RouteState) Cooling(providerID string, now int64) bool
func (r *RouteState) Seed(s ipc.RouteStateBody)
func Order(legs []*Plan, n int, strategy string, cooling func(id string) bool) []*Plan // R2 after the take
```

The state lives on the `Engine` (not in a `State` generation), as the token registry of P4g: a config push does not
reset it, as JS keeps its maps across config changes.

### D2. Planning (`plan.go`, `chat.go`, `xlate.go`, `passthrough.go`, `count.go`, `embeddings.go`)

`State.target` returns either one target or, for a route, `Route{Name, Strategy, Names}`; each planner builds its legs
with the existing single-target code (a helper `legs(route, build func(t *Target) Plan)`), sets `Plan.Route` and
`Plan.Legs`, `Plan.Takes` (1 or 2, R8) and the class from the first leg. A leg's hand-off is the plan's hand-off,
prefixed `route: `.

### D3. Serving (`serve.go`, `serve_route.go` new)

After the rate-limit `Take`: `RouteState.Take` (× `Plan.Takes`, the last one's `n` orders), `Order`, the price and
budget filters over the legs, then `serveLegs`: per leg `Guard`, the journal `open`, `Upstream.Call`, the note
(`RouteState.Note` and `provider.note` for every answer, also on non-route plans), then the answer path of today for the
chosen leg (`Respond`, `serveStream`, `serveDeliver`). The Upstream's response headers are made available to the note
before the body is read.

### D4. Replication (`gw/ipc`, `lib/ipc/session.js`, `lib/front/{index,push}.js`, `server.js`, `providers.js`)

JS: `createProviders` gains `onNote(fn)`, `onTake(fn)`, `applyNote(body)`, `applyTake(body)`, `routeState()`;
`noteResponse(p, r, at)` takes the clock; the gateway exposes them as it exposes `onRateTake`/`noteRateTake`.
`server.js` wires them to `front.sendControl` and to the control messages from the gateway; `push.js` builds the
`route.state` body; `session.js` sends it after the windows.

### D5. Shadow (`lib/gateway/shadow.js`, `providers.js`, `index.js`; `gw/engine/shadow_route.go` new)

`shadow.noteRoute({name, n, cooling, order})` in `resolveAll`; `shadow.noteLine(entry)` in `usage.record`'s callers for
a captured request (every line, not only the last); a thrown `request()` pushes `{ thrown }` to `upstream`. Go:
`Capture.Routes`, `Capture.Lines`, `ProviderAnswer.Thrown`; `CompareRoute` as Decision 11; `ShadowLine.Route`,
`ShadowLine.RouteView`; `shadow-log.js` tallies `routes` and `routeView`.

### D6. Front (`gw/front/front.go`, `lib/front/index.js`)

`GoClasses` and `GO_CLASSES` gain `routes`; `toEngine` shadows the route classes (chat, messages, responses, gemini,
embeddings) when `routes` is shadowed.

## Tests and fixtures

| Level | What | Where |
|---|---|---|
| Fixtures (JS truth), function | `resetIn`, `readQuota`, `noteResponse` sequences, `resolveAll` sequences | `scripts/fixtures/gen-router.js`, `gw/testdata/router/fn-*.json`, `gw/engine/routes_test.go` |
| Fixtures (JS truth), route | call sequences per class (Decision 12) | `gw/testdata/router/seq-*.json`, `gw/engine/serve_route_test.go` |
| Fixtures, spans | route calls with fallback lines | `scripts/fixtures/gen-spans.js`, `gw/testdata/spans/` |
| Go unit | hand-offs per planner; `Order`; replication echo-free; seed; `-race` on concurrent takes and notes | `gw/engine/routes_test.go` |
| JS unit | `applyNote`/`applyTake` equal to local ones; `route.state` packing; schema fixtures | `test/providers-replication.test.js`, `test/ipc-*.test.js` |
| Shadow | tape replay, order diff, `routeView`, a one-byte change is a diff | `gw/engine/shadow_route_test.go`, `test/front-shadow.test.js` |
| Contract | Decision 13 under `ZP_ENGINE=go` and `shadow` | `test/contract/go-routes.test.js` |

## Exit criteria (P4h, G3 prerequisite 2)

- Every `gw/testdata/router` fixture passes byte for byte on Go; the generator gives the same bytes on Node and Deno
  under the fixtures job's flags; the `spans` route cases pass.
- `go-routes.test.js` passes on Node under `ZP_ENGINE=go` and `ZP_ENGINE=shadow` (diff 0 for `routes`), including the
  cross-engine cooldown cases both ways.
- `gofmt`, `go vet` (also `GOOS=windows`), `go test -race` clean; the push-triggered Ubuntu run green; the full matrix
  on the PR.
- The program plan's G3 prerequisite 2 marked met, with the `routes` switch moving under the owner's switch rule.

## Out of scope

- The model cache and `GET /v1/models`, `/v1beta/models` (P4d answer 30).
- Fixing the copied quirks of Decision 7 (after G3, both engines).
- Balances (`balance()`): JS calls the balance APIs itself; P5 moves that egress.

## Open questions for the owner

1. **Share the routing state by replicating it both ways (Decision 3)?** From the first commit that wires the notes,
   a JS-served route call skips a provider the go engine saw at 429/503/529 (and the reverse), as v1.x did in one
   process; today, on this branch, it does not. The cost is a race of one IPC hop (≈ 0.1 ms), the same class of risk
   the owner accepted for the rate-limit window (P4f D16). Alternatives: a single owner (adds an IPC round trip to
   every route call on one side) or no sharing (keeps today's gap). **Recommended: replicate.**
