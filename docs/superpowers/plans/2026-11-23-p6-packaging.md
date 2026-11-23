# P6: packaging and release: implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or
> superpowers:executing-plans to implement this plan task by task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Each release target ships one archive holding `zaafpulse` and `zaafpulse-gw`. The compiled control plane can
start and supervise its gateway, and the Overview shows the gateway's health. The installers install both binaries,
upgrade over v1.x and roll back. A release workflow publishes on a tag only, and its dry run proves the build, the smoke
tests and the install tests on all six targets.

**Architecture:** `scripts/build-deno.js --package` compiles the Deno binary as today and builds the Go gateway for the
same target, then packs both. The binary's `cli` permission set names `zaafpulse-gw`. Every launch the product controls
puts the install folder first on `PATH`, because Deno resolves a named run grant through `PATH` at start. The workflows
split into `build.yml` (reusable, six native runners) and `release.yml` (tag: test, build, install-test, publish;
dispatch: a dry run that publishes nothing).

**Tech Stack:** Go 1.27.3 (module `gw/`, `GOTOOLCHAIN=local`, standard library only). Deno 2.9.8 and Node 24 (CommonJS,
`node:` built-ins only, `node:test`). POSIX `sh` and PowerShell 5.1/7. GitHub Actions pinned by SHA.

## Global constraints

- **The spec is binding.** `docs/superpowers/specs/2026-11-23-p6-packaging-design.md`, Decisions 1–18. Where the code
  contradicts the spec, follow the code and add an entry under "Plan deviations" at the end of this plan.
- **Dependencies:** Go is standard library only. Deno has no npm or jsr imports. Follow the `deno`, `migrate-to-deno`
  and `go-stack-defaults` skills.
- **Never publish anything.** No tag, no GitHub release, no attestation from a dispatch, no change to repo settings, no
  push to another repo.
- **Protect the owner's live install.** Never touch port 4777, launchd `com.zaaf.aipulse.server`, `.worktrees/live` or
  any installed ZAAFPulse service. **Never run `zaafpulse service install|uninstall` or an installer without
  `--no-service` on this machine.** The launchd label is the owner's live one. Service paths are exercised on CI runners
  only. Local installer tests use a temp `HOME` and prefix.
- **Processes:** kill only processes you started. Run long local runs under `caffeinate -i`.
- **Commits:** small and often. Push each commit at once. No bare `git stash`, no pushed temporary branches. Never commit
  `.superpowers/`, `dist/` or `config.json.bak-*`. No AI attribution lines (no `Co-Authored-By`).
- **Formatting:** prettier only on files that were already clean (`npx prettier --check <file>` first). Run `gofmt` on Go
  files.
- **CI policy (owner):** do not wait for CI between tasks, and do not dispatch the full matrix. The one exception is a
  single `release.yml` dry-run dispatch on this branch, in Task 12.
- **Local checks per task:**
  - Go: `go -C gw vet ./...` (also `GOOS=windows`), `gofmt -l gw`, `go -C gw test -race -count=1 ./...` (for the
    packages touched).
  - JS: the touched tests on Node (`node --test test/<file>`) and on Deno (`deno test -q --no-check --no-prompt -P=test
    test/<file>`).
  - Types: `deno task typecheck` when `lib/front` changes.

## Files

| File | Task | Change |
|---|---|---|
| `gw/cmd/zaafpulse-gw/main.go`, `main_test.go` | 1 | `version` var (default `dev`), `-version` flag |
| `scripts/build-deno.js`, `test/build-deno.test.js`, `package.json` (`build` script permissions) | 2 | `--package`: gateway build, archives, sums; `gw/` in `ARCHIVE` |
| `deno.json` (`cli` run), `lib/permissions.js` (WHY), `lib/service.js` (`servicePath`), `lib/front/bin.js`, `lib/front/index.js` (fallback reason), `bin/zaafpulse.js` (`doctor`), tests | 3 | Decision 4 |
| `scripts/smoke-binary.js` | 4 | `--front <gw>` pass |
| `lib/ipc/supervisor.js` (status `gwVersion`), `lib/front/index.js` (status fields), `public/topologyview.js` (chip), tests | 5 | Decisions 5, 6 |
| `install.sh`, `test/install-sh.test.js` | 6 | Decisions 9–11 |
| `install.ps1` | 7 | Decisions 9–11 |
| `scripts/upgrade-check.js` | 8 | Decision 12 |
| `.github/workflows/build.yml` (new), `ci.yml` | 9 | Decision 13 |
| `.github/workflows/release.yml`, `scripts/install-test.sh`, `scripts/install-test.ps1` | 10 | Decisions 8, 13, 14 |
| `docs/install.md`, `README.md` (install table only), `docs/release-hardening.md` | 11 | docs |
| `docs/migration/2026-11-23-p6-results.md`, `docs/migration/2026-11-10-deno-go-migration-plan.md` | 12 | results |

## Interfaces (fixed here; every task uses these names)

- **Archive names:** `zaafpulse-<v>-<name>.tar.gz` or `.zip` (the `win-*` targets). Top folder `zaafpulse-<v>-<name>/`
  holding `zaafpulse[.exe]`, `zaafpulse-gw[.exe]` and `LICENSE`. `<name>` comes from `TARGETS` in
  `scripts/build-deno.js`.
