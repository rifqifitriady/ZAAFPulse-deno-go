// Route fixtures (P4h spec Decision 12), function level: the real lib/gateway/providers.js resetIn and readQuota over a
// fixed corpus, and a real createProviders (stub config, a fetch that answers from the step, a step clock passed
// through its `now` option) driven by sequences of provider answers (noteResponse, through request) and of resolveAll
// calls, with the state the dashboard reads (providers.quota()) after each step. gw/engine/routes_test.go replays
// every file through the Go port (gw/engine/routes.go).
// Writes gw/testdata/router/<name>.json through scripts/fixtures/lib.js as { fn, in, out }:
// - fn-resetin: in { now, values }, out [ms | null] per value;
// - fn-readquota: in { now, headers: [{ name: value }] }, out [readQuota(...)] per headers object;
// - fn-note: in { providers, sequences: [{ note, steps: [{ at, provider?, status?, headers? }] }] }, out per sequence
//   per step { reading, quota: { id: record }, cooldown: { id: { until, strikes, reason } | null } };
// - fn-resolve: in { providers, routes, sequences: [{ note, steps: [{ at, call?, privateOnly?, provider?, status?,
//   headers? }] }] }, out per sequence per step { order: [{ name, provider, model }] } for a call, the cooldowns after
//   a note.
// A number JSON cannot hold (NaN, Infinity) is written as its String(); -0 is written as 0. Headers are written as the
// JS Headers object reads them (lower-case names, values trimmed). No date-time without an offset is written (its
// value would depend on the machine's time zone).
// Run: node scripts/fixtures/gen-router.js. No network, env, subprocess or sys access; writes only under gw/testdata.
const { writeCases, FIXED_NOW } = require('./lib'); // first: the random counter and the fixed clock
const { createProviders, resetIn, readQuota } = require('../../lib/gateway/providers');

const T0 = FIXED_NOW;
const S = 1000;

// JSON-safe copy: non-finite numbers become their String(), so the Go side can tell NaN, Infinity and null apart.
const enc = (v) => {
  if (typeof v === 'number') return Number.isFinite(v) ? (Object.is(v, -0) ? 0 : v) : String(v);
  if (Array.isArray(v)) return v.map(enc);
  if (v && typeof v === 'object') return Object.fromEntries(Object.entries(v).map(([k, x]) => [k, enc(x)]));
  return v;
};
const headersOf = (h) => Object.fromEntries(new Headers(h || {}));

const RESETIN = [
  null,
  '',
  '0',
  '1',
  '1.5',
  ' 2 ',
  '\t3\n',
  '0.0001',
  '1.0005',
  '007',
  '1.',
  '.5',
  '-1',
  '+1',
  '1'.padEnd(400, '0'),
  '6m0s',
  '1.5s',
  '250ms',
  '1h2m3s',
  '2ms3',
  '3m',
  '5mss',
  '1.5h',
  '20ms 1s',
  '12.345678s',
  '1d',
  '2026-01-01T10:00:00Z',
  '2026-01-01T10:00:00z',
  '2026-01-01t10:00:00Z',
  '2026-01-01T10:00:00.5Z',
  '2026-01-01T10:00:00.123456Z',
  '2026-01-01T10:00Z',
  '2026-01-01T10:00:00+07:00',
  '2026-01-01T10:00:00-07:30',
  '2026-01-01T10:00:00+23:59',
  '2026-01-01T24:00:00Z',
  '2026-01-01T24:00:01Z',
  '2026-01-01T23:60:00Z',
  '2026-01-01T23:59:60Z',
  '2026-01-01T10Z',
  '2026-01-01T10:00:00.Z',
  '2026-01-01T10:00:00+24:00',
  '2026-01-01T10:00:00+07:60',
  '2026-01-01T10:00:00+07',
  '2026-01-02',
  '2026-02-30',
  '2026-02-31T00:00:00Z',
  '2026-13-01',
  '2026-00-10',
  '2026-01-00',
  '2026-01-32',
  '+002026-01-02T00:00:00Z',
  '-000000-01-01T00:00:00Z',
  '+275760-09-13T00:00:00Z',
  '+275760-09-13T00:00:00.001Z',
  '2026-01-01 10:00:00Z',
  '2026-01-01 10:00Z',
  '2026-01-01 10:00:00.5Z',
  '2026-01-01 10:00:00+07:00',
  '2026-01-01 24:00Z',
  '2026-01-01 24:01Z',
  '2026-01-01 10:60Z',
  '2026-01-32 10:00Z',
  '2026-02-30 10:00Z',
  '2026-01-01 24:00:00.000Z',
  '2026-01-01 24:00:00.001Z',
  '2026-01-01T24:00:00.000Z',
  '2026-01-01T24:00:00.001Z',
  '2026-01-01T24:00:00.0001Z',
  '2026-01-01 24:00:00.0001Z',
  '2026-01-01T10:00:00.9999Z',
  '2026-01-01 10:00:00.Z',
  '2026-01-01 10:00:60Z',
  '0026-01-02 00:00Z',
  '0100-01-01 00:00Z',
  '0026-01-02T00:00:00Z',
  '-271821-04-20T00:00:00Z',
  '-271821-04-19T23:59:59.999Z',
  '2025-06-01T00:00:00Z',
  '2025-06-01',
  'x2026-01-01',
  '2026-01-01x',
  ' 2026-01-02T00:00:00Z ',
  'abc',
  'Wed, 21 Oct 2026 07:28:00 GMT',
  '1e3',
  '0x10',
  'Infinity',
];

