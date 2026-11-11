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
const { createRecorder } = require('../lib/trace/recorder');
const { jsonlLog } = require('../lib/gateway/store');
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

// Every store fails for real (its file or folder is replaced so the append cannot happen): the event is not marked,
// handle throws (the session does not acknowledge it), the failure is logged once per cause, and the same event is
// stored once the store works again (the resend after the restart). The in-process writers still swallow errors.
const BLOCKERS = {
  'usage.report': (f) => fs.mkdirSync(f.usageFile, { recursive: true }), // appending to a folder fails
  'audit.event:governance': (f) => {
    fs.rmSync(f.governanceDir, { recursive: true, force: true });
    fs.writeFileSync(f.governanceDir, 'not a folder'); // the audit key cannot be read
  },
  'audit.event:mcp': (f) => fs.mkdirSync(f.mcpAuditFile, { recursive: true }),
  'trace.span': (f) => {
    fs.rmSync(f.traceDir, { recursive: true, force: true });
    fs.writeFileSync(f.traceDir, 'not a folder');
  },
};
const SEQ_OF = { 'usage.report': 1, 'audit.event:governance': 2, 'audit.event:mcp': 5, 'trace.span': 3 };
for (const [sink, block] of Object.entries(BLOCKERS)) {
  test(`a ${sink} store that fails is not marked and not acknowledged; the resend stores it once`, (t) => {
    const root = tmpDir(t, 'strict');
    const stores = openLocalStores({ root });
    const logged = [];
    const ingest = createIngest({ ...stores.sinks, log: (l) => logged.push(l) });
    // The events before this one are stored first, so the failing event is the next one in seq order.
    const seq = SEQ_OF[sink];
    for (let s = 1; s < seq; s++) ingest.handle(msgOf(s), S);
    const backup = `${root}-backup`;
    fs.cpSync(root, backup, { recursive: true });
    t.after(() => fs.rmSync(backup, { recursive: true, force: true }));
    block(stores.files);
    assert.throws(() => ingest.handle(msgOf(seq), S));
    assert.throws(() => ingest.handle(msgOf(seq), S), 'the same failure again');
    assert.equal(ingest.mark(S), seq - 1, 'not marked');
    assert.equal(logged.length, 1, `logged once per cause: ${logged.join(' | ')}`);
    assert.match(logged[0], new RegExp(`${msgOf(seq).type}`));
    assert.throws(() => ingest.handle(msgOf(seq + 1), S), /before seq/, 'a later event waits for the unstored one');
    assert.equal(ingest.mark(S), seq - 1);
    // The store works again (the restart's resend): the event is stored once and marked.
    fs.rmSync(root, { recursive: true, force: true });
    fs.cpSync(backup, root, { recursive: true });
    const again = openLocalStores({ root });
    const resumed = createIngest({ ...again.sinks, marks: recoverMarks(again.files), log: () => {} });
    assert.equal(resumed.mark(S), seq - 1);
    assert.equal(resumed.handle(msgOf(seq), S), true);
    assert.equal(resumed.mark(S), seq);
    assert.equal(recoverMarks(again.files).get(S), seq, 'the mark is in the stored line');
  });
}

test('the in-process writers still swallow a failed write', (t) => {
  const root = tmpDir(t, 'lenient');
  const stores = openLocalStores({ root });
  fs.mkdirSync(stores.files.usageFile, { recursive: true });
  fs.mkdirSync(stores.files.mcpAuditFile, { recursive: true });
  assert.doesNotThrow(() => stores.usage.record({ keyId: 'k', model: 'm', cost: 0 }));
  assert.doesNotThrow(() => stores.mcpAudit.record({ tool: 't' }));
});

test('a post-store hook that throws neither undoes nor skips the mark; the event is acknowledged', () => {
  const { calls, sinks } = recording();
  const logged = [];
  const ingest = createIngest({
    ...sinks,
    recordUsage: (e) => (calls.usage.push(e), e),
    afterUsage: () => {
      throw new Error('budget report failed');
    },
    log: (l) => logged.push(l),
  });
  assert.equal(ingest.handle(msgOf(1), S), true, 'stored: the session acknowledges it');
  assert.equal(ingest.mark(S), 1);
  for (const seq of [2, 3]) ingest.handle(msgOf(seq), S);
  assert.equal(ingest.handle(msgOf(4), S), true);
  assert.equal(ingest.mark(S), 4);
  assert.equal(ingest.handle(msgOf(1), S), false, 'a resend is a duplicate, not stored twice');
  assert.equal(calls.usage.length, 2);
  assert.equal(logged.length, 1, 'logged once per cause');
  assert.match(logged[0], /budget report failed/);
});

