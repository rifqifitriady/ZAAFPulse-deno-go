# Gateway benchmark

`bench/run.js` measures the gateway of one runtime and writes the numbers to `bench/results/`, so two runs can be
compared. It is the measuring stick of the Deno + Go migration (program plan §2 and §4: M3, M4, M5, M7). Node standard
library only; it runs under Node and Deno and needs no install.

## What it measures

| Measure | How |
|---|---|
| Startup | `server.js` spawned on a fresh data folder until the first 200 on `/login.html`, 5 times (p50) |
| Idle RSS | the server's resident memory 5 s after it is ready (`ps`, macOS and Linux) |
| Overhead, non-streaming | 2,000 requests through the gateway and 2,000 straight to the mock provider, paired; p50/p95/p99 of the difference |
| Concurrent streams | 50, 200 and 500 SSE streams kept open for 10 s each, through the gateway and then straight to the mock: completed streams, errors, time to first byte p95, overhead p95 (gateway p95 minus direct p95) |
| Peak RSS | the server's highest RSS during the 500-stream step |

The mock provider (`bench/mock-provider.js`) runs in its own process: an OpenAI-compatible `/v1/chat/completions` that
answers after 20 ms, whole or as 20 SSE chunks 25 ms apart with usage in the last one. The server is started and seeded
by the contract harness (`test/contract/harness.js`): one external provider pointing at the mock, 25 keys so no key
reaches its rate limit, the default guardrails policy. Nothing leaves the machine.

## Run it

Close other heavy apps, keep the laptop on power, then:

```bash
ulimit -n 8192                 # 500 streams need about 2,000 open files across the processes
node bench/run.js              # the Node server: bench/results/<date>-node-<host>.json
ZP_RUNTIME=deno node bench/run.js   # the same harness against server.js under Deno
```

The IPC between the control plane and the Go gateway (P3) has its own measure: `node bench/ipc-latency.js` (round trip over the stdio pipes and event throughput; see docs/migration/2026-11-11-p3-results.md).

Under Deno instead of Node:

```bash
deno run --allow-read --allow-write="${TMPDIR:-/tmp}",bench/results --allow-net=127.0.0.1,localhost --allow-env --allow-run=node,deno,ps,git --allow-sys bench/run.js
```

Reading the stream numbers: each worker opens its next stream only when the last one ends, so "completed" is quantized
per worker (workers x steps of about 0.5 s) and moves by tens between runs; only "0 errors" and "at least 500 streams
held" are meaningful. The stream overhead p95 is the difference of two p95s from two unpaired steps run one after the
other (gateway, then direct), so machine noise between the steps lands in it; it can come out negative. Re-record on
power with other apps closed before drawing conclusions from it.

A run takes about 3 minutes (at most 5). Knobs, all optional: `ZP_BENCH_REQUESTS` (2000), `ZP_BENCH_LEVELS`
(`50,200,500`), `ZP_BENCH_SECONDS` (10), `ZP_BENCH_DELAY_MS` (20), `ZP_BENCH_CHUNKS` (20), `ZP_BENCH_INTERVAL_MS` (25),
`ZP_BENCH_STARTS` (5), `ZP_BENCH_OUT` (`bench/results`), `ZP_BENCH_HOST` (the host name). A result is only comparable
with one taken with the same knobs; the file records them under `config`.

## Compare two runs

```bash
node bench/compare.js bench/results/a.json bench/results/b.json
```

prints each metric of a and b, `b - a` and the change in percent. Lower is better for everything but completed
streams. `node bench/compare.js --baseline <file>` prints the rows the program plan's baseline table takes.

## Compiled binary

`ZP_RUNTIME=binary ZP_BINARY=<path of a built binary> node bench/run.js` measures a `deno task build` executable the
same way (the file is named `<date>-binary-<host>.json`; the contract harness runs the binary with `start`). Keep the
Mac awake and on power: on battery it falls into dark wake during a run, which stalls or resets requests (seen on
2026-11-11); `caffeinate -dims node bench/run.js` keeps it awake.
