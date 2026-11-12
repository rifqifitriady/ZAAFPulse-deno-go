# ZAAFPulse migration plan: JavaScript (Node) → Deno + Go

Date: 2026-11-10. Repository: `rifqifitriady/ZAAFPulse-deno-go` (private), work branch `migrate/deno-go`, cut from
upstream `rifqifitriady/ZAAFPulse` main at `ea27ea0`.

Owner decision (2026-11-10): combination 3, Deno for the control plane and Go for the data plane. It was picked from
TypeScript+Go, Bun+Go and Deno+Go after an analysis of language, security, scalability, dependencies, distribution
and dashboard UI/UX.

This is the **program plan**. Each phase below gets its own spec, implementation plan and SDD run in this
repository, as in the AI Governance program. Every number is measurable. Phase 0 measures every baseline marked
*(P0)* before any target is final.

---

## 1. Goal and non-goals

**Goal:** ZAAFPulse runs as two cooperating binaries on all six platforms, with no loss of features:

- **`zaafpulse`, the Deno control plane.** It runs the dashboard, indexer, governance, Cost Control, MCP server,
  agents, orchestrator and collab, and process control. Its network permission is limited to localhost.
- **`zaafpulse-gw`, the Go data plane.** It runs the LLM gateway hot path and is the single egress proxy for every
  outbound connection.

The migration must deliver measurably better gateway latency and concurrency, and a Deno permission sandbox that is
shown on the Compliance page as evidence.

**Non-goals:**
- No change to features, the API or the UI behaviour during the migration. The dashboard UI stays HTML/JS in the
  browser. The frontend modernisation, ES modules and then components, is a separate program.
- Do not move the whole backend to Go.
- No new runtime dependencies without an explicit owner decision (§9).

## 2. Baseline (measured on 2026-11-10 at `ea27ea0`)

| Metric | Value | How it was measured |
|---|---|---|
| Application code | ~82k lines: `lib/` 52.7k (161 files), `public/` 27.3k, `server.js` 2.2k | `wc -l` |
| Module system | CommonJS in all 159 `lib` modules; 0 ES modules | `grep module.exports` |
| Gateway (`lib/gateway/`) | 10,323 lines in 19 files; injected services: secrets/keychain, budget guard + ledger, audit (AsyncLocalStorage), pricing, OTel, PII guardrails, MCP protocol | `wc -l`, `require` graph |
| Other large areas | `cost` 6.1k, `trace` 4.3k, `evals` 3.8k, `monitors` 3.1k, `governance` 2.9k, `datasets` 2.2k | `wc -l` |
| Tests | 2,562 tests (2,560 pass, 2 skipped), 201 files, `node:test` | `npm test` |
| Runtime dependencies | 0 (dev: `postject`) | `package.json` |
| Release binaries (Node SEA) | 80–122 MB each, 6 targets | GitHub release v1.1.0 |
| Idle RSS, live dashboard | ~200 MB | `ps` |
| Idle RSS, demo | 142 MB | `ps` |
| Startup to first HTTP answer (demo) | 0.37 s | curl loop |
| Gateway overhead p50/p95/p99, non-streaming, mock provider (20 ms) | 3.72 / 5.67 / 9.16 ms | `bench/results/2026-11-10-node-MBA-ZAAF.json` (node v24.16.0, Apple M4) |
| Concurrent SSE streams, 10 s per level | 50: 954 done, 0 errors, overhead p95 22.6 ms; 200: 4000 done, 0 errors, overhead p95 3.15 ms; 500: 9315 done, 0 errors, overhead p95 53.3 ms (noisy: recorded on battery with Zoom running; see bench/results/2026-11-10-MBA-ZAAF-notes.md; re-record at the end of the program (owner decision 2026-11-10)) | `bench/results/2026-11-10-node-MBA-ZAAF.json` (node v24.16.0, Apple M4) |
| Server RSS idle / peak at 500 streams | 108.9 / 408.3 MB | `bench/results/2026-11-10-node-MBA-ZAAF.json` (node v24.16.0, Apple M4) |
| Startup to the first 200 on /login.html (p50 of 5) | 195.3 ms | `bench/results/2026-11-10-node-MBA-ZAAF.json` (node v24.16.0, Apple M4) |

