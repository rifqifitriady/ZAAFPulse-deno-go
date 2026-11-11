# P3 IPC contract — design

Date: 2026-11-11. Repo `ZAAFPulse-deno-go` (public since 2026-11-11). Branch `migrate/p3-ipc-contract`, cut from
`migrate/deno-go` at `aa78352` (P2 merged, gate G2 met).

Inputs:
- Program plan `docs/migration/2026-11-10-deno-go-migration-plan.md` §3 (design rules), §5 P3, §11 (owner decisions:
  option A embedded sidecar, transport-agnostic messages, budgets fail open).
- `lib/types.js`: the JSDoc typedefs written in P0 for the shapes that cross the boundary.
- A map of the gateway request path made for this spec (2026-11-11), summarised under "Facts" below.

All Deno work follows the `deno` and `migrate-to-deno` skills (owner instruction).

## Goal

A frozen, versioned message contract between the Deno control plane and a Go gateway process (`zaafpulse-gw`), proven
on both sides by shared fixtures and by kill tests, before any gateway logic moves to Go (P4). P3 ships no change in
product behaviour: nothing in `server.js` starts the Go process yet.

## Facts that shape the design (gateway request path, 2026-11-11)

- **Usage reaches the ledger through a file.** `usage.record` (lib/gateway/usage.js:104) appends one line per attempt
  to `.data/gateway-usage.jsonl` synchronously; `cost.update()` reads it by cursor every 60 s. Right after the append,
  `budgetService.record` adds the call to the budget's "pending" spend; the no-double-count logic of
  `budget-service.js` `pass()` depends on that order.
- **Secrets are resolved on every upstream call**, uncached (providers.js:330, 420 → secrets.js:41-62; a keychain read
  is a synchronous `security` call). OAuth2 and Google service-account tokens are cached in memory and refreshed.
  Every resolved value of 6+ characters joins a mask set used on error text.
- **The budget check fails open today** (guard.js:66-106, budget-service.js:241-246, lib/gateway/index.js:209-233).
  It refuses only a budget whose state is `exceeded` and whose `enforce` is `block`. Separately, a key with a budget
  may use only priced models or private-tier providers (index.js:278-288), which fails closed with 402.
- **Per-process state in the hot path:** the per-key rate-limit window, the round-robin counter per route, provider
  cool-downs, the OAuth token cache, the in-memory spend map and the `recent` ring (2,000 entries) the monitors read.
- **Other callers use the same engine in process:** the eval judge, dataset replay, `llm()` and `embed()` for
  orchestrator, collab, agents and knowledge bases, `agent/*` and `collab/*` models, and `/mcp tools/call`, whose tool
  runs in the integrations registry.
- **Audit:** plain `/v1` calls write no governance-chain entry; MCP tool calls append to `.cache/mcp-audit.jsonl`;
  key, run and session actions append to the HMAC chain through `govRecord`.

## Decisions

**D1. Transport: the child's stdin and stdout, one JSON message per line (owner decision 2026-11-11).**
- Deno starts `zaafpulse-gw` with `Deno.Command` / `child_process.spawn` and pipes; the gateway reads messages from
  stdin and writes them to stdout. Its logs go to stderr, which the supervisor forwards to the server log with a `gw:`
  prefix.
- No socket, no port, no token: only the parent holds the pipes. Deno needs no `net` grant, only `run` for the gateway
  binary (an absolute path, granted by `runScope` like the other programs started by path). The behaviour is the same
  on all six targets.
- The gateway's life is tied to the control plane: when stdin reaches end of file the gateway drains and exits
  (option A, §11.2).
- Both sides put the transport behind one interface (`send(msg)`, `onMessage(fn)`, `close()`); a network transport
  with mTLS can be added later without changing a message.
- This replaces the program plan's "HTTP/1.1 over a unix socket or named pipe"; the plan is corrected in the first
  commit.

**D2. Framing and envelope.**
- UTF-8 JSON, one message per line (`\n`), no line breaks inside a message. A line longer than 8 MiB is a protocol
  error: the reader closes the channel and the supervisor restarts the gateway.
- Envelope, every message:
  `{"v": 1, "type": "<name>", "seq": <int>, "ts": <epoch ms>, "body": {...}}`, plus `"id"` on a request that expects a
  reply and `"re"` on the reply.
- `seq` counts per direction per stream from 1. For the events the gateway sends (D4) it is durable across restarts.
- Version rule: `v` is the major version, and both sides must speak the same one (checked in the handshake).
  Additions within v1 are new optional fields or new message types only; a receiver ignores unknown fields and logs
  and drops an unknown `type` once per type. A change to an existing field is a v2.

**D3. Messages of v1.**

Handshake:
- `hello` (gw → cp): `{protocol: [1], gwVersion, stream, outboxFrom}`. `stream` is the gateway's durable event-stream
  id (D4) and `outboxFrom` the first unacknowledged `seq`.
