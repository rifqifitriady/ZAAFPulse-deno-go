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
  assert.ok(
    logs.some((l) => l.includes('refused by the control plane')),
    logs.join('\n'),
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
  assert.ok(
    logs.some((l) => l === 'gw: protocol error: a line is not JSON'),
    logs.join('\n'),
  );
});