// Header sets for readQuota: each value of the limit and remaining headers goes through Number().
const NUMBERS = [
  '',
  ' ',
  '0',
  '0.0',
  '-0',
  '5',
  ' 5 ',
  '1e3',
  '1E-2',
  '.5',
  '5.',
  '+3',
  '-3',
  '0x10',
  '0X1f',
  '0o17',
  '0b101',
  '-0x10',
  '1_000',
  'Infinity',
  '-Infinity',
  '+Infinity',
  'infinity',
  'NaN',
  'abc',
  '1.2.3',
  '1e',
  'e5',
  '0x',
  '00012',
  '9007199254740993',
  '0x20000000000001',
  '1e400',
  '1e-400',
];
const READQUOTA = [
  {},
  { 'x-ratelimit-limit-requests': '60' },
  { 'x-ratelimit-reset-requests': '1s' },
  {
    'x-ratelimit-limit-requests': '60',
    'x-ratelimit-remaining-requests': '59',
    'x-ratelimit-reset-requests': '1s',
    'x-ratelimit-limit-tokens': '150000',
    'x-ratelimit-remaining-tokens': '149984',
    'x-ratelimit-reset-tokens': '6m0s',
  },
  {
    'anthropic-ratelimit-requests-limit': '50',
    'anthropic-ratelimit-requests-remaining': '0',
    'anthropic-ratelimit-requests-reset': '2026-01-01T00:00:30Z',
    'anthropic-ratelimit-tokens-limit': '40000',
    'anthropic-ratelimit-tokens-remaining': '39000',
    'anthropic-ratelimit-tokens-reset': '2026-01-01T00:01:00Z',
  },
  {
    'x-ratelimit-remaining-tokens': '0',
    'anthropic-ratelimit-tokens-remaining': '7',
    'anthropic-ratelimit-tokens-limit': '9',
  },
  { 'x-ratelimit-remaining-requests': '0', 'x-ratelimit-reset-requests': 'soon' },
  ...NUMBERS.map((n) => ({ 'x-ratelimit-limit-requests': n, 'x-ratelimit-remaining-tokens': n })),
];

// Providers of the note and resolve sequences: external approved (a, b, c), private (loc), disabled (off), not approved
// (ext), with an alias on a.
const PROVIDERS = [
  { id: 'a', preset: 'openai', baseUrl: 'https://a.example.test/v1', approved: true, aliases: { fast: 'm-fast' } },
  { id: 'b', preset: 'groq', baseUrl: 'https://b.example.test/v1', approved: true },
  { id: 'c', preset: 'anthropic', baseUrl: 'https://c.example.test/v1', approved: true, format: 'anthropic' },
  { id: 'loc', preset: 'ollama', baseUrl: 'http://localhost:11434/v1' },
  { id: 'off', preset: 'openai', baseUrl: 'https://off.example.test/v1', approved: true, enabled: false },
  { id: 'ext', preset: 'openai', baseUrl: 'https://ext.example.test/v1', approved: false },
];
const ROUTES = [
  { name: 'router/one', targets: ['a/m1'], strategy: 'fallback' },
  { name: 'router/one-rr', targets: ['a/m1'], strategy: 'round-robin' },
  { name: 'router/two', targets: ['a/m1', 'b/m1'], strategy: 'fallback' },
  { name: 'router/two-rr', targets: ['a/m1', 'b/m1'], strategy: 'round-robin' },
  { name: 'router/three-rr', targets: ['a/m1', 'b/m1', 'c/m1'], strategy: 'round-robin' },
  {
    name: 'router/eight-rr',
    targets: ['a/m1', 'nope/x', 'b/m1', 'off/m1', 'fast', 'ext/m1', 'loc/m1', 'c/m1'],
    strategy: 'round-robin',
  },
  { name: 'router/eight', targets: ['loc/m1', 'a/m1', 'a/m2', 'b/m1', 'b/m2', 'c/m1', 'c/m2', 'loc/m2'] },
  { name: 'router/dead', targets: ['nope/x', 'off/m1', 'ext/m1'], strategy: 'round-robin' },
  { name: 'router/mixed-rr', targets: ['a/m1', 'loc/m1', 'b/m1'], strategy: 'round-robin' },
  { name: 'router/two-rr', targets: ['c/m1'], strategy: 'fallback' }, // a second route of the same name: never found
];

