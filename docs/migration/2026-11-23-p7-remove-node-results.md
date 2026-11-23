# P7 remove-node: results

Date: 2026-11-23. Branch `migrate/p7-remove-node`, cut from `migrate/deno-go` at `ccc834e`. Design:
the p7-remove-node design. **Do not merge before gate G3 is met and the owner says
so.** No pull request is open; CI was not run (owner CI policy); everything below was run locally.

## What the branch holds

| Commit | What | Changes behaviour? |
|---|---|---|
| design | the inventory of every Node-only and js-only path, Decisions 1–8, Owner questions 1–5 | no (a document) |
| Decision 3 | `isSingleFile()` in `lib/platform.js` and `lib/front/bin.js` no longer asks `node:sea`; `test/runtime.test.js` updated and pins it | no: no build of this tree makes a Node SEA since P2, so the check was always `false` under Node |
| results | this document | no |

What the branch does **not** hold, on purpose:

- **No deletion of the JS gateway hot path** (design Decision 1). At G3 the JS gateway still answers every request the
  Go engine hands back (every remote client, browsers, agents and collaborations, refresh-token providers, every `/mcp`
  method but `tools/call`, invalid bodies), the paths with no Go engine (`/v1/models`, `/a2a`, skills, agents), the
  fallback when the gateway cannot run, and the two v2 rollbacks of the switch runbook §5 (one class back to `js`; front
  mode off in one command). Deleting it makes those rollbacks impossible, which is the owner's call (Owner question 2).
- **No cut-over commit** flipping the engine defaults (design Decision 2). The cut-over is the owner's live machine
  moving class by class in `config.json` (runbook §4); a code default of front mode would put the Docker image and
  source runs without a built gateway into `fallback` with the alert raised.
- **No removal of Node as a source runtime, the Node CI legs, `engines.node`, the Node Docker image or the macOS
  autostart scripts** (Decisions 4–6, Owner questions 1, 3, 5): each is live, documented, and in the case of the
  release `install-test` the proof of the rollback to v1.1.x.

## Merge precondition

The branch is safe to merge on its own at any time (it changes no behaviour), but it is the P7 "remove the Node-only
code paths" step, so it waits for the same point as the step itself:

1. **G3 met on the live machine**: every gateway route class on `go` (`GET /api/gateway/status` → `engines` shows
   `go` and `notes` is empty for `chat`, `chat-stream`, `chat-anthropic`, `chat-anthropic-stream`, `messages`,
   `messages-stream`, `messages-count`, `responses`, `responses-stream`, `gemini`, `gemini-stream`, `gemini-count`,
   `embeddings`, `mcp`, `token-saver`, `routes`, and `provider-auth` if an OAuth2 or service-account provider is
   configured); the shadow diff at 0 (`grep -c '"outcome":"diff"' .data/gateway-shadow.jsonl` not rising since the
   last class moved).
2. **M3 and M4 met** at the final quiet-machine step (the deferred benchmarks: M3 and M4 on the external tier with the
   default guardrail policy, the `/v1/messages` translated path with `ZP_BENCH_FORMAT=anthropic`, the token saver with
   `ZP_BENCH_TOOL_RESULT=65536`, and M3 through the egress endpoint).
3. **The owner's answers to Owner questions 1–5** of the design, in particular question 2 (G3's "JS hot path deleted").
   If the answer is "delete it", the deletion is a follow-up program (design question 2), not a commit on this branch.

## Checklist before merging

1. `zaafpulse doctor` on the live machine: gateway `found, allowed, <version>`, `Gateway IPC v1`, server answering,
   service running, MCP entries all start a program that exists.
2. Runbook §4 steps 0–2 done: front mode, every class shadowed and then moved, in the runbook's order; the chip green;
   `grep -c '"ipc"' .data/gateway-usage.jsonl` rising.
3. `shadow.<class>.handoffs` in `GET /api/gateway/status` read and noted for each class: the hand-offs are the requests
   the JS gateway still serves after the cut-over, and the number the owner needs for Owner question 2.
4. Runbook §3 egress steps as far as the owner wants them for v2.0.0 (not a G3 condition).
5. The deferred benchmarks of the precondition, recorded in `bench/results/`.
6. Rebase or merge `migrate/deno-go` into this branch, then locally: `npm test` and the Deno suite, the contract suite
   in `go` mode on Node and Deno, `go test -race ./...`, `gofmt -l gw`, `go vet ./...` (and `GOOS=windows`), `deno task
   typecheck`.

## The rollbacks after the merge (unchanged by this branch)

- One class back to js (runbook §5): `"engines": { "<class>": "js" }`, picked up within a minute.
- Front mode off, in one command (runbook §5):

  ```sh
  D="<data folder>"; ( umask 077; cp "$D/config.json" "$D/config.json.pre-rollback-$(date +%Y%m%d-%H%M%S)" && jq '.gateway.engine = "js" | .egress.lockdown = false' "$D/config.json" > "$D/config.json.tmp" && mv "$D/config.json.tmp" "$D/config.json" ) && zaafpulse service restart
  ```

- Back to the Node line (v1.1.x): `install.sh --rollback` (`install.ps1 -Rollback` on Windows), which puts the v1.1.x
  install the upgrade kept back in place on the same data folder; then `zaafpulse register-mcp <client>` with v1.

All three stay possible because the JS gateway (the first two) and the installers' `--rollback` with the Node-based
M12 proof (the third) are kept.

## Proposed wording for G3 (Decision 8, for the owner's plan)

"Every gateway route class is on `go` on the live machine and the JS gateway answers only hand-offs, the paths with no
Go engine, the fallback and the rollbacks; shadow diff 0; M3 and M4 met." The plan (`2026-11-10-deno-go-migration-plan.md`)
is not edited on this branch.

## Local results (macOS, Apple M4; node v24.16.0, deno 2.9.8, go 1.27.3)

| Check | Result |
|---|---|
| Touched tests (`runtime`, `front`, `platform`, `service-path`, `permissions`, `evals-claude-cli`) on Node / Deno | 116/116 / 116/116 |
| Full suite on Node (`node --test test/*.test.js`) | 3211 pass, 0 fail, 3 skipped (3214) |
| Full suite on Deno (`deno test -P=test test/*.test.js`) | 3211 passed, 0 failed, 3 ignored (5 min 4 s) |
| Contract suite, `ZP_ENGINE=go`, Node | 222 pass, 0 fail, 5 skipped (227) |
| Contract suite, `ZP_ENGINE=go`, Deno | 223 passed, 0 failed, 4 ignored |
| `go test -race ./...` (gw) | ok, all 10 packages |
| `gofmt -l gw`, `go vet ./...`, `GOOS=windows go vet ./...` | clean, ok, ok |
| `deno task typecheck` | exit 0 |