- **`scripts/build-deno.js` exports:** add `GO_TARGETS` (triple → `{ goos, goarch }`), `archiveName(version, target)`,
  `gwBuildArgs({ version, out })` (the `go build` argv, Decision 2) and `parseArgs` accepting `--package`.
  `SHA256SUMS.txt` lines are `<hex>  <basename>`.
- **Gateway version:** `zaafpulse-gw -version` prints `<version>\n` and exits 0. Build flag `-X main.version=<v>`.
- **`lib/front/bin.js`:** add `gwAllowed(p, { deno = globalThis.Deno } = {})`, which returns `true` on Node; on Deno it
  returns `Deno.permissions.querySync({ name: 'run', command: p }).state === 'granted'`. Add
  `gwStatus(opts) → { path, found, allowed }`.
- **Refused reason (exact text):** `zaafpulse-gw is next to zaafpulse but not allowed to run: put its folder first on
  PATH (the installer's zaafpulse command does)`. When the file is missing: `zaafpulse-gw not found at <path>`.
- **`GET /api/gateway/status` additions:** `gwVersion: string|null`, `version: string`, `versionMismatch: boolean`,
  `binary: { path, found, allowed }`, `healthAgeMs: number|null`.
- **Installer options:**
  - `install.sh`: `--archive FILE`, `--sums FILE`, `--rollback`, `--verify-attestation`, plus the kept
    `--server --host --port --version --no-service --no-modify-path --uninstall --purge`.
  - `install.ps1`: `-Archive`, `-Sums`, `-Rollback`, `-VerifyAttestation`, plus the kept options.
  - The environment variable `ZAAFPULSE_PREFIX` keeps overriding the prefix of a user install.
- **Rollback folder:** `<prefix>/rollback/` holds the saved generation, and `<prefix>/rollback/VERSION` holds a single
  line, the old version (`v1.1.0`, or `unknown`). On Windows, `<prefix>` is `%LOCALAPPDATA%\ZAAFPulse`.
- **Workflow artifacts:** the build job uploads `pkg-<name>` (the archive) and `sum-<name>` (`SHA256SUMS-<name>.txt`).
  The dry run uploads `release-dry-run` (`dist/` + `SHA256SUMS.txt`).

---

## Slice P6a: build and package

### Task 1: the gateway's version

- [ ] Make `version` in `gw/cmd/zaafpulse-gw/main.go` a `var` with the default `"dev"`, keeping the comment that says
  it is set at build time. Keep every use: `hello.GwVersion` and any log line.
- [ ] Add a `-version` flag to the flag set. When it is given, print the version and exit 0 before anything else (no
  stdin read, no config).
- [ ] Add a test in `main_test.go`: it builds with `-ldflags -X main.version=9.9.9-test` into `t.TempDir()`, runs
  `-version` and checks the output. Follow the helpers already in `main_test.go`.
- [ ] Update every test or fixture that asserted `0.4.0-front`. `grep -rn "0.4.0-front" gw lib test ipc` must come back
  empty, or each remaining hit must be explained in the commit message.
- [ ] Run gofmt, vet (also `GOOS=windows`) and `go -C gw test -race -count=1 ./cmd/...`. Commit and push.

### Task 2: `deno task build --package`

