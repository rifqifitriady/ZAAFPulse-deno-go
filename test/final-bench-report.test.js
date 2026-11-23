// bench/final/report.js: the metric rows (targets, median and spread, verdicts, quiet-confirmed) and the Markdown report,
// from hand-made results shaped as bench/run.js, bench/egress-overhead.js and scripts/smoke-binary.js write them.
const test = require('node:test');
const assert = require('node:assert/strict');
const rep = require('../bench/final/report');
const { scenarios } = require('../bench/final/scenarios');

const DEFS = scenarios();
const def = (id) => DEFS.find((d) => d.id === id);

// A bench/run.js result.
function runData({ ns = 2, stream = 5, errors = 0, level = 500, gwPeak = 70, gwIdle = 40, idle = 100, startup = 400, levels } = {}) {
  const step = (l, o = stream) => ({
    level: l,
    seconds: 10,
    gateway: { completed: 9000, errors: l === level ? errors : 0, ttfbP95: 30, durP50: 500, durP95: 520 },
    direct: { completed: 9000, errors: 0, ttfbP95: 25, durP50: 495, durP95: 515 },
    overheadP95: o,
    ttfbOverheadP95: 5,
  });
  return {
    schema: 1,
    date: '2026-11-30',
    runtime: 'deno',
    runtimeVersion: 'deno 2.9.8',
    git: { sha: 'abc1234def', dirty: false },
    host: { name: 'mac', cpu: 'Apple M4', arch: 'arm64' },
    config: { delayMs: 20, seconds: 10 },
    startup: { ms: [startup], p50: startup },
    idleRssMB: idle,
    nonStream: { overhead: { p50: 1, p95: ns, p99: 4 } },
    streams: (levels || [level]).map((l) => step(l)),
    peakRssMB: 300,
    gatewayIdleRssMB: gwIdle,
    gatewayPeakRssMB: gwPeak,
  };
}
const ok = (data, o = {}) => ({ ok: true, data, noisy: false, durationS: 100, noise: { foreignMean: 3, foreignMax: 9, loadMean: 2, loadMax: 3 }, ...o });
const ctxOf = (results, o = {}) => ({
  smoke: false,
  preflight: { ran: true, ok: true, checks: [{ label: 'Load', status: 'pass', detail: '1 min 0.5' }] },
  results,
  platformName: 'darwin-arm64',
  machine: { os: 'Darwin 25.6', cpu: 'Apple M4', cores: 10, memGB: 16, deno: '2.9.8', node: '24.16.0', go: '1.27.3', sha: 'abc1234', dirty: false },
  opts: { repeats: 3, runtime: 'deno', noiseCpu: 50 },
  startedAt: '2026-11-30T08:00:00Z',
  endedAt: '2026-11-30T10:00:00Z',
  load: { min: 0.5, mean: 2, max: 3.5 },
  ...o,
});
const rowsOf = (rows, scenario, metric) => rows.filter((r) => r.scenario === scenario && (!metric || r.metric === metric));

test('median and spread', () => {
  assert.equal(rep.median([3, 1, 2]), 2);
  assert.equal(rep.median([4, 1, 2, 3]), 2.5);
  assert.equal(rep.median([null, undefined]), null);
  assert.equal(rep.spread([2, 1.234, 3]), '1.23–3 (3 runs)');
  assert.equal(rep.spread([2]), 'one run');
  assert.equal(rep.spread([]), '—');
});

test('external-go: M3, M4, M5 and M7 pass on the median, fail on it, quiet yes', () => {
  const runs = [ok(runData({ ns: 2.5 })), ok(runData({ ns: 2.0 })), ok(runData({ ns: 9 }))];
  const rows = rep.evaluate(ctxOf([{ def: def('external-go'), runs }]));
  const m3 = rowsOf(rows, 'external-go', 'M3')[0];
  assert.equal(m3.median, '2.5');
  assert.equal(m3.spread, '2–9 (3 runs)');
  assert.equal(m3.verdict, 'pass');
  assert.equal(m3.quiet, 'yes');
  assert.equal(m3.target, '≤ 2.84 ms');
  const m4 = rowsOf(rows, 'external-go', 'M4');
  assert.deepEqual(
    m4.map((r) => r.verdict),
    ['pass', 'pass', 'pass'],
  );
  assert.match(m4[0].median, /^500 held, 0 errors \(9000 done\)$/);
  assert.equal(rowsOf(rows, 'external-go', 'M5')[0].median, '140');
  assert.equal(rowsOf(rows, 'external-go', 'M7')[0].verdict, 'pass');
  const bad = rep.evaluate(ctxOf([{ def: def('external-go'), runs: [ok(runData({ ns: 3, stream: 12, gwPeak: 90, errors: 1 }))] }]));
  assert.deepEqual(
    rowsOf(bad, 'external-go').filter((r) => r.metric === 'M3' || r.metric === 'M4').map((r) => r.verdict),
    ['fail', 'fail', 'fail', 'fail'],
  );
});

