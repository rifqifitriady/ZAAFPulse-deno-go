# P4a: the Go gateway in front (slice S1 and the P3 entry conditions): design

Date: 2026-11-12. Repo `ZAAFPulse-deno-go` (public). Branch `migrate/p4-go-data-plane`, cut from `migrate/deno-go` at
`0f7afb0` (P3 merged).

Inputs:
- Program plan `docs/migration/2026-11-10-deno-go-migration-plan.md`:
  - §3 design rules;
  - §5 P4 slices S1 to S10;
  - §11 owner decisions (embedded sidecar, budgets fail open, no soak).
- P3 results `docs/migration/2026-11-11-p3-results.md`: the P4 S1 entry conditions and the P4 follow-ups.
- P3 spec `docs/superpowers/specs/2026-11-11-p3-ipc-contract-design.md`, which defines the IPC contract this builds on.

All Deno work follows the `deno` and `migrate-to-deno` skills. Go is standard library only (P0 decision).

## P4 split into sub-projects (owner approved 2026-11-12)

P4 is too large for one spec. Each sub-project below goes through its own spec, plan, SDD run and PR into
`migrate/deno-go`:

| Sub-project | Slices | Scope |
|---|---|---|
| **P4a (this spec)** | S1, plus the P3 entry conditions | Go owns the main port and forwards everything to Deno; per-route engine switch (all `js`) |
| P4b | S2, S4, plus the shadow framework | Non-streaming OpenAI pass-through; API keys, per-key allow-lists and modes; shadow compare |
| P4c | S3 | Streaming SSE (M4 at 500 streams) |
| P4d | S5 | Translation for Anthropic, OpenAI, Gemini, routers and Ollama/local |
| P4e | S6, S7 | Guardrails; budget guard (fail open) |
| P4f | S8 | Usage, audit events and trace spans from the Go engine over IPC |
| P4g | S9, S10 | `/mcp` `tools/call`; provider auth (Google service-account JWT and the others) |
| G3 | none | Every route on `go`, shadow diff 0, M3 and M4 met, the JS hot path deleted |

## Owner decisions for P4 (2026-11-12)

1. **Go is in front from the start.** Go holds the main port (`ZAAFPULSE_HOST`/`ZAAFPULSE_PORT`, TLS when it is
   configured). It forwards the dashboard and every route still on `js` to Deno.
2. **Shadow without calling the provider twice.** Only the serving engine calls the provider. The other engine gets
   the same client request and builds its provider request without sending it, then turns the same provider
   response into its client response. The diff compares the provider request, the client response, usage and cost.
   This is built in P4b, where the first route can run on `go`. P4a only reserves the config field.
3. **Deno is the parent and Go the child.** `zaafpulse start` is still the Deno process. It starts Go with the P3
   supervisor. If Go crashes, the main port is down until the supervisor restarts it (back-off 1 s, doubling to 30 s).

## Facts that shape the design

- **The gateway and the dashboard share one port.** `server.js:1511` hands every request whose path starts with
  `/v1/`, `/v1beta/`, `/mcp`, `/a2a`, `/.well-known/agent*.json` or `/skills` to `gateway.handle`
  (`lib/gateway/index.js:1602`). Everything else is the dashboard (pages, `/api/*`, SSE).
- **Deno trusts the TCP peer address**, in three places:
  - `lib/auth.js:112` `clientKey` uses `req.socket.remoteAddress` for lockouts and rate limits;
  - `lib/auth.js:160` lets a loopback `Origin` through when the auth mode is not `password`;
  - `server.js:1493` accepts `POST /v1/traces` only from a loopback peer with no forwarding header.

  `req.socket.encrypted` decides whether cookies are `Secure` (`lib/auth.js:300`). `ZAAFPULSE_TRUST_PROXY=1` makes
  `X-Forwarded-*` trusted.
  If Go connects from loopback, every request would look local. That is a security hole to close, not a detail.
