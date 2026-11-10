// Compares two bench result files: node bench/compare.js a.json b.json. Prints each metric of a and b, the change and
// the change in percent (b against a). Lower is better for every metric except completed streams.
const fs = require('fs');

// The metrics compared: [label, (result) -> number|null].
function metrics(a, b) {
  const list = [
    ['startup p50 (ms)', (r) => r.startup && r.startup.p50],
    ['idle RSS (MB)', (r) => r.idleRssMB],
    ['peak RSS (MB)', (r) => r.peakRssMB],
    ['non-stream overhead p50 (ms)', (r) => r.nonStream && r.nonStream.overhead.p50],
    ['non-stream overhead p95 (ms)', (r) => r.nonStream && r.nonStream.overhead.p95],
    ['non-stream overhead p99 (ms)', (r) => r.nonStream && r.nonStream.overhead.p99],
  ];
  const levels = [...new Set([...(a.streams || []), ...(b.streams || [])].map((s) => s.level))].sort((x, y) => x - y);
  const at = (r, level) => (r.streams || []).find((s) => s.level === level);
  for (const level of levels) {
    list.push([`${level} streams: completed`, (r) => at(r, level) && at(r, level).gateway.completed]);
    list.push([`${level} streams: errors`, (r) => at(r, level) && at(r, level).gateway.errors]);
    list.push([`${level} streams: ttfb p95 (ms)`, (r) => at(r, level) && at(r, level).gateway.ttfbP95]);
    list.push([`${level} streams: overhead p95 (ms)`, (r) => at(r, level) && at(r, level).overheadP95]);
  }
  return list;
}

function compare(a, b) {
  const rows = metrics(a, b).map(([label, get]) => {
    const x = get(a);
    const y = get(b);
    const ok = (v) => typeof v === 'number' && Number.isFinite(v);
    const delta = ok(x) && ok(y) ? Math.round((y - x) * 100) / 100 : null;
    const pctChange = ok(x) && ok(y) && x !== 0 ? Math.round(((y - x) / Math.abs(x)) * 1000) / 10 : null;
    return { label, a: ok(x) ? x : null, b: ok(y) ? y : null, delta, pct: pctChange };
  });
  return rows;
}

function format(rows, a, b) {
  const head = `a: ${a.date} ${a.runtime} ${a.runtimeVersion || ''} (${a.git && a.git.sha ? a.git.sha.slice(0, 7) : '?'})\n`;
  const headB = `b: ${b.date} ${b.runtime} ${b.runtimeVersion || ''} (${b.git && b.git.sha ? b.git.sha.slice(0, 7) : '?'})\n`;
  const show = (v) => (v === null ? '—' : String(v));
  const w = Math.max(...rows.map((r) => r.label.length));
  const lines = rows.map(
    (r) =>
      `${r.label.padEnd(w)}  ${show(r.a).padStart(10)}  ${show(r.b).padStart(10)}  ${show(r.delta).padStart(10)}  ${
        r.pct === null ? '—' : `${r.pct > 0 ? '+' : ''}${r.pct}%`
      }`,
  );
  return `${head}${headB}${'metric'.padEnd(w)}  ${'a'.padStart(10)}  ${'b'.padStart(10)}  ${'b - a'.padStart(10)}  %\n${lines.join('\n')}\n`;
}

// The Markdown the program plan's baseline table takes from one result file (docs/migration/...-migration-plan.md §2
// and the M3/M4/M5/M7 baseline cells of §4), with the file it came from.
function baseline(r, file) {
  const ms = (v) => (v === null || v === undefined ? '—' : `${v} ms`);
  const o = r.nonStream.overhead;
  const top = r.streams.reduce((a, s) => (!a || s.level > a.level ? s : a), null);
  const streams = r.streams
    .map(
      (s) => `${s.level}: ${s.gateway.completed} done, ${s.gateway.errors} errors, overhead p95 ${ms(s.overheadP95)}`,
    )
    .join('; ');
  const src = `\`${file}\` (${r.runtime} ${r.runtimeVersion || ''}, ${r.host.cpu || r.host.arch})`;
  return [
    `| Gateway overhead p50/p95/p99, non-streaming, mock provider (${r.config.delayMs} ms) | ${o.p50} / ${o.p95} / ${o.p99} ms | ${src} |`,
    `| Concurrent SSE streams, ${r.config.seconds} s per level | ${streams} | ${src} |`,
    `| Server RSS idle / peak at ${top ? top.level : '—'} streams | ${r.idleRssMB} / ${r.peakRssMB} MB | ${src} |`,
    `| Startup to the first 200 on /login.html (p50 of ${r.startup.ms.length}) | ${ms(r.startup.p50)} | ${src} |`,
    '',
    `M3 baseline cell: p95 ${ms(o.p95)} (p50 ${ms(o.p50)}, p99 ${ms(o.p99)})`,
    `M4 baseline cell: ${top ? `${top.level} streams: ${top.gateway.completed} done, ${top.gateway.errors} errors, overhead p95 ${ms(top.overheadP95)}, peak RSS ${r.peakRssMB} MB` : '—'}`,
    `M5 baseline cell, appended: bench idle ${r.idleRssMB} MB`,
    `M7 baseline cell, appended: bench startup p50 ${ms(r.startup.p50)}`,
  ].join('\n');
}

module.exports = { compare, metrics, format, baseline };

if (require.main === module) {
  const args = process.argv.slice(2);
  if (args[0] === '--baseline' && args[1]) {
    process.stdout.write(`${baseline(JSON.parse(fs.readFileSync(args[1], 'utf8')), args[1])}\n`);
    process.exit(0);
  }
  const [fa, fb] = args;
  if (!fa || !fb) {
    console.error('usage: node bench/compare.js a.json b.json | node bench/compare.js --baseline result.json');
    process.exit(2);
  }
  const a = JSON.parse(fs.readFileSync(fa, 'utf8'));
  const b = JSON.parse(fs.readFileSync(fb, 'utf8'));
  process.stdout.write(format(compare(a, b), a, b));
}
