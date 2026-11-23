# P7 security fixes

Date: 2026-11-23. Branch `migrate/p7-security-fixes`, cut from `migrate/p5-egress` at `c41a1a1`, to be merged after
PR #16. This note answers the findings of the P7 security review
(`docs/migration/2026-11-23-p7-security-review.md`, on `migrate/p7-security-review`; kept there so the two branches
do not both add that file). Each fix was written test first: the new test failed before the change and passes after.

| Finding | Commit | What changed | Tests |
|---|---|---|---|
| M1 | `525be1a` | Go refuses the cloud metadata service in every mode and on every path: the first URL and every redirect hop (`verdict`), `/check`, CONNECT, both endpoint dialers (names refused before resolution, every resolved address checked, a `Control` hook on the address dialled) and the engine's provider client (`RecordingTransport`, plus `RefuseMetadata` on its dialer). Addresses `169.254.169.254`, `fd00:ec2::254`, `169.254.170.2`, `100.100.100.200` (IPv4-mapped, IPv4-compatible and NAT64 forms too); names `metadata.google.internal`, `metadata.goog`, `metadata`, `instance-data`, `instance-data.ec2.internal` (any case, trailing dot). The allow-list cannot open them. Answered and recorded `refused` `EGRESS_PRIVATE` (`lib/net.js` already maps that code). | `gw/egress/metadata_test.go` (redirect hop in report, enforce-listing-them and public mode; `/check`; CONNECT; dialer; engine transport; host matcher), `gw/cmd/zaafpulse-gw/egress_test.go TestUpstreamDialerRefusesMetadata` |
| M1 | `b50bdf6` | `deno.json`: the three product sets deny `[fd00:ec2::254]`, `169.254.170.2` and `100.100.100.200` too; `WHY.broad.net` and the Compliance text name them. | `test/permissions.test.js` (deny list, a denial probe per new address per set) |
| M1 | `65ef060` | End-to-end proof: the real gateway in report mode, a class on `go`, a local fake that redirects to the metadata service; the caller gets `EGRESS_PRIVATE` and the hop is recorded `refused`, never `ok`. | `test/egress-metadata-gw.test.js` (shown failing with the Go fix reverted: `ETIMEDOUT`, the dial was attempted) |
| L3 | `a4573ee` | A cross-origin redirect hop drops `Cookie` and `Proxy-Authorization` with `Authorization`, as undici does; a credential once dropped stays dropped when a later hop returns to the first origin. Custom key headers are kept, as fetch keeps them (callers that send one use `fetchSameOrigin`). | `gw/egress/proxy_test.go TestCredentialHeadersDroppedCrossOrigin` |
| L2 | `af1b174` | `lib/ipc/ingest.js` writes a gateway `audit.event` governance entry only for an action in `GATEWAY_GOVERNANCE_ACTIONS` (empty today), and then with `actor: { via: "gateway" }`; anything else is dropped, logged once, counted (`governanceRefused()`) and acknowledged. The stub `--emit` tests use the explicit test-only option `testRawGovernance`. The `egress.refused` entry the control plane builds itself is unchanged. | `test/ipc-ingest-governance.test.js` |
| L4 | `319c55e` | `ci.yml`: all seven `go-version` pins are `1.27.4`. `release.yml` builds no Go yet (P6) and `gw/go.mod` has no toolchain line. `govulncheck` v1.10.0 under go1.27.4: no vulnerabilities (under go1.27.3: GO-2026-6859). | `test/ci-go-pin.test.js` |
| L1 | `85f15b6` | Go: `GET /hello?n=<64 hex>` on the egress listener answers, without the token, hex HMAC-SHA256(token, `"zaafpulse egress hello <port> <nonce>"`), port being the port the request arrived on (a relay to the live endpoint gets a proof for the wrong port). Dropped while no token is set. | `gw/egress/hello_test.go` (vector shared with JS, no-token answer, bad nonces, POST still token-checked) |
| L1 | `266ad56` | `lib/egress-discovery.js`: the pid check is gone; before every tool call mcp.js proves the endpoint (fresh nonce, constant-time compare, 2 s limit, own agent) and sends the token and the calls only after it passed; otherwise direct, logged once per file. `kill` and `tasklist` left the `mcp-stdio` run list (and its WHY text). `zaafpulse doctor` has an "MCP clients" row that names every client whose registration still grants `kill` or `tasklist` and says to run `zaafpulse register-mcp <client>` again. A registration made before this change keeps working and fails safe: the proof needs only the loopback net grant it already holds, and any failed proof (an older gateway without `/hello`, a squatter, a relay) goes direct. | `test/egress-discovery.test.js` (proof against real/squatter/relay/silent/closed listeners, loader, the squatter never gets the token through mcp.js, mcp.js under the real `mcp-stdio` Deno set, `oldRegistrations`, doctor) |

Not fixed here: none of the five findings is left open. The Info items (I1 to I8) and owner question Q3 (`enforce`
at the cut-over) were outside this brief.

Known limits, said plainly:

- With a proxy from the environment, the non-public forward path dials the proxy, so a name that only resolves to
  the metadata address is the proxy's to refuse; literal addresses and the listed names are still refused by Go.
- In report mode a name that resolves to the metadata address is recorded `would-refuse` (the allow-list verdict
  comes before resolution) and then `refused` at connect; literals and listed names are refused before that.
- Running the M1 tests against the pre-fix code sends connection attempts from this Mac to the metadata addresses
  (no cloud metadata service here); the fixed code refuses before any connect.
- The P5 spec's line "Authorization is dropped on a cross-origin hop" now reads as Authorization, Cookie and
  Proxy-Authorization (L3); the spec itself was left as written.

Local results (macOS arm64, Go 1.27.3 and 1.27.4, Node 24.16.0, Deno 2.9.8): `gofmt -l` clean; `go vet ./...` and
`GOOS=windows go vet ./...` clean; `go test -race ./...` in `gw/` all ok; `node --test test/*.test.js` 3148 pass,
0 fail, 3 skipped; `deno test -P=test test/*.test.js` 3148 passed, 0 failed, 3 ignored; `test/egress-diff.test.js`
passes on Node and Deno, with and without `HTTP_PROXY`; `deno task typecheck` clean. CI was not dispatched or awaited
(owner policy).