Deno run of the same harness, for information: bench/results/2026-11-10-deno-MBA-ZAAF.json (compare: node bench/compare.js <node file> <deno file>).

## 3. Target architecture

```
                 browser (dashboard UI, unchanged)
                          │  HTTP :4777
┌─────────────────────────▼─────────────────────────┐
│ zaafpulse — Deno control plane                     │
│  dashboard API · indexer (workers) · governance    │
│  (single audit writer) · Cost Control ledger ·     │
│  MCP server · agents/orchestrator/collab · users   │
│  process control · supervisor of zaafpulse-gw      │
│  permissions: write=data dir · net=localhost only  │
└───────────────┬───────────────────────────────────┘
                │ local IPC (the gateway's stdin/stdout, one JSON message per line, versioned)
                │  config push · secret handoff · budget snapshot ⇄ usage
                │  audit events → · trace spans → · health
┌───────────────▼───────────────────────────────────┐
│ zaafpulse-gw — Go data plane                       │
│  /v1/* gateway: key auth · guardrails · translate  │
│  · routing · budget check (fail-open) · stream     │
│  egress proxy: providers, connectors, price sync   │
│  (allow-list from config, every egress audited)    │
└───────────────┬───────────────────────────────────┘
                ▼  internet (LLM providers, integrations)
```

**Fixed design rules:**

1. **One audit writer.** Only the control plane appends to the HMAC chain. The gateway sends audit events over IPC.
2. **Budgets fail open, as in v1 (owner decision 2026-11-11, §11.6).** The gateway enforces from a pushed budget
   snapshot. When the snapshot is older than 5 s, or IPC is down, metered requests still pass: each one is marked
   `budget_unchecked` in the usage report and the audit, and the dashboard raises an alert. A budget that the snapshot
   says is spent is still refused. Fail-closed stays a later option behind a setting, not in this program.
3. **Secrets are handed over, not read.** The control plane reads the OS keychain and hands provider secrets to the
   gateway at start and on change. The gateway never needs a keychain library.
4. **One config.** `config.json` is owned by the control plane and pushed to the gateway on reload.
5. **Egress only through Go.** After P5 the Deno process holds no non-localhost network permission.

## 4. Success metrics (exit targets of the whole program)

| # | Metric | Baseline | Target |
|---|---|---|---|
| M1 | Test parity | 2,560/2,562 on Node | 100% of the same tests green on Deno, or ported 1:1 |
| M2 | Gateway contract suite (black-box HTTP) | built in P0 | 100% green against both engines (JS and Go) before each slice switches |
| M3 | Gateway overhead p95, non-streaming, mock provider | p95 5.67 ms (p50 3.72 ms, p99 9.16 ms) | ≤ 50% of the JS baseline **and** ≤ 5 ms |
| M4 | Concurrent SSE streams on the owner's laptop | 500 streams: 9315 done, 0 errors, overhead p95 53.3 ms, peak RSS 408.3 MB (noisy: recorded on battery with Zoom running; see bench/results/2026-11-10-MBA-ZAAF-notes.md; re-record at the end of the program (owner decision 2026-11-10)) | ≥ 500 streams, overhead p95 ≤ 10 ms, gateway RSS ≤ 80 MB |
| M5 | Idle RSS, both processes | 200 MB (live); bench idle 108.9 MB | ≤ 200 MB |
| M6 | Download size per platform, both binaries | 80–122 MB | ≤ current size per platform |
| M7 | Startup to both processes healthy | 0.37 s (Node, demo); bench startup p50 195.3 ms | ≤ 1.0 s |
| M8 | Platforms | 6/6 (Node SEA) | 6/6 built and smoke-tested in CI, including `windows-11-arm` |
| M9 | Deno permissions | n/a | write = data dir only; net = localhost only; env = allow-list; run = documented allow-list |
| M10 | Egress coverage | n/a | 100% of outbound connections pass the Go proxy. A test proves Deno has no outbound net permission. |
| M11 | Runtime dependencies | 0 | Deno 0 npm/jsr runtime imports; Go stdlib only, with any exception approved in §9 |
| M12 | Data compatibility | v1.1.x `.data`, `config.json` | v2 opens a v1.1 data dir unchanged; rollback to v1.1.x leaves it readable |
| M13 | Soak | n/a | ~~7 days on the owner's live dashboard~~ dropped (§11.7): the owner tests v2 in the browser instead |

