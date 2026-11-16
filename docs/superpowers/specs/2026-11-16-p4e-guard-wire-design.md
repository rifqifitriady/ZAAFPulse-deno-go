# P4e: guardrails and the budget guard on the Go engine (slices S6 and S7): design

Date: 2026-11-16. Repo `ZAAFPulse-deno-go` (public). Branch `migrate/p4e-guard-wire`, cut from `migrate/deno-go` at
`ae7b9ab` ("Merge P4c: streaming chat (SSE) on the Go engine"). P4b, P4c and the guard readiness work
(`migrate/p4e-guard-ready`) are merged. A parallel branch plans P4f (`migrate/p4f-ipc-events`, usage, audit and trace
from Go); the file overlaps are listed at the end.

Inputs:
- Program plan `docs/migration/2026-11-10-deno-go-migration-plan.md`: §3 design rule 2 (budgets fail open), §4 M3 and
  M4, §5 P4 slices S6 ("fixtures from the guardrail tests pass"; block → 400 `guardrail_blocked`) and S7 ("kill-IPC
  test: metered calls pass, each marked `budget_unchecked` and alerted; a spent budget in the snapshot is still
  refused"), §8 risk "a budget leak while IPC is down", §11 owner decision 6 (budgets fail open).
- Stream B plan `docs/superpowers/plans/2026-11-12-p4-lib-translate-guard.md`: "P4e entry conditions" (condition 4: a
  non-`*Block` error from `CheckBody` is a rejection) and notes 13–28 (one budget per call, the 10 s backstop, `log` mode
  `Failures`, `guard.Prepare`, the overrun deviation, M1/M2 of note 18).
- `docs/superpowers/plans/2026-11-13-p4e-guard-ready.md` (the library is ready; nothing is wired).
- P4b and P4c: plans and results (`docs/migration/2026-11-12-p4b-results.md`, `2026-11-13-p4c-results.md`): the
  hand-off rules, the shadow, the open note m6 (the 64 MiB soft memory limit), "guardrails and budgets on streams: P4e",
  "M4 on the external tier after P4e".
- Code at `ae7b9ab`: `lib/gateway/guardrails.js`, `lib/gateway/index.js` (`guard`, `allowedTargets`, `limitedKey`,
  `targetsFor`, `checkBudget`, `proxyChat`, `chatCompletion`, `fail`, `authed`), `lib/cost/guard.js`,
  `lib/cost/budget-service.js`, `lib/cost/budgets.js` (`legacyBudgets`), `lib/front/push.js` (`configPush`, `keyAuth`,
  `snapshotPush`), `lib/ipc/supervisor.js` (snapshot every 1 s, the unchecked alert), `lib/ipc/ingest.js`,
  `lib/gateway/shadow.js`, `lib/monitors/rules.js` (`guardrail_block`), `server.js` (`budgetGuard`, `guardBlocks`,
  `/api/gateway/status`), `gw/guard/guard.go`, `gw/ipc/{budget,messages,outbox,channel}.go`,
  `gw/engine/{chat,serve,shadow,providers,pricing,jsjson}.go`, `gw/cmd/zaafpulse-gw/main.go`,
  `scripts/fixtures/{gen-chat,gen-guard,lib}.js`, `test/contract/{guardrails,budgets,go-chat,harness}.js`.

All Deno work follows the `deno` and `migrate-to-deno` skills; all Go work follows `go-stack-defaults` (stdlib only by
the program's §9 rule). No owner was available while this was written: every choice is under "Decisions" with its
evidence, and the few real questions are under "Open questions for the owner".

## What the JS gateway does today (the reference)

| # | Fact | Where |
|---|---|---|
| J1 | The guardrails check only what goes **to** a provider. No guardrail runs on a model's answer, whole or streamed: `chatCompletion` and its stream rewriter never call `G.*`. The only `checkText` on a result is the MCP tool result (`part: 'result'`, injection only), on `/mcp` (S9). | `lib/gateway/index.js` `guard`, `chatCompletion`; `mcpTool` |
| J2 | Order for `POST /v1/chat/completions`: `authed` (401/403, then the key's rate limit 429) → body parse → `proxyChat`: `authorize` (403) → `targetsFor` (404; a *limited* key may use only priced or private targets, else 402) → `allowedTargets` (the budget guard per target; 402 when no target may go) → token saver → per target: `guard(upstreamBody)` → `callChat`. | `authed`, `proxyChat`, `targetsFor`, `allowedTargets` |
| J3 | `guard` checks `upstreamBody = { ...body, stream_options… }` (the client body with the stream usage merge), **before** `callChat` sets `model: t.model` and `adaptChat`. The policy is `resolvePolicy(gateway.guardrails, key)`: the key's own modes win, the scope is global only. Out of scope (`inScope(policy, scopeTier(provider, model))` false) → no check, no `guard` field. | `guard`, `guardrails.js resolvePolicy`, `inScope`, `scopeTier` |
| J4 | A `GuardrailBlock` → a usage line `{ keyId, provider, model, kind, format, stream, client?, local?, ms, status: 400, error: 'blocked by guardrails', guard: { action: 'blocked', hits } }` (no tokens, no `routedTo`, cost 0), then 400 `{"error":{"message":<blockMessage>,"type":"invalid_request_error","code":"guardrail_blocked"}}` with `content-type: application/json`, `cache-control: no-store` and CORS. A stream request gets the same JSON 400, not an event stream. | `guard`, `fail`, contract case "a blocked stream is a JSON 400" |
| J5 | Any other throw out of `checkBody` (a check in `redact`/`block` mode, the walker) → usage line `{ …same base, status: 500, error: 'guardrails failed' }` and 500 `{"error":{"message":"guardrails failed","type":"server_error"}}`. | `guard` |
| J6 | `redact`/`log`: the call goes on with `r.body`; the line of the attempt that ends the call carries `guard: { action, hits, error? }` (`error: true` when a check in `log` mode threw). A target that failed (599) carries no `guard`. | `guard`, `proxyChat` (`sent`), G8 note |
| J7 | What JS records for a guardrail outcome on a chat call is **only the usage line**. No governance audit entry; the `guardrail_block` monitor and the Overview counts read the usage log (`server.js guardBlocks` → `gateway.usage.recent(2000)`; `usage.summary` counts `guard.action`). The OTel span is made from the usage record by `otel.wrapUsage`. | `server.js`, `lib/monitors/rules.js`, `lib/gateway/usage.js` |
| J8 | Budgets: `server.js` always passes `budgetGuard` (wrapping `lib/cost/guard.js`), so `checkBudget` (a key's `budgetUSD` against `usage.monthSpend`) never runs in the product. A key's `budgetUSD` reaches the guard as a legacy **key budget** with `enforce: 'block'` (`budgets.js legacyBudgets`). Spend is the ledger plus every call recorded since the last pass: `usage.record` → `budgetGuard.record` → `budget-service.record`, synchronously, so the very next call is refused. | `server.js:563`, `lib/cost/budgets.js:364`, `budget-service.js record` |
| J9 | The guard fails open: a budget with `no_data`, a list that does not read or a guard that throws lets the call through (reason kept for the status). Refusal: 402 `{"error":{"message":"budget \"<name>\" is used up for this <period>","type":"insufficient_quota"}}`, **no usage line**. | `lib/cost/guard.js`, `allowedTargets` |
| J10 | `limitedKey(k)`: `k.budgetUSD != null`, or a blocking budget scoped to the key or to its cost center. Then `targetsFor` keeps private targets and priced ones (`usage.hasPrice(t.name)`); none left → 402 `this key has a budget, and <model> has no known price; add one under Settings → Model prices` (`insufficient_quota`), no usage line. | `limitedKey`, `targetsFor`, `guard.js keyLimited` |
| J11 | The Go side already has: `ipc.Decide` (pass / refuse / unchecked, shared fixtures with `lib/ipc/budget.js`), `UsageReport.Budget = "unchecked"`, `UsageRecord.Guard`, the supervisor's snapshot push every 1 s, its count of unchecked calls stored by the ingest, its alert (log line and `alert` in `/api/gateway/status`, at most once a minute). | `gw/ipc/budget.go`, `messages.go`, `lib/ipc/supervisor.js` |
| J12 | Today the engine hands off every call a check applies to (`GuardActive`) and every call a blocking budget reaches, a key with `budgetUSD`, or a stale snapshot. | `gw/engine/chat.go PlanChat` |
| J13 | The engine's usage line is sent with `Outbox.AppendSend(…, Channel.SendLine)`: the append and the **blocking write to stdout** under the outbox's send lock. A control plane that stops reading (hung) fills the pipe (64 KiB on macOS and Linux, less with `io.Pipe` in tests) and from then on every go-served call waits in `emit`, before its answer is written. | `gw/cmd/zaafpulse-gw/main.go appendAndSend`, `gw/ipc/outbox.go AppendSend`, `channel.go SendLine` |
| J14 | When the control plane dies, the gateway's stdin closes and it exits (P3 D1). "IPC down" with the gateway still serving is therefore a **hung** control plane (stopped, swapping, a long GC or a blocked event loop): no snapshot, no reads. | `main.go serve` (EOF → `drain`) |

## Decisions

Each one line, with the evidence that settled it.

1. **No guardrail on model output, whole or streamed.** The JS gateway has none (J1); adding one would make the go
   engine differ from js on every answer and break the byte-exact rule of S2/S3. A model-output guard is a new
   product feature (owner question 1).
2. **The checks run on proxyChat's upstream body**: the client body plus the `stream_options` merge, before the model
   is replaced and `AdaptChat` runs (J3). `model` is structural, so the order cannot change a hit; the provider body is
   then built from the checked body exactly as now.
3. **Order on the go path is the JS order**: rate limit (429) → price rule (402) → budget (402) → guardrails (400/500)
   → provider (J2). All of it is in `Serve`, after `Take`, before `Upstream.Call`/`Start`, and before `track()` for a
   stream, so a refusal of a stream is a JSON answer (J4).
4. **`PlanChat` stays pure and cheap.** It only records *that* a check applies (`Plan.Policy`) and keeps the upstream
   body (`Plan.pending`); `State.Guard(p)` runs the checks and finishes the provider body, and `State.Budget(p, …)` makes
   the budget decision. Reason: the shadow plans on the forwarding path of a js-served request; the guard (up to
   seconds of CPU at 20 MB) must not run there (Decision 18).
5. **Policy**: `config.push` `gateway.guardrails` (already cleaned by `push.js globalPolicy`) with the key's own modes
   from `keyAuth` (`GatewayKeyAuth.Guardrails`); the key's scope is ignored, as `resolvePolicy` does. One function
   `PolicyFor` builds the `guard.Policy`; `GuardActive` is rewritten on top of it. No IPC change.
6. **The engine's JSON tree is converted to `jsv` in memory**, not re-parsed: `engine.Value` and `jsv` both hold
   doubles, strings and ordered keys, and the engine's body is already the re-serialized one (duplicate keys resolved as
   `JSON.parse` does) and valid UTF-8 (invalid UTF-8 is `ErrUnsupported` → hand-off). This closes stream B note 18 M1
   and M2 for the request path. The checked tree is converted back only when the action is `redacted`.
7. **Byte-exact refusals**: 400 `guardrail_blocked` (J4), 500 `guardrails failed` (J5), 402 budget and 402 price (J9,
   J10), with the same headers as every engine answer (`answerHeader`). Proven by fixtures the real JS writes
   (Decision 20), not by hand-written expectations.
8. **Any error from `CheckBody` that is not a `*guard.Block` is a 500 refusal** (entry condition 4): a detector that
   failed in `redact`/`block` mode, or anything unexpected. Never a pass.
9. **An overrun (`*Block{Overrun}`) is a 400 `guardrail_blocked`** with the library's message and `hits: []` in the
   line. JS has no limit and answers such a body slowly: the recorded Go-only deviation of notes 13 and 26. The engine
   logs the overrun (check name only).
10. **`redact`/`log` outcomes go in the usage line** (`guard: { action, hits, error? }`) of the call's last line, as J6;
    a 599 line (the provider call failed) carries none. `Result.Failures` (log mode) is logged to the gateway's stderr
    by check name and count, at most once a minute per check (no detector text, no content); it needs no IPC message,
    because JS records nothing more than the line's `error: true` (J7).
11. **Monitors, Overview and audit need nothing new.** The ingest stores the engine's line through the JS gateway's own
    writer (`recordRaw`), so `guardBlocks` and the summary see go-served blocks (J7). The OTel span of a go-served call
    is P4f's (the ingest says "spans come from the Go engine itself (P4f)"); the guard fields of that span go with it.
12. **`guard.Prepare()` runs at gateway start, in a goroutine.** It compiles every pattern (0.2–0.5 s, note 22) without
    delaying `ready` (M7 ≤ 1 s); a guarded call that comes first waits on the same `sync.Once`, which `newCallBudget`
    calls before its deadline starts, so compiling never counts against a request. `guard.Prepared()` (new) lets a test
    see it ran.
13. **Budget decision on the go path = `ipc.Decide`** on the newest snapshot with `NormalizeModel(target.Name)`, the
    provider id and the key id (J2 `modelOf = t.name`): refuse → 402 (no line); pass → serve; unchecked (no snapshot,
    or older than `--stale-ms`, default 5000) → serve and mark. A budget the snapshot shows as `exceeded` is refused
    even when stale (`Decide` checks it first): S7's "a spent budget in the snapshot is still refused".
14. **The mark**: every usage line of a call that went (or tried to go) to the provider while unchecked is sent with
    `UsageReport.Budget = "unchecked"`; the 400/500 guard refusal lines are not marked (they were not metered). The
    ingest stores the mark and the supervisor counts and alerts (J11): nothing new on the JS side.
15. **The engine counts its own spend between snapshots.** JS refuses the very next call after a budget is used up
    (J8); a snapshot arrives every 1 s and the ingest lags the answer by a few ms, so without help the engine lets
    through every call of that window (the contract case "over the limit: a call may pass the amount, the next one is
    refused" fails under `ZP_ENGINE=go`). The engine keeps the cost of each line it emitted with `ts > snapshot.at`
    and, for every applicable budget with `spentUSD`/`amountUSD`, refuses when `spentUSD + own ≥ amountUSD`. The cost is
    the list cost of the line (what `Pricer.Cost` gives); a `billed` budget or a dated price version can differ until
    the next snapshot (≤ 1 s) corrects it. Lines older than the snapshot are dropped: the snapshot's spend already holds
    every line the ingest stored before it was taken (no double count; a line still in the pipe when the snapshot is
    taken is missed for one interval, recorded).
16. **The price rule** (J10) is ported: a key is limited when it has `budgetUSD`, or a blocking budget of the snapshot is
    scoped to its id or to `keyCenter[id]`; a limited key's target must be private (`TierOf`) or priced
    (`Pricer.Key(target.Name) != ""`), else 402 with the JS message. No snapshot: only `budgetUSD` limits, as JS before
    the cost guard exists.
17. **A per-key budget needs no path of its own**: it is a legacy key budget in the snapshot (J8), its spend is fed by
    the ingest (`afterUsage` → `budgetRecord`, P4b) plus Decision 15. `checkBudget` is not ported (never runs, J8).
18. **The shadow compares guarded calls off the forwarding path.** `shadowRead` plans and decides the budget (cheap)
    before forwarding; the compare goroutine runs `State.Guard` after the control plane answered. Redacted and logged
    calls are compared by the provider request body (JS sends the redacted body) and `usage.guard` (already in
    `usageFields`). Refusals (400, 500, 402) are compared by status, body and line: the JS capture gains the client body
    and the refusal's usage line (`shadow.noteClient`, `shadow.noteUsage`). A budget disagreement (one side 402, the
    other not) is `skipped` with reason "budget: the go engine decided on its snapshot", because JS decides on live
    spend and the snapshot lags by up to 1 s (Decision 15 holds only for what the engine itself served); the switch
    rule does not count `skipped`. An overrun is a real `diff` (Decision 9).
19. **The request path never waits for the control plane (S7).** `emit` stores the line in the outbox (durable, as now)
    and hands it to **one sender goroutine** that writes to stdout in seq order; the caller never blocks on the pipe
    (J13). The sender keeps an in-memory queue (4,096 lines); when it overflows, it reads the rest from the outbox
    segments (`Outbox.ReadFrom`, without the send lock) once the queue is drained. The handshake replay is unchanged and
    runs before the sender starts. The control plane still receives every event in seq order, once (the ingest's
    duplicate and gap rules hold). Without this, S7's kill-IPC test cannot pass under load.
20. **Fixtures from the real JS**: a new generator `scripts/fixtures/gen-chat-guard.js` writes
    `gw/testdata/chat-guard/*.json` with `scripts/fixtures/lib.js writeCases` (secret values encoded, scanner prefixes
    escaped). Each case runs `lib/gateway` `createGateway` in process with a policy, keys, and a `budgetGuard` made by
    `lib/cost/guard.js createGuard` over a fake budget service; the same service gives the Go side its snapshot through
    `push.js snapshotPush`. A separate generator keeps `gen-chat.js` (which P4d extends) untouched.
21. **S7 kill-IPC test, two levels.** (a) Go, on all four OS (`gw/cmd/zaafpulse-gw`): the gateway driven over pipes;
    the control plane "hangs" (stops reading and sending); `--stale-ms 300`; 300 metered calls to an unbudgeted model
    answer within 10 s; a call to a model whose budget the last snapshot shows exceeded gets 402; then the control
    plane reads again and gets the 300 reports in seq order, once, each `"budget":"unchecked"`. (b) Node and Deno,
    macOS and Linux (`test/front-budget-hang.test.js`): the real server with chat on go, `SIGSTOP` of the control plane
    for 6 s, the same calls through the main port, `SIGCONT`, then the stored lines carry `budget: 'unchecked'` and
    `/api/gateway/status` shows the supervisor's alert. Windows has no `SIGSTOP`; (a) covers it.
22. **Hand-offs removed**: "budget: the key has a monthly budget", "budget: no fresh snapshot", "budget: a blocking
    budget applies (P4e)", "guardrails: a check applies (P4e)". **Kept**: access (Host, client, browser and proxy
    headers), method, key, body (empty, over 20 MB, not an object, `ErrUnsupported`), model (missing, agent/collab,
    route, no usable target, private-only key), provider not OpenAI format (P4d), OAuth2/service account (P4g), no
    credential, token saver with tool results, a `stream_options` JS would spread into indexed keys.
23. **No extra concurrency limit for the checks.** The step budget bounds a call to about 3.8 s of CPU on the Ubuntu
    runner (note 22); the engine serves loopback clients only (P4b). Heap per guarded 20 MB call is measured in a test
    (bound: 256 MB for the checks plus the two trees); the soft memory limit default stays the owner's call (question 2).
24. **M3/M4 on the external tier are recorded, not measured here.** With P4e the engine serves calls to external
    providers under the default policy (secrets `redact`, injection `log`); the bench's `ZP_BENCH_TIER=external` is the
    measure. The owner deferred every quiet-machine benchmark to the program's final step (2026-11-16): the results doc
    records the requirement (M3 p95 ≤ 5 ms and M4 500 streams, p95 ≤ 10 ms, RSS ≤ 80 MB, external tier, guard on), and
    the CI `m4-bench` job's run on the branch is kept as a reference only.
25. **A key or budget change reaches the snapshot before the save answers.** Decision 15 counts the engine's own spend
    only against budgets the snapshot already lists; a budget saved a moment ago (a new key with `budgetUSD`, a budget
    saved in Cost Control) would be missing for up to 1 s, and the second call of the contract cases "key budget: … at
    the limit is 402" and "a blocking key budget saved in Budgets" would pass on go where JS refuses. So the supervisor's
    reload sends a fresh `budget.snapshot` **before** `config.push` and `secrets.handoff` (the gateway applies messages
    in order, so the health that names the new generation comes after the snapshot is in force), and a budget save or
    delete in the cost API triggers that reload and waits for it (`front.push()` then `front.synced()`, as a key save
    does since P4b). No IPC message changes.

## Design

### D1. Planning (`gw/engine/chat.go`)

`PlanChat` keeps every check before the provider format test as now. After it has the target, the credential and the
upstream body `out` (with the `stream_options` merge):

```
pol := PolicyFor(s.Gateway.Guardrails, key)
if checksApply(pol, ScopeTier(t.Provider, t.Model)) { p.Policy = &pol; p.pending = out }
else { p.Body = finishBody(t, out) }        // model, AdaptChat, Stringify: the P4b/P4c path, unchanged
```

The four budget and guardrail hand-offs go (Decision 22). New `Plan` fields: `Policy *guard.Policy`, `pending *Value`,
`Unchecked bool`, `Failures []guard.Failure`, `Overrun string`.

### D2. The checks (`gw/engine/guard.go`, new)

`State.Guard(p, ms) *Answer` runs `guard.CheckBody(toJSV(p.pending), *p.Policy, "openai-chat")`:

- `*guard.Block` → `refusal(p, 400, msg, "invalid_request_error", "guardrail_blocked")` with the line
  `{ base without routedTo, status 400, ms, error "blocked by guardrails", guard {blocked, hits} }`;
- another error → `refusal(p, 500, "guardrails failed", "server_error", "")` with `{ …, status 500, error "guardrails
  failed" }`;
- otherwise the body (converted back only for `redacted`) goes through `finishBody`, and `p.Base.Guard` is set when
  the action is not empty; `p.Failures` keeps `Result.Failures`.

`checkBody` is a package variable (`= guard.CheckBody`) so a test can make it fail (Decision 8). `Failed` clears
`Guard` on the 599 line (J6).

### D3. Budgets (`gw/engine/budget.go`, new)

`State.Budget(p, now, staleMs, own []Spent) (*Answer, bool)`: price rule (Decision 16) → `ipc.Decide` (Decision 13)
→ own spend (Decision 15). `Engine` holds `spend *Spend` (a mutex and a slice of `{ts, in ipc.CheckInput, usd}`),
`staleMs` (from `--stale-ms`), and adds each emitted line with `cost > 0`. `ipc.AppliesTo(b, in, centers)` is
`applies` exported with `Decide`'s key and source rules, so the engine and `Decide` cannot drift.

### D4. Serving (`gw/engine/serve.go`)

```
Take → (429)
a, unchecked := s.Budget(&p, now, e.staleMs, e.spend.Since(s.SnapshotAt)) → a: write 402, no line
p.Unchecked = unchecked
if p.Policy != nil: a := s.Guard(&p, elapsed) → a: emit a.Usage (no mark), write; return
log p.Failures / p.Overrun
stream → track → serveStream (its line carries the mark)
else Upstream.Call → Respond/Failed → emit with the mark → write
```

`emit` gets `&ipc.UsageReport{Record: rec, Budget: mark(p.Unchecked)}`; every emitted line with `Cost > 0` goes to
`e.spend`.

### D5. The sender (`gw/cmd/zaafpulse-gw/sender.go`, new; `gw/ipc/outbox.go`)

`Outbox.ReadFrom(from, fn)` is `Replay`'s loop without the send lock (segments are append-only; a line not yet ended
by `\n` is not read; a pruned segment is skipped). `ipc.SeqOf(line)` is `seqOf` exported. The gateway's `emitUsage`
(and the synthetic emitter) call `box.AppendSend(typ, body, ts, out.enqueue)`: still one lock for append and hand-over,
so lines reach the queue in seq order; `enqueue` never blocks. The sender goroutine sends queued lines with
`seq ≥ next`; after an overflow it drains the queue and reads `ReadFrom(next)`. It starts right after the handshake
replay with `next = box.Next()`, and stops in `drain` after a 1 s grace (unsent lines stay in the outbox and go out on
the next start, as now).

### D6. Shadow (`gw/engine/shadow.go`, `lib/gateway/shadow.js`, `lib/gateway/index.js`)

- JS: `shadow.noteClient(body)` from `fail` and `shadow.noteUsage(entry)` from `guard`'s refusal paths; `withShadow`'s
  `finally` stores `client: { status, body }` and `usage` when it has them (a refusal leaves both; a 402 leaves a body
  and no usage).
- Go: `shadowRead` decides the budget before forwarding (`State.Budget` with no own spend: in shadow mode the engine
  served nothing); the compare goroutine runs `State.Guard` and then `Compare`. `Compare` gains a refusal branch:
  status, body (when captured) and the line's fields, or the 402 lag rule (Decision 18).

### D7. Tests and fixtures

| Level | What | Where |
|---|---|---|
| Fixtures (JS truth) | redact, log, block, two-check block, key override, scope external/all, private target unchecked, stream redact and block, length-neutral redaction (note 27), data URL skipped, budget 402, price 402, key budget pass | `scripts/fixtures/gen-chat-guard.js`, `gw/testdata/chat-guard/`, `gw/engine/chat_guard_test.go` |
| Go unit | fail-closed 500, overrun 400, Failures logged once a minute, 599 without guard, own spend refuses the next call, stale → unchecked mark, exceeded refused when stale, hand-off list | `gw/engine/guard_test.go`, `budget_test.go`, `chat_test.go` |
| Go process | hung control plane (Decision 21a), sender order and overflow, `Prepare` at start | `gw/cmd/zaafpulse-gw/hang_test.go`, `sender_test.go`, `gw/ipc/outbox_test.go` |
| Contract | `test/contract/guardrails.test.js` and `budgets.test.js` chat cases assert the go engine served them under `ZP_ENGINE=go`; `ZP_ENGINE=shadow` ends with diff 0 | `test/contract/` |
| End to end | hung control plane with `SIGSTOP` (Decision 21b) | `test/front-budget-hang.test.js` |
| Heap | a guarded 20 MB body through `PlanChat` + `Guard`: peak extra heap bounded | `gw/engine/guard_heap_test.go` |

## Exit criteria (P4e, slices S6 and S7)

- S6: every `gw/testdata/chat-guard` fixture passes byte for byte (provider request, client answer, usage line); the
  guard library fixtures (`gw/testdata/guard`) stay green; the contract guardrail cases for OpenAI chat are served by
  the go engine under `ZP_ENGINE=go`; shadow diff 0 under `ZP_ENGINE=shadow`.
- S7: the kill-IPC tests (Decision 21) pass: metered calls pass while the control plane hangs, each stored with
  `budget: 'unchecked'`, the supervisor alerts, an exceeded budget in the snapshot is still refused; the budget contract
  cases pass under `ZP_ENGINE=go`.
- The full CI matrix is green; `gofmt`, `go vet` (also `GOOS=windows`), `go test -race` clean.
- The M3/M4 external-tier requirement is recorded in the results doc for the final quiet-machine step (Decision 24).

## Out of scope

- Model-output guardrails (Decision 1, owner question 1).
- `/mcp` `tools/call` arguments and results (S9), the other formats' guard paths (`/v1/messages`, `/v1/responses`,
  Gemini, embeddings: P4d brings those routes to the engine and reuses `State.Guard` with their format).