test('M4 held: fewer than 500 streams fails, also with 0 errors', () => {
  const rows = rep.evaluate(ctxOf([{ def: def('private-go'), runs: [ok(runData({ level: 5 }))] }]));
  assert.equal(rowsOf(rows, 'private-go', 'M4')[0].verdict, 'fail');
});

test('noisy runs, a skipped preflight, smoke, errors and no runs', () => {
  const noisy = rep.evaluate(ctxOf([{ def: def('private-go'), runs: [ok(runData()), ok(runData(), { noisy: true })] }]));
  assert.equal(rowsOf(noisy, 'private-go', 'M3')[0].verdict, 'noisy (pass)');
  assert.equal(rowsOf(noisy, 'private-go', 'M3')[0].quiet, 'no (noisy)');
  const skipped = rep.evaluate(ctxOf([{ def: def('private-go'), runs: [ok(runData())] }], { preflight: { ran: false, ok: false } }));
  assert.equal(rowsOf(skipped, 'private-go', 'M3')[0].quiet, 'no (preflight skipped)');
  const smoke = rep.evaluate(ctxOf([{ def: def('private-go'), runs: [ok(runData())] }], { smoke: true }));
  assert.equal(rowsOf(smoke, 'private-go', 'M3')[0].quiet, 'no (smoke)');
  const err = rep.evaluate(ctxOf([{ def: def('private-go'), runs: [{ ok: false, error: 'boom', noisy: false }] }]));
  assert.equal(rowsOf(err, 'private-go', 'M3')[0].verdict, 'error');
  const none = rep.evaluate(ctxOf([{ def: def('private-go'), runs: [] }]));
  assert.equal(rowsOf(none, 'private-go', 'M3')[0].verdict, 'not run');
  const unknown = rep.evaluate(ctxOf([{ def: def('private-go'), runs: [ok(runData(), { noisy: null })] }]));
  assert.equal(rowsOf(unknown, 'private-go', 'M3')[0].quiet, 'no (noise unknown)');
});

test('js and front scenarios are information; front shows the gateway RSS', () => {
  const rows = rep.evaluate(ctxOf([
    { def: def('external-js'), runs: [ok(runData())] },
    { def: def('external-front'), runs: [ok(runData())] },
  ]));
  assert.ok(rows.every((r) => r.verdict === 'info'));
  assert.equal(rowsOf(rows, 'external-js').length, 3);
  assert.equal(rowsOf(rows, 'external-front').length, 4);
});

