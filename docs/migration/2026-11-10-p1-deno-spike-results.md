# P1 Deno spike — results (2026-11-10)

Run on the owner's MacBook (Apple Silicon) with **Deno 2.9.8** (Homebrew; V8 15.0, TypeScript 6.0.3), against
`ea27ea0` plus one change: `"type": "commonjs"` in `package.json`. This is the program plan's P1, from
`docs/migration/2026-11-10-deno-go-migration-plan.md`.

## Verdict for gate G1: PASS, with follow-ups for P2

| G1 criterion | Result |
|---|---|
| ≥ 98% of tests green on Deno | ✅ **2,513 / 2,562 = 98.1%**; 47 failed, 2 ignored (2 m 55 s) |
| All 6 targets compile | ✅ 6/6, including `aarch64-pc-windows-msvc` |
| win-arm64 binary answers on an ARM runner | ⏳ Not done yet. It needs the `windows-11-arm` CI job from P0. The binary is a valid PE32+ Aarch64 file. |
| AsyncLocalStorage overhead ≤ 15% | ⏳ Benchmark in P0/P2. ALS works (probe ✅). |

## 1. Required change found

Deno runs CommonJS only when the nearest `package.json` says `"type": "commonjs"`, the file is `.cjs`, or the
`--unstable-detect-cjs` flag is passed. Without it, every module fails with `require is not defined`. Adding
`"type": "commonjs"` to `package.json` fixes this and changes nothing for Node, where CommonJS is already the
default.

## 2. Compatibility probe (`scripts/migration/deno-probe.js`)

| API | Node | Deno |
|---|---|---|
| `node:sqlite` | ✅ | ✅ |
| crypto: HMAC, `scrypt` | ✅ | ✅ |
| `AsyncLocalStorage` | ✅ | ✅ |
| zlib gzip | ✅ | ✅ |
| `child_process.spawnSync` | ✅ | ✅ |
| `worker_threads` (eval worker, workerData, postMessage) | ✅ | ✅ |

## 3. `deno compile` to all six targets

Built from macOS arm64 (cross-compile), including `bin lib public skills scripts/register-mcp.js mcp.js package.json
config.example.json LICENSE`, the same set as the Node SEA build.

| Target | Deno binary | v1.1.0 Node SEA | Δ |
|---|---|---|---|
| aarch64-apple-darwin | **68.8 MB** | 117.2 MB | −41% |
| x86_64-apple-darwin | 81.1 MB | 120.9 MB | −33% |
| aarch64-unknown-linux-gnu | 104.6 MB | 118.9 MB | −12% |
| x86_64-unknown-linux-gnu | 103.8 MB | 122.4 MB | −15% |
| x86_64-pc-windows-msvc | 80.3 MB | 91.0 MB | −12% |
| **aarch64-pc-windows-msvc** | **73.8 MB**, PE32+ Aarch64 | 79.8 MB | −8% |

One download of a base binary failed once with a network error and passed on retry. Pin and cache the `denort`
base binaries in CI.

## 4. Runtime on macOS arm64 (demo mode, temp data dir)

| Measure | Node (baseline) | Deno compiled | Deno `run`, narrow permissions |
|---|---|---|---|
| Ready, first run | 0.37 s | 1.26 s (cold, first start) | — |
| Ready, warm | 0.37 s | **0.46 s** | 0.54 s |
| RSS after start | 142 MB | **124 MB** | — |
| `/login.html`, `/login.js`, `/tracemetro.js`, `/` | 200 | 200 | 200 |
| Permission errors | n/a | n/a | **none** |

The narrow permission set used:
- `--allow-read`: the app, temp dirs and `$HOME`;
- `--allow-write`: temp dirs;
- `--allow-net`: 127.0.0.1, localhost and 0.0.0.0;
- `--allow-env` and `--allow-sys`;
- `--allow-run`: which, ps, lsof, security.

## 5. The 47 failing tests, by root cause

| Group | Tests | Root cause | Product or test? | Fix (phase) |
|---|---|---|---|---|
| A. `process.execPath` is `deno` | 36: `evals-claude-cli` 24, `evals-models` 6, `evals-cli` 2, `evals-api` 2, `evals-engine` 1, `users-http` 1 | `lib/evals/claude-cli.js` `nodeBin()` runs a JS script (Claude Code's `cli.js`) with `process.execPath` unless `isSea()`. Under Deno that is the deno binary. The test helper `fakeClaude` writes `#!<execPath>` shebangs, so the fakes fail too. | **Product** (Claude Code as an eval judge would not start under Deno) and test | P2: replace `isSea()` with `isSingleFile()`, true under Deno, so a real node is found on PATH. `fakeClaude` uses `#!/usr/bin/env node`. |
| B. `node:test` mocks on module exports | 4: `trace-registry` 3, `evals-store` 1 | `test.mock.method(fs, 'createReadStream', …)` and the like are refused by Deno's `node:test` (“Cannot mock property … not a function”) | Test | P2: inject the fs functions (the code already takes injected deps in most places) or use a stand-in |
| C. Global `localStorage` exists in Deno | 3: `cost-ui` 1, `cost-reports-ui` 2 | The tests assume `typeof localStorage === 'undefined'` in Node and inject a fake store; Deno's global persistent store shadows it | Test | P2: pass the store explicitly in the tests; ensure no server code touches the global `localStorage` |
| D. `process.platform` is an accessor | 1: `processes` | The test redefines it with `{ ...descriptor, value }`; in Deno it has get/set | Test | P2: use the existing `platform` injection |
| E. To investigate | 3: `trace-api` 2, `monitors-state` 1 | “the Claude log is listed”, and “Missing expected exception” on a failed snooze save (likely a mocked fs write) | Probably test/env (HOME and the indexer path); verify | P2 |
| F. Fetch abort timing | 1: `evals-models` “external: the timeout aborts the request” | “Promise resolution is still pending but the event loop has already resolved” | **Possibly product**: timeout and abort semantics of outbound calls | P2: reproduce in isolation. That path moves to the Go egress proxy in P5 anyway. |

Group A was also confirmed with a direct probe: `os.homedir()` honours `HOME` in both runtimes, `process.execPath`
is `/opt/homebrew/bin/deno`, `typeof localStorage === 'object'`, and `process.platform` is a get/set accessor.

## 6. Plan updates from this spike

- P2 adds four items:
  - `"type": "commonjs"` in `package.json`;
  - the `isSingleFile()` replacement for `isSea()` (fixes group A in product code);
  - portable test seams for groups B–D;
  - pinned and cached `denort` downloads in CI.
- M7 (startup ≤ 1.0 s) holds warm (0.46 s). The cold first start (1.26 s) is measured again in P2 on all targets.
- M5 and M6 look reachable: −13% RSS on macOS arm64, and −8% to −41% binary size.