## 5. Phases

Effort is for one developer working with the SDD workflow, as weeks of focused work. Every phase ends at a gate
(§6). Phases P2 and P4 can overlap only after gate G2.

### P0 — Foundations and baselines (1–2 weeks)

- **Benchmark harness** in `bench/`:
  - a mock provider that streams and not-streams with a fixed delay;
  - gateway overhead p50, p95 and p99;
  - concurrent SSE streams at 50, 200 and 500;
  - RSS at idle and under load, and startup time.
  - It writes JSON results to `bench/results/` so they can be compared.
- **Gateway contract suite** in `test/contract/`: a black-box HTTP suite for `/v1/*`, the gateway parts of `/mcp`,
  admin `/api/gateway/*`, the guardrail outcomes, budget refusal and usage records. It must be runnable against any
  base URL.
- **Types first:** `// @ts-check` + JSDoc in `lib/gateway`, `lib/cost` and `lib/governance`, checked with
  **`deno check`** in CI.
  - Compiler options (`checkJs`, `strict`) go in **`tsconfig.json`**, not in `deno.json`, so that editors see the same
    settings as Deno.
  - No `typescript` devDependency is needed, so zero dependencies still holds.
  - These types become the IPC schemas.
- **Money as integers:** micro-USD integers in the cost ledger, budgets and pricing, with a migration of stored
  values.
- **CI:** add a `windows-11-arm` job to the matrix (still Node).
- **Exit:** baselines recorded for M3, M4, M5 and M7. Contract suite ≥ 150 cases, green on the JS gateway.
  `deno check` clean for the three areas. All current tests green on 4 CI targets.

### P1 — Deno spike and compatibility gate (3–5 days)

- Run `scripts/migration/deno-spike.sh`, which covers four things:
  - a compatibility probe for `node:sqlite`, crypto, AsyncLocalStorage, `worker_threads`, `child_process` and zlib;
  - the whole test suite under Deno;
  - server boot under a narrow permission set;
  - `deno compile` to all 6 targets.
- Benchmark AsyncLocalStorage overhead on the audited admin path.
- Smoke-run the win-arm64 compiled binary on the CI `windows-11-arm` runner.
- **Exit (gate G1):**
  - ≥ 98% of tests green on Deno, with a written list of the rest;
  - every target compiles;
  - the win-arm64 binary answers `/login.html` on the ARM runner;
  - AsyncLocalStorage overhead ≤ 15% on the admin path.

### P2 — Control plane on Deno (2–3 weeks)

P2 follows the migration rungs of the `migrate-to-deno` skill. Each rung is useful on its own and can be reversed:

1. **Rung 1, Deno as the package manager.**
   - Run `deno install`. `deno.lock` is seeded from `package-lock.json` and committed.
   - CI uses **`deno ci`**, not `deno install`.
   - `package.json` keeps all dependencies and scripts. It also gets `"type": "commonjs"`, which P1 proved is
     required.
2. **Rung 2, run with Deno.**
   - Everything runs under `deno` with `-A`: server, MCP, CLI, tests through `deno test` on the unchanged
     `node:test` suite.
   - Fix the P1 failure groups A–F first.
   - Rung 2 confirms the program works. It is not where the permission policy is designed.