- [ ] In `scripts/build-deno.js`:
  - add `'gw'` to `ARCHIVE`, so the gateway sources come from the committed tree; it is not in `INCLUDE`, so the binary
    does not embed it;
  - add `GO_TARGETS`, `archiveName` and `gwBuildArgs` (see Interfaces);
  - with `--package`, for each target: compile `zaafpulse` as today into a staging folder
    `dist/.pkg/zaafpulse-<v>-<name>/`, named `zaafpulse[.exe]`; run `go build` (argv from `gwBuildArgs`, cwd `<src>/gw`,
    env = the allowed Go names plus `GOOS`, `GOARCH`, `CGO_ENABLED=0`) to `zaafpulse-gw[.exe]` in the same folder; copy
    `LICENSE`; pack it with `tar -czf` (unix targets) or `tar -a -cf <x>.zip` (win targets), with `cwd` at `dist/.pkg`
    and relative names (Git Bash on Windows reads `D:\` as a host);
  - before packing a zip, check that `tar --version` says `bsdtar`. If not, throw `zip packages need bsdtar (macOS or
    Windows); build win-* targets there`;
  - put the archive in `dist/`, then remove `dist/.pkg`. `SHA256SUMS.txt` lists the archives;
  - print each archive's size, and each binary's size, in MB;
  - without `--package`, the behaviour is unchanged.
- [ ] Update `package.json` `scripts.build`:
  - `--allow-run=git,tar,deno,go`;
  - `--allow-env=PATH,HOME,GOCACHE,GOPATH,GOROOT,GOTOOLCHAIN,GOFLAGS,GOMODCACHE,LOCALAPPDATA,USERPROFILE,TMPDIR,TEMP,TMP,SystemRoot,APPDATA`;
  - keep `--ignore-env`; check with `deno run --help` that the two flags combine as intended (an env read outside the
    list returns undefined).
  - Make sure the child env is built only from those names.
- [ ] Add to `test/build-deno.test.js`:
  - `archiveName` for all six targets;
  - `gwBuildArgs` holds `-trimpath`, `-buildvcs=false` and `-ldflags=-s -w -X main.version=<v>` (one argument), and
    never `-race`;
  - `GO_TARGETS` covers every `TARGETS` key;
  - `parseArgs` accepts `--package`;
  - `ARCHIVE` includes `gw` and `INCLUDE` does not.
- [ ] Local proof: under `caffeinate -i`, run `deno task build --package` for the host, unpack the archive into a temp
  folder, run `zaafpulse-gw -version` (expect `1.1.0`) and `zaafpulse version`, and record the sizes in the ledger. Also
  build `--package --target x86_64-pc-windows-msvc` (the zip path on bsdtar), and check that `unzip -l` lists both
  `.exe` files.
- [ ] Commit and push.

### Task 3: the binary may start its gateway (Decision 4)

- [ ] **`deno.json` `cli.run`:** add `"zaafpulse-gw"`. **`lib/permissions.js` WHY:** add its reason ("the Go gateway
  next to the binary; granted through PATH, which the installer's launcher and the service file put first").
  `test/permissions.test.js` must stay green: add the per-permission proof it requires for a new run entry, following
  the pattern used for the other names.
- [ ] **`lib/service.js` `servicePath`:** when `platform.isSingleFile()` and a `zaafpulse-gw[.exe]` exists next to
  `process.execPath`, put `dirname(process.execPath)` first. Make it injectable (`opts.singleFile`, `opts.execPath`,
  `opts.exists`) for the test. Add cases to `test/service-path.test.js`: it is put first, kept once, left out when the
  gateway is missing or the build is not a single file, and an unsafe folder is never added (`permissions.UNSAFE`).
- [ ] **`lib/front/bin.js`:** add `gwAllowed` and `gwStatus` (Interfaces). No `lib/platform.js` require, as the file's
  comment says.
- [ ] **`lib/front/index.js`:** before the supervisor's first start in front mode, check `gwStatus()`. Missing, or not
  allowed, is a fatal start: fall back at once (no restarts) with the exact reason from Interfaces. Find where a fatal
  first start is handled today (`onFailed(reason, { kind: 'fatal' })`) and reuse it. Do not add a new path.
- [ ] **`bin/zaafpulse.js doctor`:** add a line `Gateway (zaafpulse-gw): <path> — found|missing, allowed|not allowed,
  <version>`. The version comes from `<path> -version`, and only when the file is found and allowed. `doctor`'s exit
  code is unchanged by this line; it is information.
- [ ] Add a test that the fallback reason is the refused one. Put it in the closest front test file (for example
  `test/front-server.test.js`) and inject `gwStatus` or the env (`ZAAFPULSE_GW_BIN` at a missing path gives "not
  found").
- [ ] Run the touched tests on Node and Deno, and `deno task typecheck`. Commit and push.

### Task 4: `smoke-binary.js --front`

- [ ] Add the option `--front <gw path>` (when it is given, the gateway must sit next to the binary; the caller unpacks
  the archive). In this pass the script:
  - writes `{"gateway":{"engine":"front"}}` into the throwaway data folder's `config.json` before the start;
  - starts the binary with `PATH=<binary folder><sep><PATH>`;
  - waits for `/login.html` 200 through the main port;
  - checks `/api/gateway/status` (as owner, auth none): `mode` `front`, `state` `ready`, a `pid`, and `gwVersion` equal
    to the package version;
  - stops the binary and checks the main port is free within 15 s.

  Adds `front: { ok, gwVersion, readyMs }` to the JSON line. `readyMs` (spawn → gateway ready) is M7 information and is
  not quiet-confirmed.
- [ ] Keep the header comment's usage line accurate. The deno run flags of the caller must cover the new spawn: the
  binary is already allowed, and the gateway is spawned by the binary, not by the script.
- [ ] Local proof: the host package from Task 2, unpacked, then `scripts/smoke-binary.js <bin> --front <gw>`. It must
  print `ok: true` with `front.ok: true`. Commit and push.

## Slice P6b: health on the Overview

### Task 5: status fields and the chip

- [ ] `lib/ipc/supervisor.js`: keep the `gwVersion` from the last accepted hello, and put it in `status()`
  (`null` before the first hello).
- [ ] `lib/front/index.js status()`: add `version` (`package.json`), `versionMismatch` (Decision 5), `binary`
  (`gwStatus()`, cached once per start, not per call) and `healthAgeMs` (now − the time `lastHealth` was received; read
  how `lastHealth` is stored and add a receive time if it has none).
- [ ] `public/topologyview.js topoGatewayChip`, per Decision 6:
  - ready: `Gateway: Go (front) · ready · v<gwVersion>`, with a tooltip of uptime, in flight, outbox, snapshot age,
    restarts and last failure;
  - `· stale` amber when `healthAgeMs > 5000`;
  - `· version differs` amber on `versionMismatch`;
  - fallback: the tooltip carries the reason (it does already; check that the Decision 4 text arrives).

  Text and attributes only.
- [ ] Tests:
  - `test/topology-ui.test.js`, the pure chip function: ready with a version, stale, mismatch, fallback with the refused
    reason;
  - the owner-only rule stays green;
  - one server-level check that the status carries `gwVersion` and `binary`, in the front test file Task 3 used.
- [ ] Run the touched tests on Node and Deno, and `deno task typecheck`. Commit and push.
- [ ] Preview: start a preview server from this worktree on a free port other than 4777 and 4787 (use 4797), with its
  own data folder and `gateway.engine` `"front"` (`npm run gw:build` first), and check the chip. Kill only that
  process. Record the port in the ledger.

## Slice P6c: installers

### Task 6: `install.sh`

Rewrite it per Decisions 9–11, keeping the header comment style, `say`, `warn`, `die`, `fetch`, `sha256`, the PATH
profile edit, server mode and the closing summary.

- [ ] **Platform:** `darwin|linux` × `x64|arm64` only. Linux on musl (`/etc/alpine-release`, or `ldd --version` says
  musl), armv7l, ppc64le and s390x stop with: `no v2 build for <plat>-<arch>; the v1.x installer still supports it:
  curl -fsSL https://raw.githubusercontent.com/rifqifitriady/ZAAFPulse/main/install.sh | sh`.
- [ ] **Source:**
  - with `--archive FILE`, use that file. `--sums FILE` is optional; without it, warn "not checksum-verified (local
    archive)";
  - otherwise resolve `VERSION` (latest via the API, as today), and download `SHA256SUMS.txt` and
    `zaafpulse-${VERSION#v}-$PLAT-$NARCH.tar.gz` from `releases/download/$VERSION`;
  - verify the checksum. A mismatch or a missing line installs nothing;
  - with `--verify-attestation`, run `gh attestation verify <archive> --repo $REPO`. Stop if `gh` is missing or the
    check fails.
