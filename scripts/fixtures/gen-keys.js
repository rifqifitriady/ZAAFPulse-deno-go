// Fixtures for the Go gateway's API keys (P4b, slice S4): the real lib/gateway/keys.js run over a fixed corpus, written
// to gw/testdata/keys/<n>-<fn>-<name>.json as { fn, in, out }. gw/engine/keys_test.go replays every file.
//   secret:       requestSecret(headers, url)            -> the secret, or null
//   allows:       allows(patterns, value)                -> true or false (the per-key model and agent globs)
//   authenticate: authenticate(secret) over keys at now  -> { status, error } or { key: id }
//   authorize:    authorize(key, { scope, model, agent }) -> the refusal, or null
//   rateLimit:    rateLimit(key) at each time, in order  -> the seconds to wait for each (0: allowed)
// Run: node scripts/fixtures/gen-keys.js (CI runs every scripts/fixtures/gen-*.js and fails on a diff).
const fs = require('fs');
const path = require('path');
const { createKeys, allows, hash, requestSecret } = require('../../lib/gateway/keys');

const OUT = path.join(__dirname, '..', '..', 'gw', 'testdata', 'keys');
const NOW = 1767225600000;
const SECRET = 'zp_fixtureSecret0123456789abcdefghij';
const key = (id, extra = {}) => ({
  id,
  name: id,
  prefix: 'zp_fixture…',
  hash: hash(`${SECRET}-${id}`),
  scopes: ['llm'],
  models: [],
  agents: [],
  knowledge: [],
  budgetUSD: null,
  rpm: 60,
  privateOnly: false,
  expiresAt: null,
  revokedAt: null,
  createdAt: NOW - 1000,
  ...extra,
});
const KEYS = [
  key('k-ok'),
  key('k-revoked', { revokedAt: NOW - 10 }),
  key('k-expired', { expiresAt: NOW - 1 }),
  key('k-expires-now', { expiresAt: NOW }),
];

const cases = [];
const add = (fn, name, input, out) => cases.push({ fn, name, in: input, out });

// requestSecret
for (const [name, headers, url] of [
  ['bearer', { authorization: 'Bearer zp_a' }, '/v1/chat/completions'],
  ['bearer-trimmed', { authorization: 'Bearer   zp_a \t' }, '/v1/chat/completions'],
  ['bearer-empty-wins', { authorization: 'Bearer ', 'x-api-key': 'zp_b' }, '/v1/chat/completions'],
  ['bearer-lower-ignored', { authorization: 'bearer zp_a', 'x-api-key': 'zp_b' }, '/v1/chat/completions'],
  ['basic-ignored', { authorization: 'Basic eDp5' }, '/v1/chat/completions'],
  ['x-api-key', { 'x-api-key': 'zp_b' }, '/v1/messages'],
  ['x-goog-api-key', { 'x-goog-api-key': 'zp_c' }, '/v1beta/models/x:generateContent'],
  ['x-api-key-first', { 'x-api-key': 'zp_b', 'x-goog-api-key': 'zp_c' }, '/v1/chat/completions'],
  ['query-on-v1beta', {}, '/v1beta/models/x:generateContent?key=zp_q'],
  ['query-elsewhere-ignored', {}, '/v1/chat/completions?key=zp_q'],
  ['query-encoded', {}, '/v1beta/models?key=zp_%71+x'],
  ['none', {}, '/v1/chat/completions'],
  ['empty-x-api-key', { 'x-api-key': '' }, '/v1/chat/completions'],
])
  add('secret', name, { headers, url }, requestSecret(headers, url) ?? null);

// allows
for (const [name, patterns, value] of [
  ['empty-list', [], 'oa/m1'],
  ['exact', ['oa/m1'], 'oa/m1'],
  ['star', ['oa/*'], 'oa/m1'],
  ['star-middle', ['oa/*-mini'], 'oa/gpt-5-mini'],
  ['no-match', ['oa/*'], 'pv/m1'],
  ['case-insensitive', ['OA/M*'], 'oa/m1'],
  ['dot-is-literal', ['oa/m.1'], 'oa/mx1'],
  ['regex-chars-literal', ['oa/(m)+[1]?'], 'oa/(m)+[1]?'],
  ['star-not-newline', ['oa/*'], 'oa/a\nb'],
  ['several', ['pv/*', 'oa/m1'], 'oa/m1'],
  ['anchored', ['m1'], 'oa/m1'],
  ['long-s-not-s', ['oa/s*'], 'oa/ſ'],
  ['kelvin', ['oa/k'], 'oa/K'],
  ['sharp-s', ['oa/ß'], 'oa/ẞ'],
  ['greek-sigma', ['oa/σ'], 'oa/Σ'],
  ['ypogegrammeni-not-folded', ['oa/\u1f80'], 'oa/\u1f88'],
  ['astral', ['oa/*'], 'oa/\u{1F600}'],
  ['empty-value', ['*'], ''],
])
  add('allows', name, { patterns, value }, allows(patterns, value));

