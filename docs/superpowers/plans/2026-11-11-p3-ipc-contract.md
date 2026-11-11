# P3 IPC Contract Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A frozen, versioned v1 message contract between the Deno control plane and a Go gateway process (`zaafpulse-gw`), carried over the child's stdin and stdout, proven on both sides by shared fixtures and by kill tests, with no change in product behaviour.

**Architecture:** `ipc/v1/` holds one JSON Schema per message and shared fixtures; `lib/ipc/` (CommonJS, zero dependencies) validates them with a small JSON Schema subset, frames lines, runs a session with the gateway (handshake, pushes, acks), stores the gateway's events through the same writers the in-process gateway uses (deduplicated by `(stream, seq)` marks kept in the stored lines) and supervises the process. `gw/` is a standard-library Go module: `gw/ipc` (envelope, strict and lenient decoding, framing, channel, outbox, budget decision, synthetic events) and `gw/cmd/zaafpulse-gw`, a stub that speaks the whole protocol and serves no HTTP. `server.js` does not start it.

**Tech Stack:** Deno 2.9.8 and Node 24.21.0 (CI; dual runtime until P7), CommonJS, `node:test`, JSON Schema draft 2020-12 (a subset, validated by `lib/ipc/schema.js`), Go 1.27.3 standard library only, GitHub Actions pinned by SHA, prettier 3 (printWidth 120) on new JS files only, `gofmt` on Go files.

## Global Constraints

- Spec: `docs/superpowers/specs/2026-11-11-p3-ipc-contract-design.md`, decisions D1–D7 and tasks T1–T7 (binding). This plan's Task N is the spec's TN. The `deno` and `migrate-to-deno` skills bind all Deno work (owner instruction).
- Transport: the child's stdin and stdout, UTF-8 JSON, one message per line ending in `\n`. Writers emit `\n` only. A reader strips **one** trailing `\r` from a line (fixture `ipc/v1/framing/crlf.ndjson`). A line longer than **8 MiB** (8,388,608 bytes, newline not counted) is a protocol error. Logs go to stderr.
- Envelope: `{"v": 1, "type", "seq", "ts", "body"}` plus `"id"` on a request that expects a reply and `"re"` on the reply. `v` is the major version; v1 additions are new optional fields or new message types only; a receiver ignores unknown fields and logs and drops an unknown `type` once per type.
- Zero runtime dependencies in JS (`@types/node` stays dev-only); CommonJS, `.js` files, no ESM, no `jsr:`/`npm:` imports. Go: standard library only, module at `gw/` with `go 1.27`; Go **1.27.3** locally (`/opt/homebrew/bin/go`) and in CI (`actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0`). Every go command a test runs sets `GOTOOLCHAIN=local` (no toolchain download).
- Deno is pinned to **2.9.8**, Node in CI to **24.21.0**. Every `deno test`/`deno run` with `-P` also passes `-q` (P2 plan). No `-A` in anything committed.
- Every task ends green on: `node --test test/*.test.js` (Node), `deno test -q --no-check --no-prompt -P=test test/*.test.js` (Deno), and from Task 2 on `go -C gw vet ./... && go -C gw test -count=1 ./...` and `test -z "$(gofmt -l gw)"`. Local runs use a temp `HOME` and `ZAAFPULSE_DATA`: `export ZAAFPULSE_DATA="$(mktemp -d)" HOME="$(mktemp -d)"` in the shell before the suites (as CI does).
- Cross-language tests get the stub from `test/ipc-stub.js` `stubBin(t)`: it uses `ZP_GW_BIN` when set, else builds `gw/cmd/zaafpulse-gw` into a temp folder with the `go` on PATH, else calls `t.skip('go is not on PATH: …')`. CI has Go in the Node, Deno and go jobs, so nothing is skipped there.
- CI scope rule (unchanged): a push to a work branch runs Ubuntu only; the full matrix runs on pull requests, pushes to `main` and `migrate/deno-go`, tags and manual runs. The new `go` job reuses the test job's matrix expression verbatim.
- **No product behaviour change:** `server.js`, `lib/gateway/*`, `lib/cost/*`, `lib/governance/*`, `lib/audit.js`, `lib/trace/*` are not modified. `server.js` never starts the gateway in P3 (P4 S1 wires it in behind `gateway.engine`).
- The control plane stays the only writer of `gateway-usage.jsonl`, the ledger, the HMAC audit chain and `mcp-audit.jsonl` (program plan §3 rule 1). The gateway keeps secrets in memory only: never in the outbox, a log or an error text.
- Data folder: `ZAAFPULSE_DATA` through `lib/platform.js dataDir()` (call it DIR, as `server.js:114` does). The stores live where `server.js` puts them: `DIR/.data/gateway-usage.jsonl`, `DIR/.data/audit/`, `DIR/.cache/mcp-audit.jsonl`, `DIR/.data/traces/`; the outbox is `DIR/.data/gw-outbox/`.
- Prettier: `npx --yes prettier@3 --write <file>` **only on files this plan creates**; never on an existing file. `*.json`, `*.md`, `*.yml` are in `.prettierignore`. Go: `gofmt -w` on the Go files this plan creates.
- Windows: paths through `path.join` / `filepath.Join`; the hard kill in tests is `taskkill /F /T /PID <pid>`; the stub binary is `zaafpulse-gw.exe`; `.gitattributes` keeps `ipc/v1/framing/*.ndjson` byte-exact.
- Commits: one plain descriptive English sentence like the repo history, **no** `Co-Authored-By` and no AI attribution. Each task ends with its commit and `git push origin migrate/p3-ipc-contract`. Never commit `.superpowers/`.
- Never touch the owner's live dashboard (port 4777) or the port 4787 preview. Tests never reach the network and never signal a process they did not start.

## Facts measured for this plan (2026-11-11, macOS arm64)

| # | Fact | Evidence |
|---|---|---|
| F1 | Toolchains: `go version go1.27.3 darwin/arm64`, `deno 2.9.8`, `node v24.16.0`. `actions/setup-go` latest release `v7.0.0` (2026-07-16) = `b7ad1dad31e06c5925ef5d2fc7ad053ef454303e`; its inputs include `go-version` and `cache`. | `gh api repos/actions/setup-go/commits/v7.0.0 --jq .sha`, action.yml |
| F2 | `usage.record(e)` (`lib/gateway/usage.js:104-116`) spreads the caller's fields into the stored line (`{ ts: now(), ...e, cost }`), so an `ipc` marker and `budget: 'unchecked'` are stored as given. `cost` is float USD (`e.cost ?? priced`). | source |
| F3 | The budget report is not in `usage.js`: `lib/gateway/index.js:164-171` wraps `usage.record` and calls `budgetGuard.record(entry)` right after the append; `server.js:535` routes it to `costBudgets.record`. The IPC ingest must call the same wrapped function. | source |
| F4 | `governance.record(input)` (`lib/governance/audit.js:382-386`) keeps every field but the chain's own (`seq, ts, prev, mac, keyed`), so `ipc` is sealed into the MAC with the entry. It never throws (returns `null` when degraded). Month files `governance-YYYY-MM.jsonl`. `keySource({ dir, store: null })` keeps the key in `<dir>/chain.key`. | source |
| F5 | MCP audit entries (`lib/integrations.js:150-158`) are `{ ts, integration, tool, ok, ms, args: string[], keyId? }`, not `AuditEvent`. `lib/audit.js` appends them as JSONL and never throws. | source |
| F6 | The trace recorder's input (`toRecord`) has `startNs`/`endNs` as BigInt, which JSON cannot carry. What the store keeps is the compact record `{t, s, p, n, k, st, d, ok, err?, src, svc, a}` (`lib/trace/recorder.js:54-73`); `createTraceStore().writeSpans(list)` appends it synchronously to `DIR/.data/traces/<UTC day>.ndjson` and keeps unknown fields. | source |
| F7 | The guard (`lib/cost/guard.js:48-115`) refuses on the first applicable budget with `enforce === 'block'` and `state === 'exceeded'`, message `budget "<name>" is used up for this <period>`; `no_data` passes; `keyId` `'dashboard'` counts as no key; `center` scope uses `service.keyCenter(keyId)`; `source` is `collab` (runId), `judge`, `replay` or `gateway`; the model is `normalizeModel(input.model)`. | source |
| F8 | Key hashes are SHA-256 hex (`lib/gateway/keys.js:10`): 64 lowercase hex characters. | source |
| F9 | Go 1.27.3: a field tagged `json:",omitzero"` is omitted when zero and kept when present-but-zero for pointers (`0`), non-nil empty slices (`[]`) and a generic `Nullable[T]` with an `IsZero()` method; `json.RawMessage` and a `Nullable[T]` with a pointer-receiver `UnmarshalJSON` both receive a JSON `null`. `{"a":null,"r":null,"l":[],"p":0,"q":null}` round-trips to an equal value. | scratch probe `goprobe/main.go` |
| F10 | Pricing entries (`lib/pricing.js` `DEFAULT_PRICING`, 225 models) use the keys `input, output, cacheRead, cacheWrite5m, cacheWrite1h, webSearch, tiers[{above, input, output, cacheRead, cacheWrite5m, cacheWrite1h}], estimate`, in USD per 1M tokens (`webSearch` per search). | `node -e` over `DEFAULT_PRICING` |

## Spec ambiguities resolved by this plan

1. **`refusePrefix` in `budget.snapshot`** is not defined anywhere in the spec, the program plan or the code. The refusal text is fixed (F7) and derivable from the budget's name and period. v1 leaves it out; it can be added later as an optional field (additive rule).
2. **`seq` in the gateway → control plane direction** has two spaces: events (`usage.report`, `audit.event`, `trace.span`) carry the durable outbox `seq` of `stream`; control messages (`hello`, `ready`, `health`, `error`) carry a per-connection counter from 1. Dedupe and acks use the event `seq` only. Written into `ipc/v1/README.md`.
3. **Stale snapshot and an exceeded budget:** with a stale snapshot, a budget the last snapshot shows as `exceeded` (and blocking, in scope) is still refused; anything else passes with `budget: 'unchecked'`. With no snapshot at all the call passes unchecked. "Older than 5 s" means `now - at > 5000`. The gateway exits when the control plane is gone (D1), so a stale snapshot only lasts while the control plane is alive but not pushing.
4. **`trace.span` carries the stored compact record** (F6) with `t` (32 hex) and `s`/`p` (16 hex), not the recorder's BigInt input.
5. **`audit.event` for `log: 'mcp'` carries the MCP audit entry** (F5), not `AuditEvent`; the schema is a `oneOf` keyed by `log`.
6. **`config.push`:** `gateway` is `GatewayConfigSection` without `keys`; the keys go in the top-level `keys` (`GatewayKeyConfig` + `hash`). `GatewayProviderConfig.apiKey` (a reference) is not sent: a reference is refused by the strict schema, the value travels in `secrets.handoff`. Prices are Money: integer nano-USD per 1M tokens (`webSearch`: per search). `budgetUSD` of a key and the amounts of a `BudgetView` stay as the typedefs have them (float USD): the gateway compares budget states, not amounts; a Money twin can be added additively in P4.
7. **The validator subset** adds `pattern`, `minLength` and `minItems` to the spec's list (hex ids, non-empty names). It reports, not throws, on any other keyword, and a test keeps every schema file inside the subset.
8. **Required vs optional in Go:** a field without `omitzero` is required (must be present); a required pointer is the nullable one (`T|null`); an optional field may be absent but not `null`. One reflective check (`checkShape`) enforces presence, null and the `ipc:"…"` tag rules (`enum`, `min`, `max`, `minlen`, `minitems`, `pattern`), so the Go structs refuse every invalid fixture the JS validator refuses.
9. **Outbox location and `next`:** the outbox is `DIR/.data/gw-outbox/` (next to `gateway-usage.jsonl`), or `--outbox <dir>`. `stream.json` holds `{stream, next, acked}`, written on every ack batch and on close; on open the next `seq` is the larger of `stream.json`'s `next` and the last complete line of the newest segment + 1, so no file is rewritten per event. A line cut short by a kill is truncated on open (it was never sent: an event is sent only after its append returned).
10. **The budget decision's model is already normalized** (`normalizeModel` stays in JS until P4 ports it with its own fixtures). The JS side of the shared budget fixtures runs the product's own guard (`lib/cost/guard.js`) behind a snapshot-backed service, so Go is proven equal to the code that ships.
11. **The "dashboard alert"** in P3 is the supervisor's counter, its `status().alert` flag and one log line per minute; the dashboard shows it when P4 S1 wires the supervisor into the server.
12. **Writer failures:** the ingest calls the same writers as the in-process gateway, so a write that those writers swallow (`jsonlLog.append`, `createAudit.record`, a degraded governance chain that later writes `audit.gap`) behaves exactly as in v1. Only a writer that throws blocks the ack: the session then kills the gateway and the supervisor restarts it, which resends.
13. **`runScope` and the gateway binary (D1)** stay for P4 S1: in P3 only tests and the dev command start the stub, under the `test` permission set or plain Node.

## Where the spec is wrong or not feasible as written

1. **Tail recovery is bounded.** The control plane reads the last **4 MiB** of each store (and of the rotated `.1` file of the usage and MCP logs, and the two newest governance months and trace days) for `ipc` marks. If more than 4 MiB of lines without a mark (dashboard calls, other writers) follow the last stored event in every store, the mark is missed and resent events are stored twice. Recorded as a P4 follow-up (a mark file or a bigger window); the kill tests stay well inside the bound.
2. **The compiled binary does not carry `ipc/`** (the `deno compile` include list of P2 has no such folder). P3 does not need it (the server does not load `lib/ipc`); P4 S1 must add `ipc/v1/*.schema.json` to `scripts/build-deno.js`.
3. **Measuring "message latency over the pipe"** needs a reply to time: v1 has none from the gateway for a push. The stub gets a test-only flag `--health-on-snapshot` (a `health` right after each `budget.snapshot`); the round trip is measured with it.

## Files

| File | Task | Responsibility |
|---|---|---|
| `docs/migration/2026-11-10-deno-go-migration-plan.md` | 1, 7 | transport correction (D1); P3 exit ticked |
| `ipc/v1/README.md`, `ipc/v1/envelope.schema.json`, `ipc/v1/<type>.schema.json` (14) | 1 | the frozen v1 contract |
| `ipc/v1/fixtures/<type>/{valid,invalid}-*.json`, `ipc/v1/fixtures/envelope/invalid-*.json` | 1 | shared fixtures |
| `lib/ipc/schema.js`, `test/ipc-schema.test.js`, `test/ipc-types.test.js` | 1 | JSON Schema subset validator; fixtures; `lib/types.js` sync |
| `gw/go.mod`, `gw/ipc/{nullable,shape,messages,envelope}.go`, `gw/ipc/{fixtures,messages}_test.go` | 2 | Go module, structs, strict/lenient decoding |
| `.github/workflows/ci.yml`, `package.json` | 2 | `go` job; Go in the Node and Deno jobs; `test:go` script |
| `.gitattributes`, `ipc/v1/framing/crlf.ndjson` | 3 | byte-exact framing fixture |
| `lib/ipc/{framing,channel,acker,session}.js`, `scripts/ipc/build-stub.js`, `test/ipc-stub.js` | 3 | framing, transport, acks, session; stub build helper |
| `gw/ipc/{framing,channel,outbox}.go`, `gw/ipc/{framing,channel}_test.go`, `gw/cmd/zaafpulse-gw/main.go` | 3 | Go framing and channel; the stub's message loop |
| `test/ipc-framing.test.js`, `test/ipc-channel.test.js`, `test/ipc-session.test.js` | 3 | tests |
| `gw/ipc/outbox.go` (replaced), `gw/ipc/synthetic.go`, `gw/ipc/{outbox,synthetic}_test.go` | 4 | durable outbox; synthetic events |
| `lib/ipc/{marks,ingest,local}.js`, `test/ipc-ingest.test.js` | 4 | tail recovery, dedupe, stores |
| `gw/ipc/budget.go`, `gw/ipc/budget_test.go`, `ipc/v1/fixtures/budget-decide/*.json` | 5 | budget decision, shared fixtures |
| `lib/ipc/{budget,supervisor}.js`, `scripts/gw-dev.js`, `test/ipc-budget.test.js`, `test/ipc-supervisor.test.js` | 5 | JS decision; supervisor; dev command |
| `test/ipc-kill.test.js`, `test/fixtures/ipc/cp-child.js` | 6 | kill tests |
| `bench/ipc-latency.js`, `docs/migration/2026-11-11-p3-results.md`, `README.md` | 7 | measurement and docs |

---

### Task 1: Plan correction, schemas, fixtures and the validator (spec T1)

**Files:**
- Modify: `docs/migration/2026-11-10-deno-go-migration-plan.md:68,206-208,366`
- Create: `ipc/v1/README.md`, `ipc/v1/envelope.schema.json`, `ipc/v1/{hello,hello.ok,hello.refused,config.push,secrets.handoff,budget.snapshot,shutdown,ack,ready,health,usage.report,audit.event,trace.span,error}.schema.json`
- Create: `ipc/v1/fixtures/**` (57 files, listed in Step 6)
- Create: `lib/ipc/schema.js`
- Test: `test/ipc-schema.test.js`, `test/ipc-types.test.js`

**Interfaces:**
- Consumes: `lib/types.js` (read as text by the sync test).
- Produces: `lib/ipc/schema.js` exports `PROTOCOL = 1`, `DIR` (absolute path of `ipc/v1`), `TYPES: string[]` (the 14 message types), `EVENTS = ['usage.report', 'audit.event', 'trace.span']`, `validate(schema, value, { lenient? }, at = '$'): string[]`, `unsupported(schema, at = '$'): string[]`, `schemas(): Record<'envelope'|type, object>`, `check(msg, { lenient? }): { errors: string[], unknownType: boolean }`. Fixture layout `ipc/v1/fixtures/<type>/(valid|invalid)[-<name>].json`, one full envelope per file; a file named `invalid-unknown-field*.json` must pass the lenient check.

- [ ] **Step 1: Check the starting point**

Run: `git status --short && git rev-parse --abbrev-ref HEAD && deno --version | head -1 && go version`
Expected: nothing listed, `migrate/p3-ipc-contract`, `deno 2.9.8 (stable, …)`, `go version go1.27.3 …`.

- [ ] **Step 2: Correct the program plan (D1)**

In `docs/migration/2026-11-10-deno-go-migration-plan.md`:

Line 68, replace
```
                │ local IPC (unix socket / named pipe, token-auth, versioned JSON)
```
with
```
                │ local IPC (the gateway's stdin/stdout, one JSON message per line, versioned)
```

Lines 206–208 (§5 P3), replace the three lines from `- Transport: HTTP/1.1 over a unix domain socket` through `` `--allow-net=unix:<data dir>/gw.sock`. `` with:
```markdown
- Transport (corrected 2026-11-11, P3 spec D1): the gateway's stdin and stdout, one JSON message per line. No socket,
  port or token: only the parent holds the pipes. Deno needs no `net` grant, only `run` for the gateway binary. The
  gateway exits when its stdin closes. The messages below, plus `hello`, `ready`, `ack` and `error` (P3 spec D3).
```

Line 366, replace `unix socket or named pipe today` with `the child's stdin and stdout today`.

Run: `grep -n "unix socket\|named pipe\|gw.sock" docs/migration/2026-11-10-deno-go-migration-plan.md`
Expected: no output.

- [ ] **Step 3: Write `ipc/v1/README.md`**

```markdown
# ZAAFPulse IPC v1

The contract between the Deno control plane and the Go gateway (`zaafpulse-gw`). Design:
`docs/superpowers/specs/2026-11-11-p3-ipc-contract-design.md`. The JSON Schemas here are the source of truth;
`lib/ipc/schema.js` (JS) and `gw/ipc` (Go) both run every fixture in `fixtures/`.

## Transport and framing

- The control plane starts the gateway and talks to it over the child's stdin (control plane → gateway) and stdout
  (gateway → control plane). The gateway logs to stderr. When its stdin reaches end of file the gateway exits.
- UTF-8 JSON, one message per line. Writers end a line with `\n` only and never put a line break inside a message.
  A reader removes one trailing `\r` from a line (`framing/crlf.ndjson`) and skips empty lines.
- A line longer than 8 MiB (8,388,608 bytes, the `\n` not counted) is a protocol error: the reader closes the
  channel and the gateway is restarted.

## Envelope

`{"v": 1, "type": "<name>", "seq": <int>, "ts": <epoch ms>, "body": {...}}`, plus `"id"` on a request that expects
a reply (`hello`) and `"re"` on the reply (`hello.ok`, `hello.refused`).

- `seq`, gateway → control plane: events (`usage.report`, `audit.event`, `trace.span`) carry the durable outbox
  sequence of the stream named in `hello`, starting at 1 and kept across restarts; control messages (`hello`,
  `ready`, `health`, `error`) count per connection from 1. Control plane → gateway: per connection from 1.
- Money is `integer` nano-USD (1e-9 USD): `usage.report` `record.cost` and every `config.push` price.

## Version rule (frozen)

- `v` is the major version; both sides must speak the same one, checked in the handshake.
- Within v1 only two kinds of change are allowed: a new **optional** field, or a new message **type**.
- A receiver ignores unknown fields (the lenient reader) and logs and drops an unknown `type` once per type.
- Any change to an existing field (name, type, meaning, required) is v2.
- The schemas are strict (`additionalProperties: false`) so the tests catch a typo; the production readers are
  lenient (`lib/ipc/schema.js` `{ lenient: true }`, Go `Decode(line, false)`).

## Messages

| Type | Direction | Body |
|---|---|---|
| `hello` | gw → cp | `protocol`, `gwVersion`, `stream`, `outboxFrom` |
| `hello.ok` / `hello.refused` | cp → gw | `protocol`, `cpVersion` / `reason` (the gateway exits 2) |
| `config.push` | cp → gw | `gen`, `gateway` (no keys, no secret references), `keys` (with `hash`), `pricing` (Money per 1M tokens) |
| `secrets.handoff` | cp → gw | `gen`, `providers[{id, value?, token?}]`, `mask` |
| `budget.snapshot` | cp → gw | `at`, `budgets` (BudgetView), `keyCenter` |
| `shutdown` | cp → gw | `graceMs` |
| `ack` | cp → gw | `stream`, `upTo` |
| `ready` | gw → cp | `gen` |
| `health` | gw → cp | `inflight`, `outboxDepth`, `uptimeMs`, `gen`, `snapshotAgeMs` |
| `usage.report` | gw → cp | `record` (UsageRecord, `cost` in Money), `budget?: "unchecked"` |
| `audit.event` | gw → cp | `log: "governance"` + AuditEvent, or `log: "mcp"` + the MCP audit entry |
| `trace.span` | gw → cp | `span`: the stored trace record `{t, s, p, n, k, st, d, ok, err?, src, svc, a}` |
| `error` | gw → cp | `code`, `message` (a protocol problem, before the gateway exits 3) |

Budget rule: no snapshot, or `now - at > 5000`: the call passes and its usage record carries `budget: "unchecked"`,
except that a blocking budget in scope that the snapshot shows as `exceeded` is always refused (402
`insufficient_quota`, `budget "<name>" is used up for this <period>`). Shared decisions: `fixtures/budget-decide/`.
```

- [ ] **Step 4: Write the envelope and the fourteen message schemas**

Every file starts with `"$schema": "https://json-schema.org/draft/2020-12/schema"` and a `title`. Create them exactly as below.

`ipc/v1/envelope.schema.json`:
```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "Envelope of every ZAAFPulse IPC v1 message",
  "type": "object",
  "required": ["v", "type", "seq", "ts", "body"],
  "additionalProperties": false,
  "properties": {
    "v": { "type": "integer", "const": 1 },
    "type": { "type": "string", "minLength": 1 },
    "seq": { "type": "integer", "minimum": 1 },
    "ts": { "type": "integer", "minimum": 0 },
    "id": { "type": "string", "minLength": 1 },
    "re": { "type": "string", "minLength": 1 },
    "body": { "type": "object" }
  }
}
```

`ipc/v1/hello.schema.json`:
```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "hello (gateway to control plane): protocols offered, gateway version, durable event stream, first unacknowledged seq",
  "type": "object",
  "required": ["protocol", "gwVersion", "stream", "outboxFrom"],
  "additionalProperties": false,
  "properties": {
    "protocol": { "type": "array", "minItems": 1, "items": { "type": "integer" } },
    "gwVersion": { "type": "string", "minLength": 1 },
    "stream": { "type": "string", "pattern": "^[0-9a-f]{32}$" },
    "outboxFrom": { "type": "integer", "minimum": 1 }
  }
}
```

`ipc/v1/hello.ok.schema.json`:
```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "hello.ok (control plane to gateway): the protocol chosen and the control plane's version",
  "type": "object",
  "required": ["protocol", "cpVersion"],
  "additionalProperties": false,
  "properties": {
    "protocol": { "type": "integer", "minimum": 1 },
    "cpVersion": { "type": "string", "minLength": 1 }
  }
}
```

`ipc/v1/hello.refused.schema.json`:
```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "hello.refused (control plane to gateway): no common protocol; the gateway exits 2",
  "type": "object",
  "required": ["reason"],
  "additionalProperties": false,
  "properties": { "reason": { "type": "string", "minLength": 1 } }
}
```

`ipc/v1/config.push.schema.json`:
```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "config.push (control plane to gateway): the gateway section without keys or secret references, the keys with their hashes, the price table in nano-USD per 1M tokens",
  "type": "object",
  "required": ["gen", "gateway", "keys", "pricing"],
  "additionalProperties": false,
  "properties": {
    "gen": { "type": "integer", "minimum": 1 },
    "gateway": {
      "type": "object",
      "required": ["providers", "routes", "guardrails", "defaults", "tokenSaver", "modelMap"],
      "additionalProperties": false,
      "properties": {
        "providers": {
          "type": "array",
          "items": {
            "type": "object",
            "required": ["id", "preset", "baseUrl"],
            "additionalProperties": false,
            "properties": {
              "id": { "type": "string", "minLength": 1 },
              "preset": { "type": "string" },
              "baseUrl": { "type": "string" },
              "models": { "type": "array", "items": { "type": "string" } },
              "aliases": { "type": "object", "additionalProperties": { "type": "string" } },
              "enabled": { "type": "boolean" },
              "tier": { "type": "string", "enum": ["private", "external"] },
              "approved": { "type": "boolean" },
              "auth": {
                "type": "object",
                "required": ["type"],
                "additionalProperties": false,
                "properties": {
                  "type": { "type": "string", "minLength": 1 },
                  "tokenUrl": { "type": "string" },
                  "clientId": { "type": "string" },
                  "scope": { "type": "string" },
                  "grant": { "type": "string", "enum": ["client_credentials", "refresh_token"] }
                }
              },
              "format": { "type": "string", "enum": ["openai", "anthropic"] },
              "responses": { "type": "boolean" },
              "reasoning": { "type": "boolean" },
              "tokenParam": { "type": "string", "enum": ["max_tokens", "max_completion_tokens"] }
            }
          }
        },
        "routes": {
          "type": "array",
          "items": {
            "type": "object",
            "required": ["name", "targets"],
            "additionalProperties": false,
            "properties": {
              "name": { "type": "string", "minLength": 1 },
              "targets": { "type": "array", "items": { "type": "string" } },
              "strategy": { "type": "string", "enum": ["fallback", "round-robin"] },
              "description": { "type": "string" }
            }
          }
        },
        "guardrails": {
          "type": "object",
          "required": ["scope", "secrets", "pii", "injection"],
          "additionalProperties": false,
          "properties": {
            "scope": { "type": "string", "enum": ["external", "all"] },
            "secrets": { "type": "string", "enum": ["off", "log", "redact", "block"] },
            "pii": { "type": "string", "enum": ["off", "log", "redact", "block"] },
            "injection": { "type": "string", "enum": ["off", "log", "block"] }
          }
        },
        "defaults": {
          "type": "object",
          "required": ["model", "plannerModel", "codeModel"],
          "additionalProperties": false,
          "properties": {
            "model": { "type": "string" },
            "plannerModel": { "type": "string" },
            "codeModel": { "type": "string" }
          }
        },
        "tokenSaver": { "type": "boolean" },
        "modelMap": {
          "type": "array",
          "items": {
            "type": "object",
            "required": ["match", "to"],
            "additionalProperties": false,
            "properties": { "match": { "type": "string" }, "to": { "type": "string" } }
          }
        }
      }
    },
    "keys": {
      "type": "array",
      "items": {
        "type": "object",
        "required": [
          "id", "name", "scopes", "models", "agents", "knowledge", "budgetUSD", "rpm", "privateOnly", "expiresAt",
          "revokedAt", "guardrails", "prefix", "approveOwnRuns", "createdAt", "lastUsedAt", "hash"
        ],
        "additionalProperties": false,
        "properties": {
          "id": { "type": "string", "minLength": 1 },
          "name": { "type": "string" },
          "scopes": { "type": "array", "items": { "type": "string" } },
          "models": { "type": "array", "items": { "type": "string" } },
          "agents": { "type": "array", "items": { "type": "string" } },
          "knowledge": { "type": "array", "items": { "type": "string" } },
          "budgetUSD": { "type": ["number", "null"] },
          "rpm": { "type": "integer", "minimum": 0 },
          "privateOnly": { "type": "boolean" },
          "expiresAt": { "type": ["integer", "null"] },
          "revokedAt": { "type": ["integer", "null"] },
          "guardrails": {
            "type": "object",
            "additionalProperties": false,
            "properties": {
              "scope": { "type": "string", "enum": ["external", "all"] },
              "secrets": { "type": "string", "enum": ["off", "log", "redact", "block"] },
              "pii": { "type": "string", "enum": ["off", "log", "redact", "block"] },
              "injection": { "type": "string", "enum": ["off", "log", "block"] }
            }
          },
          "prefix": { "type": "string" },
          "approveOwnRuns": { "type": "boolean" },
          "createdAt": { "type": "integer", "minimum": 0 },
          "lastUsedAt": { "type": ["integer", "null"] },
          "hash": { "type": "string", "pattern": "^[0-9a-f]{64}$" }
        }
      }
    },
    "pricing": {
      "type": "object",
      "additionalProperties": {
        "type": "object",
        "required": ["input", "output"],
        "additionalProperties": false,
        "properties": {
          "input": { "type": "integer", "minimum": 0 },
          "output": { "type": "integer", "minimum": 0 },
          "cacheRead": { "type": "integer", "minimum": 0 },
          "cacheWrite5m": { "type": "integer", "minimum": 0 },
          "cacheWrite1h": { "type": "integer", "minimum": 0 },
          "webSearch": { "type": "integer", "minimum": 0 },
          "tiers": {
            "type": "array",
            "items": {
              "type": "object",
              "required": ["above", "input", "output"],
              "additionalProperties": false,
              "properties": {
                "above": { "type": "integer", "minimum": 0 },
                "input": { "type": "integer", "minimum": 0 },
                "output": { "type": "integer", "minimum": 0 },
                "cacheRead": { "type": "integer", "minimum": 0 },
                "cacheWrite5m": { "type": "integer", "minimum": 0 },
                "cacheWrite1h": { "type": "integer", "minimum": 0 }
              }
            }
          },
          "estimate": { "type": "boolean" }
        }
      }
    }
  }
}
```

`ipc/v1/secrets.handoff.schema.json`:
```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "secrets.handoff (control plane to gateway): resolved provider secrets and the mask set; never references, never stored by the gateway",
  "type": "object",
  "required": ["gen", "providers", "mask"],
  "additionalProperties": false,
  "properties": {
    "gen": { "type": "integer", "minimum": 1 },
    "providers": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["id"],
        "additionalProperties": false,
        "properties": {
          "id": { "type": "string", "minLength": 1 },
          "value": { "type": "string" },
          "token": {
            "type": "object",
            "required": ["value", "expiresAt"],
            "additionalProperties": false,
            "properties": {
              "value": { "type": "string", "minLength": 1 },
              "expiresAt": { "type": "integer", "minimum": 0 }
            }
          }
        }
      }
    },
    "mask": { "type": "array", "items": { "type": "string" } }
  }
}
```

`ipc/v1/budget.snapshot.schema.json`:
```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "budget.snapshot (control plane to gateway): the budgets as the guard enforces them, pushed on change and every second",
  "type": "object",
  "required": ["at", "budgets", "keyCenter"],
  "additionalProperties": false,
  "properties": {
    "at": { "type": "integer", "minimum": 0 },
    "budgets": {
      "type": "array",
      "items": {
        "type": "object",
        "required": [
          "id", "name", "scope", "period", "currency", "measure", "enforce", "amount", "from", "to", "state",
          "spentUSD", "amountUSD", "reason"
        ],
        "additionalProperties": false,
        "properties": {
          "id": { "type": "string", "minLength": 1 },
          "name": { "type": "string" },
          "scope": {
            "oneOf": [
              { "type": "string", "const": "global" },
              {
                "type": "object",
                "required": ["dim", "value"],
                "additionalProperties": false,
                "properties": {
                  "dim": { "type": "string", "minLength": 1 },
                  "value": { "type": "string", "minLength": 1 }
                }
              }
            ]
          },
          "period": { "type": "string", "enum": ["day", "week", "month"] },
          "currency": { "type": "string" },
          "measure": { "type": "string", "enum": ["list", "billed"] },
          "enforce": { "type": "string", "enum": ["none", "block", "pause"] },
          "amount": { "type": "number" },
          "from": { "type": "string" },
          "to": { "type": "string" },
          "state": { "type": "string", "minLength": 1 },
          "spentUSD": { "type": ["number", "null"] },
          "amountUSD": { "type": ["number", "null"] },
          "reason": { "type": ["string", "null"] }
        }
      }
    },
    "keyCenter": { "type": "object", "additionalProperties": { "type": "string" } }
  }
}
```

`ipc/v1/shutdown.schema.json`:
```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "shutdown (control plane to gateway): stop taking requests, finish within graceMs, exit 0",
  "type": "object",
  "required": ["graceMs"],
  "additionalProperties": false,
  "properties": { "graceMs": { "type": "integer", "minimum": 0 } }
}
```

`ipc/v1/ack.schema.json`:
```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "ack (control plane to gateway): every event of the stream with seq <= upTo is stored",
  "type": "object",
  "required": ["stream", "upTo"],
  "additionalProperties": false,
  "properties": {
    "stream": { "type": "string", "pattern": "^[0-9a-f]{32}$" },
    "upTo": { "type": "integer", "minimum": 0 }
  }
}
```

`ipc/v1/ready.schema.json`:
```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "ready (gateway to control plane): the first config, secrets and snapshot are applied",
  "type": "object",
  "required": ["gen"],
  "additionalProperties": false,
  "properties": { "gen": { "type": "integer", "minimum": 1 } }
}
```

`ipc/v1/health.schema.json`:
```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "health (gateway to control plane): every second; snapshotAgeMs is null before the first snapshot",
  "type": "object",
  "required": ["inflight", "outboxDepth", "uptimeMs", "gen", "snapshotAgeMs"],
  "additionalProperties": false,
  "properties": {
    "inflight": { "type": "integer", "minimum": 0 },
    "outboxDepth": { "type": "integer", "minimum": 0 },
    "uptimeMs": { "type": "integer", "minimum": 0 },
    "gen": { "type": "integer", "minimum": 0 },
    "snapshotAgeMs": { "type": ["integer", "null"], "minimum": 0 }
  }
}
```

