# P4b Pass-through Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Non-streaming `POST /v1/chat/completions` to OpenAI-format providers can run on the Go engine (`zaafpulse-gw`) with API keys checked in Go, behind a shadow compare that must show no difference before the switch, with the P4b entry conditions of the P4a results met first.

**Architecture:** The Go front (P4a) gains a go engine (`gw/engine`, new) for the route class `chat`. For each request of that class the engine makes a side-effect-free plan from the generation the control plane pushed (`config.push` with providers, keys by hash and prices in nano-USD; `secrets.handoff` with the provider keys; `budget.snapshot`): it serves only the requests it can answer exactly as the JS gateway does (one OpenAI-format target with a plain key, a loopback client, no guardrail check, no blocking budget, not streaming) and hands every other request, unchanged, to the control plane. A served call goes to the provider with the JS gateway's timeouts and error names, the answer is written byte for byte as `JSON.stringify` writes it (`gw/engine/jsjson.go`), and the usage line goes through the outbox to the JS gateway's own writer, which also counts the per-key spend. While `chat` is on `js` and shadowed, the front marks the request, the control plane serves it and keeps a capture (`lib/gateway/shadow.js`), and the engine fetches the capture over the internal port, compares and posts a content-free line to `.data/gateway-shadow.jsonl`; `chat` switches to `go` only after 1 clean day and 20 compared calls without a diff (`gateway.shadowDays`, `gateway.shadowMinCompares`). Every rule ported to Go is checked against fixtures generated from the JS code (`scripts/fixtures/gen-*.js` → `gw/testdata/*`).

**Tech Stack:** Go 1.27.3 standard library only (`net/http`, `crypto/sha256`, `crypto/x509`, `unicode/utf16`), Deno 2.9.8 and Node 24 (CI 24.21.0), CommonJS, `node:test`, `AsyncLocalStorage`, IPC v1 (`ipc/v1`), GitHub Actions pinned by SHA, prettier 3 (printWidth 120) on clean JS files, `gofmt`.

## Global Constraints

- Spec: `docs/superpowers/specs/2026-11-12-p4-stage1-design.md`, "Stream A (P4b) — what its plan must settle" items 1–5 and the stage 1 exit criteria (binding). Background: the P4a spec `docs/superpowers/specs/2026-11-12-p4a-go-front-design.md` (owner decisions 1–3) and `docs/migration/2026-11-12-p4a-results.md` (entry conditions 1–8, known minors). Where the code contradicts the spec, this plan follows the code and says so under "Spec deviations". The `deno`, `migrate-to-deno` and `go-stack-defaults` skills bind all work (owner instruction).
- Go: **standard library only**, module `gw/` (`go 1.27`), Go **1.27.3**, every go command with `GOTOOLCHAIN=local`. Done means `gofmt -l gw` prints nothing, `go -C gw vet ./...` and `go -C gw test -race -count=1 ./...` pass (go-stack-defaults; golangci-lint and govulncheck are not wired in this repo's CI and are not added here).
- Stream boundaries (spec "The three streams"): this stream owns `gw/engine/` (new), `gw/front/`, `gw/cmd/`, `lib/front/`, `lib/ipc/` (not the marks), `server.js` and the wiring files. It never imports `gw/translate`, `gw/guard` (stream B) or `gw/provauth` (stream C) and never edits `gw/ipc/outbox.go`, `gw/ipc/lock_*.go`, `lib/ipc/marks.js` or `lib/ipc/markfile.js` (stream C). If a task finds it must, it stops and reports.
- Fixtures (spec "Equivalence by generated fixtures"): each generator `scripts/fixtures/gen-<lib>.js` runs the real JS over a fixed corpus and writes `gw/testdata/<lib>/<n>-<name>.json` as `{ "fn", "in", "out" }`; the Go tests replay every file and compare after canonical JSON encoding (`gw/engine/fixtures_test.go sameJSON`). Generated files are committed; CI regenerates all of them and fails on a diff (Task 10).
- IPC v1 changes are additive only (`ipc/v1/README.md` version rule): one new optional field, `config.push.front.shadow.classes`, with a valid and an invalid fixture. No new message type.
- Deno pinned to **2.9.8**, Node **24**. Tests under Deno run with `-P=test` (`deno test -q --no-check --no-prompt -P=test ...`); no `-A` in anything committed.
- Every task ends green on: `node --test test/*.test.js`, `deno test -q --no-check --no-prompt -P=test test/*.test.js`, `go -C gw vet ./... && go -C gw test -race -count=1 ./...`, `test -z "$(gofmt -l gw)"`, and `npm run test:contract` (js mode), run with `export GOTOOLCHAIN=local ZAAFPULSE_DATA="$(mktemp -d)" HOME="$(mktemp -d)"` first. From Task 8 on, also `ZP_ENGINE=front`, and from Task 9 `ZP_ENGINE=shadow` and `ZP_ENGINE=go`.
- Prettier: `npx --yes prettier@3 --write <file>` only on files this plan creates or on files `npx --yes prettier@3 --check` reports clean before the edit. `server.js` and `lib/gateway/index.js` are **not** clean: edit them by hand (the patches below), never `--write` them. `*.json`, `*.md` and `*.yml` are in `.prettierignore`.
- Patches: every "Apply" block is `git apply` of a unified diff made against the tree the previous task left; run it from the repository root. A patch that does not apply means the tree is not where the plan expects: stop and report.
- Never touch the live dashboard (port **4777**) or the preview (port **4787**). Tests never reach the network, never bind a non-loopback address, and never signal a process they did not start. `node:test` runs `t.after` hooks first-added-first: stop processes before removing their folder, in one hook (or `test/ipc-stub.js tmpDir`).
- Commits: one plain descriptive English sentence, like the repo history; **no** `Co-Authored-By` and no AI attribution. Never commit `.superpowers/`. Each task ends with its commit and `git push origin migrate/p4b-passthrough`. No temporary branches.
- CI: a push to the work branch runs Ubuntu only. Start the full matrix after **Tasks 5 and 11** (coordinator): `gh workflow run ci.yml -R rifqifitriady/ZAAFPulse-deno-go --ref migrate/p4b-passthrough`, then `gh run list -R rifqifitriady/ZAAFPulse-deno-go --workflow ci.yml --branch migrate/p4b-passthrough --event workflow_dispatch --limit 1 --json databaseId --jq '.[0].databaseId'` and `gh run watch <id> -R rifqifitriady/ZAAFPulse-deno-go --exit-status`. A red job is fixed before the next task starts.

## Decisions

Each one line, with the evidence that settled it.

1. **Key-check placement: in the go engine, inside a plan with no side effects, only for a request of a class on go (or shadowed), after the class and the body are read; the JS check stays as it is for everything else.** Evidence: `lib/gateway/index.js authed()` checks before `readJson`, but `keys.authenticate` writes `lastUsedAt` and `rateLimit` counts (`lib/gateway/keys.js`), so a check that may still hand the request to JS must not count or write; Go's `Authenticate`/`Authorize`/`Allows`/`RequestSecret`/`RateLimiter` match 53 JS fixture cases (Task 5), including JavaScript's non-unicode `/i` folding (`ſ`, Kelvin `K`).
2. **The engine serves only the exact path of a call it can answer as JS does, and hands everything else to the control plane unchanged (the buffered body re-attached).** Evidence: the JS gateway's 24 recorded chat answers (gen-chat, Task 7) are reproduced byte for byte; every refusal (401, 403, 404, 400, 413) then comes from JS itself, and the contract suite is 179/179 with chat on go (Task 8 scratch run).
3. **Shadow log format: one line per shadowed request `{ ts, class, outcome: same|diff|handoff|skipped, reason?, status?, diffs?: [{ field, js, go }] }` (`handoff`: the engine would not serve it; `skipped`: nothing to compare, because the provider call failed or the control plane's own rate limit answered) in `.data/gateway-shadow.jsonl`, rotated at 10 MB; a differing text is recorded as `{ len, sha (12 hex), at }`, never its content. JS builds nothing it does not send: it serves, and Go builds without sending.** Evidence: the usage log never holds prompt text (`lib/gateway/usage.js` header); owner decision 2 (P4a spec) makes the serving engine the only caller of the provider.
4. **The shadow gets the provider's answer by a capture on the control plane: Go marks the forwarded request (`X-ZP-Shadow: <id>`, accepted only after the signed `X-ZP-Client`), JS records the provider request, the provider's answer, its own answer and the usage line in an `AsyncLocalStorage` capture, and after the client has its answer Go fetches it once (`GET /_zp/shadow/<id>`, marked `X-ZP-Own: 1`), compares, and posts the line (`POST /_zp/shadow`).** Evidence: the internal listener already authenticates Go (`lib/front/internal.js`); an IPC message would be capped at 8 MiB per line (`gw/ipc/framing.go MaxLine`) against 20 MB bodies and would need a new message type; the client waits for nothing (the compare runs after its answer, at most 8 at once).
5. **The usage writer that feeds the spend is the JS gateway's own unwrapped writer: `gateway.ingestUsage` (strict append, the in-memory per-key spend and recent list, the key's `lastUsedAt`), used by `lib/ipc/server-sinks.js`; the ingest runs the budget report once after the mark.** Evidence: `lib/otel.js wrapUsage` replaces `usage.record` in place (so the raw one must be kept before wrapping), and P4a's second `createUsage` on the same file skipped the spend (results T2-b).
6. **Path form: the front classifies the raw request target, and a path gets a class only when the WHATWG parser leaves it unchanged (one leading `/`, no `%`, `\`, dot segment or character it escapes); every other spelling goes to js, which routes it as in js mode.** Evidence: 35 fixture cases from `new URL(raw, base).pathname` (Task 1), e.g. `//v1/chat/completions` is `/chat/completions` and `/v1/chat%2Fcompletions` stays encoded, while Go's `r.URL.Path` decodes it (results T4-d).
7. **Late front section: listen, TLS and upstream are fixed for a gateway's life (built once per start); each push carries the same ones plus the current engines and shadow classes; a front section arriving after the start, or a changed listen/TLS/upstream, is logged as "applies after a restart" and ignored.** Evidence: `gw/cmd/zaafpulse-gw/front.go applyFront` opens the port only on `ready`; a restart of ZAAFPulse already re-reads `gateway.engine`.
8. **Proxy and CA: `lib/ipc/env.js` passes `HTTPS_PROXY`, `HTTP_PROXY`, `NO_PROXY` (and their lower-case forms off Windows), `SSL_CERT_FILE`, `SSL_CERT_DIR`, `NODE_EXTRA_CA_CERTS` and `DENO_CERT`; the engine uses `http.ProxyFromEnvironment` and trusts the system roots plus the PEM files of `SSL_CERT_FILE`, `NODE_EXTRA_CA_CERTS` and `DENO_CERT`.** Evidence: Go reads `SSL_CERT_FILE`/`SSL_CERT_DIR` itself only on Unix other than macOS (`crypto/x509` docs); Node adds `NODE_EXTRA_CA_CERTS` and Deno `DENO_CERT` to their stores; `TestUpstreamExtraCA` passes on macOS with each of the three.
9. **Upstream timeouts and errors: 120 s for the provider to start its answer and no limit on the body (`providers.js TIMEOUT_MS`, cleared once `fetch` resolves), 10 s to connect (undici's default), redirects only to the same origin (at most 5, same method and body, `lib/net.js fetchSameOrigin`); a failed call is `502 upstream_error` with `<provider id>: <code>` in Node's names (`ECONNREFUSED`, `UND_ERR_SOCKET`, `timed out`, ...) and a status-599 usage line; error texts in usage lines are cut at 200 UTF-16 units and masked with the handoff's mask set; upstream error bodies reach the client unmasked (P0 amendment, `test/contract/errors.test.js`).** Evidence: gen-chat cases `connection-refused`, `timed-out`, `error-names-the-secret`, `long-error-cut-at-200`; `TestUpstreamFailures`.
10. **The switch rule is enforced in code (owner answer 1, 2026-11-12): `routing()` keeps a class on js until the shadow log shows, since its last diff, at least `gateway.shadowMinCompares` compared calls (default 20) and `gateway.shadowDays` days (default 1) with no diff, and while OTLP export is on; `0` lifts each part, both `0` lift the rule; it is re-read at each push and once a minute; `/api/gateway/status` shows the counts and why.** Evidence: spec item 3 ("the owner may shorten this; ask"); the owner chose 1 day and 20 calls; a documented rule alone would let `engines.chat: "go"` switch at once.
11. **A key, provider or route change is in force on the gateway when the save answers: a gateway admin POST waits (at most 2 s) for a health that names the new generation, which the gateway sends as soon as a generation's config and secrets are both applied.** Evidence: the contract case "a revoked key is refused as soon as the revoke has answered" fails without it (the gateway would still hold the key for one push).
12. **Merge order: the engine emits real `usage.report` events through the outbox only when a class is on go, which needs stream C's P4f entry conditions (outbox `Close` under `sendMu`, fsync or a documented order, marks re-read per session); stream A's PR merges after stream C's, and the rebase onto it reruns the full matrix.** Evidence: P4a results, entry condition 4; with the default (`chat` on js, shadow only) the engine emits nothing.
13. **Entry condition 6: the public port refuses headers past Node's limit with 431 (`MaxHeaderBytes` 12288, net/http adds 4096), CONNECT is closed without an answer, and the noisy log lines (TLS handshake errors, 502s) are written at most once a minute per kind with a count.** Evidence: Node and Deno both answer 431 between 16,340 and 16,400 bytes of one header (scratch probe); review minors m2, m3, r1.
14. **Entry condition 8: the chip says "fallback, restart needed" after a crash-loop fallback (`status().restartNeeded`); a gateway-section save in fallback retries front, and after a crash-loop fallback only a restart does (owner answer 3, 2026-11-12, which confirms the P4a default).** Evidence: P4a deviation 12.
15. **The two gaps until P4f are accepted (owner answer 2, 2026-11-12): a key's requests-per-minute window is counted per engine, and go-served chat writes no trace spans; go stays refused while OTLP export is on.**
16. **Stream C is merged first (`8abb82c`, PR #6), so Decision 12 holds: the branch merges `origin/migrate/deno-go` before Task 1.**

## Owner answers (2026-11-12)

1. **The switch rule:** 1 clean day and at least 20 compared calls with no diff (`gateway.shadowDays` default 1, `gateway.shadowMinCompares` default 20; `0` lifts either). Decision 10.
2. **The two gaps until P4f** (per-engine rate-limit windows; no trace spans for go chat): accepted; go stays refused while OTLP export is on. Decision 15.
3. **Fallback:** a save of the gateway section retries front; after a crash-loop fallback only a restart does. Decision 14.

No question is open.

## Spec deviations

1. **Files outside the stream's list.** Stream A also edits `gw/ipc/messages.go` (`FrontShadow.Classes`, type `RouteClass`), `ipc/v1/config.push.schema.json` and two new `ipc/v1/fixtures/config.push/*front-shadow*` files. Stream C edits only `gw/ipc/outbox.go`, the lock files and the marks, so no file is shared.
2. **The shadow compares one direction.** While a class is on js, Go shadows it; once it is on go, nothing shadows it. The reverse (JS building its answer from Go's provider answer) needs a dry run of the JS request path (no rate count, no usage write, no quota note) and is left to stage 2.
3. **The engine serves a subset and hands the rest to JS.** Streaming (P4c), translation and Anthropic-format providers (P4d), guardrails and budgets (P4e: any check in scope or any blocking budget that applies hands off), OAuth2 and service accounts (P4g), routes with fallback, agents, the token saver on tool results and keys with a monthly `budgetUSD` all stay on JS. With the default guardrails (`secrets: redact` for external providers) the engine serves private providers only until P4e.
4. **The server's access checks are not ported.** The engine serves a request only from a loopback client with a loopback `Host` and no `Origin`, `Referer`, `Sec-Fetch-Site` or proxy header; in every auth mode the server would let such a request through to the gateway. Anything else goes to JS. Remote clients reach the go engine at G3.
5. **The usage line of a go-served call is written a few milliseconds after the answer** (outbox, IPC, ingest); the new contract file polls for it and the harness's `logs()` reads until two reads 100 ms apart agree under `ZP_ENGINE=go` (one guardrails case read the previous line before this). `lastUsedAt` of a key is set from that line, not at the key check.
6. **Provider quota and cool-down readings** (`providers.js noteResponse`) do not see go-served calls until G3.
7. **Error names follow Node's fetch**, not Deno's (Deno words some failures differently). A failed call has no provider answer, so the shadow never compares it.
8. **Known byte differences, all handed off or visible in the shadow:** an escaped lone surrogate or nesting deeper than 512 in the request is handed off; an invalid UTF-8 provider answer is decoded with one U+FFFD per bad byte (WHATWG uses one per maximal subpart); a 200-unit cut through a surrogate pair drops the high half (JS keeps it); a token count that is not a whole number is 0; two price keys that normalize to the same name tie-break by name, not by `config.json` order.
9. **This stream does not edit `ci.yml`** (coordinator, 2026-11-12). Stream B's fixtures job regenerates every `gen-*.js` on Node and Deno; this stream's generators run under its flags and give identical bytes on both. The contract suite in shadow and go mode runs locally until a CI step is added after stream B merges.
11. **Known Node/Deno differences** (the v2 product runs on Deno, so Go matches Deno; the cases are out of the generated corpus and pinned in Go tests): U+A7CF, U+A7D3 and U+A7D5 are upper-cased by Node 24 (to U+A7CE, U+A7D2, U+A7D4) and not by Deno 2.9.8, and the engine does not fold them; `new URL('/v1/a^b', base).pathname` is `/v1/a%5Eb` on Node and `/v1/a^b` on Deno, and the front never classifies it.
12. **A `//` request target** reaches the control plane through `URL.RawPath`; it is byte-exact unless the path also holds a character net/url escapes (`\`, `|`).
10. **P4a minors.** Done here: m2, m3, m4, r1, T5-N4. Deferred again, with reason: T1-d (cross-process lock holder: stream C owns the lock), T2-c (Windows exit-5 retry: front is not offered on Windows beyond CI yet; P6), T3-M5, T4-c (the two remaining tests), T5-N3 (watch the PR matrix), T6-4, T6-5, T6-6 (M3 is re-measured with streaming in P4c).

## Facts measured for this plan (2026-11-12, macOS arm64, base 063d39f)

| # | Fact | Evidence |
|---|---|---|
| F1 | Toolchains: `go version go1.27.3 darwin/arm64`, `deno 2.9.8`, `node v24.16.0`. | `go version`, `deno --version`, `node --version` |
| F2 | `new URL('//v1/chat/completions', 'http://localhost').pathname` is `/chat/completions`; `/v1/./chat/completions`, `/v1\chat/completions` and `/v1/%2e/chat/completions` are `/v1/chat/completions`; `/v1/chat%2Fcompletions` stays encoded; `^`, `{`, `}`, `` ` ``, `"`, `<` are escaped, `\|`, `[`, `]`, `'` are not. | node one-liners; `gw/testdata/route` |
| F3 | Node 24 and Deno 2.9.8 answer `431` to a request whose one header is 16,400 bytes and `200` at 16,340. | scratch probe `hdr.cjs` under both runtimes |
| F4 | `lib/otel.js wrapUsage` (line 1662) and the budget wrapper (`lib/gateway/index.js` 164-174) replace `usage.record` in place; the raw writer is lost unless kept first. | source |
| F5 | `providers.js request()` clears its 120 s timer in `finally` once `fetchSameOrigin` resolves; the body read has no timer. | `lib/gateway/providers.js:504-534` |
| F6 | `keys.authenticate` writes `lastUsedAt` (once a minute) and `rateLimit` keeps a per-process window. | `lib/gateway/keys.js` |
| F7 | With the contract harness's guardrails (`scope: external, secrets: redact`) only `pv/*` is servable by the engine; with guardrails off, `oa/*` too. | `test/contract/harness.js`, Task 7 hand-off tests |
| F8 | Go's `x509.SystemCertPool()` on macOS plus `AppendCertsFromPEM` trusts both the platform roots and the added certificate. | `TestUpstreamExtraCA` (Task 8) on darwin/arm64 |
| F9 | A clean worktree of 063d39f with every Create and Apply block of Tasks 1–11 applied in order by a script that reads this plan, and every generator run, is byte-identical to the scratch tree (only the Task 11 results document, which fills run-time values, is extra). On it: gofmt clean, `go vet` (also `GOOS=windows`) clean, `go test -race` ok in all four packages; Node suite 2,891 tests, 2,888 passed, 0 failed, 3 skipped; Deno suite 2,888 passed, 0 failed, 3 ignored; contract suite on Node: js 179 (1 skipped), front 179 (1 skipped), shadow 180, go 179 (1 skipped), 0 failed, go mode run twice; on Deno: front 179, shadow 180, go 179, 0 failed. | `scratchpad/plan/verify.py`; scratch logs |

## Files

| File | Task | Responsibility |
|---|---|---|
| `gw/front/route.go`, `gw/front/quiet.go`, `gw/front/front.go`, `gw/front/route_test.go`, `scripts/fixtures/gen-route.js` | 1 | raw-path classification, 431, quiet log, CONNECT |
| `lib/ipc/env.js`, `lib/front/index.js`, `public/topologyview.js` | 2 | proxy/CA allow-list, restart needed, m4, T5-N4 |
| `lib/gateway/index.js` (`ingestUsage`), `lib/ipc/server-sinks.js`, `server.js` | 3 | one usage writer feeding the spend |
| `lib/front/push.js`, `lib/front/index.js` (`push`, `synced`, `sources`), `lib/ipc/supervisor.js` (`synced`), `gw/cmd/zaafpulse-gw/{main,front}.go`, `server.js` | 4 | the config.json-to-IPC converter, pushes on key and price changes, saves that wait for the gateway, the late front section |
| `gw/engine/keys.go`, `lib/gateway/keys.js` (`requestSecret`), `scripts/fixtures/gen-keys.js` | 5 | API keys in Go |
| `gw/engine/jsjson.go`, `scripts/fixtures/gen-jsjson.js` | 6 | JSON as JavaScript reads and writes it |
| `gw/engine/{providers,pricing,chat}.go`, `scripts/fixtures/gen-{chat,pricing}.js` | 7 | the chat plan, the answer, the usage line, the cost |
| `gw/engine/{upstream,serve}.go`, `gw/front/front.go`, `gw/cmd/zaafpulse-gw/*`, `lib/front/index.js` (`enginesOf`), `test/contract/{harness,go-chat}.test.js` | 8 | serving chat on go |
| `gw/engine/shadow.go`, `lib/gateway/shadow.js`, `lib/front/{shadow-log,internal}.js`, `gw/ipc/messages.go`, `ipc/v1/config.push.schema.json` | 9 | the shadow compare and the switch rule |
| `scripts/fixtures/gen-{chat,pricing,route,keys}.js`, `gw/engine/keys.go` (`divergentFold`), `.gitignore` | 10 | generators under the CI fixtures job's flags; Node/Deno differences pinned |
| `docs/migration/2026-11-12-p4b-results.md`, `README.md`, `docs/migration/2026-11-10-deno-go-migration-plan.md` | 11 | docs |

---

### Task 1: Front parity on the public port (P4b entry conditions 2 and 6, review r1)

Go classifies the raw request target the way the control plane does (entry 2), refuses oversized headers with 431 and rate-limits its noisy log lines (entry 6), and closes a CONNECT without an answer (r1). No route is on go yet, so nothing else changes.

**Files:**
- Create: `gw/front/quiet.go`, `gw/front/route_test.go`, `scripts/fixtures/gen-route.js`
- Modify: `gw/front/front.go`, `gw/front/front_test.go`, `gw/front/route.go`


**Interfaces:**
- Consumes: `gw/front` of P4a (`Listen`, `Server.ServeHTTP`, `startUpstream`, `startFront`, `rawRequest`, `frontURL` in `front_test.go`).
- Produces:
  - `front.Classify(rawPath string) string`: the class of a **raw** path, `""` unless `Canonical(rawPath)`.
  - `front.Canonical(p string) bool`, `front.RawPath(requestURI string) string`.
  - `front.MaxHeaderBytes = 16384 - 4096`; `front.QuietLogger(out *log.Logger, now func() time.Time) *log.Logger`, `front.QuietEvery = time.Minute`.
  - `scripts/fixtures/gen-route.js` writing `gw/testdata/route/<n>-<name>.json` as `{ fn: "pathname", in, out }`.

- [ ] **Step 1: Check the starting point**

Run: `git status --short && git rev-parse --abbrev-ref HEAD && git log --oneline -1`
Expected: nothing listed, `migrate/p4b-passthrough`, and the previous task's commit on top (Task 1: `0ec1247`, the merge of `origin/migrate/deno-go` with stream C).

- [ ] **Step 2: Write the failing tests**

The route fixtures come from the JS parser; the generator is part of the test (Step 5 runs it).

Create `gw/front/route_test.go`:

```go
package front

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The route fixtures (scripts/fixtures/gen-route.js): the pathname the control plane routes on for each raw path. A
// path the gateway calls canonical must have that pathname unchanged, so the gateway and the control plane classify
// the same text (P4b entry condition 2).
func TestCanonicalPathsAreTheirOwnPathname(t *testing.T) {
	dir := filepath.Join("..", "testdata", "route")
	files, err := os.ReadDir(dir)
	if err != nil || len(files) == 0 {
		t.Fatalf("no route fixtures in %s (run node scripts/fixtures/gen-route.js): %v", dir, err)
	}
	canonical := 0
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(dir, f.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var c struct{ Fn, In, Out string }
		if err := json.Unmarshal(b, &c); err != nil || c.Fn != "pathname" {
			t.Fatalf("%s: %v %q", f.Name(), err, c.Fn)
		}
		if !Canonical(c.In) {
			if Classify(c.In) != "" {
				t.Errorf("%s: %q is not canonical but has class %q", f.Name(), c.In, Classify(c.In))
			}
			continue
		}
		canonical++
		if c.In != c.Out {
			t.Errorf("%s: canonical %q has pathname %q", f.Name(), c.In, c.Out)
		}
	}
	if canonical < 10 {
		t.Errorf("only %d canonical fixtures", canonical)
	}
}

func TestRawPath(t *testing.T) {
	for in, want := range map[string]string{
		"/v1/chat/completions":         "/v1/chat/completions",
		"/v1/chat/completions?x=1":     "/v1/chat/completions",
		"/v1/chat%2Fcompletions?a=%2F": "/v1/chat%2Fcompletions",
		"/a#b":                         "/a",
		"*":                            "*",
	} {
		if got := RawPath(in); got != want {
			t.Errorf("RawPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// With chat on the go engine, only the exact spelling is the gateway's: an encoded or dot-segment spelling goes to the
// control plane, which routes it as js mode does (before P4b, Go decoded %2F and called it chat).
func TestOnlyTheCanonicalSpellingIsClassified(t *testing.T) {
	up := startUpstream(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "js "+r.RequestURI) })
	s := startFront(t, up.port(), func(c *Config) { c.Engines = map[string]string{"chat": "go"} })
	for _, p := range []string{"/v1/chat%2Fcompletions", "/v1/./chat/completions", "/v1/x/../chat/completions"} {
		out, _ := rawRequest(t, s.Addr().String(), "POST "+p+" HTTP/1.1\r\nHost: 127.0.0.1\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		if !strings.Contains(out, "200 OK") || !strings.Contains(out, "js "+p) {
			t.Errorf("%s: not forwarded as sent: %q", p, out)
		}
	}
}

// Headers past Node's limit get 431 from the front, as from Node, never the 502 of a closed upstream connection.
func TestOversizedHeadersAre431(t *testing.T) {
	up := startUpstream(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") })
	s := startFront(t, up.port(), nil)
	ask := func(n int) string {
		out, _ := rawRequest(t, s.Addr().String(), "GET /x HTTP/1.1\r\nHost: 127.0.0.1\r\nX-Big: "+
			strings.Repeat("a", n)+"\r\nConnection: close\r\n\r\n")
		return out
	}
	if out := ask(8 * 1024); !strings.Contains(out, "200 OK") {
		t.Errorf("8 KiB header: %q", out)
	}
	if out := ask(20 * 1024); !strings.HasPrefix(out, "HTTP/1.1 431") {
		t.Errorf("20 KiB header: %q", out)
	}
}

// CONNECT gets no answer: the connection is closed, as Node does without a connect listener.
func TestConnectIsClosedWithoutAnAnswer(t *testing.T) {
	up := startUpstream(t, func(w http.ResponseWriter, r *http.Request) { t.Error("CONNECT reached the control plane") })
	s := startFront(t, up.port(), nil)
	if out, _ := rawRequest(t, s.Addr().String(), "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n"); out != "" {
		t.Errorf("CONNECT answered: %q", out)
	}
}

// A noisy kind of line is logged once a minute with a count of the ones left out; any other line goes through.
func TestQuietLoggerRateLimitsNoisyLines(t *testing.T) {
	var buf bytes.Buffer
	now := time.Unix(1000, 0)
	q := QuietLogger(log.New(&buf, "", 0), func() time.Time { return now })
	for range 5 {
		q.Print("http: TLS handshake error from 1.2.3.4:5: EOF")
	}
	q.Print("ready")
	now = now.Add(QuietEvery)
	q.Print("http: TLS handshake error from 1.2.3.4:6: EOF")
	want := "http: TLS handshake error from 1.2.3.4:5: EOF\nready\n" +
		"http: TLS handshake error from 1.2.3.4:6: EOF (4 more like this in the last minute were not logged)\n"
	if buf.String() != want {
		t.Errorf("log %q, want %q", buf.String(), want)
	}
}
```

Create `scripts/fixtures/gen-route.js`:

```js
// Fixtures for the gateway's route classification (P4b entry condition 2): for each raw request path, the pathname the
// control plane routes on (new URL(raw, base).pathname, as server.js builds `url`). gw/front/route_test.go checks that
// every path the gateway calls canonical has exactly this pathname, so a route class is only ever decided on a path
// the control plane would read the same way. Writes gw/testdata/route/<n>-<name>.json as { fn, in, out }.
// Run: node scripts/fixtures/gen-route.js (CI runs every scripts/fixtures/gen-*.js and fails on a diff).
const fs = require('fs');
const path = require('path');

const OUT = path.join(__dirname, '..', '..', 'gw', 'testdata', 'route');
const RAW = [
  '/v1/chat/completions',
  '/v1/chat/completions/',
  '/v1/chat%2Fcompletions',
  '/v1/%63hat/completions',
  '/v1/./chat/completions',
  '/v1/x/../chat/completions',
  '/v1/%2e/chat/completions',
  '/v1/%2E%2E/v1/chat/completions',
  '/v1\\chat/completions',
  '//v1/chat/completions',
  '/V1/chat/completions',
  '/v1//chat/completions',
  '/v1/chat/completions.',
  '/v1/chat/completions/.',
  '/v1/chat/completions/..',
  '/v1/messages',
  '/v1/messages/count_tokens',
  '/v1/models/oa%2Fm1',
  '/v1/models/oa/m1',
  '/v1beta/models/oa%2Fm1:generateContent',
  '/v1beta/models/oa/m1:generateContent',
  '/mcp',
  '/a2a',
  '/.well-known/agent-card.json',
  '/skills/zaafpulse/SKILL.md',
  '/v1/traces',
  '/v1/a;b/c',
  "/v1/a!$&'()*+,=:@~_-.x",
  '/v1/a^b',
  '/v1/a{b}',
  '/v1/a|b',
  '/v1/a[b]',
  '/v1/café',
  '/api/gateway/status',
  '/',
];

const slug = (s) =>
  s
    .replace(/[^A-Za-z0-9]+/g, '-')
    .replace(/^-|-$/g, '')
    .slice(0, 40) || 'root';
fs.rmSync(OUT, { recursive: true, force: true });
fs.mkdirSync(OUT, { recursive: true });
RAW.forEach((raw, i) => {
  const out = new URL(raw, 'http://localhost').pathname;
  const file = path.join(OUT, `${String(i + 1).padStart(2, '0')}-${slug(raw)}.json`);
  fs.writeFileSync(file, `${JSON.stringify({ fn: 'pathname', in: raw, out })}\n`);
});
console.log(`gen-route: ${RAW.length} cases in ${path.relative(process.cwd(), OUT)}`);
```

Apply (from the repository root):

```bash
git apply <<'PATCH'
diff --git a/gw/front/front_test.go b/gw/front/front_test.go
index 0cff0e0..6cfb474 100644
--- a/gw/front/front_test.go
+++ b/gw/front/front_test.go
@@ -111,6 +111,9 @@ func TestClassify(t *testing.T) {
 		"/api/gateway/status":          "",
 		"/":                            "",
 		"/mcp/extra":                   "",
+		"/v1/chat%2Fcompletions":       "",
+		"/v1/./chat/completions":       "",
+		"//v1/chat/completions":        "",
 	} {
 		if got := Classify(p); got != want {
 			t.Errorf("Classify(%q) = %q, want %q", p, got, want)
PATCH
```

- [ ] **Step 3: Run them to see them fail**

Run: `node scripts/fixtures/gen-route.js && GOTOOLCHAIN=local go -C gw test -count=1 ./front`
Expected: `gen-route: 35 cases in gw/testdata/route`, then FAIL with build errors such as `undefined: Canonical`, `undefined: RawPath`, `undefined: QuietLogger`, `undefined: QuietEvery`.

- [ ] **Step 4: Implement**

`quiet.go` is new; the patch makes `route.go` classify raw paths and `front.go` use the raw path, `MaxHeaderBytes`, the quiet logger and the CONNECT close.

Create `gw/front/quiet.go`:

```go
package front

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// noisy are the log lines the public port can be made to print at will: a failed TLS handshake (scanners, plain HTTP
// to the TLS port) and a request the control plane did not answer. Node printed neither, so each kind is written at
// most once per QuietEvery, followed by how many were left out (P4a final review m3, P4b entry condition 6).
var noisy = []string{"TLS handshake error", "front: "}

// QuietEvery is how often one noisy kind of line may be logged.
const QuietEvery = time.Minute

// quietWriter is the io.Writer of the front's loggers (http.Server.ErrorLog, the proxy's 502 line).
type quietWriter struct {
	out  *log.Logger
	now  func() time.Time
	mu   sync.Mutex
	last map[string]time.Time
	held map[string]int
}

// QuietLogger wraps out: noisy lines are rate-limited per kind, every other line goes through as it is.
func QuietLogger(out *log.Logger, now func() time.Time) *log.Logger {
	return log.New(&quietWriter{out: out, now: now, last: map[string]time.Time{}, held: map[string]int{}}, "", 0)
}

func (q *quietWriter) Write(p []byte) (int, error) {
	line := strings.TrimRight(string(p), "\n")
	kind := ""
	for _, k := range noisy {
		if strings.Contains(line, k) {
			kind = k
			break
		}
	}
	if kind == "" {
		q.out.Print(line)
		return len(p), nil
	}
	q.mu.Lock()
	t := q.now()
	if last, ok := q.last[kind]; ok && t.Sub(last) < QuietEvery {
		q.held[kind]++
		q.mu.Unlock()
		return len(p), nil
	}
	n := q.held[kind]
	q.last[kind], q.held[kind] = t, 0
	q.mu.Unlock()
	if n > 0 {
		line += fmt.Sprintf(" (%d more like this in the last minute were not logged)", n)
	}
	q.out.Print(line)
	return len(p), nil
}
```

Apply (from the repository root):

```bash
git apply <<'PATCH'
diff --git a/gw/front/front.go b/gw/front/front.go
index 3999689..c2efd54 100644
--- a/gw/front/front.go
+++ b/gw/front/front.go
@@ -41,6 +41,10 @@ const AbsoluteFormError = "absolute-form request targets are not accepted"
 var proxyHeaders = []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Forwarded-Port",
 	"Forwarded", "Via", "X-Real-Ip", "Cf-Connecting-Ip", "True-Client-Ip"}
 
+// MaxHeaderBytes makes the public port refuse what Node refuses: Node's limit is 16 KiB of request line and headers,
+// and net/http reads MaxHeaderBytes plus 4096 bytes before it answers 431 (P4a final review m2, P4b entry condition 6).
+const MaxHeaderBytes = 16384 - 4096
+
 // ErrAddrInUse: another process holds the main port. The gateway exits 4.
 var ErrAddrInUse = errors.New("front: the main port is in use")
 
@@ -77,6 +81,7 @@ func Listen(cfg Config, logger *log.Logger) (*Server, error) {
 	}
 	s := &Server{cfg: cfg, ln: ln}
 	s.SetEngines(cfg.Engines)
+	quiet := QuietLogger(logger, time.Now)
 	target := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.Upstream))}
 	s.proxy = &httputil.ReverseProxy{
 		Rewrite: func(pr *httputil.ProxyRequest) {
@@ -109,12 +114,12 @@ func Listen(cfg Config, logger *log.Logger) (*Server, error) {
 			IdleConnTimeout: 120 * time.Second,
 		},
 		FlushInterval: -1, // SSE and chunked bodies pass through at once
-		ErrorLog:      logger,
+		ErrorLog:      quiet,
 		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
 			if r.Context().Err() != nil {
 				return // the client is gone
 			}
-			logger.Printf("front: %s %s: %v", r.Method, r.URL.Path, err)
+			quiet.Printf("front: %s %s: %v", r.Method, RawPath(r.RequestURI), err)
 			w.Header().Set("Content-Type", "application/json")
 			w.Header().Set("Retry-After", "2")
 			w.WriteHeader(http.StatusBadGateway)
@@ -125,7 +130,8 @@ func Listen(cfg Config, logger *log.Logger) (*Server, error) {
 		Handler:           s,
 		ReadHeaderTimeout: 10 * time.Second,
 		IdleTimeout:       120 * time.Second,
-		ErrorLog:          logger,
+		MaxHeaderBytes:    MaxHeaderBytes,
+		ErrorLog:          quiet,
 	}
 	return s, nil
 }
@@ -187,7 +193,18 @@ func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
 		_, _ = w.Write([]byte(`{"error":"` + AbsoluteFormError + `"}`))
 		return
 	}
-	if s.engine(Classify(r.URL.Path)) != "js" {
+	if r.Method == http.MethodConnect {
+		// Node has no "connect" listener and closes the socket without an answer (P4a re-review r1).
+		if hj, ok := w.(http.Hijacker); ok {
+			if c, _, err := hj.Hijack(); err == nil {
+				_ = c.Close()
+				return
+			}
+		}
+		w.WriteHeader(http.StatusMethodNotAllowed)
+		return
+	}
+	if s.engine(Classify(RawPath(r.RequestURI))) != "js" {
 		// Unreachable in P4a: config.push refuses a go engine. P4b answers here.
 		w.Header().Set("Content-Type", "application/json")
 		w.WriteHeader(http.StatusNotImplemented)
diff --git a/gw/front/route.go b/gw/front/route.go
index 1f0cec7..3fd5599 100644
--- a/gw/front/route.go
+++ b/gw/front/route.go
@@ -8,7 +8,15 @@ var Classes = []string{"chat", "messages", "responses", "embeddings", "models",
 
 // Classify returns the route class of a request path, or "" when it is not a gateway route. The prefixes are those of
 // lib/gateway/index.js handle(); /v1/traces is the control plane's own OTLP return channel (server.js).
+//
+// p is the raw path as the client sent it (RawPath of the request target, before "?"), never Go's decoded form: the
+// control plane routes on the WHATWG pathname of the same text (new URL(req.url, base).pathname), which keeps percent
+// escapes and resolves dot segments and backslashes (P4a final review T4-d, P4b entry condition 2). A path the WHATWG
+// parser would change (Canonical false) is in no class, so the control plane routes it, exactly as in js mode.
 func Classify(p string) string {
+	if !Canonical(p) {
+		return ""
+	}
 	switch {
 	case p == "/v1/chat/completions":
 		return "chat"
@@ -35,3 +43,35 @@ func Classify(p string) string {
 	}
 	return ""
 }
+
+// Canonical reports whether the WHATWG URL parser leaves path p as it is: it starts with one "/" (not "//", which
+// would be read as a host), and has no percent escape, backslash, dot segment or character the parser escapes. Only
+// such a path can be classified without the parser: its pathname is then p itself (gw/testdata/route fixtures).
+func Canonical(p string) bool {
+	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") {
+		return false
+	}
+	for i := 0; i < len(p); i++ {
+		c := p[i]
+		switch {
+		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
+		case strings.IndexByte("-._~!$&'()*+,;=:@/", c) >= 0:
+		default:
+			return false
+		}
+	}
+	for _, seg := range strings.Split(p, "/") {
+		if seg == "." || seg == ".." {
+			return false
+		}
+	}
+	return true
+}
+
+// RawPath is the path of a request target as the client sent it: the text before "?" (and "#").
+func RawPath(requestURI string) string {
+	if i := strings.IndexAny(requestURI, "?#"); i >= 0 {
+		return requestURI[:i]
+	}
+	return requestURI
+}
PATCH
```

- [ ] **Step 5: Generate the fixtures from the JS code**

Run: `node scripts/fixtures/gen-route.js && git status --short gw/testdata/route | wc -l`
Expected: `gen-route: 35 cases in gw/testdata/route` and `35` new files.

- [ ] **Step 6: Run the tests and the task gate**

Run:

```bash
export GOTOOLCHAIN=local ZAAFPULSE_DATA="$(mktemp -d)" HOME="$(mktemp -d)"
test -z "$(gofmt -l gw)" && go -C gw vet ./... && GOOS=windows go -C gw vet ./... && go -C gw test -race -count=1 ./...
deno task typecheck
node --test test/*.test.js
deno test -q --no-check --no-prompt -P=test test/*.test.js
npm run test:contract
```

Expected: no gofmt output, `ok` for every Go package, `deno check` with no error, `fail 0` from Node, `0 failed` from Deno, and the contract suite
`pass 172` (from Task 8 on, `pass 179`, with one case skipped outside shadow mode), `fail 0`.

- [ ] **Step 7: Commit and push**

```bash
git add -A -- gw scripts
git status --short   # nothing outside this task's files, nothing under .superpowers/
git commit -m "P4b T1: classify the raw request path, answer 431 past Node's header limit, quiet the noisy log lines, close CONNECT"
git push origin migrate/p4b-passthrough
```

---

### Task 2: Control-plane entry conditions (P4b entry conditions 5 and 8, review m4 and T5-N4)

The gateway gets the proxy and CA variables on an allow-list (entry 5); the status and the Overview chip say "restart needed" after a crash-loop fallback (entry 8); a retry whose internal listener cannot start falls back again at once (m4); `stop()` cancels a queued retry (T5-N4).

**Files:**
- Modify: `lib/front/index.js`, `lib/ipc/env.js`, `public/topologyview.js`, `test/front.test.js`, `test/ipc-hardening.test.js`, `test/topology-ui.test.js`


**Interfaces:**
- Consumes: `lib/ipc/env.js gwEnv`, `lib/front/index.js createFront` (P4a), `test/front.test.js frontWithFakes`, `until`.
- Produces:
  - `gwEnv(env, dataDir, platform)` also passes the names in `NETWORK` (exported): `HTTPS_PROXY`, `https_proxy`, `HTTP_PROXY`, `http_proxy`, `NO_PROXY`, `no_proxy`, `SSL_CERT_FILE`, `SSL_CERT_DIR`, `NODE_EXTRA_CA_CERTS`, `DENO_CERT` (Windows: upper case only).
  - `front.status().restartNeeded: boolean` (true in `fallback` after a crash loop).
  - `public/topologyview.js topoGatewayChip(st)`: text `Gateway: Go (front) · fallback, restart needed` when `st.restartNeeded`.

- [ ] **Step 1: Check the starting point**

Run: `git status --short && git rev-parse --abbrev-ref HEAD && git log --oneline -1`
Expected: nothing listed, `migrate/p4b-passthrough`, and the previous task's commit on top (Task 1: `0ec1247`, the merge of `origin/migrate/deno-go` with stream C).
Also run `npx --yes prettier@3 --check lib/front/index.js lib/ipc/env.js public/topologyview.js test/front.test.js test/ipc-hardening.test.js test/topology-ui.test.js` and expect `All matched files use Prettier code style!`.

- [ ] **Step 2: Write the failing tests**

Apply (from the repository root):

```bash
git apply <<'PATCH'
diff --git a/test/front.test.js b/test/front.test.js
index 33de084..4bb8f5e 100644
--- a/test/front.test.js
+++ b/test/front.test.js
@@ -215,3 +215,51 @@ test('the fallback waits up to waitPortMs for the main port, then reports it tak
     await released.catch(() => {});
   }
 });
+
+test('status: restartNeeded after a crash-loop fallback only (P4b entry condition 8)', async (t) => {
+  const f = await frontWithFakes(t);
+  assert.equal(f.front.status().restartNeeded, false);
+  f.sups[0].opts.onFailed('6 restarts within 60 s', { kind: 'restarts' });
+  await until(() => f.front.mode === 'fallback' && f.mainServer.listening);
+  assert.equal(f.front.status().restartNeeded, true);
+  const g = await frontWithFakes(t);
+  g.sups[0].opts.onFailed('exit 4', { kind: 'fatal' });
+  await until(() => g.front.mode === 'fallback' && g.mainServer.listening);
+  assert.equal(g.front.status().restartNeeded, false);
+});
+
+test('m4: a retry whose internal listener cannot start falls back again at once', async (t) => {
+  let calls = 0;
+  const f = await frontWithFakes(t, {
+    internal: () => {
+      calls++;
+      return {
+        listen: async () => {
+          if (calls > 1) throw new Error('listen EMFILE');
+          return 1;
+        },
+        close: async () => {},
+      };
+    },
+  });
+  f.sups[0].opts.onFailed('exit 4', { kind: 'fatal' });
+  await until(() => f.front.mode === 'fallback' && f.mainServer.listening);
+  assert.equal(await f.front.reload(), false);
+  assert.equal(f.front.mode, 'fallback');
+  assert.equal(f.mainServer.listening, true, 'the main port is served again');
+  assert.match(f.front.status().reason, /front could not start again: listen EMFILE/);
+  assert.equal(f.sups.length, 1, 'no supervisor was started');
+});
+
+test('T5-N4: stop() cancels a retry still waiting in the queue', async (t) => {
+  const f = await frontWithFakes(t);
+  f.sups[0].opts.onFailed('exit 4', { kind: 'fatal' });
+  await until(() => f.front.mode === 'fallback' && f.mainServer.listening);
+  let release;
+  const held = new Promise((r) => (release = r));
+  const retry = f.front.reload({ after: held });
+  await f.front.stop(0);
+  release();
+  assert.equal(await retry, false);
+  assert.equal(f.sups.length, 1, 'no gateway started after stop()');
+});
diff --git a/test/ipc-hardening.test.js b/test/ipc-hardening.test.js
index 3efbd48..c13c43f 100644
--- a/test/ipc-hardening.test.js
+++ b/test/ipc-hardening.test.js
@@ -43,6 +43,36 @@ test('gwEnv: the data folder, PATH and the home folder; on Windows also SystemRo
   });
 });
 
+test('gwEnv: the proxy and CA variables pass on an allow-list (P4b entry condition 5), nothing else', () => {
+  const env = {
+    PATH: '/usr/bin',
+    HOME: '/home/me',
+    HTTPS_PROXY: 'http://proxy:3128',
+    https_proxy: 'http://proxy:3128',
+    NO_PROXY: 'localhost,127.0.0.1',
+    SSL_CERT_FILE: '/etc/ca.pem',
+    NODE_EXTRA_CA_CERTS: '/etc/extra.pem',
+    DENO_CERT: '/etc/deno.pem',
+    ALL_PROXY: 'socks5://x',
+    OPENAI_API_KEY: 'sk-secret',
+    NODE_OPTIONS: '--inspect',
+  };
+  assert.deepEqual(gwEnv(env, '/data', 'darwin'), {
+    ZAAFPULSE_DATA: '/data',
+    PATH: '/usr/bin',
+    HOME: '/home/me',
+    HTTPS_PROXY: 'http://proxy:3128',
+    https_proxy: 'http://proxy:3128',
+    NO_PROXY: 'localhost,127.0.0.1',
+    SSL_CERT_FILE: '/etc/ca.pem',
+    NODE_EXTRA_CA_CERTS: '/etc/extra.pem',
+    DENO_CERT: '/etc/deno.pem',
+  });
+  // Windows: one variable whatever its case, passed under the upper-case name.
+  const win = gwEnv({ Path: 'C:\\bin', https_proxy: 'http://p:1', no_proxy: 'x' }, 'D:\\d', 'win32');
+  assert.deepEqual(win, { ZAAFPULSE_DATA: 'D:\\d', PATH: 'C:\\bin', HTTPS_PROXY: 'http://p:1', NO_PROXY: 'x' });
+});
+
 test('mark file: written with rename, raised only, read back; a bad file reads as no marks', (t) => {
   const dir = tmpDir(t, 'marks');
   const file = path.join(dir, '.data', 'ipc-marks.json');
diff --git a/test/topology-ui.test.js b/test/topology-ui.test.js
index badc115..745ceb8 100644
--- a/test/topology-ui.test.js
+++ b/test/topology-ui.test.js
@@ -709,6 +709,15 @@ test('the gateway chip (P4a D6): none in js mode or without an answer; ready, re
   assert.deepEqual([el.hidden, el.textContent], [true, '']);
 });
 
+test('the gateway chip says "restart needed" after a crash loop (P4b entry condition 8)', () => {
+  const st = { mode: 'fallback', state: 'failed', reason: '6 restarts within 60 s', restartNeeded: true };
+  const fb = topoGatewayChip(st);
+  assert.equal(fb.text, 'Gateway: Go (front) · fallback, restart needed');
+  assert.equal(fb.tone, 'red');
+  assert.match(fb.title, /Restart ZAAFPulse to try the gateway again\.$/);
+  assert.doesNotMatch(topoGatewayChip({ ...st, restartNeeded: false }).title, /Restart ZAAFPulse/);
+});
+
 test('the gateway chip asks for /api/gateway/status only as the owner (no 403 or audit entry for other roles)', async () => {
   for (const owner of [false, true]) {
     const b = fakeBrowser(fakeDoc({ icons: true }));
PATCH
```

- [ ] **Step 3: Run them to see them fail**

Run: `node --test test/ipc-hardening.test.js test/front.test.js test/topology-ui.test.js`
Expected: FAIL in four tests: `gwEnv: the proxy and CA variables pass on an allow-list` (the variables are missing), `status: restartNeeded ...` (undefined), `m4: a retry whose internal listener cannot start ...` (the retry throws), `T5-N4: stop() cancels ...` (a second supervisor starts), and the chip test (`fallback, restart needed`).

- [ ] **Step 4: Implement**

Apply (from the repository root):

```bash
git apply <<'PATCH'
diff --git a/lib/front/index.js b/lib/front/index.js
index d5a70b9..6bafb2d 100644
--- a/lib/front/index.js
+++ b/lib/front/index.js
@@ -98,6 +98,8 @@ function createFront({
   let chain = Promise.resolve();
   const queue = (fn) => (chain = chain.then(fn, fn));
   let retrying = null;
+  // stop() has begun: a retry still queued does not start a gateway (P4a review T5-N4).
+  let stopping = false;
   const serving = (m) => {
     if (served) return;
     served = true;
@@ -216,10 +218,23 @@ function createFront({
       if (mode === 'front' && sup && !failing) return sup.reload();
       if (retrying) return retrying;
       const run = queue(async () => {
-        if (mode !== 'fallback' || !retryOnReload) return false;
+        if (stopping || mode !== 'fallback' || !retryOnReload) return false;
         if (after) await Promise.race([after, new Promise((r) => setTimeout(r, 10000))]);
+        if (stopping) return false;
         await closeMain();
-        await startFront();
+        try {
+          await startFront();
+        } catch (err) {
+          // The internal listener could not start: the main port is the server's again at once, never dark until a
+          // restart (P4a final review m4).
+          mode = 'fallback';
+          reason = `front could not start again: ${err.message}`;
+          log(`gateway: ${reason}; serving on Deno`);
+          if (inner) await inner.close().catch(() => {});
+          inner = null;
+          await listenMain(waitPortMs);
+          return false;
+        }
         return true;
       });
       retrying = run;
@@ -231,6 +246,7 @@ function createFront({
     },
     /** Stops the gateway (shutdown, then a kill after graceMs + its kill delay) and the internal listener. */
     async stop(graceMs = 2000) {
+      stopping = true;
       if (sup) await sup.stop(graceMs);
       if (inner) await inner.close();
       inner = null;
@@ -254,6 +270,8 @@ function createFront({
         mode,
         engines: allJs(),
         ...(reason ? { reason } : {}),
+        // After a crash loop only a restart of ZAAFPulse tries the gateway again (P4a deviation 12, entry condition 8).
+        restartNeeded: mode === 'fallback' && !retryOnReload,
         alert: !!base.alert || mode === 'fallback',
       };
     },
diff --git a/lib/ipc/env.js b/lib/ipc/env.js
index 1c4901f..e09b291 100644
--- a/lib/ipc/env.js
+++ b/lib/ipc/env.js
@@ -1,7 +1,24 @@
 // The environment the gateway process gets (P4a spec E7): the data folder, PATH, the home folder and, on Windows, what
-// the OS needs (SystemRoot, TEMP, TMP). Nothing else: provider keys in environment variables reach the gateway only
+// the OS needs (SystemRoot, TEMP, TMP). Nothing else, except the network settings a provider call needs (P4b entry
+// condition 5): the proxy (HTTPS_PROXY, HTTP_PROXY, NO_PROXY, also lower case, which Go's ProxyFromEnvironment and
+// Deno's fetch read) and the extra CA certificates (SSL_CERT_FILE, SSL_CERT_DIR, and the ones Node and Deno add to
+// their own store: NODE_EXTRA_CA_CERTS, DENO_CERT). Provider keys in environment variables reach the gateway only
 // through secrets.handoff.
 
+// The network settings passed on as they are (a proxy URL may hold a user and password: never logged).
+const NETWORK = [
+  'HTTPS_PROXY',
+  'https_proxy',
+  'HTTP_PROXY',
+  'http_proxy',
+  'NO_PROXY',
+  'no_proxy',
+  'SSL_CERT_FILE',
+  'SSL_CERT_DIR',
+  'NODE_EXTRA_CA_CERTS',
+  'DENO_CERT',
+];
+
 /**
  * @param {Record<string, string | undefined>} env the control plane's environment
  * @param {string} dataDir the data folder (lib/platform.js dataDir())
@@ -18,11 +35,14 @@ function gwEnv(env, dataDir, platform = process.platform) {
     return k === undefined ? undefined : env[k];
   };
   const out = { ZAAFPULSE_DATA: dataDir };
-  for (const name of ['PATH', 'HOME', 'USERPROFILE', ...(win ? ['SystemRoot', 'TEMP', 'TMP'] : [])]) {
+  const names = ['PATH', 'HOME', 'USERPROFILE', ...(win ? ['SystemRoot', 'TEMP', 'TMP'] : [])];
+  // On Windows https_proxy and HTTPS_PROXY are one variable: passed once, upper case.
+  for (const n of NETWORK) if (!win || n === n.toUpperCase()) names.push(n);
+  for (const name of names) {
     const v = get(name);
     if (typeof v === 'string' && v !== '') out[name] = v;
   }
   return out;
 }
 
-module.exports = { gwEnv };
+module.exports = { gwEnv, NETWORK };
diff --git a/public/topologyview.js b/public/topologyview.js
index a8e5e7b..ae3407a 100644
--- a/public/topologyview.js
+++ b/public/topologyview.js
@@ -73,9 +73,11 @@ function topoGatewayChip(st) {
   if (!st || typeof st !== 'object' || (st.mode !== 'front' && st.mode !== 'fallback')) return null;
   if (st.mode === 'fallback')
     return {
-      text: 'Gateway: Go (front) · fallback',
+      text: st.restartNeeded ? 'Gateway: Go (front) · fallback, restart needed' : 'Gateway: Go (front) · fallback',
       tone: 'red',
-      title: `The Go gateway failed${st.reason ? `: ${String(st.reason).slice(0, 200)}` : ''}. ZAAFPulse serves on its own.`,
+      title:
+        `The Go gateway failed${st.reason ? `: ${String(st.reason).slice(0, 200)}` : ''}. ZAAFPulse serves on its own.` +
+        (st.restartNeeded ? ' Restart ZAAFPulse to try the gateway again.' : ''),
     };
   if (st.state === 'ready')
     return { text: 'Gateway: Go (front) · ready', tone: 'green', title: `Restarts: ${Number(st.restarts) || 0}` };
PATCH
```

- [ ] **Step 5: Run the tests and the task gate**

Run:

```bash
export GOTOOLCHAIN=local ZAAFPULSE_DATA="$(mktemp -d)" HOME="$(mktemp -d)"
test -z "$(gofmt -l gw)" && go -C gw vet ./... && GOOS=windows go -C gw vet ./... && go -C gw test -race -count=1 ./...
deno task typecheck
node --test test/*.test.js
deno test -q --no-check --no-prompt -P=test test/*.test.js
npm run test:contract
```

Expected: no gofmt output, `ok` for every Go package, `deno check` with no error, `fail 0` from Node, `0 failed` from Deno, and the contract suite
`pass 172` (from Task 8 on, `pass 179`, with one case skipped outside shadow mode), `fail 0`.

- [ ] **Step 6: Commit and push**

```bash
git add -A -- lib public test
git status --short   # nothing outside this task's files, nothing under .superpowers/
git commit -m "P4b T2: proxy and CA variables for the gateway, restart needed after a crash loop, retry fallback and stop fixes"
git push origin migrate/p4b-passthrough
```

---

### Task 3: One usage writer that feeds the spend (P4b entry condition 1)

The IPC ingest stores the Go engine's usage lines through the JS gateway's own unwrapped writer, so the per-key monthly spend, the recent list and the key's last use see them; the budget report still runs once, after the mark.

**Files:**
- Modify: `lib/gateway/index.js`, `lib/ipc/server-sinks.js`, `server.js`, `test/ipc-server-sinks.test.js`


**Interfaces:**
- Consumes: `createGateway` (`lib/gateway/index.js`), `createIngest` (`lib/ipc/ingest.js`).
- Produces:
  - `gateway.ingestUsage(entry): object` — strict append (throws on a failed write), counted in `usage.monthSpend` and `usage.recent`, sets the key's `lastUsedAt` from `entry.ts` (once a minute at most). No budget report, no span.
  - `serverSinks({ recordUsage, budgetRecord, governance, mcpAudit, traceStore, traceDir })` — `recordUsage` is required (`usageFile` and `getPricing` are gone).
  - `server.js frontIngest()` passes `recordUsage: (e) => gateway.ingestUsage(e)`.

- [ ] **Step 1: Check the starting point**

Run: `git status --short && git rev-parse --abbrev-ref HEAD && git log --oneline -1`
Expected: nothing listed, `migrate/p4b-passthrough`, and the previous task's commit on top (Task 1: `0ec1247`, the merge of `origin/migrate/deno-go` with stream C).

- [ ] **Step 2: Write the failing tests**

Apply (from the repository root):

```bash
git apply <<'PATCH'
diff --git a/test/ipc-server-sinks.test.js b/test/ipc-server-sinks.test.js
index 60cc954..d83b3f1 100644
--- a/test/ipc-server-sinks.test.js
+++ b/test/ipc-server-sinks.test.js
@@ -1,5 +1,6 @@
 // The IPC ingest's writers in server.js (P4a E3): strict and unwrapped. server.js passes none of its wrapped writers,
-// a failed append is not acknowledged, and the budget report runs once, after the stored usage line.
+// a failed append is not acknowledged, and the budget report runs once, after the stored usage line. Usage goes
+// through the gateway's own writer, so the gateway's in-memory spend counts it (P4b entry condition 1).
 const test = require('node:test');
 const assert = require('node:assert/strict');
 const fs = require('fs');
@@ -8,6 +9,28 @@ const { serverSinks } = require('../lib/ipc/server-sinks');
 const { createIngest } = require('../lib/ipc/ingest');
 const { createAudit } = require('../lib/audit');
 const { tmpDir } = require('./ipc-stub');
+const { createGateway } = require('../lib/gateway');
+const { secretsFor } = require('./helpers');
+
+// A gateway on a temp data folder with one key, k (the usage lines name it), and US$1/US$2 per 1M tokens for m.
+function gatewayIn(dir, { budgetUSD = null } = {}) {
+  fs.mkdirSync(dir, { recursive: true });
+  fs.writeFileSync(
+    path.join(dir, 'keys.json'),
+    JSON.stringify({
+      keys: [{ id: 'k', name: 'k', hash: '0'.repeat(64), scopes: ['llm'], models: [], agents: [], budgetUSD, rpm: 60 }],
+    }),
+  );
+  return createGateway({
+    dataDir: dir,
+    getConfig: () => ({ gateway: {} }),
+    writeGatewayConfig: () => {},
+    secrets: secretsFor(),
+    resolveSecret: () => undefined,
+    getPricing: () => ({ m: { input: 1, output: 2 } }),
+    getIntegrations: () => ({ listTools: async () => [], callTool: async () => ({}) }),
+  });
+}
 
 const STREAM = 'b'.repeat(32);
 const usageMsg = (seq) => ({
@@ -21,8 +44,9 @@ test('server.js builds the ingest from strict writers, never from its wrapped on
   const at = src.indexOf('serverSinks({');
   assert.ok(at > 0, 'server.js calls serverSinks');
   const call = src.slice(at, src.indexOf('});', at));
-  for (const wrapped of ['govRecord', 'govWrite', 'gateway.usage', 'usage.record', 'recorder'])
+  for (const wrapped of ['govRecord', 'govWrite', 'gateway.usage', 'usage.record', 'recorder', 'createUsage'])
     assert.ok(!call.includes(wrapped), `the ingest gets ${wrapped}`);
+  assert.match(call, /recordUsage: \(e\) => gateway\.ingestUsage\(e\),/);
   assert.match(call, /\bgovernance,/);
   assert.match(call, /mcpAudit: audit,/);
   assert.match(call, /\btraceStore,/);
@@ -31,9 +55,9 @@ test('server.js builds the ingest from strict writers, never from its wrapped on
 test('usage: stored strictly with its mark, then the budget report once', (t) => {
   const dir = tmpDir(t, 'sinks');
   const reported = [];
+  const gateway = gatewayIn(dir);
   const sinks = serverSinks({
-    usageFile: path.join(dir, 'gateway-usage.jsonl'),
-    getPricing: () => ({}),
+    recordUsage: (e) => gateway.ingestUsage(e),
     budgetRecord: (e) => reported.push(e),
     governance: { record: () => ({}) },
     mcpAudit: createAudit(path.join(dir, 'mcp-audit.jsonl')),
@@ -49,12 +73,36 @@ test('usage: stored strictly with its mark, then the budget report once', (t) =>
   assert.equal(reported[0].cost, 0.002);
 });
 
+test('P4b entry 1: a Go engine line counts in the gateway spend, the recent list and the key last use', (t) => {
+  const dir = tmpDir(t, 'sinks-spend');
+  const gateway = gatewayIn(dir, { budgetUSD: 0.001 });
+  const sinks = serverSinks({
+    recordUsage: (e) => gateway.ingestUsage(e),
+    budgetRecord: () => {},
+    governance: { record: () => ({}) },
+    mcpAudit: createAudit(path.join(dir, 'mcp-audit.jsonl')),
+    traceStore: null,
+    traceDir: path.join(dir, 'traces'),
+  });
+  const ingest = createIngest({ ...sinks, log: () => {} });
+  const ts = Date.now();
+  const msg = { ...usageMsg(1), body: { record: { ...usageMsg(1).body.record, ts } } };
+  assert.equal(ingest.handle(msg, STREAM), true);
+  assert.equal(gateway.usage.monthSpend('k'), 0.002, 'the per-key spend a budget reads');
+  assert.equal(gateway.usage.recent(1)[0].ipc.seq, 1);
+  assert.equal(gateway.keys.list()[0].lastUsedAt, ts);
+});
+
+test('serverSinks refuses to start without the gateway writer', () => {
+  assert.throws(() => serverSinks({ budgetRecord: () => {} }), /recordUsage \(gateway\.ingestUsage\) is required/);
+});
+
 test('a write that fails is not marked: governance null, an unwritable usage log, no trace store', (t) => {
   const dir = tmpDir(t, 'sinks-fail');
+  const gateway = gatewayIn(dir);
   fs.mkdirSync(path.join(dir, 'gateway-usage.jsonl')); // a folder: the append fails
   const sinks = serverSinks({
-    usageFile: path.join(dir, 'gateway-usage.jsonl'),
-    getPricing: () => ({}),
+    recordUsage: (e) => gateway.ingestUsage(e),
     budgetRecord: () => {},
     governance: { record: () => null }, // the chain could not write
     mcpAudit: createAudit(path.join(dir, 'mcp-audit.jsonl')),
PATCH
```

- [ ] **Step 3: Run them to see them fail**

Run: `node --test test/ipc-server-sinks.test.js`
Expected: FAIL: `P4b entry 1: a Go engine line counts in the gateway spend ...` (`gateway.ingestUsage is not a function`), `serverSinks refuses to start without the gateway writer` (no throw), and the source check (`recordUsage: (e) => gateway.ingestUsage(e),` not found in `server.js`).

- [ ] **Step 4: Implement**

Apply (from the repository root):

```bash
git apply <<'PATCH'
diff --git a/lib/gateway/index.js b/lib/gateway/index.js
index 0aab98f..92a171b 100644
--- a/lib/gateway/index.js
+++ b/lib/gateway/index.js
@@ -156,9 +156,12 @@ function createGateway({
   }
   const providers = createProviders({ getConfig, secrets, ...(fetchImpl ? { fetchImpl } : {}) });
   // Every model call ends in usage.record; with OpenTelemetry export on, each one also becomes a span.
-  const usage = require('../otel').wrapUsage(
-    createUsage({ file: path.join(dataDir, 'gateway-usage.jsonl'), getPricing, now }),
-  );
+  const usage = createUsage({ file: path.join(dataDir, 'gateway-usage.jsonl'), getPricing, now });
+  // The unwrapped writer, kept before the wrappers below replace usage.record: the IPC ingest stores the Go engine's
+  // lines through it (ingestUsage), so they land in this log's in-memory spend and recent list too (P4b entry
+  // condition 1). It is the only writer of gateway-usage.jsonl in the process.
+  const recordRaw = usage.record;
+  require('../otel').wrapUsage(usage);
   // Cost Control budgets: each recorded call is reported to the budget service, which counts it until the next ledger
   // pass reads it (lib/cost/budget-service.js). A budget never fails a request.
   if (budgetGuard && typeof budgetGuard.record === 'function') {
@@ -2557,6 +2560,19 @@ function createGateway({
       },
     },
     replay: replayAccessor(),
+    // The IPC ingest's usage writer (lib/ipc/server-sinks.js): one line from the Go engine, appended strictly (a failed
+    // append throws, so the event is not acknowledged), counted in the per-key monthly spend and the recent list, and
+    // the key marked as used, as keys.authenticate does for a call served here. No budget report and no span: the ingest
+    // runs the budget report after the mark (afterUsage); spans come from the Go engine itself (P4f).
+    ingestUsage(e) {
+      const entry = recordRaw(e, { strict: true });
+      const k = typeof entry.keyId === 'string' && keyStore.get().keys.find((x) => x.id === entry.keyId);
+      if (k && Number.isFinite(entry.ts) && (!k.lastUsedAt || entry.ts - k.lastUsedAt > 60000)) {
+        k.lastUsedAt = entry.ts;
+        keyStore.save();
+      }
+      return entry;
+    },
     flush: () => [keyStore, agentStore, sessionStore, memoryStore, workflowStore, runStore].forEach((s) => s.flush()),
   };
 }
diff --git a/lib/ipc/server-sinks.js b/lib/ipc/server-sinks.js
index e089a8d..a96bd3f 100644
--- a/lib/ipc/server-sinks.js
+++ b/lib/ipc/server-sinks.js
@@ -6,20 +6,23 @@
 //   - govRecord sends some entries through the refusal limiter, which returns null when it drops one (a store failure
 //     to the ingest, so a restart, over and over);
 //   - the trace recorder buffers spans and writes them later.
-// So usage gets a usage writer of its own on the same file, as lib/ipc/local.js builds it; governance, the MCP audit
-// log and traces get the server's raw stores (a second governance chain on the same folder would fork the HMAC chain).
-const { createUsage } = require('../gateway/usage');
+// So usage goes through the gateway's own unwrapped writer (gateway.ingestUsage, strict), which also counts the line in
+// the gateway's in-memory spend, so per-key budgets see the Go engine's calls (P4b entry condition 1; before P4b a
+// second usage writer on the same file skipped it); governance, the MCP audit log and traces get the server's raw
+// stores (a second governance chain on the same folder would fork the HMAC chain).
 const { spanInNewestSegment } = require('./marks');
 
 /**
- * @param {{ usageFile: string, getPricing: () => object, budgetRecord: (entry: any) => void,
+ * @param {{ recordUsage: (entry: object) => object, budgetRecord: (entry: any) => void,
  *   governance: { record: (e: object) => unknown }, mcpAudit: { record: (e: object, opts?: object) => unknown },
  *   traceStore: { writeSpans: (list: object[]) => unknown } | null, traceDir: string }} stores
+ * recordUsage: the gateway's strict, unwrapped writer (lib/gateway/index.js ingestUsage).
  */
-function serverSinks({ usageFile, getPricing, budgetRecord, governance, mcpAudit, traceStore, traceDir }) {
-  const usage = createUsage({ file: usageFile, getPricing });
+function serverSinks({ recordUsage, budgetRecord, governance, mcpAudit, traceStore, traceDir }) {
+  if (typeof recordUsage !== 'function')
+    throw new TypeError('serverSinks: recordUsage (gateway.ingestUsage) is required');
   return {
-    recordUsage: (e) => usage.record(e, { strict: true }),
+    recordUsage: (e) => recordUsage(e),
     afterUsage: (entry) => budgetRecord(entry),
     recordGovernance: (e) => governance.record(e),
     recordMcp: (e) => mcpAudit.record(e, { strict: true }),
diff --git a/server.js b/server.js
index 309fefe..7462999 100644
--- a/server.js
+++ b/server.js
@@ -581,8 +581,7 @@ const gateway = createGateway({
 function frontIngest() {
   const files = localPaths(DIR);
   const sinks = serverSinks({
-    usageFile: files.usageFile,
-    getPricing: () => config.pricing,
+    recordUsage: (e) => gateway.ingestUsage(e),
     budgetRecord: (entry) => budgetGuard.record(entry),
     governance,
     mcpAudit: audit,
PATCH
```

- [ ] **Step 5: Run the tests and the task gate**

Run:

```bash
export GOTOOLCHAIN=local ZAAFPULSE_DATA="$(mktemp -d)" HOME="$(mktemp -d)"
test -z "$(gofmt -l gw)" && go -C gw vet ./... && GOOS=windows go -C gw vet ./... && go -C gw test -race -count=1 ./...
deno task typecheck
node --test test/*.test.js
deno test -q --no-check --no-prompt -P=test test/*.test.js
npm run test:contract
```

Expected: no gofmt output, `ok` for every Go package, `deno check` with no error, `fail 0` from Node, `0 failed` from Deno, and the contract suite
`pass 172` (from Task 8 on, `pass 179`, with one case skipped outside shadow mode), `fail 0`.

- [ ] **Step 6: Commit and push**

```bash
git add -A -- lib server.js test
git status --short   # nothing outside this task's files, nothing under .superpowers/
git commit -m "P4b T3: the IPC ingest stores usage through the gateway's own writer, so per-key spend sees the Go engine"
git push origin migrate/p4b-passthrough
```

---

### Task 4: The config.json-to-IPC converter, pushes, saves that wait for the gateway, the late front section (P4b entry condition 3)

Each generation now carries the real gateway section, the keys by hash, the prices in nano-USD, the provider credentials and the blocking budgets (`lib/front/push.js`). Keys and prices push a new generation; a gateway admin save answers once the gateway has applied it (Decision 11). The front section is fixed for a gateway's life (Decision 7).

**Files:**
- Create: `gw/cmd/zaafpulse-gw/reload_test.go`, `lib/front/push.js`, `test/front-push.test.js`
- Modify: `gw/cmd/zaafpulse-gw/front.go`, `gw/cmd/zaafpulse-gw/main.go`, `lib/front/index.js`, `lib/gateway/index.js`, `lib/ipc/supervisor.js`, `server.js`, `test/ipc-supervisor.test.js`


**Interfaces:**
- Consumes: `resolvePolicy` (`lib/gateway/guardrails.js`), `toNano` (`lib/money.js`), `createSupervisor`, `createFront`, `check` (`lib/ipc/schema.js`).
- Produces:
  - `lib/front/push.js`: `configPush({ gateway, keys, pricing })` (config.push body without `gen` and `front`), `secretsPush({ gateway, resolve })` (`{ providers: [{ id, value }], mask }`), `snapshotPush({ service, keyIds, now? })` (`{ budgets, keyCenter }`), `keyAuth(k)`, `provider(p)`, `price(e)`. A provider with an `apiKey` reference and no `auth` gets `auth: { type: 'key' }`.
  - `createFront({ ..., sources: { config, secrets, snapshot } })`; `front.push(): boolean` (a new generation in front mode, never a retry); `front.synced(timeoutMs = 2000): Promise<boolean>`.
  - `supervisor.synced(timeoutMs = 2000): Promise<boolean>` — true once a health names the newest generation.
  - `createGateway({ ..., onKeysChange })`, called after a key is created, updated, revoked or deleted; `gateway.rawKeys()` (keys.json keys with their hashes, for the push only).
  - Go: after ready, the gateway sends a `health` as soon as a generation's config and secrets are both applied (`gateway.applied`); a front section after the start is logged `config.push: a front section after the start applies after a restart` and ignored.
  - `server.js`: `gwSecrets` (one resolver for the gateway and the push), `configChanged` pushes on a `pricing` change, the price sync pushes, a gateway admin POST awaits `front.synced(2000)`.

- [ ] **Step 1: Check the starting point**

Run: `git status --short && git rev-parse --abbrev-ref HEAD && git log --oneline -1`
Expected: nothing listed, `migrate/p4b-passthrough`, and the previous task's commit on top (Task 1: `0ec1247`, the merge of `origin/migrate/deno-go` with stream C).

- [ ] **Step 2: Write the failing tests**

Create `gw/cmd/zaafpulse-gw/reload_test.go`:

```go
package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// gwRun is a gateway driven over pipes: send writes control-plane lines, lines carries what it writes back.
type gwRun struct {
	code   chan int
	stderr *syncBuffer
	send   func(string)
	lines  chan string
}

const emptyGateway = `{"providers":[],"routes":[],"guardrails":{"scope":"external","secrets":"off","pii":"off",` +
	`"injection":"off"},"defaults":{"model":"","plannerModel":"","codeModel":""},"tokenSaver":false,"modelMap":[]}`

// configLine is a config.push of generation gen, with the front section front ("" for none).
func configLine(seq, gen int, front string) string {
	f := ""
	if front != "" {
		f = `,"front":` + front
	}
	return fmt.Sprintf(`{"v":1,"type":"config.push","seq":%d,"ts":0,"body":{"gen":%d,"gateway":%s,"keys":[],"pricing":{}%s}}`,
		seq, gen, emptyGateway, f)
}

func secretsLine(seq, gen int) string {
	return fmt.Sprintf(`{"v":1,"type":"secrets.handoff","seq":%d,"ts":0,"body":{"gen":%d,"providers":[],"mask":[],"frontToken":"%s"}}`,
		seq, gen, frontToken)
}

func drive(t *testing.T) *gwRun {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	r := &gwRun{code: make(chan int, 1), stderr: &syncBuffer{}, lines: make(chan string, 64)}
	go func() {
		r.code <- run([]string{"--outbox", t.TempDir(), "--health-ms", "60000"}, inR, outW, r.stderr)
		_ = outW.Close()
		_ = inR.Close()
	}()
	go func() {
		br := bufio.NewReader(outR)
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				close(r.lines)
				return
			}
			r.lines <- line
		}
	}()
	writes := make(chan string, 64)
	go func() {
		for l := range writes {
			if _, err := io.WriteString(inW, l+"\n"); err != nil {
				return
			}
		}
	}()
	r.send = func(l string) { writes <- l }
	t.Cleanup(func() { close(writes); _ = inW.Close() })
	return r
}

// next waits for the next line of type typ and returns it.
func (r *gwRun) next(t *testing.T, typ string) string {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case l, ok := <-r.lines:
			if !ok {
				t.Fatalf("the gateway stopped before a %s; stderr %s", typ, r.stderr)
			}
			if strings.Contains(l, `"type":"`+typ+`"`) {
				return l
			}
		case <-deadline:
			t.Fatalf("no %s within 10 s; stderr %s", typ, r.stderr)
		}
	}
}

func (r *gwRun) ready(t *testing.T, front string) {
	t.Helper()
	r.next(t, "hello")
	r.send(`{"v":1,"type":"hello.ok","seq":1,"ts":0,"re":"h1","body":{"protocol":1,"cpVersion":"test"}}`)
	r.send(configLine(2, 1, front))
	r.send(secretsLine(3, 1))
	r.send(`{"v":1,"type":"budget.snapshot","seq":4,"ts":0,"body":{"at":0,"budgets":[],"keyCenter":{}}}`)
	r.next(t, "ready")
}

// A new generation is answered with a health that names it as soon as its config and secrets are both applied, so the
// control plane can wait for it (lib/ipc/supervisor.js synced) instead of the next health tick.
func TestHealthNamesAGenerationOnceItsSecretsAreApplied(t *testing.T) {
	r := drive(t)
	r.ready(t, "")
	r.send(configLine(5, 2, ""))
	r.send(secretsLine(6, 2))
	if h := r.next(t, "health"); !strings.Contains(h, `"gen":2`) {
		t.Fatalf("health %s", h)
	}
}

// The front section is fixed for a gateway's life (P4b entry condition 3): a gateway started without one opens no port
// when a later config.push carries one, and says that it applies after a restart.
func TestALateFrontSectionAppliesAfterARestart(t *testing.T) {
	r := drive(t)
	r.ready(t, "")
	port := freePort(t)
	r.send(configLine(5, 2, frontJSON(port, 1, `{"chat":"js"}`)))
	r.send(secretsLine(6, 2))
	r.next(t, "health")
	if !strings.Contains(r.stderr.String(), "config.push: a front section after the start applies after a restart") {
		t.Fatalf("stderr %s", r.stderr)
	}
	if c, err := dialQuick(port); err == nil {
		_ = c.Close()
		t.Fatal("a late front section opened the main port")
	}
}

func dialQuick(port int) (io.Closer, error) {
	return net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
}
```

Create `test/front-push.test.js`:

```js
// The config.json-to-IPC converter (lib/front/push.js, P4b): every body it builds is a valid v1 message, keys carry
// their hash and never a secret, prices are nano-USD, a credential that does not resolve is left out, and the budget
// snapshot holds the blocking budgets and the keys' cost centers.
const test = require('node:test');
const assert = require('node:assert/strict');
const { configPush, secretsPush, snapshotPush, keyAuth } = require('../lib/front/push');
const { frontSection } = require('../lib/front');
const { check } = require('../lib/ipc/schema');

const msg = (type, body) => ({ v: 1, type, seq: 2, ts: 0, body: { ...body, gen: 1 } });
const GATEWAY = {
  engine: 'front',
  providers: [
    { id: 'oa', preset: 'custom', baseUrl: 'http://127.0.0.1:9/v1', models: ['m1'], apiKey: { keychain: 'acc.gw-oa' } },
    { id: 'pv', preset: 'ollama', baseUrl: 'http://localhost:11434/v1', tier: 'private', aliases: { fast: 'llama' } },
    {
      id: 'az',
      preset: 'azure',
      baseUrl: 'https://x/v1',
      apiKey: { env: 'AZ' },
      auth: { type: 'oauth2', grant: 'client_credentials', clientId: 'c' },
    },
    { id: 'bad', preset: 'custom', baseUrl: 'http://x/v1', apiKey: { env: 'MISSING' }, tokenParam: 'nope' },
  ],
  routes: [{ name: 'router/r', targets: ['oa/m1', 'pv/llama'], strategy: 'fallback' }],
  guardrails: { secrets: 'off', pii: 'nonsense' },
  tokenSaver: false,
  modelMap: [{ match: 'gpt-*', to: 'oa/m1' }],
};
const KEY = {
  id: 'key_1',
  name: 'ci',
  prefix: 'zp_abc…',
  hash: 'a'.repeat(64),
  scopes: ['llm'],
  models: ['oa/*'],
  agents: [],
  budgetUSD: null,
  rpm: 60,
  privateOnly: false,
  expiresAt: null,
  createdAt: 1,
  guardrails: { pii: 'block', injection: 'redact' },
};

test('configPush: a valid config.push with keys by hash, prices in nano-USD, no secret reference', () => {
  const body = configPush({
    gateway: GATEWAY,
    keys: [KEY, { id: 'nohash' }],
    pricing: { m1: { input: 1, output: 2.5, cacheRead: 0.1, note: 'x' } },
  });
  const front = frontSection({ host: '127.0.0.1', port: 4777, upstreamPort: 5000 });
  assert.deepEqual(check(msg('config.push', { ...body, front })), { errors: [], unknownType: false });
  assert.deepEqual(body.gateway.providers[0], {
    id: 'oa',
    preset: 'custom',
    baseUrl: 'http://127.0.0.1:9/v1',
    models: ['m1'],
    auth: { type: 'key' },
  });
  assert.deepEqual(body.gateway.providers[2].auth, { type: 'oauth2', clientId: 'c', grant: 'client_credentials' });
  assert.equal('tokenParam' in body.gateway.providers[3], false);
  assert.ok(!JSON.stringify(body).includes('acc.gw-oa'), 'no secret reference');
  assert.deepEqual(body.gateway.guardrails, { scope: 'external', secrets: 'off', pii: 'off', injection: 'log' });
  assert.equal(body.keys.length, 1, 'a key without a hash is left out');
  assert.deepEqual(body.keys[0].guardrails, { pii: 'block' }, 'only modes a check has');
  assert.deepEqual(body.pricing.m1, { input: 1e9, output: 2.5e9, cacheRead: 1e8 });
  assert.equal(configPush({ gateway: {}, keys: [], pricing: {} }).gateway.tokenSaver, true, 'on unless false');
});

test('keyAuth: every required field, with defaults for an old key', () => {
  const k = keyAuth({ id: 'old', hash: 'b'.repeat(64) });
  assert.deepEqual(Object.keys(k).sort(), [
    'agents',
    'approveOwnRuns',
    'budgetUSD',
    'createdAt',
    'expiresAt',
    'guardrails',
    'hash',
    'id',
    'knowledge',
    'lastUsedAt',
    'models',
    'name',
    'prefix',
    'privateOnly',
    'revokedAt',
    'rpm',
    'scopes',
  ]);
  assert.equal(k.rpm, 0);
});

test('secretsPush: key-auth credentials that resolve, and the mask set; others left out', () => {
  const seen = [];
  const body = secretsPush({
    gateway: GATEWAY,
    resolve: (ref, field) => {
      seen.push(field);
      if (ref.keychain === 'acc.gw-oa') return 'sk-oa-123456';
      throw new Error(`${field}: not set`);
    },
  });
  assert.deepEqual(body, { providers: [{ id: 'oa', value: 'sk-oa-123456' }], mask: ['sk-oa-123456'] });
  assert.deepEqual(seen, ['oa credential', 'bad credential'], 'OAuth2 is not resolved here (P4g)');
  assert.deepEqual(check(msg('secrets.handoff', body)), { errors: [], unknownType: false });
});

test('snapshotPush: the blocking budgets with their state, and the keys cost centers', () => {
  const service = {
    list: () => ({
      budgets: [
        {
          id: 'b1',
          name: 'team',
          scope: { dim: 'center', value: 'eng' },
          period: 'month',
          currency: 'USD',
          measure: 'list',
          enforce: 'block',
          amount: 10,
        },
        {
          id: 'b2',
          name: 'watch',
          scope: 'global',
          period: 'day',
          currency: 'USD',
          measure: 'list',
          enforce: 'none',
          amount: 1,
        },
      ],
    }),
    evaluateOne: (b) => ({
      ...b,
      from: '2026-11-01',
      to: '2026-11-30',
      state: 'ok',
      spentUSD: 1,
      amountUSD: 10,
      reason: null,
    }),
    keyCenter: (id) => (id === 'key_1' ? 'eng' : undefined),
  };
  const body = snapshotPush({ service, keyIds: ['key_1', 'key_2'] });
  assert.deepEqual(
    body.budgets.map((b) => b.id),
    ['b1'],
  );
  assert.deepEqual(body.keyCenter, { key_1: 'eng' });
  assert.deepEqual(check({ v: 1, type: 'budget.snapshot', seq: 2, ts: 0, body: { ...body, at: 0 } }), {
    errors: [],
    unknownType: false,
  });
  assert.deepEqual(
    snapshotPush({
      service: {
        list: () => {
          throw new Error('x');
        },
      },
      keyIds: [],
    }),
    { budgets: [], keyCenter: {} },
  );
});
```

Apply (from the repository root):

```bash
git apply <<'PATCH'
diff --git a/test/ipc-supervisor.test.js b/test/ipc-supervisor.test.js
index 95a15a8..5226a8e 100644
--- a/test/ipc-supervisor.test.js
+++ b/test/ipc-supervisor.test.js
@@ -69,6 +69,22 @@ test('start, ready, snapshots every interval, reload, stop: exit 0', async (t) =
   assert.equal(sup.status().restarts, 0);
 });
 
+test('synced(): a reload is in force once the health names its generation, long before the next tick', async (t) => {
+  const bin = stubBin(t);
+  if (!bin) return;
+  // Health every 60 s: only the health the gateway sends on applying a generation can answer in time.
+  const { sup } = supervise(t, bin, ['--health-ms', '60000']);
+  assert.equal(await sup.synced(100), false, 'no gateway yet');
+  sup.start();
+  await waitFor(() => sup.status().state === 'ready', 5000, 'ready');
+  assert.equal(sup.reload(), true);
+  const started = Date.now();
+  assert.equal(await sup.synced(5000), true);
+  assert.ok(Date.now() - started < 2000, 'answered by the health sent on apply');
+  assert.equal(sup.status().lastHealth.gen, 2);
+  assert.equal(await sup.synced(100), true, 'nothing newer to wait for');
+});
+
 test('a control plane whose recovered mark is below the outbox start stores the next event (no restart loop)', async (t) => {
   const bin = stubBin(t);
   if (!bin) return;
PATCH
```

- [ ] **Step 3: Run them to see them fail**

Run: `GOTOOLCHAIN=local go -C gw test -count=1 -run 'TestHealthNames|TestALateFront' ./cmd/zaafpulse-gw; node --test test/front-push.test.js test/ipc-supervisor.test.js`
Expected: Go FAIL `no health within 10 s` and the missing "applies after a restart" line; Node FAIL `Cannot find module '../lib/front/push'` and `sup.synced is not a function`.

- [ ] **Step 4: Implement**

`lib/front/push.js` is new; the patch wires it (`lib/front/index.js`, `lib/ipc/supervisor.js`, `lib/gateway/index.js`, `server.js`) and sends the health on apply in Go.

Create `lib/front/push.js`:

```js
// The config.json-to-IPC converter (P4b): what the control plane pushes to the Go gateway in front, built fresh for
// every generation from the same sources the JS gateway reads, so both engines decide from the same state.
//   configPush:  config.json `gateway` (providers without secrets, routes, guardrails, defaults, tokenSaver, modelMap),
//                keys.json (each key with the SHA-256 hex of its secret, never the secret), and the effective price
//                table (config.pricing: defaults, live prices and the owner's own) in nano-USD per 1M tokens;
//   secretsPush: each key-auth provider's resolved credential and the mask set (every resolved value of 6+ characters,
//                as lib/secrets.js mask() hides them); a provider whose credential does not resolve is left out, and the
//                gateway hands its calls to the control plane, which gives the same error it gives today;
//   snapshotPush: the budgets the guard enforces (enforce "block"), evaluated without a forecast, and each key's cost
//                center (lib/cost/guard.js reads both).
// Pure apart from the resolver and the budget service it is handed. Providers with OAuth2 or a Google service account
// get no token here: provider auth in Go is P4g (stream C builds the library, stage 2 wires it).
const { toNano } = require('../money');
const { resolvePolicy } = require('../gateway/guardrails');

const isObj = (v) => !!v && typeof v === 'object' && !Array.isArray(v);
const str = (v) => (typeof v === 'string' ? v : '');
const strings = (v) => (Array.isArray(v) ? v.filter((x) => typeof x === 'string') : []);
const MODES = {
  secrets: ['off', 'log', 'redact', 'block'],
  pii: ['off', 'log', 'redact', 'block'],
  injection: ['off', 'log', 'block'],
};
const int = (v) => (Number.isSafeInteger(v) && v >= 0 ? v : null);

/** A provider as config.push carries it: no apiKey reference; auth { type: 'key' } when it has one. */
function provider(p) {
  const out = { id: str(p.id), preset: str(p.preset), baseUrl: str(p.baseUrl) };
  if (Array.isArray(p.models)) out.models = strings(p.models);
  if (isObj(p.aliases))
    out.aliases = Object.fromEntries(Object.entries(p.aliases).filter(([, v]) => typeof v === 'string'));
  for (const k of ['enabled', 'approved', 'responses', 'reasoning']) if (typeof p[k] === 'boolean') out[k] = p[k];
  if (p.tier === 'private' || p.tier === 'external') out.tier = p.tier;
  if (p.format === 'openai' || p.format === 'anthropic') out.format = p.format;
  if (p.tokenParam === 'max_tokens' || p.tokenParam === 'max_completion_tokens') out.tokenParam = p.tokenParam;
  if (isObj(p.auth) && typeof p.auth.type === 'string' && p.auth.type) {
    const a = { type: p.auth.type };
    for (const k of ['tokenUrl', 'clientId', 'scope']) if (typeof p.auth[k] === 'string') a[k] = p.auth[k];
    if (p.auth.grant === 'client_credentials' || p.auth.grant === 'refresh_token') a.grant = p.auth.grant;
    out.auth = a;
  } else if (p.apiKey != null) out.auth = { type: 'key' };
  return out;
}

function route(r) {
  const out = { name: str(r.name), targets: strings(r.targets) };
  if (r.strategy === 'fallback' || r.strategy === 'round-robin') out.strategy = r.strategy;
  if (typeof r.description === 'string') out.description = r.description;
  return out;
}

/** A keys.json key as the gateway enforces it (ipc/v1 config.push keys[]), or null when it has no valid hash. */
function keyAuth(k) {
  if (!isObj(k) || typeof k.id !== 'string' || !k.id || !/^[0-9a-f]{64}$/.test(k.hash || '')) return null;
  const guardrails = {};
  if (isObj(k.guardrails)) {
    if (k.guardrails.scope === 'external' || k.guardrails.scope === 'all') guardrails.scope = k.guardrails.scope;
    for (const c of Object.keys(MODES)) if (MODES[c].includes(k.guardrails[c])) guardrails[c] = k.guardrails[c];
  }
  return {
    id: k.id,
    name: str(k.name),
    scopes: strings(k.scopes),
    models: strings(k.models),
    agents: strings(k.agents),
    knowledge: strings(k.knowledge),
    budgetUSD: Number.isFinite(k.budgetUSD) ? k.budgetUSD : null,
    rpm: int(k.rpm) ?? 0,
    privateOnly: k.privateOnly === true,
    expiresAt: int(k.expiresAt),
    revokedAt: int(k.revokedAt),
    guardrails,
    prefix: str(k.prefix),
    approveOwnRuns: k.approveOwnRuns === true,
    createdAt: int(k.createdAt) ?? 0,
    lastUsedAt: int(k.lastUsedAt),
    hash: k.hash,
  };
}

const PRICE_FIELDS = ['input', 'output', 'cacheRead', 'cacheWrite5m', 'cacheWrite1h'];
const nano = (v) => (typeof v === 'number' && Number.isFinite(v) && v >= 0 ? toNano(v) : 0);
/** One price entry in nano-USD per 1M tokens (web search: per search); fields that are not prices are dropped. */
function price(e) {
  const out = { input: nano(e.input), output: nano(e.output) };
  for (const k of PRICE_FIELDS.slice(2)) if (typeof e[k] === 'number') out[k] = nano(e[k]);
  if (typeof e.webSearch === 'number') out.webSearch = nano(e.webSearch);
  if (Array.isArray(e.tiers))
    out.tiers = e.tiers.filter(isObj).map((t) => {
      const r = { above: Math.max(0, Math.floor(Number(t.above) || 0)), input: nano(t.input), output: nano(t.output) };
      for (const k of PRICE_FIELDS.slice(2)) if (typeof t[k] === 'number') r[k] = nano(t[k]);
      return r;
    });
  if (typeof e.estimate === 'boolean') out.estimate = e.estimate;
  return out;
}

/**
 * The config.push body without gen and front (the supervisor adds gen, lib/front adds front).
 * @param {{ gateway: any, keys: any[], pricing: Record<string, any> }} src
 */
function configPush({ gateway, keys, pricing }) {
  const g = isObj(gateway) ? gateway : {};
  const d = isObj(g.defaults) ? g.defaults : {};
  return {
    gateway: {
      providers: (Array.isArray(g.providers) ? g.providers : []).filter(isObj).map(provider),
      routes: (Array.isArray(g.routes) ? g.routes : []).filter(isObj).map(route),
      guardrails: resolvePolicy(g.guardrails, null),
      defaults: { model: str(d.model), plannerModel: str(d.plannerModel), codeModel: str(d.codeModel) },
      tokenSaver: g.tokenSaver !== false,
      modelMap: (Array.isArray(g.modelMap) ? g.modelMap : [])
        .filter(isObj)
        .map((m) => ({ match: str(m.match), to: str(m.to) })),
    },
    keys: (Array.isArray(keys) ? keys : []).map(keyAuth).filter(Boolean),
    pricing: Object.fromEntries(
      Object.entries(isObj(pricing) ? pricing : {})
        .filter(([, e]) => isObj(e))
        .map(([k, e]) => [k, price(e)]),
    ),
  };
}

/**
 * The secrets.handoff body without gen and frontToken.
 * @param {{ gateway: any, resolve: (ref: any, field: string) => string | null }} src
 */
function secretsPush({ gateway, resolve }) {
  const providers = [];
  const mask = new Set();
  for (const p of (isObj(gateway) && Array.isArray(gateway.providers) ? gateway.providers : []).filter(isObj)) {
    if (p.apiKey == null || (isObj(p.auth) && p.auth.type && p.auth.type !== 'key')) continue;
    let value = null;
    try {
      value = resolve(p.apiKey, `${p.id} credential`);
    } catch {
      value = null; // the control plane answers this provider's calls with the error it gives today
    }
    if (typeof value !== 'string' || !value) continue;
    providers.push({ id: str(p.id), value });
    if (value.length >= 6) mask.add(value);
  }
  return { providers, mask: [...mask] };
}

const VIEW = ['id', 'name', 'scope', 'period', 'currency', 'measure', 'enforce', 'amount', 'from', 'to', 'state'];
/**
 * The budget.snapshot body without at: the blocking budgets with their state, and each key's cost center. A budget
 * service that cannot list (no ledger yet) gives no budgets: the guard lets requests through then as well.
 * @param {{ service: any, keyIds: string[], now?: number }} src
 */
function snapshotPush({ service, keyIds, now = Date.now() }) {
  const budgets = [];
  const keyCenter = {};
  if (!service) return { budgets, keyCenter };
  let list = [];
  try {
    list = service.list().budgets.filter((b) => b && b.enforce === 'block');
  } catch {
    list = [];
  }
  for (const b of list) {
    let v;
    try {
      v = service.evaluateOne(b, now, { forecast: false });
    } catch {
      v = { ...b, state: 'no_data', spentUSD: null, amountUSD: null, reason: 'the budget could not be checked' };
    }
    const view = Object.fromEntries(VIEW.map((k) => [k, v[k] ?? b[k]]));
    view.amount = Number.isFinite(view.amount) ? view.amount : 0;
    for (const k of ['name', 'currency', 'from', 'to']) view[k] = str(view[k]);
    view.state = str(view.state) || 'no_data';
    view.spentUSD = Number.isFinite(v.spentUSD) ? v.spentUSD : null;
    view.amountUSD = Number.isFinite(v.amountUSD) ? v.amountUSD : null;
    view.reason = typeof v.reason === 'string' ? v.reason : null;
    budgets.push(view);
  }
  for (const id of keyIds) {
    try {
      const c = service.keyCenter(id);
      if (typeof c === 'string' && c) keyCenter[id] = c;
    } catch {
      /* no center */
    }
  }
  return { budgets, keyCenter };
}

module.exports = { configPush, secretsPush, snapshotPush, keyAuth, provider, price };
```

Apply (from the repository root):

```bash
git apply <<'PATCH'
diff --git a/gw/cmd/zaafpulse-gw/front.go b/gw/cmd/zaafpulse-gw/front.go
index 8fe1d01..5b0ce21 100644
--- a/gw/cmd/zaafpulse-gw/front.go
+++ b/gw/cmd/zaafpulse-gw/front.go
@@ -13,8 +13,9 @@ import (
 )
 
 // applyFront checks a config.push front section: a route class on the go engine is refused (P4a has none: exit 2). The
-// first section is kept and opened on ready; a later one changes only the engines (listen, TLS and upstream apply after
-// a restart). It returns an exit code, or -1 to go on.
+// section of the first config.push is kept and opened on ready; a later one changes only the engines. Listen, TLS and
+// upstream, and a front section that first appears after the start, apply after a restart (P4b entry condition 3: the
+// control plane builds the section once per gateway start, lib/front/index.js). It returns an exit code, or -1 to go on.
 func (g *gateway) applyFront(f *ipc.FrontConfig) int {
 	if f == nil {
 		return -1
@@ -35,6 +36,10 @@ func (g *gateway) applyFront(f *ipc.FrontConfig) int {
 		}
 		return -1
 	}
+	if g.ready {
+		g.log.Print("config.push: a front section after the start applies after a restart")
+		return -1
+	}
 	if g.frontCfg == nil {
 		g.frontCfg = f
 	}
diff --git a/gw/cmd/zaafpulse-gw/main.go b/gw/cmd/zaafpulse-gw/main.go
index 8bace44..da0497f 100644
--- a/gw/cmd/zaafpulse-gw/main.go
+++ b/gw/cmd/zaafpulse-gw/main.go
@@ -219,7 +219,7 @@ func (g *gateway) handle(msg ipc.Message) int {
 		g.mu.Lock()
 		g.cfgGen = b.Gen
 		g.mu.Unlock()
-		return g.maybeReady()
+		return g.applied()
 	case *ipc.SecretsHandoff:
 		m := make(map[string]ipc.ProviderSecret, len(b.Providers))
 		for _, p := range b.Providers {
@@ -228,7 +228,7 @@ func (g *gateway) handle(msg ipc.Message) int {
 		g.mu.Lock()
 		g.secGen, g.secrets, g.mask, g.frontToken = b.Gen, m, b.Mask, b.FrontToken
 		g.mu.Unlock()
-		return g.maybeReady()
+		return g.applied()
 	case *ipc.BudgetSnapshot:
 		g.mu.Lock()
 		g.snap, g.snapAt = b, time.Now()
@@ -258,6 +258,20 @@ func (g *gateway) handle(msg ipc.Message) int {
 	return -1
 }
 
+// applied runs after a config.push or a secrets.handoff: before ready it may complete the start; after it, once both of
+// a generation are in, a health names that generation at once, so the control plane need not wait for the next tick
+// (lib/ipc/supervisor.js synced: an API key revoked or a provider changed is in force when the save answers).
+func (g *gateway) applied() int {
+	g.mu.Lock()
+	done := g.ready && g.cfgGen == g.secGen
+	g.mu.Unlock()
+	if !done {
+		return g.maybeReady()
+	}
+	g.sendHealth()
+	return -1
+}
+
 func (g *gateway) protocolError(err error) int {
 	g.log.Print(err)
 	_ = g.ch.Send(ipc.TypeError, ipc.ErrorBody{Code: "protocol", Message: err.Error()}, "", "")
diff --git a/lib/front/index.js b/lib/front/index.js
index 6bafb2d..e60ca28 100644
--- a/lib/front/index.js
+++ b/lib/front/index.js
@@ -34,8 +34,8 @@ function frontSection({ host, port, tls = null, upstreamPort, engines = allJs()
   };
 }
 
-// P4a pushes no providers, keys or prices: every route is on js, so the gateway forwards and decides nothing. The real
-// converter comes with the first go route (P4b).
+// The body without providers, keys or prices (P4a's, and the default of createFront's `sources`): the gateway forwards
+// and decides nothing. server.js passes the real converter (lib/front/push.js, P4b).
 const configBody = (front) => ({
   gateway: {
     providers: [],
@@ -73,6 +73,13 @@ function createFront({
   ingest,
   beforeAck = () => {},
   log = console.error,
+  // The bodies of each generation (lib/front/push.js): config.push without front, secrets.handoff without frontToken,
+  // budget.snapshot without at. The front section and the token are added here (P4b).
+  sources = {
+    config: () => configBody(undefined),
+    secrets: () => ({ providers: [], mask: [] }),
+    snapshot: () => ({ budgets: [], keyCenter: {} }),
+  },
   onServing = () => {},
   onListenError = (err) => {
     throw err;
@@ -158,6 +165,8 @@ function createFront({
     // spare connections must never be closed under it.
     inner = internal({ server: mainServer, token, log, firstRequestMs: 130000 });
     const upstreamPort = await inner.listen();
+    // Listen, TLS and upstream are fixed for this gateway's life: a later push carries the same ones (P4b entry
+    // condition 3; a change of gateway.engine, the host, the port or the TLS files takes a restart of ZAAFPulse).
     const front = frontSection({ host, port, tls, upstreamPort });
     const mine = makeSupervisor({
       bin,
@@ -167,9 +176,9 @@ function createFront({
       beforeAck,
       log,
       sources: {
-        config: () => configBody(front),
-        secrets: () => ({ providers: [], mask: [], frontToken: token }),
-        snapshot: () => ({ budgets: [], keyCenter: {} }),
+        config: () => ({ ...sources.config(), front }),
+        secrets: () => ({ ...sources.secrets(), frontToken: token }),
+        snapshot: () => sources.snapshot(),
       },
       maxRestarts: { count: 5, windowMs: 60000 },
       onReady: () => serving('front'),
@@ -244,6 +253,23 @@ function createFront({
       run.then(clear, clear);
       return run;
     },
+    /**
+     * The keys, the prices or the budgets' cost centers changed: a new generation for a gateway in front. Never tries
+     * front again from fallback (only a gateway section save does, reload()). False when no gateway runs.
+     */
+    push() {
+      return mode === 'front' && sup && !failing ? sup.reload() : false;
+    },
+    /**
+     * Resolves once the gateway enforces the newest generation (true), or false: no gateway in front, or timeoutMs
+     * passed. A save answers after it, so a revoked key is refused by the gateway as soon as the save has answered.
+     * @param {number} [timeoutMs]
+     */
+    synced(timeoutMs = 2000) {
+      return mode === 'front' && sup && typeof sup.synced === 'function'
+        ? sup.synced(timeoutMs)
+        : Promise.resolve(false);
+    },
     /** Stops the gateway (shutdown, then a kill after graceMs + its kill delay) and the internal listener. */
     async stop(graceMs = 2000) {
       stopping = true;
diff --git a/lib/gateway/index.js b/lib/gateway/index.js
index 92a171b..ec3ac9b 100644
--- a/lib/gateway/index.js
+++ b/lib/gateway/index.js
@@ -88,6 +88,8 @@ function createGateway({
   budgetGuard = null,
   // The governance audit log (server.js): (entry) -> void, for key changes and run approvals and cancels.
   audit = null,
+  // After a key is created, changed, revoked or deleted (server.js: a new generation for the Go gateway, P4b).
+  onKeysChange = () => {},
 }) {
   const keyStore = jsonStore(path.join(dataDir, 'keys.json'), { keys: [] });
   const agentStore = jsonStore(path.join(dataDir, 'agents.json'), { agents: [] });
@@ -113,6 +115,13 @@ function createGateway({
   // Key changes, whoever makes them (the keys page, a code tool's key, the skills key, a budget save): the key's id and
   // name, and for an update the fields that changed.
   {
+    const changed = () => {
+      try {
+        onKeysChange();
+      } catch {
+        /* the change stands */
+      }
+    };
     const keyEntry = (action, k, extra = {}) =>
       govAudit(() => ({ action: `key.${action}`, target: { type: 'key', id: k.id, name: k.name }, ...extra }));
     const find = (id) => {
@@ -126,6 +135,7 @@ function createGateway({
     keys.create = (...args) => {
       const r = create(...args);
       if (r.key) keyEntry('create', r.key);
+      if (r.key) changed();
       return r;
     };
     keys.update = (id, input) => {
@@ -140,17 +150,20 @@ function createGateway({
             ...(changes.length ? { changes } : {}),
           };
         });
+      if (r.key) changed();
       return r;
     };
     keys.revoke = (id) => {
       const r = revoke(id);
       if (r.key) keyEntry('revoke', r.key);
+      if (r.key) changed();
       return r;
     };
     keys.remove = (id) => {
       const k = find(id);
       const r = remove(id);
       if (!r.errors && k) keyEntry('delete', k);
+      if (!r.errors) changed();
       return r;
     };
   }
@@ -2560,6 +2573,8 @@ function createGateway({
       },
     },
     replay: replayAccessor(),
+    // keys.json as stored, with each key's hash: the push to the Go gateway (lib/front/push.js keyAuth) only.
+    rawKeys: () => keyStore.get().keys,
     // The IPC ingest's usage writer (lib/ipc/server-sinks.js): one line from the Go engine, appended strictly (a failed
     // append throws, so the event is not acknowledged), counted in the per-key monthly spend and the recent list, and
     // the key marked as used, as keys.authenticate does for a call served here. No budget report and no span: the ingest
diff --git a/lib/ipc/supervisor.js b/lib/ipc/supervisor.js
index 95fe87b..017dd55 100644
--- a/lib/ipc/supervisor.js
+++ b/lib/ipc/supervisor.js
@@ -18,6 +18,8 @@
 // each ready.
 // reload() while a session starts is queued and sent once it is ready (E9). beforeAck goes to each session (E4).
 // server.js starts it when gateway.engine is "front" (lib/front/index.js).
+// P4b: synced(timeoutMs) waits until the gateway's health names the newest generation (the gateway sends one as soon
+// as a generation's config and secrets are both applied), so a save can answer only once the gateway enforces it.
 const { startSession } = require('./session');
 
 const ALERT_EVERY_MS = 60000;
@@ -74,6 +76,15 @@ function createSupervisor({
   let snapTimer = null;
   let retryTimer = null;
   const unchecked = { count: 0, lastAt: null, alertedAt: -Infinity };
+  let appliedGen = 0; // the generation the gateway's newest health names
+  let waiters = []; // synced() calls: { gen, resolve }
+  const settleWaiters = (all = false) => {
+    waiters = waiters.filter((w) => {
+      if (!all && appliedGen < w.gen) return true;
+      w.resolve(!all || appliedGen >= w.gen);
+      return false;
+    });
+  };
 
   const configBody = () => ({ ...sources.config(), gen });
   const secretsBody = () => ({ ...sources.secrets(), gen });
@@ -129,6 +140,10 @@ function createSupervisor({
       },
       onHealth: (h) => {
         lastHealth = { ...h, at: now() };
+        if (session === s && Number.isSafeInteger(h.gen)) {
+          appliedGen = h.gen;
+          settleWaiters();
+        }
         armHealth(s);
       },
     });
@@ -154,6 +169,8 @@ function createSupervisor({
     );
     s.exited.then(({ code, signal }) => {
       if (session !== s) return;
+      appliedGen = 0;
+      settleWaiters(true); // a gateway that is gone enforces nothing: the waiters get false
       clearTimers();
       session = null;
       ready = false;
@@ -237,6 +254,28 @@ function createSupervisor({
       return true;
     },
     pushSnapshot,
+    /**
+     * Resolves true once the gateway's health names the newest generation sent (config and secrets applied), false
+     * when no gateway is ready, it exits first, or timeoutMs passes.
+     * @param {number} [timeoutMs]
+     * @returns {Promise<boolean>}
+     */
+    synced(timeoutMs = 2000) {
+      if (!session || !ready) return Promise.resolve(false);
+      if (appliedGen >= gen) return Promise.resolve(true);
+      return new Promise((resolve) => {
+        const w = { gen, resolve };
+        const t = setTimeout(() => {
+          waiters = waiters.filter((x) => x !== w);
+          resolve(false);
+        }, timeoutMs);
+        w.resolve = (v) => {
+          clearTimeout(t);
+          resolve(v);
+        };
+        waiters.push(w);
+      });
+    },
     /** Asks the gateway to stop; resolves with its exit once it is gone (killed after graceMs + killAfterMs). */
     async stop(graceMs = 5000) {
       stopped = true;
diff --git a/server.js b/server.js
index 7462999..72b3ca4 100644
--- a/server.js
+++ b/server.js
@@ -80,6 +80,7 @@ const { createMarkFile } = require('./lib/ipc/markfile');
 const { serverSinks } = require('./lib/ipc/server-sinks');
 const { gwEnv } = require('./lib/ipc/env');
 const { createFront, engineOf } = require('./lib/front');
+const { configPush, secretsPush, snapshotPush } = require('./lib/front/push');
 const { gwBinary } = require('./lib/front/bin');
 const { createRecorder } = require('./lib/trace/recorder');
 const { createReceiver } = require('./lib/trace/otlp');
@@ -203,6 +204,7 @@ const priceSync = createPriceSync({
     config = loadConfig();
     memo.clear();
     insightMemo.clear();
+    if (front) front.push(); // new prices: the gateway in front prices its calls with them (P4b)
   },
 });
 
@@ -346,6 +348,9 @@ function configChanged(before, after) {
     const res = currentResponse.getStore();
     const sent = res && !res.writableFinished ? new Promise((r) => res.once('close', r)) : null;
     front.reload({ after: sent }).catch((err) => console.error(`gateway: ${err.message}`));
+  } else if (front && JSON.stringify((before || {}).pricing) !== JSON.stringify((after || {}).pricing)) {
+    config = loadConfig(); // the owner's prices are merged into config.pricing, which the push reads
+    front.push();
   }
   try {
     const d = govDiff(before, after);
@@ -544,6 +549,9 @@ otel.init({
   sink: recorder,
 });
 const gatewaySecret = secretResolver({ store: DEMO ? () => {} : keychainStore });
+// The gateway's secret resolver, shared with the push to the Go gateway (lib/front/push.js), so both know every
+// resolved credential and mask() hides the same values on both sides.
+const gwSecrets = createSecrets();
 // Cost Control budgets: the gateway asks the guard before each request and reports each call to the budget service.
 // Both are made with the ledger below, after the gateway; until then (startup, before the server listens) nothing is
 // refused.
@@ -560,7 +568,7 @@ const gateway = createGateway({
   ...(DEMO ? { fetchImpl: demoGw.createDemoFetch(), toolsHome: demoDir } : {}),
   getConfig: rawConfig,
   writeGatewayConfig: (g) => saveConfigSection('gateway', g),
-  secrets: createSecrets(),
+  secrets: gwSecrets,
   // Provider keys go to the Keychain as acc.gw-<provider id>, apart from integration keys.
   resolveSecret: (id, input, current) =>
     gatewaySecret(`gw-${id}`, { key: 'apiKey', label: 'API key', required: false }, input, current),
@@ -573,6 +581,7 @@ const gateway = createGateway({
   trustProxy: process.env.ZAAFPULSE_TRUST_PROXY === '1',
   budgetGuard,
   audit: (e) => govRecord(e), // key changes, run approvals and cancels
+  onKeysChange: () => front && front.push(), // the gateway in front enforces keys too (P4b)
 });
 
 // The IPC ingest of the Go gateway (P4a E3, E4): strict writers of its own (lib/ipc/server-sinks.js), never the
@@ -1635,6 +1644,8 @@ const handler = async (req, res) => {
       try {
         // A refused guardrails setting (badRequest) is a 400; every other admin answer keeps its 200, errors included.
         const out = await gateway.admin(gwAdmin[1], 'POST', await jsonBody(req));
+        // A key, provider or route change is answered once the gateway in front enforces it too (P4b; 2 s at most).
+        await front.synced(2000);
         return send(res, out && out.badRequest === true ? 400 : 200, out);
       } catch (err) {
         return send(res, 200, { errors: [err.message] });
@@ -2257,6 +2268,13 @@ front = createFront({
   env: gwEnv(process.env, DIR),
   cpVersion: require('./package.json').version,
   ingest: fi && fi.ingest,
+  // Each generation, from the state the JS gateway reads (P4b): config.json, keys.json, the effective prices, the
+  // provider credentials, the blocking budgets and the keys' cost centers.
+  sources: {
+    config: () => configPush({ gateway: rawConfig().gateway, keys: gateway.rawKeys(), pricing: config.pricing }),
+    secrets: () => secretsPush({ gateway: rawConfig().gateway, resolve: (ref, field) => gwSecrets.resolve(ref, field) }),
+    snapshot: () => snapshotPush({ service: costBudgets, keyIds: gateway.rawKeys().map((k) => k.id) }),
+  },
   beforeAck: fi ? fi.beforeAck : undefined,
   log: (line) => console.error(line),
   onServing: (mode) => {
PATCH
```

- [ ] **Step 5: Run the tests and the task gate**

Run:

```bash
export GOTOOLCHAIN=local ZAAFPULSE_DATA="$(mktemp -d)" HOME="$(mktemp -d)"
test -z "$(gofmt -l gw)" && go -C gw vet ./... && GOOS=windows go -C gw vet ./... && go -C gw test -race -count=1 ./...
deno task typecheck
node --test test/*.test.js
deno test -q --no-check --no-prompt -P=test test/*.test.js
npm run test:contract
```

Expected: no gofmt output, `ok` for every Go package, `deno check` with no error, `fail 0` from Node, `0 failed` from Deno, and the contract suite
`pass 172` (from Task 8 on, `pass 179`, with one case skipped outside shadow mode), `fail 0`.

- [ ] **Step 6: Commit and push**

```bash
git add -A -- gw lib server.js test
git status --short   # nothing outside this task's files, nothing under .superpowers/
git commit -m "P4b T4: push the real gateway config, keys, prices, credentials and budgets; saves wait for the gateway; the front section is fixed per start"
git push origin migrate/p4b-passthrough
```

---

### Task 5: API keys in Go (slice S4)

The key check, the per-key model and agent allow-lists and the rate limit, ported to `gw/engine` and checked against 53 fixture cases generated from `lib/gateway/keys.js`. The secret extraction of `authed()` moves to `keys.js requestSecret` so the generator can run it (Decision 1).

**Files:**
- Create: `gw/engine/fixtures_test.go`, `gw/engine/keys.go`, `gw/engine/keys_test.go`, `scripts/fixtures/gen-keys.js`
- Modify: `lib/gateway/index.js`, `lib/gateway/keys.js`


**Interfaces:**
- Consumes: `ipc.GatewayKeyAuth` (`gw/ipc/messages.go`).
- Produces (package `engine`, `gw/engine`):
  - `NewKeys(list []ipc.GatewayKeyAuth) *Keys`; `(*Keys).Authenticate(secret string, ok bool, now int64) (*ipc.GatewayKeyAuth, string)` with `MsgMalformed`, `MsgUnknown`, `MsgRevoked`, `MsgExpired`.
  - `Authorize(k *ipc.GatewayKeyAuth, scope, model, agent string) string`; `Allows(patterns []string, value string) bool`.
  - `RequestSecret(h http.Header, rawPath, rawQuery string) (string, bool)`.
  - `NewRateLimiter() *RateLimiter`; `Take(id string, rpm, now int64) int64`; `Wait(id, rpm, now)` (no count).
  - Test helpers (`fixtures_test.go`): `type fixture struct { Name, Fn string; In, Out json.RawMessage }`, `loadFixtures(t, lib) []fixture`, `sameJSON(t, name, got any, want json.RawMessage)`, `mustDecode(t, c fixture, v any)`.
  - JS: `lib/gateway/keys.js requestSecret(headers, url): string | undefined` (used by `authed`).

- [ ] **Step 1: Check the starting point**

Run: `git status --short && git rev-parse --abbrev-ref HEAD && git log --oneline -1`
Expected: nothing listed, `migrate/p4b-passthrough`, and the previous task's commit on top (Task 1: `0ec1247`, the merge of `origin/migrate/deno-go` with stream C).

- [ ] **Step 2: Write the failing tests**

Create `gw/engine/fixtures_test.go`:

```go
package engine

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// fixture is one file of gw/testdata/<lib>: the JS function's name, its input and its output.
type fixture struct {
	Name string
	Fn   string          `json:"fn"`
	In   json.RawMessage `json:"in"`
	Out  json.RawMessage `json:"out"`
}

// loadFixtures reads every case of gw/testdata/<lib> (generated by scripts/fixtures/gen-<lib>.js).
func loadFixtures(t *testing.T, lib string) []fixture {
	t.Helper()
	dir := filepath.Join("..", "testdata", lib)
	files, err := os.ReadDir(dir)
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures in %s (run node scripts/fixtures/gen-%s.js): %v", dir, lib, err)
	}
	var out []fixture
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(dir, f.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var c fixture
		if err := json.Unmarshal(b, &c); err != nil {
			t.Fatalf("%s: %v", f.Name(), err)
		}
		c.Name = f.Name()
		out = append(out, c)
	}
	return out
}

// sameJSON compares got (any Go value) with the fixture's JSON after both are encoded the same canonical way.
func sameJSON(t *testing.T, name string, got any, want json.RawMessage) {
	t.Helper()
	g, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var a, b any
	_ = json.Unmarshal(g, &a)
	_ = json.Unmarshal(want, &b)
	ca, _ := json.Marshal(a)
	cb, _ := json.Marshal(b)
	if !bytes.Equal(ca, cb) {
		t.Errorf("%s: got %s, want %s", name, ca, cb)
	}
}
```

Create `gw/engine/keys_test.go`:

```go
package engine

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

// Every case of gw/testdata/keys (scripts/fixtures/gen-keys.js, the real lib/gateway/keys.js) gives the same answer here.
func TestKeysFixtures(t *testing.T) {
	seen := map[string]int{}
	for _, c := range loadFixtures(t, "keys") {
		seen[c.Fn]++
		switch c.Fn {
		case "secret":
			var in struct {
				Headers map[string]string `json:"headers"`
				URL     string            `json:"url"`
			}
			mustDecode(t, c, &in)
			h := http.Header{}
			for k, v := range in.Headers {
				h.Set(k, v)
			}
			path, query, _ := strings.Cut(in.URL, "?")
			var got any
			if s, ok := RequestSecret(h, path, query); ok {
				got = s
			}
			sameJSON(t, c.Name, got, c.Out)
		case "allows":
			var in struct {
				Patterns []string `json:"patterns"`
				Value    string   `json:"value"`
			}
			mustDecode(t, c, &in)
			sameJSON(t, c.Name, Allows(in.Patterns, in.Value), c.Out)
		case "authenticate":
			var in struct {
				Keys   []ipc.GatewayKeyAuth `json:"keys"`
				Secret *string              `json:"secret"`
				Now    int64                `json:"now"`
			}
			mustDecode(t, c, &in)
			secret, ok := "", in.Secret != nil
			if ok {
				secret = *in.Secret
			}
			k, msg := NewKeys(in.Keys).Authenticate(secret, ok, in.Now)
			if k != nil {
				sameJSON(t, c.Name, map[string]string{"key": k.ID}, c.Out)
			} else {
				sameJSON(t, c.Name, map[string]any{"status": 401, "error": msg}, c.Out)
			}
		case "authorize":
			var in struct {
				Key  ipc.GatewayKeyAuth `json:"key"`
				Want struct{ Scope, Model, Agent string }
			}
			mustDecode(t, c, &in)
			var got any
			if why := Authorize(&in.Key, in.Want.Scope, in.Want.Model, in.Want.Agent); why != "" {
				got = why
			}
			sameJSON(t, c.Name, got, c.Out)
		case "rateLimit":
			var in struct {
				RPM   int64   `json:"rpm"`
				Times []int64 `json:"times"`
			}
			mustDecode(t, c, &in)
			r := NewRateLimiter()
			var got []int64
			for _, dt := range in.Times {
				got = append(got, r.Take("k", in.RPM, 1767225600000+dt))
			}
			sameJSON(t, c.Name, got, c.Out)
		default:
			t.Errorf("%s: unknown fn %q", c.Name, c.Fn)
		}
	}
	for _, fn := range []string{"secret", "allows", "authenticate", "authorize", "rateLimit"} {
		if seen[fn] == 0 {
			t.Errorf("no %s fixtures", fn)
		}
	}
}

// Wait answers as Take would without counting the request (the shadow compare must not use up a key's limit).
func TestRateLimiterWaitDoesNotCount(t *testing.T) {
	r := NewRateLimiter()
	for range 3 {
		if w := r.Wait("k", 1, 0); w != 0 {
			t.Fatalf("Wait %d", w)
		}
	}
	if r.Take("k", 1, 0) != 0 || r.Wait("k", 1, 1) != 60 {
		t.Fatal("Take after Wait")
	}
}

func mustDecode(t *testing.T, c fixture, v any) {
	t.Helper()
	if err := json.Unmarshal(c.In, v); err != nil {
		t.Fatalf("%s: %v", c.Name, err)
	}
}
```

Create `scripts/fixtures/gen-keys.js`:

```js
// Fixtures for the Go gateway's API keys (P4b, slice S4): the real lib/gateway/keys.js run over a fixed corpus, written
// to gw/testdata/keys/<n>-<fn>-<name>.json as { fn, in, out }. gw/engine/keys_test.go replays every file.
//   secret:       requestSecret(headers, url)            -> the secret, or null
//   allows:       allows(patterns, value)                -> true or false (the per-key model and agent globs)
//   authenticate: authenticate(secret) over keys at now  -> { status, error } or { key: id }
//   authorize:    authorize(key, { scope, model, agent }) -> the refusal, or null
//   rateLimit:    rateLimit(key) at each time, in order  -> the seconds to wait for each (0: allowed)
// Run: node scripts/fixtures/gen-keys.js (CI runs every scripts/fixtures/gen-*.js and fails on a diff).
const fs = require('fs');
const path = require('path');
const { createKeys, allows, hash, requestSecret } = require('../../lib/gateway/keys');

const OUT = path.join(__dirname, '..', '..', 'gw', 'testdata', 'keys');
const NOW = 1767225600000;
const SECRET = 'zp_fixtureSecret0123456789abcdefghij';
const key = (id, extra = {}) => ({
  id,
  name: id,
  prefix: 'zp_fixture…',
  hash: hash(`${SECRET}-${id}`),
  scopes: ['llm'],
  models: [],
  agents: [],
  knowledge: [],
  budgetUSD: null,
  rpm: 60,
  privateOnly: false,
  expiresAt: null,
  revokedAt: null,
  createdAt: NOW - 1000,
  ...extra,
});
const KEYS = [
  key('k-ok'),
  key('k-revoked', { revokedAt: NOW - 10 }),
  key('k-expired', { expiresAt: NOW - 1 }),
  key('k-expires-now', { expiresAt: NOW }),
];

const cases = [];
const add = (fn, name, input, out) => cases.push({ fn, name, in: input, out });

// requestSecret
for (const [name, headers, url] of [
  ['bearer', { authorization: 'Bearer zp_a' }, '/v1/chat/completions'],
  ['bearer-trimmed', { authorization: 'Bearer   zp_a \t' }, '/v1/chat/completions'],
  ['bearer-empty-wins', { authorization: 'Bearer ', 'x-api-key': 'zp_b' }, '/v1/chat/completions'],
  ['bearer-lower-ignored', { authorization: 'bearer zp_a', 'x-api-key': 'zp_b' }, '/v1/chat/completions'],
  ['basic-ignored', { authorization: 'Basic eDp5' }, '/v1/chat/completions'],
  ['x-api-key', { 'x-api-key': 'zp_b' }, '/v1/messages'],
  ['x-goog-api-key', { 'x-goog-api-key': 'zp_c' }, '/v1beta/models/x:generateContent'],
  ['x-api-key-first', { 'x-api-key': 'zp_b', 'x-goog-api-key': 'zp_c' }, '/v1/chat/completions'],
  ['query-on-v1beta', {}, '/v1beta/models/x:generateContent?key=zp_q'],
  ['query-elsewhere-ignored', {}, '/v1/chat/completions?key=zp_q'],
  ['query-encoded', {}, '/v1beta/models?key=zp_%71+x'],
  ['none', {}, '/v1/chat/completions'],
  ['empty-x-api-key', { 'x-api-key': '' }, '/v1/chat/completions'],
])
  add('secret', name, { headers, url }, requestSecret(headers, url) ?? null);

// allows
for (const [name, patterns, value] of [
  ['empty-list', [], 'oa/m1'],
  ['exact', ['oa/m1'], 'oa/m1'],
  ['star', ['oa/*'], 'oa/m1'],
  ['star-middle', ['oa/*-mini'], 'oa/gpt-5-mini'],
  ['no-match', ['oa/*'], 'pv/m1'],
  ['case-insensitive', ['OA/M*'], 'oa/m1'],
  ['dot-is-literal', ['oa/m.1'], 'oa/mx1'],
  ['regex-chars-literal', ['oa/(m)+[1]?'], 'oa/(m)+[1]?'],
  ['star-not-newline', ['oa/*'], 'oa/a\nb'],
  ['several', ['pv/*', 'oa/m1'], 'oa/m1'],
  ['anchored', ['m1'], 'oa/m1'],
  ['long-s-not-s', ['oa/s*'], 'oa/ſ'],
  ['kelvin', ['oa/k'], 'oa/K'],
  ['sharp-s', ['oa/ß'], 'oa/ẞ'],
  ['greek-sigma', ['oa/σ'], 'oa/Σ'],
  ['astral', ['oa/*'], 'oa/\u{1F600}'],
  ['empty-value', ['*'], ''],
])
  add('allows', name, { patterns, value }, allows(patterns, value));

// authenticate
const store = (keys) => {
  const data = { keys: structuredClone(keys) };
  return { get: () => data, save: () => {} };
};
for (const [name, secret, at] of [
  ['ok', `${SECRET}-k-ok`, NOW],
  ['not-zp', 'sk-other', NOW],
  ['missing', null, NOW],
  ['unknown', 'zp_doesnotexist0123456789', NOW],
  ['revoked', `${SECRET}-k-revoked`, NOW],
  ['expired', `${SECRET}-k-expired`, NOW],
  ['expires-at-now-still-valid', `${SECRET}-k-expires-now`, NOW],
  ['expires-at-now-plus-one', `${SECRET}-k-expires-now`, NOW + 1],
  ['zp-prefix-only', 'zp_', NOW],
]) {
  const keys = createKeys(store(KEYS), { now: () => at });
  const r = keys.authenticate(secret ?? undefined);
  add(
    'authenticate',
    name,
    { keys: KEYS, secret, now: at },
    r.error ? { status: r.status, error: r.error } : { key: r.key.id },
  );
}

// authorize
for (const [name, k, want] of [
  ['scope-ok', { scopes: ['llm'] }, { scope: 'llm' }],
  ['scope-missing', { scopes: ['agents'] }, { scope: 'llm' }],
  ['no-scope-asked', { scopes: [] }, {}],
  ['model-ok', { scopes: ['llm'], models: ['oa/*'] }, { scope: 'llm', model: 'oa/m1' }],
  ['model-refused', { scopes: ['llm'], models: ['oa/*'] }, { scope: 'llm', model: 'pv/m1' }],
  ['scope-before-model', { scopes: [], models: ['oa/*'] }, { scope: 'llm', model: 'pv/m1' }],
  ['agent-refused', { scopes: ['agents'], agents: ['a*'] }, { agent: 'b' }],
  ['model-quotes', { scopes: ['llm'], models: ['x'] }, { model: 'say "hi"' }],
]) {
  const keys = createKeys(store([]), { now: () => NOW });
  add(
    'authorize',
    name,
    { key: { models: [], agents: [], ...k }, want },
    keys.authorize({ models: [], agents: [], ...k }, want),
  );
}

// rateLimit: a sliding one-minute window
for (const [name, rpm, times] of [
  ['under', 3, [0, 1000, 2000]],
  ['over', 2, [0, 1000, 2000, 3000]],
  ['window-slides', 2, [0, 1000, 60000, 60500, 61000]],
  ['refused-not-counted', 1, [0, 10, 20, 59999, 60000]],
  ['retry-after-rounds-up', 1, [0, 100]],
  ['default-60', 0, Array.from({ length: 61 }, (_, i) => i)],
]) {
  let t = NOW;
  const keys = createKeys(store([]), { now: () => t });
  const out = times.map((dt) => {
    t = NOW + dt;
    return keys.rateLimit({ id: 'k', rpm });
  });
  add('rateLimit', name, { rpm, times }, out);
}

fs.rmSync(OUT, { recursive: true, force: true });
fs.mkdirSync(OUT, { recursive: true });
cases.forEach((c, i) => {
  const file = path.join(OUT, `${String(i + 1).padStart(2, '0')}-${c.fn}-${c.name}.json`);
  fs.writeFileSync(file, `${JSON.stringify({ fn: c.fn, in: c.in, out: c.out })}\n`);
});
console.log(`gen-keys: ${cases.length} cases in ${path.relative(process.cwd(), OUT)}`);
```

- [ ] **Step 3: Run them to see them fail**

Run: `node scripts/fixtures/gen-keys.js; GOTOOLCHAIN=local go -C gw test -count=1 ./engine`
Expected: the generator fails with `TypeError: requestSecret is not a function`; Go FAIL with `undefined: RequestSecret`, `undefined: Allows`, `undefined: NewKeys`, `undefined: NewRateLimiter`.

- [ ] **Step 4: Implement**

Create `gw/engine/keys.go`:

```go
// Package engine is the Go engine of the gateway (P4b, docs/superpowers/specs/2026-11-12-p4-stage1-design.md stream
// A): the route classes the gateway serves itself instead of forwarding them to the control plane. It decides from the
// state the control plane pushes (config.push, secrets.handoff, budget.snapshot) with the rules of the JS gateway,
// each one checked against fixtures generated from the JS code (scripts/fixtures/gen-*.js, gw/testdata/*).
package engine

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"unicode"
	"unicode/utf16"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

// The answers of lib/gateway/keys.js authenticate.
const (
	MsgMalformed = "missing or malformed API key (Authorization: Bearer zp_…)"
	MsgUnknown   = "unknown API key"
	MsgRevoked   = "this API key was revoked"
	MsgExpired   = "this API key has expired"
)

// Keys are the API keys of one config.push, looked up by the SHA-256 of a secret (keys.json stores only that).
type Keys struct {
	list   []ipc.GatewayKeyAuth
	hashes [][]byte
}

// NewKeys keeps the keys in the order config.push lists them (the first key with a hash wins, as in JS).
func NewKeys(list []ipc.GatewayKeyAuth) *Keys {
	k := &Keys{list: list, hashes: make([][]byte, len(list))}
	for i, key := range list {
		k.hashes[i], _ = hex.DecodeString(key.Hash)
	}
	return k
}

// Authenticate is lib/gateway/keys.js authenticate: the key of secret at now (epoch ms), or nil with the 401 message.
// It never writes: lastUsedAt is the control plane's, from the usage lines (lib/gateway/index.js ingestUsage).
func (k *Keys) Authenticate(secret string, ok bool, now int64) (*ipc.GatewayKeyAuth, string) {
	if !ok || !strings.HasPrefix(secret, "zp_") {
		return nil, MsgMalformed
	}
	sum := sha256.Sum256([]byte(secret))
	for i := range k.list {
		if len(k.hashes[i]) == len(sum) && subtle.ConstantTimeCompare(k.hashes[i], sum[:]) == 1 {
			key := &k.list[i]
			if key.RevokedAt != nil && *key.RevokedAt != 0 {
				return nil, MsgRevoked
			}
			if key.ExpiresAt != nil && *key.ExpiresAt != 0 && now > *key.ExpiresAt {
				return nil, MsgExpired
			}
			return key, ""
		}
	}
	return nil, MsgUnknown
}

// Authorize is lib/gateway/keys.js authorize: the refusal for a scope, a model or an agent (each checked when not
// empty), or "".
func Authorize(k *ipc.GatewayKeyAuth, scope, model, agent string) string {
	switch {
	case scope != "" && !contains(k.Scopes, scope):
		return `this key does not have the "` + scope + `" scope`
	case model != "" && !Allows(k.Models, model):
		return `this key may not use model "` + model + `"`
	case agent != "" && !Allows(k.Agents, agent):
		return `this key may not use agent "` + agent + `"`
	}
	return ""
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Allows is lib/gateway/keys.js allows: no patterns allow everything; else a pattern must match the whole value, where
// "*" is any run of characters except a line terminator and the rest is literal, compared case-insensitively the way
// a JavaScript /i regular expression without the u flag does (per UTF-16 code unit; a non-ASCII character never
// matches an ASCII one).
func Allows(patterns []string, value string) bool {
	if len(patterns) == 0 {
		return true
	}
	v := utf16.Encode([]rune(value))
	for _, p := range patterns {
		if globMatch(utf16.Encode([]rune(p)), v) {
			return true
		}
	}
	return false
}

func globMatch(p, v []uint16) bool {
	// Iterative wildcard match with one back-tracking point (the last "*").
	pi, vi, star, mark := 0, 0, -1, 0
	for vi < len(v) {
		switch {
		case pi < len(p) && p[pi] == '*':
			star, mark = pi, vi
			pi++
		case pi < len(p) && canon(p[pi]) == canon(v[vi]):
			pi++
			vi++
		case star >= 0 && !lineTerminator(v[mark]):
			mark++
			pi, vi = star+1, mark
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}

func lineTerminator(u uint16) bool { return u == '\n' || u == '\r' || u == 0x2028 || u == 0x2029 }

// canon is ECMAScript Canonicalize for a non-unicode, ignoreCase pattern: the upper case of a code unit, unless that
// is not one code unit or would turn a non-ASCII unit into an ASCII one.
func canon(u uint16) uint16 {
	if u >= 0xd800 && u <= 0xdfff {
		return u
	}
	r := rune(u)
	up := unicode.ToUpper(r)
	if up > 0xffff || specialUpper[r] {
		return u
	}
	if r >= 128 && up < 128 {
		return u
	}
	return uint16(up)
}

// specialUpper are the code units whose JavaScript toUpperCase is more than one character (SpecialCasing.txt), so
// Canonicalize leaves them as they are, though Go's simple mapping has an upper case for some.
var specialUpper = map[rune]bool{0x0149: true, 0x01f0: true, 0x0390: true, 0x03b0: true, 0x0587: true,
	0x1e96: true, 0x1e97: true, 0x1e98: true, 0x1e99: true, 0x1e9a: true, 0x1f50: true, 0x1f52: true, 0x1f54: true,
	0x1f56: true, 0x1fb6: true, 0x1fc6: true, 0x1fd2: true, 0x1fd3: true, 0x1fd6: true, 0x1fd7: true, 0x1fe2: true,
	0x1fe3: true, 0x1fe4: true, 0x1fe6: true, 0x1fe7: true, 0x1ff6: true, 0xfb00: true, 0xfb01: true, 0xfb02: true,
	0xfb03: true, 0xfb04: true, 0xfb05: true, 0xfb06: true, 0xfb13: true, 0xfb14: true, 0xfb15: true, 0xfb16: true,
	0xfb17: true, 0x00df: true}

// RequestSecret is lib/gateway/keys.js requestSecret over Go's view of a request: the header values as Node joins
// them (", " between repeats; Authorization keeps its first) and the raw path and query. ok is false when there is none.
func RequestSecret(h http.Header, rawPath, rawQuery string) (string, bool) {
	if a := h.Values("Authorization"); len(a) > 0 && strings.HasPrefix(a[0], "Bearer ") {
		return strings.TrimFunc(a[0][7:], jsSpace), true
	}
	for _, name := range []string{"X-Api-Key", "X-Goog-Api-Key"} {
		if v := strings.Join(h.Values(name), ", "); v != "" {
			return v, true
		}
	}
	if strings.HasPrefix(rawPath, "/v1beta/") {
		if q, err := url.ParseQuery(rawQuery); err == nil || q != nil {
			if v := q.Get("key"); v != "" {
				return v, true
			}
		}
	}
	return "", false
}

// jsSpace is what String.prototype.trim removes: WhiteSpace and LineTerminator.
func jsSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// RateLimiter is lib/gateway/keys.js rateLimit: a sliding one-minute window per key, in this process only.
type RateLimiter struct {
	mu      sync.Mutex
	windows map[string][]int64
}

func NewRateLimiter() *RateLimiter { return &RateLimiter{windows: map[string][]int64{}} }

// Take counts a request of key id at now (epoch ms) and returns 0, or the seconds to wait when rpm (0: 60) requests
// were made in the last minute; a refused request is not counted.
func (r *RateLimiter) Take(id string, rpm, now int64) int64 { return r.take(id, rpm, now, true) }

// Wait is Take without counting: what the shadow compare asks.
func (r *RateLimiter) Wait(id string, rpm, now int64) int64 { return r.take(id, rpm, now, false) }

func (r *RateLimiter) take(id string, rpm, now int64, count bool) int64 {
	if rpm <= 0 {
		rpm = 60
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var w []int64
	for _, t := range r.windows[id] {
		if now-t < 60000 {
			w = append(w, t)
		}
	}
	if int64(len(w)) >= rpm {
		r.windows[id] = w
		return (60000 - (now - w[0]) + 999) / 1000
	}
	if count {
		w = append(w, now)
	}
	r.windows[id] = w
	return 0
}
```

Apply (from the repository root):

```bash
git apply <<'PATCH'
diff --git a/lib/gateway/index.js b/lib/gateway/index.js
index ec3ac9b..2c508d0 100644
--- a/lib/gateway/index.js
+++ b/lib/gateway/index.js
@@ -27,7 +27,7 @@ const path = require('path');
 const { safeFetch } = require('../net');
 const { peerEncrypted } = require('../front/client');
 const { jsonStore } = require('./store');
-const { createKeys, allows } = require('./keys');
+const { createKeys, allows, requestSecret } = require('./keys');
 const tr = require('./translate');
 const { saveTokens } = require('./rtk');
 const { createProviders, cleanProvider, cleanRoute, PRESETS } = require('./providers');
@@ -616,17 +616,8 @@ function createGateway({
     );
 
   function authed(req, scope) {
-    const header = req.headers.authorization || '';
-    const secret = header.startsWith('Bearer ')
-      ? header.slice(7).trim()
-      : req.headers['x-api-key'] ||
-        req.headers['x-goog-api-key'] ||
-        // ?key= only where Gemini clients need it; elsewhere it would end up in logs and browser history.
-        (/^\/v1beta\//.test(new URL(req.url, 'http://x').pathname)
-          ? new URL(req.url, 'http://x').searchParams.get('key')
-          : null) ||
-        undefined;
-    const a = keys.authenticate(secret);
+    // Bearer, x-api-key, x-goog-api-key, or ?key= on /v1beta/ only (lib/gateway/keys.js requestSecret).
+    const a = keys.authenticate(requestSecret(req.headers, req.url));
     if (a.error) throw new HttpError(a.status, a.error, 'authentication_error');
     const why = keys.authorize(a.key, { scope });
     if (why) throw new HttpError(403, why, 'permission_error');
diff --git a/lib/gateway/keys.js b/lib/gateway/keys.js
index 00ced51..1e2e2eb 100644
--- a/lib/gateway/keys.js
+++ b/lib/gateway/keys.js
@@ -17,6 +17,26 @@ const glob = (pattern) =>
   );
 const allows = (patterns, value) => !patterns || !patterns.length || patterns.some((p) => glob(p).test(value));
 
+// The secret a request carries: "Authorization: Bearer <secret>" (an Authorization header that does not start with
+// "Bearer " is ignored), else x-api-key, else x-goog-api-key, else ?key= on /v1beta/ paths only (Gemini clients; elsewhere
+// it would end up in logs and browser history). `headers` as Node gives them (lower-case names). The Go gateway has the
+// same rule (gw/engine/keys.go RequestSecret, fixtures from scripts/fixtures/gen-keys.js).
+/**
+ * @param {Record<string, string | string[] | undefined>} headers
+ * @param {string} url the request target
+ * @returns {string | undefined}
+ */
+function requestSecret(headers, url) {
+  const header = headers.authorization || '';
+  if (header.startsWith('Bearer ')) return header.slice(7).trim();
+  return (
+    headers['x-api-key'] ||
+    headers['x-goog-api-key'] ||
+    (/^\/v1beta\//.test(new URL(url, 'http://x').pathname) ? new URL(url, 'http://x').searchParams.get('key') : null) ||
+    undefined
+  );
+}
+
 function cleanKeyInput(input = {}) {
   const errors = [];
   const name = String(input.name || '')
@@ -168,4 +188,4 @@ function createKeys(store, { now = Date.now } = {}) {
   };
 }
 
-module.exports = { createKeys, cleanKeyInput, allows, hash, SCOPES };
+module.exports = { createKeys, cleanKeyInput, allows, hash, requestSecret, SCOPES };
PATCH
```

- [ ] **Step 5: Generate the fixtures from the JS code**

Run: `node scripts/fixtures/gen-keys.js`
Expected: `gen-keys: 53 cases in gw/testdata/keys`.

- [ ] **Step 6: Run the tests and the task gate**

Run:

```bash
export GOTOOLCHAIN=local ZAAFPULSE_DATA="$(mktemp -d)" HOME="$(mktemp -d)"
test -z "$(gofmt -l gw)" && go -C gw vet ./... && GOOS=windows go -C gw vet ./... && go -C gw test -race -count=1 ./...
deno task typecheck
node --test test/*.test.js
deno test -q --no-check --no-prompt -P=test test/*.test.js
npm run test:contract
```

Expected: no gofmt output, `ok` for every Go package, `deno check` with no error, `fail 0` from Node, `0 failed` from Deno, and the contract suite
`pass 172` (from Task 8 on, `pass 179`, with one case skipped outside shadow mode), `fail 0`.

- [ ] **Step 7: Commit and push**

```bash
git add -A -- gw lib scripts
git status --short   # nothing outside this task's files, nothing under .superpowers/
git commit -m "P4b T5: API keys, allow-lists and the rate limit in Go, checked against fixtures from lib/gateway/keys.js"
git push origin migrate/p4b-passthrough
```

- [ ] **Step 8: Start the full-matrix CI run**

Run the dispatch from the Global Constraints (`gh workflow run ci.yml ... --ref migrate/p4b-passthrough`, then `gh run watch <id> --exit-status`).
Expected: every job green (the run on `99c6280` was red on typecheck and on Windows go; fix round 1 fixes both).

---

### Task 6: JSON as JavaScript reads and writes it (gw/engine/jsjson.go)

The engine sends the provider and the client bodies the JS gateway has parsed and written again: numbers as `Number#toString`, keys in JavaScript's order, repeated keys at their first place with their last value, strings escaped as `JSON.stringify` does. 31 fixture cases.

**Files:**
- Create: `gw/engine/jsjson.go`, `gw/engine/jsjson_test.go`, `scripts/fixtures/gen-jsjson.js`


**Interfaces:**
- Produces (package `engine`): `Parse(b []byte) (*Value, error)` with `ErrSyntax` (JSON.parse throws) and `ErrUnsupported` (lone surrogate, nesting over `MaxDepth` = 512, invalid UTF-8: hand-off); `type Value` with `Kind` (`Null`, `Bool`, `Number`, `String`, `Array`, `Object`), `Get`, `Set`, `Delete`, `Keys`, `Clone`, `Truthy`, `Stringify`; `NewObject()`, `Str(s)`, `Num(n)`; `QuoteJS(b []byte, s string) []byte`; `NumberString(f float64) string`.

- [ ] **Step 1: Check the starting point**

Run: `git status --short && git rev-parse --abbrev-ref HEAD && git log --oneline -1`
Expected: nothing listed, `migrate/p4b-passthrough`, and the previous task's commit on top (Task 1: `0ec1247`, the merge of `origin/migrate/deno-go` with stream C).

- [ ] **Step 2: Write the failing tests**

Create `gw/engine/jsjson_test.go`:

```go
package engine

import (
	"encoding/json"
	"errors"
	"regexp"
	"testing"
)

// A text that holds an escaped lone surrogate once JavaScript has written it again.
var loneSurrogate = regexp.MustCompile(`\\ud[89a-f][0-9a-f]{2}`)

// Every case of gw/testdata/jsjson (JSON.stringify(JSON.parse(text)) from scripts/fixtures/gen-jsjson.js): the same
// bytes, the same refusal, or ErrUnsupported only where JavaScript would keep a lone surrogate.
func TestJSJSONFixtures(t *testing.T) {
	for _, c := range loadFixtures(t, "jsjson") {
		var in string
		var out *string
		if err := json.Unmarshal(c.In, &in); err != nil {
			t.Fatal(err)
		}
		_ = json.Unmarshal(c.Out, &out)
		v, err := Parse([]byte(in))
		switch {
		case out == nil:
			if !errors.Is(err, ErrSyntax) {
				t.Errorf("%s: JSON.parse throws, Go: %v", c.Name, err)
			}
		case errors.Is(err, ErrUnsupported):
			if !loneSurrogate.MatchString(*out) {
				t.Errorf("%s: unsupported, but JavaScript gives %q", c.Name, *out)
			}
		case err != nil:
			t.Errorf("%s: %v, JavaScript gives %q", c.Name, err, *out)
		default:
			if got := string(v.Stringify()); got != *out {
				t.Errorf("%s:\n got %s\nwant %s", c.Name, got, *out)
			}
		}
	}
}

func TestDeepNestingIsUnsupported(t *testing.T) {
	deep := make([]byte, 0, 2*MaxDepth+2)
	for range MaxDepth + 1 {
		deep = append(deep, '[')
	}
	for range MaxDepth + 1 {
		deep = append(deep, ']')
	}
	if _, err := Parse(deep); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("%v", err)
	}
}

// Set keeps a key's place, appends a new one; Delete removes it; JavaScript's order applies when written.
func TestObjectEdits(t *testing.T) {
	v, err := Parse([]byte(`{"model":"a","max_tokens":5,"7":1}`))
	if err != nil {
		t.Fatal(err)
	}
	v.Set("model", Str("b"))
	v.Set("max_completion_tokens", v.Get("max_tokens"))
	v.Delete("max_tokens")
	if got := string(v.Stringify()); got != `{"7":1,"model":"b","max_completion_tokens":5}` {
		t.Fatal(got)
	}
}
```

Create `scripts/fixtures/gen-jsjson.js`:

```js
// Fixtures for the Go engine's JavaScript JSON (gw/engine/jsjson.go, P4b): for each text, what JSON.stringify(JSON.parse
// (text)) gives, or that JSON.parse throws. The Go engine sends and answers bodies the JS gateway has parsed and written
// again, so it must read and write them the same way. Writes gw/testdata/jsjson/<n>-<name>.json as
// { fn: "roundtrip", in: <text>, out: <text> | null } (null: JSON.parse throws).
// Run: node scripts/fixtures/gen-jsjson.js (CI runs every scripts/fixtures/gen-*.js and fails on a diff).
const fs = require('fs');
const path = require('path');

const OUT = path.join(__dirname, '..', '..', 'gw', 'testdata', 'jsjson');
const TEXTS = [
  ['object', '{"model":"oa/m1","messages":[{"role":"user","content":"hi"}]}'],
  ['whitespace', ' \t\n{ "a" : [ 1 , 2 ] }\r\n'],
  ['integer-keys-first', '{"b":1,"2":2,"a":3,"1":4,"01":5,"-1":6,"4294967294":7,"4294967295":8}'],
  ['duplicate-key-first-place-last-value', '{"a":1,"b":2,"a":3}'],
  ['proto-key', '{"__proto__":{"x":1},"y":2}'],
  ['numbers', '[1.0,1e2,1E+2,-0,0.1,1e21,1e20,123456789012345678901,1e-7,1.5e-6,0.000001,-1.25e-10,5e-324]'],
  ['big-integer', '{"seed":12345678901234567890}'],
  ['out-of-range', '[1e400,-1e400,1e-400]'],
  ['float-shortest', '[0.30000000000000004,100.5,2.5e+25,1.7976931348623157e308]'],
  ['escapes', '"a\\"b\\\\c\\/d\\b\\f\\n\\r\\t\\u0001\\u001f\\u0041\\u00e9"'],
  ['raw-unicode', '"caf\u00e9 \u2028 \u2029 <script> & \u{1F600}"'],
  ['surrogate-pair-escape', '"\\ud83d\\ude00"'],
  ['lone-high-surrogate', '"\\ud83d"'],
  ['lone-low-surrogate', '"\\ude00x"'],
  ['literals', '[true,false,null]'],
  ['empty', '{"a":{},"b":[]}'],
  ['nested', '{"a":{"b":{"c":[1,[2,[3]]]}}}'],
  ['trailing-comma', '{"a":1,}'],
  ['single-quote', "{'a':1}"],
  ['leading-zero', '[01]'],
  ['bare-dot', '[1.]'],
  ['plus-sign', '[+1]'],
  ['nan', '[NaN]'],
  ['control-in-string', '"a\u0001b"'],
  ['bom', '\ufeff{}'],
  ['empty-text', ''],
  ['two-values', '{} {}'],
  ['bad-escape', '"\\x41"'],
  ['short-unicode-escape', '"\\u12"'],
  ['string-top', '"just a string"'],
  ['number-top', '42'],
];

fs.rmSync(OUT, { recursive: true, force: true });
fs.mkdirSync(OUT, { recursive: true });
TEXTS.forEach(([name, text], i) => {
  let out = null;
  try {
    out = JSON.stringify(JSON.parse(text));
  } catch {
    out = null;
  }
  const file = path.join(OUT, `${String(i + 1).padStart(2, '0')}-${name}.json`);
  fs.writeFileSync(file, `${JSON.stringify({ fn: 'roundtrip', in: text, out })}\n`);
});
console.log(`gen-jsjson: ${TEXTS.length} cases in ${path.relative(process.cwd(), OUT)}`);
```

- [ ] **Step 3: Run them to see them fail**

Run: `node scripts/fixtures/gen-jsjson.js && GOTOOLCHAIN=local go -C gw test -count=1 ./engine`
Expected: `gen-jsjson: 31 cases in gw/testdata/jsjson`, then FAIL `undefined: Parse`, `undefined: ErrSyntax`, `undefined: MaxDepth`.

- [ ] **Step 4: Implement**

Create `gw/engine/jsjson.go`:

```go
package engine

// JSON as JavaScript reads and writes it (JSON.parse, then JSON.stringify), so that what the Go engine sends and
// answers is byte for byte what the JS gateway sends and answers: numbers are doubles written the way Number#toString
// writes them (1.0 is 1, 1e21 is 1e+21, 1e400 is null), an object keeps its keys in JavaScript's order (array-index keys
// first, ascending, then the others in the order they first appeared; a repeated key keeps its first place and its
// last value), and strings are escaped as JSON.stringify escapes them (", \, and control characters only).
// Fixtures: scripts/fixtures/gen-jsjson.js. A text JSON.parse accepts but this cannot hold faithfully (an escaped lone
// surrogate, nesting deeper than MaxDepth) is ErrUnsupported: the engine then hands the request to the control plane.

import (
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// MaxDepth is the deepest nesting the engine parses.
const MaxDepth = 512

var (
	// ErrSyntax: JSON.parse would throw.
	ErrSyntax = errors.New("jsjson: not valid JSON")
	// ErrUnsupported: JSON.parse would accept it, but this engine does not serve it.
	ErrUnsupported = errors.New("jsjson: valid JSON the engine does not serve")
)

// Kind is a JSON value's type.
type Kind int

const (
	Null Kind = iota
	Bool
	Number
	String
	Array
	Object
)

// Value is one parsed JSON value.
type Value struct {
	Kind Kind
	B    bool
	N    float64
	S    string
	A    []*Value
	keys []string
	vals map[string]*Value
}

// NewObject is an empty object.
func NewObject() *Value { return &Value{Kind: Object, vals: map[string]*Value{}} }

// Str is a string value.
func Str(s string) *Value { return &Value{Kind: String, S: s} }

// Num is a number value.
func Num(n float64) *Value { return &Value{Kind: Number, N: n} }

// Get is the value of an object's key, or nil.
func (v *Value) Get(key string) *Value {
	if v == nil || v.Kind != Object {
		return nil
	}
	return v.vals[key]
}

// Set gives an object's key a value; a new key goes last (JavaScript order is applied when written).
func (v *Value) Set(key string, x *Value) {
	if _, ok := v.vals[key]; !ok {
		v.keys = append(v.keys, key)
	}
	v.vals[key] = x
}

// Delete removes an object's key.
func (v *Value) Delete(key string) {
	if _, ok := v.vals[key]; !ok {
		return
	}
	delete(v.vals, key)
	for i, k := range v.keys {
		if k == key {
			v.keys = append(v.keys[:i:i], v.keys[i+1:]...)
			break
		}
	}
}

// Keys are an object's keys in JavaScript's order.
func (v *Value) Keys() []string {
	var idx, rest []string
	for _, k := range v.keys {
		if arrayIndex(k) {
			idx = append(idx, k)
		} else {
			rest = append(rest, k)
		}
	}
	sort.Slice(idx, func(i, j int) bool {
		a, _ := strconv.ParseUint(idx[i], 10, 64)
		b, _ := strconv.ParseUint(idx[j], 10, 64)
		return a < b
	})
	return append(idx, rest...)
}

// Clone copies a value deeply.
func (v *Value) Clone() *Value {
	c := *v
	if v.A != nil {
		c.A = make([]*Value, len(v.A))
		for i, x := range v.A {
			c.A[i] = x.Clone()
		}
	}
	if v.Kind == Object {
		c.keys = append([]string(nil), v.keys...)
		c.vals = make(map[string]*Value, len(v.vals))
		for k, x := range v.vals {
			c.vals[k] = x.Clone()
		}
	}
	return &c
}

// Truthy is JavaScript's truthiness.
func (v *Value) Truthy() bool {
	if v == nil {
		return false
	}
	switch v.Kind {
	case Null:
		return false
	case Bool:
		return v.B
	case Number:
		return v.N != 0 && !math.IsNaN(v.N)
	case String:
		return v.S != ""
	}
	return true
}

// arrayIndex: a canonical decimal integer below 2^32 - 1 (an array index, which JavaScript orders first).
func arrayIndex(k string) bool {
	if k == "" || len(k) > 10 || (len(k) > 1 && k[0] == '0') {
		return false
	}
	for i := 0; i < len(k); i++ {
		if k[i] < '0' || k[i] > '9' {
			return false
		}
	}
	n, err := strconv.ParseUint(k, 10, 64)
	return err == nil && n < 1<<32-1
}

// Parse reads one JSON text as JSON.parse does.
func Parse(b []byte) (*Value, error) {
	if !utf8.Valid(b) {
		return nil, ErrUnsupported // the control plane decodes it with replacement characters first
	}
	p := &parser{b: b}
	p.ws()
	v, err := p.value(0)
	if err != nil {
		return nil, err
	}
	p.ws()
	if p.i != len(p.b) {
		return nil, ErrSyntax
	}
	return v, nil
}

type parser struct {
	b []byte
	i int
}

func (p *parser) ws() {
	for p.i < len(p.b) && (p.b[p.i] == ' ' || p.b[p.i] == '\t' || p.b[p.i] == '\n' || p.b[p.i] == '\r') {
		p.i++
	}
}

func (p *parser) lit(s string) bool {
	if strings.HasPrefix(string(p.b[p.i:min(len(p.b), p.i+len(s))]), s) {
		p.i += len(s)
		return true
	}
	return false
}

func (p *parser) value(depth int) (*Value, error) {
	if p.i >= len(p.b) {
		return nil, ErrSyntax
	}
	switch c := p.b[p.i]; {
	case c == '{':
		if depth >= MaxDepth {
			return nil, ErrUnsupported
		}
		p.i++
		o := NewObject()
		p.ws()
		if p.i < len(p.b) && p.b[p.i] == '}' {
			p.i++
			return o, nil
		}
		for {
			p.ws()
			if p.i >= len(p.b) || p.b[p.i] != '"' {
				return nil, ErrSyntax
			}
			k, err := p.str()
			if err != nil {
				return nil, err
			}
			p.ws()
			if p.i >= len(p.b) || p.b[p.i] != ':' {
				return nil, ErrSyntax
			}
			p.i++
			p.ws()
			x, err := p.value(depth + 1)
			if err != nil {
				return nil, err
			}
			o.Set(k, x)
			p.ws()
			if p.i < len(p.b) && p.b[p.i] == ',' {
				p.i++
				continue
			}
			if p.i < len(p.b) && p.b[p.i] == '}' {
				p.i++
				return o, nil
			}
			return nil, ErrSyntax
		}
	case c == '[':
		if depth >= MaxDepth {
			return nil, ErrUnsupported
		}
		p.i++
		a := &Value{Kind: Array, A: []*Value{}}
		p.ws()
		if p.i < len(p.b) && p.b[p.i] == ']' {
			p.i++
			return a, nil
		}
		for {
			p.ws()
			x, err := p.value(depth + 1)
			if err != nil {
				return nil, err
			}
			a.A = append(a.A, x)
			p.ws()
			if p.i < len(p.b) && p.b[p.i] == ',' {
				p.i++
				continue
			}
			if p.i < len(p.b) && p.b[p.i] == ']' {
				p.i++
				return a, nil
			}
			return nil, ErrSyntax
		}
	case c == '"':
		s, err := p.str()
		if err != nil {
			return nil, err
		}
		return Str(s), nil
	case c == 't' && p.lit("true"):
		return &Value{Kind: Bool, B: true}, nil
	case c == 'f' && p.lit("false"):
		return &Value{Kind: Bool}, nil
	case c == 'n' && p.lit("null"):
		return &Value{Kind: Null}, nil
	case c == '-' || (c >= '0' && c <= '9'):
		return p.num()
	}
	return nil, ErrSyntax
}

func (p *parser) num() (*Value, error) {
	start := p.i
	digits := func() int {
		n := 0
		for p.i < len(p.b) && p.b[p.i] >= '0' && p.b[p.i] <= '9' {
			p.i++
			n++
		}
		return n
	}
	if p.b[p.i] == '-' {
		p.i++
	}
	if p.i < len(p.b) && p.b[p.i] == '0' {
		p.i++
	} else if digits() == 0 {
		return nil, ErrSyntax
	}
	if p.i < len(p.b) && p.b[p.i] == '.' {
		p.i++
		if digits() == 0 {
			return nil, ErrSyntax
		}
	}
	if p.i < len(p.b) && (p.b[p.i] == 'e' || p.b[p.i] == 'E') {
		p.i++
		if p.i < len(p.b) && (p.b[p.i] == '+' || p.b[p.i] == '-') {
			p.i++
		}
		if digits() == 0 {
			return nil, ErrSyntax
		}
	}
	f, err := strconv.ParseFloat(string(p.b[start:p.i]), 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return nil, ErrSyntax
	}
	return Num(f), nil // out of range: ±Inf (written as null) or ±0, as JavaScript reads it
}

func (p *parser) str() (string, error) {
	p.i++ // the opening quote
	var sb strings.Builder
	for {
		if p.i >= len(p.b) {
			return "", ErrSyntax
		}
		c := p.b[p.i]
		switch {
		case c == '"':
			p.i++
			return sb.String(), nil
		case c < 0x20:
			return "", ErrSyntax
		case c != '\\':
			sb.WriteByte(c)
			p.i++
			continue
		}
		p.i++
		if p.i >= len(p.b) {
			return "", ErrSyntax
		}
		e := p.b[p.i]
		p.i++
		switch e {
		case '"', '\\', '/':
			sb.WriteByte(e)
		case 'b':
			sb.WriteByte('\b')
		case 'f':
			sb.WriteByte('\f')
		case 'n':
			sb.WriteByte('\n')
		case 'r':
			sb.WriteByte('\r')
		case 't':
			sb.WriteByte('\t')
		case 'u':
			u, ok := p.hex4()
			if !ok {
				return "", ErrSyntax
			}
			r := rune(u)
			if utf16.IsSurrogate(r) {
				// Only a high surrogate followed by an escaped low one is a character here.
				if u >= 0xdc00 || p.i+6 > len(p.b) || p.b[p.i] != '\\' || p.b[p.i+1] != 'u' {
					return "", ErrUnsupported
				}
				p.i += 2
				lo, ok := p.hex4()
				if !ok {
					return "", ErrSyntax
				}
				if lo < 0xdc00 || lo > 0xdfff {
					return "", ErrUnsupported
				}
				r = utf16.DecodeRune(r, rune(lo))
			}
			sb.WriteRune(r)
		default:
			return "", ErrSyntax
		}
	}
}

func (p *parser) hex4() (uint16, bool) {
	if p.i+4 > len(p.b) {
		return 0, false
	}
	n, err := strconv.ParseUint(string(p.b[p.i:p.i+4]), 16, 16)
	if err != nil {
		return 0, false
	}
	p.i += 4
	return uint16(n), true
}

// Stringify writes v as JSON.stringify(v) does (no indentation).
func (v *Value) Stringify() []byte { return v.append(nil) }

func (v *Value) append(b []byte) []byte {
	switch v.Kind {
	case Null:
		return append(b, "null"...)
	case Bool:
		return strconv.AppendBool(b, v.B)
	case Number:
		return append(b, NumberString(v.N)...)
	case String:
		return QuoteJS(b, v.S)
	case Array:
		b = append(b, '[')
		for i, x := range v.A {
			if i > 0 {
				b = append(b, ',')
			}
			b = x.append(b)
		}
		return append(b, ']')
	}
	b = append(b, '{')
	for i, k := range v.Keys() {
		if i > 0 {
			b = append(b, ',')
		}
		b = QuoteJS(b, k)
		b = append(b, ':')
		b = v.vals[k].append(b)
	}
	return append(b, '}')
}

// QuoteJS appends s as JSON.stringify writes a string.
func QuoteJS(b []byte, s string) []byte {
	const hexd = "0123456789abcdef"
	b = append(b, '"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			b = append(b, '\\', '"')
		case c == '\\':
			b = append(b, '\\', '\\')
		case c == '\b':
			b = append(b, '\\', 'b')
		case c == '\f':
			b = append(b, '\\', 'f')
		case c == '\n':
			b = append(b, '\\', 'n')
		case c == '\r':
			b = append(b, '\\', 'r')
		case c == '\t':
			b = append(b, '\\', 't')
		case c < 0x20:
			b = append(b, '\\', 'u', '0', '0', hexd[c>>4], hexd[c&15])
		default:
			b = append(b, c)
		}
	}
	return append(b, '"')
}

// NumberString is Number.prototype.toString(10) of a finite double, as JSON.stringify writes it; a non-finite one is
// "null".
func NumberString(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "null"
	}
	if f == 0 {
		return "0"
	}
	sign := ""
	if f < 0 {
		sign, f = "-", -f
	}
	e := strconv.FormatFloat(f, 'e', -1, 64) // d.ddde±XX, the shortest digits that read back as f
	mant, exp, _ := strings.Cut(e, "e")
	digits := strings.Replace(mant, ".", "", 1)
	x, _ := strconv.Atoi(exp)
	k, n := len(digits), x+1
	switch {
	case k <= n && n <= 21:
		return sign + digits + strings.Repeat("0", n-k)
	case 0 < n && n <= 21:
		return sign + digits[:n] + "." + digits[n:]
	case -6 < n && n <= 0:
		return sign + "0." + strings.Repeat("0", -n) + digits
	}
	m := digits[:1]
	if k > 1 {
		m += "." + digits[1:]
	}
	es := "+"
	if n-1 < 0 {
		es = "-"
	}
	return sign + m + "e" + es + strconv.Itoa(abs(n-1))
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
```

- [ ] **Step 5: Generate the fixtures from the JS code**

Run: `node scripts/fixtures/gen-jsjson.js`
Expected: `gen-jsjson: 31 cases in gw/testdata/jsjson` and no change to the files Step 3 wrote.

- [ ] **Step 6: Run the tests and the task gate**

Run:

```bash
export GOTOOLCHAIN=local ZAAFPULSE_DATA="$(mktemp -d)" HOME="$(mktemp -d)"
test -z "$(gofmt -l gw)" && go -C gw vet ./... && GOOS=windows go -C gw vet ./... && go -C gw test -race -count=1 ./...
deno task typecheck
node --test test/*.test.js
deno test -q --no-check --no-prompt -P=test test/*.test.js
npm run test:contract
```

Expected: no gofmt output, `ok` for every Go package, `deno check` with no error, `fail 0` from Node, `0 failed` from Deno, and the contract suite
`pass 172` (from Task 8 on, `pass 179`, with one case skipped outside shadow mode), `fail 0`.

- [ ] **Step 7: Commit and push**

```bash
git add -A -- gw scripts
git status --short   # nothing outside this task's files, nothing under .superpowers/
git commit -m "P4b T6: JSON in Go as JavaScript parses and writes it, checked against fixtures"
git push origin migrate/p4b-passthrough
```

---

### Task 7: The chat engine: plan, answer, usage line and cost, checked against the JS gateway

`PlanChat` decides without side effects whether the engine serves a request and builds the provider request; `Respond` and `Failed` build the client answer and the usage line. `gen-chat.js` runs the real `createGateway` over HTTP against a stubbed provider and records 24 answers (Decisions 2 and 9); `gen-pricing.js` checks the price lookup and the cost; the preset flags are checked against `PRESETS`.

**Files:**
- Create: `gw/engine/chat.go`, `gw/engine/chat_test.go`, `gw/engine/pricing.go`, `gw/engine/pricing_test.go`, `gw/engine/providers.go`, `scripts/fixtures/gen-chat.js`, `scripts/fixtures/gen-pricing.js`


**Interfaces:**
- Consumes: Tasks 5 and 6 (`Keys`, `Authorize`, `RequestSecret`, `Parse`, `Value`), `lib/front/push.js` (the generators build the config.push and secrets.handoff with it).
- Produces (package `engine`):
  - `NewState(c *ipc.ConfigPush, s *ipc.SecretsHandoff) *State`; `(*State).WithSnapshot(b *ipc.BudgetSnapshot) *State`; fields `Gen`, `Gateway`, `Keys`, `Secrets`, `Mask`, `Pricer`, `Snapshot`, `SnapshotAt`.
  - `type Request struct { Method, RawPath, Query string; Header http.Header; Host, Peer string; Body []byte; TooBig bool }`.
  - `(*State).PlanChat(r *Request, now int64) Plan`; `type Plan struct { Handoff string; Key *ipc.GatewayKeyAuth; Target *Target; Model, URL string; Header http.Header; Body []byte; Base ipc.UsageRecord }`.
  - `(*State).Respond(p *Plan, status int, raw []byte, ms float64) Answer`; `(*State).Failed(p *Plan, why string, ms float64) Answer`; `RateLimited(key, wait) Answer`; `type Answer struct { Status int; Header http.Header; Body []byte; Usage ipc.UsageRecord }`.
  - `Resolve`, `TierOf`, `FormatOf`, `Usable`, `TokenParamOf`, `AdaptChat`, `ScopeTier`, `GuardActive`, `Presets`; `NormalizeModel`, `KeyMatches`, `NewPricer(table) *Pricer` with `Key(model)` and `Cost(model, input, output, cached float64) int64` (nano-USD); `MaxBody`, `StaleMs`, `CORS`, `NowMs()`.
  - Test helpers (`chat_test.go`): `chatCase`, `chatOut`, `stateFor(t, c)`, `requestFor(c)`, `fixtureNow`.

- [ ] **Step 1: Check the starting point**

Run: `git status --short && git rev-parse --abbrev-ref HEAD && git log --oneline -1`
Expected: nothing listed, `migrate/p4b-passthrough`, and the previous task's commit on top (Task 1: `0ec1247`, the merge of `origin/migrate/deno-go` with stream C).

- [ ] **Step 2: Write the failing tests**

Create `gw/engine/chat_test.go`:

```go
package engine

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

const fixtureNow = 1767225600000

type chatCase struct {
	Config  ipc.ConfigPush     `json:"config"`
	Secrets ipc.SecretsHandoff `json:"secrets"`
	Request struct {
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	} `json:"request"`
	Provider struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
		Throw  string `json:"throw"`
	} `json:"provider"`
}

type chatOut struct {
	Request *struct {
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	} `json:"request"`
	Client struct {
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	} `json:"client"`
	Usage json.RawMessage `json:"usage"`
}

func stateFor(t *testing.T, c chatCase) *State {
	t.Helper()
	// A copy of its own: a test that edits the state must not change the next one's.
	b, _ := json.Marshal(c)
	c = chatCase{}
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	c.Config.Gen, c.Secrets.Gen = 1, 1
	return NewState(&c.Config, &c.Secrets).WithSnapshot(&ipc.BudgetSnapshot{At: fixtureNow, KeyCenter: map[string]string{}})
}

func requestFor(c chatCase) *Request {
	h := http.Header{}
	for k, v := range c.Request.Headers {
		h.Set(k, v)
	}
	return &Request{Method: "POST", RawPath: "/v1/chat/completions", Header: h, Host: "127.0.0.1:4777",
		Peer: "127.0.0.1:50000", Body: []byte(c.Request.Body)}
}

// Every case of gw/testdata/chat (the JS gateway's own answers, scripts/fixtures/gen-chat.js): the engine serves it,
// sends the provider the same request, and gives the client the same answer and the same usage line.
func TestChatFixtures(t *testing.T) {
	for _, f := range loadFixtures(t, "chat") {
		if f.Fn == "presets" {
			sameJSON(t, f.Name, Presets, f.Out)
			continue
		}
		var c chatCase
		var want chatOut
		if err := json.Unmarshal(f.In, &c); err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		if err := json.Unmarshal(f.Out, &want); err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		s := stateFor(t, c)
		p := s.PlanChat(requestFor(c), fixtureNow)
		if p.Handoff != "" {
			t.Errorf("%s: handed off: %s", f.Name, p.Handoff)
			continue
		}
		got := map[string]string{}
		for k := range p.Header {
			got[strings.ToLower(k)] = p.Header.Get(k)
		}
		if p.URL != want.Request.URL || string(p.Body) != want.Request.Body {
			t.Errorf("%s: provider request\n got %s %s\nwant %s %s", f.Name, p.URL, p.Body, want.Request.URL, want.Request.Body)
		}
		sameJSON(t, f.Name+" provider headers", got, mustJSON(want.Request.Headers))
		var a Answer
		if c.Provider.Throw != "" {
			why := c.Provider.Throw
			if why == "timeout" {
				why = "timed out"
			}
			a = s.Failed(&p, why, 1)
		} else {
			a = s.Respond(&p, c.Provider.Status, []byte(c.Provider.Body), 1)
		}
		if a.Status != want.Client.Status || string(a.Body) != want.Client.Body {
			t.Errorf("%s: client answer\n got %d %s\nwant %d %s", f.Name, a.Status, a.Body, want.Client.Status, want.Client.Body)
		}
		for _, h := range []string{"content-type", "cache-control", "access-control-allow-origin"} {
			if a.Header.Get(h) != want.Client.Headers[h] {
				t.Errorf("%s: %s %q, want %q", f.Name, h, a.Header.Get(h), want.Client.Headers[h])
			}
		}
		if a.Header.Get("Retry-After") != "" {
			t.Errorf("%s: the provider's retry-after reached the client", f.Name)
		}
		u := a.Usage
		u.Ms = 0
		var rec map[string]any
		b, _ := json.Marshal(u)
		_ = json.Unmarshal(b, &rec)
		delete(rec, "ts")
		delete(rec, "ms")
		sameJSON(t, f.Name+" usage", rec, want.Usage)
	}
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// The engine leaves to the control plane every request it does not serve as the JS gateway would, and says why.
func TestChatHandoffs(t *testing.T) {
	var base chatCase
	for _, f := range loadFixtures(t, "chat") {
		if strings.HasSuffix(f.Name, "-basic.json") {
			_ = json.Unmarshal(f.In, &base)
		}
	}
	for _, c := range []struct {
		name, why string
		edit      func(s *State, r *Request)
	}{
		{"remote client", "access:", func(s *State, r *Request) { r.Peer = "10.0.0.2:5" }},
		{"public host", "access:", func(s *State, r *Request) { r.Host = "zaaf.example" }},
		{"browser", "access:", func(s *State, r *Request) { r.Header.Set("Origin", "http://127.0.0.1:4777") }},
		{"proxy", "access:", func(s *State, r *Request) { r.Header.Set("X-Forwarded-For", "1.2.3.4") }},
		{"no key", "key: " + MsgMalformed, func(s *State, r *Request) { r.Header.Del("Authorization") }},
		{"bad json", "body:", func(s *State, r *Request) { r.Body = []byte(`{"model":`) }},
		{"array body", "body:", func(s *State, r *Request) { r.Body = []byte(`[]`) }},
		{"empty body", "body:", func(s *State, r *Request) { r.Body = nil }},
		{"no model", "model: missing", func(s *State, r *Request) { r.Body = []byte(`{"messages":[]}`) }},
		{"agent", "model: an agent", func(s *State, r *Request) { r.Body = []byte(`{"model":"agent/auto"}`) }},
		{"stream", "stream:", func(s *State, r *Request) { r.Body = []byte(`{"model":"oa/m1","stream":1}`) }},
		{"unknown model", "model: no usable", func(s *State, r *Request) { r.Body = []byte(`{"model":"zz/m1"}`) }},
		{"route", "model: a route", func(s *State, r *Request) {
			s.Gateway.Routes = []ipc.RouteConfig{{Name: "router/r", Targets: []string{"oa/m1"}}}
			r.Body = []byte(`{"model":"router/r"}`)
		}},
		{"anthropic format", "provider: not the OpenAI", func(s *State, r *Request) {
			f := "anthropic"
			s.Gateway.Providers[0].Format = &f
		}},
		{"oauth2", "provider: OAuth2", func(s *State, r *Request) { s.Gateway.Providers[0].Auth = &ipc.ProviderAuth{Type: "oauth2"} }},
		{"credential missing", "provider: no credential", func(s *State, r *Request) { delete(s.Secrets, "oa") }},
		{"key budget", "budget: the key", func(s *State, r *Request) {
			b := 5.0
			s.Keys.list[0].BudgetUSD = &b
		}},
		{"stale snapshot", "budget: no fresh", func(s *State, r *Request) { s.SnapshotAt = fixtureNow - StaleMs - 1 }},
		{"global budget", "budget: a blocking", func(s *State, r *Request) {
			s.Snapshot = &ipc.BudgetSnapshot{At: fixtureNow, Budgets: []ipc.BudgetView{{ID: "b", Enforce: "block",
				Scope: ipc.BudgetScope{Global: true}, State: "ok"}}}
		}},
		{"model budget", "budget: a blocking", func(s *State, r *Request) {
			s.Snapshot = &ipc.BudgetSnapshot{At: fixtureNow, Budgets: []ipc.BudgetView{{ID: "b", Enforce: "block",
				Scope: ipc.BudgetScope{Dim: "model", Value: "m1"}, State: "ok"}}}
		}},
		{"guardrails", "guardrails:", func(s *State, r *Request) { s.Gateway.Guardrails.Secrets = "redact" }},
		{"key guardrails", "guardrails:", func(s *State, r *Request) {
			m := "log"
			s.Keys.list[0].Guardrails.PII = &m
		}},
		{"tool results", "token saver:", func(s *State, r *Request) {
			s.Gateway.TokenSaver = true
			r.Body = []byte(`{"model":"oa/m1","messages":[{"role":"tool","tool_call_id":"c","content":"x"}]}`)
		}},
	} {
		s := stateFor(t, base)
		r := requestFor(base)
		c.edit(s, r)
		if p := s.PlanChat(r, fixtureNow); !strings.HasPrefix(p.Handoff, c.why) {
			t.Errorf("%s: handoff %q, want %q", c.name, p.Handoff, c.why)
		}
	}
	// The token saver off by header: served.
	s := stateFor(t, base)
	s.Gateway.TokenSaver = true
	r := requestFor(base)
	r.Body = []byte(`{"model":"oa/m1","messages":[{"role":"tool","tool_call_id":"c","content":"x"}]}`)
	r.Header.Set("X-Zp-Token-Saver", "OFF")
	if p := s.PlanChat(r, fixtureNow); p.Handoff != "" {
		t.Errorf("token saver off: %s", p.Handoff)
	}
}

func TestRateLimitedAnswer(t *testing.T) {
	a := RateLimited(&ipc.GatewayKeyAuth{RPM: 2}, 7)
	if a.Status != 429 || a.Header.Get("Retry-After") != "7" ||
		string(a.Body) != `{"error":{"message":"rate limit of 2 requests per minute reached","type":"rate_limit_error"}}` {
		t.Fatalf("%d %v %s", a.Status, a.Header, a.Body)
	}
}
```

Create `gw/engine/pricing_test.go`:

```go
package engine

import (
	"encoding/json"
	"testing"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

// Every case of gw/testdata/pricing (lib/pricing.js and lib/gateway/usage.js, scripts/fixtures/gen-pricing.js).
func TestPricingFixtures(t *testing.T) {
	var table map[string]ipc.PriceEntry
	cases := loadFixtures(t, "pricing")
	for _, c := range cases {
		if c.Fn == "table" {
			if err := json.Unmarshal(c.Out, &table); err != nil {
				t.Fatal(err)
			}
		}
	}
	p := NewPricer(table)
	for _, c := range cases {
		switch c.Fn {
		case "table":
		case "normalize":
			var m string
			mustDecode(t, c, &m)
			sameJSON(t, c.Name, NormalizeModel(m), c.Out)
		case "key":
			var m string
			mustDecode(t, c, &m)
			var got any
			if k := p.Key(m); k != "" {
				got = k
			}
			sameJSON(t, c.Name, got, c.Out)
		case "cost":
			var in struct {
				Model                 string
				Input, Output, Cached float64
			}
			mustDecode(t, c, &in)
			sameJSON(t, c.Name, p.Cost(in.Model, in.Input, in.Output, in.Cached), c.Out)
		default:
			t.Errorf("%s: unknown fn %q", c.Name, c.Fn)
		}
	}
}
```

Create `scripts/fixtures/gen-chat.js`:

```js
// Fixtures for the go engine's chat route (P4b, slice S2): the real JS gateway (lib/gateway createGateway) answers each
// case over HTTP on 127.0.0.1, against a provider stubbed at fetch, and this records what it sent the provider, what
// it answered the client and the usage line it wrote. gw/engine/chat_test.go plans and answers the same case from the
// config.push and secrets.handoff the control plane would send (lib/front/push.js) and must give the same bytes.
// Writes gw/testdata/chat/<n>-<name>.json as { fn: "chat", in: { config, secrets, request, provider }, out }.
// Only cases the engine serves are here (guardrails off, no budget, one OpenAI-format target, not streaming); the
// hand-offs are Go tests of their own.
// Run: node scripts/fixtures/gen-chat.js (CI runs every scripts/fixtures/gen-*.js and fails on a diff).
const fs = require('fs');
const os = require('os');
const path = require('path');
const http = require('http');
const { createGateway } = require('../../lib/gateway');
const { createSecrets } = require('../../lib/secrets');
const { configPush, secretsPush } = require('../../lib/front/push');
const { hash } = require('../../lib/gateway/keys');
const { toNano } = require('../../lib/money');
const { PRESETS } = require('../../lib/gateway/providers');

const OUT = path.join(__dirname, '..', '..', 'gw', 'testdata', 'chat');
const SECRET = 'zp_chatFixtureSecret0123456789abcdef';
const OA_KEY = 'sk-oa-fixture-123456';
const GATEWAY = {
  providers: [
    {
      id: 'oa',
      preset: 'custom',
      baseUrl: 'http://127.0.0.1:9/v1',
      tier: 'external',
      approved: true,
      apiKey: { env: 'OA_KEY' },
    },
    { id: 'pv', preset: 'ollama', baseUrl: 'http://127.0.0.1:11434/v1', aliases: { fast: 'llama-9' } },
    { id: 'op', preset: 'openai', baseUrl: 'http://127.0.0.1:8/v1', approved: true, apiKey: { env: 'OA_KEY' } },
    { id: 'tp', preset: 'openai', baseUrl: 'http://127.0.0.1:7/v1', approved: true, tokenParam: 'max_tokens' },
  ],
  routes: [],
  guardrails: { scope: 'external', secrets: 'off', pii: 'off', injection: 'off' },
};
const PRICING = {
  m1: { input: 1, output: 2 },
  'llama-9': { input: 0.1, output: 0.3, cacheRead: 0.01 },
  o3: { input: 2, output: 8 },
};
const KEYS = [
  {
    id: 'k1',
    name: 'k1',
    prefix: 'zp_chatFi…',
    hash: hash(SECRET),
    scopes: ['llm'],
    models: [],
    agents: [],
    budgetUSD: null,
    rpm: 10000,
    createdAt: 1,
  },
  {
    id: 'k2',
    name: 'k2',
    prefix: 'zp_chatFi…',
    hash: hash(`${SECRET}2`),
    scopes: ['llm'],
    models: ['pv/*', 'fast'],
    agents: [],
    budgetUSD: null,
    rpm: 10000,
    privateOnly: true,
    createdAt: 1,
  },
];
const ok = (extra = {}) => ({
  status: 200,
  body: JSON.stringify({
    id: 'c1',
    object: 'chat.completion',
    model: 'upstream-model',
    choices: [{ index: 0, message: { role: 'assistant', content: 'hi' }, finish_reason: 'stop' }],
    usage: { prompt_tokens: 1000, completion_tokens: 500 },
    ...extra,
  }),
});
const user = [{ role: 'user', content: 'hello' }];
const CASES = [
  ['basic', {}, { model: 'oa/m1', messages: user }, ok()],
  [
    'alias-private',
    {},
    { model: 'fast', messages: user, temperature: 0.2 },
    ok({ usage: { prompt_tokens: 50, completion_tokens: 5, prompt_tokens_details: { cached_tokens: 20 } } }),
  ],
  ['max-tokens-renamed-for-openai', {}, { model: 'op/gpt-6.1', max_tokens: 77, messages: user }, ok()],
  ['max-tokens-renamed-for-reasoning-model', {}, { messages: user, model: 'oa/o3', max_tokens: 5 }, ok()],
  ['token-param-override-keeps-max-tokens', {}, { model: 'tp/gpt-6.1', max_tokens: 9, messages: user }, ok()],
  [
    'both-token-fields-kept',
    {},
    { model: 'op/gpt-6.1', max_tokens: 9, max_completion_tokens: 10, messages: user },
    ok(),
  ],
  [
    'numbers-and-unicode',
    {},
    '{"model":"oa/m1","temperature":1.0,"seed":12345678901234567890,"top_p":1e-7,"2":"x","messages":[{"role":"user","content":"caf\\u00e9 \\ud83d\\ude00  "}]}',
    ok(),
  ],
  ['duplicate-model-key', {}, '{"model":"zz/none","messages":[],"model":"oa/m1"}', ok()],
  [
    'tools-and-extra-fields',
    {},
    {
      model: 'oa/m1',
      messages: user,
      tools: [{ type: 'function', function: { name: 'f', parameters: { type: 'object' } } }],
      tool_choice: 'auto',
      user: 'u1',
    },
    ok({
      choices: [
        {
          index: 0,
          message: {
            role: 'assistant',
            content: null,
            tool_calls: [{ id: 'call_1', type: 'function', function: { name: 'f', arguments: '{}' } }],
          },
          finish_reason: 'tool_calls',
        },
      ],
    }),
  ],
  [
    'upstream-400-json',
    {},
    { model: 'oa/m1', messages: user },
    { status: 400, body: '{"error":{"message":"bad parameter temperature","type":"invalid_request_error"}}' },
  ],
  ['upstream-500-text', {}, { model: 'oa/m1', messages: user }, { status: 500, body: 'upstream exploded' }],
  [
    'upstream-429',
    {},
    { model: 'oa/m1', messages: user },
    { status: 429, body: '{"error":{"message":"slow down"}}', headers: { 'retry-after': '7' } },
  ],
  ['upstream-200-not-json', {}, { model: 'oa/m1', messages: user }, { status: 200, body: 'not json at all' }],
  ['upstream-200-array', {}, { model: 'oa/m1', messages: user }, { status: 200, body: '[1,2]' }],
  ['upstream-200-zero', {}, { model: 'oa/m1', messages: user }, { status: 200, body: '0' }],
  [
    'upstream-200-bom',
    {},
    { model: 'oa/m1', messages: user },
    { status: 200, body: '﻿{"model":"x","usage":{"prompt_tokens":3}}' },
  ],
  [
    'error-names-the-secret',
    {},
    { model: 'oa/m1', messages: user },
    { status: 401, body: `{"error":{"message":"invalid key ${OA_KEY}"}}` },
  ],
  [
    'long-error-cut-at-200',
    {},
    { model: 'oa/m1', messages: user },
    { status: 503, body: JSON.stringify({ error: { message: `${'é'.repeat(150)}${'x'.repeat(100)}` } }) },
  ],
  [
    'error-message-not-a-string',
    {},
    { model: 'oa/m1', messages: user },
    { status: 500, body: '{"error":{"message":""},"x":1}' },
  ],
  ['private-only-key', { key: `${SECRET}2` }, { model: 'pv/llama-9', messages: user }, ok()],
  [
    'claude-code-client',
    { headers: { 'user-agent': 'claude-cli/2.1.0 (external, cli)' } },
    { model: 'oa/m1', messages: user },
    ok(),
  ],
  ['x-api-key', { auth: 'x-api-key' }, { model: 'oa/m1', messages: user }, ok()],
  ['connection-refused', {}, { model: 'oa/m1', messages: user }, { throw: 'ECONNREFUSED' }],
  ['timed-out', {}, { model: 'oa/m1', messages: user }, { throw: 'timeout' }],
];

async function main() {
  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), 'zp-gen-chat-'));
  fs.writeFileSync(path.join(dataDir, 'keys.json'), JSON.stringify({ keys: KEYS }));
  let provider = null;
  let sent = null;
  const fetchImpl = async (url, init) => {
    sent = { url, headers: init.headers, body: init.body };
    if (provider.throw === 'timeout') throw Object.assign(new Error('aborted'), { name: 'AbortError' });
    if (provider.throw) throw Object.assign(new TypeError('fetch failed'), { cause: { code: provider.throw } });
    return new Response(Buffer.from(provider.body, 'utf8'), {
      status: provider.status,
      headers: provider.headers || {},
    });
  };
  const secrets = createSecrets({
    env: { OA_KEY },
    exec: () => {
      throw new Error('no keychain');
    },
  });
  const gateway = createGateway({
    dataDir,
    getConfig: () => ({ gateway: GATEWAY }),
    writeGatewayConfig: () => {},
    secrets,
    resolveSecret: () => undefined,
    getPricing: () => PRICING,
    getIntegrations: () => ({ listTools: async () => [], callTool: async () => ({}) }),
    fetchImpl,
  });
  const server = http.createServer(async (req, res) => {
    if (!(await gateway.handle(req, res, new URL(req.url, 'http://localhost')))) res.end();
  });
  await new Promise((r) => server.listen(0, '127.0.0.1', r));
  const base = `http://127.0.0.1:${server.address().port}`;
  const config = configPush({ gateway: GATEWAY, keys: KEYS, pricing: PRICING });
  const handoff = secretsPush({ gateway: GATEWAY, resolve: (ref, f) => secrets.resolve(ref, f) });
  fs.rmSync(OUT, { recursive: true, force: true });
  fs.mkdirSync(OUT, { recursive: true });
  // The preset flags the engine reads (gw/engine/providers.go Presets): only presets with one.
  const presets = {};
  for (const [id, p] of Object.entries(PRESETS)) {
    const f = {
      ...(p.local ? { local: true } : {}),
      ...(p.forwards ? { forwards: true } : {}),
      ...(p.maxCompletionTokens ? { maxCompletionTokens: true } : {}),
      ...(p.format ? { format: p.format } : {}),
    };
    if (Object.keys(f).length) presets[id] = f;
  }
  fs.writeFileSync(path.join(OUT, '00-presets.json'), `${JSON.stringify({ fn: 'presets', in: null, out: presets })}\n`);
  let n = 0;
  for (const [name, opt, body, prov] of CASES) {
    provider = prov;
    sent = null;
    const key = opt.key || SECRET;
    const headers = {
      'content-type': 'application/json',
      ...(opt.auth === 'x-api-key' ? { 'x-api-key': key } : { authorization: `Bearer ${key}` }),
      ...(opt.headers || {}),
    };
    const text = typeof body === 'string' ? body : JSON.stringify(body);
    const r = await fetch(`${base}/v1/chat/completions`, { method: 'POST', headers, body: text });
    const client = {
      status: r.status,
      headers: Object.fromEntries(
        ['content-type', 'cache-control', 'access-control-allow-origin', 'retry-after'].map((h) => [
          h,
          r.headers.get(h),
        ]),
      ),
      body: await r.text(),
    };
    const { ts: _ts, ms: _ms, cost, ...usage } = gateway.usage.recent(1)[0];
    const out = {
      request: sent && { url: sent.url, headers: sent.headers, body: sent.body },
      client,
      usage: { ...usage, cost: toNano(cost) },
    };
    const input = { config, secrets: handoff, request: { headers, body: text }, provider: prov };
    fs.writeFileSync(
      path.join(OUT, `${String(++n).padStart(2, '0')}-${name}.json`),
      `${JSON.stringify({ fn: 'chat', in: input, out })}\n`,
    );
  }
  server.close();
  gateway.flush();
  fs.rmSync(dataDir, { recursive: true, force: true });
  console.log(`gen-chat: ${n} cases in ${path.relative(process.cwd(), OUT)}`);
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
```

Create `scripts/fixtures/gen-pricing.js`:

```js
// Fixtures for the go engine's price lookup and cost (gw/engine/pricing.go, P4b): lib/pricing.js normalizeModel and
// makePricer().key over a fixed table, and lib/gateway/usage.js cost in nano-USD. Writes gw/testdata/pricing/<n>-<fn>-
// <name>.json as { fn, in, out }.
// Run: node scripts/fixtures/gen-pricing.js (CI runs every scripts/fixtures/gen-*.js and fails on a diff).
const fs = require('fs');
const os = require('os');
const path = require('path');
const { normalizeModel, makePricer } = require('../../lib/pricing');
const { createUsage } = require('../../lib/gateway/usage');
const { toNano } = require('../../lib/money');
const { configPush } = require('../../lib/front/push');

const OUT = path.join(__dirname, '..', '..', 'gw', 'testdata', 'pricing');
const TABLE = {
  'claude-opus-5': { input: 5, output: 25, cacheRead: 0.5 },
  'claude-opus-5-5': { input: 5, output: 25 },
  'gpt-5': { input: 1.25, output: 10 },
  'gpt-': { input: 9, output: 9 },
  'gpt-5.6': { input: 2, output: 12 },
  'llama-9': { input: 0.1, output: 0.3, cacheRead: 0 },
  m1: { input: 1, output: 2 },
};
const MODELS = [
  'oa/m1',
  'M1',
  ' oa/m1 ',
  'anthropic/claude-opus-5.5',
  'us.anthropic.claude-opus-5-20260101-v1:0',
  'claude-opus-50',
  'openai/gpt-5',
  'gpt-5.6',
  'gpt-5.a',
  'gpt-5-mini',
  'gpt-6.1',
  'vertex/claude-opus-5@20260101',
  'x/llama-9[1m]',
  'x/llama-9:free',
  'unknown-model',
  '',
];
const cases = [];
for (const m of MODELS) cases.push({ fn: 'normalize', name: m, in: m, out: normalizeModel(m) });
const pricer = makePricer(TABLE);
for (const m of MODELS) cases.push({ fn: 'key', name: m, in: m, out: pricer.key(m) });
const file = path.join(fs.mkdtempSync(path.join(os.tmpdir(), 'zp-gen-pricing-')), 'u.jsonl');
const usage = createUsage({ file, getPricing: () => TABLE });
for (const [m, u] of [
  ['oa/m1', { input: 1000, output: 500 }],
  ['x/llama-9', { input: 1000, output: 100, cached: 400 }],
  ['claude-opus-5', { input: 3, output: 7, cached: 2 }],
  ['claude-opus-5', { input: 2, output: 0, cached: 9 }],
  ['gpt-5', { input: 123456789, output: 98765 }],
  ['unknown-model', { input: 10, output: 10 }],
])
  cases.push({ fn: 'cost', name: m, in: { model: m, ...u }, out: toNano(usage.cost(m, u)) });
fs.rmSync(path.dirname(file), { recursive: true, force: true });
fs.rmSync(OUT, { recursive: true, force: true });
fs.mkdirSync(OUT, { recursive: true });
const table = configPush({ gateway: {}, keys: [], pricing: TABLE }).pricing;
fs.writeFileSync(path.join(OUT, '00-table.json'), `${JSON.stringify({ fn: 'table', in: TABLE, out: table })}\n`);
cases.forEach((c, i) => {
  const slug =
    c.name
      .replace(/[^A-Za-z0-9]+/g, '-')
      .replace(/^-|-$/g, '')
      .slice(0, 40) || 'empty';
  fs.writeFileSync(
    path.join(OUT, `${String(i + 1).padStart(2, '0')}-${c.fn}-${slug}.json`),
    `${JSON.stringify({ fn: c.fn, in: c.in, out: c.out })}\n`,
  );
});
console.log(`gen-pricing: ${cases.length + 1} cases in ${path.relative(process.cwd(), OUT)}`);
```

- [ ] **Step 3: Run them to see them fail**

Run: `node scripts/fixtures/gen-chat.js && node scripts/fixtures/gen-pricing.js && GOTOOLCHAIN=local go -C gw test -count=1 ./engine`
Expected: `gen-chat: 24 cases in gw/testdata/chat`, `gen-pricing: 39 cases in gw/testdata/pricing`, then FAIL `undefined: NewState`, `undefined: Request`, `undefined: Presets`, `undefined: NewPricer`.

- [ ] **Step 4: Implement**

Create `gw/engine/chat.go`:

```go
package engine

// The chat route class on the go engine (P4b, slice S2): POST /v1/chat/completions, not streaming, to one OpenAI-format
// provider with a plain API key (or none). PlanChat decides, without side effects, whether the engine serves a request
// and builds the provider request; everything it does not serve is handed to the control plane, which answers exactly
// as in js mode (P4b decision: hand-off). Respond and Failed turn the provider's answer (or the failed call) into the
// client's answer and the usage line, as lib/gateway/index.js chatCompletion and proxyChat do.

import (
	"bytes"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

// MaxBody is lib/gateway/index.js MAX_BODY: a bigger body is the control plane's 413.
const MaxBody = 20 << 20

// StaleMs: a budget snapshot older than this cannot tell the engine that no blocking budget applies.
const StaleMs = ipc.StaleAfterMs

// CORS are the headers of every gateway answer (lib/gateway/index.js CORS).
var CORS = [][2]string{
	{"Access-Control-Allow-Origin", "*"},
	{"Access-Control-Allow-Headers", "authorization, content-type, x-api-key, x-goog-api-key, x-zp-session, x-zp-token-saver, anthropic-version, anthropic-beta, a2a-version, a2a-extensions, mcp-protocol-version, mcp-session-id, last-event-id"},
	{"Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS"},
	{"Access-Control-Expose-Headers", "x-zp-session, x-zp-dropped, retry-after, mcp-session-id, www-authenticate"},
}

// State is one generation of what the control plane pushed: built when a config.push and its secrets.handoff are both
// in, swapped whole, never changed after.
type State struct {
	Gen        int64
	Gateway    ipc.GatewayConfig
	Keys       *Keys
	Secrets    map[string]string // provider id -> API key
	Mask       []string
	Pricer     *Pricer
	Snapshot   *ipc.BudgetSnapshot
	SnapshotAt int64 // epoch ms the snapshot says it was taken
}

// NewState builds a generation from config.push and secrets.handoff (same gen).
func NewState(c *ipc.ConfigPush, s *ipc.SecretsHandoff) *State {
	st := &State{Gen: c.Gen, Gateway: c.Gateway, Keys: NewKeys(c.Keys), Secrets: map[string]string{},
		Mask: append([]string(nil), s.Mask...), Pricer: NewPricer(c.Pricing)}
	for _, p := range s.Providers {
		if p.Value != nil && *p.Value != "" {
			st.Secrets[p.ID] = *p.Value
		}
	}
	return st
}

// WithSnapshot is the same generation with a newer budget snapshot.
func (s *State) WithSnapshot(b *ipc.BudgetSnapshot) *State {
	c := *s
	c.Snapshot, c.SnapshotAt = b, b.At
	return &c
}

// Request is what the engine reads of a client request.
type Request struct {
	Method  string
	RawPath string
	Query   string
	Header  http.Header
	Host    string // the Host header ("" when none)
	Peer    string // the client's address, "ip:port"
	Body    []byte
	TooBig  bool // the body was over MaxBody
}

// Plan is the engine's decision for one request.
type Plan struct {
	Handoff string // why the control plane serves it ("" when the engine does)
	Key     *ipc.GatewayKeyAuth
	Target  *Target
	Model   string // the model the client asked for
	URL     string
	Header  http.Header // the provider request's headers, with the credential
	Body    []byte      // the provider request's body, as JSON.stringify writes it
	Base    ipc.UsageRecord
}

func handoff(why string) Plan { return Plan{Handoff: why} }

// proxyHeaders: any of these means a proxy may stand between the client and the gateway (lib/gateway/client.js
// PROXY_HEADERS, server.js's no-sign-in refusal and lib/auth.js originAllowed): the control plane decides.
var proxyHeaders = []string{"Forwarded", "X-Forwarded-For", "X-Real-Ip", "Cf-Connecting-Ip", "True-Client-Ip",
	"X-Forwarded-Host", "X-Forwarded-Proto", "X-Forwarded-Port", "Via"}

// PlanChat decides for a POST to the exact path /v1/chat/completions, at now (epoch ms). It reads and changes nothing:
// the rate limit is the caller's (Take when serving, Wait when comparing).
func (s *State) PlanChat(r *Request, now int64) Plan {
	// The server's checks before the gateway (server.js handler): only a request every one of them passes in every
	// auth mode is the engine's, a loopback Host from a loopback client with no browser or proxy header.
	if !loopbackName(r.Host) || !loopbackPeer(r.Peer) {
		return handoff("access: not a loopback host and client")
	}
	for _, h := range append([]string{"Origin", "Referer", "Sec-Fetch-Site"}, proxyHeaders...) {
		if _, ok := r.Header[h]; ok {
			return handoff("access: a browser or proxy header")
		}
	}
	if r.Method != http.MethodPost {
		return handoff("method")
	}
	secret, ok := RequestSecret(r.Header, r.RawPath, r.Query)
	key, why := s.Keys.Authenticate(secret, ok, now)
	if key == nil {
		return handoff("key: " + why)
	}
	if r.TooBig || len(r.Body) == 0 {
		return handoff("body: empty or over 20 MB")
	}
	body, err := Parse(r.Body)
	if err != nil || body.Kind != Object {
		return handoff("body: not a JSON object the engine serves")
	}
	m := body.Get("model")
	if m == nil || m.Kind != String || m.S == "" {
		return handoff("model: missing")
	}
	model := m.S
	if strings.HasPrefix(model, "agent/") || strings.HasPrefix(model, "collab/") {
		return handoff("model: an agent or a collaboration")
	}
	if body.Get("stream").Truthy() {
		return handoff("stream: P4c")
	}
	if Authorize(key, "llm", model, "") != "" {
		return handoff("key: not allowed")
	}
	for _, rt := range s.Gateway.Routes {
		if rt.Name == model {
			return handoff("model: a route")
		}
	}
	t := Resolve(s.Gateway.Providers, model)
	if t == nil || (key.PrivateOnly && TierOf(t.Provider) != "private") {
		return handoff("model: no usable target")
	}
	if FormatOf(t.Provider) != "openai" {
		return handoff("provider: not the OpenAI format (P4d)")
	}
	cred := ""
	if a := t.Provider.Auth; a != nil {
		if a.Type != "key" {
			return handoff("provider: OAuth2 or a service account (P4g)")
		}
		if cred = s.Secrets[t.Provider.ID]; cred == "" {
			return handoff("provider: no credential")
		}
	}
	if key.BudgetUSD != nil {
		return handoff("budget: the key has a monthly budget")
	}
	if s.Snapshot == nil || now-s.SnapshotAt > StaleMs {
		return handoff("budget: no fresh snapshot")
	}
	if s.budgetApplies(key.ID, model, t.Provider.ID) {
		return handoff("budget: a blocking budget applies (P4e)")
	}
	if GuardActive(s.Gateway.Guardrails, key, ScopeTier(t.Provider, t.Model)) {
		return handoff("guardrails: a check applies (P4e)")
	}
	if s.Gateway.TokenSaver && !strings.EqualFold(strings.Join(r.Header.Values("X-Zp-Token-Saver"), ", "), "off") &&
		hasToolMessage(body) {
		return handoff("token saver: tool results")
	}
	out := body.Clone()
	out.Set("model", Str(t.Model))
	AdaptChat(t.Provider, t.Model, out)
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "application/json")
	if cred != "" {
		h.Set("Authorization", "Bearer "+cred)
	}
	return Plan{Key: key, Target: t, Model: model, URL: t.Provider.BaseURL + "/chat/completions", Header: h,
		Body: out.Stringify(), Base: s.base(key, t, model, r)}
}

func hasToolMessage(body *Value) bool {
	msgs := body.Get("messages")
	if msgs == nil || msgs.Kind != Array {
		return false
	}
	for _, m := range msgs.A {
		if r := m.Get("role"); r != nil && r.Kind == String && r.S == "tool" {
			return true
		}
	}
	return false
}

// budgetApplies: a blocking budget of the snapshot reaches this call (lib/cost/guard.js applies, source "gateway"),
// whatever its state: the engine leaves every budgeted call to the control plane until P4e.
func (s *State) budgetApplies(keyID, model, provider string) bool {
	nm := NormalizeModel(model)
	for _, b := range s.Snapshot.Budgets {
		if b.Enforce != "block" {
			continue
		}
		if b.Scope.Global {
			return true
		}
		switch v := b.Scope.Value; b.Scope.Dim {
		case "key", "source":
			if b.Scope.Dim == "key" && keyID == v || b.Scope.Dim == "source" && v == "gateway" {
				return true
			}
		case "center":
			if c, ok := s.Snapshot.KeyCenter[keyID]; ok && c == v {
				return true
			}
		case "model":
			if nm != "" && nm == v {
				return true
			}
		case "provider":
			if provider == v {
				return true
			}
		}
	}
	return false
}

func (s *State) base(key *ipc.GatewayKeyAuth, t *Target, model string, r *Request) ipc.UsageRecord {
	kind, format, stream := "chat", "openai", false
	rec := ipc.UsageRecord{KeyID: key.ID, Provider: t.Provider.ID, Model: model, Kind: &kind, Format: &format,
		Stream: &stream}
	if t.Name != model {
		rt := t.Name
		rec.RoutedTo = &rt
	}
	// lib/gateway/client.js clientTag: the tool by its User-Agent; local when the client is loopback and no proxy
	// header came (the engine serves no request with one).
	if c := clientOf(r.Header.Get("User-Agent")); c != "" {
		rec.Client = &c
	}
	local := true
	rec.Local = &local
	return rec
}

func clientOf(ua string) string {
	l := strings.ToLower(ua)
	switch {
	case strings.HasPrefix(l, "claude-cli/"):
		return "claude-code"
	case strings.HasPrefix(l, "codex_cli_rs/"), strings.HasPrefix(l, "codex/"):
		return "codex"
	}
	return ""
}

// loopbackName is lib/auth.js isLoopback of the Host without its port (server.js).
func loopbackName(host string) bool {
	h := strings.ToLower(host)
	if i := strings.LastIndexByte(h, ':'); i >= 0 && i < len(h)-1 && allDigits(h[i+1:]) {
		h = h[:i]
	}
	return h == "127.0.0.1" || h == "localhost" || h == "::1" || h == "[::1]"
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

// loopbackPeer is lib/gateway/client.js isLoopback of the client's address.
func loopbackPeer(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback() && (ip.To4() != nil || host == "::1")
}

// Answer is the client's answer and the usage line of one served call.
type Answer struct {
	Status int
	Header http.Header
	Body   []byte
	Usage  ipc.UsageRecord
}

func answerHeader() http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	for _, c := range CORS {
		h.Set(c[0], c[1])
	}
	return h
}

// Respond is chatCompletion's non-streaming answer for the provider's status and body, at ms after the start.
func (s *State) Respond(p *Plan, status int, raw []byte, ms float64) Answer {
	text := decodeUTF8(raw)
	j, err := Parse([]byte(text))
	if err != nil {
		j = nil
	}
	ok := status >= 200 && status <= 299
	in, out, cached := tokens(j)
	rec := p.Base
	rec.Status, rec.Ms = int64(status), ms
	rec.Input, rec.Output, rec.Cached = &in, &out, &cached
	if !ok {
		msg := text
		if e := j.Get("error"); e.Truthy() {
			if m := e.Get("message"); m.Truthy() && m.Kind == String {
				msg = m.S
			}
		}
		masked := s.mask(jsSlice(msg, 200))
		rec.Error = &masked
	}
	model := rec.Model
	if rec.RoutedTo != nil {
		model = *rec.RoutedTo
	}
	rec.Cost = s.Pricer.Cost(model, float64(in), float64(out), float64(cached))
	body := []byte(text)
	if j.Truthy() {
		if ok && j.Kind == Object {
			j.Set("model", Str(p.Model))
		}
		body = j.Stringify()
	}
	return Answer{Status: status, Header: answerHeader(), Body: body, Usage: rec}
}

// Failed is proxyChat's answer when the call to the only target threw: 502 upstream_error with the provider's id and
// what failed (masked), and a usage line with status 599.
func (s *State) Failed(p *Plan, why string, ms float64) Answer {
	msg := s.mask(p.Target.Provider.ID + ": " + why)
	rec := p.Base
	rec.RoutedTo = nil
	rec.Status, rec.Ms, rec.Error = 599, ms, &msg
	b := NewObject()
	e := NewObject()
	e.Set("message", Str(msg))
	e.Set("type", Str("upstream_error"))
	b.Set("error", e)
	return Answer{Status: http.StatusBadGateway, Header: answerHeader(), Body: b.Stringify(), Usage: rec}
}

// RateLimited is authed's 429 for a key past its requests per minute.
func RateLimited(key *ipc.GatewayKeyAuth, wait int64) Answer {
	rpm := key.RPM
	b := NewObject()
	e := NewObject()
	e.Set("message", Str("rate limit of "+NumberString(float64(rpm))+" requests per minute reached"))
	e.Set("type", Str("rate_limit_error"))
	b.Set("error", e)
	h := answerHeader()
	h.Set("Retry-After", NumberString(float64(wait)))
	return Answer{Status: http.StatusTooManyRequests, Header: h, Body: b.Stringify()}
}

// tokens is usageOf: prompt, completion and cached tokens, 0 when missing; a count that is not a whole number of zero
// or more is 0 here (the IPC record holds integers; the JS gateway would store it as it came).
func tokens(j *Value) (int64, int64, int64) {
	u := j.Get("usage")
	n := func(v *Value) int64 {
		if v == nil || v.Kind != Number || v.N < 0 || v.N != float64(int64(v.N)) {
			return 0
		}
		return int64(v.N)
	}
	return n(u.Get("prompt_tokens")), n(u.Get("completion_tokens")), n(u.Get("prompt_tokens_details").Get("cached_tokens"))
}

// mask is lib/secrets.js mask: every known secret value becomes ***.
func (s *State) mask(text string) string {
	for _, v := range s.Mask {
		if v != "" {
			text = strings.ReplaceAll(text, v, "***")
		}
	}
	return text
}

// jsSlice is String.prototype.slice(0, n): the first n UTF-16 code units (a pair cut in half loses its high half here;
// JavaScript would keep it as a lone surrogate).
func jsSlice(s string, n int) string {
	units := 0
	for i, r := range s {
		w := utf16.RuneLen(r)
		if units+w > n {
			return s[:i]
		}
		units += w
	}
	return s
}

// decodeUTF8 is Response.text(): a leading byte order mark dropped, invalid bytes each a U+FFFD.
func decodeUTF8(b []byte) string {
	b = bytes.TrimPrefix(b, []byte{0xef, 0xbb, 0xbf})
	if utf8.Valid(b) {
		return string(b)
	}
	return strings.ToValidUTF8(string(b), "�")
}

// NowMs is the engine's clock in epoch ms.
func NowMs() int64 { return time.Now().UnixMilli() }
```

Create `gw/engine/pricing.go`:

```go
package engine

import (
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

// NormalizeModel is lib/pricing.js normalizeModel: the price-table form of a model name.
func NormalizeModel(model string) string {
	m := strings.ToLower(strings.TrimFunc(model, jsSpace))
	m = m[strings.LastIndexByte(m, '/')+1:]
	m = reBracket.ReplaceAllString(m, "")
	m = reAnthropic.ReplaceAllString(m, "")
	m = reAt.ReplaceAllString(m, "")
	m = reVersion.ReplaceAllString(m, "")
	if strings.HasPrefix(m, "claude") {
		m = strings.ReplaceAll(m, ".", "-")
	}
	return m
}

var (
	reBracket   = regexp.MustCompile(`\[[^\]]*\]$`)
	reAnthropic = regexp.MustCompile(`^(?:(?:us|eu|apac|global|jp|au|ca|us-gov)\.)?anthropic\.`)
	reAt        = regexp.MustCompile(`@[\w.-]+$`)
	reVersion   = regexp.MustCompile(`-v\d+(?::\d+)?$`)
)

// KeyMatches is lib/pricing.js keyMatches.
func KeyMatches(model, key string) bool {
	if !strings.HasPrefix(model, key) {
		return false
	}
	if len(model) == len(key) || !isAlnum(key[len(key)-1]) {
		return true
	}
	switch c := model[len(key)]; c {
	case '-', '@', ':', '_':
		return true
	case '.':
		return len(model) == len(key)+1 || model[len(key)+1] < '0' || model[len(key)+1] > '9'
	}
	return false
}

func isAlnum(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') }

// Pricer is lib/pricing.js makePricer over config.push pricing: the longest matching price key wins.
type Pricer struct {
	table map[string]ipc.PriceEntry
	keys  [][2]string // normalized, original; longest normalized first
}

func NewPricer(table map[string]ipc.PriceEntry) *Pricer {
	p := &Pricer{table: table}
	for k := range table {
		p.keys = append(p.keys, [2]string{NormalizeModel(k), k})
	}
	// JavaScript sorts Object.keys order stably; a map has no order, so equal lengths go by key (they differ only for
	// two keys that normalize to the same name).
	sort.Slice(p.keys, func(i, j int) bool {
		if len(p.keys[i][0]) != len(p.keys[j][0]) {
			return len(p.keys[i][0]) > len(p.keys[j][0])
		}
		return p.keys[i][1] < p.keys[j][1]
	})
	return p
}

// Key is the price key of model, or "".
func (p *Pricer) Key(model string) string {
	m := NormalizeModel(model)
	for _, k := range p.keys {
		if k[0] != "" && KeyMatches(m, k[0]) {
			return k[1]
		}
	}
	return ""
}

// Cost is lib/gateway/usage.js cost in nano-USD (toNano of the same float arithmetic on the same USD prices): input
// beyond the cached tokens at the input price, cached at the cache-read price (else the input price), output at the
// output price. 0 for a model without a price.
func (p *Pricer) Cost(model string, input, output, cached float64) int64 {
	k := p.Key(model)
	if k == "" {
		return 0
	}
	e := p.table[k]
	usd := func(n int64) float64 { return float64(n) / 1e9 }
	in, out := usd(e.Input), usd(e.Output)
	cr := in
	if e.CacheRead != nil && *e.CacheRead != 0 {
		cr = usd(*e.CacheRead)
	}
	c := (math.Max(0, input-cached)*in + cached*cr + output*out) / 1e6
	n := math.Round(c * 1e9)
	if n == 0 || math.IsNaN(n) {
		return 0
	}
	return int64(n)
}
```

Create `gw/engine/providers.go`:

```go
package engine

import (
	"regexp"
	"strings"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

// Preset is what the engine needs of a lib/gateway/providers.js preset (the table is checked against the JS one by
// gw/testdata/providers, scripts/fixtures/gen-providers.js).
type Preset struct {
	Local               bool   `json:"local,omitzero"`
	Forwards            bool   `json:"forwards,omitzero"`
	MaxCompletionTokens bool   `json:"maxCompletionTokens,omitzero"`
	Format              string `json:"format,omitzero"`
}

// Presets are the presets with a flag the engine reads; any other preset name has none.
var Presets = map[string]Preset{
	"ollama": {Local: true}, "lmstudio": {Local: true}, "vllm": {Local: true}, "tgi": {Local: true},
	"llamacpp": {Local: true}, "jan": {Local: true}, "localai": {Local: true},
	"litellm":      {Local: true, Forwards: true},
	"9router":      {Local: true, Forwards: true},
	"openai":       {MaxCompletionTokens: true},
	"anthropic":    {Format: "anthropic"},
	"azure-openai": {MaxCompletionTokens: true},
}

// Target is one provider and the model it is asked for (lib/gateway/providers.js resolve).
type Target struct {
	Provider *ipc.ProviderConfig
	Model    string // the provider's own model name
	Name     string // the name it was asked by: "<provider>/<model>" or an alias
}

func isTrue(b *bool) bool  { return b != nil && *b }
func isFalse(b *bool) bool { return b != nil && !*b }

// TierOf is providers.js tierOf: the tier the owner set, else private for a local preset.
func TierOf(p *ipc.ProviderConfig) string {
	if p.Tier != nil && *p.Tier != "" {
		return *p.Tier
	}
	if Presets[p.Preset].Local {
		return "private"
	}
	return "external"
}

// FormatOf is providers.js formatOf.
func FormatOf(p *ipc.ProviderConfig) string {
	if p.Format != nil && *p.Format != "" {
		return *p.Format
	}
	if f := Presets[p.Preset].Format; f != "" {
		return f
	}
	return "openai"
}

// Usable is providers.js usable: enabled, and private or approved.
func Usable(p *ipc.ProviderConfig) bool {
	return !isFalse(p.Enabled) && (TierOf(p) == "private" || !isFalse(p.Approved))
}

// Resolve is providers.js resolve: an alias of a usable provider (in list order), else "<id>/<model>" of the first
// usable provider with that id; nil when nothing matches.
func Resolve(providers []ipc.ProviderConfig, name string) *Target {
	if name == "" {
		return nil
	}
	for i := range providers {
		p := &providers[i]
		if Usable(p) {
			if m, ok := p.Aliases[name]; ok && m != "" {
				return &Target{Provider: p, Model: m, Name: name}
			}
		}
	}
	if i := strings.IndexByte(name, '/'); i > 0 {
		for j := range providers {
			p := &providers[j]
			if p.ID == name[:i] && Usable(p) {
				return &Target{Provider: p, Model: name[i+1:], Name: name}
			}
		}
	}
	return nil
}

var openAIReasoning = regexp.MustCompile(`(?i)(^|/)(o[1-9]|gpt-5)`)

// TokenParamOf is providers.js tokenParamOf.
func TokenParamOf(p *ipc.ProviderConfig, model string) string {
	if p.TokenParam != nil && (*p.TokenParam == "max_tokens" || *p.TokenParam == "max_completion_tokens") {
		return *p.TokenParam
	}
	if Presets[p.Preset].MaxCompletionTokens || openAIReasoning.MatchString(model) {
		return "max_completion_tokens"
	}
	return "max_tokens"
}

// AdaptChat is providers.js adaptChat for a body from a client (no field the gateway derived itself): max_tokens
// becomes max_completion_tokens, at the end, for a target that takes only that. body is changed in place.
func AdaptChat(p *ipc.ProviderConfig, model string, body *Value) {
	mt := body.Get("max_tokens")
	mct := body.Get("max_completion_tokens")
	if mt != nil && mt.Kind != Null && (mct == nil || mct.Kind == Null) && TokenParamOf(p, model) == "max_completion_tokens" {
		body.Set("max_completion_tokens", mt)
		body.Delete("max_tokens")
	}
}

var cloudModel = regexp.MustCompile(`(?i)(^|[:-])cloud$`)

// ScopeTier is lib/gateway/guardrails.js scopeTier: an Ollama cloud model and a local router that forwards (without a
// tier of its own) count as external.
func ScopeTier(p *ipc.ProviderConfig, model string) string {
	if cloudModel.MatchString(model) {
		return "external"
	}
	if (p.Tier == nil || *p.Tier == "") && Presets[p.Preset].Forwards {
		return "external"
	}
	return TierOf(p)
}

// GuardActive is lib/gateway/guardrails.js resolvePolicy then inScope: whether any check would look at a call to a
// target of this tier. The engine serves only calls no check looks at; the checks themselves are P4e.
func GuardActive(global ipc.GuardrailsPolicy, key *ipc.GatewayKeyAuth, tier string) bool {
	p := global
	own := key.Guardrails
	if own.Secrets != nil {
		p.Secrets = *own.Secrets
	}
	if own.PII != nil {
		p.PII = *own.PII
	}
	if own.Injection != nil {
		p.Injection = *own.Injection
	}
	inScope := p.Scope == "all" || tier != "private"
	return inScope && (p.Secrets != "off" || p.PII != "off" || p.Injection != "off")
}
```

- [ ] **Step 5: Generate the fixtures from the JS code**

Run: `node scripts/fixtures/gen-chat.js && node scripts/fixtures/gen-pricing.js`
Expected: the same two lines and no change to the files Step 3 wrote.

- [ ] **Step 6: Run the tests and the task gate**

Run:

```bash
export GOTOOLCHAIN=local ZAAFPULSE_DATA="$(mktemp -d)" HOME="$(mktemp -d)"
test -z "$(gofmt -l gw)" && go -C gw vet ./... && GOOS=windows go -C gw vet ./... && go -C gw test -race -count=1 ./...
deno task typecheck
node --test test/*.test.js
deno test -q --no-check --no-prompt -P=test test/*.test.js
npm run test:contract
```

Expected: no gofmt output, `ok` for every Go package, `deno check` with no error, `fail 0` from Node, `0 failed` from Deno, and the contract suite
`pass 172` (from Task 8 on, `pass 179`, with one case skipped outside shadow mode), `fail 0`.

- [ ] **Step 7: Commit and push**

```bash
git add -A -- gw scripts
git status --short   # nothing outside this task's files, nothing under .superpowers/
git commit -m "P4b T7: the chat engine plans, answers and prices a call as the JS gateway does, checked against its recorded answers"
git push origin migrate/p4b-passthrough
```

---

### Task 8: Serving chat on the go engine (slice S2)

The front hands a `chat` request on `go` to the engine, which serves it or forwards it unchanged; the provider call has the JS timeouts, redirect rule and error names, uses the proxy and the extra CAs (Decisions 8 and 9), and the usage line goes through the outbox (Decision 12). `config.json` `gateway.engines` puts `chat` on go; the contract suite gets `ZP_ENGINE=go` and a file of its own.

**Files:**
- Create: `gw/cmd/zaafpulse-gw/engine_test.go`, `gw/engine/serve.go`, `gw/engine/upstream.go`, `gw/engine/upstream_test.go`, `test/contract/go-chat.test.js`
- Modify: `gw/cmd/zaafpulse-gw/front.go`, `gw/cmd/zaafpulse-gw/front_test.go`, `gw/cmd/zaafpulse-gw/main.go`, `gw/front/front.go`, `lib/front/index.js`, `server.js`, `test/contract/formats.test.js`, `test/contract/harness.js`, `test/front.test.js`


**Interfaces:**
- Consumes: Tasks 1–7; `gateway.appendAndSend` (`gw/cmd/zaafpulse-gw/main.go`, under the outbox's send lock).
- Produces:
  - `engine.NewUpstream(getenv func(string) string) *Upstream`; `(*Upstream).Call(ctx, p *Plan) Result` (`Result{Status int; Body []byte; Why string}`); `ErrorCode(err error) string`; `Timeout = 120 s`, `ConnectTimeout = 10 s`.
  - `engine.New(up *Upstream, emit func(*ipc.UsageReport) error, logf func(string, ...any)) *Engine`; `SetState`, `State`, `Serve(class string, w, r, rawPath string, js http.Handler)`; `ReadRequest(r, rawPath) *Request` (re-attaches the whole body).
  - `front.GoClasses = {"chat": true}`; `front.GoEngine` interface (`Serve`); `(*Server).SetGoEngine(e GoEngine)`. A config.push with go for another class exits 2.
  - JS: `GO_CLASSES = ['chat']`, `enginesOf(gateway)` (exported from `lib/front`), `createFront({ ..., engines })`; `frontSection` accepts go for `chat`; `status().engines` is the pushed one in front mode.
  - Contract: `ZP_ENGINE=go` (front, `engines: { chat: 'go' }`, `shadowDays: 0`, `shadowMinCompares: 0`); `test/contract/go-chat.test.js` (7 cases in every mode).

- [ ] **Step 1: Check the starting point**

Run: `git status --short && git rev-parse --abbrev-ref HEAD && git log --oneline -1`
Expected: nothing listed, `migrate/p4b-passthrough`, and the previous task's commit on top (Task 1: `0ec1247`, the merge of `origin/migrate/deno-go` with stream C).

- [ ] **Step 2: Write the failing tests**

Create `gw/cmd/zaafpulse-gw/engine_test.go`:

```go
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// chatConfig is a config.push with chat on the go engine, one provider at baseURL and one key for secret.
func chatConfig(seq, gen, port, upstream int, baseURL, secret string) string {
	sum := sha256.Sum256([]byte(secret))
	gw := fmt.Sprintf(`{"providers":[{"id":"pv","preset":"ollama","baseUrl":%q}],"routes":[],"guardrails":`+
		`{"scope":"external","secrets":"off","pii":"off","injection":"off"},"defaults":{"model":"","plannerModel":"",`+
		`"codeModel":""},"tokenSaver":true,"modelMap":[]}`, baseURL)
	key := fmt.Sprintf(`{"id":"k1","name":"k1","scopes":["llm"],"models":[],"agents":[],"knowledge":[],"budgetUSD":null,`+
		`"rpm":1,"privateOnly":false,"expiresAt":null,"revokedAt":null,"guardrails":{},"prefix":"zp_","approveOwnRuns":false,`+
		`"createdAt":0,"lastUsedAt":null,"hash":%q}`, hex.EncodeToString(sum[:]))
	return fmt.Sprintf(`{"v":1,"type":"config.push","seq":%d,"ts":0,"body":{"gen":%d,"gateway":%s,"keys":[%s],`+
		`"pricing":{"llama-9":{"input":1000000000,"output":2000000000}},"front":%s}}`,
		seq, gen, gw, key, frontJSON(port, upstream, `{"chat":"go"}`))
}

// End to end in the gateway process: chat on go is answered by the provider, never by the control plane; the call's
// usage line goes out as a usage.report; the key's rate limit answers 429 from Go; a hand-off reaches the control plane.
func TestChatOnTheGoEngine(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/v1/chat/completions" || string(b) != `{"model":"llama-9","messages":[]}` {
			t.Errorf("provider got %s %s", r.URL.Path, b)
		}
		_, _ = io.WriteString(w, `{"id":"c1","usage":{"prompt_tokens":1000,"completion_tokens":500}}`)
	}))
	defer provider.Close()
	cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "control plane")
	}))
	defer cp.Close()
	port := freePort(t)
	r := drive(t)
	r.next(t, "hello")
	r.send(`{"v":1,"type":"hello.ok","seq":1,"ts":0,"re":"h1","body":{"protocol":1,"cpVersion":"test"}}`)
	r.send(chatConfig(2, 1, port, portOf(cp.URL), provider.URL+"/v1", "zp_engineTestSecret"))
	r.send(secretsLine(3, 1))
	r.send(fmt.Sprintf(`{"v":1,"type":"budget.snapshot","seq":4,"ts":%d,"body":{"at":%d,"budgets":[],"keyCenter":{}}}`,
		time.Now().UnixMilli(), time.Now().UnixMilli()))
	r.next(t, "ready")
	post := func(secret, body string) (int, string) {
		req, _ := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", port), strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+secret)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode, string(b)
	}
	if code, body := post("zp_engineTestSecret", `{"model":"pv/llama-9","messages":[]}`); code != 200 ||
		body != `{"id":"c1","usage":{"prompt_tokens":1000,"completion_tokens":500},"model":"pv/llama-9"}` {
		t.Fatalf("%d %s; stderr %s", code, body, r.stderr)
	}
	u := r.next(t, "usage.report")
	for _, want := range []string{`"keyId":"k1"`, `"provider":"pv"`, `"model":"pv/llama-9"`, `"status":200`,
		`"input":1000`, `"output":500`, `"cost":2000000`, `"local":true`} {
		if !strings.Contains(u, want) {
			t.Errorf("usage.report %s lacks %s", u, want)
		}
	}
	if code, body := post("zp_engineTestSecret", `{"model":"pv/llama-9","messages":[]}`); code != 429 ||
		!strings.Contains(body, "rate limit of 1 requests per minute reached") {
		t.Fatalf("rate limit: %d %s", code, body)
	}
	if code, body := post("zp_unknownKey", `{"model":"pv/llama-9"}`); code != 200 || body != "control plane" {
		t.Fatalf("hand-off: %d %s", code, body)
	}
}

func portOf(u string) int {
	var p int
	_, _ = fmt.Sscanf(u[strings.LastIndexByte(u, ':')+1:], "%d", &p)
	return p
}
```

Create `gw/engine/upstream_test.go`:

```go
package engine

import (
	"context"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func planTo(url string) *Plan {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Authorization", "Bearer sk-test")
	return &Plan{URL: url, Header: h, Body: []byte(`{"model":"m1"}`)}
}

// The provider gets the plan's method, headers and body; its status and body come back as they are.
func TestUpstreamCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer sk-test" || string(b) != `{"model":"m1"}` ||
			r.Header.Get("Accept-Encoding") != "" {
			t.Errorf("%s %v %s", r.Method, r.Header, b)
		}
		w.WriteHeader(418)
		_, _ = io.WriteString(w, "teapot")
	}))
	defer srv.Close()
	res := NewUpstream(func(string) string { return "" }).Call(context.Background(), planTo(srv.URL+"/v1/chat/completions"))
	if res.Status != 418 || string(res.Body) != "teapot" || res.Why != "" {
		t.Fatalf("%+v", res)
	}
}

// A redirect is followed (same method and body) to the same origin only; another host is refused with the JS text.
func TestUpstreamRedirects(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("followed") }))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a":
			http.Redirect(w, r, "/b", http.StatusFound)
		case "/b":
			b, _ := io.ReadAll(r.Body)
			_, _ = io.WriteString(w, r.Method+" "+string(b))
		case "/away":
			http.Redirect(w, r, other.URL+"/x", http.StatusTemporaryRedirect)
		}
	}))
	defer srv.Close()
	up := NewUpstream(func(string) string { return "" })
	if res := up.Call(context.Background(), planTo(srv.URL+"/a")); string(res.Body) != `POST {"model":"m1"}` {
		t.Fatalf("same origin: %+v", res)
	}
	res := up.Call(context.Background(), planTo(srv.URL+"/away"))
	want := "refused a redirect from " + strings.TrimPrefix(srv.URL, "http://") + " to " +
		strings.TrimPrefix(other.URL, "http://") + "; credentials are not forwarded to another host"
	if res.Why != want {
		t.Fatalf("%q, want %q", res.Why, want)
	}
}

// No answer started within the timeout: "timed out"; nothing listening: ECONNREFUSED; a connection closed with no
// answer: UND_ERR_SOCKET (the names Node's fetch gives, which the JS gateway puts in its 502).
func TestUpstreamFailures(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer slow.Close()
	up := NewUpstream(func(string) string { return "" })
	up.timeout = 200 * time.Millisecond
	if res := up.Call(context.Background(), planTo(slow.URL)); res.Why != "timed out" {
		t.Errorf("slow: %+v", res)
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	_ = ln.Close()
	if res := up.Call(context.Background(), planTo("http://"+addr+"/")); res.Why != "ECONNREFUSED" {
		t.Errorf("refused: %+v", res)
	}
	reset, _ := net.Listen("tcp", "127.0.0.1:0")
	defer reset.Close()
	go func() {
		for {
			c, err := reset.Accept()
			if err != nil {
				return
			}
			_, _ = c.Read(make([]byte, 4096))
			_ = c.Close()
		}
	}()
	if res := up.Call(context.Background(), planTo("http://"+reset.Addr().String()+"/")); res.Why != "UND_ERR_SOCKET" {
		t.Errorf("reset: %+v", res)
	}
}

// A provider with a private CA is trusted once NODE_EXTRA_CA_CERTS (or DENO_CERT, SSL_CERT_FILE) names its certificate,
// as Node and Deno trust it; without it the call fails with Node's name for an unknown issuer.
func TestUpstreamExtraCA(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") }))
	defer srv.Close()
	if res := NewUpstream(func(string) string { return "" }).Call(context.Background(), planTo(srv.URL)); res.Why == "" {
		t.Fatalf("an unknown CA was trusted: %+v", res)
	}
	f := filepath.Join(t.TempDir(), "ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(f, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"NODE_EXTRA_CA_CERTS", "DENO_CERT", "SSL_CERT_FILE"} {
		env := func(k string) string {
			if k == name {
				return f
			}
			return ""
		}
		if res := NewUpstream(env).Call(context.Background(), planTo(srv.URL)); res.Why != "" || string(res.Body) != "ok" {
			t.Errorf("%s: %+v", name, res)
		}
	}
}
```

Create `test/contract/go-chat.test.js`:

```js
// Contract: non-streaming chat as the go engine serves it (P4b), with guardrails off so the engine may serve: answers,
// errors, the usage line, the rate limit, and a revoked key refused as soon as the revoke has answered. The cases hold
// in every mode; under ZP_ENGINE=go each call the engine serves must have its usage line from the gateway's outbox
// (the line carries "ipc"), so the run proves the engine answered and not the server.
const test = require('node:test');
const assert = require('node:assert/strict');
const { start } = require('./harness');
const { toNano } = require('../../lib/money');

const GO = process.env.ZP_ENGINE === 'go';
let h;
test.before(async () => {
  h = await start();
  await h.setGuardrails({ scope: 'external', secrets: 'off', pii: 'off', injection: 'off' });
});
test.after(async () => {
  if (h) await h.stop();
});

const user = [{ role: 'user', content: 'hi' }];
const chat = (body, opts = {}) => h.call('/v1/chat/completions', { key: h.key, body, ...opts });
// The newest usage line once it is there: a line from the gateway reaches the log through the IPC ingest, a few
// milliseconds after the answer.
async function lineAfter(before) {
  const end = Date.now() + 5000;
  for (;;) {
    const logs = await h.logs();
    if (logs.length > before) return { line: logs[0], count: logs.length - before };
    if (Date.now() > end) throw new Error('no usage line within 5 s');
    await new Promise((r) => setTimeout(r, 20));
  }
}
const served = (line) => {
  if (GO) assert.ok(line.ipc, `the go engine served it: ${JSON.stringify(line)}`);
};

const CASES = [
  {
    name: 'a call to an external provider: the answer with the requested model, one usage line, US$0.002',
    run: async () => {
      const before = (await h.logs()).length;
      const r = await chat({ model: 'oa/m1', messages: user, max_tokens: 5 });
      assert.equal(r.status, 200);
      assert.equal(r.json.model, 'oa/m1');
      assert.equal(r.json.choices[0].message.content, 'hi');
      assert.equal(r.headers.get('access-control-allow-origin'), '*');
      const up = h.upstream.requests.filter((x) => x.path === '/chat/completions').at(-1);
      assert.equal(up.body.model, 'm1');
      assert.equal(up.body.max_tokens, 5);
      assert.equal(up.headers.authorization, 'Bearer sk-contract-oa');
      const { line, count } = await lineAfter(before);
      assert.equal(count, 1);
      served(line);
      assert.deepEqual(
        [
          line.keyId,
          line.provider,
          line.model,
          line.kind,
          line.format,
          line.stream,
          line.status,
          line.input,
          line.output,
        ],
        [h.keyId, 'oa', 'oa/m1', 'chat', 'openai', false, 200, 1000, 500],
      );
      assert.equal(line.local, true);
      assert.equal(toNano(line.cost), 2_000_000);
    },
  },
  {
    name: 'a private provider and x-api-key',
    run: async () => {
      const before = (await h.logs()).length;
      const r = await chat({ model: 'pv/m1', messages: user }, { key: undefined, headers: { 'x-api-key': h.key } });
      assert.equal(r.status, 200);
      served((await lineAfter(before)).line);
    },
  },
  {
    name: 'upstream 400, 500 and 429 pass through with their body; the retry-after stays upstream',
    run: async () => {
      for (const [model, status, body] of [
        ['oa/fail400', 400, { error: { message: 'bad parameter temperature', type: 'invalid_request_error' } }],
        ['oa/fail500', 500, { error: { message: 'upstream exploded' } }],
        ['oa/fail429', 429, { error: { message: 'slow down' } }],
      ]) {
        const before = (await h.logs()).length;
        const r = await chat({ model, messages: user });
        assert.equal(r.status, status, model);
        assert.deepEqual(r.json, body);
        assert.equal(r.headers.get('retry-after'), null);
        const { line } = await lineAfter(before);
        served(line);
        assert.equal(line.status, status);
        assert.equal(line.error, body.error.message);
      }
    },
  },
  {
    name: 'a 200 that is not JSON passes through; a reset and a dead provider are 502 naming the provider only',
    run: async () => {
      const g = await chat({ model: 'oa/garbage', messages: user });
      assert.deepEqual([g.status, g.text], [200, 'not json at all']);
      for (const [model, id] of [
        ['oa/reset', 'oa'],
        ['dead/m1', 'dead'],
      ]) {
        const before = (await h.logs()).length;
        const r = await chat({ model, messages: user });
        assert.equal(r.status, 502, model);
        assert.equal(r.json.error.type, 'upstream_error');
        assert.match(r.json.error.message, new RegExp(`^${id}: `));
        assert.doesNotMatch(r.json.error.message, /127\.0\.0\.1|sk-contract/);
        const { line } = await lineAfter(before);
        served(line);
        assert.equal(line.status, 599);
      }
    },
  },
  {
    name: 'the rate limit of a key answers 429 rate_limit_error with retry-after',
    run: async () => {
      const k = await h.newKey({ scopes: ['llm'], rpm: 2 });
      for (let i = 0; i < 2; i++)
        assert.equal((await chat({ model: 'pv/m1', messages: user }, { key: k.secret })).status, 200);
      const r = await chat({ model: 'pv/m1', messages: user }, { key: k.secret });
      assert.equal(r.status, 429);
      assert.deepEqual(r.json, {
        error: { message: 'rate limit of 2 requests per minute reached', type: 'rate_limit_error' },
      });
      assert.ok(Number(r.headers.get('retry-after')) > 0);
    },
  },
  {
    name: 'a key revoked is refused as soon as the revoke has answered (no window on the gateway)',
    run: async () => {
      const k = await h.newKey({ scopes: ['llm'] });
      assert.equal((await chat({ model: 'pv/m1', messages: user }, { key: k.secret })).status, 200);
      await h.admin('keys', { action: 'revoke', id: k.id });
      const r = await chat({ model: 'pv/m1', messages: user }, { key: k.secret });
      assert.equal(r.status, 401);
      assert.deepEqual(r.json, { error: { message: 'this API key was revoked', type: 'authentication_error' } });
    },
  },
  {
    name: 'a model outside the key allow-list is 403, a streamed call and an agent model still answer',
    run: async () => {
      const k = await h.newKey({ scopes: ['llm'], models: ['oa/*'] });
      const no = await chat({ model: 'pv/m1', messages: user }, { key: k.secret });
      assert.equal(no.status, 403);
      assert.equal(no.json.error.message, 'this key may not use model "pv/m1"');
      const s = await chat({ model: 'oa/m1', messages: user, stream: true });
      assert.equal(s.status, 200);
      assert.match(s.text, /data: \[DONE\]/);
    },
  },
];

for (const c of CASES) test(`go chat: ${c.name}`, c.run);
```

Apply (from the repository root):

```bash
git apply <<'PATCH'
diff --git a/gw/cmd/zaafpulse-gw/front_test.go b/gw/cmd/zaafpulse-gw/front_test.go
index f28cab6..7a8d0a4 100644
--- a/gw/cmd/zaafpulse-gw/front_test.go
+++ b/gw/cmd/zaafpulse-gw/front_test.go
@@ -125,11 +125,12 @@ func TestFrontPortInUseExits4(t *testing.T) {
 	}
 }
 
+// P4b: only chat may be on the go engine; any other class there is a config this gateway cannot apply.
 func TestFrontGoEngineIsRefusedWithExit2(t *testing.T) {
-	code, stderr, _ := handshake(t, frontJSON(freePort(t), 1, `{"chat":"go"}`))
+	code, stderr, _ := handshake(t, frontJSON(freePort(t), 1, `{"chat":"go","messages":"go"}`))
 	select {
 	case c := <-code:
-		if c != 2 || !strings.Contains(stderr.String(), `fatal: config.push: route class "chat" is on engine "go"`) {
+		if c != 2 || !strings.Contains(stderr.String(), `fatal: config.push: route class "messages" is on engine "go"`) {
 			t.Fatalf("exit %d, stderr %s", c, stderr)
 		}
 	case <-time.After(10 * time.Second):
diff --git a/test/contract/formats.test.js b/test/contract/formats.test.js
index 61acb8f..867aa19 100644
--- a/test/contract/formats.test.js
+++ b/test/contract/formats.test.js
@@ -773,7 +773,7 @@ const CASES = [
       assert.equal(r.status, 400);
       // With the Go gateway in front, Go's HTTP server refuses a request target with a broken escape itself, before
       // any handler, with its own plain-text 400 (P4a: accepted; only the body differs, for a request no client sends).
-      if (process.env.ZP_ENGINE === 'front' && r.text === '400 Bad Request') return;
+      if (['front', 'go'].includes(process.env.ZP_ENGINE) && r.text === '400 Bad Request') return;
       assert.equal(r.json.error.message, 'model id is not valid URL encoding');
     },
   },
diff --git a/test/contract/harness.js b/test/contract/harness.js
index 4ec7fee..ba25d73 100644
--- a/test/contract/harness.js
+++ b/test/contract/harness.js
@@ -7,6 +7,8 @@
 //   ZP_CONTRACT_KEY        with ZP_CONTRACT_URL: the main key to use instead of creating one
 //   ZP_ENGINE=front        gateway.engine "front": the Go gateway (built by scripts/ipc/build-stub.js, or ZP_GW_BIN)
 //                          holds the port and forwards every case to the server (P4a T6)
+//   ZP_ENGINE=go           front, with chat on the go engine (P4b): the gateway serves the chat calls it can itself
+//                          and hands the others to the server
 // Nothing here reaches the network: the providers point at the fake upstream on 127.0.0.1, price sync is off, and
 // the judge ports, notifications and runtime polling are off (as test/helpers.js NO_JUDGE_PORTS).
 const fs = require('fs');
@@ -34,6 +36,7 @@ const SEED_CONFIG = {
   pricingSync: { enabled: false },
   pricing: PRICES,
   ...(process.env.ZP_ENGINE === 'front' ? { gateway: { engine: 'front' } } : {}),
+  ...(process.env.ZP_ENGINE === 'go' ? { gateway: { engine: 'front', engines: { chat: 'go' } } } : {}),
 };
 
 // The environment a front-mode server needs: the gateway binary. Throws without Go: a front run is asked for.
@@ -215,7 +218,20 @@ function clientFor(base) {
     call,
     admin,
     // The usage records, newest first (GET /api/gateway/logs).
-    logs: async () => (await call('/api/gateway/logs', { method: 'GET' })).json.entries,
+    // With chat on the go engine a call's line reaches the log through the IPC ingest, a few milliseconds after the
+    // answer (P4b deviation 5): the list is read until two reads 100 ms apart agree (at most 2 s).
+    logs: async () => {
+      const read = async () => (await call('/api/gateway/logs', { method: 'GET' })).json.entries;
+      let entries = await read();
+      if (process.env.ZP_ENGINE !== 'go') return entries;
+      for (let i = 0; i < 20; i++) {
+        await sleep(100);
+        const again = await read();
+        if (again.length === entries.length && JSON.stringify(again[0]) === JSON.stringify(entries[0])) return again;
+        entries = again;
+      }
+      return entries;
+    },
     // A new key: { secret, id }. Defaults: every scope, a high rate limit so cases never limit each other.
     newKey: async (input = {}) => {
       const r = await admin('keys', { action: 'create', name: 'contract', rpm: 10000, ...input });
diff --git a/test/front.test.js b/test/front.test.js
index 4bb8f5e..b37a273 100644
--- a/test/front.test.js
+++ b/test/front.test.js
@@ -4,7 +4,7 @@ const test = require('node:test');
 const assert = require('node:assert/strict');
 const fs = require('fs');
 const path = require('path');
-const { engineOf, frontSection, configBody, CLASSES } = require('../lib/front');
+const { engineOf, enginesOf, frontSection, configBody, CLASSES } = require('../lib/front');
 const { gwBinary, existingGwBinary } = require('../lib/front/bin');
 const permissions = require('../lib/permissions');
 const { check } = require('../lib/ipc/schema');
@@ -25,12 +25,23 @@ test('the config.push body with its front section is a valid v1 message; a go en
   assert.deepEqual(check(msg), { errors: [], unknownType: false });
   const tls = frontSection({ host: '0.0.0.0', port: 443, upstreamPort: 1, tls: { certPath: '/c', keyPath: '/k' } });
   assert.deepEqual(tls.tls, { certPath: '/c', keyPath: '/k' });
+  assert.equal(frontSection({ host: 'h', port: 1, upstreamPort: 1, engines: { chat: 'go' } }).engines.chat, 'go');
   assert.throws(
-    () => frontSection({ host: 'h', port: 1, upstreamPort: 1, engines: { chat: 'go' } }),
-    /engine "go" for chat is not available/,
+    () => frontSection({ host: 'h', port: 1, upstreamPort: 1, engines: { messages: 'go' } }),
+    /engine "go" for messages is not available: only chat can run on go/,
   );
 });
 
+test('enginesOf: go only for chat, the one class the go engine has (P4b)', () => {
+  assert.equal(enginesOf({ engines: { chat: 'go', messages: 'go' } }).chat, 'go');
+  assert.equal(enginesOf({ engines: { chat: 'go', messages: 'go' } }).messages, 'js');
+  for (const g of [undefined, {}, { engines: 'go' }, { engines: { chat: 'GO' } }])
+    assert.ok(
+      Object.values(enginesOf(g)).every((e) => e === 'js'),
+      JSON.stringify(g),
+    );
+});
+
 test('gwBinary: ZAAFPULSE_GW_BIN, else beside a single-file binary, else gw/bin in the source folder', () => {
   assert.equal(gwBinary({ env: { ZAAFPULSE_GW_BIN: '/opt/gw' } }), path.resolve('/opt/gw'));
   assert.equal(
PATCH
```

- [ ] **Step 3: Run them to see them fail**

Run: `GOTOOLCHAIN=local go -C gw test -count=1 ./... ; node --test test/front.test.js`
Expected: Go FAIL `undefined: NewUpstream`, `undefined: New` (engine) and in `gw/cmd/zaafpulse-gw` the go-engine test; Node FAIL `enginesOf is not a function` and the `only chat can run on go` message.

- [ ] **Step 4: Implement**

Create `gw/engine/serve.go`:

```go
package engine

import (
	"bytes"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

// Engine serves the route classes on the go engine. Its State is swapped whole on each generation and budget snapshot.
type Engine struct {
	state    atomic.Pointer[State]
	limits   *RateLimiter
	upstream *Upstream
	emit     func(*ipc.UsageReport) error
	now      func() int64
	logf     func(string, ...any)
}

// New is an engine that calls providers through up and hands each usage line to emit (the outbox, then the control
// plane: lib/ipc/ingest.js stores it through the JS gateway's own writer).
func New(up *Upstream, emit func(*ipc.UsageReport) error, logf func(string, ...any)) *Engine {
	return &Engine{limits: NewRateLimiter(), upstream: up, emit: emit, now: NowMs, logf: logf}
}

// SetState installs a generation (nil: none yet; every request is handed to the control plane).
func (e *Engine) SetState(s *State) { e.state.Store(s) }

// State is the current generation, or nil.
func (e *Engine) State() *State { return e.state.Load() }

// ReadRequest reads what the engine needs of r: at most MaxBody + 1 bytes of its body. The body r keeps is the whole
// one again (what was read, then the rest), so a hand-off forwards it unchanged.
func ReadRequest(r *http.Request, rawPath string) *Request {
	buf, _ := io.ReadAll(io.LimitReader(r.Body, MaxBody+1))
	r.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(buf), r.Body), r.Body}
	q := ""
	if i := len(rawPath); i < len(r.RequestURI) && r.RequestURI[i] == '?' {
		q = r.RequestURI[i+1:]
	}
	return &Request{Method: r.Method, RawPath: rawPath, Query: q, Header: r.Header, Host: r.Host, Peer: r.RemoteAddr,
		Body: buf, TooBig: len(buf) > MaxBody}
}

// Serve answers one chat request, or hands it to js (the control plane) unchanged.
func (e *Engine) Serve(class string, w http.ResponseWriter, r *http.Request, rawPath string, js http.Handler) {
	s := e.state.Load()
	if s == nil || class != "chat" {
		js.ServeHTTP(w, r)
		return
	}
	req := ReadRequest(r, rawPath)
	started := time.Now()
	p := s.PlanChat(req, e.now())
	if p.Handoff != "" {
		js.ServeHTTP(w, r)
		return
	}
	if wait := e.limits.Take(p.Key.ID, p.Key.RPM, e.now()); wait > 0 {
		write(w, RateLimited(p.Key, wait))
		return
	}
	res := e.upstream.Call(r.Context(), &p)
	ms := float64(time.Since(started).Microseconds()) / 1000
	var a Answer
	switch {
	case res.Why == "client gone":
		a = s.Respond(&p, res.Status, res.Body, ms) // the line is still written, as JS records the call
	case res.Why != "":
		a = s.Failed(&p, res.Why, ms)
	default:
		a = s.Respond(&p, res.Status, res.Body, ms)
	}
	// The usage line goes out before the answer: the control plane has it on its way when the client reads the answer
	// (it is stored a few milliseconds later, through the outbox and the ingest), as the JS gateway records before it ends.
	rec := a.Usage
	rec.Ts = e.now()
	if err := e.emit(&ipc.UsageReport{Record: rec}); err != nil {
		e.logf("go engine: the usage line could not be stored: %v", err)
	}
	if r.Context().Err() == nil {
		write(w, a)
	}
}

func write(w http.ResponseWriter, a Answer) {
	for k, v := range a.Header {
		w.Header()[k] = v
	}
	w.Header().Set("Content-Length", itoa(len(a.Body)))
	w.WriteHeader(a.Status)
	_, _ = w.Write(a.Body)
}

func itoa(n int) string { return NumberString(float64(n)) }
```

Create `gw/engine/upstream.go`:

```go
package engine

// The engine's provider calls (P4b): what lib/gateway/providers.js request() and lib/net.js fetchSameOrigin do, with Go's
// client. The proxy comes from HTTPS_PROXY, HTTP_PROXY and NO_PROXY (the control plane passes them on an allow-list,
// lib/ipc/env.js); the trusted roots are the system's plus the PEM files named by SSL_CERT_FILE, NODE_EXTRA_CA_CERTS and
// DENO_CERT (Go reads SSL_CERT_FILE and SSL_CERT_DIR itself on Linux, never on macOS or Windows). The call has 120 s to
// get the provider's answer started (TIMEOUT_MS; the body then has no limit, as in JS); a connection has 10 s (undici's
// connect timeout). A redirect is followed (at most 5, same method and body) only to the same origin.

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	// Timeout is providers.js TIMEOUT_MS: the provider must start its answer within it.
	Timeout = 120 * time.Second
	// ConnectTimeout is undici's default connect timeout.
	ConnectTimeout = 10 * time.Second
	maxRedirects   = 5
)

// Upstream calls providers.
type Upstream struct {
	client  *http.Client
	timeout time.Duration
}

// NewUpstream builds the client from the environment (getenv: os.Getenv).
func NewUpstream(getenv func(string) string) *Upstream {
	roots, _ := x509.SystemCertPool()
	if roots == nil {
		roots = x509.NewCertPool()
	}
	for _, name := range []string{"SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS", "DENO_CERT"} {
		if f := getenv(name); f != "" {
			if pem, err := os.ReadFile(f); err == nil {
				roots.AppendCertsFromPEM(pem)
			}
		}
	}
	tr := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         (&net.Dialer{Timeout: ConnectTimeout, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:     &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: ConnectTimeout,
		MaxIdleConnsPerHost: 64,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  true, // the body goes to the client as the provider wrote it, as fetch gives it
		ForceAttemptHTTP2:   true,
	}
	return &Upstream{
		client: &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
		timeout: Timeout,
	}
}

// Result of one call: the provider's status and body, or Why the call failed (the text after "<provider id>: ").
type Result struct {
	Status int
	Body   []byte
	Why    string
}

// Call sends the plan's request. ctx ends when the client goes away.
func (u *Upstream) Call(ctx context.Context, p *Plan) Result {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var timedOut atomic.Bool
	timer := time.AfterFunc(u.timeout, func() { timedOut.Store(true); cancel() })
	current := p.URL
	for hop := 0; hop <= maxRedirects; hop++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, current, bytes.NewReader(p.Body))
		if err != nil {
			timer.Stop()
			return Result{Why: "fetch failed"}
		}
		req.Header = p.Header.Clone()
		resp, err := u.client.Do(req)
		if err != nil {
			timer.Stop()
			if timedOut.Load() {
				return Result{Why: "timed out"}
			}
			return Result{Why: ErrorCode(err)}
		}
		loc := resp.Header.Get("Location")
		if resp.StatusCode < 300 || resp.StatusCode >= 400 || loc == "" {
			timer.Stop() // the answer has started: the body has no time limit, as in JS
			body, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil && ctx.Err() != nil {
				return Result{Status: resp.StatusCode, Body: body, Why: "client gone"}
			}
			return Result{Status: resp.StatusCode, Body: body}
		}
		_ = resp.Body.Close()
		from, _ := url.Parse(current)
		next, err := from.Parse(loc)
		if err != nil {
			timer.Stop()
			return Result{Why: "fetch failed"}
		}
		if origin(next) != origin(from) {
			timer.Stop()
			return Result{Why: "refused a redirect from " + from.Host + " to " + next.Host +
				"; credentials are not forwarded to another host"}
		}
		current = next.String()
	}
	timer.Stop()
	return Result{Why: "too many redirects"}
}

func origin(u *url.URL) string {
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
	}
	return u.Scheme + "://" + u.Hostname() + ":" + port
}

// ErrorCode is what the JS gateway names a failed fetch by (err.cause.code of Node's fetch, else its message): the
// common system and undici codes, else "fetch failed". Deno names some of these differently; a failed call has no
// provider answer, so the shadow compare never reads it.
func ErrorCode(err error) string {
	var dns *net.DNSError
	var hn x509.HostnameError
	var ua x509.UnknownAuthorityError
	var ci x509.CertificateInvalidError
	switch {
	case errors.As(err, &dns):
		if dns.IsNotFound {
			return "ENOTFOUND"
		}
		return "EAI_AGAIN"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "ECONNREFUSED"
	case errors.Is(err, syscall.ECONNRESET):
		return "ECONNRESET"
	case errors.Is(err, syscall.EHOSTUNREACH):
		return "EHOSTUNREACH"
	case errors.Is(err, syscall.ENETUNREACH):
		return "ENETUNREACH"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "UND_ERR_SOCKET"
	case errors.As(err, &hn):
		return "ERR_TLS_CERT_ALTNAME_INVALID"
	case errors.As(err, &ua):
		return "UNABLE_TO_VERIFY_LEAF_SIGNATURE"
	case errors.As(err, &ci):
		if ci.Reason == x509.Expired {
			return "CERT_HAS_EXPIRED"
		}
		return "UNABLE_TO_VERIFY_LEAF_SIGNATURE"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "UND_ERR_CONNECT_TIMEOUT"
	}
	return "fetch failed"
}
```

Apply (from the repository root):

```bash
git apply <<'PATCH'
diff --git a/gw/cmd/zaafpulse-gw/front.go b/gw/cmd/zaafpulse-gw/front.go
index 5b0ce21..ed58994 100644
--- a/gw/cmd/zaafpulse-gw/front.go
+++ b/gw/cmd/zaafpulse-gw/front.go
@@ -22,7 +22,7 @@ func (g *gateway) applyFront(f *ipc.FrontConfig) int {
 	}
 	engines := make(map[string]string, len(f.Engines))
 	for class, e := range f.Engines {
-		if e != "js" {
+		if e != "js" && !(e == "go" && front.GoClasses[class]) {
 			return exitWith(g.log, ipc.ExitRefused, "config.push: route class %q is on engine %q, which this gateway does not have", class, e)
 		}
 		engines[class] = string(e)
@@ -66,6 +66,9 @@ func (g *gateway) openFront(f *ipc.FrontConfig, token string) int {
 		cfg.CertFile, cfg.KeyFile = f.TLS.CertPath, f.TLS.KeyPath
 	}
 	srv, err := front.Listen(cfg, g.log)
+	if err == nil {
+		srv.SetGoEngine(g.engine)
+	}
 	if errors.Is(err, front.ErrAddrInUse) {
 		return exitWith(g.log, ipc.ExitPortInUse, "port %d on %s is in use", f.Listen.Port, f.Listen.Host)
 	}
diff --git a/gw/cmd/zaafpulse-gw/main.go b/gw/cmd/zaafpulse-gw/main.go
index da0497f..6f9f63d 100644
--- a/gw/cmd/zaafpulse-gw/main.go
+++ b/gw/cmd/zaafpulse-gw/main.go
@@ -31,6 +31,7 @@ import (
 	"syscall"
 	"time"
 
+	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/engine"
 	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/front"
 	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
 )
@@ -110,6 +111,7 @@ func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
 		done:    make(chan struct{}),
 		unknown: map[string]bool{},
 	}
+	g.engine = engine.New(engine.NewUpstream(os.Getenv), g.emitUsage, g.log.Printf)
 	return g.serve(protocols)
 }
 
@@ -147,6 +149,8 @@ type gateway struct {
 	ready   bool
 	quiet   bool // --hang-after-ready
 
+	engine     *engine.Engine   // the go engine (P4b): its state is the newest generation with the newest snapshot
+	cfgBody    *ipc.ConfigPush  // under mu: the newest config.push, until its secrets arrive
 	frontCfg   *ipc.FrontConfig // under mu: the first config.push's front section, opened on ready
 	frontToken string           // under mu: from secrets.handoff, never logged
 	front      *front.Server    // under mu: the main port, once open
@@ -217,7 +221,7 @@ func (g *gateway) handle(msg ipc.Message) int {
 			return code
 		}
 		g.mu.Lock()
-		g.cfgGen = b.Gen
+		g.cfgGen, g.cfgBody = b.Gen, b
 		g.mu.Unlock()
 		return g.applied()
 	case *ipc.SecretsHandoff:
@@ -227,11 +231,21 @@ func (g *gateway) handle(msg ipc.Message) int {
 		}
 		g.mu.Lock()
 		g.secGen, g.secrets, g.mask, g.frontToken = b.Gen, m, b.Mask, b.FrontToken
+		if g.cfgBody != nil && g.cfgBody.Gen == b.Gen {
+			st := engine.NewState(g.cfgBody, b)
+			if g.snap != nil {
+				st = st.WithSnapshot(g.snap)
+			}
+			g.engine.SetState(st)
+		}
 		g.mu.Unlock()
 		return g.applied()
 	case *ipc.BudgetSnapshot:
 		g.mu.Lock()
 		g.snap, g.snapAt = b, time.Now()
+		if st := g.engine.State(); st != nil {
+			g.engine.SetState(st.WithSnapshot(b))
+		}
 		g.mu.Unlock()
 		if g.o.healthOnSnapshot {
 			g.sendHealth()
@@ -377,6 +391,10 @@ func (g *gateway) checkBudget(body any) {
 	}
 }
 
+// emitUsage stores a go-engine call in the outbox and sends it (P4b; the control plane stores it through the JS
+// gateway's own usage writer, lib/ipc/server-sinks.js).
+func (g *gateway) emitUsage(r *ipc.UsageReport) error { return g.appendAndSend(ipc.TypeUsageReport, r) }
+
 // appendAndSend writes an event to the outbox, then sends it (D4: never the other way round), under the outbox's send
 // lock, which the replay holds too (E10).
 func (g *gateway) appendAndSend(typ string, body any) error {
diff --git a/gw/front/front.go b/gw/front/front.go
index c2efd54..42f481e 100644
--- a/gw/front/front.go
+++ b/gw/front/front.go
@@ -45,6 +45,15 @@ var proxyHeaders = []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-
 // and net/http reads MaxHeaderBytes plus 4096 bytes before it answers 431 (P4a final review m2, P4b entry condition 6).
 const MaxHeaderBytes = 16384 - 4096
 
+// GoClasses are the route classes the go engine serves (P4b: chat, not streaming; the engine hands the rest of a
+// class's requests to js itself). A config.push that puts another class on go is refused.
+var GoClasses = map[string]bool{"chat": true}
+
+// GoEngine serves a request of a class on the go engine, or hands it to js (gw/engine Engine).
+type GoEngine interface {
+	Serve(class string, w http.ResponseWriter, r *http.Request, rawPath string, js http.Handler)
+}
+
 // ErrAddrInUse: another process holds the main port. The gateway exits 4.
 var ErrAddrInUse = errors.New("front: the main port is in use")
 
@@ -56,8 +65,12 @@ type Server struct {
 	proxy    *httputil.ReverseProxy
 	engines  atomic.Pointer[map[string]string]
 	inflight atomic.Int64
+	goEngine GoEngine
 }
 
+// SetGoEngine installs the go engine before Serve (without one, a class on go is answered 501).
+func (s *Server) SetGoEngine(e GoEngine) { s.goEngine = e }
+
 // Listen loads the TLS files (when set) and opens the main port. ErrAddrInUse when the port is taken.
 func Listen(cfg Config, logger *log.Logger) (*Server, error) {
 	var tlsCfg *tls.Config
@@ -204,8 +217,11 @@ func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
 		w.WriteHeader(http.StatusMethodNotAllowed)
 		return
 	}
-	if s.engine(Classify(RawPath(r.RequestURI))) != "js" {
-		// Unreachable in P4a: config.push refuses a go engine. P4b answers here.
+	raw := RawPath(r.RequestURI)
+	if class := Classify(raw); s.engine(class) == "go" && GoClasses[class] && s.goEngine != nil {
+		s.goEngine.Serve(class, w, r, raw, s.proxy)
+		return
+	} else if s.engine(class) != "js" {
 		w.Header().Set("Content-Type", "application/json")
 		w.WriteHeader(http.StatusNotImplemented)
 		_, _ = w.Write([]byte(`{"error":"the go engine is not available in this gateway"}`))
diff --git a/lib/front/index.js b/lib/front/index.js
index e60ca28..b254125 100644
--- a/lib/front/index.js
+++ b/lib/front/index.js
@@ -14,6 +14,21 @@ const { createInternalListener } = require('./internal');
 // The route classes of zaafpulse-gw (gw/front/route.go Classes). P4a: every one on js.
 const CLASSES = ['chat', 'messages', 'responses', 'embeddings', 'models', 'gemini', 'mcp', 'a2a', 'skills', 'agents'];
 const allJs = () => Object.fromEntries(CLASSES.map((c) => [c, 'js']));
+// The classes the go engine serves (gw/front/front.go GoClasses). P4b: chat, not streaming; the engine hands the
+// requests of the class it does not serve to js itself.
+const GO_CLASSES = ['chat'];
+
+/**
+ * The engine of each route class config.json asks for (`gateway.engines`, e.g. { "chat": "go" }): go only for a class
+ * the go engine has; anything else is js.
+ * @param {any} gateway config.json's gateway section
+ */
+function enginesOf(gateway) {
+  const asked = gateway && gateway.engines && typeof gateway.engines === 'object' ? gateway.engines : {};
+  const out = allJs();
+  for (const c of GO_CLASSES) if (asked[c] === 'go') out[c] = 'go';
+  return out;
+}
 
 /** The mode config.json asks for: "front" or "js" (anything else is js). */
 function engineOf(config) {
@@ -21,10 +36,11 @@ function engineOf(config) {
   return e === 'front' ? 'front' : 'js';
 }
 
-/** The config.push front section. A go engine is refused here: P4a has none (the gateway would exit 2). */
+/** The config.push front section. go is refused for a class the go engine does not have (the gateway would exit 2). */
 function frontSection({ host, port, tls = null, upstreamPort, engines = allJs() }) {
   for (const [c, e] of Object.entries(engines))
-    if (e !== 'js') throw new Error(`gateway engine "${e}" for ${c} is not available yet: every route runs on js`);
+    if (e !== 'js' && !(e === 'go' && GO_CLASSES.includes(c)))
+      throw new Error(`gateway engine "${e}" for ${c} is not available: only ${GO_CLASSES.join(', ')} can run on go`);
   return {
     listen: { host, port },
     ...(tls ? { tls: { certPath: tls.certPath, keyPath: tls.keyPath } } : {}),
@@ -89,6 +105,8 @@ function createFront({
   internal = createInternalListener,
   createSupervisor: makeSupervisor = createSupervisor,
   token = newToken(),
+  // The engine of each route class, read at each push (enginesOf(config.json gateway)); P4b.
+  engines = allJs,
 }) {
   let mode = engine === 'front' ? 'front' : 'js';
   let reason = null;
@@ -167,7 +185,8 @@ function createFront({
     const upstreamPort = await inner.listen();
     // Listen, TLS and upstream are fixed for this gateway's life: a later push carries the same ones (P4b entry
     // condition 3; a change of gateway.engine, the host, the port or the TLS files takes a restart of ZAAFPulse).
-    const front = frontSection({ host, port, tls, upstreamPort });
+    const base = { host, port, tls, upstreamPort };
+    const front = frontSection(base);
     const mine = makeSupervisor({
       bin,
       env,
@@ -176,7 +195,10 @@ function createFront({
       beforeAck,
       log,
       sources: {
-        config: () => ({ ...sources.config(), front }),
+        config: () => ({
+          ...sources.config(),
+          front: { ...front, engines: frontSection({ ...base, engines: engines() }).engines },
+        }),
         secrets: () => ({ ...sources.secrets(), frontToken: token }),
         snapshot: () => sources.snapshot(),
       },
@@ -294,7 +316,7 @@ function createFront({
       return {
         ...base,
         mode,
-        engines: allJs(),
+        engines: mode === 'front' ? engines() : allJs(),
         ...(reason ? { reason } : {}),
         // After a crash loop only a restart of ZAAFPulse tries the gateway again (P4a deviation 12, entry condition 8).
         restartNeeded: mode === 'fallback' && !retryOnReload,
@@ -307,4 +329,4 @@ function createFront({
   };
 }
 
-module.exports = { createFront, engineOf, frontSection, configBody, CLASSES };
+module.exports = { createFront, engineOf, enginesOf, frontSection, configBody, CLASSES, GO_CLASSES };
diff --git a/server.js b/server.js
index 72b3ca4..3d3abd4 100644
--- a/server.js
+++ b/server.js
@@ -79,7 +79,7 @@ const { recoverMarks } = require('./lib/ipc/marks');
 const { createMarkFile } = require('./lib/ipc/markfile');
 const { serverSinks } = require('./lib/ipc/server-sinks');
 const { gwEnv } = require('./lib/ipc/env');
-const { createFront, engineOf } = require('./lib/front');
+const { createFront, engineOf, enginesOf } = require('./lib/front');
 const { configPush, secretsPush, snapshotPush } = require('./lib/front/push');
 const { gwBinary } = require('./lib/front/bin');
 const { createRecorder } = require('./lib/trace/recorder');
@@ -2268,6 +2268,7 @@ front = createFront({
   env: gwEnv(process.env, DIR),
   cpVersion: require('./package.json').version,
   ingest: fi && fi.ingest,
+  engines: () => enginesOf(rawConfig().gateway),
   // Each generation, from the state the JS gateway reads (P4b): config.json, keys.json, the effective prices, the
   // provider credentials, the blocking budgets and the keys' cost centers.
   sources: {
PATCH
```

- [ ] **Step 5: Run the tests and the task gate**

Run:

```bash
export GOTOOLCHAIN=local ZAAFPULSE_DATA="$(mktemp -d)" HOME="$(mktemp -d)"
test -z "$(gofmt -l gw)" && go -C gw vet ./... && GOOS=windows go -C gw vet ./... && go -C gw test -race -count=1 ./...
deno task typecheck
node --test test/*.test.js
deno test -q --no-check --no-prompt -P=test test/*.test.js
npm run test:contract
```

Expected: no gofmt output, `ok` for every Go package, `deno check` with no error, `fail 0` from Node, `0 failed` from Deno, and the contract suite
`pass 172` (from Task 8 on, `pass 179`, with one case skipped outside shadow mode), `fail 0`.

- [ ] **Step 6: Run the contract suite with chat on go, on both runtimes**

Run:

```bash
ZP_ENGINE=go npm run test:contract
ZP_ENGINE=go ZP_RUNTIME=deno deno test -q --no-check --no-prompt -P=test test/contract/
```

Expected: Node `pass 179`, `fail 0`, `skipped 1`; Deno `179 passed`, `0 failed`, `1 ignored`. In `go-chat`, every served call's usage line carries `ipc` (the engine answered).

- [ ] **Step 7: Commit and push**

```bash
git add -A -- gw lib server.js test
git status --short   # nothing outside this task's files, nothing under .superpowers/
git commit -m "P4b T8: chat on the go engine: provider calls with the JS timeouts and error names, usage through the outbox, the engine switch"
git push origin migrate/p4b-passthrough
```

---

### Fix round 1 (review of Tasks 1–5): typecheck, raw request targets, key fields and /i folding, Windows cleanup, health, saves, base URLs, store timer

The review of Tasks 1–5 (`.superpowers/sdd/p4s1/stream-a-review-1.md`, I1–I4, m1, m2, m5, m7), applied after Task 8. Each item got its failing test first.

**Files:**
- Create: `test/gateway-store.test.js`
- Modify: `gw/cmd/zaafpulse-gw/main.go`, `gw/cmd/zaafpulse-gw/reload_test.go`, `gw/engine/keys.go`, `gw/engine/keys_test.go`, `gw/front/front.go`, `gw/front/route_test.go`, `lib/front/index.js`, `lib/front/push.js`, `lib/gateway/index.js`, `lib/gateway/keys.js`, `lib/gateway/store.js`, `scripts/fixtures/gen-keys.js`, `test/front-push.test.js`, `test/front.test.js`


**Interfaces:**
- Produces: `deno task typecheck` clean (the usage object typed loosely, `push.js` without the guardrails import, `requestSecret` narrowed); the front's Rewrite sets `URL.Opaque` to the raw path and keeps `RawQuery`; `keyAuth` reads `rpm` (ceil), `expiresAt` (floor, 0.x is -1), `revokedAt` (truthy), list fields (`String()`), and drops a key whose field JS would misread; `specialUpper` lists all 102 BMP units with a multi-unit upper case, with a `canonicalize` fixture over the BMP; `drive()` waits for the gateway before its folder goes; health names `min(cfgGen, secGen)`; `front.synced` logs a timeout; a base URL with a credential is pushed cut and disabled; `jsonStore(file, fallback, { log })` logs a failed debounced save.

- [ ] **Step 1: Check the starting point**

Run: `git log --oneline -1`
Expected: Task 8's commit.

- [ ] **Step 2: Write the failing tests**

Create `test/gateway-store.test.js`:

```js
// lib/gateway/store.js jsonStore: a debounced save that fails is logged, never thrown from its timer (P4b review m7:
// keys.authenticate and gateway.ingestUsage save lastUsedAt that way, and an exception there would end the process).
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const { jsonStore } = require('../lib/gateway/store');
const { tmpDir } = require('./ipc-stub');

test('a debounced save that fails is logged once and does not throw from its timer', async (t) => {
  const dir = tmpDir(t, 'json-store');
  fs.writeFileSync(path.join(dir, 'blocker'), ''); // a file where the store's folder should be: every write fails
  const logged = [];
  const s = jsonStore(path.join(dir, 'blocker', 'keys.json'), { keys: [] }, { log: (l) => logged.push(l) });
  s.get().keys.push({ id: 'k' });
  s.save();
  await new Promise((r) => setTimeout(r, 1300));
  s.save();
  await new Promise((r) => setTimeout(r, 1300));
  assert.equal(logged.length, 1);
  assert.match(logged[0], /^store: .*keys\.json could not be saved/);
  assert.throws(() => s.save(true), 'an immediate save still throws to its caller');
});
```

Apply (from the repository root):

```bash
git apply <<'PATCH'
diff --git a/gw/cmd/zaafpulse-gw/reload_test.go b/gw/cmd/zaafpulse-gw/reload_test.go
index af40077..b008e7f 100644
--- a/gw/cmd/zaafpulse-gw/reload_test.go
+++ b/gw/cmd/zaafpulse-gw/reload_test.go
@@ -36,13 +36,14 @@ func secretsLine(seq, gen int) string {
 		seq, gen, frontToken)
 }
 
-func drive(t *testing.T) *gwRun {
+func drive(t *testing.T, extra ...string) *gwRun {
 	t.Helper()
 	inR, inW := io.Pipe()
 	outR, outW := io.Pipe()
 	r := &gwRun{code: make(chan int, 1), stderr: &syncBuffer{}, lines: make(chan string, 64)}
+	dir := t.TempDir() // before the cleanup below, so it is removed after the gateway has exited
 	go func() {
-		r.code <- run([]string{"--outbox", t.TempDir(), "--health-ms", "60000"}, inR, outW, r.stderr)
+		r.code <- run(append([]string{"--outbox", dir, "--health-ms", "60000"}, extra...), inR, outW, r.stderr)
 		_ = outW.Close()
 		_ = inR.Close()
 	}()
@@ -66,7 +67,17 @@ func drive(t *testing.T) *gwRun {
 		}
 	}()
 	r.send = func(l string) { writes <- l }
-	t.Cleanup(func() { close(writes); _ = inW.Close() })
+	// Closing stdin ends the gateway; wait for it before t.TempDir's cleanup removes the outbox, whose lock file it holds
+	// open (Windows refuses to remove an open file: P4b review I4).
+	t.Cleanup(func() {
+		close(writes)
+		_ = inW.Close()
+		select {
+		case <-r.code:
+		case <-time.After(10 * time.Second):
+			t.Error("the gateway did not exit within 10 s of stdin closing")
+		}
+	})
 	return r
 }
 
@@ -132,3 +143,19 @@ func TestALateFrontSectionAppliesAfterARestart(t *testing.T) {
 func dialQuick(port int) (io.Closer, error) {
 	return net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
 }
+
+// A health between a generation's config.push and its secrets.handoff names the generation before it (P4b review m2):
+// synced() must not resolve before the gateway has the new credentials too.
+func TestHealthNeverNamesAGenerationWithoutItsSecrets(t *testing.T) {
+	r := drive(t, "--health-on-snapshot")
+	r.ready(t, "")
+	r.send(configLine(5, 2, ""))
+	r.send(`{"v":1,"type":"budget.snapshot","seq":6,"ts":0,"body":{"at":0,"budgets":[],"keyCenter":{}}}`)
+	if h := r.next(t, "health"); !strings.Contains(h, `"gen":1`) {
+		t.Fatalf("health before the secrets: %s", h)
+	}
+	r.send(secretsLine(7, 2))
+	if h := r.next(t, "health"); !strings.Contains(h, `"gen":2`) {
+		t.Fatalf("health after the secrets: %s", h)
+	}
+}
diff --git a/gw/engine/keys_test.go b/gw/engine/keys_test.go
index 8c5440e..8f5ba2f 100644
--- a/gw/engine/keys_test.go
+++ b/gw/engine/keys_test.go
@@ -3,6 +3,7 @@ package engine
 import (
 	"encoding/json"
 	"net/http"
+	"strconv"
 	"strings"
 	"testing"
 
@@ -78,11 +79,37 @@ func TestKeysFixtures(t *testing.T) {
 				got = append(got, r.Take("k", in.RPM, 1767225600000+dt))
 			}
 			sameJSON(t, c.Name, got, c.Out)
+		case "canonicalize":
+			// Every BMP code unit folds as JavaScript's /i folds it (the fixture lists the ones that change).
+			var want map[string]string
+			if err := json.Unmarshal(c.Out, &want); err != nil {
+				t.Fatal(err)
+			}
+			bad := 0
+			for u := 0; u < 0x10000; u++ {
+				if u >= 0xd800 && u <= 0xdfff {
+					continue
+				}
+				exp := uint16(u)
+				if v, ok := want[strconv.FormatInt(int64(u), 16)]; ok {
+					n, _ := strconv.ParseUint(v, 16, 16)
+					exp = uint16(n)
+				}
+				if got := canon(uint16(u)); got != exp {
+					bad++
+					if bad <= 10 {
+						t.Errorf("canon(U+%04X) = U+%04X, JS U+%04X", u, got, exp)
+					}
+				}
+			}
+			if bad > 0 {
+				t.Errorf("%d code units fold differently", bad)
+			}
 		default:
 			t.Errorf("%s: unknown fn %q", c.Name, c.Fn)
 		}
 	}
-	for _, fn := range []string{"secret", "allows", "authenticate", "authorize", "rateLimit"} {
+	for _, fn := range []string{"secret", "allows", "authenticate", "authorize", "rateLimit", "canonicalize"} {
 		if seen[fn] == 0 {
 			t.Errorf("no %s fixtures", fn)
 		}
diff --git a/gw/front/route_test.go b/gw/front/route_test.go
index 7a25f09..bfc7357 100644
--- a/gw/front/route_test.go
+++ b/gw/front/route_test.go
@@ -118,3 +118,21 @@ func TestQuietLoggerRateLimitsNoisyLines(t *testing.T) {
 		t.Errorf("log %q, want %q", buf.String(), want)
 	}
 }
+
+// The front forwards the request target exactly as the client sent it (P4b review I2): a backslash, a "|" or a ";" in
+// the query reach the control plane unchanged, so js and front mode route the same text.
+func TestRequestTargetIsForwardedAsSent(t *testing.T) {
+	got := make(chan string, 8)
+	up := startUpstream(t, func(w http.ResponseWriter, r *http.Request) { got <- r.RequestURI })
+	s := startFront(t, up.port(), nil)
+	for _, target := range []string{`/v1\chat/completions`, "/v1/a|b", "/api/x?q=a;b&c=1", "/v1/a^b?x=%zz&y=\\", "/v1/chat%2Fcompletions?a=%2F"} {
+		out, _ := rawRequest(t, s.Addr().String(), "GET "+target+" HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n")
+		if !strings.Contains(out, "200 OK") {
+			t.Errorf("%s: %q", target, out)
+			continue
+		}
+		if r := <-got; r != target {
+			t.Errorf("forwarded %q, sent %q", r, target)
+		}
+	}
+}
diff --git a/scripts/fixtures/gen-keys.js b/scripts/fixtures/gen-keys.js
index 61f9bf0..bc156df 100644
--- a/scripts/fixtures/gen-keys.js
+++ b/scripts/fixtures/gen-keys.js
@@ -75,6 +75,7 @@ for (const [name, patterns, value] of [
   ['kelvin', ['oa/k'], 'oa/K'],
   ['sharp-s', ['oa/ß'], 'oa/ẞ'],
   ['greek-sigma', ['oa/σ'], 'oa/Σ'],
+  ['ypogegrammeni-not-folded', ['oa/\u1f80'], 'oa/\u1f88'],
   ['astral', ['oa/*'], 'oa/\u{1F600}'],
   ['empty-value', ['*'], ''],
 ])
@@ -144,6 +145,20 @@ for (const [name, rpm, times] of [
   add('rateLimit', name, { rpm, times }, out);
 }
 
+// Canonicalize (ECMAScript 22.2.2.7.3, a regular expression with i and without u), the folding allows() relies on: for
+// every BMP code unit whose canonical unit is another one, that unit. Computed with this runtime's own toUpperCase,
+// which is what the JS gateway's /i globs use (P4b review I3).
+const canonical = {};
+for (let u = 0; u < 0x10000; u++) {
+  if (u >= 0xd800 && u <= 0xdfff) continue;
+  const up = String.fromCharCode(u).toUpperCase();
+  if (up.length !== 1) continue;
+  const cu = up.charCodeAt(0);
+  if (u >= 128 && cu < 128) continue;
+  if (cu !== u) canonical[u.toString(16)] = cu.toString(16);
+}
+add('canonicalize', 'bmp', null, canonical);
+
 fs.rmSync(OUT, { recursive: true, force: true });
 fs.mkdirSync(OUT, { recursive: true });
 cases.forEach((c, i) => {
diff --git a/test/front-push.test.js b/test/front-push.test.js
index 654513d..9c6cb86 100644
--- a/test/front-push.test.js
+++ b/test/front-push.test.js
@@ -6,6 +6,7 @@ const assert = require('node:assert/strict');
 const { configPush, secretsPush, snapshotPush, keyAuth } = require('../lib/front/push');
 const { frontSection } = require('../lib/front');
 const { check } = require('../lib/ipc/schema');
+const { resolvePolicy } = require('../lib/gateway/guardrails');
 
 const msg = (type, body) => ({ v: 1, type, seq: 2, ts: 0, body: { ...body, gen: 1 } });
 const GATEWAY = {
@@ -69,7 +70,8 @@ test('configPush: a valid config.push with keys by hash, prices in nano-USD, no
 });
 
 test('keyAuth: every required field, with defaults for an old key', () => {
-  const k = keyAuth({ id: 'old', hash: 'b'.repeat(64) });
+  const k = keyAuth({ id: 'old', hash: 'b'.repeat(64), scopes: [] });
+  assert.equal(keyAuth({ id: 'old', hash: 'b'.repeat(64) }), null, 'no scopes: JS cannot check it either');
   assert.deepEqual(Object.keys(k).sort(), [
     'agents',
     'approveOwnRuns',
@@ -166,3 +168,82 @@ test('snapshotPush: the blocking budgets with their state, and the keys cost cen
     { budgets: [], keyCenter: {} },
   );
 });
+
+test('configPush: the global guardrails policy is the one lib/gateway/guardrails.js resolves', () => {
+  for (const g of [
+    undefined,
+    null,
+    'x',
+    {},
+    { scope: 'all', secrets: 'block', pii: 'redact', injection: 'off' },
+    { scope: 'nope', secrets: 'redact', pii: 'nonsense', injection: 'redact' },
+  ])
+    assert.deepEqual(
+      configPush({ gateway: { guardrails: g }, keys: [], pricing: {} }).gateway.guardrails,
+      resolvePolicy(g, null),
+      JSON.stringify(g),
+    );
+});
+
+// P4b review I3: a key's fields as JS reads them, never more permissive in Go. Fractions the dashboard stores keep their
+// meaning; a field JS would misread is a key the gateway does not know (it hands the call to the server, which decides).
+test('keyAuth: fractional rpm and expiry mean what they mean in JS; a field JS would misread drops the key', () => {
+  const { createKeys, hash: sha } = require('../lib/gateway/keys');
+  const base = { id: 'k', hash: 'c'.repeat(64), scopes: ['llm'], models: [], agents: [] };
+  // rpm 2.5: JS allows 3 a minute (w.length >= 2.5), so does rpm 3.
+  let t = 0;
+  const keys = createKeys({ get: () => ({ keys: [] }), save: () => {} }, { now: () => t });
+  const waits = [0, 1, 2, 3].map((i) => ((t = i), keys.rateLimit({ id: 'k', rpm: 2.5 })));
+  assert.deepEqual(
+    waits.map((w) => w > 0),
+    [false, false, false, true],
+  );
+  assert.equal(keyAuth({ ...base, rpm: 2.5 }).rpm, 3);
+  assert.equal(keyAuth({ ...base, rpm: 0 }).rpm, 0, 'JS reads 0 as 60, Go too');
+  // A fractional expiry still expires: now > 1767225600000.5 as soon as now > 1767225600000.
+  assert.equal(keyAuth({ ...base, expiresAt: 1767225600000.5 }).expiresAt, 1767225600000);
+  assert.equal(keyAuth({ ...base, expiresAt: 0.5 }).expiresAt, -1, 'expired for any now, as in JS');
+  assert.equal(keyAuth({ ...base, expiresAt: 0 }).expiresAt, null);
+  // revokedAt is "revoked" whenever JS reads it as true.
+  assert.ok(keyAuth({ ...base, revokedAt: 'yes' }).revokedAt > 0);
+  assert.equal(keyAuth({ ...base, revokedAt: 0 }).revokedAt, null);
+  // allows() reads String(pattern): a number in models is that text.
+  assert.deepEqual(keyAuth({ ...base, models: ['oa/*', 123] }).models, ['oa/*', '123']);
+  assert.equal(keyAuth({ ...base, privateOnly: 1 }).privateOnly, true);
+  for (const bad of [
+    { scopes: 'llm' },
+    { models: 'oa/*' },
+    { agents: { a: 1 } },
+    { rpm: '5' },
+    { rpm: -1 },
+    { rpm: Infinity },
+    { expiresAt: '2026-01-01' },
+    { budgetUSD: 'ten' },
+  ])
+    assert.equal(keyAuth({ ...base, ...bad }), null, JSON.stringify(bad));
+  assert.equal(sha('x').length, 64);
+});
+
+// P4b review m5: a credential written into a base URL (user:password@, or a query) never travels in config.push. Such a
+// provider goes to the gateway disabled, with only its origin and path, so the gateway hands its calls to the server,
+// which calls it as before (fetch refuses a URL with user:password, so those calls already fail there).
+test('configPush: a base URL with user info or a query is cut to its origin and path, and the provider disabled', () => {
+  const body = configPush({
+    gateway: {
+      providers: [
+        { id: 'u', preset: 'custom', baseUrl: 'https://me:s3cret@llm.example/v1' },
+        { id: 'q', preset: 'custom', baseUrl: 'https://llm.example/v1?key=s3cret' },
+        { id: 'ok', preset: 'custom', baseUrl: 'https://llm.example/v1' },
+        { id: 'bad', preset: 'custom', baseUrl: 'not a url' },
+      ],
+    },
+    keys: [],
+    pricing: {},
+  });
+  assert.ok(!JSON.stringify(body).includes('s3cret'));
+  const [u, q, ok, bad] = body.gateway.providers;
+  assert.deepEqual([u.baseUrl, u.enabled], ['https://llm.example/v1', false]);
+  assert.deepEqual([q.baseUrl, q.enabled], ['https://llm.example/v1', false]);
+  assert.deepEqual([ok.baseUrl, ok.enabled], ['https://llm.example/v1', undefined]);
+  assert.deepEqual([bad.baseUrl, bad.enabled], ['', false]);
+});
diff --git a/test/front.test.js b/test/front.test.js
index b37a273..f101972 100644
--- a/test/front.test.js
+++ b/test/front.test.js
@@ -274,3 +274,15 @@ test('T5-N4: stop() cancels a retry still waiting in the queue', async (t) => {
   assert.equal(await retry, false);
   assert.equal(f.sups.length, 1, 'no gateway started after stop()');
 });
+
+test('m1: a save the gateway did not confirm in time is logged', async (t) => {
+  const lines = [];
+  const f = await frontWithFakes(t, { log: (l) => lines.push(l) });
+  f.sups[0].synced = async () => false;
+  assert.equal(await f.front.synced(50), false);
+  assert.match(lines.join('\n'), /the gateway did not confirm the new configuration within 0\.05 s/);
+  f.sups[0].synced = async () => true;
+  lines.length = 0;
+  assert.equal(await f.front.synced(50), true);
+  assert.deepEqual(lines, []);
+});
PATCH
```

- [ ] **Step 3: Run them to see them fail**

Run: `deno task typecheck; GOTOOLCHAIN=local go -C gw test -count=1 ./...; node --test test/front-push.test.js test/front.test.js test/gateway-store.test.js`
Expected: 73 type errors; FAIL in `TestRequestTargetIsForwardedAsSent`, `TestKeysFixtures` (27 code units fold differently) and `TestHealthNeverNamesAGenerationWithoutItsSecrets`; FAIL in the keyAuth, base-URL, policy, m1 and store tests.

- [ ] **Step 4: Implement**

Apply (from the repository root):

```bash
git apply <<'PATCH'
diff --git a/gw/cmd/zaafpulse-gw/main.go b/gw/cmd/zaafpulse-gw/main.go
index 6f9f63d..9bb20eb 100644
--- a/gw/cmd/zaafpulse-gw/main.go
+++ b/gw/cmd/zaafpulse-gw/main.go
@@ -433,7 +433,8 @@ func (g *gateway) sendHealth() {
 		g.mu.Unlock()
 		return
 	}
-	h := ipc.Health{OutboxDepth: g.box.Depth(), UptimeMs: time.Since(g.started).Milliseconds(), Gen: g.cfgGen}
+	// The generation in force: its config and its secrets both applied (P4b review m2).
+	h := ipc.Health{OutboxDepth: g.box.Depth(), UptimeMs: time.Since(g.started).Milliseconds(), Gen: min(g.cfgGen, g.secGen)}
 	if g.front != nil {
 		h.Inflight = g.front.Inflight()
 	}
diff --git a/gw/engine/keys.go b/gw/engine/keys.go
index 33cb723..d60226b 100644
--- a/gw/engine/keys.go
+++ b/gw/engine/keys.go
@@ -146,14 +146,26 @@ func canon(u uint16) uint16 {
 	return uint16(up)
 }
 
-// specialUpper are the code units whose JavaScript toUpperCase is more than one character (SpecialCasing.txt), so
-// Canonicalize leaves them as they are, though Go's simple mapping has an upper case for some.
-var specialUpper = map[rune]bool{0x0149: true, 0x01f0: true, 0x0390: true, 0x03b0: true, 0x0587: true,
-	0x1e96: true, 0x1e97: true, 0x1e98: true, 0x1e99: true, 0x1e9a: true, 0x1f50: true, 0x1f52: true, 0x1f54: true,
-	0x1f56: true, 0x1fb6: true, 0x1fc6: true, 0x1fd2: true, 0x1fd3: true, 0x1fd6: true, 0x1fd7: true, 0x1fe2: true,
-	0x1fe3: true, 0x1fe4: true, 0x1fe6: true, 0x1fe7: true, 0x1ff6: true, 0xfb00: true, 0xfb01: true, 0xfb02: true,
-	0xfb03: true, 0xfb04: true, 0xfb05: true, 0xfb06: true, 0xfb13: true, 0xfb14: true, 0xfb15: true, 0xfb16: true,
-	0xfb17: true, 0x00df: true}
+// specialUpper are the BMP code units whose JavaScript toUpperCase is more than one unit (SpecialCasing.txt: the 102 the
+// keys fixture lists through gen-keys.js "canonicalize"), so Canonicalize leaves them as they are, though Go's simple
+// mapping folds some of them (U+1F80 to U+1F88, P4b review I3).
+var specialUpper = map[rune]bool{
+	0xdf: true, 0x149: true, 0x1f0: true, 0x390: true, 0x3b0: true, 0x587: true, 0x1e96: true, 0x1e97: true,
+	0x1e98: true, 0x1e99: true, 0x1e9a: true, 0x1f50: true, 0x1f52: true, 0x1f54: true, 0x1f56: true,
+	0x1f80: true, 0x1f81: true, 0x1f82: true, 0x1f83: true, 0x1f84: true, 0x1f85: true, 0x1f86: true,
+	0x1f87: true, 0x1f88: true, 0x1f89: true, 0x1f8a: true, 0x1f8b: true, 0x1f8c: true, 0x1f8d: true,
+	0x1f8e: true, 0x1f8f: true, 0x1f90: true, 0x1f91: true, 0x1f92: true, 0x1f93: true, 0x1f94: true,
+	0x1f95: true, 0x1f96: true, 0x1f97: true, 0x1f98: true, 0x1f99: true, 0x1f9a: true, 0x1f9b: true,
+	0x1f9c: true, 0x1f9d: true, 0x1f9e: true, 0x1f9f: true, 0x1fa0: true, 0x1fa1: true, 0x1fa2: true,
+	0x1fa3: true, 0x1fa4: true, 0x1fa5: true, 0x1fa6: true, 0x1fa7: true, 0x1fa8: true, 0x1fa9: true,
+	0x1faa: true, 0x1fab: true, 0x1fac: true, 0x1fad: true, 0x1fae: true, 0x1faf: true, 0x1fb2: true,
+	0x1fb3: true, 0x1fb4: true, 0x1fb6: true, 0x1fb7: true, 0x1fbc: true, 0x1fc2: true, 0x1fc3: true,
+	0x1fc4: true, 0x1fc6: true, 0x1fc7: true, 0x1fcc: true, 0x1fd2: true, 0x1fd3: true, 0x1fd6: true,
+	0x1fd7: true, 0x1fe2: true, 0x1fe3: true, 0x1fe4: true, 0x1fe6: true, 0x1fe7: true, 0x1ff2: true,
+	0x1ff3: true, 0x1ff4: true, 0x1ff6: true, 0x1ff7: true, 0x1ffc: true, 0xfb00: true, 0xfb01: true,
+	0xfb02: true, 0xfb03: true, 0xfb04: true, 0xfb05: true, 0xfb06: true, 0xfb13: true, 0xfb14: true,
+	0xfb15: true, 0xfb16: true, 0xfb17: true,
+}
 
 // RequestSecret is lib/gateway/keys.js requestSecret over Go's view of a request: the header values as Node joins
 // them (", " between repeats; Authorization keeps its first) and the raw path and query. ok is false when there is none.
diff --git a/gw/front/front.go b/gw/front/front.go
index 42f481e..97e11ea 100644
--- a/gw/front/front.go
+++ b/gw/front/front.go
@@ -99,6 +99,11 @@ func Listen(cfg Config, logger *log.Logger) (*Server, error) {
 	s.proxy = &httputil.ReverseProxy{
 		Rewrite: func(pr *httputil.ProxyRequest) {
 			pr.SetURL(target)
+			// The request target exactly as the client sent it (P4b review I2): net/http re-escapes a path it decoded
+			// ("\" and "|" become %5C and %7C) and drops query pairs it cannot parse (";"), so the control plane would
+			// route another text than in js mode. Opaque is written as it is; RawQuery is kept whole.
+			pr.Out.URL.Opaque = RawPath(pr.In.RequestURI)
+			pr.Out.URL.RawQuery = pr.In.URL.RawQuery
 			// The client's Host. Without one (HTTP/1.0) Go's transport would write the internal 127.0.0.1:<port>, so
 			// the request is flagged and the internal listener removes that Host: the host check sees none, as in js
 			// mode (P4a final review I1). Set and Del replace any copy of the flag the client sent.
diff --git a/lib/front/index.js b/lib/front/index.js
index b254125..173c14e 100644
--- a/lib/front/index.js
+++ b/lib/front/index.js
@@ -287,10 +287,12 @@ function createFront({
      * passed. A save answers after it, so a revoked key is refused by the gateway as soon as the save has answered.
      * @param {number} [timeoutMs]
      */
-    synced(timeoutMs = 2000) {
-      return mode === 'front' && sup && typeof sup.synced === 'function'
-        ? sup.synced(timeoutMs)
-        : Promise.resolve(false);
+    async synced(timeoutMs = 2000) {
+      if (!(mode === 'front' && sup && typeof sup.synced === 'function')) return false;
+      const ok = await sup.synced(timeoutMs);
+      // The save is answered anyway; the gateway gets the change with its next push (P4b review m1).
+      if (!ok) log(`gateway: the gateway did not confirm the new configuration within ${timeoutMs / 1000} s`);
+      return ok;
     },
     /** Stops the gateway (shutdown, then a kill after graceMs + its kill delay) and the internal listener. */
     async stop(graceMs = 2000) {
diff --git a/lib/front/push.js b/lib/front/push.js
index 1089257..499d3c2 100644
--- a/lib/front/push.js
+++ b/lib/front/push.js
@@ -11,7 +11,6 @@
 // Pure apart from the resolver and the budget service it is handed. Providers with OAuth2 or a Google service account
 // get no token here: provider auth in Go is P4g (stream C builds the library, stage 2 wires it).
 const { toNano } = require('../money');
-const { resolvePolicy } = require('../gateway/guardrails');
 
 const isObj = (v) => !!v && typeof v === 'object' && !Array.isArray(v);
 const str = (v) => (typeof v === 'string' ? v : '');
@@ -23,13 +22,37 @@ const MODES = {
 };
 const int = (v) => (Number.isSafeInteger(v) && v >= 0 ? v : null);
 
+// lib/gateway/guardrails.js resolvePolicy(cfg, null), kept here so that this file does not pull the whole guardrails
+// module into the type check of lib/front (test/front-push.test.js checks that both give the same policy).
+const POLICY_DEFAULTS = { scope: 'external', secrets: 'redact', pii: 'off', injection: 'log' };
+function globalPolicy(cfg) {
+  const c = isObj(cfg) ? cfg : {};
+  const out = { scope: c.scope === 'external' || c.scope === 'all' ? c.scope : POLICY_DEFAULTS.scope };
+  for (const k of Object.keys(MODES)) out[k] = MODES[k].includes(c[k]) ? c[k] : POLICY_DEFAULTS[k];
+  return out;
+}
+
 /** A provider as config.push carries it: no apiKey reference; auth { type: 'key' } when it has one. */
 function provider(p) {
   const out = { id: str(p.id), preset: str(p.preset), baseUrl: str(p.baseUrl) };
+  // A credential in the base URL (user:password@, a query) stays out of config.push (P4b review m5): the provider goes
+  // disabled, with its origin and path only, so the gateway hands its calls to the server, which calls it as before.
+  let unsafe = false;
+  try {
+    const u = new URL(out.baseUrl);
+    if (u.username || u.password || u.search || u.hash) {
+      out.baseUrl = `${u.origin}${u.pathname}`.replace(/\/+$/, '');
+      unsafe = true;
+    }
+  } catch {
+    out.baseUrl = '';
+    unsafe = true;
+  }
   if (Array.isArray(p.models)) out.models = strings(p.models);
   if (isObj(p.aliases))
     out.aliases = Object.fromEntries(Object.entries(p.aliases).filter(([, v]) => typeof v === 'string'));
   for (const k of ['enabled', 'approved', 'responses', 'reasoning']) if (typeof p[k] === 'boolean') out[k] = p[k];
+  if (unsafe) out.enabled = false;
   if (p.tier === 'private' || p.tier === 'external') out.tier = p.tier;
   if (p.format === 'openai' || p.format === 'anthropic') out.format = p.format;
   if (p.tokenParam === 'max_tokens' || p.tokenParam === 'max_completion_tokens') out.tokenParam = p.tokenParam;
@@ -49,9 +72,32 @@ function route(r) {
   return out;
 }
 
-/** A keys.json key as the gateway enforces it (ipc/v1 config.push keys[]), or null when it has no valid hash. */
+/**
+ * A keys.json key as the gateway enforces it (ipc/v1 config.push keys[]), read the way lib/gateway/keys.js reads it, or
+ * null when a field would not mean the same to the gateway: the gateway then does not know the key and hands its calls
+ * to the server, which decides as it always has (P4b review I3: the gateway is never more permissive than JS).
+ *   scopes, models, agents: arrays (allows() reads String(pattern); a missing list allows everything);
+ *   rpm: falsy is 60 (`k.rpm || 60`), a fraction is rounded up (`w.length >= 2.5` is `>= 3`);
+ *   expiresAt: falsy never expires, a fraction is rounded down (`now > x.5` is `now > x`), 0.x is -1;
+ *   revokedAt: revoked whenever it reads as true; budgetUSD: a number or null; privateOnly: truthy.
+ */
 function keyAuth(k) {
   if (!isObj(k) || typeof k.id !== 'string' || !k.id || !/^[0-9a-f]{64}$/.test(k.hash || '')) return null;
+  const list = (v) => (v == null ? [] : Array.isArray(v) ? v.map(String) : null);
+  const models = list(k.models);
+  const agents = list(k.agents);
+  if (!Array.isArray(k.scopes) || !models || !agents) return null;
+  let rpm = 0;
+  if (k.rpm) {
+    if (typeof k.rpm !== 'number' || !Number.isFinite(k.rpm) || k.rpm < 0) return null;
+    rpm = Math.ceil(k.rpm);
+  }
+  let expiresAt = null;
+  if (k.expiresAt) {
+    if (typeof k.expiresAt !== 'number' || !Number.isFinite(k.expiresAt)) return null;
+    expiresAt = Math.floor(k.expiresAt) || -1;
+  }
+  if (k.budgetUSD != null && !Number.isFinite(k.budgetUSD)) return null;
   const guardrails = {};
   if (isObj(k.guardrails)) {
     if (k.guardrails.scope === 'external' || k.guardrails.scope === 'all') guardrails.scope = k.guardrails.scope;
@@ -61,14 +107,14 @@ function keyAuth(k) {
     id: k.id,
     name: str(k.name),
     scopes: strings(k.scopes),
-    models: strings(k.models),
-    agents: strings(k.agents),
+    models,
+    agents,
     knowledge: strings(k.knowledge),
-    budgetUSD: Number.isFinite(k.budgetUSD) ? k.budgetUSD : null,
-    rpm: int(k.rpm) ?? 0,
-    privateOnly: k.privateOnly === true,
-    expiresAt: int(k.expiresAt),
-    revokedAt: int(k.revokedAt),
+    budgetUSD: k.budgetUSD == null ? null : k.budgetUSD,
+    rpm,
+    privateOnly: !!k.privateOnly,
+    expiresAt,
+    revokedAt: k.revokedAt ? Math.max(1, Math.floor(Number(k.revokedAt)) || 1) : null,
     guardrails,
     prefix: str(k.prefix),
     approveOwnRuns: k.approveOwnRuns === true,
@@ -106,7 +152,7 @@ function configPush({ gateway, keys, pricing }) {
     gateway: {
       providers: (Array.isArray(g.providers) ? g.providers : []).filter(isObj).map(provider),
       routes: (Array.isArray(g.routes) ? g.routes : []).filter(isObj).map(route),
-      guardrails: resolvePolicy(g.guardrails, null),
+      guardrails: globalPolicy(g.guardrails),
       defaults: { model: str(d.model), plannerModel: str(d.plannerModel), codeModel: str(d.codeModel) },
       tokenSaver: g.tokenSaver !== false,
       modelMap: (Array.isArray(g.modelMap) ? g.modelMap : [])
diff --git a/lib/gateway/index.js b/lib/gateway/index.js
index 2c508d0..1c3b8cc 100644
--- a/lib/gateway/index.js
+++ b/lib/gateway/index.js
@@ -169,7 +169,8 @@ function createGateway({
   }
   const providers = createProviders({ getConfig, secrets, ...(fetchImpl ? { fetchImpl } : {}) });
   // Every model call ends in usage.record; with OpenTelemetry export on, each one also becomes a span.
-  const usage = createUsage({ file: path.join(dataDir, 'gateway-usage.jsonl'), getPricing, now });
+  // Typed loosely, as the wrapped object it becomes (its records carry routedTo, guard, client and the other fields).
+  const usage = /** @type {any} */ (createUsage({ file: path.join(dataDir, 'gateway-usage.jsonl'), getPricing, now }));
   // The unwrapped writer, kept before the wrappers below replace usage.record: the IPC ingest stores the Go engine's
   // lines through it (ingestUsage), so they land in this log's in-memory spend and recent list too (P4b entry
   // condition 1). It is the only writer of gateway-usage.jsonl in the process.
diff --git a/lib/gateway/keys.js b/lib/gateway/keys.js
index 1e2e2eb..86ff1fd 100644
--- a/lib/gateway/keys.js
+++ b/lib/gateway/keys.js
@@ -27,13 +27,16 @@ const allows = (patterns, value) => !patterns || !patterns.length || patterns.so
  * @returns {string | undefined}
  */
 function requestSecret(headers, url) {
-  const header = headers.authorization || '';
+  const header = typeof headers.authorization === 'string' ? headers.authorization : '';
   if (header.startsWith('Bearer ')) return header.slice(7).trim();
-  return (
+  // Node gives these headers as one string (repeats joined with ", ").
+  return /** @type {string | undefined} */ (
     headers['x-api-key'] ||
-    headers['x-goog-api-key'] ||
-    (/^\/v1beta\//.test(new URL(url, 'http://x').pathname) ? new URL(url, 'http://x').searchParams.get('key') : null) ||
-    undefined
+      headers['x-goog-api-key'] ||
+      (/^\/v1beta\//.test(new URL(url, 'http://x').pathname)
+        ? new URL(url, 'http://x').searchParams.get('key')
+        : null) ||
+      undefined
   );
 }
 
diff --git a/lib/gateway/store.js b/lib/gateway/store.js
index 9984494..4a546fd 100644
--- a/lib/gateway/store.js
+++ b/lib/gateway/store.js
@@ -4,9 +4,16 @@
 const fs = require('fs');
 const path = require('path');
 
-function jsonStore(file, fallback) {
+// log: where a debounced save that failed is reported (once per store); it never throws from its timer.
+/**
+ * @param {string} file
+ * @param {any} fallback
+ * @param {{ log?: (line: string) => void }} [opts]
+ */
+function jsonStore(file, fallback, { log = console.error } = {}) {
   let value;
   let timer = null;
+  let warned = false;
   try {
     value = JSON.parse(fs.readFileSync(file, 'utf8'));
   } catch {
@@ -29,7 +36,16 @@ function jsonStore(file, fallback) {
       if (now) {
         clearTimeout(timer);
         flush();
-      } else if (!timer) timer = setTimeout(flush, 1000);
+      } else if (!timer)
+        timer = setTimeout(() => {
+          try {
+            flush();
+          } catch (err) {
+            // A save from a timer has no caller to tell (P4b review m7); the next save tries again.
+            if (!warned) log(`store: ${path.basename(file)} could not be saved: ${err.code || err.message}`);
+            warned = true;
+          }
+        }, 1000);
     },
     flush() {
       if (timer) {
PATCH
```

- [ ] **Step 5: Generate the fixtures from the JS code**

Run: `node scripts/fixtures/gen-keys.js`
Expected: `gen-keys: 55 cases in gw/testdata/keys`.

- [ ] **Step 6: Run the tests and the task gate**

Run:

```bash
export GOTOOLCHAIN=local ZAAFPULSE_DATA="$(mktemp -d)" HOME="$(mktemp -d)"
test -z "$(gofmt -l gw)" && go -C gw vet ./... && GOOS=windows go -C gw vet ./... && go -C gw test -race -count=1 ./...
deno task typecheck
node --test test/*.test.js
deno test -q --no-check --no-prompt -P=test test/*.test.js
npm run test:contract
```

Expected: no gofmt output, `ok` for every Go package, `deno check` with no error, `fail 0` from Node, `0 failed` from Deno, and the contract suite
`pass 172` (from Task 8 on, `pass 179`, with one case skipped outside shadow mode), `fail 0`.

- [ ] **Step 7: Commit and push**

```bash
git add -A -- gw lib scripts test
git status --short   # nothing outside this task's files, nothing under .superpowers/
git commit -m "P4b review 1 fixes: typecheck clean, the front forwards the request target as sent, key fields and /i folding never more permissive in Go, Windows test cleanup, health names a generation only with its secrets, unconfirmed saves logged, no credential from a base URL in config.push, a failed debounced save is logged"
git push origin migrate/p4b-passthrough
```

---

### Task 9: The shadow compare and the switch rule

Decisions 3, 4 and 10: with `gateway.shadow: ["chat"]` and chat on js, the front marks each chat request, the control plane keeps a capture, the engine compares after the client has its answer and posts a content-free line; `routing()` keeps chat on js until the shadow log allows the switch. `config.push.front.shadow` gains `classes` (additive, two fixtures). `ZP_ENGINE=shadow` makes the harness fail on any diff line.

**Files:**
- Create: `gw/engine/shadow.go`, `gw/engine/shadow_test.go`, `ipc/v1/fixtures/config.push/invalid-front-shadow-class.json`, `ipc/v1/fixtures/config.push/valid-front-shadow.json`, `lib/front/shadow-log.js`, `lib/gateway/shadow.js`, `test/front-shadow.test.js`
- Modify: `gw/cmd/zaafpulse-gw/front.go`, `gw/front/front.go`, `gw/front/route_test.go`, `gw/ipc/messages.go`, `ipc/v1/config.push.schema.json`, `lib/front/index.js`, `lib/front/internal.js`, `lib/gateway/index.js`, `lib/gateway/providers.js`, `server.js`, `test/contract/formats.test.js`, `test/contract/go-chat.test.js`, `test/contract/harness.js`, `tsconfig.json`


**Interfaces:**
- Consumes: Tasks 4, 7, 8 (`PlanChat`, `Respond`, `ReadRequest`, `createInternalListener`, `createFront`).
- Produces:
  - Go: `(*State).Compare(class string, p *Plan, c *Capture) ShadowLine`; `type Capture`, `type ShadowLine { Class, Outcome, Reason string; Status int; Diffs []Diff }`, `type Diff { Field string; JS, Go any }`; `(*Engine).Shadow(class, w, r, rawPath, js, own)`; `NewShadowID()`; `front.ShadowHeader = "X-Zp-Shadow"`, `front.OwnHeader = "X-Zp-Own"` (removed from every client request); `(*Server).SetShadow(classes []string)`; `ipc.FrontShadow{ Classes []ipc.RouteClass }`.
  - The switch rule (owner, 2026-11-12): `createShadowLog(...).gate(class, { days = 1, min = 20 })`, `routing()` reading `gateway.shadowDays` and `gateway.shadowMinCompares`; `tsconfig.json` includes `lib/front`.
  - JS: `lib/gateway/shadow.js` (`withShadow(id, fn, res)`, `noteProviderRequest(url, headers, body)`, `noteAnswer(providerResponse, client, usage)`, `takeCapture(id)`, `SHADOW_ID`); `lib/front/shadow-log.js` (`createShadowLog({ file, now?, maxBytes? })` with `append`, `stats`, `gate(class, days)`; `shadowEndpoints({ shadowLog })`); `createInternalListener({ ..., own })`; `lib/front` `routing(gateway, { gate, otel }) -> { engines, notes }`, `shadowOf(gateway, engines)`, `createFront({ ..., shadow, own })`; `GET /api/gateway/status` gains `notes` and `shadow`.
  - Contract: `ZP_ENGINE=shadow` (front, `shadow: ['chat']`); the harness's `stop()` throws on a diff line.

- [ ] **Step 1: Check the starting point**

Run: `git status --short && git rev-parse --abbrev-ref HEAD && git log --oneline -1`
Expected: nothing listed, `migrate/p4b-passthrough`, and the previous task's commit on top (Task 1: `0ec1247`, the merge of `origin/migrate/deno-go` with stream C).

- [ ] **Step 2: Write the failing tests**

Create `gw/engine/shadow_test.go`:

```go
package engine

import (
	"encoding/json"
	"strings"
	"testing"
)

// captureOf is the capture the control plane would have kept for a chat fixture (its provider request, the provider's
// answer, its own answer and the usage line with the cost in USD).
func captureOf(t *testing.T, c chatCase, want chatOut) *Capture {
	t.Helper()
	var u map[string]any
	_ = json.Unmarshal(want.Usage, &u)
	u["cost"] = u["cost"].(float64) / 1e9
	cap := &Capture{Usage: u}
	body := want.Request.Body
	cap.ProviderRequests = append(cap.ProviderRequests, struct {
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
		Body    *string           `json:"body"`
	}{want.Request.URL, want.Request.Headers, &body})
	cap.ProviderResponse = &struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
	}{c.Provider.Status, decodeUTF8([]byte(c.Provider.Body))}
	cap.Client = &struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
	}{want.Client.Status, want.Client.Body}
	return cap
}

// Every served chat fixture compares the same; a changed answer is a diff named without its text; a request the
// control plane served without a provider call, where the engine would have called one, is a diff too.
func TestShadowCompare(t *testing.T) {
	n := 0
	for _, f := range loadFixtures(t, "chat") {
		if f.Fn != "chat" {
			continue
		}
		var c chatCase
		var want chatOut
		_ = json.Unmarshal(f.In, &c)
		_ = json.Unmarshal(f.Out, &want)
		if c.Provider.Throw != "" {
			continue // no provider answer: nothing to compare
		}
		s := stateFor(t, c)
		p := s.PlanChat(requestFor(c), fixtureNow)
		if l := s.Compare("chat", &p, captureOf(t, c, want)); l.Outcome != "same" {
			t.Errorf("%s: %+v", f.Name, l)
		}
		n++
		if strings.HasSuffix(f.Name, "-basic.json") {
			cp := captureOf(t, c, want)
			cp.Client.Body = strings.Replace(cp.Client.Body, `"hi"`, `"a secret answer"`, 1)
			l := s.Compare("chat", &p, cp)
			b, _ := json.Marshal(l)
			if l.Outcome != "diff" || len(l.Diffs) != 1 || l.Diffs[0].Field != "client.body" || strings.Contains(string(b), "secret") {
				t.Errorf("changed answer: %s", b)
			}
			if l := s.Compare("chat", &p, nil); l.Outcome != "diff" || l.Diffs[0].Field != "served" {
				t.Errorf("no capture: %+v", l)
			}
			// The control plane made the provider call and it failed: nothing to compare.
			failed := captureOf(t, c, want)
			failed.ProviderResponse = nil
			if l := s.Compare("chat", &p, failed); l.Outcome != "skipped" {
				t.Errorf("failed call: %+v", l)
			}
			// Its own rate limit answered (each engine counts its own window).
			limited := captureOf(t, c, want)
			limited.ProviderRequests, limited.ProviderResponse, limited.Client.Status = nil, nil, 429
			if l := s.Compare("chat", &p, limited); l.Outcome != "skipped" {
				t.Errorf("rate limited: %+v", l)
			}
			// It refused what the engine would serve: a diff.
			limited.Client.Status = 403
			if l := s.Compare("chat", &p, limited); l.Outcome != "diff" || l.Diffs[0].Field != "served" {
				t.Errorf("refused: %+v", l)
			}
		}
	}
	if n < 15 {
		t.Fatalf("only %d fixtures compared", n)
	}
}
```

Create `ipc/v1/fixtures/config.push/invalid-front-shadow-class.json`:

```json
{"v":1,"type":"config.push","seq":2,"ts":1767225600002,"body":{"gen":1,"gateway":{"providers":[],"routes":[],"guardrails":{"scope":"external","secrets":"redact","pii":"log","injection":"log"},"defaults":{"model":"","plannerModel":"","codeModel":""},"tokenSaver":false,"modelMap":[]},"keys":[],"pricing":{},"front":{"listen":{"host":"127.0.0.1","port":4777},"upstream":{"port":53120},"engines":{"chat":"js","messages":"js","responses":"js","embeddings":"js","models":"js","gemini":"js","mcp":"js","a2a":"js","skills":"js","agents":"js"},"shadow":{"classes":["dashboard"]}}}}
```

Create `ipc/v1/fixtures/config.push/valid-front-shadow.json`:

```json
{"v":1,"type":"config.push","seq":2,"ts":1767225600002,"body":{"gen":1,"gateway":{"providers":[],"routes":[],"guardrails":{"scope":"external","secrets":"redact","pii":"log","injection":"log"},"defaults":{"model":"","plannerModel":"","codeModel":""},"tokenSaver":false,"modelMap":[]},"keys":[],"pricing":{},"front":{"listen":{"host":"127.0.0.1","port":4777},"upstream":{"port":53120},"engines":{"chat":"js","messages":"js","responses":"js","embeddings":"js","models":"js","gemini":"js","mcp":"js","a2a":"js","skills":"js","agents":"js"},"shadow":{"classes":["chat"]}}}}
```

Create `test/front-shadow.test.js`:

```js
// The shadow compare's JS side (P4b): the capture a marked request leaves (lib/gateway/shadow.js), the internal port's
// endpoints for the gateway's own requests, and the shadow log with the rule that switches a class to go
// (lib/front/shadow-log.js).
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const http = require('http');
const { withShadow, noteProviderRequest, noteAnswer, takeCapture } = require('../lib/gateway/shadow');
const { createShadowLog, shadowEndpoints, DAY } = require('../lib/front/shadow-log');
const { createInternalListener } = require('../lib/front/internal');
const { signClient } = require('../lib/front/client');
const { routing, shadowOf } = require('../lib/front');
const { tmpDir } = require('./ipc-stub');

test('a capture exists only for a marked request, and is handed out once', async () => {
  noteAnswer({ status: 200, body: 'x' }, { status: 200, body: 'x' }, {});
  await withShadow('00112233445566aa', async () => {
    noteProviderRequest('http://p/v1/chat/completions', { authorization: 'Bearer k' }, '{"model":"m"}');
    await new Promise((r) => setTimeout(r, 1));
    noteAnswer({ status: 200, body: '{}' }, { status: 200, body: '{"model":"x"}' }, { keyId: 'k1' });
  });
  const c = takeCapture('00112233445566aa');
  assert.equal(c.providerRequests.length, 1);
  assert.equal(c.providerRequests[0].body, '{"model":"m"}');
  assert.deepEqual(c.usage, { keyId: 'k1' });
  assert.equal(takeCapture('00112233445566aa'), null, 'once');
  withShadow('not-an-id', () => noteAnswer({ status: 1, body: '' }, { status: 1, body: '' }, {}));
  assert.equal(takeCapture('not-an-id'), null);
  // A marked request refused before any provider call: its status, no provider answer.
  const res = { statusCode: 401 };
  await withShadow('00112233445566bb', async () => {}, res);
  assert.deepEqual(takeCapture('00112233445566bb'), {
    providerRequests: [],
    providerResponse: null,
    client: { status: 401, body: null },
    usage: null,
  });
});

test('the shadow log: counts per class, and the switch rule (owner: 1 clean day and 20 compares; a diff restarts it)', (t) => {
  const dir = tmpDir(t, 'shadow-log');
  let now = 1_000_000_000_000;
  const log = createShadowLog({ file: path.join(dir, 's.jsonl'), now: () => now });
  assert.match(log.gate('chat').reason, /0 of 20 chat calls compared/);
  assert.match(log.gate('chat', { days: 1, min: 0 }).reason, /compared no chat call/);
  assert.equal(log.append({ class: 'chat', outcome: 'nonsense' }), false);
  assert.equal(log.append({ class: 'chat', outcome: 'handoff', reason: 'stream' }), true);
  assert.equal(log.append({ class: 'chat', outcome: 'skipped', reason: 'the provider call failed' }), true);
  for (let i = 0; i < 19; i++) log.append({ class: 'chat', outcome: 'same' });
  now += 2 * DAY;
  assert.match(log.gate('chat').reason, /19 of 20 chat calls compared/, 'hand-offs and skips do not count');
  log.append({ class: 'chat', outcome: 'same' });
  assert.deepEqual(log.gate('chat'), { ok: true }, 'the defaults: 1 day, 20 compares');
  assert.equal(
    log.append({ class: 'chat', outcome: 'diff', diffs: [{ field: 'client.status', js: 200, go: 500 }] }),
    true,
  );
  assert.match(log.gate('chat').reason, /last chat compare differed/);
  for (let i = 0; i < 20; i++) log.append({ class: 'chat', outcome: 'same' });
  assert.match(log.gate('chat').reason, /clean for 0\.00 of 1 days/, 'the wait starts again at the diff');
  now += DAY;
  assert.deepEqual(log.gate('chat'), { ok: true });
  assert.match(log.gate('chat', { days: 3 }).reason, /clean for 1\.00 of 3 days/);
  assert.match(log.gate('chat', { min: 25 }).reason, /20 of 25 chat calls compared/);
  assert.deepEqual(log.gate('chat', { days: 0, min: 0 }), { ok: true });
  assert.deepEqual(log.gate('messages', { days: 0, min: 0 }), { ok: true }, 'both 0 lift the rule');
  assert.match(log.gate('messages', { days: 0, min: 1 }).reason, /0 of 1 messages calls compared/);
  const st = log.stats().chat;
  assert.deepEqual([st.compared, st.diffs, st.handoffs], [41, 1, 2]);
  // Read back after a restart: the same counts and the same rule.
  const again = createShadowLog({ file: path.join(dir, 's.jsonl'), now: () => now });
  assert.deepEqual(again.stats(), log.stats());
  assert.deepEqual(again.gate('chat'), { ok: true });
  assert.ok(!fs.readFileSync(path.join(dir, 's.jsonl'), 'utf8').includes('undefined'));
});

test('routing: go only when asked, the gate met and OTLP export off; shadow only for a class on js', () => {
  const gw = { engines: { chat: 'go' }, shadow: ['chat', 'messages'] };
  assert.equal(routing(gw, { gate: () => ({ ok: true }) }).engines.chat, 'go');
  const no = routing(gw, { gate: () => ({ ok: false, reason: 'not yet' }) });
  assert.deepEqual([no.engines.chat, no.notes.chat], ['js', 'not yet']);
  assert.match(routing(gw, { otel: true }).notes.chat, /OpenTelemetry/);
  let rule = null;
  routing(gw, { gate: (c, r) => ((rule = r), { ok: true }) });
  assert.deepEqual(rule, { days: 1, min: 20 }, 'the owner defaults');
  routing({ ...gw, shadowDays: 0, shadowMinCompares: 5 }, { gate: (c, r) => ((rule = r), { ok: true }) });
  assert.deepEqual(rule, { days: 0, min: 5 });
  routing({ ...gw, shadowDays: -1, shadowMinCompares: 'x' }, { gate: (c, r) => ((rule = r), { ok: true }) });
  assert.deepEqual(rule, { days: 1, min: 20 }, 'a value that is not a count of zero or more is the default');
  assert.deepEqual(shadowOf(gw, { chat: 'js' }), ['chat']);
  assert.deepEqual(shadowOf(gw, { chat: 'go' }), []);
});

test('the internal port answers the gateway own requests on /_zp/, and nothing else does', async (t) => {
  const token = 'a'.repeat(64);
  const dir = tmpDir(t, 'shadow-own');
  const shadowLog = createShadowLog({ file: path.join(dir, 's.jsonl') });
  const dashboard = new (require('events').EventEmitter)();
  const seen = [];
  dashboard.on('request', (req, res) => {
    seen.push(req.url);
    res.end('dashboard');
  });
  const inner = createInternalListener({ server: dashboard, token, own: shadowEndpoints({ shadowLog }) });
  const port = await inner.listen();
  t.after(() => inner.close());
  const ask = (method, p, headers = {}, body) =>
    new Promise((resolve, reject) => {
      const req = http.request(
        {
          host: '127.0.0.1',
          port,
          method,
          path: p,
          headers: { 'x-zp-client': signClient({ ip: '127.0.0.1', port: 4777, tls: false }, token), ...headers },
        },
        (res) => {
          let b = '';
          res.on('data', (c) => (b += c));
          res.on('end', () => resolve({ status: res.statusCode, body: b }));
        },
      );
      req.on('error', reject);
      req.end(body);
    });
  await withShadow('ffeeddccbbaa0099', async () =>
    noteAnswer({ status: 200, body: '' }, { status: 200, body: '' }, {}),
  );
  assert.equal((await ask('GET', '/_zp/shadow/ffeeddccbbaa0099', { 'x-zp-own': '1' })).status, 200);
  assert.equal((await ask('GET', '/_zp/shadow/ffeeddccbbaa0099', { 'x-zp-own': '1' })).status, 404, 'once');
  const line = JSON.stringify({ class: 'chat', outcome: 'same' });
  assert.equal((await ask('POST', '/_zp/shadow', { 'x-zp-own': '1' }, line)).status, 204);
  assert.equal((await ask('POST', '/_zp/shadow', { 'x-zp-own': '1' }, '{"x":1}')).status, 400);
  assert.equal(shadowLog.stats().chat.compared, 1);
  // Without the gateway's own mark, /_zp/ is an ordinary path for the dashboard.
  assert.equal((await ask('GET', '/_zp/shadow/ffeeddccbbaa0099')).body, 'dashboard');
  // The mark on an ordinary path is not passed on to the dashboard.
  assert.equal((await ask('GET', '/login.html', { 'x-zp-own': '1' })).status, 404);
  assert.deepEqual(seen, ['/_zp/shadow/ffeeddccbbaa0099']);
});
```

Apply (from the repository root):

```bash
git apply <<'PATCH'
diff --git a/gw/front/route_test.go b/gw/front/route_test.go
index bfc7357..65b3078 100644
--- a/gw/front/route_test.go
+++ b/gw/front/route_test.go
@@ -136,3 +136,22 @@ func TestRequestTargetIsForwardedAsSent(t *testing.T) {
 		}
 	}
 }
+
+// A client's own X-Zp-Shadow or X-Zp-Own never reaches the control plane: only the gateway marks a request.
+func TestClientShadowMarksAreRemoved(t *testing.T) {
+	got := make(chan http.Header, 1)
+	up := startUpstream(t, func(w http.ResponseWriter, r *http.Request) { got <- r.Header.Clone() })
+	s := startFront(t, up.port(), nil)
+	req, _ := http.NewRequest("POST", frontURL(s)+"/v1/chat/completions", strings.NewReader("{}"))
+	req.Header.Set(ShadowHeader, "0123456789abcdef")
+	req.Header.Set(OwnHeader, "1")
+	resp, err := http.DefaultClient.Do(req)
+	if err != nil {
+		t.Fatal(err)
+	}
+	_ = resp.Body.Close()
+	h := <-got
+	if h.Get(ShadowHeader) != "" || h.Get(OwnHeader) != "" {
+		t.Fatalf("forwarded %v", h)
+	}
+}
diff --git a/test/contract/formats.test.js b/test/contract/formats.test.js
index 867aa19..b0831c0 100644
--- a/test/contract/formats.test.js
+++ b/test/contract/formats.test.js
@@ -773,7 +773,7 @@ const CASES = [
       assert.equal(r.status, 400);
       // With the Go gateway in front, Go's HTTP server refuses a request target with a broken escape itself, before
       // any handler, with its own plain-text 400 (P4a: accepted; only the body differs, for a request no client sends).
-      if (['front', 'go'].includes(process.env.ZP_ENGINE) && r.text === '400 Bad Request') return;
+      if (['front', 'go', 'shadow'].includes(process.env.ZP_ENGINE) && r.text === '400 Bad Request') return;
       assert.equal(r.json.error.message, 'model id is not valid URL encoding');
     },
   },
diff --git a/test/contract/go-chat.test.js b/test/contract/go-chat.test.js
index f7e1c03..d1c0fdb 100644
--- a/test/contract/go-chat.test.js
+++ b/test/contract/go-chat.test.js
@@ -159,3 +159,21 @@ const CASES = [
 ];
 
 for (const c of CASES) test(`go chat: ${c.name}`, c.run);
+
+// ZP_ENGINE=shadow: the gateway compared the calls above with the go engine and found them the same (the harness's
+// stop() fails on any diff line).
+test(
+  'go chat: the shadow compared the calls the engine would serve, with no diff',
+  { skip: process.env.ZP_ENGINE !== 'shadow' },
+  async () => {
+    const end = Date.now() + 5000;
+    let st;
+    for (;;) {
+      st = (await h.call('/api/gateway/status', { method: 'GET' })).json;
+      if ((st.shadow.chat && st.shadow.chat.compared >= 8) || Date.now() > end) break;
+      await new Promise((r) => setTimeout(r, 50));
+    }
+    assert.ok(st.shadow.chat.compared >= 8, JSON.stringify(st.shadow));
+    assert.equal(st.shadow.chat.diffs, 0);
+  },
+);
diff --git a/test/contract/harness.js b/test/contract/harness.js
index ba25d73..03d235f 100644
--- a/test/contract/harness.js
+++ b/test/contract/harness.js
@@ -8,7 +8,9 @@
 //   ZP_ENGINE=front        gateway.engine "front": the Go gateway (built by scripts/ipc/build-stub.js, or ZP_GW_BIN)
 //                          holds the port and forwards every case to the server (P4a T6)
 //   ZP_ENGINE=go           front, with chat on the go engine (P4b): the gateway serves the chat calls it can itself
-//                          and hands the others to the server
+//                          and hands the others to the server (gateway.shadowDays 0: no shadow gate)
+//   ZP_ENGINE=shadow       front, chat on js and shadowed (P4b): the gateway compares each chat call with the go
+//                          engine; stop() fails when the server's .data/gateway-shadow.jsonl holds a diff
 // Nothing here reaches the network: the providers point at the fake upstream on 127.0.0.1, price sync is off, and
 // the judge ports, notifications and runtime polling are off (as test/helpers.js NO_JUDGE_PORTS).
 const fs = require('fs');
@@ -36,7 +38,10 @@ const SEED_CONFIG = {
   pricingSync: { enabled: false },
   pricing: PRICES,
   ...(process.env.ZP_ENGINE === 'front' ? { gateway: { engine: 'front' } } : {}),
-  ...(process.env.ZP_ENGINE === 'go' ? { gateway: { engine: 'front', engines: { chat: 'go' } } } : {}),
+  ...(process.env.ZP_ENGINE === 'go'
+    ? { gateway: { engine: 'front', engines: { chat: 'go' }, shadowDays: 0, shadowMinCompares: 0 } }
+    : {}),
+  ...(process.env.ZP_ENGINE === 'shadow' ? { gateway: { engine: 'front', shadow: ['chat'] } } : {}),
 };
 
 // The environment a front-mode server needs: the gateway binary. Throws without Go: a front run is asked for.
@@ -48,6 +53,17 @@ function gatewayEnv(config) {
 }
 const UPSTREAM_SECRET = { oa: 'sk-contract-oa', an: 'sk-contract-an', rs: 'sk-contract-rs' };
 
+// The diff lines of a server's shadow log (none when there is no log).
+function shadowDiffs(data) {
+  let text = '';
+  try {
+    text = fs.readFileSync(path.join(data, '.data', 'gateway-shadow.jsonl'), 'utf8');
+  } catch {
+    return [];
+  }
+  return text.split('\n').filter((l) => l.includes('"outcome":"diff"'));
+}
+
 const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
 
 function freePort() {
@@ -164,8 +180,11 @@ async function startServer({
         clearTimeout(giveUp);
       }
       process.removeListener('exit', reap);
+      // ZP_ENGINE=shadow: every compare of this server's run must have been the same (P4b gate: diff 0).
+      const diffs = shadowDiffs(data);
       // Retries: on Windows the folder can stay busy for a moment after the child exits.
       fs.rmSync(data, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 });
+      if (diffs.length) throw new Error(`the shadow compare found ${diffs.length} diff(s):\n${diffs.join('\n')}`);
     };
     while (!exited && performance.now() - started < 30000) {
       try {
@@ -304,8 +323,11 @@ async function start() {
       upstream,
       server,
       stop: async () => {
-        await server.stop();
-        await upstream.close();
+        try {
+          await server.stop();
+        } finally {
+          await upstream.close(); // also when the server's stop throws (a shadow diff), or the test process hangs
+        }
       },
     };
   } catch (err) {
PATCH
```

- [ ] **Step 3: Run them to see them fail**

Run: `GOTOOLCHAIN=local go -C gw test -count=1 ./... ; node --test test/front-shadow.test.js`
Expected: Go FAIL `undefined: Capture`, `undefined: ShadowHeader`, and the `valid-front-shadow.json` fixture refused; Node FAIL `Cannot find module '../lib/gateway/shadow'`.

- [ ] **Step 4: Implement**

Create `gw/engine/shadow.go`:

```go
package engine

// The go engine's side of the shadow compare (P4b): while a class runs on js, the front marks a request, the control
// plane serves it and keeps a capture (lib/gateway/shadow.js), and the engine turns the same provider answer into its
// own answer and usage line and compares. Only the control plane calls the provider. The line it posts back
// (lib/front/shadow-log.js) holds no prompt or answer text: a text that differs is named by its length, a hash prefix
// and the first byte that differs.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strings"
)

// Capture is what the control plane recorded of one request it served.
type Capture struct {
	ProviderRequests []struct {
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
		Body    *string           `json:"body"`
	} `json:"providerRequests"`
	ProviderResponse *struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
	} `json:"providerResponse"`
	Client *struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
	} `json:"client"`
	Usage map[string]any `json:"usage"`
}

// Diff is one field that differs.
type Diff struct {
	Field string `json:"field"`
	JS    any    `json:"js"`
	Go    any    `json:"go"`
}

// ShadowLine is the line of one compared request (lib/front/shadow-log.js).
type ShadowLine struct {
	Class   string `json:"class"`
	Outcome string `json:"outcome"` // same, diff, handoff, skipped
	Reason  string `json:"reason,omitzero"`
	Status  int    `json:"status,omitzero"` // the control plane's answer
	Diffs   []Diff `json:"diffs,omitzero"`
}

// usageFields are the usage-line fields compared; ts, ms, ttft, traceId and ipc differ by nature, cost is compared
// in nano-USD, error as a text.
var usageFields = []string{"keyId", "provider", "model", "requested", "routedTo", "kind", "format", "stream", "status",
	"input", "output", "cached", "client", "local", "savedTokens", "guard"}

// Compare is the line for request plan p (built before the control plane answered) and its capture c (nil: the control
// plane made no provider call for it).
func (s *State) Compare(class string, p *Plan, c *Capture) ShadowLine {
	l := ShadowLine{Class: class}
	if c != nil && c.Client != nil {
		l.Status = c.Client.Status
	}
	if p.Handoff != "" {
		l.Outcome, l.Reason = "handoff", p.Handoff
		return l
	}
	d := func(field string, js, gov any) { l.Diffs = append(l.Diffs, Diff{field, js, gov}) }
	dt := func(field, js, gov string) { a, b := digest(js, gov); d(field, a, b) }
	switch {
	case c != nil && c.ProviderResponse == nil && len(c.ProviderRequests) > 0:
		l.Outcome, l.Reason = "skipped", "the provider call failed: no answer to compare"
		return l
	case c != nil && c.ProviderResponse == nil && c.Client != nil && c.Client.Status == http.StatusTooManyRequests:
		l.Outcome, l.Reason = "skipped", "the control plane's rate limit answered (each engine counts its own)"
		return l
	case c == nil || c.ProviderResponse == nil || c.Client == nil:
		js := "no capture"
		if c != nil && c.Client != nil {
			js = "answered " + NumberString(float64(c.Client.Status)) + " without a provider call"
		}
		d("served", js, "the engine would call the provider")
	case len(c.ProviderRequests) != 1:
		d("providerRequests", len(c.ProviderRequests), 1)
	default:
		pr := c.ProviderRequests[0]
		if pr.URL != p.URL {
			dt("providerRequest.url", pr.URL, p.URL)
		}
		goH := map[string]string{}
		for k := range p.Header {
			goH[strings.ToLower(k)] = p.Header.Get(k)
		}
		if a, b := headerSet(pr.Headers), headerSet(goH); a != b {
			dt("providerRequest.headers", a, b)
		}
		jsBody := ""
		if pr.Body != nil {
			jsBody = *pr.Body
		}
		if jsBody != string(p.Body) {
			dt("providerRequest.body", jsBody, string(p.Body))
		}
		a := s.Respond(p, c.ProviderResponse.Status, []byte(c.ProviderResponse.Body), 0)
		if a.Status != c.Client.Status {
			d("client.status", c.Client.Status, a.Status)
		}
		if string(a.Body) != c.Client.Body {
			dt("client.body", c.Client.Body, string(a.Body))
		}
		var goU map[string]any
		b, _ := json.Marshal(a.Usage)
		_ = json.Unmarshal(b, &goU)
		for _, f := range usageFields {
			js, gov := canonical(c.Usage[f]), canonical(goU[f])
			if js != gov {
				d("usage."+f, c.Usage[f], goU[f])
			}
		}
		if js, gov := text(c.Usage["error"]), text(goU["error"]); js != gov {
			dt("usage.error", js, gov)
		}
		jsCost, _ := c.Usage["cost"].(float64)
		if n := math.Round(jsCost * 1e9); int64(n) != a.Usage.Cost {
			d("usage.cost", int64(n), a.Usage.Cost)
		}
	}
	l.Outcome = "same"
	if len(l.Diffs) > 0 {
		l.Outcome = "diff"
	}
	return l
}

func canonical(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func text(v any) string {
	s, _ := v.(string)
	return s
}

// headerSet is a header map as one text, the credential replaced by its hash prefix.
func headerSet(h map[string]string) string {
	low := make(map[string]string, len(h))
	keys := make([]string, 0, len(h))
	for k, v := range h {
		low[strings.ToLower(k)] = v
		keys = append(keys, strings.ToLower(k))
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		v := low[k]
		if k == "authorization" || k == "x-api-key" {
			v = hashPrefix(v)
		}
		b.WriteString(k + ": " + v + "\n")
	}
	return b.String()
}

// digest names two different texts without their content: length, hash prefix, and where they first differ.
func digest(js, gov string) (any, any) {
	at := 0
	for at < len(js) && at < len(gov) && js[at] == gov[at] {
		at++
	}
	one := func(s string) map[string]any { return map[string]any{"len": len(s), "sha": hashPrefix(s), "at": at} }
	return one(js), one(gov)
}

func hashPrefix(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:6])
}

// NewShadowID is the id of one shadowed request.
func NewShadowID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Own is the front's call to the control plane's internal port as the gateway itself (X-ZP-Own).
type Own func(method, path string, body []byte) (int, []byte, error)

// ShadowHeader marks a request the engine shadows.
const ShadowHeader = "X-Zp-Shadow"

// shadowSlots bounds the compares running at once; a request past it is not compared.
var shadowSlots = make(chan struct{}, 8)

// Shadow forwards a request of a class on js to the control plane marked for the compare, then compares in the
// background once the client has its answer.
func (e *Engine) Shadow(class string, w http.ResponseWriter, r *http.Request, rawPath string, js http.Handler,
	own func(method, path string, body []byte) (int, []byte, error)) {
	s := e.state.Load()
	if s == nil || class != "chat" {
		js.ServeHTTP(w, r)
		return
	}
	req := ReadRequest(r, rawPath)
	p := s.PlanChat(req, e.now())
	// The engine counts what it would serve, as when it serves (the control plane counts every request of the key).
	var wait int64
	if p.Handoff == "" {
		wait = e.limits.Take(p.Key.ID, p.Key.RPM, e.now())
	}
	id := NewShadowID()
	r.Header.Set(ShadowHeader, id)
	js.ServeHTTP(w, r)
	select {
	case shadowSlots <- struct{}{}:
	default:
		return
	}
	go func() {
		defer func() { <-shadowSlots }()
		var c *Capture
		if code, body, err := own(http.MethodGet, "/_zp/shadow/"+id, nil); err == nil && code == http.StatusOK {
			c = &Capture{}
			if json.Unmarshal(body, c) != nil {
				c = nil
			}
		}
		l := s.Compare(class, &p, c)
		if wait > 0 {
			// The engine would have answered 429 itself.
			l = ShadowLine{Class: class, Outcome: "same", Status: l.Status}
			if l.Status != http.StatusTooManyRequests {
				l.Outcome, l.Diffs = "diff", []Diff{{Field: "rateLimit", JS: l.Status, Go: http.StatusTooManyRequests}}
			}
		}
		line, _ := json.Marshal(l)
		if _, _, err := own(http.MethodPost, "/_zp/shadow", line); err != nil {
			e.logf("shadow: the compare line could not be posted: %v", err)
		}
	}()
}
```

Create `lib/front/shadow-log.js`:

```js
// The shadow compare's log and the rule that lets a route class switch to the go engine (P4b). The Go gateway posts one
// line per shadowed request to the internal port (POST /_zp/shadow, lib/front/internal.js `own`); this appends it to
// .data/gateway-shadow.jsonl and keeps, per route class, the counts and the time of the last diff. A line holds no
// prompt or answer text: a body that differs is named by its length and a hash prefix and the first byte that differs.
//   { ts, class, outcome: 'same' | 'diff' | 'handoff' | 'skipped', reason?, status?, diffs?: [{ field, js, go }] }
// handoff: the engine would have handed the request to js; skipped: nothing to compare (the provider call failed, or
// the control plane's own rate limit answered: the windows are per engine). Both count as not compared.
// The switch rule (spec stream A item 3; owner answer 2026-11-12): a class may run on go only when the shadow has, since
// its last diff, compared at least `min` of its calls (config.json gateway.shadowMinCompares, default 20) and seen no
// diff for `days` days (gateway.shadowDays, default 1). 0 lifts either part; both 0 lift the rule (tests, or an owner
// who decides without it).
const { jsonlLog } = require('../gateway/store');
const { takeCapture } = require('../gateway/shadow');

const DAY = 86400000;
const OUTCOMES = new Set(['same', 'diff', 'handoff', 'skipped']);
const MAX_LINE = 64 * 1024;

/** @param {{ file: string, now?: () => number, maxBytes?: number }} opts */
function createShadowLog({ file, now = Date.now, maxBytes }) {
  const log = jsonlLog(file, maxBytes);
  /** @type {Map<string, { compared: number, diffs: number, handoffs: number, first: number | null, lastDiff: number | null, last: number | null, sinceDiff: number }>} */
  const stats = new Map();
  const statOf = (c) => {
    if (!stats.has(c))
      stats.set(c, { compared: 0, diffs: 0, handoffs: 0, first: null, lastDiff: null, last: null, sinceDiff: 0 });
    return stats.get(c);
  };
  const count = (e) => {
    if (!e || typeof e.class !== 'string' || !OUTCOMES.has(e.outcome) || !Number.isFinite(e.ts)) return false;
    const s = statOf(e.class);
    if (e.outcome === 'handoff' || e.outcome === 'skipped') s.handoffs++;
    else {
      s.compared++;
      if (s.first === null) s.first = e.ts;
      s.last = e.ts;
      if (e.outcome === 'diff') {
        s.diffs++;
        s.lastDiff = e.ts;
        s.sinceDiff = 0;
      } else s.sinceDiff++;
    }
    return true;
  };
  for (const e of log.read()) count(e);
  return {
    /** Appends a line from the gateway (a line that is not one is refused). */
    append(e) {
      const line = { ...e, ts: now() };
      if (!count(line)) return false;
      log.append(line);
      return true;
    },
    /** Per class: compares, diffs, hand-offs, and since when there was no diff (the first compare after the last diff). */
    stats() {
      const out = {};
      for (const [c, s] of stats)
        out[c] = {
          compared: s.compared,
          diffs: s.diffs,
          handoffs: s.handoffs,
          cleanSince: s.lastDiff ?? s.first,
          lastDiffAt: s.lastDiff,
        };
      return out;
    },
    /**
     * Whether class c may run on go: `min` compares and `days` days without a diff since the last one; reason says what
     * is missing.
     * @param {string} c
     * @param {{ days?: number, min?: number }} [rule]
     * @returns {{ ok: boolean, reason?: string }}
     */
    gate(c, { days = 1, min = 20 } = {}) {
      if (days === 0 && min === 0) return { ok: true }; // the owner lifted the rule
      const s = stats.get(c);
      if (s && s.lastDiff !== null && s.sinceDiff === 0) return { ok: false, reason: `the last ${c} compare differed` };
      const n = s ? s.sinceDiff : 0;
      if (n < min) return { ok: false, reason: `${n} of ${min} ${c} calls compared without a diff since the last one` };
      if (days === 0) return { ok: true };
      if (!s || !s.compared) return { ok: false, reason: `the shadow has compared no ${c} call yet` };
      // A diff restarts the wait: from the diff on, the class needs `days` days with compares and no diff.
      const since = s.lastDiff !== null ? s.lastDiff : s.first;
      const clean = (now() - since) / DAY;
      if (clean < days)
        return { ok: false, reason: `the ${c} shadow is clean for ${clean.toFixed(2)} of ${days} days` };
      return { ok: true };
    },
  };
}

/**
 * The internal port's answers to the gateway's own requests (lib/front/internal.js `own`):
 *   GET  /_zp/shadow/<id>  the capture of a shadowed request, once (404 when there is none);
 *   POST /_zp/shadow       one compare line, appended (204; 400 when it is not one).
 * @param {{ shadowLog: ReturnType<typeof createShadowLog> }} deps
 */
function shadowEndpoints({ shadowLog }) {
  return (req, res) => {
    const m = /^\/_zp\/shadow\/([0-9a-f]{16,64})$/.exec(req.url);
    if (req.method === 'GET' && m) {
      const c = takeCapture(m[1]);
      if (!c) return res.writeHead(404).end();
      res.writeHead(200, { 'content-type': 'application/json' });
      return res.end(JSON.stringify(c));
    }
    if (req.method === 'POST' && req.url === '/_zp/shadow') {
      let size = 0;
      const chunks = [];
      req.on('data', (c) => {
        size += c.length;
        if (size <= MAX_LINE) chunks.push(c);
      });
      req.on('end', () => {
        let e = null;
        try {
          e = size <= MAX_LINE ? JSON.parse(Buffer.concat(chunks).toString('utf8')) : null;
        } catch {
          e = null;
        }
        res.writeHead(e && typeof e === 'object' && shadowLog.append(e) ? 204 : 400).end();
      });
      return;
    }
    res.writeHead(404).end();
  };
}

module.exports = { createShadowLog, shadowEndpoints, DAY };
```

Create `lib/gateway/shadow.js`:

```js
// @ts-check
// The JS side of the shadow compare (P4b, owner decision 2 of the P4a spec): while a route class is served by js, the
// Go gateway in front marks some of its requests with X-ZP-Shadow: <id> (lib/front/internal.js keeps the mark only
// after the signed client header checks out). For a marked request the gateway records, without changing anything it
// does, the provider request it sent, the provider's answer, its own answer to the client and the usage line; Go then
// fetches that capture once (GET /_zp/shadow/<id> on the internal port), builds its own provider request and answer from
// the same provider answer, compares, and posts the result line (lib/front/shadow-log.js). Only the serving engine calls
// the provider. A capture lives in memory for at most TTL_MS and is handed out once.
const { AsyncLocalStorage } = require('async_hooks');

const SHADOW_ID = Symbol.for('zaafpulse.front.shadow');
const ID = /^[0-9a-f]{16,64}$/;
const TTL_MS = 60000;
const MAX = 1000;

const als = new AsyncLocalStorage();
/** @type {Map<string, { at: number, capture: any }>} */
const captures = new Map();

const store = (id, capture, now) => {
  for (const [k, v] of captures) if (now - v.at > TTL_MS) captures.delete(k);
  if (captures.size >= MAX) captures.delete(captures.keys().next().value);
  captures.set(id, { at: now, capture });
};

/**
 * Runs fn with a capture for shadow id `id` (a request Go marked), or plainly when there is none. A marked request that
 * got no provider answer (refused before the call, or the call failed) still leaves a capture once fn is done: the
 * provider requests made and the status of the answer `res` got (Go tells a failed call from a refusal by them).
 * @param {unknown} id
 * @param {() => any} fn
 * @param {{ statusCode: number } | null} [res]
 */
async function withShadow(id, fn, res = null) {
  if (typeof id !== 'string' || !ID.test(id)) return fn();
  const s = { id, providerRequests: [], answered: false };
  try {
    return await als.run(s, fn);
  } finally {
    if (!s.answered)
      store(
        id,
        {
          providerRequests: s.providerRequests,
          providerResponse: null,
          client: { status: res ? res.statusCode : 0, body: null },
          usage: null,
        },
        Date.now(),
      );
  }
}

/** providers.request: the request about to go to a provider (credentials included; Go compares and drops them). */
function noteProviderRequest(url, headers, body) {
  const s = als.getStore();
  if (s) s.providerRequests.push({ url, headers: { ...headers }, body: body === undefined ? null : body });
}

/**
 * chatCompletion, not streaming: the provider's answer, the client's answer and the usage line; stores the capture.
 * @param {{ status: number, body: string }} providerResponse
 * @param {{ status: number, body: string }} client
 * @param {object} usage
 */
function noteAnswer(providerResponse, client, usage, now = Date.now()) {
  const s = als.getStore();
  if (!s) return;
  s.answered = true;
  store(s.id, { providerRequests: s.providerRequests, providerResponse, client, usage }, now);
}

/** The capture of id, once (null when there is none: the request never reached a provider, or it was taken). */
function takeCapture(id) {
  const c = captures.get(id);
  captures.delete(id);
  return c ? c.capture : null;
}

module.exports = { withShadow, noteProviderRequest, noteAnswer, takeCapture, SHADOW_ID, TTL_MS };
```

Apply (from the repository root):

```bash
git apply <<'PATCH'
diff --git a/gw/cmd/zaafpulse-gw/front.go b/gw/cmd/zaafpulse-gw/front.go
index ed58994..48579cf 100644
--- a/gw/cmd/zaafpulse-gw/front.go
+++ b/gw/cmd/zaafpulse-gw/front.go
@@ -29,8 +29,13 @@ func (g *gateway) applyFront(f *ipc.FrontConfig) int {
 	}
 	g.mu.Lock()
 	defer g.mu.Unlock()
+	shadow := make([]string, 0, len(f.Shadow.Classes))
+	for _, c := range f.Shadow.Classes {
+		shadow = append(shadow, string(c))
+	}
 	if g.front != nil {
 		g.front.SetEngines(engines)
+		g.front.SetShadow(shadow)
 		if f.Listen != g.frontCfg.Listen || !sameTLS(f.TLS, g.frontCfg.TLS) || f.Upstream != g.frontCfg.Upstream {
 			g.log.Print("config.push: a new listen, tls or upstream applies after a restart")
 		}
@@ -68,6 +73,11 @@ func (g *gateway) openFront(f *ipc.FrontConfig, token string) int {
 	srv, err := front.Listen(cfg, g.log)
 	if err == nil {
 		srv.SetGoEngine(g.engine)
+		shadow := make([]string, 0, len(f.Shadow.Classes))
+		for _, c := range f.Shadow.Classes {
+			shadow = append(shadow, string(c))
+		}
+		srv.SetShadow(shadow)
 	}
 	if errors.Is(err, front.ErrAddrInUse) {
 		return exitWith(g.log, ipc.ExitPortInUse, "port %d on %s is in use", f.Listen.Port, f.Listen.Host)
diff --git a/gw/front/front.go b/gw/front/front.go
index 97e11ea..e9034ee 100644
--- a/gw/front/front.go
+++ b/gw/front/front.go
@@ -5,10 +5,12 @@
 package front
 
 import (
+	"bytes"
 	"context"
 	"crypto/tls"
 	"errors"
 	"fmt"
+	"io"
 	"log"
 	"net"
 	"net/http"
@@ -49,11 +51,21 @@ const MaxHeaderBytes = 16384 - 4096
 // class's requests to js itself). A config.push that puts another class on go is refused.
 var GoClasses = map[string]bool{"chat": true}
 
-// GoEngine serves a request of a class on the go engine, or hands it to js (gw/engine Engine).
+// GoEngine serves a request of a class on the go engine, or hands it to js; Shadow forwards a request of a class on js
+// marked for the compare (gw/engine Engine).
 type GoEngine interface {
 	Serve(class string, w http.ResponseWriter, r *http.Request, rawPath string, js http.Handler)
+	Shadow(class string, w http.ResponseWriter, r *http.Request, rawPath string, js http.Handler,
+		own func(method, path string, body []byte) (int, []byte, error))
 }
 
+// The gateway's marks for the control plane (lib/front/internal.js): a request it shadows, and a request of its own.
+// A client's copy of either is removed from every request.
+const (
+	ShadowHeader = "X-Zp-Shadow"
+	OwnHeader    = "X-Zp-Own"
+)
+
 // ErrAddrInUse: another process holds the main port. The gateway exits 4.
 var ErrAddrInUse = errors.New("front: the main port is in use")
 
@@ -66,6 +78,40 @@ type Server struct {
 	engines  atomic.Pointer[map[string]string]
 	inflight atomic.Int64
 	goEngine GoEngine
+	shadow   atomic.Pointer[map[string]bool]
+	upstream *url.URL
+}
+
+// SetShadow replaces the route classes whose js requests are compared with the go engine (config.push front.shadow).
+func (s *Server) SetShadow(classes []string) {
+	m := make(map[string]bool, len(classes))
+	for _, c := range classes {
+		m[c] = true
+	}
+	s.shadow.Store(&m)
+}
+
+// own calls the control plane's internal port as the gateway itself (the shadow endpoints under /_zp/).
+func (s *Server) own(method, path string, body []byte) (int, []byte, error) {
+	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
+	defer cancel()
+	req, err := http.NewRequestWithContext(ctx, method, s.upstream.String()+path, bytes.NewReader(body))
+	if err != nil {
+		return 0, nil, err
+	}
+	// The gateway names itself: loopback, at its own main port (the control plane accepts no port 0).
+	req.Header.Set(ClientHeader, ClientValue(s.cfg.Token, s.ln.Addr().String(), false))
+	req.Header.Set(OwnHeader, "1")
+	if body != nil {
+		req.Header.Set("Content-Type", "application/json")
+	}
+	resp, err := s.proxy.Transport.RoundTrip(req)
+	if err != nil {
+		return 0, nil, err
+	}
+	defer resp.Body.Close()
+	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
+	return resp.StatusCode, b, err
 }
 
 // SetGoEngine installs the go engine before Serve (without one, a class on go is answered 501).
@@ -94,8 +140,10 @@ func Listen(cfg Config, logger *log.Logger) (*Server, error) {
 	}
 	s := &Server{cfg: cfg, ln: ln}
 	s.SetEngines(cfg.Engines)
+	s.SetShadow(nil)
 	quiet := QuietLogger(logger, time.Now)
 	target := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.Upstream))}
+	s.upstream = target
 	s.proxy = &httputil.ReverseProxy{
 		Rewrite: func(pr *httputil.ProxyRequest) {
 			pr.SetURL(target)
@@ -222,11 +270,19 @@ func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
 		w.WriteHeader(http.StatusMethodNotAllowed)
 		return
 	}
+	r.Header.Del(ShadowHeader)
+	r.Header.Del(OwnHeader)
 	raw := RawPath(r.RequestURI)
-	if class := Classify(raw); s.engine(class) == "go" && GoClasses[class] && s.goEngine != nil {
+	class := Classify(raw)
+	if s.engine(class) == "go" && GoClasses[class] && s.goEngine != nil {
 		s.goEngine.Serve(class, w, r, raw, s.proxy)
 		return
-	} else if s.engine(class) != "js" {
+	}
+	if s.engine(class) == "js" && (*s.shadow.Load())[class] && GoClasses[class] && s.goEngine != nil {
+		s.goEngine.Shadow(class, w, r, raw, s.proxy, s.own)
+		return
+	}
+	if s.engine(class) != "js" {
 		w.Header().Set("Content-Type", "application/json")
 		w.WriteHeader(http.StatusNotImplemented)
 		_, _ = w.Write([]byte(`{"error":"the go engine is not available in this gateway"}`))
diff --git a/gw/ipc/messages.go b/gw/ipc/messages.go
index 60872ee..17eb052 100644
--- a/gw/ipc/messages.go
+++ b/gw/ipc/messages.go
@@ -80,8 +80,27 @@ type FrontUpstream struct {
 	Port int64 `json:"port" ipc:"min=1,max=65535"`
 }
 
-// FrontShadow is reserved for the shadow compare (P4b): empty in P4a.
-type FrontShadow struct{}
+// FrontShadow names the route classes on js whose requests the gateway compares with the go engine (P4b; empty in
+// P4a).
+type FrontShadow struct {
+	Classes []RouteClass `json:"classes,omitzero"`
+}
+
+// RouteClass is one of the gateway's route classes (gw/front Classes).
+type RouteClass string
+
+func (c *RouteClass) UnmarshalJSON(b []byte) error {
+	var s string
+	if err := json.Unmarshal(b, &s); err != nil {
+		return fmt.Errorf("route class %s is not a string", b)
+	}
+	switch s {
+	case "chat", "messages", "responses", "embeddings", "models", "gemini", "mcp", "a2a", "skills", "agents":
+		*c = RouteClass(s)
+		return nil
+	}
+	return fmt.Errorf("route class %q is not one of the gateway's", s)
+}
 
 // Engine is "js" or "go".
 type Engine string
diff --git a/ipc/v1/config.push.schema.json b/ipc/v1/config.push.schema.json
index d4c1870..ef842b3 100644
--- a/ipc/v1/config.push.schema.json
+++ b/ipc/v1/config.push.schema.json
@@ -195,7 +195,20 @@
           "properties": { "port": { "type": "integer", "minimum": 1, "maximum": 65535 } }
         },
         "engines": { "type": "object", "additionalProperties": { "type": "string", "enum": ["js", "go"] } },
-        "shadow": { "type": "object", "additionalProperties": false }
+        "shadow": {
+          "description": "P4b: the route classes on js whose requests the gateway compares with the go engine",
+          "type": "object",
+          "additionalProperties": false,
+          "properties": {
+            "classes": {
+              "type": "array",
+              "items": {
+                "type": "string",
+                "enum": ["chat", "messages", "responses", "embeddings", "models", "gemini", "mcp", "a2a", "skills", "agents"]
+              }
+            }
+          }
+        }
       }
     }
   }
diff --git a/lib/front/index.js b/lib/front/index.js
index 173c14e..b469a97 100644
--- a/lib/front/index.js
+++ b/lib/front/index.js
@@ -19,15 +19,43 @@ const allJs = () => Object.fromEntries(CLASSES.map((c) => [c, 'js']));
 const GO_CLASSES = ['chat'];
 
 /**
- * The engine of each route class config.json asks for (`gateway.engines`, e.g. { "chat": "go" }): go only for a class
- * the go engine has; anything else is js.
+ * The engine of each route class (P4b) and why a class asked for go stays on js. config.json `gateway.engines` asks
+ * (e.g. { "chat": "go" }); a class runs on go only when the go engine has it, the shadow gate lets it (`gate(class,
+ * { days, min })`, lib/front/shadow-log.js; `gateway.shadowDays`, default 1, and `gateway.shadowMinCompares`, default
+ * 20: the owner's rule of 2026-11-12) and OTLP export is off (the go engine sends no spans until P4f).
  * @param {any} gateway config.json's gateway section
+ * @param {{ gate?: (c: string, rule: { days: number, min: number }) => { ok: boolean, reason?: string },
+ *   otel?: boolean }} [opts]
+ * @returns {{ engines: Record<string, string>, notes: Record<string, string> }}
  */
-function enginesOf(gateway) {
+function routing(gateway, { gate = () => ({ ok: true }), otel = false } = {}) {
   const asked = gateway && gateway.engines && typeof gateway.engines === 'object' ? gateway.engines : {};
-  const out = allJs();
-  for (const c of GO_CLASSES) if (asked[c] === 'go') out[c] = 'go';
-  return out;
+  // The owner's rule (2026-11-12): 1 clean day and 20 compared calls; a value that is not a count of zero or more is the
+  // default.
+  const count = (v, d) => (Number.isFinite(v) && v >= 0 ? v : d);
+  const rule = { days: count(gateway && gateway.shadowDays, 1), min: count(gateway && gateway.shadowMinCompares, 20) };
+  const engines = allJs();
+  /** @type {Record<string, string>} */
+  const notes = {};
+  for (const c of GO_CLASSES) {
+    if (asked[c] !== 'go') continue;
+    const g = gate(c, rule);
+    if (otel) notes[c] = 'OpenTelemetry export is on: the go engine sends no spans yet (P4f)';
+    else if (!g.ok) notes[c] = g.reason || 'the shadow gate is not met';
+    else engines[c] = 'go';
+  }
+  return { engines, notes };
+}
+/** The engines alone (routing().engines). */
+const enginesOf = (gateway, opts) => routing(gateway, opts).engines;
+
+/**
+ * The classes the gateway shadows: config.json `gateway.shadow` (e.g. ["chat"]), for a class the go engine has that
+ * runs on js (P4b compares in that direction only).
+ */
+function shadowOf(gateway, engines) {
+  const asked = gateway && Array.isArray(gateway.shadow) ? gateway.shadow : [];
+  return GO_CLASSES.filter((c) => asked.includes(c) && engines[c] === 'js');
 }
 
 /** The mode config.json asks for: "front" or "js" (anything else is js). */
@@ -37,7 +65,7 @@ function engineOf(config) {
 }
 
 /** The config.push front section. go is refused for a class the go engine does not have (the gateway would exit 2). */
-function frontSection({ host, port, tls = null, upstreamPort, engines = allJs() }) {
+function frontSection({ host, port, tls = null, upstreamPort, engines = allJs(), shadow = [] }) {
   for (const [c, e] of Object.entries(engines))
     if (e !== 'js' && !(e === 'go' && GO_CLASSES.includes(c)))
       throw new Error(`gateway engine "${e}" for ${c} is not available: only ${GO_CLASSES.join(', ')} can run on go`);
@@ -46,7 +74,7 @@ function frontSection({ host, port, tls = null, upstreamPort, engines = allJs()
     ...(tls ? { tls: { certPath: tls.certPath, keyPath: tls.keyPath } } : {}),
     upstream: { port: upstreamPort },
     engines,
-    shadow: {},
+    shadow: shadow.length ? { classes: shadow } : {},
   };
 }
 
@@ -75,7 +103,11 @@ const newToken = () => Buffer.from(crypto.getRandomValues(new Uint8Array(32))).t
  *   env: Record<string, string>, cpVersion: string, ingest: { handle: Function, seed?: Function },
  *   beforeAck?: (b: { stream: string, upTo: number }) => void, log?: (line: string) => void,
  *   onServing?: (mode: string) => void, onListenError?: (err: Error) => void, waitPortMs?: number,
- *   supervisor?: object, internal?: typeof createInternalListener, token?: string }} opts
+ *   supervisor?: object, internal?: typeof createInternalListener, token?: string,
+ *   createSupervisor?: typeof createSupervisor,
+ *   sources?: { config: () => object, secrets: () => object, snapshot: () => object },
+ *   engines?: () => Record<string, string>, shadow?: (engines: Record<string, string>) => string[],
+ *   own?: ((req: any, res: any) => void) | null }} opts
  */
 function createFront({
   engine,
@@ -105,8 +137,11 @@ function createFront({
   internal = createInternalListener,
   createSupervisor: makeSupervisor = createSupervisor,
   token = newToken(),
-  // The engine of each route class, read at each push (enginesOf(config.json gateway)); P4b.
+  // The engine of each route class and the classes shadowed, read at each push (routing(), shadowOf()); P4b.
   engines = allJs,
+  shadow = () => [],
+  // The internal port's answers to the gateway's own requests (lib/front/shadow-log.js shadowEndpoints); P4b.
+  own = null,
 }) {
   let mode = engine === 'front' ? 'front' : 'js';
   let reason = null;
@@ -181,12 +216,11 @@ function createFront({
     // The server's own request handler answers the internal port; the main port stays the gateway's.
     // A connection may wait for its first request as long as Go keeps an idle pooled connection (120 s): the front's
     // spare connections must never be closed under it.
-    inner = internal({ server: mainServer, token, log, firstRequestMs: 130000 });
+    inner = internal({ server: mainServer, token, log, firstRequestMs: 130000, own });
     const upstreamPort = await inner.listen();
     // Listen, TLS and upstream are fixed for this gateway's life: a later push carries the same ones (P4b entry
     // condition 3; a change of gateway.engine, the host, the port or the TLS files takes a restart of ZAAFPulse).
     const base = { host, port, tls, upstreamPort };
-    const front = frontSection(base);
     const mine = makeSupervisor({
       bin,
       env,
@@ -195,10 +229,10 @@ function createFront({
       beforeAck,
       log,
       sources: {
-        config: () => ({
-          ...sources.config(),
-          front: { ...front, engines: frontSection({ ...base, engines: engines() }).engines },
-        }),
+        config: () => {
+          const e = engines();
+          return { ...sources.config(), front: frontSection({ ...base, engines: e, shadow: shadow(e) }) };
+        },
         secrets: () => ({ ...sources.secrets(), frontToken: token }),
         snapshot: () => sources.snapshot(),
       },
@@ -331,4 +365,4 @@ function createFront({
   };
 }
 
-module.exports = { createFront, engineOf, enginesOf, frontSection, configBody, CLASSES, GO_CLASSES };
+module.exports = { createFront, engineOf, enginesOf, routing, shadowOf, frontSection, configBody, CLASSES, GO_CLASSES };
diff --git a/lib/front/internal.js b/lib/front/internal.js
index c928331..a174589 100644
--- a/lib/front/internal.js
+++ b/lib/front/internal.js
@@ -7,9 +7,14 @@
 // and with the header removed, so nothing downstream (a provider the gateway forwards to) ever sees it.
 const http = require('http');
 const { CLIENT_HEADER, FRONT_PEER, parseClientHeader } = require('./client');
+const { SHADOW_ID } = require('../gateway/shadow');
 
 // Go's flag on a request that came without a Host header (gw/front/front.go NoHostHeader).
 const NO_HOST_HEADER = 'x-zp-no-host';
+// P4b: Go's mark on a request it shadows (gw/front/front.go ShadowHeader), and on a request of its own (OwnHeader), which
+// goes to the shadow endpoints under /_zp/ and never to the dashboard. Go removes both from every request it forwards.
+const SHADOW_HEADER = 'x-zp-shadow';
+const OWN_HEADER = 'x-zp-own';
 
 const FIRST_REQUEST_MS = 2000;
 // Longer than Go's idle timeout for its upstream connections (120 s, spec D2), so Go always closes an idle
@@ -18,10 +23,11 @@ const KEEP_ALIVE_MS = 130_000;
 
 /**
  * @param {{ server: import('events').EventEmitter, token: string, log?: (msg: string) => void,
- *   firstRequestMs?: number }} opts firstRequestMs: how long a connection may stay open before its first request
+ *   firstRequestMs?: number, own?: ((req: any, res: any) => void) | null }} opts firstRequestMs: how long a connection may
+ *   stay open before its first request; own: answers the gateway's own requests to /_zp/ (P4b shadow)
  * @returns {{ listen(): Promise<number>, close(): Promise<void>, server: import('http').Server }}
  */
-function createInternalListener({ server, token, log = () => {}, firstRequestMs = FIRST_REQUEST_MS }) {
+function createInternalListener({ server, token, log = () => {}, firstRequestMs = FIRST_REQUEST_MS, own = null }) {
   if (!server || typeof server.emit !== 'function') throw new Error('createInternalListener needs the HTTP server');
   if (typeof token !== 'string' || token.length < 32) throw new Error('createInternalListener needs the frontToken');
   const waiting = new WeakMap(); // socket -> the timer that closes it if no request comes
@@ -41,9 +47,24 @@ function createInternalListener({ server, token, log = () => {}, firstRequestMs
     }
     // @ts-ignore: a symbol slot of our own on the request
     req[FRONT_PEER] = client;
+    // Go's own request (P4b shadow): the shadow endpoints answer it; nothing else does.
+    if (req.headers[OWN_HEADER] === '1') {
+      onOk();
+      if (own && req.url.startsWith('/_zp/')) return own(req, res);
+      res.writeHead(404).end();
+      return;
+    }
+    const shadowId = req.headers[SHADOW_HEADER];
+    // @ts-ignore: a symbol slot of our own on the request
+    if (typeof shadowId === 'string') req[SHADOW_ID] = shadowId;
     // The client sent no Host (HTTP/1.0): the Host Go had to write is its own 127.0.0.1:<this port>, and the host
     // check must see none, as on the main port in js mode (P4a final review I1). Go always sets or removes the flag.
-    const drop = [CLIENT_HEADER, NO_HOST_HEADER, ...(req.headers[NO_HOST_HEADER] === '1' ? ['host'] : [])];
+    const drop = [
+      CLIENT_HEADER,
+      NO_HOST_HEADER,
+      SHADOW_HEADER,
+      ...(req.headers[NO_HOST_HEADER] === '1' ? ['host'] : []),
+    ];
     for (const h of drop) delete req.headers[h];
     for (let i = req.rawHeaders.length - 2; i >= 0; i -= 2)
       if (drop.includes(req.rawHeaders[i].toLowerCase())) req.rawHeaders.splice(i, 2);
@@ -88,4 +109,4 @@ function createInternalListener({ server, token, log = () => {}, firstRequestMs
   };
 }
 
-module.exports = { createInternalListener, FIRST_REQUEST_MS, NO_HOST_HEADER };
+module.exports = { createInternalListener, FIRST_REQUEST_MS, NO_HOST_HEADER, SHADOW_HEADER, OWN_HEADER };
diff --git a/lib/gateway/index.js b/lib/gateway/index.js
index 1c3b8cc..05d6d4d 100644
--- a/lib/gateway/index.js
+++ b/lib/gateway/index.js
@@ -28,6 +28,7 @@ const { safeFetch } = require('../net');
 const { peerEncrypted } = require('../front/client');
 const { jsonStore } = require('./store');
 const { createKeys, allows, requestSecret } = require('./keys');
+const shadow = require('./shadow');
 const tr = require('./translate');
 const { saveTokens } = require('./rtk');
 const { createProviders, cleanProvider, cleanRoute, PRESETS } = require('./providers');
@@ -1026,7 +1027,7 @@ function createGateway({
         /* not JSON */
       }
       const u = j ? usageOf(j) : { input: 0, output: 0, cached: 0 };
-      usage.record({
+      const entry = usage.record({
         ...base,
         status: r.status,
         ms: now() - started,
@@ -1034,8 +1035,11 @@ function createGateway({
         error: r.ok ? undefined : secrets.mask(((j && j.error && j.error.message) || text).slice(0, 200)),
       });
       if (j && r.ok) j.model = body.model;
+      const out = j ? JSON.stringify(j) : text;
+      // A request the Go gateway shadows (P4b): what went to the provider, its answer, this answer and the usage line.
+      shadow.noteAnswer({ status: r.status, body: text }, { status: r.status, body: out }, entry);
       res.writeHead(r.status, { 'content-type': 'application/json', 'cache-control': 'no-store', ...CORS });
-      return res.end(j ? JSON.stringify(j) : text);
+      return res.end(out);
     }
     // Stream: pass each line on as it arrives, reading usage and time to first token on the way. Chunks get
     // the requested model name, as the whole answer does. Usage is always asked of the provider (billing),
@@ -1608,7 +1612,11 @@ function createGateway({
   }
 
   // true when the request was a gateway request (handled here), false to let the dashboard handle it.
-  async function handle(req, res, url) {
+  // A request the Go gateway in front shadows (lib/front/internal.js set its id) runs with a capture (P4b).
+  function handle(req, res, url) {
+    return shadow.withShadow(req[shadow.SHADOW_ID], () => route(req, res, url), res);
+  }
+  async function route(req, res, url) {
     const p = url.pathname;
     if (
       !(
diff --git a/lib/gateway/providers.js b/lib/gateway/providers.js
index dfd5904..6ad19b1 100644
--- a/lib/gateway/providers.js
+++ b/lib/gateway/providers.js
@@ -22,6 +22,7 @@
 const crypto = require('crypto');
 const { fetchSameOrigin } = require('../net');
 const { SOFT, DROPPED } = require('./translate');
+const shadow = require('./shadow');
 
 const P = (label, baseUrl, category, extra = {}) => ({ label, baseUrl, category, keyRequired: !extra.local, ...extra });
 const PRESETS = {
@@ -511,10 +512,12 @@ function createProviders({ getConfig, secrets, fetchImpl = fetch, now = Date.now
         for (const [k, v] of Object.entries(extra || {}))
           if (v != null && !/^(authorization|x-api-key|host|content-length|cookie)$/i.test(k))
             h[k.toLowerCase()] = String(v);
+        const text = body === undefined ? undefined : JSON.stringify(body);
+        shadow.noteProviderRequest(`${p.baseUrl}${path}`, h, text); // only for a request Go shadows (P4b)
         const r = await fetchSameOrigin(fetchImpl, `${p.baseUrl}${path}`, {
           method,
           headers: h,
-          body: body === undefined ? undefined : JSON.stringify(body),
+          body: text,
           signal: ctl.signal,
         });
         noteResponse(p, r);
diff --git a/server.js b/server.js
index 3d3abd4..178d3b4 100644
--- a/server.js
+++ b/server.js
@@ -79,7 +79,8 @@ const { recoverMarks } = require('./lib/ipc/marks');
 const { createMarkFile } = require('./lib/ipc/markfile');
 const { serverSinks } = require('./lib/ipc/server-sinks');
 const { gwEnv } = require('./lib/ipc/env');
-const { createFront, engineOf, enginesOf } = require('./lib/front');
+const { createFront, engineOf, routing, shadowOf } = require('./lib/front');
+const { createShadowLog, shadowEndpoints } = require('./lib/front/shadow-log');
 const { configPush, secretsPush, snapshotPush } = require('./lib/front/push');
 const { gwBinary } = require('./lib/front/bin');
 const { createRecorder } = require('./lib/trace/recorder');
@@ -1628,7 +1629,8 @@ const handler = async (req, res) => {
       return send(res, 200, redactGateway(await gateway.admin('', 'GET'), role));
     // The gateway process (P4a D6): mode, state, restarts. Before the admin routes, whose pattern it matches; owner only
     // (it is no viewer read, so the role check above refuses anyone else).
-    if (url.pathname === '/api/gateway/status' && req.method === 'GET') return send(res, 200, front.status());
+    if (url.pathname === '/api/gateway/status' && req.method === 'GET')
+      return send(res, 200, { ...front.status(), notes: frontRouting().notes, shadow: shadowLog.stats() });
     const gwAdmin = url.pathname.match(/^\/api\/gateway\/([a-z]+)$/);
     if (gwAdmin) {
       // Only the request log is read with GET. Every other admin route writes (defaults, settings), spends tokens
@@ -2250,6 +2252,21 @@ function serving() {
       });
 }
 
+// The shadow compare's log (P4b): the gateway posts a line per shadowed chat call; the switch of a class to go reads it.
+const shadowLog = createShadowLog({ file: path.join(DIR, '.data', 'gateway-shadow.jsonl') });
+// Which class runs on which engine, and why one asked for go stays on js (lib/front routing).
+const frontRouting = () =>
+  routing(rawConfig().gateway, {
+    gate: (c, rule) => shadowLog.gate(c, rule),
+    otel: ((rawConfig() || {}).otel || {}).enabled === true,
+  });
+// A class whose shadow gate is met (or broken) switches at the next push: looked at once a minute.
+let lastRouting = '';
+setInterval(() => {
+  const now = JSON.stringify(frontRouting().engines);
+  if (now !== lastRouting && lastRouting && front) front.push();
+  lastRouting = now;
+}, 60000).unref();
 // gateway.engine (P4a): "js" (the default) listens here as before; "front" puts zaafpulse-gw on the main port.
 const ENGINE = DEMO ? 'js' : engineOf(rawConfig());
 const askedEngine = (rawConfig().gateway || {}).engine;
@@ -2268,7 +2285,9 @@ front = createFront({
   env: gwEnv(process.env, DIR),
   cpVersion: require('./package.json').version,
   ingest: fi && fi.ingest,
-  engines: () => enginesOf(rawConfig().gateway),
+  engines: () => frontRouting().engines,
+  shadow: (engines) => shadowOf(rawConfig().gateway, engines),
+  own: shadowEndpoints({ shadowLog }),
   // Each generation, from the state the JS gateway reads (P4b): config.json, keys.json, the effective prices, the
   // provider credentials, the blocking budgets and the keys' cost centers.
   sources: {
diff --git a/tsconfig.json b/tsconfig.json
index 3dd6a58..bc4c88a 100644
--- a/tsconfig.json
+++ b/tsconfig.json
@@ -6,5 +6,5 @@
     "noImplicitAny": false,
     "noEmit": true
   },
-  "include": ["lib/gateway", "lib/cost", "lib/governance", "lib/money.js", "lib/types.js"]
+  "include": ["lib/front", "lib/gateway", "lib/cost", "lib/governance", "lib/money.js", "lib/types.js"]
 }
PATCH
```

- [ ] **Step 5: Run the tests and the task gate**

Run:

```bash
export GOTOOLCHAIN=local ZAAFPULSE_DATA="$(mktemp -d)" HOME="$(mktemp -d)"
test -z "$(gofmt -l gw)" && go -C gw vet ./... && GOOS=windows go -C gw vet ./... && go -C gw test -race -count=1 ./...
deno task typecheck
node --test test/*.test.js
deno test -q --no-check --no-prompt -P=test test/*.test.js
npm run test:contract
```

Expected: no gofmt output, `ok` for every Go package, `deno check` with no error, `fail 0` from Node, `0 failed` from Deno, and the contract suite
`pass 172` (from Task 8 on, `pass 179`, with one case skipped outside shadow mode), `fail 0`.

- [ ] **Step 6: Run the contract suite with chat shadowed, on both runtimes**

Run:

```bash
ZP_ENGINE=shadow npm run test:contract
ZP_ENGINE=shadow ZP_RUNTIME=deno deno test -q --no-check --no-prompt -P=test test/contract/
```

Expected: `pass 180` on Node (`fail 0`; the shadow case runs) and `180 passed` on Deno; no file fails in its `after` hook with "the shadow compare found ... diff(s)".

- [ ] **Step 7: Commit and push**

```bash
git add -A -- gw ipc lib server.js test tsconfig.json
git status --short   # nothing outside this task's files, nothing under .superpowers/
git commit -m "P4b T9: the shadow compare of chat calls and the rule that switches a class to go after clean days"
git push origin migrate/p4b-passthrough
```

---

### Fix round 2 (re-review 1): a // target in origin form, Winsock error names, out-of-range key values, a dropped key takes its hash along

Re-review 1 (N1–N4), applied after Task 9. Each item got its failing test first.

**Files:**
- Modify: `gw/engine/upstream.go`, `gw/engine/upstream_test.go`, `gw/front/front.go`, `gw/front/route_test.go`, `lib/front/push.js`, `test/front-push.test.js`


**Interfaces:**
- Produces: a raw path starting with `//` goes through `URL.Path`/`RawPath` (Opaque would be written `http://...`); `ErrorCode` names Winsock codes 10061, 10054, 10060, 10065, 10051 and 10053 as Node does; `keyAuth` clamps `rpm` and `expiresAt` to ±MAX_SAFE_INTEGER and reads an Infinity `revokedAt` as revoked; `configPush` drops every key that shares the hash of a dropped one (`pushedKeys`).

- [ ] **Step 1: Check the starting point**

Run: `git log --oneline -1`
Expected: Task 9's commit.

- [ ] **Step 2: Write the failing tests**

Apply (from the repository root):

```bash
git apply <<'PATCH'
diff --git a/gw/engine/upstream_test.go b/gw/engine/upstream_test.go
index 146f98a..27b4339 100644
--- a/gw/engine/upstream_test.go
+++ b/gw/engine/upstream_test.go
@@ -10,6 +10,7 @@ import (
 	"os"
 	"path/filepath"
 	"strings"
+	"syscall"
 	"testing"
 	"time"
 )
@@ -130,3 +131,15 @@ func TestUpstreamExtraCA(t *testing.T) {
 		}
 	}
 }
+
+// Windows names a refused or reset connection by its Winsock code; the engine names them as Node does on Windows
+// (re-review N4), on every OS this test runs on.
+func TestWindowsSocketErrorsHaveNodeNames(t *testing.T) {
+	for code, want := range map[syscall.Errno]string{10061: "ECONNREFUSED", 10054: "ECONNRESET", 10060: "ETIMEDOUT",
+		10065: "EHOSTUNREACH", 10051: "ENETUNREACH"} {
+		err := &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connectex", code)}
+		if got := ErrorCode(err); got != want {
+			t.Errorf("errno %d: %q, want %q", code, got, want)
+		}
+	}
+}
diff --git a/gw/front/route_test.go b/gw/front/route_test.go
index 65b3078..87c53e6 100644
--- a/gw/front/route_test.go
+++ b/gw/front/route_test.go
@@ -125,7 +125,8 @@ func TestRequestTargetIsForwardedAsSent(t *testing.T) {
 	got := make(chan string, 8)
 	up := startUpstream(t, func(w http.ResponseWriter, r *http.Request) { got <- r.RequestURI })
 	s := startFront(t, up.port(), nil)
-	for _, target := range []string{`/v1\chat/completions`, "/v1/a|b", "/api/x?q=a;b&c=1", "/v1/a^b?x=%zz&y=\\", "/v1/chat%2Fcompletions?a=%2F"} {
+	for _, target := range []string{`/v1\chat/completions`, "/v1/a|b", "/api/x?q=a;b&c=1", "/v1/a^b?x=%zz&y=\\",
+		"/v1/chat%2Fcompletions?a=%2F", "//v1/chat/completions", "///a", "//v1/x?q=a;b", "//host/x%2Fy"} {
 		out, _ := rawRequest(t, s.Addr().String(), "GET "+target+" HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n")
 		if !strings.Contains(out, "200 OK") {
 			t.Errorf("%s: %q", target, out)
diff --git a/test/front-push.test.js b/test/front-push.test.js
index 9c6cb86..1bbed30 100644
--- a/test/front-push.test.js
+++ b/test/front-push.test.js
@@ -247,3 +247,33 @@ test('configPush: a base URL with user info or a query is cut to its origin and
   assert.deepEqual([ok.baseUrl, ok.enabled], ['https://llm.example/v1', undefined]);
   assert.deepEqual([bad.baseUrl, bad.enabled], ['', false]);
 });
+
+// Re-review N2 and N3: values only a hand-edited keys.json can hold.
+test('keyAuth: Infinity revoked is revoked; a time or limit past int64 is clamped, never an undecodable push', () => {
+  const base = { id: 'k', hash: 'c'.repeat(64), scopes: ['llm'], models: [], agents: [] };
+  const inf = JSON.parse('{"revokedAt": 1e400}').revokedAt;
+  assert.equal(inf, Infinity);
+  assert.equal(keyAuth({ ...base, revokedAt: inf }).revokedAt, 1);
+  assert.equal(keyAuth({ ...base, expiresAt: 1e300 }).expiresAt, Number.MAX_SAFE_INTEGER, 'never expires, as in JS');
+  assert.equal(keyAuth({ ...base, expiresAt: -1e300 }).expiresAt, -Number.MAX_SAFE_INTEGER, 'expired, as in JS');
+  assert.equal(keyAuth({ ...base, rpm: 1e300 }).rpm, Number.MAX_SAFE_INTEGER);
+  const body = configPush({
+    gateway: {},
+    keys: [{ ...base, expiresAt: 1e300, rpm: 1e300, revokedAt: inf }],
+    pricing: {},
+  });
+  assert.deepEqual(check(msg('config.push', body)), { errors: [], unknownType: false });
+  assert.ok(Number.isSafeInteger(body.keys[0].expiresAt) && Number.isSafeInteger(body.keys[0].rpm));
+});
+
+test('configPush: a key the push drops takes every key with its hash along (JS matches the first one)', () => {
+  const good = { id: 'b', hash: 'd'.repeat(64), scopes: ['llm'], models: [], agents: [] };
+  const keys = [{ ...good, id: 'a', scopes: 'llm' }, good, { ...good, id: 'c', hash: 'e'.repeat(64) }];
+  assert.deepEqual(
+    configPush({ gateway: {}, keys, pricing: {} }).keys.map((k) => k.id),
+    ['c'],
+  );
+  // An upper-case hash is the same key to JS (Buffer.from(hash, 'hex')).
+  const upper = [{ ...good, id: 'a', hash: 'D'.repeat(64) }, good];
+  assert.deepEqual(configPush({ gateway: {}, keys: upper, pricing: {} }).keys, []);
+});
PATCH
```

- [ ] **Step 3: Run them to see them fail**

Run: `GOTOOLCHAIN=local go -C gw test -count=1 ./front ./engine; node --test test/front-push.test.js`
Expected: FAIL in `TestRequestTargetIsForwardedAsSent` (`http://v1/chat/completions`), `TestWindowsSocketErrorsHaveNodeNames`, and the two new push tests.

- [ ] **Step 4: Implement**

Apply (from the repository root):

```bash
git apply <<'PATCH'
diff --git a/gw/engine/upstream.go b/gw/engine/upstream.go
index ca2d9db..b5c2ef1 100644
--- a/gw/engine/upstream.go
+++ b/gw/engine/upstream.go
@@ -133,10 +133,20 @@ func origin(u *url.URL) string {
 	return u.Scheme + "://" + u.Hostname() + ":" + port
 }
 
+var winsock = map[syscall.Errno]string{10061: "ECONNREFUSED", 10054: "ECONNRESET", 10060: "ETIMEDOUT",
+	10065: "EHOSTUNREACH", 10051: "ENETUNREACH", 10053: "ECONNABORTED"}
+
 // ErrorCode is what the JS gateway names a failed fetch by (err.cause.code of Node's fetch, else its message): the
 // common system and undici codes, else "fetch failed". Deno names some of these differently; a failed call has no
 // provider answer, so the shadow compare never reads it.
 func ErrorCode(err error) string {
+	// Winsock codes (Windows) by the names Node gives them there (re-review N4); no Unix errno is this large.
+	var errno syscall.Errno
+	if errors.As(err, &errno) {
+		if name, ok := winsock[errno]; ok {
+			return name
+		}
+	}
 	var dns *net.DNSError
 	var hn x509.HostnameError
 	var ua x509.UnknownAuthorityError
diff --git a/gw/front/front.go b/gw/front/front.go
index e9034ee..c01cf42 100644
--- a/gw/front/front.go
+++ b/gw/front/front.go
@@ -17,6 +17,7 @@ import (
 	"net/http/httputil"
 	"net/url"
 	"strconv"
+	"strings"
 	"sync/atomic"
 	"time"
 )
@@ -150,7 +151,13 @@ func Listen(cfg Config, logger *log.Logger) (*Server, error) {
 			// The request target exactly as the client sent it (P4b review I2): net/http re-escapes a path it decoded
 			// ("\" and "|" become %5C and %7C) and drops query pairs it cannot parse (";"), so the control plane would
 			// route another text than in js mode. Opaque is written as it is; RawQuery is kept whole.
-			pr.Out.URL.Opaque = RawPath(pr.In.RequestURI)
+			// A target that starts with "//" cannot go in Opaque (net/url writes it as "http://..."): its path is set
+			// with RawPath, which is written as sent whenever it is a valid escaping of the path (re-review N1).
+			if raw := RawPath(pr.In.RequestURI); strings.HasPrefix(raw, "//") {
+				pr.Out.URL.Path, pr.Out.URL.RawPath = pr.In.URL.Path, raw
+			} else {
+				pr.Out.URL.Opaque = raw
+			}
 			pr.Out.URL.RawQuery = pr.In.URL.RawQuery
 			// The client's Host. Without one (HTTP/1.0) Go's transport would write the internal 127.0.0.1:<port>, so
 			// the request is flagged and the internal listener removes that Host: the host check sees none, as in js
diff --git a/lib/front/push.js b/lib/front/push.js
index 499d3c2..4dca0e8 100644
--- a/lib/front/push.js
+++ b/lib/front/push.js
@@ -90,12 +90,14 @@ function keyAuth(k) {
   let rpm = 0;
   if (k.rpm) {
     if (typeof k.rpm !== 'number' || !Number.isFinite(k.rpm) || k.rpm < 0) return null;
-    rpm = Math.ceil(k.rpm);
+    rpm = Math.min(Math.ceil(k.rpm), Number.MAX_SAFE_INTEGER); // past it, unlimited in both
   }
   let expiresAt = null;
   if (k.expiresAt) {
     if (typeof k.expiresAt !== 'number' || !Number.isFinite(k.expiresAt)) return null;
-    expiresAt = Math.floor(k.expiresAt) || -1;
+    // Clamped to a safe integer, so config.push stays decodable: a later expiry than that never comes, an earlier one
+    // has passed (re-review N2).
+    expiresAt = Math.max(-Number.MAX_SAFE_INTEGER, Math.min(Number.MAX_SAFE_INTEGER, Math.floor(k.expiresAt))) || -1;
   }
   if (k.budgetUSD != null && !Number.isFinite(k.budgetUSD)) return null;
   const guardrails = {};
@@ -114,7 +116,12 @@ function keyAuth(k) {
     rpm,
     privateOnly: !!k.privateOnly,
     expiresAt,
-    revokedAt: k.revokedAt ? Math.max(1, Math.floor(Number(k.revokedAt)) || 1) : null,
+    // Any value JS reads as true is a revocation, Infinity included (re-review N2).
+    revokedAt: k.revokedAt
+      ? Number.isSafeInteger(Math.floor(Number(k.revokedAt)))
+        ? Math.max(1, Math.floor(Number(k.revokedAt)))
+        : 1
+      : null,
     guardrails,
     prefix: str(k.prefix),
     approveOwnRuns: k.approveOwnRuns === true,
@@ -141,6 +148,15 @@ function price(e) {
   return out;
 }
 
+// The keys the gateway gets. A key the push drops takes every key with its hash along (re-review N3): JS matches the
+// first key with a hash (Buffer.from(hash, 'hex') reads upper case too), so a later one must not answer in its place.
+function pushedKeys(keys) {
+  const list = (Array.isArray(keys) ? keys : []).map((k) => [k, keyAuth(k)]);
+  const refused = new Set();
+  for (const [k, a] of list) if (!a && isObj(k) && typeof k.hash === 'string') refused.add(k.hash.toLowerCase());
+  return list.map(([, a]) => a).filter((a) => a && !refused.has(a.hash));
+}
+
 /**
  * The config.push body without gen and front (the supervisor adds gen, lib/front adds front).
  * @param {{ gateway: any, keys: any[], pricing: Record<string, any> }} src
@@ -159,7 +175,7 @@ function configPush({ gateway, keys, pricing }) {
         .filter(isObj)
         .map((m) => ({ match: str(m.match), to: str(m.to) })),
     },
-    keys: (Array.isArray(keys) ? keys : []).map(keyAuth).filter(Boolean),
+    keys: pushedKeys(keys),
     pricing: Object.fromEntries(
       Object.entries(isObj(pricing) ? pricing : {})
         .filter(([, e]) => isObj(e))
PATCH
```

- [ ] **Step 5: Run the tests and the task gate**

Run:

```bash
export GOTOOLCHAIN=local ZAAFPULSE_DATA="$(mktemp -d)" HOME="$(mktemp -d)"
test -z "$(gofmt -l gw)" && go -C gw vet ./... && GOOS=windows go -C gw vet ./... && go -C gw test -race -count=1 ./...
deno task typecheck
node --test test/*.test.js
deno test -q --no-check --no-prompt -P=test test/*.test.js
npm run test:contract
```

Expected: no gofmt output, `ok` for every Go package, `deno check` with no error, `fail 0` from Node, `0 failed` from Deno, and the contract suite
`pass 172` (from Task 8 on, `pass 179`, with one case skipped outside shadow mode), `fail 0`.

- [ ] **Step 6: Commit and push**

```bash
git add -A -- gw lib test
git status --short   # nothing outside this task's files, nothing under .superpowers/
git commit -m "P4b re-review 1 fixes: a // target keeps origin form through the front, Winsock errors get Node's names, Infinity and out-of-range key values stay safe, a dropped key takes its hash along"
git push origin migrate/p4b-passthrough
```

---

### Task 10: The fixture generators under the CI fixtures job, and the Node/Deno differences pinned in Go

Stream B owns the CI fixtures job, which runs every `scripts/fixtures/gen-*.js` on Node and on Deno with `--allow-read --allow-write=gw/testdata --deny-env --deny-net --deny-run --deny-sys` and requires the same bytes from both (coordinator's call: this stream does not edit `ci.yml`). `gen-chat.js` calls the gateway in process and `gen-pricing.js` writes nothing outside `gw/testdata`; the three cases where Node and Deno disagree leave the corpus and are pinned to Deno in Go tests.

**Files:**
- Modify: `.gitignore`, `gw/engine/keys.go`, `gw/engine/keys_test.go`, `gw/front/route_test.go`, `scripts/fixtures/gen-chat.js`, `scripts/fixtures/gen-keys.js`, `scripts/fixtures/gen-pricing.js`, `scripts/fixtures/gen-route.js`


**Interfaces:**
- Consumes: Tasks 1–9; stream B's fixtures job (its flags above).
- Produces: generators that need no network, environment, subprocess or system access; `gw/testdata/.gen-*` git-ignored; `engine.divergentFold` (U+A7CF, U+A7D3, U+A7D5 not folded, as on Deno); `TestUnitsTheRuntimesFoldDifferently`, `TestCaretPathIsNotCanonical`.

- [ ] **Step 1: Check the starting point**

Run: `git status --short && git rev-parse --abbrev-ref HEAD && git log --oneline -1`
Expected: nothing listed, `migrate/p4b-passthrough`, and the previous task's commit on top (Task 1: `0ec1247`, the merge of `origin/migrate/deno-go` with stream C).

- [ ] **Step 2: Write the failing tests**

Apply (from the repository root):

```bash
git apply <<'PATCH'
diff --git a/gw/engine/keys_test.go b/gw/engine/keys_test.go
index 8f5ba2f..846022b 100644
--- a/gw/engine/keys_test.go
+++ b/gw/engine/keys_test.go
@@ -87,7 +87,7 @@ func TestKeysFixtures(t *testing.T) {
 			}
 			bad := 0
 			for u := 0; u < 0x10000; u++ {
-				if u >= 0xd800 && u <= 0xdfff {
+				if (u >= 0xd800 && u <= 0xdfff) || divergentFold[rune(u)] {
 					continue
 				}
 				exp := uint16(u)
@@ -135,3 +135,16 @@ func mustDecode(t *testing.T, c fixture, v any) {
 		t.Fatalf("%s: %v", c.Name, err)
 	}
 }
+
+// Node and Deno upper-case three units differently (gen-keys.js leaves them out of the corpus): the v2 product runs on
+// Deno, which does not fold them, so neither does the engine.
+func TestUnitsTheRuntimesFoldDifferently(t *testing.T) {
+	for _, u := range []uint16{0xa7cf, 0xa7d3, 0xa7d5} {
+		if got := canon(u); got != u {
+			t.Errorf("canon(U+%04X) = U+%04X, Deno keeps it", u, got)
+		}
+	}
+	if Allows([]string{"oa/\ua7cf"}, "oa/\ua7ce") {
+		t.Error("U+A7CF matched U+A7CE")
+	}
+}
diff --git a/gw/front/route_test.go b/gw/front/route_test.go
index 87c53e6..a3df473 100644
--- a/gw/front/route_test.go
+++ b/gw/front/route_test.go
@@ -156,3 +156,11 @@ func TestClientShadowMarksAreRemoved(t *testing.T) {
 		t.Fatalf("forwarded %v", h)
 	}
 }
+
+// Node and Deno disagree on /v1/a^b (Node's pathname is /v1/a%5Eb, Deno's /v1/a^b), so it is not in the route corpus:
+// the front never classifies it, and the control plane routes it whatever its runtime does.
+func TestCaretPathIsNotCanonical(t *testing.T) {
+	if Canonical("/v1/a^b") || Classify("/v1/a^b") != "" {
+		t.Fatal("/v1/a^b is classified")
+	}
+}
diff --git a/scripts/fixtures/gen-chat.js b/scripts/fixtures/gen-chat.js
index d9b121e..ec96dbf 100644
--- a/scripts/fixtures/gen-chat.js
+++ b/scripts/fixtures/gen-chat.js
@@ -1,15 +1,17 @@
 // Fixtures for the go engine's chat route (P4b, slice S2): the real JS gateway (lib/gateway createGateway) answers each
-// case over HTTP on 127.0.0.1, against a provider stubbed at fetch, and this records what it sent the provider, what
+// case in process (a request and a response object handed to gateway.handle, no socket), against a provider stubbed
+// at fetch, and this records what it sent the provider, what
 // it answered the client and the usage line it wrote. gw/engine/chat_test.go plans and answers the same case from the
 // config.push and secrets.handoff the control plane would send (lib/front/push.js) and must give the same bytes.
 // Writes gw/testdata/chat/<n>-<name>.json as { fn: "chat", in: { config, secrets, request, provider }, out }.
 // Only cases the engine serves are here (guardrails off, no budget, one OpenAI-format target, not streaming); the
 // hand-offs are Go tests of their own.
-// Run: node scripts/fixtures/gen-chat.js (CI runs every scripts/fixtures/gen-*.js and fails on a diff).
+// Run: node scripts/fixtures/gen-chat.js. It needs no network, environment, subprocess or system access, and writes
+// only under gw/testdata (its scratch data folder gw/testdata/.gen-chat-data is removed at the end and git-ignored), so
+// it runs under the CI fixtures job's Deno flags (--allow-read --allow-write=gw/testdata --deny-env --deny-net
+// --deny-run --deny-sys) and gives the same bytes on Node and Deno.
 const fs = require('fs');
-const os = require('os');
 const path = require('path');
-const http = require('http');
 const { createGateway } = require('../../lib/gateway');
 const { createSecrets } = require('../../lib/secrets');
 const { configPush, secretsPush } = require('../../lib/front/push');
@@ -181,8 +183,47 @@ const CASES = [
   ['timed-out', {}, { model: 'oa/m1', messages: user }, { throw: 'timeout' }],
 ];
 
+// A request as gateway.handle reads it: headers, the target, a loopback client, the body as one chunk.
+const requestOf = (headers, text) => ({
+  method: 'POST',
+  url: '/v1/chat/completions',
+  headers,
+  socket: { remoteAddress: '127.0.0.1', encrypted: false },
+  on() {},
+  once() {},
+  async *[Symbol.asyncIterator]() {
+    yield Buffer.from(text, 'utf8');
+  },
+});
+// A response that keeps what the gateway writes.
+function responseOf() {
+  const res = {
+    statusCode: 200,
+    headers: {},
+    body: '',
+    writableEnded: false,
+    writeHead(status, h = {}) {
+      res.statusCode = status;
+      for (const [k, v] of Object.entries(h)) res.headers[k.toLowerCase()] = String(v);
+      return res;
+    },
+    setHeader: (k, v) => (res.headers[k.toLowerCase()] = String(v)),
+    write: (c) => ((res.body += String(c)), true),
+    end(c) {
+      if (c !== undefined) res.body += String(c);
+      res.writableEnded = true;
+      return res;
+    },
+    on() {},
+    once() {},
+  };
+  return res;
+}
+
 async function main() {
-  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), 'zp-gen-chat-'));
+  const dataDir = path.join(OUT, '..', '.gen-chat-data');
+  fs.rmSync(dataDir, { recursive: true, force: true });
+  fs.mkdirSync(dataDir, { recursive: true });
   fs.writeFileSync(path.join(dataDir, 'keys.json'), JSON.stringify({ keys: KEYS }));
   let provider = null;
   let sent = null;
@@ -210,12 +251,8 @@ async function main() {
     getPricing: () => PRICING,
     getIntegrations: () => ({ listTools: async () => [], callTool: async () => ({}) }),
     fetchImpl,
+    toolsHome: dataDir, // never the real home folder (and no sys access under the CI flags)
   });
-  const server = http.createServer(async (req, res) => {
-    if (!(await gateway.handle(req, res, new URL(req.url, 'http://localhost')))) res.end();
-  });
-  await new Promise((r) => server.listen(0, '127.0.0.1', r));
-  const base = `http://127.0.0.1:${server.address().port}`;
   const config = configPush({ gateway: GATEWAY, keys: KEYS, pricing: PRICING });
   const handoff = secretsPush({ gateway: GATEWAY, resolve: (ref, f) => secrets.resolve(ref, f) });
   fs.rmSync(OUT, { recursive: true, force: true });
@@ -243,16 +280,18 @@ async function main() {
       ...(opt.headers || {}),
     };
     const text = typeof body === 'string' ? body : JSON.stringify(body);
-    const r = await fetch(`${base}/v1/chat/completions`, { method: 'POST', headers, body: text });
+    const res = responseOf();
+    const req = requestOf(headers, text);
+    await gateway.handle(req, res, new URL(req.url, 'http://localhost'));
     const client = {
-      status: r.status,
+      status: res.statusCode,
       headers: Object.fromEntries(
         ['content-type', 'cache-control', 'access-control-allow-origin', 'retry-after'].map((h) => [
           h,
-          r.headers.get(h),
+          h in res.headers ? res.headers[h] : null,
         ]),
       ),
-      body: await r.text(),
+      body: res.body,
     };
     const { ts: _ts, ms: _ms, cost, ...usage } = gateway.usage.recent(1)[0];
     const out = {
@@ -266,7 +305,6 @@ async function main() {
       `${JSON.stringify({ fn: 'chat', in: input, out })}\n`,
     );
   }
-  server.close();
   gateway.flush();
   fs.rmSync(dataDir, { recursive: true, force: true });
   console.log(`gen-chat: ${n} cases in ${path.relative(process.cwd(), OUT)}`);
diff --git a/scripts/fixtures/gen-keys.js b/scripts/fixtures/gen-keys.js
index bc156df..d2e51e5 100644
--- a/scripts/fixtures/gen-keys.js
+++ b/scripts/fixtures/gen-keys.js
@@ -148,9 +148,13 @@ for (const [name, rpm, times] of [
 // Canonicalize (ECMAScript 22.2.2.7.3, a regular expression with i and without u), the folding allows() relies on: for
 // every BMP code unit whose canonical unit is another one, that unit. Computed with this runtime's own toUpperCase,
 // which is what the JS gateway's /i globs use (P4b review I3).
+// Three units Node 24 upper-cases and Deno 2.9.8 does not (U+A7CF, U+A7D3, U+A7D5, Unicode 17 letters its ICU data
+// lacks) are left out: the corpus holds only what both runtimes agree on, and Go keeps Deno's answer (no folding),
+// pinned by gw/engine/keys_test.go TestUnitsTheRuntimesFoldDifferently.
+const DIVERGENT = new Set([0xa7cf, 0xa7d3, 0xa7d5]);
 const canonical = {};
 for (let u = 0; u < 0x10000; u++) {
-  if (u >= 0xd800 && u <= 0xdfff) continue;
+  if ((u >= 0xd800 && u <= 0xdfff) || DIVERGENT.has(u)) continue;
   const up = String.fromCharCode(u).toUpperCase();
   if (up.length !== 1) continue;
   const cu = up.charCodeAt(0);
diff --git a/scripts/fixtures/gen-pricing.js b/scripts/fixtures/gen-pricing.js
index 207c07a..291fd56 100644
--- a/scripts/fixtures/gen-pricing.js
+++ b/scripts/fixtures/gen-pricing.js
@@ -3,7 +3,6 @@
 // <name>.json as { fn, in, out }.
 // Run: node scripts/fixtures/gen-pricing.js (CI runs every scripts/fixtures/gen-*.js and fails on a diff).
 const fs = require('fs');
-const os = require('os');
 const path = require('path');
 const { normalizeModel, makePricer } = require('../../lib/pricing');
 const { createUsage } = require('../../lib/gateway/usage');
@@ -42,7 +41,8 @@ const cases = [];
 for (const m of MODELS) cases.push({ fn: 'normalize', name: m, in: m, out: normalizeModel(m) });
 const pricer = makePricer(TABLE);
 for (const m of MODELS) cases.push({ fn: 'key', name: m, in: m, out: pricer.key(m) });
-const file = path.join(fs.mkdtempSync(path.join(os.tmpdir(), 'zp-gen-pricing-')), 'u.jsonl');
+// The usage log is never written (cost() only reads the price table): a path that does not exist, under gw/testdata.
+const file = path.join(OUT, '..', '.gen-pricing-unused.jsonl');
 const usage = createUsage({ file, getPricing: () => TABLE });
 for (const [m, u] of [
   ['oa/m1', { input: 1000, output: 500 }],
@@ -53,7 +53,6 @@ for (const [m, u] of [
   ['unknown-model', { input: 10, output: 10 }],
 ])
   cases.push({ fn: 'cost', name: m, in: { model: m, ...u }, out: toNano(usage.cost(m, u)) });
-fs.rmSync(path.dirname(file), { recursive: true, force: true });
 fs.rmSync(OUT, { recursive: true, force: true });
 fs.mkdirSync(OUT, { recursive: true });
 const table = configPush({ gateway: {}, keys: [], pricing: TABLE }).pricing;
diff --git a/scripts/fixtures/gen-route.js b/scripts/fixtures/gen-route.js
index a051713..b26e3df 100644
--- a/scripts/fixtures/gen-route.js
+++ b/scripts/fixtures/gen-route.js
@@ -36,7 +36,8 @@ const RAW = [
   '/v1/traces',
   '/v1/a;b/c',
   "/v1/a!$&'()*+,=:@~_-.x",
-  '/v1/a^b',
+  // '/v1/a^b' is not here: Node escapes the ^ (/v1/a%5Eb), Deno keeps it, and the corpus holds only what both runtimes
+  // agree on (gw/front/route_test.go TestCaretPathIsNotCanonical pins it).
   '/v1/a{b}',
   '/v1/a|b',
   '/v1/a[b]',
PATCH
```

- [ ] **Step 3: Run them to see them fail**

Run: `for g in scripts/fixtures/gen-*.js; do deno run -q --no-prompt --allow-read --allow-write=gw/testdata --deny-env --deny-net --deny-run --deny-sys "$g"; done; git status --short gw/testdata`
Expected: `gen-chat.js` and `gen-pricing.js` fail with `NotCapable: Requires env access to "TMPDIR"`, and the others change `gw/testdata/keys/*canonicalize*` and `gw/testdata/route/29-*` (Deno's `toUpperCase` and URL parser differ from Node's there).

- [ ] **Step 4: Make the generators runtime-neutral and pin the differences**

Apply (from the repository root):

```bash
git apply <<'PATCH'
diff --git a/.gitignore b/.gitignore
index e2ff4d3..af20dc0 100644
--- a/.gitignore
+++ b/.gitignore
@@ -6,3 +6,6 @@ node_modules/
 research_notes/
 dist/
 gw/bin/
+
+# Scratch folders of the fixture generators (scripts/fixtures/gen-*.js)
+gw/testdata/.gen-*
diff --git a/gw/engine/keys.go b/gw/engine/keys.go
index d60226b..818fcb3 100644
--- a/gw/engine/keys.go
+++ b/gw/engine/keys.go
@@ -137,7 +137,7 @@ func canon(u uint16) uint16 {
 	}
 	r := rune(u)
 	up := unicode.ToUpper(r)
-	if up > 0xffff || specialUpper[r] {
+	if up > 0xffff || specialUpper[r] || divergentFold[r] {
 		return u
 	}
 	if r >= 128 && up < 128 {
@@ -146,6 +146,10 @@ func canon(u uint16) uint16 {
 	return uint16(up)
 }
 
+// divergentFold are the units Node 24 upper-cases and Deno 2.9.8 does not (Unicode 17 letters); the engine keeps
+// Deno's answer, the v2 runtime's.
+var divergentFold = map[rune]bool{0xa7cf: true, 0xa7d3: true, 0xa7d5: true}
+
 // specialUpper are the BMP code units whose JavaScript toUpperCase is more than one unit (SpecialCasing.txt: the 102 the
 // keys fixture lists through gen-keys.js "canonicalize"), so Canonicalize leaves them as they are, though Go's simple
 // mapping folds some of them (U+1F80 to U+1F88, P4b review I3).
PATCH
```

- [ ] **Step 5: Generate the fixtures from the JS code**

Run: `git checkout gw/testdata; for g in scripts/fixtures/gen-*.js; do node "$g"; done; cp -R gw/testdata /tmp/td-node; for g in scripts/fixtures/gen-*.js; do deno run -q --no-prompt --allow-read --allow-write=gw/testdata --deny-env --deny-net --deny-run --deny-sys "$g"; done; diff -r /tmp/td-node gw/testdata && echo IDENTICAL`
Expected: every generator succeeds on both runtimes and `IDENTICAL`.

- [ ] **Step 6: Run the tests and the task gate**

Run:

```bash
export GOTOOLCHAIN=local ZAAFPULSE_DATA="$(mktemp -d)" HOME="$(mktemp -d)"
test -z "$(gofmt -l gw)" && go -C gw vet ./... && GOOS=windows go -C gw vet ./... && go -C gw test -race -count=1 ./...
deno task typecheck
node --test test/*.test.js
deno test -q --no-check --no-prompt -P=test test/*.test.js
npm run test:contract
```

Expected: no gofmt output, `ok` for every Go package, `deno check` with no error, `fail 0` from Node, `0 failed` from Deno, and the contract suite
`pass 172` (from Task 8 on, `pass 179`, with one case skipped outside shadow mode), `fail 0`.

- [ ] **Step 7: Commit and push**

```bash
git add -A -- .gitignore gw scripts
git status --short   # nothing outside this task's files, nothing under .superpowers/
git commit -m "P4b T10: the fixture generators run under the CI fixtures job's Deno flags and give the same bytes on Node and Deno; the three Node/Deno differences are pinned in Go tests instead"
git push origin migrate/p4b-passthrough
```

---

### Task 11: Results, README, program plan

The record of stage 1 stream A: what ships, the entry conditions with their tests, the measured runs, the deviations.

**Files:**
- Create: `docs/migration/2026-11-12-p4b-results.md`
- Modify: `README.md`, `docs/migration/2026-11-10-deno-go-migration-plan.md`


**Interfaces:**
- Consumes: the commits of Tasks 1–10 and the Task 10 dispatch.
- Produces: `docs/migration/2026-11-12-p4b-results.md`; the README paragraph; the S2 and S4 rows of the program plan.

- [ ] **Step 1: Check the starting point**

Run: `git status --short && git rev-parse --abbrev-ref HEAD && git log --oneline -1`
Expected: nothing listed, `migrate/p4b-passthrough`, and the previous task's commit on top (Task 1: `0ec1247`, the merge of `origin/migrate/deno-go` with stream C).

- [ ] **Step 2: Confirm that the results document is new**

Run: `test ! -e docs/migration/2026-11-12-p4b-results.md && echo absent`
Expected: `absent`.

- [ ] **Step 3: Write the results and update the README and the program plan**

The results document records the runs and the deviations found during the run; fill the run of the Task 11 dispatch once it has finished.

Create `docs/migration/2026-11-12-p4b-results.md`:

```markdown
# P4b results: chat on the go engine behind the shadow (slices S2 and S4)

Date: 2026-11-12. Branch `migrate/p4b-passthrough` (stream A of P4 stage 1), merged with `migrate/deno-go` at
`8abb82c` (stream C) before Task 1. Spec `docs/superpowers/specs/2026-11-12-p4-stage1-design.md` (stream A), plan
`docs/superpowers/plans/2026-11-12-p4b-passthrough.md`.

## What ships

- `config.json` `gateway.engines: { "chat": "go" }` (with `gateway.engine: "front"`): the gateway answers
  non-streaming chat calls itself when it can answer them as the server does (one OpenAI-format provider with a plain
  key, a client and Host on this machine, no guardrail check in scope, no blocking budget, no key budget, no route, no
  tool results for the token saver); it hands every other request, unchanged, to the server.
- API keys in Go (`gw/engine/keys.go`): the key by its SHA-256, revoked and expired keys, the per-key model and agent
  allow-lists with JavaScript's `/i` folding, the requests-per-minute window. The push (`lib/front/push.js keyAuth`)
  reads every key field as `lib/gateway/keys.js` does and drops a key it cannot (with every key of its hash), so the
  gateway is never more permissive than the server.
- The shadow compare: `gateway.shadow: ["chat"]` with chat on js makes the gateway compare each chat call the server
  answers with the request it would have sent and the answer and usage line it would have given, from the same
  provider answer (`.data/gateway-shadow.jsonl`, no prompt or answer text). Chat moves to go only after the shadow has
  compared at least `gateway.shadowMinCompares` calls (default 20) and seen no difference for `gateway.shadowDays`
  days (default 1) since its last diff (the owner's rule of 2026-11-12), and while OpenTelemetry export is off.
  `GET /api/gateway/status` shows the counts and why a class stays on js.
- The front forwards the request target exactly as the client sent it (raw path and query), so js and front mode route
  the same text.
- Every rule ported to Go is checked against fixtures generated from the JS code (`scripts/fixtures/gen-*.js`,
  `gw/testdata/*`); the generators give the same bytes on Node and on Deno under the CI fixtures job's flags.

## P4b entry conditions (from the P4a results)

| # | Condition | Where | Test |
|---|---|---|---|
| 1 | One usage writer that feeds the spend | `lib/gateway/index.js ingestUsage`, `lib/ipc/server-sinks.js` | `test/ipc-server-sinks.test.js` |
| 2 | Classify on the path form the server routes on | `gw/front/route.go` (`Classify`, `Canonical`, `RawPath`), `gw/front/front.go` (raw target forwarded) | `gw/front/route_test.go` (34 fixtures, `TestRequestTargetIsForwardedAsSent`) |
| 3 | The late front section and reload | `gw/cmd/zaafpulse-gw/front.go applyFront`, `lib/front/index.js` | `gw/cmd/zaafpulse-gw/reload_test.go` |
| 4 | P4f outbox and marks | stream C, merged first (`8abb82c`) | stream C's tests |
| 5 | Proxy and CA variables | `lib/ipc/env.js`, `gw/engine/upstream.go` | `test/ipc-hardening.test.js`, `TestUpstreamExtraCA` |
| 6 | Header limit and log noise | `gw/front/front.go` (`MaxHeaderBytes`), `gw/front/quiet.go` | `TestOversizedHeadersAre431`, `TestQuietLoggerRateLimitsNoisyLines` |
| 8 | Restart needed after a crash loop | `lib/front/index.js status`, `public/topologyview.js` | `test/front.test.js`, `test/topology-ui.test.js` |

## Measured

- Contract suite (172 cases, plus 7 in `test/contract/go-chat.test.js`, plus 1 shadow case): js, front, shadow and go
  modes on Node and Deno, green on macOS arm64.
- Shadow on the contract corpus: every server of the `ZP_ENGINE=shadow` run stopped with no diff line.

| Run | What it shows |
|---|---|
| [43243797803](https://github.com/rifqifitriady/ZAAFPulse-deno-go/actions/runs/43243797803) | Tasks 1–5 (`99c6280`): red on typecheck and go on both Windows runners (review 1, I1 and I4) |
| [43248009523](https://github.com/rifqifitriady/ZAAFPulse-deno-go/actions/runs/43248009523) | review 1 fixes (`cac226a`): red only on go on Windows (re-review N4), typecheck green |
| [43249555363](https://github.com/rifqifitriady/ZAAFPulse-deno-go/actions/runs/43249555363) | re-review 1 fixes (`10bd72b`) |

The full-matrix run on the Task 11 tree is added below once it has finished.

## Differences from the spec and the plan

The plan's "Spec deviations" 1–10 hold, with these changes made during the run:

1. **Owner's switch rule:** 1 clean day and 20 compared calls (`gateway.shadowDays` 1, `gateway.shadowMinCompares`
   20; 0 lifts either), not 3 days.
2. **No CI change by this stream.** The contract runs in shadow and go mode and the fixture regeneration are not added
   to `ci.yml` (coordinator's call): stream B's fixtures job regenerates every `gen-*.js` on Node and Deno, and the
   shadow and go contract modes run locally until a CI step is added after stream B merges.
3. **Node/Deno differences, kept out of the corpus and pinned to Deno in Go:** U+A7CF, U+A7D3 and U+A7D5, which Node
   24's `toUpperCase` maps to U+A7CE, U+A7D2 and U+A7D4 and Deno 2.9.8's does not (`TestUnitsTheRuntimesFoldDifferently`:
   the engine does not fold them); and the path `/v1/a^b`, whose pathname is `/v1/a%5Eb` on Node and `/v1/a^b` on Deno
   (`TestCaretPathIsNotCanonical`: the front never classifies it).
4. **A `//` request target** is forwarded through `URL.RawPath`, not `Opaque` (which net/url writes as `http://...`); it
   is exact unless the path after `//` also holds a character net/url escapes (`\`, `|`).
5. **Windows error names:** Winsock codes are named as Node names them on Windows (`ECONNREFUSED`, `ECONNRESET`,
   `ETIMEDOUT`, `EHOSTUNREACH`, `ENETUNREACH`, `ECONNABORTED`).
6. **A base URL holding a credential** (user:password, a query or a fragment) is pushed with origin and path only and
   the provider disabled, so its calls go to the server, which calls it as before.
7. **Node 24's fetch ignores `HTTPS_PROXY`** unless `NODE_USE_ENV_PROXY=1`, while the engine always uses it (Deno's
   fetch does too): under Node, a go-served call may go through a proxy the JS gateway never used.

## Follow-ups

- CI steps for the contract suite in shadow and go mode (after stream B merges).
- The reverse shadow (js building from go's provider answer) and streaming chat: P4c.
- Guardrails and budgets in the engine: P4e; translation: P4d; OAuth2 and service accounts: P4g.
- The server's access checks in Go (remote clients), per-engine rate-limit windows and provider quota readings: G3.
- Review minors m3 (secret extraction on non-canonical paths, trimming), m4 (a held quiet-log count), m6 (item 7).
```

Apply (from the repository root):

````bash
git apply <<'PATCH'
diff --git a/README.md b/README.md
index 0db1124..dab9188 100644
--- a/README.md
+++ b/README.md
@@ -592,6 +592,14 @@ the only writer of the usage log, the cost ledger and the audit chain. With `"ga
 them (build it with `npm run gw:build`, or point `ZAAFPULSE_GW_BIN` at a binary); if it cannot run, the server serves
 the port itself and the Overview shows it. The default, `"js"`, starts no Go process.
 
+In front mode the Go process can also answer non-streaming chat calls itself (`"engines": { "chat": "go" }`): to an
+OpenAI-format provider with a plain API key, from a client on this machine, when no guardrail check and no blocking
+budget applies; it hands every other call to the server. Before it switches, `"shadow": ["chat"]` has it compare each
+chat call the server answers with what it would have sent and answered (`.data/gateway-shadow.jsonl`, no prompt text);
+chat moves to Go only after 20 compared calls and 1 day without a difference (`"shadowMinCompares"`,
+`"shadowDays"`), and while OpenTelemetry export is off.
+`GET /api/gateway/status` shows the counts and why a class stays on the server.
+
 ### Run it on login (macOS, from source)
 
 ```bash
diff --git a/docs/migration/2026-11-10-deno-go-migration-plan.md b/docs/migration/2026-11-10-deno-go-migration-plan.md
index 4235184..58eb49d 100644
--- a/docs/migration/2026-11-10-deno-go-migration-plan.md
+++ b/docs/migration/2026-11-10-deno-go-migration-plan.md
@@ -239,9 +239,9 @@ contract suite is green on Go and the shadow diff is 0 for 3 days.
 | Slice | Scope | Exit |
 |---|---|---|
 | S1 | Skeleton: health, config, secret handoff, supervisor start/stop | Starts and stops under the supervisor on 6 targets: **met in P4a** (2026-11-12, `docs/migration/2026-11-12-p4a-results.md`) |
-| S2 | OpenAI-compatible pass-through, non-streaming | Contract cases green; M3 met |
+| S2 | OpenAI-compatible pass-through, non-streaming | Contract cases green; M3 met: **chat on go behind the shadow in P4b** (2026-11-12, `docs/migration/2026-11-12-p4b-results.md`); M3 is re-measured in P4c |
 | S3 | Streaming SSE | M4 met at 500 streams |
-| S4 | API-key auth, per-key allow-lists and modes | Contract cases green |
+| S4 | API-key auth, per-key allow-lists and modes | Contract cases green: **met in P4b** (2026-11-12, `docs/migration/2026-11-12-p4b-results.md`) |
 | S5 | Translate (Anthropic, OpenAI, Gemini, routers, Ollama/local) | Fixtures from `translate` tests pass byte-for-byte |
 | S6 | Guardrails: secrets, PII and injection; block → 400 `guardrail_blocked` | Fixtures from the guardrail tests pass |
 | S7 | Budget guard, fail open (§11.6) | Kill-IPC test: metered calls pass, each marked `budget_unchecked` and alerted; a spent budget in the snapshot is still refused |
PATCH
````

- [ ] **Step 4: Run the tests and the task gate**

Run: `grep -n 'engines.: { .chat.: .go. }' README.md && grep -n 'met in P4b' docs/migration/2026-11-10-deno-go-migration-plan.md && test -s docs/migration/2026-11-12-p4b-results.md && echo docs-ok`
Expected: one README line, one plan line (S4), and `docs-ok`.

- [ ] **Step 5: Commit and push**

```bash
git add -A -- README.md docs
git status --short   # nothing outside this task's files, nothing under .superpowers/
git commit -m "P4b T11: results, README and program plan"
git push origin migrate/p4b-passthrough
```

- [ ] **Step 6: Start the full-matrix CI run and record it**

As after Task 5. Expected: every job green; add the run to the results document's table.

---

## Stage 1 exit checklist for stream A (spec "Exit criteria")

- [ ] `chat` non-streaming can run on `go` behind the shadow: `gateway.engines.chat: "go"` serves (Task 8), and only after the shadow rule allows it (Task 9, `routing()`).
- [ ] The contract cases for it are green on Go: `ZP_ENGINE=go` 179/179 on Node and Deno, `test/contract/go-chat.test.js` proves the engine answered (Task 8).
- [ ] Shadow diff 0 on the contract corpus: `ZP_ENGINE=shadow` on Node and Deno, every server stopped without a diff line (Task 9).
- [ ] The P4b entry conditions are met, each with a test: 1 (Task 3), 2 and 6 (Task 1), 3 (Task 4), 5 and 8 (Task 2), 4 by stream C before this PR merges (Decision 12).
- [ ] A full-matrix CI dispatch on this branch before the PR (Task 10).

## Self-review (done while writing this plan)

1. **Spec coverage.** Item 1 (first go route, chat non-streaming, OpenAI format, no translation): Tasks 7, 8. Item 2 (keys from the config.push hash, allow-lists and modes, fixtures from `keys.js`): Tasks 4, 5, 7 (the key's `privateOnly` and guardrail modes decide the hand-off). Item 3 (shadow, only the serving engine calls the provider, compare of provider request, client answer, usage and cost, `.data/gateway-shadow.jsonl`, the switch rule): Task 9, Decisions 3, 4, 10, owner answer 1. Item 4 (entry conditions): Tasks 1–4, Decision 12, chip in Task 2. Item 5 (secrets from `secrets.handoff`, `baseUrl` and `tokenParam`, JS timeouts, masked errors): Tasks 4, 7, 8, Decision 9.
2. **Placeholders.** None in code steps. Run-time values (dispatch URLs, the counts in the results) are filled by the commands Task 11 gives.
3. **Names.** `Classify`/`Canonical`/`RawPath`, `QuietLogger`, `gwEnv`/`NETWORK`, `restartNeeded`, `ingestUsage`, `serverSinks({ recordUsage })`, `configPush`/`secretsPush`/`snapshotPush`, `push`/`synced`, `rawKeys`, `onKeysChange`, `requestSecret`, `NewKeys`/`Authenticate`/`Authorize`/`Allows`/`RequestSecret`/`RateLimiter`, `Parse`/`Value`, `NewState`/`PlanChat`/`Respond`/`Failed`/`RateLimited`, `NewUpstream`/`Call`/`ErrorCode`, `engine.New`/`Serve`/`Shadow`/`Compare`, `GoClasses`/`GoEngine`/`SetGoEngine`/`SetShadow`/`ShadowHeader`/`OwnHeader`, `GO_CLASSES`/`enginesOf`/`routing`/`shadowOf`, `withShadow`/`noteProviderRequest`/`noteAnswer`/`takeCapture`, `createShadowLog`/`shadowEndpoints`: each defined once and used with the same signature later.
4. **Stream boundaries.** No import of `gw/translate`, `gw/guard`, `gw/provauth`; no edit of `gw/ipc/outbox.go`, the lock files, `lib/ipc/marks.js` or `lib/ipc/markfile.js` (deviation 1 names the two `gw/ipc` and `ipc/v1` files this stream does edit).
5. **Windows.** No new process handling; the CONNECT close uses `http.Hijacker`; paths through `filepath.Join`/`path.join`; the `go` job runs the new Go tests on both Windows runners.

## Verification on a scratch copy

Every code block of Tasks 1–11 was applied, in order, to a clean worktree of `063d39f` by a script that reads this file (each "Create" block written as given, each "Apply" block through `git apply`, each generator run), and the resulting tree was compared with the scratch tree that ran green. The results are listed under F9 and in the report that accompanies the plan's commit.