const H429 = (h) => ({ status: 429, headers: h });
const NOTE_SEQUENCES = [
  {
    note: '429 with retry-after: 3, then a 2xx clears',
    steps: [
      { at: T0, provider: 'a', ...H429({ 'retry-after': '3' }) },
      { at: T0 + S, provider: 'a', status: 200, headers: {} },
      { at: T0 + S },
    ],
  },
  {
    note: '429, remaining requests 0 and reset 6m0s: capped at 5 minutes',
    steps: [
      {
        at: T0,
        provider: 'a',
        ...H429({ 'x-ratelimit-remaining-requests': '0', 'x-ratelimit-reset-requests': '6m0s' }),
      },
      { at: T0 + 299 * S },
      { at: T0 + 300 * S },
    ],
  },
  {
    note: '429, remaining tokens 0 and reset 20s (requests not exhausted)',
    steps: [
      {
        at: T0,
        provider: 'b',
        ...H429({
          'x-ratelimit-limit-requests': '30',
          'x-ratelimit-remaining-requests': '29',
          'x-ratelimit-reset-requests': '2s',
          'x-ratelimit-remaining-tokens': '0',
          'x-ratelimit-reset-tokens': '20s',
        }),
      },
    ],
  },
  {
    note: 'anthropic headers: remaining 0 and an RFC 3339 reset; a reading on a 200 without cooldown',
    steps: [
      {
        at: T0,
        provider: 'c',
        status: 200,
        headers: { 'anthropic-ratelimit-requests-limit': '50', 'anthropic-ratelimit-requests-remaining': '49' },
      },
      {
        at: T0 + S,
        provider: 'c',
        ...H429({
          'anthropic-ratelimit-requests-limit': '50',
          'anthropic-ratelimit-requests-remaining': '0',
          'anthropic-ratelimit-requests-reset': '2026-01-01T00:00:45Z',
        }),
      },
    ],
  },
  {
    note: "remaining '' is Number('') = 0; remaining '0.0' is 0; remaining '-0' is 0",
    steps: [
      { at: T0, provider: 'a', ...H429({ 'x-ratelimit-remaining-requests': '', 'x-ratelimit-reset-requests': '7s' }) },
      {
        at: T0,
        provider: 'b',
        ...H429({ 'x-ratelimit-remaining-requests': '0.0', 'x-ratelimit-reset-requests': '9s' }),
      },
      { at: T0, provider: 'c', ...H429({ 'x-ratelimit-remaining-tokens': '-0', 'x-ratelimit-reset-tokens': '11s' }) },
    ],
  },
  {
    note: 'remaining 0 without a reset, and remaining 1: exponential back-off',
    steps: [
      { at: T0, provider: 'a', ...H429({ 'x-ratelimit-remaining-requests': '0' }) },
      { at: T0, provider: 'b', ...H429({ 'x-ratelimit-remaining-requests': '1', 'x-ratelimit-reset-requests': '9s' }) },
    ],
  },
  {
    note: '503 without headers four times: 1, 2, 4, 8 s; then 529 and a 2xx',
    steps: [
      { at: T0, provider: 'a', status: 503, headers: {} },
      { at: T0 + 2 * S, provider: 'a', status: 503, headers: {} },
      { at: T0 + 5 * S, provider: 'a', status: 503, headers: {} },
      { at: T0 + 10 * S, provider: 'a', status: 503, headers: {} },
      { at: T0 + 30 * S, provider: 'a', status: 529, headers: {} },
      { at: T0 + 31 * S, provider: 'a', status: 204, headers: {} },
      { at: T0 + 32 * S, provider: 'a', status: 503, headers: {} },
    ],
  },
  {
    note: 'back-off up to the cap: strikes keep growing while the wait stays at 5 minutes',
    steps: Array.from({ length: 12 }, (_, i) => ({ at: T0 + i * S, provider: 'b', status: 503, headers: {} })),
  },
  {
    note: 'a 401, a 500, a 404 and a 302 leave the cooldown; a 299 clears it',
    steps: [
      { at: T0, provider: 'a', status: 529, headers: { 'retry-after': '60' } },
      { at: T0 + S, provider: 'a', status: 401, headers: {} },
      { at: T0 + S, provider: 'a', status: 500, headers: {} },
      { at: T0 + S, provider: 'a', status: 404, headers: { 'x-ratelimit-remaining-requests': '0' } },
      { at: T0 + S, provider: 'a', status: 302, headers: {} },
      { at: T0 + S, provider: 'a', status: 299, headers: {} },
    ],
  },
  {
    note: 'a reset in the past: max(1000, ...)',
    steps: [
      { at: T0, provider: 'a', ...H429({ 'retry-after': '2025-06-01T00:00:00Z' }) },
      {
        at: T0,
        provider: 'b',
        ...H429({ 'anthropic-ratelimit-tokens-remaining': '0', 'anthropic-ratelimit-tokens-reset': '2025-06-01' }),
      },
      { at: T0, provider: 'c', ...H429({ 'retry-after': '0' }) },
    ],
  },
  {
    note: 'a cooldown that expires by the clock keeps its strikes',
    steps: [
      { at: T0, provider: 'a', status: 503, headers: {} },
      { at: T0 + 999 },
      { at: T0 + S },
      { at: T0 + 5 * S, provider: 'a', status: 503, headers: {} },
      { at: T0 + 7 * S },
      { at: T0 + 8 * S, provider: 'a', status: 503, headers: {} },
    ],
  },
  {
    note: 'retry-after wins over the reset; fractional, huge, date and unreadable retry-after values',
    steps: [
      {
        at: T0,
        provider: 'a',
        ...H429({ 'retry-after': '4', 'x-ratelimit-remaining-requests': '0', 'x-ratelimit-reset-requests': '20s' }),
      },
      { at: T0, provider: 'b', ...H429({ 'retry-after': '1.0005' }) },
      { at: T0, provider: 'c', ...H429({ 'retry-after': '1'.padEnd(400, '0') }) },
      { at: T0 + S, provider: 'a', ...H429({ 'retry-after': '2026-01-01T00:00:31.5Z' }) },
      { at: T0 + S, provider: 'b', ...H429({ 'retry-after': 'Wed, 21 Oct 2026 07:28:00 GMT' }) },
      { at: T0 + S, provider: 'c', status: 503, headers: { 'retry-after': '12.5s' } },
    ],
  },
  {
    note: 'a 429 without headers on two providers, a 2xx for one',
    steps: [
      { at: T0, provider: 'a', status: 429, headers: {} },
      { at: T0, provider: 'b', status: 429, headers: {} },
      { at: T0 + 100, provider: 'b', status: 200, headers: { 'x-ratelimit-remaining-requests': '0' } },
    ],
  },
];

