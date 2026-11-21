# P4g: `/mcp` `tools/call` and provider auth on the Go engine (slices S9 and S10): design

Date: 2026-11-21. Repo `ZAAFPulse-deno-go` (public). Branch `migrate/p4g-mcp-auth`, cut from `migrate/deno-go` at
`61ec0aa` (P4d merged). The token saver slice (`migrate/p4-rtk-go`) is being finished in parallel and ships in one
combined PR with this branch; the overlaps are listed at the end. Documents only: nothing is implemented here.

Inputs:
- Program plan `docs/migration/2026-11-10-deno-go-migration-plan.md` §3 (rule 3: secrets are handed over, not read;
  rule 5: egress only through Go), §5 P4 rows S9 ("`/mcp` `tools/call` path, gateway keys. Exit: contract cases green")
  and S10 ("Google service-account JWT (`createSign` RSA-SHA256) and other provider auth. Exit: live provider smoke tests
  (owner keys, manual)"), §9 (Go stdlib only), §11.
- Stage 1 spec and plan (`2026-11-12-p4-stage1-design.md`, `plans/2026-11-12-p4-lib-ipc-auth.md` D5, D6): `gw/provauth`
  (OAuth2 client_credentials and refresh_token, the Google service account's RS256 JWT, the token cache with a shared
  refresh, the refused redirect), proven by `gw/testdata/provauth` from the real JS. Its D5 fixes the wiring: the secret
  is read from the latest `secrets.handoff` at every refresh.
- P4b, P4d, P4e, P4f specs and results: the per-class engine switch with shadow mode and the owner's switch rule (1 clean
  day, 20 compares), hand-offs for everything the engine does not answer, route-level fixtures from the real JS replayed
  byte for byte, the usage line through the outbox and the call journal, the fixtures job's Deno deny flags.
- The rtk spec on `origin/migrate/p4-rtk-go` (`2026-11-17-rtk-go-design.md` D13, D14): a switch that is not a path
  (`token-saver`) with its own engine entry, shadow entry and switch-rule record; a shadow line flag counted for it.
