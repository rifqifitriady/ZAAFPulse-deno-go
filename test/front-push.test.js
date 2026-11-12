// The config.json-to-IPC converter (lib/front/push.js, P4b): every body it builds is a valid v1 message, keys carry
// their hash and never a secret, prices are nano-USD, a credential that does not resolve is left out, and the budget
// snapshot holds the blocking budgets and the keys' cost centers.
const test = require('node:test');
const assert = require('node:assert/strict');
const { configPush, secretsPush, snapshotPush, keyAuth } = require('../lib/front/push');
const { frontSection } = require('../lib/front');
const { check } = require('../lib/ipc/schema');

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
  const k = keyAuth({ id: 'old', hash: 'b'.repeat(64) });
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