- [ ] **Unpack** into `$TMP`, and check that both binaries are there and executable. On Darwin, run
  `xattr -d com.apple.quarantine` on both (ignore "no such xattr").
- [ ] **Detect the current install:**
  - v1: `$PREFIX/app/bin/zaafpulse.js` exists;
  - v2: `$PREFIX/bin/zaafpulse` exists;
  - `OLD_VERSION`: for v2, from `"$PREFIX/bin/zaafpulse" version`; for v1, from `$PREFIX/app/package.json` (sed for
    `"version"`); else `unknown`.
- [ ] **Stop the old service,** only when the old install exists and `--no-service` is not set:
  `"$LAUNCHER" service uninstall [--system] || true`. Run the old launcher before it is replaced.
- [ ] **Save the rollback generation:**
  1. `rm -rf $PREFIX/rollback.new`, then `mkdir` it;
  2. move into it `$PREFIX/app` and `$PREFIX/node` (v1), or `$PREFIX/bin` (v2), plus a copy of `$LAUNCHER` as
     `launcher`;
  3. write `VERSION` (`v$OLD_VERSION`, or `unknown`);
  4. then `rm -rf $PREFIX/rollback` and `mv $PREFIX/rollback.new $PREFIX/rollback`.

  Do nothing when there is no old install.
- [ ] **Install:** `$PREFIX/bin.new` ← both binaries, `mv` into `$PREFIX/bin`. Write the launcher wrapper:
  ```sh
  #!/bin/sh
  PATH="<prefix>/bin:$PATH"; export PATH
  exec "<prefix>/bin/zaafpulse" "$@"
  ```
  For a root install, `chmod -R a+rX`.
- [ ] **`--rollback`:**
  1. stop when `$PREFIX/rollback/launcher` is missing ("nothing to roll back to");
  2. stop the current service through `$LAUNCHER` (unless `--no-service`);
  3. move the current `bin` (and the v1 `app`/`node`, if any) into `$PREFIX/rollforward/`, replacing an earlier one;
  4. move the saved generation's folders back, and copy `rollback/launcher` to `$LAUNCHER`;
  5. `rm -rf $PREFIX/rollback`;
  6. reinstall the service through the restored launcher (unless `--no-service`);
  7. say which version is now active.
- [ ] **`--uninstall`:** also removes `bin`, `rollback` and `rollforward`. `--purge` is unchanged.
- [ ] **Update the help text** (lines 2–17) and the usage URL:
  `https://github.com/rifqifitriady/ZAAFPulse-deno-go/releases/latest/download/install.sh`.
- [ ] **`test/install-sh.test.js`** (skip on win32; needs `sh` and `tar`). Every case runs with `HOME` and
  `ZAAFPULSE_PREFIX` in a temp folder, `--no-service` and `--no-modify-path`, and uses a fake archive made in the test:
  two shell scripts named `zaafpulse` (prints `2.0.0-test` for `version`, and its args otherwise) and `zaafpulse-gw`,
  with real sums.
  - clean install: the layout, the launcher runs the binary with its folder first on `PATH` (the fake prints `$PATH`
    for a `path` arg), and there is no `rollback/`;
  - a checksum mismatch installs nothing;
  - an upgrade over a fake v1 layout (`app/bin/zaafpulse.js`, `app/package.json` version `1.1.0`, a launcher):
    `rollback/VERSION` is `v1.1.0` and `rollback/app` exists;
  - `--rollback` restores the v1 launcher and app;
  - `--rollback` with nothing saved fails and changes nothing;
  - an upgrade v2 → v2 keeps one generation;
  - `--uninstall` removes everything but the data.

  The installer is run with `sh install.sh …`. The test never runs a real service command: the fakes record their args
  to a file, and the test asserts `service` was not called under `--no-service`.
