# P7: removing the Node-only code paths: inventory and design

Date: 2026-11-23. Repo `ZAAFPulse-deno-go` (public). Branch `migrate/p7-remove-node`, cut from `migrate/deno-go` at
`ccc834e`. This branch is the prepared "remove the Node-only code paths" step of P7 (plan §5 P7, gate G3's fourth
bullet). It must not be merged until gate G3 is met and the owner says so.

Inputs:
- Plan §5 P4 gate G3: "every gateway route is on `go`; shadow diff is 0; M3 and M4 are met; the JS hot path is
  deleted." Plan §5 P7: "Release v2.0.0 from this repo, and remove the Node-only code paths."
- Plan §6 (G3, "If not met: keep the JS gateway for the slices not met; ship hybrid"), §8 (risk 1: "Dual-runtime CI
  until cut-over"), §11 decisions 4 and 7 (the cut-over "keeps the rollback to the Node line one command away").
- `docs/migration/2026-11-23-migration-note.md` ("Rolling back"; "The Docker image still runs on Node.js for now") and
  `docs/migration/2026-11-23-switch-runbook.md` §5 (the rollbacks).
- The code at `ccc834e`: `gw/front/front.go`, `gw/engine/*.go` (the hand-offs), `lib/front/index.js` (the fallback),
  `server.js`, `lib/platform.js`, `lib/front/bin.js`, `.github/workflows/*.yml`, `Dockerfile`, `package.json`,
  `scripts/`.

## 1. What "rollback one command away" means for this tree

The plan and the runbook name three rollbacks. Each needs something different to stay in the tree.

| Rollback | Command | What it runs | Needs in this tree |
|---|---|---|---|
| R1. v2 to the Node line (v1.1.x) | `install.sh --rollback` / `install.ps1 -Rollback`, or the v1.1.x installer after `zaafpulse service uninstall` | the v1.1.x install the upgrade kept in `rollback/`, or the v1.1.x release assets of the upstream repo | `install.sh`, `install.ps1` (the `--rollback` code), and the M12 proof `scripts/upgrade-check.js`, which starts v1.1.0 **on Node**. No Node code of this tree is run |
| R2. Front mode off, in v2 | runbook §5 "Everything, in one command": `gateway.engine = "js"`, `egress.lockdown = false`, restart | the Deno server answering every gateway call itself: the **JS gateway** (`lib/gateway/index.js` and what it requires) | the whole JS gateway hot path |
| R3. One class back to js, in v2 | runbook §5: `"engines": { "<class>": "js" }` | the JS gateway for that class | the whole JS gateway hot path |

The Node line itself (R1) is the v1.x release line: it lives in the upstream `rifqifitriady/ZAAFPulse` repo and as its
released assets, and in the `rollback/` folder the v2 installer keeps. Nothing in this tree's source has to keep
running on Node for R1 to work. R2 and R3, the rollbacks the runbook documents for the live switch, run on the JS
gateway in this tree; that JS gateway runs on Deno, not Node. "Node-only" and "JS hot path" are therefore two
different things, and the inventory treats them separately.

## 2. Inventory

### 2a. The JS gateway hot path

`lib/gateway/index.js` (2,691 lines) with `translate.js`, `guardrails.js`, `rtk.js`, `providers.js` (routes, provider
auth), `keys.js`, `usage.js`, `shadow.js`; plus the budget guard, pricing, audit and trace emission it calls. The Go
engine (`gw/engine`, `gw/guard`, `gw/translate`, `gw/provauth`) duplicates the paths of the seventeen classes of the
runbook. What the JS still does once every class is on `go`:

| Role | Where | Still needed at G3? |
|---|---|---|
| **Hand-off server.** The Go engine serves a request only when it can; otherwise it forwards it unchanged to the control plane, which answers with the JS gateway. The reasons in `gw/engine` (`handoff(...)`): `access: not a loopback host and client` (every remote client: server mode, Docker, a LAN client), `access: a browser or proxy header`, `model: an agent or a collaboration`, `model: mapping needs the model list`, `model: no usable target`, `model: missing`, `key: not allowed`, `body: empty or over 20 MB`, `body: not a JSON object the engine serves`, `body: messages is not an array`, `translate: the request cannot be translated`, `path: not a … call`, `method`, nine `mcp:` reasons (every `/mcp` method other than `tools/call`), `route: …` (a route the Go router cannot serve), `secrets.handoff` (a refresh-token provider), the token saver's panic guard | **Yes.** Every class can hand a request to JS, and some requests always go there (remote clients, agents and collaborations, refresh-token providers, every `/mcp` method but `tools/call`). Deleting the JS hot path turns each of these into a failure |
| **Paths with no Go engine** | `GET /v1/models`, `/a2a`, `skills`, `agents` (runbook §1) | **Yes**: "stay on js whatever is set" |
| **Fallback server.** When the gateway cannot start or crash-loops, the server takes the main port and answers every call itself (`lib/front/index.js fallback`, the red chip `· fallback`) | **Yes**: "nothing goes down" (migration note) depends on it |
| **Rollback engine** R2 and R3 (§1) | runbook §5 | **Yes** while the documented rollback stays |
| **Default engine.** `gateway.engine` defaults to `"js"`; the Docker image and a source run without a built gateway always run on it | `lib/front/index.js engineOf` | **Yes** while the default is `js` |
| **Shadow compare** | `lib/gateway/shadow.js`, `lib/front/shadow-log.js` | Yes while any class may be shadowed again (runbook §5, "put it back in `gateway.shadow`") |
| **Fixture source.** The Go ports are proven against fixtures the JS writes (`scripts/fixtures/gen-*.js`, the CI `fixtures` job) | `scripts/fixtures/` | Yes while the JS exists: it is the reference the Go is held to |
| **Tool endpoint** for `/mcp tools/call` on go: the tool runs in the control plane over the internal port | `lib/front/tools.js`, `/_zp/mcp/call` | Yes, not part of the hot path (stays at any stage) |
| **IPC handlers** (config push, secrets, budget snapshot, usage, audit, trace, routing state) | `lib/front/push.js`, `lib/front/client.js`, `lib/ipc/*`, `server.js` | Yes, not part of the hot path (stays at any stage) |

What is **truly duplicate**: the code path a request takes when it is of a class on `go`, from a loopback client, not a
browser, with a model the engine resolves, a body it serves, and a provider it can authenticate. That path is not a
separate module: it is the same functions (`proxyChat`, `translate`, `checkBody`, the budget guard, `usage.record`)
that the hand-offs, the fallback, R2 and R3 call. There is no part of `lib/gateway` that only the duplicated path
reaches.

### 2b. Node-specific runtime code

| Item | Where | Status |
|---|---|---|
| Node SEA detection (`require('node:sea').isSea()`) | `lib/platform.js isSingleFile`, `lib/front/bin.js isSingleFile`, `test/runtime.test.js` | **Dead.** The SEA build (`scripts/build-single.js`, `scripts/sea-main.js`, `postject`) was removed in P2; nothing in this tree can produce a SEA, so under Node it is always `false` (and v1.1 SEA binaries carry their own code) |
| SEA build scripts, `postject` | gone since P2 | nothing left |
| `process.versions.deno` / `runtime()` branches | `bin/zaafpulse.js doctor` (Node line), `lib/evals/claude-cli.js`, `lib/permissions.js describe`, `lib/gateway/shadow.js`, `bench/*`, the tests | Live: source runs on Node are supported (README "From source: Node 18+ or Deno 2.9") |
| `lib/trace/antigravity.js` Node-version gate for `node:sqlite` | `lib/trace/antigravity.js` | Live on Node source runs |
| `package.json` `engines.node >=18`, `scripts` that start `node` (`start`, `demo`, `mcp`, `mcp:register`, `test`, `test:contract`, `gw:dev`, `media`) | `package.json` | Live: the documented source-run commands (`npm start`, `npm test`) |
| `bin/zaafpulse.js` shebang `#!/usr/bin/env node` | `bin/zaafpulse.js` | Live for `npm`/source runs; the binary is `deno compile`d from it and ignores the shebang |
| Docker image on `node:24-alpine`, `ENTRYPOINT ["node", …]`, runs as user `node` | `Dockerfile`, `compose.yaml`, README "Docker", `docs/install.md#docker` | Live: the only Docker image; it has no Go gateway and runs the JS gateway |
| macOS autostart on Node | `scripts/install-autostart.sh`, `scripts/launch.sh` (finds a `node`), `scripts/uninstall-autostart.sh` | Live and documented (README, `docs/install.md`): the only way to get the Chrome opener (`com.zaaf.aipulse.opener`); `com.zaaf.aipulse.server` is the owner's current live service label |
| Node-only dev scripts (`#!/usr/bin/env node`) | `scripts/bench-guard.js`, `bench-index.js`, `capture-media.js`, `eval-signals.js`, `register-mcp.js`, `upgrade-check.js`, `bench/*.js` | Live: they also run under `deno run`, and `upgrade-check.js` is R1's proof |
| CI: the Node `test` job (4 OS), Node in `contract-go-engine`, `m4-bench` (js mode), `fixtures` (Node regenerate), `guard-bench` (JS guard timing), release `install-test` (Node for v1.1.0) | `.github/workflows/ci.yml`, `release.yml` | Live: dual-runtime CI "until cut-over" (plan §8); `install-test` needs Node for R1's proof |

### 2c. The dual-runtime harness and the contract suite modes

The contract suite (`test/contract/harness.js`) takes `ZP_RUNTIME` (`node` default, `deno`) and `ZP_ENGINE`:

| Mode | What it runs | Stays? |
|---|---|---|
| `js` (default) | the suite against the server's JS gateway | Yes: it tests R2, R3, the fallback, the hand-offs and the default engine |
| `front` | the Go gateway in front, every class on js (forwarded) | Yes: step 0 of the runbook, the default of front mode |
| `shadow` | classes shadowed by the go engine; any diff fails | Yes: runbook step 1 and the re-prove path of R3 |
| `go` | classes served by the go engine | Yes: the cut-over state |
| `ZP_RUNTIME=node` | the server under Node | Yes while Node source runs are supported (Owner question 1) |

## 3. Decisions

**Decision 1 (recommended, pending owner): keep the JS gateway hot path at G3; do not delete it on this branch.**
Reason: it is not a duplicate that G3 makes unreachable. It is the hand-off server for every request the Go engine
declines (all remote clients, agents and collaborations, refresh-token providers, every `/mcp` method but
`tools/call`, `/v1/models`, `/a2a`), the fallback when the gateway cannot run, the engine of the documented rollbacks R2
and R3, and the default engine (`gateway.engine: "js"`) of the Docker image and of source runs. Deleting it would make
R2 and R3 impossible and turn the hand-offs and the fallback into failures. Risk of keeping: two implementations to
maintain; mitigated by the fixtures job and the shadow mode, which hold the Go to the JS. Precondition for any later
deletion: see Owner question 2. **This is the owner question this branch stops at** (it changes a documented rollback
and the plan's G3 wording).

**Decision 2 (recommended, pending owner): no "cut-over commit" that flips the engine defaults in code.**
The cut-over at G3 is the owner's live machine moving class by class through `config.json` (runbook §4); G3 is
measured there. Flipping the defaults in code (`gateway.engine: "front"`, every class `go`, every class shadowed so
the switch rule can still be met) would change every install that has no setting of its own: the Docker image and a
source run without a built gateway would start in `fallback` with the alert raised, and a fresh install would shadow
for a day. Risk of not flipping: a new v2 install stays on js until its owner follows the runbook, as the migration
note already says ("Out of the box v2 behaves as v1.1"). Precondition for flipping later: the Docker image ships the
gateway (Owner question 3) and the owner wants front mode as the default for new installs (Owner question 4).

**Decision 3 (recommended, pending owner): delete the Node SEA detection now.** `isSingleFile()` in
`lib/platform.js` and `lib/front/bin.js` keeps only the Deno check (`Deno.build.standalone`); under Node it returns
`false`. Reason: no build of this tree produces a Node SEA since P2, so the branch is dead code; the behaviour of every
launch this tree supports is unchanged (Node source run: `false` before and after; Deno: unchanged). Risk: none for a
supported launch; someone who hand-builds a SEA from v2 source would get the source-run paths, which is the state P2
declared ("the Node SEA build removed"). Precondition: none; valid now.

**Decision 4 (recommended, pending owner): keep Node as a supported source runtime for v2.0.0, with its CI.**
The Node `test` job, the Node legs of `contract-go-engine`, `fixtures` and `guard-bench`, `package.json` `engines` and
the `node` scripts stay. Reason: the README documents "Node 18+ or Deno 2.9" for source runs, the Docker image runs on
Node, and R1's proof (`scripts/upgrade-check.js`, release `install-test`) needs Node to start v1.1.0. Precondition for
removing them: Owner questions 1 and 3. Release `install-test`'s Node stays regardless: it is R1's proof.

**Decision 5 (recommended, pending owner): keep the Docker image on Node for v2.0.0.** Reason: moving it to Deno and
adding the gateway is a packaging change (a base image, the permission set, the gateway binary, the `node` user and
paths in the docs), not a removal; the migration note already says it "still runs on Node.js for now". Precondition:
Owner question 3.

**Decision 6 (recommended, pending owner): keep the macOS autostart scripts.** `scripts/install-autostart.sh` and
`launch.sh` start the server on Node, but they are documented and are the only way to get the Chrome opener; the
owner's live service (`com.zaaf.aipulse.server`) is installed by them. Precondition for removing: Owner question 5.

**Decision 7 (recommended, pending owner): keep every contract-suite mode** (`js`, `front`, `shadow`, `go`) and both
`ZP_RUNTIME` values. Reason: §2c; each mode tests a state the runbook or a rollback uses.

**Decision 8 (recommended, pending owner): change the plan's wording, not the code, for G3's fourth bullet.** Proposed
wording: "the JS hot path serves no class by default on the live machine: every route class is on `go`, and the JS
gateway answers only hand-offs, the paths with no Go engine, the fallback and the rollbacks". Made in the results
document as a proposal; the plan itself is the owner's and is not edited on this branch.

## 4. Owner questions

1. **Drop Node as a source runtime?** (README "Node 18+ or Deno 2.9", `package.json` `engines` and `node` scripts, the
   Node CI legs.) Recommendation: **not at v2.0.0**. Keep Node source runs through v2.0.x; drop them in a later minor
   once the Docker image is on Deno (question 3), then switch the scripts to `deno task`, remove `engines.node`, the
   Node CI legs (except release `install-test`, R1's proof) and the `runtime() === 'node'` branches.
2. **G3's "the JS hot path is deleted".** It cannot be done without losing R2 and R3, the fallback and every hand-off
   (§2a). Recommendation: **reword G3 as in Decision 8 and keep the JS gateway in v2.0.0**. If the owner wants the
   deletion later, it is its own program after G3: port the hand-off reasons that real traffic hits (remote clients,
   agents and collaborations, refresh-token providers, the other `/mcp` methods, `/v1/models`, `/a2a`), measure the
   hand-off count on the live machine (`shadow.<class>.handoffs`) at zero for a period the owner sets, replace the
   fallback with "gateway restart or fail", and replace R2/R3 with R1 only.
3. **Docker base image.** Recommendation: **keep `node:24-alpine` for v2.0.0** (Decision 5); move to a Deno image with
   the gateway binary in a later minor, with its own smoke test in CI.
4. **Front mode as the default for new installs.** Recommendation: **not at v2.0.0** (Decision 2); revisit after the
   Docker image and source runs carry the gateway.
5. **macOS autostart scripts on Node.** Recommendation: **keep for v2.0.0** (Decision 6); later, port the opener to
   `zaafpulse service install` on macOS and retire the scripts, after the owner's live service has moved to the v2
   service.

## 5. What this branch implements

- This design.
- Decision 3: the dead Node SEA detection removed, with its test updated (`test/runtime.test.js`) and a test for
  `lib/front/bin.js`.
- `docs/migration/2026-11-23-p7-remove-node-results.md`: what the branch holds, the merge precondition and checklist,
  and the one-command rollbacks.

Not implemented, by Decisions 1 and 2: no JS hot path deletion and no cut-over commit. They wait for the owner's answer
to questions 2 and 4.
