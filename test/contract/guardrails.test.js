// Contract: the guardrails on what the gateway sends upstream. Secrets, PII and prompt injection in each mode, the
// scope (external vs private targets), and the 400 guardrail_blocked shape in each wire format. The global policy is
// set through the admin API per case and put back to the defaults after the file.
const test = require('node:test');
const assert = require('node:assert/strict');
const { start } = require('./harness');
const { INJECTION } = require('./upstream');

const SECRET = 'sk-ant-api03-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789abcd';
const RED = '‹secret: Anthropic API key›';
const BLOCKED =
  'Blocked by ZAAFPulse guardrails: secret (Anthropic API key). Remove it, or ask the owner to change the policy.';
const DEFAULTS = { scope: 'external', secrets: 'redact', pii: 'off', injection: 'log' };
const EMAIL = 'jane.doe@example.com';
const PHONE = '+1 415 555 0100';
const IP = '203.0.113.9';
const BLOCK_PREFIX = 'Blocked by ZAAFPulse guardrails: ';

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

const sent = (path) => h.upstream.requests.filter((r) => r.path === path);
const chat = (model, content) =>
  h.call('/v1/chat/completions', { key: h.key, body: { model, messages: [{ role: 'user', content }] } });
const lastSent = (path) => sent(path).at(-1);
const newest = async () => (await h.logs())[0];
const xKey = () => ({ 'x-api-key': h.key });
const gKey = () => ({ 'x-goog-api-key': h.key });
const anthropic = (model, content, extra = {}) =>
  h.call('/v1/messages', {
    headers: xKey(),
    body: { model, max_tokens: 5, messages: [{ role: 'user', content }], ...extra },
  });
const gemini = (model, text) =>
  h.call(`/v1beta/models/${encodeURIComponent(model)}:generateContent`, {
    headers: gKey(),
    body: { contents: [{ role: 'user', parts: [{ text }] }] },
  });
const assertBlocked = (r) => {
  assert.equal(r.status, 400, r.text);
  assert.equal(r.json.error.code, 'guardrail_blocked');
};