// authenticate
const store = (keys) => {
  const data = { keys: structuredClone(keys) };
  return { get: () => data, save: () => {} };
};
for (const [name, secret, at] of [
  ['ok', `${SECRET}-k-ok`, NOW],
  ['not-zp', 'sk-other', NOW],
  ['missing', null, NOW],
  ['unknown', 'zp_doesnotexist0123456789', NOW],
  ['revoked', `${SECRET}-k-revoked`, NOW],
  ['expired', `${SECRET}-k-expired`, NOW],
  ['expires-at-now-still-valid', `${SECRET}-k-expires-now`, NOW],
  ['expires-at-now-plus-one', `${SECRET}-k-expires-now`, NOW + 1],
  ['zp-prefix-only', 'zp_', NOW],
]) {
  const keys = createKeys(store(KEYS), { now: () => at });
  const r = keys.authenticate(secret ?? undefined);
  add(
    'authenticate',
    name,
    { keys: KEYS, secret, now: at },
    r.error ? { status: r.status, error: r.error } : { key: r.key.id },
  );
}

// authorize
for (const [name, k, want] of [
  ['scope-ok', { scopes: ['llm'] }, { scope: 'llm' }],
  ['scope-missing', { scopes: ['agents'] }, { scope: 'llm' }],
  ['no-scope-asked', { scopes: [] }, {}],
  ['model-ok', { scopes: ['llm'], models: ['oa/*'] }, { scope: 'llm', model: 'oa/m1' }],
  ['model-refused', { scopes: ['llm'], models: ['oa/*'] }, { scope: 'llm', model: 'pv/m1' }],
  ['scope-before-model', { scopes: [], models: ['oa/*'] }, { scope: 'llm', model: 'pv/m1' }],
  ['agent-refused', { scopes: ['agents'], agents: ['a*'] }, { agent: 'b' }],
  ['model-quotes', { scopes: ['llm'], models: ['x'] }, { model: 'say "hi"' }],
]) {
  const keys = createKeys(store([]), { now: () => NOW });
  add(
    'authorize',
    name,
    { key: { models: [], agents: [], ...k }, want },
    keys.authorize({ models: [], agents: [], ...k }, want),
  );
}

// rateLimit: a sliding one-minute window
for (const [name, rpm, times] of [
  ['under', 3, [0, 1000, 2000]],
  ['over', 2, [0, 1000, 2000, 3000]],
  ['window-slides', 2, [0, 1000, 60000, 60500, 61000]],
  ['refused-not-counted', 1, [0, 10, 20, 59999, 60000]],
  ['retry-after-rounds-up', 1, [0, 100]],
  ['default-60', 0, Array.from({ length: 61 }, (_, i) => i)],
]) {
  let t = NOW;
  const keys = createKeys(store([]), { now: () => t });
  const out = times.map((dt) => {
    t = NOW + dt;
    return keys.rateLimit({ id: 'k', rpm });
  });
  add('rateLimit', name, { rpm, times }, out);
}

// Canonicalize (ECMAScript 22.2.2.7.3, a regular expression with i and without u), the folding allows() relies on: for
// every BMP code unit whose canonical unit is another one, that unit. Computed with this runtime's own toUpperCase,
// which is what the JS gateway's /i globs use (P4b review I3).
// Three units Node 24 upper-cases and Deno 2.9.8 does not (U+A7CF, U+A7D3, U+A7D5, Unicode 17 letters its ICU data
// lacks) are left out: the corpus holds only what both runtimes agree on, and Go keeps Deno's answer (no folding),
// pinned by gw/engine/keys_test.go TestUnitsTheRuntimesFoldDifferently.
const DIVERGENT = new Set([0xa7cf, 0xa7d3, 0xa7d5]);
const canonical = {};
for (let u = 0; u < 0x10000; u++) {
  if ((u >= 0xd800 && u <= 0xdfff) || DIVERGENT.has(u)) continue;
  const up = String.fromCharCode(u).toUpperCase();
  if (up.length !== 1) continue;
  const cu = up.charCodeAt(0);
  if (u >= 128 && cu < 128) continue;
  if (cu !== u) canonical[u.toString(16)] = cu.toString(16);
}
add('canonicalize', 'bmp', null, canonical);

fs.rmSync(OUT, { recursive: true, force: true });
fs.mkdirSync(OUT, { recursive: true });
cases.forEach((c, i) => {
  const file = path.join(OUT, `${String(i + 1).padStart(2, '0')}-${c.fn}-${c.name}.json`);
  fs.writeFileSync(file, `${JSON.stringify({ fn: c.fn, in: c.in, out: c.out })}\n`);
});
console.log(`gen-keys: ${cases.length} cases in ${path.relative(process.cwd(), OUT)}`);
