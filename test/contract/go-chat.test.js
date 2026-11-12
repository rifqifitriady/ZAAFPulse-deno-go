// Contract: non-streaming chat as the go engine serves it (P4b), with guardrails off so the engine may serve: answers,
// errors, the usage line, the rate limit, and a revoked key refused as soon as the revoke has answered. The cases hold
// in every mode; under ZP_ENGINE=go each call the engine serves must have its usage line from the gateway's outbox
// (the line carries "ipc"), so the run proves the engine answered and not the server.
const test = require('node:test');
const assert = require('node:assert/strict');
const { start } = require('./harness');
const { toNano } = require('../../lib/money');

const GO = process.env.ZP_ENGINE === 'go';
let h;
test.before(async () => {
  h = await start();
  await h.setGuardrails({ scope: 'external', secrets: 'off', pii: 'off', injection: 'off' });
});
test.after(async () => {
  if (h) await h.stop();
});

const user = [{ role: 'user', content: 'hi' }];
const chat = (body, opts = {}) => h.call('/v1/chat/completions', { key: h.key, body, ...opts });
// The newest usage line once it is there: a line from the gateway reaches the log through the IPC ingest, a few
// milliseconds after the answer.
async function lineAfter(before) {
  const end = Date.now() + 5000;
  for (;;) {
    const logs = await h.logs();
    if (logs.length > before) return { line: logs[0], count: logs.length - before };
    if (Date.now() > end) throw new Error('no usage line within 5 s');
    await new Promise((r) => setTimeout(r, 20));
  }
}
const served = (line) => {
  if (GO) assert.ok(line.ipc, `the go engine served it: ${JSON.stringify(line)}`);
};

const CASES = [
  {
    name: 'a call to an external provider: the answer with the requested model, one usage line, US$0.002',
    run: async () => {
      const before = (await h.logs()).length;
      const r = await chat({ model: 'oa/m1', messages: user, max_tokens: 5 });
      assert.equal(r.status, 200);
      assert.equal(r.json.model, 'oa/m1');
      assert.equal(r.json.choices[0].message.content, 'hi');
      assert.equal(r.headers.get('access-control-allow-origin'), '*');
      const up = h.upstream.requests.filter((x) => x.path === '/chat/completions').at(-1);
      assert.equal(up.body.model, 'm1');
      assert.equal(up.body.max_tokens, 5);
      assert.equal(up.headers.authorization, 'Bearer sk-contract-oa');
      const { line, count } = await lineAfter(before);
      assert.equal(count, 1);
      served(line);
      assert.deepEqual(
        [
          line.keyId,
          line.provider,
          line.model,
          line.kind,
          line.format,
          line.stream,
          line.status,
          line.input,
          line.output,
        ],
        [h.keyId, 'oa', 'oa/m1', 'chat', 'openai', false, 200, 1000, 500],
      );
      assert.equal(line.local, true);
      assert.equal(toNano(line.cost), 2_000_000);
    },
  },
  {
    name: 'a private provider and x-api-key',
    run: async () => {
      const before = (await h.logs()).length;
      const r = await chat({ model: 'pv/m1', messages: user }, { key: undefined, headers: { 'x-api-key': h.key } });
      assert.equal(r.status, 200);
      served((await lineAfter(before)).line);
    },
  },
  {
    name: 'upstream 400, 500 and 429 pass through with their body; the retry-after stays upstream',
    run: async () => {
      for (const [model, status, body] of [
        ['oa/fail400', 400, { error: { message: 'bad parameter temperature', type: 'invalid_request_error' } }],
        ['oa/fail500', 500, { error: { message: 'upstream exploded' } }],
        ['oa/fail429', 429, { error: { message: 'slow down' } }],
      ]) {
        const before = (await h.logs()).length;
        const r = await chat({ model, messages: user });
        assert.equal(r.status, status, model);
        assert.deepEqual(r.json, body);
        assert.equal(r.headers.get('retry-after'), null);
        const { line } = await lineAfter(before);
        served(line);
        assert.equal(line.status, status);
        assert.equal(line.error, body.error.message);
      }
    },
  },
  {
    name: 'a 200 that is not JSON passes through; a reset and a dead provider are 502 naming the provider only',
    run: async () => {
      const g = await chat({ model: 'oa/garbage', messages: user });
      assert.deepEqual([g.status, g.text], [200, 'not json at all']);
      for (const [model, id] of [
        ['oa/reset', 'oa'],
        ['dead/m1', 'dead'],
      ]) {
        const before = (await h.logs()).length;
        const r = await chat({ model, messages: user });
        assert.equal(r.status, 502, model);
        assert.equal(r.json.error.type, 'upstream_error');
        assert.match(r.json.error.message, new RegExp(`^${id}: `));
        assert.doesNotMatch(r.json.error.message, /127\.0\.0\.1|sk-contract/);
        const { line } = await lineAfter(before);
        served(line);
        assert.equal(line.status, 599);
      }
    },
  },
  {
    name: 'the rate limit of a key answers 429 rate_limit_error with retry-after',
    run: async () => {
      const k = await h.newKey({ scopes: ['llm'], rpm: 2 });
      for (let i = 0; i < 2; i++)
        assert.equal((await chat({ model: 'pv/m1', messages: user }, { key: k.secret })).status, 200);
      const r = await chat({ model: 'pv/m1', messages: user }, { key: k.secret });
      assert.equal(r.status, 429);
      assert.deepEqual(r.json, {
        error: { message: 'rate limit of 2 requests per minute reached', type: 'rate_limit_error' },
      });
      assert.ok(Number(r.headers.get('retry-after')) > 0);
    },
  },
  {
    name: 'a key revoked is refused as soon as the revoke has answered (no window on the gateway)',
    run: async () => {
      const k = await h.newKey({ scopes: ['llm'] });
      assert.equal((await chat({ model: 'pv/m1', messages: user }, { key: k.secret })).status, 200);
      await h.admin('keys', { action: 'revoke', id: k.id });
      const r = await chat({ model: 'pv/m1', messages: user }, { key: k.secret });
      assert.equal(r.status, 401);
      assert.deepEqual(r.json, { error: { message: 'this API key was revoked', type: 'authentication_error' } });
    },
  },
  {
    name: 'a model outside the key allow-list is 403, a streamed call and an agent model still answer',
    run: async () => {
      const k = await h.newKey({ scopes: ['llm'], models: ['oa/*'] });
      const no = await chat({ model: 'pv/m1', messages: user }, { key: k.secret });
      assert.equal(no.status, 403);
      assert.equal(no.json.error.message, 'this key may not use model "pv/m1"');
      const s = await chat({ model: 'oa/m1', messages: user, stream: true });
      assert.equal(s.status, 200);
      assert.match(s.text, /data: \[DONE\]/);
    },
  },
];

for (const c of CASES) test(`go chat: ${c.name}`, c.run);