const CASES = [
  {
    name: 'secrets redact: an external provider gets the placeholder; the usage record says redacted, no text',
    run: async () => {
      await h.setGuardrails({ ...DEFAULTS, secrets: 'redact' });
      const r = await chat('oa/m1', `key ${SECRET} here`);
      assert.equal(r.status, 200);
      assert.equal(sent('/chat/completions').at(-1).body.messages[0].content, `key ${RED} here`);
      const e = (await h.logs())[0];
      assert.deepEqual(e.guard, {
        action: 'redacted',
        hits: [{ check: 'secrets', label: 'Anthropic API key', count: 1 }],
      });
      assert.doesNotMatch(JSON.stringify(e), /sk-ant-/);
    },
  },
  {
    name: 'secrets block: 400 guardrail_blocked (OpenAI shape), nothing sent, recorded as blocked',
    run: async () => {
      await h.setGuardrails({ ...DEFAULTS, secrets: 'block' });
      const before = sent('/chat/completions').length;
      const r = await chat('oa/m1', `key ${SECRET}`);
      assert.equal(r.status, 400);
      assert.deepEqual(r.json, {
        error: { message: BLOCKED, type: 'invalid_request_error', code: 'guardrail_blocked' },
      });
      assert.equal(sent('/chat/completions').length, before, 'nothing reached the provider');
      const e = (await h.logs())[0];
      assert.deepEqual([e.status, e.error, e.guard.action], [400, 'blocked by guardrails', 'blocked']);
    },
  },
  {
    name: 'scope external: a private target is not checked, even in block mode',
    run: async () => {
      await h.setGuardrails({ ...DEFAULTS, scope: 'external', secrets: 'block' });
      const r = await chat('pv/m1', `key ${SECRET}`);
      assert.equal(r.status, 200);
      assert.equal(sent('/chat/completions').at(-1).body.messages[0].content, `key ${SECRET}`);
      assert.equal('guard' in (await h.logs())[0], false);
    },
  },
  {
    name: 'secrets off: the provider gets the secret, the record has no guard',
    run: async () => {
      await h.setGuardrails({ ...DEFAULTS, secrets: 'off' });
      assert.equal((await chat('oa/m1', `key ${SECRET}`)).status, 200);
      assert.equal(lastSent('/chat/completions').body.messages[0].content, `key ${SECRET}`);
      assert.equal('guard' in (await newest()), false);
    },
  },
  {
    name: 'secrets log: the provider gets the secret, the record says logged',
    run: async () => {
      await h.setGuardrails({ ...DEFAULTS, secrets: 'log' });
      assert.equal((await chat('oa/m1', `key ${SECRET}`)).status, 200);
      assert.equal(lastSent('/chat/completions').body.messages[0].content, `key ${SECRET}`);
      assert.deepEqual((await newest()).guard, {
        action: 'logged',
        hits: [{ check: 'secrets', label: 'Anthropic API key', count: 1 }],
      });
    },
  },
  {
    name: 'two secrets are both redacted and counted',
    run: async () => {
      await h.setGuardrails(DEFAULTS);
      assert.equal((await chat('oa/m1', `a ${SECRET} b ${SECRET}`)).status, 200);
      assert.equal(lastSent('/chat/completions').body.messages[0].content, `a ${RED} b ${RED}`);
      assert.deepEqual((await newest()).guard.hits, [{ check: 'secrets', label: 'Anthropic API key', count: 2 }]);
    },
  },
  {
    name: 'pii off: an email reaches the provider unchanged, no guard',
    run: async () => {
      await h.setGuardrails({ ...DEFAULTS, pii: 'off' });
      assert.equal((await chat('oa/m1', `mail ${EMAIL}`)).status, 200);
      assert.equal(lastSent('/chat/completions').body.messages[0].content, `mail ${EMAIL}`);
      assert.equal('guard' in (await newest()), false);
    },
  },
  {
    name: 'pii log: unchanged upstream, recorded as logged',
    run: async () => {
      await h.setGuardrails({ ...DEFAULTS, pii: 'log' });
      assert.equal((await chat('oa/m1', `mail ${EMAIL}`)).status, 200);
      assert.equal(lastSent('/chat/completions').body.messages[0].content, `mail ${EMAIL}`);
      assert.deepEqual((await newest()).guard, {
        action: 'logged',
        hits: [{ check: 'pii', label: 'email', count: 1 }],
      });
    },
  },
  {
    name: 'pii redact: email, phone and IP get their placeholders',
    run: async () => {
      await h.setGuardrails({ ...DEFAULTS, pii: 'redact' });
      assert.equal((await chat('oa/m1', `mail ${EMAIL} or call ${PHONE} from ${IP}`)).status, 200);
      assert.equal(lastSent('/chat/completions').body.messages[0].content, 'mail ‹email› or call ‹phone› from ‹ip›');
      const hits = (await newest()).guard.hits;
      assert.deepEqual(hits.map((x) => [x.check, x.label, x.count]).sort(), [
        ['pii', 'email', 1],
        ['pii', 'ip', 1],
        ['pii', 'phone', 1],
      ]);
    },
  },
  {
    name: 'pii block: 400 naming personal data (email), nothing sent',
    run: async () => {
      await h.setGuardrails({ ...DEFAULTS, pii: 'block' });
      const before = sent('/chat/completions').length;
      const r = await chat('oa/m1', `mail ${EMAIL}`);
      assert.equal(r.status, 400);
      assert.deepEqual(r.json, {
        error: {
          message: `${BLOCK_PREFIX}personal data (email). Remove it, or ask the owner to change the policy.`,
          type: 'invalid_request_error',
          code: 'guardrail_blocked',
        },
      });
      assert.equal(sent('/chat/completions').length, before);
    },
  },
  {
    name: 'injection off: no guard',
    run: async () => {
      await h.setGuardrails({ ...DEFAULTS, injection: 'off' });
      assert.equal((await chat('oa/m1', INJECTION)).status, 200);
      assert.equal('guard' in (await newest()), false);
    },
  },
  {
    name: 'injection log: unchanged upstream, recorded with both labels',
    run: async () => {
      await h.setGuardrails({ ...DEFAULTS, injection: 'log' });
      assert.equal((await chat('oa/m1', INJECTION)).status, 200);
      assert.equal(lastSent('/chat/completions').body.messages[0].content, INJECTION);
      const g = (await newest()).guard;
      assert.equal(g.action, 'logged');
      assert.ok(g.hits.every((x) => x.check === 'injection'));
      assert.deepEqual(g.hits.map((x) => x.label).sort(), ['Exfiltration request', 'Instruction override']);
    },
  },
  {
    name: 'injection block: 400 naming prompt injection',
    run: async () => {
      await h.setGuardrails({ ...DEFAULTS, injection: 'block' });
      const r = await chat('oa/m1', INJECTION);
      assertBlocked(r);
      assert.ok(r.json.error.message.startsWith(`${BLOCK_PREFIX}prompt injection (`), r.json.error.message);
    },
  },
  {
    name: 'injection checks the new input only: an injection before the last assistant turn passes',
    run: async () => {
      await h.setGuardrails({ ...DEFAULTS, injection: 'block' });
      const r = await h.call('/v1/chat/completions', {
        key: h.key,
        body: {
          model: 'oa/m1',
          messages: [
            { role: 'user', content: INJECTION },
            { role: 'assistant', content: 'no' },
            { role: 'user', content: 'ok thanks' },
          ],
        },
      });
      assert.equal(r.status, 200);
    },
  },
  {
    name: 'injection cannot be redacted: the setting is refused and nothing is written',
    run: async () => {
      await h.setGuardrails({ ...DEFAULTS, injection: 'log' });
      const r = await h.call('/api/gateway/settings', { body: { guardrails: { injection: 'redact' } } });
      assert.equal(r.status, 400);
      assert.deepEqual(r.json, { errors: ['guardrails.injection: off, log or block'], badRequest: true });
      const g = await h.call('/api/gateway', { method: 'GET' });
      assert.equal(g.json.settings.guardrails.injection, 'log');
    },
  },
  {
    name: 'two checks block together: the message names both, never the values',
    run: async () => {
      await h.setGuardrails({ ...DEFAULTS, secrets: 'block', pii: 'block' });
      const r = await chat('oa/m1', `key ${SECRET} mail ${EMAIL}`);
      assert.equal(r.status, 400);
      assert.equal(
        r.json.error.message,
        `${BLOCK_PREFIX}secret (Anthropic API key); personal data (email). Remove it, or ask the owner to change the policy.`,
      );
    },
  },
  {
    name: 'scope all: a private target is checked',
    run: async () => {
      await h.setGuardrails({ ...DEFAULTS, scope: 'all', secrets: 'block' });
      assertBlocked(await chat('pv/m1', `key ${SECRET}`));
    },
  },
  {
    name: 'scope external: a local router on automatic tier counts as external',
    run: async () => {
      await h.setGuardrails(DEFAULTS);
      assert.equal((await chat('lr/m1', `key ${SECRET}`)).status, 200);
      assert.equal(lastSent('/chat/completions').body.messages[0].content, `key ${RED}`);
    },
  },
  {
    name: 'scope external: an Ollama cloud model on a private provider counts as external',
    run: async () => {
      await h.setGuardrails(DEFAULTS);
      assert.equal((await chat('pv/gpt-oss:120b-cloud', `key ${SECRET}`)).status, 200);
      assert.equal(lastSent('/chat/completions').body.messages[0].content, `key ${RED}`);
    },
  },
  {
    name: 'block shape: Anthropic native on /v1/messages',
    run: async () => {
      await h.setGuardrails({ ...DEFAULTS, secrets: 'block' });
      const r = await anthropic('an/claude-x', `key ${SECRET}`);
      assert.equal(r.status, 400);
      assert.deepEqual(r.json, {
        type: 'error',
        error: { type: 'invalid_request_error', message: BLOCKED, code: 'guardrail_blocked' },
      });
    },
  },
  {
    name: 'block shape: Anthropic translated on /v1/messages',
    run: async () => {
      await h.setGuardrails({ ...DEFAULTS, secrets: 'block' });
      const r = await anthropic('oa/m1', `key ${SECRET}`);
      assert.equal(r.status, 400);
      assert.equal(r.json.error.code, 'guardrail_blocked');
    },
  },
  {
    name: 'block shape: /v1/responses',
    run: async () => {
      await h.setGuardrails({ ...DEFAULTS, secrets: 'block' });
      const r = await h.call('/v1/responses', { key: h.key, body: { model: 'oa/m1', input: `key ${SECRET}` } });
      assert.equal(r.status, 400);
      assert.deepEqual(r.json, {
        error: { message: BLOCKED, type: 'invalid_request_error', code: 'guardrail_blocked' },
      });
    },
  },
  {
    name: 'block shape: Gemini',
    run: async () => {
      await h.setGuardrails({ ...DEFAULTS, secrets: 'block' });
      const r = await gemini('oa/m1', `key ${SECRET}`);
      assert.equal(r.status, 400);
      assert.deepEqual(r.json, {
        error: {
          code: 400,
          message: BLOCKED,
          status: 'INVALID_ARGUMENT',
          details: [
            { '@type': 'type.googleapis.com/google.rpc.ErrorInfo', reason: 'guardrail_blocked', domain: 'zaafpulse' },
          ],
        },
      });
    },
  },
  {
    name: 'block shape: embeddings',
    run: async () => {
      await h.setGuardrails({ ...DEFAULTS, secrets: 'block' });
      const r = await h.call('/v1/embeddings', { key: h.key, body: { model: 'oa/m1', input: `doc ${SECRET}` } });
      assertBlocked(r);
    },
  },
  {
    name: 'a blocked stream is a JSON 400, not an event stream',
    run: async () => {
      await h.setGuardrails({ ...DEFAULTS, secrets: 'block' });
      const r = await h.call('/v1/chat/completions', {
        key: h.key,
        body: { model: 'oa/m1', stream: true, messages: [{ role: 'user', content: `key ${SECRET}` }] },
      });
      assertBlocked(r);
      assert.equal(r.headers.get('content-type'), 'application/json');
    },
  },
  {
    name: 'count_tokens under block falls back to its estimate and sends nothing',
    run: async () => {
      await h.setGuardrails({ ...DEFAULTS, secrets: 'block' });
      const before = sent('/messages/count_tokens').length;
      const r = await h.call('/v1/messages/count_tokens', {
        headers: xKey(),
        body: { model: 'an/claude-x', messages: [{ role: 'user', content: `key ${SECRET}` }] },
      });
      assert.equal(r.status, 200);
      assert.ok(r.json.input_tokens > 0);
      assert.equal(sent('/messages/count_tokens').length, before);
    },
  },
  {
    name: 'redact: Anthropic native',
    run: async () => {
      await h.setGuardrails(DEFAULTS);
      assert.equal((await anthropic('an/claude-x', `key ${SECRET}`)).status, 200);
      assert.equal(lastSent('/messages').body.messages[0].content, `key ${RED}`);
      assert.equal((await newest()).guard.action, 'redacted');
    },
  },
  {
    name: 'redact: Anthropic translated',
    run: async () => {
      await h.setGuardrails(DEFAULTS);
      assert.equal((await anthropic('oa/m1', `key ${SECRET}`)).status, 200);
      assert.match(JSON.stringify(lastSent('/chat/completions').body.messages), new RegExp(RED));
    },
  },
  {
    name: 'redact: native Responses',
    run: async () => {
      await h.setGuardrails(DEFAULTS);
      const r = await h.call('/v1/responses', {
        key: h.key,
        body: { model: 'rs/gpt-5-codex', input: `key ${SECRET}` },
      });
      assert.equal(r.status, 200);
      assert.equal(lastSent('/responses').body.input, `key ${RED}`);
    },
  },
  {
    name: 'redact: Gemini',
    run: async () => {
      await h.setGuardrails(DEFAULTS);
      assert.equal((await gemini('oa/m1', `key ${SECRET}`)).status, 200);
      assert.match(JSON.stringify(lastSent('/chat/completions').body.messages), new RegExp(RED));
    },
  },
  {
    name: 'redact: embeddings',
    run: async () => {
      await h.setGuardrails(DEFAULTS);
      const r = await h.call('/v1/embeddings', { key: h.key, body: { model: 'oa/m1', input: [`doc ${SECRET}`] } });
      assert.equal(r.status, 200);
      assert.deepEqual(lastSent('/embeddings').body.input, [`doc ${RED}`]);
    },
  },
  {
    name: 'redaction is deterministic and no request header turns it off',
    run: async () => {
      await h.setGuardrails(DEFAULTS);
      const body = { model: 'oa/m1', messages: [{ role: 'user', content: `key ${SECRET} and ${SECRET}` }] };
      assert.equal((await h.call('/v1/chat/completions', { key: h.key, body })).status, 200);
      const first = JSON.stringify(lastSent('/chat/completions').body);
      const r = await h.call('/v1/chat/completions', { key: h.key, headers: { 'x-zp-guardrails': 'off' }, body });
      assert.equal(r.status, 200);
      const second = JSON.stringify(lastSent('/chat/completions').body);
      assert.equal(second, first);
      assert.doesNotMatch(second, /sk-ant-/);
      assert.match(second, new RegExp(RED));
    },
  },
];

for (const c of CASES) test(`guardrails: ${c.name}`, c.run);
