# P4 stage 1: three parallel streams — design

Date: 2026-11-12. Repo `ZAAFPulse-deno-go`. Base `migrate/deno-go` at `652ab24` (P4a merged).

Inputs: the P4 split in `docs/superpowers/specs/2026-11-12-p4a-go-front-design.md`; the P4a results and its P4b/P4f
entry conditions in `docs/migration/2026-11-12-p4a-results.md`; the program plan §5 P4 (slices S1–S10).

All Deno work follows the `deno` and `migrate-to-deno` skills. Go is standard library only (P0 decision); the only
crypto is `crypto/*` from the standard library.

## Owner decision (2026-11-12)

P4b–P4g split into a library part (pure logic, testable alone with fixtures from the JS code) and a wiring part (into
the Go request path P4b creates). Stage 1 runs three streams in parallel, each on its own branch and worktree, each with
its own plan, SDD run, reviews and PR into `migrate/deno-go`. Stage 2 (P4c and the wiring of P4d–P4g) is mostly
sequential and gets its own specs.

## The three streams

| Stream | Branch | Scope | Touches |
|---|---|---|---|
| A — P4b | `migrate/p4b-passthrough` | S2 non-streaming OpenAI pass-through on the `go` engine, S4 API keys (per-key allow-lists and modes), the shadow compare, and the P4b entry conditions | `gw/engine/` (new), `gw/front/`, `gw/cmd/`, `lib/front/`, `lib/ipc/`, `server.js` where the shadow and the engine switch need it |
| B — libraries | `migrate/p4-lib-translate-guard` | P4d translation library and P4e guardrail library, pure Go, no request path | `gw/translate/` and `gw/guard/` (new), fixture generators under `scripts/fixtures/`, fixtures under `gw/testdata/` |
| C — IPC + provider auth | `migrate/p4-lib-ipc-auth` | P4f entry conditions (outbox `Close` under `sendMu` and safe twice; store fsync before the mark or a documented order; marks re-read per session) and the P4g provider-auth library | `gw/ipc/` (outbox only), `lib/ipc/` (marks only), `gw/provauth/` (new) |

No two streams edit the same file. Stream A owns `gw/engine/` and every wiring file; streams B and C add new packages
and never import from `gw/engine/`. If a stream finds it must change a file another stream owns, it stops and reports.

## Equivalence by generated fixtures (streams B and C)

The JS modules stay the reference until G3. Each library gets a generator script, `scripts/fixtures/gen-<lib>.js`,
that runs the real JS function over a fixed input corpus and writes `gw/testdata/<lib>/<case>.json` as
`{ "fn": "<name>", "in": …, "out": … }` (or `"throws": "<message>"`). The Go tests replay every file and compare
byte-for-byte after canonical JSON encoding. The corpus starts from the inputs the existing JS tests already use
(`test/gateway-compat.test.js`, `test/router9.test.js`, `test/guardrails*.test.js`) plus edge cases each plan adds. A CI
step regenerates the fixtures and fails on a diff, so JS and Go cannot drift silently.

### Stream B interfaces (Go package `translate`, package `guard`)

- `translate`: one Go function per JS export used on the request path:
  `AnthropicToOpenAI`, `OpenAIToAnthropic`, `OpenAIToAnthropicRequest`, `AnthropicResponseToOpenAI`,
  `AnthropicEventsToOpenAI`, `AnthropicUsageToOpenAI`, `ResponsesToOpenAI`, `OpenAIToResponses`, `GeminiToOpenAI`,
  `OpenAIToGemini`, `EstimateTokens`, plus the stop-reason tables. JSON in and out as `json.RawMessage` or
  `map[string]any` where the JS is shape-agnostic; the plan fixes each signature. The streaming transforms
  (`anthropicStream`, `responsesStream`, `geminiStream`, `readSse`, `wholeToChunks`) are ported as pure event-in,
  event-out functions over a slice of SSE events; the HTTP streaming itself is P4c.
