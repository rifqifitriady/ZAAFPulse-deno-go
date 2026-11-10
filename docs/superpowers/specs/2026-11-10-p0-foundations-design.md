# P0 Foundations — design

Date: 2026-11-10. Repository `ZAAFPulse-deno-go` (private), branch `migrate/p0-foundations`, cut from `migrate/deno-go` at
`f2990a3`. Program plan: `docs/migration/2026-11-10-deno-go-migration-plan.md`, §5 P0. P1 has already passed (gate G1)
and is recorded in `docs/migration/2026-11-10-p1-deno-spike-results.md`.

All Deno work follows the `deno` and `migrate-to-deno` skills (owner instruction).

## Goal

P0 builds the safety net and the measuring stick for every later phase:

1. CI that runs on migration branches, including Windows ARM and Deno.
2. A benchmark harness and a recorded **Node baseline** for metrics M3, M4, M5 and M7.
3. A **black-box gateway contract suite** that can be pointed at any engine (JS today, Go in P4).
4. **Type checking** of the gateway, cost and governance code with `deno check`.
5. **One money representation** for the IPC and Go: integer nano-USD.

Product behaviour does not change in P0. The only runtime change is a new pure helper module (`lib/money.js`) and
JSDoc comments.

## Decisions taken in this design

**D1. Type checking uses `deno check --sloppy-imports`, `tsconfig.json` and `@types/node` (dev only).**
- Tested on 2026-11-10 with Deno 2.9.8:
  - `deno check` needs `@types/node` for the `node:` types;
  - it resolves types across CommonJS `require()` only when the path has an extension, **or** with
    `--sloppy-imports` (our code uses extensionless requires such as `require('./keys')`);
  - with both in place, all three planted errors were caught: a same-file error, a cross-file error, and a wrong
    `node:fs` call.
- `@types/node` is a **devDependency holding types only**. Nothing of it ships or runs, so there are still zero
  runtime dependencies.
- The compiler options go in `tsconfig.json`, as the skill requires.

**D2. Strictness ramps up.**
- P0 runs `checkJs` with `strict: false` and `noImplicitAny: false`. It adds `// @ts-check` to every file under
  `lib/gateway/`, `lib/cost/` and `lib/governance/`, and fixes what that reports.
- Real JSDoc `@typedef`s are written for the shapes that cross the future IPC (D3, §T2).
- Strict mode per area is a later, separate step. Turning on `strict` across ~19k lines in one go would make P0 a
  rewrite.

**D3. Money in the IPC and in Go is integer nano-USD (1e-9 USD).** The on-disk format does not change in P0.
- The plan said micro-USD. The code already rounds every stored amount to **1e-8 USD** (`round8`/`usd()` in
  `ledger.js`, `budgets.js` and `preview.js`). Micro-USD (1e-6) would lose precision on tiny token costs, while
  nano-USD keeps it.
- Range: `Number.MAX_SAFE_INTEGER` nano-USD is about **US$9 million**, far above personal use. Go uses `int64`.
- The cost ledger keeps its JSONL format (floats rounded to 1e-8). Migrating stored data in P0 would put M12 at risk
  (v1.1 data must open unchanged, and rollback must work).
- Conversion happens at the IPC boundary through `lib/money.js`.
- Whether to move the ledger itself to integers is decided after P4, with measured need.

**D4. The contract suite is black-box over HTTP and starts the real `server.js`.**
- It does not call `createGateway()` in-process. It starts `server.js` as a child process, on a temp data dir with a
  seeded `config.json` and gateway key, and with a fake upstream provider. It can also target a running instance
  through `ZP_CONTRACT_URL`.
- The runtime is chosen with `ZP_RUNTIME=node|deno`. This makes the same suite the acceptance test for the Go
  gateway in P4.

**D5. Benchmarks use the Node standard library only, so they run unchanged under Node and Deno.** There is no
autocannon or similar tool.

## Tasks

### T1 — CI for the migration branches

Changes to `.github/workflows/ci.yml`:

- **Triggers:** add `push` and `pull_request` for `migrate/**` and `migrate/deno-go`. Keep `main`.
- **Matrix:**
  - add `windows-11-arm` to the existing Node test job, which then runs on ubuntu, macos, windows and
    windows-11-arm;
  - pin Node to the version in `package.json` `engines` and current CI.
- **New job `deno`** (ubuntu and macos):
  - install the pinned Deno version (`2.9.8`, from `denoland/setup-deno` pinned by commit SHA, as the existing
    actions are pinned);
  - run `deno ci`, which needs `deno.lock`. T1 creates it with `deno install`, seeded from `package-lock.json`, and
    commits it;
  - run `deno test -A --no-check test/`;
  - parse and report the pass and fail counts;
  - the job **does not block** in P0 (`continue-on-error: true`), because the 47 known failures are P2 work;
  - it uploads the summary as an artifact.
