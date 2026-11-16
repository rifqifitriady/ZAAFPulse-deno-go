# P4f (slice S8): usage, audit events and trace spans over IPC — design

Date: 2026-11-16. Repo `ZAAFPulse-deno-go`. Branch `migrate/p4f-ipc-events`, cut from `migrate/deno-go` at `ae7b9ab`
(P4c merged). Documents only: this round writes the spec and the plan; nothing is implemented.

Inputs:
- Program plan `docs/migration/2026-11-10-deno-go-migration-plan.md` §3 (rule 1: one audit writer), §5 P4 row S8
  ("Ledger and audit identical to JS for the same run"), §8 (risk "a two-writer audit chain or ledger": "S8 equality
  test").
- P3: spec `docs/superpowers/specs/2026-11-11-p3-ipc-contract-design.md` (D3 messages, D4 outbox and marks), results
  `docs/migration/2026-11-11-p3-results.md` (strict sinks, the 4 MiB tail, `audit.event {log}`, `trace.span` as the
  stored record, the known minors).
- Stream C: plan `docs/superpowers/plans/2026-11-12-p4-lib-ipc-auth.md` and
  `docs/migration/2026-11-12-p4f-mark-durability.md` (owner decision: no fsync; a killed process is the target).
- The gaps accepted until P4f: `docs/superpowers/plans/2026-11-12-p4b-passthrough.md` decision 15 and owner answer 2
  (per-engine rate-limit windows; go-served chat writes no trace spans; go refused while OTLP export is on), P4c plan
  decision 18 and `docs/migration/2026-11-13-p4c-results.md` "Known gaps" (a gateway crash loses the usage line of each
  in-flight go stream), the P4b stream A re-review minors n1 and n2 (`.superpowers/sdd/p4s1/progress.md` line 21).
- The P4a minors deferred to P4f: `docs/migration/2026-11-12-p4a-results.md` T1-c, T2-f, T6-2.
- Code read for this spec: `lib/otel.js`, `lib/trace/recorder.js`, `lib/trace/store.js`, `lib/trace/gateway.js`,
  `lib/trace/model.js`, `lib/audit.js`, `lib/governance/audit.js`, `server.js` (`govRecord`, `frontIngest`, the trace
  store and recorder, `frontRouting`), `lib/gateway/index.js` (`chatCompletion`, `authed`, `govAudit`,
  `ingestUsage`), `lib/gateway/keys.js` (`rateLimit`), `lib/ipc/{ingest,server-sinks,session,marks}.js`,
  `lib/front/{index,push}.js`, `gw/engine/{serve,chat,stream,keys}.go`, `gw/ipc/{outbox,messages}.go`,
  `gw/cmd/zaafpulse-gw/main.go`, `gw/guard/secrets.go`, `gw/guard/jsre`, `ipc/v1/*.schema.json`,
  `test/contract/harness.js`.

All Deno work follows the `deno` and `migrate-to-deno` skills; all Go work follows `go-stack-defaults` with the P0
rule on top: **Go is standard library only**.

## Goal

A call the go engine answers leaves the control plane's stores exactly as the same call answered by the JS gateway
does: the same usage line (`gateway-usage.jsonl`, and so the cost ledger), the same governance chain and MCP audit
log, the same trace store records and the same OTLP export. A gateway crash in the middle of a call leaves exactly one
usage line for it, never two. A key's requests-per-minute window is one window across both engines. A test runs the
same calls through `js` and through `go` and diffs every store.

## Facts that shape the design

1. **What JS records for a gateway chat call** (`lib/otel.js`):
   - every traced request (`/v1/*`, `/v1beta/*`, `/mcp`, `/a2a`, `/api/*`) runs inside `wrapHandler`
     (`otel.js:1202-1220`), which makes a request context when tracing is live (OTLP export on, or the local trace
     store on: `live()`, `otel.js:926`);
   - at the response's `finish` or `close` it builds a **SERVER span** `"{method} {route}"` with `http.request.method`,
     `http.route`, `url.path`, `url.scheme`, `network.protocol.version`, `http.response.status_code`, and
     `user_agent.original` when a User-Agent came (`otel.js:1159-1180`). A status of 500 or more fails it
     (`error.type` = the status, no message). "Model endpoints always get their span" (`format` set for
     `POST /v1/chat/completions`), so a 401, a 429 or a 400 on that path has one too;
   - each `usage.record` (wrapped by `wrapUsage`, `otel.js:1660-1673`) adds the request's `traceId` to the usage entry
     when the request is sampled and tracing is live (`usageTraceId`, `otel.js:1465-1474`), then builds a **CLIENT
     span** `"chat {model}"` (`modelSpan`, `otel.js:1258-1311`), child of the server span, with the GenAI attributes,
     `server.address`/`server.port` from the provider's `baseUrl` in config.json, the cost, key and provider ids;
   - a chat client span waits for the response to end (`ctx.pending`), then gets `gen_ai.response.id` and
     `gen_ai.response.finish_reasons` from `responseObserver`, which reads the bytes the gateway wrote back
     (`otel.js:759-860`, `finishModelSpan` `otel.js:1312-1342`);
   - an incoming W3C `traceparent` is continued (the server span's parent is remote; `tracestate` passed on when sane);
     an unsampled one (`-00`) means no span and no `traceId` on the usage line;
   - the local store keeps each span as `toRecord` makes it (`lib/trace/recorder.js:52-71`): `{t, s, p, n, k, st, d,
     ok, err?, src: "gateway", svc: "zaafpulse", a}`, microseconds, content keys dropped, `err` masked by
     `maskSecrets` (`lib/trace/model.js:18`, which is `lib/signals.js secretSpans`) and cut to 200 UTF-16 units. The
     recorder buffers and writes once a second; a span while the store is off is dropped.
   - Order: the usage line is written during the call (`lib/gateway/index.js:1030`, before `res.end`; for a stream
     `:1124`, right after `res.end`); both spans are recorded when the response ends, the server span first.
2. **What the Go engine records today**: one usage line per call it answers (`gw/engine/serve.go`), no `traceId`, no
   span. `routing()` keeps every class on `js` while OTLP export is on (`lib/front/index.js:46`), because a go call
   would be missing from the export (P4b decision 15).
3. **Audit for a gateway chat call**: none. `lib/gateway/index.js` calls `govAudit` only for key changes
   (`:127`, `:146`), run approvals and cancels (`:479`, `:1955`), knowledge ingest (`:2007`) and session deletes
   (`:2037`); `authed` (`:620-636`) only sets the actor. `/v1/chat/completions` writes no governance entry and no MCP
   audit entry, for a served call, a refused key, a 429 or a provider error. The P3 facts said the same ("plain `/v1`
   calls write no governance-chain entry").
4. **The ingest's trace path assumes the store is on**: `serverSinks().writeSpans` throws "the local trace store is
   off" when `traceStore` is null (`lib/ipc/server-sinks.js`), which the ingest treats as a store failure (no ack, the
   gateway restarts, the replay fails again): a restart loop the first time Go sends a span with tracing off. It also
   writes spans to the store only: nothing reaches the OTLP exporter.
5. **Go encodes `SpanRecord.A` as `map[string]any`** (`gw/ipc/messages.go:483`): `encoding/json` sorts map keys, JS
   keeps insertion order. A stored line written from a Go span would differ from the JS line in key order.
6. **Money**: `usage.report` carries `cost` in nano-USD; the ingest stores `fromNano(cost)` (`lib/ipc/ingest.js`).
   The JS gateway computes its float cost directly, so the two floats can differ in the last bits for the same call;
   the IPC contract's unit of equality is the nano-USD (P3 results §2, "Money is integer nano-USD").
7. **The crash gap**: `Serve` emits the usage line after the provider answered; `serveStream` emits it when the stream
   ends (`gw/engine/serve.go`). A gateway killed in between leaves none (accepted until P4f, owner answer 2 of
   2026-11-13). The same window exists for a non-streaming call (the provider call can take a minute).
8. **Rate limit**: JS counts a sliding one-minute window per key in `keys.rateLimit` (`lib/gateway/keys.js:180-190`),
   called from `authed` (25 call sites) after the key and scope checks, synchronously. Go counts its own
   (`gw/engine/keys.go RateLimiter`) for the calls it serves; `Serve` hands everything else to JS, which counts again
   only what it serves. In front mode every external request passes through Go; in-process callers (`llm()`, the
   judge) use no key and no window.
9. **Ports the engine can reuse**: `gw/guard.SecretSpans` (the exact port of `lib/signals.js secretSpans`, under a
   `jsre.Budget`), `gw/guard/jsre` (JavaScript regular expressions with JS semantics, UTF-16), `engine.Value` (an
   ordered JSON object with JS number formatting), `jsSlice` (UTF-16 slicing).
10. **The front speaks HTTP/1.1 only** (`gw/front/front.go:172`, `NextProtos: ["http/1.1"]`), like Node's
    `http`/`https` server: `network.protocol.version` is `1.1` or `1.0` on both.

## Decisions

Each decision is taken without asking (brainstorming, design part); the evidence follows each one. The few real owner
questions are listed at the end.

**D1. The spans of a go-answered request are the JS gateway's spans for the same request.** For every request the go
engine answers itself (a served call, its own 429, and later the guardrail blocks of P4e and the translated calls of
P4d), it emits one SERVER span `POST /v1/chat/completions`, and for each usage line it emits one CLIENT span
`chat {model}`, child of the server span, in that order (server, then client), after the response's last byte. It
emits none when tracing is not live (D8) or the request carries an unsampled `traceparent`. A request the engine hands
to the server gets its spans from the server, as today. Evidence: Fact 1; `lib/gateway/index.js:1030`, `:1124`.

**D2. Go builds the stored record; the control plane stores it unchanged.** The engine ports the span building
(`wrapHandler`'s server span, `modelSpan`, `finishModelSpan`, `toRecord`) and is proven by fixtures the real
`lib/otel.js` writes (`scripts/fixtures/gen-spans.js`, the pattern of every port since P4b). `trace.span` keeps the
shape P3 froze. Rejected: sending the call's facts and letting the control plane build the spans with `lib/otel.js`
(equal by construction, but a second fact message that duplicates `usage.report`, the gateway's telemetry left in the
JS hot path that G3 deletes, and `modelSpan` reading the provider list at ingest time instead of call time).

**D3. Attribute order is part of the record.** `SpanRecord.A` becomes `json.RawMessage` in Go (the exact bytes, in
the order they were built), and the engine builds `a` with `engine.Value` in JS insertion order. A stored line from a
go span is then byte-equal to the JS line, apart from ids, times and the `ipc` mark. Evidence: Fact 5. The schema
does not change (`a` is still an object).

**D4. Provider-derived attributes are computed by the control plane and pushed.** `gen_ai.provider.name`,
`server.address` and `server.port` come from the provider's preset and `baseUrl` through WHATWG `new URL`
(`otel.js:1260-1275`), which `push.js` normalizes before pushing (`lib/front/push.js:37-48`). A new exported
`providerSpanAttrs(provider)` in `lib/otel.js`, used by `modelSpan` itself, is pushed per provider as the optional
`span` object of `config.push` providers. No WHATWG host parser in Go; the value is JS's by construction.

**D5. The response observer is ported whole.** `gw/engine/observer.go` is `responseObserver(maxChars, false)`: JSON or
SSE, the four formats (OpenAI chat, Anthropic, Responses, Gemini), the 1 MiB caps counted in UTF-16 units, the
response id and the de-duplicated finish reasons. It never keeps text (D10 keeps content capture on js). P4d's
translated answers need no change here. Evidence: `otel.js:759-860`.

**D6. Trace context and ids as JS makes them.** `traceparent` and `tracestate` parsing are ported (`parseTraceparent`,
`cleanTracestate`, `otel.js:74-87`); ids are `crypto/rand` hex, never all zero. The usage line carries `traceId` under
the rule of `usageTraceId`: sampled and tracing live. Evidence: Fact 1.

**D7. Error text masked as JS masks it.** The client span's status message is `mask(e.error || "HTTP {status}")`
capped at 500 UTF-16 units, where the engine's `mask` is the handoff's mask list (as `e.error` already is) followed by
`scrub` (`GENERIC_SECRETS`, `otel.js:101-112`) run through `gw/guard/jsre` for exact JS regex semantics (RE2's `\s` and
`(?i)` differ from JS's). The stored `err` is `maskErr`: `guard.SecretSpans` replaced by `‹label hidden›`, cut to 200
units, a trailing high surrogate dropped. Evidence: `lib/trace/recorder.js:47-51`, Fact 9.

**D8. Tracing live is pushed.** `config.push` gains the optional `trace: { spans: boolean }`, true when the local
store exists and `tracing.enabled` is not `false`, or OTLP export is on (the JS `live()` rule). It is pushed with each
generation, on a save of the `tracing` or `otel` section (`server.js configChanged`), and by the minute check when it
differs from what was last pushed. With `spans` false the engine emits no span and puts no `traceId` on usage lines,
as JS does.

**D9. The ingest stores a span only where JS would, and never fails for "the store is off".** A `trace.span` is
written to the trace store strictly when the store exists and tracing is on; otherwise it is skipped and still marked
and acknowledged (the recorder drops such spans too). After the mark, a post-store hook hands the span to the OTLP
exporter when export is on (`otel.exportRecord`, best effort, never throws), once: a resend at or below the mark is a
duplicate and is not exported again. Evidence: Fact 4; the ingest's existing `afterUsage` hook pattern.

**D10. The OTLP refusal is lifted, except with `captureContent`.** `routing()` no longer keeps go classes on js because
export is on; it still does when export is on with `captureContent: true`, because the content attributes would carry
prompt and answer text through the outbox, which is a file on disk until acknowledged (the gateway never stores request
text today). The note says so. Owner question 2.

**D11. OTLP export of a go span equals the JS export.** `trace.span` gains the optional `otlp: { traceState?, remote?,
message? }`: the span's `tracestate`, whether its parent is remote (the server span of a request with an incoming
`traceparent`), and the status message (D7, up to 500 units; `err` keeps only 200). `exportRecord` rebuilds the
exporter's record losslessly: microseconds × 1000 are the nanoseconds JS had (`nowNs` and `msToNs` are whole
microseconds, `otel.js:96-97`), flags `0x101 | (remote ? 0x200 : 0)`, status ERROR with the message when `ok` is
false, UNSET otherwise, `dropped` 0 (a gateway span has at most 25 attributes).

**D12. n2 closed.** The routing reads OTLP export through `otel.status({ fresh: true })`, which bypasses the 2 s cache,
so the push on an `otel` save routes on the new value at once. With D10 the minute of lag n2 described applies only
to switching `captureContent`. n1 (the shadow slot leak) was closed in P4c (final review I1).

**D13. Audit: no audit event per go-served chat call, as in JS; one governance entry per unchecked-budget window
(corrected 2026-11-16, D24).** A go-served chat call, served or refused, writes no governance entry and no MCP audit
entry, exactly as the JS gateway (Fact 3); `audit.event` stays in v1 unchanged for the slices that produce one
(`/mcp tools/call`, S9, `log: "mcp"`, and any governance entry a later slice moves to Go). The one audit entry this
slice adds is written by the control plane itself, not sent by Go: the supervisor's unchecked-budget alert (D24). The
S8 equality test (D18) diffs the governance chain and the MCP audit log and proves zero extra entries on the go side for
its corpus, which has no stale snapshot; the D24 entry has its own test. The P3 minor "`audit.gap` over-counted while
the audit key is unreadable" stays deferred to S9, the first slice that sends governance events from Go.

**D14. The crash gap is closed by a call journal in the outbox folder.** `gw-outbox/calls.jsonl`, append only, same
writer lock and durability target as the outbox (a killed process, no fsync; the owner's decision of 2026-11-12):
- `{"o": <id>, "ts": <start ms>, "r": <base usage record>}` is appended when a go-served call starts (before the
  provider call);
- when its usage line is emitted, `Outbox.AppendSendNoted` holds the send lock, reads the seq the line will take,
  appends `{"c": <id>, "seq": <n>}` to the journal, then appends and sends the line;
- on start, before `hello`, `RecoverCalls` reads the journal: a call with no close, or with a close whose `seq` is not
  below the reopened outbox's `Next` (the line was never written), gets one recovered line appended to the outbox
  (with its own close noted first), and the journal is then emptied. A close with `seq < Next` means the line was
  written (in a segment, or acknowledged and pruned): nothing to do.

Exactly one line: a line is written only with its close noted before it, under the lock that also hands out the seq;
`Next` after a reopen is above every written seq and not above an unwritten one (`stream.json` is written on ack and
close only, never ahead of an append). Recovery notes its own closes, so a crash during recovery cannot recover twice.
The journal covers non-streaming calls too (Fact 7). The journal is emptied at runtime when it holds no open call and
has passed 64 KiB.

**D15. The recovered line.** The call's base record (key, provider, model, routed model, kind, format, stream, client,
local), `ts` = the call's start, `ms` 0, `status` 599, `error` `"the gateway stopped before the call ended"`, no
token counts (as the engine's own 599 line), cost 0, no `traceId` (no span was stored). The tokens are unknown: a stream's usage arrives in its last chunk. Owner
question 1.

**D16. One rate-limit window, replicated.** Each engine decides locally and synchronously, as today, and tells the
other each request it admits: `ratelimit.take {keyId, at}`, a control message in both directions (not an outbox event:
a lost take changes a window, never a ledger). Each side inserts the other's takes into the key's window in time order
(`keys.note`, `RateLimiter.Note`). A new gateway session is seeded with `ratelimit.window` (the newest `rpm` takes per
key, split to stay under 1 MiB per message), sent after `secrets.handoff` and before `budget.snapshot`, so the window is
applied before `ready`. Both sides admit at most a few requests over `rpm` when they admit the last slot within the
same IPC hop (P3 measured a round trip p95 of 0.09 ms). Rejected: a single owner. JS as owner puts the JS event loop on
the go engine's hot path (the reason the engine exists); Go as owner needs `authed` to wait on an IPC reply, which
makes `authed` and its 25 callers asynchronous. Owner question 3.

**D17. Money is compared at nano-USD.** The equality test compares `cost` and `zaafpulse.cost.usd` by `toNano`, and
the cost ledger's amounts after rounding to nano-USD. Evidence: Fact 6.

**D18. The S8 proof is one test that diffs every store.** `test/s8-equality.test.js` starts two servers with the
contract harness (`startServer`), one with `gateway.engine` `js`, one with chat and chat-stream on `go`, each on a
fresh data folder, each with OTLP export (JSON) to a receiver on `127.0.0.1:0` in the test, against the same fake
upstream. It runs one corpus through both, sequentially, each call with a fixed `traceparent` (or none, where the case
is about a fresh root). Then it normalizes and diffs:
- `gateway-usage.jsonl`: `ipc` dropped; `ts`, `ms`, `ttft` replaced by markers; `cost` by `toNano`; `traceId` by the
  case's trace;
- the cost ledger's rows (after one ledger pass on each server): amounts at nano-USD;
- the governance chain: `ts`, `hmac`, `prev`, `seq` and generated ids dropped; the entries as a list;
- `mcp-audit.jsonl`: the same rule;
- the trace store's records of the corpus' traces: `ipc` dropped, span ids renamed by order of appearance in their
  trace, `st` and `d` replaced by markers (a check keeps each child inside its parent);
- the OTLP spans received: the same renaming and time markers.
In go mode the test also asserts that every served case's usage line carries `ipc` (the engine answered).

**D19. The shadow does not compare spans.** The spans of a shadowed call are the server's; the engine's would-be spans
are proven by the fixtures (D2) and the equality test (D18). Adding spans to the shadow line would add ids and times
the compare must strip and no new evidence.

**D20. The P3/P4a minors that belong here.**
- T6-2 (the call after a gateway restart is checked for presence, not for exactly one line): the kill test asserts
  exactly one line, now also for the call that was in flight (D14).
- T1-c (no test for pruning during a replay): a test, with the journal's prune rule.
- T2-f (a failed mark write is logged once and the idle acker does not retry): the acker retries the mark write on its
  next tick while acks are held back.
- Deferred, with reasons: `audit.gap` over-count (D13, S9); `Replay` holds the send lock across blocking sends (by
  design, P4a E10: one lock keeps seq order); the 4 MiB tail bound (stream C's mark file is the remedy; the
  power-loss corner is the owner's no-fsync decision).

**D21. Measured cost of P4f on the hot path.** A go call now writes three outbox events (usage, server span, client
span) and two journal lines. M3 (non-stream overhead p95 ≤ 2.84 ms) and M4 (500 streams, overhead p95 ≤ 10 ms, gateway
RSS ≤ 80 MB) are re-measured on the private tier with tracing on (the default) and with OTLP export on, on the owner's
MacBook as in P4c; a regression beyond either target stops the task.

**D22. Schema bookkeeping.** `ipc/v1` goes from 15 to 17 schema files (`ratelimit.take`, `ratelimit.window`), with
fixtures; `scripts/build-deno.js` lists them; the `doctor` line becomes `Gateway IPC  v1, 17 schemas`. The additions
to existing messages are all optional fields (version rule).

**D23. The owner's answers (2026-11-16).** All three defaults are accepted: the recovered crash line is status 599
with cost 0 (D15); `captureContent: true` keeps the go classes on js (D10); the replicated rate-limit window is accepted
(D16). The questions are closed; see "Owner answers" below.

**D24. One governance entry per unchecked-budget alert window (owner decision, program plan §3 design rule 2: "marked
`budget_unchecked` in the usage report and the audit, and the dashboard raises an alert").** Today a stale snapshot
marks the usage line (`budget: "unchecked"`) and the supervisor logs an alert at most once a minute
(`lib/ipc/supervisor.js noteUnchecked`, `ALERT_EVERY_MS`), but nothing reaches the audit. From P4f, each time the
supervisor raises that alert it also calls `onUncheckedAlert({ calls, since, at })`, and `server.js` writes one
governance entry through `govRecord` as the system: `{ action: "gateway.budget_unchecked", outcome: "error", target:
{ type: "gateway" }, actor: { via: "system" }, calls, since }`, where `calls` is the number of unchecked calls stored
since the previous entry (this one included) and `since` the time of the first of them (ISO). One entry per alert
window, never one per call: a stale snapshot under load must not bury the chain (the reason `lib/users/refusals.js`
batches refused requests). A resend after a restart is a duplicate the ingest does not count, so it adds no entry. The
entry is written by the control plane, the chain's only writer (rule 1); Go sends nothing new. An entry that cannot be
written (the audit key unreadable) is the chain's own `degraded` case (counted in its next `audit.gap`); the alert and
the counter are unaffected.

## IPC v1 additions (additive only)

| Message | Change | Direction |
|---|---|---|
| `config.push` | optional `trace: { spans: boolean }` (D8) | cp → gw |
| `config.push` `gateway.providers[]` | optional `span: { name, address?, port? }` (D4) | cp → gw |
| `trace.span` | optional `otlp: { traceState?, remote?, message? }` (D11) | gw → cp |
| `ratelimit.take` (new type) | `{ keyId, at }` (D16) | both |
| `ratelimit.window` (new type) | `{ windows: [{ keyId, at: [ms...] }] }` (D16) | cp → gw |

`usage.report`, `audit.event` and the envelope do not change.

## Tasks

| # | Task | Exit |
|---|---|---|
| T1 | Span records in Go, proven by JS fixtures (D1–D7, D11): `gen-spans.js`, `gw/engine/spans.go`, `observer.go`, `traceparent.go`, `SpanRecord.A` as raw JSON, `trace.span.otlp` | every fixture byte-equal on Go; the generator gives the same bytes on Node and Deno |
| T2 | The call journal (D14, D15, T1-c, T6-2) | kill tests: a gateway killed during a non-stream call and during a stream leaves exactly one line, 20 seeded runs each; recovery idempotent |
| T3 | Control plane side (D8–D12, D4 push, D9 ingest, `exportRecord`, routing) | ingest tests for store on/off and export; routing tests; the session survives spans with tracing off |
| T4 | The engine emits spans and `traceId` (D1, D6, D8) for served calls, streams and 429s | contract suite in go mode with spans asserted; engine tests |
| T5 | One rate-limit window (D16) | a key with rpm 3 alternating js and go routes is refused on the fourth call on either; the window survives a gateway restart |
| T6 | The S8 equality test (D17, D18), run by the existing Node (four OS) and Deno (two OS) jobs, which take `test/*.test.js` | the diff is empty on Node and Deno |
| T7 | The acker retries a failed mark write (T2-f) | test: a mark file that fails once is written on the next tick, acks follow |
| T9 | One governance entry per unchecked-budget alert window (D24) | supervisor test: three unchecked calls in a minute give one `onUncheckedAlert`, the next window a second; server test: one `gateway.budget_unchecked` entry in the chain per window, `calls` and `since` right |
| T8 | Bench, results and docs (D21, D22) | M3 and M4 re-measured within target; `docs/migration/2026-11-16-p4f-results.md`; plan §5 S8 ticked |

Order: T1 → T2 (independent of T1; may run in parallel) → T3 → T4 → T5 → T6 → T7 → T8. Riskiest first: T1 (byte
equality across many JS semantics), T2 (exactly once across a crash).

Scheduling (coordinator, 2026-11-16): P4e (guard and budget wiring) is being implemented on `migrate/p4e-guard-wire`
and touches `serve.go`, `main.go`, `outbox.go` and `ingest.js`. T1 and T7 do not, and run now; T2–T6, T9 and T8 run
after P4e merges into `migrate/deno-go` and this branch merges it.

## Exit (plan §5 S8)

- [ ] Ledger and audit identical to JS for the same run: the S8 equality test's diff is empty on Node and Deno (usage
  log, cost ledger, governance chain, MCP audit log, trace store, OTLP export).
- [ ] A gateway crash mid-call leaves exactly one usage line (kill tests).
- [ ] One rate-limit window across engines.
- [ ] Go routes allowed while OTLP export is on (without `captureContent`).
- [ ] One governance entry per unchecked-budget alert window (D24).
- [ ] M3 and M4 still met with spans and the journal on.

## Out of scope

- Translated calls (P4d), guardrails and budgets in the engine (P4e), provider OAuth2 and service accounts (P4g),
  `/mcp tools/call` (S9): each emits through the hooks this slice adds (the span builder takes the path and format; the
  journal wraps any served call), and each slice adds its own cases to the equality corpus.
- Content capture through the gateway (D10, owner question 2).
- fsync anywhere in the chain (owner decision 2026-11-12).
- Spans in the shadow compare (D19).

## Overlap with the parallel slices

| File | P4e (guard and budget wiring) | P4d (translate wiring) | What P4f changes there |
|---|---|---|---|
| `gw/engine/serve.go` | the guard check and budget decision before the provider call, the blocked answer | translated answers and streams | span emission at the answer's end, journal open/close around the call |
| `gw/engine/chat.go` | `PlanChat` hand-offs for guardrails and budgets removed; usage `guard` field | `PlanChat` format hand-off removed, `AdaptChat` | none to `PlanChat`; `Plan` gains the trace context |
| `gw/engine/stream.go` | guard on streams | translated streams | none (the observer reads the bytes `serveStream` writes) |
| `gw/cmd/zaafpulse-gw/main.go` | `budget.snapshot` use, guard policy | – | journal recovery before `hello`, span emitter, `ratelimit.*` handling |
| `gw/ipc/messages.go`, `ipc/v1/*.schema.json`, `scripts/build-deno.js` | possible usage `budget` / guard fields | – | D22 additions |
| `lib/ipc/ingest.js` | the `unchecked` count and alert | – | the span store gate and the export hook |
| `lib/front/index.js` (`routing`) | guard notes | new go classes | the OTLP note |
| `lib/front/push.js` | guard policy and budget push | translate presets | `trace`, provider `span` |
| `server.js` | front wiring | – | sinks, push triggers, `ratelimit` hooks |
| `gw/guard` | its owner (P4e) | – | read only: `SecretSpans`, `jsre` |
| `test/contract/go-*.test.js`, `test/contract/upstream.js`, `test/contract/harness.js` | new cases | new cases | `go-spans`, `go-ratelimit`; the `slowwhole` model; `providerList` exported |

The engine's answer path gets one helper, `e.finish(w, r, ans)` (D1), that every answer the engine writes goes
through; P4e's blocked answer and P4d's translated answers use it and get their spans for free. Merge order: whichever
of P4e and P4f lands second rebases its `serve.go` hunks onto the helper; the coordinator decides.

## Owner answers (2026-11-16)

The three questions below were answered on 2026-11-16: every default is accepted (D23). They are kept as asked.

1. **The recovered line after a crash (D15)**: status 599, no token counts, cost 0. **Accepted.**
2. **Content capture (D10)**: with OTLP `captureContent: true`, go classes stay on js. **Accepted.**
3. **The replicated window (D16)**: a few requests over `rpm` are possible at the boundary. **Accepted.**

The unchecked-budget audit entry (D24) is an earlier owner decision (design rule 2), added to this slice on the same
day.
