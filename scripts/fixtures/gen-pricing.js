// Fixtures for the go engine's price lookup and cost (gw/engine/pricing.go, P4b): lib/pricing.js normalizeModel and
// makePricer().key over a fixed table, and lib/gateway/usage.js cost in nano-USD. Writes gw/testdata/pricing/<n>-<fn>-
// <name>.json as { fn, in, out }.
// Run: node scripts/fixtures/gen-pricing.js (CI runs every scripts/fixtures/gen-*.js and fails on a diff).
const fs = require('fs');
const path = require('path');
const { normalizeModel, makePricer } = require('../../lib/pricing');
const { createUsage } = require('../../lib/gateway/usage');
const { toNano } = require('../../lib/money');
const { configPush } = require('../../lib/front/push');

const OUT = path.join(__dirname, '..', '..', 'gw', 'testdata', 'pricing');
const TABLE = {
  'claude-opus-5': { input: 5, output: 25, cacheRead: 0.5 },
  'claude-opus-5-5': { input: 5, output: 25 },
  'gpt-5': { input: 1.25, output: 10 },
  'gpt-': { input: 9, output: 9 },
  'gpt-5.6': { input: 2, output: 12 },
  'llama-9': { input: 0.1, output: 0.3, cacheRead: 0 },
  m1: { input: 1, output: 2 },
};
const MODELS = [
  'oa/m1',
  'M1',
  ' oa/m1 ',
  'anthropic/claude-opus-5.5',
  'us.anthropic.claude-opus-5-20260101-v1:0',
  'claude-opus-50',
  'openai/gpt-5',
  'gpt-5.6',
  'gpt-5.a',
  'gpt-5-mini',
  'gpt-6.1',
  'vertex/claude-opus-5@20260101',
  'x/llama-9[1m]',
  'x/llama-9:free',
  'unknown-model',
  '',
];
const cases = [];
for (const m of MODELS) cases.push({ fn: 'normalize', name: m, in: m, out: normalizeModel(m) });
const pricer = makePricer(TABLE);
for (const m of MODELS) cases.push({ fn: 'key', name: m, in: m, out: pricer.key(m) });
// The usage log is never written (cost() only reads the price table): a path that does not exist, under gw/testdata.
const file = path.join(OUT, '..', '.gen-pricing-unused.jsonl');
const usage = createUsage({ file, getPricing: () => TABLE });
for (const [m, u] of [
  ['oa/m1', { input: 1000, output: 500 }],
  ['x/llama-9', { input: 1000, output: 100, cached: 400 }],
  ['claude-opus-5', { input: 3, output: 7, cached: 2 }],
  ['claude-opus-5', { input: 2, output: 0, cached: 9 }],
  ['gpt-5', { input: 123456789, output: 98765 }],
  ['unknown-model', { input: 10, output: 10 }],
])
  cases.push({ fn: 'cost', name: m, in: { model: m, ...u }, out: toNano(usage.cost(m, u)) });
fs.rmSync(OUT, { recursive: true, force: true });
fs.mkdirSync(OUT, { recursive: true });
const table = configPush({ gateway: {}, keys: [], pricing: TABLE }).pricing;
fs.writeFileSync(path.join(OUT, '00-table.json'), `${JSON.stringify({ fn: 'table', in: TABLE, out: table })}\n`);
cases.forEach((c, i) => {
  const slug =
    c.name
      .replace(/[^A-Za-z0-9]+/g, '-')
      .replace(/^-|-$/g, '')
      .slice(0, 40) || 'empty';
  fs.writeFileSync(
    path.join(OUT, `${String(i + 1).padStart(2, '0')}-${c.fn}-${slug}.json`),
    `${JSON.stringify({ fn: c.fn, in: c.in, out: c.out })}\n`,
  );
});
console.log(`gen-pricing: ${cases.length + 1} cases in ${path.relative(process.cwd(), OUT)}`);
