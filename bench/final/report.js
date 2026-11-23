// The report of the final benchmark runner (scripts/final-bench.js): every metric row (id, scenario, target, median and
// spread over the repeats, verdict, quiet-confirmed), the machine, the preflight, the runs, what the runner does not
// cover, and where the numbers go. Pure: evaluate(ctx) -> rows, buildReport(ctx) -> Markdown
// (test/final-bench-report.test.js).
const { TARGETS, P4F_GAP_MS } = require('./scenarios');
const compare = require('../compare');

const SMOKE_STAMP = 'SMOKE RUN, NOT A MEASUREMENT';
// v1.1.0's download per target (P6 results, M6): the size v2's archive must not pass.
const V1_DOWNLOAD_MB = {
  'darwin-arm64': 122.9,
  'darwin-x64': 126.8,
  'linux-x64': 128.3,
  'linux-arm64': 124.7,
  'win-x64': 95.4,
  'win-arm64': 83.7,
};

// The metrics the runner cannot measure, with their targets and where they are proven (plan §4 and the results docs).
const NOT_COVERED = [
  ['M1', 'Test parity', '100% of the same tests green on Deno', 'the test suites (`npm test`, the Deno suite) in CI; not a timing'],
  ['M2', 'Gateway contract suite', '100% green against both engines', 'the contract jobs in CI (`npm run test:contract`, `ZP_ENGINE=go`/`shadow`)'],
  ['M6', 'Download size, the other five targets', '≤ v1.1.0 per platform', 'the release dry run (P6 results: met on all six); this machine\'s archive is measured by `binary`'],
  ['M8', 'Platforms', '6/6 built and smoke-tested', 'CI release dry run (P6 results); a build matrix, not a quiet-machine number'],
  ['M9', 'Deno permissions', 'write = data dir; net = localhost; env and run allow-lists', '`test/permissions.test.js`, the lock-down tests (P5)'],
  ['M10', 'Egress coverage', '100% of outbound connections through the Go proxy', '`test/egress-classes.test.js`, the denial test (P5)'],
  ['M11', 'Runtime dependencies', 'Deno 0 npm/jsr runtime imports; Go stdlib only', '`go.mod`, `package.json`; a count, not a timing'],
  ['M12', 'Data compatibility', 'v2 opens a v1.1 data dir; rollback reads it', '`scripts/upgrade-check.js` (P6 results: met locally); needs the v1.1.0 download'],
  ['P4f', 'Journal on/off A/B', 'the +17 ms explained', 'the gateway has no switch to turn its journal off; `p4f-base`, `p4f-head` and `p4f-notrace` bound it'],
  ['P4g', 'Live provider smoke tests', 'OAuth2 and service-account providers answer', 'needs the owner\'s real keys (P4g results); not a benchmark'],
];

const median = (xs) => {
  const s = xs.filter((x) => typeof x === 'number' && Number.isFinite(x)).sort((a, b) => a - b);
  if (!s.length) return null;
  const m = Math.floor(s.length / 2);
  return Math.round((s.length % 2 ? s[m] : (s[m - 1] + s[m]) / 2) * 100) / 100;
};
const numbers = (xs) => xs.filter((x) => typeof x === 'number' && Number.isFinite(x));
const fmt = (v) => (v === null || v === undefined ? '—' : String(Math.round(v * 100) / 100));
function spread(xs) {
  const n = numbers(xs);
  if (!n.length) return '—';
  if (n.length === 1) return 'one run';
  return `${fmt(Math.min(...n))}–${fmt(Math.max(...n))} (${n.length} runs)`;
}

// The top stream level of a run.js result.
const topOf = (d) => (d && Array.isArray(d.streams) ? d.streams.reduce((a, s) => (!a || s.level > a.level ? s : a), null) : null);

