# P2 Control plane on Deno — design

Date: 2026-11-10. Repo `ZAAFPulse-deno-go` (private). Branch `migrate/p2-control-plane-deno`, cut from `migrate/deno-go`
at `a89dba3` (P0 merged, gate G0 met).

Inputs:
- Program plan `docs/migration/2026-11-10-deno-go-migration-plan.md` §5 P2 and gate G2.
- P1 results `docs/migration/2026-11-10-p1-deno-spike-results.md`.
- P0 contract-under-Deno report `docs/migration/2026-11-10-p0-contract-deno.md`.

All Deno work follows the `deno` and `migrate-to-deno` skills (owner instruction).

## Goal

ZAAFPulse's whole application runs on Deno 2.9.8 as a `deno compile` binary on all 6 platforms, with the existing
test suite fully green under Deno, a narrow and tested permission matrix, and no Node-only code paths left in what
ships. The gateway still runs inside this process. Go comes in P4; this phase does not touch the gateway's
behaviour.

## Correction to P1 and to the program plan (verified 2026-11-10)

`DENO_AUDIT_PERMISSIONS` logs a `write` entry for **pure reads**. A script doing only `fs.readFileSync`,
`existsSync` and `statSync` produced one `read` and one `write` entry per call, and it ran fine with
`--allow-read` alone.

So the P1 finding "the app writes into its own install folder (`lib/data/prices-snapshot.json`, `skills/*`)" is
**wrong**:
- `lib/pricing.js` only reads the snapshot;
- `lib/gateway/skills.js` reads bundled skills from the app folder and writes copies into the agents' skill folders.

Consequence for the method: the permission matrix takes **read, net, env, sys and run** candidates from the audit,
but **write** permissions are proven only by running with a scoped `--allow-write` and `--no-prompt` and observing
denials. P2's first commit corrects the P1 results document and the program plan.

## Decisions

**D1. The runtime is detected by capability, not by "SEA".**
- `lib/platform.js` gets `runtime()`, which returns `'node' | 'deno'`, and `isSingleFile()`, which is true for a
  `deno compile` binary and for a Node SEA.
- Every `isSea()` call is reviewed: `service.js`, `platform.js`, `evals/claude-cli.js`, `server.js` and
  `bin/zaafpulse.js`.
- Any code that runs a JS file through `process.execPath` must use a real `node` whenever the runtime is not Node.
  This fixes P1 group A, the Claude Code judge that does not start under Deno.

**D2. Compiled binaries do not unpack files, so the start-time manifest check is dropped.**
- The Node SEA build unpacked the app to disk, and `sea-main.js` re-checked SHA-256 hashes on every start.
- `deno compile` serves embedded files from inside the binary (we do not use `--self-extracting`).
- Integrity is covered by the release's `SHA256SUMS` and GitHub attestations, as for the binary itself.
- `scripts/sea-main.js` and `scripts/build-single.js` (Node SEA) are removed from the v2 line once the Deno build is
  green on 6/6. `postject` leaves `devDependencies`.

**D3. Permission sets live in `deno.json`.**
- This is the first `deno.json` in the repo. It holds only Deno config, per the skill: `permissions` sets and
  `compile` settings. Dependencies and scripts stay in `package.json`, compiler options in `tsconfig.json`.
- Named sets:
  - `server`: dashboard, gateway, indexer, MCP over HTTP;
  - `cli`: the `zaafpulse` subcommands;
  - `mcp-stdio`: `mcp.js`;
  - `test`: the test runner, used by CI.
- Every set is narrow:
  - write: only the data folder (`ZAAFPULSE_DATA` or the platform default), the agent skill folders (from
    `lib/logroots.js` and `lib/gateway/skills.js`) and the OS temp folder;
  - net: listen on the configured host and port; outbound is still needed in P2 (providers, connectors, price sync)
    until P5 moves egress to Go, and it is scoped to the hosts in config where Deno allows that; otherwise it stays
    broad and is recorded as P5 debt;
  - env: an allow-list;
  - run: an allow-list of binaries (`ps`, `lsof`, `which`/`where`, `security`, `secret-tool`, `powershell`,
    `launchctl`, `systemctl`, `schtasks`, `claude`, `node`, plus runtimes the owner configured), with every
    entry justified;
  - sys: only the APIs used.
- `--deny-*` is added where an allow stays broad (deny always wins).

**D4. Per-permission tests.** For each set there is at least one test that a representative feature works, and one
test that a denied neighbour fails **loudly** (an error the UI or log shows, never a silent empty result). The matrix
and its test file are listed on the Compliance page as a control ("runtime permissions"), with read-only text and no
new UI framework.

