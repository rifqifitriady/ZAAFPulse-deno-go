# P0: the gateway contract suite under Deno

Date: 2026-11-10. Measured on the tree of the commit that adds this file (parent `926d4b5`), macOS arm64,
Deno 2.9.8, Node 24.

The contract suite (`test/contract/`, 172 cases) was run twice with the server under Deno, besides the Node reference
run (`npm run test:contract`: 172 passed, 0 failed, about 2.6 s).

## Commands

```bash
# 1. Tests under Node, server.js under Deno (the narrow permission set in test/contract/harness.js, not -A)
ZP_RUNTIME=deno node --test --test-concurrency=1 test/contract/*.test.js 2>&1 | grep -E "^(✖|ℹ (tests|pass|fail))"

# 2. Tests and server.js both under Deno
NO_COLOR=1 ZP_RUNTIME=deno deno test --no-check --allow-read --allow-write="${TMPDIR:-/tmp}" --allow-net=127.0.0.1,localhost --allow-run=node,deno --allow-env --allow-sys test/contract/ > /tmp/zp-contract-deno.txt 2>&1
scripts/migration/deno-test-summary.sh /tmp/zp-contract-deno.txt "contract suite, server and tests under Deno 2.9.8"
```

## Results

Run 1 (`node --test`, Deno server): 172 tests, 172 passed, 0 failed, about 3.0 s.

Run 2 (`deno test`, Deno server), the summary script's output:

## contract suite, server and tests under Deno 2.9.8

| passed | failed | ignored |
|---|---|---|
| 172 | 0 | 0 |

`deno test` reported no leaked resources (its sanitizers are on by default), and no connection resets were seen in
either run.

## Failing cases

None. No case maps to a P1 group (A `process.execPath`, B `node:test` mocks, C global `localStorage`,
D `process.platform`, E to investigate, F fetch abort timing) or to a new one.

The one difference between the runtimes that is already known is visible only in a message the suite does not pin: a
connection reset by the provider is `oa: UND_ERR_SOCKET` under Node and `oa: fetch failed` under Deno, so the error
cases assert only the `<provider>: ` prefix.

## Not covered by this run

- Linux and Windows: the CI `deno` job runs the same suite on ubuntu and macOS (not blocking); Windows runs only the
  Node reference in the `test` job.
- The Deno server ran with `--allow-run=which,ps,lsof,security`; no case lists processes, so the Windows process
  table (PowerShell) is not exercised here.