/** The named numbers of one run's data, by scenario kind. */
function extract(def, d) {
  if (!d) return {};
  if (def.kind === 'run') {
    const top = topOf(d);
    return {
      nsP50: d.nonStream && d.nonStream.overhead.p50,
      nsP95: d.nonStream && d.nonStream.overhead.p95,
      nsP99: d.nonStream && d.nonStream.overhead.p99,
      level: top ? top.level : null,
      completed: top ? top.gateway.completed : null,
      errors: top ? top.gateway.errors : null,
      streamP95: top ? top.overheadP95 : null,
      ttfbP95: top ? top.ttfbOverheadP95 : null,
      gwPeak: d.gatewayPeakRssMB,
      gwIdle: d.gatewayIdleRssMB,
      idle: d.idleRssMB,
      peak: d.peakRssMB,
      bothIdle: typeof d.idleRssMB === 'number' ? d.idleRssMB + (d.gatewayIdleRssMB || 0) : null,
      startup: d.startup && d.startup.p50,
      otlp: d.otlpExports,
    };
  }
  if (def.kind === 'egress')
    return { p50: d.overhead && d.overhead.p50, p95: d.overhead && d.overhead.p95, p99: d.overhead && d.overhead.p99 };
  if (def.kind === 'binary')
    return {
      readyMs: d.smoke && d.smoke.front ? d.smoke.front.readyMs : null,
      demoMs: d.smoke ? d.smoke.startupMs : null,
      archiveMB: d.archive ? Math.round((d.archive.bytes / 1e6) * 10) / 10 : null,
    };
  if (def.id === 'ipc')
    return {
      rtP50: d.roundTrip && d.roundTrip.p50Ms,
      rtP99: d.roundTrip && d.roundTrip.p99Ms,
      eps: d.throughput && d.throughput.eventsPerSecond,
    };
  if (def.id === 'front-proxy') return { p50: d.overhead && d.overhead.p50, p99: d.overhead && d.overhead.p99 };
  return {};
}

/**
 * Every metric row. ctx: { smoke, preflight: { ran, ok }, results: [{ def, runs: [{ ok, data, noisy, error }] }],
 * platformName }.
 */