`ipc/v1/usage.report.schema.json`:
```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "usage.report (gateway to control plane): one gateway call as lib/gateway/usage.js records it, cost in nano-USD",
  "type": "object",
  "required": ["record"],
  "additionalProperties": false,
  "properties": {
    "record": {
      "type": "object",
      "required": ["ts", "keyId", "provider", "model", "status", "ms", "cost"],
      "additionalProperties": false,
      "properties": {
        "ts": { "type": "integer", "minimum": 0 },
        "keyId": { "type": "string", "minLength": 1 },
        "provider": { "type": "string" },
        "model": { "type": "string" },
        "requested": { "type": "string" },
        "routedTo": { "type": "string" },
        "kind": { "type": "string", "enum": ["chat", "agent", "embeddings", "mcp"] },
        "format": { "type": "string", "enum": ["openai", "anthropic", "responses", "gemini"] },
        "stream": { "type": "boolean" },
        "status": { "type": "integer", "minimum": 0 },
        "ms": { "type": "number", "minimum": 0 },
        "ttft": { "type": ["number", "null"] },
        "input": { "type": "integer", "minimum": 0 },
        "output": { "type": "integer", "minimum": 0 },
        "cached": { "type": "integer", "minimum": 0 },
        "cost": { "type": "integer", "minimum": 0 },
        "error": { "type": "string" },
        "guard": {
          "type": "object",
          "required": ["action", "hits"],
          "additionalProperties": false,
          "properties": {
            "action": { "type": "string", "enum": ["redacted", "logged", "blocked"] },
            "hits": {
              "type": "array",
              "items": {
                "type": "object",
                "required": ["check", "label", "count"],
                "additionalProperties": false,
                "properties": {
                  "check": { "type": "string", "enum": ["secrets", "pii", "injection"] },
                  "label": { "type": "string" },
                  "count": { "type": "integer", "minimum": 0 }
                }
              }
            },
            "error": { "type": "boolean" }
          }
        },
        "client": { "type": "string", "enum": ["claude-code", "codex"] },
        "local": { "type": "boolean", "const": true },
        "savedTokens": { "type": "integer", "minimum": 0 },
        "agent": { "type": "string" },
        "sessionId": { "type": "string" },
        "runId": { "type": "string" },
        "traceId": { "type": "string" }
      }
    },
    "budget": { "type": "string", "enum": ["unchecked"] }
  }
}
```

`ipc/v1/audit.event.schema.json`:
```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "audit.event (gateway to control plane): a governance entry for the HMAC chain, or an MCP tool call for mcp-audit.jsonl",
  "type": "object",
  "oneOf": [
    {
      "type": "object",
      "required": ["log", "event"],
      "additionalProperties": false,
      "properties": {
        "log": { "type": "string", "const": "governance" },
        "event": {
          "type": "object",
          "required": ["action"],
          "additionalProperties": false,
          "properties": {
            "action": { "type": "string", "minLength": 1 },
            "target": {
              "type": "object",
              "required": ["type"],
              "additionalProperties": false,
              "properties": {
                "type": { "type": "string", "minLength": 1 },
                "id": { "type": "string" },
                "name": { "type": "string" }
              }
            },
            "outcome": { "type": "string", "enum": ["ok", "refused", "error"] },
            "status": { "type": "integer" },
            "changes": {
              "type": "array",
              "items": {
                "type": "object",
                "required": ["path"],
                "additionalProperties": false,
                "properties": { "path": { "type": "string" }, "from": {}, "to": {} }
              }
            },
            "actor": {
              "type": "object",
              "required": ["via"],
              "additionalProperties": false,
              "properties": {
                "via": { "type": "string", "minLength": 1 },
                "keyId": { "type": "string" },
                "user": { "type": "string" },
                "rule": { "type": "string" }
              }
            }
          }
        }
      }
    },
    {
      "type": "object",
      "required": ["log", "event"],
      "additionalProperties": false,
      "properties": {
        "log": { "type": "string", "const": "mcp" },
        "event": {
          "type": "object",
          "required": ["ts", "integration", "tool", "ok", "ms", "args"],
          "additionalProperties": false,
          "properties": {
            "ts": { "type": "integer", "minimum": 0 },
            "integration": { "type": "string", "minLength": 1 },
            "tool": { "type": "string", "minLength": 1 },
            "ok": { "type": "boolean" },
            "ms": { "type": "number", "minimum": 0 },
            "args": { "type": "array", "items": { "type": "string" } },
            "keyId": { "type": "string" }
          }
        }
      }
    }
  ]
}
```

`ipc/v1/trace.span.schema.json`:
```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "trace.span (gateway to control plane): one finished span as the local trace store keeps it (microseconds, no content)",
  "type": "object",
  "required": ["span"],
  "additionalProperties": false,
  "properties": {
    "span": {
      "type": "object",
      "required": ["t", "s", "p", "n", "k", "st", "d", "ok", "src", "svc", "a"],
      "additionalProperties": false,
      "properties": {
        "t": { "type": "string", "pattern": "^[0-9a-f]{32}$" },
        "s": { "type": "string", "pattern": "^[0-9a-f]{16}$" },
        "p": { "type": ["string", "null"], "pattern": "^[0-9a-f]{16}$" },
        "n": { "type": "string", "minLength": 1 },
        "k": { "type": "string", "enum": ["internal", "server", "client"] },
        "st": { "type": "integer", "minimum": 0 },
        "d": { "type": "integer", "minimum": 0 },
        "ok": { "type": "boolean" },
        "err": { "type": "string" },
        "src": { "type": "string" },
        "svc": { "type": "string" },
        "a": { "type": "object" }
      }
    }
  }
}
```

`ipc/v1/error.schema.json`:
```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "error (gateway to control plane): a protocol problem the gateway reports before it exits 3",
  "type": "object",
  "required": ["code", "message"],
  "additionalProperties": false,
  "properties": {
    "code": { "type": "string", "minLength": 1 },
    "message": { "type": "string" }
  }
}
```

- [ ] **Step 5: Write the failing schema tests**

Create `test/ipc-schema.test.js`:

```js
// ipc/v1 (P3 T1): every fixture passes or fails as its name says under the strict validator, an unknown field passes
// the lenient one (D2), the validator implements its JSON Schema subset, and every schema file stays inside it.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const { DIR, TYPES, validate, unsupported, schemas, check } = require('../lib/ipc/schema');

const FIX = path.join(DIR, 'fixtures');
// Fixture folders that are not message types: envelope-level cases, and the budget decisions of Task 5.
const NOT_TYPES = new Set(['envelope', 'budget-decide']);
const read = (...p) => JSON.parse(fs.readFileSync(path.join(FIX, ...p), 'utf8'));

test('every message type has a schema and valid and invalid fixtures; every fixture folder is a type', () => {
  const folders = fs
    .readdirSync(FIX)
    .filter((d) => !NOT_TYPES.has(d))
    .sort();
  assert.deepEqual(folders, [...TYPES].sort());
  for (const t of TYPES) {
    const files = fs.readdirSync(path.join(FIX, t));
    assert.ok(
      files.some((f) => f.startsWith('valid')),
      `${t} has a valid fixture`,
    );
    assert.ok(
      files.some((f) => f.startsWith('invalid')),
      `${t} has an invalid fixture`,
    );
  }
  for (const d of fs.readdirSync(FIX).filter((x) => x !== 'budget-decide'))
    for (const f of fs.readdirSync(path.join(FIX, d)))
      assert.match(f, /^(valid|invalid)(-[a-z0-9-]+)?\.json$/, `${d}/${f} is named valid[-name].json or invalid[-name].json`);
});

for (const folder of fs.readdirSync(FIX).filter((d) => d !== 'budget-decide')) {
  for (const file of fs.readdirSync(path.join(FIX, folder))) {
    test(`fixture ${folder}/${file}`, () => {
      const msg = read(folder, file);
      const r = check(msg);
      if (file.startsWith('valid')) {
        assert.deepEqual(r, { errors: [], unknownType: false });
        if (folder !== 'envelope') assert.equal(msg.type, folder);
        // Decoding and encoding again gives the same JSON value.
        assert.deepEqual(JSON.parse(JSON.stringify(msg)), msg);
      } else {
        assert.ok(r.errors.length > 0, `refused, got ${JSON.stringify(r)}`);
      }
      if (file.startsWith('invalid-unknown-field'))
        assert.deepEqual(
          check(msg, { lenient: true }),
          { errors: [], unknownType: false },
          'the lenient reader ignores an unknown field (D2)',
        );
    });
  }
}

test('validate: the JSON Schema subset', () => {
  const scope = { oneOf: [{ type: 'string', const: 'global' }, { type: 'object' }] };
  const cases = [
    [{ type: 'integer' }, 3, true],
    [{ type: 'integer' }, 3.5, false],
    [{ type: 'number' }, 3, true],
    [{ type: ['string', 'null'] }, null, true],
    [{ type: 'string' }, null, false],
    [{ const: 1 }, 1, true],
    [{ const: 1 }, 2, false],
    [{ enum: ['a', 'b'] }, 'c', false],
    [{ type: 'integer', minimum: 1 }, 0, false],
    [{ type: 'integer', maximum: 1 }, 2, false],
    [{ type: 'string', minLength: 1 }, '', false],
    [{ type: 'string', pattern: '^[0-9a-f]{2}$' }, 'AB', false],
    [{ type: ['string', 'null'], pattern: '^[0-9a-f]{2}$' }, null, true],
    [{ type: 'array', minItems: 1 }, [], false],
    [{ type: 'array', items: { type: 'string' } }, ['a', 1], false],
    [{ type: 'object', required: ['a'] }, {}, false],
    [{ type: 'object', properties: { a: { type: 'string' } }, additionalProperties: false }, { b: 1 }, false],
    [{ type: 'object', additionalProperties: { type: 'integer' } }, { x: 1, y: 'no' }, false],
    [scope, 'global', true],
    [scope, { dim: 'key' }, true],
    [scope, 'all', false],
    [{}, { anything: [1, 'x'] }, true],
  ];
  for (const [schema, value, ok] of cases)
    assert.equal(validate(schema, value).length === 0, ok, `${JSON.stringify(schema)} with ${JSON.stringify(value)}`);
  const closed = { type: 'object', properties: {}, additionalProperties: false };
  assert.deepEqual(validate(closed, { x: 1 }, { lenient: true }), [], 'lenient: an unknown field is ignored');
  assert.deepEqual(validate({ type: 'object', additionalProperties: { type: 'integer' } }, { y: 'no' }, { lenient: true }), [
    '$.y: must be integer',
  ]);
});

test('every schema file is draft 2020-12 and uses only the keywords lib/ipc/schema.js implements', () => {
  for (const [name, s] of Object.entries(schemas())) {
    assert.equal(s.$schema, 'https://json-schema.org/draft/2020-12/schema', name);
    assert.deepEqual(unsupported(s), [], name);
  }
  assert.deepEqual(unsupported({ type: 'string', format: 'uri', properties: { a: { $ref: '#/x' } } }), [
    '$: format',
    '$.a: $ref',
  ]);
});

test('an unknown message type is reported, not refused (D2)', () => {
  assert.deepEqual(check({ v: 1, type: 'telemetry.v9', seq: 1, ts: 0, body: { any: 1 } }), {
    errors: [],
    unknownType: true,
  });
});
```

- [ ] **Step 6: Write the fixtures**

Run this in `bash` from the repo root (type `bash` first if your shell is zsh). It creates 57 files, one envelope each:

```bash
F=ipc/v1/fixtures
S=0f1e2d3c4b5a69788796a5b4c3d2e1f0
H=9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08
mkdir -p $F/envelope $F/hello $F/hello.ok $F/hello.refused $F/config.push $F/secrets.handoff $F/budget.snapshot \
  $F/shutdown $F/ack $F/ready $F/health $F/usage.report $F/audit.event $F/trace.span $F/error
HB='{"inflight":0,"outboxDepth":0,"uptimeMs":10,"gen":1,"snapshotAgeMs":5}'
echo '{"v":2,"type":"health","seq":1,"ts":1767225600000,"body":'"$HB"'}' > $F/envelope/invalid-version-2.json
echo '{"v":1,"type":"health","ts":1767225600000,"body":'"$HB"'}' > $F/envelope/invalid-missing-seq.json
echo '{"v":1,"type":"health","seq":0,"ts":1767225600000,"body":'"$HB"'}' > $F/envelope/invalid-seq-zero.json
echo '{"v":1,"type":"health","seq":1,"ts":1767225600000,"body":[]}' > $F/envelope/invalid-body-array.json
echo '{"v":1,"type":"health","seq":1,"ts":1767225600000,"trace":"x","body":'"$HB"'}' > $F/envelope/invalid-unknown-field.json

echo '{"v":1,"type":"hello","seq":1,"ts":1767225600000,"id":"h1","body":{"protocol":[1],"gwVersion":"0.3.0-stub","stream":"'$S'","outboxFrom":1}}' > $F/hello/valid-first-start.json
echo '{"v":1,"type":"hello","seq":1,"ts":1767225600000,"id":"h1","body":{"protocol":[1,2],"gwVersion":"0.4.0","stream":"'$S'","outboxFrom":41}}' > $F/hello/valid-resume.json
echo '{"v":1,"type":"hello","seq":1,"ts":1767225600000,"id":"h1","body":{"protocol":[],"gwVersion":"0.3.0-stub","stream":"'$S'","outboxFrom":1}}' > $F/hello/invalid-empty-protocol.json
echo '{"v":1,"type":"hello","seq":1,"ts":1767225600000,"id":"h1","body":{"protocol":[1],"gwVersion":"0.3.0-stub","stream":"gw-1","outboxFrom":1}}' > $F/hello/invalid-stream-not-hex.json

echo '{"v":1,"type":"hello.ok","seq":1,"ts":1767225600001,"re":"h1","body":{"protocol":1,"cpVersion":"2.0.0-dev"}}' > $F/hello.ok/valid.json
echo '{"v":1,"type":"hello.ok","seq":1,"ts":1767225600001,"re":"h1","body":{"protocol":"1","cpVersion":"2.0.0-dev"}}' > $F/hello.ok/invalid-protocol-string.json
echo '{"v":1,"type":"hello.refused","seq":1,"ts":1767225600001,"re":"h1","body":{"reason":"this control plane speaks protocol 1, the gateway offers [2]"}}' > $F/hello.refused/valid.json
echo '{"v":1,"type":"hello.refused","seq":1,"ts":1767225600001,"re":"h1","body":{"reason":""}}' > $F/hello.refused/invalid-empty-reason.json

GW='{"providers":[],"routes":[],"guardrails":{"scope":"external","secrets":"redact","pii":"log","injection":"log"},"defaults":{"model":"","plannerModel":"","codeModel":""},"tokenSaver":false,"modelMap":[]}'
KEY='"id":"k1","name":"ci","scopes":["llm"],"models":[],"agents":[],"knowledge":[],"budgetUSD":25,"rpm":60,"privateOnly":false,"expiresAt":null,"revokedAt":null,"guardrails":{"pii":"block"},"prefix":"zp_live_ab","approveOwnRuns":false,"createdAt":1767225000000,"lastUsedAt":null'
echo '{"v":1,"type":"config.push","seq":2,"ts":1767225600002,"body":{"gen":1,"gateway":'"$GW"',"keys":[],"pricing":{}}}' > $F/config.push/valid-minimal.json
echo '{"v":1,"type":"config.push","seq":2,"ts":1767225600002,"body":{"gen":0,"gateway":'"$GW"',"keys":[],"pricing":{}}}' > $F/config.push/invalid-gen-zero.json
echo '{"v":1,"type":"config.push","seq":2,"ts":1767225600002,"body":{"gen":1,"gateway":'"$GW"',"keys":[{'"$KEY"'}],"pricing":{}}}' > $F/config.push/invalid-key-without-hash.json
echo '{"v":1,"type":"config.push","seq":2,"ts":1767225600002,"body":{"gen":1,"gateway":'"$GW"',"keys":[],"pricing":{"m":{"input":3.5,"output":15}}}}' > $F/config.push/invalid-price-float.json
echo '{"v":1,"type":"config.push","seq":2,"ts":1767225600002,"body":{"gen":1,"gateway":{"providers":[{"id":"openai","preset":"openai","baseUrl":"https://api.openai.com/v1","apiKey":{"env":"OPENAI_API_KEY"}}],"routes":[],"guardrails":{"scope":"external","secrets":"redact","pii":"log","injection":"log"},"defaults":{"model":"","plannerModel":"","codeModel":""},"tokenSaver":false,"modelMap":[]},"keys":[],"pricing":{}}}' > $F/config.push/invalid-api-key-reference.json
echo '{"v":1,"type":"config.push","seq":2,"ts":1767225600002,"body":{"gen":1,"gateway":{"providers":[],"routes":[],"guardrails":{"scope":"external","secrets":"redact","pii":"log","injection":"log"},"defaults":{"model":"","plannerModel":"","codeModel":""},"tokenSaver":false,"modelMap":[],"cache":true},"keys":[],"pricing":{}}}' > $F/config.push/invalid-unknown-field.json
cat > $F/config.push/valid-full.json <<EOF
{"v":1,"type":"config.push","seq":2,"ts":1767225600002,"body":{"gen":3,
 "gateway":{
  "providers":[
   {"id":"openai","preset":"openai","baseUrl":"https://api.openai.com/v1","models":["gpt-6.1-sol"],"aliases":{"fast":"gpt-6.1-sol"},"enabled":true,"tier":"external","approved":true,"format":"openai","responses":true,"reasoning":false,"tokenParam":"max_completion_tokens"},
   {"id":"vertex","preset":"vertex","baseUrl":"https://europe-west4-aiplatform.googleapis.com/v1","auth":{"type":"google-service-account","tokenUrl":"https://oauth2.googleapis.com/token","scope":"https://www.googleapis.com/auth/cloud-platform"}},
   {"id":"ollama","preset":"ollama","baseUrl":"http://127.0.0.1:11434/v1","tier":"private","models":[]}],
  "routes":[{"name":"router/fast","targets":["openai/gpt-6.1-sol","ollama/llama-5"],"strategy":"fallback","description":"cloud first, local when it fails"}],
  "guardrails":{"scope":"all","secrets":"block","pii":"redact","injection":"block"},
  "defaults":{"model":"openai/gpt-6.1-sol","plannerModel":"","codeModel":""},
  "tokenSaver":true,
  "modelMap":[{"match":"gpt-4o*","to":"openai/gpt-6.1-sol"}]},
 "keys":[{$KEY,"hash":"$H"}],
 "pricing":{
  "claude-opus-5-5":{"input":5000000000,"output":25000000000,"cacheRead":500000000,"cacheWrite5m":6250000000,"cacheWrite1h":10000000000,"webSearch":10000000},
  "gemini-3-pro":{"input":2000000000,"output":12000000000,"tiers":[{"above":200000,"input":4000000000,"output":18000000000}]},
  "gpt-6.1-sol":{"input":1250000000,"output":10000000000,"estimate":true}}}}
EOF

echo '{"v":1,"type":"secrets.handoff","seq":3,"ts":1767225600003,"body":{"gen":1,"providers":[{"id":"openai","value":"test-openai-key-0001"},{"id":"vertex","token":{"value":"test-vertex-token-0001","expiresAt":1767229200000}},{"id":"ollama"}],"mask":["test-openai-key-0001","test-vertex-token-0001"]}}' > $F/secrets.handoff/valid.json
echo '{"v":1,"type":"secrets.handoff","seq":3,"ts":1767225600003,"body":{"gen":1,"providers":[{"id":"vertex","token":{"value":"test-vertex-token-0001"}}],"mask":[]}}' > $F/secrets.handoff/invalid-token-without-expiry.json
echo '{"v":1,"type":"secrets.handoff","seq":3,"ts":1767225600003,"body":{"gen":1,"providers":[],"mask":[1]}}' > $F/secrets.handoff/invalid-mask-not-strings.json
echo '{"v":1,"type":"secrets.handoff","seq":3,"ts":1767225600003,"body":{"gen":1,"providers":[{"id":"openai","env":"OPENAI_API_KEY"}],"mask":[]}}' > $F/secrets.handoff/invalid-unknown-field-env.json

B1='{"id":"b-global","name":"All AI","scope":"global","period":"month","currency":"USD","measure":"list","enforce":"block","amount":500,"from":"2026-01-01","to":"2026-01-31","state":"warn_80","spentUSD":412.5,"amountUSD":500,"reason":null}'
B2='{"id":"b-key","name":"CI key","scope":{"dim":"key","value":"k1"},"period":"day","currency":"EUR","measure":"billed","enforce":"none","amount":10,"from":"2026-01-01","to":"2026-01-01","state":"no_data","spentUSD":null,"amountUSD":null,"reason":"the billing history could not be read"}'
echo '{"v":1,"type":"budget.snapshot","seq":4,"ts":1767225600004,"body":{"at":1767225600000,"budgets":['"$B1,$B2"'],"keyCenter":{"k1":"platform"}}}' > $F/budget.snapshot/valid.json
echo '{"v":1,"type":"budget.snapshot","seq":4,"ts":1767225600004,"body":{"at":1767225600000,"budgets":[],"keyCenter":{}}}' > $F/budget.snapshot/valid-empty.json
echo '{"v":1,"type":"budget.snapshot","seq":4,"ts":1767225600004,"body":{"at":1767225600000,"budgets":['"$(printf %s "$B1" | sed 's/"scope":"global"/"scope":"all"/')"'],"keyCenter":{}}}' > $F/budget.snapshot/invalid-scope-word.json
echo '{"v":1,"type":"budget.snapshot","seq":4,"ts":1767225600004,"body":{"at":1767225600000,"budgets":['"$(printf %s "$B1" | sed 's/"enforce":"block"/"enforce":"stop"/')"'],"keyCenter":{}}}' > $F/budget.snapshot/invalid-enforce.json
echo '{"v":1,"type":"budget.snapshot","seq":4,"ts":1767225600004,"body":{"at":1767225600000,"budgets":['"$(printf %s "$B1" | sed 's/"state":"warn_80",//')"'],"keyCenter":{}}}' > $F/budget.snapshot/invalid-missing-state.json

echo '{"v":1,"type":"shutdown","seq":9,"ts":1767225609000,"body":{"graceMs":5000}}' > $F/shutdown/valid.json
echo '{"v":1,"type":"shutdown","seq":9,"ts":1767225609000,"body":{"graceMs":-1}}' > $F/shutdown/invalid-negative.json
echo '{"v":1,"type":"ack","seq":5,"ts":1767225600100,"body":{"stream":"'$S'","upTo":256}}' > $F/ack/valid.json
echo '{"v":1,"type":"ack","seq":5,"ts":1767225600100,"body":{"stream":"gw","upTo":256}}' > $F/ack/invalid-stream.json
echo '{"v":1,"type":"ack","seq":5,"ts":1767225600100,"body":{"stream":"'$S'","upTo":1.5}}' > $F/ack/invalid-upto-float.json
echo '{"v":1,"type":"ready","seq":2,"ts":1767225600005,"body":{"gen":1}}' > $F/ready/valid.json
echo '{"v":1,"type":"ready","seq":2,"ts":1767225600005,"body":{}}' > $F/ready/invalid-gen-missing.json
echo '{"v":1,"type":"health","seq":3,"ts":1767225601005,"body":{"inflight":0,"outboxDepth":12,"uptimeMs":4021,"gen":1,"snapshotAgeMs":120}}' > $F/health/valid.json
echo '{"v":1,"type":"health","seq":2,"ts":1767225601005,"body":{"inflight":0,"outboxDepth":0,"uptimeMs":1001,"gen":0,"snapshotAgeMs":null}}' > $F/health/valid-no-snapshot.json
echo '{"v":1,"type":"health","seq":3,"ts":1767225601005,"body":{"inflight":0,"outboxDepth":-1,"uptimeMs":4021,"gen":1,"snapshotAgeMs":120}}' > $F/health/invalid-depth-negative.json

echo '{"v":1,"type":"usage.report","seq":41,"ts":1767225600124,"body":{"record":{"ts":1767225600123,"keyId":"k1","provider":"openai","model":"router/fast","requested":"gpt-4o","routedTo":"openai/gpt-6.1-sol","kind":"chat","format":"anthropic","stream":true,"status":200,"ms":1840.5,"ttft":212,"input":1200,"output":340,"cached":200,"cost":4650000,"guard":{"action":"redacted","hits":[{"check":"pii","label":"email","count":2}]},"client":"claude-code","local":true,"savedTokens":800,"agent":"coder","sessionId":"s-42","runId":"r-7","traceId":"4bf92f3577b34da6a3ce929d0e0e4736"}}}' > $F/usage.report/valid-full.json
echo '{"v":1,"type":"usage.report","seq":42,"ts":1767225630124,"body":{"record":{"ts":1767225630123,"keyId":"dashboard","provider":"ollama","model":"ollama/llama-5","status":599,"ms":30000,"cost":0,"error":"no answer"}}}' > $F/usage.report/valid-minimal.json
echo '{"v":1,"type":"usage.report","seq":43,"ts":1767225640124,"body":{"record":{"ts":1767225640123,"keyId":"k1","provider":"openai","model":"openai/gpt-6.1-sol","status":200,"ms":900,"ttft":null,"input":1000,"output":500,"cost":1500000},"budget":"unchecked"}}' > $F/usage.report/valid-unchecked.json
echo '{"v":1,"type":"usage.report","seq":43,"ts":1767225640124,"body":{"record":{"ts":1767225640123,"keyId":"k1","provider":"openai","model":"openai/gpt-6.1-sol","status":200,"ms":900,"cost":0.0015}}}' > $F/usage.report/invalid-cost-float.json
echo '{"v":1,"type":"usage.report","seq":43,"ts":1767225640124,"body":{"record":{"ts":1767225640123,"keyId":"k1","provider":"openai","model":"openai/gpt-6.1-sol","status":200,"ms":900,"cost":0},"budget":"skipped"}}' > $F/usage.report/invalid-budget-word.json
echo '{"v":1,"type":"usage.report","seq":43,"ts":1767225640124,"body":{"record":{"ts":1767225640123,"keyId":"k1","provider":"openai","model":"openai/gpt-6.1-sol","kind":"image","status":200,"ms":900,"cost":0}}}' > $F/usage.report/invalid-kind.json
echo '{"v":1,"type":"usage.report","seq":43,"ts":1767225640124,"body":{"record":{"ts":1767225640123,"keyId":"k1","provider":"openai","model":"openai/gpt-6.1-sol","status":200,"ms":900,"cost":0,"region":"eu"}}}' > $F/usage.report/invalid-unknown-field.json

echo '{"v":1,"type":"audit.event","seq":44,"ts":1767225650000,"body":{"log":"governance","event":{"action":"key.update","target":{"type":"key","id":"k1","name":"ci"},"outcome":"ok","status":200,"changes":[{"path":"rpm","from":60,"to":120},{"path":"guardrails.pii","from":null,"to":"block"}],"actor":{"via":"apikey","keyId":"k1"}}}}' > $F/audit.event/valid-governance.json
echo '{"v":1,"type":"audit.event","seq":45,"ts":1767225650200,"body":{"log":"mcp","event":{"ts":1767225650200,"integration":"dynatrace","tool":"list_problems","ok":true,"ms":312,"args":["from","limit"],"keyId":"k1"}}}' > $F/audit.event/valid-mcp.json
echo '{"v":1,"type":"audit.event","seq":45,"ts":1767225650200,"body":{"log":"other","event":{"action":"key.update"}}}' > $F/audit.event/invalid-log.json
echo '{"v":1,"type":"audit.event","seq":45,"ts":1767225650200,"body":{"log":"governance","event":{"ts":1767225650200,"integration":"dynatrace","tool":"list_problems","ok":true,"ms":312,"args":[]}}}' > $F/audit.event/invalid-governance-shape.json
echo '{"v":1,"type":"audit.event","seq":45,"ts":1767225650200,"body":{"log":"governance","event":{"action":"key.update","changes":[{"from":1,"to":2}]}}}' > $F/audit.event/invalid-changes-path-missing.json

echo '{"v":1,"type":"trace.span","seq":46,"ts":1767225650300,"body":{"span":{"t":"4bf92f3577b34da6a3ce929d0e0e4736","s":"00f067aa0ba902b7","p":"53995c3f42cd8ad8","n":"chat openai/gpt-6.1-sol","k":"client","st":1767225600123000,"d":1840500,"ok":true,"src":"gateway","svc":"zaafpulse","a":{"gen_ai.request.model":"gpt-6.1-sol","gen_ai.usage.input_tokens":1200,"http.response.status_code":200}}}}' > $F/trace.span/valid.json
echo '{"v":1,"type":"trace.span","seq":47,"ts":1767225650400,"body":{"span":{"t":"4bf92f3577b34da6a3ce929d0e0e4736","s":"53995c3f42cd8ad8","p":null,"n":"POST /v1/chat/completions","k":"server","st":1767225600100000,"d":1900000,"ok":false,"err":"upstream answered 503","src":"gateway","svc":"zaafpulse","a":{}}}}' > $F/trace.span/valid-root-error.json
echo '{"v":1,"type":"trace.span","seq":47,"ts":1767225650400,"body":{"span":{"t":"4BF92F3577B34DA6A3CE929D0E0E4736","s":"53995c3f42cd8ad8","p":null,"n":"x","k":"server","st":1,"d":1,"ok":true,"src":"gateway","svc":"zaafpulse","a":{}}}}' > $F/trace.span/invalid-trace-id-upper.json
echo '{"v":1,"type":"trace.span","seq":47,"ts":1767225650400,"body":{"span":{"t":"4bf92f3577b34da6a3ce929d0e0e4736","s":"53995c3f42cd8ad8","p":null,"n":"x","k":"producer","st":1,"d":1,"ok":true,"src":"gateway","svc":"zaafpulse","a":{}}}}' > $F/trace.span/invalid-kind.json

echo '{"v":1,"type":"error","seq":4,"ts":1767225600010,"body":{"code":"protocol","message":"config.push: body.gen: out of range"}}' > $F/error/valid.json
echo '{"v":1,"type":"error","seq":4,"ts":1767225600010,"body":{"message":"no code"}}' > $F/error/invalid-missing-code.json
find $F -name '*.json' | wc -l
```
Expected: `57`. Then check that every file parses: `for f in $(find ipc/v1/fixtures -name '*.json'); do node -e "JSON.parse(require('fs').readFileSync('$f','utf8'))" || echo "BAD $f"; done` prints nothing. Check the three `budget.snapshot` invalid files by eye: `invalid-scope-word.json` has `"scope":"all"`, `invalid-enforce.json` has `"enforce":"stop"`, `invalid-missing-state.json` has no `"state"`.

- [ ] **Step 7: Run the schema tests to see them fail**

Run: `node --test test/ipc-schema.test.js 2>&1 | tail -3`
Expected: FAIL: `Cannot find module '../lib/ipc/schema'`.

- [ ] **Step 8: Write `lib/ipc/schema.js`**

```js
// The IPC v1 contract on the control-plane side (P3 D6): the JSON Schemas in ipc/v1 and a validator for the subset of
// JSON Schema they use (type, const, enum, properties, required, additionalProperties, items, minimum, maximum,
// minLength, minItems, pattern, oneOf). No dependency. The schemas are strict (additionalProperties: false); the
// production reader is lenient ({ lenient: true }): an unknown field is ignored, as the version rule wants (D2).
const fs = require('fs');
const path = require('path');

const PROTOCOL = 1;
const DIR = path.join(__dirname, '..', '..', 'ipc', 'v1');
const TYPES = [
  'hello',
  'hello.ok',
  'hello.refused',
  'config.push',
  'secrets.handoff',
  'budget.snapshot',
  'shutdown',
  'ack',
  'ready',
  'health',
  'usage.report',
  'audit.event',
  'trace.span',
  'error',
];
// The gateway's events: written to its outbox first, carrying the stream's durable seq, acknowledged once stored.
const EVENTS = ['usage.report', 'audit.event', 'trace.span'];
const KEYWORDS = new Set([
  '$schema',
  'title',
  'description',
  'type',
  'const',
  'enum',
  'properties',
  'required',
  'additionalProperties',
  'items',
  'minimum',
  'maximum',
  'minLength',
  'minItems',
  'pattern',
  'oneOf',
]);

const kindOf = (v) =>
  v === null
    ? 'null'
    : Array.isArray(v)
      ? 'array'
      : typeof v === 'number'
        ? Number.isInteger(v)
          ? 'integer'
          : 'number'
        : typeof v;
const typeMatches = (t, v) => t === kindOf(v) || (t === 'number' && kindOf(v) === 'integer');

/**
 * The errors of `value` against `schema` ([] when it is valid). `lenient` ignores additionalProperties: false.
 * @param {Record<string, any>} schema
 * @param {unknown} value
 * @param {{ lenient?: boolean }} [opts]
 * @param {string} [at]
 * @returns {string[]}
 */
function validate(schema, value, opts = {}, at = '$') {
  if (schema.type !== undefined) {
    const types = Array.isArray(schema.type) ? schema.type : [schema.type];
    if (!types.some((t) => typeMatches(t, value))) return [`${at}: must be ${types.join(' or ')}`];
  }
  const errors = [];
  if (Object.hasOwn(schema, 'const') && value !== schema.const)
    errors.push(`${at}: must be ${JSON.stringify(schema.const)}`);
  if (schema.enum && !schema.enum.includes(value))
    errors.push(`${at}: must be one of ${schema.enum.map((x) => JSON.stringify(x)).join(', ')}`);
  if (typeof value === 'number') {
    if (schema.minimum !== undefined && value < schema.minimum) errors.push(`${at}: must be at least ${schema.minimum}`);
    if (schema.maximum !== undefined && value > schema.maximum) errors.push(`${at}: must be at most ${schema.maximum}`);
  }
  if (typeof value === 'string') {
    if (schema.minLength !== undefined && value.length < schema.minLength)
      errors.push(`${at}: must be at least ${schema.minLength} characters`);
    if (schema.pattern !== undefined && !new RegExp(schema.pattern, 'u').test(value))
      errors.push(`${at}: must match ${schema.pattern}`);
  }
  if (Array.isArray(value)) {
    if (schema.minItems !== undefined && value.length < schema.minItems)
      errors.push(`${at}: must have at least ${schema.minItems} items`);
    if (schema.items) value.forEach((x, i) => errors.push(...validate(schema.items, x, opts, `${at}[${i}]`)));
  }
  if (kindOf(value) === 'object') {
    const obj = /** @type {Record<string, unknown>} */ (value);
    for (const r of schema.required || []) if (!Object.hasOwn(obj, r)) errors.push(`${at}.${r}: required`);
    const props = schema.properties || {};
    const extra = schema.additionalProperties;
    for (const [k, v] of Object.entries(obj)) {
      if (Object.hasOwn(props, k)) errors.push(...validate(props[k], v, opts, `${at}.${k}`));
      else if (extra === false) {
        if (!opts.lenient) errors.push(`${at}.${k}: unknown field`);
      } else if (extra && typeof extra === 'object') errors.push(...validate(extra, v, opts, `${at}.${k}`));
    }
  }
  if (schema.oneOf) {
    const matching = schema.oneOf.filter((s) => validate(s, value, opts, at).length === 0).length;
    if (matching !== 1) errors.push(`${at}: must match exactly one of ${schema.oneOf.length} shapes (matches ${matching})`);
  }
  return errors;
}

/**
 * The keywords of `schema` (and its subschemas) that validate() does not implement, as "<path>: <keyword>".
 * @param {Record<string, any>} schema
 * @param {string} [at]
 * @returns {string[]}
 */
function unsupported(schema, at = '$') {
  const out = Object.keys(schema)
    .filter((k) => !KEYWORDS.has(k))
    .map((k) => `${at}: ${k}`);
  for (const [k, s] of Object.entries(schema.properties || {})) out.push(...unsupported(s, `${at}.${k}`));
  if (schema.items) out.push(...unsupported(schema.items, `${at}[]`));
  if (schema.additionalProperties && typeof schema.additionalProperties === 'object')
    out.push(...unsupported(schema.additionalProperties, `${at}.*`));
  (schema.oneOf || []).forEach((s, i) => out.push(...unsupported(s, `${at}/oneOf/${i}`)));
  return out;
}

let cache = null;
/** @returns {Record<string, Record<string, any>>} the envelope schema and one schema per message type */
function schemas() {
  if (cache) return cache;
  const load = (name) => JSON.parse(fs.readFileSync(path.join(DIR, `${name}.schema.json`), 'utf8'));
  const map = { envelope: load('envelope') };
  for (const t of TYPES) map[t] = load(t);
  return (cache = map);
}

/**
 * A whole message: the envelope, then the body against its type's schema. An envelope that is valid but names a type
 * v1 does not know is `unknownType` (the caller logs it once and drops it, D2), not an error.
 * @param {unknown} msg
 * @param {{ lenient?: boolean }} [opts]
 * @returns {{ errors: string[], unknownType: boolean }}
 */
function check(msg, { lenient = false } = {}) {
  const s = schemas();
  const errors = validate(s.envelope, msg, { lenient });
  if (errors.length) return { errors, unknownType: false };
  const m = /** @type {{ type: string, body: unknown }} */ (msg);
  if (!TYPES.includes(m.type)) return { errors: [], unknownType: true };
  return { errors: validate(s[m.type], m.body, { lenient }, '$.body'), unknownType: false };
}

module.exports = { PROTOCOL, DIR, TYPES, EVENTS, validate, unsupported, schemas, check };
```