const RESOLVE_SEQUENCES = [
  {
    note: 'fallback and round-robin rotation over 1, 2, 3 and 8 targets',
    steps: [
      ...['router/one', 'router/one-rr', 'router/two', 'router/one-rr'].map((call) => ({ at: T0, call })),
      ...Array.from({ length: 5 }, () => ({ at: T0, call: 'router/two-rr' })),
      ...Array.from({ length: 4 }, () => ({ at: T0, call: 'router/three-rr' })),
      ...Array.from({ length: 9 }, () => ({ at: T0, call: 'router/eight-rr' })),
      ...Array.from({ length: 2 }, () => ({ at: T0, call: 'router/eight' })),
    ],
  },
  {
    note: 'unresolvable, disabled, unapproved, alias, plain model and unknown names',
    steps: [
      { at: T0, call: 'router/dead' },
      { at: T0, call: 'router/dead' },
      { at: T0, call: 'fast' },
      { at: T0, call: 'b/m1' },
      { at: T0, call: 'b/' },
      { at: T0, call: 'off/m1' },
      { at: T0, call: 'ext/m1' },
      { at: T0, call: 'zzz' },
      { at: T0, call: 'router/nope' },
      { at: T0, call: '' },
      { at: T0, call: '/m1' },
    ],
  },
  {
    note: 'a private-only key: rotation still counts, external targets drop',
    steps: [
      { at: T0, call: 'router/mixed-rr', privateOnly: true },
      { at: T0, call: 'router/mixed-rr', privateOnly: true },
      { at: T0, call: 'router/mixed-rr' },
      { at: T0, call: 'router/mixed-rr', privateOnly: true },
      { at: T0, call: 'router/eight', privateOnly: true },
      { at: T0, call: 'a/m1', privateOnly: true },
      { at: T0, call: 'loc/m9', privateOnly: true },
    ],
  },
  {
    note: 'cooling targets move to the back, in stable order, until the cooldown ends',
    steps: [
      { at: T0, call: 'router/three-rr' },
      { at: T0, provider: 'a', ...H429({ 'retry-after': '30' }) },
      { at: T0 + S, call: 'router/three-rr' },
      { at: T0 + S, call: 'router/three-rr' },
      { at: T0 + S, call: 'router/two' },
      { at: T0 + S, call: 'router/eight' },
      { at: T0 + S, provider: 'c', status: 503, headers: {} },
      { at: T0 + S, call: 'router/eight' },
      { at: T0 + S, call: 'router/eight-rr' },
      { at: T0 + S, call: 'a/m1' },
      { at: T0 + S, call: 'fast' },
      { at: T0 + 2 * S, call: 'router/eight' },
      { at: T0 + 31 * S, call: 'router/two' },
      { at: T0 + 31 * S, provider: 'b', status: 429, headers: {} },
      { at: T0 + 31 * S, call: 'router/two' },
      { at: T0 + 31 * S, provider: 'b', status: 200, headers: {} },
      { at: T0 + 31 * S, call: 'router/two' },
    ],
  },
];

