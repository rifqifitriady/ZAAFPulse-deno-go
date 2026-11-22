# P5: the Go egress proxy and the network lock-down: design

Date: 2026-11-22. Repo `ZAAFPulse-deno-go` (public). Branch `migrate/p5-egress`, cut from `migrate/deno-go` at
`6a9800a` (all of P4 merged). Documents only: nothing is implemented here.

Inputs:
- Program plan `docs/migration/2026-11-10-deno-go-migration-plan.md`: §3 rule 5 ("Egress only through Go. After P5 the
  Deno process holds no non-localhost network permission"), §4 M9 (net = localhost only) and M10 (100% of outbound
  connections pass the Go proxy; a test proves Deno has no outbound net permission), §5 P5, §8 (a permission that is
  too narrow fails silently: a test per permission, loud errors, the matrix on the Compliance page), §9 (Go stdlib
  only, Deno no npm/jsr), §11.
- P2 (`deno.json` permission sets, `lib/permissions.js` `selfCommand`/`writeScope`/`runScope`/`describe`, the
  `runtime_permissions` control), P3 (IPC v1 over stdio NDJSON, additive-only rule, durable outbox events), P4a (the Go
  front, the internal listener, `frontToken`, the supervisor and its fallback), P4b–P4h (per-class switches with a
  shadow gate and the owner's switch rule of 1 clean day and 20 compares; fixtures from the real JS; the fixtures job's
  Deno deny flags; the engine's provider client in `gw/engine/upstream.go` with the proxy environment and the trusted
  roots), P4g (the internal port the gateway calls for its own requests, `X-Zp-Own`).

All Deno work follows the `deno` and `migrate-to-deno` skills; Go is standard library only. The owner's standing
instruction is to follow the recommended option: every choice below is "Decision N (recommended, pending owner)".
Three choices are flagged as owner questions at the end; none blocks a slice before the last.

## Inventory: every outbound connection the control plane makes today

Found by `grep` for `fetch`, `fetchImpl`, `http(s).request/get`, `net.connect`, `tls.connect`, `WebSocket`, `undici`,
`dns` over `lib/`, `server.js`, `mcp.js`, `bin/` (no `WebSocket`, `undici` or `dgram` user exists). "Loopback" means the
default target is on this machine; the same code reaches another host when the owner configures one.

