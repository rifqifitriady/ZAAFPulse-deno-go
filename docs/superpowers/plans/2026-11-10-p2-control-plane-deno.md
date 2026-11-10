# P2 Control Plane on Deno Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** ZAAFPulse runs entirely on Deno 2.9.8 as a `deno compile` binary on all six platforms, with the whole `node:test` suite green under Deno and Node, a narrow and tested permission matrix in `deno.json`, no Node-only code path left in what ships, and the Node SEA build removed.

**Architecture:** Nothing is rewritten: CommonJS, `node:test` and prettier stay (rung 4 of the `migrate-to-deno` skill is not adopted). `lib/platform.js` learns which runtime runs it (`runtime()`, `isSingleFile()`), and the call sites that ran a script through `process.execPath` find a real `node` when that is not Node. The 47 Deno failures are fixed at their measured root causes (one product bug, the rest test seams). `deno.json` holds four permission sets (`server`, `cli`, `mcp-stdio`, `test`); `lib/permissions.js` computes what `deno.json` cannot hold (the per-user write folders and owner-configured programs of a `deno run` launch), names the reason for every entry, and reports the running process's grants to the Compliance page. `scripts/build-deno.js` compiles the committed tree with `-P=server`; CI builds and smoke-tests the six targets on native runners.

**Tech Stack:** Deno 2.9.8 (`deno test`, `deno compile`, `deno ci`, permission sets in `deno.json`), Node 24.21.0 in CI (dual runtime until P7), CommonJS, `node:test`, GitHub Actions pinned by SHA, prettier 3 (printWidth 120).

## Global Constraints

- Spec: `docs/superpowers/specs/2026-11-10-p2-control-plane-deno-design.md` (decisions D1–D7, tasks T1–T7, gate G2). Program plan §5 P2 and §6 G2. The `deno` and `migrate-to-deno` skills bind all Deno work (owner instruction).
- Deno is pinned to **2.9.8** everywhere (CI `denoland/setup-deno` `deno-version: 2.9.8`); Node in CI stays **24.21.0**.
- **Dual runtime:** every task ends with `npm test` green under Node (2,576 tests at `ba4a08d`, plus the ones this plan adds) and the Deno count no worse than before the task. From Task 4 on, the CI `deno` job is blocking. The Node job keeps running until P7.
- **No `-A` in anything committed by the end of P2:** not in `package.json` scripts, CI, docs commands or code that builds a command line. `-A` appears in this plan only in local investigation commands, never in a file. Task 4 removes it from CI.
- Zero runtime dependencies. Dev dependencies: `@types/node` (exact `24.19.2`) stays; `postject` leaves in Task 6. Dependencies and scripts live in `package.json` (run with `deno task <name>`), compiler options in `tsconfig.json`, and `deno.json` holds **only** `permissions` (D3). `deno.lock` is committed; CI installs with `deno ci`.
- CommonJS is kept: `"type": "commonjs"`, `require`/`module.exports`, `.js` files. No ESM, no `.ts`, no `jsr:`/`npm:` imports.
- Every `deno run`, `deno test` and `deno compile` that uses `-P` also passes `-q`: Deno 2.9.8 prints "Permissions in the config file is an experimental feature" and one "Failed to resolve '<name>' for allow-run" line per program missing on that OS on every start (measured), and `deno compile -q` bakes the quiet log level into the binary (measured).
- Write permissions are proven only by running with `--no-prompt` and a scoped write grant and observing denials. `DENO_AUDIT_PERMISSIONS` is used only for read, net, env, sys and run candidates: it logs a `write` entry for pure reads (spec, "Correction"), and a spawned `deno` child truncates the same audit file (measured: a full-suite audit kept 2,819 lines).
- Product behaviour is frozen except where this plan says. Known v1 bugs stay unfixed: the Windows single-quoted backtick-t process format (`lib/processes.js:210`), unmasked upstream error bodies, budgets fail open.
- S1: never write content into the page through `innerHTML` or another HTML sink. P2 touches no frontend file.
- Prettier: `npx --yes prettier@3 --write` **only on new files**. Measured at `ba4a08d`: **not clean** (hand-edit, never `--write`): `lib/gateway/index.js`, `lib/cost/guard.js`, `lib/cost/index.js`, `lib/cost/rules.js`, `server.js`, `public/styles.css`, `public/traceview.js`, `test/trace-ui.test.js`, `test/register.test.js`, `test/signin.test.js`, `lib/evals/claude-cli.js`, `lib/evals/models.js`, `test/evals-claude-cli.test.js`, `test/trace-registry.test.js`, `test/evals-store.test.js`, `test/trace-api.test.js`, `test/evals-models.test.js`, `test/platform.test.js`. **Clean** (must stay clean; check with `npx --yes prettier@3 --check <file>` after editing): `lib/platform.js`, `lib/service.js`, `lib/processes.js`, `lib/trace/lines.js`, `lib/secret-store.js`, `lib/secrets.js`, `lib/gateway/skills.js`, `lib/indexer.js`, `lib/governance/controls.js`, `lib/governance/api.js`, `bin/zaafpulse.js`, `test/helpers.js`, `test/cost-ui.test.js`, `test/cost-reports-ui.test.js`, `test/processes.test.js`, `test/monitors-state.test.js`, `test/reqlogs.test.js`, `test/governance-controls.test.js`, `test/governance-inventory-http.test.js`, `test/secrets.test.js`, `test/contract/harness.js`, `bench/run.js`. `*.md`, `*.yml`, `*.yaml`, `*.json` are in `.prettierignore`.
- Commits are plain English with **no** `Co-Authored-By` line and no AI attribution of any kind. Never commit `.superpowers/`. Pushes, PRs and merges are done by the controller after the owner's OK, never by an implementer.
- Tests never reach the real network and never signal a process they did not start. Local runs in this plan use a temp `HOME` and `ZAAFPULSE_DATA` and a port other than 4777/4778.
- **Never touch the owner's live dashboard** (launchd `com.zaaf.aipulse.server`, the other repo's `.worktrees/live`). The 48 h live run of gate G2 is an owner action, written as the gate checklist at the end of this plan, not as a task.

## Facts measured for this plan (2026-11-10, macOS arm64, Deno 2.9.8, Node 24.16)

These were reproduced before writing the plan; each task relies on them.

| # | Fact | Evidence |
|---|---|---|
| F1 | `deno test -A --no-check test/` at `ba4a08d`: **2,698 passed, 47 failed, 3 ignored** (2 m 55 s). Same 47 as P1, by file: evals-claude-cli 24, evals-models 7, evals-cli 2, evals-api 2, evals-engine 1, users-http 1, trace-registry 3, evals-store 1, cost-ui 1, cost-reports-ui 2, processes 1, trace-api 2, monitors-state 1. | full log in the plan author's scratch run |
| F2 | Group A is fixed by two changes together: `nodeBin()` must not use `process.execPath` under Deno, **and** the test fakes need a real node by absolute path (a `#!/usr/bin/env node` shebang fails in tests that set `PATH: ''`). With both, evals-claude-cli + evals-models under Deno: 72 passed, 1 failed (the group F test); under Node 0 failed. | scratch copy |
| F3 | Group B is only `trace-registry` (3). Deno's `node:fs` exports refuse `test.mock.method` ("Cannot mock property 'createReadStream' because it is not a function"); a stub on an own object works on both runtimes. | scratch copy |
| F4 | `evals-store` (P1 put it in B) and `monitors-state` (E) share one root cause: **Deno's `fs.unlinkSync` removes an empty directory** (Node throws `EPERM`). Both tests block a write with an empty folder at `<file>.tmp`; the code's `rmQuiet(tmp)` (`unlinkSync`) silently removes it under Deno and the write succeeds. A non-empty folder makes `unlinkSync` fail on both runtimes (`ENOTEMPTY` / `EPERM`). | probe `unlinkSync(emptyDir)`: Deno removes it, Node throws EPERM |
| F5 | `trace-api` (2, group E) fails only after `test/reqlogs.test.js` in the same process: that file sets `HOME`, `ZAAFPULSE_CLAUDE_DIRS`, `CODEX_HOME`… at load time and never restores them. `node --test` runs each file in its own process; `deno test` runs all files in one, so the server `trace-api` spawns (env `...process.env`) reads the dead reqlogs folder. Reproduced: `deno test reqlogs.test.js trace-api.test.js` fails 2; `trace-api.test.js` alone passes 9/9. | scratch run |
| F6 | Group F root cause: the external judge call's only pending work in the test is an **unref'd** timer (`timer.unref?.()` in `lib/evals/models.js`); the test's fake `complete()` holds no handle. Deno's test runner ends a test whose event loop is empty ("Promise resolution is still pending but the event loop has already resolved"); Node's runner keeps the process alive. A minimal `node:test` file with only an unref'd `setTimeout` reproduces it under Deno and passes under Node. The abort itself reaches the socket on both runtimes (a hanging `http` server sees the socket close 54 ms after `AbortController.abort()` under Deno and Node). So: **test fix, no product change.** | probes `unref.js`, `abort.cjs` |
| F7 | Group D: `process.platform` is a get/set accessor in Deno, so `Object.defineProperty(process, 'platform', { ...descriptor, value })` throws "Cannot both specify accessors and a value". `lib/processes.js` has **no** platform injection today (P1 said "the existing one"); `rows()` gets a `platform` option. | test error text |
| F8 | Group C: Deno's `globalThis.localStorage` is an accessor (get/set, configurable); `global.localStorage = x` is silently ignored, and the real one is persistent on disk. `Object.defineProperty(globalThis, 'localStorage', { value })` works on both runtimes; restoring the saved descriptor works. | probe `ls.cjs` |
| F9 | **Product bug under any scoped grant:** `fs.existsSync('')` throws "Empty path is not allowed" under Deno with `--allow-read` (even unscoped), returns `false` under Node and under `-A`. `lib/processes.js installed()` expands `%LOCALAPPDATA%/…` to `''` on macOS/Linux, so `/api/live` answers 500 and `/api/topology` and the governance inventory silently lose every AI app. Also fails `test/log-roots.test.js:183` once the test grant is scoped. | narrow server walk; `ex.cjs` probe |
| F10 | Under Deno a child spawned as `process.execPath script.js` runs with **all permissions** whatever the parent holds; `process.execPath run <flags> script.js` gets exactly those flags. | probes `parent.cjs`, `parent2.cjs` |
| F11 | `deno.json` permission paths are resolved relative to `deno.json` when it is read (`~` and `$HOME` are not expanded), and `deno compile -P=<set>` bakes them as absolute paths of the build machine. CLI flags given to `deno compile` with **relative** paths resolve against the binary's working folder at run time. `Deno.permissions.revokeSync({ name: 'write', path })` does not narrow an unscoped grant. A CLI `--allow-write=<list>` / `--allow-env=<list>` **replaces** that entry of the loaded set (other entries and `--deny-*` stay). | probes `cw.js`, `cw2.js`, `rv.js`, `e2.js` |
| F12 | `--allow-env=<list>` alone makes any other name **throw** NotCapable, and `{ ...process.env }` and every `child_process` spawn with the default env throw ("Requires env access"). With `"env": { "allow": [...], "ignore": true }` (or `--ignore-env`), other names read as `undefined`, the spread yields only allowed names, and a child spawned with the default env still inherits the full OS environment. Prefix wildcards work (`ZP_*`); leading wildcards do not (`*_API_KEY`). | probes `envs.js`, `childenv.js`, `wc.js` |
| F13 | A denied `spawnSync` returns `{}` (no `status`, no `error`) under Deno — silent; `execFile`/`spawn` report "Requires run access to …" through the callback / `error` event. `--allow-run=<name>` resolves the name on PATH **at start** and also allows the same file by absolute path. | probe `run2.cjs` |
| F14 | `Deno.symlinkSync()` requires **unscoped** `--allow-read` and `--allow-write`. 12 tests create symlinks, so the `test` set needs `write: true`. Product code creates no symlinks. | candidate `test` run: 62 failed = 47 known + 14 symlink/denied + F9 |
| F15 | Candidate `server` set (read all; write data dir + temp + skill roots; net all minus metadata; env allow-list + ignore; sys 8 APIs; run allow-list) on the real server, non-demo, temp HOME with fixture logs: every page and API of a 33-path walk answers the same status as `-A`, and the JSON bodies differ only in port, keys made per process and live CPU numbers (after the F9 fix in a scratch copy). `mcp.js` over stdio with the `mcp-stdio` candidate answers 10/10 requests like `-A`. CLI `doctor`, `status`, `version`, `help` run under the `cli` candidate. | scratch walks |
| F16 | Denied writes fail loudly in the UI: `POST /api/gateway/skills {action:'install',target:'agents'}` without `~/.agents/skills` in scope answers `{"errors":["Requires write access to \"…/.agents/skills/zaafpulse\" …"]}`; MCP registration of Kiro without its folder answers the same kind of error. | scratch walk |
| F17 | Compiled binary (macOS arm64, `-P=server`, P1 include list + `server.js`): 72 MB; demo `/login.html` 200; warm start-to-200 0.30–0.33 s and RSS 127–144 MB (curl loop, 3 runs); `Deno.build.standalone === true`, `process.argv` is `[binary, binary, ...args]` (so `argv.slice(2)` still holds the arguments), `__dirname` is a virtual `/var/folders/…/deno-compile-<name>/` path that does not exist on disk; `worker_threads` workers from embedded files work. The P0 bench measured `deno run` startup p50 **258 ms vs Node 195.3 ms (+32%)** and idle RSS 88.6 vs 108.9 MB. | probes, `bench/results/2026-11-10-*.json` |
| F18 | `net: { "allow": true, "deny": ["169.254.169.254", "metadata.google.internal"] }` in a set allows everything else and refuses both with NotCapable. `Deno.permissions.querySync({ name })` reports `{ state: 'granted', partial: true }` for such an entry. | probes `n.cjs`, `q.js` |
| F19 | CI runners: `windows-11-arm` runs on this private repo (P0 CI). `ubuntu-24.04-arm` availability to a private repo is not verified (risk R2). `macos-13` is retired; the x64 macOS label is `macos-15-intel`. The `denort` base binaries cache at `$DENO_DIR/dl/release/v2.9.8/denort-<target>.zip`. `actions/cache` latest is v6.1.0 = `55cc8345863c7cc4c66a329aec7e433d2d1c52a9`. | `gh api`, local cache |

## Where the spec is wrong or infeasible (decisions this plan takes)