**D5. CI turns the Deno suite blocking at the end of P2.**
- The `deno` job runs `deno test -P=test test/` (or the scoped grant), no longer `-A`.
- It stops being `continue-on-error` in the commit that reaches 100%.
- The Node job keeps running until cut-over in P7, as the plan's dual-runtime rule requires.

**D6. Release artifacts in P2 are CI build artifacts only.**
- A new workflow job `build-deno` compiles the 6 targets on push to `migrate/**`. For each one it runs a smoke test
  on a native runner: the binary starts in demo mode on a temp data dir and `/login.html` returns 200. The runners
  are ubuntu x64, ubuntu arm64 if available, macos arm64, macos x64 (or arm64 via `--target`, smoke-tested only
  where it can run), windows x64 and windows-11-arm.
- Publishing releases, the authenticated install from the private repo and signing docs belong to P6.

**D7. Gate G2's live run is the owner's action.** Dropped by the owner on 2026-11-11: there is no 48 h live run in P2.
The owner's dashboard stays on Node; the first live run on Deno is the P7 cut-over (program plan §11, decision 4).
The text below is kept for the record.
- The owner's dashboard (launchd `com.zaaf.aipulse.server`) is switched to the Deno binary for 48 h only with the
  owner's explicit go-ahead.
- The rollback is the current `.worktrees/live` Node checkout, with a backup taken first.

## Tasks

| # | Task | Scope | Exit |
|---|---|---|---|
| T1 | Corrections + runtime detection | Fix the P1 doc and the plan (audit write over-reporting). Add `platform.runtime()` and `isSingleFile()`; replace the `isSea()` call sites; fix `nodeBin()` in `claude-cli.js` (group A product fix) | Group A: 36 tests green under Deno; Node suite unchanged |
| T2 | Test portability (groups B–E) | Test seams instead of `mock.method` on `fs` (B); explicit store instead of the global `localStorage` (C); platform injection instead of redefining `process.platform` (D); root-cause the 3 group E tests (`trace-api` 2, `monitors-state` 1) | All of B–E green under Deno and under Node |
| T3 | Group F: fetch abort semantics | Reproduce `external: the timeout aborts the request` under Deno in isolation; fix the product code if the abort does not reach the socket, otherwise fix the test | Green on both runtimes; the root cause is written down |
| T4 | Deno suite blocking | `deno test` at 100%. CI `deno` job blocking, with a scoped grant instead of `-A`. The summary script keeps reporting counts | CI: Node 4 OS + Deno (ubuntu, macos) green and blocking |
| T5 | Permission matrix | `deno.json` permission sets (D3) built by denial testing; per-permission tests (D4); Compliance "runtime permissions" control text | Every set narrow and justified; tests green; `-A` gone from committed tasks and CI |
| T6 | `deno compile` pipeline | `scripts/build-deno.js` (or a `deno task`) compiles the 6 targets with the right `--include` list and `-P=server`; `isSingleFile()` true in the binary; service install uses the binary; `bin/zaafpulse.js` version string; remove the SEA scripts and `postject` once green; CI `build-deno` job with native smoke tests (D6) | 6/6 built in CI; every runnable target answers `/login.html` 200 (incl. windows-11-arm) |
| T7 | Measure and document | Bench: the compiled Deno binary against the P0 Node baseline for startup, idle RSS and non-stream overhead (stream numbers stay noisy and are re-recorded at program end). README and `docs/` describe running and building on Deno | Startup and RSS within +10% of the Node baseline; docs updated |

Order: T1 → T2 → T3 → T4 → T5 → T6 → T7.
- T4 depends on T1–T3 being green.
- T5 needs a green suite to prove the narrow sets.
- T6 compiles with the `server` set from T5.

## Gate G2 (exit of P2)

- [ ] 100% of the suite green under Deno (M1); the CI Deno job blocking.
- [ ] 6/6 targets built in CI; native smoke tests green, including windows-11-arm (M8).
- [ ] Permission sets narrow and tested; no `-A` in committed tasks or CI (M9, partial: net stays broad until P5).
- [ ] Startup and idle RSS of the compiled binary within +10% of the Node baseline (M5, M7).
- [x] ~~The owner's live dashboard runs on the Deno binary for 48 h without incident (owner action, D7).~~ Dropped by the owner, 2026-11-11.
- [ ] The Node suite still green (dual-runtime until P7).

## Out of scope

- Any Go code (P4).
- The IPC (P3).
- Moving egress to Go (P5).
- Publishing releases and the authenticated install (P6).
- The Windows backtick-t process bug and the other v1 findings: they are scheduled separately with the owner.
  The process bug will become visible to the per-permission tests on Windows; it stays tagged, and is not fixed here.
- Strict TypeScript.
- The frontend.
