// lib/ipc/supervisor.js against the Go stub (P3 T5): start, snapshots, reload and shutdown; a gateway that hangs, one
// that crashes (restarted with doubling back-off), one that writes a bad line, and one that ignores shutdown; metered
// calls that pass without a budget check are counted and alerted.
const test = require('node:test');
const assert = require('node:assert/strict');
const path = require('path');
const fs = require('fs');
const os = require('os');
const { createSupervisor } = require('../lib/ipc/supervisor');
const { createIngest } = require('../lib/ipc/ingest');
const { DIR } = require('../lib/ipc/schema');
const { stubBin, initial, waitFor } = require('./ipc-stub');

function supervise(t, bin, extraArgs = [], opts = {}) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'zp-ipc-sup-'));
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
    // Generous: a busy machine (both suites in parallel) must not see a hang. The hang test sets its own 300 ms.
    healthTimeoutMs: 10000,
    snapshotEveryMs: 100,
    backoff: { firstMs: 50, maxMs: 200, resetAfterMs: 60000 },
    killAfterMs: 300,
    ...opts,
  });
  // One hook, in this order: the gateway must be gone before its folder is removed (Windows cannot remove a folder
  // whose files a running process holds open, and a failed hook would leave the gateway running).
  t.after(async () => {
    try {
      await sup.stop(0);
    } finally {
      fs.rmSync(root, { recursive: true, force: true, maxRetries: 20, retryDelay: 100 });
    }
  });
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
  // Past the first snapshot's age (400 ms), a health received now still reports a young snapshot: they keep coming.
  await new Promise((r) => setTimeout(r, 400));
  const after = Date.now();
  await waitFor(
    () => sup.status().lastHealth.at > after && sup.status().lastHealth.snapshotAgeMs < 300,
    5000,
    'a health after 400 ms that reports a snapshot younger than 300 ms',
  );
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
  // A lasting store failure: the replayed event fails again within moments of every ready, so no run is ever healthy
  // for resetAfterMs (5 s here) and the back-off keeps growing instead of looping.
  const { sup, logs } = supervise(t, bin, ['--emit', '1'], {
    ingest: {
      handle: () => {
        throw new Error('disk full');
      },
    },
    backoff: { firstMs: 50, maxMs: 200, resetAfterMs: 5000 },
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

// A fake session (the start option): the tests below drive ready, events, health and exit by hand, with a fake clock.
function fakeStart() {
  const sessions = [];
  const start = (opts) => {
    let ready;
    let exit;
    const s = {
      opts,
      pid: 1000 + sessions.length,
      failure: null,
      killed: false,
      shutdownAsked: false,
      ready: new Promise((r) => (ready = r)),
      exited: new Promise((r) => (exit = r)),
      send: () => {},
      kill: () => {
        s.killed = true;
      },
      shutdown: () => {
        s.shutdownAsked = true;
        return s.exited;
      },
      becomeReady: () => ready(),
      exit: (code, signal = null) => exit({ code, signal }),
    };
    sessions.push(s);
    return s;
  };
  return { sessions, start };
}
// stop() waits for the gateway to exit: let every fake session exit.
function stopFake(sup, sessions) {
  const done = sup.stop(0);
  for (const s of sessions) s.exit(0);
  return done;
}
const fakeOpts = (start, logs, clock, extra = {}) => ({
  bin: 'unused',
  cpVersion: 'test',
  sources: { config: () => ({}), secrets: () => ({}), snapshot: () => ({ budgets: [], keyCenter: {} }) },
  log: (l) => logs.push(l),
  now: () => clock.t,
  healthTimeoutMs: 60000,
  snapshotEveryMs: 60000,
  start,
  ...extra,
});

test('a resent event is counted once: only what the ingest stored counts as an unchecked call', async (t) => {
  const { sessions, start } = fakeStart();
  const logs = [];
  const clock = { t: 1000 };
  const ingest = createIngest({
    recordUsage: () => {},
    recordGovernance: () => {},
    recordMcp: () => {},
    writeSpans: () => {},
    log: () => {},
  });
  const sup = createSupervisor(fakeOpts(start, logs, clock, { ingest }));
  t.after(() => stopFake(sup, sessions));
  sup.start();
  const msg = JSON.parse(fs.readFileSync(path.join(DIR, 'fixtures', 'usage.report', 'valid-unchecked.json'), 'utf8'));
  // Delivered, then resent twice from the outbox (a restart before the ack).
  for (let i = 0; i < 3; i++) sessions[0].opts.onEvent(msg, 'gw-1');
  assert.deepEqual(ingest.stats(), {
    stored: 1,
    duplicates: 2,
    unchecked: 1,
    lastUncheckedAt: ingest.stats().lastUncheckedAt,
  });
  assert.equal(sup.status().unchecked.count, 1);
  assert.equal(logs.filter((l) => l.startsWith('gw: alert:')).length, 1);
});

test('a run healthy for resetAfterMs resets the back-off even when it ends with a store failure', async (t) => {
  const { sessions, start } = fakeStart();
  const logs = [];
  const clock = { t: 0 };
  const sup = createSupervisor(
    fakeOpts(start, logs, clock, { backoff: { firstMs: 10, maxMs: 40, resetAfterMs: 60000 } }),
  );
  t.after(() => stopFake(sup, sessions));
  sup.start();
  const delays = () =>
    logs
      .map((l) => l.match(/restarting in (\d+) ms$/))
      .filter(Boolean)
      .map((m) => Number(m[1]));
  // Three crashes before ready: 10, 20, 40.
  for (let i = 0; i < 3; i++) {
    sessions[i].exit(3);
    await waitFor(() => sessions.length === i + 2, 5000, `restart ${i + 1}`);
  }
  assert.deepEqual(delays(), [10, 20, 40]);
  // Healthy for 60 s, then one store failure: back to firstMs.
  const long = sessions[3];
  long.becomeReady();
  await waitFor(() => sup.status().state === 'ready', 5000, 'ready');
  clock.t += 60000;
  long.failure = { kind: 'store', message: 'EBUSY' };
  long.exit(null, 'SIGKILL');
  await waitFor(() => sessions.length === 5, 5000, 'restart after the store failure');
  assert.deepEqual(delays(), [10, 20, 40, 10]);
  assert.ok(
    logs.some((l) => l.includes("could not store the gateway's events (EBUSY)")),
    logs.join('\n'),
  );
  assert.equal(sup.status().lastFailure.kind, 'store');
  // A store failure moments after the next ready (the replay fails again) is not a healthy run: the back-off grows.
  const short = sessions[4];
  short.becomeReady();
  await waitFor(() => sup.status().state === 'ready', 5000, 'ready again');
  clock.t += 5;
  short.failure = { kind: 'store', message: 'EBUSY' };
  short.exit(null, 'SIGKILL');
  await waitFor(() => sessions.length === 6, 5000, 'restart after the second store failure');
  assert.deepEqual(delays(), [10, 20, 40, 10, 20]);
});

test('the health watchdog neither re-arms nor kills once stop() has begun', async (t) => {
  const { sessions, start } = fakeStart();
  const logs = [];
  const clock = { t: 0 };
  const sup = createSupervisor(fakeOpts(start, logs, clock, { healthTimeoutMs: 30 }));
  sup.start();
  const s = sessions[0];
  s.becomeReady();
  await waitFor(() => sup.status().state === 'ready', 5000, 'ready');
  s.opts.onHealth({ gen: 1 });
  const stopping = sup.stop(5000);
  assert.equal(s.shutdownAsked, true);
  // A health that arrives while the gateway drains must not arm the watchdog again.
  s.opts.onHealth({ gen: 1 });
  await new Promise((r) => setTimeout(r, 150));
  assert.equal(s.killed, false, 'killed by the watchdog during shutdown');
  assert.ok(!logs.some((l) => l.startsWith('gw: no health')), logs.join('\n'));
  s.exit(0);
  assert.deepEqual(await stopping, { code: 0, signal: null });
});