1. **D3 "write: only the data folder" cannot be baked into a compiled binary** (F11): the data folder and the agent folders are per user, `deno.json` paths do not expand `~`/`$VARS` and are fixed at build time, and revoke cannot narrow. This plan: every set has `"write": true` in `deno.json`; every `deno run` launch narrows it with a computed `--allow-write=<writeScope()>` (which replaces the set's entry, F11); the compiled binary keeps unscoped write, recorded as P2 debt next to net and shown as "unrestricted: write" on the Compliance control. Options for later (P5/P7): Deno's permission broker, or a launcher.
2. **D3 "env: an allow-list" breaks v1 secret references** whose variable name is not on the list (`docs/integrations.md` shows `{ "env": "DT_TOKEN" }`) — M12 says v2 opens a v1.1 config unchanged. This plan keeps the allow-list (with `ignore: true`, F12) and makes the failure loud and specific ("…is not readable by this build… rename it to ZP_SECRET_DT_TOKEN"). **Owner decision O1** at the gate: keep, or widen env to all for v2.0.
3. **D3 "run: … plus runtimes the owner configured"** works only for `deno run` launches (`runScope()` adds the owner's `localRuntimes` commands at launch). A compiled binary cannot learn them after build: an owner runtime outside the preset names is refused with "Requires run access" (loud). **Owner decision O2**: accept for v2.0, or give the binary unscoped run. The same holds for a Claude Code installed only inside an IDE extension folder.
4. **D1 "`isSingleFile()` true under Deno" (P1 wording) is not what group A needs:** under `deno run` there is no single file. The judge needs "a real node unless this process **is** node from source", which this plan names `useOwnNode`.
5. **D2/program plan "keep the start-time SHA-256 manifest check"** contradicts D2; D2 wins (no unpacking, no check). Task 1 corrects the program plan.
6. **T7 "startup within +10% of the Node baseline" is at risk** (F17: `deno run` +32% in the P0 bench). The compiled binary is measured with the same harness in Task 7; if it misses, the number is recorded and raised to the owner at the gate (M7's absolute target ≤ 1.0 s holds). No behaviour is changed to chase it.
7. **The P1 "test" grant `--allow-run=node,deno` is effectively unscoped** (F10), and the `test` set needs unscoped write (F14) and run (fakes are run by absolute temp paths; Deno cannot scope run by folder). The `test` set is narrow on net, env and sys only, and says so.
8. P1 listed `evals-store` under group B; its cause is F4 (group E).

## Files

| File | Task | Responsibility |
|---|---|---|
| `docs/migration/2026-11-10-p1-deno-spike-results.md`, `docs/migration/2026-11-10-deno-go-migration-plan.md` | 1 | the audit-write correction; the manifest-check line |
| `lib/platform.js`, `test/runtime.test.js` (new) | 1 | `runtime()`, `isSingleFile()`; `isSea()` removed |
| `lib/evals/claude-cli.js`, `test/helpers.js`, `test/evals-claude-cli.test.js` | 1 | `useOwnNode`; `NODE` for the fakes |
| `lib/service.js`, `server.js`, `bin/zaafpulse.js` | 1, 5, 6 | call sites; service command and working folder |
| `lib/trace/lines.js`, `test/trace-registry.test.js` | 2 | group B seam |
| `test/helpers.js`, `test/cost-ui.test.js`, `test/cost-reports-ui.test.js` | 2 | `stubGlobal` (group C) |
| `lib/processes.js`, `test/processes.test.js` | 2, 4 | `rows({ platform })` (group D); empty-path fix (F9) |
| `test/monitors-state.test.js`, `test/evals-store.test.js`, `test/reqlogs.test.js`, `test/trace-api.test.js` | 2 | group E |
| `test/evals-models.test.js` | 3 | group F |
| `deno.json` (new), `.github/workflows/ci.yml`, `scripts/migration/deno-test-summary.sh` | 4, 5, 6 | `test` set (4), the other sets (5); blocking Deno job (4); `build-deno` job (6) |
| `lib/permissions.js` (new), `test/permissions.test.js` (new), `test/fixtures/permissions/probe.js` (new) | 5 | scopes, reasons, `describe()`, `spawnRefused()`; per-permission tests |
| `lib/secrets.js`, `lib/secret-store.js`, `lib/gateway/skills.js` | 5 | loud env/run refusals; `skillRoots()` |
| `lib/governance/controls.js`, `test/governance-controls.test.js`, `test/governance-inventory-http.test.js` | 5 | control `runtime_permissions` |
| `test/contract/harness.js` | 5, 7 | `-P=server` (5); `ZP_RUNTIME=binary` (7) |
| `scripts/build-deno.js` (new), `scripts/smoke-binary.js` (new), `test/build-deno.test.js` (new), `package.json`, `package-lock.json`, `deno.lock`, `.github/workflows/release.yml` | 6 | compile, smoke, CI, SEA removal |
| `scripts/sea-main.js`, `scripts/build-single.js`, `test/sea.test.js` | 6 | deleted |
| `CONTRIBUTING.md`, `README.md`, `docs/install.md`, `docs/performance.md`, `lib/indexer.js` (comment) | 6, 7 | references to the removed build; running and building on Deno |
| `bench/run.js`, `bench/README.md`, `bench/results/<date>-binary-<host>.json`, `docs/migration/2026-11-10-p2-results.md` (new) | 7 | measurement |

---

### Task 1: Corrections and runtime detection (spec T1)

**Files:**
- Modify: `docs/migration/2026-11-10-p1-deno-spike-results.md:99,105-106`
- Modify: `docs/migration/2026-11-10-deno-go-migration-plan.md:183-189`
- Modify: `lib/platform.js:13-20,33,130,145`
- Modify: `lib/evals/claude-cli.js:218-222,258-261`
- Modify: `lib/service.js:41-46`
- Modify: `server.js:1579`
- Modify: `bin/zaafpulse.js:224-227`
- Modify: `test/helpers.js:193-213`, `test/evals-claude-cli.test.js:10,452-503,576`
- Create: `test/runtime.test.js`

**Interfaces:**
- Consumes: nothing.
- Produces: `platform.runtime({ versions? }): 'node' | 'deno'`; `platform.isSingleFile({ deno?, sea? }): boolean` (true in a `deno compile` binary or a Node SEA); `platform.isSea` no longer exists. `createClaude({ useOwnNode?: () => boolean, ... })` replaces the `isSea` option. `test/helpers.js` exports `NODE: string` (absolute path of a real node). `/api/platform` gains `runtime: 'node' | 'deno'` next to `single`.

- [ ] **Step 1: Check the starting point**

Run: `git status --short && git rev-parse --abbrev-ref HEAD && deno --version | head -1 && node --version`
Expected: nothing listed (or only `.superpowers/`), branch `migrate/p2-control-plane-deno`, `deno 2.9.8 (stable, …)`, `v24.x`.

- [ ] **Step 2: Correct the P1 results document**

In `docs/migration/2026-11-10-p1-deno-spike-results.md` replace the `read / write` table row (line 99) with:

```markdown
| read / write | 6,956 / 6,971 | Almost all in the data dir (`gateway-usage.jsonl`, `kb`, `datasets`, `brain`, `audit`, `traces`, `*.json.tmp`), plus reads of the app's own folder (`lib/data/prices-snapshot.json`, `skills/zaafpulse*`). The audit logs a `write` entry for every pure read (corrected in P2, see below), so this column does not show writes. |
```

and replace the two lines starting `**New P2 item:** the app writes into its own install folder.` with:

```markdown
**Correction (P2, 2026-11-10):** the paragraph that stood here said the app writes into its own install folder. It
does not. `DENO_AUDIT_PERMISSIONS` logs a `write` entry for pure reads: a script doing only `fs.readFileSync`,
`existsSync` and `statSync` produced one `read` and one `write` entry per call and ran with `--allow-read` alone.
`lib/pricing.js` only reads the snapshot, and `lib/gateway/skills.js` reads the bundled skills and writes copies into
the agents' skill folders. P2 therefore takes read, net, env, sys and run candidates from the audit and proves write
permissions by denial testing (`--no-prompt` and a scoped `--allow-write`).
```

- [ ] **Step 3: Correct the program plan**

In `docs/migration/2026-11-10-deno-go-migration-plan.md` replace lines 183–189 (from `- **Writable state out of the app folder**` through `- replace \`node:sea\`'s \`isSea()\` in \`lib/platform.js\` with a Deno check.`) with:

```markdown
- **Writable state:** the P1 finding that the app writes into its own install folder was wrong: the permission audit
  logs a `write` entry for pure reads (corrected in the P1 results). Write permissions are proven by denial testing.
- Replace the Node SEA pipeline:
  - `scripts/build-single.js` and `scripts/sea-main.js` become `deno compile` with embedded assets;
  - a compiled binary serves its files from inside itself and unpacks nothing, so the start-time SHA-256 manifest
    check goes away; integrity is the release's `SHA256SUMS` and GitHub attestations (P2 spec D2);
  - `isSea()` in `lib/platform.js` becomes `runtime()` and `isSingleFile()`.
```

- [ ] **Step 4: Write the failing runtime tests**

Create `test/runtime.test.js`:

```js
// lib/platform.js: which runtime runs this code, and whether it is a single-file build (P2 T1).
const test = require('node:test');
const assert = require('node:assert/strict');
const platform = require('../lib/platform');

test('runtime() names the runtime running the tests', () => {
  assert.equal(platform.runtime(), process.versions.deno ? 'deno' : 'node');
  assert.equal(platform.runtime({ versions: { node: '24.0.0', deno: '2.9.8' } }), 'deno');
  assert.equal(platform.runtime({ versions: { node: '24.0.0' } }), 'node');
});

test('isSingleFile() is false when the tests run from source', () => {
  assert.equal(platform.isSingleFile(), false);
});

test('isSingleFile() reads Deno.build.standalone under Deno and node:sea under Node', () => {
  assert.equal(platform.isSingleFile({ deno: { build: { standalone: true } } }), true);
  assert.equal(platform.isSingleFile({ deno: { build: { standalone: false } } }), false);
  assert.equal(platform.isSingleFile({ deno: null, sea: { isSea: () => true } }), true);
  assert.equal(platform.isSingleFile({ deno: null, sea: { isSea: () => false } }), false);
  assert.equal(
    platform.isSingleFile({
      deno: null,
      sea: {
        isSea: () => {
          throw new Error('no node:sea');
        },
      },
    }),
    false,
  );
});

test('isSea() is gone: every caller asks isSingleFile()', () => {
  assert.equal(platform.isSea, undefined);
});
```

Run: `npx --yes prettier@3 --write test/runtime.test.js`

- [ ] **Step 5: Run it to see it fail**

Run: `node --test test/runtime.test.js 2>&1 | grep -E "^ℹ (pass|fail)"`
Expected: `ℹ pass 0`, `ℹ fail 4` (`platform.runtime is not a function`).

- [ ] **Step 6: Implement `runtime()` and `isSingleFile()` in `lib/platform.js`**

Replace lines 13–20 (the `isSea` function and its comment) with:

```js
// The runtime running this code: 'deno' under Deno (deno run, deno test, or a deno compile binary), else 'node'.
function runtime({ versions = process.versions } = {}) {
  return versions && versions.deno ? 'deno' : 'node';
}

// True inside a single-file executable: a `deno compile` binary (Deno.build.standalone) or a Node SEA (node:sea). Its
// files are not on disk next to process.execPath, process.execPath is ZAAFPulse itself, and the MCP server is
// `<binary> mcp`.
function isSingleFile({ deno = globalThis.Deno, sea } = {}) {
  if (deno) return !!(deno.build && deno.build.standalone);
  try {
    return (sea || require('node:sea')).isSea();
  } catch {
    return false;
  }
}
```

In `implicitDataDir()` replace `return inApp && !isSea()` with `return inApp && !isSingleFile()`. In `mcpCommand()` replace `const cmd = isSea()` with `const cmd = isSingleFile()`. In `module.exports` replace the line `  isSea,` with:

```js
  runtime,
  isSingleFile,
```

Run: `node --test test/runtime.test.js 2>&1 | grep -E "^ℹ (pass|fail)" && deno test -q --no-check --allow-read --allow-env --allow-sys test/runtime.test.js 2>&1 | tail -1`
Expected: `ℹ pass 4`, `ℹ fail 0`; then `ok | 4 passed | 0 failed …`.

- [ ] **Step 7: Replace the other `isSea()` call sites**

`lib/service.js` lines 41–46 become:

```js
// The command that starts the server: the single-file binary, or node + bin/zaafpulse.js.
function startCommand() {
  return platform.isSingleFile()
    ? [process.execPath, 'start']
    : [process.execPath, path.join(platform.APP_DIR, 'bin', 'zaafpulse.js'), 'start'];
}
```

`server.js` (hand edit; not prettier-clean) line 1579: replace `        single: platform.isSea(),` with:

```js
        single: platform.isSingleFile(),
        runtime: platform.runtime(),
```

`bin/zaafpulse.js` lines 224–227 (in `doctor()`): replace

```js
    add('ZAAFPulse', `${pkg.version}${platform.isSea() ? ' (single-file build)' : ''}`);
    add('OS', `${platform.OS_LABEL} ${os.release()} ${process.arch}`);
    const major = Number(process.versions.node.split('.')[0]);
    add('Node.js', process.versions.node, major >= 18);
```

with

```js
    add('ZAAFPulse', `${pkg.version}${platform.isSingleFile() ? ' (single-file build)' : ''}`);
    add('OS', `${platform.OS_LABEL} ${os.release()} ${process.arch}`);
    if (platform.runtime() === 'deno') add('Deno', process.versions.deno);
    else {
      const major = Number(process.versions.node.split('.')[0]);
      add('Node.js', process.versions.node, major >= 18);
    }
```

Run: `git grep -n "isSea" -- lib bin server.js mcp.js test | grep -v "test/sea.test.js\|scripts/"`
Expected: only the `isSea` lines in `lib/evals/claude-cli.js` and `test/evals-claude-cli.test.js` (next steps).

- [ ] **Step 8: Write the failing judge test**

In `test/evals-claude-cli.test.js` (hand edit) change line 10 to:

```js
const { fakeClaude, winEnv, NODE } = require('./helpers');
```

Replace every `isSea: () => true` in the file (5 places, lines 454–496) with `useOwnNode: () => false`. Replace both `exec "${process.execPath}" "$@"` (lines 471 and 489) with `exec "${NODE}" "$@"`. Replace line 576 `  const bin = { file: process.execPath, args: [broken] };` with `  const bin = { file: NODE, args: [broken] };`. Then, after the test that ends at line 503, add:

```js
test('discovery: by default a script runs through this process only under Node from source', { skip: !POSIX }, async () => {
  const script = fakeClaude(tmp('zp-claude-js-'), 'claude.js');
  const nodeDir = tmp('zp-claude-node-');
  fs.writeFileSync(path.join(nodeDir, 'node'), `#!/bin/sh\nexec "${NODE}" "$@"\n`, { mode: 0o755 });
  const bin = await createClaude({
    env: { ZAAFPULSE_CLAUDE_BIN: script, PATH: nodeDir },
    home: tmp(),
    systemDirs: [],
    nodeDirs: [],
  }).find();
  if (process.versions.deno) assert.equal(bin.file, path.join(nodeDir, 'node'), 'a real node, never deno');
  else assert.equal(bin.file, process.execPath);
  assert.deepEqual(bin.args, [script]);
});
```

In `test/helpers.js` replace the line `function fakeClaude(dir, name = process.platform === 'win32' ? 'claude.js' : 'claude') {` with:

```js
// The node that runs the fakes and the scripts the tests hand to a node: this process's own under Node; under Deno,
// whose process.execPath is deno, the node on PATH (an absolute path, so a test that empties PATH still runs it).
const NODE = process.versions.deno
  ? require('child_process')
      .execFileSync(process.platform === 'win32' ? 'where' : 'which', ['node'], { encoding: 'utf8' })
      .split(/\r?\n/)[0]
      .trim()
  : process.execPath;
function fakeClaude(dir, name = process.platform === 'win32' ? 'claude.js' : 'claude') {
```

replace `  const node = /\s/.test(process.execPath) ? '/usr/bin/env node' : process.execPath;` with `  const node = /\s/.test(NODE) ? '/usr/bin/env node' : NODE;`, and the export line with:

```js
module.exports = { fakeServer, secretsFor, NO_JUDGE_PORTS, fakeClaude, winEnv, NODE };
```

Run: `npx --yes prettier@3 --check test/helpers.js`
Expected: `All matched files use Prettier code style!`

- [ ] **Step 9: Run the group A files under Deno to see them fail**

Run:
```bash
export ZAAFPULSE_DATA="$(mktemp -d)" HOME="$(mktemp -d)"
NO_COLOR=1 deno test -A --no-check test/evals-claude-cli.test.js 2>&1 | grep -E "^(ok|FAILED) \|"
```
Expected: `FAILED | … | ≥ 1 failed` (the new test: `bin.file` is deno's path or the `useOwnNode` option is ignored).

- [ ] **Step 10: Implement `useOwnNode` in `lib/evals/claude-cli.js`**

(Hand edit; not prettier-clean.) Replace lines 218–222:

```js
  // Where a real node is looked for when this process is the single-file build (its execPath is not node).
  nodeDirs = platform === 'win32'
    ? [env.ProgramFiles && path.win32.join(env.ProgramFiles, 'nodejs')].filter(Boolean)
    : ['/opt/homebrew/bin', '/usr/local/bin', '/usr/bin'],
  isSea = () => require('../platform').isSea(),
```

with

```js
  // Where a real node is looked for when this process's execPath is not node (Deno, or a single-file build).
  nodeDirs = platform === 'win32'
    ? [env.ProgramFiles && path.win32.join(env.ProgramFiles, 'nodejs')].filter(Boolean)
    : ['/opt/homebrew/bin', '/usr/local/bin', '/usr/bin'],
  // Whether a script runs through this process's own executable: only under Node from source. Under Deno execPath is
  // deno, and in a single-file build it is ZAAFPulse itself.
  useOwnNode = () => {
    const p = require('../platform');
    return p.runtime() === 'node' && !p.isSingleFile();
  },
```

and in `nodeBin()` replace the comment and first line:

```js
  // The node that runs a script: this process's own, unless this is the single-file build, whose execPath is
  // ZAAFPulse itself; then a node on PATH or in the usual folders that answers `--version`, or none.
  function nodeBin() {
    if (!isSea()) return Promise.resolve(process.execPath);
```

with

```js
  // The node that runs a script: this process's own under Node from source; otherwise (Deno, a single-file build) a
  // node on PATH or in the usual folders that answers `--version`, or none.
  function nodeBin() {
    if (useOwnNode()) return Promise.resolve(process.execPath);
```

- [ ] **Step 11: Run the group A files on both runtimes**

Run:
```bash
export ZAAFPULSE_DATA="$(mktemp -d)" HOME="$(mktemp -d)"
NO_COLOR=1 deno test -A --no-check test/evals-claude-cli.test.js test/evals-models.test.js test/evals-cli.test.js test/evals-api.test.js test/evals-engine.test.js test/users-http.test.js 2>&1 | grep -E "^(ok|FAILED) \||=> "
node --test test/evals-claude-cli.test.js test/evals-models.test.js test/evals-cli.test.js test/evals-api.test.js test/evals-engine.test.js test/users-http.test.js test/runtime.test.js 2>&1 | grep -E "^ℹ (pass|fail)"
```
Expected under Deno: one failure only, `external: the timeout aborts the request, not only the wait` (group F, Task 3). Under Node: `ℹ fail 0`.

- [ ] **Step 12: Full suites**

Run:
```bash
npm test 2>&1 | grep -E "^ℹ (tests|pass|fail)"
export ZAAFPULSE_DATA="$(mktemp -d)" HOME="$(mktemp -d)"
NO_COLOR=1 deno test -A --no-check test/ > "$ZAAFPULSE_DATA/deno.log" 2>&1; scripts/migration/deno-test-summary.sh "$ZAAFPULSE_DATA/deno.log" "after T1"
```
Expected: Node `ℹ fail 0`. Deno: **11 failed** (47 − 36 group A), all in trace-registry, evals-store, cost-ui, cost-reports-ui, processes, trace-api, monitors-state and the one evals-models timeout test.

- [ ] **Step 13: Commit**

```bash
npx --yes prettier@3 --check lib/platform.js lib/service.js bin/zaafpulse.js test/helpers.js test/runtime.test.js
git add docs/migration/2026-11-10-p1-deno-spike-results.md docs/migration/2026-11-10-deno-go-migration-plan.md lib/platform.js lib/evals/claude-cli.js lib/service.js server.js bin/zaafpulse.js test/helpers.js test/evals-claude-cli.test.js test/runtime.test.js
git commit -m "Detect the runtime instead of SEA; the Claude Code judge runs scripts through a real node under Deno

Correct the P1 audit finding: the permission audit logs a write for pure reads, so the app never wrote into its install folder."
```

---

### Task 2: Test portability, groups B–E (spec T2)

**Files:**
- Modify: `lib/trace/lines.js:3,10,62`; `test/trace-registry.test.js:1196-1210`
- Modify: `test/helpers.js` (add `stubGlobal`); `test/cost-ui.test.js:7,71-93`; `test/cost-reports-ui.test.js:15,313-407`
- Modify: `lib/processes.js:242-247,291-293,326-328,341-344`; `test/processes.test.js:106-134,139-147`
- Modify: `test/monitors-state.test.js:592-596`; `test/evals-store.test.js:285`; `test/reqlogs.test.js:14,24`; `test/trace-api.test.js:34-37`

**Interfaces:**
- Consumes: `NODE` from Task 1 (unchanged here).
- Produces: `require('./lib/trace/lines').io: { createReadStream(file, opts) }` (a stub point); `test/helpers.js` `stubGlobal(name): { set(value|undefined): void, restore(): void }`; `processes.rows({ cwds?: boolean, platform?: string })`.

- [ ] **Step 1: Confirm the failures and their causes**

Run:
```bash
export ZAAFPULSE_DATA="$(mktemp -d)" HOME="$(mktemp -d)"
NO_COLOR=1 deno test -A --no-check test/trace-registry.test.js test/evals-store.test.js test/cost-ui.test.js test/cost-reports-ui.test.js test/processes.test.js test/monitors-state.test.js 2>&1 | grep -E "^(ok|FAILED) \||=> |^error"
NO_COLOR=1 deno test -A --no-check test/trace-api.test.js 2>&1 | grep -E "^(ok|FAILED) \|"
NO_COLOR=1 deno test -A --no-check test/reqlogs.test.js test/trace-api.test.js 2>&1 | grep -E "^(ok|FAILED) \|"
```
Expected: 9 failures in the first run (trace-registry 3 "Cannot mock property 'createReadStream'", evals-store 1 `0 !== 1`, cost-ui 1, cost-reports-ui 2, processes 1 "Cannot both specify accessors", monitors-state 1 "Missing expected exception"); `trace-api.test.js` alone `ok | 9 passed`; with reqlogs first `FAILED | 8 passed | 2 failed` (F5). These failing tests are the failing tests of this task.

- [ ] **Step 2: Group B — an own stub point in `lib/trace/lines.js`**

After `const fs = require('fs');` (line 3) add:

```js

// The file functions readLines uses, on an object of our own that a test can stub: Deno's node:fs refuses
// test.mock.method on its exports; an own object works on both runtimes.
const io = { createReadStream: (...a) => fs.createReadStream(...a) };
```

Replace `  const stream = fs.createReadStream(file, { start, highWaterMark });` with `  const stream = io.createReadStream(file, { start, highWaterMark });` and the last line with `module.exports = { readLines, readRange, io };`.

In `test/trace-registry.test.js` (hand edit) replace the start of `failLastReads`:

```js
async function failLastReads(t, n, run) {
  let calls = 0;
  const real = fs.createReadStream;
  const count = test.mock.method(fs, 'createReadStream', (...a) => (calls++, real(...a)));
```

with

```js
async function failLastReads(t, n, run) {
  const { io } = require('../lib/trace/lines');
  let calls = 0;
  const real = io.createReadStream;
  const count = test.mock.method(io, 'createReadStream', (...a) => (calls++, real(...a)));
```

and in the same function `const gone = test.mock.method(fs, 'createReadStream',` with `const gone = test.mock.method(io, 'createReadStream',`.

- [ ] **Step 3: Group C — `stubGlobal` in `test/helpers.js`**

Before the `module.exports` line add:

```js
// Replaces a global for one test. Deno defines some globals as accessors (localStorage, which Deno keeps on disk), and a
// plain assignment to one is ignored; defineProperty works on both runtimes. set(undefined) removes the global;
// restore() puts back what was there before.
function stubGlobal(name) {
  const saved = Object.getOwnPropertyDescriptor(globalThis, name);
  const set = (value) => {
    delete globalThis[name];
    if (value !== undefined)
      Object.defineProperty(globalThis, name, { value, configurable: true, writable: true, enumerable: true });
  };
  const restore = () => {
    delete globalThis[name];
    if (saved) Object.defineProperty(globalThis, name, saved);
  };
  return { set, restore };
}
```

and make the export `module.exports = { fakeServer, secretsFor, NO_JUDGE_PORTS, fakeClaude, winEnv, NODE, stubGlobal };`.

`test/cost-ui.test.js`: after `const path = require('path');` add `const { stubGlobal } = require('./helpers');`. Replace the test body of `controls: localStorage is optional; …` (lines 72–92) with:

```js
  const store = stubGlobal('localStorage');
  try {
    const box = new Map();
    store.set({ getItem: (k) => box.get(k) ?? null, setItem: (k, v) => box.set(k, v) });
    U.costStoreSet({ range: '7d', cur: 'USD', measure: 'list' });
    assert.equal(box.get(U.COST_STORE_KEY), JSON.stringify({ range: '7d', cur: 'USD', measure: 'list' }));
    assert.equal(U.costStoreGet(), box.get('zp.cost.controls'));
    store.set({
      getItem: () => {
        throw new Error('blocked');
      },
      setItem: () => {
        throw new Error('blocked');
      },
    });
    assert.equal(U.costStoreGet(), null);
    assert.doesNotThrow(() => U.costStoreSet({ range: '7d' }));
    store.set(undefined);
    assert.equal(U.costStoreGet(), null, 'no storage at all');
  } finally {
    store.restore();
  }
```

`test/cost-reports-ui.test.js`: after `const path = require('path');` add `const { stubGlobal } = require('./helpers');`. In both `fix round 1` and `fix round 2` tests: remove the line `    localStorage: global.localStorage,` from `saved` and add `  const store = stubGlobal('localStorage');` right after the `saved` object; replace `    if (saved.localStorage === undefined) delete global.localStorage;` with `    store.restore();`. In `fix round 1` replace `  global.localStorage = { getItem: () => JSON.stringify({ cur: 'IDR' }) };` with `  store.set({ getItem: () => JSON.stringify({ cur: 'IDR' }) });`. In `fix round 2` rename the local text variable so it does not clash: `  let store = JSON.stringify({ range: '7d', cur: 'IDR' });` → `  let stored = JSON.stringify({ range: '7d', cur: 'IDR' });`, `  global.localStorage = { getItem: () => store, setItem: (k, v) => (store = v) };` → `  store.set({ getItem: () => stored, setItem: (k, v) => (stored = v) });`, and `assert.equal(JSON.parse(store).range, '7d', …)` → `assert.equal(JSON.parse(stored).range, '7d', …)`.

Check no server code touches the global store: `git grep -n localStorage -- lib server.js mcp.js bin` → expected: no output.

- [ ] **Step 4: Group D — `rows({ platform })` in `lib/processes.js`**

Replace `rowsForOs` (lines 242–247) with:

```js
const rowsForOs = (platform = process.platform) =>
  platform === 'linux' ? Promise.resolve().then(() => procRows()) : platform === 'win32' ? winRows() : psRows();
```

`withCwds`: signature `function withCwds(rows, wanted = (r) => CLAUDE.re.test(r.args), platform = process.platform) {` and its check `if (platform !== 'darwin' || !pids.length) return Promise.resolve(rows);`. `withArgv`: signature `function withArgv(rows, wanted, platform = process.platform) {` and the same check. Replace `rows`:

```js
// `platform` is for the tests (a pretend macOS); it defaults to this machine's.
const rows = async ({ cwds = true, platform = process.platform } = {}) => {
  const all = await rowsForOs(platform);
  return withArgv(cwds ? await withCwds(all, isAgent, platform) : all, isAgent, platform);
};
```

`snapshot()` keeps calling `withCwds(await rowsForOs())` (defaults).

`test/processes.test.js`: replace the comment above `onFakeMac` with:

```js
// lib/processes takes execFile when it loads: a fresh copy is loaded with a recording execFile, and rows() is told the
// platform (a pretend macOS), so nothing real is spawned.
```

delete `  const realPlatform = Object.getOwnPropertyDescriptor(process, 'platform');`, `    Object.defineProperty(process, 'platform', { ...realPlatform, value: 'darwin' });` and `    Object.defineProperty(process, 'platform', realPlatform);`, and in the test `rows({ cwds: false }) reads argv…` use `fresh.rows({ cwds: false, platform: 'darwin' })` and `fresh.rows({ platform: 'darwin' })`. Check `grep -n onFakeMac test/processes.test.js` shows the definition and one use.

- [ ] **Step 5: Group E — the folder in the way must not be empty (F4)**

`test/monitors-state.test.js`, test `a failed snooze save puts the snoozed flags back too`: after `  fs.mkdirSync(\`${file}.tmp\`);` add

```js
  fs.writeFileSync(path.join(`${file}.tmp`, 'keep'), ''); // not empty: Deno's unlinkSync removes an empty folder
```

and replace `  fs.rmdirSync(\`${file}.tmp\`);` with `  fs.rmSync(\`${file}.tmp\`, { recursive: true });`.

`test/evals-store.test.js` (hand edit), test `a failing compaction inside put is logged once and does not throw`: after `  fs.mkdirSync(\`${file}.tmp\`); // a directory in the way makes every rewrite fail` add the same `fs.writeFileSync(path.join(\`${file}.tmp\`, 'keep'), '');` line with the same comment.

- [ ] **Step 6: Group E — no environment leaks between test files (F5)**

`test/reqlogs.test.js`: before `Object.assign(process.env, {` add

```js
// Put back when the file is done: `deno test` runs every test file in one process, so a later file would see them.
const ENV_KEYS = ['HOME', 'USERPROFILE', 'ZAAFPULSE_CLAUDE_DIRS', 'CLAUDE_CONFIG_DIR', 'XDG_CONFIG_HOME', 'CODEX_HOME'];
const savedEnv = Object.fromEntries(ENV_KEYS.map((k) => [k, process.env[k]]));
```

and replace `test.after(() => fs.rmSync(home, { recursive: true, force: true }));` with

```js
test.after(() => {
  for (const k of ENV_KEYS) {
    if (savedEnv[k] === undefined) delete process.env[k];
    else process.env[k] = savedEnv[k];
  }
  fs.rmSync(home, { recursive: true, force: true });
});
```

`test/trace-api.test.js` (hand edit) in `startServer`'s `env`, after `        DEMO: '',` add

```js
        // The log roots come from HOME below, never from a variable an earlier test file left set.
        ZAAFPULSE_CLAUDE_DIRS: '',
        CLAUDE_CONFIG_DIR: '',
        CODEX_HOME: '',
        XDG_CONFIG_HOME: '',
```

(`''` is how `test/reqlogs.test.js` already says "unset" to the log-root code.)

- [ ] **Step 7: Run the touched files on both runtimes**

Run:
```bash
export ZAAFPULSE_DATA="$(mktemp -d)" HOME="$(mktemp -d)"
F="test/trace-registry.test.js test/evals-store.test.js test/cost-ui.test.js test/cost-reports-ui.test.js test/processes.test.js test/monitors-state.test.js test/reqlogs.test.js test/trace-api.test.js"
NO_COLOR=1 deno test -A --no-check $F 2>&1 | grep -E "^(ok|FAILED) \||=> "
node --test $F 2>&1 | grep -E "^ℹ (pass|fail)"
npx --yes prettier@3 --check lib/trace/lines.js lib/processes.js test/helpers.js test/cost-ui.test.js test/cost-reports-ui.test.js test/processes.test.js test/monitors-state.test.js test/reqlogs.test.js
```
Expected: Deno `ok | … | 0 failed` (measured on a scratch copy: these fixes give 163 + 38 passed, 0 failed); Node `ℹ fail 0`; prettier clean.

- [ ] **Step 8: Full suites**

Run the two commands of Task 1 Step 12.
Expected: Node `ℹ fail 0`; Deno **1 failed** (the group F test).

- [ ] **Step 9: Commit**

```bash
git add lib/trace/lines.js lib/processes.js test/trace-registry.test.js test/helpers.js test/cost-ui.test.js test/cost-reports-ui.test.js test/processes.test.js test/monitors-state.test.js test/evals-store.test.js test/reqlogs.test.js test/trace-api.test.js
git commit -m "Make the test seams work under Deno: own stub points, stubGlobal, an injected platform, no env leaks between files"
```

---

### Task 3: Group F, the timeout test (spec T3)

**Files:**
- Modify: `test/evals-models.test.js:378-400`
- Create: `docs/migration/2026-11-10-p2-results.md` (started here, completed in Task 7)

**Interfaces:**
- Consumes: nothing new.
- Produces: `docs/migration/2026-11-10-p2-results.md` with a "Group F" section (Task 7 appends).

- [ ] **Step 1: Reproduce in isolation**

Run:
```bash
NO_COLOR=1 deno test -A --no-check --filter "timeout aborts the request" test/evals-models.test.js 2>&1 | grep -E "^(ok|FAILED) \||^error"
node --test --test-name-pattern "timeout aborts the request" test/evals-models.test.js 2>&1 | grep -E "^ℹ (pass|fail)"
```
Expected: Deno `error: Promise resolution is still pending but the event loop has already resolved.` and `FAILED | 0 passed | 1 failed`; Node `ℹ pass 1`.

- [ ] **Step 2: Prove the root cause (F6) with two probes**

Write `$TMPDIR/zp-f/unref.cjs` (a temp folder, never committed):

```js
const test = require('node:test');
test('unref timer only', async () => {
  await new Promise((res) => setTimeout(res, 50).unref());
});
test('ref timer', async () => {
  await new Promise((res) => setTimeout(res, 50));
});
```

and `$TMPDIR/zp-f/abort.cjs`:

```js
// A request that never answers; the client aborts after 50 ms. Does the server see the socket close?
const http = require('http');
let t0;
const srv = http.createServer((req) => {
  req.socket.on('close', () => {
    console.log('server: socket closed after', Date.now() - t0, 'ms');
    srv.close();
  });
});
srv.listen(0, '127.0.0.1', async () => {
  const ctl = new AbortController();
  t0 = Date.now();
  setTimeout(() => ctl.abort(), 50);
  try {
    await fetch(`http://127.0.0.1:${srv.address().port}/`, { signal: ctl.signal });
  } catch (e) {
    console.log('client:', e.name);
  }
});
```

Run:
```bash
mkdir -p "$TMPDIR/zp-f" && cd "$TMPDIR/zp-f" && echo '{"type":"commonjs"}' > package.json
NO_COLOR=1 deno test --no-check unref.cjs 2>&1 | grep -E "ok|FAILED|^error"; node --test unref.cjs 2>&1 | grep -E "^ℹ (pass|fail)"
deno run --allow-net=127.0.0.1 abort.cjs; node abort.cjs; cd - >/dev/null; rm -rf "$TMPDIR/zp-f"
```
Expected: Deno `unref timer only … FAILED`, `ref timer … ok`, the same "event loop has already resolved" error; Node `ℹ pass 2`. Both runtimes print `client: AbortError` and `server: socket closed after ~54 ms`.

Conclusion (written in Step 4): the product's `ctl.abort()` reaches the socket on both runtimes; the timer is unref'd on purpose and is always cleared in `finally`, so a real call (whose fetch keeps the loop alive) times out correctly. Only the test, whose fake `complete()` holds no handle, leaves nothing to keep Deno's loop alive. No product change.

- [ ] **Step 3: Fix the test**

In `test/evals-models.test.js` (hand edit) replace the body of `external: the timeout aborts the request, not only the wait` with:

```js
test('external: the timeout aborts the request, not only the wait', async () => {
  // The call's timer is unref'd, and this fake upstream holds no socket: keep the event loop alive for the test, as a
  // real request would. Deno's test runner ends a test whose loop is empty; Node's does not.
  const keepAlive = setInterval(() => {}, 1000);
  try {
    let signal;
    const gateway = cloud({
      complete: ({ signal: s }) => {
        signal = s;
        return new Promise((_, rej) => s.addEventListener('abort', () => rej(new Error('aborted upstream'))));
      },
    });
    const m = createModels({
      settings: () => ({ model: 'big-1', provider: 'cloud' }),
      gateway,
      ollamaUrl: 'http://127.0.0.1:9',
      lmstudioUrl: 'http://127.0.0.1:9',
      callMs: 50,
    });
    const r = await m.resolve();
    await assert.rejects(m.call(r, { system: 's', user: 'u', schema: SCHEMA }), (e) => {
      assert.equal(e.status, 0);
      assert.match(e.message, /timed out/);
      return true;
    });
    assert.equal(signal.aborted, true);
  } finally {
    clearInterval(keepAlive);
  }
});
```

Run the two commands of Step 1.
Expected: Deno `ok | 1 passed | 0 failed | 34 filtered out`; Node `ℹ pass 1`.

- [ ] **Step 4: Write the root cause down**

Create `docs/migration/2026-11-10-p2-results.md`:

```markdown
# P2 Control plane on Deno — results

Measured on the owner's MacBook (Apple Silicon), Deno 2.9.8, Node 24. Plan:
`docs/superpowers/plans/2026-11-10-p2-control-plane-deno.md`.

## Root causes of the 47 Deno failures (P1 §5)

| Group | Tests | Root cause (measured) | Fix |
|---|---|---|---|
| A | 36 | `nodeBin()` ran Claude Code's `cli.js` through `process.execPath`, which is deno; the test fakes' shebang named deno | `useOwnNode` (product); fakes run by an absolute real node |
| B | 3 (trace-registry) | Deno's `node:fs` exports refuse `test.mock.method` | own stub point `lib/trace/lines.js io` |
| C | 3 (cost-ui, cost-reports-ui) | `globalThis.localStorage` is an accessor; assignment is ignored | `stubGlobal` (defineProperty) |
| D | 1 (processes) | `process.platform` is an accessor | `rows({ platform })` |
| E | 2 (trace-api) | `test/reqlogs.test.js` left HOME and log-root variables set; `deno test` runs all files in one process | restore in `test.after`; the spawned server gets explicit empty log-root variables |
| E | 2 (monitors-state, evals-store; P1 had evals-store in B) | Deno's `fs.unlinkSync` removes an empty directory (Node: EPERM), so the folder that should block the write vanished | the blocking folder is not empty |
| F | 1 (evals-models) | the only pending work is an unref'd timer; Deno's test runner ends the test when the loop is empty | the test keeps a ref'd interval; the abort reaches the socket on both runtimes (probe), no product change |
```

- [ ] **Step 5: Full suites**

Run the two commands of Task 1 Step 12.
Expected: Node `ℹ fail 0`; Deno `ok | … passed | 0 failed | 3 ignored` (**100% under `-A`**).

- [ ] **Step 6: Commit**

```bash
git add test/evals-models.test.js docs/migration/2026-11-10-p2-results.md
git commit -m "Keep the event loop alive in the judge timeout test; record the root causes of the 47 Deno failures"
```

---

### Task 4: Deno suite blocking, with the `test` permission set (spec T4)

Deviation from the spec, on purpose: the spec puts `deno.json` in T5. The CI job must stop using `-A` in this task, and writing the scoped grant once as flags in `ci.yml` and again as a set in Task 5 would put the same list in two places. So this task creates `deno.json` with the `test` set only; Task 5 adds the other three.

**Files:**
- Create: `deno.json`
- Modify: `lib/processes.js:73-83` (F9)
- Modify: `.github/workflows/ci.yml` (header comment, `deno` job)

**Interfaces:**
- Consumes: Tasks 1–3 (100% under `-A`).
- Produces: `deno.json` `permissions.test` (Task 5 adds `server`, `cli`, `mcp-stdio` and tests every entry); CI job id `deno`, blocking, running `deno test -q --no-check --no-prompt -P=test`.

- [ ] **Step 1: Create `deno.json` with the `test` set**

```json
{
  "permissions": {
    "test": {
      "read": true,
      "write": true,
      "net": ["127.0.0.1", "localhost"],
      "env": {
        "allow": [
          "ZAAFPULSE_*", "ZP_*", "ACC_*", "OTEL_*", "XDG_*", "NODE_TEST_*", "DEMO", "HOST", "PORT",
          "HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "ProgramFiles", "TEMP", "TMP", "TMPDIR",
          "PATH", "Path", "PATHEXT", "SystemRoot", "SYSTEMROOT", "COMPUTERNAME",
          "USER", "USERNAME", "LOGNAME", "LANG", "LC_*", "TERM", "TERM_PROGRAM", "SHELL",
          "DISPLAY", "WAYLAND_DISPLAY", "DBUS_SESSION_BUS_ADDRESS",
          "CLAUDE_CONFIG_DIR", "CODEX_HOME", "CLAUDECODE", "CLAUDE_CODE_*", "ANTHROPIC_*", "VSCODE_*",
          "ELECTRON_RUN_AS_NODE", "GIT_*", "NODE_V8_COVERAGE"
        ],
        "ignore": true
      },
      "sys": ["homedir", "uid", "gid", "userInfo", "hostname", "osRelease", "systemMemoryInfo", "osUptime", "loadavg", "cpus", "umask", "networkInterfaces"],
      "run": true
    }
  }
}
```

Why each broad entry (copied into `lib/permissions.js` in Task 5): **write: true** — `Deno.symlinkSync()` requires unscoped read and write (F14) and 12 tests make symlinks (log-roots 4, platform 1, agents-control 1, register 1, runtimes-control 1, runtimes-state 1, trace-content 3, trust 1), and `test/platform.test.js` checks the data-folder rule against the checkout itself; **run: true** — the fakes run by absolute paths in temp folders, and Deno cannot scope run by folder; spawning `process.execPath` gives the child all permissions anyway (F10). **net** is this machine only: the test process never reaches the network. **env** is the names the suite reads or sets, the rest read as unset.

Run: `deno ci && git status --short`
Expected: `deno ci` installs `@types/node` and `postject` as before; `deno.lock` unchanged (measured); `git status` shows `?? deno.json`.

- [ ] **Step 2: Run the suite with the set to see the scoped grant's failure**

Run:
```bash
export ZAAFPULSE_DATA="$(mktemp -d)" HOME="$(mktemp -d)"
NO_COLOR=1 deno test -q --no-check --no-prompt -P=test test/ > "$ZAAFPULSE_DATA/deno.log" 2>&1; scripts/migration/deno-test-summary.sh "$ZAAFPULSE_DATA/deno.log" "test set"
```
Expected: exactly **1 failed**: `installed apps: Claude Code counts as installed when any resolved log folder exists => ./test/log-roots.test.js:183:1`, error `Empty path is not allowed` (F9). (With a scoped write, 14 more fail on `Deno.symlinkSync`; that is why the set has `write: true`.)

- [ ] **Step 3: Fix F9 in `lib/processes.js`**

Replace the last line of `installed()`:

```js
  return APPS.filter((a) => (logs[a.id] || a.paths).some((p) => fs.existsSync(expand(p)))).map((a) => a.id);
```

with

```js
  // A %LOCALAPPDATA% path where that variable is unset expands to '' and is skipped: under Deno, existsSync('') throws
  // once read permission is granted by flag instead of -A (Node returns false).
  return APPS.filter((a) =>
    (logs[a.id] || a.paths).some((p) => {
      const x = expand(p);
      return x !== '' && fs.existsSync(x);
    }),
  ).map((a) => a.id);
```

Run Step 2's command again, then `npx --yes prettier@3 --check lib/processes.js && npm test 2>&1 | grep -E "^ℹ (pass|fail)"`.
Expected: Deno `ok | … | 0 failed | 3 ignored`; prettier clean; Node `ℹ fail 0`.

- [ ] **Step 4: The contract suite under the set**

Run: `ZP_RUNTIME=deno NO_COLOR=1 deno test -q --no-check --no-prompt -P=test test/contract/ 2>&1 | tail -1`
Expected: `ok | 172 passed | 0 failed`.

- [ ] **Step 5: Make the CI job blocking and scoped**

In `.github/workflows/ci.yml` replace the last two header comment lines (`# The deno job runs the same suite …` and `# failures, P2 work: …`) with:

```yaml
# The deno job runs the same suite and the contract suite under the pinned Deno with the "test" permission set of
# deno.json (never -A), and blocks like the Node job (P2 T4). The Node job keeps running until the cut-over (P7).
```

In the `deno` job: `name: deno (${{ matrix.os }})`; delete the line `    continue-on-error: true # P0: reported, not blocking`; in `Test suite under Deno` replace `deno test -A --no-check test/*.test.js` with `deno test -q --no-check --no-prompt -P=test test/*.test.js`; replace the contract step's `run:` block with:

```yaml
        run: |
          set +e
          # The "test" set: read anywhere, write and run unscoped (symlinks and fakes, see deno.json), net to this
          # machine only. The harness starts server.js under its own narrow set.
          ZP_RUNTIME=deno deno test -q --no-check --no-prompt -P=test test/contract/ > "$RUNNER_TEMP/deno-contract.log" 2>&1
          code=$?
          set -e
          scripts/migration/deno-test-summary.sh "$RUNNER_TEMP/deno-contract.log" "contract suite under Deno (${{ matrix.os }})" \
            | tee -a "$RUNNER_TEMP/deno-summary.md" >> "$GITHUB_STEP_SUMMARY"
          exit $code
```

and change the contract step's comment to `# Runs even when the unit suite above failed, so both results are reported.`

Run: `grep -n -- "-A\b\|continue-on-error" .github/workflows/ci.yml`
Expected: no output.

- [ ] **Step 6: Check the summary script still reports counts**

Run: `scripts/migration/deno-test-summary.sh "$ZAAFPULSE_DATA/deno.log" "local"`
Expected: a table with `| <passed> | 0 | 3 |`. (`deno test -q` keeps the `ok | … passed | … failed` summary line the script parses; measured.)

- [ ] **Step 7: Commit**

```bash
git add deno.json lib/processes.js .github/workflows/ci.yml
git commit -m "Run the Deno suite blocking with the test permission set; skip an empty install path instead of throwing under Deno"
```

---

### Task 5: The permission matrix (spec T5)

**Files:**
- Modify: `deno.json` (add `server`, `cli`, `mcp-stdio`)
- Create: `lib/permissions.js`, `test/permissions.test.js`, `test/fixtures/permissions/probe.js`
- Modify: `lib/gateway/skills.js:49-58,154` (`skillRoots`)
- Modify: `lib/platform.js` (`mcpCommand`), `lib/service.js` (`startCommand`, `sh`)
- Modify: `lib/secrets.js:49-51`, `lib/secret-store.js:17-20,54-120`
- Modify: `lib/governance/controls.js` (control), `server.js` (`controlState` part)
- Modify: `test/governance-controls.test.js:13-61,65-87,420`, `test/governance-inventory-http.test.js:222,270`, `test/platform.test.js:288`
- Modify: `test/contract/harness.js:43-66`

**Interfaces:**
- Consumes: `platform.runtime()`, `platform.isSingleFile()`, `platform.APP_DIR`, `platform.dataDir()` (Task 1); `deno.json` `test` set (Task 4); `clients(home, { env, platform })` from `lib/mcp-clients.js`; `ownerEntries(list)` from `lib/runtimes/registry.js`.
- Produces (`lib/permissions.js`):
  - `SETS: ['server', 'cli', 'mcp-stdio', 'test']`, `CONFIG: string` (absolute path of `deno.json`), `WHY: { run: Record<string,string>, sys: Record<string,string>, env: Record<string,string> }`;
  - `readSets(file?: string): Record<string, object>`;
  - `writeScope({ set, dataDir, home?, env?, platform?, tmp? }): string[]`;
  - `runScope({ set, config?, env? }): string[]`;
  - `selfCommand({ script, sub, set, dataDir, env?, config? }): { command: string, args: string[] }`;
  - `describe({ deno? }): { runtime: 'node' } | { runtime: 'deno', version: string, singleFile: boolean, grants: Record<'read'|'write'|'net'|'env'|'run'|'sys', 'all'|'all but denied'|'scoped'|'unknown'> }`;
  - `envBlocked(name, { deno? }): boolean`; `spawnRefused(result): boolean`.
  - `lib/gateway/skills.js` exports `skillRoots({ home?, env? }): { claude: string, agents: string }`.
  - Governance control id `runtime_permissions`, state part `runtimePermissions` (= `describe()`).

- [ ] **Step 1: Add the three sets to `deno.json`**

The candidate sets were run on the real server, `mcp.js` and the CLI with `--no-prompt` (F15, F16). Add next to `"test"`:

```json
    "server": {
      "read": true,
      "write": true,
      "net": { "allow": true, "deny": ["169.254.169.254", "metadata.google.internal"] },
      "env": {
        "allow": [
          "ZAAFPULSE_*", "ZP_*", "ACC_*", "OTEL_*", "XDG_*", "DEMO", "HOST", "PORT",
          "HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "ProgramFiles", "TEMP", "TMP", "TMPDIR",
          "PATH", "Path", "PATHEXT", "SystemRoot", "SYSTEMROOT", "COMPUTERNAME",
          "USER", "USERNAME", "LOGNAME", "LANG", "LC_*", "TERM", "SHELL",
          "DISPLAY", "WAYLAND_DISPLAY", "DBUS_SESSION_BUS_ADDRESS",
          "CLAUDE_CONFIG_DIR", "CODEX_HOME", "ANTHROPIC_*", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX",
          "AWS_*", "CLOUD_ML_REGION", "GOOGLE_APPLICATION_CREDENTIALS",
          "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy",
          "NODE_EXTRA_CA_CERTS", "SSL_CERT_FILE", "NODE_V8_COVERAGE"
        ],
        "ignore": true
      },
      "sys": ["homedir", "uid", "gid", "userInfo", "hostname", "systemMemoryInfo", "osUptime", "loadavg", "cpus", "umask"],
      "run": [
        "ps", "lsof", "vm_stat", "scutil", "id", "hostnamectl", "getent", "which", "where",
        "security", "secret-tool", "powershell.exe", "powershell", "osascript", "notify-send", "taskkill",
        "launchctl", "open", "xdg-open", "cmd", "claude", "node",
        "ollama", "lms", "llama-server", "local-ai", "brew"
      ]
    },
    "cli": {
      "read": true,
      "write": true,
      "net": { "allow": true, "deny": ["169.254.169.254", "metadata.google.internal"] },
      "env": {
        "allow": [
          "ZAAFPULSE_*", "ZP_*", "ACC_*", "OTEL_*", "XDG_*", "DEMO", "HOST", "PORT",
          "HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "ProgramFiles", "TEMP", "TMP", "TMPDIR",
          "PATH", "Path", "PATHEXT", "SystemRoot", "SYSTEMROOT", "COMPUTERNAME",
          "USER", "USERNAME", "LOGNAME", "LANG", "LC_*", "TERM", "SHELL",
          "DISPLAY", "WAYLAND_DISPLAY", "DBUS_SESSION_BUS_ADDRESS",
          "CLAUDE_CONFIG_DIR", "CODEX_HOME", "ANTHROPIC_*", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX",
          "AWS_*", "CLOUD_ML_REGION", "GOOGLE_APPLICATION_CREDENTIALS",
          "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy",
          "NODE_EXTRA_CA_CERTS", "SSL_CERT_FILE", "NODE_V8_COVERAGE"
        ],
        "ignore": true
      },
      "sys": ["homedir", "uid", "gid", "userInfo", "hostname", "systemMemoryInfo", "osUptime", "loadavg", "cpus", "umask", "osRelease"],
      "run": [
        "ps", "lsof", "vm_stat", "scutil", "id", "hostnamectl", "getent", "which", "where",
        "security", "secret-tool", "powershell.exe", "powershell", "osascript", "notify-send", "taskkill",
        "launchctl", "open", "xdg-open", "cmd", "claude", "node",
        "ollama", "lms", "llama-server", "local-ai", "brew",
        "systemctl", "loginctl", "useradd", "chown", "schtasks", "wscript.exe"
      ]
    },
    "mcp-stdio": {
      "read": true,
      "write": true,
      "net": { "allow": true, "deny": ["169.254.169.254", "metadata.google.internal"] },
      "env": {
        "allow": [
          "ZAAFPULSE_*", "ZP_*", "ACC_*", "OTEL_*", "XDG_*", "DEMO", "HOST", "PORT",
          "HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "ProgramFiles", "TEMP", "TMP", "TMPDIR",
          "PATH", "Path", "PATHEXT", "SystemRoot", "SYSTEMROOT", "COMPUTERNAME",
          "USER", "USERNAME", "LOGNAME", "LANG", "LC_*", "TERM", "SHELL",
          "DISPLAY", "WAYLAND_DISPLAY", "DBUS_SESSION_BUS_ADDRESS",
          "CLAUDE_CONFIG_DIR", "CODEX_HOME", "ANTHROPIC_*", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX",
          "AWS_*", "CLOUD_ML_REGION", "GOOGLE_APPLICATION_CREDENTIALS",
          "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy",
          "NODE_EXTRA_CA_CERTS", "SSL_CERT_FILE", "NODE_V8_COVERAGE"
        ],
        "ignore": true
      },
      "sys": ["homedir", "uid", "gid", "userInfo", "systemMemoryInfo", "osUptime", "loadavg", "cpus", "umask"],
      "run": ["ps", "lsof", "vm_stat", "powershell.exe", "which", "where", "security", "secret-tool"]
    }
```

`os.userInfo()` needs `uid`, `gid` and `userInfo` together (measured on macOS and Linux, Deno 2.9.8; `lib/platform.js` `fullName()`, `lib/service.js`), so every set lists all three (Task 4 fix round 1). JSON has no references, so the env list is written three times and the `server` run list twice; `test/permissions.test.js` (Step 2) fails if they drift. The final `deno.json` is `{ "permissions": { "test": …, "server": …, "cli": …, "mcp-stdio": … } }`, the `test` set unchanged from Task 4.

Why the broad entries are broad (also in `WHY` and on the Compliance control): **read: true** — the indexer reads agent logs under the home folder and owner-chosen folders (Obsidian vaults, `ZAAFPULSE_CLAUDE_DIRS`), runtime detection reads `/opt/homebrew`, `/usr/local`, `/Applications`, `/proc`; **write: true** in the file, narrowed at every `deno run` launch to `writeScope()` (CLI flags replace the set's entry, F11), unscoped only in the compiled binary (spec finding 1); **net: all but cloud metadata** — providers, connectors and price sync go out until P5 moves egress to Go; the two metadata names are denied outright. The write scope grants each MCP client's config folder; for Codex and Gemini CLI that is `~/.codex` and `~/.gemini`, which also hold their logs (`clients()` in `lib/mcp-clients.js` names them; measured list: `.kiro/settings`, `.gemini/config`, `.cursor`, `Library/Application Support/Code/User`, `Library/Application Support/Claude`, `.codex`, `.gemini`, `.config/devin`, `.codeium/windsurf`, `.config/zed`; `~/.claude.json` is skipped because its folder is the home). `osRelease` is in `cli` only (`doctor`); `hostname` is not in `mcp-stdio` (measured unused); `mcp.js` runs no CLI, no runtime and no notification.

- [ ] **Step 2: Write the failing static matrix tests**

Create `test/permissions.test.js` (part 1, runs under both runtimes):

```js
// The Deno permission matrix (P2 spec D3, D4): deno.json's sets, the reason for every entry, the write and run scopes
// of a `deno run` launch, and per-permission checks that a feature works and its denied neighbour fails loudly.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const os = require('os');
const path = require('path');
const { spawnSync } = require('child_process');
const permissions = require('../lib/permissions');

const tmp = (p = 'zp-perm-') => fs.mkdtempSync(path.join(os.tmpdir(), p));
const sets = permissions.readSets();
const list = (v) => (Array.isArray(v) ? v : v && Array.isArray(v.allow) ? v.allow : []);

test('deno.json has exactly the four sets, and nothing grants all', () => {
  assert.deepEqual(Object.keys(sets).sort(), ['cli', 'mcp-stdio', 'server', 'test']);
  assert.ok(!JSON.stringify(sets).includes('"all"'));
  const raw = JSON.parse(fs.readFileSync(permissions.CONFIG, 'utf8'));
  assert.deepEqual(Object.keys(raw), ['permissions'], 'deno.json holds only Deno config: the permission sets');
});

test('every allow-listed run, sys and env entry has a reason, and every reason is used', () => {
  for (const kind of ['run', 'sys', 'env']) {
    const used = new Set();
    for (const name of ['server', 'cli', 'mcp-stdio']) {
      for (const e of list(sets[name][kind])) {
        assert.ok(permissions.WHY[kind][e], `${name}.${kind}: ${e} has no reason in lib/permissions.js WHY`);
        used.add(e);
      }
    }
    for (const e of Object.keys(permissions.WHY[kind])) assert.ok(used.has(e), `WHY.${kind}.${e} is in no set`);
  }
});

test('cli holds everything server holds; server and mcp-stdio share the env list', () => {
  for (const kind of ['run', 'sys', 'env']) {
    const cli = new Set(list(sets.cli[kind]));
    for (const e of list(sets.server[kind])) assert.ok(cli.has(e), `cli.${kind} lacks ${e}`);
  }
  assert.deepEqual(list(sets['mcp-stdio'].env), list(sets.server.env));
  assert.deepEqual(list(sets.cli.env), list(sets.server.env));
  for (const name of ['server', 'cli', 'mcp-stdio']) {
    assert.equal(sets[name].env.ignore, true, `${name}: other variables read as unset, never throw`);
    assert.deepEqual(sets[name].net.deny, ['169.254.169.254', 'metadata.google.internal']);
  }
});

test('writeScope: the data folder and temp; server adds the agent skill and MCP client folders, never the home', () => {
  const home = tmp('zp-perm-home-');
  const data = path.join(home, 'data');
  const env = { HOME: home };
  const mcp = permissions.writeScope({ set: 'mcp-stdio', dataDir: data, home, env, platform: 'darwin', tmp: '/t' });
  assert.deepEqual(mcp, [data, '/t']);
  const server = permissions.writeScope({ set: 'server', dataDir: data, home, env, platform: 'darwin', tmp: '/t' });
  assert.ok(server.includes(path.join(home, '.claude', 'skills')));
  assert.ok(server.includes(path.join(home, '.agents', 'skills')));
  assert.ok(server.includes(path.join(home, '.kiro', 'settings')));
  assert.ok(!server.includes(home) && !server.includes('/'), 'never the home folder or the root');
  const cli = permissions.writeScope({ set: 'cli', dataDir: data, home, env, platform: 'darwin', tmp: '/t' });
  assert.ok(cli.includes(path.join(home, 'Library', 'LaunchAgents')));
  assert.ok(!server.includes(path.join(home, 'Library', 'LaunchAgents')), 'the service folders are for the CLI');
});

test('runScope: the set, plus the owner runtimes and ZAAFPULSE_CLAUDE_BIN for a deno run launch', () => {
  const r = permissions.runScope({
    set: 'server',
    config: { localRuntimes: [{ id: 'mine', command: ['/opt/x/bin/my-server', '--port', '9000'], port: 9000 }] },
    env: { ZAAFPULSE_CLAUDE_BIN: '/opt/claude/claude' },
  });
  assert.ok(r.includes('ps') && r.includes('/opt/x/bin/my-server') && r.includes('/opt/claude/claude'));
  assert.equal(new Set(r).size, r.length);
});

test('selfCommand: node from source, deno run with the set and computed scopes, or the single-file binary', () => {
  const data = tmp();
  const node = permissions.selfCommand({ script: 'mcp.js', sub: 'mcp', set: 'mcp-stdio', dataDir: data, runtime: 'node', singleFile: false });
  assert.deepEqual(node, { command: process.execPath, args: [path.join(permissions.APP_DIR, 'mcp.js')] });
  const bin = permissions.selfCommand({ script: 'mcp.js', sub: 'mcp', set: 'mcp-stdio', dataDir: data, runtime: 'deno', singleFile: true });
  assert.deepEqual(bin, { command: process.execPath, args: ['mcp'] });
  const run = permissions.selfCommand({ script: 'mcp.js', sub: 'mcp', set: 'mcp-stdio', dataDir: data, runtime: 'deno', singleFile: false });
  assert.equal(run.command, process.execPath);
  assert.deepEqual(run.args.slice(0, 5), ['run', '-q', '--no-prompt', `--config=${permissions.CONFIG}`, '-P=mcp-stdio']);
  assert.ok(run.args.some((a) => a.startsWith('--allow-write=') && a.includes(data)));
  assert.equal(run.args.at(-1), path.join(permissions.APP_DIR, 'mcp.js'));
  assert.ok(!run.args.includes('-A') && !run.args.includes('--allow-all'));
});

test('spawnRefused: only the empty result Deno gives for a refused program', () => {
  assert.equal(permissions.spawnRefused({}), true);
  assert.equal(permissions.spawnRefused({ status: 0 }), false);
  assert.equal(permissions.spawnRefused({ status: null, signal: null, error: new Error('ENOENT') }), false);
});

test('describe: the runtime and what the running process holds', () => {
  if (!process.versions.deno) return assert.deepEqual(permissions.describe(), { runtime: 'node' });
  const d = permissions.describe();
  assert.equal(d.runtime, 'deno');
  assert.equal(d.singleFile, false);
  assert.equal(d.grants.write, 'all', 'the test set has unscoped write (symlinks)');
  assert.equal(d.grants.net, 'scoped');
});
```

(`selfCommand` takes `runtime` and `singleFile` overrides for the tests; they default to `platform.runtime()` and `platform.isSingleFile()`.)

Run: `npx --yes prettier@3 --write test/permissions.test.js && node --test test/permissions.test.js 2>&1 | grep -E "^ℹ (pass|fail)"`
Expected: fails (`Cannot find module '../lib/permissions'`).

- [ ] **Step 3: Export `skillRoots` from `lib/gateway/skills.js`**

Replace, inside `createSkills`, the block

```js
  const e = env || (home == null ? process.env : {});
  const h = home == null ? os.homedir() : home;
  const roots = {
    claude: e.CLAUDE_CONFIG_DIR
      ? path.join(path.resolve(String(e.CLAUDE_CONFIG_DIR).replace(/^~(?=$|[\\/])/, h)), 'skills')
      : path.join(h, '.claude', 'skills'),
    agents: path.join(h, '.agents', 'skills'),
  };
```

with

```js
  const h = home == null ? os.homedir() : home;
  const roots = skillRoots({ home, env });
```

and add above `createSkills` (keeping its JSDoc block attached to `createSkills`):

```js
// Where skills are installed: Claude Code's skills folder and ~/.agents/skills. Also the write scope of a Deno launch
// (lib/permissions.js). "home" given without "env" (tests, demo) never reads this machine's CLAUDE_CONFIG_DIR.
/**
 * @param {Record<string, any>} [opts]
 */
function skillRoots({ home, env } = {}) {
  const e = env || (home == null ? process.env : {});
  const h = home == null ? os.homedir() : home;
  return {
    claude: e.CLAUDE_CONFIG_DIR
      ? path.join(path.resolve(String(e.CLAUDE_CONFIG_DIR).replace(/^~(?=$|[\\/])/, h)), 'skills')
      : path.join(h, '.claude', 'skills'),
    agents: path.join(h, '.agents', 'skills'),
  };
}
```

Export: `module.exports = { createSkills, frontmatter, skillRoots };`. Check that `e` is not used elsewhere in `createSkills`: `grep -n "\be\." lib/gateway/skills.js` — if a later line in `createSkills` uses `e`, keep `const e = env || (home == null ? process.env : {});` there.

Run: `deno task typecheck && npx --yes prettier@3 --check lib/gateway/skills.js && node --test test/skills*.test.js test/gateway-skills*.test.js 2>&1 | grep -E "^ℹ (pass|fail)"`
Expected: typecheck clean, prettier clean, `ℹ fail 0` (if the glob matches no file, run `git grep -l "createSkills" test` and run those).

- [ ] **Step 4: Write `lib/permissions.js`**

```js
// The Deno permission matrix in code (P2 spec D3, D4). The sets themselves live in deno.json. This module:
//   - says how this program starts one of its own entry points under the running runtime (selfCommand);
//   - computes the folders a `deno run` launch may write (writeScope) and the programs it may run (runScope), which
//     deno.json cannot hold: its paths are fixed when the file is read (relative to deno.json; `~` and $VARS are not
//     expanded), and a compiled binary bakes them in at build time, so the binary keeps an unscoped write;
//   - names the reason for every allow-listed entry (WHY; test/permissions.test.js keeps it and deno.json in step);
//   - tells the Compliance page what the running process holds (describe).
// Under Node nothing here restricts anything.
const fs = require('fs');
const os = require('os');
const path = require('path');
const platform = require('./platform');

const APP_DIR = platform.APP_DIR;
const CONFIG = path.join(APP_DIR, 'deno.json');
const SETS = ['server', 'cli', 'mcp-stdio', 'test'];

const WHY = {
  run: {
    ps: 'running AI apps and their argv on macOS (lib/processes.js psRows, withArgv)',
    lsof: 'the working folder of a running agent on macOS (lib/processes.js withCwds)',
    vm_stat: 'used memory on macOS (lib/processes.js usedMemory)',
    scutil: 'the computer name on macOS (lib/platform.js computerName)',
    id: 'the first name for the greeting on macOS (lib/platform.js fullName)',
    hostnamectl: 'the computer name on Linux (lib/platform.js computerName)',
    getent: 'the first name on Linux (lib/platform.js fullName)',
    which: 'whether secret-tool is installed on Linux (lib/secret-store.js has)',
    where: 'the same check on Windows (lib/secret-store.js has)',
    security: 'secrets in the macOS Keychain (lib/secret-store.js keychainBackend)',
    'secret-tool': 'secrets in the Linux keyring (lib/secret-store.js libsecretBackend)',
    'powershell.exe': 'the Windows process list and DPAPI (lib/processes.js winRows, lib/secret-store.js dpapi)',
    powershell: 'Windows notifications (lib/monitors/channels.js)',
    osascript: 'macOS notifications (lib/monitors/channels.js)',
    'notify-send': 'Linux notifications (lib/monitors/channels.js)',
    taskkill: 'stop an agent or a model server on Windows (lib/agents/control.js, lib/runtimes/control.js)',
    launchctl: 'whether a model server is a LaunchAgent (lib/runtimes/state.js); the macOS service (lib/service.js)',
    open: 'open the dashboard and start desktop apps on macOS (lib/platform.js openUrl, lib/runtimes/state.js)',
    'xdg-open': 'open the dashboard on Linux (lib/platform.js openUrl)',
    cmd: 'open the dashboard on Windows (lib/platform.js openUrl)',
    claude: 'the Claude Code judge and MCP registration (lib/evals/claude-cli.js, scripts/register-mcp.js)',
    node: 'a Claude Code installed as cli.js runs through node (lib/evals/claude-cli.js nodeBin)',
    ollama: 'the Ollama preset (lib/runtimes/registry.js)',
    lms: 'the LM Studio preset (lib/runtimes/registry.js)',
    'llama-server': 'the llama.cpp preset (lib/runtimes/registry.js)',
    'local-ai': 'the LocalAI preset (lib/runtimes/registry.js)',
    brew: 'brew services stop for a Homebrew model server (lib/runtimes/control.js)',
    systemctl: 'install the Linux service (lib/service.js); cli only',
    loginctl: 'keep the Linux user service running after logout (lib/service.js); cli only',
    useradd: 'the service account of a Linux system install (lib/service.js --system); cli only',
    chown: 'give the data folder to that account (lib/service.js --system); cli only',
    schtasks: 'the Windows boot task (lib/service.js --system); cli only',
    'wscript.exe': 'start the hidden Windows launcher (lib/service.js); cli only',
  },
  sys: {
    homedir: 'every per-user folder (os.homedir)',
    uid: 'root and ownership checks, the login name (process.getuid, os.userInfo)',
    hostname: 'the computer name when the OS tool gives none (lib/platform.js computerName)',
    systemMemoryInfo: 'the system card (os.totalmem, os.freemem)',
    osUptime: 'the system card (os.uptime)',
    loadavg: 'the system card (os.loadavg)',
    cpus: 'the system card and worker count (os.cpus)',
    umask: 'private files: process.umask(0o077) in mcp.js and server.js',
    osRelease: 'zaafpulse doctor prints the OS version (os.release); cli only',
  },
  env: {
    'ZAAFPULSE_*': 'the settings of ZAAFPulse itself',
    'ZP_*': 'secret references named ZP_SECRET_* (lib/gateway/orchestrator.js) and ZP_DRY_RUN',
    'ACC_*': 'ACC_CONFIG and ACC_CACHE_DIR of mcp.js',
    'OTEL_*': 'the OTLP exporter (lib/otel.js)',
    'XDG_*': 'Linux per-user folders (data, config, cache)',
    DEMO: 'demo mode (server.js)',
    HOST: 'the listen address, older name (server.js)',
    PORT: 'the port, older name (server.js)',
    HOME: 'the home folder',
    USERPROFILE: 'the home folder on Windows',
    APPDATA: 'per-user folders on Windows',
    LOCALAPPDATA: 'per-user folders on Windows',
    ProgramFiles: 'where node is looked for on Windows (lib/evals/claude-cli.js)',
    TEMP: 'the temp folder on Windows',
    TMP: 'the temp folder on Windows',
    TMPDIR: 'the temp folder on macOS and Linux',
    PATH: 'program lookup',
    Path: 'program lookup on Windows',
    PATHEXT: 'program lookup on Windows',
    SystemRoot: 'what a Windows child needs to start',
    SYSTEMROOT: 'what a Windows child needs to start',
    COMPUTERNAME: 'the computer name on Windows',
    USER: 'passed to the Claude Code judge (lib/evals/claude-cli.js childEnv)',
    USERNAME: 'passed to the Claude Code judge on Windows',
    LOGNAME: 'passed to the Claude Code judge',
    LANG: 'passed to the Claude Code judge',
    'LC_*': 'passed to the Claude Code judge',
    TERM: 'passed to the Claude Code judge',
    SHELL: 'passed to the Claude Code judge',
    DISPLAY: 'a Linux desktop: keyring and notifications (lib/secret-store.js pickBackend)',
    WAYLAND_DISPLAY: 'a Linux desktop: keyring and notifications',
    DBUS_SESSION_BUS_ADDRESS: 'a Linux desktop: keyring and notifications',
    CLAUDE_CONFIG_DIR: 'where Claude Code keeps its logs and login (lib/claude.js)',
    CODEX_HOME: 'where Codex keeps its logs (lib/codex.js)',
    'ANTHROPIC_*': "the owner's Claude Code API key or router setup, passed to the judge",
    CLAUDE_CODE_USE_BEDROCK: 'a Claude Code Bedrock setup, passed to the judge',
    CLAUDE_CODE_USE_VERTEX: 'a Claude Code Vertex setup, passed to the judge',
    'AWS_*': 'a Claude Code Bedrock setup, passed to the judge',
    CLOUD_ML_REGION: 'a Claude Code Vertex setup, passed to the judge',
    GOOGLE_APPLICATION_CREDENTIALS: 'a Claude Code Vertex setup, passed to the judge',
    HTTP_PROXY: 'a proxy for the judge and other children',
    HTTPS_PROXY: 'a proxy for the judge and other children',
    NO_PROXY: 'a proxy for the judge and other children',
    http_proxy: 'a proxy for the judge and other children',
    https_proxy: 'a proxy for the judge and other children',
    no_proxy: 'a proxy for the judge and other children',
    NODE_EXTRA_CA_CERTS: 'extra CA certificates for the judge',
    SSL_CERT_FILE: 'extra CA certificates for the judge',
    NODE_V8_COVERAGE: "read by Deno's node:child_process on every spawn",
  },
};

// The sets in deno.json, by name.
function readSets(file = CONFIG) {
  return JSON.parse(fs.readFileSync(file, 'utf8')).permissions;
}

const unique = (list) => [...new Set(list.filter(Boolean))];

// The folders a `deno run` launch of `set` may write, as absolute paths. Every set writes the data folder and the OS
// temp folder. server and cli also install skills (lib/gateway/skills.js) and register the MCP server in each client's
// config folder (scripts/register-mcp.js writes <file>.tmp-<pid> and <file>.bak-<time> beside the file, so the folder
// is granted); a client whose file sits directly in the home folder (Claude Code's ~/.claude.json, registered through
// the claude CLI) is left out, so the home folder itself is never granted. cli also writes the service files.
function writeScope({ set, dataDir, home = os.homedir(), env = process.env, platform: os_ = process.platform, tmp = os.tmpdir() }) {
  const out = [dataDir, tmp];
  if (set === 'mcp-stdio' && env.ACC_CACHE_DIR) out.push(path.resolve(platform.expandHome(env.ACC_CACHE_DIR)));
  if (set === 'server' || set === 'cli') {
    const roots = require('./gateway/skills').skillRoots({ home, env });
    out.push(roots.claude, roots.agents);
    const { clients } = require('./mcp-clients');
    for (const c of Object.values(clients(home, { env, platform: os_ }))) {
      const dir = path.dirname(c.file);
      if (dir !== home && dir !== path.parse(dir).root) out.push(dir);
    }
  }
  if (set === 'cli') {
    if (os_ === 'darwin') out.push(path.join(home, 'Library', 'LaunchAgents'), path.join(home, 'Library', 'Logs', 'ZAAFPulse'));
    else if (os_ === 'win32') {
      const appData = env.APPDATA || path.join(home, 'AppData', 'Roaming');
      const local = env.LOCALAPPDATA || path.join(home, 'AppData', 'Local');
      out.push(path.join(appData, 'Microsoft', 'Windows', 'Start Menu', 'Programs', 'Startup'), path.join(local, 'ZAAFPulse'));
    } else out.push(path.join(env.XDG_CONFIG_HOME || path.join(home, '.config'), 'systemd', 'user'));
  }
  return unique(out);
}

// The programs a `deno run` launch of `set` may run: the set's list, plus the owner's own model servers from
// config.json "localRuntimes" (their first word) and ZAAFPULSE_CLAUDE_BIN. A CLI --allow-run replaces the set's
// list, so the whole list is passed. A compiled binary has only the set's list (spec finding 3).
function runScope({ set, config = {}, env = process.env }) {
  const own = Array.isArray(readSets()[set].run) ? readSets()[set].run : [];
  const extra = [];
  if (set === 'server' || set === 'cli') {
    const { ownerEntries } = require('./runtimes/registry');
    for (const e of ownerEntries(config && config.localRuntimes)) if (!e.error && e.command[0]) extra.push(e.command[0]);
    if (env.ZAAFPULSE_CLAUDE_BIN) extra.push(env.ZAAFPULSE_CLAUDE_BIN);
  }
  return unique([...own, ...extra]);
}

function readConfig(dataDir) {
  try {
    return JSON.parse(fs.readFileSync(path.join(dataDir, 'config.json'), 'utf8'));
  } catch {
    return {};
  }
}

// How this program starts one of its own entry points: `script` (relative to the app folder) under the runtime that
// runs now, or `sub` (a subcommand) of the single-file binary. Under `deno run` the launch carries the set and the
// computed write and run scopes, never -A.
function selfCommand({
  script,
  sub,
  set,
  dataDir,
  env = process.env,
  config,
  runtime = platform.runtime(),
  singleFile = platform.isSingleFile(),
}) {
  if (singleFile) return { command: process.execPath, args: [sub] };
  const file = path.join(APP_DIR, script);
  if (runtime !== 'deno') return { command: process.execPath, args: [file] };
  const cfg = config || readConfig(dataDir);
  return {
    command: process.execPath,
    args: [
      'run',
      '-q',
      '--no-prompt',
      `--config=${CONFIG}`,
      `-P=${set}`,
      `--allow-write=${writeScope({ set, dataDir, env }).join(',')}`,
      `--allow-run=${runScope({ set, config: cfg, env }).join(',')}`,
      file,
    ],
  };
}

// What the running process holds, per permission: 'all', 'all but denied' (granted with a deny list), 'scoped' (only
// some paths, names or hosts, or none) or 'unknown'. { runtime: 'node' } under Node.
function describe({ deno = globalThis.Deno } = {}) {
  if (!deno) return { runtime: 'node' };
  const grant = (name) => {
    try {
      const s = deno.permissions.querySync({ name });
      return s.state === 'granted' ? (s.partial ? 'all but denied' : 'all') : 'scoped';
    } catch {
      return 'unknown';
    }
  };
  return {
    runtime: 'deno',
    version: (deno.version && deno.version.deno) || '',
    singleFile: !!(deno.build && deno.build.standalone),
    grants: Object.fromEntries(['read', 'write', 'net', 'env', 'run', 'sys'].map((k) => [k, grant(k)])),
  };
}

// Under Deno, whether reading the variable `name` is refused by the env allow-list (it then reads as unset).
function envBlocked(name, { deno = globalThis.Deno } = {}) {
  if (!deno) return false;
  try {
    return deno.permissions.querySync({ name: 'env', variable: name }).state !== 'granted';
  } catch {
    return false;
  }
}

// spawnSync under Deno returns {} (no status, no signal, no error) when --allow-run refuses the program; Node always
// sets one of them. Callers turn this into a loud error.
function spawnRefused(r) {
  return !!r && r.status === undefined && r.signal === undefined && r.error === undefined;
}

module.exports = {
  APP_DIR,
  CONFIG,
  SETS,
  WHY,
  readSets,
  writeScope,
  runScope,
  selfCommand,
  describe,
  envBlocked,
  spawnRefused,
};
```

Run: `npx --yes prettier@3 --write lib/permissions.js && node --test test/permissions.test.js 2>&1 | grep -E "^ℹ (pass|fail)"`
Expected: `ℹ fail 0` under Node (the per-permission part of Step 6 is not written yet). If `writeScope` misses `.kiro/settings`, print `Object.values(require('./lib/mcp-clients').clients('/h', { env: {}, platform: 'darwin' })).map((c) => c.file)` and correct the test's expectation to the folder the Kiro entry really names.

- [ ] **Step 5: Use `selfCommand` and make refusals loud**

`lib/platform.js` `mcpCommand()`: replace

```js
  const cmd = isSingleFile()
    ? { command: process.execPath, args: ['mcp'] }
    : { command: process.execPath, args: [path.join(APP_DIR, 'mcp.js')] };
```

with

```js
  // node + mcp.js, the binary with "mcp", or deno run with the mcp-stdio set and its write scope (lib/permissions.js).
  const cmd = require('./permissions').selfCommand({
    script: 'mcp.js',
    sub: 'mcp',
    set: 'mcp-stdio',
    dataDir: data || env.ZAAFPULSE_DATA ? path.resolve(expandHome(data || env.ZAAFPULSE_DATA)) : implicitDataDir(),
    env,
  });
```

`lib/service.js`: `const permissions = require('./permissions');` after `const platform = require('./platform');`; `startCommand()` becomes

```js
// The command that starts the server: the single-file binary, node + bin/zaafpulse.js, or deno run with the server set.
function startCommand(env = process.env) {
  const { command, args } = permissions.selfCommand({
    script: path.join('bin', 'zaafpulse.js'),
    sub: 'start',
    set: 'server',
    dataDir: platform.dataDir(env),
    env,
  });
  return platform.isSingleFile() ? [command, ...args] : [command, ...args, 'start'];
}
```

and `sh`:

```js
const sh = (cmd, args, opts = {}) => {
  const r = spawnSync(cmd, args, { encoding: 'utf8', windowsHide: true, ...opts });
  if (permissions.spawnRefused(r)) throw new Error(`${cmd}: not allowed by this build's run permissions (deno.json "cli")`);
  return r;
};
```

Check the `startCommand()` callers pass nothing or `env`: `grep -n "startCommand(" lib/service.js`; where `install({ env })` calls it, pass that `env`.

`lib/secret-store.js`: `const { spawnRefused } = require('./permissions');` and a checked spawn used by `has()`, the default `spawn` of both backends and `dpapi()`:

```js
// spawnSync, except that a program the runtime permissions refuse is an error, not a silent empty result (Deno).
function spawnChecked(cmd, args, opts) {
  const r = spawnSync(cmd, args, opts);
  if (spawnRefused(r)) throw new Error(`${cmd}: not allowed by this build's run permissions`);
  return r;
}
```

(`has()` uses `spawnChecked`; `keychainBackend({ exec = execFileSync, spawn = spawnChecked } = {})`; `libsecretBackend({ spawn = spawnChecked } = {})`; `dpapi` calls `spawnChecked('powershell.exe', …)`.)

`lib/secrets.js` in `resolve()` replace

```js
      value = env[ref.env] || '';
      if (!value) throw new Error(`${field}: environment variable ${ref.env} is not set`);
```

with

```js
      value = env[ref.env] || '';
      // Under Deno a name outside the env allow-list reads as unset: say why instead of "not set".
      if (!value && env === process.env && require('./permissions').envBlocked(ref.env))
        throw new Error(
          `${field}: environment variable ${ref.env} cannot be read: this build reads only ZAAFPULSE_*, ZP_* and the names in deno.json; rename it to ZP_SECRET_${ref.env}`,
        );
      if (!value) throw new Error(`${field}: environment variable ${ref.env} is not set`);
```

`test/platform.test.js` (hand edit; not prettier-clean), test `platform: home expansion, data folder rules and the MCP command`: under Deno the plain MCP command is now a `deno run` launch. Replace `  const plain = { command: process.execPath, args: [path.join(platform.APP_DIR, 'mcp.js')] };` with:

```js
  // Under Deno the command is a `deno run` launch with the mcp-stdio set and its scopes (lib/permissions.js).
  const plain = process.versions.deno
    ? require('../lib/permissions').selfCommand({ script: 'mcp.js', sub: 'mcp', set: 'mcp-stdio', dataDir: platform.dataDir({}), env: {} })
    : { command: process.execPath, args: [path.join(platform.APP_DIR, 'mcp.js')] };
```

Run: `npx --yes prettier@3 --check lib/platform.js lib/service.js lib/secret-store.js lib/secrets.js && npm test 2>&1 | grep -E "^ℹ (pass|fail)" && NO_COLOR=1 deno test -q --no-check --no-prompt -P=test test/platform.test.js test/secrets.test.js test/secret-store*.test.js 2>&1 | tail -1`
Expected: clean; `ℹ fail 0` (under Node `selfCommand` gives the same `node + script` commands as before; the service tests in `test/platform.test.js` pass the command in themselves); Deno `ok | … | 0 failed`.

- [ ] **Step 6: Write the per-permission tests (D4)**

Create `test/fixtures/permissions/probe.js`:

```js
// One operation under whatever permissions this process holds; prints {"ok":true,…} or {"ok":false,"error":"…"} as
// one JSON line (test/permissions.test.js).
//   probe.js write <file> | net <url> | env <name> | run <program> | sys <api> | secret-env <name>
const fs = require('fs');
const os = require('os');
const cp = require('child_process');

const SYS = { homedir: () => os.homedir(), hostname: () => os.hostname(), networkInterfaces: () => os.networkInterfaces() };
const [op, arg] = process.argv.slice(2);

async function main() {
  if (op === 'write') fs.writeFileSync(arg, 'x');
  else if (op === 'net') await fetch(arg, { signal: AbortSignal.timeout(3000) }).then((r) => r.body && r.body.cancel());
  else if (op === 'env') return { value: process.env[arg] === undefined ? null : 'set' };
  else if (op === 'run')
    await new Promise((resolve, reject) =>
      cp.execFile(arg, ['--version'], (e) => (e && /Requires run access/.test(e.message) ? reject(e) : resolve())),
    );
  else if (op === 'sys') SYS[arg]();
  else if (op === 'secret-env') require('../../../lib/secrets').createSecrets().resolve({ env: arg }, 'probe');
  else throw new Error(`unknown op ${op}`);
  return {};
}

main().then(
  (r) => console.log(JSON.stringify({ ok: true, ...r })),
  (e) => console.log(JSON.stringify({ ok: false, error: String((e && e.message) || e).split('\n')[0] })),
);
```

Append to `test/permissions.test.js`:

```js
// ---- per permission: the feature works, the denied neighbour fails loudly (spec D4) ----

const PROBE = path.join(__dirname, 'fixtures', 'permissions', 'probe.js');
const DENO = process.versions.deno ? process.execPath : 'deno';
const hasDeno = process.versions.deno || spawnSync('deno', ['--version'], { stdio: 'ignore' }).status === 0;
const skip = !hasDeno && 'deno is not installed';

// Runs the probe as `deno run -P=<set>` with the write scope a launch of that set would get, in a throwaway home.
function probe(set, op, arg, { home, data, env = {} }) {
  const scope = permissions.writeScope({ set, dataDir: data, home, env: { HOME: home } });
  const r = spawnSync(
    DENO,
    ['run', '-q', '--no-prompt', `--config=${permissions.CONFIG}`, `-P=${set}`, `--allow-write=${scope.join(',')}`, PROBE, op, arg],
    { encoding: 'utf8', env: { PATH: process.env.PATH, HOME: home, USERPROFILE: home, ZAAFPULSE_DATA: data, ...env } },
  );
  const line = String(r.stdout || '').trim().split('\n').pop();
  assert.ok(line, `no answer from the probe: ${r.stderr}`);
  return JSON.parse(line);
}
const rig = () => {
  const home = tmp('zp-perm-home-');
  const data = path.join(home, 'data');
  fs.mkdirSync(data, { recursive: true });
  return { home, data };
};

test('write: server writes its data folder and the skill folders; the home folder is refused loudly', { skip }, () => {
  const t = rig();
  assert.deepEqual(probe('server', 'write', path.join(t.data, 'x.json'), t), { ok: true });
  fs.mkdirSync(path.join(t.home, '.claude', 'skills'), { recursive: true });
  assert.deepEqual(probe('server', 'write', path.join(t.home, '.claude', 'skills', 'x'), t), { ok: true });
  fs.mkdirSync(path.join(t.home, '.ssh'), { recursive: true });
  const r = probe('server', 'write', path.join(t.home, '.ssh', 'x'), t);
  assert.equal(r.ok, false);
  assert.match(r.error, /Requires write access to ".*\.ssh/);
});

test('write: mcp-stdio writes only its data folder, not the skill folders', { skip }, () => {
  const t = rig();
  assert.deepEqual(probe('mcp-stdio', 'write', path.join(t.data, 'x.json'), t), { ok: true });
  fs.mkdirSync(path.join(t.home, '.claude', 'skills'), { recursive: true });
  assert.match(probe('mcp-stdio', 'write', path.join(t.home, '.claude', 'skills', 'x'), t).error, /Requires write access/);
});

test('net: everything but cloud metadata, which is refused loudly', { skip }, async () => {
  const t = rig();
  const http = require('http');
  const srv = http.createServer((req, res) => res.end('ok'));
  await new Promise((r) => srv.listen(0, '127.0.0.1', r));
  try {
    const url = `http://127.0.0.1:${srv.address().port}/`;
    // spawnSync blocks this process, so the probe runs in a child that answers while we wait for it asynchronously.
    const r = await new Promise((resolve) => {
      const child = require('child_process').spawn(
        DENO,
        ['run', '-q', '--no-prompt', `--config=${permissions.CONFIG}`, '-P=server', PROBE, 'net', url],
        { env: { PATH: process.env.PATH, HOME: t.home } },
      );
      let out = '';
      child.stdout.on('data', (b) => (out += b));
      child.on('close', () => resolve(JSON.parse(out.trim().split('\n').pop())));
    });
    assert.deepEqual(r, { ok: true });
  } finally {
    srv.close();
  }
  assert.match(probe('server', 'net', 'http://169.254.169.254/latest/meta-data/', t).error, /Requires net access to "169\.254\.169\.254/);
});

test('env: an allow-listed name reads, another reads as unset, and a secret from it fails with the reason', { skip }, () => {
  const t = rig();
  const env = { ZAAFPULSE_PROBE: 'x', OTHER_TOKEN: 'secret-value' };
  assert.deepEqual(probe('server', 'env', 'ZAAFPULSE_PROBE', { ...t, env }), { ok: true, value: 'set' });
  assert.deepEqual(probe('server', 'env', 'OTHER_TOKEN', { ...t, env }), { ok: true, value: null });
  const r = probe('server', 'secret-env', 'OTHER_TOKEN', { ...t, env });
  assert.equal(r.ok, false);
  assert.match(r.error, /OTHER_TOKEN cannot be read: this build reads only .*rename it to ZP_SECRET_OTHER_TOKEN/);
  assert.deepEqual(probe('server', 'secret-env', 'ZP_SECRET_PROBE', { ...t, env: { ZP_SECRET_PROBE: 'abcdefgh' } }), { ok: true });
});

test('run: an allow-listed program runs; another is refused loudly', { skip: skip || (process.platform === 'win32' && 'POSIX programs') }, () => {
  const t = rig();
  assert.deepEqual(probe('server', 'run', 'ps', t), { ok: true });
  const r = probe('server', 'run', 'curl', t);
  assert.equal(r.ok, false);
  assert.match(r.error, /Requires run access to "curl/);
});

test('sys: server reads the home folder; network interfaces are refused; mcp-stdio has no hostname', { skip }, () => {
  const t = rig();
  assert.deepEqual(probe('server', 'sys', 'homedir', t), { ok: true });
  assert.match(probe('server', 'sys', 'networkInterfaces', t).error, /Requires sys access to "networkInterfaces"/);
  assert.match(probe('mcp-stdio', 'sys', 'hostname', t).error, /Requires sys access to "hostname"/);
});
```

Run:
```bash
npx --yes prettier@3 --write test/fixtures/permissions/probe.js && npx --yes prettier@3 --write test/permissions.test.js
export ZAAFPULSE_DATA="$(mktemp -d)" HOME="$(mktemp -d)"
NO_COLOR=1 deno test -q --no-check --no-prompt -P=test test/permissions.test.js 2>&1 | grep -E "^(ok|FAILED) \||=> "
node --test test/permissions.test.js 2>&1 | grep -E "^ℹ (pass|fail|skipped)"
```
Expected: Deno `ok | 14 passed | 0 failed`; Node `ℹ fail 0` (the per-permission tests run when `deno` is on PATH, else they are skipped).

- [ ] **Step 7: The Compliance control `runtime_permissions`**

Failing test first. In `test/governance-controls.test.js`: in `good()` add before `  inventory: {`:

```js
  runtimePermissions: {
    runtime: 'deno',
    version: '2.9.8',
    singleFile: true,
    grants: { read: 'scoped', write: 'scoped', net: 'scoped', env: 'scoped', run: 'scoped', sys: 'scoped' },
  },
```

change the catalogue test's title to `'the catalogue: nineteen controls in a fixed order, each with a link and at least one clause'`, append `'runtime_permissions',` after `'inventory_coverage',` in its list, change `assert.equal(r.body.controls.length, 18);` (line 420) to `19`, and add:

```js
test('runtime permissions: off on Node, partial while a permission is unrestricted, in place when all are scoped', () => {
  const rp = (v) => ({ ...good(), runtimePermissions: v });
  assert.equal(one(rp({ runtime: 'node' }), 'runtime_permissions').status, 'off');
  assert.deepEqual(one(rp({ runtime: 'node' }), 'runtime_permissions').evidence, ['runs on Node.js: no runtime permission sandbox']);
  assert.equal(one(good(), 'runtime_permissions').status, 'in place');
  const live = one(
    rp({
      runtime: 'deno',
      version: '2.9.8',
      singleFile: true,
      grants: { read: 'all', write: 'all', net: 'all but denied', env: 'scoped', run: 'scoped', sys: 'scoped' },
    }),
    'runtime_permissions',
  );
  assert.equal(live.status, 'partial');
  assert.deepEqual(live.evidence, [
    'Deno 2.9.8, single-file build',
    'unrestricted: read, write',
    'all but denied hosts: net',
    'allow-listed: env, run, sys',
    'matrix: deno.json; tests: test/permissions.test.js',
  ]);
});
```

`test/governance-inventory-http.test.js` lines 222 and 270: `18` → `19`.

Run: `node --test test/governance-controls.test.js test/governance-inventory-http.test.js 2>&1 | grep -E "^ℹ (pass|fail)"`
Expected: failures (19 ≠ 18, no such control).

Add to `lib/governance/controls.js` after the `inventory_coverage` entry of `CATALOGUE`:

```js
  {
    id: 'runtime_permissions',
    title: 'Runtime permissions',
    clauses: ['nist-ms-2.7', 'iso-a6'],
    link: '#governance',
    part: 'runtimePermissions',
    check(rp) {
      if (rp.runtime !== 'deno') return { status: 'off', evidence: ['runs on Node.js: no runtime permission sandbox'] };
      const g = isObj(rp.grants) ? rp.grants : {};
      const kinds = ['read', 'write', 'net', 'env', 'run', 'sys'];
      const all = kinds.filter((k) => g[k] === 'all');
      const most = kinds.filter((k) => g[k] === 'all but denied');
      const scoped = kinds.filter((k) => g[k] === 'scoped');
      return {
        status: all.length || most.length ? 'partial' : 'in place',
        evidence: [
          `Deno ${rp.version || ''}${rp.singleFile ? ', single-file build' : ''}`.replace(' ,', ','),
          ...(all.length ? [`unrestricted: ${all.join(', ')}`] : []),
          ...(most.length ? [`all but denied hosts: ${most.join(', ')}`] : []),
          ...(scoped.length ? [`allow-listed: ${scoped.join(', ')}`] : []),
          'matrix: deno.json; tests: test/permissions.test.js',
        ],
      };
    },
  },
```

and update the header comment of `controls(state)` to list `runtimePermissions` among the state parts. In `server.js` (hand edit) `controlState()`'s `parts`, after the `traces: () => ({ … }),` entry add:

```js
    // P2: what the Deno permission sandbox holds (lib/permissions.js); { runtime: 'node' } under Node.
    runtimePermissions: () => require('./lib/permissions').describe(),
```

Run: `deno task typecheck && npx --yes prettier@3 --check lib/governance/controls.js test/governance-controls.test.js test/governance-inventory-http.test.js && node --test test/governance-*.test.js 2>&1 | grep -E "^ℹ (pass|fail)"`
Expected: typecheck clean (controls.js is `// @ts-check`), prettier clean, `ℹ fail 0`. `public/governanceview.js` renders controls from the API generically: check `grep -n "inventory_coverage" public/*.js` shows no per-id rendering; if it does, stop and report (the frontend is out of scope).

- [ ] **Step 8: The contract harness starts the Deno server with `-P=server`**

In `test/contract/harness.js` `command()`, replace the comment and the `deno` branch's `args` with:

```js
// The command that runs server.js under a runtime. Under `deno test` process.execPath is deno, so node is found on
// PATH; under `node --test` deno is found on PATH. Deno gets the "server" set of deno.json (never -A) with the write
// scope of this data folder and the temp folder, as a real launch narrows it (lib/permissions.js).
```

```js
      args: ['run', '-q', '--no-prompt', '-P=server', `--allow-write=${data},${os.tmpdir()}`, server],
```

Run: `ZP_RUNTIME=deno npm run test:contract 2>&1 | grep -E "^ℹ (pass|fail)" && ZP_RUNTIME=deno NO_COLOR=1 deno test -q --no-check --no-prompt -P=test test/contract/ 2>&1 | tail -1`
Expected: `ℹ pass 172`, `ℹ fail 0`; `ok | 172 passed | 0 failed`.

- [ ] **Step 9: Denial walk on the real server (re-run of F15 with the committed sets)**

Run (temp HOME seeded from the fixtures, port 47811, kill only what this starts):
```bash
W="$(mktemp -d)"; H="$W/home"; D="$W/data"; mkdir -p "$H/.claude/projects" "$H/.codex/sessions" "$D"
cp -R test/fixtures/trace/claude/. "$H/.claude/projects/"; cp -R test/fixtures/trace/codex/. "$H/.codex/sessions/"
SCOPE="$(HOME="$H" node -e "console.log(require('./lib/permissions').writeScope({ set: 'server', dataDir: process.argv[1] }).join(','))" "$D")"
HOME="$H" ZAAFPULSE_DATA="$D" ZAAFPULSE_PORT=47811 ZAAFPULSE_AUTH=none deno run -q --no-prompt -P=server --allow-write="$SCOPE" server.js > "$W/server.log" 2>&1 < /dev/null &
PID=$!; for i in $(seq 1 60); do curl -s -o /dev/null --max-time 1 http://127.0.0.1:47811/login.html && break; sleep 0.25; done
for p in /login.html / /api/session /api/summary /api/live /api/insights /api/timeline /api/topology /api/brain /api/config /api/gateway /api/governance/inventory /api/integrations /api/office /api/platform /api/prices /api/logs /api/router /api/vault /api/trace/sources /api/trace/sessions /api/cost/status /api/monitors /api/runtimes; do echo "$(curl -s -o /dev/null -w '%{http_code}' --max-time 30 http://127.0.0.1:47811$p) $p"; done | grep -v "^200" ; echo "non-200 above (expected: none)"
curl -s -X POST -H 'content-type: application/json' -d '{"action":"install","target":"all"}' http://127.0.0.1:47811/api/gateway/skills | head -c 200; echo
curl -s http://127.0.0.1:47811/api/governance/controls 2>/dev/null | grep -o '"id":"runtime_permissions"[^}]*' | head -c 300; echo
kill $PID; wait $PID 2>/dev/null; grep -c "NotCapable\|Requires .* access" "$W/server.log"; rm -rf "$W"
```
Expected: no non-200 line; the skills install answers `{"installed":[…]` for both roots; the control line shows `"status":"partial"` (read and net broad); `0` NotCapable lines. (If the controls route is named differently, find it with `grep -n "controlsView" lib/governance/api.js server.js`.)

- [ ] **Step 10: Full suites, both runtimes**

Run: `npm test 2>&1 | grep -E "^ℹ (pass|fail)"`, then Task 4 Step 2's Deno command, then `ZP_RUNTIME=deno npm run test:contract 2>&1 | grep -E "^ℹ (pass|fail)"`.
Expected: all `fail 0`.

- [ ] **Step 11: Commit**

```bash
git add deno.json lib/permissions.js lib/gateway/skills.js lib/platform.js lib/service.js lib/secret-store.js lib/secrets.js lib/governance/controls.js server.js test/platform.test.js test/permissions.test.js test/fixtures/permissions/probe.js test/governance-controls.test.js test/governance-inventory-http.test.js test/contract/harness.js
git commit -m "Add the server, cli and mcp-stdio permission sets with a reason for every entry, launch scopes, per-permission tests and a Compliance control"
```

---

### Task 6: The `deno compile` pipeline and SEA removal (spec T6)

**Files:**
- Create: `scripts/build-deno.js`, `scripts/smoke-binary.js`, `test/build-deno.test.js`
- Modify: `package.json` (scripts `build`; `postject` removed), `package-lock.json`, `deno.lock`
- Modify: `lib/service.js:69,126` (working folder); `test/platform.test.js` (service test)
- Modify: `.github/workflows/ci.yml` (job `build-deno`), `.github/workflows/release.yml`
- Delete: `scripts/sea-main.js`, `scripts/build-single.js`, `test/sea.test.js`
- Modify: `CONTRIBUTING.md:16,37,105-116`, `README.md:275,946,1021`, `docs/install.md:113`, `docs/performance.md:75-77`, `lib/indexer.js:7`

**Interfaces:**
- Consumes: `deno.json` `server` set (Task 5); `platform.isSingleFile()`, `platform.runtime()`, `/api/platform` `{ single, runtime }` (Task 1).
- Produces: `scripts/build-deno.js` exports `{ INCLUDE: string[], ARCHIVE: string[], TARGETS: Record<string,string>, hostTarget(platform?, arch?): string, outName(version, target): string, compileArgs({ target, out, entry }): string[], parseArgs(argv): { targets: string[], allowDirty: boolean } }`; `deno task build [--target <triple>]… [--all] [--allow-dirty]` writes `dist/zaafpulse-<version>-<name>[.exe]` and `dist/SHA256SUMS.txt`; `scripts/smoke-binary.js <binary>` prints one JSON line `{ ok, binary, startupMs, version, single, runtime }` and exits non-zero on failure; CI job id `build-deno`, artifacts `zaafpulse-<name>`.

- [ ] **Step 1: Write the failing build-script tests**

Create `test/build-deno.test.js`:

```js
// scripts/build-deno.js: what ships, the target names, and the compile command (P2 T6). Pure: compiles nothing.
const test = require('node:test');
const assert = require('node:assert/strict');
const build = require('../scripts/build-deno');

test('the binary embeds the app, never the tests, docs or build scripts', () => {
  assert.deepEqual(build.INCLUDE, [
    'bin',
    'lib',
    'public',
    'skills',
    'scripts/register-mcp.js',
    'server.js',
    'mcp.js',
    'package.json',
    'config.example.json',
    'LICENSE',
  ]);
  assert.deepEqual(build.ARCHIVE, [...build.INCLUDE, 'deno.json']);
});

test('the six targets keep the v1 release names', () => {
  assert.deepEqual(Object.values(build.TARGETS).sort(), [
    'darwin-arm64',
    'darwin-x64',
    'linux-arm64',
    'linux-x64',
    'win-arm64',
    'win-x64',
  ]);
  assert.equal(build.outName('2.0.0', 'aarch64-pc-windows-msvc'), 'zaafpulse-2.0.0-win-arm64.exe');
  assert.equal(build.outName('2.0.0', 'x86_64-unknown-linux-gnu'), 'zaafpulse-2.0.0-linux-x64');
  assert.throws(() => build.outName('2.0.0', 'riscv64-unknown-linux-gnu'), /unknown target/);
  assert.equal(build.hostTarget('darwin', 'arm64'), 'aarch64-apple-darwin');
  assert.equal(build.hostTarget('win32', 'x64'), 'x86_64-pc-windows-msvc');
  assert.equal(build.hostTarget('linux', 'arm64'), 'aarch64-unknown-linux-gnu');
});

test('compile: quiet, never prompting, the server set baked in, no -A', () => {
  const a = build.compileArgs({ target: 'x86_64-unknown-linux-gnu', out: '/o/z', entry: 'bin/zaafpulse.js' });
  assert.deepEqual(a.slice(0, 6), ['compile', '-q', '--no-prompt', '-P=server', '--no-check', '--target']);
  assert.ok(!a.includes('-A') && !a.includes('--allow-all'));
  assert.equal(a.filter((x) => x === '--include').length, build.INCLUDE.length);
  assert.deepEqual(a.slice(-3), ['-o', '/o/z', 'bin/zaafpulse.js']);
});

test('arguments: --target (repeatable), --all, --allow-dirty; anything else is refused', () => {
  assert.deepEqual(build.parseArgs(['--target', 'aarch64-apple-darwin']), {
    targets: ['aarch64-apple-darwin'],
    allowDirty: false,
  });
  assert.equal(build.parseArgs(['--all']).targets.length, 6);
  assert.equal(build.parseArgs(['--allow-dirty']).allowDirty, true);
  assert.deepEqual(build.parseArgs([]).targets, [build.hostTarget()]);
  assert.throws(() => build.parseArgs(['--target', 'nope']), /unknown target/);
  assert.throws(() => build.parseArgs(['--fast']), /unknown option/);
});
```

Run: `npx --yes prettier@3 --write test/build-deno.test.js && node --test test/build-deno.test.js 2>&1 | grep -E "^ℹ (pass|fail)"`
Expected: fails (`Cannot find module '../scripts/build-deno'`).

- [ ] **Step 2: Write `scripts/build-deno.js`**

```js
// Builds the single-file ZAAFPulse executables with `deno compile` (P2 T6); replaces the Node SEA build.
//   deno task build                                  this machine's target
//   deno task build --target <triple> [--target …]   any of TARGETS
//   deno task build --all                            all six (needs the denort base binaries, downloaded once)
//   --allow-dirty                                    build the working tree instead of the last commit (local tests)
// What ships is exactly the committed files under INCLUDE: they are taken from `git archive HEAD` into dist/.work, so
// an untracked or edited file never ships, and the build stops when a file that would ship has uncommitted changes.
// The binary runs with the "server" permission set of deno.json, baked in at compile time (-P=server, never -A); -q
// keeps Deno's own notices out of the binary's output. A compiled binary serves its files from inside itself and
// unpacks nothing (spec D2): integrity is the release's SHA256SUMS and attestations.
// Output: dist/zaafpulse-<version>-<name>[.exe] and dist/SHA256SUMS.txt.
const { execFileSync } = require('child_process');
const crypto = require('crypto');
const fs = require('fs');
const path = require('path');

const ROOT = path.join(__dirname, '..');
const DIST = path.join(ROOT, 'dist');
const INCLUDE = [
  'bin',
  'lib',
  'public',
  'skills',
  'scripts/register-mcp.js',
  'server.js',
  'mcp.js',
  'package.json',
  'config.example.json',
  'LICENSE',
];
// deno.json is in the source folder for -P=server to find; it is not embedded.
const ARCHIVE = [...INCLUDE, 'deno.json'];
// Deno target triple -> the release name used since v1 (install.sh and install.ps1 pick by it).
const TARGETS = {
  'aarch64-apple-darwin': 'darwin-arm64',
  'x86_64-apple-darwin': 'darwin-x64',
  'x86_64-unknown-linux-gnu': 'linux-x64',
  'aarch64-unknown-linux-gnu': 'linux-arm64',
  'x86_64-pc-windows-msvc': 'win-x64',
  'aarch64-pc-windows-msvc': 'win-arm64',
};

function hostTarget(platform = process.platform, arch = process.arch) {
  const cpu = arch === 'arm64' ? 'aarch64' : 'x86_64';
  const os = platform === 'darwin' ? 'apple-darwin' : platform === 'win32' ? 'pc-windows-msvc' : 'unknown-linux-gnu';
  return `${cpu}-${os}`;
}

function outName(version, target) {
  const name = TARGETS[target];
  if (!name) throw new Error(`unknown target ${target}; one of ${Object.keys(TARGETS).join(', ')}`);
  return `zaafpulse-${version}-${name}${name.startsWith('win') ? '.exe' : ''}`;
}

function compileArgs({ target, out, entry }) {
  return [
    'compile',
    '-q',
    '--no-prompt',
    '-P=server',
    '--no-check',
    '--target',
    target,
    ...INCLUDE.flatMap((p) => ['--include', p]),
    '-o',
    out,
    entry,
  ];
}

function parseArgs(argv) {
  const targets = [];
  let allowDirty = false;
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a === '--allow-dirty') allowDirty = true;
    else if (a === '--all') targets.push(...Object.keys(TARGETS));
    else if (a === '--target') {
      const t = argv[++i];
      if (!TARGETS[t]) throw new Error(`unknown target ${t}; one of ${Object.keys(TARGETS).join(', ')}`);
      targets.push(t);
    } else throw new Error(`unknown option ${a}`);
  }
  return { targets: targets.length ? [...new Set(targets)] : [hostTarget()], allowDirty };
}

const git = (args) => execFileSync('git', ['-C', ROOT, ...args], { encoding: 'utf8' });
const sha256 = (file) => crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex');

// The folder to compile from: the committed tree under ARCHIVE, or the working tree with --allow-dirty.
function source(allowDirty) {
  const dirty = git(['status', '--porcelain', '--untracked-files=no', '--', ...ARCHIVE])
    .split('\n')
    .filter(Boolean);
  if (dirty.length && !allowDirty)
    throw new Error(
      `uncommitted changes in files that would ship (${dirty.slice(0, 5).join(', ')}); commit them, or pass --allow-dirty for a local test build`,
    );
  if (allowDirty) {
    if (dirty.length) console.warn(`warning: building with uncommitted changes: ${dirty.join(', ')}`);
    return ROOT;
  }
  const work = path.join(DIST, '.work');
  fs.rmSync(work, { recursive: true, force: true });
  fs.mkdirSync(path.join(work, 'src'), { recursive: true });
  const tar = path.join(work, 'src.tar');
  git(['archive', '--format=tar', '-o', tar, 'HEAD', '--', ...ARCHIVE]);
  execFileSync('tar', ['-xf', tar, '-C', path.join(work, 'src')], { stdio: 'inherit' });
  return path.join(work, 'src');
}

function main(argv) {
  const { targets, allowDirty } = parseArgs(argv);
  const version = JSON.parse(fs.readFileSync(path.join(ROOT, 'package.json'), 'utf8')).version;
  fs.mkdirSync(DIST, { recursive: true });
  const src = source(allowDirty);
  const sums = [];
  for (const target of targets) {
    const out = path.join(DIST, outName(version, target));
    const args = compileArgs({ target, out, entry: path.join('bin', 'zaafpulse.js') });
    // One retry: the denort base binary for a target is downloaded on first use, and a download can fail once.
    for (let attempt = 1; ; attempt++) {
      try {
        execFileSync(process.execPath, args, { cwd: src, stdio: 'inherit' });
        break;
      } catch (err) {
        if (attempt === 2) throw err;
        console.warn(`deno compile failed for ${target}; trying once more`);
      }
    }
    sums.push(`${sha256(out)}  ${path.basename(out)}`);
    console.log(`built dist/${path.basename(out)} (${(fs.statSync(out).size / 1e6).toFixed(0)} MB)`);
  }
  fs.appendFileSync(path.join(DIST, 'SHA256SUMS.txt'), `${sums.join('\n')}\n`);
  if (src !== ROOT) fs.rmSync(path.join(DIST, '.work'), { recursive: true, force: true });
}

if (require.main === module) {
  try {
    main(process.argv.slice(2));
  } catch (err) {
    console.error(err.message);
    process.exit(1);
  }
} else module.exports = { INCLUDE, ARCHIVE, TARGETS, hostTarget, outName, compileArgs, parseArgs };
```

In `package.json` `scripts` add (after `typecheck`):

```json
    "build": "deno run -q --no-prompt --allow-read --allow-write=dist --allow-run=git,tar,deno --allow-env=PATH --ignore-env scripts/build-deno.js",
```

(Why `--allow-run=deno`: the script runs `deno compile`, which is the toolchain itself; a spawned deno is effectively unscoped (F10), which is accepted for the build task only. `--ignore-env` lets the children inherit the OS environment, F12.)

Run: `npx --yes prettier@3 --write scripts/build-deno.js && node --test test/build-deno.test.js 2>&1 | grep -E "^ℹ (pass|fail)"`
Expected: `ℹ pass 4`, `ℹ fail 0`. `dist/` is already in `.gitignore`: check `git check-ignore dist/x` prints `dist/x`; if not, add `dist/` to `.gitignore`.

- [ ] **Step 3: Build this machine's target**

Run: `deno task build --allow-dirty && ls -la dist && cat dist/SHA256SUMS.txt`
Expected: `built dist/zaafpulse-1.1.0-darwin-arm64 (~72 MB)` on the owner's Mac (F17), one checksum line. (After committing, `deno task build` without `--allow-dirty` must give the same result.)

- [ ] **Step 4: Write `scripts/smoke-binary.js`**

```js
// Smoke test of one built binary on this machine (P2 T6, CI build-deno): `<binary> version`; `<binary> start --demo`
// on a throwaway data folder until /login.html answers 200; /api/platform says single-file and deno; `<binary> doctor`
// exits 0. Prints one JSON line and exits non-zero on any failure. Kills only the process it started.
//   deno run -q --no-prompt --allow-read --allow-write=dist --allow-net=127.0.0.1 --allow-run=<binary> \
//     --allow-env=PATH,SystemRoot,SYSTEMROOT,PATHEXT,TEMP,TMP,windir,ComSpec --ignore-env scripts/smoke-binary.js <binary>
const { spawn, execFileSync } = require('child_process');
const fs = require('fs');
const net = require('net');
const path = require('path');

const ROOT = path.join(__dirname, '..');
const KEEP = ['PATH', 'SystemRoot', 'SYSTEMROOT', 'PATHEXT', 'TEMP', 'TMP', 'windir', 'ComSpec'];

const freePort = () =>
  new Promise((resolve, reject) => {
    const s = net.createServer();
    s.once('error', reject);
    s.listen(0, '127.0.0.1', () => {
      const { port } = s.address();
      s.close(() => resolve(port));
    });
  });

async function smoke(binary) {
  const bin = path.resolve(binary);
  const version = JSON.parse(fs.readFileSync(path.join(ROOT, 'package.json'), 'utf8')).version;
  const work = fs.mkdtempSync(path.join(ROOT, 'dist', '.smoke-'));
  const port = await freePort();
  const env = {
    ...Object.fromEntries(KEEP.filter((k) => process.env[k] !== undefined).map((k) => [k, process.env[k]])),
    HOME: work,
    USERPROFILE: work,
    APPDATA: path.join(work, 'AppData', 'Roaming'),
    LOCALAPPDATA: path.join(work, 'AppData', 'Local'),
    ZAAFPULSE_DATA: path.join(work, 'data'),
    ZAAFPULSE_PORT: String(port),
  };
  const out = { ok: false, binary: path.basename(bin) };
  let child;
  try {
    out.version = execFileSync(bin, ['version'], { env, encoding: 'utf8', timeout: 30000 }).trim();
    if (out.version !== version) throw new Error(`version says ${out.version}, package.json ${version}`);
    const t0 = Date.now();
    child = spawn(bin, ['start', '--demo'], { env, stdio: 'ignore' });
    let exited = false;
    child.on('exit', () => (exited = true));
    const base = `http://127.0.0.1:${port}`;
    for (;;) {
      if (exited) throw new Error('the server exited before answering');
      if (Date.now() - t0 > 30000) throw new Error('no 200 on /login.html within 30 s');
      try {
        const r = await fetch(`${base}/login.html`, { signal: AbortSignal.timeout(1000) });
        await r.body?.cancel();
        if (r.status === 200) break;
      } catch {
        /* not up yet */
      }
      await new Promise((r) => setTimeout(r, 50));
    }
    out.startupMs = Date.now() - t0;
    const p = await (await fetch(`${base}/api/platform`)).json();
    out.single = p.single;
    out.runtime = p.runtime;
    if (p.single !== true || p.runtime !== 'deno') throw new Error(`/api/platform: ${JSON.stringify(p)}`);
    const doctor = execFileSync(bin, ['doctor'], { env, encoding: 'utf8', timeout: 60000 });
    if (!/\(single-file build\)/.test(doctor) || !/Deno\s+\d/.test(doctor)) throw new Error(`doctor:\n${doctor}`);
    out.ok = true;
  } catch (err) {
    out.error = String(err.message || err).slice(0, 2000);
  } finally {
    if (child && child.exitCode === null) {
      const gone = new Promise((r) => child.once('exit', r));
      child.kill();
      await gone;
    }
    fs.rmSync(work, { recursive: true, force: true, maxRetries: 3, retryDelay: 200 });
  }
  return out;
}

smoke(process.argv[2]).then((r) => {
  console.log(JSON.stringify(r));
  process.exit(r.ok ? 0 : 1);
});
```

Run:
```bash
npx --yes prettier@3 --write scripts/smoke-binary.js
B="$(cd dist && ls zaafpulse-*-darwin-arm64)"
deno run -q --no-prompt --allow-read --allow-write=dist --allow-net=127.0.0.1 --allow-run="dist/$B" --allow-env=PATH,SystemRoot,SYSTEMROOT,PATHEXT,TEMP,TMP,windir,ComSpec --ignore-env scripts/smoke-binary.js "dist/$B"
```
Expected: `{"ok":true,"binary":"zaafpulse-1.1.0-darwin-arm64","version":"1.1.0","startupMs":<~300–1700>,"single":true,"runtime":"deno"}`, exit 0.

- [ ] **Step 5: The service runs the binary from a folder that exists**

A compiled binary's `APP_DIR` is a virtual folder (F17), and launchd/systemd refuse a missing working folder. In `lib/service.js` add after `startCommand`:

```js
// The service's working folder: the app folder from source; the data folder for a single-file binary, whose app folder
// exists only inside the binary.
const workDir = (env = process.env) => (platform.isSingleFile() ? platform.dataDir(env) : platform.APP_DIR);
```

and use it: in `plist()` replace `<key>WorkingDirectory</key><string>${xml(platform.APP_DIR)}</string>` with `<key>WorkingDirectory</key><string>${xml(workDir(env))}</string>`; in `unitText()` replace `WorkingDirectory=${platform.APP_DIR}` with `WorkingDirectory=${workDir(env)}` (both functions already receive `env`; check with `grep -n "function plist\|function unitText" lib/service.js`). The service tests live in `test/platform.test.js` (hand edit). Add:

```js
test('service: a single-file binary works in its data folder, a checkout in the app folder', (t) => {
  const service = require('../lib/service');
  const d = tmp('zp-svc-');
  const env = { ZAAFPULSE_DATA: d };
  const at = (dir) => `<key>WorkingDirectory</key><string>${dir}</string>`;
  assert.ok(service.plist(['/x/zaafpulse', 'start'], env, '/tmp/l.log').includes(at(platform.APP_DIR)));
  t.mock.method(platform, 'isSingleFile', () => true); // an own export object: Deno's node:test mocks it
  assert.ok(service.plist(['/x/zaafpulse', 'start'], env, '/tmp/l.log').includes(at(d)));
  assert.ok(service.unitText(['/x/zaafpulse', 'start'], env, { system: false }).includes(`WorkingDirectory=${d}`));
});
```

(Check `service.plist` and `service.unitText` are exported: `grep -n "module.exports" -A12 lib/service.js`; the existing tests at lines 230–264 call them. If `unitText` is not exported, drop that assertion.)

Run: `npx --yes prettier@3 --check lib/service.js && npm test 2>&1 | grep -E "^ℹ (pass|fail)"`
Expected: clean, `ℹ fail 0`.

- [ ] **Step 6: Remove the Node SEA build**

Run:
```bash
git rm scripts/sea-main.js scripts/build-single.js test/sea.test.js
npm uninstall --save-dev postject
deno install
git diff --stat package.json package-lock.json deno.lock
grep -n postject package.json package-lock.json deno.lock
```
Expected: `package.json` `devDependencies` is `{ "@types/node": "24.19.2" }`; no `postject` left in the three files; `deno install` rewrites `deno.lock` without it. Then `rm -rf node_modules && deno ci` succeeds.

Update the references (exact edits; `git grep -n "build-single\|sea-main\|postject\|SEA" -- ':!docs/superpowers' ':!docs/migration'` must list nothing afterwards except `CHANGELOG`-style history if any):
- `lib/indexer.js:7`: replace `runs as the single-file build (scripts/sea-main.js unpacks real files and requires them). Any` with `runs as the single-file build (deno compile serves the worker file from inside the binary). Any`; keep the rest of the comment.
- `CONTRIBUTING.md:16`: `You need Node.js 18 or newer, or Deno 2.9.8, and nothing else. ZAAFPulse has no runtime dependencies: the only npm package is @types/node, a pinned devDependency that holds types for \`deno check\`.`
- `CONTRIBUTING.md:37` table row: `| \`deno task build\` | Build a single-file executable for this machine into \`dist/\` (see "Releases") |`
- `CONTRIBUTING.md:105–116` ("Releases"): replace the SEA bullets with: binaries are built by `deno task build` (`scripts/build-deno.js`) from the committed tree with `deno compile -P=server`; only files tracked by git are packed and the build stops on uncommitted changes (`--allow-dirty` for local test builds); nothing is unpacked at start, integrity is `SHA256SUMS` plus attestations; CI builds and smoke-tests the six targets on native runners (`build-deno`).
- `README.md:275`: `| Runtime dependencies | **0** (Node and Deno standard libraries only) |`; `README.md:946`: replace the sentence about `scripts/build-single.js` with `(The build, \`deno task build\`, downloads Deno's runtime base binaries once; the running app never does.)`; `README.md:1021`: `single-file build (build-deno.js, smoke-binary.js)`.
- `docs/install.md:113`: `To build them yourself: \`deno task build\` builds this machine's target, \`deno task build --target x86_64-unknown-linux-gnu\` or \`--all\` builds others (Deno cross-compiles every target from any OS).`
- `docs/performance.md:75–77`: `- **Single-file build:** \`deno compile\` serves the worker script and parsers from inside the binary (worker_threads work there, checked in P2).`

- [ ] **Step 7: The CI `build-deno` job**

Add to `.github/workflows/ci.yml` (after `typecheck`):

```yaml
  # Compiles the six single-file binaries with deno compile and the "server" permission set, and smoke-tests each one on
  # a runner of its own OS and CPU (P2 T6): start in demo mode on a throwaway data folder, /login.html 200, /api/platform
  # single-file on Deno, doctor. Pushes and release calls only, not pull requests. The denort base binary of each target
  # is cached (a download failed once in P1). Publishing is P6.
  build-deno:
    name: build-deno (${{ matrix.name }})
    if: ${{ github.event_name != 'pull_request' }}
    runs-on: ${{ matrix.runner }}
    timeout-minutes: 20
    strategy:
      fail-fast: false
      matrix:
        include:
          - { name: linux-x64, runner: ubuntu-latest, target: x86_64-unknown-linux-gnu }
          - { name: linux-arm64, runner: ubuntu-24.04-arm, target: aarch64-unknown-linux-gnu }
          - { name: darwin-arm64, runner: macos-latest, target: aarch64-apple-darwin }
          - { name: darwin-x64, runner: macos-15-intel, target: x86_64-apple-darwin }
          - { name: win-x64, runner: windows-latest, target: x86_64-pc-windows-msvc }
          - { name: win-arm64, runner: windows-11-arm, target: aarch64-pc-windows-msvc }
    env:
      DENO_DIR: ${{ github.workspace }}/../deno-dir
      NO_COLOR: '1'
    steps:
      - run: git config --global core.autocrlf false
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - uses: denoland/setup-deno@22d081ff2d3a40755e97629de92e3bcbfa7cf2ed # v2.0.5
        with:
          deno-version: 2.9.8
      - uses: actions/cache@55cc8345863c7cc4c66a329aec7e433d2d1c52a9 # v6.1.0
        with:
          path: ${{ github.workspace }}/../deno-dir/dl
          key: denort-2.9.8-${{ matrix.target }}
      - name: Build
        shell: bash
        run: deno task build --target ${{ matrix.target }}
      - name: Smoke test on this runner
        shell: bash
        run: |
          B="$(cd dist && ls zaafpulse-*-${{ matrix.name }}*)"
          deno run -q --no-prompt --allow-read --allow-write=dist --allow-net=127.0.0.1 --allow-run="dist/$B" \
            --allow-env=PATH,SystemRoot,SYSTEMROOT,PATHEXT,TEMP,TMP,windir,ComSpec --ignore-env \
            scripts/smoke-binary.js "dist/$B" | tee "$RUNNER_TEMP/smoke.json"
          echo "| ${{ matrix.name }} | $(cat "$RUNNER_TEMP/smoke.json") |" >> "$GITHUB_STEP_SUMMARY"
      - run: mv dist/SHA256SUMS.txt dist/SHA256SUMS-${{ matrix.name }}.txt
        shell: bash
      - uses: actions/upload-artifact@cf430e030ddbb5b0abf93d22962f4752f3646cd9 # v7.0.2
        with:
          name: zaafpulse-${{ matrix.name }}
          path: dist/zaafpulse-*
          if-no-files-found: error
      - uses: actions/upload-artifact@cf430e030ddbb5b0abf93d22962f4752f3646cd9 # v7.0.2
        with:
          name: sha256-${{ matrix.name }}
          path: dist/SHA256SUMS-${{ matrix.name }}.txt
```

Runner fallbacks (decide on the first CI run, record in the P2 results): if `ubuntu-24.04-arm` stays queued for 15 minutes or is refused for this private repo, change that row to `runner: ubuntu-latest` and make its smoke step `if: matrix.name != 'linux-arm64'` with a summary line "built, not smoke-tested (no arm64 Linux runner)" — and say so at the gate (M8 then 5/6 smoke-tested). If `denoland/setup-deno` cannot install on `windows-11-arm`, build `win-arm64` on `windows-latest` (cross-compile with the same `--target`) in one job and smoke-test it in a second job on `windows-11-arm` that downloads the artifact and runs only the smoke step with a Deno installed by `irm https://deno.land/install.ps1 | iex` pinned to `v2.9.8`.

- [ ] **Step 8: What `release.yml` does after P2**

Release publishing belongs to P6. After P2, `release.yml` must not call the removed SEA build. Replace its `jobs:` block and the header comment with:

```yaml
# P2 state (publishing returns in P6): a version tag or a manual run calls ci.yml, which runs both test suites and the
# build-deno job: the six Deno binaries are compiled, smoke-tested on native runners and kept as build artifacts with
# their SHA-256 sums. Nothing is published and no GitHub release is created. Never move a published tag
# (CONTRIBUTING.md).
name: Release
on:
  push:
    tags: ['v*']
  workflow_dispatch:
permissions:
  contents: read
concurrency:
  group: release-${{ github.ref }}
  cancel-in-progress: false
jobs:
  test-and-build:
    uses: ./.github/workflows/ci.yml
```

Run: `grep -n "build-single\|postject\|setup-node\|gh release" .github/workflows/release.yml`
Expected: no output.

- [ ] **Step 9: Verify, then commit**

Run:
```bash
npm test 2>&1 | grep -E "^ℹ (pass|fail)"
export ZAAFPULSE_DATA="$(mktemp -d)" HOME="$(mktemp -d)"; NO_COLOR=1 deno test -q --no-check --no-prompt -P=test test/ 2>&1 | tail -1
git grep -n -- "deno test -A\|deno run -A\|--allow-all" -- .github package.json scripts docs/*.md README.md CONTRIBUTING.md bench test
```
Expected: `ℹ fail 0`; `ok | … | 0 failed | 3 ignored`; the grep prints nothing (`-A` gone from everything committed). If `bench/README.md` still shows an `-A`-free but broad command, leave it (it is not `-A`).

```bash
git add scripts/build-deno.js scripts/smoke-binary.js test/build-deno.test.js package.json package-lock.json deno.lock lib/service.js test/platform.test.js .github/workflows/ci.yml .github/workflows/release.yml CONTRIBUTING.md README.md docs/install.md docs/performance.md lib/indexer.js
git commit -m "Build the six single-file binaries with deno compile and smoke-test them in CI; remove the Node SEA build and postject"
```

Exit check after the controller pushes: the CI run on the branch shows six green `build-deno` jobs (or the recorded fallback), each summary line `"ok":true`.

---

### Task 7: Measure and document (spec T7)

**Files:**
- Modify: `test/contract/harness.js` (`ZP_RUNTIME=binary`), `bench/run.js`, `bench/README.md`
- Create: `bench/results/<date>-binary-<host>.json`
- Modify: `docs/migration/2026-11-10-p2-results.md`, `README.md` (a "Run on Deno" section), `docs/install.md`

**Interfaces:**
- Consumes: `dist/zaafpulse-<version>-<name>` (Task 6); `bench/compare.js`; `bench/results/2026-11-10-node-MBA-ZAAF.json` (baseline: startup p50 195.3 ms, idle RSS 108.9 MB).
- Produces: `ZP_RUNTIME=binary` with `ZP_BINARY=<path>` in the harness and bench; the P2 results document with the M5/M7 rows.

- [ ] **Step 1: The harness runs a binary**

In `test/contract/harness.js` `command()`, before the final `throw`, add:

```js
  if (runtime === 'binary') {
    if (!process.env.ZP_BINARY) throw new Error('ZP_RUNTIME=binary needs ZP_BINARY, the path of a built binary');
    return { bin: path.resolve(process.env.ZP_BINARY), args: ['start'] };
  }
```

update the error text to `ZP_RUNTIME must be node, deno or binary, not "${runtime}"`, and the header comment line `//   ZP_RUNTIME=node|deno   the runtime that runs server.js (default node)` to `//   ZP_RUNTIME=node|deno|binary   what runs the server (default node); binary needs ZP_BINARY=<path of a built binary>`. In `bench/run.js` `versionOf(runtime)`: when `runtime === 'binary'` return the output of `<ZP_BINARY> version` prefixed `binary `.

Run: `ZP_RUNTIME=binary ZP_BINARY="dist/$(cd dist && ls zaafpulse-*-darwin-arm64)" npm run test:contract 2>&1 | grep -E "^ℹ (pass|fail)"`
Expected: `ℹ pass 172`, `ℹ fail 0` (the contract suite green against the compiled binary).

- [ ] **Step 2: Bench the binary**

On the owner's Mac, on power, other heavy apps closed (bench/README.md):
```bash
ulimit -n 8192
ZP_RUNTIME=binary ZP_BINARY="dist/$(cd dist && ls zaafpulse-*-darwin-arm64)" node bench/run.js
node bench/compare.js bench/results/2026-11-10-node-MBA-ZAAF.json bench/results/<date>-binary-<host>.json
```
Expected: a new `bench/results/<date>-binary-<host>.json`; the comparison prints startup p50, idle RSS, non-stream overhead p50/p95/p99 (stream numbers are recorded but not judged; they are re-recorded at program end).

- [ ] **Step 3: Judge M5 and M7 for P2**

Gate rule (spec T7): startup p50 ≤ 195.3 × 1.10 = **214.8 ms** and idle RSS ≤ 108.9 × 1.10 = **119.8 MB**. Write the measured numbers into `docs/migration/2026-11-10-p2-results.md` as a table (metric, Node baseline, binary, change, met?). If startup misses (likely, finding 6: `deno run` was +32%), write the miss down plainly with the absolute value against M7 (≤ 1.0 s) and add it to the gate's owner questions; do not change product behaviour to chase it.

- [ ] **Step 4: Document running and building on Deno**

`README.md`: add a section "Run on Deno" after the install section:

```markdown
### Run on Deno (v2 line)

ZAAFPulse runs on Deno 2.9.8 with a narrow permission set per entry point (`deno.json`: `server`, `cli`,
`mcp-stdio`, `test`). The single-file binaries carry the `server` set. From a checkout:

    deno task build                                   # dist/zaafpulse-<version>-<os>-<cpu>
    deno run -q --no-prompt -P=server --allow-write="<data folder>,${TMPDIR:-/tmp}" server.js

A denied action fails with "Requires … access to …" in the UI or the log, never silently. The Compliance page
shows what the running process holds (control "Runtime permissions"). Secrets read from environment variables must be
named `ZP_SECRET_*` (or `ZAAFPULSE_*`), or be added to the env list in `deno.json` for a build of your own.
```

`docs/install.md`: one paragraph after the build line: the binaries are Deno binaries; `zaafpulse service install` points launchd/systemd/the Startup folder at the binary with the data folder as working folder; `zaafpulse doctor` prints `Deno <version>` and `(single-file build)`.

`docs/migration/2026-11-10-p2-results.md`: append the permission matrix (one row per set: read, write, net, env, sys, run, with the measured evidence of F15/F16 and the per-permission tests), the denial findings (F9, F12, F13, F14), the CI runner outcome of Task 6, and the spec findings of this plan with the owner decisions O1–O3 still open.

- [ ] **Step 5: Commit**

```bash
npx --yes prettier@3 --check test/contract/harness.js bench/run.js
git add test/contract/harness.js bench/run.js bench/README.md bench/results/*-binary-*.json docs/migration/2026-11-10-p2-results.md README.md docs/install.md
git commit -m "Measure the compiled binary against the Node baseline; document running and building on Deno"
```

---

## Gate G2 checklist

- [ ] 100% of the suite green under Deno with `-P=test` (M1); the CI `deno` job blocking (Task 4).
- [ ] 6/6 targets built in CI; native smoke tests green including `windows-11-arm` (M8), or the recorded fallback for `linux-arm64` (Task 6 Step 7).
- [ ] Permission sets narrow and tested; no `-A` in committed tasks or CI (M9 partial: read, net and the compiled binary's write stay broad, recorded).
- [ ] Startup and idle RSS of the compiled binary within +10% of the Node baseline (M5, M7) — or the miss recorded and accepted by the owner (finding 6).
- [ ] The Node suite still green (dual runtime until P7).
- [ ] Owner decisions recorded: **O1** env allow-list vs v1 secret names (keep, or `env: true` for v2.0); **O2** owner runtimes and IDE-only Claude Code in the compiled binary (accept, or unscoped run); **O3** the startup miss if any.
- [ ] **Owner action: 48 h live run on the Deno binary** (D7). Done by the owner, never by an implementer:
  1. **Pre-check (read only):** `plutil -p ~/Library/LaunchAgents/com.zaaf.aipulse.server.plist` — note `ProgramArguments`, `WorkingDirectory` and every `EnvironmentVariables` entry. The data folder is `ZAAFPULSE_DATA` if set, otherwise the live checkout folder itself (`.worktrees/live`, which holds `config.json` and `.data`). Run `grep -o '"env": *"[A-Za-z0-9_]*"' <data folder>/config.json`: every name must match `ZAAFPULSE_*`, `ZP_*` or the env list in `deno.json` (else rename it to `ZP_SECRET_<NAME>` in both the shell profile and config.json before switching, O1). List owner runtimes in `config.json` `localRuntimes`: their programs must be one of the preset names or they will be refused (O2).
  2. **Backup:** `B=~/.zaafpulse-backups/g2-$(date +%Y%m%d-%H%M); mkdir -p "$B"; cp ~/Library/LaunchAgents/com.zaaf.aipulse.server.plist "$B/"; launchctl bootout gui/$(id -u)/com.zaaf.aipulse.server; tar -czf "$B/data.tgz" -C "<data folder>" config.json .data` (the server is stopped while the data is copied).
  3. **Switch:** download the CI artifact `zaafpulse-darwin-arm64` of the merged commit, check it with `shasum -a 256` against `SHA256SUMS-darwin-arm64.txt`, `install -m 0755 zaafpulse-*-darwin-arm64 ~/.local/bin/zaafpulse-v2`, `xattr -d com.apple.quarantine ~/.local/bin/zaafpulse-v2 2>/dev/null`. Then, with every variable from step 1 exported in the shell **and** `ZAAFPULSE_DATA=<data folder>` set explicitly (a binary never uses the checkout as its data folder), run `~/.local/bin/zaafpulse-v2 service install`. It writes the same label `com.zaaf.aipulse.server` pointing at the binary, working folder = the data folder.
  4. **Verify:** `~/.local/bin/zaafpulse-v2 status` → running on the usual port; the dashboard opens; Compliance shows "Runtime permissions" with `Deno 2.9.8, single-file build`; `zaafpulse-v2 doctor` shows the same data folder as before.
  5. **Watch 48 h:** `grep -c "NotCapable\|Requires .* access" ~/Library/Logs/ZAAFPulse/server.log` stays 0 (any hit is a missing permission: note the feature and roll back if it matters); `launchctl print gui/$(id -u)/com.zaaf.aipulse.server | grep -E "runs|last exit"` shows 1 run and no crash; the audit chain verifies on the Governance page; budgets and the gateway usage keep counting.
  6. **Rollback (any time):** `launchctl bootout gui/$(id -u)/com.zaaf.aipulse.server; cp "$B/com.zaaf.aipulse.server.plist" ~/Library/LaunchAgents/; launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.zaaf.aipulse.server.plist`. v2 keeps the v1.1 data format (M12); only if the v1 server reports a problem with its data, stop it again and restore `tar -xzf "$B/data.tgz" -C "<data folder>"` before starting it.

## Risks

| # | Risk | Mitigation |
|---|---|---|
| R1 | Permissions in `deno.json` are an experimental Deno feature ("may change") | Deno pinned to 2.9.8; `test/permissions.test.js` fails on any change of behaviour; re-check on every Deno bump |
| R2 | `ubuntu-24.04-arm` not offered to this private repo | fallback in Task 6 Step 7 (cross-compile, smoke-test skipped, said at the gate) |
| R3 | `setup-deno` on `windows-11-arm` | fallback in Task 6 Step 7 (build on windows-latest, smoke on windows-11-arm) |
| R4 | A feature needs a permission the walk did not exercise (Linux/Windows-only paths, owner runtimes, notifications) | loud refusals (F13 fix, F16), the per-permission tests on CI's ubuntu and macOS, the 48 h log grep |
| R5 | Startup +10% target missed | finding 6; recorded, owner decision O3 |
