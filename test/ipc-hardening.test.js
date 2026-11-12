// The P3 entry conditions on the control-plane side (P4a T2): a minimal environment for the gateway (E7), the mark
// file (E4), the failed state (E8), a reload queued while starting (E9), and acks that wait for the mark file.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const { spawn } = require('child_process');
const { gwEnv } = require('../lib/ipc/env');
const { createMarkFile, readMarkFile } = require('../lib/ipc/markfile');
const { recoverMarks } = require('../lib/ipc/marks');
const { localPaths, openLocalStores } = require('../lib/ipc/local');
const { createIngest } = require('../lib/ipc/ingest');
const { createAcker } = require('../lib/ipc/acker');
const { createSupervisor } = require('../lib/ipc/supervisor');
const { stubBin, initial, killHard, waitFor, tmpDir } = require('./ipc-stub');

const STREAM = 'a'.repeat(32);

test('gwEnv: the data folder, PATH and the home folder; on Windows also SystemRoot, TEMP and TMP; nothing else', () => {
  const env = {
    PATH: '/usr/bin',
    HOME: '/home/me',
    OPENAI_API_KEY: 'sk-secret',
    ZAAFPULSE_ADMIN_PASSWORD: 'pw',
    SystemRoot: 'C:\\Windows',
  };
  assert.deepEqual(gwEnv(env, '/data', 'linux'), { ZAAFPULSE_DATA: '/data', PATH: '/usr/bin', HOME: '/home/me' });
  const win = {
    Path: 'C:\\bin',
    USERPROFILE: 'C:\\Users\\me',
    SYSTEMROOT: 'C:\\Windows',
    TEMP: 'C:\\t',
    TMP: 'C:\\t',
    X: '1',
  };
  assert.deepEqual(gwEnv(win, 'D:\\data', 'win32'), {
    ZAAFPULSE_DATA: 'D:\\data',
    PATH: 'C:\\bin',
    USERPROFILE: 'C:\\Users\\me',
    SystemRoot: 'C:\\Windows',
    TEMP: 'C:\\t',
    TMP: 'C:\\t',
  });
});

test('mark file: written with rename, raised only, read back; a bad file reads as no marks', (t) => {
  const dir = tmpDir(t, 'marks');
  const file = path.join(dir, '.data', 'ipc-marks.json');
  const m = createMarkFile(file);
  m.save(STREAM, 5);
  m.save(STREAM, 3); // never lowered
  assert.deepEqual([...readMarkFile(file)], [[STREAM, 5]]);
  assert.ok(!fs.existsSync(`${file}.tmp`));
  assert.deepEqual([...createMarkFile(file).get()], [[STREAM, 5]]);
  fs.writeFileSync(file, '{"not-a-stream": 3, "' + STREAM + '": -1');
  assert.deepEqual([...readMarkFile(file)], []);
});

test('recoverMarks: the mark file wins over a tail window the mark fell out of (E4)', (t) => {
  const root = tmpDir(t, 'tail');
  const files = localPaths(root);
  fs.mkdirSync(path.dirname(files.usageFile), { recursive: true });
  const marked = JSON.stringify({ ts: 1, keyId: 'k', model: 'm', ipc: { stream: STREAM, seq: 7 } });
  const filler = Array.from({ length: 64 }, (_, i) =>
    JSON.stringify({ ts: i, keyId: 'dashboard', model: 'x'.repeat(40) }),
  );
  fs.writeFileSync(files.usageFile, [marked, ...filler, ''].join('\n'));
  const { marksFile, ...withoutFile } = files;
  assert.equal(recoverMarks(withoutFile, 1024).get(STREAM), undefined, 'out of a 1 KiB window');
  createMarkFile(marksFile).save(STREAM, 7);
  assert.equal(recoverMarks(files, 1024).get(STREAM), 7);
  assert.equal(recoverMarks(files).get(STREAM), 7, 'the larger of the two');
});

test('acker: an ack whose send throws is not counted as sent; the next flush sends it', () => {
  const sent = [];
  let fail = true;
  const a = createAcker({
    send: (b) => {
      if (fail) throw new Error('disk full');
      sent.push(b);
    },
    everyMs: 1e9,
  });
  a.note(STREAM, 3);
  a.flush();
  assert.deepEqual(sent, []);
  fail = false;
  a.flush();
  assert.deepEqual(sent, [{ stream: STREAM, upTo: 3 }]);
  a.stop();
});