- [ ] `sh -n install.sh`. Run shellcheck if it is installed (it is not on this machine today; note that). Run the test
  on Node and on Deno (`-P=test`). Commit and push.

### Task 7: `install.ps1`

- [ ] Mirror Task 6 in PowerShell 5.1-compatible syntax, keeping today's structure: `Say`, `Warn`, the user `PATH`
  edit, server mode, the Start menu shortcut (it now runs `zaafpulse.exe open` through the same hidden `.vbs`) and the
  summary.
  - Arch: `x64|arm64`. 32-bit Windows stops with the v1.x pointer.
  - Source: `-Archive` and `-Sums`, or the release `.zip` with `SHA256SUMS.txt`. `-VerifyAttestation` runs `gh`.
  - Unpack with `Expand-Archive` into a temp folder.
  - Detect the old install: v1 is `$Root\app\bin\zaafpulse.js`; v2 is `$BinDir\zaafpulse.exe`.
  - Stop the old service with the old launcher (`zaafpulse.cmd` for v1, `zaafpulse.exe` for v2), unless `-NoService`.
    This also matters because running `.exe` files cannot be replaced.
  - Save to `$Root\rollback\`: v1 `app`, `node`, `bin\zaafpulse.cmd`, `bin\zaafpulse-open.vbs`; or v2
    `bin\zaafpulse.exe`, `bin\zaafpulse-gw.exe`. Write `VERSION`.
  - Install both `.exe` files into `$BinDir`, and remove a leftover `zaafpulse.cmd` (it is saved in rollback).
  - `-Rollback` and `-Uninstall` per Decision 11.
- [ ] There is no PowerShell on this machine, so there is no local run: the first execution is the dry run's
  install-test (Task 10). Make the code reviewable:
  - no aliases;
  - `$ErrorActionPreference = 'Stop'`;
  - every external call checks `$LASTEXITCODE`;
  - paths through `Join-Path`.

  The reviewer reads it against `install.sh` line by line.
- [ ] Commit and push.

## Slice P6d: M12

### Task 8: `scripts/upgrade-check.js`

- [ ] Write the script per Decision 12. It is Node-runnable (`node scripts/upgrade-check.js --v2 <cmd…> [--v1 DIR]`).
  `--v2` takes the v2 start command: the built binary path, or `deno-run` to use `deno run` with the `cli` set from this
  tree.
  - **v1 source:** without `--v1`, download `zaafpulse-1.1.0-source.tar.gz` and `SHA256SUMS.txt` from
    `https://github.com/rifqifitriady/ZAAFPulse/releases/download/v1.1.0/`, verify, and unpack into a temp folder.
  - **Data folder:** a temp folder. Every run sets `ZAAFPULSE_DATA`, `HOME` and `USERPROFILE` to temp paths,
    `ZAAFPULSE_AUTH=none`, and a free port that is never 4777 or 4787.
  - **Fake provider:** a local `http.createServer` answering an OpenAI chat completion with fixed usage.
  - **Through each version's HTTP API,** in order v1 → v2 → v1. Find the exact routes in `server.js` of both
    versions: v1 is the unpacked source.
    - add a provider pointing at the fake, a gateway key, and a budget;
    - make one `/v1/chat/completions` call with the key;
    - change one setting (an audit entry);
    - on v2, set `gateway.engine` `"front"` before its start and put the gateway next to the binary (or `ZAAFPULSE_GW_BIN`
      for `deno-run`), then make one call.
  - **Final v1 checks:**
    - `/login.html` 200;
    - the audit verify route says ok;
    - the usage or cost totals count 2 or more calls (v1's and v2's);
    - every key in the v1-written `config.json` is still there with the same value (deep compare on v1's keys only).
  - **Output:** one JSON line `{ ok, steps: [...] }`, and a non-zero exit on failure. Kill only the children it
    started, also on failure (`finally`).
- [ ] Local proof:
  - `caffeinate -i node scripts/upgrade-check.js --v2 <the unpacked host binary from Task 2>` must print `ok: true`;
  - run once more with `--v2 deno-run`;
  - record both in the ledger.

  If v1.1.0 cannot read something v2 wrote, stop and report it: that is an M12 failure, not a test to bend.
- [ ] Commit and push.

## Slice P6e: workflows

### Task 9: `build.yml` and `ci.yml`

- [ ] Create `.github/workflows/build.yml`:
  - `on: workflow_call` with no inputs, `permissions: contents: read`;
  - one job `build` over the six-target matrix of today's `build-deno`, with the same runners, the same Deno setup and
    the same denort cache, plus `setup-go` 1.27.3 (`cache: false`, `GOTOOLCHAIN: local`).
  - Steps:
    1. `deno task build --package --target <triple>`;
    2. on macOS, `codesign --verify --strict` on both unpacked files;
    3. unpack the archive into `$RUNNER_TEMP/pkg` (tar, or `Expand-Archive`/`unzip` on Windows; `tar -xf` works with
       Windows bsdtar for zip too);
    4. `scripts/smoke-binary.js <zaafpulse>` (as today);
    5. `scripts/smoke-binary.js <zaafpulse> --front <zaafpulse-gw>`;
    6. today's Go-front smoke (`smoke-front.js`);
    7. append the archive's size in MB and v1.1.0's size for that target to the step summary (the v1.1.0 sizes as a
       fixed table in the workflow: they never change, and they are in the spec).
  - Upload `pkg-<name>` and `sum-<name>` (Interfaces).
- [ ] `ci.yml`:
  - add `workflow_call` input `skip-build` (boolean, default false);
  - replace the `build-deno` job with `build-deno: uses: ./.github/workflows/build.yml`, with
    `if: ${{ !inputs.skip-build && (<the full-scope condition>) }}`;
  - update the header comment.

  A reusable workflow called from a job takes no `runs-on`: check the syntax against the GitHub docs, and note it in
  the header.
- [ ] Validate both files with `python3 -c 'import yaml,sys; yaml.safe_load(open(sys.argv[1]))'`, if PyYAML is there,
  else with `deno eval` and a YAML parse (`jsr:@std/yaml` is allowed in a throwaway eval, but never committed). Read
  every `${{ }}` against the docs. Commit and push. Do not wait for CI.

### Task 10: `release.yml` and the install test

- [ ] Write `scripts/install-test.sh` (macOS, Linux) and `scripts/install-test.ps1` (Windows). Each takes the archive,
  the sums and the target name, and runs Decision 14's three parts.
  - Temp `HOME` and prefix. Never port 4777: use 4877.
  - **Clean install:**
    1. install with `--archive --sums --no-modify-path` (service: `--no-service` on Linux; on macOS and Windows
       runners, with the service);
    2. check `version` and `doctor`;
    3. start in the background through the launcher, or check that the service answers;
    4. check that `/api/gateway/status` is `ready` (config `gateway.engine` `"front"`, written first);
    5. stop it, or uninstall the service.
  - **Upgrade:**
    1. fetch the v1.1.0 installer from `raw.githubusercontent.com/rifqifitriady/ZAAFPulse/v1.1.0/install.sh` (`.ps1`);
    2. run it with `--no-service --version v1.1.0` into the same prefix;
    3. start it, write a marker setting through the API, and stop it;
    4. run the v2 installer from the artifacts;
    5. check that v2 starts, sees the marker, and that `rollback/VERSION` is `v1.1.0`;
    6. `--rollback`;
    7. check that v1.1.0 starts and sees the marker.
  - Print a summary line per part.
- [ ] Rewrite `.github/workflows/release.yml` per Decision 13:
  - triggers: `push: tags: ['v*']` and `workflow_dispatch` (input `tests`, boolean, default false), top-level
    `permissions: contents: read`, and today's concurrency;
  - jobs:
    - `version` (tag only): the tag equals `v` + `package.json` version;
    - `test`: `uses: ./.github/workflows/ci.yml` with `skip-build: true`;
      `if: startsWith(github.ref, 'refs/tags/v') || inputs.tests`;
    - `build`: `uses: ./.github/workflows/build.yml`;
    - `install-test`: needs `build`, over the six-target matrix. It downloads `pkg-<name>` and `sum-<name>`, sets up
      Node 24.21.0 (the v1.1.0 install needs Node), and runs the right install-test script;
    - `assemble` (dispatch only): needs `build` and `install-test`. It downloads every `pkg-*` and `sum-*`, adds
      `install.sh` and `install.ps1`, writes `SHA256SUMS.txt` over everything (`sha256sum`, the `<hex>  <name>`
      form), and uploads `release-dry-run`;
    - `publish` (`if: startsWith(github.ref, 'refs/tags/v')`): needs `version`, `test`, `build` and `install-test`. It
      has `permissions: contents: write, id-token: write, attestations: write`. It does the same assembly, unpacks the
      archives into `dist/bin/` for the attestation subjects only (they are not uploaded), attests per Decision 8 with
      the v1 pin, and runs `gh release create "$GITHUB_REF_NAME" <assets> --repo "$GITHUB_REPOSITORY" --verify-tag
      --title "ZAAFPulse $GITHUB_REF_NAME" --generate-notes`.
  - The header comment states:
    - a dispatch never publishes, attests or tags;
    - a tag publishes;
    - never move a published tag.
- [ ] Validate the YAML as in Task 9. Review that no job but `publish` has a write permission, and that `publish`
  cannot run on `workflow_dispatch`. Commit and push.

## Slice P6f: docs and results

### Task 11: install docs

- [ ] `docs/install.md`:
  - update the opening, "Pick a way", both installer sections (layout table, options, upgrade, rollback) and
    "Single-file executables" (it becomes "Release packages": the archive, the two binaries, `PATH`);
  - add a section **"Unsigned binaries: first start and verifying a download"**:
    - macOS Gatekeeper: `xattr -d com.apple.quarantine zaafpulse zaafpulse-gw` for a browser download, or System
      Settings → Privacy & Security → Open Anyway; the installer's `curl` download needs neither;
    - Windows SmartScreen: More info → Run anyway, on the first start of each `.exe`; the installer's download is not
      marked;
    - checking `SHA256SUMS.txt`: `shasum -a 256 -c --ignore-missing SHA256SUMS.txt`,
      `sha256sum -c --ignore-missing SHA256SUMS.txt`, and in PowerShell `Get-FileHash` compared to the line;
    - checking the attestation: `gh attestation verify <file> --repo rifqifitriady/ZAAFPulse-deno-go`, and what a pass
      looks like;
  - note the v1.x-only platforms (musl, armv7l, ppc64le, s390x).
- [ ] `README.md`: only the install table rows and the Releases link point at this repo's release assets. The rest of
  the README is P7's.
- [ ] `docs/release-hardening.md`: add a short section saying the same two settings are to be applied to
  `rifqifitriady/ZAAFPulse-deno-go` before its first tag. Give the commands with that repo name. Do not run them.
- [ ] Prettier check before and after, only on files that were clean. Commit and push.

### Task 12: dry run, results, program plan, final review

- [ ] Dispatch once: `gh workflow run release.yml --ref migrate/p6-packaging -f tests=false`. Record the run id. Watch
  it with `gh run watch <id> --exit-status` (or poll `gh run view`) under `caffeinate -i`.
  - **If it fails:** fix it locally, then re-dispatch. Each re-dispatch is a deviation to note, because the owner
    allowed one dispatch: keep them to the minimum, and fix all the failures one run shows before re-dispatching.
  - **When it passes:** download `release-dry-run` and check the asset list and `SHA256SUMS.txt` (`sha256sum -c`).
- [ ] Write `docs/migration/2026-11-23-p6-results.md`:
  - what ships per slice;
  - M6 (a table per target: archive MB against v1.1.0 MB, from the dry run);
  - M8 (the six build jobs and six install tests, with job links);
  - M12 (`upgrade-check` results: local and CI);
  - M7 information, not quiet-confirmed, added to the deferred list;
  - Q1 and Q2 as decided, pending owner;
  - known differences;
  - **the manual owner steps with exact commands:**
    1. apply the tag ruleset and immutable releases to this repo;
    2. bump `package.json` to the release version (P7);
    3. push the tag;
    4. watch `release.yml`;
    5. verify the release (`gh release verify`, `gh attestation verify` on each asset);
    6. run the clean install and the upgrade from v1.1.0 from the public release on each of the six platforms (the
       commands per OS, including a v1.1.0 install first and `--rollback`).
- [ ] `docs/migration/2026-11-10-deno-go-migration-plan.md`:
  - P6 exit: "met on `migrate/p6-packaging` except the public-release leg (owner, after the first tag)";
  - the M6 and M8 rows, and the P5 Q1/Q2 note;
  - keep the edits small.
- [ ] Final whole-branch review (a fresh reviewer over `git diff c41a1a1..HEAD`), then fix rounds. Commit and push.

## Plan deviations

- **Task 2, the zip (spec Decision 3):** the win-* zip is written in JS by `writeZip` in `scripts/build-deno.js`
  (deflate from `node:zlib`, a table CRC-32, no zip64, files of 4 GiB or more refused, a fixed 1980-01-01 DOS time so
  the archive is reproducible, unix mode 0755 on the `.exe` files), not with `tar -a -cf`. On the Windows runners the
  build runs under bash, where `tar` is Git's GNU tar, which cannot write a zip, so the bsdtar check would have failed
  every win-* package in CI. The bsdtar check is gone, and win-* packages now build on any OS. The tar.gz path is
  unchanged (`tar -czf`).
- **Task 3, the fatal start (spec Decision 4):** the check runs inside the supervisor, through a new `precheck` option
  of `createSupervisor` (`lib/ipc/supervisor.js`), which `start()` asks before the first launch and, on a reason, ends
  in the existing `fail(reason, code, 'fatal')`. The front passes `precheck: () => gwRefusal(gwStatus(bin))`. Checking in
  `lib/front/index.js` alone could only reach the fallback without the supervisor's failed state (`state: 'failed'`,
  `failed.reason`), which `test/front-server.test.js` and the Task 5 chip read. `lib/front/bin.js` also exports
  `gwRefusal(status)`, the one source of the two reason texts. A missing binary now reads `zaafpulse-gw not found at
  <path>` instead of the spawn error. `servicePath` adds the binary's folder only when it writes a PATH at all: on
  Windows with no PATH at install, a PATH of that folder alone would hide the system's programs (the install folder is
  on the user PATH there anyway).