- **The supervisor already exists** (`lib/ipc/supervisor.js`). It has start, ready, health, snapshots, reload and
  stop, plus restart with back-off. `server.js` does not use it yet.

## Design

### D1. Processes, ports and start order

1. Deno opens an **internal listener** on `127.0.0.1:0` (port chosen by the OS). Only Go may use it (D3).
2. Deno starts Go through the supervisor. `config.push` carries a new `front` object:
   - `listen`: `{ host, port }`, taken from `ZAAFPULSE_HOST`/`ZAAFPULSE_PORT`;
   - `tls`: `{ certPath, keyPath }`, only when TLS is configured;
   - `upstream`: `{ port }`, Deno's internal port;
   - `engines`: `{ [routeClass]: 'js' | 'go' }`, every value `js` in P4a;
   - `shadow`: `{}`, reserved for P4b.

   `secrets.handoff` carries a new `frontToken`, 32 random bytes from `crypto.getRandomValues`, hex encoded and
   generated again on every Deno start. Both changes are additive in v1, as the P3 version rule allows.
3. Go reads the TLS key file itself, and only after the handshake. Go then opens the main port and sends `ready`.
   If the port is taken, Go exits with a new exit code, 4, so the supervisor reports "port in use" instead of
   restarting blindly.
4. The `zaafpulse start` banner, `scripts/open-dashboard.js` and the "server is up" probe keep using the main port.
   No URL changes for users or AI clients.
5. **Engine switch in config.** `gateway.engine` in `config.json` decides the mode:
   - `"js"` (default in P4a) means no Go process. Deno opens the main port itself, exactly as today. This is the
     rollback, and it keeps every existing install unchanged.
   - `"front"` means Go is in front, as in this spec.

   The preview (port 4787) runs `front`.

### D2. Go request routing

- A route table classifies each request by path, using the same prefixes as `gateway.handle`. Gateway routes are
  split into classes (`chat`, `messages`, `responses`, `embeddings`, `models`, `gemini`, `mcp`, `a2a`, `skills`,
  `agents`). The `engines` map picks `js` or `go` for each class.
- In P4a every class is `js`, and so is anything not classified (the dashboard). Go has no `go` engine handler yet.
  A class set to `go` is refused at `config.push` validation with an error.
- `js` means a reverse proxy to `upstream.port` through `net/http/httputil.ReverseProxy`, with a `Rewrite` function:
  - **Kept as is:** method, path, query, body, the Host header and every client header.
  - **Removed, not added:** the hop-by-hop headers and any `X-Forwarded-*` or `Forwarded` header the client sent.
    Go does not add forwarding headers; the client's address travels in the connection preamble (D3).
  - **Streaming:** `FlushInterval: -1` flushes at once, so SSE (gateway and dashboard) and chunked bodies pass
    through without buffering.
  - **Protocol upgrades:** WebSocket upgrades pass through, using `ReverseProxy`'s built-in upgrade handling.
- **Timeouts and limits:**
  - no overall request timeout, so long streams survive;
  - `ReadHeaderTimeout` 10 s and an idle timeout of 120 s;
  - request bodies are not limited by Go. Deno keeps its current limits.
- **Upstream down** (Deno not listening): Go answers 502 with a JSON body
  `{"error":"the control plane is not answering"}`, plus a `Retry-After: 2` header.

### D3. Connection preamble: no request looks local by accident

Go opens its connections to Deno's internal port with a **preamble line** before the HTTP bytes:

```
ZPGW1 <frontToken> <client-ip> <client-port> <tls:0|1>\r\n
```

Deno's internal listener is a `net.Server`. For each connection it:
1. reads up to 256 bytes and up to the first `\r\n`, within 2 s, and closes the connection otherwise;
2. compares the token in constant time (`crypto.timingSafeEqual`), and closes the connection without a reply when
   the token is wrong or missing;