// A stand-in for lib/ipc/session.js startSession: the test ends each run with the code and fatal line it picks.
function fakeStart() {
  const runs = [];
  const start = (opts) => {
    let exit;
    let ready;
    const run = {
      opts,
      pid: 1000 + runs.length,
      sent: [],
      failure: null,
      fatal: null,
      ready: new Promise((r) => (ready = r)),
      exited: new Promise((r) => (exit = r)),
      send: (type, body) => run.sent.push([type, body]),
      kill: () => exit({ code: null, signal: 'SIGKILL' }),
      shutdown: async () => (exit({ code: 0, signal: null }), { code: 0, signal: null }),
      becomeReady: () => ready({ gen: 1 }),
      end: (code, fatal = null) => {
        run.fatal = fatal;
        exit({ code, signal: null });
      },
    };
    run.ready.catch(() => {});
    runs.push(run);
    return run;
  };
  return { runs, start };
}

function fakeSupervisor(t, extra = {}) {
  const f = fakeStart();
  const logs = [];
  const failures = [];
  const sup = createSupervisor({
    bin: 'unused',
    cpVersion: 'test',
    sources: {
      config: () => initial().config,
      secrets: () => initial().secrets,
      snapshot: () => ({ budgets: [], keyCenter: {} }),
    },
    ingest: { handle: () => true },
    log: (l) => logs.push(l),
    healthTimeoutMs: 60000,
    backoff: { firstMs: 10, maxMs: 10, resetAfterMs: 60000 },
    start: f.start,
    onFailed: (r) => failures.push(r),
    ...extra,
  });
  t.after(() => sup.stop(0));
  return { sup, f, logs, failures };
}

for (const [code, fatal] of [
  [1, 'outbox: damaged'],
  [2, 'config.push: route class "chat" is on engine "go"'],
  [4, 'port 4777 on 127.0.0.1 is in use'],
  [5, 'the outbox is in use by another gateway process'],
]) {
  test(`exit ${code} with a fatal line: the failed state with its reason, no restart (E8)`, async (t) => {
    const { sup, f, failures } = fakeSupervisor(t);
    sup.start();
    f.runs[0].end(code, fatal);
    await waitFor(() => sup.status().state === 'failed', 5000, 'failed');
    assert.deepEqual(failures, [fatal]);
    assert.equal(sup.status().failed.code, code);
    assert.equal(sup.status().restarts, 0);
    await new Promise((r) => setTimeout(r, 50)); // past the back-off: still one run
    assert.equal(f.runs.length, 1);
  });
}

test('exit 1 without a fatal line (a hard kill on Windows) is restarted', async (t) => {
  const { sup, f } = fakeSupervisor(t);
  sup.start();
  f.runs[0].end(1);
  await waitFor(() => f.runs.length === 2, 5000, 'a restart');
  assert.notEqual(sup.status().state, 'failed');
});

test('maxRestarts: the exit after 5 restarts within the window is the failed state', async (t) => {
  const { sup, f, failures } = fakeSupervisor(t, { maxRestarts: { count: 5, windowMs: 60000 } });
  sup.start();
  for (let i = 0; i < 6; i++) {
    await waitFor(() => f.runs.length === i + 1, 5000, `run ${i + 1}`);
    f.runs[i].end(3);
  }
  await waitFor(() => sup.status().state === 'failed', 5000, 'failed');
  assert.equal(sup.status().restarts, 5);
  assert.deepEqual(failures, ['5 restarts within 60 s']);
  sup.start(); // start() begins again
  await waitFor(() => f.runs.length === 7, 5000, 'a new run');
  assert.equal(sup.status().state, 'starting');
});

test('reload() while starting is queued and sent once ready; onReady runs on each ready (E9)', async (t) => {
  let readies = 0;
  const { sup, f } = fakeSupervisor(t, { onReady: () => readies++ });
  sup.start();
  assert.equal(sup.reload(), true);
  assert.deepEqual(f.runs[0].sent, []);
  f.runs[0].becomeReady();
  await waitFor(() => f.runs[0].sent.length === 2, 5000, 'the queued reload');
  assert.deepEqual(
    f.runs[0].sent.map(([type, body]) => [type, body.gen]),
    [
      ['config.push', 2],
      ['secrets.handoff', 2],
    ],
  );
  assert.equal(readies, 1);
});

