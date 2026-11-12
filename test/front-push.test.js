// The config.json-to-IPC converter (lib/front/push.js, P4b): every body it builds is a valid v1 message, keys carry
// their hash and never a secret, prices are nano-USD, a credential that does not resolve is left out, and the budget
// snapshot holds the blocking budgets and the keys' cost centers.
const test = require('node:test');
const assert = require('node:assert/strict');
const { configPush, secretsPush, snapshotPush, keyAuth } = require('../lib/front/push');
const { frontSection } = require('../lib/front');
const { check } = require('../lib/ipc/schema');
const { resolvePolicy } = require('../lib/gateway/guardrails');

const msg = (type, body) => ({ v: 1, type, seq: 2, ts: 0, body: { ...body, gen: 1 } });
const GATEWAY = {
  engine: 'front',
  providers: [
    { id: 'oa', preset: 'custom', baseUrl: 'http://127.0.0.1:9/v1', models: ['m1'], apiKey: { keychain: 'acc.gw-oa' } },
    { id: 'pv', preset: 'ollama', baseUrl: 'http://localhost:11434/v1', tier: 'private', aliases: { fast: 'llama' } },
    {
      id: 'az',
      preset: 'azure',
      baseUrl: 'https://x/v1',
      apiKey: { env: 'AZ' },
      auth: { type: 'oauth2', grant: 'client_credentials', clientId: 'c' },
    },
    { id: 'bad', preset: 'custom', baseUrl: 'http://x/v1', apiKey: { env: 'MISSING' }, tokenParam: 'nope' },
  ],
  routes: [{ name: 'router/r', targets: ['oa/m1', 'pv/llama'], strategy: 'fallback' }],
  guardrails: { secrets: 'off', pii: 'nonsense' },
  tokenSaver: false,
  modelMap: [{ match: 'gpt-*', to: 'oa/m1' }],
};
const KEY = {
  id: 'key_1',
  name: 'ci',
  prefix: 'zp_abc…',
  hash: 'a'.repeat(64),
  scopes: ['llm'],
  models: ['oa/*'],
  agents: [],
  budgetUSD: null,
  rpm: 60,
  privateOnly: false,
  expiresAt: null,
  createdAt: 1,
  guardrails: { pii: 'block', injection: 'redact' },
};

test('configPush: a valid config.push with keys by hash, prices in nano-USD, no secret reference', () => {
  const body = configPush({
    gateway: GATEWAY,
    keys: [KEY, { id: 'nohash' }],
    pricing: { m1: { input: 1, output: 2.5, cacheRead: 0.1, note: 'x' } },
  });
  const front = frontSection({ host: '127.0.0.1', port: 4777, upstreamPort: 5000 });
  assert.deepEqual(check(msg('config.push', { ...body, front })), { errors: [], unknownType: false });
  assert.deepEqual(body.gateway.providers[0], {
    id: 'oa',
    preset: 'custom',
    baseUrl: 'http://127.0.0.1:9/v1',
    models: ['m1'],
    auth: { type: 'key' },
  });
  assert.deepEqual(body.gateway.providers[2].auth, { type: 'oauth2', clientId: 'c', grant: 'client_credentials' });
  assert.equal('tokenParam' in body.gateway.providers[3], false);
  assert.ok(!JSON.stringify(body).includes('acc.gw-oa'), 'no secret reference');
  assert.deepEqual(body.gateway.guardrails, { scope: 'external', secrets: 'off', pii: 'off', injection: 'log' });
  assert.equal(body.keys.length, 1, 'a key without a hash is left out');
  assert.deepEqual(body.keys[0].guardrails, { pii: 'block' }, 'only modes a check has');
  assert.deepEqual(body.pricing.m1, { input: 1e9, output: 2.5e9, cacheRead: 1e8 });
  assert.equal(configPush({ gateway: {}, keys: [], pricing: {} }).gateway.tokenSaver, true, 'on unless false');
});

test('keyAuth: every required field, with defaults for an old key', () => {
  const k = keyAuth({ id: 'old', hash: 'b'.repeat(64), scopes: [] });
  assert.equal(keyAuth({ id: 'old', hash: 'b'.repeat(64) }), null, 'no scopes: JS cannot check it either');
  assert.deepEqual(Object.keys(k).sort(), [
    'agents',
    'approveOwnRuns',
    'budgetUSD',
    'createdAt',
    'expiresAt',
    'guardrails',
    'hash',
    'id',
    'knowledge',
    'lastUsedAt',
    'models',
    'name',
    'prefix',
    'privateOnly',
    'revokedAt',
    'rpm',
    'scopes',
  ]);
  assert.equal(k.rpm, 0);
});

