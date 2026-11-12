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

test('the mark file: a server never lowers or drops a mark another server wrote', (t) => {
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
  fs.mkdirSync(`${file}.tmp`); // the temp file cannot be written
  assert.throws(() => m.save(S, 4));
  assert.equal(m.get().get(S), 2);
});