// One createProviders per sequence: the step clock, and a fetch that answers with the step's status and headers.
function machine(gateway) {
  let clock = T0;
  let answer = null;
  const providers = createProviders({
    getConfig: () => ({ gateway }),
    secrets: { resolve: () => null, mask: (s) => s },
    fetchImpl: async () => new Response(null, answer),
    now: () => clock,
  });
  const note = async (step) => {
    answer = { status: step.status, headers: step.headers };
    const p = providers.list().find((x) => x.id === step.provider);
    await providers.request(p, '/chat/completions', { body: {} });
  };
  const cooldowns = () => Object.fromEntries(providers.quota().map((q) => [q.id, enc(q.cooldown)]));
  return { providers, note, cooldowns, setClock: (t) => (clock = t) };
}

async function runNotes({ sequences }) {
  const out = [];
  for (const seq of sequences) {
    const m = machine({ providers: PROVIDERS, routes: [] });
    const steps = [];
    for (const step of seq.steps) {
      m.setClock(step.at);
      if (step.provider) await m.note(step);
      const quota = {};
      for (const q of m.providers.quota()) {
        const { id, cooldown, token, balance, ...rest } = q;
        if (Object.keys(rest).length) quota[id] = enc(rest);
      }
      steps.push({
        reading: step.provider ? enc(readQuota(new Headers(step.headers), step.at)) : null,
        quota,
        cooldown: m.cooldowns(),
      });
    }
    out.push(steps);
  }
  return out;
}

async function runResolve({ sequences }) {
  const out = [];
  for (const seq of sequences) {
    const m = machine({ providers: PROVIDERS, routes: ROUTES });
    const steps = [];
    for (const step of seq.steps) {
      m.setClock(step.at);
      if (step.provider) {
        await m.note(step);
        steps.push({ cooldown: m.cooldowns() });
        continue;
      }
      const ts = m.providers.resolveAll(step.call, { privateOnly: !!step.privateOnly });
      steps.push({ order: ts.map((t) => ({ name: t.name, provider: t.provider.id, model: t.model })) });
    }
    out.push(steps);
  }
  return out;
}

// Headers as JS reads them (lower-case names, values trimmed), so Go sees what noteResponse saw.
const normal = (sequences) =>
  sequences.map((s) => ({
    ...s,
    steps: s.steps.map((st) => (st.provider ? { ...st, headers: headersOf(st.headers) } : st)),
  }));

async function main() {
  const cases = [
    {
      name: 'fn-resetin',
      fn: 'resetIn',
      args: { now: T0, values: RESETIN },
      call: ({ now, values }) => values.map((v) => enc(resetIn(v, now))),
    },
    {
      name: 'fn-readquota',
      fn: 'readQuota',
      args: { now: T0, headers: READQUOTA.map(headersOf) },
      call: ({ now, headers }) => headers.map((h) => enc(readQuota(new Headers(h), now))),
    },
    {
      name: 'fn-note',
      fn: 'noteResponse',
      args: { providers: PROVIDERS, sequences: normal(NOTE_SEQUENCES) },
      call: runNotes,
    },
    {
      name: 'fn-resolve',
      fn: 'resolveAll',
      args: { providers: PROVIDERS, routes: ROUTES, sequences: normal(RESOLVE_SEQUENCES) },
      call: runResolve,
    },
  ];
  await writeCases('router', cases);
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