- Audit and trace events from Go (P4f); fail-closed budgets (a later opt-in, §11.6).

## Open questions for the owner

1. **Model-output guardrails.** JS checks only what goes to a provider. Should a later phase (after G3) add a check on
   answers (and on streams, where only `log` or a cut is possible)? Default here: no.
2. **The soft memory limit** (`gateway.memLimitMB`, default 64, P4c note m6). With P4e the engine serves guarded bodies
   up to 20 MB, whose checks may use up to 256 MB of heap. Raise the default to 128? Default here: keep 64; Task 7
   measures and reports.
3. **An audit entry for unchecked windows.** Design rule 2 says the mark goes "in the usage report and the audit".
   Today the usage line and the supervisor's alert carry it. Should the supervisor also append one governance entry per
   alert window (at most once a minute)? Default here: yes, in P4f, which owns audit events from Go; P4e adds nothing
   to the audit.
4. **Who owns the sender (Decision 19).** It changes how the gateway sends every outbox event, which P4f also touches.
   Default here: P4e lands it first (it is S7's riskiest part); P4f builds on it.

## Files that overlap other P4 branches

- **P4d (translate wiring):** `gw/engine/chat.go` (`PlanChat`, the format hand-off), `gw/engine/serve.go`,
  `gw/engine/shadow.go` (`Compare`), `gw/engine/chat_test.go` (`TestChatHandoffs`), `lib/gateway/index.js` (`guard`,
  `fail`), `test/contract/guardrails.test.js` (the block-shape cases of the other formats), the program plan.
  `scripts/fixtures/gen-chat.js` is deliberately not touched (Decision 20).
- **P4f (usage, audit and trace from Go):** `gw/cmd/zaafpulse-gw/main.go` (`emitUsage`, `appendAndSend`, `drain`),
  `gw/ipc/outbox.go` (`ReadFrom`, `SeqOf`), `gw/engine/serve.go` (the emit sites, where spans will be made),
  `lib/ipc/supervisor.js` (`sendReload` order, Decision 25; and the audit entry only if owner question 3 is answered
  for P4e), `lib/gateway/shadow.js`, `server.js` (the cost API's reload hook), `lib/cost/api.js`.