- `hello.ok` (cp → gw): `{protocol: 1, cpVersion}`, or `hello.refused` with a reason, after which the gateway exits 2.

Control plane → gateway:
- `config.push`: `{gen, gateway: GatewayConfigSection, keys: GatewayKeyAuth[], pricing}`.
  - `gen` increases with every push; later messages name the `gen` they belong to.
  - `GatewayKeyAuth` is `GatewayKeyConfig` plus the key's `hash` (the hash only: the gateway authenticates by hashing
    the presented key, as keys.js does today).
  - `pricing` is the merged price table the pricer uses (per 1M tokens, Money per token class).
- `secrets.handoff`: `{gen, providers: [{id, value?, token?: {value, expiresAt}}], mask: string[]}`.
  - The control plane resolves every provider reference (env, keychain, file store) and refreshes OAuth tokens; it
    sends the values, never the references.
  - Sent after every `config.push`, on a secret change, and before a token expires.
  - The gateway keeps secrets in memory only: never in the outbox, a log or an error text; `mask` seeds its masker.
- `budget.snapshot`: `{at, budgets: BudgetView[], keyCenter: Record<keyId, center>, refusePrefix}`.
  - Pushed after the control plane ingests a usage report that changes a budget, and as a heartbeat every 1 s.
  - The gateway applies the guard's rules: a budget counts when `enforce === 'block'` and its scope matches the call;
    it refuses when `state === 'exceeded'`.
  - **Fail open (owner decision 2026-11-11, plan §11.6):** when the newest snapshot is older than 5 s, or none has
    arrived, the call passes and its usage record carries `budget: 'unchecked'`. The control plane counts these and
    raises a dashboard alert. A budget the snapshot shows as exceeded is still refused (402 `insufficient_quota`, the
    same text as today).
- `shutdown`: `{graceMs}`. The gateway stops taking new requests, lets in-flight ones finish within `graceMs`, flushes
  the outbox and exits 0.

Gateway → control plane:
- `ready`: `{gen}` after the first config, secrets and snapshot are applied.
- `health`: `{inflight, outboxDepth, uptimeMs, gen, snapshotAgeMs}` every 1 s.
- `usage.report`: `{record: UsageRecord}`, with `cost` as Money (nano-USD), plus the optional `budget: 'unchecked'`.
- `audit.event`: `{log: 'governance' | 'mcp', event: AuditEvent}`.
- `trace.span`: `{span}` in the shape the local trace recorder stores (lib/trac* recorder input), with `traceId` and
  `spanId` as hex.
- `error`: `{code, message}` for a protocol problem the gateway reports before exiting.

Acknowledgement:
- `ack` (cp → gw): `{stream, upTo}`: every event with `seq <= upTo` is stored by the control plane (D4).

**D4. Events are never lost or counted twice.**
- The gateway writes every `usage.report`, `audit.event` and `trace.span` to an **outbox** before sending it: append
  only segment files under `<data dir>/gw-outbox/`, named by the first `seq` they hold. The `stream` id and the next
  `seq` live in `gw-outbox/stream.json`.
- Durability target: a killed process (SIGKILL, crash), not power loss. A write to the file is enough; there is no
  fsync per event.
- The control plane stores an event, then acknowledges it. Acks are batched (every 100 ms or 256 events). The gateway
  deletes a segment once every event in it is acknowledged.
- After a restart the gateway sends everything from `outboxFrom` again.
- The control plane drops duplicates by `(stream, seq)`. The high-water mark is not a separate file: each stored usage
  line carries `"ipc": {"stream", "seq"}`, and on start the control plane reads the tail of `gateway-usage.jsonl` (and
  of the audit logs) to recover the last `seq` per stream. Storing and marking are one append, so a crash between them
  cannot happen.
- **The control plane stays the only writer** of `gateway-usage.jsonl`, the cost ledger, the HMAC audit chain and
  `mcp-audit.jsonl` (plan §3, rule 1). Ingesting a `usage.report` calls the same `usage.record` path as today, so
  `budgetService.record`, the `recent` ring, the spend map and the ledger cursor work unchanged.

**D5. Supervisor (control plane).**
- `lib/ipc/supervisor.js` starts the gateway, runs the handshake, pushes config, secrets and the first snapshot, and
  waits for `ready`.
- No `health` for 5 s, a protocol error, or an unexpected exit: kill and restart with back-off (1 s, doubling, at most
  30 s; reset after 60 s healthy). Every restart is logged and shown in `health` on the dashboard.
- A config reload (config.json mtime, settings save, key change) pushes a new `config.push` and `secrets.handoff`.
- `shutdown` on server stop; the process is killed when `graceMs` + 2 s passes.
- In P3 the supervisor is used by tests and a dev command only; `server.js` does not start it (P4 S1 wires it in
  behind `gateway.engine`).

