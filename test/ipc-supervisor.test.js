// lib/ipc/supervisor.js against the Go stub (P3 T5): start, snapshots, reload and shutdown; a gateway that hangs, one
// that crashes (restarted with doubling back-off), one that writes a bad line, and one that ignores shutdown; metered
// calls that pass without a budget check are counted and alerted.
const test = require('node:test');
const assert = require('node:assert/strict');
const path = require('path');
const { createSupervisor } = require('../lib/ipc/supervisor');
const { stubBin, initial, waitFor, tmpDir } = require('./ipc-stub');

function supervise(t, bin, extraArgs = [], opts = {}) {
  const root = tmpDir(t, 'sup');
  const logs = [];
  const first = initial();
  const sup = createSupervisor({
    bin,
    args: ['--outbox', path.join(root, 'gw-outbox'), '--health-ms', '50', ...extraArgs],
    cpVersion: 'test',
    sources: {
      config: () => first.config, // the supervisor sets gen
      secrets: () => first.secrets,
      snapshot: () => ({ budgets: [], keyCenter: {} }),
    },
    log: (l) => logs.push(l),
    healthTimeoutMs: 1000,
    snapshotEveryMs: 100,
    backoff: { firstMs: 50, maxMs: 200, resetAfterMs: 60000 },
    killAfterMs: 300,
    ...opts,
  });
  t.after(() => sup.stop(0));
  return { sup, logs };
}

test('start, ready, snapshots every interval, reload, stop: exit 0', async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  const { sup } = supervise(t, bin);
  sup.start();
  await waitFor(() => sup.status().state === 'ready', 5000, 'ready');
  assert.equal(sup.status().gen, 1);
  await waitFor(() => sup.status().lastHealth && sup.status().lastHealth.gen === 1, 5000, 'health');
  await new Promise((r) => setTimeout(r, 400));
  assert.ok(sup.status().lastHealth.snapshotAgeMs < 300, `snapshot age ${sup.status().lastHealth.snapshotAgeMs}`);
  assert.equal(sup.reload(), true);
  await waitFor(() => sup.status().lastHealth.gen === 2, 5000, 'health of generation 2');
  const exit = await sup.stop(1000);
  assert.deepEqual([exit.code, exit.signal], [0, null]);
  assert.equal(sup.status().state, 'stopped');
  assert.equal(sup.status().restarts, 0);
});

test('a gateway that stops sending health is killed and restarted', async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  const { sup, logs } = supervise(t, bin, ['--hang-after-ready'], { healthTimeoutMs: 300 });
  sup.start();
  await waitFor(() => sup.status().restarts >= 1, 5000, 'a restart');
  assert.ok(
    logs.some((l) => l.startsWith('gw: no health for 300 ms')),
    logs.join('\n'),
  );
});

test('a gateway that crashes is restarted with back-off 50, 100, 200, 200 ms', async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  const { sup, logs } = supervise(t, bin, ['--crash-after-ready-ms', '20']);
  sup.start();
  await waitFor(() => sup.status().restarts >= 4, 10000, 'four restarts');
  const delays = logs
    .map((l) => l.match(/restarting in (\d+) ms$/))
    .filter(Boolean)
    .map((m) => Number(m[1]));
  assert.deepEqual(delays.slice(0, 4), [50, 100, 200, 200]);
  assert.equal(sup.status().lastExit.code, 3);
});

test('a bad line from the gateway is a protocol error: killed and restarted', async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  const { sup, logs } = supervise(t, bin, ['--bad-line']);
  sup.start();
  await waitFor(() => sup.status().restarts >= 1, 5000, 'a restart');
  assert.ok(logs.includes('gw: protocol error: a line is not JSON'), logs.join('\n'));
});

test('a gateway that ignores shutdown is killed after graceMs + killAfterMs', async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  const { sup } = supervise(t, bin, ['--ignore-shutdown']);
  sup.start();
  await waitFor(() => sup.status().state === 'ready', 5000, 'ready');
  const started = Date.now();
  const exit = await sup.stop(100);
  const took = Date.now() - started;
  assert.notEqual(exit.code, 0);
  assert.ok(took >= 350 && took < 3000, `killed after ${took} ms`);
  assert.equal(sup.status().restarts, 0, 'a stop is not restarted');
});

test('metered calls without a budget check are counted, alerted once, and stored with budget "unchecked"', async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  const stored = [];
  const { sup, logs } = supervise(t, bin, ['--emit', '3', '--stale-ms', '-1'], {
    ingest: { handle: (msg) => stored.push(msg) },
  });
  sup.start();
  await waitFor(() => sup.status().unchecked.count === 3, 5000, 'three unchecked calls');
  assert.equal(sup.status().alert, true);
  assert.equal(logs.filter((l) => l.startsWith('gw: alert:')).length, 1);
  const usage = stored.filter((m) => m.type === 'usage.report');
  assert.deepEqual(
    usage.map((m) => m.body.budget),
    ['unchecked', 'unchecked', 'unchecked'],
  );
});

test('an event that cannot be stored is not a crash: the cause is logged and the back-off keeps growing', async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  // resetAfterMs 0: a crash would count as a healthy run and reset the back-off; a store failure must not.
  const { sup, logs } = supervise(t, bin, ['--emit', '1'], {
    ingest: {
      handle: () => {
        throw new Error('disk full');
      },
    },
    backoff: { firstMs: 50, maxMs: 200, resetAfterMs: 0 },
  });
  sup.start();
  await waitFor(() => sup.status().restarts >= 4, 10000, 'four restarts');
  const delays = logs
    .map((l) => l.match(/could not store the gateway's events \(.*disk full.*\); restarting in (\d+) ms$/))
    .filter(Boolean)
    .map((m) => Number(m[1]));
  assert.deepEqual(delays.slice(0, 4), [50, 100, 200, 200], logs.join('\n'));
  assert.equal(sup.status().lastFailure.kind, 'store');
  assert.match(sup.status().lastFailure.message, /disk full/);
});
