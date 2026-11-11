// Kill tests (P3 T6): SIGKILL (taskkill /F on Windows) the gateway stub at random points of an --emit stream and
// restart it, 20 runs; then SIGKILL the control-plane side (a child process running test/fixtures/ipc/cp-child.js
// under this runtime) and restart it. After each run: every usage line stored exactly once (no loss, no duplicate),
// the governance chain verifies, the MCP audit log and the trace store hold each event once, and the outbox is empty.
// The random points come from a seeded generator: ZP_KILL_SEED=<n> replays a run; every failure names its seed.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const { spawn } = require('child_process');
const { openLocalStores } = require('../lib/ipc/local');
const { createIngest } = require('../lib/ipc/ingest');
const { startSession } = require('../lib/ipc/session');
const { stubBin, initial, killHard, alive, sleep, waitFor, tmpDir } = require('./ipc-stub');

const ROUNDS = 40; // 120 events a run
const TOTAL = 3 * ROUNDS;
const SEED = Number(process.env.ZP_KILL_SEED) || Date.now() % 1e9;

// mulberry32: a small seeded generator, so a failing run can be replayed.
function random(seed) {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

const readJsonl = (file) =>
  fs.existsSync(file)
    ? fs
        .readFileSync(file, 'utf8')
        .split('\n')
        .filter(Boolean)
        .map((l) => JSON.parse(l))
    : [];
const marked = (list) => list.filter((e) => e.ipc);
const seqs = (list) => marked(list).map((e) => e.ipc.seq);
const expected = (offset, every = 3) => Array.from({ length: TOTAL / every }, (_, i) => every * i + offset);

// What a finished run must look like, whatever was killed and when.
function checkRun(root, label) {
  const stores = openLocalStores({ root });
  const usage = readJsonl(stores.files.usageFile);
  assert.deepEqual(seqs(usage), expected(1), `${label}: each usage line once, in order`);
  const mcp = readJsonl(stores.files.mcpAuditFile);
  assert.deepEqual(seqs(mcp), expected(5, 6), `${label}: each MCP audit entry once`);
  const govDir = stores.files.governanceDir;
  const gov = fs
    .readdirSync(govDir)
    .filter((f) => f.startsWith('governance-'))
    .flatMap((f) => readJsonl(path.join(govDir, f)));
  assert.deepEqual(seqs(gov), expected(2, 6), `${label}: each governance entry once`);
  const verify = stores.governance.verify();
  assert.equal(verify.ok, true, `${label}: the audit chain verifies: ${JSON.stringify(verify)}`);
  const spans = fs
    .readdirSync(stores.files.traceDir)
    .filter((f) => f.endsWith('.ndjson'))
    .flatMap((f) => readJsonl(path.join(stores.files.traceDir, f)));
  assert.deepEqual(seqs(spans), expected(3), `${label}: each span once`);
  assert.deepEqual(
    fs.readdirSync(stores.files.outboxDir).filter((f) => f.endsWith('.jsonl')),
    [],
    `${label}: the outbox is empty after the drain`,
  );
}

test(`SIGKILL the gateway at random points, 20 runs (seed ${SEED})`, async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  const rand = random(SEED);
  for (let run = 0; run < 20; run++) {
    const label = `seed ${SEED}, run ${run}`;
    const root = tmpDir(t, `kill-gw-${run}`);
    const stores = openLocalStores({ root });
    const ingest = createIngest(stores.sinks);
    let stream = null;
    let kills = 0;
    const args = ['--outbox', stores.files.outboxDir, '--emit', String(ROUNDS), '--emit-every-ms', '1'];
    while (!stream || ingest.mark(stream) < TOTAL) {
      const s = startSession({
        bin,
        args: [...args, '--segment-events', '25'],
        cpVersion: 'kill-test',
        initial: initial(),
        log: () => {},
        onEvent: (msg, st) => ingest.handle(msg, st),
      });
      await Promise.race([sleep(Math.floor(rand() * 250)), s.exited]);
      stream = s.stream || stream;
      killHard(s.pid);
      await s.exited;
      kills++;
      assert.ok(kills < 100, `${label}: the stream never completed`);
    }
    // A last, clean run acknowledges everything: the outbox drains.
    const s = startSession({
      bin,
      args,
      cpVersion: 'kill-test',
      initial: initial(),
      log: () => {},
      onEvent: (m, st) => ingest.handle(m, st),
    });
    await s.ready;
    assert.equal((await s.shutdown(2000)).code, 0, label);
    checkRun(root, `${label} (${kills} kills)`);
  }
});

