// Contract: who may call. Missing, malformed, unknown, revoked and expired keys; scopes; model allow-lists; private-only
// keys; a key's own guardrails modes; the per-key rate limit. Each error in the caller's wire format.
const test = require('node:test');
const assert = require('node:assert/strict');
const { start } = require('./harness');

let h;
test.before(async () => {
  h = await start();
});
test.after(async () => {
  if (h) {
    await h.setGuardrails(DEFAULTS);
    await h.stop();
  }
});

const chatBody = (model = 'oa/m1') => ({ model, messages: [{ role: 'user', content: 'hi' }] });
const MALFORMED = 'missing or malformed API key (Authorization: Bearer zp_…)';
const SECRET = 'sk-ant-api03-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789abcd';
const DEFAULTS = { scope: 'external', secrets: 'redact', pii: 'off', injection: 'log' };
const chat = (key, model) => h.call('/v1/chat/completions', { key, body: chatBody(model) });
const authError = (r, status, message) => {
  assert.equal(r.status, status);
  assert.deepEqual(r.json, { error: { message, type: status === 401 ? 'authentication_error' : 'permission_error' } });
};

const CASES = [
  {
    name: 'no key: 401 authentication_error with a Bearer challenge (OpenAI shape)',
    run: async () => {
      const r = await h.call('/v1/chat/completions', { body: chatBody() });
      assert.equal(r.status, 401);
      assert.equal(r.headers.get('www-authenticate'), 'Bearer realm="zaafpulse"');
      assert.deepEqual(r.json, {
        error: { message: 'missing or malformed API key (Authorization: Bearer zp_…)', type: 'authentication_error' },
      });
    },
  },
  {
    name: 'model allow-list: a model outside the globs is 403 permission_error, one inside answers',
    run: async () => {
      const k = await h.newKey({ scopes: ['llm'], models: ['oa/m*'] });
      const no = await h.call('/v1/chat/completions', { key: k.secret, body: chatBody('pv/m1') });
      assert.equal(no.status, 403);
      assert.equal(no.json.error.type, 'permission_error');
      assert.equal(no.json.error.message, 'this key may not use model "pv/m1"');
      assert.equal((await h.call('/v1/chat/completions', { key: k.secret, body: chatBody('oa/m1') })).status, 200);
    },
  },
  {
    name: 'rate limit: the request past rpm is 429 rate_limit_error with retry-after',
    run: async () => {
      const k = await h.newKey({ scopes: ['llm'], rpm: 2 });
      for (let i = 0; i < 2; i++)
        assert.equal((await h.call('/v1/models', { method: 'GET', key: k.secret })).status, 200);
      const r = await h.call('/v1/models', { method: 'GET', key: k.secret });
      assert.equal(r.status, 429);
      assert.equal(r.json.error.type, 'rate_limit_error');
      assert.equal(r.json.error.message, 'rate limit of 2 requests per minute reached');
      assert.ok(Number(r.headers.get('retry-after')) > 0);
    },
  },
  {
    name: 'a key that is not zp_…: 401 malformed',
    run: async () => authError(await chat('sk-not-ours'), 401, MALFORMED),
  },
  {
    name: 'an unknown zp_ key: 401 unknown API key',
    run: async () => authError(await chat('zp_doesnotexist0123456789'), 401, 'unknown API key'),
  },
  {
    name: 'a revoked key: 401 this API key was revoked',
    run: async () => {
      const k = await h.newKey({ scopes: ['llm'] });
      assert.equal((await chat(k.secret)).status, 200);
      await h.admin('keys', { action: 'revoke', id: k.id });
      authError(await chat(k.secret), 401, 'this API key was revoked');
    },
  },
  {
    name: 'a deleted key: 401 unknown API key',
    run: async () => {
      const k = await h.newKey({ scopes: ['llm'] });
      await h.admin('keys', { action: 'delete', id: k.id });
      authError(await chat(k.secret), 401, 'unknown API key');
    },
  },
  {
    name: 'an expired key: 401 this API key has expired',
    run: async () => {
      const k = await h.newKey({ scopes: ['llm'], expiresAt: Date.now() - 1000 });
      authError(await chat(k.secret), 401, 'this API key has expired');
    },
  },
  {
    name: '401 in the Anthropic shape on /v1/messages',
    run: async () => {
      const r = await h.call('/v1/messages', {
        body: { model: 'an/claude-x', max_tokens: 5, messages: [{ role: 'user', content: 'x' }] },
      });
      assert.equal(r.status, 401);
      assert.deepEqual(r.json, { type: 'error', error: { type: 'authentication_error', message: MALFORMED } });
    },
  },
  {
    name: '401 in the Gemini shape',
    run: async () => {
      const r = await h.call('/v1beta/models/oa%2Fm1:generateContent', {
        body: { contents: [{ role: 'user', parts: [{ text: 'x' }] }] },
      });
      assert.equal(r.status, 401);
      assert.deepEqual(r.json, { error: { code: 401, message: MALFORMED, status: 'UNAUTHENTICATED' } });
    },
  },
  {
    name: 'x-api-key is accepted on /v1/chat/completions',
    run: async () => {
      const r = await h.call('/v1/chat/completions', { headers: { 'x-api-key': h.key }, body: chatBody() });
      assert.equal(r.status, 200);
    },
  },
  {
    name: 'no llm scope on /v1/messages: 403 permission_error in the Anthropic shape',
    run: async () => {
      const k = await h.newKey({ scopes: ['agents'] });
      const r = await h.call('/v1/messages', {
        headers: { 'x-api-key': k.secret },
        body: { model: 'an/claude-x', max_tokens: 5, messages: [{ role: 'user', content: 'x' }] },
      });
      assert.equal(r.status, 403);
      assert.deepEqual(r.json, {
        type: 'error',
        error: { type: 'permission_error', message: 'this key does not have the "llm" scope' },
      });
    },
  },
  {
    name: 'no agents scope: GET /v1/agents is 403',
    run: async () => {
      const k = await h.newKey({ scopes: ['llm'] });
      assert.equal((await h.call('/v1/agents', { method: 'GET', key: k.secret })).status, 403);
    },
  },
  {
    name: 'no mcp scope: /mcp tools/list is 403',
    run: async () => {
      const k = await h.newKey({ scopes: ['llm'] });
      const r = await h.call('/mcp', { key: k.secret, body: { jsonrpc: '2.0', id: 1, method: 'tools/list' } });
      assert.equal(r.status, 403);
    },
  },
  {
    name: 'no rag scope: GET /v1/knowledge is 403',
    run: async () => {
      const k = await h.newKey({ scopes: ['llm'] });
      const r = await h.call('/v1/knowledge', { method: 'GET', key: k.secret });
      assert.equal(r.status, 403);
      assert.equal(r.json.error.message, 'this key does not have the "rag" scope');
    },
  },
  {
    name: '/v1/models lists only what the key may use',
    run: async () => {
      const k = await h.newKey({ scopes: ['llm'], models: ['oa/m*'] });
      const r = await h.call('/v1/models', { method: 'GET', key: k.secret });
      assert.equal(r.status, 200);
      assert.deepEqual(r.json.data.map((m) => m.id).sort(), ['oa/m1', 'oa/m2']);
    },
  },
  {
    name: 'a private-only key may not use an external model (404)',
    run: async () => {
      const k = await h.newKey({ scopes: ['llm'], privateOnly: true });
      const r = await chat(k.secret, 'oa/m1');
      assert.equal(r.status, 404);
      assert.equal(r.json.error.message, 'this key may use private models only');
    },
  },
  {
    name: 'a private-only key may use a private model',
    run: async () => {
      const k = await h.newKey({ scopes: ['llm'], privateOnly: true });
      assert.equal((await chat(k.secret, 'pv/m1')).status, 200);
    },
  },
  {
    name: 'a key may block what the global policy redacts',
    run: async () => {
      await h.setGuardrails(DEFAULTS);
      const k = await h.newKey({ scopes: ['llm'], guardrails: { secrets: 'block' } });
      const body = { model: 'oa/m1', messages: [{ role: 'user', content: `k ${SECRET}` }] };
      const r = await h.call('/v1/chat/completions', { key: k.secret, body });
      assert.equal(r.status, 400);
      assert.equal(r.json.error.code, 'guardrail_blocked');
      assert.equal((await h.call('/v1/chat/completions', { key: h.key, body })).status, 200);
    },
  },
  {
    name: 'a key may turn a check off',
    run: async () => {
      await h.setGuardrails({ ...DEFAULTS, secrets: 'block' });
      try {
        const k = await h.newKey({ scopes: ['llm'], guardrails: { secrets: 'off' } });
        const r = await h.call('/v1/chat/completions', {
          key: k.secret,
          body: { model: 'oa/m1', messages: [{ role: 'user', content: `k ${SECRET}` }] },
        });
        assert.equal(r.status, 200);
        const up = h.upstream.requests.filter((x) => x.path === '/chat/completions').at(-1);
        assert.equal(up.body.messages[0].content, `k ${SECRET}`);
      } finally {
        await h.setGuardrails(DEFAULTS);
      }
    },
  },
  {
    name: 'a key update applies at once',
    run: async () => {
      const k = await h.newKey({ scopes: ['llm'], models: ['pv/*'] });
      assert.equal((await chat(k.secret, 'oa/m1')).status, 403);
      await h.admin('keys', { action: 'update', id: k.id, models: [] });
      assert.equal((await chat(k.secret, 'oa/m1')).status, 200);
    },
  },
  {
    name: 'rate limit in the Anthropic shape',
    run: async () => {
      const k = await h.newKey({ scopes: ['llm'], rpm: 1 });
      const send = () =>
        h.call('/v1/messages', {
          headers: { 'x-api-key': k.secret },
          body: { model: 'an/claude-x', max_tokens: 5, messages: [{ role: 'user', content: 'x' }] },
        });
      assert.equal((await send()).status, 200);
      const r = await send();
      assert.equal(r.status, 429);
      assert.deepEqual(r.json, {
        type: 'error',
        error: { type: 'rate_limit_error', message: 'rate limit of 1 requests per minute reached' },
      });
      assert.ok(Number(r.headers.get('retry-after')) > 0);
    },
  },
  {
    name: 'a key created is a governance audit entry with its id and name',
    run: async () => {
      const k = await h.newKey({ scopes: ['llm'], name: 'audited' });
      const r = await h.call('/api/governance/audit?limit=50', { method: 'GET' });
      assert.equal(r.status, 200);
      const e = r.json.entries.find((x) => x.action === 'key.create' && x.target && x.target.id === k.id);
      assert.ok(e, JSON.stringify(r.json).slice(0, 400));
      assert.equal(e.outcome, 'ok');
      assert.deepEqual(e.target, { type: 'key', id: k.id, name: 'audited' });
    },
  },
];

for (const c of CASES) test(`auth: ${c.name}`, c.run);
