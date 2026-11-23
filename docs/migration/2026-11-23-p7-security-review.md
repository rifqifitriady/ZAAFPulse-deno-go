# P7 security review: IPC v1, permission matrix, egress proxy, provider auth

Date: 2026-11-23. Branch `migrate/p7-security-review`, cut from `migrate/p5-egress` at `c41a1a1` (everything merged
through P4 plus the P5 egress work). Reviewer: the independent security-expert agent (program plan §5 P7, "Security
review of the IPC, the permission matrix and the egress proxy, plus `govulncheck`"). This document reports; it changes
no product code.

## Summary

| Severity | Count |
|---|---|
| Critical | 0 |
| High | 0 |
| Medium | 1 |
| Low | 4 |
| Info | 8 |

The IPC, the front token, the internal port and the egress endpoint's authentication hold up: nothing a client or
another local user can reach without a secret it should not have was found. The one Medium finding is a regression
P5 introduces once a class is switched to `go`: the cloud-metadata refusal that Deno's `--deny-net` gave every
outbound call does not exist in the Go egress path, and in the default `report` mode the endpoint follows a redirect
to any address. It was shown with a local probe. The Low findings are an endpoint the stdio MCP server trusts on a
pid check alone, governance entries the control plane accepts from the gateway with any actor, credential headers
kept on a cross-origin redirect, and a standard-library advisory that is present but not exploitable here.

`govulncheck`, `deno audit` and `npm audit` found nothing else.

## Scope and method

Read: `ipc/v1/` (schemas), `lib/ipc/*`, `gw/ipc/*`, `gw/cmd/zaafpulse-gw/{main,egress,front,sender}.go`,
`gw/front/*`, `lib/front/{internal,client,tools,index}.js`, `gw/egress/*`, `lib/egress*.js`, `lib/net.js`,
`lib/permissions.js`, `lib/service.js`, `deno.json`, `gw/provauth/*`, `gw/engine/{mcp,serve_mcp}.go`, the egress call
sites, and the P3, P4g and P5 specs and results. Threat model: (a) a remote client of the public port, (b) a remote
server the product calls (provider, integration, MCP upstream, remote agent, a URL a user typed), (c) another local
user on the same machine, (d) a compromised or buggy gateway process seen from the control plane, and the reverse,
(e) the same local user (out of scope as an attacker: it owns the data folder and the keychain).

Proof levels: **run** = a command or probe showed it; **read** = follows from the code, nothing executed.

Probes ran only against local fakes in a scratch folder outside the repository (`httptest` servers on 127.0.0.1). No
real credential or user data was used, and nothing was sent to an outside host.

## Findings

### M1 · Medium · proof: run — The Go egress path drops the cloud-metadata refusal; `report` mode follows a redirect to it

**Where:** `gw/egress/egress.go:222-263` (`dial`: private addresses are checked only when `public` is true),
`gw/egress/proxy.go:155-174` (`verdict`: in `report` mode a miss passes), `gw/egress/proxy.go:256-306` (redirects
followed per hop), `gw/egress/tunnel.go:30-42` (CONNECT, same), against `deno.json:21,36,51`
(`"deny": ["169.254.169.254", "metadata.google.internal"]`).

**What:** Before P5 every outbound call of the `server`, `cli` and `mcp-stdio` sets was refused by Deno for the cloud
metadata service, including on each redirect hop (Deno's fetch checks the permission per hop). For a class on `go` the
call is made by Go, which has no such rule: in the non-public path any address is dialled, and in `report` mode (the
default, `lib/egress-allow.js`, `gw/egress/allow.go:15-22`) a host off the allow-list is only recorded
`would-refuse`. The lock-down does not require `enforce` (`lib/permissions.js lockdownState`), so the same holds
under the lock-down.

**Paths:**
1. A remote server the product calls with `redirect: follow` answers `302 Location: http://169.254.169.254/...`.
   Example: a remote A2A agent (`lib/gateway/orchestrator.js:497`, class `agents`): Go turns the POST into a GET,
   follows it, and `r.json()` hands the metadata service's JSON (IAM role credentials on AWS IMDSv1) back as the
   agent's answer, which reaches the user or the model. Other follow callers: `routers` (OmniRoute), `pricing`,
   `evals`, `notify`.
2. With `ZAAFPULSE_ALLOW_PRIVATE_URLS=1`, `ingest-url` (`lib/gateway/index.js:2280-2296`) fetches a URL a dashboard
   user typed through class `public` without public mode: `http://169.254.169.254/latest/meta-data/...` is stored
   in a knowledge base. Before P5 Deno refused it.
3. A config URL (integration, OTLP, webhook) pointed at the metadata address.

Impact is limited to hosts on a cloud VM whose metadata service answers a plain GET (AWS IMDSv1; GCP and Azure need
a request header the redirect does not carry). It is live only once a class is switched to `go`, which P7's
cut-over (owner question Q3) intends.

**Evidence:** probe `egressprobe/probe_test.go` (scratch folder), `go test -v`: a fake target redirects to a second
fake that stands for the metadata service; the endpoint is built with `egress.New`, the derived token, and an empty
allow-list.

```
mode=report allowlist=false -> status 200 body "SECRET-METADATA" err=""
  second hop reached /latest/meta-data/iam/security-credentials/; Authorization="" Cookie="session=COOKIE" X-Api-Key="APIKEY"
  record 127.0.0.1:56919 outcome=would-refuse err=EGRESS_REFUSED
  record 127.0.0.1:56919 outcome=ok err=
mode=enforce allowlist=true -> status 502 body "{\"code\":\"EGRESS_REFUSED\",...}" err="EGRESS_REFUSED"
  record 127.0.0.1:56924 outcome=refused err=EGRESS_REFUSED
```

`enforce` refuses the second hop; `report` reaches it and returns its body.

**Fix (owner: backend-devops-engineer, Go gateway):** refuse the metadata endpoints in Go always, in every mode and
for every path (forward, `/check` verdict, CONNECT, the engine's own client), as a separate rule from the allow-list:
- in `dial`, before the connect and again in a `Control` hook on every dialer (not only the public one), refuse
  `169.254.169.254`, `169.254.170.2` (ECS task metadata), `fd00:ec2::254` (AWS IMDS over IPv6), `100.100.100.200`
  (Alibaba); an IPv4-mapped form is the same address;
- in `verdict` and `tunnel`, refuse the names `metadata.google.internal` and `metadata` (with a trailing dot too)
  before any resolution;
- answer `EGRESS_PRIVATE` (or a new `EGRESS_METADATA`) and record `refused`;
- add `fd00:ec2::254`, `169.254.170.2` and `100.100.100.200` to the three `deny` lists in `deno.json` (and to
  `lib/permissions.js` WHY.broad.net and its test);
- a test per address and per path, including a followed redirect.

Separately, owner question Q3 (turn `egress.allowList` to `enforce` at the cut-over) closes the wider form of this
for off-list hosts; see Decisions.

### L1 · Low · proof: read — The stdio MCP server trusts the egress endpoint on a pid check alone

**Where:** `lib/egress-discovery.js:63-74, 95-138`; `deno.json:54` (`kill`, `tasklist` in the `mcp-stdio` run list).

**What:** `mcp.js` sends every integration call of a `go` class (with its credential headers: Dynatrace API tokens,
n8n keys, MCP upstream tokens) plus the derived egress token to `127.0.0.1:<port>` from `.data/egress.json`, once
`kill(pid, 0)` says the pid in the file is alive. Nothing proves that the process on that port is the gateway.

**Path:** on a multi-user machine: the dashboard is killed (SIGKILL, a crash, a power cut), so the file stays. Its pid
is reused by any process (`EPERM` counts as alive), and another local user binds the freed loopback port. The next
`mcp.js` tool call sends that user the derived token, the target URLs and the integration credentials. The window
needs a crash, pid reuse and a port race, hence Low. On a single-user machine there is no attacker this enables.

The `kill` and `tasklist` run grants were added to `mcp-stdio` only for this check. `kill` lets that process signal
any of the user's processes; no code path passes it a pid from a tool argument (`mcp.js` reaches `kill` only through
`lib/permissions.js kill`, which allows a fixed signal set), so this is a wider grant, not a reachable abuse.

**Fix (owner: backend-devops-engineer for Go, fullstack-developer or backend-devops-engineer for
`lib/egress-discovery.js`):** authenticate the endpoint, not the pid. Add `GET /hello?n=<random nonce>` on the egress
listener, answered without a token with `hex HMAC-SHA256(derivedToken, "zaafpulse egress hello " + nonce)`; `mcp.js`
checks it (constant time) when it reads a new file or a connection fails, and never sends the token before it
passes. Then drop the pid check and `kill`/`tasklist` from the `mcp-stdio` set (and from WHY.run), or keep the pid
check only as a fast negative.

### L2 · Low · proof: read — Governance entries from the gateway are signed into the HMAC chain with any actor

**Where:** `lib/ipc/ingest.js:131-136` (`recordGovernance({ ...b.event, ipc })`), `lib/ipc/server-sinks.js:43`,
`ipc/v1/audit.event.schema.json` (actor `{via, keyId, user, rule}` free text).

**What:** an `audit.event` with `log: "governance"` from the gateway is written into the tamper-evident chain as the
gateway wrote it, including `action`, `target` and `actor.user`. No production gateway code sends one: the only
emitter is the test stub's `--emit` (`gw/ipc/synthetic.go`); `egress.refused` is built on the JS side.

**Path:** a compromised or faulty gateway (the one process that also runs untrusted traffic parsing) writes chain
entries such as `{action: "keys.revoke", actor: {via: "dashboard", user: "admin"}}`, which the control plane seals
with the chain key. The chain then attests to events nobody did. Low because the gateway is already a trusted peer
that holds every provider secret.

**Fix (owner: backend-devops-engineer):** in `ingest.js` accept governance events only from an allow-list of actions
the gateway is entitled to (none today), and force `actor` to `{ via: "gateway" }` (drop `user`, `rule`, and a
`keyId` that does not exist) for anything accepted. Until an action is needed, refuse the type in production (keep it
for the kill tests behind the stub flag).

### L3 · Low · proof: run — A cross-origin redirect keeps `Cookie` and custom credential headers

**Where:** `gw/egress/proxy.go:302-304` (only `Authorization` is removed on a cross-origin hop).

**What:** undici's fetch also removes `Cookie` and `Proxy-Authorization` on a cross-origin redirect; Go's endpoint
keeps `Cookie` (`Proxy-*` is already stripped as hop-by-hop). Custom key headers (`x-api-key`, `api-key`) are kept,
as fetch keeps them. The probe above shows `Cookie` and `X-Api-Key` arriving at the cross-origin second hop.

**Path:** a caller on `follow` that sends a cookie or a key in a custom header to a server that redirects elsewhere.
The connectors and the MCP client already use `fetchSameOrigin` (manual redirects, cross-origin refused), and the
follow callers found send `Authorization` only, so no current caller leaks a key this way.

**Fix (owner: backend-devops-engineer):** remove `Cookie` too on a cross-origin hop (match undici), and document that
custom credential headers need `fetchSameOrigin`. Optionally make `manual` the default for `X-ZP-Egress-Redirect`
when the request carries any header besides a safe list.

### L4 · Low · proof: run — Go 1.27.3 carries GO-2026-6859 (net/http); not exploitable here

**Where:** the toolchain pin, `.github/workflows/ci.yml:60,105,197,230,273,358,417` (`go-version: '1.27.3'`).

**What:** `govulncheck ./...` (v1.10.0, DB 2026-11-11) reports one vulnerability, GO-2026-6859 "HTTP/1 server crash
due to MaxBytesReader header race", fixed in go1.27.4. govulncheck marks it reachable at the symbol level (the front's
`http.Server`, the egress listener), but the trigger needs `http.MaxBytesReader` or `MaxBytesHandler`, and
`grep -rn MaxBytesReader gw` finds no use. The egress handler does read the body while writing the header
(`EnableFullDuplex`, `proxy.go:200`), so a later change that wraps a body in `MaxBytesReader` would make it a
remote crash of the public port.

**Fix (owner: backend-devops-engineer, CI and release):** pin go1.27.4 or later in CI and in the release build (P6).

### Info

- **I1 · Lock-down is an accident guard, not containment.** Under the narrowed `--allow-net` the server can still
  start `node`, `osascript`, `open`, `brew`, `claude`, `codex` and the other launchers (`deno.json:22-29`), and the
  loopback grant covers every port, so any local forward proxy is reachable. This is documented (`lib/permissions.js`
  WHY.launchers, the `runtime_permissions` and `network_egress` controls name it); keep the Compliance wording
  unchanged when the defaults flip.
- **I2 · Under the lock-down the egress endpoint is an open forward proxy while the allow-list is in `report` mode.**
  Any holder of the derived token (the server, `mcp.js`, anyone who can read `.data/egress.json`) reaches any host,
  loopback and private addresses included. By design until Q3; see Decisions.
- **I3 · `X-ZP-Client` has no freshness.** The MAC covers `"<ip> <port> <tls>"` only, so a value is valid for the life
  of the front token (one server start) and is replayable by whoever sees loopback traffic (root). The gateway's own
  calls use one fixed value per run. Acceptable on loopback; a nonce or a timestamp window would be needed if the
  transport ever leaves the host (P3 D1 mentions mTLS for that case).
- **I4 · Secret wiping is best effort.** `Channel.Recv` clears its line copy (`gw/ipc/channel.go:73-75`), but the
  64 KiB `bufio` buffer and the decoded Go strings keep the values until reused or collected; Deno holds the same
  values as JS strings. Fine against log and outbox leakage (both covered); not against a memory dump.
- **I5 · Token endpoints are not required to be HTTPS.** OAuth2 `tokenUrl` and a service account's `token_uri` (taken
  from the stored JSON, `gw/provauth/google.go:54-60`) may be `http://`; the client secret or the signed assertion
  then travels in clear text. Same as the JS. Suggest refusing non-HTTPS except loopback, in both engines.