- **New job `typecheck`:** `deno check --sloppy-imports` on the three areas (T2). This job **does block**.
- **Exit:** a PR into `migrate/deno-go` shows all of these checks; the Node job is green on 4 targets, including
  windows-11-arm.

### T2 — Types: `tsconfig.json`, `deno check`, and JSDoc for the IPC shapes

- `package.json`: `devDependencies` gets `@types/node`, pinned to an exact version. `deno.lock` (from T1) is refreshed
  with `deno install`.
- `tsconfig.json`:
  - `compilerOptions`: `{ checkJs: true, allowJs: true, strict: false, noImplicitAny: false, noEmit: true }`;
  - `include`: `lib/gateway`, `lib/cost` and `lib/governance`.
- A `package.json` script `typecheck`: `deno check --sloppy-imports lib/gateway lib/cost lib/governance`. It is run
  with `deno task typecheck`. Scripts live in `package.json` because the project has one, as the skill prefers, so no
  `deno.json` is needed yet.
- Add `// @ts-check` to every `.js` file in the three areas, and fix the reported errors. A fix must not change
  behaviour. When a fix would, use a `@type` cast or `// @ts-expect-error` with a reason, and list it in the report.
- Write `lib/types.js`: JSDoc `@typedef`s only, with no runtime code, for:
  - `UsageRecord`: what the gateway reports per call;
  - `BudgetSnapshot`: what the guard enforces;
  - `AuditEvent`: what the gateway would send to the single audit writer;
  - `GatewayConfigSection`: providers, routes, keys and guardrails policy, without secrets;
  - `GuardrailOutcome`;
  - `Money` (nano-USD integer, see T3).
  The existing producers of these shapes annotate their return values with these types.
- **Exit:**
  - `deno task typecheck` is clean;
  - every file in the three areas has `// @ts-check`;
  - the typedefs are used by at least the producer of each shape;
  - the existing `npm test` still passes, with 0 failing.

### T3 — Money: `lib/money.js` (nano-USD)

Pure functions with no I/O:

| Function | Contract |
|---|---|
| `toNano(usd)` | A finite number in USD becomes a safe integer, `Math.round(usd * 1e9)`. It throws on a non-finite value or out-of-range input. |
| `fromNano(n)` | A safe integer becomes a number in USD. |
| `addNano(...ns)` | Integer sum that throws when the result leaves the safe range. |
| `round8(usd)` | The existing 1e-8 rounding, moved here. `ledger.js`, `budgets.js` and `preview.js` import it instead of each defining their own `usd()`, so the code is not duplicated three times. |

Tests:
- round trips for typical token costs;
- the `0.55 * 100` float case;
- sub-micro amounts kept, which micro-USD would lose;
- range limits.

`lib/types.js` `Money` is documented as a nano-USD integer.

**Exit:** the tests pass; the three files use the shared `round8`; the ledger output stays byte-identical for the
existing ledger tests.

### T4 — Benchmark harness and the Node baseline

Contents of `bench/`, all Node stdlib with no dependencies, runnable under Node and Deno:

- `bench/mock-provider.js`: an OpenAI-compatible upstream (`/v1/chat/completions`) with a non-streaming answer and an
  SSE stream (N chunks at a fixed interval), plus a fixed server delay (default 20 ms). Usage is reported in the
  final chunk.
- `bench/run.js`:
  1. starts the mock provider;
  2. starts `server.js` (runtime from `ZP_RUNTIME`) on a temp data dir, with the mock as its only provider and one
     gateway key;
  3. measures:
     - **startup**: spawn until the first 200 on `/login.html`;
     - **overhead non-stream**: 2,000 sequential requests through the gateway and 2,000 straight to the mock;
       overhead = p50, p95 and p99 of the paired difference;
     - **concurrency**: 50, 200 and 500 concurrent SSE streams for 10 s each, recording completed streams, errors,
       time to first byte p95 and overhead p95;
     - **RSS**: idle after 5 s, and the peak during the 500-stream step, read through `ps` on macOS and Linux;
  4. writes `bench/results/<date>-<runtime>-<host>.json`, including the git SHA, runtime version and machine info.
- `bench/README.md`: how to run it and how to compare two result files (`node bench/compare.js a.json b.json`
  prints the deltas).
- **The baseline:** the run on the owner's MacBook with Node is committed as
  `bench/results/2026-11-xx-node-<host>.json`. That sets the real numbers for M3, M4, M5 and M7, replacing the
  *(P0)* placeholders in the plan's baseline table. A Deno run of the same harness is committed next to it for
  information.
- **Exit:**
  - both result files are committed;
  - the plan's §2 table is updated with the measured values;
  - a run takes ≤ 5 minutes.

### T5 — Gateway contract suite (black-box)