Run: `npx --yes prettier@3 --write lib/ipc/schema.js test/ipc-schema.test.js`

- [ ] **Step 9: Run the schema tests**

Run: `node --test test/ipc-schema.test.js 2>&1 | grep -E "^ℹ (pass|fail)"`
Expected: `ℹ pass 61`, `ℹ fail 0` (57 fixture tests, the folder test and three validator tests). If a fixture fails, the message names the path that is wrong: fix the fixture so it fails or passes **for the reason in its name**, never the schema to fit a fixture.

- [ ] **Step 10: Write the `lib/types.js` sync test**

Create `test/ipc-types.test.js`:

```js
// lib/types.js and ipc/v1 stay in step (P3 D6): every property of a typedef that crosses the boundary exists in its
// schema with a compatible type, and is required there exactly when the typedef does not mark it optional. A new
// typedef must be mapped here or listed as not in v1, so the contract cannot drift silently.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const { schemas } = require('../lib/ipc/schema');

const TYPES_JS = fs.readFileSync(path.join(__dirname, '..', 'lib', 'types.js'), 'utf8');

// typedef -> [schema (message type), path inside its body: a property name, '[]' for items, 'oneOf:N' for a branch]
const MAP = {
  UsageRecord: ['usage.report', ['record']],
  GuardrailOutcome: ['usage.report', ['record', 'guard']],
  GuardrailHit: ['usage.report', ['record', 'guard', 'hits', '[]']],
  BudgetSnapshot: ['budget.snapshot', []],
  BudgetView: ['budget.snapshot', ['budgets', '[]']],
  AuditEvent: ['audit.event', ['oneOf:0', 'event']],
  AuditTarget: ['audit.event', ['oneOf:0', 'event', 'target']],
  GatewayConfigSection: ['config.push', ['gateway']],
  GatewayProviderConfig: ['config.push', ['gateway', 'providers', '[]']],
  GatewayRouteConfig: ['config.push', ['gateway', 'routes', '[]']],
  GuardrailsPolicy: ['config.push', ['gateway', 'guardrails']],
  GatewayKeyConfig: ['config.push', ['keys', '[]']],
};
const NOT_IN_V1 = {
  Money: 'a scalar: integer nano-USD, written "type": "integer" where it appears',
  BudgetCheckInput: 'asked inside the gateway; no v1 message carries it',
  BudgetRefusal: 'answered inside the gateway as a 402; no v1 message carries it',
};
// Properties a schema deliberately does not carry where the typedef has them.
const MOVED = {
  'GatewayConfigSection.keys': 'sent as config.push keys (GatewayKeyConfig plus hash), checked as GatewayKeyConfig',
  'GatewayProviderConfig.apiKey': 'a secret reference: the control plane resolves it and sends the value in secrets.handoff',
};

function typedefs(text) {
  const out = {};
  for (const block of text.match(/\/\*\*[\s\S]*?\*\//g) || []) {
    const head = block.match(/@typedef\s+\{[^}]+\}\s+(\w+)/);
    if (!head) continue;
    const props = [];
    for (const m of block.matchAll(/@property\s+\{(.+?)\}\s+(\[?)([\w$]+)\]?/g))
      props.push({ type: m[1], optional: m[2] === '[', name: m[3] });
    out[head[1]] = props;
  }
  return out;
}

function splitTop(s) {
  const parts = [];
  let depth = 0;
  let cur = '';
  for (const c of s) {
    if ('{<('.includes(c)) depth++;
    else if ('}>)'.includes(c)) depth--;
    if (c === '|' && depth === 0) {
      parts.push(cur.trim());
      cur = '';
    } else cur += c;
  }
  parts.push(cur.trim());
  return parts;
}

function jsdocKinds(type) {
  const kinds = new Set();
  for (const part of splitTop(type)) {
    if (part === 'string' || /^'.*'$/.test(part)) kinds.add('string');
    else if (part === 'number') kinds.add('number');
    else if (part === 'Money') kinds.add('integer');
    else if (part === 'boolean' || part === 'true' || part === 'false') kinds.add('boolean');
    else if (part === 'null') kinds.add('null');
    else if (part === 'unknown') kinds.add('any');
    else if (part.endsWith('[]') || part.startsWith('Array<')) kinds.add('array');
    else if (part.startsWith('{') || /^(Record|Partial)</.test(part) || /^[A-Z]\w*$/.test(part)) kinds.add('object');
    else throw new Error(`lib/types.js: a type this test cannot read: ${part}`);
  }
  return kinds;
}

function schemaKinds(node) {
  if (node.type) return new Set(Array.isArray(node.type) ? node.type : [node.type]);
  if (node.oneOf) return new Set(node.oneOf.flatMap((b) => [...schemaKinds(b)]));
  if (Object.hasOwn(node, 'const')) return new Set([node.const === null ? 'null' : typeof node.const]);
  return new Set(['any']);
}

function compatible(jsdoc, schema) {
  if (schema.has('any')) return true;
  for (const k of jsdoc) {
    if (k === 'any') continue;
    if (k === 'number' || k === 'integer') {
      if (!schema.has('number') && !schema.has('integer')) return false;
    } else if (!schema.has(k)) return false;
  }
  return true;
}

function resolve(schema, steps) {
  let node = schema;
  for (const s of steps) {
    if (s === '[]') node = node.items;
    else if (s.startsWith('oneOf:')) node = node.oneOf[Number(s.slice(6))];
    else node = node.properties && node.properties[s];
    if (!node) throw new Error(`no schema node at ${steps.join(' > ')}`);
  }
  return node;
}

const defs = typedefs(TYPES_JS);

test('the parser reads lib/types.js', () => {
  assert.ok(defs.UsageRecord.some((p) => p.name === 'cost' && p.type === 'number' && !p.optional));
  assert.ok(defs.BudgetView.some((p) => p.name === 'scope' && p.type === "'global'|{ dim: string, value: string }"));
  assert.ok(defs.AuditEvent.some((p) => p.name === 'actor' && p.optional));
});

test('every typedef in lib/types.js is mapped to a schema or listed as not in v1', () => {
  const unaccounted = Object.keys(defs).filter((n) => !MAP[n] && !NOT_IN_V1[n]);
  assert.deepEqual(unaccounted, []);
  for (const n of [...Object.keys(MAP), ...Object.keys(NOT_IN_V1)]) assert.ok(defs[n], `${n} is a typedef`);
});

for (const [name, [type, steps]] of Object.entries(MAP)) {
  test(`${name} matches ${type} ${steps.join(' > ') || '(body)'}`, () => {
    const node = resolve(schemas()[type], steps);
    for (const p of defs[name]) {
      if (MOVED[`${name}.${p.name}`]) {
        assert.ok(!node.properties[p.name], `${name}.${p.name} is not in the schema (${MOVED[`${name}.${p.name}`]})`);
        continue;
      }
      const prop = node.properties[p.name];
      assert.ok(prop, `${name}.${p.name} is in the schema`);
      const j = jsdocKinds(p.type);
      const s = schemaKinds(prop);
      assert.ok(compatible(j, s), `${name}.${p.name}: typedef {${p.type}} vs schema ${[...s].join('|')}`);
      const required = (node.required || []).includes(p.name);
      assert.equal(required, !p.optional, `${name}.${p.name} is ${p.optional ? 'optional' : 'required'} in both`);
    }
  });
}

test('config.push keys carry every GatewayKeyConfig property plus the SHA-256 hash, and no secret reference', () => {
  const key = resolve(schemas()['config.push'], ['keys', '[]']);
  assert.deepEqual(Object.keys(key.properties).sort(), [...defs.GatewayKeyConfig.map((p) => p.name), 'hash'].sort());
  assert.equal(key.properties.hash.pattern, '^[0-9a-f]{64}$');
  const provider = resolve(schemas()['config.push'], ['gateway', 'providers', '[]']);
  assert.equal(provider.properties.apiKey, undefined);
});

test('Money crosses as an integer: usage cost and every price', () => {
  const s = schemas();
  assert.equal(resolve(s['usage.report'], ['record', 'cost']).type, 'integer');
  const price = s['config.push'].properties.pricing.additionalProperties;
  for (const k of ['input', 'output', 'cacheRead', 'cacheWrite5m', 'cacheWrite1h', 'webSearch'])
    assert.equal(price.properties[k].type, 'integer', k);
});
```

Run: `npx --yes prettier@3 --write test/ipc-types.test.js`

- [ ] **Step 11: Run the sync test**

Run: `node --test test/ipc-types.test.js 2>&1 | grep -E "^ℹ (pass|fail)"`
Expected: `ℹ pass 16`, `ℹ fail 0`. A failure names the typedef property and both types: fix the schema (Step 4) to match `lib/types.js`; `lib/types.js` is not changed in P3.

- [ ] **Step 12: Run both suites**

Run: `node --test test/*.test.js 2>&1 | grep -E "^ℹ (pass|fail)"` then `deno test -q --no-check --no-prompt -P=test test/*.test.js 2>&1 | tail -3`
Expected: Node `ℹ fail 0`; Deno `ok | … passed | 0 failed`.

- [ ] **Step 13: Commit**

```bash
git add docs/migration/2026-11-10-deno-go-migration-plan.md ipc lib/ipc/schema.js test/ipc-schema.test.js test/ipc-types.test.js
git commit -m "Freeze the v1 IPC schemas with shared fixtures, a JSON Schema subset validator and a types sync test"
git push origin migrate/p3-ipc-contract
```

---

### Task 2: Go module and messages (spec T2)

**Files:**
- Create: `gw/go.mod`, `gw/ipc/nullable.go`, `gw/ipc/shape.go`, `gw/ipc/messages.go`, `gw/ipc/envelope.go`
- Test: `gw/ipc/fixtures_test.go`, `gw/ipc/messages_test.go`
- Modify: `.github/workflows/ci.yml` (header comment; `test` and `deno` jobs; new `go` job), `package.json` (`test:go`)

**Interfaces:**
- Consumes: `ipc/v1/fixtures/**` (Task 1), read from `gw/ipc` as `../../ipc/v1/fixtures`.
- Produces (package `github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc`): `const Version = 1`; `Type*` constants for the 14 types; `type Envelope struct{V int; Type string; Seq, Ts int64; ID, Re string; Body json.RawMessage}`; `type Message struct{Envelope; Body any; Unknown bool}`; `func Decode(line []byte, strict bool) (Message, error)`; `func Encode(env Envelope, body any) ([]byte, error)` (no newline); `func Types() []string`; body structs `Hello, HelloOK, HelloRefused, ConfigPush, SecretsHandoff, BudgetSnapshot, Shutdown, Ack, Ready, Health, UsageReport, AuditEventBody (Governance(), MCP()), TraceSpanBody, ErrorBody` and their parts (`UsageRecord`, `BudgetView`, `BudgetScope`, `AuditEvent`, `MCPAuditEntry`, `SpanRecord`, `ProviderSecret`, `OAuthToken`, …); `Nullable[T]`; unexported `decodeBody(raw, v, strict, path)`, `checkShape`, `patterns` (used by later tasks in the same package). Decoded bodies are pointers (`*ipc.Hello`, …).

- [ ] **Step 1: Create the module**

`gw/go.mod`:
```
module github.com/rifqifitriady/ZAAFPulse-deno-go/gw

go 1.27
```

Run: `go -C gw mod tidy && git status --short gw`
Expected: only `gw/go.mod` (standard library only: no `go.sum` appears).

- [ ] **Step 2: Write the failing fixture tests**

`gw/ipc/fixtures_test.go`:
```go
package ipc

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

var fixtureRoot = filepath.Join("..", "..", "ipc", "v1", "fixtures")

// Folders of fixtures that are not message types: envelope-level cases and the budget decisions (budget_test.go).
var notTypes = map[string]bool{"envelope": true, "budget-decide": true}

func TestEveryTypeHasFixtures(t *testing.T) {
	entries, err := os.ReadDir(fixtureRoot)
	if err != nil {
		t.Fatal(err)
	}
	var folders []string
	for _, e := range entries {
		if e.IsDir() && !notTypes[e.Name()] {
			folders = append(folders, e.Name())
		}
	}
	if !slices.Equal(folders, Types()) {
		t.Fatalf("fixture folders %v, message types %v", folders, Types())
	}
	for _, typ := range Types() {
		files, _ := os.ReadDir(filepath.Join(fixtureRoot, typ))
		var valid, invalid bool
		for _, f := range files {
			valid = valid || strings.HasPrefix(f.Name(), "valid")
			invalid = invalid || strings.HasPrefix(f.Name(), "invalid")
		}
		if !valid || !invalid {
			t.Errorf("%s: needs a valid and an invalid fixture", typ)
		}
	}
}

// Every valid fixture decodes strictly and encodes to the same JSON value; every invalid one is refused; an
// invalid-unknown-field fixture decodes leniently (the v1 version rule).
func TestFixtures(t *testing.T) {
	entries, _ := os.ReadDir(fixtureRoot)
	n := 0
	for _, d := range entries {
		if !d.IsDir() || d.Name() == "budget-decide" {
			continue
		}
		files, _ := os.ReadDir(filepath.Join(fixtureRoot, d.Name()))
		for _, f := range files {
			folder, name := d.Name(), f.Name()
			n++
			t.Run(folder+"/"+name, func(t *testing.T) {
				data, err := os.ReadFile(filepath.Join(fixtureRoot, folder, name))
				if err != nil {
					t.Fatal(err)
				}
				data = bytes.TrimSpace(data)
				msg, err := Decode(data, true)
				switch {
				case strings.HasPrefix(name, "valid"):
					if err != nil {
						t.Fatalf("refused: %v", err)
					}
					if msg.Unknown || (folder != "envelope" && msg.Type != folder) {
						t.Fatalf("type %q in folder %q", msg.Type, folder)
					}
					out, err := Encode(msg.Envelope, msg.Body)
					if err != nil {
						t.Fatal(err)
					}
					var a, b any
					_ = json.Unmarshal(data, &a)
					_ = json.Unmarshal(out, &b)
					if !reflect.DeepEqual(a, b) {
						t.Fatalf("the round trip changed it:\n in: %s\nout: %s", data, out)
					}
				case strings.HasPrefix(name, "invalid"):
					if err == nil && !msg.Unknown {
						t.Fatal("accepted")
					}
				default:
					t.Fatal("a fixture is named valid[-name].json or invalid[-name].json")
				}
				if strings.HasPrefix(name, "invalid-unknown-field") {
					if _, err := Decode(data, false); err != nil {
						t.Fatalf("the lenient decode refused it: %v", err)
					}
				}
			})
		}
	}
	if n < 57 {
		t.Fatalf("only %d fixtures", n)
	}
}

func TestUnknownTypeIsReportedNotRefused(t *testing.T) {
	msg, err := Decode([]byte(`{"v":1,"type":"telemetry.v9","seq":1,"ts":0,"body":{"x":1}}`), true)
	if err != nil || !msg.Unknown || msg.Type != "telemetry.v9" {
		t.Fatalf("got %+v, %v", msg, err)
	}
}
```

`gw/ipc/messages_test.go`:
```go
package ipc

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestSecretsNeverPrint(t *testing.T) {
	v, tok := "test-openai-key-0001", "test-vertex-token-0001"
	h := SecretsHandoff{Gen: 1, Providers: []ProviderSecret{{ID: "openai", Value: &v}, {ID: "vertex", Token: &OAuthToken{Value: tok, ExpiresAt: 1}}}, Mask: []string{v, tok}}
	for _, s := range []string{fmt.Sprint(h), fmt.Sprintf("%v %+v", h, &h), fmt.Sprint(h.Providers), fmt.Sprintf("%+v", *h.Providers[1].Token)} {
		if strings.Contains(s, v) || strings.Contains(s, tok) {
			t.Fatalf("a secret reached a formatted string: %s", s)
		}
	}
}

func TestNullableKeepsAbsentNullAndValue(t *testing.T) {
	for _, in := range []string{
		`{"ts":1,"keyId":"k","provider":"p","model":"m","status":200,"ms":1,"cost":0}`,
		`{"ts":1,"keyId":"k","provider":"p","model":"m","status":200,"ms":1,"cost":0,"ttft":null}`,
		`{"ts":1,"keyId":"k","provider":"p","model":"m","status":200,"ms":1,"cost":0,"ttft":12.5}`,
	} {
		line := `{"v":1,"type":"usage.report","seq":1,"ts":0,"body":{"record":` + in + `}}`
		msg, err := Decode([]byte(line), true)
		if err != nil {
			t.Fatal(err)
		}
		out, _ := Encode(msg.Envelope, msg.Body)
		var a, b any
		_ = json.Unmarshal([]byte(line), &a)
		_ = json.Unmarshal(out, &b)
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("\n in: %s\nout: %s", line, out)
		}
	}
}

func TestOptionalFieldMayNotBeNull(t *testing.T) {
	line := `{"v":1,"type":"usage.report","seq":1,"ts":0,"body":{"record":{"ts":1,"keyId":"k","provider":"p","model":"m","status":200,"ms":1,"cost":0,"requested":null}}}`
	if _, err := Decode([]byte(line), false); err == nil || !strings.Contains(err.Error(), "requested: must not be null") {
		t.Fatalf("got %v", err)
	}
}
```

Run: `GOTOOLCHAIN=local go -C gw test ./... 2>&1 | head -3`
Expected: FAIL to build: `undefined: Decode` (and the other names).

- [ ] **Step 3: Write `gw/ipc/nullable.go`**

```go
package ipc

import "encoding/json"

// Nullable is an optional field whose value may also be JSON null (TypeScript `T|null` behind `[name]`): absent,
// null and a value all survive a decode and an encode. Tag it `json:",omitzero"`.
type Nullable[T any] struct {
	V    T
	Null bool
	Set  bool
}

// Some is a present, non-null value.
func Some[T any](v T) Nullable[T] { return Nullable[T]{V: v, Set: true} }

// Null is a present null.
func Null[T any]() Nullable[T] { return Nullable[T]{Null: true, Set: true} }

// IsZero reports an absent field, so omitzero leaves it out.
func (n Nullable[T]) IsZero() bool { return !n.Set }

// MarshalJSON writes the value, or null.
func (n Nullable[T]) MarshalJSON() ([]byte, error) {
	if !n.Set || n.Null {
		return []byte("null"), nil
	}
	return json.Marshal(n.V)
}

// UnmarshalJSON reads a value or null; encoding/json calls it for a JSON null too.
func (n *Nullable[T]) UnmarshalJSON(b []byte) error {
	*n = Nullable[T]{Set: true}
	if string(b) == "null" {
		n.Null = true
		return nil
	}
	return json.Unmarshal(b, &n.V)
}
```

- [ ] **Step 4: Write `gw/ipc/shape.go`** (the schema rules `encoding/json` does not check)

```go
package ipc

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The structs in messages.go carry the schema rules of ipc/v1 that encoding/json does not check:
//   - a field without omitzero is required (it must be present);
//   - a required pointer is the nullable one (T|null); an optional field may be absent but never null;
//   - the ipc tag holds enum=a|b, min=N, max=N, minlen=N, minitems=N and pattern=hex16|hex32|hex64.
// checkShape enforces them on the decoded JSON value before it is decoded into the struct. In strict mode (tests) it
// also refuses a key that is not one of the struct's exact JSON names (encoding/json matches names case-insensitively).

var unmarshalerType = reflect.TypeFor[json.Unmarshaler]()

var patterns = map[string]*regexp.Regexp{
	"hex16": regexp.MustCompile(`^[0-9a-f]{16}$`),
	"hex32": regexp.MustCompile(`^[0-9a-f]{32}$`),
	"hex64": regexp.MustCompile(`^[0-9a-f]{64}$`),
}

// decodesItself: a type with its own UnmarshalJSON (Nullable, json.RawMessage, BudgetScope) checks its own value.
func decodesItself(t reflect.Type) bool { return reflect.PointerTo(t).Implements(unmarshalerType) }

func checkShape(v any, t reflect.Type, path string, strict bool) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if decodesItself(t) {
		return nil
	}
	switch t.Kind() {
	case reflect.Struct:
		obj, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: must be an object", path)
		}
		known := make(map[string]bool, t.NumField())
		for i := range t.NumField() {
			f := t.Field(i)
			name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "" || name == "-" {
				continue
			}
			known[name] = true
			p := path + "." + name
			optional := opts == "omitzero"
			val, present := obj[name]
			if !present {
				if optional {
					continue
				}
				return fmt.Errorf("%s: required", p)
			}
			if val == nil {
				if decodesItself(f.Type) || (f.Type.Kind() == reflect.Pointer && !optional) {
					continue
				}
				return fmt.Errorf("%s: must not be null", p)
			}
			if err := checkRules(val, f.Tag.Get("ipc"), p); err != nil {
				return err
			}
			if err := checkShape(val, f.Type, p, strict); err != nil {
				return err
			}
		}
		if strict {
			for k := range obj {
				if !known[k] {
					return fmt.Errorf("%s.%s: unknown field", path, k)
				}
			}
		}
	case reflect.Slice:
		arr, ok := v.([]any)
		if !ok {
			return fmt.Errorf("%s: must be an array", path)
		}
		elem := t.Elem()
		for i, x := range arr {
			p := fmt.Sprintf("%s[%d]", path, i)
			if x == nil && elem.Kind() != reflect.Interface && !decodesItself(elem) {
				return fmt.Errorf("%s: must not be null", p)
			}
			if err := checkShape(x, elem, p, strict); err != nil {
				return err
			}
		}
	case reflect.Map:
		obj, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: must be an object", path)
		}
		elem := t.Elem()
		for k, x := range obj {
			if x == nil && elem.Kind() != reflect.Interface {
				return fmt.Errorf("%s.%s: must not be null", path, k)
			}
			if err := checkShape(x, elem, path+"."+k, strict); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkRules applies one field's ipc tag to its decoded JSON value. A rule meant for another JSON type is skipped:
// a wrong type is the decoder's error.
func checkRules(v any, tag, path string) error {
	if tag == "" {
		return nil
	}
	for _, rule := range strings.Split(tag, ",") {
		key, arg, _ := strings.Cut(rule, "=")
		switch key {
		case "enum":
			s := fmt.Sprint(v)
			if !slices.Contains(strings.Split(arg, "|"), s) {
				return fmt.Errorf("%s: %q is not one of %s", path, s, arg)
			}
		case "min", "max":
			n, ok := v.(float64)
			if !ok {
				continue
			}
			lim, err := strconv.ParseFloat(arg, 64)
			if err != nil {
				return fmt.Errorf("%s: bad rule %q", path, rule)
			}
			if (key == "min" && n < lim) || (key == "max" && n > lim) {
				return fmt.Errorf("%s: %v is out of range (%s %s)", path, n, key, arg)
			}
		case "minlen":
			s, ok := v.(string)
			if ok && utf8.RuneCountInString(s) < atoi(arg) {
				return fmt.Errorf("%s: must be at least %s characters", path, arg)
			}
		case "minitems":
			a, ok := v.([]any)
			if ok && len(a) < atoi(arg) {
				return fmt.Errorf("%s: must have at least %s items", path, arg)
			}
		case "pattern":
			re := patterns[arg]
			if re == nil {
				return fmt.Errorf("%s: unknown pattern %q", path, arg)
			}
			if s, ok := v.(string); ok && !re.MatchString(s) {
				return fmt.Errorf("%s: must match %s", path, re)
			}
		default:
			return fmt.Errorf("%s: unknown rule %q", path, rule)
		}
	}
	return nil
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
```

- [ ] **Step 5: Write `gw/ipc/messages.go`**

Every field mirrors `ipc/v1/<type>.schema.json`: required ⇔ no `omitzero`; a nullable required field is a pointer without `omitzero`; an optional scalar is a pointer with `omitzero` (so `0`, `false` and `[]` survive a round trip); `ipc:"…"` carries `enum`, `min`, `max`, `minlen`, `minitems`, `pattern`.

```go
package ipc

import (
	"encoding/json"
	"errors"
	"fmt"
)

// The bodies of the v1 messages (ipc/v1/*.schema.json). Field rules: see shape.go. Money fields are int64 nano-USD.

// Message type names (D3).
const (
	TypeHello          = "hello"
	TypeHelloOK        = "hello.ok"
	TypeHelloRefused   = "hello.refused"
	TypeConfigPush     = "config.push"
	TypeSecretsHandoff = "secrets.handoff"
	TypeBudgetSnapshot = "budget.snapshot"
	TypeShutdown       = "shutdown"
	TypeAck            = "ack"
	TypeReady          = "ready"
	TypeHealth         = "health"
	TypeUsageReport    = "usage.report"
	TypeAuditEvent     = "audit.event"
	TypeTraceSpan      = "trace.span"
	TypeError          = "error"
)

// Hello is the gateway's first message: the protocols it speaks, its version, its durable event stream and the
// first event the control plane has not acknowledged.
type Hello struct {
	Protocol   []int  `json:"protocol" ipc:"minitems=1"`
	GwVersion  string `json:"gwVersion" ipc:"minlen=1"`
	Stream     string `json:"stream" ipc:"pattern=hex32"`
	OutboxFrom int64  `json:"outboxFrom" ipc:"min=1"`
}

// HelloOK is the control plane's answer: the protocol both speak.
type HelloOK struct {
	Protocol  int    `json:"protocol" ipc:"min=1"`
	CpVersion string `json:"cpVersion" ipc:"minlen=1"`
}

// HelloRefused ends the handshake; the gateway exits 2.
type HelloRefused struct {
	Reason string `json:"reason" ipc:"minlen=1"`
}

// ConfigPush is the gateway section of config.json (no keys, no secret references), the keys with their hashes and
// the price table in nano-USD per 1M tokens.
type ConfigPush struct {
	Gen     int64                 `json:"gen" ipc:"min=1"`
	Gateway GatewayConfig         `json:"gateway"`
	Keys    []GatewayKeyAuth      `json:"keys"`
	Pricing map[string]PriceEntry `json:"pricing"`
}

type GatewayConfig struct {
	Providers  []ProviderConfig `json:"providers"`
	Routes     []RouteConfig    `json:"routes"`
	Guardrails GuardrailsPolicy `json:"guardrails"`
	Defaults   Defaults         `json:"defaults"`
	TokenSaver bool             `json:"tokenSaver"`
	ModelMap   []ModelMapEntry  `json:"modelMap"`
}

type ProviderConfig struct {
	ID         string            `json:"id" ipc:"minlen=1"`
	Preset     string            `json:"preset"`
	BaseURL    string            `json:"baseUrl"`
	Models     []string          `json:"models,omitzero"`
	Aliases    map[string]string `json:"aliases,omitzero"`
	Enabled    *bool             `json:"enabled,omitzero"`
	Tier       *string           `json:"tier,omitzero" ipc:"enum=private|external"`
	Approved   *bool             `json:"approved,omitzero"`
	Auth       *ProviderAuth     `json:"auth,omitzero"`
	Format     *string           `json:"format,omitzero" ipc:"enum=openai|anthropic"`
	Responses  *bool             `json:"responses,omitzero"`
	Reasoning  *bool             `json:"reasoning,omitzero"`
	TokenParam *string           `json:"tokenParam,omitzero" ipc:"enum=max_tokens|max_completion_tokens"`
}

type ProviderAuth struct {
	Type     string  `json:"type" ipc:"minlen=1"`
	TokenURL *string `json:"tokenUrl,omitzero"`
	ClientID *string `json:"clientId,omitzero"`
	Scope    *string `json:"scope,omitzero"`
	Grant    *string `json:"grant,omitzero" ipc:"enum=client_credentials|refresh_token"`
}

type RouteConfig struct {
	Name        string   `json:"name" ipc:"minlen=1"`
	Targets     []string `json:"targets"`
	Strategy    *string  `json:"strategy,omitzero" ipc:"enum=fallback|round-robin"`
	Description *string  `json:"description,omitzero"`
}

type GuardrailsPolicy struct {
	Scope     string `json:"scope" ipc:"enum=external|all"`
	Secrets   string `json:"secrets" ipc:"enum=off|log|redact|block"`
	PII       string `json:"pii" ipc:"enum=off|log|redact|block"`
	Injection string `json:"injection" ipc:"enum=off|log|block"`
}

// GuardrailsPartial is a key's own guardrails: each setting overrides the gateway's when present.
type GuardrailsPartial struct {
	Scope     *string `json:"scope,omitzero" ipc:"enum=external|all"`
	Secrets   *string `json:"secrets,omitzero" ipc:"enum=off|log|redact|block"`
	PII       *string `json:"pii,omitzero" ipc:"enum=off|log|redact|block"`
	Injection *string `json:"injection,omitzero" ipc:"enum=off|log|block"`
}

type Defaults struct {
	Model        string `json:"model"`
	PlannerModel string `json:"plannerModel"`
	CodeModel    string `json:"codeModel"`
}

type ModelMapEntry struct {
	Match string `json:"match"`
	To    string `json:"to"`
}

// GatewayKeyAuth is a key as the gateway enforces it, with the SHA-256 hex of its secret (never the secret).
type GatewayKeyAuth struct {
	ID             string            `json:"id" ipc:"minlen=1"`
	Name           string            `json:"name"`
	Scopes         []string          `json:"scopes"`
	Models         []string          `json:"models"`
	Agents         []string          `json:"agents"`
	Knowledge      []string          `json:"knowledge"`
	BudgetUSD      *float64          `json:"budgetUSD"`
	RPM            int64             `json:"rpm" ipc:"min=0"`
	PrivateOnly    bool              `json:"privateOnly"`
	ExpiresAt      *int64            `json:"expiresAt"`
	RevokedAt      *int64            `json:"revokedAt"`
	Guardrails     GuardrailsPartial `json:"guardrails"`
	Prefix         string            `json:"prefix"`
	ApproveOwnRuns bool              `json:"approveOwnRuns"`
	CreatedAt      int64             `json:"createdAt" ipc:"min=0"`
	LastUsedAt     *int64            `json:"lastUsedAt"`
	Hash           string            `json:"hash" ipc:"pattern=hex64"`
}

// PriceEntry is one model's list price in nano-USD per 1M tokens (WebSearch: per search).
type PriceEntry struct {
	Input        int64       `json:"input" ipc:"min=0"`
	Output       int64       `json:"output" ipc:"min=0"`
	CacheRead    *int64      `json:"cacheRead,omitzero" ipc:"min=0"`
	CacheWrite5m *int64      `json:"cacheWrite5m,omitzero" ipc:"min=0"`
	CacheWrite1h *int64      `json:"cacheWrite1h,omitzero" ipc:"min=0"`
	WebSearch    *int64      `json:"webSearch,omitzero" ipc:"min=0"`
	Tiers        []PriceTier `json:"tiers,omitzero"`
	Estimate     *bool       `json:"estimate,omitzero"`
}

type PriceTier struct {
	Above        int64  `json:"above" ipc:"min=0"`
	Input        int64  `json:"input" ipc:"min=0"`
	Output       int64  `json:"output" ipc:"min=0"`
	CacheRead    *int64 `json:"cacheRead,omitzero" ipc:"min=0"`
	CacheWrite5m *int64 `json:"cacheWrite5m,omitzero" ipc:"min=0"`
	CacheWrite1h *int64 `json:"cacheWrite1h,omitzero" ipc:"min=0"`
}

// SecretsHandoff carries resolved provider secrets. They live in memory only: String hides them from every log.
type SecretsHandoff struct {
	Gen       int64            `json:"gen" ipc:"min=1"`
	Providers []ProviderSecret `json:"providers"`
	Mask      []string         `json:"mask"`
}

func (s SecretsHandoff) String() string {
	return fmt.Sprintf("SecretsHandoff{gen %d, %d providers, %d masked values}", s.Gen, len(s.Providers), len(s.Mask))
}

type ProviderSecret struct {
	ID    string      `json:"id" ipc:"minlen=1"`
	Value *string     `json:"value,omitzero"`
	Token *OAuthToken `json:"token,omitzero"`
}

func (p ProviderSecret) String() string { return "ProviderSecret{" + p.ID + ", <redacted>}" }

type OAuthToken struct {
	Value     string `json:"value" ipc:"minlen=1"`
	ExpiresAt int64  `json:"expiresAt" ipc:"min=0"`
}

func (t OAuthToken) String() string {
	return fmt.Sprintf("OAuthToken{<redacted>, expires %d}", t.ExpiresAt)
}

// BudgetSnapshot is the budgets as the guard enforces them, with each key's cost center.
type BudgetSnapshot struct {
	At        int64             `json:"at" ipc:"min=0"`
	Budgets   []BudgetView      `json:"budgets"`
	KeyCenter map[string]string `json:"keyCenter"`
}

type BudgetView struct {
	ID        string      `json:"id" ipc:"minlen=1"`
	Name      string      `json:"name"`
	Scope     BudgetScope `json:"scope"`
	Period    string      `json:"period" ipc:"enum=day|week|month"`
	Currency  string      `json:"currency"`
	Measure   string      `json:"measure" ipc:"enum=list|billed"`
	Enforce   string      `json:"enforce" ipc:"enum=none|block|pause"`
	Amount    float64     `json:"amount"`
	From      string      `json:"from"`
	To        string      `json:"to"`
	State     string      `json:"state" ipc:"minlen=1"`
	SpentUSD  *float64    `json:"spentUSD"`
	AmountUSD *float64    `json:"amountUSD"`
	Reason    *string     `json:"reason"`
}

// BudgetScope is "global" or {"dim", "value"}.
type BudgetScope struct {
	Global bool
	Dim    string
	Value  string
}

func (s BudgetScope) MarshalJSON() ([]byte, error) {
	if s.Global {
		return []byte(`"global"`), nil
	}
	return json.Marshal(struct {
		Dim   string `json:"dim"`
		Value string `json:"value"`
	}{s.Dim, s.Value})
}

func (s *BudgetScope) UnmarshalJSON(b []byte) error {
	var word string
	if err := json.Unmarshal(b, &word); err == nil {
		if word != "global" {
			return fmt.Errorf("budget scope %q is not \"global\" or an object", word)
		}
		*s = BudgetScope{Global: true}
		return nil
	}
	var o map[string]any
	if err := json.Unmarshal(b, &o); err != nil || o == nil {
		return errors.New(`budget scope must be "global" or {"dim", "value"}`)
	}
	dim, _ := o["dim"].(string)
	value, _ := o["value"].(string)
	if dim == "" || value == "" {
		return errors.New("budget scope needs a non-empty dim and value")
	}
	*s = BudgetScope{Dim: dim, Value: value}
	return nil
}

type Shutdown struct {
	GraceMs int64 `json:"graceMs" ipc:"min=0"`
}

type Ack struct {
	Stream string `json:"stream" ipc:"pattern=hex32"`
	UpTo   int64  `json:"upTo" ipc:"min=0"`
}

type Ready struct {
	Gen int64 `json:"gen" ipc:"min=1"`
}

type Health struct {
	Inflight      int64  `json:"inflight" ipc:"min=0"`
	OutboxDepth   int64  `json:"outboxDepth" ipc:"min=0"`
	UptimeMs      int64  `json:"uptimeMs" ipc:"min=0"`
	Gen           int64  `json:"gen" ipc:"min=0"`
	SnapshotAgeMs *int64 `json:"snapshotAgeMs" ipc:"min=0"`
}

// UsageReport is one gateway call; Budget is "unchecked" when no fresh snapshot could check it.
type UsageReport struct {
	Record UsageRecord `json:"record"`
	Budget *string     `json:"budget,omitzero" ipc:"enum=unchecked"`
}

type UsageRecord struct {
	Ts          int64             `json:"ts" ipc:"min=0"`
	KeyID       string            `json:"keyId" ipc:"minlen=1"`
	Provider    string            `json:"provider"`
	Model       string            `json:"model"`
	Requested   *string           `json:"requested,omitzero"`
	RoutedTo    *string           `json:"routedTo,omitzero"`
	Kind        *string           `json:"kind,omitzero" ipc:"enum=chat|agent|embeddings|mcp"`
	Format      *string           `json:"format,omitzero" ipc:"enum=openai|anthropic|responses|gemini"`
	Stream      *bool             `json:"stream,omitzero"`
	Status      int64             `json:"status" ipc:"min=0"`
	Ms          float64           `json:"ms" ipc:"min=0"`
	TTFT        Nullable[float64] `json:"ttft,omitzero"`
	Input       *int64            `json:"input,omitzero" ipc:"min=0"`
	Output      *int64            `json:"output,omitzero" ipc:"min=0"`
	Cached      *int64            `json:"cached,omitzero" ipc:"min=0"`
	Cost        int64             `json:"cost" ipc:"min=0"`
	Error       *string           `json:"error,omitzero"`
	Guard       *GuardrailOutcome `json:"guard,omitzero"`
	Client      *string           `json:"client,omitzero" ipc:"enum=claude-code|codex"`
	Local       *bool             `json:"local,omitzero" ipc:"enum=true"`
	SavedTokens *int64            `json:"savedTokens,omitzero" ipc:"min=0"`
	Agent       *string           `json:"agent,omitzero"`
	SessionID   *string           `json:"sessionId,omitzero"`
	RunID       *string           `json:"runId,omitzero"`
	TraceID     *string           `json:"traceId,omitzero"`
}

type GuardrailOutcome struct {
	Action string         `json:"action" ipc:"enum=redacted|logged|blocked"`
	Hits   []GuardrailHit `json:"hits"`
	Error  *bool          `json:"error,omitzero"`
}

type GuardrailHit struct {
	Check string `json:"check" ipc:"enum=secrets|pii|injection"`
	Label string `json:"label"`
	Count int64  `json:"count" ipc:"min=0"`
}

// AuditEventBody is a governance entry (log "governance") or an MCP tool call (log "mcp"). Event keeps the exact
// JSON; Governance() and MCP() give the decoded entry of its kind after Decode.
type AuditEventBody struct {
	Log        string          `json:"log" ipc:"enum=governance|mcp"`
	Event      json.RawMessage `json:"event"`
	governance *AuditEvent
	mcp        *MCPAuditEntry
}

func (a *AuditEventBody) Governance() *AuditEvent { return a.governance }
func (a *AuditEventBody) MCP() *MCPAuditEntry     { return a.mcp }

func (a *AuditEventBody) afterDecode(strict bool) error {
	switch a.Log {
	case "governance":
		a.governance = new(AuditEvent)
		return decodeBody(a.Event, a.governance, strict, "body.event")
	case "mcp":
		a.mcp = new(MCPAuditEntry)
		return decodeBody(a.Event, a.mcp, strict, "body.event")
	}
	return fmt.Errorf("body.log: %q is not governance or mcp", a.Log)
}

// GovernanceEvent and MCPEvent build an audit.event body.
func GovernanceEvent(e AuditEvent) (*AuditEventBody, error) {
	raw, err := json.Marshal(e)
	return &AuditEventBody{Log: "governance", Event: raw, governance: &e}, err
}

func MCPEvent(e MCPAuditEntry) (*AuditEventBody, error) {
	raw, err := json.Marshal(e)
	return &AuditEventBody{Log: "mcp", Event: raw, mcp: &e}, err
}

type AuditEvent struct {
	Action  string        `json:"action" ipc:"minlen=1"`
	Target  *AuditTarget  `json:"target,omitzero"`
	Outcome *string       `json:"outcome,omitzero" ipc:"enum=ok|refused|error"`
	Status  *int64        `json:"status,omitzero"`
	Changes []AuditChange `json:"changes,omitzero"`
	Actor   *AuditActor   `json:"actor,omitzero"`
}

type AuditTarget struct {
	Type string  `json:"type" ipc:"minlen=1"`
	ID   *string `json:"id,omitzero"`
	Name *string `json:"name,omitzero"`
}

type AuditChange struct {
	Path string          `json:"path"`
	From json.RawMessage `json:"from,omitzero"`
	To   json.RawMessage `json:"to,omitzero"`
}

type AuditActor struct {
	Via   string  `json:"via" ipc:"minlen=1"`
	KeyID *string `json:"keyId,omitzero"`
	User  *string `json:"user,omitzero"`
	Rule  *string `json:"rule,omitzero"`
}

// MCPAuditEntry is one MCP tool call as lib/integrations.js audits it: argument names, never values.
type MCPAuditEntry struct {
	Ts          int64    `json:"ts" ipc:"min=0"`
	Integration string   `json:"integration" ipc:"minlen=1"`
	Tool        string   `json:"tool" ipc:"minlen=1"`
	OK          bool     `json:"ok"`
	Ms          float64  `json:"ms" ipc:"min=0"`
	Args        []string `json:"args"`
	KeyID       *string  `json:"keyId,omitzero"`
}

// TraceSpanBody is one finished span as the local trace store keeps it.
type TraceSpanBody struct {
	Span SpanRecord `json:"span"`
}

type SpanRecord struct {
	T   string         `json:"t" ipc:"pattern=hex32"`
	S   string         `json:"s" ipc:"pattern=hex16"`
	P   *string        `json:"p" ipc:"pattern=hex16"`
	N   string         `json:"n" ipc:"minlen=1"`
	K   string         `json:"k" ipc:"enum=internal|server|client"`
	St  int64          `json:"st" ipc:"min=0"`
	D   int64          `json:"d" ipc:"min=0"`
	OK  bool           `json:"ok"`
	Err *string        `json:"err,omitzero"`
	Src string         `json:"src"`
	Svc string         `json:"svc"`
	A   map[string]any `json:"a"`
}

type ErrorBody struct {
	Code    string `json:"code" ipc:"minlen=1"`
	Message string `json:"message"`
}
```