function evaluate(ctx) {
  const rows = [];
  const byId = new Map(ctx.results.map((r) => [r.def.id, r]));
  const valuesOf = (res, key) => res.runs.filter((r) => r.ok).map((r) => extract(res.def, r.data)[key]);
  const quietOf = (res) => {
    if (ctx.smoke) return 'no (smoke)';
    if (!ctx.preflight.ran) return 'no (preflight skipped)';
    if (!ctx.preflight.ok) return 'no (preflight failed)';
    if (res.runs.some((r) => r.noisy)) return 'no (noisy)';
    if (!res.runs.some((r) => r.ok)) return 'no (no run)';
    if (res.runs.some((r) => r.noisy === null)) return 'no (noise unknown)';
    return 'yes';
  };
  for (const res of ctx.results) {
    const { def } = res;
    const ok = res.runs.filter((r) => r.ok);
    const noisy = res.runs.some((r) => r.noisy);
    const quiet = quietOf(res);
    const add = (metric, measure, target, key, test, o = {}) => {
      const vals = o.values || valuesOf(res, key);
      const med = o.median !== undefined ? o.median : median(vals);
      let verdict;
      if (!res.runs.length) verdict = 'not run';
      else if (!ok.length) verdict = 'error';
      else if (!test) verdict = noisy ? 'info, noisy' : 'info';
      else if (med === null) verdict = 'error';
      else {
        const pass = test(med, vals);
        verdict = noisy ? `noisy (${pass ? 'pass' : 'fail'})` : pass ? 'pass' : 'fail';
      }
      rows.push({
        metric,
        scenario: def.id,
        measure,
        target: test ? target : o.target || '—',
        median: o.show ? o.show(med, vals) : fmt(med),
        spread: o.spread || spread(vals),
        verdict,
        quiet: o.quiet || quiet,
      });
    };
    const has = (c) => def.checks.includes(c);
    if (def.kind === 'run') {
      add(has('M3') ? 'M3' : 'M3 ref', 'non-stream overhead p95 (ms)', `≤ ${TARGETS.M3} ms`, 'nsP95', has('M3') ? (m) => m <= TARGETS.M3 : null);
      if (has('M4')) {
        add('M4', 'streams held at the top level, errors', `≥ ${TARGETS.M4_STREAMS} held, 0 errors`, 'errors', (m, vals) => {
          const levels = numbers(valuesOf(res, 'level'));
          return levels.length > 0 && Math.min(...levels) >= TARGETS.M4_STREAMS && Math.max(...vals) === 0;
        }, {
          show: (m) => `${fmt(median(valuesOf(res, 'level')))} held, ${fmt(m)} errors (${fmt(median(valuesOf(res, 'completed')))} done)`,
        });
        add('M4', 'stream overhead p95 at the top level (ms)', `≤ ${TARGETS.M4_OVERHEAD} ms`, 'streamP95', (m) => m <= TARGETS.M4_OVERHEAD);
        add('M4', 'gateway peak RSS (MB)', `≤ ${TARGETS.M4_RSS} MB`, 'gwPeak', (m) => m <= TARGETS.M4_RSS);
      } else if (!def.baseline) {
        add('M4 ref', 'stream overhead p95 at the top level (ms)', '', 'streamP95', null);
        add('M4 ref', 'errors at the top level', '', 'errors', null);
        if (def.engine !== 'js') add('M4 ref', 'gateway peak RSS (MB)', '', 'gwPeak', null);
      }
      if (has('M5')) add('M5', 'idle RSS, server + gateway (MB)', `≤ ${TARGETS.M5} MB`, 'bothIdle', (m) => m <= TARGETS.M5);
      if (has('M7bench'))
        add('M7', 'bench start-up to the first 200, gateway in front (p50, ms)', `≤ ${TARGETS.M7} ms`, 'startup', (m) => m <= TARGETS.M7);
      if (def.baseline) {
        for (const level of [50, 200, 500]) {
          const at = (r) => (r.data && r.data.streams ? r.data.streams.find((s) => s.level === level) : null);
          const vals = ok.map((r) => (at(r) ? at(r).overheadP95 : null));
          if (numbers(vals).length) add('baseline', `${level} streams: overhead p95 (ms)`, '', null, null, { values: vals });
        }
        add('baseline', 'server idle RSS (MB)', '', 'idle', null);
        add('baseline', 'server peak RSS (MB)', '', 'peak', null);
        add('baseline', 'start-up to the first 200 (p50, ms)', '', 'startup', null);
      }
      if (def.gapOf && byId.has(def.gapOf)) {
        const base = byId.get(def.gapOf);
        const b = median(valuesOf(base, 'streamP95'));
        const h = median(valuesOf(res, 'streamP95'));
        const gap = b === null || h === null ? null : Math.round((h - b) * 100) / 100;
        add('P4f gap', `top-level stream overhead p95 minus ${def.gapOf} (ms)`, '', null, null, {
          values: [gap],
          spread: `P4f measured +${P4F_GAP_MS} ms (not quiet-confirmed)`,
          target: 'within noise',
        });
        const bn = median(valuesOf(base, 'nsP95'));
        const hn = median(valuesOf(res, 'nsP95'));
        add('P4f gap', `non-stream overhead p95 minus ${def.gapOf} (ms)`, '', null, null, {
          values: [bn === null || hn === null ? null : Math.round((hn - bn) * 100) / 100],
          spread: 'P4f measured +0.66 ms',
          target: 'within noise',
        });
      }
      if (def.env && def.env.ZP_BENCH_OTLP === 'on') add('D21', 'OTLP exports received by the sink', '', 'otlp', null, { target: '> 0' });
    } else if (def.kind === 'egress') {
      add('M3 (egress)', 'egress overhead p95, connectors on go minus direct (ms)', `≤ ${TARGETS.M3} ms`, 'p95', (m) => m <= TARGETS.M3);
      add('M3 (egress)', 'egress overhead p50 (ms)', '', 'p50', null);
      add('M3 (egress)', 'egress overhead p99 (ms)', '', 'p99', null);
    } else if (def.kind === 'binary') {
      add('M7', 'packaged binary: spawn to the gateway ready, front mode (ms)', `≤ ${TARGETS.M7} ms`, 'readyMs', (m) => m <= TARGETS.M7);
      add('M7 ref', 'packaged binary: demo start-up to the first 200, no gateway (ms)', '', 'demoMs', null);
      const ref = V1_DOWNLOAD_MB[ctx.platformName];
      add('M6', `archive size, ${ctx.platformName || 'this platform'} (MB)`, ref ? `≤ ${ref} MB (v1.1.0)` : '', 'archiveMB', ref ? (m) => m <= ref : null, {
        quiet: 'n/a (a size)',
      });
    } else if (def.id === 'ipc') {
      add('P3 ref', 'IPC round trip p50 (ms)', '', 'rtP50', null);
      add('P3 ref', 'IPC round trip p99 (ms)', '', 'rtP99', null);
      add('P3 ref', 'IPC events per second', '', 'eps', null);
    } else if (def.id === 'front-proxy') {
      add('P4a ref', 'front proxy overhead p50 (ms)', '', 'p50', null);
      add('P4a ref', 'front proxy overhead p99 (ms)', '', 'p99', null);
    }
  }
  return rows;
}

