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
// - seq-<class>-<what> (fn "sequence"), route level: one real createGateway per file (as gen-xlate.js, a fetch stub,
//   a fresh data folder) answers a sequence of client calls, so rotation and cooldowns carry from call to call. in
//   { note, configPush, secretsPush, snapshot, start, steps: [{ advanceMs, request: { method, path, headers, body },
//   answers: [{ status, headers, body } | { status, headers, chunks } | { throw: code }] }] }; out { steps: [{ now,
//   requests: [{ url, method, headers, body }], client: { status, headers, body }, usage: [line, oldest first],
//   events: [{ type: 'provider.note' | 'route.take', body }], state: routeState() }] }. The clock is start plus the
//   advanceMs of every step so far; the random counter runs over the whole sequence. Each step must use every answer.
// - shadow-<class>-<what> (fn "shadow", P4h spec Decision 11): the sequence seq-<class>-<what> again, every call marked
//   as one the go gateway shadows; in { seq }, out { steps: [{ capture }] }: lib/gateway/shadow.js's capture of each
//   call (the route tape, every line, the provider answers with the thrown calls), the runtime's used-body text in the
//   client body written as the seq files write it.
// A number JSON cannot hold (NaN, Infinity) is written as its String(); -0 is written as 0. Headers are written as the
// JS Headers object reads them (lower-case names, values trimmed). No date-time without an offset is written (its
// value would depend on the machine's time zone).
// Run: node scripts/fixtures/gen-router.js. No network, env, subprocess or sys access; writes only under gw/testdata.
const fs = require('fs');
const path = require('path');
const { writeCases, ROOT, FIXED_NOW } = require('./lib'); // first: the random counter and the fixed clock
const { createProviders, resetIn, readQuota } = require('../../lib/gateway/providers');
const { createGateway } = require('../../lib/gateway');
const shadow = require('../../lib/gateway/shadow');
const { createSecrets } = require('../../lib/secrets');
const { createGuard } = require('../../lib/cost/guard');
const { configPush, secretsPush, snapshotPush } = require('../../lib/front/push');
const { hash } = require('../../lib/gateway/keys');
const { toNano } = require('../../lib/money');

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

/* ---------- route level: call sequences through the real gateway ---------- */

// The generator's clock for the sequences: lib.js's fixed time plus the offset the steps advance. Set before any gateway
// is made, since createGateway and createProviders keep the Date.now they were made with.
let clockOffset = 0;
Date.now = () => FIXED_NOW + clockOffset;

const SECRET = 'zp_routerFixtureSecret0123456789abcd';
const OA_KEY = ['sk', 'oa', 'router', 'fixture', '123456'].join('-');
const OB_KEY = ['sk', 'ob', 'router', 'fixture', '778899'].join('-');
const AN_KEY = ['sk', 'an', 'router', 'fixture', '654321'].join('-');
// A secret-shaped value for the guardrail case, built by concatenation (no scanner-shaped literal in this file).
const ANT = ['sk', 'ant', 'api03', 'AbCdEfGhIjKlMnOpQrStUvWxYz0123456789abcd'].join('-');
const SEQ_SECRETS = new Set([SECRET, `${SECRET}3`, `${SECRET}5`, OA_KEY, OB_KEY, AN_KEY, ANT]);