- Code at `61ec0aa`: `lib/gateway/index.js` (`route` `/mcp`, `guardedTool`, `mcpFor`, `authed`), `lib/mcp/protocol.js`
  (`createServer.handle`, `SUPPORTED_VERSIONS`), `lib/gateway/providers.js` (`refreshToken`, `accessToken`, `headers`,
  `request` with the 401 replay, the background refresh timer, `quota().token`), `lib/front/push.js secretsPush`,
  `lib/front/internal.js` (the gateway's own requests to `/_zp/`), `lib/front/shadow-log.js`, `gw/engine/{plan,serve,
  upstream,shadow,guard}.go`, `gw/front/front.go`, `gw/provauth/*`, `gw/guard` (`CheckBody` format `arguments`,
  `CheckText` part `result`, already ported for this slice).

All Deno work follows the `deno` and `migrate-to-deno` skills; Go is standard library only. The owner's standing
instruction is to follow the recommended option: every choice below is "Decision N (recommended, pending owner)".

## What the JS gateway does today (the reference)

| # | Fact | Where |
|---|---|---|
| J1 | `POST /mcp`: `authed(req,'mcp')` (401, 403, then the key's rate limit 429 in the OpenAI `fail` shape) → an `MCP-Protocol-Version` header that is not one of `SUPPORTED_VERSIONS` → 400 `{jsonrpc, id:null, error:{-32600,…}}` → `readJson` (413 → 413 -32600, bad JSON → 400 -32700) → a batch (array): version ≥ 2025-06-18 → 400, empty → 400, > 20 → 400, each extra message takes from the rate limit → `mcp.handle(m)` per message → no reply → 202 empty, else `json(200, reply)`. Other methods → 405. | `index.js` route |
| J2 | `handle(msg)`: a response message → no reply; not an object/`jsonrpc !== '2.0'`/method not a string → -32600; no `id` → no reply (notification); `params` null → `{}`, not an object → -32602; `initialize` (server name, app `version`, instructions), `ping`, `tools/list` (every integration's tools), `tools/call` (`params.name` a string else -32602; `arguments` null → `{}`, not a plain object → -32602; `r.error` → `{error:{code,message}}`, else `{result: r.result}`), other → -32601; a throw → -32603 `Internal error`. | `lib/mcp/protocol.js` |
| J3 | `guardedTool(key, name, args)`: policy = `resolvePolicy(guardrails, key)`; tier `private` for a name starting `acc_`, else `external`; not in scope → `callTool(name, args)` and nothing else. In scope: `checkBody(args, {format:'arguments'})` (block → reply error -32602 with the block message, failure → -32603 `guardrails failed`; each with a line 400 `blocked by guardrails` or 500 `guardrails failed`) → `callTool(name, a.body)` → the text of every `text` part and embedded `resource.text`, joined by `\n` → `checkText(text, {part:'result'})` (block or failure → `result: {content:[{type:'text', text: message}], isError: true}` and the line; the hits are the arguments' then the result's) → a line only when there are hits or a check failed: `{keyId, provider: <name before the first '_'> \|\| name, model: name, kind:'mcp', ms, status: r.error ? 502 : 200, error?: 'integration error' \| 'tool error', guard:{action: 'redacted' \| 'logged', hits, error?}}`. | `index.js` |
| J4 | Every `/mcp` request gets the server span (`wrapHandler`); each usage line gets its client span (`wrapUsage`). | `lib/otel.js` |
| J5 | Provider auth: `headers(p, force)` → for `auth.type` `oauth2`/`google-sa`, `accessToken(p, force)` (cached until 5 min before expiry; one refresh per provider at a time), else the stored key; Anthropic format → `x-api-key`, else `authorization: Bearer`. The token is fetched inside `request`'s `send`, so a token failure is a failed call: `"<id>: <id>: token refresh failed (…)"`, masked. | `providers.js` |
| J6 | `request`: a 401 answer from a provider with token auth → the body cancelled, a forced refresh, the request sent once more (new 120 s timer). Every served path (chat, streams, messages, responses, gemini, embeddings, count_tokens) goes through it. | `providers.js request` |
| J7 | A 60 s timer force-refreshes every usable token-auth provider whose cached token expires within 10 min. `quota()` shows each provider's token expiry and refresh time (the Providers page). | `providers.js` |
| J8 | The token cache is keyed by provider id, in memory, and survives config changes; the secret is resolved at each refresh. | `providers.js` |
| J9 | `secretsPush` leaves out every provider whose `auth.type` is not `key` (comment: "provider auth in Go is P4g"); the engine hands such a target off ("provider: OAuth2 or a service account (P4g)"). | `lib/front/push.js`, `gw/engine/plan.go credential` |

## Decisions

### S9: `/mcp` `tools/call`

1. **Scope (recommended, pending owner): the engine serves one JSON-RPC message per POST whose method is
   `tools/call`; everything else on `/mcp` is handed off.** Handed off: any method other than POST, a 401/403 key, an
   unsupported `MCP-Protocol-Version`, an empty, over-20-MB or non-JSON body, a batch, a response message, a
   notification, an invalid request, `initialize`, `ping`, `tools/list`, any other method, `params` that are not an
   object, a `name` that is not a string, `arguments` that are not a plain object. Evidence: S9's row names the
   `tools/call` path; every other answer comes before any work (no tool, no guardrail), so a hand-off is exact (P4d
   Decision 14) and avoids porting `initialize`'s app version and the integrations' tool list. "Gateway keys" in S9's row
   is the key check of that path: `RequestSecret`, `Authenticate`, scope `mcp`, the rate limit (all ported in P4b/P4f).
2. **The tool runs in the control plane, called by the engine over the internal port (recommended, pending owner).**
   Integrations (connectors, `acc_*` reading local data) stay in Deno (program plan: only the gateway hot path moves).
   The engine owns the HTTP edge: key, rate limit, the envelope, both guardrail checks, the usage line, the spans, the
   reply. It calls `POST /_zp/mcp/call` on the internal port as the gateway itself (`X-Zp-Own`, the signed client
   header, as the shadow endpoints), body `{"name", "arguments"}` (the checked arguments, `JSON.stringify`), answer
   `{"r": <callTool's value>}` or `{"thrown": true}` when `callTool` threw. No IPC schema change (the transport is the
   one the shadow already uses). The call has no time limit of its own and is not cancelled when the client leaves, as
   in JS (the tool's own timeouts apply). A failed internal call (the control plane gone) answers -32603
   `Internal error`, as a throw in `handle` does.
3. **One new sub-class `mcp`** with its own engine entry, shadow entry and switch-rule record (`Subs["mcp"] =
   {"mcp"}`, `GO_CLASSES`). Default `js`.
4. **Order as JS (J1, J3):** key → rate limit (`Take`, 429 in the OpenAI shape) → the arguments check → the tool → the
   result check → the line → the reply. No budget check (JS has none on `/mcp`). The policy and scope are
   `guard.ResolvePolicy` and the P4e scope test with tier `private` for `acc_*`.
5. **Lines and spans as JS:** a line only when J3 writes one, through the outbox (kind `mcp`, cost 0 as the JS
   pricer gives a call without tokens); the server span for every served call and the client span for a line, as
   `lib/otel.js` builds them for kind `mcp` (proven by the spans fixtures or a contract case; if the Go span builder
   cannot give the same span, the class hands off while `trace.spans` is on and this is recorded). **No call journal
   entry**: JS writes the line only at the end and only sometimes, so a 599 recovered line would be a line JS never
   writes.
6. **Proof: route-level fixtures from the real JS.** `scripts/fixtures/gen-mcp.js` runs `createGateway` in process
   with a stubbed `getIntegrations` that records each `callTool(name, args)` and answers from the case; it writes
   `gw/testdata/mcp/<name>.json`: the request, the arguments the tool received (or none), the tool's value, the client's
   status, headers and bytes, the last usage line. The Go test plans each case, checks the arguments it would send, feeds
   the recorded tool value, and must give the same bytes and line. One extra file pins `SUPPORTED_VERSIONS`.
7. **Shadow:** `guardedTool` notes the tool call in the capture (`shadow.noteTool(name, args, r)`; also on the
   not-in-scope path), with the client bytes `withShadow` keeps and the last line. The engine replays: the arguments it
   would send against the captured ones, then the captured tool value through the result check and the reply builder,
   comparing the client status, bytes and the line. A capture without a tool call (JS refused first) compares the
   client body only; a rate-limit answer is `skipped` as in P4e.

### S10: provider auth

8. **The engine refreshes its own tokens with `gw/provauth` (recommended, pending owner).** Evidence: stage 1 built
   `provauth` for this wiring (its D5: "the wiring reads the latest `secrets.handoff`") and rule 5 makes Go the only
   egress after P5, token endpoints included. The alternative, the control plane refreshing and handing tokens over
   (`ProviderSecret.token` from P3), needs a gateway-to-control-plane request for the forced refresh after a 401 and
   keeps a second egress path. P3's `token` field stays in the schema, unused.
9. **`secretsPush` hands over the stored credential of `oauth2` and `google-sa` providers too** (the client secret,
   the `{client_secret, refresh_token}` JSON, or the service account JSON), with the mask set built as today. This is
   rule 3 as written ("the control plane … hands provider secrets to the gateway"): the same pipe, memory only, never
   logged (`ProviderSecret.String`, `provauth.Cached.String`). A provider whose credential does not resolve is still
   left out, and the engine hands it off (JS answers with its own resolve error).
10. **One token registry per engine, outside the state generations**: a source per provider, keyed by the provider id
    and its auth settings (type, token URL, client id, scope, grant). The secret function reads the newest state's
    handed value; the mask function the newest state's mask set; the HTTP client is the engine's provider client
    (proxy env, trusted roots), copied by `provauth` with redirects refused. A config change of the auth settings makes a
    new source (JS keeps the old token until it expires: known difference 3).
11. **The plan carries the source, not the token.** `credential(t)` returns the source for a token-auth target; the
    plan's headers hold no credential; `Upstream.Start` gets the token (inside the 120 s limit) and sets
    `Authorization: Bearer <token>` or, for an Anthropic-format provider, `X-Api-Key: <token>`. A token error is a failed
    call whose text is the source's message (`"<id>: token refresh failed (401)"`), so the line and the client answer
    read `"<id>: <id>: …"` exactly as J5. Plans stay pure.
12. **The 401 replay (J6):** when the final answer (after same-origin redirects) is 401 and the plan has a source: the
    body is closed, `Refresh` is called, the request is sent once more with a new 120 s limit. Every served path gets it
    because every one goes through `Start`.
13. **Background refresh (J7):** a 60 s ticker in the registry force-refreshes, in the background, each source whose
    provider is usable in the newest state and whose cached token expires within 10 minutes. `provauth` gains
    `(*Cached).Expiry() (time.Time, bool)`.
14. **A switch of its own, `provider-auth`** (the rtk `token-saver` pattern): not a path; its own engine entry, shadow
    entry and switch-rule record. A call to a token-auth target is on go only when its sub-class **and**
    `provider-auth` are on go; it is shadowed when `provider-auth` is shadowed. The front gives the engine the
    provider classes (chat, messages, responses, gemini, embeddings) while `provider-auth` is shadowed. A shadow line of
    such a call carries `auth: true`, and `shadow-log.js` counts it for its class and for `provider-auth`. Evidence: the
    owner's rule is per switch (P4d Decision 2); without it, every token-auth call of an already switched class (chat)
    would move to go without passing the gate.
15. **Shadow compare with tokens:** each engine holds its own token, so for a token-auth plan the credential header is
    compared by name and scheme only (`Bearer <token>` / `<token>` for `x-api-key`), never by value or hash. A capture in
    which JS replayed after a 401 (two provider requests) is `skipped` ("a token refresh replay: each engine holds its
    own token"). The engine never fetches a token for the shadow.
16. **Proof: route-level fixtures from the real JS.** `scripts/fixtures/gen-auth.js` runs `createGateway` with a
    `fetchImpl` that answers the token endpoint and the provider from a queue and records every exchange in order (the
    token form included; the JWT is deterministic: RSASSA-PKCS1-v1_5, the fixed clock, the test key of
    `gen-provauth.js`). Cases: chat whole and stream, a translated `/v1/messages`, an Anthropic-format provider
    (`x-api-key`), embeddings, native count_tokens, OAuth2 client_credentials and refresh_token (rotation over two
    calls), a Google service account, the 401 replay (ok, and 401 again), a token endpoint that fails (4xx, not JSON, a
    redirect, no `access_token`), two calls that share the cached token. Writes `gw/testdata/auth/<name>.json`. The Go
    test runs `Engine.Serve` over a replay transport that checks every request (URL, headers, body) in order and answers
    the recorded response.
17. **No IPC schema change.** `config.push` already carries `auth` (type, tokenUrl, clientId, scope, grant); the
    handoff's `providers[].value` already holds any string. The `provider-auth` switch name joins the `RouteClass` enum
    (`ipc/v1/config.push.schema.json`, `gw/ipc/messages.go`), as `token-saver` does: an additive enum value.
18. **Live provider smoke tests are manual and deferred** (S10's exit): listed in the results document with their
    steps; nothing is sent to a real provider or token endpoint by this branch's tests, which use local fakes only.

## Design

### D1. `gw/engine/mcp.go` (new)

`PlanMCP(r, now)`: admit with scope `mcp` (hand-offs as Decision 1), `MCP-Protocol-Version` against `MCPVersions`
(fixture-pinned), the envelope checks, then a plan with `Class "mcp"`, `Upstream UpTool`, `Shape ShapeOpenAI`, the
JSON-RPC `id` as parsed (`Value`), the tool name, the arguments, the policy when in scope. `State.CheckToolArgs(p)`
(the arguments check: reply error or the arguments to send), `State.ToolReply(p, toolValue, ms)` (the result check,
the reply bytes, the line or none) are pure; both tested by the fixtures.

### D2. Serving (`gw/engine/serve_mcp.go`, new; `serve.go` dispatch)

`Serve` → plan → sub-class `mcp` → `Take` → `serveMCP`: the arguments check (a refusal: its line, then the reply) →
`e.tools(ctx, name, args)` (detached context) → `ToolReply` → the line → the reply → spans. `Engine.SetTools(f)` is
wired in `gw/cmd/zaafpulse-gw` with the front's upstream transport (`X-Zp-Own`, the client header, no 5 s limit).

### D3. Control plane (`lib/front/tools.js`, new; `server.js`, `lib/front/index.js`)

`toolEndpoint({ getIntegrations })` answers `POST /_zp/mcp/call` (own requests only, composed with
`shadowEndpoints`): `callTool(name, arguments)` → `{r}`, a throw → `{thrown: true}`, a bad body → 400.

### D4. Provider auth (`gw/engine/auth.go`, new; `plan.go`, `upstream.go`; `gw/provauth`)

`Tokens` (the registry: `Source(p *ipc.ProviderConfig) *provauth.Cached`, the ticker), `Plan.auth`, `credential`
returning it, `Upstream.Start` with the token and the replay. `lib/front/push.js secretsPush` includes token providers.

### D5. The switch (`gw/front/front.go`, `gw/engine/serve.go`, `lib/front/index.js`, `lib/front/shadow-log.js`, the
config.push enum)

As rtk D13/D14 with `provider-auth`, `auth: true`, and `authClasses` in `toEngine`.

### D6. Shadow (`lib/gateway/shadow.js`, `index.js`; `gw/engine/shadow.go`, `shadow_mcp.go` new)

`noteTool`; `CompareMCP`; the credential-scheme compare and the replay skip (Decision 15).

## Tests and fixtures

| Level | What | Where |
|---|---|---|
| Fixtures (JS truth) | `tools/call`: not in scope, in scope without hits (no line), redact, block (-32602, 400 line), result injection log/block, integration error with and without hits, `isError`, an `acc_` tool, ids (number, string, null), `arguments` omitted/null, 429; the version list | `scripts/fixtures/gen-mcp.js`, `gw/testdata/mcp/`, `gw/engine/mcp_test.go` |
| Fixtures (JS truth) | token auth on every served path, the 401 replay, token failures, shared cache, JWT | `scripts/fixtures/gen-auth.js`, `gw/testdata/auth/`, `gw/engine/auth_test.go` |
| Go unit | MCP hand-off list; the registry (key change, secret read live, mask), background refresh (fake clock), `Expiry`, a token error text, the replay once only | `gw/engine/*_test.go`, `gw/provauth/*_test.go` |
| Shadow | `CompareMCP` same/diff; token plans: scheme compare, replay skipped; the `provider-auth` tally | `gw/engine/shadow_*_test.go`, `test/front-shadow.test.js` |
| JS | `secretsPush` with token providers; `/_zp/mcp/call` | `test/front-push*.test.js`, `test/front-tools.test.js` |
| Contract | `mcp.test.js` cases served by go under `ZP_ENGINE=go` (`go-mcp.test.js` asserts the engine served them); `go-auth.test.js` against a fake token endpoint in `test/contract/upstream.js` (OAuth2 and a service account with a test key; the 401 replay); `ZP_ENGINE=shadow` ends with diff 0 | `test/contract/` |

## Exit criteria (P4g)

- Every `gw/testdata/mcp` and `gw/testdata/auth` fixture passes byte for byte; the generators give the same bytes on
  Node and on Deno under the fixtures job's flags; `gw/testdata/provauth` stays green.
- The contract `/mcp` cases and the token-auth cases are served by the go engine under `ZP_ENGINE=go`; shadow ends
  with diff 0.
- `gofmt`, `go vet` (also `GOOS=windows`), `go test -race` clean; the push-triggered Ubuntu CI run green (the full
  matrix runs once on the combined PR, owner's CI policy of 2026-11-21).
- S9's row met; S10's row: everything but the manual live smoke tests, which are listed.

## Known differences from the JS gateway (recorded, not fixed)

1. A failed internal tool call (the control plane gone mid-call) answers -32603 `Internal error` and writes no line;
   JS has no such path.
2. The Providers page's token expiry (`quota().token`) shows the control plane's own token (from its model-list and
   test calls), not the gateway's.
3. After a change of a provider's auth settings the engine fetches a new token at once; JS keeps the cached token until
   it expires. A change of the stored secret alone behaves as JS (read at the next refresh).
4. Two token caches while both engines serve calls of one provider: twice the token requests at most.
5. The arguments reach the integration through `JSON.stringify`/`JSON.parse` (Go to the control plane): the same
   value for any JSON value the engine parses; a lone surrogate or a `__proto__` key are handed off by `engine.Parse`
   rules as in P4b.

## Out of scope

- `initialize`, `ping`, `tools/list`, batches and every other `/mcp` answer (handed off; Decision 1).
- `/a2a`, `/skills`, agents, `GET /v1/models` (stay on js; program plan: agents stay in Deno).
- Egress of the integrations themselves (P5).
- Live provider smoke tests (manual, deferred; Decision 18).

## Files that overlap the token saver branch (`migrate/p4-rtk-go`)

`gw/front/front.go` (`GoClasses`, `toEngine`), `gw/engine/serve.go` (the switch composition after the sub-class mode),
`gw/engine/shadow.go`, `gw/engine/plan.go`/`xlate.go`/`passthrough.go` (plans), `lib/front/index.js`
(`SUB_CLASSES`, `GO_CLASSES`), `lib/front/shadow-log.js` (`tally`), `gw/ipc/messages.go` and
`ipc/v1/config.push.schema.json` (the `RouteClass` enum), `scripts/fixtures/gen-xlate.js` and `test/front-shadow.test.js`.
`origin/migrate/p4-rtk-go` is merged into this branch before the tasks that touch these files, and again before the
final review, so the combined PR has one resolution.
