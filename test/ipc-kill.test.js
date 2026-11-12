// Kill tests (P3 T6): SIGKILL (taskkill /F on Windows) the gateway stub at random points of an --emit stream and
// restart it, 20 runs; then SIGKILL the control-plane side (a child process running test/fixtures/ipc/cp-child.js
// under this runtime) and restart it. After each run: every usage line stored exactly once (no loss, no duplicate),
// the governance chain verifies, the MCP audit log and the trace store hold each event once, and the outbox is empty.
// The random points come from a seeded generator, one per test, both seeded with ZP_KILL_SEED: every test name and
// every failure message names the ZP_KILL_SEED=<n> that replays it (0 is a seed too). A replay draws the same kill
// points in the same order, not the same interleaving: how many restarts a run needs, and how far the stub or the
// control plane got when it was killed, depend on timing (the control plane's target is relative to what is stored when
// it starts). A seed reproduces the first kill point of a run exactly and the later ones only roughly.
// A test that fails or times out kills the control plane and the stub it started, so the file never hangs. On Windows
// taskkill /F /T kills the control plane's tree, stub included; the "stub sees its stdin close" exit is exercised on
// POSIX only.
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
const given = process.env.ZP_KILL_SEED;
const SEED = given !== undefined && given !== '' && Number.isFinite(Number(given)) ? Number(given) : Date.now() % 1e9;
const REPLAY = `ZP_KILL_SEED=${SEED}`;

/** Rejects when `promise` has not settled in `ms`; the timer never keeps the process alive. */
function within(promise, ms, what) {
  let timer;
  const late = new Promise((_, reject) => {
    timer = setTimeout(() => reject(new Error(`timed out after ${ms} ms waiting for ${what}`)), ms);
    timer.unref();
  });
  return Promise.race([promise, late]).finally(() => clearTimeout(timer));
}

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

test(`SIGKILL the gateway at random points, 20 runs (replay: ${REPLAY})`, async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  const rand = random(SEED);
  let current = null; // the session whose stub is alive, killed when the test fails
  let midStream = 0; // kills that landed with some events stored and not all
  try {
    for (let run = 0; run < 20; run++) {
      const label = `${REPLAY}, run ${run}`;
      const root = tmpDir(t, `kill-gw-${run}`);
      const stores = openLocalStores({ root });
      const ingest = createIngest(stores.sinks);
      let stream = null;
      let kills = 0;
      const args = ['--outbox', stores.files.outboxDir, '--emit', String(ROUNDS), '--emit-every-ms', '1'];
      while (!stream || ingest.mark(stream) < TOTAL) {
        const s = (current = startSession({
          bin,
          args: [...args, '--segment-events', '25'],
          cpVersion: 'kill-test',
          initial: initial(),
          log: () => {},
          onEvent: (msg, st) => ingest.handle(msg, st),
        }));
        await Promise.race([sleep(Math.floor(rand() * 250)), s.exited]);
        stream = s.stream || stream;
        const stored = stream ? ingest.mark(stream) : 0;
        killHard(s.pid);
        await within(s.exited, 10000, `${label}: the stub ${s.pid} to exit after the kill`);
        kills++;
        if (stored > 0 && stored < TOTAL) midStream++;
        assert.ok(kills < 100, `${label}: the stream never completed`);
      }
      // A last, clean run acknowledges everything: the outbox drains.
      const s = (current = startSession({
        bin,
        args,
        cpVersion: 'kill-test',
        initial: initial(),
        log: () => {},
        onEvent: (m, st) => ingest.handle(m, st),
      }));
      await within(s.ready, 10000, `${label}: the last stub to be ready`);
      assert.equal((await within(s.shutdown(2000), 10000, `${label}: the last stub to shut down`)).code, 0, label);
      checkRun(root, `${label} (${kills} kills)`);
    }
    assert.ok(midStream > 0, `${REPLAY}: no kill landed in the middle of a stream, so the test proved nothing`);
  } finally {
    if (current) killHard(current.pid);
  }
});

function runtimeArgs(script) {
  // Under Deno a child is started with `deno run` and the test set, never as a bare script (which would hold every
  // permission, P2 finding F10).
  return process.versions.deno
    ? [process.execPath, ['run', '-q', '--no-check', '--no-prompt', '-P=test', script]]
    : [process.execPath, [script]];
}