- **I6 · No resource limits for token holders.** The egress endpoint keeps up to 32 MiB of body per followed request
  for a 307/308 replay and has no connection cap; the IPC accepts any number of 8 MiB lines from the gateway. Only
  trusted holders can reach either.
- **I7 · The data folder is a trust boundary for the same user.** A writer of `.data/` can plant outbox segments that
  are replayed and stored, raise marks in `ipc-marks.json` to suppress events, or edit `egress.json`. The same writer
  can already queue governance events in `pending/`; files are `0600`, folders `0700`. On Windows the modes do not
  apply and the profile's ACLs are what protect them.
- **I8 · `mcp-stdio` net is not narrowed** (owner question Q2): its integrations go through the dashboard's endpoint
  only when the discovery file is there and the client is configured, and direct otherwise, under its full grant.

## Checked and found sound

IPC v1
- Framing: 8 MiB per line on both sides (`gw/ipc/framing.go`, `lib/ipc/framing.js`), memory bounded while a line
  builds, a partial last line is an error, CR and empty lines handled alike.
- Every incoming message on the control plane is checked against `ipc/v1` before use (`lib/ipc/channel.js:28`);
  unknown types are dropped once-logged; Go decodes strictly in tests and leniently in production as specified.
- Validation errors on either side name the path and the rule, never the value (`lib/ipc/schema.js`,
  `gw/ipc/shape.go`; the only value printed is an enum value, and no secret field is an enum).