const OFF = { scope: 'external', secrets: 'off', pii: 'off', injection: 'off' };
const SEQ_PROVIDERS = [
  {
    id: 'oa',
    preset: 'custom',
    baseUrl: 'http://127.0.0.1:9/v1',
    tier: 'external',
    approved: true,
    apiKey: { env: 'OA_KEY' },
  },
  {
    id: 'ob',
    preset: 'custom',
    baseUrl: 'http://127.0.0.1:13/v1',
    tier: 'external',
    approved: true,
    apiKey: { env: 'OB_KEY' },
  },
  { id: 'op', preset: 'openai', baseUrl: 'http://127.0.0.1:8/v1', approved: true, apiKey: { env: 'OA_KEY' } },
  { id: 'op2', preset: 'openai', baseUrl: 'http://127.0.0.1:7/v1', approved: true, apiKey: { env: 'OB_KEY' } },
  { id: 'an', preset: 'anthropic', baseUrl: 'http://127.0.0.1:10/v1', approved: true, apiKey: { env: 'AN_KEY' } },
  { id: 'pv', preset: 'ollama', baseUrl: 'http://127.0.0.1:11434/v1' },
  {
    id: 'gm',
    preset: 'gemini',
    baseUrl: 'http://127.0.0.1:12/v1beta/openai',
    approved: true,
    apiKey: { env: 'OA_KEY' },
  },
];
const SEQ_ROUTES = [
  { name: 'router/fb', targets: ['oa/m1', 'ob/m1'], strategy: 'fallback' },
  { name: 'router/rr2', targets: ['oa/m1', 'ob/m1'], strategy: 'round-robin' },
  { name: 'router/rr3', targets: ['oa/m1', 'ob/m1', 'op/gpt-6.1'], strategy: 'round-robin' },
  { name: 'router/mix', targets: ['pv/llama-9', 'oa/m1'], strategy: 'fallback' },
  { name: 'router/an', targets: ['an/claude-x', 'oa/m1'], strategy: 'fallback' },
  { name: 'router/native', targets: ['op/gpt-6.1', 'op2/gpt-6.1'], strategy: 'fallback' },
  { name: 'router/gem', targets: ['gm/gemini-2.5-pro', 'oa/m1'], strategy: 'fallback' },
  { name: 'router/emb', targets: ['oa/e1', 'ob/e1'], strategy: 'fallback' },
  { name: 'router/priced', targets: ['oa/nopr', 'ob/m1'], strategy: 'fallback' },
  { name: 'router/unpriced', targets: ['oa/nopr', 'ob/nopr2'], strategy: 'fallback' },
];
const SEQ_MODEL_MAP = [{ match: 'claude-*', to: 'router/rr2' }];
const SEQ_PRICING = {
  m1: { input: 1, output: 2 },
  'llama-9': { input: 0.1, output: 0.3, cacheRead: 0.01 },
  'gpt-6.1': { input: 2, output: 8 },
  'claude-x': { input: 3, output: 15, cacheRead: 0.3 },
  e1: { input: 0.02, output: 0 },
  'gemini-2.5-pro': { input: 1.25, output: 10 },
};
const seqKey = (id, extra = {}) => ({
  id,
  name: id,
  prefix: 'zp_routerF…',
  hash: hash(id === 'k1' ? SECRET : `${SECRET}${id.slice(1)}`),
  scopes: ['llm'],
  models: [],
  agents: [],
  budgetUSD: null,
  rpm: 10000,
  createdAt: 1,
  ...extra,
});
const SEQ_KEYS = [
  seqKey('k1'),
  seqKey('k3', { budgetUSD: 1 }), // a limited key: priced or private targets only
  seqKey('k5', { privateOnly: true }),
];
const secretOf = { k1: SECRET, k3: `${SECRET}3`, k5: `${SECRET}5` };
const budget = (id, scope, state) => ({
  id,
  name: `Budget ${id}`,
  scope,
  period: 'month',
  currency: 'USD',
  measure: 'list',
  enforce: 'block',
  amount: 1,
  from: '2026-01-01',
  to: '2026-01-31',
  state,
  spentUSD: state === 'exceeded' ? 2 : 0.5,
  amountUSD: 1,
  reason: null,
});
// The budget service the guard and snapshotPush read: budgets with a fixed state, no cost centers.
const fakeService = (budgets) => ({
  list: () => ({ budgets }),
  evaluateOne: (b) => ({ ...b }),
  keyCenter: () => null,
  reportGuard: () => {},
});

// Provider answers: { status, headers, body } whole, { status, headers, chunks } streamed, { throw: code } when fetch
// rejects (a TypeError "fetch failed" with cause.code, as Node's and Deno's fetch give it).
const json = (status, body, headers = {}) => ({
  status,
  headers: { 'content-type': 'application/json', ...headers },
  body: JSON.stringify(body),
});
const chatOk = (content = 'hi') =>
  json(200, {
    id: 'chatcmpl-up',
    object: 'chat.completion',
    model: 'upstream-model',
    choices: [{ index: 0, message: { role: 'assistant', content }, finish_reason: 'stop' }],
    usage: { prompt_tokens: 1000, completion_tokens: 500, prompt_tokens_details: { cached_tokens: 200 } },
  });
const chunk = (delta, finish = null) => ({
  id: 'chatcmpl-up',
  object: 'chat.completion.chunk',
  model: 'upstream-model',
  choices: [{ index: 0, delta, finish_reason: finish }],
});
const chatStream = () => ({
  status: 200,
  headers: { 'content-type': 'text/event-stream' },
  chunks: [
    chunk({ role: 'assistant', content: '' }),
    chunk({ content: 'Hel' }),
    chunk({ content: 'lo' }),
    chunk({}, 'stop'),
    {
      id: 'chatcmpl-up',
      object: 'chat.completion.chunk',
      choices: [],
      usage: { prompt_tokens: 12, completion_tokens: 3 },
    },
  ]
    .map((c) => `data: ${JSON.stringify(c)}\n\n`)
    .concat('data: [DONE]\n\n'),
});
const anMsg = () =>
  json(200, {
    id: 'msg_up',
    type: 'message',
    role: 'assistant',
    model: 'claude-x',
    content: [{ type: 'text', text: 'hi there' }],
    stop_reason: 'end_turn',
    usage: { input_tokens: 10, output_tokens: 5, cache_read_input_tokens: 2 },
  });