**D6. The schema is the source of truth.**
- `ipc/v1/` holds one JSON Schema (draft 2020-12) per message type and `envelope.schema.json`, plus fixtures:
  `ipc/v1/fixtures/<type>/valid-*.json` and `invalid-*.json`.
- JS: `lib/ipc/schema.js` is a small validator for the subset of JSON Schema the files use (type, properties, required,
  enum, const, items, additionalProperties, integer bounds, oneOf). No new dependency (zero runtime dependencies stays).
- Go: hand-written structs in `gw/ipc/`, decoded strictly in tests (`DisallowUnknownFields`) and leniently in
  production (unknown fields ignored, D2).
- Both sides run every fixture: valid ones must decode and re-encode to the same JSON value, invalid ones must be
  refused.
- A test keeps `lib/types.js` and the schemas in step (every typedef property that crosses the boundary exists in the
  schema with a compatible type).

**D7. Go module and stub.**
- `gw/` is a Go module (`go 1.27`, standard library only, no third-party modules), with `gw/ipc` (framing, envelope,
  messages, outbox, channel) and `gw/cmd/zaafpulse-gw`.
- The P3 binary is a **stub**: it speaks the whole protocol (handshake, config, secrets, snapshot, staleness, health,
  outbox, shutdown) but serves no HTTP. A test-only flag `--emit N` makes it send N synthetic `usage.report`,
  `audit.event` and `trace.span` events, so the kill tests have a real event stream.
- CI: a `go` job runs `go vet` and `go test ./...` on the four Node OS runners on full-scope runs, on Ubuntu on work
  branches; the Deno job builds the stub first so the cross-language tests run against it. Go pinned to 1.27.3.

## Tasks

| # | Task | Scope | Exit |
|---|---|---|---|
| T1 | Plan correction + schemas | Correct the program plan (§5 P3 transport, D1). `ipc/v1/*.schema.json` for the envelope and every D3 message, fixtures (valid and invalid per type), `lib/ipc/schema.js` validator, the `lib/types.js` sync test | Every fixture passes or fails as named, under Node and Deno |
| T2 | Go module + messages | `gw/` module, `gw/ipc` envelope and message structs, fixture round-trip tests, CI `go` job | `go test ./...` green on 4 OS; every fixture round-trips |
| T3 | Channel + handshake | Framing (8 MiB limit), the transport interface and stdio channel on both sides, handshake and version rules, the stub's message loop | Cross-language test: Deno starts the stub, handshake, config, secrets, snapshot, `ready`, `health`, `shutdown` exit 0 |
| T4 | Outbox + ingest | Go outbox (segments, `stream.json`, delete on ack); JS ingest (`usage.record` path, audit writers, trace recorder), `ipc` marker on stored lines, tail recovery, dedupe, batched acks | Resend after restart stores each event exactly once; ledger, budget pending and audit chain match a same-input in-process run |
| T5 | Supervisor + budget staleness | `lib/ipc/supervisor.js` (start, restart back-off, reload pushes, shutdown); the stub's snapshot staleness (`budget: 'unchecked'`) and exceeded refusal logic in `gw/ipc` as a pure function; the control plane's unchecked counter and alert | Supervisor tests (hang, crash, bad line, slow shutdown); budget fixtures decide pass, refuse and unchecked the same in JS and Go |
| T6 | Kill tests | SIGKILL the stub during an `--emit` stream at random points (20 runs), then restart; SIGKILL the control-plane side (a child Deno process) and restart it; Windows uses `taskkill /F` | No lost and no duplicated usage line; audit chain verifies; outbox empty after drain |
| T7 | Results + docs | `docs/migration/2026-11-11-p3-results.md` (message list, measured message latency over the pipe, kill-test results); README architecture note; plan §5 P3 exit ticked | Docs match the code; gate P3 met |

Order: T1 → T2 → T3 → T4 → T5 → T6 → T7. T2 can start once T1's schemas exist.

## Exit (plan §5 P3)

- [ ] Schemas frozen at v1 (`ipc/v1/`), with the additive-only rule written down.
- [ ] Both sides pass the shared fixtures, on Node, Deno and Go, on 4 OS in CI.
- [ ] Kill test: killing either process never corrupts the audit chain or the ledger, and loses or duplicates no usage
  line.
- [ ] No change in product behaviour: the Node and Deno suites and the contract suite stay green; `server.js` does not
  start the gateway.

## Out of scope

- Any gateway logic in Go: HTTP, keys, guardrails, translate, routing (P4).
- Messages for the in-process callers (judge, `llm()`, `embed()`, agent and collab models) and for `/mcp tools/call`
  into the integrations registry: P4 slices add them as v1 additions.
- Egress through Go (P5), packaging the Go binary into releases (P6).
- Fail-closed budgets (owner decision: later, as an opt-in setting).