| # | Caller (file) | Transport | Destination | Default | Class |
|---|---|---|---|---|---|
| E1 | JS gateway engine provider calls (`lib/gateway/providers.js request`, via `fetchSameOrigin`) and token endpoints (`refreshToken`: OAuth2, Google SA, Azure AD) | `fetchImpl` (global fetch) | provider `baseUrl`s (49 presets: api.openai.com, api.anthropic.com, openrouter.ai, …), `tokenUrl`s (oauth2.googleapis.com, login.microsoftonline.com) | remote | `gateway` |
| E2 | Connectors (`lib/connectors/base.js makeClient` → Dynatrace, Arize, n8n incl. its allow-listed webhooks, HTTP, router connector `router.js`, `openrouter.js`) | `fetchSameOrigin(fetchImpl)` | integration `baseUrl`s from config | remote | `connectors` |
| E3 | MCP upstream client (`lib/mcp/client.js`, Streamable HTTP, SSE answers) used by `lib/connectors/mcp-proxy.js` and brain Heybrain | `fetchSameOrigin(fetchImpl)` | upstream MCP URLs from config | remote | `mcp` |
| E4 | OmniRoute (`lib/omniroute.js`) and the OpenRouter page (`lib/openrouter.js`: models, providers, endpoints, credits, key) | `fetchImpl` | OmniRoute base URL (config); `openrouter.ai` | remote | `routers` |
| E5 | Price sync (`lib/price-sync.js`) | `fetchImpl` | `openrouter.ai/api/v1/models` | remote | `pricing` |
| E6 | Judge and evals (`lib/evals/models.js`: model lists and calls to Ollama/LM Studio; the gateway path goes through the gateway itself) | `fetchImpl` | `ZAAFPULSE_OLLAMA_URL`, `ZAAFPULSE_LMSTUDIO_URL` | loopback | `evals` |
| E7 | Remote A2A agents (`lib/gateway/orchestrator.js`, `agent.remoteUrl`) | `fetchImpl` | agent URLs from config | remote | `agents` |
| E8 | Monitor webhooks (`lib/monitors/channels.js`) | `fetchImpl` | webhook URL from settings (Slack, Teams, any) | remote | `notify` |
| E9 | Second-brain apps (`lib/brain/apps.js call`: Notion, Roam with its 308 to a peer host, Mem, Reflect) | global `fetch` | api.notion.com, api.roamresearch.com (+ peer.api…), api.mem.ai, reflect.app | remote | `brain` |
| E10 | Vector stores (`lib/brain/vectors.js`: Pinecone control and index hosts, Qdrant, others) | `fetchSameOrigin(fetch)` | api.pinecone.io, `*.pinecone.io` index hosts it learns at run time, Qdrant URL | remote or loopback | `brain` |
| E11 | Postgres/pgvector (`lib/brain/pg.js`) | `net.connect` + `tls.connect({socket})` | Postgres host:port from config | either | `brain` (tunnel) |
| E12 | URL ingest into a knowledge base (`lib/gateway/index.js` `ingest-url`): `safeFetch` (public addresses only, DNS pinned, 5 redirects re-checked, 5 MB cap) or, with `ZAAFPULSE_ALLOW_PRIVATE_URLS=1`, plain `fetch` | `node:https` / `fetch`, `dns.lookup` | any URL a user typed | remote | `public` |
| E13 | OTLP export (`lib/otel.js post`) | `node:http(s).request` | OTLP endpoint from config (a local collector or a SaaS endpoint such as Dynatrace) | either | `otel` |
| E14 | Local runtimes (`lib/runtimes/models.js`, `lib/runtimes/state.js`) | `node:http` request/get | runtime base URLs (Ollama, LM Studio, llama.cpp, LocalAI) | loopback | `runtimes` |
| L1 | Demo warm-up (`lib/demo-gateway.js warm`), `bin/zaafpulse.js` health probe, `lib/front/*` (internal listener), `scripts/*` | fetch / `node:http` | `127.0.0.1` | loopback only | not egress |
| X1 | Child processes (`claude`, `codex`, `node`, model servers, `powershell`, `osascript`, `brew`) | their own | anything | n/a | outside Deno's permissions and outside the proxy (P2: run is not a sandbox boundary) |
| X2 | The stdio MCP server (`mcp.js`, a separate Deno process an AI client starts): E2, E3 through its integrations | as E2/E3 | as E2/E3 | remote | as E2/E3 (Decision 15) |
| G1 | The Go engine's own provider calls and token fetches (`gw/engine/upstream.go`, `gw/provauth`) | Go `net/http` | provider and token URLs | remote | already in Go; recorded as `gateway` (Decision 9) |

