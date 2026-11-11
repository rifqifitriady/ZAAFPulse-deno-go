// The control plane stores the gateway's events (P3 T4): through the in-process writers, with an ipc mark in every
// stored line, duplicates skipped by (stream, seq), marks recovered from the stores' tails after a restart. Against
// the Go stub: a resend after the gateway restarts, or after the control plane restarts, stores each event exactly
// once; the stored usage lines, the budget reports, the governance chain, the MCP audit log and the trace store equal
// an in-process run of the same events; acknowledged segments leave the outbox.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const { createIngest } = require('../lib/ipc/ingest');
const { recoverMarks, marksIn, tailLines } = require('../lib/ipc/marks');
const { openLocalStores } = require('../lib/ipc/local');
const { startSession } = require('../lib/ipc/session');
const { fromNano } = require('../lib/money');
const { stubBin, initial, synthetic, waitFor, tmpDir } = require('./ipc-stub');

const S = '0f1e2d3c4b5a69788796a5b4c3d2e1f0';
const ROUNDS = 20; // 60 events
const TOTAL = 3 * ROUNDS;
const msgOf = (seq) => {
  const [type, body] = synthetic(seq);
  return { v: 1, type, seq, ts: 0, body };
};
const readJsonl = (file) =>
  fs.existsSync(file)
    ? fs
        .readFileSync(file, 'utf8')
        .split('\n')
        .filter(Boolean)
        .map((l) => JSON.parse(l))
    : [];
const traceSpans = (dir) =>
  fs.existsSync(dir)
    ? fs
        .readdirSync(dir)
        .filter((f) => f.endsWith('.ndjson'))
        .flatMap((f) => readJsonl(path.join(dir, f)))
    : [];
const without = (e, ...keys) => Object.fromEntries(Object.entries(e).filter(([k]) => !keys.includes(k)));
const CHAIN = ['seq', 'ts', 'prev', 'mac', 'keyed'];
const governanceEntries = (dir) =>
  fs
    .readdirSync(dir)
    .filter((f) => /^governance-.*\.jsonl$/.test(f))
    .sort()
    .flatMap((f) => readJsonl(path.join(dir, f)));

function recording() {
  const calls = { usage: [], governance: [], mcp: [], spans: [] };
  const sinks = {
    recordUsage: (e) => calls.usage.push(e),
    recordGovernance: (e) => calls.governance.push(e),
    recordMcp: (e) => calls.mcp.push(e),
    writeSpans: (l) => calls.spans.push(...l),
  };
  return { calls, sinks };
}

test('each event goes to its writer with an ipc mark; cost back to USD; a resend is skipped', () => {
  const { calls, sinks } = recording();
  const ingest = createIngest({ ...sinks, now: () => 42 });
  for (let seq = 1; seq <= 6; seq++) assert.equal(ingest.handle(msgOf(seq), S), true);
  assert.equal(calls.usage.length, 2);
  assert.equal(calls.usage[0].cost, 0.0015);
  assert.deepEqual(calls.usage[0].ipc, { stream: S, seq: 1 });
  assert.equal(calls.governance.length, 1, 'round 1: governance');
  assert.equal(calls.mcp.length, 1, 'round 2: mcp');
  assert.deepEqual(calls.governance[0].ipc, { stream: S, seq: 2 });
  assert.deepEqual(calls.spans[1].ipc, { stream: S, seq: 6 });
  assert.equal(ingest.mark(S), 6);
  assert.equal(ingest.handle(msgOf(4), S), false, 'at or below the mark: a duplicate');
  assert.equal(calls.usage.length, 2);
  const unchecked = { ...msgOf(7), body: { ...msgOf(7).body, budget: 'unchecked' } };
  ingest.handle(unchecked, S);
  assert.equal(calls.usage[2].budget, 'unchecked');
  assert.deepEqual(ingest.stats(), { stored: 7, duplicates: 1, unchecked: 1, lastUncheckedAt: 42 });
  assert.throws(() => ingest.handle({ type: 'health', seq: 8, body: {} }, S), /not an event/);
});

test('a writer that throws leaves the mark where it was, so the event is not acknowledged', () => {
  const { sinks } = recording();
  const ingest = createIngest({
    ...sinks,
    recordUsage: () => {
      throw new Error('disk full');
    },
  });
  assert.throws(() => ingest.handle(msgOf(1), S), /disk full/);
  assert.equal(ingest.mark(S), 0);
});

test('marks: the highest seq per stream from the stores, rotated usage log included; a cut last line is ignored', (t) => {
  const root = tmpDir(t, 'marks');
  const stores = openLocalStores({ root });
  const other = 'a'.repeat(32);
  const ingest = createIngest(stores.sinks);
  for (let seq = 1; seq <= 9; seq++) ingest.handle(msgOf(seq), S);
  ingest.handle(msgOf(1), other);
  fs.renameSync(stores.files.usageFile, `${stores.files.usageFile}.1`);
  fs.appendFileSync(stores.files.usageFile, `{"ts":1,"keyId":"dashboard","cost":0}\n{"ipc":{"stream":"${S}","seq":99`);
  const marks = recoverMarks(stores.files);
  assert.equal(marks.get(S), 9, 'trace.span seq 9 is the newest');
  assert.equal(marks.get(other), 1);
  assert.deepEqual(tailLines(stores.files.usageFile), ['{"ts":1,"keyId":"dashboard","cost":0}']);
  assert.deepEqual([...marksIn(['not json', '{"ipc":{"stream":"x","seq":"3"}}'], new Map())], []);
});

