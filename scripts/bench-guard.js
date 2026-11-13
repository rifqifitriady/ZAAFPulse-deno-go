#!/usr/bin/env node
// Times the JS guard (lib/gateway/guardrails.js checkBody) on the real-text corpus of the Go guard benchmark, so the Go
// port can be compared with it on the same machine (P4e entry condition 1: at most 3x the JS).
//   node scripts/bench-guard.js                 print the table
//   node scripts/bench-guard.js --json out.json also write the numbers for gw/guard (ZP_GUARD_BENCH_JS)
// The corpus is the files gw/guard/testdata/bench-corpus.txt names, joined with a newline; the slices, the body shape,
// the policy, the sample counts and the percentile rule match gw/guard/bench_test.go exactly.
const fs = require('fs');
const path = require('path');
const { checkBody } = require('../lib/gateway/guardrails');

const ROOT = path.join(__dirname, '..');

// The same sizes, offsets per size and rounds as bench_test.go (benchPlan).
const PLAN = [
  { size: 32 << 10, offsets: 100, rounds: 3 },
  { size: 128 << 10, offsets: 50, rounds: 3 },
  { size: 1 << 20, offsets: 20, rounds: 2 },
];
const POLICY = { scope: 'all', secrets: 'redact', pii: 'redact', injection: 'log' };

function corpus() {
  const list = fs.readFileSync(path.join(ROOT, 'gw', 'guard', 'testdata', 'bench-corpus.txt'), 'utf8');
  const parts = [];
  for (const raw of list.split('\n')) {
    const line = raw.trim();
    if (!line || line.startsWith('#')) continue;
    parts.push(fs.readFileSync(path.join(ROOT, line)));
  }
  const nl = Buffer.from('\n');
  return Buffer.concat(parts.flatMap((p, i) => (i ? [nl, p] : [p])));
}

// benchText: n bytes from off (the corpus repeated as needed), both ends moved back to a UTF-8 character boundary.
function text(c, off, n) {
  while (c.length < off + n) c = Buffer.concat([c, Buffer.from('\n'), c]);
  let start = off;
  let end = off + n;
  while (start > 0 && (c[start] & 0xc0) === 0x80) start--;
  while (end > start && end < c.length && (c[end] & 0xc0) === 0x80) end--;
  return c.subarray(start, end).toString('utf8');
}

function offsets(total, n, k) {
  const span = Math.max(total - n, 0);
  return Array.from({ length: k }, (_, i) => (k > 1 ? Math.floor((span * i) / (k - 1)) : 0));
}

// Nearest rank: the smallest value with at least p of the samples at or below it.
function pct(sorted, p) {
  return sorted[Math.max(0, Math.ceil(p * sorted.length) - 1)];
}

function main() {
  const c = corpus();
  const out = { runtime: `node ${process.version}`, sizes: {} };
  for (const { size, offsets: k, rounds } of PLAN) {
    const bodies = offsets(c.length, size, k).map((o) => ({
      model: 'm',
      messages: [{ role: 'user', content: text(c, o, size) }],
    }));
    for (const b of bodies.slice(0, Math.min(5, bodies.length))) checkBody(b, POLICY, { format: 'openai-chat' }); // warm-up
    const ms = [];
    for (let r = 0; r < rounds; r++) {
      for (const b of bodies) {
        const t = process.hrtime.bigint();
        checkBody(b, POLICY, { format: 'openai-chat' });
        ms.push(Number(process.hrtime.bigint() - t) / 1e6);
      }
    }
    ms.sort((a, b) => a - b);
    out.sizes[size] = { n: ms.length, p50: pct(ms, 0.5), p99: pct(ms, 0.99), max: ms[ms.length - 1] };
    console.log(
      `${String(size >> 10).padStart(5)} KB  p50 ${pct(ms, 0.5).toFixed(2)} ms  p99 ${pct(ms, 0.99).toFixed(2)} ms  (${ms.length} runs)`,
    );
  }
  const i = process.argv.indexOf('--json');
  if (i > 0) fs.writeFileSync(process.argv[i + 1], JSON.stringify(out, null, 2) + '\n');
}

main();
