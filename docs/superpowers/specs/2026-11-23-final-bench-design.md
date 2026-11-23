# The final benchmark runner: design

Date: 2026-11-23. Repo `ZAAFPulse-deno-go`. Branch `migrate/final-bench`, cut from `migrate/deno-go` at `74f2f31`.
Every decision below is **recommended, pending owner**.

## The problem

The owner deferred every quiet-machine benchmark of the Deno + Go migration to one final session (owner decision
2026-11-16): the laptop on AC power, the heavy apps closed (Football Manager, Chrome, Antigravity IDE), no agent
running, untouched for the length of the run. The phases left a list of owed measurements spread over ten results
documents, each with its own knobs and environment variables. The runner turns that list into one command that checks
the conditions, runs every scenario, and writes one report.

## The command

```bash
deno task bench:final            # or: node scripts/final-bench.js
```

Flags: `--yes` (no confirmation), `--only <ids>`, `--smoke` (tiny scale, every output stamped "SMOKE RUN, NOT A
MEASUREMENT"), `--out <dir>`, `--skip-preflight` (results stamped "not quiet-confirmed"), `--repeats <n>`, `--runtime
deno|node`, `--binary <path> --gw <path>`, `--no-build`, `--countdown <s>`, `--heavy <names>`, `--noise-cpu <pct>`,
`--list`, `--help`.

## Scenarios, targets and sources

Targets: M3 non-stream overhead p95 ≤ 2.84 ms (plan §4: ≤ 50% of the JS baseline 5.67 ms and ≤ 5 ms); M4 500
concurrent streams held with 0 errors, stream overhead p95 ≤ 10 ms, gateway peak RSS ≤ 80 MB; M5 idle RSS of both
processes ≤ 200 MB; M7 start-up to both processes healthy ≤ 1.0 s.

| Id | What | Knobs | Targets | Source |
|---|---|---|---|---|
| `baseline` | The JS gateway on Node, external tier, 50/200/500 streams: the re-recorded streaming baseline | `ZP_RUNTIME=node`, js | info (baseline rows) | plan §2 (owner decision 2026-11-10: stream cells noisy, re-record at the end) |
| `private-js`, `private-front`, `private-go` | M4 trio in one session, private tier | `ZP_ENGINE`, `ZP_BENCH_TIER=private` | go: M3, M4; js and front: info | P4c results, owner note item 1 |
| `external-js`, `external-front`, `external-go` | The trio on the external tier, default guardrail policy | `ZP_BENCH_TIER=external` | go: M3, M4, M5, M7 (bench); js, front: info | P4e results "Owed", owner note item 2, P7 precondition 2 |
| `translated-go` | `/v1/messages` in the Anthropic format, translated to chat | `ZP_BENCH_FORMAT=anthropic` | M3, M4 | P4d results "Owed" |
| `saver-go`, `saver-js` | A 64 KiB tool result in every call through the token saver | `ZP_BENCH_TOOL_RESULT=65536` | go: M3, M4; js: info | rtk-go results "Owed" |
| `route-go`, `route-js` | Chat on a two-target round-robin route | `ZP_BENCH_ROUTE=round-robin` (new knob) | go: M3, M4; js: info | P4h results "Owed" |
| `p4f-base`, `p4f-head`, `p4f-notrace`, `p4f-otlp` | The +17 ms at 500 streams: `e41e2ea` against this tree, same session, Node, go engine, private tier; tracing off; OTLP export on | `ZP_BENCH_TRACING=off`, `ZP_BENCH_OTLP=on` (new knob) | otlp: M3, M4; gap: info | P4f results "Follow-ups" (final review m7), spec D21 |
| `egress` | A `connectors` call through the egress endpoint against direct, local fake | `bench/egress-overhead.js` (new) | overhead p95 ≤ 2.84 ms (M3's target, as P5 asks) | P5 results "Benchmarks" |
| `binary` | Start-up of the packaged binary with the gateway in front; archive size | `scripts/smoke-binary.js --front` on a `deno task build --package` of this machine | M7 ≤ 1.0 s; M6 info against v1.1.0 | P6 results "Deferred" |
| `ipc` | IPC round trip and event throughput | `bench/ipc-latency.js` | info | P3 results §3 (measured on battery) |
| `front-proxy` | The Go front's proxy overhead on dashboard pages | `bench/front-overhead.js` | info | P4a results (the M3 front baseline) |

Not measured by the runner, listed in the report with their targets and where they are proven: M1 (test parity), M2
(contract suite), M8 (6/6 platforms), M9 (permissions), M10 (egress coverage), M11 (dependencies), M12 (data
compatibility, `scripts/upgrade-check.js`), M6 on the other five targets (release dry run), the P4f journal on/off A/B
(the gateway has no switch to turn the journal off), and the P4g live provider smoke tests (owner keys).

## Preflight

Each check prints pass, warn or fail; a fail stops the run unless `--skip-preflight`.

1. Load average below 2 over the last 1 and 5 minutes (`os.loadavg`; Windows has none: warn).
2. AC power: macOS `pmset -g batt`, Linux `/sys/class/power_supply`, Windows `Win32_Battery` through PowerShell; no
   battery found is a desktop (pass); undetectable is a warn, and the confirmation then asks.
3. No heavy app: Football Manager, Google Chrome / Chrome, Antigravity, plus `--heavy`; and the top CPU processes over a
   3 s window (a foreign process above 30% of a core is a warn).
4. No other benchmark or agent: `node --test`, `deno test`, `go test`, test binaries, the `claude` and `codex` CLIs,
   `bench/run.js`, and `deno` or `zaafpulse-gw` processes that are neither this runner's nor the live dashboard's.
5. The live dashboard on 4777 (and the preview on 4787): reported with its CPU, never touched.
6. Free disk of at least 2 GB where the results and temp folders are.
7. `ulimit -n` can reach 8192 (500 streams need about 2,000 open files); every scenario is started with it raised.
8. Tools: Go (required), Deno and Node (each needed by some scenarios).
9. The gateway binary `gw/bin/zaafpulse-gw` built from this tree (built when missing or older than its sources).
10. Keep-awake: `caffeinate -dims -w <pid>` (macOS), `systemd-inhibit … tail --pid` (Linux), a PowerShell
    `SetThreadExecutionState` holder (Windows); each ends with the runner even when it is killed, and is stopped at the
    end.
11. A note: leave the machine untouched until the report is written.

## Decisions

- **D1. Repeats in rounds.** `--repeats 3` by default; round r runs every scenario once, so an interrupted session still
  has every scenario once, and slow drift spreads over all of them. The report shows the median and the min–max spread.
- **D2. Noise from foreign CPU, not load.** The bench itself loads the machine (500 streams keep four processes busy),
  so the load average during a scenario says little. Every 2 s the runner reads the cumulative CPU time of every
  process (`ps -o time` on macOS and Linux, `Win32_Process` on Windows) and counts the CPU used by processes outside its
  own tree. A run is **noisy** when that foreign CPU averaged above 50% of one core (`--noise-cpu`) or one sample was
  above 200%. The load average is sampled too and reported.
- **D3. Quiet-confirmed** means: the preflight ran and passed, not a smoke run, and no repeat of the scenario was noisy.
- **D4. Runtime.** The scenarios run the server on Deno (what v2 ships) unless `--runtime node`; `baseline` and the P4f
  A/B run on Node, as the records they repeat were taken.
- **D5. Levels.** Only `baseline` runs 50, 200 and 500 streams (the baseline table has all three cells); every other
  scenario runs 500 only, which is what the targets read. Other knobs stay at their defaults, so the numbers are
  comparable with every earlier record.
- **D6. The P4f base** is `git archive e41e2ea` into a temp folder (no worktree, nothing changed in `.git`), its gateway
  built there.
- **D7. Isolation.** Every child gets a scrubbed environment: no `ZP_CONTRACT_URL`, no inherited `ZP_BENCH_*`,
  `ZP_ENGINE`, `ZP_RUNTIME`, no `OTEL_*`, no proxy variables (`NO_PROXY` set to loopback). Servers, mocks and fakes
  listen on port 0 of 127.0.0.1 (never 4777 or 4787) with their own temp data folders and fake keys.
- **D8. Cleanup.** The runner keeps every process it has seen in its tree and stops them all (SIGTERM, then SIGKILL; on
  Windows `taskkill /T /F`) at the end, on a failure and on Ctrl-C, and stops the keep-awake child.
- **D9. Smoke.** `--smoke` runs every scenario at tiny scale (20 requests, 5 streams for 1 s, one start), writes to
  `bench/results/<date>-final-smoke/` (ignored by git) and stamps the console, every JSON file and the report.
- **D10. The binary.** Without `--binary`, the runner builds this machine's package (`deno task build --package`)
  before the countdown, unpacks it in a temp folder and runs `scripts/smoke-binary.js --front` once per repeat;
  `--no-build` skips the scenario.
- **D11. Slow test.** The end-to-end smoke test runs only with `ZP_FINAL_BENCH_SMOKE=1` (the repo marks slow tests
  with an environment variable, as `ZAAFPULSE_PERF=1`).

- **D12. The task lives in `package.json`.** `deno task bench:final` reads `package.json` scripts; `deno.json` holds
  only the permission sets (`test/permissions.test.js` asserts it), as with `build` and `typecheck`. `npm run
  bench:final:node` and `node scripts/final-bench.js` run it on Node. The Deno launch grants loopback net, the sys
  readings it uses, run (it orchestrates go, git, tar, ps, the keep-awake tool and its bench children, each with its own
  grant) and broad write (`--out` names any folder).

## Pending owner

- Repeats 3 (about 2 h 15 min) against 1 (about 50 min); the runner prints its estimate before the confirmation.
- The server runtime for the headline scenarios: Deno (D4).
- The noise threshold (D2).