test('local stores: the budget report runs after the usage line is stored and marked', (t) => {
  const root = tmpDir(t, 'hook');
  const seen = [];
  const stores = openLocalStores({
    root,
    onUsage: (entry) => {
      seen.push(entry.ipc.seq);
      throw new Error('budget report failed');
    },
  });
  const ingest = createIngest({ ...stores.sinks, log: () => {} });
  assert.equal(ingest.handle(msgOf(1), S), true);
  assert.deepEqual(seen, [1]);
  assert.equal(ingest.mark(S), 1);
  assert.equal(recoverMarks(stores.files).get(S), 1);
});

// Fails the next fs.readSync once. Right after a trace store append, that is the store's index scan (the append itself
// does not read); the ingest's own tail check reads after it, with the real readSync.
function failNextRead(t) {
  const realRead = fs.readSync;
  let fail = true;
  fs.readSync = (...a) => {
    if (fail) {
      fail = false;
      throw Object.assign(new Error('EIO: injected'), { code: 'EIO' });
    }
    return realRead(...a);
  };
  t.after(() => (fs.readSync = realRead));
}

test('trace index failure after a completed append: the span is in the newest segment, so it is stored once', (t) => {
  const root = tmpDir(t, 'trace-index');
  const stores = openLocalStores({ root });
  const logged = [];
  const ingest = createIngest({ ...stores.sinks, log: (l) => logged.push(l) });
  ingest.handle(msgOf(1), S);
  ingest.handle(msgOf(2), S);
  failNextRead(t);
  assert.throws(
    () => stores.traces.writeSpans([{ t: 'a'.repeat(32), s: 'b'.repeat(16) }]),
    /EIO/,
    'the trace store itself throws, as at aa78352',
  );
  failNextRead(t);
  assert.equal(ingest.handle(msgOf(3), S), true, 'stored: the session acknowledges it');
  assert.equal(ingest.mark(S), 3);
  assert.deepEqual(logged, []);
  const [type, body] = synthetic(3);
  assert.equal(type, 'trace.span');
  const stored = traceSpans(stores.files.traceDir).filter((sp) => sp.ipc && sp.ipc.stream === S && sp.ipc.seq === 3);
  assert.equal(stored.length, 1, 'on disk once');
  assert.equal(ingest.handle(msgOf(3), S), false, 'the resend is a duplicate');
  for (const seq of [4, 5, 6]) ingest.handle(msgOf(seq), S);
  assert.ok(stores.traces.getSpans(body.span.t), 'indexed by the next write');
});

test('writeSpans throws and the span is not in the newest segment: a store failure, not acknowledged', (t) => {
  const root = tmpDir(t, 'trace-nomark');
  const stores = openLocalStores({ root });
  const other = 'a'.repeat(32);
  const logged = [];
  let fail = false;
  const ingest = createIngest({
    ...stores.sinks,
    writeSpans: (list) => {
      if (fail) throw Object.assign(new Error('EIO: injected'), { code: 'EIO' });
      return stores.sinks.writeSpans(list);
    },
    log: (l) => logged.push(l),
  });
  ingest.handle(msgOf(3), other); // the same seq of another stream is in the segment: it must not count
  ingest.handle(msgOf(1), S);
  ingest.handle(msgOf(2), S);
  fail = true;
  assert.throws(() => ingest.handle(msgOf(3), S), /EIO/);
  assert.equal(ingest.mark(S), 2, 'not marked');
  assert.equal(logged.length, 1);
  assert.match(logged[0], /trace\.span could not be stored/);
  fail = false;
  assert.equal(ingest.handle(msgOf(3), S), true, 'the resend stores it');
  assert.equal(ingest.mark(S), 3);
});