3. **Rung 3, tighten permissions.**
   - Derive the narrowest set from **`DENO_AUDIT_PERMISSIONS`** runs: JSONL of every access across the full test
     suite and a scripted dashboard walk.
   - Store it as named permission sets in `deno.json`, used with `-P`/`--permission-set`.
   - Add `--deny-*` where a broad allow remains (deny always wins).
   - `-A` stays out of committed tasks, apart from rung 2 migration aids.
4. **Rung 4, toolchain: not adopted.**
   - Keep `node:test` (run by `deno test`) and prettier.
   - `deno check` replaces `tsc`, because it adds no dependency.
   - `deno fmt` and `deno lint` are not part of this program, because parity is incomplete and the toolchain is not
     the goal.

Config placement, following the `deno` skill:
- dependencies and scripts go in `package.json`;
- compiler options go in `tsconfig.json`;
- `deno.json` holds only Deno-specific config: permission sets, compile `include`/`exclude`, and `exclude`.

- **Writable state:** the P1 finding that the app writes into its own install folder was wrong: the permission audit
  logs a `write` entry for pure reads (corrected in the P1 results). Write permissions are proven by denial testing.
- Replace the Node SEA pipeline:
  - `scripts/build-single.js` and `scripts/sea-main.js` become `deno compile` with embedded assets;
  - a compiled binary serves its files from inside itself and unpacks nothing, so the start-time SHA-256 manifest
    check goes away; integrity is the release's `SHA256SUMS` and GitHub attestations (P2 spec D2);
  - `isSea()` in `lib/platform.js` becomes `runtime()` and `isSingleFile()`.
- The **permission matrix** lives in one place, the `deno.json` permission sets. It has a test per permission: the
  feature works, and the denied neighbour fails loudly.
  - The matrix is shown on the Compliance page as a control.
  - A `DENO_AUDIT_PERMISSIONS` trail (JSONL, or the `otel` exporter) is offered as audit evidence.
- Dual-runtime CI: the suite runs on Node **and** Deno until cut-over.
- Point service install (launchd, systemd user unit, Windows) at the Deno binary.
- **Exit (gate G2):**
  - 100% of tests green on Deno (M1);
  - 6/6 targets build and smoke-test (M8);
  - Deno RSS and startup within +10% of the Node baseline;
  - ~~the owner's live dashboard runs on Deno for 48 h without incident~~ dropped by the owner on 2026-11-11 (§11, decision 4).

### P3 — IPC contract (1 week)

- Transport (corrected 2026-11-11, P3 spec D1): the gateway's stdin and stdout, one JSON message per line. No socket,
  port or token: only the parent holds the pipes. Deno needs no `net` grant, only `run` for the gateway binary. The
  gateway exits when its stdin closes. The messages below, plus `hello`, `ready`, `ack` and `error` (P3 spec D3).
- Messages, as versioned JSON schemas generated from the P0 JSDoc types:
  - `config.push`
  - `secrets.handoff`
  - `budget.snapshot`
  - `usage.report`
  - `audit.event`
  - `trace.span`
  - `health`
  - `shutdown`
- Contract tests run on both sides from the same fixtures.
- The transport sits behind one interface on both sides (§11.2), so a network transport with mTLS can be added
  later without changing any message.
- **Exit** (met 2026-11-11, `docs/migration/2026-11-11-p3-results.md`):
  - [x] schemas frozen at v1 (`ipc/v1`), with the additive-only rule written down;
  - [x] both sides pass the shared fixtures, on Node and Go on 4 OS and on Deno on Ubuntu and macOS in CI (evidence:
    the manual full-matrix runs 43039341557 on `b954107` and 43048746223 on `4627057`; the pull request's run will be
    added);
  - [x] a kill test: killing either process never corrupts the audit chain or the ledger, and loses or duplicates no
    usage line;
  - [x] no change in product behaviour: `server.js` does not start the gateway; by owner decision three writers
    (`lib/audit.js`, `jsonlLog.append`, `usage.record`) gained an opt-in parameter that changes nothing by default and
    that the product never passes; `lib/trace` is unchanged (see the results document, which also lists the P4 S1
    entry conditions).