const ev = (name, data) => `event: ${name}\ndata: ${JSON.stringify(data)}\n\n`;
const respOk = () =>
  json(200, {
    id: 'resp_up',
    object: 'response',
    model: 'gpt-6.1',
    status: 'completed',
    output: [{ type: 'message', id: 'msg_1', role: 'assistant', content: [{ type: 'output_text', text: 'hi' }] }],
    usage: { input_tokens: 7, output_tokens: 3, input_tokens_details: { cached_tokens: 1 } },
  });
const respStream = () => ({
  status: 200,
  headers: { 'content-type': 'text/event-stream' },
  chunks: [
    ev('response.created', {
      type: 'response.created',
      response: { id: 'resp_up', model: 'gpt-6.1', status: 'in_progress' },
    }),
    ev('response.output_text.delta', { type: 'response.output_text.delta', delta: 'hi' }),
    ev('response.completed', {
      type: 'response.completed',
      response: { id: 'resp_up', model: 'gpt-6.1', status: 'completed', usage: { input_tokens: 7, output_tokens: 3 } },
    }),
  ],
});
const embOk = () =>
  json(200, {
    object: 'list',
    data: [{ object: 'embedding', index: 0, embedding: [0.1, -0.2] }],
    model: 'e1-up',
    usage: { prompt_tokens: 4, total_tokens: 4 },
  });
const THROW = { throw: 'ECONNREFUSED' };
const err = (status, message, headers = {}) => json(status, { error: { message, type: 'upstream' } }, headers);
const R429 = (headers) => err(429, 'slow down', headers);

// Client calls: [path, body, answers, opt]; opt: { advanceMs, key, auth: 'bearer' | 'x-api-key' | 'goog', headers }.
const user = (content = 'hi') => [{ role: 'user', content }];
const CHAT = '/v1/chat/completions';
const MSG = '/v1/messages';
const COUNT = '/v1/messages/count_tokens';
const RESP = '/v1/responses';
const EMB = '/v1/embeddings';
const chat = (model, extra = {}) => ({ model, messages: user(), ...extra });
const msgs = (model, extra = {}) => ({ model, max_tokens: 64, messages: user(), ...extra });
const XA = { auth: 'x-api-key' };
// Token saver: a long tool result (rtk spec R2): every leg sends the shortened text.
const SAVER_DIFF = Array.from({ length: 3 }, (_, f) =>
  [
    `diff --git a/f${f}.js b/f${f}.js`,
    '@@ -1,90 +1,90 @@',
    ...Array.from({ length: 90 }, (_, i) => `+ line ${i}`),
  ].join('\n'),
).join('\n');
const saverTurn = [
  { role: 'user', content: 'go' },
  { role: 'assistant', content: [{ type: 'tool_use', id: 't1', name: 'Bash', input: { command: 'git diff' } }] },
  { role: 'user', content: [{ type: 'tool_result', tool_use_id: 't1', content: SAVER_DIFF }] },
];

// The same tool result in a chat body: the tool call names the command, the tool message carries the output.
const saverChatTurn = [
  { role: 'user', content: 'go' },
  {
    role: 'assistant',
    content: null,
    tool_calls: [{ id: 't1', type: 'function', function: { name: 'Bash', arguments: '{"command":"git diff"}' } }],
  },
  { role: 'tool', tool_call_id: 't1', content: SAVER_DIFF },
];
// A body read after proxyChat cancelled it (a target that fell back, then every later target threw): the runtime's
// TypeError text, which differs between Node and Deno (spec Decision 15f), is written as this marker so both runtimes
// write the same bytes; the Go replay maps Deno's text (the control plane's runtime) to it.
const BODY_USED = '<<runtime: body already used>>';
const BODY_USED_TEXTS = ['Body is unusable: Body has already been read', 'Body already consumed'];
const bodyUsed = (text) => BODY_USED_TEXTS.reduce((t, m) => t.split(m).join(BODY_USED), text);