No update check exists in this tree (the README's update notes are P6). DNS: only `lib/net.js assertPublic` calls
`dns.lookup`; it moves into Go with E12.

## Decisions

### The proxy

1. **A forward endpoint on a loopback listener of `zaafpulse-gw`, not an HTTP proxy in the environment.** The control
   plane sends each outbound request to `http://127.0.0.1:<egress port>/` with the target in a header; Go makes the
   real request. Evidence: Deno checks the net permission against the URL's host before it looks at any proxy, so an
   `HTTPS_PROXY` route would still need the outbound grant M9 removes; a `CONNECT` proxy also hides the path and the
   status from the record. Raw TCP (E11) uses `CONNECT host:port` on the same listener (a tunnel; TLS stays end to end
   in JS).
2. **The listener** binds `127.0.0.1:0` when the first `config.push` carries an `egress` section, before `ready`; its
   port goes back in `ready.egress.port` (an optional field). It lives as long as the gateway; a restart gives a new
   port, which the control plane takes from the next `ready`. The control plane sends the section in front mode from
   P5a on; with every class `direct` nothing uses it.
3. **Authentication:** every request carries `X-ZP-Egress-Token: <hex HMAC-SHA256(frontToken, "zaafpulse egress v1")>`;
   Go derives the same value from `secrets.handoff.frontToken` and compares in constant time. A missing or wrong token
   gets no reply (the connection is closed), as the internal listener does. No new secret field crosses IPC. The derived
   token, not `frontToken`, is what Decision 15's discovery file holds.
4. **The request** (`X-ZP-Egress-*` headers, all removed before the request leaves Go):
   `-URL` the absolute `http:`/`https:` target (a user name or password in it is refused), `-Purpose` the class and an
   optional detail (`connectors:dynatrace`), `-Redirect` `follow` | `manual` | `error`, `-Public: 1` for an untrusted
   URL (Decision 7). Method, headers and body go through unchanged and streamed; Go removes the hop-by-hop headers and
   sets `Host` from the target. The answer is the target's status, headers (hop-by-hop removed) and body, flushed per
   read (SSE works), plus `X-ZP-Egress-URL` (the final URL) and `X-ZP-Egress-Redirected: 1` after a followed redirect.
5. **Failures are a fetch failure on the JS side.** Go answers 502 with `X-ZP-Egress-Error: <code>` and a JSON body
   `{code, message}`; the client throws `TypeError("fetch failed", { cause: { code, message } })` (Node's shape; every
   caller already reads `err.cause?.code`). Codes: `ECONNREFUSED`, `ENOTFOUND`, `ETIMEDOUT` (10 s connect, undici's and
   `upstream.go`'s), `ECONNRESET`, `CERT_INVALID`, `EPROTO`, `EGRESS_REFUSED` (allow-list, enforce mode),
   `EGRESS_PRIVATE` (public mode), `EGRESS_REDIRECTS` (over 20), `EGRESS_REDIRECT` (`-Redirect: error`),
   `EGRESS_UNAVAILABLE` (client side: no ready gateway under the lock-down, Decision 14). A caller's abort closes the
   loopback request and Go cancels the target request. No other timeout is added.
6. **Redirects follow fetch's rules in Go** for `follow`: at most 20; 301/302 with POST and a 303 with any method but GET or
   HEAD become a GET without body and body headers; `Authorization` is dropped on a cross-origin hop. Each hop is checked against the
   allow-list and recorded. `manual` returns the 3xx (every caller that handles redirects itself: `fetchSameOrigin`,
   `brain/apps.js`, `safeFetch`); `error` fails.
7. **Public mode (E12) moves the SSRF guard into Go's dialer.** `-Public: 1` makes Go refuse, at connect time, every
   address `lib/net.js isPrivateAddress` refuses (ported; proven by fixtures the JS function writes), so a second DNS
   answer cannot pass (rebinding) and no DNS is needed in Deno. `safeFetch` keeps its redirect loop (manual, 5 hops),
   size cap and timeout in JS. `ZAAFPULSE_ALLOW_PRIVATE_URLS=1` keeps its meaning: the same class without `-Public`.
8. **Fidelity with a direct fetch.** The client fills what the runtime's fetch would add when the caller did not:
   `accept: */*`, `accept-language: *`, `user-agent` (`Deno/<version>` under Deno, `node` under Node). Go asks for gzip
   and decodes it (the caller reads the decoded body, as with fetch); the caller's `accept-encoding` is not sent (known
   difference 1). Proven by a differential test: one local fake records what it receives, and the same calls made
   directly and through the proxy, on Node and on Deno, must give the fake the same method, path, query, body and
   headers (but `accept-encoding`, `host` port and order) and give the caller the same status, headers and body.

### Records

9. **Every egress is recorded by Go, as a new IPC event type `egress.event`** (gateway to control plane, durable in the
   outbox with its `seq`, as `usage.report`): `{ts, purpose, method, scheme, host, port, status?, outcome, error?,
   bytesOut, bytesIn, ms, via}`. `outcome`: `ok` (an answer came, any status), `error` (a failure code), `refused`
   (allow-list, enforce), `would-refuse` (allow-list, report), `checked` (Decision 11). `via`: `proxy`, `tunnel`,
   `engine` (G1: the engine's own provider calls and token fetches, through a recording `RoundTripper` on its client)
   or `check` (a `/check` verdict, Decision 11; added in the Task 5 review round).
   No path, query or header is recorded (a key may sit in a query). Bytes are body bytes (decoded), not wire bytes.
10. **The control plane stores them in `.data/egress.jsonl`** with the writers' strict mode (store, mark, acknowledge;
   `lib/ipc/ingest.js`), with the MCP audit log's rotation; a `refused` one also gets a governance chain entry
   (`action: "egress.refused"`, target `{type: "host", name: "<host>:<port>"}`, outcome `refused`). A redirect hop is its
   own record. `GET /api/egress` (owner) gives the last records and per-class counts for the Compliance page.

### Routing each caller class

11. **One switch per class, `config.json egress.classes.<class>`: `direct` (default) | `check` | `go`.** The classes are
   the inventory's: `gateway`, `connectors`, `mcp`, `routers`, `pricing`, `evals`, `agents`, `notify`, `brain`,
   `public`, `otel`, `runtimes`. `check` is the shadow: the call goes direct as today, and the client asks Go
   `POST /check` (same token, same headers, no body sent anywhere) for the allow-list verdict, which Go records as
   `checked` or `would-refuse`. A class asked for `go` runs on go only after its gate: the owner's rule (1 clean day and
   20 checked calls, `egress.gateDays`/`egress.gateMinChecks`, defaults 1 and 20), clean meaning no `would-refuse` and
   no check error; until then it stays `check` and `/api/gateway/status` says why. Only `/check` verdicts (`via:
   check`) and the client's check errors are evidence. The gate is sticky: a class that met it stays on `go` (kept in
   `.data/egress-gate.json`) until a diff of its own or until the owner sets it to `direct` or `check`. Calls of a `go` class go direct when
   no gateway is ready (js mode, fallback, a restart), except under the lock-down (Decision 14).