3. sets the connection's `remoteAddress`, `remotePort` and `encrypted` to the preamble's values, on the socket object
   the HTTP server sees, then hands the socket to the existing HTTP server with `server.emit('connection', socket)`,
   pushing back any bytes read past the preamble with `socket.unshift`.

Every existing check therefore reads the real client: `clientKey`, the loopback `Origin` rule, the `/v1/traces`
loopback check and `Secure` cookies. No call site changes.
- A client that sends `X-Forwarded-For` gets it removed by Go (D2), so `ZAAFPULSE_TRUST_PROXY=1` keeps its meaning:
  a proxy in front of Go.
- `ZAAFPULSE_TRUST_PROXY` is passed to Go in `front`. When it is set, Go keeps the right-most `X-Forwarded-*` entries,
  so the proxy's headers still reach Deno.
- Task 3 starts with a probe on Deno 2.9.8 and Node 24. It checks that `remoteAddress` can be redefined on a
  `node:net` socket and that `emit('connection')` with `unshift` works. If either fails on Deno, the fallback is that
  Go sends one header, `X-ZP-Client: <ip> <port> <tls> <hmac>`, and Deno reads the client from that header in
  `clientKey`, the `Origin` rule, the traces check and `Secure` cookies (four call sites). The probe result decides;
  the spec does not.

### D4. P3 entry conditions (all in P4a, before `server.js` starts Go)

| # | Condition | Change |
|---|---|---|
| E1 | Outbox single writer | Hold an exclusive lock on `gw-outbox/.lock` for the gateway's lifetime (Unix: `syscall.Flock` `LOCK_EX\|LOCK_NB`; Windows: `syscall.CreateFile` with share mode 0). While another process holds it, exit with code 5 ("outbox in use"). |
| E2 | Missing `stream.json` (T4-2) | Segments present and no `stream.json` means damaged: exit 1, and do not start a new stream. |
| E3 | Separate IPC writers (I2) | The ingest in `server.js` uses strict sinks of its own, built the way `lib/ipc/local.js` builds them. It never uses the server's `usage.record` wrapper, `govRecord` or the trace recorder. A test asserts that `server.js` passes no wrapped writer. |
| E4 | Mark recovery without the 4 MiB window (T4-7) | A mark file `.data/ipc-marks.json` holds `{stream: mark}`. It is written with `rename`, after the store and before the ack, so it never runs ahead of the store. On start, the larger of the mark file and the tail scan wins. |
| E5 | `deno compile` includes `ipc/v1` (T1-7) | Add `ipc/v1/*.schema.json` to `scripts/build-deno.js`, and add a test on the compiled binary. |
| E6 | Secrets out of logs (T2-3) | Zero `Envelope.Body` after a `secrets.handoff` decode, and add a test proving no formatter prints a secret value. |
| E7 | Minimal environment (N-b) | Go gets `ZAAFPULSE_DATA`, `PATH`, `HOME`/`USERPROFILE`, and `SystemRoot`/`TEMP`/`TMP` on Windows. Nothing else. |
| E8 | No endless restart (N-c, T3-3) | Exit 2 (handshake refused), 1 (damaged outbox), 4 (port in use) and 5 (outbox in use) stop the supervisor in the `failed` state with the reason, and arm no hello timer after a refusal. |
| E9 | `reload()` while starting (N-d) | Queue the reload and send it after `ready`. |
| E10 | One send lock (T4-4) | Every Go event send happens under the same lock as the append and the replay. |
| E11 | Stdin read error exit code (T3-5) | Exit 1, as in the contract. |

### D5. When Go fails

- **Crash or no health:** the supervisor restarts Go with back-off. While Go is down, the main port refuses
  connections, and users see the browser's "cannot connect" page. The dashboard status (D6) shows the restarts.
- **`failed` state** (E8, or 5 restarts within 60 s): Deno **falls back to `js` mode**.
  - Deno opens the main port itself on the same host and port, logs `gateway failed: <reason>; serving on Deno`, and
    raises a dashboard alert.
  - A later `reload` or `zaafpulse restart` tries `front` again.
  - Go has exited by then, so the port is free; if it is still taken, Deno waits up to 5 s for it.