// [name, note, steps, opt]; opt: { guardrails, budgets, modelMap }.
const SEQUENCES = [
  [
    'seq-chat-fallback-429',
    'chat on a fallback route: a 429 (retry-after 30) falls back to the second target; the next call tries the cooling target last',
    [
      [CHAT, chat('router/fb'), [R429({ 'retry-after': '30' }), chatOk()]],
      [CHAT, chat('router/fb'), [chatOk()], { advanceMs: 1000 }],
    ],
  ],
  [
    'seq-chat-fallback-500',
    'chat on a fallback route: a 500 falls back without a cooldown; the next call tries the first target first',
    [
      [CHAT, chat('router/fb'), [err(500, 'down'), chatOk()]],
      [CHAT, chat('router/fb'), [chatOk()]],
    ],
  ],
  [
    'seq-chat-fallback-throw',
    'chat on a fallback route: a refused connection (599 line) falls back; no cooldown',
    [
      [CHAT, chat('router/fb'), [THROW, chatOk()]],
      [CHAT, chat('router/fb'), [chatOk()]],
    ],
  ],
  [
    'seq-chat-last-408',
    'chat on a fallback route: 503 on the first target, 408 on the last is answered as is; the first target cools down',
    [
      [CHAT, chat('router/fb'), [err(503, 'busy'), err(408, 'too slow')]],
      [CHAT, chat('router/fb'), [chatOk()], { advanceMs: 500 }],
    ],
  ],
  [
    'seq-chat-all-throw',
    'chat on a fallback route: every target refuses the connection: 502 with the last error',
    [[CHAT, chat('router/fb'), [THROW, THROW]]],
  ],
  [
    'seq-chat-rr3',
    'chat on a round-robin route of three over four calls',
    [
      [CHAT, chat('router/rr3'), [chatOk('one')]],
      [CHAT, chat('router/rr3'), [chatOk('two')]],
      [CHAT, chat('router/rr3'), [chatOk('three')]],
      [CHAT, chat('router/rr3'), [chatOk('four')]],
    ],
  ],
  [
    'seq-chat-cooldown',
    'a 429 with remaining 0 and reset 20s cools the first target; it moves back while cooling, returns to the front once the clock passes until, a 2xx clears it, and a later 503 starts again at one strike',
    [
      [
        CHAT,
        chat('router/fb'),
        [R429({ 'x-ratelimit-remaining-requests': '0', 'x-ratelimit-reset-requests': '20s' }), chatOk()],
      ],
      [CHAT, chat('router/fb'), [chatOk()], { advanceMs: 19999 }],
      [CHAT, chat('router/rr2'), [chatOk()]],
      [CHAT, chat('router/fb'), [chatOk()], { advanceMs: 1 }],
      [CHAT, chat('router/fb'), [err(503, 'busy'), chatOk()], { advanceMs: 1000 }],
    ],
  ],
  [
    'seq-chat-private-only',
    'a private-only key: a mixed route keeps its private target only; a route of external targets is 404',
    [
      [CHAT, chat('router/mix'), [chatOk()], { key: 'k5' }],
      [CHAT, chat('router/fb'), [], { key: 'k5' }],
      [CHAT, chat('router/rr2'), [], { key: 'k5' }],
      [CHAT, chat('router/rr2'), [chatOk()]],
    ],
  ],
  [
    'seq-chat-key-budget',
    'a key with a budget: the unpriced target is filtered out; a route of unpriced targets is 402',
    [
      [CHAT, chat('router/priced'), [chatOk()], { key: 'k3' }],
      [CHAT, chat('router/unpriced'), [], { key: 'k3' }],
      [CHAT, chat('router/priced'), [chatOk()]],
    ],
  ],
  [
    'seq-chat-provider-budget',
    'a blocking provider budget used up refuses the first target; the second serves; a model of that provider alone is 402',
    [
      [CHAT, chat('router/fb'), [chatOk()]],
      [CHAT, chat('oa/m1'), []],
    ],
    { budgets: [budget('b1', { dim: 'provider', value: 'oa' }, 'exceeded')] },
  ],
  [
    'seq-chat-guard-second-leg',
    'secrets blocked for the external tier only: the private first leg goes out as is, fails with 500, and the external second leg is blocked (400)',
    [[CHAT, chat('router/mix', { messages: user(`key ${ANT}`) }), [err(500, 'down')]]],
    { guardrails: { ...OFF, secrets: 'block' } },
  ],
  [
    'seq-chat-stream-fallback',
    'a chat stream on a fallback route: the 429 of the first target falls back before the first byte',
    [
      [CHAT, chat('router/fb', { stream: true }), [R429({ 'retry-after': '2' }), chatStream()]],
      [CHAT, chat('router/fb', { stream: true, stream_options: { include_usage: true } }), [chatStream()]],
    ],
  ],
  [
    'seq-chat-an-route',
    'chat on a route whose first target speaks Anthropic: served there; then its 529 falls back to the OpenAI target',
    [
      [CHAT, chat('router/an'), [anMsg()]],
      [
        CHAT,
        chat('router/an'),
        [json(529, { type: 'error', error: { type: 'overloaded_error', message: 'Overloaded' } }), chatOk()],
      ],
      [CHAT, chat('router/an'), [chatOk()], { advanceMs: 1000 }],
    ],
  ],
  [
    'seq-messages-modelmap-rr',
    'messages with a modelMap to a two-target round-robin route take twice: the second target serves every call',
    [
      [MSG, msgs('claude-sonnet-9'), [chatOk('one')], XA],
      [MSG, msgs('claude-sonnet-9'), [chatOk('two')], XA],
      [CHAT, chat('router/rr2'), [chatOk('three')]],
      [MSG, msgs('claude-sonnet-9'), [chatOk('four')], XA],
    ],
  ],
  [
    'seq-messages-stream',
    'a messages stream on a fallback route: the 429 falls back before the first byte; messages on a route with an Anthropic first target is translated',
    [
      [MSG, msgs('router/fb', { stream: true }), [R429({ 'retry-after': '5' }), chatStream()], XA],
      [MSG, msgs('router/an'), [anMsg()], XA],
    ],
  ],
  [
    'seq-count-route',
    'count_tokens on a route: the first target speaks Anthropic and counts; once it cools, the first is OpenAI and the estimate answers; after the wait it counts again',
    [
      [COUNT, msgs('router/an'), [json(200, { input_tokens: 42 })], XA],
      [CHAT, chat('router/an'), [R429({ 'retry-after': '5' }), chatOk()]],
      [COUNT, msgs('router/an'), [], XA],
      [COUNT, msgs('router/an'), [json(200, { input_tokens: 43 })], { ...XA, advanceMs: 5000 }],
    ],
  ],
  [
    'seq-responses-native',
    'responses on a route of native Responses targets: passthrough with its own fallback; the cooling target goes last; a stream',
    [
      [RESP, { model: 'router/native', input: 'hi' }, [R429({ 'retry-after': '10' }), respOk()]],
      [RESP, { model: 'router/native', input: 'hi', stream: true }, [respStream()], { advanceMs: 1000 }],
      [RESP, { model: 'router/native', input: 'hi' }, [THROW, err(500, 'down')], { advanceMs: 10000 }],
    ],
  ],
  [
    'seq-responses-translated',
    'responses on a route of chat targets: translated, with proxyChat fallback; a round-robin route takes twice',
    [
      [RESP, { model: 'router/fb', input: 'hi' }, [err(500, 'down'), chatOk()]],
      [RESP, { model: 'router/rr2', input: 'hi', stream: true }, [chatStream()]],
      [RESP, { model: 'router/rr2', input: 'hi' }, [chatOk()]],
    ],
  ],
  [
    'seq-gemini-route',
    'gemini generateContent on a route: the 503 of the first target falls back',
    [
      [
        '/v1beta/models/router%2Fgem:generateContent',
        { contents: [{ role: 'user', parts: [{ text: 'hi' }] }] },
        [err(503, 'busy'), chatOk()],
        { auth: 'goog' },
      ],
      [
        '/v1beta/models/router%2Fgem:streamGenerateContent?alt=sse',
        { contents: [{ role: 'user', parts: [{ text: 'hi' }] }] },
        [chatStream()],
        { auth: 'goog', advanceMs: 1000 },
      ],
    ],
  ],
  [
    'seq-embeddings-route',
    'embeddings on a route: the first target only, a 429 answered without fallback; the cooling target goes last on the next call',
    [
      [EMB, { model: 'router/emb', input: 'hello' }, [embOk()]],
      [EMB, { model: 'router/emb', input: 'hello' }, [R429({ 'retry-after': '3' })]],
      [EMB, { model: 'router/emb', input: 'hello' }, [embOk()], { advanceMs: 1000 }],
    ],
  ],
  [
    'seq-messages-saver',
    'the token saver on a route call with a tool result: run once; both legs send the shortened text',
    [[MSG, msgs('router/fb', { messages: saverTurn }), [err(500, 'down'), chatOk()], XA]],
  ],
  [
    'seq-takes-per-class',
    'takes per class on a two-target round-robin route, pinned by the counter: count, gemini, embeddings and chat take once, messages and responses twice',
    [
      [COUNT, msgs('router/rr2'), [], XA],
      [
        '/v1beta/models/router%2Frr2:generateContent',
        { contents: [{ role: 'user', parts: [{ text: 'hi' }] }] },
        [chatOk()],
        { auth: 'goog' },
      ],
      [EMB, { model: 'router/rr2', input: 'hello' }, [embOk()]],
      [MSG, msgs('router/rr2'), [chatOk()], XA],
      [RESP, { model: 'router/rr2', input: 'hi' }, [chatOk()]],
      [CHAT, chat('router/rr2'), [chatOk()]],
      [COUNT, msgs('router/rr2'), [], XA],
      [EMB, { model: 'router/rr2', input: 'hello' }, [embOk()]],
    ],
  ],
  [
    'seq-xlate-fallback-then-throw',
    'the copied quirk on the P4d paths: a target falls back, the last throws, and the cancelled body read answers 502 in the client shape; messages whole and stream, responses translated, gemini, and the responses passthrough loop',
    [
      [MSG, msgs('router/fb'), [err(503, 'busy'), THROW], XA],
      [MSG, msgs('router/fb', { stream: true }), [err(503, 'busy'), THROW], XA],
      [RESP, { model: 'router/fb', input: 'hi' }, [err(503, 'busy'), THROW]],
      [
        '/v1beta/models/router%2Ffb:generateContent',
        { contents: [{ role: 'user', parts: [{ text: 'hi' }] }] },
        [err(503, 'busy'), THROW],
        { auth: 'goog' },
      ],
      [RESP, { model: 'router/native', input: 'hi' }, [err(503, 'busy'), THROW]],
    ],
  ],
  [
    'seq-guard-native-count',
    'secrets blocked for the external tier: the responses passthrough leg is blocked (its line names routedTo); count_tokens on a route answers the estimate when its leg is blocked',
    [
      [RESP, { model: 'router/native', input: `key ${ANT}` }, []],
      [COUNT, msgs('router/an', { messages: user(`key ${ANT}`) }), [], XA],
    ],
    { guardrails: { ...OFF, secrets: 'block' } },
  ],
  [
    'seq-responses-native-mapped',
    'a model the modelMap sends to a route of native Responses targets: the passthrough lines name the requested model, the fallback and the 599 lines too',
    [
      [RESP, { model: 'gpt-x1', input: 'hi' }, [err(503, 'busy'), respOk()]],
      [RESP, { model: 'gpt-x1', input: 'hi' }, [THROW, err(500, 'down')], { advanceMs: 60000 }],
    ],
    { modelMap: [{ match: 'gpt-x*', to: 'router/native' }, ...SEQ_MODEL_MAP] },
  ],
  [
    'seq-messages-reasoning-drop',
    'adaptChat drops reasoning_effort on a leg that does not take it into the one list every leg shares: once the Gemini target cools, the OpenAI leg drops it and falls back, and the Gemini answer still names it',
    [
      [MSG, msgs('router/gem', { thinking: { type: 'adaptive' } }), [R429({ 'retry-after': '30' }), chatOk()], XA],
      [MSG, msgs('router/gem', { thinking: { type: 'adaptive' } }), [err(500, 'down'), chatOk()], XA],
    ],
  ],
  [
    'seq-chat-rr-402',
    'a round-robin route whose every target a provider budget refuses: 402, and the take happens before it',
    [
      [CHAT, chat('router/rr2'), []],
      [CHAT, chat('router/rr2'), []],
      [CHAT, chat('router/rr3'), [chatOk()]],
    ],
    {
      budgets: [
        budget('b1', { dim: 'provider', value: 'oa' }, 'exceeded'),
        budget('b2', { dim: 'provider', value: 'ob' }, 'exceeded'),
      ],
    },
  ],
  [
    'seq-chat-fallback-then-throw',
    'a target that fell back keeps the call: the last target throws (599 line), then the cancelled body is read and the runtime error answers 500 (copied quirk, spec Decision 7); whole and stream',
    [
      [CHAT, chat('router/fb'), [err(503, 'busy'), THROW]],
      [CHAT, chat('router/fb', { stream: true }), [err(503, 'busy'), THROW]],
    ],
  ],
  [
    'seq-chat-saver',
    'the token saver on chat route calls: run once on the client body; every leg sends the shortened text; savedTokens only on the answer line, never on the 599, fallback or guard lines',
    [
      [CHAT, chat('router/rr3', { messages: saverChatTurn }), [THROW, err(503, 'busy'), chatOk()]],
      [
        CHAT,
        chat('router/mix', { messages: [...saverChatTurn, { role: 'user', content: `key ${ANT}` }] }),
        [err(500, 'down')],
      ],
    ],
    { guardrails: { ...OFF, secrets: 'block' } },
  ],
  [
    'seq-messages-dropped',
    'a translated route call that drops a server-side tool: x-zp-dropped on the answer, after a fallback',
    [
      [
        MSG,
        msgs('router/fb', { tools: [{ type: 'web_search_20250305', name: 'web_search' }] }),
        [err(500, 'down'), chatOk()],
        XA,
      ],
    ],
  ],
];