- `test/contract/harness.js`:
  - starts the fake upstream (reusing `test/helpers.js` `fakeServer`, extended for Anthropic, Gemini and SSE
    shapes);
  - starts `server.js` with a seeded temp data dir and returns `{ base, key, upstream }`, or uses
    `ZP_CONTRACT_URL` and `ZP_CONTRACT_KEY`.
- Table-driven cases in `test/contract/*.test.js`, **at least 150 cases** across:

  | Area | Cases (min) |
  |---|---|
  | Formats: `/v1/chat/completions`, `/v1/messages`, `/v1/messages/count_tokens`, `/v1/responses`, `/v1/embeddings`, `/v1/models`; each × non-stream/stream where it applies × the provider types the gateway translates to | 60 |
  | Auth: missing, invalid or disabled key; model allow-lists; key mode override; rate limit | 20 |
  | Guardrails: secrets, PII and injection × off/log/redact/block; external vs local scope; the `guardrail_blocked` 400 shape | 30 |
  | Budgets: under, at and over the limit; fail closed; the refusal shape | 10 |
  | Usage records: fields, client tag, cost in nano-USD after conversion | 15 |
  | `/mcp` through the gateway: `tools/call` with a gateway key; refused content | 10 |
  | Errors: upstream 4xx/5xx passed through, timeouts, malformed JSON | 10 |

- Every case asserts on **HTTP-visible behaviour only**: status, headers that matter, body shape, the stream event
  sequence, and the usage or audit record read back through the admin API. That way the Go engine is held to the
  same contract.
- `npm run test:contract` runs the suite, and so does the `deno` job with `ZP_RUNTIME=deno`.
- **Exit:**
  - ≥ 150 cases, all green on the JS gateway under Node;
  - the run under Deno is reported, and any failures are mapped to the P1 groups;
  - suite runtime ≤ 3 minutes.

## Order and dependencies

T1 → T3 → T2 → T5 → T4.

- T1 comes first so that every later PR is checked.
- T3 comes before T2, because the money typedef is used by the types.
- T5 comes before T4, because the benchmark reuses the contract harness's server start and seeding.

## Gate G0 (exit of P0)

- [ ] CI runs on `migrate/**`. The Node job is green on 4 targets, including windows-11-arm. The `deno` and
  `typecheck` jobs are present, and `typecheck` is green.
- [ ] `deno task typecheck` is clean for the three areas. The IPC typedefs are in `lib/types.js` and used.
- [ ] `lib/money.js` is in place with tests. The duplicated `usd()` helpers are gone. Stored data is unchanged.
- [ ] The contract suite has ≥ 150 cases and is green on the Node JS gateway.
- [ ] The Node baseline for M3, M4, M5 and M7 is committed and copied into the plan.
- [ ] `npm test`: 0 failing. Product behaviour is unchanged.

## Out of scope for P0

- Running the control plane on Deno (P2).
- Fixing the 47 Deno failures (P2).
- Any Go code (P4).
- Changing the ledger's on-disk format (D3).
- Strict mode (D2).
- Frontend changes.

## Amendments from planning (2026-11-10)

These were found while writing the implementation plan (`docs/superpowers/plans/2026-11-10-p0-foundations.md`) and
verified against the running server:

1. **Budgets fail open in v1.** `lib/cost/guard.js` fails open on purpose. The only fail-closed path today is the 402
   for a budgeted key on an unpriced model. The contract suite tests the **current** behaviour. The program's design
   rule 2 (the Go gateway fails closed when the budget snapshot is stale) is a deliberate change of behaviour; the
   owner confirms it in P3.
2. **No timeout cases.** The provider timeout is a fixed 120 s, so the error cases cover a reset connection and a
   refused connection instead (both 502).
3. **The `typecheck` CI job arrives in T2**, in the commit that makes it green, not in T1.
4. **`@types/node` is added with `npm install -D -E`, then `deno install`,** so that `package-lock.json` and
   `deno.lock` agree. `deno add` alone would leave `npm ci` in `release.yml` broken.
5. **Six more `round8` copies** exist in `lib/cost/` beyond the three named in T3. All of them move to `lib/money.js`.
6. **Typedefs with no single producer yet:** `BudgetSnapshot` and the full `GatewayConfigSection` are assembled only
   in P3. P0 annotates their parts.
7. **Prices for key budgets must be in `config.json` before the server starts.** A target given by
   `ZP_CONTRACT_URL` must therefore have been started with the harness seed config.
8. **Risk:** the `windows-11-arm` hosted runner may not be available to this private repository. If the job cannot
   start, the G0 item is recorded as blocked rather than failed.
9. **Product finding, not changed in P0:** the non-streaming chat path passes upstream error bodies through without
   masking secrets. It is recorded for the owner and fixed outside P0, because behaviour is frozen in P0.
