// Contract: what the caller sees when the upstream fails or the request is broken. Upstream 4xx/5xx pass through with
// their status, a connection that fails is 502 upstream_error naming only the provider, bad JSON is 400.
const test = require('node:test');
const assert = require('node:assert/strict');
const { start } = require('./harness');

let h;
test.before(async () => {
  h = await start();
});
test.after(async () => {
  if (h) await h.stop();
});

const chat = (model) =>
  h.call('/v1/chat/completions', { key: h.key, body: { model, messages: [{ role: 'user', content: 'x' }] } });

const CASES = [
  {
    // v1 behaviour, see P0 spec amendments: the upstream error body reaches the caller unmasked.
    name: 'upstream 500 on chat: status and body passed through',
    run: async () => {
      const r = await chat('oa/fail500');
      assert.equal(r.status, 500);
      assert.deepEqual(r.json, { error: { message: 'upstream exploded' } });
    },
  },
  {
    name: 'connection reset by the upstream: 502 upstream_error whose message names the provider only',
    run: async () => {
      const r = await chat('oa/reset');
      assert.equal(r.status, 502);
      assert.equal(r.json.error.type, 'upstream_error');
      assert.match(r.json.error.message, /^oa: /);
      assert.doesNotMatch(r.json.error.message, /127\.0\.0\.1|sk-contract/);
    },
  },
  {
    name: 'a body that is not JSON: 400 invalid_request_error',
    run: async () => {
      const r = await h.call('/v1/chat/completions', { key: h.key, raw: '{"model": "oa/m1", ' });
      assert.equal(r.status, 400);
      assert.deepEqual(r.json, { error: { message: 'body is not valid JSON', type: 'invalid_request_error' } });
    },
  },
  {
    // v1 behaviour, see P0 spec amendments: the upstream error body reaches the caller unmasked.
    name: 'upstream 400 passes through with its status and body',
    run: async () => {
      const r = await chat('oa/fail400');
      assert.equal(r.status, 400);
      assert.deepEqual(r.json, { error: { message: 'bad parameter temperature', type: 'invalid_request_error' } });
    },
  },
  {
    // v1 behaviour, see P0 spec amendments: the upstream error body reaches the caller unmasked.
    name: 'upstream 401 passes through',
    run: async () => {
      const r = await chat('oa/fail401');
      assert.equal(r.status, 401);
      assert.deepEqual(r.json, { error: { message: 'invalid upstream key' } });
    },
  },
  {
    // v1 behaviour, see P0 spec amendments: the upstream error body reaches the caller unmasked.
    name: "upstream 429 passes through; the upstream's retry-after is not forwarded",
    run: async () => {
      const r = await chat('oa/fail429');
      assert.equal(r.status, 429);
      assert.deepEqual(r.json, { error: { message: 'slow down' } });
      assert.equal(r.headers.get('retry-after'), null);
    },
  },
  {
    name: 'nothing listens at the provider: 502 upstream_error naming the provider',
    run: async () => {
      const r = await chat('dead/m1');
      assert.equal(r.status, 502);
      assert.equal(r.json.error.type, 'upstream_error');
      assert.match(r.json.error.message, /^dead: /);
      assert.doesNotMatch(r.json.error.message, /127\.0\.0\.1/);
    },
  },
  {
    name: 'upstream 500 on /v1/messages, translated: Anthropic api_error',
    run: async () => {
      const r = await h.call('/v1/messages', {
        headers: { 'x-api-key': h.key },
        body: { model: 'oa/fail500', max_tokens: 5, messages: [{ role: 'user', content: 'x' }] },
      });
      assert.equal(r.status, 500);
      assert.deepEqual(r.json, { type: 'error', error: { type: 'api_error', message: 'upstream exploded' } });
    },
  },
  {
    name: 'upstream 500 on Gemini: UNAVAILABLE',
    run: async () => {
      const r = await h.call('/v1beta/models/oa%2Ffail500:generateContent', {
        headers: { 'x-goog-api-key': h.key },
        body: { contents: [{ role: 'user', parts: [{ text: 'x' }] }] },
      });
      assert.equal(r.status, 500);
      assert.deepEqual(r.json, { error: { code: 500, message: 'upstream exploded', status: 'UNAVAILABLE' } });
    },
  },
  {
    // v1 behaviour, see P0 spec amendments: the upstream error body reaches the caller unmasked.
    name: 'upstream 400 on embeddings passes through',
    run: async () => {
      const r = await h.call('/v1/embeddings', { key: h.key, body: { model: 'oa/fail400', input: 'x' } });
      assert.equal(r.status, 400);
      assert.deepEqual(r.json, { error: { message: 'bad parameter temperature', type: 'invalid_request_error' } });
    },
  },
  {
    name: 'a body that is not JSON on /v1/messages: 400 in the Anthropic shape',
    run: async () => {
      const r = await h.call('/v1/messages', { headers: { 'x-api-key': h.key }, raw: '{"model": ' });
      assert.equal(r.status, 400);
      assert.deepEqual(r.json, {
        type: 'error',
        error: { type: 'invalid_request_error', message: 'body is not valid JSON' },
      });
    },
  },
  {
    name: 'an unknown model is 404; a missing model is 400',
    run: async () => {
      const r = await chat('zz/m1');
      assert.equal(r.status, 404);
      assert.equal(
        r.json.error.message,
        'unknown model "zz/m1": use <provider id>/<model>, an alias or a router/<name> route',
      );
      const n = await h.call('/v1/chat/completions', {
        key: h.key,
        body: { messages: [{ role: 'user', content: 'x' }] },
      });
      assert.equal(n.status, 400);
      assert.equal(n.json.error.message, 'model is required');
    },
  },
  {
    name: 'a 200 whose body is not JSON passes through as it is',
    run: async () => {
      const r = await chat('oa/garbage');
      assert.equal(r.status, 200);
      assert.equal(r.text, 'not json at all');
    },
  },
];

for (const c of CASES) test(`errors: ${c.name}`, c.run);