- Handshake: protocol version checked, a refused hello makes the gateway exit 2, no event before `hello` is accepted.
- Replay: dedupe by `(stream, seq)`, a gap is refused and not acknowledged, the mark only rises, the mark file keeps
  only `hex32` streams and positive integers and is written `0600` by rename.
- Outbox, call journal and lock file are `0600` in a `0700` folder; none holds a secret (`SecretsHandoff`,
  `ProviderSecret`, `OAuthToken`, `provauth.Cached` all redact in `String`/`GoString`).
- Secrets reach the gateway only over the stdin pipe: the child's environment is an allow-list (`lib/ipc/env.js`),
  nothing on the command line.

Front token and internal port
- `frontToken`: 32 random bytes per start, memory only, pattern-checked on the Go side. The egress token is an HMAC of
  it, so `.data/egress.json` never holds the front token; the two MACs cannot be confused (different message forms).
- The internal listener closes any connection without a valid `X-ZP-Client` (`timingSafeEqual`, strict parser, 2 s
  first-request limit, no `100 Continue` before the check, no 400 on a malformed request).
- Go removes `X-Zp-Own` and `X-Zp-Shadow` from every client request and sets (replaces) `X-Zp-Client` and
  `X-Zp-No-Host` (`gw/front/front.go:270-287, 390-391`); a header named in `Connection` cannot remove them (Rewrite
  runs after the hop-by-hop removal). `/_zp/*`, including `POST /_zp/mcp/call`, answers only a request that has both
  the own flag and a valid MAC; the dashboard's handler has no `/_zp/` route. CONNECT and absolute-form are refused
  at the front.