- **Deno crash:** Go sees stdin close and exits 0, as in P3. launchd or the service restarts Deno, as it does today.

### D6. Status in the dashboard

`/api/gateway/status` returns the supervisor's `status()`, plus `mode` (`js`, `front` or `fallback`) and `engines`.
The Overview topology card shows a "Gateway: Go (front)" chip with the state: ready, restarting or fallback. This is a
small UI change, and the owner checks it in the preview.

### D7. Permissions

- **`server` set:** `run` gains the gateway binary path. `runScope` already computes install-time run grants, so the
  binary path is added there and not hard-coded.
- **`net`:** unchanged. Deno listens on loopback for the internal port.
- **Go gets only what it needs:** the main port, the internal loopback port, the outbox folder, and the TLS files when
  TLS is configured.

## Tasks

| Task | Scope | Exit |
|---|---|---|
| T1 | Go hardening: E1, E2, E6, E10, E11, exit codes 4 and 5 | Go tests on 4 OS: a second gateway on the same outbox exits 5; segments without `stream.json` exit 1; no secret in any log output |
| T2 | JS hardening: E3, E4, E5, E7, E8, E9 | Tests: mark file beats the tail window; no wrapped writer; minimal environment; `failed` state on 1, 2, 4 and 5; reload queued; compiled binary loads the schemas |
| T3 | Connection preamble: the probe first, then the internal listener (D3) | Tests on Node and Deno: wrong or missing token closes the connection; a forged `X-Forwarded-For` is ignored; `/v1/traces` from outside is refused through Go; `clientKey` sees the preamble address; `Secure` cookies follow `tls` |
| T4 | Go front proxy (D1, D2): listen with TLS, route table, reverse proxy with preamble, `front` in `config.push`, `frontToken` | Go tests: SSE flushes per event; WebSocket upgrade passes; client hop and forwarding headers removed; 502 when upstream is down; port in use exits 4 |
| T5 | `server.js` wiring (D1.5, D5, D6, D7): `gateway.engine`, start order, fallback, status API and chip, `runScope` | Tests: `js` mode unchanged; `front` mode serves the dashboard and gateway through Go; killing Go brings the port back; 5 failures fall back to Deno on the same port |
| T6 | Contract suite through the front, kill tests, overhead bench, 6-target smoke | All 172 contract cases green in `front` mode on Node and Deno; kill test (Go killed during SSE: the client sees the cut, the port returns, nothing double-counted); proxy overhead recorded (p50, p99, per request); the binaries start and stop on all 6 targets in CI |
| T7 | Results doc, README note, plan §5 P4 S1 ticked | Docs match the code |

Order: T1 → T2 → T3 → T4 → T5 → T6 → T7. T1 and T3 do not depend on each other. Because a work-branch push runs on
Ubuntu only, start a **full-matrix CI dispatch after T1, T4 and T5** (lesson from P3).

## Exit criteria (P4a, gate S1)

- [ ] The P3 entry conditions E1 to E11 are met, each with a test.
- [ ] In `front` mode, Go holds the main port and every request reaches Deno with its real client address. A forged
      address or a missing token is refused.
- [ ] All 172 contract cases pass through the front, on Node and Deno.
- [ ] Killing Go during traffic loses no usage line and double-counts none. The port comes back, and after 5 failures
      Deno serves on its own.
- [ ] The binaries start and stop under the supervisor on all 6 targets (plan S1 exit).
- [ ] The proxy overhead is measured and recorded as the M3 baseline.
- [ ] `gateway.engine: "js"` (the default) behaves exactly as before P4a. The live dashboard is not touched.

## Out of scope

- Any route on the `go` engine, and the shadow compare (P4b).
- Go as the parent process (rejected, decision 3).
- Removing JS gateway code (G3).