test('baseline: the three levels, RSS and start-up, and the plan rows in the report', () => {
  const data = runData({ levels: [50, 200, 500] });
  const results = [{ def: def('baseline'), runs: [ok(data, { file: 'baseline-1.json' })] }];
  const rows = rep.evaluate(ctxOf(results));
  assert.deepEqual(
    rowsOf(rows, 'baseline', 'baseline').map((r) => r.measure),
    [
      '50 streams: overhead p95 (ms)',
      '200 streams: overhead p95 (ms)',
      '500 streams: overhead p95 (ms)',
      'server idle RSS (MB)',
      'server peak RSS (MB)',
      'start-up to the first 200 (p50, ms)',
    ],
  );
  const md = rep.buildReport(ctxOf(results));
  assert.match(md, /## Baseline rows for the program plan/);
  assert.match(md, /M3 baseline cell: p95 2 ms/);
  assert.match(md, /`baseline-1\.json`/);
});

test('P4f: the gap against the base, and the OTLP row', () => {
  const results = [
    { def: def('p4f-base'), runs: [ok(runData({ stream: 30, ns: 3 }))] },
    { def: def('p4f-head'), runs: [ok(runData({ stream: 35.5, ns: 3.5 }))] },
    { def: def('p4f-otlp'), runs: [ok({ ...runData(), otlpExports: 12 })] },
  ];
  const rows = rep.evaluate(ctxOf(results));
  const gap = rowsOf(rows, 'p4f-head', 'P4f gap');
  assert.equal(gap[0].median, '5.5');
  assert.equal(gap[1].median, '0.5');
  assert.match(gap[0].spread, /\+17\.07 ms/);
  assert.equal(rowsOf(rows, 'p4f-otlp', 'D21')[0].median, '12');
  assert.equal(rowsOf(rows, 'p4f-otlp', 'M3')[0].verdict, 'pass');
  assert.match(rep.buildReport(ctxOf(results)), /## P4f: this tree against the base/);
});

test('egress, binary (M7 and M6 against v1.1.0), ipc and front-proxy', () => {
  const results = [
    { def: def('egress'), runs: [ok({ overhead: { p50: 0.2, p95: 0.5, p99: 1 } })] },
    { def: def('binary'), runs: [ok({ smoke: { startupMs: 600, front: { readyMs: 700 } }, archive: { bytes: 34_100_000 } })] },
    { def: def('ipc'), runs: [ok({ roundTrip: { p50Ms: 0.05, p99Ms: 0.2 }, throughput: { eventsPerSecond: 90000 } })] },
    { def: def('front-proxy'), runs: [ok({ overhead: { p50: 0.1, p99: 0.4 } })] },
  ];
  const rows = rep.evaluate(ctxOf(results));
  assert.equal(rowsOf(rows, 'egress', 'M3 (egress)')[0].verdict, 'pass');
  assert.equal(rowsOf(rows, 'binary', 'M7')[0].verdict, 'pass');
  const m6 = rowsOf(rows, 'binary', 'M6')[0];
  assert.equal(m6.median, '34.1');
  assert.equal(m6.target, '≤ 122.9 MB (v1.1.0)');
  assert.equal(m6.verdict, 'pass');
  assert.equal(m6.quiet, 'n/a (a size)');
  assert.equal(rowsOf(rows, 'ipc').length, 3);
  assert.equal(rowsOf(rows, 'front-proxy').length, 2);
  const slow = rep.evaluate(ctxOf([{ def: def('binary'), runs: [ok({ smoke: { front: { readyMs: 1500 } }, archive: { bytes: 1 } })] }]));
  assert.equal(rowsOf(slow, 'binary', 'M7')[0].verdict, 'fail');
});

test('the report: sections, the not-covered list, the machine; smoke and skipped preflight stamped', () => {
  const results = [{ def: def('external-go'), runs: [ok(runData(), { file: 'external-go-1.json' })] }];
  const md = rep.buildReport(ctxOf(results));
  for (const h of ['# Final benchmarks', '## Metrics', '## Scenarios', '## Runs', '## Machine', '## Preflight', '## Not covered by the runner', '## What to do with this'])
    assert.ok(md.includes(h), h);
  for (const id of ['M1', 'M2', 'M8', 'M9', 'M10', 'M11', 'M12']) assert.match(md, new RegExp(`\\| ${id} \\|`));
  assert.match(md, /Apple M4 \(10 cores\)/);
  assert.match(md, /2\.9\.8 \/ 24\.16\.0 \/ 1\.27\.3/);
  assert.match(md, /external-go-1\.json/);
  assert.doesNotMatch(md, /SMOKE/);
  const smoke = rep.buildReport(ctxOf(results, { smoke: true }));
  assert.match(smoke, /^# Final benchmarks \(SMOKE RUN, NOT A MEASUREMENT\)/);
  assert.match(smoke, /none is a measurement/);
  const skipped = rep.buildReport(ctxOf(results, { preflight: { ran: false, ok: false } }));
  assert.match(skipped, /Preflight skipped: no result here is quiet-confirmed/);
  assert.match(skipped, /Skipped \(`--skip-preflight`\): the checks did not stop the run/);
});
