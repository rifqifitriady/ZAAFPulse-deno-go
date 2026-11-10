// Contract: the usage record of each call, read back through GET /api/gateway/logs: its fields, the client tag, and
// the cost as integer nano-USD (lib/money.js), never the prompt text.
const test = require('node:test');
const assert = require('node:assert/strict');
const { start } = require('./harness');
const { toNano } = require('../../lib/money');

let h;
test.before(async () => {
  h = await start();
});
test.after(async () => {
  if (h) await h.stop();
});

const last = async () => (await h.logs())[0];

const CASES = [
  {
    name: 'a whole chat call: key, provider, model, kind, format, tokens, status; cost 2,000,000 nano-USD',
    run: async () => {
      const r = await h.call('/v1/chat/completions', {
        key: h.key,
        body: { model: 'oa/m1', messages: [{ role: 'user', content: 'a prompt that is never logged' }] },
      });
      assert.equal(r.status, 200);
      const e = await last();
      assert.deepEqual(
        [e.keyId, e.provider, e.model, e.kind, e.format, e.stream, e.status, e.input, e.output, e.cached],
        [h.keyId, 'oa', 'oa/m1', 'chat', 'openai', false, 200, 1000, 500, 0],
      );
      assert.ok(Number.isInteger(e.ts) && Number.isFinite(e.ms));
      assert.equal(toNano(e.cost), 2_000_000);
      // The logs endpoint strips traceId itself (lib/gateway/index.js), so this only documents the endpoint's shape.
      assert.equal('traceId' in e, false);
      assert.doesNotMatch(JSON.stringify(await h.logs()), /never logged/);
    },
  },
  {
    name: 'a streamed chat call: stream true, time to first token, usage from the final chunk (14,000 nano-USD)',
    run: async () => {
      await h.call('/v1/chat/completions', {
        key: h.key,
        body: { model: 'oa/m1', stream: true, messages: [{ role: 'user', content: 'x' }] },
      });
      const e = await last();
      assert.deepEqual([e.stream, e.input, e.output, e.status], [true, 10, 2, 200]);
      assert.ok(Number.isFinite(e.ttft));
      assert.equal(toNano(e.cost), 14_000);
    },
  },
  {
    name: 'client tag: a Claude Code User-Agent is named claude-code and local, the raw User-Agent is not kept',
    run: async () => {
      await h.call('/v1/chat/completions', {
        key: h.key,
        headers: { 'user-agent': 'claude-cli/2.1.304' },
        body: { model: 'oa/m1', messages: [{ role: 'user', content: 'x' }] },
      });
      const e = await last();
      assert.deepEqual([e.client, e.local], ['claude-code', true]);
      assert.equal('userAgent' in e, false);
      assert.doesNotMatch(JSON.stringify(e), /claude-cli/);
    },
  },
  {
    name: 'an Anthropic native call: format anthropic, provider an, requested name, tokens, cost 60,000 nano-USD',
    run: async () => {
      const r = await h.call('/v1/messages', {
        headers: { 'x-api-key': h.key },
        body: { model: 'an/claude-x', max_tokens: 5, messages: [{ role: 'user', content: 'x' }] },
      });
      assert.equal(r.status, 200);
      const e = await last();
      assert.deepEqual(
        [e.format, e.provider, e.requested, e.input, e.output],
        ['anthropic', 'an', 'an/claude-x', 10, 2],
      );
      assert.equal(toNano(e.cost), 60_000);
    },
  },
  {
    name: 'a Responses call translated to chat: format responses, provider oa, tokens from the chat answer',
    run: async () => {
      const r = await h.call('/v1/responses', { key: h.key, body: { model: 'oa/m1', input: 'x' } });
      assert.equal(r.status, 200);
      const e = await last();
      assert.deepEqual([e.format, e.provider, e.input, e.output], ['responses', 'oa', 1000, 500]);
    },
  },
  {
    name: 'a native Responses call: cached tokens counted, cost 40,000 nano-USD',
    run: async () => {
      const r = await h.call('/v1/responses', { key: h.key, body: { model: 'rs/gpt-5-codex', input: 'x' } });
      assert.equal(r.status, 200);
      const e = await last();
      assert.deepEqual([e.format, e.provider, e.input, e.output, e.cached], ['responses', 'rs', 30, 5, 4]);
      assert.equal(toNano(e.cost), 40_000);
    },
  },
  {
    name: 'a native Responses stream: stream true, usage from response.completed, time to first token',
    run: async () => {
      const r = await h.call('/v1/responses', {
        key: h.key,
        body: { model: 'rs/gpt-5-codex', input: 'x', stream: true },
      });
      assert.equal(r.status, 200);
      const e = await last();
      assert.deepEqual([e.stream, e.input, e.output, e.cached], [true, 40, 6, 8]);
      assert.ok(Number.isFinite(e.ttft));
    },
  },
  {
    name: 'a Gemini call: format gemini, tokens from the chat answer',
    run: async () => {
      const r = await h.call('/v1beta/models/oa%2Fm1:generateContent', {
        headers: { 'x-goog-api-key': h.key },
        body: { contents: [{ role: 'user', parts: [{ text: 'x' }] }] },
      });
      assert.equal(r.status, 200);
      const e = await last();
      assert.deepEqual([e.format, e.input, e.output], ['gemini', 1000, 500]);
    },
  },
  {
    name: 'client tag: a Codex User-Agent is named codex and local',
    run: async () => {
      await h.call('/v1/chat/completions', {
        key: h.key,
        headers: { 'user-agent': 'codex_cli_rs/0.48.0 (Mac OS 26.6; arm64)' },
        body: { model: 'oa/m1', messages: [{ role: 'user', content: 'x' }] },
      });
      const e = await last();
      assert.deepEqual([e.client, e.local], ['codex', true]);
    },
  },
  {
    name: 'client tag: any other client is not named, the loopback caller is still local',
    run: async () => {
      await h.call('/v1/chat/completions', {
        key: h.key,
        headers: { 'user-agent': 'my-own-script/1.0' },
        body: { model: 'oa/m1', messages: [{ role: 'user', content: 'x' }] },
      });
      const e = await last();
      assert.equal('client' in e, false);
      assert.equal(e.local, true);
    },
  },
  {
    name: 'a private provider: the record keeps the list cost (2,000,000 nano-USD)',
    run: async () => {
      await h.call('/v1/chat/completions', {
        key: h.key,
        body: { model: 'pv/m1', messages: [{ role: 'user', content: 'x' }] },
      });
      const e = await last();
      assert.equal(e.provider, 'pv');
      assert.equal(toNano(e.cost), 2_000_000);
    },
  },
  {
    name: 'a route fallback leaves two records: the failed target and the one that answered',
    run: async () => {
      const r = await h.call('/v1/chat/completions', {
        key: h.key,
        body: { model: 'router/fallback', messages: [{ role: 'user', content: 'x' }] },
      });
      assert.equal(r.status, 200);
      const [newest, before] = await h.logs();
      assert.deepEqual([newest.model, newest.routedTo, newest.status], ['router/fallback', 'oa/m1', 200]);
      assert.deepEqual(
        [before.routedTo, before.status, before.error],
        ['oa/fail503', 503, 'fell back to the next route target'],
      );
    },
  },
  {
    name: 'an embeddings record: kind embeddings, input tokens, no output',
    run: async () => {
      const r = await h.call('/v1/embeddings', { key: h.key, body: { model: 'oa/m1', input: 'x' } });
      assert.equal(r.status, 200);
      const e = await last();
      assert.deepEqual([e.kind, e.input, e.output, e.status], ['embeddings', 3, 0, 200]);
    },
  },
  {
    name: 'a failed upstream call is recorded with its status and error, no tokens, no cost',
    run: async () => {
      const r = await h.call('/v1/chat/completions', {
        key: h.key,
        body: { model: 'oa/fail500', messages: [{ role: 'user', content: 'x' }] },
      });
      assert.equal(r.status, 500);
      const e = await last();
      assert.deepEqual([e.status, e.error, e.input], [500, 'upstream exploded', 0]);
      assert.equal(toNano(e.cost), 0);
    },
  },
  {
    name: 'a guardrail block is recorded without its text or tokens',
    run: async () => {
      await h.setGuardrails({ scope: 'external', secrets: 'block', pii: 'off', injection: 'log' });
      try {
        const r = await h.call('/v1/chat/completions', {
          key: h.key,
          body: {
            model: 'oa/m1',
            messages: [{ role: 'user', content: 'k sk-ant-api03-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789abcd' }],
          },
        });
        assert.equal(r.status, 400);
        const e = await last();
        assert.deepEqual([e.status, e.guard.action], [400, 'blocked']);
        assert.equal('input' in e, false);
        assert.doesNotMatch(JSON.stringify(await h.logs()), /sk-ant-/);
      } finally {
        await h.setGuardrails({ scope: 'external', secrets: 'redact', pii: 'off', injection: 'log' });
      }
    },
  },
  {
    name: 'no record carries any prompt text or key this file sent',
    run: async () => {
      const all = await h.logs();
      assert.ok(all.length > 10);
      // The logs endpoint strips traceId itself (lib/gateway/index.js), so there is nothing to assert on it here.
      assert.doesNotMatch(JSON.stringify(all), /never logged|sk-ant-/);
    },
  },
  {
    name: '/api/gateway shows the month totals: requests and guard counters',
    run: async () => {
      const n = (await h.logs()).length;
      const r = await h.call('/api/gateway', { method: 'GET' });
      assert.equal(r.status, 200);
      assert.ok(r.json.usage.totals.requests >= n, `${r.json.usage.totals.requests} < ${n}`);
      const g = r.json.usage.totals.guard;
      for (const k of ['redacted', 'blocked', 'logged']) assert.equal(typeof g[k], 'number', k);
    },
  },
];

for (const c of CASES) test(`usage: ${c.name}`, c.run);