const SEQ_HEADERS = ['content-type', 'cache-control', 'access-control-allow-origin', 'retry-after', 'x-zp-dropped'];
const lower = (h) => Object.fromEntries(Object.entries(h || {}).map(([k, v]) => [k.toLowerCase(), String(v)]));
// A request as gateway.handle reads it: headers, the target, a loopback client, the body as one chunk.
const requestOf = (url, headers, text) => ({
  method: 'POST',
  url,
  headers,
  socket: { remoteAddress: '127.0.0.1', encrypted: false },
  on() {},
  once() {},
  async *[Symbol.asyncIterator]() {
    yield Buffer.from(text, 'utf8');
  },
});
// A response that keeps what the gateway writes.
function responseOf() {
  const res = {
    statusCode: 200,
    headers: {},
    body: '',
    headersSent: false,
    writableEnded: false,
    writeHead(status, h = {}) {
      res.statusCode = status;
      res.headersSent = true;
      for (const [k, v] of Object.entries(h)) res.headers[k.toLowerCase()] = String(v);
      return res;
    },
    setHeader: (k, v) => (res.headers[k.toLowerCase()] = String(v)),
    write: (c) => ((res.body += String(c)), true),
    end(c) {
      if (c !== undefined && typeof c !== 'function') res.body += String(c);
      res.writableEnded = true;
      return res;
    },
    on() {},
    once() {},
  };
  return res;
}
// A usage line as the fixtures compare it: no ts or ms; cost in nano-dollars; ttft as absent, null or set.
const lineOf = ({ ts: _ts, ms: _ms, ttft, cost, ...rest }) => ({
  ...rest,
  cost: toNano(cost),
  ttft: ttft === undefined ? 'absent' : ttft === null ? null : 'set',
});

