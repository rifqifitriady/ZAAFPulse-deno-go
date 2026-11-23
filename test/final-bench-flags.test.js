// bench/final/flags.js and bench/final/scenarios.js: the runner's command line and its scenario table.
const test = require('node:test');
const assert = require('node:assert/strict');
const { parseFlags, USAGE } = require('../bench/final/flags');
const sc = require('../bench/final/scenarios');

const IDS = sc.IDS;

test('defaults: three repeats, Deno, a 10 s countdown, the build on', () => {
  const { opts } = parseFlags([], IDS);
  assert.equal(opts.repeats, 3);
  assert.equal(opts.runtime, 'deno');
  assert.equal(opts.countdown, 10);
  assert.equal(opts.build, true);
  assert.equal(opts.only, null);
  assert.equal(opts.smoke, false);
  assert.equal(opts.skipPreflight, false);
  assert.equal(opts.noiseCpu, 50);
});

test('smoke: one repeat and no countdown unless asked', () => {
  assert.equal(parseFlags(['--smoke'], IDS).opts.repeats, 1);
  assert.equal(parseFlags(['--smoke'], IDS).opts.countdown, 0);
  assert.equal(parseFlags(['--smoke', '--repeats', '2', '--countdown=3'], IDS).opts.repeats, 2);
  assert.equal(parseFlags(['--smoke', '--countdown=3'], IDS).opts.countdown, 3);
});

test('every flag, in both spellings', () => {
  const { opts } = parseFlags(
    [
      '--yes',
      '--only=external-go,egress,external-go',
      '--out',
      '/tmp/x',
      '--skip-preflight',
      '--runtime',
      'node',
      '--binary',
      'b',
      '--gw=g',
      '--no-build',
      '--heavy',
      'Slack, Zoom',
      '--heavy=Teams',
      '--noise-cpu',
      '80',
      '--list',
      '-h',
    ],
    IDS,
  );
  assert.deepEqual(opts.only, ['external-go', 'egress']);
  assert.equal(opts.out, '/tmp/x');
  assert.equal(opts.yes && opts.skipPreflight && opts.list && opts.help, true);
  assert.equal(opts.runtime, 'node');
  assert.equal(opts.binary, 'b');
  assert.equal(opts.gw, 'g');
  assert.equal(opts.build, false);
  assert.deepEqual(opts.heavy, ['Slack', 'Zoom', 'Teams']);
  assert.equal(opts.noiseCpu, 80);
});

test('errors: unknown flags and ids, missing or bad values', () => {
  const e = (argv) => parseFlags(argv, IDS).error;
  assert.match(e(['--bogus']), /unknown flag --bogus/);
  assert.match(e(['stray']), /unexpected argument/);
  assert.match(e(['--only', 'nope,egress']), /unknown scenario nope/);
  assert.match(e(['--only', ',']), /at least one/);
  assert.match(e(['--only']), /needs a value/);
  assert.match(e(['--out', '--yes']), /needs a value/);
  assert.match(e(['--repeats', '0']), /whole number from 1 to 20/);
  assert.match(e(['--repeats', '2.5']), /whole number/);
  assert.match(e(['--countdown', '-1']), /whole number from 0/);
  assert.match(e(['--runtime', 'bun']), /deno or node/);
  assert.match(e(['--noise-cpu', '0']), /above 0/);
  assert.match(e(['--yes=1']), /takes no value/);
  assert.match(e(['--gw', 'g']), /needs --binary/);
  assert.match(USAGE, /--skip-preflight/);
});

test('scenarios: unique ids, a source and a title each; checks name known targets', () => {
  const list = sc.scenarios();
  assert.equal(new Set(list.map((s) => s.id)).size, list.length);
  for (const s of list) {
    assert.ok(s.title && s.source, s.id);
    for (const c of s.checks) assert.ok(['M3', 'M4', 'M5', 'M7', 'M7bench', 'egress'].includes(c), `${s.id}: ${c}`);
  }
  assert.equal(sc.TARGETS.M3, 2.84);
  assert.equal(list.find((s) => s.id === 'p4f-base').commit, sc.P4F_BASE);
  assert.equal(sc.scenarios({ runtime: 'node' }).find((s) => s.id === 'external-go').runtime, 'node');
  assert.equal(sc.scenarios({ runtime: 'deno' }).find((s) => s.id === 'baseline').runtime, 'node');
});

test('runEnv: 500 streams only (the baseline 50/200/500), smoke at tiny scale', () => {
  const list = sc.scenarios();
  const by = (id) => list.find((s) => s.id === id);
  assert.deepEqual(sc.runEnv(by('external-go')), {
    ZP_RUNTIME: 'deno',
    ZP_ENGINE: 'go',
    ZP_BENCH_TIER: 'external',
    ZP_BENCH_LEVELS: '500',
  });
  assert.equal(sc.runEnv(by('baseline')).ZP_BENCH_LEVELS, '50,200,500');
  assert.equal(sc.runEnv(by('baseline')).ZP_ENGINE, undefined);
  assert.equal(sc.runEnv(by('saver-go')).ZP_BENCH_TOOL_RESULT, '65536');
  assert.equal(sc.runEnv(by('p4f-otlp')).ZP_BENCH_OTLP, 'on');
  const smoke = sc.runEnv(by('route-go'), { smoke: true });
  assert.equal(smoke.ZP_BENCH_REQUESTS, '20');
  assert.equal(smoke.ZP_BENCH_LEVELS, '5');
  assert.equal(smoke.ZP_BENCH_ROUTE, 'round-robin');
  assert.equal(sc.runEnv(by('baseline'), { smoke: true }).ZP_BENCH_LEVELS, '5,10');
});

test('scriptArgs and estimates', () => {
  const list = sc.scenarios();
  const by = (id) => list.find((s) => s.id === id);
  assert.deepEqual(sc.scriptArgs(by('egress')), ['--count', '2000', '--warmup', '100']);
  assert.deepEqual(sc.scriptArgs(by('front-proxy'), { smoke: true, runtime: 'node' }), ['--count', '50', '--runtime', 'node']);
  const full = list.reduce((a, s) => a + sc.estimateSeconds(s), 0);
  const smoke = list.reduce((a, s) => a + sc.estimateSeconds(s, { smoke: true }), 0);
  assert.ok(full > 30 * 60 && full < 60 * 60, `one round ${full} s`);
  assert.ok(smoke < 8 * 60, `one smoke round ${smoke} s`);
});