test('a refused handshake fails at once and arms no hello timer: one run, the reason kept (stub)', async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  const stops = [];
  const root = tmpDir(t, 'refused', stops);
  const failures = [];
  const sup = createSupervisor({
    bin,
    args: ['--outbox', path.join(root, 'gw-outbox'), '--protocol', '2'],
    cpVersion: 'test',
    sources: {
      config: () => initial().config,
      secrets: () => initial().secrets,
      snapshot: () => ({ budgets: [], keyCenter: {} }),
    },
    ingest: { handle: () => true },
    log: () => {},
    backoff: { firstMs: 10, maxMs: 10 },
    onFailed: (r) => failures.push(r),
  });
  stops.push(() => sup.stop(0));
  sup.start();
  await waitFor(() => sup.status().state === 'failed', 10000, 'failed');
  assert.match(failures[0], /refused/);
  assert.equal(sup.status().lastExit.code, 2);
  assert.equal(sup.status().restarts, 0);
});

test('a second gateway on a held outbox fails with exit 5 (stub)', async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  const stops = [];
  const root = tmpDir(t, 'held', stops);
  const outbox = path.join(root, 'gw-outbox');
  // The holder: a stub that has the outbox open and waits on its stdin.
  const holder = spawn(bin, ['--outbox', outbox], { stdio: ['pipe', 'pipe', 'ignore'] });
  const holderGone = new Promise((r) => holder.once('close', r));
  stops.push(async () => {
    holder.stdin.end();
    killHard(holder.pid);
    await holderGone; // gone before tmpDir removes the folder
  });
  await new Promise((r) => holder.stdout.once('data', r)); // its hello: the lock is held
  const failures = [];
  const sup = createSupervisor({
    bin,
    args: ['--outbox', outbox],
    cpVersion: 'test',
    sources: {
      config: () => initial().config,
      secrets: () => initial().secrets,
      snapshot: () => ({ budgets: [], keyCenter: {} }),
    },
    ingest: { handle: () => true },
    log: () => {},
    backoff: { firstMs: 10, maxMs: 10 },
    onFailed: (r) => failures.push(r),
  });
  stops.push(() => sup.stop(0));
  sup.start();
  await waitFor(() => sup.status().state === 'failed', 10000, 'failed');
  assert.equal(sup.status().lastExit.code, 5);
  assert.match(failures[0], /in use/);
});

test('a damaged outbox (segments, no stream.json) fails with exit 1 (stub)', async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  const stops = [];
  const root = tmpDir(t, 'damaged', stops);
  const outbox = path.join(root, 'gw-outbox');
  fs.mkdirSync(outbox, { recursive: true });
  fs.writeFileSync(path.join(outbox, '00000000000000000001.jsonl'), '');
  const sup = createSupervisor({
    bin,
    args: ['--outbox', outbox],
    cpVersion: 'test',
    sources: {
      config: () => initial().config,
      secrets: () => initial().secrets,
      snapshot: () => ({ budgets: [], keyCenter: {} }),
    },
    ingest: { handle: () => true },
    log: () => {},
    backoff: { firstMs: 10, maxMs: 10 },
  });
  stops.push(() => sup.stop(0));
  sup.start();
  await waitFor(() => sup.status().state === 'failed', 10000, 'failed');
  assert.equal(sup.status().lastExit.code, 1);
  assert.match(sup.status().failed.reason, /stream\.json/);
});

test('acks go out only after the mark file holds them, and never ahead of the store (stub, E4)', async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  const stops = [];
  const root = tmpDir(t, 'ackmarks', stops);
  const stores = openLocalStores({ root });
  const ingest = createIngest({ ...stores.sinks, marks: recoverMarks(stores.files) });
  const markFile = createMarkFile(stores.files.marksFile);
  const seen = [];
  const sup = createSupervisor({
    bin,
    args: ['--outbox', stores.files.outboxDir, '--emit', '2'],
    cpVersion: 'test',
    sources: {
      config: () => initial().config,
      secrets: () => initial().secrets,
      snapshot: () => ({ budgets: [], keyCenter: {} }),
    },
    ingest,
    log: () => {},
    beforeAck: ({ stream, upTo }) => {
      assert.ok(upTo <= ingest.mark(stream), 'the mark file never runs ahead of the store');
      markFile.save(stream, upTo);
      seen.push(upTo);
    },
  });
  stops.push(() => sup.stop(0));
  sup.start();
  await waitFor(
    () =>
      readMarkFile(stores.files.marksFile).size === 1 && [...readMarkFile(stores.files.marksFile).values()][0] === 6,
    10000,
    'mark 6 on disk',
  );
  assert.equal(seen[seen.length - 1], 6);
});