### P4 — Go data plane, strangler slices (5–8 weeks)

The control plane routes each gateway route to an engine through `gateway.engine` per route (`js` | `go`), with a
**shadow mode**: Go answers, JS answers too, and the outputs are compared and logged. A slice switches only when the
contract suite is green on Go and the shadow diff is 0 for 3 days.

| Slice | Scope | Exit |
|---|---|---|
| S1 | Skeleton: health, config, secret handoff, supervisor start/stop | Starts and stops under the supervisor on 6 targets: **met in P4a** (2026-11-12, `docs/migration/2026-11-12-p4a-results.md`) |
| S2 | OpenAI-compatible pass-through, non-streaming | Contract cases green; M3 met |
| S3 | Streaming SSE | M4 met at 500 streams |
| S4 | API-key auth, per-key allow-lists and modes | Contract cases green |
| S5 | Translate (Anthropic, OpenAI, Gemini, routers, Ollama/local) | Fixtures from `translate` tests pass byte-for-byte |
| S6 | Guardrails: secrets, PII and injection; block → 400 `guardrail_blocked` | Fixtures from the guardrail tests pass |
| S7 | Budget guard, fail open (§11.6) | Kill-IPC test: metered calls pass, each marked `budget_unchecked` and alerted; a spent budget in the snapshot is still refused |
| S8 | Usage, audit events and trace spans over IPC | Ledger and audit identical to JS for the same run |
| S9 | `/mcp` `tools/call` path, gateway keys | Contract cases green |
| S10 | Google service-account JWT (`createSign` RSA-SHA256) and other provider auth | Live provider smoke tests (owner keys, manual) |

**Exit (gate G3):**
- every gateway route is on `go`;
- shadow diff is 0;
- M3 and M4 are met;
- the JS hot path is deleted.

Agents, orchestrator, collab, a2a, workflows, knowledge, memory and skills **stay in Deno**. They call the gateway
like any client.

### P5 — Egress proxy and network lock-down (2 weeks)

- Route every outbound call from the control plane through the Go egress proxy:
  - connectors (Dynatrace, Arize, n8n, HTTP, MCP upstream);
  - price sync;
  - OpenRouter, OmniRoute and the other routers;
  - judge and evals calls.
- The allow-list is built from config, and every egress is audited (host, purpose, bytes, outcome).
- Narrow Deno `--allow-net` to localhost.
- New Compliance control: "Network egress is mediated and logged".
- **Exit:** M9 and M10 are met, and the test proves a direct outbound connect from Deno is denied.

### P6 — Packaging and release (1–2 weeks)

- Two binaries per target, signing, SHA256SUMS, attestations and the release workflow.
- `install.sh` and `install.ps1` install both binaries.
- `zaafpulse` supervises `zaafpulse-gw`: restart with back-off, and a health check shown on the Overview.
- Upgrade path from v1.1.x and rollback (M12).
- ~~An authenticated install and update from the private repo (§11.1)~~ not needed: the repo is public since 2026-11-11 (§11.5). Install and update fetch the public release assets, as in v1.x.
- Install docs for unsigned binaries: the SmartScreen and Gatekeeper steps, and checking SHA256SUMS and the
  attestation (§11.3).
- **Exit:**
  - M6 and M8 are met;
  - a clean install and an upgrade from v1.1.0 pass on all 6 targets, from the public release (no token, §11.5).

### P7 — Hardening and cut-over (1–2 weeks)

- Security review of the IPC, the permission matrix and the egress proxy (security-expert agent), plus
  `govulncheck`.
- ~~7-day soak on the owner's live dashboard (M13)~~ dropped (§11.7). The owner tests in the browser on the preview
  after each change; the cut-over keeps the rollback to the Node line one command away.
