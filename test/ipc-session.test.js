// The control plane starts the Go stub (gw/cmd/zaafpulse-gw) and runs the v1 handshake with it (P3 T3): hello,
// hello.ok, config.push, secrets.handoff and budget.snapshot lead to ready; health arrives; shutdown exits 0; a
// gateway offering another protocol is refused and exits 2; closing its stdin makes it exit 0; a bad line from it is
// a protocol error that kills it. Every message the stub sends is checked strictly against ipc/v1.
const test = require('node:test');
const assert = require('node:assert/strict');
const path = require('path');
const { startSession } = require('../lib/ipc/session');
const { stubBin, initial, waitFor, tmpDir } = require('./ipc-stub');

function start(t, bin, extraArgs = [], opts = {}) {
  const root = tmpDir(t, 'session');
  const logs = [];
  const health = [];
  const s = startSession({
    bin,
    args: ['--outbox', path.join(root, 'gw-outbox'), '--health-ms', '50', ...extraArgs],
    cpVersion: 'test',
    initial: initial(),
    strict: true,
    onHealth: (h) => health.push(h),
    log: (l) => logs.push(l),
    ...opts,
  });
  t.after(() => s.kill());
  return { s, logs, health };
}

test('handshake, config, secrets and snapshot lead to ready; health arrives; shutdown exits 0', async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  const { s, logs, health } = start(t, bin);
  assert.deepEqual(await s.ready, { gen: 1 });
  assert.match(s.stream, /^[0-9a-f]{32}$/);
  await waitFor(() => health.length >= 2, 5000, 'two health messages');
  const h = health.at(-1);
  assert.equal(h.gen, 1);
  assert.equal(h.outboxDepth, 0);
  assert.ok(h.snapshotAgeMs >= 0 && h.snapshotAgeMs < 5000, `snapshot age ${h.snapshotAgeMs}`);
  assert.deepEqual(await s.shutdown(1000), { code: 0, signal: null });
  assert.ok(
    logs.some((l) => l.startsWith('gw: ready (config generation 1)')),
    logs.join('\n'),
  );
});

test('a gateway that offers only another protocol is refused and exits 2', async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  const { s, logs } = start(t, bin, ['--protocol', '2']);
  await assert.rejects(s.ready, /protocol mismatch/);
  assert.deepEqual(await s.exited, { code: 2, signal: null });
  // The 'exit' event can come before the last stderr lines are read (it did on macOS under Deno): wait for the line.
  await waitFor(
    () => logs.some((l) => l.includes('refused by the control plane')),
    5000,
    'the refusal in the gateway log',
  );
});

test('the gateway exits 0 when its stdin closes (the control plane is gone)', async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  const { s } = start(t, bin);
  await s.ready;
  s.close();
  assert.deepEqual(await s.exited, { code: 0, signal: null });
});

test('a line from the gateway that is not JSON is a protocol error: the gateway is killed', async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  const { s, logs } = start(t, bin, ['--bad-line']);
  await s.ready;
  const r = await s.exited;
  assert.notEqual(r.code, 0);
  assert.deepEqual(s.failure, { kind: 'protocol', message: 'protocol error: a line is not JSON' });
  assert.ok(
    logs.some((l) => l === 'gw: protocol error: a line is not JSON'),
    logs.join('\n'),
  );
});

// A fake gateway process: the session reads what it writes to stdout, and kill ends it.
function fakeChild() {
  const { EventEmitter } = require('events');
  const { PassThrough } = require('stream');
  const child = Object.assign(new EventEmitter(), {
    pid: 1,
    stdin: new PassThrough(),
    stdout: new PassThrough(),
    stderr: new PassThrough(),
    killed: false,
    kill() {
      if (child.killed) return;
      child.killed = true;
      // As a real child: 'exit', then 'close' once its pipes are done.
      setImmediate(() => {
        child.emit('exit', null, 'SIGKILL');
        child.emit('close', null, 'SIGKILL');
      });
    },
  });
  return child;
}

test('an event that cannot be stored ends the session: the events read after it are dropped, nothing is acked', async () => {
  const child = fakeChild();
  const sent = [];
  child.stdin.on('data', (c) => {
    for (const l of c.toString().split('\n')) if (l) sent.push(JSON.parse(l));
  });
  const seen = [];
  const logs = [];
  const s = startSession({
    bin: 'fake',
    cpVersion: 'test',
    initial: initial(),
    spawnImpl: () => child,
    log: (l) => logs.push(l),
    ackEveryMs: 1,
    onEvent: (msg) => {
      seen.push(msg.seq);
      throw new Error('disk full');
    },
  });
  const line = (type, seq, body) => JSON.stringify({ v: 1, type, seq, ts: 0, body }) + '\n';
  const S = '0f1e2d3c4b5a69788796a5b4c3d2e1f0';
  child.stdout.write(line('hello', 1, { protocol: [1], gwVersion: 't', stream: S, outboxFrom: 1 }));
  const ev = { log: 'mcp', event: { ts: 1, integration: 'i', tool: 't', ok: true, ms: 1, args: [] } };
  child.stdout.write(line('audit.event', 2, ev) + line('audit.event', 3, ev) + line('audit.event', 4, ev));
  await assert.rejects(s.ready, /could not be stored/);
  await s.exited;
  await new Promise((r) => setTimeout(r, 20));
  assert.deepEqual(seen, [2], 'the events after the failed one are not handed on');
  assert.equal(s.failure.kind, 'store', 'the session says why it stopped');
  assert.match(s.failure.message, /disk full/);
  assert.equal(sent.filter((m) => m.type === 'ack').length, 0);
  assert.equal(logs.filter((l) => /could not be stored/.test(l)).length, 1, logs.join('\n'));
});