12. **One client module, `lib/egress.js`, process-wide.** `egress.fetchFor(class, detail?)` returns a fetch-compatible
   function; `egress.connect(class, host, port)` returns a socket (the tunnel or `net.connect`). Unconfigured (tests,
   CLI commands, Node without a gateway) it is the global `fetch`/`net.connect` unchanged, so no existing test changes
   behaviour. `server.js` configures it with the gateway's port, the derived token, the switches and the lock-down. A
   target whose host is `localhost`, `127.0.0.1` or `[::1]` always goes direct (these are exactly the hosts the
   lock-down grants). The loopback leg uses `node:http` with a keep-alive agent (never the environment proxy) and turns
   the answer into a WHATWG `Response` whose `url` and `redirected` are set from Go's headers. Every call site keeps its
   own injection point (`fetchImpl`, `fetch`) and gets its default from `egress.fetchFor(<class>)`; the `node:http`
   callers (E13, E14) move to the returned fetch, E11 to `egress.connect`, E12's `safeFetch` to the proxy in public mode.

### The allow-list

13. **Built by the control plane from config and pushed in `config.push.egress.allow`**: rules `{host, port?}` where
   `host` is a name, an address or `*.suffix`. Sources: every provider `baseUrl` and `tokenUrl`; every integration's
   `baseUrl` and n8n's allow-listed webhook URLs; MCP upstream URLs (and Heybrain's); the OmniRoute URL; `openrouter.ai`
   (when price sync or the OpenRouter page is on); the brain apps' fixed API hosts for each configured app (Roam's
   `*.roamresearch.com`), Pinecone (`api.pinecone.io`, `*.pinecone.io`), Qdrant and Postgres from config; remote agent
   URLs; the monitor webhook URL; the OTLP endpoint; non-loopback runtime URLs and `ZAAFPULSE_OLLAMA_URL`/
   `ZAAFPULSE_LMSTUDIO_URL`. `public` is never matched against the list (any public address, Decision 7). Mode
   `egress.allowList`: `report` (default: a miss is recorded `would-refuse` and passes) | `enforce` (a miss is refused,
   `EGRESS_REFUSED`). Report by default because a host the code learns at run time and the list misses would otherwise
   break a feature the product calls today. The Go engine's own calls are checked too; provider hosts are on the list by
   construction.

