// The egress log (P5 Decision 10): .data/egress.jsonl through the MCP audit log's writer (0600, rotated at 5 MB keeping
// one previous file); append is strict, tail gives the newest first, counts groups the records since a time by purpose
// and outcome, the rotated file included.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const os = require('os');
const path = require('path');
const { createEgressLog, READ_LIMIT } = require('../lib/egress-log');
const { tmpDir } = require('./ipc-stub');

const rec = (ts, purpose, outcome) => ({
  ts,
  purpose,
  method: 'GET',
  scheme: 'https',
  host: 'api.example.com',
  port: 443,
  outcome,
  bytesOut: 0,
  bytesIn: 0,
  ms: 1,
  via: 'proxy',
});

test('append writes one line to egress.jsonl (owner only); tail gives the newest first', (t) => {
  const dir = path.join(tmpDir(t, 'egress-log'), '.data');
  const log = createEgressLog({ dir });
  assert.equal(log.file, path.join(dir, 'egress.jsonl'));
  assert.deepEqual(log.tail(), [], 'no file yet');
  log.append(rec(1, 'pricing', 'ok'));
  log.append(rec(2, 'notify', 'refused'));
  assert.deepEqual(
    log.tail().map((r) => r.ts),
    [2, 1],
  );
  assert.deepEqual(
    log.tail(1).map((r) => r.ts),
    [2],
  );
  if (process.platform !== 'win32') assert.equal(fs.statSync(log.file).mode & 0o777, 0o600);
});

test('append is strict: a line that cannot be appended throws', (t) => {
  const dir = tmpDir(t, 'egress-log-strict');
  fs.mkdirSync(path.join(dir, 'egress.jsonl'));
  assert.throws(() => createEgressLog({ dir }).append(rec(1, 'pricing', 'ok')));
});

test('counts: per purpose and outcome since a time, the rotated file included; a cut line is skipped', (t) => {
  const dir = tmpDir(t, 'egress-log-counts');
  const log = createEgressLog({ dir });
  fs.writeFileSync(
    `${log.file}.1`,
    JSON.stringify(rec(10, 'mcp', 'ok')) + '\n' + JSON.stringify(rec(5, 'mcp', 'ok')) + '\n',
  );
  log.append(rec(20, 'mcp', 'would-refuse'));
  log.append(rec(30, 'notify', 'refused'));
  log.append(rec(31, 'notify', 'checked'));
  fs.appendFileSync(log.file, '{"ts":40,"purpose":"no');
  const zero = { ok: 0, error: 0, refused: 0, 'would-refuse': 0, checked: 0 };
  assert.deepEqual(log.counts(10), {
    mcp: { ...zero, ok: 1, 'would-refuse': 1, total: 2 },
    notify: { ...zero, refused: 1, checked: 1, total: 2 },
  });
  assert.equal(log.counts().mcp.total, 3, 'from the start');
  assert.deepEqual(log.counts(100), {});
});

test('rotation: past 5 MB the file moves to egress.jsonl.1 and a new one starts', (t) => {
  const dir = tmpDir(t, 'egress-log-rotate');
  const log = createEgressLog({ dir });
  fs.writeFileSync(log.file, 'x'.repeat(5 * 1024 * 1024 + 1) + '\n');
  log.append(rec(1, 'pricing', 'ok'));
  assert.ok(fs.statSync(`${log.file}.1`).size > 5 * 1024 * 1024);
  assert.deepEqual(log.tail(), [rec(1, 'pricing', 'ok')]);
});

test('tail: fills from the rotated file when the current one holds fewer than n; a line being written is left out', (t) => {
  const dir = tmpDir(t, 'egress-log-tail-rotated');
  const log = createEgressLog({ dir });
  fs.writeFileSync(`${log.file}.1`, [1, 2, 3].map((i) => JSON.stringify(rec(i, 'mcp', 'ok'))).join('\n') + '\n');
  log.append(rec(4, 'mcp', 'ok'));
  log.append(rec(5, 'mcp', 'ok'));
  fs.appendFileSync(log.file, '{"ts":6,"purpose":"mc');
  assert.deepEqual(
    log.tail(4).map((r) => r.ts),
    [5, 4, 3, 2],
  );
  assert.deepEqual(
    log.tail().map((r) => r.ts),
    [5, 4, 3, 2, 1],
  );
  // Lines longer than one read chunk, and non-ASCII text, come back whole.
  const long = { ...rec(7, 'notify', 'ok'), host: 'é'.repeat(70000) };
  fs.appendFileSync(log.file, '\n' + JSON.stringify(long) + '\n');
  assert.deepEqual(log.tail(1), [long]);
});

test('counts and tail read at most the newest READ_LIMIT bytes, not both whole files', (t) => {
  const dir = tmpDir(t, 'egress-log-bounded');
  const log = createEgressLog({ dir });
  const line = JSON.stringify(rec(1, 'old', 'ok')) + '\n';
  // A rotated file of old records and a current file larger than the limit: only the current file's tail is read.
  fs.writeFileSync(`${log.file}.1`, line.repeat(1000));
  const n = Math.ceil((READ_LIMIT + 4096) / line.length);
  fs.writeFileSync(
    log.file,
    JSON.stringify(rec(2, 'new', 'ok'))
      .concat('\n')
      .repeat(n),
  );
  const reads = [];
  const real = fs.readSync;
  t.mock.method(fs, 'readSync', (...a) => (reads.push(a[2]), real(...a)));
  const c = log.counts();
  assert.equal(c.old, undefined, 'the rotated file is past the limit');
  assert.ok(c.new.total < n && c.new.total > n - 200, `${c.new.total} of ${n}`);
  assert.ok(reads.reduce((x, y) => x + y, 0) <= READ_LIMIT, 'bytes read');
});

test('counts: truncated when the 2 MB read is cut inside the window, not otherwise', (t) => {
  const { createEgressLog, READ_LIMIT } = require('../lib/egress-log');
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'zp-egress-trunc-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const log = createEgressLog({ dir });
  const rec = (ts) => ({
    ts,
    purpose: 'connectors',
    method: 'GET',
    scheme: 'https',
    host: 'h',
    port: 443,
    outcome: 'ok',
    bytesOut: 0,
    bytesIn: 0,
    ms: 1,
    via: 'proxy',
    pad: 'x'.repeat(400),
  });
  const lines = [];
  for (let i = 0; i < Math.ceil((READ_LIMIT * 1.5) / 500); i++) lines.push(JSON.stringify(rec(1000 + i)));
  fs.writeFileSync(log.file, lines.join('\n') + '\n');
  assert.equal(log.counts(0).truncated, true); // the window starts before the oldest record read
  assert.equal(log.counts(1e12).truncated, false); // nothing in the window
  assert.ok(!Object.keys(log.counts(0)).includes('truncated'));
});
