// The egress log (P5 Decision 10): .data/egress.jsonl through the MCP audit log's writer (0600, rotated at 5 MB keeping
// one previous file); append is strict, tail gives the newest first, counts groups the records since a time by purpose
// and outcome, the rotated file included.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const { createEgressLog } = require('../lib/egress-log');
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
