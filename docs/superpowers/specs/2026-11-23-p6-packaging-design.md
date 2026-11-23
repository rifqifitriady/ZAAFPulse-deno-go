# P6: packaging and release: design

Date: 2026-11-23. Repo `ZAAFPulse-deno-go` (public). Branch `migrate/p6-packaging`, cut from `migrate/p5-egress` at
`c41a1a1` (P5 is in PR #16; the branch merges cleanly on top of it). Documents only: nothing is implemented here.

Inputs:
- Program plan `docs/migration/2026-11-10-deno-go-migration-plan.md`: §5 P6, §4 M6 (download size per platform, both
  binaries, ≤ the v1.1.0 size), M8 (6/6 targets built and smoke-tested in CI, `windows-11-arm` included), M12 (v2 opens
  a v1.1 data folder unchanged; a rollback to v1.1.x leaves it readable), §8 (two processes on Windows: sign both, one
  installer), §11.1 (v2 ships from this repo), §11.3 (no Windows certificate, no Apple Developer ID: unsigned on
  Windows, ad-hoc signed on macOS; integrity from SHA256SUMS and attestations), §11.5 (the repo is public: no
  authenticated install).
- P2 (`deno compile` with the `cli` permission set baked in, `scripts/build-deno.js`, the six native smoke tests in the
  `build-deno` CI job, `lib/permissions.js` `selfCommand`/`runScope`, `lib/service.js` `serviceEnv`), P3 (IPC v1, the
  gateway exits when its stdin closes; `hello` carries `gwVersion`), P4a (D7: in a single-file build the gateway binary
  is looked for next to the executable, `lib/front/bin.js`; the supervisor `lib/ipc/supervisor.js` restarts with
  back-off 1 s doubling to 30 s, reset after 60 s ready, and falls back after 5 restarts in 60 s; the Overview chip
  `public/topologyview.js topoGatewayChip`), P5 results (open questions Q1 and Q2, decided here).
- The v1.1.0 release of the public ZAAFPulse repo: seven binaries (Node SEA, 84–128 MB), a source package and
  `SHA256SUMS.txt`; its `install.sh`/`install.ps1` install the source package and run it on Node (downloading Node when
  there is none), under `~/.local/share/zaafpulse/{app,node}` or `%LOCALAPPDATA%\ZAAFPulse\{app,node}`.

All Deno work follows the `deno` and `migrate-to-deno` skills; the Go side follows `go-stack-defaults` (standard
library only). The owner's standing instruction is to follow the recommended option: every choice below is
"Decision N (recommended, pending owner)". Nothing in P6 publishes a release, creates a tag or changes a repo setting:
those steps are listed for the owner in the results document.

## What exists, and the gaps P6 closes

| Need | Today | Gap |
|---|---|---|
| Two binaries per target | `deno task build` makes `zaafpulse-<v>-<target>`; the gateway is built only in tests (`go -C gw build`) | No release build of `zaafpulse-gw`, no version stamp (`const version = "0.4.0-front"`), no package holding both |
| The compiled binary starts the gateway | `lib/front/bin.js` looks next to `process.execPath` | **Measured 2026-11-23:** the baked `cli` set has no grant for it. Deno grants an `--allow-run=<name>` only through `PATH` at start, so even with the name added a compiled binary can run its sibling only when that folder is on `PATH` (a scratch binary with `--allow-run=helper`: refused off `PATH`, runs on it; a symlinked launch reports the link's path as `execPath` on macOS) |
| Supervision with back-off | `lib/ipc/supervisor.js` (P4a) | None for the policy; the status lacks the gateway's version and its health details |
| Health on the Overview | Chip "Gateway: Go (front) · ready/restarting/fallback" | No version, no health age, no reason the binary could not start |
| Release workflow | P2 state: builds and smoke-tests, publishes nothing | Packages, sums over everything, attestations, publish on a tag only, a dry run |
| Installers | v1: Node + source package from the public ZAAFPulse repo | Install both binaries from this repo's release; upgrade over v1.x; rollback |
| Install docs | Single-file section for v1 binaries | SmartScreen, Gatekeeper, SHA256SUMS and `gh attestation verify` for two binaries |

## Decisions

### Decision 1 (recommended, pending owner): one archive per target holds both binaries

The release assets are:
- `zaafpulse-<v>-<target>.tar.gz` for `darwin-arm64`, `darwin-x64`, `linux-x64`, `linux-arm64`, and
  `zaafpulse-<v>-<target>.zip` for `win-x64`, `win-arm64` (the v1 target names, which the installers pick by). Each holds
  one folder `zaafpulse-<v>-<target>/` with `zaafpulse[.exe]`, `zaafpulse-gw[.exe]` and `LICENSE`.
- `install.sh` and `install.ps1` (Decision 9), so an install is pinned to the release it came from.
- `SHA256SUMS.txt` over every other asset.

There are no loose per-binary assets. M6 is judged on the archive, which is the download. Measured on this machine
(darwin-arm64, 2026-11-23): `zaafpulse` 72.5 MB plus `zaafpulse-gw` 9.4 MB (stripped) pack to **34.1 MB** with
`tar -czf`, against 122.9 MB for v1.1.0. Two loose binaries would fail M6 on `win-arm64` (P2 measured 77 MB for
`zaafpulse` alone against v1.1.0's 83.7 MB, plus about 9 MB of gateway). Zip is used on Windows because
`Expand-Archive` reads it with no extra tool.

Rejected: loose binaries (fails M6 on win-arm64, and two downloads to keep in step); one archive for all targets
(a 200 MB download).

### Decision 2 (recommended, pending owner): the gateway build

- `zaafpulse-gw` is built with `CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags "-s -w -X main.version=<v>"`.
  `<v>` is the `package.json` version, the same as the control plane's.
- `version` in `gw/cmd/zaafpulse-gw/main.go` becomes a `var`, `"dev"` by default. `zaafpulse-gw -version` prints it and
  exits 0. That is the one Go change, and it touches no IPC message: `hello.gwVersion` already carries the version.
- The gateway is pure Go, so it cross-compiles from any OS. In CI each target is still built on its own native runner,
  so the smoke test there runs the exact file that ships.

### Decision 3 (recommended, pending owner): `deno task build` makes the package

`scripts/build-deno.js` gains `--package`. It builds `zaafpulse` (unchanged), builds `zaafpulse-gw` for the same target
(Decision 2, `GOOS`/`GOARCH` from the target triple), and writes the archive into `dist/`. `SHA256SUMS.txt` lists the
archives, plus the loose files when `--package` is not given (a local build keeps today's output).
- tar.gz is written with `tar -czf`. The zip is written with `tar -a -cf` (bsdtar: macOS, Windows 10+). With GNU tar the
  build stops with a clear message, which can happen only when a Windows target is built on Linux.
- The build stays a committed-tree build (`git archive HEAD`), and `gw/` joins the archived paths.
- Go is found on `PATH`. The task's permissions grow by `go` in `--allow-run`, and by the Go environment names
  (`HOME`, `GOCACHE`, `GOPATH`, `GOROOT`, `GOTOOLCHAIN`, `GOFLAGS`, `LOCALAPPDATA`, `USERPROFILE`, `TMPDIR`, `TEMP`,
  `TMP`, `SystemRoot`) in `--allow-env`. They are passed to the child explicitly, never `-A`.

### Decision 4 (recommended, pending owner): how the compiled binary may start its gateway

Deno resolves a run grant by name through `PATH` at start, so the binary's folder must be on `PATH` when it starts.
- The `cli` set in `deno.json` names `zaafpulse-gw` in `run` (with its WHY entry in `lib/permissions.js`). The name
  grants exactly the first `zaafpulse-gw` on `PATH`.
- Every launch the product controls puts the install folder first on `PATH`:
  - the `install.sh` launcher (`~/.local/bin/zaafpulse`) is a two-line wrapper: `PATH="<prefix>/bin:$PATH" exec
    "<prefix>/bin/zaafpulse" "$@"`;
  - on Windows the binaries live in `%LOCALAPPDATA%\ZAAFPulse\bin`, which is already on the user `PATH`;
  - `lib/service.js serviceEnv` puts `dirname(process.execPath)` first in the service `PATH` when the build is a single
    file and a `zaafpulse-gw` sits there.
- The gateway path the supervisor spawns stays `dirname(execPath)/zaafpulse-gw` (P4a D7). When Deno refuses it (the
  folder is not first on `PATH`, or another `zaafpulse-gw` comes earlier), the front falls back as for any fatal start.
  The reason it gives is specific: *"zaafpulse-gw is next to zaafpulse but not allowed to run: put its folder first on
  PATH (the installer's `zaafpulse` command does)"*. `doctor` prints the same check.
- The gateway runs only with `gateway.engine` `"front"` (P4a). The default stays `"js"` until P7, so a binary started
  from an unpacked archive outside `PATH` works as before; it only cannot put the gateway in front.

Rejected:
- an unscoped `run` grant in the binary (breaks M9's run allow-list);
- the binary re-executing itself with a fixed `PATH` (it would need a run grant on itself, the same problem);
- an absolute path baked at compile time (the install folder is not known then).

### Decision 5 (recommended, pending owner): supervision policy unchanged, status gains version and health

The P4a policy stays: back-off 1 s doubling to 30 s, reset after 60 s ready, kill after the grace period, fallback after
5 restarts in 60 s or a fatal exit, and only a ZAAFPulse restart tries again after a crash loop.

`GET /api/gateway/status` gains:
- `gwVersion` (from `hello`) and `version` (the control plane's);
- `versionMismatch`, true when both are set, neither is `dev`, and they differ;
- `binary: { path, found, allowed }`, where `allowed` is Deno's `permissions.querySync({ name: 'run', command })`, or
  `true` on Node;
- `healthAgeMs`, the age of `lastHealth`.

A mismatch warns and does not refuse: the protocol version in `hello` already decides compatibility, and a refusal
would leave the main port dark.

### Decision 6 (recommended, pending owner): the health check on the Overview

The topology card's chip (`topoGatewayChip`) keeps its three states and adds:
- **ready** shows `· v<gwVersion>`. Its tooltip gives uptime, in-flight calls, outbox depth, snapshot age, restarts and
  the last failure;
- **amber "ready · stale"** when `healthAgeMs` > 5 s, since health arrives every second;
- **amber "version differs"** on `versionMismatch`;
- the **fallback** tooltip carries the Decision 4 reason when the binary was missing or refused.

The chip is still asked for only by the owner, and it is still painted with text and attributes only, never HTML.

### Decision 7 (recommended, pending owner): signing

- **macOS:** ad-hoc signed. `deno compile` and the Go linker both sign ad-hoc. The macOS build job runs
  `codesign --verify --strict` on both files and fails when either is unsigned.
- **Windows:** unsigned (§11.3). The two-process risk (§8) is met with one installer, one archive and integrity from
  `SHA256SUMS.txt` and attestations. A Windows certificate or an Apple Developer ID would be an owner purchase and is not
  asked for (§11.3 already decided it).
- **Linux:** no signing.

### Decision 8 (recommended, pending owner): attestations

The publish job runs `actions/attest-build-provenance` (pinned SHA, as v1) over every archive, every binary inside them,
`install.sh`, `install.ps1` and `SHA256SUMS.txt`. A user checks with:

```
gh attestation verify <file> --repo rifqifitriady/ZAAFPulse-deno-go
```

A dry run makes no attestation: an attestation is a public record in the repo's store, so it is a publish.

### Decision 9 (recommended, pending owner): the v2 installers

`install.sh` and `install.ps1` are rewritten for the two binaries. They keep their options, behaviour and messages where
those still apply.
- **Source:** `REPO=rifqifitriady/ZAAFPulse-deno-go`. The latest release (`releases/latest`), or `--version vX.Y.Z`.
  Install with `curl -fsSL https://github.com/rifqifitriady/ZAAFPulse-deno-go/releases/latest/download/install.sh | sh`
  (the asset, Decision 1), and the same for `install.ps1`.
- **Verify:** download `SHA256SUMS.txt` and the archive, and compare. A mismatch or a missing sum installs nothing.
  `--verify-attestation` (`-VerifyAttestation`) also runs `gh attestation verify`, and fails when `gh` is missing.
- **Layout:**
  - macOS and Linux, as the user: `~/.local/share/zaafpulse/bin/{zaafpulse,zaafpulse-gw}`, with the launcher wrapper in
    `~/.local/bin/zaafpulse` (Decision 4);
  - Linux as root: `/opt/zaafpulse/bin`, with the launcher in `/usr/local/bin`;
  - Windows: `%LOCALAPPDATA%\ZAAFPulse\bin\{zaafpulse.exe,zaafpulse-gw.exe}`.
  - The data folders are unchanged from v1.
- **Swap:** the new files are unpacked into `bin.new`, then renamed into place. On macOS `com.apple.quarantine` is
  cleared on both files; a `curl` download carries none, but a browser download put into `--archive` does.
- **Local archive:** `--archive FILE [--sums FILE]` (`-Archive`, `-Sums`) installs a local package; CI and offline
  installs use it. `--source DIR` is removed, because a source tree needs Deno and Go to build.
- **Unsupported platforms:** no v2 binary exists for musl/Alpine, armv7l, ppc64le, s390x or 32-bit Windows. The
  installer stops there and names the v1.x installer, which still serves them.
- **No Node.js:** neither installer downloads Node.js any more.
- **Upgrade and rollback:** Decisions 10 and 11.

### Decision 10 (recommended, pending owner): upgrade from v1.x and from an earlier v2

1. The installer finds the current install: v1 has `<prefix>/app` (and maybe `<prefix>/node`), with a launcher that
   runs `node`; v2 has `<prefix>/bin/zaafpulse`.
2. It stops the running service through the **old** launcher (`zaafpulse service uninstall`, with `--system` for a root
   install).
3. It moves the old generation into `<prefix>/rollback/`, replacing any earlier one. One generation is kept: v1's
   `app`, `node` and launcher, or v2's `bin` and launcher. It records the old version in `<prefix>/rollback/VERSION`.
4. It installs v2 and the service through the new launcher.

The data folder is never touched. What M12 needs, and how it is proved, is in Decision 12.

### Decision 11 (recommended, pending owner): rollback

`install.sh --rollback` (`-Rollback`):
1. stops the v2 service;
2. moves the current generation aside, and moves `<prefix>/rollback/` back into place;
3. reinstalls the service through the restored launcher.

When nothing is saved it says so and changes nothing. Running the v1.1.x installer from the public repo
(`--version v1.1.0`) also works, and the docs give it as the second way: the v2 layout does not get in its way. Its
`app/` and launcher win, and the leftover `bin/` is inert; the docs say to delete it.

### Decision 12 (recommended, pending owner): the M12 proof

`scripts/upgrade-check.js` runs on Node and is driven by CI and by hand:
1. It downloads the v1.1.0 source package from the public release and checks it against that release's
   `SHA256SUMS.txt`, or it takes a folder given with `--v1 DIR`.
2. It runs v1.1.0 (`node bin/zaafpulse.js start`) on a fresh data folder, with the auth set to none and a local fake
   provider. It writes the kinds of data the dashboard keeps: an API key, a provider, a call through the gateway (a
   usage line and its cost), a budget, a settings change (an audit entry), and the sign-in setting.
3. It stops v1, then runs v2 (the built binary, or `deno run` with the `cli` set) on the same folder, with
   `gateway.engine` `"front"` and the gateway next to it. v2 makes a call of its own and changes a setting.
4. It stops v2 and runs v1.1.0 again. Then it checks four things:
   - the dashboard answers;
   - the audit chain verifies;
   - the usage and cost totals include both calls;
   - every `config.json` key v1 wrote is unchanged.

`.data` files that only v2 writes (`egress.jsonl`, `gateway-shadow.jsonl`, the IPC marks) must not break v1. The check
proves that by v1 starting and verifying. Kills only its own processes; data under a temp folder.

### Decision 13 (recommended, pending owner): workflows

- **`.github/workflows/build.yml`** (new, `workflow_call` only): the six-target matrix of today's `build-deno` job, on the
  same native runners. Each job:
  1. runs `deno task build --package --target <triple>`;
  2. on macOS, runs `codesign --verify` (Decision 7);
  3. smoke-tests the unpacked files: `scripts/smoke-binary.js` as today, plus a new `--front` pass. That pass writes
     `gateway.engine: "front"` into the throwaway data folder and puts the unpacked folder first on `PATH`. It checks
     that `/api/gateway/status` is `ready` with `gwVersion` equal to the package version, and that the main port is
     free after a stop;
  4. uploads the archive and its sum.

  The Go-front smoke test of today (`scripts/smoke-front.js`, `deno run` + a native `go build`) stays.
- **`ci.yml`:** `build-deno` becomes a call of `build.yml`, with the same full-scope condition. A new input `skip-build`
  (default false) lets `release.yml` run the suites without building twice.
- **`release.yml`:**
  - **On a `v*` tag:**
    1. checks the tag equals `v<package.json version>`;
    2. `test` (`ci.yml` with `skip-build: true`), then `build` (`build.yml`), then `install-test` (Decision 14);
    3. `publish`, the only job with `contents: write`, `id-token: write` and `attestations: write`. It assembles
       `dist/`, writes `SHA256SUMS.txt`, attests (Decision 8), and runs `gh release create "$GITHUB_REF_NAME" dist/*
       --verify-tag --title … --generate-notes`. `--verify-tag` means the job never creates a tag.
  - **`workflow_dispatch` is always a dry run.** It runs `build`, then `install-test`, then `assemble`: the same `dist/`
    and `SHA256SUMS.txt`, uploaded as the artifact `release-dry-run`. There is no `test` unless the `tests` input is
    true (default false, owner CI policy), no attestation, no release and no tag. The `publish` job's condition is
    `startsWith(github.ref, 'refs/tags/v')`, so a dispatch can never reach it, whatever branch it runs on.
- Every action stays pinned by commit SHA (the v1 pins are reused), and `persist-credentials: false` is set
  everywhere.

### Decision 14 (recommended, pending owner): the install test in CI, the exit evidence before a release

`install-test` runs on the same six native runners as the build, from that run's artifacts (`--archive`, `--sums`).
1. **Clean install:**
   1. install into a temp `HOME` and prefix;
   2. check that `zaafpulse version` prints the package version and `zaafpulse doctor` passes;
   3. check that `zaafpulse start` (in the background, front mode, through the launcher) answers `/login.html` and has
      the gateway `ready`;
   4. stop it.

   The service manager is exercised only where the runner has a session for it (launchd on macOS, the Startup launcher
   on Windows). Linux runners have no user systemd session, so there `--no-service` plus a background start is the
   check. Every service test runs only on a CI runner, never on a developer's machine: the launchd label is the owner's
   live one.
2. **Upgrade from v1.1.0:**
   1. run the v1.1.0 installer from the public repo at its tag
      (`https://raw.githubusercontent.com/rifqifitriady/ZAAFPulse/v1.1.0/install.sh`, `.ps1` likewise) with
      `--no-service` into the same temp prefix;
   2. start it and write a marker setting;
   3. run the v2 installer from the artifacts;
   4. check that v2 starts, sees the marker, and that `rollback/VERSION` says `v1.1.0`;
   5. run `--rollback`, and check that v1.1.0 starts and still sees the marker.
3. **M12:** `scripts/upgrade-check.js` runs on the Linux and Windows x64 runners, not on all six (it takes a few
   minutes; the data format has no OS branch).

The exit's "from the public release" leg cannot run before a release exists. It is a manual step for the owner after
the first tag. The results document lists the exact commands.

### Decision 15 (recommended, pending owner): P5 Q1, no narrow `--allow-net` baked into the binary

The single-file binary keeps the `cli` set's net grant (allow all, deny the metadata addresses). It does not have the
lock-down's loopback-only grant baked in.
- A baked grant cannot follow `egress.lockdown`, the listen host or the gate: those are known only at run time, and a
  compiled binary's permissions are fixed at compile time. Baking it in would make every outbound class fail before its
  gate passes.
- The client lock (`lib/egress.js`, `EGRESS_UNAVAILABLE`) still holds in the binary. The `network_egress` control
  already says the binary is locked in the client only.
- M9's net part is therefore met for the `deno run` launch (the service from a source install, and the preview) and
  documented as client-enforced for the binary.
- Revisit in P7, together with Q3, if the owner wants a second compiled entry that is locked down.

### Decision 16 (recommended, pending owner): P5 Q2, the stdio MCP server's grant is not narrowed

The `mcp-stdio` set keeps its net grant. That server is started by an AI client, not by ZAAFPulse, so its launch line
belongs to the client's config (`scripts/register-mcp.js`). It reaches the outside through the dashboard's egress
endpoint when one runs (P5 discovery file), and directly otherwise.

Narrowing it would break the connector tools whenever the dashboard is down. That is a change of behaviour, and the
owner has not asked for it.

### Decision 17 (recommended, pending owner): what P6 leaves alone

- **No in-app update check.** None exists in v1.x. Running the installer again is the update.
- **The Docker image stays on Node** (`node:24-alpine`) until P7. An Alpine image has no glibc for the Deno binary, so
  moving it needs its own decision.
- **`package.json` stays at `1.1.0`.** The bump to `2.0.0` and the first tag are P7 and the owner's (§5 P7). A dry run
  builds `1.1.0`-named packages, and that is fine.
- **The repo settings are untouched.** The tag ruleset and immutable releases (`docs/release-hardening.md`) are off on
  this repo today. Turning them on is an owner step before the first tag; the commands are in the results document.

### Decision 18 (recommended, pending owner): measurements

- **M6** is exact and needs no quiet machine: the archive sizes in the dry run, against v1.1.0's asset sizes, per
  target.
- **M8** is the dry run's six green build-and-smoke jobs and six green install tests.
- **M7** (start-up to both processes healthy) is measured by the smoke step on runners for information only. The
  quiet-machine figure joins the program's deferred list, marked **not quiet-confirmed**.

## Slices

Each slice merges on its own, in this order.

| Slice | Scope | Proof |
|---|---|---|
| **P6a** build and package | Decisions 1–4: the gateway version (var, `-version`), `build-deno.js --package` (both binaries, archives, sums), `zaafpulse-gw` in the `cli` set, `serviceEnv` `PATH`, the refused-binary reason, `doctor`'s gateway line, `smoke-binary.js --front` | Go tests (`-race`), `test/build-deno.test.js`, `test/permissions.test.js`, `test/service-path.test.js`, a local `deno task build --package` and `--front` smoke of the host target |
| **P6b** health on the Overview | Decisions 5–6: status fields, the chip | `test/topology-ui.test.js`, a front status test, the preview |
| **P6c** installers | Decisions 9–11: `install.sh`, `install.ps1` (clean, upgrade over v1, rollback, `--archive`) | `test/install-sh.test.js` on macOS/Linux (temp `HOME`, `--no-service`, fake v1 layout); `install.ps1` in CI only (no PowerShell here) |
| **P6d** M12 | Decision 12: `scripts/upgrade-check.js` | a local run against the v1.1.0 release package |
| **P6e** workflows | Decisions 7, 8, 13, 14: `build.yml`, `ci.yml`, `release.yml`, the install test | `actionlint`-style review by hand (no `act` here), then **one** dry-run dispatch of `release.yml` on the branch near the end |
| **P6f** docs and results | `docs/install.md`, README install table, `docs/release-hardening.md` for this repo, the results document, the program plan | review |

## Owner questions

None blocks a slice. The only owner-gated items are the manual release steps (the version bump, the repo settings, the
tag, then the install from the public release on six machines), and they are listed in the results document.