### Lock-down

14. **`egress.lockdown: true` narrows Deno's net grant at launch.** `lib/permissions.js selfCommand` adds
   `--allow-net=127.0.0.1,localhost,[::1]` plus the dashboard's listen host when it is not one of them (`0.0.0.0`, a LAN
   address), for the `server` set, when the config asks for it, `gateway.engine` is `front`, and every class is on
   `go` (gates met). A CLI `--allow-net` replaces the set's entry and the set's `--deny-net` for cloud metadata stays.
   Otherwise the launch is unchanged and `/api/gateway/status` and the Compliance control say which condition is
   missing. Under the lock-down the client never goes direct for a non-loopback host: with no ready gateway the call
   fails with `EGRESS_UNAVAILABLE` ("the gateway is not running; network egress is locked to it"), loudly.
   **Fallback, documented and tested:** set `egress.lockdown` to `false` (Settings or `config.json`) and restart, or
   start with `ZAAFPULSE_EGRESS_LOCKDOWN=0`, which wins over the config; a service install computes its launch line at
   install time, so `zaafpulse service install` again applies either. The lock-down can always be turned off.
15. **The stdio MCP server (X2) uses the dashboard's proxy when one runs.** The server writes
   `.data/egress.json` (`{pid, port, token, engines, lockdown}`: `engines` the gated engine of each class, as the
   client is configured; mode 0600, written with a rename, removed on stop) when its gateway is ready; `mcp.js`
   configures `lib/egress.js` from it on each call (re-read when it changes, ignored when the pid is gone). Without it, `mcp.js` goes direct. Its own net grant is not narrowed in P5 (owner question Q2).
16. **The compiled single-file binary keeps the broad net grant baked in P5** (as it keeps an unscoped write, P2), and
   gets the lock-down in code only: with `egress.lockdown` on, `lib/egress.js` refuses direct non-loopback calls as in
   Decision 14. The Compliance control says "enforced by the egress client; Deno's grant is not narrowed in the
   single-file build". Baking the narrow grant into the binary is owner question Q1, for P6.
17. **The proof (M10):** a Deno test launches a script with exactly the lock-down launch line `selfCommand` computes and
   asserts that a direct `fetch` and a `net.connect` to a non-loopback host (`example.com`, `10.0.0.1`,
   `169.254.169.254`, `[2001:db8::1]`, `127.0.0.2`) throw Deno's `NotCapable` before any packet is sent, that the same
   call through the proxy reaches a local fake and is recorded, and that `127.0.0.1` still works. A permission test per
   new entry (`test/permissions.test.js`: the feature works, the denied neighbour fails loudly).

### Compliance

18. **A new control `network_egress`, "Network egress is mediated and logged"** (`lib/governance/controls.js`, clauses
   `nist-ms-2.7`, `iso-a6`, `eu-12`). `off` in js mode; `partial` while any class is not on `go`, the allow-list is in
   report mode, or the lock-down is not in force; `in place` when every class is on `go`, the allow-list enforces and
   Deno's net grant is scoped (`describe().grants.net === 'scoped'`). Evidence: the classes per switch, the allow-list
   mode and size, records and refusals in the last 24 h, the lock-down state and why it is not in force, and the
   standing note that child processes (X1) are outside Deno's permissions and the proxy.

### IPC

19. **Additive only:** a new type `egress.event` (schema, fixtures on both sides, outbox-durable), optional
   `ready.egress {port}`, optional `config.push.egress {allow[], allowList, classes}`. No change to an existing field.

## Slices (each merges on its own and is reversible by config)