function runtimeArgs(script) {
  // Under Deno a child is started with `deno run` and the test set, never as a bare script (which would hold every
  // permission, P2 finding F10).
  return process.versions.deno
    ? [process.execPath, ['run', '-q', '--no-check', '--no-prompt', '-P=test', script]]
    : [process.execPath, [script]];
}

test(`SIGKILL the control plane at random points, 5 runs (seed ${SEED})`, async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  const rand = random(SEED + 1);
  const script = path.join(__dirname, 'fixtures', 'ipc', 'cp-child.js');
  const [cmd, base] = runtimeArgs(script);
  let totalKills = 0;
  for (let run = 0; run < 5; run++) {
    const label = `seed ${SEED + 1}, control plane run ${run}`;
    const root = tmpDir(t, `kill-cp-${run}`);
    fs.writeFileSync(path.join(root, 'initial.json'), JSON.stringify(initial()));
    let done = false;
    let kills = 0;
    const pidFile = path.join(root, 'gw.pid');
    const usageFile = openLocalStores({ root }).files.usageFile;
    // The pid file is written in two steps (created, then filled): a pid counts once it has digits.
    const stubPid = () => (fs.existsSync(pidFile) ? Number(fs.readFileSync(pidFile, 'utf8')) || 0 : 0);
    while (!done) {
      fs.rmSync(pidFile, { force: true });
      const child = spawn(cmd, [...base, root, bin, String(ROUNDS)], {
        cwd: path.join(__dirname, '..'),
        stdio: ['ignore', 'pipe', 'inherit'],
      });
      let out = '';
      child.stdout.on('data', (c) => (out += c));
      const exited = new Promise((r) => child.once('exit', (code) => r(code)));
      // The kill point is a count of stored usage lines, not a delay: the control plane stores the whole run in about
      // 150 ms, so a wall-clock delay would mostly kill nothing (the task brief's 150-750 ms did exactly that), and a
      // slow runtime start (deno run under load) would make every kill land before any work. The target is random in
      // (stored so far, ROUNDS - 1]; a run with one usage line left is let finish. A little random jitter follows.
      let code;
      exited.then((c) => (code = c ?? -1));
      const stored = () => (fs.existsSync(usageFile) ? fs.readFileSync(usageFile, 'utf8').split('\n').length - 1 : 0);
      const remaining = ROUNDS - stored();
      if (remaining > 1) {
        const target = ROUNDS - remaining + 1 + Math.floor(rand() * (remaining - 1));
        await waitFor(() => code !== undefined || stored() >= target, 30000, `${label}: ${target} usage lines stored`);
        if (code === undefined) await Promise.race([sleep(Math.floor(rand() * 30)), exited]);
        if (code === undefined) code = null;
      } else await waitFor(() => code !== undefined, 30000, `${label}: the last run to finish`);
      if (code === null) {
        killHard(child.pid);
        await exited;
        kills++;
      }
      // The stub sees its stdin close and exits; never start a second one on the same outbox while it lives.
      const pid = stubPid();
      if (pid > 0) {
        await waitFor(() => !alive(pid), 10000, `${label}: the stub ${pid} to exit after its control plane died`);
      }
      done = code === 0 && out.includes('done 0');
      assert.ok(kills < 50, `${label}: the run never completed`);
    }
    checkRun(root, `${label} (${kills} kills)`);
    totalKills += kills;
  }
  assert.ok(totalKills > 0, `seed ${SEED + 1}: no run killed the control plane, so the test proved nothing`);
});
