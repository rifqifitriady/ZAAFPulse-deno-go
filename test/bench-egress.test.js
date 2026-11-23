// bench/egress-overhead.js (the final benchmarks' P5 scenario) at tiny scale: it prints one JSON object with the direct,
// go and overhead percentiles, and every call it timed was recorded by the gateway as passed through the endpoint.
// Skipped without Go, as the IPC tests are (scripts/ipc/build-stub.js).
const test = require('node:test');
const assert = require('node:assert/strict');
const path = require('path');
const { spawnSync } = require('child_process');
const { buildStub } = require('../scripts/ipc/build-stub');

test('bench/egress-overhead.js: paired direct and go calls, each recorded by the gateway', { timeout: 120000 }, (t) => {
  const built = buildStub();
  if (built.skip) return t.skip(built.skip);
  const file = path.join(__dirname, '..', 'bench', 'egress-overhead.js');
  const args = process.versions.deno
    ? ['run', '-q', '--no-prompt', `--config=${path.join(__dirname, '..', 'deno.json')}`, '-P=test', file]
    : [file];
  const r = spawnSync(process.execPath, [...args, '--count', '10', '--warmup', '2'], {
    encoding: 'utf8',
    env: { ...process.env, ZP_GW_BIN: built.bin },
    timeout: 100000,
  });
  assert.equal(r.status, 0, r.stderr);
  const out = JSON.parse(r.stdout.trim().split('\n').pop());
  assert.equal(out.count, 10);
  assert.equal(out.recorded >= 12, true);
  for (const k of ['direct', 'go', 'overhead']) assert.equal(out[k].n, 10);
  assert.equal(typeof out.overhead.p95, 'number');
});