// The run whose non-stream p95 is the median one (the representative file of a run scenario).
function representative(res) {
  const ok = res.runs.filter((r) => r.ok && r.data);
  if (!ok.length) return null;
  const sorted = [...ok].sort((a, b) => (extract(res.def, a.data).nsP95 ?? 0) - (extract(res.def, b.data).nsP95 ?? 0));
  return sorted[Math.floor((sorted.length - 1) / 2)];
}

// A GFM table cell: backslashes first (so an input backslash cannot escape the pipe escape after it), then pipes; a
// line break would end the row.
const cell = (s) =>
  String(s === null || s === undefined ? '—' : s)
    .replace(/\\/g, '\\\\')
    .replace(/\|/g, '\\|')
    .replace(/\r?\n/g, ' ');
const table = (head, rows) =>
  [`| ${head.join(' | ')} |`, `|${head.map(() => '---').join('|')}|`, ...rows.map((r) => `| ${r.map(cell).join(' | ')} |`)].join('\n');

/** The report as Markdown. ctx as evaluate, plus machine, opts, startedAt, endedAt, load, preflight.checks, outDir. */
function buildReport(ctx) {
  const rows = evaluate(ctx);
  const out = [];
  const stamp = ctx.smoke ? `**${SMOKE_STAMP}.** Every number below comes from a tiny-scale run that proves the runner works; none is a measurement.\n` : '';
  out.push(`# Final benchmarks${ctx.smoke ? ` (${SMOKE_STAMP})` : ''}`, '');
  if (stamp) out.push(stamp);
  if (!ctx.smoke && !ctx.preflight.ran) out.push('**Preflight skipped: no result here is quiet-confirmed.**\n');
  else if (!ctx.smoke && !ctx.preflight.ok) out.push('**The preflight failed and was overridden: no result here is quiet-confirmed.**\n');
  const m = ctx.machine || {};
  const failed = rows.filter((r) => /fail/.test(r.verdict)).length;
  const passed = rows.filter((r) => /pass/.test(r.verdict)).length;
  out.push(
    `Run ${ctx.startedAt} to ${ctx.endedAt}, ${ctx.opts.repeats} repeat(s) per scenario in rounds, server runtime ${ctx.opts.runtime}` +
      ` (baseline and P4f on Node). Targets checked: ${passed} pass, ${failed} fail` +
      `${rows.some((r) => /noisy/.test(r.verdict)) ? ', some noisy' : ''}.`,
    '',
    '## Metrics',
    '',
    'Median over the repeats; spread is min–max. A run is noisy when processes outside the runner used more than ' +
      `${ctx.opts.noiseCpu}% of one core on average (or 200% in one sample) while it ran. Quiet-confirmed: the preflight ` +
      'passed, not a smoke run, and no repeat was noisy.',
    '',
    table(
      ['Metric', 'Scenario', 'Measure', 'Target', 'Median', 'Spread', 'Verdict', 'Quiet-confirmed'],
      rows.map((r) => [r.metric, r.scenario, r.measure, r.target, r.median, r.spread, r.verdict, r.quiet]),
    ),
    '',
  );
  out.push('## Scenarios', '');
  out.push(
    table(
      ['Id', 'What', 'Source'],
      ctx.results.map((r) => [r.def.id, r.def.title, r.def.source]),
    ),
    '',
  );
  out.push('## Runs', '');
  const runRows = [];
  for (const res of ctx.results)
    res.runs.forEach((r, i) =>
      runRows.push([
        res.def.id,
        i + 1,
        r.ok ? 'ok' : `error: ${String(r.error || '').slice(0, 160)}`,
        r.durationS,
        r.noisy === null || r.noisy === undefined ? 'unknown' : r.noisy ? 'yes' : 'no',
        r.noise ? `${fmt(r.noise.foreignMean)} / ${fmt(r.noise.foreignMax)}` : '—',
        r.noise ? `${fmt(r.noise.loadMean)} / ${fmt(r.noise.loadMax)}` : '—',
        r.file || '—',
      ]),
    );
  out.push(table(['Scenario', 'Repeat', 'Result', 'Seconds', 'Noisy', 'Foreign CPU % mean / max', 'Load 1 min mean / max', 'Raw file'], runRows), '');
  // The baseline rows of the program plan, and the P4f comparison, from the representative runs.
  const base = ctx.results.find((r) => r.def.baseline);
  const rep = base && representative(base);
  if (rep) {
    out.push('## Baseline rows for the program plan (§2 and §4 cells)', '', '```', compare.baseline(rep.data, rep.file || 'baseline'), '```', '');
  }
  const a = ctx.results.find((r) => r.def.id === 'p4f-base');
  const b = ctx.results.find((r) => r.def.id === 'p4f-head');
  const ra = a && representative(a);
  const rb = b && representative(b);
  if (ra && rb) out.push('## P4f: this tree against the base', '', '```', compare.format(compare.compare(ra.data, rb.data), ra.data, rb.data).trimEnd(), '```', '');
  out.push('## Machine', '');
  out.push(
    table(
      ['Fact', 'Value'],
      [
        ['OS', m.os],
        ['CPU', `${m.cpu} (${m.cores} cores)`],
        ['RAM', `${m.memGB} GB`],
        ['Deno / Node / Go', `${m.deno || '—'} / ${m.node || '—'} / ${m.go || '—'}`],
        ['Git', `${m.sha || '—'}${m.dirty ? ' (uncommitted changes)' : ''}`],
        ['Load average during the run (1 min)', ctx.load ? `min ${fmt(ctx.load.min)}, mean ${fmt(ctx.load.mean)}, max ${fmt(ctx.load.max)}` : '—'],
        ['Runner', ctx.runner || '—'],
      ],
    ),
    '',
  );
  out.push('## Preflight', '');
  if (!ctx.preflight.ran) out.push('Skipped (`--skip-preflight`): the checks did not stop the run. As they read at the start:', '');
  if (ctx.preflight.checks && ctx.preflight.checks.length)
    out.push(table(['Check', 'Status', 'Detail'], ctx.preflight.checks.map((c) => [c.label, c.status, c.detail])), '');
  out.push('## Not covered by the runner', '');
  out.push(table(['Metric', 'What', 'Target', 'Where it is proven, and why not here'], NOT_COVERED), '');
  out.push(
    '## What to do with this',
    '',
    ctx.smoke
      ? `Nothing: this is a ${SMOKE_STAMP.toLowerCase()}. Run \`deno task bench:final\` on the quiet machine for the real numbers.`
      : [
          '1. Check that every row you need says quiet-confirmed **yes**. A noisy or failed row: close what the preflight or the Runs table',
          '   names and run that scenario again (`deno task bench:final --only <id>`).',
          '2. Program plan (`docs/migration/2026-11-10-deno-go-migration-plan.md`): replace the §2 baseline rows with the block',
          '   above (the stream cells lose their "noisy" note), and fill the M3, M4, M5 and M7 cells of §4 from the `external-go`,',
          '   `private-go` and `binary` rows; the G3/G4 gates read them.',
          '3. The phase results docs, in their "Owed" sections: P4c and P4e (`private-*`, `external-*`), P4d (`translated-go`),',
          '   the token saver (`saver-*`), P4h (`route-*`), P4f (`p4f-*`, the gap rows and D21), P5 (`egress`), P6 (`binary`, M7 and',
          '   M6), and P7 remove-node precondition 2 and its checklist item 5.',
          '4. The v2.0.0 release notes: the M3, M4, M5, M6 and M7 rows with the machine line above.',
          '5. Commit this folder (`bench/results/<date>-final/`): the raw JSON is the evidence the docs point at.',
          '6. The rows under "Not covered" stay with CI and the tests named there.',
        ].join('\n'),
    '',
  );
  return out.join('\n');
}

module.exports = { cell, table, SMOKE_STAMP, NOT_COVERED, V1_DOWNLOAD_MB, median, spread, extract, evaluate, representative, buildReport };