- [ ] **Step 6: Write `gw/ipc/envelope.go`**

```go
// Package ipc is the gateway side of the ZAAFPulse IPC v1 contract (ipc/v1 at the repo root): the envelope, the
// message bodies, strict decoding for tests and lenient decoding for production, framing, the stdio channel, the
// durable outbox and the budget decision.
package ipc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
)

// Version is the major protocol version this package speaks.
const Version = 1

// Envelope wraps every message. Body is the raw JSON of the message's body.
type Envelope struct {
	V    int             `json:"v"`
	Type string          `json:"type" ipc:"minlen=1"`
	Seq  int64           `json:"seq" ipc:"min=1"`
	Ts   int64           `json:"ts" ipc:"min=0"`
	ID   string          `json:"id,omitzero" ipc:"minlen=1"`
	Re   string          `json:"re,omitzero" ipc:"minlen=1"`
	Body json.RawMessage `json:"body"`
}

// Message is a decoded envelope with its typed body (a pointer to one of the structs in messages.go). Unknown is
// true for a type v1 does not know: the receiver logs it once and drops it.
type Message struct {
	Envelope
	Body    any
	Unknown bool
}

var bodies = map[string]func() any{
	TypeHello:          func() any { return new(Hello) },
	TypeHelloOK:        func() any { return new(HelloOK) },
	TypeHelloRefused:   func() any { return new(HelloRefused) },
	TypeConfigPush:     func() any { return new(ConfigPush) },
	TypeSecretsHandoff: func() any { return new(SecretsHandoff) },
	TypeBudgetSnapshot: func() any { return new(BudgetSnapshot) },
	TypeShutdown:       func() any { return new(Shutdown) },
	TypeAck:            func() any { return new(Ack) },
	TypeReady:          func() any { return new(Ready) },
	TypeHealth:         func() any { return new(Health) },
	TypeUsageReport:    func() any { return new(UsageReport) },
	TypeAuditEvent:     func() any { return new(AuditEventBody) },
	TypeTraceSpan:      func() any { return new(TraceSpanBody) },
	TypeError:          func() any { return new(ErrorBody) },
}

// Types lists the v1 message types, sorted.
func Types() []string {
	out := make([]string, 0, len(bodies))
	for t := range bodies {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// postDecoder is a body that decodes part of itself after the first pass (AuditEventBody).
type postDecoder interface{ afterDecode(strict bool) error }

// Decode reads one message. strict (tests) refuses unknown fields, as the schemas do; production decodes leniently
// and ignores them (the v1 version rule). A version other than 1 is always an error.
func Decode(line []byte, strict bool) (Message, error) {
	var generic map[string]any
	if err := json.Unmarshal(line, &generic); err != nil || generic == nil {
		return Message{}, errors.New("envelope: not a JSON object")
	}
	if err := checkShape(generic, reflect.TypeFor[Envelope](), "envelope", strict); err != nil {
		return Message{}, err
	}
	if _, ok := generic["body"].(map[string]any); !ok {
		return Message{}, errors.New("envelope.body: must be an object")
	}
	var env Envelope
	if err := json.Unmarshal(line, &env); err != nil {
		return Message{}, fmt.Errorf("envelope: %w", err)
	}
	if env.V != Version {
		return Message{Envelope: env}, fmt.Errorf("envelope.v: version %d is not %d", env.V, Version)
	}
	ctor, ok := bodies[env.Type]
	if !ok {
		return Message{Envelope: env, Unknown: true}, nil
	}
	body := ctor()
	if err := decodeBody(env.Body, body, strict, "body"); err != nil {
		return Message{Envelope: env}, fmt.Errorf("%s: %w", env.Type, err)
	}
	return Message{Envelope: env, Body: body}, nil
}

func decodeBody(raw json.RawMessage, v any, strict bool, path string) error {
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := checkShape(generic, reflect.TypeOf(v), path, strict); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if strict {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if p, ok := v.(postDecoder); ok {
		return p.afterDecode(strict)
	}
	return nil
}

// Encode writes one message as a single line of JSON, without the newline.
func Encode(env Envelope, body any) ([]byte, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	env.Body = raw
	return json.Marshal(env)
}
```

- [ ] **Step 7: Run the Go tests**

Run: `gofmt -w gw && test -z "$(gofmt -l gw)" && GOTOOLCHAIN=local go -C gw vet ./... && GOTOOLCHAIN=local go -C gw test -count=1 ./...`
Expected: `ok  	github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc`. Every invalid fixture is refused for the reason in its name (measured for this plan): e.g. `config.push/invalid-api-key-reference.json` → `body.gateway.providers[0].apiKey: unknown field`, `usage.report/invalid-cost-float.json` → `cannot unmarshal number 0.0015 … of type int64`, `budget.snapshot/invalid-scope-word.json` → `budget scope "all" is not "global" or an object`.

- [ ] **Step 8: CI: the `go` job, and Go in the Node and Deno jobs**

In `.github/workflows/ci.yml`:

1. In the header comment, after the line `# The build-deno job compiles and smoke-tests the six single-file binaries on native runners (P2 T6).` add:
```yaml
# The go job (P3) vets, format-checks and tests the Go gateway module (gw/) against the shared fixtures in ipc/v1, on
# the test job's OS matrix. The Node and Deno jobs set up the same Go, so the cross-language IPC tests build the stub
# (gw/cmd/zaafpulse-gw) and never skip; the Deno job builds it once up front (ZP_GW_BIN). Go is pinned to 1.27.3.
```
and change `The condition is repeated in the three places that use it` to `The condition is repeated in the four places that use it`, and in the scope paragraph the list `(Node suite, Deno suite,` / `# typecheck)` (it wraps over two comment lines) to `(Node suite, Deno suite,` / `# Go, typecheck)`.

2. In the `test` job, after the `actions/setup-node` step (before `- name: Test suite`), add:
```yaml
      - uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0
        with:
          go-version: '1.27.3'
          cache: false
```

3. In the `deno` job, after its `actions/setup-node` step and before `- run: deno ci`, add:
```yaml
      - uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0
        with:
          go-version: '1.27.3'
          cache: false
      # The cross-language IPC tests use this build of the gateway stub instead of building it themselves.
      - name: Build the gateway stub
        shell: bash
        run: |
          go -C gw build -o "$RUNNER_TEMP/gw/zaafpulse-gw" ./cmd/zaafpulse-gw
          echo "ZP_GW_BIN=$RUNNER_TEMP/gw/zaafpulse-gw" >> "$GITHUB_ENV"
```