test(`SIGKILL the control plane at random points, 5 runs (replay: ${REPLAY})`, async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  const rand = random(SEED);
  const script = path.join(__dirname, 'fixtures', 'ipc', 'cp-child.js');
  const [cmd, base] = runtimeArgs(script);
  let totalKills = 0;
  let child = null;
  let pidFile = null;
  // The pid file is written in two steps (created, then filled): a pid counts once it has digits.
  const stubPid = () => (pidFile && fs.existsSync(pidFile) ? Number(fs.readFileSync(pidFile, 'utf8')) || 0 : 0);
  try {
    for (let run = 0; run < 5; run++) {
      const label = `${REPLAY}, control plane run ${run}`;
      const root = tmpDir(t, `kill-cp-${run}`);
      fs.writeFileSync(path.join(root, 'initial.json'), JSON.stringify(initial()));
      let done = false;
      let kills = 0;
      let attempts = 0;
      pidFile = path.join(root, 'gw.pid');
      const usageFile = openLocalStores({ root }).files.usageFile;
      const stored = () => (fs.existsSync(usageFile) ? fs.readFileSync(usageFile, 'utf8').split('\n').length - 1 : 0);
      while (!done) {
        assert.ok(++attempts <= 60, `${label}: the run never completed after ${attempts - 1} control planes`);
        fs.rmSync(pidFile, { force: true });
        child = spawn(cmd, [...base, root, bin, String(ROUNDS)], {
          cwd: path.join(__dirname, '..'),
          stdio: ['ignore', 'pipe', 'pipe'],
        });
        let out = '';
        let err = '';
        child.stdout.on('data', (c) => (out += c));
        child.stderr.on('data', (c) => (err = (err + c).slice(-4000)));
        // 'close', not 'exit': it comes after stdout is drained, so "done 0" is in `out` when the child exited cleanly.
        const exited = new Promise((r) => child.once('close', (code, signal) => r({ code, signal })));
        let exit = null;
        exited.then((e) => (exit = e));
        // Both draws are made every time, so a seed gives the same sequence of draws however the run goes.
        const pick = rand();
        const jitter = rand();
        // The kill point is a count of stored usage lines, not a delay: the control plane stores the whole run in about
        // 150 ms, so a wall-clock delay would mostly kill nothing (the task brief's 150-750 ms did exactly that), and a
        // slow runtime start (deno run under load) would make every kill land before any work. The target is random in
        // (stored so far, ROUNDS - 1]; a run with one usage line left is let finish. A little random jitter follows.
        const remaining = ROUNDS - stored();
        let killed = false;
        if (remaining > 1) {
          const target = ROUNDS - remaining + 1 + Math.floor(pick * (remaining - 1));
          await waitFor(() => exit || stored() >= target, 30000, `${label}: ${target} usage lines stored`);
          if (!exit) await Promise.race([sleep(Math.floor(jitter * 30)), exited]);
          if (!exit && child.exitCode === null && child.signalCode === null) {
            killHard(child.pid);
            killed = true;
          }
        }
        const { code, signal } = await within(exited, 30000, `${label}: the control plane ${child.pid} to exit`);
        const finished = code === 0 && out.includes('done 0');
        // A kill that found the child finished already is no kill.
        if (killed && !finished) kills++;
        else if (!finished)
          assert.fail(
            `${label}: the control plane exited by itself with code ${code}, signal ${signal}, ` +
              `stdout ${JSON.stringify(out)}, stderr:\n${err}`,
          );
        // The stub sees its stdin close and exits; never start a second one on the same outbox while it lives.
        const pid = stubPid();
        if (pid > 0)
          await waitFor(() => !alive(pid), 10000, `${label}: the stub ${pid} to exit after its control plane`);
        done = finished;
      }
      checkRun(root, `${label} (${kills} kills)`);
      totalKills += kills;
    }
    assert.ok(totalKills > 0, `${REPLAY}: no run killed the control plane, so the test proved nothing`);
  } finally {
    if (child) killHard(child.pid);
    killHard(stubPid());
  }
});