test('secretsPush: key-auth credentials that resolve, and the mask set; others left out', () => {
  const seen = [];
  const body = secretsPush({
    gateway: GATEWAY,
    resolve: (ref, field) => {
      seen.push(field);
      if (ref.keychain === 'acc.gw-oa') return 'sk-oa-123456';
      throw new Error(`${field}: not set`);
    },
  });
  assert.deepEqual(body, { providers: [{ id: 'oa', value: 'sk-oa-123456' }], mask: ['sk-oa-123456'] });
  assert.deepEqual(seen, ['oa credential', 'bad credential'], 'OAuth2 is not resolved here (P4g)');
  assert.deepEqual(check(msg('secrets.handoff', body)), { errors: [], unknownType: false });
});

test('snapshotPush: the blocking budgets with their state, and the keys cost centers', () => {
  const service = {
    list: () => ({
      budgets: [
        {
          id: 'b1',
          name: 'team',
          scope: { dim: 'center', value: 'eng' },
          period: 'month',
          currency: 'USD',
          measure: 'list',
          enforce: 'block',
          amount: 10,
        },
        {
          id: 'b2',
          name: 'watch',
          scope: 'global',
          period: 'day',
          currency: 'USD',
          measure: 'list',
          enforce: 'none',
          amount: 1,
        },
      ],
    }),
    evaluateOne: (b) => ({
      ...b,
      from: '2026-11-01',
      to: '2026-11-30',
      state: 'ok',
      spentUSD: 1,
      amountUSD: 10,
      reason: null,
    }),
    keyCenter: (id) => (id === 'key_1' ? 'eng' : undefined),
  };
  const body = snapshotPush({ service, keyIds: ['key_1', 'key_2'] });
  assert.deepEqual(
    body.budgets.map((b) => b.id),
    ['b1'],
  );
  assert.deepEqual(body.keyCenter, { key_1: 'eng' });
  assert.deepEqual(check({ v: 1, type: 'budget.snapshot', seq: 2, ts: 0, body: { ...body, at: 0 } }), {
    errors: [],
    unknownType: false,
  });
  assert.deepEqual(
    snapshotPush({
      service: {
        list: () => {
          throw new Error('x');
        },
      },
      keyIds: [],
    }),
    { budgets: [], keyCenter: {} },
  );
});

test('configPush: the global guardrails policy is the one lib/gateway/guardrails.js resolves', () => {
  for (const g of [
    undefined,
    null,
    'x',
    {},
    { scope: 'all', secrets: 'block', pii: 'redact', injection: 'off' },
    { scope: 'nope', secrets: 'redact', pii: 'nonsense', injection: 'redact' },
  ])
    assert.deepEqual(
      configPush({ gateway: { guardrails: g }, keys: [], pricing: {} }).gateway.guardrails,
      resolvePolicy(g, null),
      JSON.stringify(g),
    );
});

// P4b review I3: a key's fields as JS reads them, never more permissive in Go. Fractions the dashboard stores keep their
// meaning; a field JS would misread is a key the gateway does not know (it hands the call to the server, which decides).
test('keyAuth: fractional rpm and expiry mean what they mean in JS; a field JS would misread drops the key', () => {
  const { createKeys, hash: sha } = require('../lib/gateway/keys');
  const base = { id: 'k', hash: 'c'.repeat(64), scopes: ['llm'], models: [], agents: [] };
  // rpm 2.5: JS allows 3 a minute (w.length >= 2.5), so does rpm 3.
  let t = 0;
  const keys = createKeys({ get: () => ({ keys: [] }), save: () => {} }, { now: () => t });
  const waits = [0, 1, 2, 3].map((i) => ((t = i), keys.rateLimit({ id: 'k', rpm: 2.5 })));
  assert.deepEqual(
    waits.map((w) => w > 0),
    [false, false, false, true],
  );
  assert.equal(keyAuth({ ...base, rpm: 2.5 }).rpm, 3);
  assert.equal(keyAuth({ ...base, rpm: 0 }).rpm, 0, 'JS reads 0 as 60, Go too');
  // A fractional expiry still expires: now > 1767225600000.5 as soon as now > 1767225600000.
  assert.equal(keyAuth({ ...base, expiresAt: 1767225600000.5 }).expiresAt, 1767225600000);
  assert.equal(keyAuth({ ...base, expiresAt: 0.5 }).expiresAt, -1, 'expired for any now, as in JS');
  assert.equal(keyAuth({ ...base, expiresAt: 0 }).expiresAt, null);
  // revokedAt is "revoked" whenever JS reads it as true.
  assert.ok(keyAuth({ ...base, revokedAt: 'yes' }).revokedAt > 0);
  assert.equal(keyAuth({ ...base, revokedAt: 0 }).revokedAt, null);
  // allows() reads String(pattern): a number in models is that text.
  assert.deepEqual(keyAuth({ ...base, models: ['oa/*', 123] }).models, ['oa/*', '123']);
  assert.equal(keyAuth({ ...base, privateOnly: 1 }).privateOnly, true);
  for (const bad of [
    { scopes: 'llm' },
    { models: 'oa/*' },
    { agents: { a: 1 } },
    { rpm: '5' },
    { rpm: -1 },
    { rpm: Infinity },
    { expiresAt: '2026-01-01' },
    { budgetUSD: 'ten' },
  ])
    assert.equal(keyAuth({ ...base, ...bad }), null, JSON.stringify(bad));
  assert.equal(sha('x').length, 64);
});