- Docs: README, architecture diagram, `docs/governance.md` (the new controls), and a migration note.
- Release **v2.0.0** from this repo (§11.1, public since 2026-11-11, §11.5), and remove the Node-only code paths.
- **Exit (gate G4):** every metric M1–M12 is met and recorded in `bench/results/` and the release notes (M13 dropped,
  §11.7).

## 6. Decision gates

| Gate | After | Go when | If not met |
|---|---|---|---|
| G0 | P0 | Baselines and contract suite exist | Do not start P1. The suite is the safety net for everything after it. |
| G1 | P1 | Deno compatibility and win-arm64 proven, AsyncLocalStorage overhead acceptable | Fall back to **TypeScript (Node) + Go**: P2 is skipped, P3–P7 unchanged |
| G2 | P2 | Control plane fully on Deno, CI green on every OS (the 48 h live run was dropped, §11 decision 4) | Stay on Node for the control plane; continue P3–P4 |
| G3 | P4 | Gateway fully on Go, M3 and M4 met | Keep the JS gateway for the slices not met; ship hybrid |
| G4 | P7 | All metrics met (M1–M12; M13 dropped, §11.7) | No v2.0.0; the public v1.x line stays the only release |

Every gate leaves a shippable product. Upstream `main` (public) keeps releasing v1.x the whole time; v2.0.0 is released from this repo (§11.1; public since 2026-11-11, §11.5).

## 7. Timeline

| Week | 1–2 | 3 | 4–6 | 7 | 8–15 | 16–17 | 18–19 | 20–21 |
|---|---|---|---|---|---|---|---|---|
| Phase | P0 | P1 | P2 | P3 | P4 (S1–S10) | P5 | P6 | P7 |

- **Total:** about 16–21 weeks (4–5 months) for one developer with the SDD workflow.
- **Faster option:** with P2 and P4 in parallel after G2, about 13–17 weeks.
- These are estimates. The P0 and P1 results set the final dates.

## 8. Risk register

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| A Deno Node-compat gap that tests do not catch | Medium | Medium | Dual-runtime CI until cut-over; shadow mode; the P7 cut-over is the first live run (the P2 48 h live gate was dropped, §11 decision 4) |
| AsyncLocalStorage overhead under Deno | Medium | Low–medium | Benchmark in P1; the hot path moves to Go anyway |
| A permission that is too narrow, so a feature fails silently | Medium | Medium | A test per permission; loud errors; matrix shown on the Compliance page |
| A two-writer audit chain or ledger | Low | High | Single-writer rule; kill tests in P3; S8 equality test |
| A budget leak while IPC is down | Low | High | Fail closed after 5 s staleness (S7 test) |
| Translate or guardrail behaviour drift JS → Go | Medium | High | Byte-for-byte fixtures taken from the JS tests; shadow diff 0 for 3 days |
| win-arm64 runtime issue (`deno compile` target is new, 2.9.3) | Low–medium | Medium | `windows-11-arm` CI job from P0; pin the Deno version |
| Two processes on Windows (service, signing, AV false positives) | Medium | Medium | P6 dedicated; sign both binaries; one installer |
| Upstream v1.x keeps changing during the migration | High | Medium | Merge `upstream/main` into `migrate/deno-go` weekly; the contract suite catches drift |
| Scope creep, such as rewriting more than the gateway in Go | Medium | Medium | Non-goals in §1; gate reviews |

## 9. Dependency policy

- **Deno:** no npm or jsr runtime imports. Std-like needs are met by `node:` built-ins already in use.
- **Go:** stdlib only. Any exception (for example an SSE helper or a SQLite driver) needs an owner decision recorded
  here with its licence and `govulncheck` result. None is expected for P4–P5.
- **Dev only:** the pinned Deno and Go toolchains in CI. Type checking uses `deno check`, so there is no `typescript`
  package. `postject` goes away with the SEA pipeline in P2.
- **Skills:** all Deno work in this program follows the `deno` and `migrate-to-deno` skills (owner instruction,
  2026-11-10).