test('product trace recorder: an index failure after the append counts the spans dropped and logs "traces: write failed"', (t) => {
  const root = tmpDir(t, 'trace-recorder');
  const stores = openLocalStores({ root });
  const logged = [];
  const rec = createRecorder({ store: stores.traces, flushMs: 0, log: (l) => logged.push(l) });
  rec.add({
    traceId: '4bf92f3577b34da6a3ce929d0e0e4736',
    spanId: '00f067aa0ba902b7',
    name: 'chat m',
    kind: 3,
    startNs: 1792642201123456789n,
    endNs: 1792642201223456789n,
    attributes: {},
    status: { code: 0, message: '' },
  });
  failNextRead(t);
  assert.equal(rec.flush(), 0);
  const st = rec.stats();
  assert.equal(st.written, 0);
  assert.equal(st.dropped, 1);
  assert.equal(st.writeErrors, 1);
  assert.equal(logged.length, 1, logged.join(' | '));
  assert.match(logged[0], /^traces: write failed: EIO/);
});

test('an audit.event with an unknown log is refused, not stored in the MCP log', () => {
  const { calls, sinks } = recording();
  const ingest = createIngest({ ...sinks, log: () => {} });
  const m = msgOf(2);
  assert.throws(() => ingest.handle({ ...m, body: { ...m.body, log: 'other' } }, S), /unknown log/);
  assert.equal(calls.mcp.length + calls.governance.length, 0);
  assert.equal(ingest.mark(S), 0);
});

test('a seq gap after a mark fails the event (not stored, not acknowledged); any first seq is accepted', () => {
  const { calls, sinks } = recording();
  const logged = [];
  const ingest = createIngest({ ...sinks, log: (l) => logged.push(l) });
  assert.equal(ingest.handle(msgOf(5), S), true, 'mark 0: the first seq may be anything (a replay from Acked+1)');
  assert.throws(() => ingest.handle(msgOf(7), S), /seq 7 of .* after seq 5/);
  assert.throws(() => ingest.handle(msgOf(7), S), /after seq 5/, 'again');
  assert.equal(ingest.mark(S), 5, 'not marked');
  const all = calls.mcp.length + calls.spans.length + calls.usage.length + calls.governance.length;
  assert.equal(all, 1, 'not stored');
  assert.equal(logged.length, 1, 'logged once per cause');
  assert.match(logged[0], /gap/);
  assert.equal(ingest.handle(msgOf(5), S), false, 'a resend at the mark is still a duplicate');
  assert.equal(ingest.handle(msgOf(6), S), true, 'the replay, in order');
  assert.equal(ingest.handle(msgOf(7), S), true);
  assert.equal(ingest.mark(S), 7);
  const resumed = createIngest({ ...sinks, marks: new Map([[S, 7]]), log: () => {} });
  assert.throws(() => resumed.handle(msgOf(9), S), /after seq 7/, 'a recovered mark counts too');
});

test('the writers take a null second argument as before: no TypeError, the default (lenient) path', (t) => {
  const root = tmpDir(t, 'null-opts');
  const stores = openLocalStores({ root });
  assert.doesNotThrow(() => stores.mcpAudit.record({ tool: 't' }, null));
  assert.doesNotThrow(() => stores.usage.record({ keyId: 'k', model: 'm', cost: 0 }, null));
  assert.equal(readJsonl(stores.files.mcpAuditFile).length, 1);
  assert.equal(readJsonl(stores.files.usageFile).length, 1);
  fs.rmSync(stores.files.usageFile);
  fs.rmSync(stores.files.mcpAuditFile);
  fs.mkdirSync(stores.files.usageFile, { recursive: true });
  fs.mkdirSync(stores.files.mcpAuditFile, { recursive: true });
  assert.doesNotThrow(() => stores.mcpAudit.record({ tool: 't' }, null), 'a failed append is swallowed');
  assert.doesNotThrow(() => stores.usage.record({ keyId: 'k', model: 'm', cost: 0 }, null));
  assert.doesNotThrow(() => jsonlLog(path.join(stores.files.usageFile, 'x')).append({}, null));
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
    if (type === 'usage.report')
      a.sinks.afterUsage(a.usage.record({ ...body.record, cost: fromNano(body.record.cost) }));
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