| Slice | Scope | Reversal |
|---|---|---|
| P5a | Go egress listener, forward and tunnel, public mode, redirects, recorder; `egress.event` → `.data/egress.jsonl`; engine calls recorded; `lib/egress.js` (unconfigured = direct); differential test | no class uses it until P5b; `gateway.engine: js` starts no gateway |
| P5b | The switches with `check` and the gate; every call site E1–E14 through `fetchFor`/`connect`; `safeFetch` in public mode; the discovery file and `mcp.js` | every class defaults to `direct`; set a class back to `direct` |
| P5c | The allow-list from config, pushed; report and enforce | `egress.allowList: report` (default) |
| P5d | The `network_egress` control and `GET /api/egress` | read-only |
| P5e | The lock-down launch line, `EGRESS_UNAVAILABLE`, the denial test, the fallback | `egress.lockdown: false` or `ZAAFPULSE_EGRESS_LOCKDOWN=0`; default off |

The riskiest step, narrowing Deno's grant, is last and off by default.

## Exit criteria (P5)

- M10: every inventory row E1–E14 has a call site that goes through `lib/egress.js` (a test per class proves that with
  the class on `go` the local fake is reached through the proxy and a record is written); G1 is recorded.
- M9 (net part): under the lock-down launch line the denial test passes on Ubuntu and macOS in CI.
- The differential test passes on Node and Deno; `gofmt`, `go vet` (also `GOOS=windows`), `go test -race` clean; the
  contract suite green in js, go and shadow modes; the push-triggered Ubuntu run green.
- No test sends anything to a real outside host: every target is a local fake; the denial test's outside hosts are
  refused by Deno before any packet.

## Known differences from a direct call (recorded, not fixed)

1. Requests advertise `accept-encoding: gzip` only (fetch sends `gzip, deflate, br`); a caller's own
   `accept-encoding` is not sent.
2. A failure is `TypeError("fetch failed")` with `cause.code` on both runtimes; Deno's own fetch message ("error sending
   request for url …") is no longer seen.
3. The proxy environment (`HTTPS_PROXY`, `NO_PROXY`) is applied by Go for every class; before, `node:http` callers
   (E13, E14) ignored it.
4. Trusted roots are Go's (system plus `SSL_CERT_FILE`, `NODE_EXTRA_CA_CERTS`, `DENO_CERT`), as for the engine.
5. Recorded bytes are decoded body bytes.
6. A gateway restart cuts the egress calls in flight (they fail like a dropped connection; callers retry as today).
7. The SSRF set is stricter than before P5, on both runtimes (`lib/net.js isPrivateAddress` and `gw/egress IsPrivate`
   together, proven by `gw/testdata/egress/private.json`): IPv6 `::/96` (the deprecated IPv4-compatible
   `::a.b.c.d`, besides `::` and `::1`), NAT64's local-use `64:ff9b:1::/48` (inside the refused `64:ff9b::/32`), the
   deprecated site-local `fec0::/10`, Teredo `2001:0::/32` (refused whole) and 6to4 `2002::/16` when the IPv4 address
   it embeds is private are refused too. An IPv4-mapped address written in hex groups (`::ffff:7f00:1`) is judged as
   its IPv4 address on the JS side as Go always did (before, JS refused every such text form).

## Owner questions (recommendation taken; none blocks P5a–P5d)

- **Q1 (P6):** bake `--allow-net=<loopback>` into the single-file binary? It cannot then be widened at run time: the
  fallback would be reinstalling the previous release, and js mode (no gateway) would have no egress at all.
  Recommended: no in P5 (Decision 16); decide in P6 together with making the gateway always run.
- **Q2 (P6):** narrow the stdio MCP server's grant? Its launch line is written into each AI client's config at
  registration, so a change needs a re-registration, and it would have no egress while the dashboard is not running.
  Recommended: no in P5 (Decision 15).
- **Q3 (P7):** turn the defaults to `enforce` and `lockdown: true` at the cut-over? Recommended: yes at P7, after the
  owner has run with them on the preview; P5 keeps both off by default so nothing the product calls today is blocked.

## Out of scope

- Child processes' own network use (X1); the Go engine's provider calls beyond recording them (already Go).
- The stdio MCP server's permission set and the single-file binary's baked grant (Q1, Q2).
- Quiet-machine benchmarks (deferred to the program's last step); any overhead figure in the results is marked
  "not quiet-confirmed".