- `/_zp/mcp/call` runs the same `callTool` the JS `/mcp` path runs (write tools still need `allowWrite`); the
  gateway authenticates the key before it calls it, as the JS did.

Egress proxy
- Token required before anything (connection dropped, no reply), constant-time compare, unset token refuses all.
- `X-ZP-Egress-*` removed from the outbound request (the token never reaches a target) and from the answer (a target
  cannot fake an error, the final URL or a redirect); caller `Host` replaced; hop-by-hop and `Proxy-*` removed.
- Target checks on the first URL and every redirect: absolute `http`/`https`, no user info, port 1..65535, at most 20
  redirects, fetch's method rewrite, allow-list per hop (probe: `enforce` refuses the off-list second hop).
- Public mode: proxy environment off, every address a name resolves to checked before the connect, the connect made to
  the checked address, and a `Control` hook re-checks it; numeric host forms are resolved and then checked; the
  private set matches `lib/net.js` by the shared fixture. No DNS-rebinding window.
- Client side: loopback goes direct (only the hosts the lock-down grants); under the lock-down a non-loopback call
  without a ready gateway fails with `EGRESS_UNAVAILABLE`, for fetch and for CONNECT; a caller's own
  `x-zp-egress-*` headers are overwritten; the status page says when the launch was not narrowed.