4. After the `typecheck` job, add the `go` job (the matrix expression is the `test` job's, verbatim):
```yaml
  # The Go gateway module (P3): gofmt, go vet and go test, including every fixture of ipc/v1.
  go:
    name: go (${{ matrix.os }})
    runs-on: ${{ matrix.os }}
    timeout-minutes: 15
    strategy:
      fail-fast: false
      matrix:
        os: ${{ fromJSON((github.event_name != 'push' || github.ref == 'refs/heads/main' || github.ref == 'refs/heads/migrate/deno-go' || startsWith(github.ref, 'refs/tags/')) && '["ubuntu-latest","macos-latest","windows-latest","windows-11-arm"]' || '["ubuntu-latest"]') }}
    env:
      GOTOOLCHAIN: local
    steps:
      - run: git config --global core.autocrlf false
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0
        with:
          go-version: '1.27.3'
          cache: false
      - name: gofmt
        shell: bash
        run: test -z "$(gofmt -l gw)" || { gofmt -l gw; exit 1; }
      - run: go -C gw vet ./...
      - run: go -C gw test -count=1 ./...
```

5. In `package.json` `scripts`, after `"test:contract"`, add `"test:go": "go -C gw vet ./... && go -C gw test -count=1 ./...",`.

Run: `ruby -e 'require "yaml"; YAML.load_file(".github/workflows/ci.yml"); puts "ok"'`
Expected: `ok`. Then `grep -c "setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0" .github/workflows/ci.yml` prints `3`.

- [ ] **Step 9: Run the three suites**

Run: `npm run test:go && node --test test/*.test.js 2>&1 | grep -E "^ℹ fail" && deno test -q --no-check --no-prompt -P=test test/*.test.js 2>&1 | tail -1`
Expected: Go `ok`; `ℹ fail 0`; Deno `ok | … passed | 0 failed`.

- [ ] **Step 10: Commit**

```bash
git add gw .github/workflows/ci.yml package.json
git commit -m "Add the Go gateway module with the v1 IPC messages, fixture round-trip tests and a CI go job"
git push origin migrate/p3-ipc-contract
```
After the push, check the work-branch run: `gh run list --branch migrate/p3-ipc-contract --limit 1` then `gh run watch <id> --exit-status` shows `go (ubuntu-latest)` green. The four-OS `go` exit of T2 is met by the pull request run (full scope).

---

### Task 3: Channel and handshake (spec T3)

**Files:**
- Create: `.gitattributes`, `ipc/v1/framing/crlf.ndjson`
- Create: `gw/ipc/framing.go`, `gw/ipc/channel.go`, `gw/ipc/outbox.go` (stream identity only; Task 4 replaces it), `gw/cmd/zaafpulse-gw/main.go`
- Test: `gw/ipc/framing_test.go`, `gw/ipc/channel_test.go`
- Create: `lib/ipc/framing.js`, `lib/ipc/channel.js`, `lib/ipc/acker.js`, `lib/ipc/session.js`, `scripts/ipc/build-stub.js`, `test/ipc-stub.js` (helper, not a test file)
- Test: `test/ipc-framing.test.js`, `test/ipc-channel.test.js`, `test/ipc-session.test.js`

**Interfaces:**
- Consumes: `lib/ipc/schema.js` `check`, `PROTOCOL`, `EVENTS`, `DIR` (Task 1); Go `Decode`, `Encode`, `Envelope`, `Message`, body structs, `patterns` (Task 2).
- Produces:
  - Go: `const MaxLine = 8 << 20`; `ErrLineTooLong`, `ErrPartialLine`; `NewLineReader(r io.Reader, max int) *LineReader` with `Next() ([]byte, error)`; `type Transport interface{ Send(typ string, body any, id, re string) error; SendLine(line []byte) error; Recv() (Message, error); Close() error }`; `NewChannel(in io.Reader, out io.Writer, strict bool) *Channel` (implements Transport; field `Now func() time.Time`); `*ProtocolError`; `OpenOutbox(dir string) (*Outbox, error)` with `Stream() string`, `Acked() int64`, `Next() int64`, `Depth() int64`, `Close() error`.
  - Stub command line: `zaafpulse-gw [--outbox DIR] [--health-ms N] [--protocol 1[,2]] [--hang-after-ready] [--crash-after-ready-ms N] [--bad-line] [--ignore-shutdown]`; exit 0 on shutdown or end of stdin, 1 on an I/O failure, 2 when refused or on a bad command line, 3 after a protocol error.
  - JS: `framing.js` `createLineReader({ onLine, onError, maxBytes? }) → { push(chunk), end() }`, `MAX_LINE`; `channel.js` `createChannel({ input, output, strict?, maxBytes?, log? }) → { send(msg): boolean, onMessage(fn), onClose(fn), close(info?), closed }` (close info: `{ reason: 'eof' | 'protocol' | 'error' | 'closed', error? }`); `acker.js` `createAcker({ send, everyMs = 100, max = 256, setTimer?, clearTimer? }) → { note(stream, seq), flush(), stop() }`; `session.js` `startSession({ bin, args?, env?, cpVersion, initial: { config, secrets, snapshot }, onEvent?(msg, stream), onHealth?(body), log?, now?, helloMs = 5000, strict = false, ackEveryMs = 100, ackMax = 256, killAfterMs = 2000, spawnImpl? }) → { pid, stream (getter), ready: Promise<{gen}>, exited: Promise<{code, signal}>, channel, send(type, body, extra?), flushAcks(), shutdown(graceMs = 5000): Promise<{code, signal}>, close(), kill() }`; `scripts/ipc/build-stub.js` `buildStub({ env? }) → { bin } | { skip }`, `GW_DIR`; `test/ipc-stub.js` `stubBin(t)`, `initial(at?)`, `fixtureBody(rel)`, `killHard(pid)`, `alive(pid)`, `sleep(ms)`, `waitFor(fn, ms?, what?)`, `tmpDir(t, name)`.

- [ ] **Step 1: The CRLF framing fixture, byte-exact**

`.gitattributes` (new file):
```
# The IPC framing fixtures are compared byte for byte (ipc/v1/framing/crlf.ndjson ends a line with \r\n on purpose).
ipc/v1/framing/*.ndjson -text
```

Run:
```bash
mkdir -p ipc/v1/framing
printf '%s\r\n%s\n' \
  '{"v":1,"type":"health","seq":1,"ts":1767225601000,"body":{"inflight":0,"outboxDepth":0,"uptimeMs":1000,"gen":1,"snapshotAgeMs":12}}' \
  '{"v":1,"type":"health","seq":2,"ts":1767225602000,"body":{"inflight":0,"outboxDepth":0,"uptimeMs":2000,"gen":1,"snapshotAgeMs":8}}' \
  > ipc/v1/framing/crlf.ndjson
od -c ipc/v1/framing/crlf.ndjson | grep -c '\\r'
```
Expected: `1` (exactly one `\r`, before the first newline).

- [ ] **Step 2: Write the failing Go framing and channel tests**

`gw/ipc/framing_test.go`:
```go
package ipc

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLineReaderSplitsLinesSkipsEmptyAndStripsOneCR(t *testing.T) {
	r := NewLineReader(strings.NewReader("a\n\nbc\r\nd\r\r\ne"), 10)
	for _, want := range []string{"a", "bc", "d\r"} {
		line, err := r.Next()
		if err != nil || string(line) != want {
			t.Fatalf("got %q, %v; want %q", line, err, want)
		}
	}
	if _, err := r.Next(); !errors.Is(err, ErrPartialLine) {
		t.Fatalf("a stream ending inside a line: got %v", err)
	}
	if _, err := NewLineReader(strings.NewReader(""), 10).Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("an empty stream: got %v", err)
	}
}

func TestLineReaderLimit(t *testing.T) {
	ok := strings.Repeat("x", 10)
	if line, err := NewLineReader(strings.NewReader(ok+"\n"), 10).Next(); err != nil || string(line) != ok {
		t.Fatalf("a line of exactly the limit: %v", err)
	}
	if _, err := NewLineReader(strings.NewReader(ok+"x\n"), 10).Next(); !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("one byte over: got %v", err)
	}
	// A line longer than bufio's 64 KiB buffer but under the limit; and one over the limit with no newline yet.
	long := strings.Repeat("y", 150000)
	if line, err := NewLineReader(strings.NewReader(long+"\n"), 200000).Next(); err != nil || len(line) != 150000 {
		t.Fatalf("a long line: %v", err)
	}
	if _, err := NewLineReader(strings.NewReader(long), 100000).Next(); !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("over the limit without a newline: got %v", err)
	}
}

// ipc/v1/framing/crlf.ndjson: a message ended by \r\n and one by \n both decode.
func TestCRLFFixture(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "ipc", "v1", "framing", "crlf.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r := NewLineReader(f, MaxLine)
	for seq := int64(1); seq <= 2; seq++ {
		line, err := r.Next()
		if err != nil {
			t.Fatal(err)
		}
		msg, err := Decode(line, true)
		if err != nil || msg.Seq != seq {
			t.Fatalf("message %d: %v", seq, err)
		}
	}
	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("got %v", err)
	}
}
```

`gw/ipc/channel_test.go`:
```go
package ipc

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestChannelSendRecv(t *testing.T) {
	r, w := io.Pipe()
	out := NewChannel(strings.NewReader(""), w, true)
	out.Now = func() time.Time { return time.UnixMilli(1767225600000) }
	in := NewChannel(r, io.Discard, true)
	go func() {
		_ = out.Send(TypeHello, Hello{Protocol: []int{1}, GwVersion: "t", Stream: strings.Repeat("a", 32), OutboxFrom: 1}, "h1", "")
		_ = out.Send(TypeReady, Ready{Gen: 1}, "", "")
		_ = out.SendLine([]byte(`{"v":1,"type":"telemetry.v9","seq":7,"ts":0,"body":{}}`))
		_ = out.SendLine([]byte("not json\n"))
		w.Close()
	}()
	m, err := in.Recv()
	if err != nil || m.Type != TypeHello || m.Seq != 1 || m.ID != "h1" || m.Ts != 1767225600000 {
		t.Fatalf("hello: %+v %v", m, err)
	}
	if m.Body.(*Hello).OutboxFrom != 1 {
		t.Fatal("hello body")
	}
	if m, err = in.Recv(); err != nil || m.Type != TypeReady || m.Seq != 2 || m.Body.(*Ready).Gen != 1 {
		t.Fatalf("ready: %+v %v", m, err)
	}
	if m, err = in.Recv(); err != nil || !m.Unknown || m.Type != "telemetry.v9" {
		t.Fatalf("an unknown type is reported, not refused: %+v %v", m, err)
	}
	var pe *ProtocolError
	if _, err = in.Recv(); !errors.As(err, &pe) {
		t.Fatalf("a line that is not JSON: got %v", err)
	}
	if _, err = in.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("got %v", err)
	}
}
```

Run: `GOTOOLCHAIN=local go -C gw test ./... 2>&1 | head -3`
Expected: FAIL to build: `undefined: NewLineReader`.

- [ ] **Step 3: Write `gw/ipc/framing.go` and `gw/ipc/channel.go`**

`gw/ipc/framing.go`:
```go
package ipc

import (
	"bufio"
	"bytes"
	"errors"
	"io"
)

// MaxLine is the longest message line either side accepts (D2): 8 MiB, the newline not counted.
const MaxLine = 8 << 20

var (
	// ErrLineTooLong is a protocol error: the channel closes.
	ErrLineTooLong = errors.New("ipc: a message line is longer than 8 MiB")
	// ErrPartialLine: the stream ended inside a line (the other side died while writing).
	ErrPartialLine = errors.New("ipc: the stream ended inside a message line")
)

// LineReader splits a stream into message lines. It removes one trailing \r and skips empty lines.
type LineReader struct {
	br  *bufio.Reader
	max int
}

// NewLineReader reads lines of at most max bytes (MaxLine when max <= 0).
func NewLineReader(r io.Reader, max int) *LineReader {
	if max <= 0 {
		max = MaxLine
	}
	return &LineReader{br: bufio.NewReaderSize(r, 64<<10), max: max}
}

// Next returns the next non-empty line without its line end; io.EOF at a clean end of the stream.
func (l *LineReader) Next() ([]byte, error) {
	var buf []byte
	for {
		chunk, err := l.br.ReadSlice('\n')
		buf = append(buf, chunk...)
		switch {
		case err == nil:
			line := buf[:len(buf)-1]
			if len(line) > l.max {
				return nil, ErrLineTooLong
			}
			line = bytes.TrimSuffix(line, []byte{'\r'})
			if len(line) == 0 {
				buf = buf[:0]
				continue
			}
			return line, nil
		case errors.Is(err, bufio.ErrBufferFull):
			if len(buf) > l.max {
				return nil, ErrLineTooLong
			}
		case errors.Is(err, io.EOF):
			if len(buf) == 0 {
				return nil, io.EOF
			}
			return nil, ErrPartialLine
		default:
			return nil, err
		}
	}
}
```

`gw/ipc/channel.go`:
```go
package ipc

import (
	"errors"
	"io"
	"sync"
	"time"
)

// Transport is the one interface both sides put the IPC behind (D1): stdio today, a network transport later
// without changing a message.
type Transport interface {
	// Send writes a control message with this connection's next seq.
	Send(typ string, body any, id, re string) error
	// SendLine writes an encoded line as it is (an outbox event, which carries its own durable seq).
	SendLine(line []byte) error
	// Recv reads the next message: io.EOF or ErrPartialLine when the other side is gone, *ProtocolError otherwise.
	Recv() (Message, error)
	Close() error
}

// ProtocolError is a line that is too long, not JSON, or not a valid v1 message.
type ProtocolError struct{ Err error }

func (e *ProtocolError) Error() string { return "protocol error: " + e.Err.Error() }
func (e *ProtocolError) Unwrap() error { return e.Err }

// Channel is the stdio Transport: messages in from in, out to out, one per line.
type Channel struct {
	in     *LineReader
	out    io.Writer
	strict bool
	mu     sync.Mutex
	seq    int64
	Now    func() time.Time
}

var _ Transport = (*Channel)(nil)

// NewChannel reads from in and writes to out. strict decodes as the tests do (unknown fields refused).
func NewChannel(in io.Reader, out io.Writer, strict bool) *Channel {
	return &Channel{in: NewLineReader(in, MaxLine), out: out, strict: strict, Now: time.Now}
}

func (c *Channel) Send(typ string, body any, id, re string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	line, err := Encode(Envelope{V: Version, Type: typ, Seq: c.seq, Ts: c.Now().UnixMilli(), ID: id, Re: re}, body)
	if err != nil {
		return err
	}
	_, err = c.out.Write(append(line, '\n'))
	return err
}

func (c *Channel) SendLine(line []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(line) == 0 || line[len(line)-1] != '\n' {
		line = append(line[:len(line):len(line)], '\n')
	}
	_, err := c.out.Write(line)
	return err
}

func (c *Channel) Recv() (Message, error) {
	line, err := c.in.Next()
	if err != nil {
		if errors.Is(err, ErrLineTooLong) {
			return Message{}, &ProtocolError{err}
		}
		return Message{}, err
	}
	msg, err := Decode(line, c.strict)
	if err != nil {
		return Message{}, &ProtocolError{err}
	}
	return msg, nil
}

func (c *Channel) Close() error {
	if cl, ok := c.out.(io.Closer); ok {
		return cl.Close()
	}
	return nil
}
```

Run: `gofmt -w gw && GOTOOLCHAIN=local go -C gw test -count=1 ./...`
Expected: `ok  	github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc`.

- [ ] **Step 4: Write the stub's outbox identity and its message loop**

`gw/ipc/outbox.go` (Task 4 replaces this file with the durable outbox; the methods here keep their signatures):
```go
package ipc

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// Outbox is the gateway's durable event stream. In this first version it holds the stream's identity only
// (stream.json: {stream, next, acked}); Task 4 of the P3 plan adds the segments, Append, Replay and Ack.
type Outbox struct {
	dir   string
	mu    sync.Mutex
	state streamState
}

type streamState struct {
	Stream string `json:"stream"`
	Next   int64  `json:"next"`
	Acked  int64  `json:"acked"`
}

// OpenOutbox opens (or creates) the outbox in dir.
func OpenOutbox(dir string) (*Outbox, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	o := &Outbox{dir: dir}
	data, err := os.ReadFile(filepath.Join(dir, "stream.json"))
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &o.state); err != nil || !patterns["hex32"].MatchString(o.state.Stream) {
			return nil, fmt.Errorf("outbox: %s is damaged", filepath.Join(dir, "stream.json"))
		}
	case errors.Is(err, fs.ErrNotExist):
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		o.state = streamState{Stream: hex.EncodeToString(b), Next: 1}
	default:
		return nil, err
	}
	o.state.Next = max(o.state.Next, o.state.Acked+1)
	return o, o.save()
}

func (o *Outbox) save() error {
	data, err := json.Marshal(o.state)
	if err != nil {
		return err
	}
	tmp := filepath.Join(o.dir, "stream.json.tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(o.dir, "stream.json"))
}

func (o *Outbox) Stream() string { o.mu.Lock(); defer o.mu.Unlock(); return o.state.Stream }
func (o *Outbox) Acked() int64   { o.mu.Lock(); defer o.mu.Unlock(); return o.state.Acked }
func (o *Outbox) Next() int64    { o.mu.Lock(); defer o.mu.Unlock(); return o.state.Next }

// Depth is the number of events not acknowledged yet.
func (o *Outbox) Depth() int64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.state.Next - 1 - o.state.Acked
}

func (o *Outbox) Close() error { o.mu.Lock(); defer o.mu.Unlock(); return o.save() }
```

`gw/cmd/zaafpulse-gw/main.go`:
```go
// Command zaafpulse-gw is the gateway process of ZAAFPulse v2. In P3 it is a stub: it speaks the whole v1 IPC protocol
// with the control plane over stdin and stdout (ipc/v1; docs/superpowers/specs/2026-11-11-p3-ipc-contract-design.md)
// and serves no HTTP. It logs to stderr; the control plane forwards each line with a "gw: " prefix. When stdin reaches
// end of file (the control plane is gone) it exits 0.
//
// Exit codes: 0 done (shutdown or end of input), 1 an I/O failure, 2 refused or a bad command line, 3 a protocol
// error (after an "error" message) or --crash-after-ready-ms.
//
// Test-only flags make it misbehave for the supervisor tests: --protocol, --hang-after-ready, --crash-after-ready-ms,
// --bad-line and --ignore-shutdown.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

const version = "0.3.0-stub"

type options struct {
	outbox            string
	healthMs          int
	protocol          string
	hangAfterReady    bool
	crashAfterReadyMs int
	badLine           bool
	ignoreShutdown    bool
}

func main() {
	// A write to a closed stdout (the control plane died) returns EPIPE instead of killing the process.
	signal.Ignore(syscall.SIGPIPE)
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	logger := log.New(stderr, "", 0)
	var o options
	fs := flag.NewFlagSet("zaafpulse-gw", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.outbox, "outbox", "", "the outbox folder (default $ZAAFPULSE_DATA/.data/gw-outbox)")
	fs.IntVar(&o.healthMs, "health-ms", 1000, "how often health is sent, in ms")
	fs.StringVar(&o.protocol, "protocol", "1", "test only: the protocol versions hello offers, comma-separated")
	fs.BoolVar(&o.hangAfterReady, "hang-after-ready", false, "test only: stop sending health once ready")
	fs.IntVar(&o.crashAfterReadyMs, "crash-after-ready-ms", 0, "test only: exit 3 this many ms after ready")
	fs.BoolVar(&o.badLine, "bad-line", false, "test only: write a line that is not JSON once ready")
	fs.BoolVar(&o.ignoreShutdown, "ignore-shutdown", false, "test only: ignore shutdown")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir := o.outbox
	if dir == "" {
		data := os.Getenv("ZAAFPULSE_DATA")
		if data == "" {
			logger.Print("no outbox folder: pass --outbox or set ZAAFPULSE_DATA")
			return 2
		}
		dir = filepath.Join(data, ".data", "gw-outbox")
	}
	protocols, err := parseProtocols(o.protocol)
	if err != nil {
		logger.Print(err)
		return 2
	}
	box, err := ipc.OpenOutbox(dir)
	if err != nil {
		logger.Printf("outbox: %v", err)
		return 1
	}
	defer box.Close()
	g := &gateway{
		o:       o,
		box:     box,
		ch:      ipc.NewChannel(stdin, stdout, false),
		log:     logger,
		started: time.Now(),
		done:    make(chan struct{}),
		unknown: map[string]bool{},
	}
	return g.serve(protocols)
}

func parseProtocols(s string) ([]int, error) {
	var out []int
	for _, p := range strings.Split(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || n < 1 {
			return nil, fmt.Errorf("--protocol %q: a comma-separated list of versions", s)
		}
		out = append(out, n)
	}
	return out, nil
}

type gateway struct {
	o       options
	box     *ipc.Outbox
	ch      *ipc.Channel
	log     *log.Logger
	started time.Time
	done    chan struct{}
	unknown map[string]bool

	mu      sync.Mutex
	cfgGen  int64
	secGen  int64
	secrets map[string]ipc.ProviderSecret // in memory only: never logged, never written to the outbox
	mask    []string
	snap    *ipc.BudgetSnapshot
	snapAt  time.Time
	ready   bool
	quiet   bool // --hang-after-ready
}

func (g *gateway) serve(protocols []int) int {
	defer close(g.done)
	hello := ipc.Hello{Protocol: protocols, GwVersion: version, Stream: g.box.Stream(), OutboxFrom: g.box.Acked() + 1}
	if err := g.ch.Send(ipc.TypeHello, hello, "h1", ""); err != nil {
		g.log.Printf("hello: %v", err)
		return 1
	}
	handshaken := false
	for {
		msg, err := g.ch.Recv()
		if errors.Is(err, io.EOF) || errors.Is(err, ipc.ErrPartialLine) {
			g.drain()
			return 0
		}
		if err != nil {
			return g.protocolError(err)
		}
		if msg.Unknown {
			if !g.unknown[msg.Type] {
				g.unknown[msg.Type] = true
				g.log.Printf("unknown message type %q dropped", msg.Type)
			}
			continue
		}
		if !handshaken {
			switch b := msg.Body.(type) {
			case *ipc.HelloOK:
				if b.Protocol != ipc.Version {
					g.log.Printf("the control plane chose protocol %d; this gateway speaks %d", b.Protocol, ipc.Version)
					return 2
				}
				handshaken = true
				go g.healthLoop()
			case *ipc.HelloRefused:
				g.log.Printf("refused by the control plane: %s", b.Reason)
				return 2
			default:
				return g.protocolError(fmt.Errorf("%s before the handshake", msg.Type))
			}
			continue
		}
		if code := g.handle(msg); code >= 0 {
			return code
		}
	}
}

// handle applies one message after the handshake; it returns an exit code, or -1 to go on.
func (g *gateway) handle(msg ipc.Message) int {
	switch b := msg.Body.(type) {
	case *ipc.ConfigPush:
		g.mu.Lock()
		g.cfgGen = b.Gen
		g.mu.Unlock()
		g.maybeReady()
	case *ipc.SecretsHandoff:
		m := make(map[string]ipc.ProviderSecret, len(b.Providers))
		for _, p := range b.Providers {
			m[p.ID] = p
		}
		g.mu.Lock()
		g.secGen, g.secrets, g.mask = b.Gen, m, b.Mask
		g.mu.Unlock()
		g.maybeReady()
	case *ipc.BudgetSnapshot:
		g.mu.Lock()
		g.snap, g.snapAt = b, time.Now()
		g.mu.Unlock()
		g.maybeReady()
	case *ipc.Ack:
		// The outbox (Task 4) deletes what is acknowledged.
	case *ipc.Shutdown:
		if g.o.ignoreShutdown {
			g.log.Print("shutdown ignored (--ignore-shutdown)")
			return -1
		}
		g.shutdown(time.Duration(b.GraceMs) * time.Millisecond)
		return 0
	default:
		g.log.Printf("unexpected %s from the control plane, dropped", msg.Type)
	}
	return -1
}

func (g *gateway) protocolError(err error) int {
	g.log.Print(err)
	_ = g.ch.Send(ipc.TypeError, ipc.ErrorBody{Code: "protocol", Message: err.Error()}, "", "")
	return 3
}

// maybeReady sends ready once the first config, its secrets and a snapshot are applied.
func (g *gateway) maybeReady() {
	g.mu.Lock()
	if g.ready || g.cfgGen == 0 || g.secGen != g.cfgGen || g.snap == nil {
		g.mu.Unlock()
		return
	}
	g.ready = true
	gen := g.cfgGen
	g.mu.Unlock()
	if err := g.ch.Send(ipc.TypeReady, ipc.Ready{Gen: gen}, "", ""); err != nil {
		g.log.Printf("ready: %v", err)
		return
	}
	g.log.Printf("ready (config generation %d)", gen)
	g.afterReady()
}

func (g *gateway) afterReady() {
	if g.o.hangAfterReady {
		g.mu.Lock()
		g.quiet = true
		g.mu.Unlock()
	}
	if g.o.crashAfterReadyMs > 0 {
		time.AfterFunc(time.Duration(g.o.crashAfterReadyMs)*time.Millisecond, func() {
			g.log.Print("crashing on purpose (--crash-after-ready-ms)")
			os.Exit(3)
		})
	}
	if g.o.badLine {
		_ = g.ch.SendLine([]byte("this line is not JSON"))
	}
}

func (g *gateway) healthLoop() {
	t := time.NewTicker(time.Duration(g.o.healthMs) * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-g.done:
			return
		case <-t.C:
			g.sendHealth()
		}
	}
}

func (g *gateway) sendHealth() {
	g.mu.Lock()
	if g.quiet {
		g.mu.Unlock()
		return
	}
	h := ipc.Health{OutboxDepth: g.box.Depth(), UptimeMs: time.Since(g.started).Milliseconds(), Gen: g.cfgGen}
	if g.snap != nil {
		age := time.Since(g.snapAt).Milliseconds()
		h.SnapshotAgeMs = &age
	}
	g.mu.Unlock()
	if err := g.ch.Send(ipc.TypeHealth, h, "", ""); err != nil {
		g.log.Printf("health: %v", err)
	}
}

// drain: the control plane closed the channel.
func (g *gateway) drain() {
	g.log.Print("the control plane closed the channel: exiting")
}

func (g *gateway) shutdown(grace time.Duration) {
	g.log.Printf("shutting down (grace %s)", grace)
}
```

Run: `gofmt -w gw && test -z "$(gofmt -l gw)" && GOTOOLCHAIN=local go -C gw vet ./... && GOOS=windows GOARCH=arm64 GOTOOLCHAIN=local go -C gw vet ./... && echo ok`
Expected: `ok` (the Windows vet proves `syscall.SIGPIPE` and the rest build for `windows-11-arm`).

- [ ] **Step 5: Write the failing JS framing and channel tests**

`test/ipc-framing.test.js`:
```js
// lib/ipc/framing.js (P3 D2): lines across chunks, a character cut between chunks, one \r removed, empty lines
// skipped, the 8 MiB limit (here a small one), and a stream that ends inside a line.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const { createLineReader, MAX_LINE } = require('../lib/ipc/framing');
const { DIR, check } = require('../lib/ipc/schema');

function reader(maxBytes) {
  const lines = [];
  const errors = [];
  const r = createLineReader({ maxBytes, onLine: (l) => lines.push(l), onError: (e) => errors.push(e.message) });
  return { r, lines, errors };
}

test('the limit is 8 MiB', () => {
  assert.equal(MAX_LINE, 8 * 1024 * 1024);
});

test('lines split across chunks, several in one chunk, and a UTF-8 character cut in two', () => {
  const { r, lines, errors } = reader();
  const text = Buffer.from('{"a":"é"}\n{"b":2}\n{"c":3}\n');
  r.push(text.subarray(0, 7)); // ends inside é
  r.push(text.subarray(7, 15));
  r.push(text.subarray(15));
  assert.deepEqual(lines, ['{"a":"é"}', '{"b":2}', '{"c":3}']);
  assert.deepEqual(errors, []);
});

test('one trailing \\r is removed and an empty line is skipped (ipc/v1/framing/crlf.ndjson)', () => {
  const { r, lines } = reader();
  r.push(fs.readFileSync(path.join(DIR, 'framing', 'crlf.ndjson')));
  r.push('\n\r\nx\r\r\n');
  assert.equal(lines.length, 3);
  for (const l of lines.slice(0, 2)) assert.deepEqual(check(JSON.parse(l), {}), { errors: [], unknownType: false });
  assert.equal(lines[2], 'x\r', 'only one \\r is removed');
});

test('a line of exactly the limit passes; one byte more is an error and the reader stops', () => {
  const ok = reader(10);
  ok.r.push('0123456789\n');
  assert.deepEqual(ok.lines, ['0123456789']);
  const over = reader(10);
  over.r.push('01234567890\n{"after":1}\n');
  assert.deepEqual(over.lines, []);
  assert.equal(over.errors.length, 1);
});

test('a line over the limit is refused before its newline arrives', () => {
  const { r, errors } = reader(10);
  r.push('012345');
  r.push('67890');
  assert.equal(errors.length, 1);
});

test('the stream ending inside a line is an error; a clean end is not', () => {
  const cut = reader();
  cut.r.push('{"a":1}\n{"b"');
  cut.r.end();
  assert.equal(cut.errors.length, 1);
  const clean = reader();
  clean.r.push('{"a":1}\n');
  clean.r.end();
  assert.deepEqual(clean.errors, []);
});
```

`test/ipc-channel.test.js`:
```js
// lib/ipc/channel.js and lib/ipc/acker.js (P3 D1, D2, D4): messages in and out over a pair of streams, an unknown type
// logged once and dropped, a bad line or an invalid message closing the channel, strict sending, batched acks.
const test = require('node:test');
const assert = require('node:assert/strict');
const { PassThrough } = require('stream');
const { createChannel } = require('../lib/ipc/channel');
const { createAcker } = require('../lib/ipc/acker');

const S = '0f1e2d3c4b5a69788796a5b4c3d2e1f0';
const ready = (seq) => ({ v: 1, type: 'ready', seq, ts: 0, body: { gen: 1 } });

function pair(opts = {}) {
  const input = new PassThrough();
  const output = new PassThrough();
  const got = [];
  const closes = [];
  const logs = [];
  const ch = createChannel({ input, output, log: (l) => logs.push(l), ...opts });
  ch.onMessage((m) => got.push(m));
  ch.onClose((i) => closes.push(i));
  return { input, output, ch, got, closes, logs };
}

test('messages in, one per line; messages out, one per line', () => {
  const p = pair({ strict: true });
  p.input.write(`${JSON.stringify(ready(1))}\n${JSON.stringify(ready(2))}\n`);
  assert.deepEqual(
    p.got.map((m) => m.seq),
    [1, 2],
  );
  assert.equal(p.ch.send({ v: 1, type: 'ack', seq: 1, ts: 0, body: { stream: S, upTo: 2 } }), true);
  assert.equal(String(p.output.read()), `{"v":1,"type":"ack","seq":1,"ts":0,"body":{"stream":"${S}","upTo":2}}\n`);
});

test('an unknown type is logged once per type and dropped (D2)', () => {
  const p = pair();
  const unknown = (seq) => JSON.stringify({ v: 1, type: 'telemetry.v9', seq, ts: 0, body: {} });
  p.input.write(`${unknown(1)}\n${unknown(2)}\n${JSON.stringify(ready(3))}\n`);
  assert.deepEqual(
    p.got.map((m) => m.type),
    ['ready'],
  );
  assert.deepEqual(p.logs, ['ipc: unknown message type "telemetry.v9" dropped']);
  assert.deepEqual(p.closes, []);
});

test('lenient by default: an unknown field is ignored; strict refuses it', () => {
  const extra = JSON.stringify({ ...ready(1), body: { gen: 1, region: 'eu' } });
  const lenient = pair();
  lenient.input.write(`${extra}\n`);
  assert.equal(lenient.got.length, 1);
  const strict = pair({ strict: true });
  strict.input.write(`${extra}\n`);
  assert.equal(strict.got.length, 0);
  assert.equal(strict.closes[0].reason, 'protocol');
});

test('a line that is not JSON, or an invalid message, closes the channel as a protocol error', () => {
  const bad = pair();
  bad.input.write('not json\n');
  assert.deepEqual(bad.closes, [{ reason: 'protocol', error: 'a line is not JSON' }]);
  assert.equal(bad.ch.send(ready(1)), false, 'a closed channel sends nothing');
  const invalid = pair();
  invalid.input.write(`${JSON.stringify({ ...ready(1), body: {} })}\n`);
  assert.match(invalid.closes[0].error, /^ready: \$\.body\.gen: required$/);
});

test('the end of the input closes the channel with reason eof', async () => {
  const p = pair();
  p.input.end();
  await new Promise((r) => setImmediate(r));
  assert.deepEqual(p.closes, [{ reason: 'eof' }]);
});

test('strict send refuses a message that is not valid v1', () => {
  const p = pair({ strict: true });
  assert.throws(() => p.ch.send({ v: 1, type: 'ack', seq: 1, ts: 0, body: { stream: 'x', upTo: 1 } }), /invalid ack/);
});

test('acks: one per batch with the highest seq, when the timer fires or max events wait', () => {
  const sent = [];
  const timers = [];
  const a = createAcker({
    send: (b) => sent.push(b),
    everyMs: 100,
    max: 3,
    setTimer: (fn) => (timers.push(fn), timers.length),
    clearTimer: () => {},
  });
  a.note(S, 1);
  a.note(S, 2);
  assert.deepEqual(sent, []);
  timers[0]();
  assert.deepEqual(sent, [{ stream: S, upTo: 2 }]);
  a.note(S, 3);
  a.note(S, 4);
  a.note(S, 5);
  assert.deepEqual(sent.at(-1), { stream: S, upTo: 5 }, 'the third waiting event flushes at once');
  a.flush();
  assert.equal(sent.length, 2, 'nothing new: no second ack for the same seq');
});
```

Run: `node --test test/ipc-framing.test.js test/ipc-channel.test.js 2>&1 | tail -3`
Expected: FAIL: `Cannot find module '../lib/ipc/framing'`.

- [ ] **Step 6: Write `lib/ipc/framing.js`, `lib/ipc/channel.js`, `lib/ipc/acker.js`**

`lib/ipc/framing.js`:
```js
// Framing of the IPC (P3 D2): UTF-8 JSON, one message per line. A reader joins the chunks of a stream into lines (a
// line may arrive in pieces, and a character may be cut between two chunks), removes one trailing \r, skips empty
// lines, and refuses a line longer than 8 MiB: onError is called once and the reader stops.
const MAX_LINE = 8 * 1024 * 1024;

/**
 * @param {{ onLine: (line: string) => void, onError: (err: Error) => void, maxBytes?: number }} opts
 * @returns {{ push: (chunk: Uint8Array | string) => void, end: () => void }}
 */
function createLineReader({ onLine, onError, maxBytes = MAX_LINE }) {
  let parts = [];
  let size = 0;
  let dead = false;
  const fail = (message) => {
    dead = true;
    parts = [];
    size = 0;
    onError(new Error(message));
  };
  function push(chunk) {
    if (dead) return;
    const buf = typeof chunk === 'string' ? Buffer.from(chunk, 'utf8') : Buffer.from(chunk);
    let start = 0;
    for (;;) {
      const nl = buf.indexOf(10, start);
      if (nl < 0) break;
      const piece = buf.subarray(start, nl);
      if (size + piece.length > maxBytes) return fail(`a message line is longer than ${maxBytes} bytes`);
      let line = size ? Buffer.concat([...parts, piece]) : piece;
      parts = [];
      size = 0;
      start = nl + 1;
      if (line.length && line[line.length - 1] === 13) line = line.subarray(0, -1);
      if (line.length) onLine(line.toString('utf8'));
      if (dead) return;
    }
    const rest = buf.subarray(start);
    if (size + rest.length > maxBytes) return fail(`a message line is longer than ${maxBytes} bytes`);
    if (rest.length) {
      parts.push(rest);
      size += rest.length;
    }
  }
  function end() {
    if (!dead && size) fail('the stream ended inside a message line');
  }
  return { push, end };
}

module.exports = { createLineReader, MAX_LINE };
```

`lib/ipc/channel.js`:
```js
// The transport of the IPC (P3 D1): messages over a pair of streams, one per line, behind send(msg), onMessage(fn),
// onClose(fn) and close(). The stdio pipes of the gateway process are the only transport of v1; a network transport
// can implement the same four functions later without changing a message.
// Incoming lines are checked against ipc/v1: leniently by default (unknown fields ignored, D2), strictly in tests. An
// unknown type is logged once per type and dropped. A line that is too long, not JSON or not a valid message closes
// the channel with { reason: 'protocol', error }; the end of the input closes it with { reason: 'eof' }.
const { createLineReader, MAX_LINE } = require('./framing');
const schema = require('./schema');

/**
 * @param {{ input: NodeJS.ReadableStream, output: NodeJS.WritableStream, strict?: boolean, maxBytes?: number,
 *   log?: (line: string) => void }} opts
 */
function createChannel({ input, output, strict = false, maxBytes = MAX_LINE, log = () => {} }) {
  const listeners = [];
  const closers = [];
  const unknown = new Set();
  let closed = false;

  function onLine(line) {
    if (closed) return;
    let msg;
    try {
      msg = JSON.parse(line);
    } catch {
      return close({ reason: 'protocol', error: 'a line is not JSON' });
    }
    const r = schema.check(msg, { lenient: !strict });
    if (r.errors.length) return close({ reason: 'protocol', error: `${msg && msg.type}: ${r.errors[0]}` });
    if (r.unknownType) {
      if (!unknown.has(msg.type)) {
        unknown.add(msg.type);
        log(`ipc: unknown message type "${msg.type}" dropped`);
      }
      return;
    }
    for (const fn of listeners) fn(msg);
  }
  const reader = createLineReader({
    maxBytes,
    onLine,
    onError: (err) => close({ reason: 'protocol', error: err.message }),
  });
  const onData = (chunk) => reader.push(chunk);
  const onEnd = () => {
    reader.end();
    close({ reason: 'eof' });
  };
  const onError = (err) => close({ reason: 'error', error: err.message });
  input.on('data', onData);
  input.on('end', onEnd);
  input.on('error', onError);
  // A write to a pipe whose reader died must not throw out of the process: the channel just closes.
  output.on('error', onError);

  function close(info = { reason: 'closed' }) {
    if (closed) return;
    closed = true;
    input.off('data', onData);
    input.off('end', onEnd);
    for (const fn of closers) fn(info);
  }

  return {
    /** Writes one message; false when the channel is closed. strict: a message that is not valid v1 throws. */
    send(msg) {
      if (closed) return false;
      if (strict) {
        const r = schema.check(msg);
        if (r.errors.length || r.unknownType)
          throw new Error(`ipc: refusing to send an invalid ${msg.type}: ${r.errors[0] || 'unknown type'}`);
      }
      output.write(`${JSON.stringify(msg)}\n`);
      return true;
    },
    onMessage: (fn) => listeners.push(fn),
    onClose: (fn) => closers.push(fn),
    close,
    get closed() {
      return closed;
    },
  };
}

module.exports = { createChannel };
```

`lib/ipc/acker.js`:
```js
// Batched acknowledgements (P3 D4): the control plane acknowledges an event once it is stored, at most every
// `everyMs` ms or as soon as `max` events wait, with one `ack { stream, upTo }` for the highest seq stored.
/**
 * @param {{ send: (body: { stream: string, upTo: number }) => void, everyMs?: number, max?: number,
 *   setTimer?: typeof setTimeout, clearTimer?: typeof clearTimeout }} opts
 */
function createAcker({ send, everyMs = 100, max = 256, setTimer = setTimeout, clearTimer = clearTimeout }) {
  let stream = null;
  let upTo = 0;
  let sent = 0;
  let waiting = 0;
  let timer = null;
  function flush() {
    if (timer) clearTimer(timer);
    timer = null;
    waiting = 0;
    if (stream && upTo > sent) {
      sent = upTo;
      send({ stream, upTo });
    }
  }
  return {
    /** An event of `s` with sequence `seq` is stored (or was a duplicate). */
    note(s, seq) {
      if (s !== stream) [stream, upTo, sent] = [s, 0, 0];
      if (seq > upTo) upTo = seq;
      if (++waiting >= max) flush();
      else if (!timer) {
        timer = setTimer(flush, everyMs);
        if (timer && timer.unref) timer.unref();
      }
    },
    flush,
    /** Drops a pending timer without sending (the session is over). */
    stop() {
      if (timer) clearTimer(timer);
      timer = null;
    },
  };
}

module.exports = { createAcker };
```

Run: `npx --yes prettier@3 --write lib/ipc/framing.js lib/ipc/channel.js lib/ipc/acker.js test/ipc-framing.test.js test/ipc-channel.test.js && node --test test/ipc-framing.test.js test/ipc-channel.test.js 2>&1 | grep -E "^ℹ (pass|fail)"`
Expected: `ℹ pass 13`, `ℹ fail 0`.

- [ ] **Step 7: Write the stub build helper and the test helper**

`scripts/ipc/build-stub.js`:
```js
// Builds the P3 gateway stub (gw/cmd/zaafpulse-gw) with the Go on PATH, for the cross-language IPC tests and
// scripts/gw-dev.js. ZP_GW_BIN names a binary built already (the CI Deno job builds it first). Returns { bin } or, when
// there is no Go, { skip: <reason> }. A build that fails throws: that is a broken gateway, not a missing tool.
const fs = require('fs');
const os = require('os');
const path = require('path');
const { spawnSync } = require('child_process');

const GW_DIR = path.join(__dirname, '..', '..', 'gw');

function buildStub({ env = process.env } = {}) {
  if (env.ZP_GW_BIN) {
    if (!fs.existsSync(env.ZP_GW_BIN)) throw new Error(`ZP_GW_BIN names a file that does not exist: ${env.ZP_GW_BIN}`);
    return { bin: path.resolve(env.ZP_GW_BIN) };
  }
  const goEnv = { ...env, GOTOOLCHAIN: 'local', CGO_ENABLED: '0' };
  const v = spawnSync('go', ['version'], { encoding: 'utf8', env: goEnv });
  if (v.status !== 0)
    return { skip: 'go is not on PATH: the cross-language IPC tests build gw/cmd/zaafpulse-gw with it (CI has it)' };
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'zp-gw-bin-'));
  process.once('exit', () => fs.rmSync(dir, { recursive: true, force: true }));
  const bin = path.join(dir, process.platform === 'win32' ? 'zaafpulse-gw.exe' : 'zaafpulse-gw');
  const r = spawnSync('go', ['build', '-o', bin, './cmd/zaafpulse-gw'], { cwd: GW_DIR, encoding: 'utf8', env: goEnv });
  if (r.status !== 0) throw new Error(`go build of gw/cmd/zaafpulse-gw failed:\n${r.stderr || r.error}`);
  return { bin };
}

module.exports = { buildStub, GW_DIR };
```

`test/ipc-stub.js` (its name does not match `*.test.js`, so the suites do not run it as a test file):
```js
// Helpers of the cross-language IPC tests (not a test file): the gateway stub binary, the first config, secrets and
// snapshot it needs (from the shared fixtures), a hard kill (taskkill /F on Windows), waiting, temp folders.
const fs = require('fs');
const os = require('os');
const path = require('path');
const { spawnSync } = require('child_process');
const { buildStub } = require('../scripts/ipc/build-stub');
const { DIR } = require('../lib/ipc/schema');

let built; // one build per test process: deno test runs every file in one process
/** The stub's path, or null after t.skip() when there is no Go. */
function stubBin(t) {
  if (built === undefined) built = buildStub();
  if (built.skip) {
    t.skip(built.skip);
    return null;
  }
  return built.bin;
}

const fixtureBody = (rel) => JSON.parse(fs.readFileSync(path.join(DIR, 'fixtures', rel), 'utf8')).body;
/** config.push, secrets.handoff (both gen 1) and a budget snapshot taken now, without budgets. */
const initial = (at = Date.now()) => ({
  config: fixtureBody('config.push/valid-minimal.json'),
  secrets: fixtureBody('secrets.handoff/valid.json'),
  snapshot: { ...fixtureBody('budget.snapshot/valid-empty.json'), at },
});

function killHard(pid) {
  if (!pid) return;
  if (process.platform === 'win32') spawnSync('taskkill', ['/F', '/T', '/PID', String(pid)], { stdio: 'ignore' });
  else
    try {
      process.kill(pid, 'SIGKILL');
    } catch {
      /* already gone */
    }
}

function alive(pid) {
  try {
    process.kill(pid, 0);
    return true;
  } catch (err) {
    return err.code === 'EPERM';
  }
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
async function waitFor(fn, ms = 10000, what = 'a condition') {
  const end = Date.now() + ms;
  while (!fn()) {
    if (Date.now() > end) throw new Error(`timed out after ${ms} ms waiting for ${what}`);
    await sleep(10);
  }
}

function tmpDir(t, name) {
  const d = fs.mkdtempSync(path.join(os.tmpdir(), `zp-ipc-${name}-`));
  t.after(() => fs.rmSync(d, { recursive: true, force: true }));
  return d;
}

module.exports = { stubBin, initial, fixtureBody, killHard, alive, sleep, waitFor, tmpDir };
```

- [ ] **Step 8: Write the failing cross-language session test**

`test/ipc-session.test.js`:
```js
// The control plane starts the Go stub (gw/cmd/zaafpulse-gw) and runs the v1 handshake with it (P3 T3): hello,
// hello.ok, config.push, secrets.handoff and budget.snapshot lead to ready; health arrives; shutdown exits 0; a
// gateway offering another protocol is refused and exits 2; closing its stdin makes it exit 0; a bad line from it is
// a protocol error that kills it. Every message the stub sends is checked strictly against ipc/v1.
const test = require('node:test');
const assert = require('node:assert/strict');
const path = require('path');
const { startSession } = require('../lib/ipc/session');
const { stubBin, initial, waitFor, tmpDir } = require('./ipc-stub');

function start(t, bin, extraArgs = [], opts = {}) {
  const root = tmpDir(t, 'session');
  const logs = [];
  const health = [];
  const s = startSession({
    bin,
    args: ['--outbox', path.join(root, 'gw-outbox'), '--health-ms', '50', ...extraArgs],
    cpVersion: 'test',
    initial: initial(),
    strict: true,
    onHealth: (h) => health.push(h),
    log: (l) => logs.push(l),
    ...opts,
  });
  t.after(() => s.kill());
  return { s, logs, health };
}

test('handshake, config, secrets and snapshot lead to ready; health arrives; shutdown exits 0', async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  const { s, logs, health } = start(t, bin);
  assert.deepEqual(await s.ready, { gen: 1 });
  assert.match(s.stream, /^[0-9a-f]{32}$/);
  await waitFor(() => health.length >= 2, 5000, 'two health messages');
  const h = health.at(-1);
  assert.equal(h.gen, 1);
  assert.equal(h.outboxDepth, 0);
  assert.ok(h.snapshotAgeMs >= 0 && h.snapshotAgeMs < 5000, `snapshot age ${h.snapshotAgeMs}`);
  assert.deepEqual(await s.shutdown(1000), { code: 0, signal: null });
  assert.ok(
    logs.some((l) => l.startsWith('gw: ready (config generation 1)')),
    logs.join('\n'),
  );
});

test('a gateway that offers only another protocol is refused and exits 2', async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  const { s, logs } = start(t, bin, ['--protocol', '2']);
  await assert.rejects(s.ready, /protocol mismatch/);
  assert.deepEqual(await s.exited, { code: 2, signal: null });
  assert.ok(
    logs.some((l) => l.includes('refused by the control plane')),
    logs.join('\n'),
  );
});

test('the gateway exits 0 when its stdin closes (the control plane is gone)', async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  const { s } = start(t, bin);
  await s.ready;
  s.close();
  assert.deepEqual(await s.exited, { code: 0, signal: null });
});

test('a line from the gateway that is not JSON is a protocol error: the gateway is killed', async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  const { s, logs } = start(t, bin, ['--bad-line']);
  await s.ready;
  const r = await s.exited;
  assert.notEqual(r.code, 0);
  assert.ok(
    logs.some((l) => l === 'gw: protocol error: a line is not JSON'),
    logs.join('\n'),
  );
});
```

Run: `node --test test/ipc-session.test.js 2>&1 | tail -3`
Expected: FAIL: `Cannot find module '../lib/ipc/session'`.

- [ ] **Step 9: Write `lib/ipc/session.js`**

```js
// One run of the gateway process (P3 D1, D3, D4): start it with pipes, run the handshake, push the first config,
// secrets and budget snapshot, wait for `ready`, hand its events to onEvent and acknowledge them in batches, forward
// health, and stop it with `shutdown` (killed when it does not exit in graceMs + killAfterMs). Restarts are the
// supervisor's (lib/ipc/supervisor.js). stderr lines go to log with a "gw: " prefix.
// A protocol error, a hello that offers no common protocol, or an onEvent that throws (the event could not be
// stored, so it must not be acknowledged) ends the session: the process is killed and `ready` rejects if pending.
const { spawn } = require('child_process');
const { createChannel } = require('./channel');
const { createLineReader } = require('./framing');
const { createAcker } = require('./acker');
const { PROTOCOL, EVENTS } = require('./schema');

const EVENT = new Set(EVENTS);

/**
 * @param {{ bin: string, args?: string[], env?: Record<string, string | undefined>, cpVersion: string,
 *   initial: { config: object, secrets: object, snapshot: object },
 *   onEvent?: (msg: any, stream: string) => void, onHealth?: (body: any) => void, log?: (line: string) => void,
 *   now?: () => number, helloMs?: number, strict?: boolean, ackEveryMs?: number, ackMax?: number,
 *   killAfterMs?: number, spawnImpl?: typeof spawn }} opts
 */
function startSession({
  bin,
  args = [],
  env = process.env,
  cpVersion,
  initial,
  onEvent = () => {},
  onHealth = () => {},
  log = console.error,
  now = Date.now,
  helloMs = 5000,
  strict = false,
  ackEveryMs = 100,
  ackMax = 256,
  killAfterMs = 2000,
  spawnImpl = spawn,
}) {
  const child = spawnImpl(bin, args, { stdio: ['pipe', 'pipe', 'pipe'], env, windowsHide: true });
  let seq = 0;
  let stream = null;
  let exitedAs = null;
  let settle;
  const ready = new Promise((resolve, reject) => (settle = { resolve, reject }));
  ready.catch(() => {}); // a caller that never awaits ready must not see an unhandled rejection
  let resolveExit;
  const exited = new Promise((resolve) => (resolveExit = resolve));
  const finish = (code, signal) => {
    if (exitedAs) return;
    exitedAs = { code, signal };
    clearTimeout(helloTimer);
    acker.stop();
    settle.reject(new Error(`the gateway exited (code ${code}, signal ${signal}) before it was ready`));
    resolveExit(exitedAs);
  };
  child.once('exit', (code, signal) => finish(code, signal));
  child.once('error', (err) => {
    log(`gw: the gateway could not be started: ${err.message}`);
    finish(null, null);
  });

  const stderr = createLineReader({
    maxBytes: 64 * 1024,
    onLine: (line) => log(`gw: ${line}`),
    onError: () => log('gw: a log line was longer than 64 KiB; the rest of its log is dropped'),
  });
  child.stderr.on('data', (c) => stderr.push(c));

  const channel = createChannel({ input: child.stdout, output: child.stdin, strict, log });
  const send = (type, body, extra = {}) => channel.send({ v: 1, type, seq: ++seq, ts: now(), ...extra, body });
  const acker = createAcker({ send: (body) => send('ack', body), everyMs: ackEveryMs, max: ackMax });
  const helloTimer = setTimeout(() => fail(new Error(`no hello within ${helloMs} ms`)), helloMs);

  function kill() {
    if (!exitedAs) child.kill('SIGKILL');
  }
  function fail(err) {
    log(`gw: ${err.message}`);
    settle.reject(err);
    kill();
  }

  channel.onClose((info) => {
    if (info.reason === 'protocol') fail(new Error(`protocol error: ${info.error}`));
  });
  channel.onMessage((msg) => {
    if (msg.type === 'hello') {
      const reply = msg.id ? { re: msg.id } : {};
      if (!msg.body.protocol.includes(PROTOCOL)) {
        send(
          'hello.refused',
          {
            reason: `this control plane speaks protocol ${PROTOCOL}, the gateway offers ${JSON.stringify(msg.body.protocol)}`,
          },
          reply,
        );
        settle.reject(new Error('protocol mismatch: the gateway was refused'));
        return;
      }
      clearTimeout(helloTimer);
      stream = msg.body.stream;
      send('hello.ok', { protocol: PROTOCOL, cpVersion }, reply);
      send('config.push', initial.config);
      send('secrets.handoff', initial.secrets);
      send('budget.snapshot', initial.snapshot);
    } else if (!stream) fail(new Error(`protocol error: ${msg.type} before hello`));
    else if (EVENT.has(msg.type)) {
      try {
        onEvent(msg, stream);
      } catch (err) {
        fail(new Error(`an event could not be stored, so it is not acknowledged: ${err.message}`));
        return;
      }
      acker.note(stream, msg.seq);
    } else if (msg.type === 'ready') settle.resolve(msg.body);
    else if (msg.type === 'health') onHealth(msg.body);
    else if (msg.type === 'error')
      log(`gw: the gateway reports a protocol error: ${msg.body.code}: ${msg.body.message}`);
    else log(`gw: unexpected ${msg.type} from the gateway, dropped`);
  });

  return {
    pid: child.pid,
    get stream() {
      return stream;
    },
    ready,
    exited,
    channel,
    /** Sends a control message (config.push, secrets.handoff, budget.snapshot, ...) with the next seq. */
    send,
    flushAcks: () => acker.flush(),
    /** Acknowledges what is stored, asks the gateway to stop, and kills it when it has not exited in time. */
    async shutdown(graceMs = 5000) {
      // Asked from inside onEvent, that event's ack is noted only when onEvent returns: let it be, then flush.
      await Promise.resolve();
      if (exitedAs) return exitedAs;
      acker.flush();
      send('shutdown', { graceMs });
      const t = setTimeout(kill, graceMs + killAfterMs);
      const r = await exited;
      clearTimeout(t);
      return r;
    },
    /** Closes the gateway's stdin: it exits 0 (the control plane is gone, D1). */
    close: () => child.stdin.end(),
    kill,
  };
}

module.exports = { startSession };
```

- [ ] **Step 10: Run the session test on Node and Deno**

Run: `npx --yes prettier@3 --write lib/ipc/session.js scripts/ipc/build-stub.js test/ipc-stub.js test/ipc-session.test.js && node --test test/ipc-session.test.js 2>&1 | grep -E "^ℹ (pass|fail|skipped)" && deno test -q --no-check --no-prompt -P=test test/ipc-session.test.js 2>&1 | tail -1`
Expected: Node `ℹ pass 4`, `ℹ fail 0`, `ℹ skipped 0`; Deno `ok | 4 passed | 0 failed`. (With `PATH` stripped of Go the four tests report `skipped` with the reason `go is not on PATH: …`.)

- [ ] **Step 11: Run the three suites**

Run: `npm run test:go && test -z "$(gofmt -l gw)" && node --test test/*.test.js 2>&1 | grep -E "^ℹ fail" && deno test -q --no-check --no-prompt -P=test test/*.test.js 2>&1 | tail -1`
Expected: Go `ok`; `ℹ fail 0`; Deno `0 failed`.

- [ ] **Step 12: Commit**

```bash
git add .gitattributes ipc/v1/framing gw lib/ipc scripts/ipc test/ipc-stub.js test/ipc-framing.test.js test/ipc-channel.test.js test/ipc-session.test.js
git commit -m "Add line framing, the stdio channel and the handshake on both sides, with a Go stub that speaks the protocol"
git push origin migrate/p3-ipc-contract
```

---

### Task 4: Outbox and ingest (spec T4)

**Files:**
- Replace: `gw/ipc/outbox.go`
- Create: `gw/ipc/synthetic.go`
- Test: `gw/ipc/outbox_test.go`, `gw/ipc/synthetic_test.go`
- Modify: `gw/cmd/zaafpulse-gw/main.go` (`--emit`, `--emit-every-ms`, `--emit-base`, `--segment-events`; replay after `hello.ok`; `ack`; the emitter), `test/ipc-stub.js` (`synthetic`, `EMIT_BASE`)
- Create: `lib/ipc/marks.js`, `lib/ipc/ingest.js`, `lib/ipc/local.js`
- Test: `test/ipc-ingest.test.js`

**Interfaces:**
- Consumes: Task 3's channel, session, stub and helpers; `lib/money.js` `fromNano`; `lib/gateway/usage.js` `createUsage({ file, getPricing, now })`; `lib/governance/audit.js` `createGovernanceAudit({ dir, getKey, now })`, `keySource({ dir, store, now })`; `lib/audit.js` `createAudit(file)`; `lib/trace/store.js` `createTraceStore({ dir, now })` (`writeSpans(list)`).
- Produces:
  - Go `Outbox`: `OpenOutbox(dir)`, `SetPerSegment(n int)`, `Append(typ string, body any, ts int64) (line []byte, seq int64, err error)` (line ends in `\n`), `Replay(from int64, send func(line []byte) error) error`, `Ack(upTo int64) error`, `Segments() []int64`, plus Task 3's `Stream/Acked/Next/Depth/Close`. Segment files `<20-digit first seq>.jsonl`, `stream.json` `{stream, next, acked}`.
  - Go `Synthetic(seq, base int64) (typ string, body any)`, `DefaultEmitBase = 1767225600000`.
  - JS `marks.js`: `recoverMarks({ usageFile, governanceDir, mcpAuditFile, traceDir }, bytes = TAIL_BYTES): Map<stream, seq>`, `marksIn(lines, marks)`, `tailLines(file, bytes?)`, `TAIL_BYTES = 4 MiB`.
  - JS `ingest.js`: `createIngest({ recordUsage, recordGovernance, recordMcp, writeSpans, marks?, now? }) → { handle(msg, stream): boolean, mark(stream): number, stats(): { stored, duplicates, unchecked, lastUncheckedAt } }`.
  - JS `local.js`: `localPaths(root) → { usageFile, governanceDir, mcpAuditFile, traceDir, outboxDir }`; `openLocalStores({ root, now?, getPricing?, onUsage? }) → { files, usage, governance, mcpAudit, traces, sinks }` (`sinks` is what `createIngest` takes).
  - `test/ipc-stub.js` adds `synthetic(seq, base?) → [type, body]` and `EMIT_BASE`.

- [ ] **Step 1: Write the failing outbox and synthetic tests**

`gw/ipc/outbox_test.go`:
```go
package ipc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func appendN(t *testing.T, o *Outbox, n int) {
	t.Helper()
	for range n {
		typ, body := Synthetic(o.Next(), DefaultEmitBase)
		if _, _, err := o.Append(typ, body, 1); err != nil {
			t.Fatal(err)
		}
	}
}

func replayed(t *testing.T, o *Outbox, from int64) []int64 {
	t.Helper()
	var seqs []int64
	err := o.Replay(from, func(line []byte) error {
		msg, err := Decode(line[:len(line)-1], true)
		if err != nil {
			return err
		}
		seqs = append(seqs, msg.Seq)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return seqs
}

func TestOutboxAppendReplayAck(t *testing.T) {
	o, err := OpenOutbox(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	o.SetPerSegment(3)
	appendN(t, o, 7)
	if got := o.Segments(); !slices.Equal(got, []int64{1, 4, 7}) {
		t.Fatalf("segments %v", got)
	}
	if got := replayed(t, o, 1); !slices.Equal(got, []int64{1, 2, 3, 4, 5, 6, 7}) {
		t.Fatalf("replay %v", got)
	}
	if err := o.Ack(5); err != nil {
		t.Fatal(err)
	}
	if got := o.Segments(); !slices.Equal(got, []int64{4, 7}) {
		t.Fatalf("after ack 5: segments %v", got)
	}
	if o.Depth() != 2 || o.Acked() != 5 {
		t.Fatalf("depth %d acked %d", o.Depth(), o.Acked())
	}
	if got := replayed(t, o, 6); !slices.Equal(got, []int64{6, 7}) {
		t.Fatalf("replay from 6: %v", got)
	}
	if err := o.Ack(99); err != nil { // more than was sent: capped
		t.Fatal(err)
	}
	if len(o.Segments()) != 0 || o.Depth() != 0 || o.Acked() != 7 {
		t.Fatalf("after ack all: %v depth %d acked %d", o.Segments(), o.Depth(), o.Acked())
	}
	appendN(t, o, 1)
	if got := o.Segments(); !slices.Equal(got, []int64{8}) {
		t.Fatalf("a new segment starts at 8: %v", got)
	}
}

// A killed gateway leaves no stream.json update since the last ack: reopening finds the stream and the next seq from
// the segments.
func TestOutboxReopenAfterKill(t *testing.T) {
	dir := t.TempDir()
	o, _ := OpenOutbox(dir)
	stream := o.Stream()
	appendN(t, o, 5)
	_ = o.Ack(2)
	appendN(t, o, 1) // seq 6, after the last stream.json write
	o2, err := OpenOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	if o2.Stream() != stream || o2.Next() != 7 || o2.Acked() != 2 {
		t.Fatalf("stream %s next %d acked %d", o2.Stream(), o2.Next(), o2.Acked())
	}
	if got := replayed(t, o2, o2.Acked()+1); !slices.Equal(got, []int64{3, 4, 5, 6}) {
		t.Fatalf("replay %v", got)
	}
}

func TestOutboxTruncatesALineCutByAKill(t *testing.T) {
	dir := t.TempDir()
	o, _ := OpenOutbox(dir)
	appendN(t, o, 3)
	seg := filepath.Join(dir, "00000000000000000001.jsonl")
	f, _ := os.OpenFile(seg, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(`{"v":1,"type":"usage.rep`)
	_ = f.Close()
	o2, err := OpenOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	if o2.Next() != 4 {
		t.Fatalf("next %d", o2.Next())
	}
	if got := replayed(t, o2, 1); !slices.Equal(got, []int64{1, 2, 3}) {
		t.Fatalf("replay %v", got)
	}
	appendN(t, o2, 1)
	if got := replayed(t, o2, 1); !slices.Equal(got, []int64{1, 2, 3, 4}) {
		t.Fatalf("replay after append %v", got)
	}
}

func TestOutboxDamagedStreamFile(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "stream.json"), []byte(`{"stream":"x"}`), 0o600)
	if _, err := OpenOutbox(dir); err == nil {
		t.Fatal("a damaged stream.json must not start a new stream silently")
	}
}