- **Task 4, `gwVersion` early:** `/api/gateway/status` had no `gwVersion` before Task 5, so the `--front` check could not
  pass. Task 4 takes Task 5's first bullet: `lib/ipc/supervisor.js` keeps the version of the last accepted hello in
  `status().gwVersion` (`null` before the first), with its check in `test/ipc-supervisor.test.js`. Task 5 keeps the rest.
- **Task 4, the `--front` pass starts without `--demo`:** demo mode always serves on Deno (`server.js` `ENGINE`), so a
  demo start would never put the gateway in front. The pass uses its own throwaway data folder and port, with
  `ZAAFPULSE_AUTH=none` and a HOME that holds no logs.
- **Task 4, the schema count:** the smoke's doctor check expected `v1, 20 schemas`; the tree has 21 since P5
  (`egress.event`), so the first pass already failed. It now counts `ipc/v1/*.schema.json` in the source folder.
- **Task 6, the physical prefix:** `install.sh` resolves the prefix with `pwd -P` before it writes the launcher. Deno
  compares the gateway's folder found on `PATH` with the path the binary was started by, as typed: on macOS a prefix
  under `/tmp` (a link to `/private/tmp`) started through `/private/tmp/...` reads "not allowed" in `doctor`. With one
  physical path in both the `PATH` entry and the `exec` line, the local proof reads "found, allowed".
