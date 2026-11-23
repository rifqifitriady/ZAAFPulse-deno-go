# The final benchmarks

The migration deferred every quiet-machine measurement to one session at the end (owner decision 2026-11-16). One
command runs them all and writes the report. Design and decisions:
the final-bench design.

## The one command

```bash
deno task bench:final
```

Without Deno: `node scripts/final-bench.js` (the same flags). `--help` lists the flags; `--list` lists the scenarios.

## Before you run it

- Plug the laptop in to AC power.
- Quit Football Manager, Chrome and the Antigravity IDE, and any other heavy app (`--heavy "Slack,Zoom"` makes the
  preflight look for more).
- Stop every agent and test run: Claude Code sessions, Codex, `node --test`, `go test`, other benchmarks. The live
  dashboard on port 4777 and the preview on 4787 may stay up: the runner only reports them, and never touches them,
  their data or their ports.
- Wait until the machine is calm (the load average under 2), then run the command and leave the machine alone, lid open,
  until it prints the report path.

The runner checks all of this first (the preflight) and prints each check as pass, WARN or FAIL. A FAIL stops it;
`--skip-preflight` runs anyway and stamps every result "not quiet-confirmed". It also checks the open-files limit, free
disk, Go, Deno and Node, builds the Go gateway if it is missing or older than its sources, and keeps the machine awake
for the run (`caffeinate` on macOS, `systemd-inhibit` on Linux, a PowerShell holder on Windows), stopping that at the
end. Then it asks you to type `yes`, counts down 10 seconds, and starts.

Everything runs on this machine: local fake providers on 127.0.0.1, fake keys, temp data folders and free ports. Nothing
leaves the machine. Every process it starts is stopped at the end, on an error, and on Ctrl-C (which still writes a
partial report).

## How long it takes

About 45 minutes per round of all scenarios, plus about 4 minutes to prepare (the P4f base tree and the package
build). The default is 3 rounds: **about 2 h 15 min**. `--repeats 1` takes about 50 minutes but shows no spread.
The runner prints its estimate before it asks to start. Estimated from the scenario knobs (each `bench/run.js` run is
about 2,000 paired calls of about 26 ms, then 500 streams for 10 s through the gateway and 10 s direct) and checked
against the smoke run, whose fixed costs (server starts, seeding, the 5 s idle wait) are the same at full scale.

## Reading the report

`bench/results/<date>-final/report.md`, next to one raw JSON file per scenario (every run, its numbers and its noise
reading) and `session.json` (the machine and the preflight).

- **Metrics**: one row per measured value: the metric (M3, M4, …), the scenario, the target, the median over the rounds
  and the spread (min–max), the verdict and whether it is quiet-confirmed.
  - Verdict `pass` or `fail` against the target; `noisy (pass)` or `noisy (fail)` when another process used more than
    half a core on average while that scenario ran (the Runs table names the busiest); `info` for reference values
    without a target (the JS gateway, the front mode, the baseline); `error` when no run of it succeeded.
  - Quiet-confirmed `yes` only when the preflight passed, it was not a smoke run and no round of that scenario was
    noisy.
- **Baseline rows**: the program plan's §2 baseline rows and the §4 cells, from the re-recorded Node baseline.
- **P4f**: this tree against the base `e41e2ea`, metric by metric.
- **Not covered by the runner**: M1, M2, M8–M12, the P4f journal A/B and the P4g live tests, with where each is proven.
- **What to do with this**: where to paste the numbers (the plan, the phase results docs, the release notes).

## The scenarios

| Id | What | Targets |
|---|---|---|
| `baseline` | The JS gateway on Node, 50/200/500 streams: the baseline re-recorded | information |
| `private-js`, `private-front`, `private-go` | The M4 trio, private tier | go: M3, M4 |
| `external-js`, `external-front`, `external-go` | The trio on the external tier (guardrails in the path) | go: M3, M4, M5, M7 |
| `translated-go` | `/v1/messages` (Anthropic format) translated to chat | M3, M4 |
| `saver-go`, `saver-js` | A 64 KiB tool result through the token saver | go: M3, M4 |
| `route-go`, `route-js` | Chat on a two-target round-robin route | go: M3, M4 |
| `p4f-base`, `p4f-head`, `p4f-notrace`, `p4f-otlp` | The +17 ms gap against `e41e2ea`; tracing off; OTLP export on | otlp: M3, M4 |
| `egress` | A connectors call through the egress endpoint against direct | p95 ≤ 2.84 ms |
| `binary` | The packaged binary with the gateway in front; the archive size | M7, M6 |
| `ipc`, `front-proxy` | IPC round trip and throughput; the front's proxy overhead | information |

Targets: M3 non-stream overhead p95 ≤ 2.84 ms; M4 500 streams held with 0 errors, stream overhead p95 ≤ 10 ms, gateway
RSS ≤ 80 MB; M5 idle RSS of both processes ≤ 200 MB; M7 start-up to both processes healthy ≤ 1 s; M6 the archive no
larger than v1.1.0's download for the platform.

## One scenario, or a few

```bash
deno task bench:final --only external-go
deno task bench:final --only external-go,translated-go --repeats 5
deno task bench:final --only binary --binary ./zaafpulse-1.1.0-darwin-arm64/zaafpulse
```

A second session on the same day writes `<date>-final-2/`, so nothing is overwritten.

## Proving the runner without measuring

```bash
deno task bench:final --smoke --yes --skip-preflight
```

Every scenario at tiny scale (20 calls, 5 streams for 1 s), about 4 minutes with the package build (3 min 45 s on
the owner's laptop on 2026-11-23), into
`bench/results/<date>-final-smoke/` (ignored by git). The console, the report and every JSON file say "SMOKE RUN, NOT A
MEASUREMENT": the numbers prove the runner works and mean nothing. The test of it runs only on request:
`ZP_FINAL_BENCH_SMOKE=1 node --test --test-timeout=600000 test/final-bench-smoke.test.js`.

## Platforms

The scenarios run on macOS, Linux and Windows wherever `bench/run.js` does (RSS readings need `ps`: macOS and Linux).
The preflight's platform parts are: power (`pmset`, `/sys/class/power_supply`, `Win32_Battery`), the process list
(`ps`, `Win32_Process`), the port owner (`lsof` or `ss`, `netstat`), free disk (`statfs`, `df`, `Get-PSDrive`), the
open-files limit (POSIX only), the load average (none on Windows: a warning) and the keep-awake tool.