// P4b review m5: a credential written into a base URL (user:password@, or a query) never travels in config.push. Such a
// provider goes to the gateway disabled, with only its origin and path, so the gateway hands its calls to the server,
// which calls it as before (fetch refuses a URL with user:password, so those calls already fail there).
test('configPush: a base URL with user info or a query is cut to its origin and path, and the provider disabled', () => {
  const body = configPush({
    gateway: {
      providers: [
        { id: 'u', preset: 'custom', baseUrl: 'https://me:s3cret@llm.example/v1' },
        { id: 'q', preset: 'custom', baseUrl: 'https://llm.example/v1?key=s3cret' },
        { id: 'ok', preset: 'custom', baseUrl: 'https://llm.example/v1' },
        { id: 'bad', preset: 'custom', baseUrl: 'not a url' },
      ],
    },
    keys: [],
    pricing: {},
  });
  assert.ok(!JSON.stringify(body).includes('s3cret'));
  const [u, q, ok, bad] = body.gateway.providers;
  assert.deepEqual([u.baseUrl, u.enabled], ['https://llm.example/v1', false]);
  assert.deepEqual([q.baseUrl, q.enabled], ['https://llm.example/v1', false]);
  assert.deepEqual([ok.baseUrl, ok.enabled], ['https://llm.example/v1', undefined]);
  assert.deepEqual([bad.baseUrl, bad.enabled], ['', false]);
});

// Re-review N2 and N3: values only a hand-edited keys.json can hold.
test('keyAuth: Infinity revoked is revoked; a time or limit past int64 is clamped, never an undecodable push', () => {
  const base = { id: 'k', hash: 'c'.repeat(64), scopes: ['llm'], models: [], agents: [] };
  const inf = JSON.parse('{"revokedAt": 1e400}').revokedAt;
  assert.equal(inf, Infinity);
  assert.equal(keyAuth({ ...base, revokedAt: inf }).revokedAt, 1);
  assert.equal(keyAuth({ ...base, expiresAt: 1e300 }).expiresAt, Number.MAX_SAFE_INTEGER, 'never expires, as in JS');
  assert.equal(keyAuth({ ...base, expiresAt: -1e300 }).expiresAt, -Number.MAX_SAFE_INTEGER, 'expired, as in JS');
  assert.equal(keyAuth({ ...base, rpm: 1e300 }).rpm, Number.MAX_SAFE_INTEGER);
  const body = configPush({
    gateway: {},
    keys: [{ ...base, expiresAt: 1e300, rpm: 1e300, revokedAt: inf }],
    pricing: {},
  });
  assert.deepEqual(check(msg('config.push', body)), { errors: [], unknownType: false });
  assert.ok(Number.isSafeInteger(body.keys[0].expiresAt) && Number.isSafeInteger(body.keys[0].rpm));
});

test('configPush: a key the push drops takes every key with its hash along (JS matches the first one)', () => {
  const good = { id: 'b', hash: 'd'.repeat(64), scopes: ['llm'], models: [], agents: [] };
  const keys = [{ ...good, id: 'a', scopes: 'llm' }, good, { ...good, id: 'c', hash: 'e'.repeat(64) }];
  assert.deepEqual(
    configPush({ gateway: {}, keys, pricing: {} }).keys.map((k) => k.id),
    ['c'],
  );
  // An upper-case hash is the same key to JS (Buffer.from(hash, 'hex')).
  const upper = [{ ...good, id: 'a', hash: 'D'.repeat(64) }, good];
  assert.deepEqual(configPush({ gateway: {}, keys: upper, pricing: {} }).keys, []);
});
