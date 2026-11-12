// P4f entry 4: the marks are read again when a new session starts, so a second server on the same data folder cannot
// make this one store again what that one stored; and a server never lowers a mark another one wrote to the mark file.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const { createIngest } = require('../lib/ipc/ingest');
const { recoverMarks } = require('../lib/ipc/marks');
const { createMarkFile, readMarkFile } = require('../lib/ipc/markfile');
const { openLocalStores } = require('../lib/ipc/local');
const { synthetic, tmpDir } = require('./ipc-stub');

const S = '0f1e2d3c4b5a69788796a5b4c3d2e1f0';
const T = 'f'.repeat(32);
const usage = (seq) => ({ v: 1, type: 'usage.report', seq, ts: 0, body: synthetic(1)[1] });
const lines = (file) => fs.readFileSync(file, 'utf8').split('\n').filter(Boolean);

test('a new session reads the marks again: what another server stored meanwhile is a duplicate here', (t) => {
  const root = tmpDir(t, 'reread');
  const a = openLocalStores({ root });
  const first = createIngest({ ...a.sinks, marks: recoverMarks(a.files), log: () => {} });
  first.seed(S, 0);
  for (let seq = 1; seq <= 3; seq++) assert.equal(first.handle(usage(seq), S), true);
  // A second server on the same folder stores 4 and 5 of the same stream.
  const b = openLocalStores({ root });
  const second = createIngest({ ...b.sinks, marks: recoverMarks(b.files), log: () => {} });
  second.seed(S, 3);
  for (let seq = 4; seq <= 5; seq++) assert.equal(second.handle(usage(seq), S), true);
  assert.equal(first.mark(S), 3, 'memory alone is stale');
  first.seed(S, 0); // the next session's hello (the outbox was not acknowledged past 0)
  assert.equal(first.mark(S), 5);
  assert.equal(first.handle(usage(4), S), false, 'a duplicate, not stored again');
  assert.equal(lines(a.files.usageFile).length, 5);
});

test('the re-read includes the mark file; it only raises marks', (t) => {
  const root = tmpDir(t, 'rereadfile');
  const s = openLocalStores({ root });
  const ingest = createIngest({ ...s.sinks, marks: recoverMarks(s.files), log: () => {} });
  ingest.seed(T, 9);
  createMarkFile(s.files.marksFile).save(S, 7);
  ingest.seed(T, 0);
  assert.equal(ingest.mark(S), 7);
  assert.equal(ingest.mark(T), 9, 'never lowered');
});

test('a re-read that fails is logged once and the seed still applies', () => {
  const marks = new Map([[S, 2]]);
  Object.defineProperty(marks, 'reread', {
    value: () => {
      throw new Error('EIO');
    },
  });
  const logged = [];
  const sinks = { recordUsage: () => {}, recordGovernance: () => {}, recordMcp: () => {}, writeSpans: () => {} };
  const ingest = createIngest({ ...sinks, marks, log: (l) => logged.push(l) });
  ingest.seed(S, 4);
  ingest.seed(S, 5);
  assert.equal(ingest.mark(S), 5);
  assert.equal(logged.length, 1);
  assert.match(logged[0], /EIO/);
});

test('a store failure waiting on a seq is cleared when the re-read shows it stored', (t) => {
  const root = tmpDir(t, 'unstored');
  const s = openLocalStores({ root });
  let fail = true;
  const ingest = createIngest({
    ...s.sinks,
    recordUsage: (e) => {
      if (fail) throw new Error('disk full');
      return s.sinks.recordUsage(e);
    },
    marks: recoverMarks(s.files),
    log: () => {},
  });
  assert.throws(() => ingest.handle(usage(1), S), /disk full/);
  fail = false;
  createMarkFile(s.files.marksFile).save(S, 1); // another server stored seq 1
  ingest.seed(S, 0);
  assert.equal(ingest.mark(S), 1);
  assert.equal(ingest.handle(usage(2), S), true, 'seq 2 no longer waits for seq 1');
});