- **Task 6, `--uninstall` keeps a data folder inside the prefix:** v1 ran `rm -rf $PREFIX`, but on Linux the user's data
  folder is `~/.local/share/zaafpulse/data`, inside the default prefix. The v2 uninstall removes only what the installers
  put there (`app`, `node`, `bin`, `bin.new`, `rollback`, `rollback.new`, `rollforward`, and the v1 `app.new`/`app.old`)
  and then the prefix if it is empty. `--purge` is unchanged. `--uninstall` and `--rollback` also honour `--no-service`
  (v1's uninstall always ran `service uninstall`), so the tests can run them without a service command.
- **Task 6, small additions:** `--rollback` also copies the launcher it replaces to `rollforward/launcher`, so the
  replaced install can be brought back by hand. `--source` and `--version main` stop with a message (v2 installs release
  packages only). `--help` prints the header comment up to the first non-comment line, so it needs no line numbers;
  piped into `sh` it prints nothing. shellcheck is not installed here; the test also passes with `SHELL_UNDER_TEST=/bin/dash`.
- **Task 7, what a Windows generation holds:** `install.ps1` moves every file of `bin\` it knows (`zaafpulse.exe`,
  `zaafpulse-gw.exe`, `zaafpulse.cmd`, `zaafpulse-open.vbs`, `LICENSE`) plus `app` and `node` into `rollback\`, for v1
  and v2 alike, so a v2 → v2 rollback also gets its Start menu opener back. The user `PATH` holds the bin folder once,
  spelled exactly as `$BinDir` (the string the installer runs the binary by): an entry that differs only in letter case
  or a trailing `\` is replaced in place, since Deno's PATH match is case-sensitive. The unpacked `.exe` files get
  `Unblock-File` (the Windows side of clearing `com.apple.quarantine`). `-Uninstall` and `-Rollback` honour
  `-NoService`, as in `install.sh`. Not run here (no PowerShell on this machine); the first run is the dry run's
  install-test (Task 10).
- **Task 7, for the docs (Task 11):** on Windows the v1.1.x installer run after v2 writes `zaafpulse.cmd` into the same
  `bin\` that holds `zaafpulse.exe`, and `.EXE` comes before `.CMD` in `PATHEXT`, so the leftover `zaafpulse.exe` is not
  inert there: the docs must say to delete `bin\zaafpulse.exe` and `bin\zaafpulse-gw.exe` after that second way of
  rolling back (Decision 11). `install.ps1` itself treats a v1 `app\` as the current install in that case and stops its
  service through `zaafpulse.cmd`.
- **Tasks 6–7, review fixes:** both installers run the staged binary's `version` before they stop or move the old
  install, and stop with "nothing was changed" when it fails. `rollback.new` gets its `VERSION` before anything moves
  into it; a later run that finds a `rollback.new` with `VERSION` and no install in place finishes the move and keeps it
  as `rollback`, and otherwise moves its folders back and removes it. A successful install removes `rollforward/`.
  `install.sh --rollback` is gated on `rollback/VERSION`; a generation saved without a launcher gets a fresh v2 wrapper
  (v2) or, for v1, the stale wrapper is removed with a warning that names the node command. `install.ps1` ends what
  still runs from `$Root` (an executable under it, or v1's node running `app\bin\zaafpulse.js`) after each service
  stop, waiting up to 10 s, but not under `-NoService`, which leaves running processes alone as it leaves the service
  alone. `lib/service.js` finds a Windows server by PowerShell `-match` patterns (`WIN_SERVER_RE`, `WIN_START_RE`,
  exported for the test) that accept `start` bare or quoted, with `\x22` for the quote so none crosses the
  powershell.exe command line. `install.ps1` is still not parsed or run here (no PowerShell on this machine).
- **Task 8, what each version writes:** v1 changes `userName` through `POST /api/config`; v2 changes the audit retention
  through `POST /api/governance/settings` (`config.json` `governance.audit.retentionMonths`, a key v1 did not write).
  A v2 change of a v1 key would make the "every v1 key unchanged" check fail by design. The data folder starts with a
  `config.json` of `{ "pricingSync": { "enabled": false } }` so no run downloads the price list; it is one of the v1
  keys compared. The spec's "sign-in setting" (Decision 12, step 2) is the `ZAAFPULSE_AUTH=none` every run uses: no
  password is set, since the API steps need no sign-in. The v1 usage check reads `GET /api/gateway` `usage.totals`
  (requests, errors 0, tokens and a cost above 0; the fake answers `gpt-4o-mini`, which has a list price), and the
  key, the provider and the budget are read back on the last v1 run. `test/upgrade-check.test.js` covers the argument
  parsing and the config compare only; the proof is the script run.