// A kill between creating a segment and writing to it leaves an empty file: it is removed, and the next seq still
// follows the last event written, so no seq is used twice.
func TestOutboxEmptyLastSegment(t *testing.T) {
	dir := t.TempDir()
	o, _ := OpenOutbox(dir)
	o.SetPerSegment(3)
	appendN(t, o, 4) // segments 1 and 4
	_ = os.WriteFile(filepath.Join(dir, "00000000000000000005.jsonl"), nil, 0o600)
	o2, err := OpenOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	if o2.Next() != 5 || !slices.Equal(o2.Segments(), []int64{1, 4}) {
		t.Fatalf("next %d segments %v", o2.Next(), o2.Segments())
	}
}

// Ack writes stream.json before it deletes a segment: when a segment goes, stream.json already holds the ack and the
// next seq (found by the kill tests: a kill between the two made a reopened outbox reuse sent seqs).
func TestOutboxAckWritesStreamFileBeforeDeleting(t *testing.T) {
	dir := t.TempDir()
	o, _ := OpenOutbox(dir)
	o.SetPerSegment(2)
	appendN(t, o, 5)
	removed := 0
	o.remove = func(path string) error {
		removed++
		var st streamState
		data, _ := os.ReadFile(filepath.Join(dir, "stream.json"))
		if err := json.Unmarshal(data, &st); err != nil || st.Acked != 5 || st.Next != 6 {
			t.Fatalf("stream.json when %s goes: %s", filepath.Base(path), data)
		}
		return os.Remove(path)
	}
	if err := o.Ack(5); err != nil {
		t.Fatal(err)
	}
	if removed != 3 {
		t.Fatalf("removed %d segments", removed)
	}
}
```

`gw/ipc/synthetic_test.go`:
```go
package ipc

import (
	"testing"
)

// Every synthetic event is a valid v1 message under the strict decoder, for both kinds of audit event.
func TestSyntheticEventsAreValid(t *testing.T) {
	want := []string{TypeUsageReport, TypeAuditEvent, TypeTraceSpan, TypeUsageReport, TypeAuditEvent, TypeTraceSpan}
	for seq := int64(1); seq <= 6; seq++ {
		typ, body := Synthetic(seq, DefaultEmitBase)
		if typ != want[seq-1] {
			t.Fatalf("seq %d: %s", seq, typ)
		}
		line, err := Encode(Envelope{V: Version, Type: typ, Seq: seq, Ts: 1}, body)
		if err != nil {
			t.Fatal(err)
		}
		msg, err := Decode(line, true)
		if err != nil {
			t.Fatalf("seq %d: %v\n%s", seq, err, line)
		}
		if a, ok := msg.Body.(*AuditEventBody); ok {
			if (seq == 2) != (a.Governance() != nil) || (seq == 5) != (a.MCP() != nil) {
				t.Fatalf("seq %d: round 1 is governance, round 2 is mcp", seq)
			}
		}
	}
}
```

Run: `GOTOOLCHAIN=local go -C gw test ./... 2>&1 | head -3`
Expected: FAIL to build: `undefined: Synthetic` / `o.SetPerSegment undefined`.

- [ ] **Step 2: Replace `gw/ipc/outbox.go` with the durable outbox**

Two orders matter, both found by the kill tests while this plan was written: `Ack` writes `stream.json` **before** it deletes a segment (a kill in between must never leave a `stream.json` whose `next` is behind events already sent, or a reopened outbox reuses their seqs and the control plane drops the new events as duplicates), and `OpenOutbox` removes an empty newest segment (a kill between creating a segment and writing to it) before it reads the next seq.

```go
package ipc

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// Outbox is the gateway's durable event stream (P3 D4). Every usage.report, audit.event and trace.span is appended
// here before it is sent, to append-only segment files named by the first seq they hold (00000000000000000001.jsonl),
// one encoded envelope per line. stream.json holds {stream, next, acked}. A segment is deleted once every event in it
// is acknowledged. The durability target is a killed process, not power loss: a write is enough, there is no fsync.
//
// On open, the next seq is the larger of stream.json's next and the last complete line + 1, so stream.json is written
// on ack and close only. A line cut short by a kill is truncated: Append returns before the event is sent, so a cut
// line was never sent and its seq is free again.
type Outbox struct {
	dir        string
	perSegment int
	mu         sync.Mutex
	state      streamState
	segs       []int64 // first seq of each segment, ascending
	cur        *os.File
	curCount   int
	remove     func(string) error // os.Remove; a test watches the order of writes
}

type streamState struct {
	Stream string `json:"stream"`
	Next   int64  `json:"next"`
	Acked  int64  `json:"acked"`
}

const segmentExt = ".jsonl"

// OpenOutbox opens (or creates) the outbox in dir.
func OpenOutbox(dir string) (*Outbox, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	o := &Outbox{dir: dir, perSegment: 1000, remove: os.Remove}
	data, err := os.ReadFile(filepath.Join(dir, "stream.json"))
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &o.state); err != nil || !patterns["hex32"].MatchString(o.state.Stream) {
			return nil, fmt.Errorf("outbox: %s is damaged", filepath.Join(dir, "stream.json"))
		}
	case errors.Is(err, fs.ErrNotExist):
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		o.state = streamState{Stream: hex.EncodeToString(b), Next: 1}
	default:
		return nil, err
	}
	o.state.Next = max(o.state.Next, o.state.Acked+1)
	if err := o.loadSegments(); err != nil {
		return nil, err
	}
	if err := o.save(); err != nil {
		return nil, err
	}
	return o, o.prune()
}

// SetPerSegment sets how many events a segment holds before the next one starts (tests use a small number).
func (o *Outbox) SetPerSegment(n int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if n > 0 {
		o.perSegment = n
	}
}

func (o *Outbox) segPath(first int64) string {
	return filepath.Join(o.dir, fmt.Sprintf("%020d%s", first, segmentExt))
}

func (o *Outbox) loadSegments() error {
	entries, err := os.ReadDir(o.dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, segmentExt) {
			continue
		}
		first, err := strconv.ParseInt(strings.TrimSuffix(name, segmentExt), 10, 64)
		if err == nil && first >= 1 {
			o.segs = append(o.segs, first)
		}
	}
	slices.Sort(o.segs)
	// The newest segments may end in a line cut by a kill (truncated: it was never sent) or be empty (a kill between
	// creating the file and the first write: removed). The next seq follows the last complete line.
	for len(o.segs) > 0 {
		path := o.segPath(o.segs[len(o.segs)-1])
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if cut := bytes.LastIndexByte(data, '\n') + 1; cut < len(data) {
			if err := os.Truncate(path, int64(cut)); err != nil {
				return err
			}
			data = data[:cut]
		}
		if len(data) == 0 {
			if err := os.Remove(path); err != nil {
				return err
			}
			o.segs = o.segs[:len(o.segs)-1]
			continue
		}
		lines := bytes.Split(bytes.TrimSuffix(data, []byte{'\n'}), []byte{'\n'})
		seq, err := seqOf(lines[len(lines)-1])
		if err != nil {
			return fmt.Errorf("outbox: %s: %w", path, err)
		}
		o.state.Next = max(o.state.Next, seq+1)
		break
	}
	return nil
}

func seqOf(line []byte) (int64, error) {
	var head struct {
		Seq int64 `json:"seq"`
	}
	if err := json.Unmarshal(line, &head); err != nil || head.Seq < 1 {
		return 0, fmt.Errorf("a line without a seq: %.80s", line)
	}
	return head.Seq, nil
}

// Append stores one event with the stream's next seq and returns its line (newline included), ready to send.
func (o *Outbox) Append(typ string, body any, ts int64) ([]byte, int64, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	seq := o.state.Next
	line, err := Encode(Envelope{V: Version, Type: typ, Seq: seq, Ts: ts}, body)
	if err != nil {
		return nil, 0, err
	}
	line = append(line, '\n')
	if o.cur == nil || o.curCount >= o.perSegment {
		if err := o.rotate(seq); err != nil {
			return nil, 0, err
		}
	}
	if _, err := o.cur.Write(line); err != nil {
		return nil, 0, err
	}
	o.curCount++
	o.state.Next = seq + 1
	return line, seq, nil
}

func (o *Outbox) rotate(first int64) error {
	if o.cur != nil {
		_ = o.cur.Close()
		o.cur = nil
	}
	f, err := os.OpenFile(o.segPath(first), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if n := len(o.segs); n == 0 || o.segs[n-1] != first {
		o.segs = append(o.segs, first)
	}
	o.cur, o.curCount = f, 0
	return nil
}

// Replay calls send with every stored line whose seq is at least from, in order.
func (o *Outbox) Replay(from int64, send func(line []byte) error) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	for i, first := range o.segs {
		if i+1 < len(o.segs) && o.segs[i+1] <= from {
			continue
		}
		data, err := os.ReadFile(o.segPath(first))
		if err != nil {
			return err
		}
		for len(data) > 0 {
			nl := bytes.IndexByte(data, '\n')
			if nl < 0 {
				break
			}
			line := data[:nl+1]
			data = data[nl+1:]
			seq, err := seqOf(line[:nl])
			if err != nil {
				return err
			}
			if seq < from {
				continue
			}
			if err := send(line); err != nil {
				return err
			}
		}
	}
	return nil
}

// Ack records that the control plane stored every event with seq <= upTo, and deletes the segments that are done.
func (o *Outbox) Ack(upTo int64) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	upTo = min(upTo, o.state.Next-1)
	if upTo <= o.state.Acked {
		return nil
	}
	o.state.Acked = upTo
	// stream.json first: a kill between the two leaves acknowledged segments behind (deleted on the next open), never
	// a stream.json whose next seq is behind events already sent (their seqs would be used twice).
	if err := o.save(); err != nil {
		return err
	}
	return o.prune()
}

// prune deletes every segment whose events are all acknowledged (the open one is closed first).
func (o *Outbox) prune() error {
	keep := o.segs[:0]
	for i, first := range o.segs {
		last := o.state.Next - 1
		if i+1 < len(o.segs) {
			last = o.segs[i+1] - 1
		}
		if last > o.state.Acked {
			keep = append(keep, o.segs[i:]...)
			break
		}
		if i == len(o.segs)-1 && o.cur != nil {
			_ = o.cur.Close()
			o.cur = nil
		}
		if err := o.remove(o.segPath(first)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	o.segs = keep
	return nil
}

func (o *Outbox) save() error {
	data, err := json.Marshal(o.state)
	if err != nil {
		return err
	}
	tmp := filepath.Join(o.dir, "stream.json.tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(o.dir, "stream.json"))
}

func (o *Outbox) Stream() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.state.Stream
}

func (o *Outbox) Acked() int64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.state.Acked
}

// Next is the seq the next Append gets.
func (o *Outbox) Next() int64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.state.Next
}

// Depth is the number of events not acknowledged yet.
func (o *Outbox) Depth() int64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.state.Next - 1 - o.state.Acked
}

// Segments lists the first seq of each segment file (for tests and health).
func (o *Outbox) Segments() []int64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.segs)
}

// Close writes stream.json and closes the open segment.
func (o *Outbox) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.cur != nil {
		_ = o.cur.Close()
		o.cur = nil
	}
	return o.save()
}
```

- [ ] **Step 3: Write `gw/ipc/synthetic.go`**

```go
package ipc

import (
	"encoding/json"
	"fmt"
)

// DefaultEmitBase is the ts of the stub's first synthetic round (2026-01-01T00:00:00Z) when --emit-base is not given.
const DefaultEmitBase int64 = 1767225600000

// Synthetic is the event the stub's --emit sends as sequence seq: rounds of a usage.report, an audit.event and a
// trace.span (round r = (seq-1)/3 + 1). The audit event is a governance entry in odd rounds and an MCP tool call in
// even ones. Everything is derived from seq, so a restarted stub emits the same stream and a test can rebuild every
// event (test/ipc-stub.js synthetic() is the JS twin).
func Synthetic(seq, base int64) (string, any) {
	round := (seq-1)/3 + 1
	ts := base + round
	str := func(s string) *string { return &s }
	i64 := func(n int64) *int64 { return &n }
	switch (seq - 1) % 3 {
	case 0:
		stream := false
		return TypeUsageReport, &UsageReport{Record: UsageRecord{
			Ts: ts, KeyID: "k-emit", Provider: "stub", Model: "stub/model-a", Kind: str("chat"), Format: str("openai"),
			Stream: &stream, Status: 200, Ms: 12, Input: i64(1000), Output: i64(500), Cached: i64(0), Cost: 1_500_000,
			SessionID: str(fmt.Sprintf("emit-%d", round)),
		}}
	case 1:
		if round%2 == 1 {
			e := AuditEvent{
				Action: "key.update",
				Target: &AuditTarget{Type: "key", ID: str("k-emit"), Name: str(fmt.Sprintf("emit %d", round))},
				Actor:  &AuditActor{Via: "apikey", KeyID: str("k-emit")},
			}
			raw, _ := json.Marshal(e)
			return TypeAuditEvent, &AuditEventBody{Log: "governance", Event: raw, governance: &e}
		}
		e := MCPAuditEntry{Ts: ts, Integration: "stub", Tool: "echo", OK: true, Ms: 3, Args: []string{"text"}, KeyID: str("k-emit")}
		raw, _ := json.Marshal(e)
		return TypeAuditEvent, &AuditEventBody{Log: "mcp", Event: raw, mcp: &e}
	default:
		return TypeTraceSpan, &TraceSpanBody{Span: SpanRecord{
			T: fmt.Sprintf("%032x", round), S: fmt.Sprintf("%016x", round), N: "chat stub/model-a", K: "client",
			St: ts * 1000, D: 12000, OK: true, Src: "gateway", Svc: "zaafpulse",
			A: map[string]any{"gen_ai.request.model": "model-a"},
		}}
	}
}
```

Run: `gofmt -w gw && GOTOOLCHAIN=local go -C gw vet ./... && GOTOOLCHAIN=local go -C gw test -count=1 ./...`
Expected: `ok  	github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc`.

- [ ] **Step 4: The stub replays, acknowledges and emits**

Apply this change to `gw/cmd/zaafpulse-gw/main.go` and `test/ipc-stub.js` (save it as `/tmp/p3-t4.diff` and run `git apply /tmp/p3-t4.diff`; or make the same edits by hand):

```diff
diff --git a/gw/cmd/zaafpulse-gw/main.go b/gw/cmd/zaafpulse-gw/main.go
--- a/gw/cmd/zaafpulse-gw/main.go
+++ b/gw/cmd/zaafpulse-gw/main.go
@@ -7,7 +7,8 @@
 // error (after an "error" message) or --crash-after-ready-ms.
 //
 // Test-only flags make it misbehave for the supervisor tests: --protocol, --hang-after-ready, --crash-after-ready-ms,