const seqSecrets = () =>
  createSecrets({
    env: { OA_KEY, OB_KEY, AN_KEY },
    exec: () => {
      throw new Error('no keychain');
    },
  });

// One gateway per sequence, fresh data folder and clock: its steps share the rotation counters and the cooldowns.
async function runSequence(dataDir, gw, service, steps, { capture = false } = {}) {
  fs.rmSync(dataDir, { recursive: true, force: true });
  fs.mkdirSync(dataDir, { recursive: true });
  fs.writeFileSync(path.join(dataDir, 'keys.json'), JSON.stringify({ keys: SEQ_KEYS }));
  clockOffset = 0;
  let queue = [];
  let sent = [];
  let problem = null;
  const fetchImpl = async (url, init = {}) => {
    sent.push({
      url: String(url),
      method: init.method || 'GET',
      headers: lower(init.headers),
      body: init.body === undefined ? null : String(init.body),
    });
    const a = queue[sent.length - 1];
    if (!a) {
      problem = problem || `request ${sent.length} (${url}) has no answer`;
      throw new Error(problem);
    }
    if (a.throw) throw Object.assign(new TypeError('fetch failed'), { cause: { code: a.throw } });
    if (a.chunks) {
      const pieces = a.chunks.map((c) => new Uint8Array(Buffer.from(c, 'utf8')));
      let i = 0;
      const body = new ReadableStream({
        pull(ctrl) {
          if (i < pieces.length) ctrl.enqueue(pieces[i++]);
          else ctrl.close();
        },
      });
      return new Response(body, { status: a.status, headers: a.headers });
    }
    return new Response(Buffer.from(a.body, 'utf8'), { status: a.status, headers: a.headers });
  };
  const guard = createGuard({ service, now: () => FIXED_NOW });
  const gateway = createGateway({
    dataDir,
    getConfig: () => ({ gateway: gw }),
    writeGatewayConfig: () => {},
    secrets: seqSecrets(),
    resolveSecret: () => undefined,
    getPricing: () => SEQ_PRICING,
    getIntegrations: () => ({ listTools: async () => [], callTool: async () => ({}) }),
    fetchImpl,
    toolsHome: dataDir,
    budgetGuard: {
      check: (ctx) => guard.check(ctx),
      keyLimited: (id) => guard.keyLimited(id),
      record: () => {},
      failed: () => {},
    },
  });
  let events = [];
  gateway.onRouteEvent((type, body) => events.push({ type, body: JSON.parse(JSON.stringify(body)) }));
  const out = [];
  try {
    for (const [i, step] of steps.entries()) {
      clockOffset += step.advanceMs;
      queue = step.answers;
      sent = [];
      events = [];
      const before = gateway.usage.recent(2000).length;
      const res = responseOf();
      const r = step.request;
      const req = requestOf(r.path, r.headers, r.body);
      const id = `5ad0${String(i).padStart(12, '0')}`;
      if (capture) Object.assign(req, { [shadow.SHADOW_ID]: id });
      await gateway.handle(req, res, new URL(r.path, 'http://localhost'));
      if (problem) throw new Error(`step ${i}: ${problem}`);
      if (sent.length !== queue.length)
        throw new Error(`step ${i}: ${queue.length} answers given, ${sent.length} provider requests made`);
      if (capture) {
        const c = shadow.takeCapture(id);
        if (c && c.client && typeof c.client.body === 'string') c.client.body = bodyUsed(c.client.body);
        out.push({ capture: c });
        continue;
      }
      const lines = gateway.usage.recent(2000);
      out.push({
        now: FIXED_NOW + clockOffset,
        requests: sent,
        client: {
          status: res.statusCode,
          headers: Object.fromEntries(SEQ_HEADERS.map((h) => [h, res.headers[h] ?? null])),
          body: bodyUsed(res.body),
        },
        usage: lines
          .slice(0, lines.length - before)
          .reverse()
          .map(lineOf),
        events,
        state: gateway.routeState(),
      });
    }
  } finally {
    gateway.flush();
    clockOffset = 0;
  }
  return { steps: out };
}