- No outbound call site in `lib/`, `server.js`, `mcp.js`, `bin/` bypasses `lib/egress.js` (grep for `fetch(`,
  `http(s).request/get`, `net.connect`, `tls.connect`): the three hits are loopback or the pgvector socket that
  `egress.connect` hands over.

Permission matrix
- Scope entries with a comma, a quote or a control character are refused before a launch line is built
  (`checkScope`); owner runtimes widen `run` only after an approval whose hash matches the command; the lock-down
  only narrows, and a CLI `--allow-net` keeps the set's metadata deny.
- `env: true`, `read: true` and the launchers are owner decisions recorded in `WHY` and on the Compliance page.

Provider auth
- RS256 with RSASSA-PKCS1-v1_5/SHA-256, `exp` one hour, key parsed from the secret at each refresh, never cached
  beyond the handoff; token endpoint redirects refused; 1 MiB answer cap; 20 s timeout; error text masked and never
  holds the URL or the address.

Dependencies
- `govulncheck ./...` in `gw/`: one standard-library advisory (L4), no module advisory (standard library only).
- `deno audit`: "No known vulnerabilities found". `npm audit --package-lock-only`: "found 0 vulnerabilities" (one dev
  dependency, `@types/node`).

## Not checked

- Who may call `ingest-url` and the other routes that feed URLs (dashboard roles): not traced; M1 path 2 assumes a
  user who can add a knowledge source.
- The lock-down launch line was not run here; the P5 results' M10 denial test (CI, Ubuntu and macOS) is relied on.
- Windows ACLs of the data folder; Linux `/proc` behaviour.
- The Go engine's provider request path (P4) beyond `gw/provauth` and the `/mcp` call; the dashboard's own auth.
- No fuzzing of the framing or the schema validator; no DAST; `semgrep`, `gitleaks` and the supply-chain registry
  check were not run (outside this brief's list).

## Decisions for the owner

- **Q3 (P7):** turn `egress.allowList` to `enforce` with the lock-down. With M1 fixed, `report` still lets the endpoint
  reach any off-list host; `enforce` refuses it. Recommended: yes, after a clean run on the preview.
- **L1:** whether to replace the pid check with an endpoint proof and drop `kill`/`tasklist` from `mcp-stdio`
  (recommended), which needs the AI clients registered again.