- `guard`: `CheckBody(body, policy, format) (Result, error)`, `CheckText(text, policy) Result`, `ResolvePolicy`,
  `BlockMessage`, with the secrets, PII and injection checks. `lib/signals.js` `scanInjection`, `lib/evals/pii.js` and
  the secret patterns are ported as far as the guardrail calls them, no further.

### Stream C interfaces (package `ipc` changes, package `provauth`)

- `ipc`: `(*Outbox).Close` takes `sendMu`, is idempotent, and never writes `stream.json` after the lock is released.
  Marks: the JS ingest re-reads the mark file at each new session; the order "store, then mark" holds after a crash and,
  for power loss, either fsync the stores before the mark or document why the order is acceptable (the plan decides and
  says which).
- `provauth`: `type Source interface { Token(ctx) (string, time.Time, error) }` with `OAuth2(cfg, secret, httpClient,
  clock)` for `client_credentials` and `refresh_token`, and `GoogleServiceAccount(saJSON, scope, httpClient, clock)`
  signing an RS256 JWT. Tokens are cached until 60 s before expiry; concurrent callers share one refresh. Secrets come
  from `secrets.handoff`, never from the environment. Fixtures: the JWT header and claims are compared against the JS
  output for a fixed clock and a test key; token endpoints are faked with `httptest`.

## Stream A (P4b) — what its plan must settle

Stream A is the large one and needs design detail the plan settles with evidence from the code, listing each choice
under "Decisions" for the owner's review:

1. The first `go` route class: `chat` (`/v1/chat/completions`) non-streaming, OpenAI-format providers only
   (`format: openai`, no translation — translation is stream B, wired in stage 2).
2. API keys (S4): Go checks the key hash from `config.push` (P3 schema), the per-key allow-lists and modes, with
   fixtures generated from `lib/gateway/keys.js` the same way as streams B and C.
3. Shadow compare (owner decision 2, P4a spec): only the serving engine calls the provider; the other engine builds its
   provider request and turns the same provider response into its client response; the diff compares provider request,
   client response, usage and cost, and is logged to `.data/gateway-shadow.jsonl`. A route switches to `go` only after
   the contract suite is green on Go and the shadow diff is 0 (plan §5 P4: for 3 days — the owner may shorten this; ask).
4. P4b entry conditions from the P4a results: one usage writer that also feeds the in-memory spend for per-key
   budgets; route classification on the same (encoded) path as `gateway.handle`; when a late `front` section takes
   effect and how reload applies it; proxy and CA variables passed to the gateway on an allow-list before the first
   provider call; header limits and log noise in line with Node; the chip says "restart needed" after a crash loop.
5. Upstream calls: secrets from `secrets.handoff`, the provider's `baseUrl` and `tokenParam`, timeouts equal to the JS
   ones, error bodies masked with the handoff's mask set.

## Exit criteria (stage 1)

- [ ] Stream A merged: `chat` non-streaming can run on `go` behind the shadow; contract cases for it green on Go;
      shadow diff 0 on the contract corpus; the P4b entry conditions met, each with a test.
- [ ] Stream B merged: every listed `translate` and `guard` function matches the JS fixtures byte-for-byte; the
      regenerate-and-diff CI step is green.
- [ ] Stream C merged: the P4f entry conditions met with tests; `provauth` matches the JS JWT fixtures and passes the
      fake-endpoint tests.
- [ ] Each stream ran a full-matrix CI dispatch on its own branch before its PR.

## Rules for all three streams

- No AI attribution in commits. Never commit `.superpowers/`. Push only the stream's own branch; no temporary branches.
  Never remove a worktree you did not create. Kill only processes you start.
- node:test runs `t.after` hooks in the order added: stop processes before removing their folder, in one hook.
- Never touch the live dashboard (port 4777) or the preview (port 4787).