// Runs the stub with --emit until every event of the run is stored, with acks held back so all of them stay in the
// outbox. Returns the stream id.
async function emitAll(t, bin, root, ingest, extra = {}) {
  const stores = extra.stores;
  const s = startSession({
    bin,
    args: ['--outbox', stores.files.outboxDir, '--emit', String(ROUNDS), '--segment-events', '25'],
    cpVersion: 'test',
    initial: initial(),
    strict: true,
    onEvent: (msg, stream) => ingest.handle(msg, stream),
    log: () => {},
    ackEveryMs: 1e9,
    ackMax: 1e9,
  });
  t.after(() => s.kill());
  await s.ready;
  await waitFor(() => s.stream && ingest.mark(s.stream) === TOTAL, 10000, `${TOTAL} events stored`);
  return s;
}

test('the gateway restarts without acks: everything is resent and stored once (stub)', async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  const root = tmpDir(t, 'resend');
  const stores = openLocalStores({ root });
  const ingest = createIngest(stores.sinks);
  const first = await emitAll(t, bin, root, ingest, { stores });
  first.kill(); // no ack was sent
  await first.exited;
  assert.equal(ingest.stats().duplicates, 0);
  const again = startSession({
    bin,
    args: ['--outbox', stores.files.outboxDir, '--emit', String(ROUNDS), '--segment-events', '25'],
    cpVersion: 'test',
    initial: initial(),
    strict: true,
    onEvent: (msg, stream) => ingest.handle(msg, stream),
    log: () => {},
  });
  t.after(() => again.kill());
  await again.ready;
  assert.equal(again.stream, first.stream, 'the same durable stream');
  assert.equal(ingest.stats().duplicates, TOTAL, 'all of them came again, before ready');
  assert.deepEqual(await again.shutdown(2000), { code: 0, signal: null });
  const usage = readJsonl(stores.files.usageFile);
  assert.equal(usage.length, ROUNDS);
  assert.deepEqual(
    usage.map((e) => e.ipc.seq),
    Array.from({ length: ROUNDS }, (_, i) => 3 * i + 1),
  );
  assert.deepEqual(
    fs.readdirSync(stores.files.outboxDir).filter((f) => f.endsWith('.jsonl')),
    [],
    'every segment is acknowledged and deleted',
  );
});

test('the control plane restarts: marks from the stores make the resend a no-op (stub)', async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  const root = tmpDir(t, 'cp-restart');
  const stores = openLocalStores({ root });
  const first = await emitAll(t, bin, root, createIngest(stores.sinks), { stores });
  first.kill();
  await first.exited;
  // A new control plane: new stores and a new ingest, its marks read from the files.
  const stores2 = openLocalStores({ root });
  const ingest2 = createIngest({ ...stores2.sinks, marks: recoverMarks(stores2.files) });
  assert.equal(ingest2.mark(first.stream), TOTAL);
  const again = startSession({
    bin,
    args: ['--outbox', stores2.files.outboxDir],
    cpVersion: 'test',
    initial: initial(),
    strict: true,
    onEvent: (msg, stream) => ingest2.handle(msg, stream),
    log: () => {},
  });
  t.after(() => again.kill());
  await again.ready;
  await again.shutdown(2000);
  assert.equal(ingest2.stats().duplicates, TOTAL);
  assert.equal(ingest2.stats().stored, 0);
  assert.equal(readJsonl(stores2.files.usageFile).length, ROUNDS);
  assert.equal(stores2.governance.verify().ok, true);
});

test('stored through the IPC equals the same events recorded in process (stub)', async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  // In process: the gateway's own calls, as lib/gateway/index.js and lib/integrations.js make them.
  const local = tmpDir(t, 'in-process');
  const budgetLocal = [];
  const a = openLocalStores({ root: local, onUsage: (e) => budgetLocal.push(e) });
  for (let seq = 1; seq <= TOTAL; seq++) {
    const [type, body] = synthetic(seq);
    if (type === 'usage.report') a.sinks.recordUsage({ ...body.record, cost: fromNano(body.record.cost) });
    else if (type === 'audit.event' && body.log === 'governance') a.sinks.recordGovernance(body.event);
    else if (type === 'audit.event') a.sinks.recordMcp(body.event);
    else a.sinks.writeSpans([body.span]);
  }
  // Over the IPC.
  const remote = tmpDir(t, 'over-ipc');
  const budgetIpc = [];
  const b = openLocalStores({ root: remote, onUsage: (e) => budgetIpc.push(e) });
  const s = await emitAll(t, bin, remote, createIngest(b.sinks), { stores: b });
  await s.shutdown(2000);

  const strip = (list) => list.map((e) => without(e, 'ipc', 'budget'));
  assert.deepEqual(
    strip(readJsonl(b.files.usageFile)),
    strip(readJsonl(a.files.usageFile)),
    'usage lines (ledger input)',
  );
  assert.deepEqual(strip(budgetIpc), strip(budgetLocal), 'budget reports');
  assert.deepEqual(strip(readJsonl(b.files.mcpAuditFile)), strip(readJsonl(a.files.mcpAuditFile)), 'MCP audit');
  assert.deepEqual(strip(traceSpans(b.files.traceDir)), strip(traceSpans(a.files.traceDir)), 'trace store');
  const gov = (dir) => governanceEntries(dir).map((e) => without(e, ...CHAIN, 'ipc'));
  assert.deepEqual(gov(b.files.governanceDir), gov(a.files.governanceDir), 'governance entries');
  assert.equal(b.governance.verify().ok, true, 'the chain written from IPC events verifies');
});
