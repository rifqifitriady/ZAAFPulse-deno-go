// bench/compare.js: the deltas between two benchmark result files, and the baseline rows for the program plan.
const test = require('node:test');
const assert = require('node:assert/strict');
const { compare, baseline } = require('../bench/compare');

const result = (over = {}) => ({
  date: '2026-11-10',
  runtime: 'node',
  runtimeVersion: 'v24.21.0',
  git: { sha: 'abcdef0123', dirty: false },
  host: { name: 'mac', cpu: 'Apple M4', arch: 'arm64' },
  config: { delayMs: 20, seconds: 10 },
  startup: { ms: [300, 310, 320], p50: 310 },
  idleRssMB: 100,
  peakRssMB: 300,
  nonStream: { overhead: { p50: 4, p95: 6, p99: 8 } },
  streams: [
    { level: 50, gateway: { completed: 900, errors: 0, ttfbP95: 40 }, overheadP95: 10 },
    { level: 500, gateway: { completed: 8000, errors: 2, ttfbP95: 150 }, overheadP95: 50 },
  ],
  ...over,
});

test('compare: each metric of a and b, the change and the change in percent', () => {
  const rows = compare(result(), result({ idleRssMB: 80, nonStream: { overhead: { p50: 2, p95: 3, p99: 8 } } }));
  const by = Object.fromEntries(rows.map((r) => [r.label, r]));
  assert.deepEqual(by['idle RSS (MB)'], { label: 'idle RSS (MB)', a: 100, b: 80, delta: -20, pct: -20 });
  assert.deepEqual(by['non-stream overhead p95 (ms)'], {
    label: 'non-stream overhead p95 (ms)',
    a: 6,
    b: 3,
    delta: -3,
    pct: -50,
  });
  assert.equal(by['500 streams: errors'].pct, 0);
  assert.equal(by['50 streams: overhead p95 (ms)'].delta, 0);
});

test('compare: a level only one file has, or a missing value, compares as null', () => {
  const rows = compare(result(), result({ streams: [], peakRssMB: null }));
  const by = Object.fromEntries(rows.map((r) => [r.label, r]));
  assert.deepEqual([by['peak RSS (MB)'].b, by['peak RSS (MB)'].delta], [null, null]);
  assert.deepEqual([by['500 streams: completed'].a, by['500 streams: completed'].b], [8000, null]);
});

test('baseline: the plan rows and the M3/M4/M5/M7 cells name their result file', () => {
  const text = baseline(result(), 'bench/results/2026-11-10-node-mac.json');
  assert.match(
    text,
    /\| 4 \/ 6 \/ 8 ms \| `bench\/results\/2026-11-10-node-mac\.json` \(node v24\.21\.0, Apple M4\) \|/,
  );
  assert.match(text, /50: 900 done, 0 errors, overhead p95 10 ms; 500: 8000 done, 2 errors, overhead p95 50 ms/);
  assert.match(text, /^M3 baseline cell: p95 6 ms \(p50 4 ms, p99 8 ms\)$/m);
  assert.match(text, /^M4 baseline cell: 500 streams: 8000 done, 2 errors, overhead p95 50 ms, peak RSS 300 MB$/m);
  assert.match(text, /^M7 baseline cell, appended: bench startup p50 310 ms$/m);
});