-// --bad-line and --ignore-shutdown.
+// --bad-line and --ignore-shutdown; --emit N sends N rounds of synthetic events (ipc.Synthetic) for the ingest and
+// kill tests.
 package main
 
 import (
@@ -38,6 +39,10 @@ type options struct {
 	crashAfterReadyMs int
 	badLine           bool
 	ignoreShutdown    bool
+	emit              int64
+	emitEveryMs       int
+	emitBase          int64
+	segmentEvents     int
 }
 
 func main() {
@@ -58,6 +63,10 @@ func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
 	fs.IntVar(&o.crashAfterReadyMs, "crash-after-ready-ms", 0, "test only: exit 3 this many ms after ready")
 	fs.BoolVar(&o.badLine, "bad-line", false, "test only: write a line that is not JSON once ready")
 	fs.BoolVar(&o.ignoreShutdown, "ignore-shutdown", false, "test only: ignore shutdown")
+	fs.Int64Var(&o.emit, "emit", 0, "test only: send this many rounds of synthetic usage, audit and trace events")
+	fs.IntVar(&o.emitEveryMs, "emit-every-ms", 0, "test only: pause between synthetic events, in ms")
+	fs.Int64Var(&o.emitBase, "emit-base", ipc.DefaultEmitBase, "test only: ts of the first synthetic round")
+	fs.IntVar(&o.segmentEvents, "segment-events", 0, "test only: events per outbox segment (default 1000)")
 	if err := fs.Parse(args); err != nil {
 		return 2
 	}
@@ -81,6 +90,7 @@ func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
 		return 1
 	}
 	defer box.Close()
+	box.SetPerSegment(o.segmentEvents)
 	g := &gateway{
 		o:       o,
 		box:     box,
@@ -114,6 +124,10 @@ type gateway struct {
 	done    chan struct{}
 	unknown map[string]bool
 
+	sendMu   sync.Mutex // an event's append and its send stay in seq order
+	stopEmit chan struct{}
+	emitDone chan struct{}
+
 	mu      sync.Mutex
 	cfgGen  int64
 	secGen  int64
@@ -157,6 +171,11 @@ func (g *gateway) serve(protocols []int) int {
 					return 2
 				}
 				handshaken = true
+				// Everything not acknowledged goes out again, before any new event (D4).
+				if err := g.box.Replay(g.box.Acked()+1, g.ch.SendLine); err != nil {
+					g.log.Printf("outbox replay: %v", err)
+					return 1
+				}
 				go g.healthLoop()
 			case *ipc.HelloRefused:
 				g.log.Printf("refused by the control plane: %s", b.Reason)
@@ -195,7 +214,13 @@ func (g *gateway) handle(msg ipc.Message) int {
 		g.mu.Unlock()
 		g.maybeReady()
 	case *ipc.Ack:
-		// The outbox (Task 4) deletes what is acknowledged.
+		if b.Stream != g.box.Stream() {
+			g.log.Printf("ack for another stream %s, ignored", b.Stream)
+			return -1
+		}
+		if err := g.box.Ack(b.UpTo); err != nil {
+			g.log.Printf("outbox ack: %v", err)
+		}
 	case *ipc.Shutdown:
 		if g.o.ignoreShutdown {
 			g.log.Print("shutdown ignored (--ignore-shutdown)")
@@ -248,6 +273,63 @@ func (g *gateway) afterReady() {
 	if g.o.badLine {
 		_ = g.ch.SendLine([]byte("this line is not JSON"))
 	}
+	g.startEmitter()
+}
+
+// startEmitter sends the synthetic events of --emit from the outbox's next seq up to 3*emit: a restarted stub picks up
+// where the killed one stopped, so a run is exactly 3*emit events whatever the kills.
+func (g *gateway) startEmitter() {
+	if g.o.emit <= 0 {
+		return
+	}
+	g.stopEmit, g.emitDone = make(chan struct{}), make(chan struct{})
+	go func() {
+		defer close(g.emitDone)
+		total := 3 * g.o.emit
+		for g.box.Next() <= total {
+			select {
+			case <-g.stopEmit:
+				return
+			default:
+			}
+			typ, body := ipc.Synthetic(g.box.Next(), g.o.emitBase)
+			if err := g.appendAndSend(typ, body); err != nil {
+				g.log.Printf("emit: %v", err)
+				return
+			}
+			if g.o.emitEveryMs > 0 {
+				select {
+				case <-g.stopEmit:
+					return
+				case <-time.After(time.Duration(g.o.emitEveryMs) * time.Millisecond):
+				}
+			}
+		}
+		g.log.Printf("emitted every synthetic event up to seq %d", total)
+	}()
+}
+
+// appendAndSend writes an event to the outbox, then sends it (D4: never the other way round).
+func (g *gateway) appendAndSend(typ string, body any) error {
+	g.sendMu.Lock()
+	defer g.sendMu.Unlock()
+	line, _, err := g.box.Append(typ, body, time.Now().UnixMilli())
+	if err != nil {
+		return err
+	}
+	return g.ch.SendLine(line)
+}
+
+func (g *gateway) stopEmitter(grace time.Duration) {
+	if g.stopEmit == nil {
+		return
+	}
+	close(g.stopEmit)
+	select {
+	case <-g.emitDone:
+	case <-time.After(grace):
+		g.log.Print("the emitter did not stop within the grace period")
+	}
 }
 
 func (g *gateway) healthLoop() {
@@ -283,8 +365,10 @@ func (g *gateway) sendHealth() {
 // drain: the control plane closed the channel.
 func (g *gateway) drain() {
 	g.log.Print("the control plane closed the channel: exiting")
+	g.stopEmitter(time.Second)
 }
 
 func (g *gateway) shutdown(grace time.Duration) {
 	g.log.Printf("shutting down (grace %s)", grace)
+	g.stopEmitter(grace)
 }
diff --git a/test/ipc-stub.js b/test/ipc-stub.js
--- a/test/ipc-stub.js
+++ b/test/ipc-stub.js
@@ -26,6 +26,74 @@ const initial = (at = Date.now()) => ({
   snapshot: { ...fixtureBody('budget.snapshot/valid-empty.json'), at },
 });
 
+// The JS twin of gw/ipc/synthetic.go Synthetic(): the event the stub's --emit sends as `seq`, as [type, body].
+const EMIT_BASE = 1767225600000;
+function synthetic(seq, base = EMIT_BASE) {
+  const round = Math.floor((seq - 1) / 3) + 1;
+  const ts = base + round;
+  const kind = (seq - 1) % 3;
+  if (kind === 0)
+    return [
+      'usage.report',
+      {
+        record: {
+          ts,
+          keyId: 'k-emit',
+          provider: 'stub',
+          model: 'stub/model-a',
+          kind: 'chat',
+          format: 'openai',
+          stream: false,
+          status: 200,
+          ms: 12,
+          input: 1000,
+          output: 500,
+          cached: 0,
+          cost: 1500000,
+          sessionId: `emit-${round}`,
+        },
+      },
+    ];
+  if (kind === 1)
+    return round % 2 === 1
+      ? [
+          'audit.event',
+          {
+            log: 'governance',
+            event: {
+              action: 'key.update',
+              target: { type: 'key', id: 'k-emit', name: `emit ${round}` },
+              actor: { via: 'apikey', keyId: 'k-emit' },
+            },
+          },
+        ]
+      : [
+          'audit.event',
+          {
+            log: 'mcp',
+            event: { ts, integration: 'stub', tool: 'echo', ok: true, ms: 3, args: ['text'], keyId: 'k-emit' },
+          },
+        ];
+  return [
+    'trace.span',
+    {
+      span: {
+        t: round.toString(16).padStart(32, '0'),
+        s: round.toString(16).padStart(16, '0'),
+        p: null,
+        n: 'chat stub/model-a',
+        k: 'client',
+        st: ts * 1000,
+        d: 12000,
+        ok: true,
+        src: 'gateway',
+        svc: 'zaafpulse',
+        a: { 'gen_ai.request.model': 'model-a' },
+      },
+    },
+  ];
+}
+
 function killHard(pid) {
   if (!pid) return;
   if (process.platform === 'win32') spawnSync('taskkill', ['/F', '/T', '/PID', String(pid)], { stdio: 'ignore' });
@@ -61,4 +129,4 @@ function tmpDir(t, name) {
   return d;
 }
 
-module.exports = { stubBin, initial, fixtureBody, killHard, alive, sleep, waitFor, tmpDir };
+module.exports = { stubBin, initial, fixtureBody, synthetic, EMIT_BASE, killHard, alive, sleep, waitFor, tmpDir };
```

Run: `gofmt -w gw && test -z "$(gofmt -l gw)" && GOTOOLCHAIN=local go -C gw vet ./... && npx --yes prettier@3 --check test/ipc-stub.js`
Expected: no output from gofmt, vet clean, `All matched files use Prettier code style!`.

- [ ] **Step 5: Write the failing ingest tests**

`test/ipc-ingest.test.js`:
```js
// The control plane stores the gateway's events (P3 T4): through the in-process writers, with an ipc mark in every
// stored line, duplicates skipped by (stream, seq), marks recovered from the stores' tails after a restart. Against
// the Go stub: a resend after the gateway restarts, or after the control plane restarts, stores each event exactly
// once; the stored usage lines, the budget reports, the governance chain, the MCP audit log and the trace store equal
// an in-process run of the same events; acknowledged segments leave the outbox.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const { createIngest } = require('../lib/ipc/ingest');
const { recoverMarks, marksIn, tailLines } = require('../lib/ipc/marks');
const { openLocalStores } = require('../lib/ipc/local');
const { startSession } = require('../lib/ipc/session');
const { fromNano } = require('../lib/money');
const { stubBin, initial, synthetic, waitFor, tmpDir } = require('./ipc-stub');

const S = '0f1e2d3c4b5a69788796a5b4c3d2e1f0';
const ROUNDS = 20; // 60 events
const TOTAL = 3 * ROUNDS;
const msgOf = (seq) => {
  const [type, body] = synthetic(seq);
  return { v: 1, type, seq, ts: 0, body };
};
const readJsonl = (file) =>
  fs.existsSync(file)
    ? fs
        .readFileSync(file, 'utf8')
        .split('\n')
        .filter(Boolean)
        .map((l) => JSON.parse(l))
    : [];
const traceSpans = (dir) =>
  fs.existsSync(dir)
    ? fs
        .readdirSync(dir)
        .filter((f) => f.endsWith('.ndjson'))
        .flatMap((f) => readJsonl(path.join(dir, f)))
    : [];
const without = (e, ...keys) => Object.fromEntries(Object.entries(e).filter(([k]) => !keys.includes(k)));
const CHAIN = ['seq', 'ts', 'prev', 'mac', 'keyed'];
const governanceEntries = (dir) =>
  fs
    .readdirSync(dir)
    .filter((f) => /^governance-.*\.jsonl$/.test(f))
    .sort()
    .flatMap((f) => readJsonl(path.join(dir, f)));

function recording() {
  const calls = { usage: [], governance: [], mcp: [], spans: [] };
  const sinks = {
    recordUsage: (e) => calls.usage.push(e),
    recordGovernance: (e) => calls.governance.push(e),
    recordMcp: (e) => calls.mcp.push(e),
    writeSpans: (l) => calls.spans.push(...l),
  };
  return { calls, sinks };
}

test('each event goes to its writer with an ipc mark; cost back to USD; a resend is skipped', () => {
  const { calls, sinks } = recording();
  const ingest = createIngest({ ...sinks, now: () => 42 });
  for (let seq = 1; seq <= 6; seq++) assert.equal(ingest.handle(msgOf(seq), S), true);
  assert.equal(calls.usage.length, 2);
  assert.equal(calls.usage[0].cost, 0.0015);
  assert.deepEqual(calls.usage[0].ipc, { stream: S, seq: 1 });
  assert.equal(calls.governance.length, 1, 'round 1: governance');
  assert.equal(calls.mcp.length, 1, 'round 2: mcp');
  assert.deepEqual(calls.governance[0].ipc, { stream: S, seq: 2 });
  assert.deepEqual(calls.spans[1].ipc, { stream: S, seq: 6 });
  assert.equal(ingest.mark(S), 6);
  assert.equal(ingest.handle(msgOf(4), S), false, 'at or below the mark: a duplicate');
  assert.equal(calls.usage.length, 2);
  const unchecked = { ...msgOf(7), body: { ...msgOf(7).body, budget: 'unchecked' } };
  ingest.handle(unchecked, S);
  assert.equal(calls.usage[2].budget, 'unchecked');
  assert.deepEqual(ingest.stats(), { stored: 7, duplicates: 1, unchecked: 1, lastUncheckedAt: 42 });
  assert.throws(() => ingest.handle({ type: 'health', seq: 8, body: {} }, S), /not an event/);
});

test('a writer that throws leaves the mark where it was, so the event is not acknowledged', () => {
  const { sinks } = recording();
  const ingest = createIngest({
    ...sinks,
    recordUsage: () => {
      throw new Error('disk full');
    },
  });
  assert.throws(() => ingest.handle(msgOf(1), S), /disk full/);
  assert.equal(ingest.mark(S), 0);
});

test('marks: the highest seq per stream from the stores, rotated usage log included; a cut last line is ignored', (t) => {
  const root = tmpDir(t, 'marks');
  const stores = openLocalStores({ root });
  const other = 'a'.repeat(32);
  const ingest = createIngest(stores.sinks);
  for (let seq = 1; seq <= 9; seq++) ingest.handle(msgOf(seq), S);
  ingest.handle(msgOf(1), other);
  fs.renameSync(stores.files.usageFile, `${stores.files.usageFile}.1`);
  fs.appendFileSync(stores.files.usageFile, `{"ts":1,"keyId":"dashboard","cost":0}\n{"ipc":{"stream":"${S}","seq":99`);
  const marks = recoverMarks(stores.files);
  assert.equal(marks.get(S), 9, 'trace.span seq 9 is the newest');
  assert.equal(marks.get(other), 1);
  assert.deepEqual(tailLines(stores.files.usageFile), ['{"ts":1,"keyId":"dashboard","cost":0}']);
  assert.deepEqual([...marksIn(['not json', '{"ipc":{"stream":"x","seq":"3"}}'], new Map())], []);
});

// Runs the stub with --emit until every event of the run is stored, with acks held back so all of them stay in the
// outbox. Returns the stream id.
async function emitAll(t, bin, root, ingest, extra = {}) {
  const stores = extra.stores;
  const s = startSession({
    bin,
    args: ['--outbox', stores.files.outboxDir, '--emit', String(ROUNDS), '--segment-events', '25'],
    cpVersion: 'test',
    initial: initial(),
    strict: true,
    onEvent: (msg, stream) => ingest.handle(msg, stream),
    log: () => {},
    ackEveryMs: 1e9,
    ackMax: 1e9,
  });
  t.after(() => s.kill());
  await s.ready;
  await waitFor(() => s.stream && ingest.mark(s.stream) === TOTAL, 10000, `${TOTAL} events stored`);
  return s;
}

test('the gateway restarts without acks: everything is resent and stored once (stub)', async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  const root = tmpDir(t, 'resend');
  const stores = openLocalStores({ root });
  const ingest = createIngest(stores.sinks);
  const first = await emitAll(t, bin, root, ingest, { stores });
  first.kill(); // no ack was sent
  await first.exited;
  assert.equal(ingest.stats().duplicates, 0);
  const again = startSession({
    bin,
    args: ['--outbox', stores.files.outboxDir, '--emit', String(ROUNDS), '--segment-events', '25'],
    cpVersion: 'test',
    initial: initial(),
    strict: true,
    onEvent: (msg, stream) => ingest.handle(msg, stream),
    log: () => {},
  });
  t.after(() => again.kill());
  await again.ready;
  assert.equal(again.stream, first.stream, 'the same durable stream');
  assert.equal(ingest.stats().duplicates, TOTAL, 'all of them came again, before ready');
  assert.deepEqual(await again.shutdown(2000), { code: 0, signal: null });
  const usage = readJsonl(stores.files.usageFile);
  assert.equal(usage.length, ROUNDS);
  assert.deepEqual(
    usage.map((e) => e.ipc.seq),
    Array.from({ length: ROUNDS }, (_, i) => 3 * i + 1),
  );
  assert.deepEqual(
    fs.readdirSync(stores.files.outboxDir).filter((f) => f.endsWith('.jsonl')),
    [],
    'every segment is acknowledged and deleted',
  );
});

test('the control plane restarts: marks from the stores make the resend a no-op (stub)', async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  const root = tmpDir(t, 'cp-restart');
  const stores = openLocalStores({ root });
  const first = await emitAll(t, bin, root, createIngest(stores.sinks), { stores });
  first.kill();
  await first.exited;
  // A new control plane: new stores and a new ingest, its marks read from the files.
  const stores2 = openLocalStores({ root });
  const ingest2 = createIngest({ ...stores2.sinks, marks: recoverMarks(stores2.files) });
  assert.equal(ingest2.mark(first.stream), TOTAL);
  const again = startSession({
    bin,
    args: ['--outbox', stores2.files.outboxDir],
    cpVersion: 'test',
    initial: initial(),
    strict: true,
    onEvent: (msg, stream) => ingest2.handle(msg, stream),
    log: () => {},
  });
  t.after(() => again.kill());
  await again.ready;
  await again.shutdown(2000);
  assert.equal(ingest2.stats().duplicates, TOTAL);
  assert.equal(ingest2.stats().stored, 0);
  assert.equal(readJsonl(stores2.files.usageFile).length, ROUNDS);
  assert.equal(stores2.governance.verify().ok, true);
});

test('stored through the IPC equals the same events recorded in process (stub)', async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  // In process: the gateway's own calls, as lib/gateway/index.js and lib/integrations.js make them.
  const local = tmpDir(t, 'in-process');
  const budgetLocal = [];
  const a = openLocalStores({ root: local, onUsage: (e) => budgetLocal.push(e) });
  for (let seq = 1; seq <= TOTAL; seq++) {
    const [type, body] = synthetic(seq);
    if (type === 'usage.report') a.sinks.recordUsage({ ...body.record, cost: fromNano(body.record.cost) });
    else if (type === 'audit.event' && body.log === 'governance') a.sinks.recordGovernance(body.event);
    else if (type === 'audit.event') a.sinks.recordMcp(body.event);
    else a.sinks.writeSpans([body.span]);
  }
  // Over the IPC.
  const remote = tmpDir(t, 'over-ipc');
  const budgetIpc = [];
  const b = openLocalStores({ root: remote, onUsage: (e) => budgetIpc.push(e) });
  const s = await emitAll(t, bin, remote, createIngest(b.sinks), { stores: b });
  await s.shutdown(2000);

  const strip = (list) => list.map((e) => without(e, 'ipc', 'budget'));
  assert.deepEqual(
    strip(readJsonl(b.files.usageFile)),
    strip(readJsonl(a.files.usageFile)),
    'usage lines (ledger input)',
  );
  assert.deepEqual(strip(budgetIpc), strip(budgetLocal), 'budget reports');
  assert.deepEqual(strip(readJsonl(b.files.mcpAuditFile)), strip(readJsonl(a.files.mcpAuditFile)), 'MCP audit');
  assert.deepEqual(strip(traceSpans(b.files.traceDir)), strip(traceSpans(a.files.traceDir)), 'trace store');
  const gov = (dir) => governanceEntries(dir).map((e) => without(e, ...CHAIN, 'ipc'));
  assert.deepEqual(gov(b.files.governanceDir), gov(a.files.governanceDir), 'governance entries');
  assert.equal(b.governance.verify().ok, true, 'the chain written from IPC events verifies');
});
```

Run: `node --test test/ipc-ingest.test.js 2>&1 | tail -3`
Expected: FAIL: `Cannot find module '../lib/ipc/ingest'`.

- [ ] **Step 6: Write `lib/ipc/marks.js`, `lib/ipc/ingest.js` and `lib/ipc/local.js`**

`lib/ipc/marks.js`:
```js
// The control plane's high-water marks per gateway event stream (P3 D4). There is no mark file: every event stored
// from the gateway carries "ipc": { stream, seq } in its own line (the usage log, the governance chain, the MCP audit
// log, the trace store), so storing an event and marking it are one append. On start the marks are recovered from the
// tails of those files: the highest seq stored per stream. Events arrive in seq order and are stored in that order, so
// every seq at or below the mark is stored. Bound: the last TAIL_BYTES of each file are read (see the P3 plan).
const fs = require('fs');
const path = require('path');

const TAIL_BYTES = 4 * 1024 * 1024;

// The complete lines in the last `bytes` of a file ([] when it cannot be read).
function tailLines(file, bytes = TAIL_BYTES) {
  let fd;
  try {
    fd = fs.openSync(file, 'r');
  } catch {
    return [];
  }
  try {
    const size = fs.fstatSync(fd).size;
    const start = Math.max(0, size - bytes);
    const buf = Buffer.alloc(size - start);
    fs.readSync(fd, buf, 0, buf.length, start);
    const lines = buf.toString('utf8').split('\n');
    if (start > 0) lines.shift(); // the first piece may be cut
    lines.pop(); // the piece after the last newline: empty, or a line still being written
    return lines;
  } finally {
    fs.closeSync(fd);
  }
}

// The newest `n` files of a folder whose names match `re` (names sort by date).
function newest(dir, re, n) {
  try {
    return fs
      .readdirSync(dir)
      .filter((f) => re.test(f))
      .sort()
      .slice(-n)
      .map((f) => path.join(dir, f));
  } catch {
    return [];
  }
}

/** Adds the marks found in `lines` to `marks` (Map stream -> highest seq). */
function marksIn(lines, marks) {
  for (const line of lines) {
    if (!line.includes('"ipc"')) continue;
    let e;
    try {
      e = JSON.parse(line);
    } catch {
      continue;
    }
    const m = e && e.ipc;
    if (m && typeof m.stream === 'string' && Number.isSafeInteger(m.seq) && m.seq > (marks.get(m.stream) || 0))
      marks.set(m.stream, m.seq);
  }
  return marks;
}

/**
 * The marks of every stream, from the stores' tails.
 * @param {{ usageFile: string, governanceDir: string, mcpAuditFile: string, traceDir: string }} files
 * @param {number} [bytes]
 * @returns {Map<string, number>}
 */
function recoverMarks({ usageFile, governanceDir, mcpAuditFile, traceDir }, bytes = TAIL_BYTES) {
  const marks = new Map();
  const files = [
    `${usageFile}.1`,
    usageFile,
    `${mcpAuditFile}.1`,
    mcpAuditFile,
    ...newest(governanceDir, /^governance-\d{4}-\d{2}\.jsonl$/, 2),
    ...newest(traceDir, /^\d{4}-\d{2}-\d{2}\.ndjson$/, 2),
  ];
  for (const f of files) marksIn(tailLines(f, bytes), marks);
  return marks;
}

module.exports = { recoverMarks, marksIn, tailLines, TAIL_BYTES };
```

`lib/ipc/ingest.js`:
```js
// Stores the gateway's events on the control plane (P3 D4), through the same writers the in-process gateway uses, so
// the control plane stays the only writer of the usage log, the ledger's input, the governance chain, the MCP audit
// log and the trace store (program plan §3 rule 1):
//   usage.report -> recordUsage (lib/gateway/usage.js record, then the budget report, as lib/gateway/index.js wraps it),
//                   cost converted from nano-USD to the stored float USD;
//   audit.event  -> recordGovernance (server.js govRecord) or recordMcp (lib/audit.js record), by `log`;
//   trace.span   -> writeSpans (lib/trace/store.js), one record.
// Each stored event carries "ipc": { stream, seq }. An event at or below its stream's mark is a resend of one already
// stored: it is skipped (and still acknowledged by the session). A writer that throws is not caught: the session does
// not acknowledge the event, kills the gateway, and the event is sent again after the restart.
const { fromNano } = require('../money');
const { EVENTS } = require('./schema');

/**
 * @param {{ recordUsage: (e: object) => unknown, recordGovernance: (e: object) => unknown,
 *   recordMcp: (e: object) => unknown, writeSpans: (list: object[]) => unknown, marks?: Map<string, number>,
 *   now?: () => number }} sinks
 */
function createIngest({ recordUsage, recordGovernance, recordMcp, writeSpans, marks = new Map(), now = Date.now }) {
  const hwm = new Map(marks);
  const stats = { stored: 0, duplicates: 0, unchecked: 0, lastUncheckedAt: null };

  /**
   * Stores one event of `stream`. Returns true when it was stored, false when it was a duplicate.
   * @param {{ type: string, seq: number, body: any }} msg
   * @param {string} stream
   */
  function handle(msg, stream) {
    if (!EVENTS.includes(msg.type)) throw new Error(`ipc ingest: ${msg.type} is not an event`);
    if (msg.seq <= (hwm.get(stream) || 0)) {
      stats.duplicates++;
      return false;
    }
    const ipc = { stream, seq: msg.seq };
    const b = msg.body;
    if (msg.type === 'usage.report') {
      recordUsage({ ...b.record, cost: fromNano(b.record.cost), ...(b.budget ? { budget: b.budget } : {}), ipc });
      if (b.budget === 'unchecked') {
        stats.unchecked++;
        stats.lastUncheckedAt = now();
      }
    } else if (msg.type === 'audit.event') {
      if (b.log === 'governance') recordGovernance({ ...b.event, ipc });
      else recordMcp({ ...b.event, ipc });
    } else writeSpans([{ ...b.span, ipc }]);
    hwm.set(stream, msg.seq);
    stats.stored++;
    return true;
  }

  return {
    handle,
    /** The highest seq stored for `stream` (0: none). */
    mark: (stream) => hwm.get(stream) || 0,
    stats: () => ({ ...stats }),
  };
}

module.exports = { createIngest };
```

`lib/ipc/local.js`:
```js
// The control plane's stores for the IPC ingest, at the paths server.js uses under the data folder DIR
// (lib/platform.js dataDir()): .data/gateway-usage.jsonl, .data/audit (the governance chain, its key in
// .data/audit/chain.key when no secret store is given), .cache/mcp-audit.jsonl and .data/traces; the gateway's outbox
// is .data/gw-outbox. Used by the IPC tests, the kill tests' control plane and scripts/gw-dev.js. P4 S1 gives the
// ingest the server's own objects (its wrapped usage.record, govRecord, audit, trace store) instead.
const path = require('path');
const { createUsage } = require('../gateway/usage');
const { createGovernanceAudit, keySource } = require('../governance/audit');
const { createAudit } = require('../audit');
const { createTraceStore } = require('../trace/store');

/** @param {string} root the data folder (DIR) */
function localPaths(root) {
  return {
    usageFile: path.join(root, '.data', 'gateway-usage.jsonl'),
    governanceDir: path.join(root, '.data', 'audit'),
    mcpAuditFile: path.join(root, '.cache', 'mcp-audit.jsonl'),
    traceDir: path.join(root, '.data', 'traces'),
    outboxDir: path.join(root, '.data', 'gw-outbox'),
  };
}

/**
 * @param {{ root: string, now?: () => number, getPricing?: () => object, onUsage?: (entry: object) => void }} opts
 * onUsage stands for the budget report (lib/gateway/index.js calls budgetGuard.record right after usage.record).
 */
function openLocalStores({ root, now = Date.now, getPricing = () => ({}), onUsage = () => {} }) {
  const files = localPaths(root);
  const usage = createUsage({ file: files.usageFile, getPricing, now });
  const governance = createGovernanceAudit({
    dir: files.governanceDir,
    getKey: keySource({ dir: files.governanceDir, store: null, now }),
    now,
  });
  const mcpAudit = createAudit(files.mcpAuditFile);
  const traces = createTraceStore({ dir: files.traceDir, now });
  return {
    files,
    usage,
    governance,
    mcpAudit,
    traces,
    sinks: {
      recordUsage: (e) => {
        const entry = usage.record(e);
        onUsage(entry);
        return entry;
      },
      recordGovernance: (e) => governance.record(e),
      recordMcp: (e) => mcpAudit.record(e),
      writeSpans: (list) => traces.writeSpans(list),
    },
  };
}

module.exports = { openLocalStores, localPaths };
```

- [ ] **Step 7: Run the ingest tests on Node and Deno**

Run: `npx --yes prettier@3 --write lib/ipc/marks.js lib/ipc/ingest.js lib/ipc/local.js test/ipc-ingest.test.js && node --test test/ipc-ingest.test.js 2>&1 | grep -E "^ℹ (pass|fail)" && deno test -q --no-check --no-prompt -P=test test/ipc-ingest.test.js 2>&1 | tail -1`
Expected: Node `ℹ pass 6`, `ℹ fail 0`; Deno `ok | 6 passed | 0 failed`. The equality test is the T4 exit: the usage lines (the ledger's input), the budget reports, the governance chain (which verifies), the MCP audit log and the trace store written from IPC events equal an in-process run of the same events, `ipc` and `budget` aside.

- [ ] **Step 8: Run the three suites**

Run: `npm run test:go && node --test test/*.test.js 2>&1 | grep -E "^ℹ fail" && deno test -q --no-check --no-prompt -P=test test/*.test.js 2>&1 | tail -1`
Expected: Go `ok`; `ℹ fail 0`; Deno `0 failed`.

- [ ] **Step 9: Commit**

```bash
git add gw lib/ipc test/ipc-stub.js test/ipc-ingest.test.js
git commit -m "Add the gateway outbox and the control plane's IPC ingest with marks in the stored lines, so a resend is stored once"
git push origin migrate/p3-ipc-contract
```

---

### Task 5: Supervisor and budget staleness (spec T5)

**Files:**
- Create: `ipc/v1/fixtures/budget-decide/*.json` (24 files), `gw/ipc/budget.go`, `lib/ipc/budget.js`, `lib/ipc/supervisor.js`, `scripts/gw-dev.js`
- Test: `gw/ipc/budget_test.go`, `test/ipc-budget.test.js`, `test/ipc-supervisor.test.js`
- Modify: `gw/cmd/zaafpulse-gw/main.go` (`--stale-ms`, `--health-on-snapshot`, the budget decision on emitted usage), `package.json` (`gw:dev`)

**Interfaces:**
- Consumes: `lib/cost/guard.js` `createGuard({ service, now })` with `service.list()`, `evaluateOne(b)`, `keyCenter(keyId)`, `reportGuard()`; Task 3's `startSession`; Task 4's ingest.
- Produces:
  - Go: `const StaleAfterMs int64 = 5000`; `Pass`, `Refuse`, `Unchecked`; `type CheckInput struct{KeyID, Model, Provider, Agent, RunID string}` (JSON `keyId, model, provider, agent, runId`, Model already normalized); `type Decision struct{Action, Budget, Message string}`; `func Decide(s *BudgetSnapshot, now, staleMs int64, in CheckInput) Decision`.
  - JS `budget.js`: `decide(snapshot | null, now, input, staleMs = 5000) → { action, budget?, message? }`, `STALE_AFTER_MS`.
  - JS `supervisor.js`: `createSupervisor({ bin, args?, env?, cpVersion, sources: { config(), secrets(), snapshot() }, ingest?, log?, now?, healthTimeoutMs = 5000, snapshotEveryMs = 1000, backoff: { firstMs = 1000, maxMs = 30000, resetAfterMs = 60000 }, killAfterMs = 2000, start? }) → { start(), reload(): boolean, pushSnapshot(), stop(graceMs = 5000): Promise<exit>, status(): { state: 'starting'|'ready'|'restarting'|'stopped', pid, gen, restarts, lastExit, lastHealth, unchecked: { count, lastAt }, alert } }`. `sources.config()`/`secrets()` return bodies whose `gen` the supervisor overwrites; `snapshot()` returns `{ budgets, keyCenter }`, the supervisor adds `at`.
  - Fixture shape `budget-decide/<expected action>-<name>.json`: `{ snapshot: budget.snapshot body | null, now, input: CheckInput, expect: Decision }`.
  - Stub flags `--stale-ms N` (default 5000; `-1` = always stale) and `--health-on-snapshot`.

- [ ] **Step 1: Write the shared budget decisions**

Run in `bash` from the repo root:
```bash
D=ipc/v1/fixtures/budget-decide
mkdir -p $D
# One budget view; scope, enforce and state vary per fixture.
b() { echo '{"id":"'$1'","name":"'$2'","scope":'$3',"period":"'$4'","currency":"USD","measure":"list","enforce":"'$5'","amount":100,"from":"2026-01-01","to":"2026-01-31","state":"'$6'","spentUSD":120,"amountUSD":100,"reason":null}'; }
snap() { echo '{"at":1767225600000,"budgets":['"$1"'],"keyCenter":{"k1":"platform","k2":"research"}}'; }
fx() { echo '{"snapshot":'"$2"',"now":'"$3"',"input":'"$4"',"expect":'"$5"'}' > $D/$1.json; }
NOW=1767225601000
G=$(b b-global 'All AI' '"global"' month block exceeded)
fx pass-no-budgets "$(snap '')" $NOW '{"keyId":"k1","model":"gpt-6.1-sol","provider":"openai"}' '{"action":"pass"}'
fx refuse-global-exceeded "$(snap "$G")" $NOW '{"keyId":"k1","model":"gpt-6.1-sol","provider":"openai"}' '{"action":"refuse","budget":"b-global","message":"budget \"All AI\" is used up for this month"}'
fx refuse-global-dashboard-call "$(snap "$G")" $NOW '{"keyId":"dashboard","model":"gpt-6.1-sol"}' '{"action":"refuse","budget":"b-global","message":"budget \"All AI\" is used up for this month"}'
fx pass-global-warn "$(snap "$(b b-global 'All AI' '"global"' month block warn_80)")" $NOW '{"keyId":"k1"}' '{"action":"pass"}'
fx pass-not-blocking "$(snap "$(b b-global 'All AI' '"global"' month none exceeded)")" $NOW '{"keyId":"k1"}' '{"action":"pass"}'
fx pass-pause-is-not-block "$(snap "$(b b-global 'All AI' '"global"' month pause exceeded)")" $NOW '{"keyId":"k1"}' '{"action":"pass"}'
fx pass-no-data "$(snap "$(b b-global 'All AI' '"global"' month block no_data)")" $NOW '{"keyId":"k1"}' '{"action":"pass"}'
K='{"dim":"key","value":"k1"}'
fx refuse-key-scope "$(snap "$(b b-key 'CI key' "$K" day block exceeded)")" $NOW '{"keyId":"k1","model":"gpt-6.1-sol"}' '{"action":"refuse","budget":"b-key","message":"budget \"CI key\" is used up for this day"}'
fx pass-key-scope-other-key "$(snap "$(b b-key 'CI key' "$K" day block exceeded)")" $NOW '{"keyId":"k2"}' '{"action":"pass"}'
fx pass-key-scope-dashboard "$(snap "$(b b-dash 'Dashboard' '{"dim":"key","value":"dashboard"}' day block exceeded)")" $NOW '{"keyId":"dashboard"}' '{"action":"pass"}'
C='{"dim":"center","value":"research"}'
fx refuse-center-scope "$(snap "$(b b-research 'Research' "$C" week block exceeded)")" $NOW '{"keyId":"k2"}' '{"action":"refuse","budget":"b-research","message":"budget \"Research\" is used up for this week"}'
fx pass-center-scope-other-center "$(snap "$(b b-research 'Research' "$C" week block exceeded)")" $NOW '{"keyId":"k1"}' '{"action":"pass"}'
fx pass-center-scope-unknown-key "$(snap "$(b b-research 'Research' "$C" week block exceeded)")" $NOW '{"keyId":"k9"}' '{"action":"pass"}'
fx refuse-model-scope "$(snap "$(b b-opus 'Opus' '{"dim":"model","value":"claude-opus-5-5"}' month block exceeded)")" $NOW '{"keyId":"k1","model":"claude-opus-5-5"}' '{"action":"refuse","budget":"b-opus","message":"budget \"Opus\" is used up for this month"}'
fx pass-model-scope-other-model "$(snap "$(b b-opus 'Opus' '{"dim":"model","value":"claude-opus-5-5"}' month block exceeded)")" $NOW '{"keyId":"k1","model":"gpt-6.1-sol"}' '{"action":"pass"}'
fx refuse-provider-scope "$(snap "$(b b-oai 'OpenAI' '{"dim":"provider","value":"openai"}' month block exceeded)")" $NOW '{"keyId":"k1","provider":"openai"}' '{"action":"refuse","budget":"b-oai","message":"budget \"OpenAI\" is used up for this month"}'
SRC='{"dim":"source","value":"gateway"}'
fx refuse-source-gateway "$(snap "$(b b-src 'Gateway' "$SRC" month block exceeded)")" $NOW '{"keyId":"k1"}' '{"action":"refuse","budget":"b-src","message":"budget \"Gateway\" is used up for this month"}'
fx pass-source-collab-run "$(snap "$(b b-src 'Gateway' "$SRC" month block exceeded)")" $NOW '{"keyId":"k1","runId":"r-7"}' '{"action":"pass"}'
fx pass-source-judge "$(snap "$(b b-src 'Gateway' "$SRC" month block exceeded)")" $NOW '{"keyId":"k1","agent":"judge"}' '{"action":"pass"}'
fx refuse-first-exceeded-in-order "$(snap "$(b b-a 'A' '"global"' day block warn_90),$(b b-b 'B' "$K" day block exceeded),$(b b-c 'C' '"global"' day block exceeded)")" $NOW '{"keyId":"k1"}' '{"action":"refuse","budget":"b-b","message":"budget \"B\" is used up for this day"}'
fx unchecked-no-snapshot null $NOW '{"keyId":"k1"}' '{"action":"unchecked"}'
fx unchecked-stale "$(snap "$(b b-global 'All AI' '"global"' month block ok)")" 1767225605001 '{"keyId":"k1"}' '{"action":"unchecked"}'
fx pass-at-exactly-5000ms "$(snap "$(b b-global 'All AI' '"global"' month block ok)")" 1767225605000 '{"keyId":"k1"}' '{"action":"pass"}'
fx refuse-stale-but-exceeded "$(snap "$G")" 1767225699000 '{"keyId":"k1"}' '{"action":"refuse","budget":"b-global","message":"budget \"All AI\" is used up for this month"}'
ls $D | wc -l
```
Expected: `24`. Every file name starts with the action it expects (`pass-`, `refuse-`, `unchecked-`).

- [ ] **Step 2: Write the failing Go and JS budget tests**

`gw/ipc/budget_test.go`:
```go
package ipc

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type budgetFixture struct {
	Snapshot json.RawMessage `json:"snapshot"`
	Now      int64           `json:"now"`
	Input    CheckInput      `json:"input"`
	Expect   Decision        `json:"expect"`
}

// ipc/v1/fixtures/budget-decide: the same decisions as lib/cost/guard.js (test/ipc-budget.test.js runs them there).
func TestBudgetDecideFixtures(t *testing.T) {
	dir := filepath.Join(fixtureRoot, "budget-decide")
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 20 {
		t.Fatalf("only %d budget fixtures", len(files))
	}
	for _, f := range files {
		t.Run(strings.TrimSuffix(f.Name(), ".json"), func(t *testing.T) {
			data, _ := os.ReadFile(filepath.Join(dir, f.Name()))
			dec := json.NewDecoder(bytes.NewReader(data))
			dec.DisallowUnknownFields()
			var fx budgetFixture
			if err := dec.Decode(&fx); err != nil {
				t.Fatal(err)
			}
			var snap *BudgetSnapshot
			if string(fx.Snapshot) != "null" {
				snap = new(BudgetSnapshot)
				if err := decodeBody(fx.Snapshot, snap, true, "snapshot"); err != nil {
					t.Fatal(err)
				}
			}
			if got := Decide(snap, fx.Now, StaleAfterMs, fx.Input); got != fx.Expect {
				t.Fatalf("got %+v, want %+v", got, fx.Expect)
			}
			if !strings.HasPrefix(f.Name(), fx.Expect.Action+"-") {
				t.Fatalf("the file name starts with the expected action %q", fx.Expect.Action)
			}
		})
	}
}
```

`test/ipc-budget.test.js`:
```js
// The budget decision of the IPC (P3 T5): ipc/v1/fixtures/budget-decide decided by lib/ipc/budget.js, which asks the
// product's own guard (lib/cost/guard.js). gw/ipc/budget_test.go runs the same files against the Go decision.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const { decide, STALE_AFTER_MS } = require('../lib/ipc/budget');
const { DIR, validate, schemas } = require('../lib/ipc/schema');

const FIX = path.join(DIR, 'fixtures', 'budget-decide');

test('the fixtures cover pass, refuse and unchecked, and every snapshot is a valid budget.snapshot body', () => {
  const files = fs.readdirSync(FIX);
  assert.ok(files.length >= 20, `${files.length} fixtures`);
  for (const action of ['pass', 'refuse', 'unchecked'])
    assert.ok(
      files.some((f) => f.startsWith(`${action}-`)),
      action,
    );
  for (const f of files) {
    const fx = JSON.parse(fs.readFileSync(path.join(FIX, f), 'utf8'));
    assert.deepEqual(Object.keys(fx).sort(), ['expect', 'input', 'now', 'snapshot']);
    if (fx.snapshot) assert.deepEqual(validate(schemas()['budget.snapshot'], fx.snapshot), [], f);
  }
  assert.equal(STALE_AFTER_MS, 5000);
});

for (const f of fs.readdirSync(FIX)) {
  test(`budget-decide/${f}`, () => {
    const fx = JSON.parse(fs.readFileSync(path.join(FIX, f), 'utf8'));
    assert.deepEqual(decide(fx.snapshot, fx.now, fx.input), fx.expect);
    assert.ok(f.startsWith(`${fx.expect.action}-`), 'the file name starts with the expected action');
  });
}
```

Run: `GOTOOLCHAIN=local go -C gw test ./... 2>&1 | head -2; node --test test/ipc-budget.test.js 2>&1 | tail -2`
Expected: Go `undefined: Decide`; Node `Cannot find module '../lib/ipc/budget'`.

- [ ] **Step 3: Write `gw/ipc/budget.go` and `lib/ipc/budget.js`**

`gw/ipc/budget.go`:
```go
package ipc

// The gateway's budget check against the newest budget.snapshot (P3 D3), the same rules as lib/cost/guard.js:
// a budget applies when its enforce is "block" and its scope matches the call; the first applicable budget whose
// state is "exceeded" refuses the call (402 insufficient_quota with Message). Budgets fail open (owner decision
// 2026-11-11): with no snapshot, or one older than staleMs, a call no exceeded budget refuses passes "unchecked" and
// its usage record says so. A budget the newest snapshot shows as exceeded is refused even when the snapshot is stale.
// Shared decisions: ipc/v1/fixtures/budget-decide (budget_test.go, test/ipc-budget.test.js).

// StaleAfterMs: a snapshot older than this (now - at > StaleAfterMs) no longer checks a call.
const StaleAfterMs int64 = 5000

// Decision actions.
const (
	Pass      = "pass"
	Refuse    = "refuse"
	Unchecked = "unchecked"
)

// CheckInput is what the gateway knows about a call before it goes to a route target. Model is already normalized
// (lib/pricing.js normalizeModel: no provider path, lower case); P4 ports the normalizer with its own fixtures.
type CheckInput struct {
	KeyID    string `json:"keyId,omitzero"`
	Model    string `json:"model,omitzero"`
	Provider string `json:"provider,omitzero"`
	Agent    string `json:"agent,omitzero"`
	RunID    string `json:"runId,omitzero"`
}

// Decision is the outcome; Budget and Message are set on a refusal.
type Decision struct {
	Action  string `json:"action"`
	Budget  string `json:"budget,omitzero"`
	Message string `json:"message,omitzero"`
}

// Decide checks one call against snapshot s (nil: none yet) at time now (epoch ms).
func Decide(s *BudgetSnapshot, now, staleMs int64, in CheckInput) Decision {
	if s == nil {
		return Decision{Action: Unchecked}
	}
	keyID := in.KeyID
	if keyID == "dashboard" {
		keyID = ""
	}
	source := "gateway"
	switch {
	case in.RunID != "":
		source = "collab"
	case in.Agent == "judge":
		source = "judge"
	case in.Agent == "replay":
		source = "replay"
	}
	for _, b := range s.Budgets {
		if applies(b, keyID, in, source, s.KeyCenter) && b.State == "exceeded" {
			return Decision{Action: Refuse, Budget: b.ID, Message: "budget \"" + b.Name + "\" is used up for this " + b.Period}
		}
	}
	if now-s.At > staleMs {
		return Decision{Action: Unchecked}
	}
	return Decision{Action: Pass}
}

func applies(b BudgetView, keyID string, in CheckInput, source string, centers map[string]string) bool {
	if b.Enforce != "block" {
		return false
	}
	if b.Scope.Global {
		return true
	}
	switch b.Scope.Dim {
	case "key":
		return keyID != "" && keyID == b.Scope.Value
	case "center":
		c, ok := centers[keyID]
		return keyID != "" && ok && c == b.Scope.Value
	case "model":
		return in.Model != "" && in.Model == b.Scope.Value
	case "provider":
		return in.Provider != "" && in.Provider == b.Scope.Value
	case "source":
		return keyID != "" && b.Scope.Value == "gateway" && source == "gateway"
	}
	return false
}
```

`lib/ipc/budget.js` (the product's guard behind a snapshot-backed service, so Go is checked against the code that ships):
```js
// The control plane's twin of the gateway's budget decision (gw/ipc/budget.go): the product's own guard
// (lib/cost/guard.js) asked against a budget.snapshot instead of the live budget service, plus the snapshot's
// staleness. With no snapshot, or one older than staleMs (now - at > staleMs), a call that no exceeded budget refuses
// is 'unchecked'; a budget the snapshot shows as exceeded is still refused. The shared fixtures in
// ipc/v1/fixtures/budget-decide run here and in Go, so the gateway decides as the code that ships today.
const { createGuard } = require('../cost/guard');

const STALE_AFTER_MS = 5000;

/**
 * @param {{ at: number, budgets: import('../types').BudgetView[], keyCenter: Record<string, string> } | null} snapshot
 * @param {number} now epoch ms
 * @param {import('../types').BudgetCheckInput} input
 * @param {number} [staleMs]
 * @returns {{ action: 'pass' | 'refuse' | 'unchecked', budget?: string, message?: string }}
 */
function decide(snapshot, now, input = {}, staleMs = STALE_AFTER_MS) {
  if (!snapshot) return { action: 'unchecked' };
  const centers = snapshot.keyCenter || {};
  const guard = createGuard({
    service: {
      list: () => ({ budgets: snapshot.budgets }),
      evaluateOne: (b) => b, // a snapshot's budgets are evaluated views already
      keyCenter: (keyId) => (Object.hasOwn(centers, keyId) ? centers[keyId] : undefined),
      reportGuard() {},
    },
    now: () => now,
  });
  const r = guard.check(input);
  if (r) return { action: 'refuse', budget: r.budget, message: r.message };
  return now - snapshot.at > staleMs ? { action: 'unchecked' } : { action: 'pass' };
}

module.exports = { decide, STALE_AFTER_MS };
```

Run: `gofmt -w gw && GOTOOLCHAIN=local go -C gw test -count=1 -run TestBudgetDecideFixtures -v ./ipc 2>&1 | grep -c -- "--- PASS"; npx --yes prettier@3 --write lib/ipc/budget.js test/ipc-budget.test.js && node --test test/ipc-budget.test.js 2>&1 | grep -E "^ℹ (pass|fail)"`
Expected: Go `25` (24 fixtures and the parent); Node `ℹ pass 25`, `ℹ fail 0`.

- [ ] **Step 4: The stub checks budgets on what it emits, and can answer a snapshot with health**

Apply (as in Task 4 Step 4, `git apply`):
```diff
diff --git a/gw/cmd/zaafpulse-gw/main.go b/gw/cmd/zaafpulse-gw/main.go
--- a/gw/cmd/zaafpulse-gw/main.go
+++ b/gw/cmd/zaafpulse-gw/main.go
@@ -43,6 +43,8 @@ type options struct {
 	emitEveryMs       int
 	emitBase          int64
 	segmentEvents     int
+	staleMs           int64
+	healthOnSnapshot  bool
 }
 
 func main() {
@@ -67,6 +69,8 @@ func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
 	fs.IntVar(&o.emitEveryMs, "emit-every-ms", 0, "test only: pause between synthetic events, in ms")
 	fs.Int64Var(&o.emitBase, "emit-base", ipc.DefaultEmitBase, "test only: ts of the first synthetic round")
 	fs.IntVar(&o.segmentEvents, "segment-events", 0, "test only: events per outbox segment (default 1000)")
+	fs.Int64Var(&o.staleMs, "stale-ms", ipc.StaleAfterMs, "test only: age at which a budget snapshot is stale, in ms")
+	fs.BoolVar(&o.healthOnSnapshot, "health-on-snapshot", false, "test only: send health after every budget snapshot")
 	if err := fs.Parse(args); err != nil {
 		return 2
 	}
@@ -212,6 +216,9 @@ func (g *gateway) handle(msg ipc.Message) int {
 		g.mu.Lock()
 		g.snap, g.snapAt = b, time.Now()
 		g.mu.Unlock()
+		if g.o.healthOnSnapshot {
+			g.sendHealth()
+		}
 		g.maybeReady()
 	case *ipc.Ack:
 		if b.Stream != g.box.Stream() {
@@ -293,6 +300,7 @@ func (g *gateway) startEmitter() {
 			default:
 			}
 			typ, body := ipc.Synthetic(g.box.Next(), g.o.emitBase)
+			g.checkBudget(body)
 			if err := g.appendAndSend(typ, body); err != nil {
 				g.log.Printf("emit: %v", err)
 				return
@@ -309,6 +317,27 @@ func (g *gateway) startEmitter() {
 	}()
 }
 
+// checkBudget applies the budget decision to a synthetic call, as the gateway will before a call goes upstream
+// (P4): refused, the record is a 402 with the guard's message and no cost; unchecked, it carries budget "unchecked".
+func (g *gateway) checkBudget(body any) {
+	r, ok := body.(*ipc.UsageReport)
+	if !ok {
+		return
+	}
+	g.mu.Lock()
+	snap := g.snap
+	g.mu.Unlock()
+	// "stub/model-a" normalized as lib/pricing.js normalizeModel does.
+	in := ipc.CheckInput{KeyID: r.Record.KeyID, Model: "model-a", Provider: r.Record.Provider}
+	switch d := ipc.Decide(snap, time.Now().UnixMilli(), g.o.staleMs, in); d.Action {
+	case ipc.Unchecked:
+		u := ipc.Unchecked
+		r.Budget = &u
+	case ipc.Refuse:
+		r.Record.Status, r.Record.Error, r.Record.Cost = 402, &d.Message, 0
+	}
+}
+
 // appendAndSend writes an event to the outbox, then sends it (D4: never the other way round).
 func (g *gateway) appendAndSend(typ string, body any) error {
 	g.sendMu.Lock()
```

Run: `gofmt -w gw && test -z "$(gofmt -l gw)" && GOTOOLCHAIN=local go -C gw vet ./... && echo ok`
Expected: `ok`.

- [ ] **Step 5: Write the failing supervisor tests**

`test/ipc-supervisor.test.js`:
```js
// lib/ipc/supervisor.js against the Go stub (P3 T5): start, snapshots, reload and shutdown; a gateway that hangs, one
// that crashes (restarted with doubling back-off), one that writes a bad line, and one that ignores shutdown; metered
// calls that pass without a budget check are counted and alerted.
const test = require('node:test');
const assert = require('node:assert/strict');
const path = require('path');
const { createSupervisor } = require('../lib/ipc/supervisor');
const { stubBin, initial, waitFor, tmpDir } = require('./ipc-stub');

function supervise(t, bin, extraArgs = [], opts = {}) {
  const root = tmpDir(t, 'sup');
  const logs = [];
  const first = initial();
  const sup = createSupervisor({
    bin,
    args: ['--outbox', path.join(root, 'gw-outbox'), '--health-ms', '50', ...extraArgs],
    cpVersion: 'test',
    sources: {
      config: () => first.config, // the supervisor sets gen
      secrets: () => first.secrets,
      snapshot: () => ({ budgets: [], keyCenter: {} }),
    },
    log: (l) => logs.push(l),
    healthTimeoutMs: 1000,
    snapshotEveryMs: 100,
    backoff: { firstMs: 50, maxMs: 200, resetAfterMs: 60000 },
    killAfterMs: 300,
    ...opts,
  });
  t.after(() => sup.stop(0));
  return { sup, logs };
}

test('start, ready, snapshots every interval, reload, stop: exit 0', async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  const { sup } = supervise(t, bin);
  sup.start();
  await waitFor(() => sup.status().state === 'ready', 5000, 'ready');
  assert.equal(sup.status().gen, 1);
  await waitFor(() => sup.status().lastHealth && sup.status().lastHealth.gen === 1, 5000, 'health');
  await new Promise((r) => setTimeout(r, 400));
  assert.ok(sup.status().lastHealth.snapshotAgeMs < 300, `snapshot age ${sup.status().lastHealth.snapshotAgeMs}`);
  assert.equal(sup.reload(), true);
  await waitFor(() => sup.status().lastHealth.gen === 2, 5000, 'health of generation 2');
  const exit = await sup.stop(1000);
  assert.deepEqual([exit.code, exit.signal], [0, null]);
  assert.equal(sup.status().state, 'stopped');
  assert.equal(sup.status().restarts, 0);
});

test('a gateway that stops sending health is killed and restarted', async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  const { sup, logs } = supervise(t, bin, ['--hang-after-ready'], { healthTimeoutMs: 300 });
  sup.start();
  await waitFor(() => sup.status().restarts >= 1, 5000, 'a restart');
  assert.ok(
    logs.some((l) => l.startsWith('gw: no health for 300 ms')),
    logs.join('\n'),
  );
});

test('a gateway that crashes is restarted with back-off 50, 100, 200, 200 ms', async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  const { sup, logs } = supervise(t, bin, ['--crash-after-ready-ms', '20']);
  sup.start();
  await waitFor(() => sup.status().restarts >= 4, 10000, 'four restarts');
  const delays = logs
    .map((l) => l.match(/restarting in (\d+) ms$/))
    .filter(Boolean)
    .map((m) => Number(m[1]));
  assert.deepEqual(delays.slice(0, 4), [50, 100, 200, 200]);
  assert.equal(sup.status().lastExit.code, 3);
});

test('a bad line from the gateway is a protocol error: killed and restarted', async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  const { sup, logs } = supervise(t, bin, ['--bad-line']);
  sup.start();
  await waitFor(() => sup.status().restarts >= 1, 5000, 'a restart');
  assert.ok(logs.includes('gw: protocol error: a line is not JSON'), logs.join('\n'));
});

test('a gateway that ignores shutdown is killed after graceMs + killAfterMs', async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  const { sup } = supervise(t, bin, ['--ignore-shutdown']);
  sup.start();
  await waitFor(() => sup.status().state === 'ready', 5000, 'ready');
  const started = Date.now();
  const exit = await sup.stop(100);
  const took = Date.now() - started;
  assert.notEqual(exit.code, 0);
  assert.ok(took >= 350 && took < 3000, `killed after ${took} ms`);
  assert.equal(sup.status().restarts, 0, 'a stop is not restarted');
});

test('metered calls without a budget check are counted, alerted once, and stored with budget "unchecked"', async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  const stored = [];
  const { sup, logs } = supervise(t, bin, ['--emit', '3', '--stale-ms', '-1'], {
    ingest: { handle: (msg) => stored.push(msg) },
  });
  sup.start();
  await waitFor(() => sup.status().unchecked.count === 3, 5000, 'three unchecked calls');
  assert.equal(sup.status().alert, true);
  assert.equal(logs.filter((l) => l.startsWith('gw: alert:')).length, 1);
  const usage = stored.filter((m) => m.type === 'usage.report');
  assert.deepEqual(
    usage.map((m) => m.body.budget),
    ['unchecked', 'unchecked', 'unchecked'],
  );
});
```

Run: `node --test test/ipc-supervisor.test.js 2>&1 | tail -2`
Expected: FAIL: `Cannot find module '../lib/ipc/supervisor'`.

- [ ] **Step 6: Write `lib/ipc/supervisor.js`**

```js
// The control plane's supervisor of the gateway process (P3 D5). It starts a session (lib/ipc/session.js) with the
// first config, secrets and budget snapshot, pushes a snapshot every snapshotEveryMs, and restarts the gateway with
// back-off (firstMs, doubling, at most maxMs; back to firstMs after resetAfterMs ready) when it exits, sends no health
// for healthTimeoutMs, or breaks the protocol (the session kills it). reload() pushes a new config generation and its
// secrets; stop() sends shutdown and kills after graceMs + killAfterMs. Usage that passed without a budget check
// (budget: 'unchecked') is counted, and logged once a minute as an alert; status() reports it with the restarts.
// P3: used by the tests and scripts/gw-dev.js only. server.js does not start it (P4 S1 does, behind gateway.engine).
const { startSession } = require('./session');

const ALERT_EVERY_MS = 60000;

/**
 * @param {{ bin: string, args?: string[], env?: Record<string, string | undefined>, cpVersion: string,
 *   sources: { config: () => object, secrets: () => object, snapshot: () => object },
 *   ingest?: { handle: (msg: any, stream: string) => unknown } | null, log?: (line: string) => void,
 *   now?: () => number, healthTimeoutMs?: number, snapshotEveryMs?: number,
 *   backoff?: { firstMs?: number, maxMs?: number, resetAfterMs?: number }, killAfterMs?: number,
 *   start?: typeof startSession }} opts
 * sources return bodies without gen (config, secrets) or at (snapshot): the supervisor adds them.
 */
function createSupervisor({
  bin,
  args = [],
  env = process.env,
  cpVersion,
  sources,
  ingest = null,
  log = console.error,
  now = Date.now,
  healthTimeoutMs = 5000,
  snapshotEveryMs = 1000,
  backoff = {},
  killAfterMs = 2000,
  start = startSession,
}) {
  const firstMs = backoff.firstMs ?? 1000;
  const maxMs = backoff.maxMs ?? 30000;
  const resetAfterMs = backoff.resetAfterMs ?? 60000;
  let session = null;
  let ready = false;
  let stopped = true;
  let gen = 0;
  let restarts = 0;
  let delay = firstMs;
  let lastExit = null;
  let lastHealth = null;
  let healthTimer = null;
  let snapTimer = null;
  let resetTimer = null;
  let retryTimer = null;
  const unchecked = { count: 0, lastAt: null, alertedAt: -Infinity };

  const configBody = () => ({ ...sources.config(), gen });
  const secretsBody = () => ({ ...sources.secrets(), gen });
  const snapshotBody = () => ({ ...sources.snapshot(), at: now() });

  function clearTimers() {
    for (const t of [healthTimer, resetTimer, retryTimer]) clearTimeout(t);
    clearInterval(snapTimer);
    healthTimer = resetTimer = retryTimer = snapTimer = null;
  }
  function armHealth(s) {
    clearTimeout(healthTimer);
    healthTimer = setTimeout(() => {
      if (session !== s) return;
      log(`gw: no health for ${healthTimeoutMs} ms: killing the gateway`);
      s.kill();
    }, healthTimeoutMs);
  }
  function noteUnchecked() {
    unchecked.count++;
    unchecked.lastAt = now();
    if (unchecked.lastAt - unchecked.alertedAt >= ALERT_EVERY_MS) {
      unchecked.alertedAt = unchecked.lastAt;
      log(`gw: alert: ${unchecked.count} metered calls passed without a budget check (the budget snapshot was stale)`);
    }
  }

  function launch() {
    gen += 1;
    ready = false;
    const s = start({
      bin,
      args,
      env,
      cpVersion,
      log,
      now,
      killAfterMs,
      initial: { config: configBody(), secrets: secretsBody(), snapshot: snapshotBody() },
      onEvent: (msg, stream) => {
        if (ingest) ingest.handle(msg, stream);
        if (msg.type === 'usage.report' && msg.body.budget === 'unchecked') noteUnchecked();
      },
      onHealth: (h) => {
        lastHealth = { ...h, at: now() };
        armHealth(s);
      },
    });
    session = s;
    armHealth(s); // no health at all within healthTimeoutMs of the start is a hang too
    s.ready.then(
      () => {
        if (session !== s) return;
        ready = true;
        resetTimer = setTimeout(() => (delay = firstMs), resetAfterMs);
        snapTimer = setInterval(pushSnapshot, snapshotEveryMs);
      },
      () => {},
    );
    s.exited.then(({ code, signal }) => {
      if (session !== s) return;
      clearTimers();
      session = null;
      ready = false;
      lastExit = { code, signal, at: now() };
      if (stopped) return;
      restarts++;
      log(`gw: the gateway exited (code ${code}, signal ${signal}); restarting in ${delay} ms`);
      retryTimer = setTimeout(() => {
        retryTimer = null;
        if (!stopped) launch();
      }, delay);
      delay = Math.min(delay * 2, maxMs);
    });
  }

  function pushSnapshot() {
    if (session && ready) session.send('budget.snapshot', snapshotBody());
  }

  return {
    start() {
      if (!stopped) return;
      stopped = false;
      launch();
    },
    /** config.json, a setting or a key changed: a new generation of config and its secrets. */
    reload() {
      if (!session || !ready) return false;
      gen += 1;
      session.send('config.push', configBody());
      session.send('secrets.handoff', secretsBody());
      return true;
    },
    pushSnapshot,
    /** Asks the gateway to stop; resolves with its exit once it is gone (killed after graceMs + killAfterMs). */
    async stop(graceMs = 5000) {
      stopped = true;
      clearTimers();
      const s = session;
      if (!s) return lastExit;
      return s.shutdown(graceMs);
    },
    status() {
      return {
        state: session ? (ready ? 'ready' : 'starting') : stopped ? 'stopped' : 'restarting',
        pid: session ? session.pid : null,
        gen,
        restarts,
        lastExit,
        lastHealth,
        unchecked: { count: unchecked.count, lastAt: unchecked.lastAt },
        alert: unchecked.lastAt !== null && now() - unchecked.lastAt < ALERT_EVERY_MS,
      };
    },
  };
}

module.exports = { createSupervisor };
```

- [ ] **Step 7: Run the supervisor tests on Node and Deno**

Run: `npx --yes prettier@3 --write lib/ipc/supervisor.js test/ipc-supervisor.test.js && node --test test/ipc-supervisor.test.js 2>&1 | grep -E "^ℹ (pass|fail)" && deno test -q --no-check --no-prompt -P=test test/ipc-supervisor.test.js 2>&1 | tail -1`
Expected: Node `ℹ pass 6`, `ℹ fail 0`; Deno `ok | 6 passed | 0 failed`. Run the Node command three times: no flake (measured: 3 of 3 green for this plan).

- [ ] **Step 8: The dev command**

`scripts/gw-dev.js`:
```js
// Dev command (P3 D5): builds the gateway stub, supervises it on a throwaway data folder and prints its health, its
// events and its log until Ctrl-C (then shutdown). Not used by the server.
//   node scripts/gw-dev.js [--emit N]
//   deno run -q --no-prompt --allow-read --allow-write --allow-run --allow-env scripts/gw-dev.js [--emit N]
const fs = require('fs');
const os = require('os');
const path = require('path');
const { buildStub } = require('./ipc/build-stub');
const { createSupervisor } = require('../lib/ipc/supervisor');
const { createIngest } = require('../lib/ipc/ingest');
const { openLocalStores } = require('../lib/ipc/local');
const { recoverMarks } = require('../lib/ipc/marks');
const { DIR } = require('../lib/ipc/schema');

const built = buildStub();
if (built.skip) {
  console.error(built.skip);
  process.exit(1);
}
const root = fs.mkdtempSync(path.join(os.tmpdir(), 'zp-gw-dev-'));
const stores = openLocalStores({ root });
const ingest = createIngest({ ...stores.sinks, marks: recoverMarks(stores.files) });
const body = (rel) => JSON.parse(fs.readFileSync(path.join(DIR, 'fixtures', rel), 'utf8')).body;
const emit = process.argv.indexOf('--emit');
const sup = createSupervisor({
  bin: built.bin,
  args: [
    '--outbox',
    stores.files.outboxDir,
    ...(emit > 0 ? ['--emit', process.argv[emit + 1], '--emit-every-ms', '200'] : []),
  ],
  cpVersion: 'gw-dev',
  sources: {
    config: () => body('config.push/valid-full.json'),
    secrets: () => body('secrets.handoff/valid.json'),
    snapshot: () => ({ budgets: body('budget.snapshot/valid.json').budgets, keyCenter: { k1: 'platform' } }),
  },
  ingest: {
    handle: (msg, stream) => {
      const stored = ingest.handle(msg, stream);
      console.log(`${stored ? 'stored' : 'duplicate'} ${msg.type} #${msg.seq}`);
    },
  },
  log: (l) => console.log(l),
});
console.log(`data folder: ${root}`);
sup.start();
const timer = setInterval(() => console.log(JSON.stringify(sup.status())), 5000);
process.on('SIGINT', async () => {
  clearInterval(timer);
  console.log(JSON.stringify(await sup.stop(2000)));
  process.exit(0);
});
```

In `package.json` `scripts`, after `"test:go"`, add `"gw:dev": "node scripts/gw-dev.js",`.

Run: `npx --yes prettier@3 --write scripts/gw-dev.js && (node scripts/gw-dev.js --emit 2 > /tmp/p3-gwdev.log 2>&1 & P=$!; sleep 3; kill -INT $P; sleep 1); cat /tmp/p3-gwdev.log`
Expected (measured):
```
data folder: /var/folders/…/zp-gw-dev-…
gw: ready (config generation 1)
stored usage.report #1
stored audit.event #2
stored trace.span #3
stored usage.report #4
stored audit.event #5
stored trace.span #6
gw: emitted every synthetic event up to seq 6
gw: shutting down (grace 2s)
{"code":0,"signal":null}
```

- [ ] **Step 9: Run the three suites**

Run: `npm run test:go && node --test test/*.test.js 2>&1 | grep -E "^ℹ fail" && deno test -q --no-check --no-prompt -P=test test/*.test.js 2>&1 | tail -1 && git diff --stat -- server.js`
Expected: Go `ok`; `ℹ fail 0`; Deno `0 failed`; no change to `server.js`.

- [ ] **Step 10: Commit**

```bash
git add ipc/v1/fixtures/budget-decide gw lib/ipc scripts/gw-dev.js test/ipc-budget.test.js test/ipc-supervisor.test.js package.json
git commit -m "Add the gateway supervisor and the fail-open budget decision with shared fixtures in Go and JS"
git push origin migrate/p3-ipc-contract
```

---

### Task 6: Kill tests (spec T6)

**Files:**
- Create: `test/fixtures/ipc/cp-child.js`
- Test: `test/ipc-kill.test.js`

**Interfaces:**
- Consumes: `startSession` (Task 3), `openLocalStores`, `createIngest`, `recoverMarks` (Task 4), `test/ipc-stub.js` `stubBin`, `initial`, `killHard`, `alive`, `sleep`, `waitFor`, `tmpDir`; the stub's `--emit`, `--emit-every-ms`, `--segment-events`.
- Produces: `ZP_KILL_SEED=<n>` replays the random kill points of a run (every assertion message names its seed).

- [ ] **Step 1: Write the control-plane child**

`test/fixtures/ipc/cp-child.js`:
```js
// The control plane of the kill test (test/ipc-kill.test.js), run as a child process so the test can SIGKILL it: it
// opens the stores of <data folder> like the server, recovers its marks, starts the stub with --emit, stores the
// events, and once every event of the run is stored shuts the stub down (which acknowledges them) and exits 0 after
// printing "done". It writes the stub's pid to <data folder>/gw.pid, so the test can wait for the stub to exit too.
// argv: <data folder> <stub binary> <rounds>
const fs = require('fs');
const path = require('path');
const { openLocalStores } = require('../../../lib/ipc/local');
const { createIngest } = require('../../../lib/ipc/ingest');
const { recoverMarks } = require('../../../lib/ipc/marks');
const { startSession } = require('../../../lib/ipc/session');

const [root, bin, rounds] = process.argv.slice(2);
const total = 3 * Number(rounds);
const stores = openLocalStores({ root });
const ingest = createIngest({ ...stores.sinks, marks: recoverMarks(stores.files) });
const initial = JSON.parse(fs.readFileSync(path.join(root, 'initial.json'), 'utf8'));
initial.snapshot.at = Date.now();
let finishing = false;
const s = startSession({
  bin,
  args: [
    '--outbox',
    stores.files.outboxDir,
    '--emit',
    String(rounds),
    '--emit-every-ms',
    '1',
    '--segment-events',
    '25',
  ],
  cpVersion: 'kill-test',
  initial,
  log: () => {},
  onEvent: (msg, stream) => {
    ingest.handle(msg, stream);
    maybeDone();
  },
});
fs.writeFileSync(path.join(root, 'gw.pid'), String(s.pid));
s.ready.then(maybeDone, () => {});
s.exited.then(() => {
  if (!finishing) process.exit(1);
});
function maybeDone() {
  if (finishing || !s.stream || ingest.mark(s.stream) < total) return;
  finishing = true;
  s.shutdown(2000).then(({ code }) => {
    process.stdout.write(`done ${code}\n`);
    process.exit(0);
  });
}
```

- [ ] **Step 2: Write the kill tests**

`test/ipc-kill.test.js`:
```js
// Kill tests (P3 T6): SIGKILL (taskkill /F on Windows) the gateway stub at random points of an --emit stream and
// restart it, 20 runs; then SIGKILL the control-plane side (a child process running test/fixtures/ipc/cp-child.js
// under this runtime) and restart it. After each run: every usage line stored exactly once (no loss, no duplicate),
// the governance chain verifies, the MCP audit log and the trace store hold each event once, and the outbox is empty.
// The random points come from a seeded generator: ZP_KILL_SEED=<n> replays a run; every failure names its seed.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const { spawn } = require('child_process');
const { openLocalStores } = require('../lib/ipc/local');
const { createIngest } = require('../lib/ipc/ingest');
const { startSession } = require('../lib/ipc/session');
const { stubBin, initial, killHard, alive, sleep, waitFor, tmpDir } = require('./ipc-stub');

const ROUNDS = 40; // 120 events a run
const TOTAL = 3 * ROUNDS;
const SEED = Number(process.env.ZP_KILL_SEED) || Date.now() % 1e9;

// mulberry32: a small seeded generator, so a failing run can be replayed.
function random(seed) {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

const readJsonl = (file) =>
  fs.existsSync(file)
    ? fs
        .readFileSync(file, 'utf8')
        .split('\n')
        .filter(Boolean)
        .map((l) => JSON.parse(l))
    : [];
const marked = (list) => list.filter((e) => e.ipc);
const seqs = (list) => marked(list).map((e) => e.ipc.seq);
const expected = (offset, every = 3) => Array.from({ length: TOTAL / every }, (_, i) => every * i + offset);

// What a finished run must look like, whatever was killed and when.
function checkRun(root, label) {
  const stores = openLocalStores({ root });
  const usage = readJsonl(stores.files.usageFile);
  assert.deepEqual(seqs(usage), expected(1), `${label}: each usage line once, in order`);
  const mcp = readJsonl(stores.files.mcpAuditFile);
  assert.deepEqual(seqs(mcp), expected(5, 6), `${label}: each MCP audit entry once`);
  const govDir = stores.files.governanceDir;
  const gov = fs
    .readdirSync(govDir)
    .filter((f) => f.startsWith('governance-'))
    .flatMap((f) => readJsonl(path.join(govDir, f)));
  assert.deepEqual(seqs(gov), expected(2, 6), `${label}: each governance entry once`);
  const verify = stores.governance.verify();
  assert.equal(verify.ok, true, `${label}: the audit chain verifies: ${JSON.stringify(verify)}`);
  const spans = fs
    .readdirSync(stores.files.traceDir)
    .filter((f) => f.endsWith('.ndjson'))
    .flatMap((f) => readJsonl(path.join(stores.files.traceDir, f)));
  assert.deepEqual(seqs(spans), expected(3), `${label}: each span once`);
  assert.deepEqual(
    fs.readdirSync(stores.files.outboxDir).filter((f) => f.endsWith('.jsonl')),
    [],
    `${label}: the outbox is empty after the drain`,
  );
}

test(`SIGKILL the gateway at random points, 20 runs (seed ${SEED})`, async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  const rand = random(SEED);
  for (let run = 0; run < 20; run++) {
    const label = `seed ${SEED}, run ${run}`;
    const root = tmpDir(t, `kill-gw-${run}`);
    const stores = openLocalStores({ root });
    const ingest = createIngest(stores.sinks);
    let stream = null;
    let kills = 0;
    const args = ['--outbox', stores.files.outboxDir, '--emit', String(ROUNDS), '--emit-every-ms', '1'];
    while (!stream || ingest.mark(stream) < TOTAL) {
      const s = startSession({
        bin,
        args: [...args, '--segment-events', '25'],
        cpVersion: 'kill-test',
        initial: initial(),
        log: () => {},
        onEvent: (msg, st) => ingest.handle(msg, st),
      });
      await Promise.race([sleep(Math.floor(rand() * 250)), s.exited]);
      stream = s.stream || stream;
      killHard(s.pid);
      await s.exited;
      kills++;
      assert.ok(kills < 100, `${label}: the stream never completed`);
    }
    // A last, clean run acknowledges everything: the outbox drains.
    const s = startSession({
      bin,
      args,
      cpVersion: 'kill-test',
      initial: initial(),
      log: () => {},
      onEvent: (m, st) => ingest.handle(m, st),
    });
    await s.ready;
    assert.equal((await s.shutdown(2000)).code, 0, label);
    checkRun(root, `${label} (${kills} kills)`);
  }
});

function runtimeArgs(script) {
  // Under Deno a child is started with `deno run` and the test set, never as a bare script (which would hold every
  // permission, P2 finding F10).
  return process.versions.deno
    ? [process.execPath, ['run', '-q', '--no-check', '--no-prompt', '-P=test', script]]
    : [process.execPath, [script]];
}

test(`SIGKILL the control plane at random points, 5 runs (seed ${SEED})`, async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  const rand = random(SEED + 1);
  const script = path.join(__dirname, 'fixtures', 'ipc', 'cp-child.js');
  const [cmd, base] = runtimeArgs(script);
  for (let run = 0; run < 5; run++) {
    const label = `seed ${SEED + 1}, control plane run ${run}`;
    const root = tmpDir(t, `kill-cp-${run}`);
    fs.writeFileSync(path.join(root, 'initial.json'), JSON.stringify(initial()));
    let done = false;
    let kills = 0;
    while (!done) {
      const child = spawn(cmd, [...base, root, bin, String(ROUNDS)], {
        cwd: path.join(__dirname, '..'),
        stdio: ['ignore', 'pipe', 'inherit'],
      });
      let out = '';
      child.stdout.on('data', (c) => (out += c));
      const exited = new Promise((r) => child.once('exit', (code) => r(code)));
      const code = await Promise.race([sleep(150 + Math.floor(rand() * 600)).then(() => null), exited]);
      if (code === null) {
        killHard(child.pid);
        await exited;
        kills++;
      }
      // The stub sees its stdin close and exits; never start a second one on the same outbox while it lives.
      const pidFile = path.join(root, 'gw.pid');
      if (fs.existsSync(pidFile)) {
        const pid = Number(fs.readFileSync(pidFile, 'utf8'));
        await waitFor(() => !alive(pid), 10000, `${label}: the stub ${pid} to exit after its control plane died`);
      }
      done = code === 0 && out.includes('done 0');
      assert.ok(kills < 50, `${label}: the run never completed`);
    }
    checkRun(root, `${label} (${kills} kills)`);
  }
});
```

- [ ] **Step 3: Run them on Node and Deno, several times**

Run: `npx --yes prettier@3 --write test/fixtures/ipc/cp-child.js test/ipc-kill.test.js && for i in 1 2 3 4 5; do node --test test/ipc-kill.test.js 2>&1 | grep -E "^ℹ fail|seed [0-9]+, "; done; deno test -q --no-check --no-prompt -P=test test/ipc-kill.test.js 2>&1 | tail -1`
Expected: five times `ℹ fail 0`; Deno `ok | 2 passed | 0 failed`. A run takes about 7 s (20 gateway runs, 5 control-plane runs). Measured while writing this plan: 250 consecutive gateway runs with fresh random seeds, 0 failures, after the two outbox fixes of Task 4 Step 2 (before them, 1 run in about 30 lost the order of `stream.json` and segment deletion). If a run fails, replay it with the printed seed (`ZP_KILL_SEED=<seed> node --test test/ipc-kill.test.js`), and fix the code, never the test's expectation.

- [ ] **Step 4: Run the three suites**

Run: `npm run test:go && node --test test/*.test.js 2>&1 | grep -E "^ℹ fail" && deno test -q --no-check --no-prompt -P=test test/*.test.js 2>&1 | tail -1`
Expected: Go `ok`; `ℹ fail 0`; Deno `0 failed`.

- [ ] **Step 5: Commit**

```bash
git add test/fixtures/ipc/cp-child.js test/ipc-kill.test.js
git commit -m "Add kill tests: killing the gateway or the control plane loses and duplicates no event and keeps the audit chain valid"
git push origin migrate/p3-ipc-contract
```

---

### Task 7: Results and docs (spec T7)

**Files:**
- Create: `bench/ipc-latency.js`, `docs/migration/2026-11-11-p3-results.md`
- Modify: `README.md` (after the `### Run on Deno (v2 line)` section), `docs/migration/2026-11-10-deno-go-migration-plan.md` (§5 P3 exit), `bench/README.md` (one line)

**Interfaces:**
- Consumes: everything above.
- Produces: the measured numbers; gate P3 recorded.

- [ ] **Step 1: Write the latency bench**

`bench/ipc-latency.js`:
```js
// IPC v1 over the gateway's stdio pipes (P3 T7): the round trip of one message each way, and the throughput of the
// gateway's event stream into the control plane's stores. Runs under Node and Deno; builds the Go stub (or uses
// ZP_GW_BIN). Prints one JSON object; nothing is written outside a temp folder.
//   node bench/ipc-latency.js [--count 2000] [--events 30000]
// Round trip: a budget.snapshot sent, the health the stub answers with (--health-on-snapshot) received, timed with
// performance.now(), one at a time. Throughput: --emit with no pause, from ready until the last event is stored by the
// real writers (lib/ipc/local.js), acks included.
const fs = require('fs');
const os = require('os');
const path = require('path');
const { buildStub } = require('../scripts/ipc/build-stub');
const { startSession } = require('../lib/ipc/session');
const { createIngest } = require('../lib/ipc/ingest');
const { openLocalStores } = require('../lib/ipc/local');
const { DIR } = require('../lib/ipc/schema');

const arg = (name, dflt) => {
  const i = process.argv.indexOf(`--${name}`);
  return i > 0 ? Number(process.argv[i + 1]) : dflt;
};
const COUNT = arg('count', 2000);
const EVENTS = arg('events', 30000);
const body = (rel) => JSON.parse(fs.readFileSync(path.join(DIR, 'fixtures', rel), 'utf8')).body;
const initial = () => ({
  config: body('config.push/valid-full.json'),
  secrets: { ...body('secrets.handoff/valid.json'), gen: 3 },
  snapshot: { ...body('budget.snapshot/valid.json'), at: Date.now() },
});
const pct = (sorted, p) => sorted[Math.min(sorted.length - 1, Math.floor((p / 100) * sorted.length))];
const round3 = (x) => Math.round(x * 1000) / 1000;

async function roundTrip(bin, root) {
  let resolveHealth = null;
  const s = startSession({
    bin,
    args: ['--outbox', path.join(root, 'rt-outbox'), '--health-on-snapshot', '--health-ms', '3600000'],
    cpVersion: 'bench',
    initial: initial(),
    log: () => {},
    onHealth: () => resolveHealth && resolveHealth(),
  });
  await s.ready;
  await new Promise((r) => setTimeout(r, 100)); // the health that answers the first snapshot
  const snapshot = body('budget.snapshot/valid.json');
  const times = [];
  for (let i = 0; i < COUNT + 100; i++) {
    const got = new Promise((r) => (resolveHealth = r));
    const t0 = performance.now();
    s.send('budget.snapshot', { ...snapshot, at: Date.now() });
    await got;
    if (i >= 100) times.push(performance.now() - t0); // the first 100 warm up
  }
  await s.shutdown(1000);
  times.sort((a, b) => a - b);
  return { count: COUNT, p50Ms: round3(pct(times, 50)), p95Ms: round3(pct(times, 95)), p99Ms: round3(pct(times, 99)) };
}

async function throughput(bin, root) {
  const stores = openLocalStores({ root: path.join(root, 'tp') });
  const ingest = createIngest(stores.sinks);
  const rounds = Math.ceil(EVENTS / 3);
  let t0 = 0;
  let done;
  const finished = new Promise((r) => (done = r));
  const s = startSession({
    bin,
    args: ['--outbox', stores.files.outboxDir, '--emit', String(rounds)],
    cpVersion: 'bench',
    initial: initial(),
    log: () => {},
    onEvent: (msg, stream) => {
      ingest.handle(msg, stream);
      if (ingest.mark(stream) === 3 * rounds) done(performance.now());
    },
  });
  await s.ready;
  t0 = performance.now();
  const t1 = await finished;
  await s.shutdown(2000);
  const seconds = (t1 - t0) / 1000;
  return { events: 3 * rounds, seconds: round3(seconds), eventsPerSecond: Math.round((3 * rounds) / seconds) };
}

(async () => {
  const built = buildStub();
  if (built.skip) throw new Error(built.skip);
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'zp-ipc-bench-'));
  try {
    const out = {
      runtime: process.versions.deno ? `deno ${process.versions.deno}` : `node ${process.versions.node}`,
      host: `${os.platform()}-${os.arch()}`,
      roundTrip: await roundTrip(built.bin, root),
      throughput: await throughput(built.bin, root),
    };
    console.log(JSON.stringify(out));
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
})().catch((err) => {
  console.error(err.message);
  process.exit(1);
});
```

In `bench/README.md`, after the `## Run it` code block, add the line: ``The IPC between the control plane and the Go gateway (P3) has its own measure: `node bench/ipc-latency.js` (round trip over the stdio pipes and event throughput; see docs/migration/2026-11-11-p3-results.md).``

- [ ] **Step 2: Measure**

On the owner's Mac, on power, other heavy apps closed:
```bash
npx --yes prettier@3 --write bench/ipc-latency.js
node bench/ipc-latency.js
deno run -q --no-prompt --allow-read --allow-write --allow-run --allow-env bench/ipc-latency.js
```
Expected: one JSON line each. Measured while writing this plan (MacBook, darwin-arm64, not on a quiet machine):
```
{"runtime":"node 24.16.0","host":"darwin-arm64","roundTrip":{"count":2000,"p50Ms":0.044,"p95Ms":0.056,"p99Ms":0.135},"throughput":{"events":30000,"seconds":1.27,"eventsPerSecond":23627}}
{"runtime":"deno 2.9.8","host":"darwin-arm64","roundTrip":{"count":2000,"p50Ms":0.04,"p95Ms":0.064,"p99Ms":0.089},"throughput":{"events":30000,"seconds":2.224,"eventsPerSecond":13492}}
```
Record the numbers of this step's run, not these.

- [ ] **Step 3: Write `docs/migration/2026-11-11-p3-results.md`**

Sections, filled with this branch's facts (no placeholders left in the committed file):
1. **Summary**: the v1 contract is frozen in `ipc/v1` (14 message types, the additive-only rule in `ipc/v1/README.md`); stdio transport; `server.js` does not start the gateway.
2. **Messages**: the table of `ipc/v1/README.md` (type, direction, body), and the counts: 14 schemas, 57 message fixtures, 24 budget decisions, all run by JS (Node and Deno) and Go.
3. **Measured latency** (Step 2): round trip p50/p95/p99 on Node and Deno, throughput in events/s; one sentence on what it means against M3 (the gateway's overhead budget is ≤ 5 ms p95: the IPC round trip is two orders of magnitude below, and P4 sends a usage report per call, not a round trip).
4. **Kill tests**: what is killed, how often (20 gateway runs × random points, 5 control-plane runs), on which CI OS (Node job: four OS including `windows-11-arm` with `taskkill /F`; Deno job: Ubuntu and macOS), and the outcome per run from the pull request's CI. The two outbox defects the kill tests found while this plan was written (Task 4 Step 2) and their fixes.
5. **Spec deviations and open points**: the 13 resolutions and 3 corrections at the top of `docs/superpowers/plans/2026-11-11-p3-ipc-contract.md`, one line each; P4 follow-ups: wire the supervisor into `server.js` behind `gateway.engine` with the `runScope` grant of the binary and the dashboard alert; add `ipc/v1` to the `deno compile` include list; port `normalizeModel` with fixtures; a config.json → `config.push` converter (prices to Money); the 4 MiB tail bound.

- [ ] **Step 4: README architecture note**

In `README.md`, after the `### Run on Deno (v2 line)` section and before `### Run it on login (macOS, from source)`, add:

```markdown
### The gateway process (v2 line, in progress)

ZAAFPulse v2 moves the Agent Gateway's data plane into a Go process, `zaafpulse-gw`, started and supervised by the
Deno control plane. The two talk over the gateway's stdin and stdout, one JSON message per line, as frozen in
[`ipc/v1`](ipc/v1/README.md): config, resolved secrets and budget snapshots go in; usage, audit events and trace spans
come out through a durable outbox, so killing either process loses or duplicates no record. The control plane stays
the only writer of the usage log, the cost ledger and the audit chain. Today the Go process is a protocol stub used by
the tests (`npm run test:go`, `npm run gw:dev`); the gateway in the server is still the JavaScript one.
```

- [ ] **Step 5: Tick the P3 exit in the program plan**

In `docs/migration/2026-11-10-deno-go-migration-plan.md` §5 P3, replace the three `**Exit:**` bullets with:
```markdown
- **Exit** (met 2026-11-1x, `docs/migration/2026-11-11-p3-results.md`):
  - [x] schemas frozen at v1 (`ipc/v1`), with the additive-only rule written down;
  - [x] both sides pass the shared fixtures, on Node, Deno and Go, on 4 OS in CI;
  - [x] a kill test: killing either process never corrupts the audit chain or the ledger, and loses or duplicates no
    usage line;
  - [x] no change in product behaviour: `server.js` does not start the gateway.
```
using the real date of the green pull request run. Tick only what the pull request's CI shows green: a box whose evidence is missing stays `[ ]` with the reason after it.

- [ ] **Step 6: Final check**

Run: `npm run test:go && test -z "$(gofmt -l gw)" && node --test test/*.test.js 2>&1 | grep -E "^ℹ (pass|fail)" && deno test -q --no-check --no-prompt -P=test test/*.test.js 2>&1 | tail -1 && npm run test:contract 2>&1 | grep -E "^ℹ fail" && git diff --stat aa78352 -- server.js lib/gateway lib/cost lib/governance lib/audit.js lib/trace`
Expected: Go `ok`; Node `ℹ fail 0`; Deno `0 failed`; contract `ℹ fail 0`; the last command prints nothing (no product file changed in P3).

- [ ] **Step 7: Commit**

```bash
npx --yes prettier@3 --check bench/ipc-latency.js
git add bench/ipc-latency.js bench/README.md docs/migration/2026-11-11-p3-results.md docs/migration/2026-11-10-deno-go-migration-plan.md README.md
git commit -m "Record the P3 results: IPC latency over the pipes, kill-test outcomes, and the gateway process in the README"
git push origin migrate/p3-ipc-contract
```

---

## Gate P3 checklist

- [ ] `ipc/v1` frozen: 14 schemas, the version rule in `ipc/v1/README.md`, `test/ipc-types.test.js` keeps `lib/types.js` in step.
- [ ] Shared fixtures green in JS (Node and Deno) and Go; the `go` job green on 4 OS on the pull request run.
- [ ] Kill tests green on every CI OS that runs them; no lost or duplicated usage line; the audit chain verifies.
- [ ] `server.js` and the product modules unchanged (Task 7 Step 6); the Node, Deno and contract suites green.
- [ ] The P4 follow-ups listed in the results document.

## Self-review (done while writing this plan)

- **Spec coverage:** D1 transport and interface → Tasks 3 (channel, session, Go `Transport`) and 1 (plan correction); D2 framing, 8 MiB, envelope, version rule → Tasks 1, 3; D3 messages → Tasks 1, 2 (schemas, structs), 3 (handshake, health, shutdown), 4 (events, ack), 5 (snapshot staleness, unchecked); D4 outbox, marks in stored lines, tail recovery, dedupe, batched acks, single writer → Tasks 3 (acker), 4; D5 supervisor, back-off, reload, shutdown, dev command → Task 5; D6 schemas as source of truth, validator, strict/lenient Go, fixtures on both sides, types sync → Tasks 1, 2; D7 Go module, stub, `--emit`, CI `go` job, Go 1.27.3 → Tasks 2–5. T1–T7 map 1:1 to Tasks 1–7. Exit bullets → Gate checklist.
- **Placeholders:** none; every code step holds the complete file or an exact diff, and every command its expected output. The results document (Task 7 Step 3) is written from measured numbers, so its numbers are not in this plan.
- **Type consistency:** `startSession` / `createSupervisor` / `createIngest` / `openLocalStores` / `recoverMarks` / `decide` signatures in the Interfaces blocks match their code; Go `OpenOutbox` keeps the Task 3 method set in Task 4; `CheckInput`/`Decision` JSON names match the fixtures; stub flags used by tests (`--outbox`, `--health-ms`, `--protocol`, `--bad-line`, `--hang-after-ready`, `--crash-after-ready-ms`, `--ignore-shutdown`, `--emit`, `--emit-every-ms`, `--segment-events`, `--stale-ms`, `--health-on-snapshot`) are all defined by Tasks 3–5.
- **Measured:** every code block of this plan was run in a scratch copy of the branch at `70c2097` (Node 24.16, Deno 2.9.8, Go 1.27.3, macOS arm64): Task 1–6 tests green on Node and Deno, `go vet`/`go test`/`gofmt` clean, `GOOS=windows GOARCH=arm64 go vet` clean, the full Node suite unchanged apart from a path test that fails only outside a git checkout.