function sequenceCases(dataDir) {
  const sec = seqSecrets();
  return SEQUENCES.map(([name, note, calls, opt = {}]) => {
    const gw = {
      providers: SEQ_PROVIDERS,
      routes: SEQ_ROUTES,
      guardrails: opt.guardrails || OFF,
      defaults: { model: '', plannerModel: '', codeModel: '' },
      tokenSaver: true,
      modelMap: opt.modelMap || SEQ_MODEL_MAP,
    };
    const service = fakeService(opt.budgets || []);
    const steps = calls.map(([url, body, answers, o = {}]) => {
      const secret = secretOf[o.key || 'k1'];
      const auth = o.auth || 'bearer';
      return {
        advanceMs: o.advanceMs || 0,
        request: {
          method: 'POST',
          path: url,
          headers: {
            'content-type': 'application/json',
            'user-agent': 'claude-cli/9.9.9',
            ...(auth === 'bearer' ? { authorization: `Bearer ${secret}` } : {}),
            ...(auth === 'x-api-key' ? { 'x-api-key': secret } : {}),
            ...(auth === 'goog' ? { 'x-goog-api-key': secret } : {}),
            ...(o.headers || {}),
          },
          body: JSON.stringify(body),
        },
        answers,
      };
    });
    const args = {
      note,
      configPush: configPush({ gateway: gw, keys: SEQ_KEYS, pricing: SEQ_PRICING }),
      secretsPush: secretsPush({ gateway: gw, resolve: (ref, f) => sec.resolve(ref, f) }),
      snapshot: { ...snapshotPush({ service, keyIds: SEQ_KEYS.map((k) => k.id), now: FIXED_NOW }), at: FIXED_NOW },
      start: FIXED_NOW,
      steps,
    };
    return {
      name,
      fn: 'sequence',
      args,
      dataDir,
      gw,
      service,
      call: (a) => runSequence(dataDir, gw, service, a.steps),
    };
  });
}

// The same sequences with every call shadowed: the capture of each (P4h spec Decision 11).
function shadowCases(seqs) {
  return seqs.map((c) => ({
    name: c.name.replace(/^seq-/, 'shadow-'),
    fn: 'shadow',
    args: { seq: c.name },
    call: () => runSequence(c.dataDir, c.gw, c.service, JSON.parse(JSON.stringify(c.args.steps)), { capture: true }),
  }));
}

async function main() {
  const dataDir = path.join(ROOT, 'gw', 'testdata', '.gen-router-data');
  const seqs = sequenceCases(dataDir);
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
    ...seqs,
    ...shadowCases(seqs),
  ];
  try {
    await writeCases('router', cases, { secrets: SEQ_SECRETS });
  } finally {
    fs.rmSync(dataDir, { recursive: true, force: true });
  }
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
