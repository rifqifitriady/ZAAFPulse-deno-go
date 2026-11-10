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
| Gateway overhead p50/p95/p99, concurrent streams, RSS under load | *(P0)* | bench harness, P0 |

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
                │ local IPC (unix socket / named pipe, token-auth, versioned JSON)
                │  config push · secret handoff · budget snapshot ⇄ usage
                │  audit events → · trace spans → · health
┌───────────────▼───────────────────────────────────┐
│ zaafpulse-gw — Go data plane                       │
│  /v1/* gateway: key auth · guardrails · translate  │
│  · routing · budget check (fail-closed) · stream   │
│  egress proxy: providers, connectors, price sync   │
│  (allow-list from config, every egress audited)    │
└───────────────┬───────────────────────────────────┘
                ▼  internet (LLM providers, integrations)
```

**Fixed design rules:**

1. **One audit writer.** Only the control plane appends to the HMAC chain. The gateway sends audit events over IPC.
2. **Budgets fail closed.** The gateway enforces from a pushed budget snapshot. When the snapshot is older than
   5 s, or IPC is down, it refuses metered requests.
3. **Secrets are handed over, not read.** The control plane reads the OS keychain and hands provider secrets to the
   gateway at start and on change. The gateway never needs a keychain library.
4. **One config.** `config.json` is owned by the control plane and pushed to the gateway on reload.
5. **Egress only through Go.** After P5 the Deno process holds no non-localhost network permission.

## 4. Success metrics (exit targets of the whole program)

| # | Metric | Baseline | Target |
|---|---|---|---|
| M1 | Test parity | 2,560/2,562 on Node | 100% of the same tests green on Deno, or ported 1:1 |
| M2 | Gateway contract suite (black-box HTTP) | built in P0 | 100% green against both engines (JS and Go) before each slice switches |
| M3 | Gateway overhead p95, non-streaming, mock provider | *(P0)* | ≤ 50% of the JS baseline **and** ≤ 5 ms |
| M4 | Concurrent SSE streams on the owner's laptop | *(P0)* | ≥ 500 streams, overhead p95 ≤ 10 ms, gateway RSS ≤ 80 MB |
| M5 | Idle RSS, both processes | 200 MB (live) | ≤ 200 MB |
| M6 | Download size per platform, both binaries | 80–122 MB | ≤ current size per platform |
| M7 | Startup to both processes healthy | 0.37 s (Node, demo) | ≤ 1.0 s |
| M8 | Platforms | 6/6 (Node SEA) | 6/6 built and smoke-tested in CI, including `windows-11-arm` |
| M9 | Deno permissions | n/a | write = data dir only; net = localhost only; env = allow-list; run = documented allow-list |
| M10 | Egress coverage | n/a | 100% of outbound connections pass the Go proxy. A test proves Deno has no outbound net permission. |
| M11 | Runtime dependencies | 0 | Deno 0 npm/jsr runtime imports; Go stdlib only, with any exception approved in §9 |
| M12 | Data compatibility | v1.1.x `.data`, `config.json` | v2 opens a v1.1 data dir unchanged; rollback to v1.1.x leaves it readable |
| M13 | Soak | n/a | 7 days on the owner's live dashboard: 0 crashes, 0 audit-chain breaks, 0 budget leaks |

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
- **Types first:** `// @ts-check` + JSDoc in `lib/gateway`, `lib/cost` and `lib/governance`, with `tsc --noEmit` in
  CI (`typescript` as a dev tool only). These types become the IPC schemas.
- **Money as integers:** micro-USD integers in the cost ledger, budgets and pricing, with a migration of stored
  values.
- **CI:** add a `windows-11-arm` job to the matrix (still Node).
- **Exit:** baselines recorded for M3, M4, M5 and M7. Contract suite ≥ 150 cases, green on the JS gateway.
  `tsc --noEmit` clean for the three areas. All current tests green on 4 CI targets.

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

- `deno.json` (tasks, compile config, permission sets). CommonJS stays as is: Deno 2 runs CJS through the
  `package.json` `"type": "commonjs"` and `.cjs` rules. Moving to ES modules is optional and done per file later.
- Replace the Node SEA pipeline:
  - `scripts/build-single.js` and `scripts/sea-main.js` become `deno compile` with embedded assets;
  - keep the start-time SHA-256 manifest check;
  - replace `node:sea`'s `isSea()` in `lib/platform.js` with a Deno check.
- The **permission matrix** lives in one file, `deno.json`. It has a test per permission: the feature works, and the
  denied neighbour fails loudly. The matrix is shown on the Compliance page as a control.
- Dual-runtime CI: the suite runs on Node **and** Deno until cut-over.
- Point service install (launchd, systemd user unit, Windows) at the Deno binary.
- **Exit (gate G2):**
  - 100% of tests green on Deno (M1);
  - 6/6 targets build and smoke-test (M8);
  - Deno RSS and startup within +10% of the Node baseline;
  - the owner's live dashboard runs on Deno for 48 h without incident.

### P3 — IPC contract (1 week)

- Transport: HTTP/1.1 over a unix domain socket (named pipe on Windows), with a per-start random token. The fallback
  is 127.0.0.1 TCP with the same token.
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
- **Exit:**
  - schemas frozen at v1;
  - both sides pass the shared fixtures;
  - a kill test: killing either process never corrupts the audit chain or the ledger.

### P4 — Go data plane, strangler slices (5–8 weeks)

The control plane routes each gateway route to an engine through `gateway.engine` per route (`js` | `go`), with a
**shadow mode**: Go answers, JS answers too, and the outputs are compared and logged. A slice switches only when the
contract suite is green on Go and the shadow diff is 0 for 3 days.

| Slice | Scope | Exit |
|---|---|---|
| S1 | Skeleton: health, config, secret handoff, supervisor start/stop | Starts and stops under the supervisor on 6 targets |
| S2 | OpenAI-compatible pass-through, non-streaming | Contract cases green; M3 met |
| S3 | Streaming SSE | M4 met at 500 streams |
| S4 | API-key auth, per-key allow-lists and modes | Contract cases green |
| S5 | Translate (Anthropic, OpenAI, Gemini, routers, Ollama/local) | Fixtures from `translate` tests pass byte-for-byte |
| S6 | Guardrails: secrets, PII and injection; block → 400 `guardrail_blocked` | Fixtures from the guardrail tests pass |
| S7 | Budget guard, fail closed | Kill-IPC test refuses metered calls |
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
- **Exit:** M6 and M8 are met; a clean install and an upgrade from v1.1.0 pass on all 6 targets.

### P7 — Hardening and cut-over (1–2 weeks)

- Security review of the IPC, the permission matrix and the egress proxy (security-expert agent), plus
  `govulncheck`.
- 7-day soak on the owner's live dashboard (M13).
- Docs: README, architecture diagram, `docs/governance.md` (the new controls), and a migration note.
- Release **v2.0.0**, and remove the Node-only code paths.
- **Exit (gate G4):** every metric M1–M13 is met and recorded in `bench/results/` and the release notes.

## 6. Decision gates

| Gate | After | Go when | If not met |
|---|---|---|---|
| G0 | P0 | Baselines and contract suite exist | Do not start P1. The suite is the safety net for everything after it. |
| G1 | P1 | Deno compatibility and win-arm64 proven, AsyncLocalStorage overhead acceptable | Fall back to **TypeScript (Node) + Go**: P2 is skipped, P3–P7 unchanged |
| G2 | P2 | Control plane fully on Deno, 48 h live | Stay on Node for the control plane; continue P3–P4 |
| G3 | P4 | Gateway fully on Go, M3 and M4 met | Keep the JS gateway for the slices not met; ship hybrid |
| G4 | P7 | All metrics met | No v2.0.0; keep v1.x as the release line |

Every gate leaves a shippable product. Upstream `main` keeps releasing v1.x the whole time.

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
| A Deno Node-compat gap that tests do not catch | Medium | Medium | Dual-runtime CI until cut-over; shadow mode; 48 h live gate |
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
- **Dev only:** `typescript` (type checking) and the pinned Deno and Go toolchains in CI.

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

## 11. Open questions for the owner

1. Release line: should v2.0.0 ship from this private repo, with the result pushed back to the public ZAAFPulse, or
   should the public repo switch at G4?
2. Should the Go gateway also be offered as a standalone headless server for team deployments after G3?
3. Windows code signing certificate: is there one, or do we stay ad-hoc/unsigned as in v1.x?