## 10. Ways of working

- **Branches:**
  - `migrate/deno-go` is the integration branch;
  - each phase or slice works on `migrate/<phase>-<topic>` and merges into `migrate/deno-go` by PR, with CI green
    and the owner's OK;
  - upstream ZAAFPulse `main` is untouched.
- **Workflow:** each phase gets its own spec (`docs/superpowers/specs/`), plan (`docs/superpowers/plans/`) and SDD
  run with per-task review, a final review and a progress ledger.
- **Weekly sync:** `git fetch upstream && git merge upstream/main` into `migrate/deno-go`.
- **Reporting:** a progress table after every task (Task | Status | Progress | Next step), and the metric table
  (§4) updated at every gate.

## 11. Owner decisions (2026-11-10)

1. **Release line: v2.0.0 ships from this repo (`ZAAFPulse-deno-go`).** It was private when this was decided; it is
   public since 2026-11-11 (decision 5), so the authenticated-install bullets below no longer apply. The public ZAAFPulse stays on the
   v1.x line and is not switched.
   - Assets of a private repo cannot be downloaded without authentication, so P6 must deliver an authenticated
     install and update path:
     - `install.sh` and `install.ps1` fetch the release with a GitHub token (`GH_TOKEN`, or `gh release download`
       when `gh` is signed in);
     - the in-app update check queries the private repo with the same token and degrades to "no update info" without
       it;
     - the token is kept in the OS secret store, never in `config.json`.
   - v1.x users of the public repo do not receive v2 automatically.
2. **The Go gateway is embedded only (option A)**, running as a sidecar of the Deno control plane. There is no
   standalone team server in this program.
   - The P3 IPC contract must still be transport-agnostic:
     - the messages and schemas are independent of the transport;
     - the transport sits behind one interface, the child's stdin and stdout today, so a network transport with mTLS
       can be added later without changing messages.
   - A standalone `zaafpulse-gw` is reconsidered only after G3, with measured numbers.
3. **Target users are personal, so there is no Windows code-signing certificate** (and no Apple Developer ID). The
   binaries stay unsigned on Windows and ad-hoc signed on macOS, as in v1.x. Integrity comes from SHA256SUMS and
   GitHub attestations. The install docs explain the SmartScreen "More info → Run anyway" step and the macOS
   Gatekeeper step. Enterprise rollout behind AppLocker/WDAC is out of scope.
4. **Gate G2 has no 48 h live run (2026-11-11).** The owner's dashboard stays on the Node checkout through P2–P6.
   - Gate G2 rests on CI only: the Deno suite, the contract suite and the six native smoke tests.
   - What this leaves uncovered: a permission that only a long-running real install exercises (the owner's runtimes,
     notifications, the service started by launchd with its install-time PATH). Those surface at the P7 cut-over
     instead, where the rollback to the Node line must stay one command away.
5. **The repo is public (2026-11-11).** Actions minutes for a private repo ran out and blocked every CI job; a public
   repo runs the standard runners free. Before the switch the whole history was scanned: the only key-shaped strings
   are the fake test key `sk-ant-api03-AbCdEf…`, and no `config.json`, data folder or key file was ever committed.
   - P6 drops the authenticated install and update: the install scripts and the update check read the public
     release, as in v1.x.
   - The public ZAAFPulse repo still carries v1.x; v1.x users are not moved to v2 automatically.
6. **Budgets fail open for now (2026-11-11).** When the gateway cannot check a budget (stale snapshot, IPC down), the
   request passes, as in v1.x, and is marked and alerted (design rule 2). A spent budget is still refused. Fail-closed
   may come later as an opt-in setting.
7. **No 7-day soak before v2.0.0 (2026-11-12).** M13 is dropped. The owner tests in the browser on the preview
   (port 4787) after each change, as for P2–P3. Long-running issues (launchd PATH, notifications, the owner's
   runtimes) surface at the P7 cut-over, where the rollback to the Node line must stay one command away.