test('recoverMarks: reread is not an entry and runs the same recovery again', (t) => {
  const root = tmpDir(t, 'rr');
  const s = openLocalStores({ root });
  const marks = recoverMarks(s.files);
  assert.deepEqual([...marks], []);
  assert.deepEqual(Object.keys(marks), []);
  createMarkFile(s.files.marksFile).save(S, 3);
  assert.deepEqual([...marks.reread()], [[S, 3]]);
  assert.equal(typeof marks.reread().reread, 'function');
});

test('the mark file: with one writer at a time, a server never lowers or drops a mark another server wrote', (t) => {
  const file = path.join(tmpDir(t, 'shared'), '.data', 'ipc-marks.json');
  const one = createMarkFile(file);
  const two = createMarkFile(file); // read before one wrote anything
  one.save(S, 5);
  two.save(T, 2);
  assert.deepEqual(Object.fromEntries(readMarkFile(file)), { [S]: 5, [T]: 2 });
  two.save(S, 3);
  assert.equal(readMarkFile(file).get(S), 5);
  assert.deepEqual(Object.fromEntries(two.get()), { [S]: 5, [T]: 2 });
});

test('the mark file: a failed write leaves the marks in memory at what the file holds', (t) => {
  const dir = tmpDir(t, 'failwrite');
  const file = path.join(dir, '.data', 'ipc-marks.json');
  const m = createMarkFile(file);
  m.save(S, 2);
  fs.rmSync(file);
  fs.mkdirSync(file); // the rename onto the mark file fails
  assert.throws(() => m.save(S, 4));
  assert.equal(m.get().get(S), 2);
  assert.deepEqual(
    fs.readdirSync(path.dirname(file)).filter((f) => f.endsWith('.tmp')),
    [],
    'no temp file is left behind',
  );
});

test('the mark file: the temp file is unique per process and per save', (t) => {
  const file = path.join(tmpDir(t, 'tmpname'), '.data', 'ipc-marks.json');
  const seen = [];
  const rename = fs.renameSync;
  fs.renameSync = (from, to) => {
    seen.push(path.basename(String(from)));
    return rename(from, to);
  };
  try {
    const m = createMarkFile(file);
    m.save(S, 1);
    m.save(S, 2);
  } finally {
    fs.renameSync = rename;
  }
  assert.equal(seen.length, 2);
  assert.notEqual(seen[0], seen[1]);
  for (const name of seen) assert.match(name, new RegExp(`^ipc-marks\\.json\\.${process.pid}\\.[0-9a-f]{12}\\.tmp$`));
});

// The per-session re-read travels as recoverMarks' reread(), a property of the Map itself (no wiring file changes in
// P4 stage 1). A copy (new Map(...), a spread, structuredClone) drops it silently, so every place that builds the
// ingest for real must pass recoverMarks' own Map. This test fails when one of them copies it or passes other marks.
test('the wiring passes the Map recoverMarks returned to createIngest, so the per-session re-read is kept', () => {
  const root = path.join(__dirname, '..');
  const calls = [];
  for (const file of ['server.js', 'scripts/gw-dev.js']) {
    const src = fs.readFileSync(path.join(root, file), 'utf8');
    for (const m of src.matchAll(/createIngest\(\{(.*?)\}\)/g)) calls.push({ file, args: m[1] });
  }
  assert.ok(
    calls.some((c) => c.file === 'server.js'),
    'server.js builds the ingest',
  );
  for (const { file, args } of calls) {
    const marks = /(?:^|[\s,{])marks:\s*([^,}]+)/.exec(args);
    assert.ok(marks, `${file}: createIngest gets marks`);
    assert.match(marks[1].trim(), /^recoverMarks\([^()]*\)$/, `${file}: marks must be recoverMarks(...) itself`);
  }
  // What the check guards against: a copy loses reread(), and the ingest then never reads the marks again.
  const sinks = { recordUsage: () => {}, recordGovernance: () => {}, recordMcp: () => {}, writeSpans: () => {} };
  let reads = 0;
  const own = new Map();
  Object.defineProperty(own, 'reread', { value: () => (reads++, new Map()) });
  createIngest({ ...sinks, marks: own }).seed(S, 0);
  createIngest({ ...sinks, marks: new Map(own) }).seed(S, 0);
  assert.equal(reads, 1);
});
