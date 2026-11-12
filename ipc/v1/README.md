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
| `config.push` | cp → gw | `gen`, `gateway` (no keys, no secret references), `keys` (with `hash`), `pricing` (Money per 1M tokens), `front?` (P4a: `listen`, `tls?`, `upstream`, `engines`, `shadow`) |
| `secrets.handoff` | cp → gw | `gen`, `providers[{id, value?, token?}]`, `mask`, `frontToken?` (P4a: 64 hex) |
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

## The front (added in v1 by P4a, both fields optional)

With `config.push.front` the gateway holds the main port (`listen`, TLS from `tls` when given) and forwards each
route class to the engine `engines` names: `js` is the control plane's internal port `upstream.port` on 127.0.0.1;
`go` is accepted by the schema for P4b and refused by a P4a gateway (exit 2). `shadow` is reserved (empty). Every
request the gateway forwards to the internal port carries the header
`X-ZP-Client: <client-ip> <client-port> <tls:0|1> <hmac>` (at most 256 bytes; the gateway replaces any such header the
client sent), where `<hmac>` is the lowercase hex HMAC-SHA256 of `"<client-ip> <client-port> <tls>"` keyed with
`secrets.handoff.frontToken` (its hex text as UTF-8), new on every control-plane start. Exit codes of the gateway: 1 damaged outbox, 2 refused
(handshake or config), 3 protocol error, 4 main port not opened, 5 outbox in use; before 1, 2, 4 and 5 it logs a line
starting with `fatal: `.
