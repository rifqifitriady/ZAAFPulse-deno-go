// Contract: budgets the gateway enforces before a call goes upstream. Under, at and over the limit; a key with a budget
// on a model whose spend cannot be counted (fail closed); the 402 refusal in each wire format; saved budgets.
const test = require('node:test');
const assert = require('node:assert/strict');
const { start } = require('./harness');

let h;
const saved = [];
// The key of the saved-budget case, for the case after it that deletes the budget.
let savedKey = null;
test.before(async () => {
  h = await start();
});
test.after(async () => {
  if (h) {
    for (const id of saved) await h.call(`/api/cost/budgets/${id}/delete`, { body: {} });
    await h.stop();
  }
});

const chat = (key, model = 'oa/m1') =>
  h.call('/v1/chat/completions', { key, body: { model, messages: [{ role: 'user', content: 'x' }] } });

const CASES = [
  {
    name: 'key budget: under the limit answers; at the limit (US$0.002 spent of 0.002) is 402 insufficient_quota',
    run: async () => {
      const k = await h.newKey({ scopes: ['llm'], budgetUSD: 0.002, name: 'at-limit' });
      assert.equal((await chat(k.secret)).status, 200);
      const r = await chat(k.secret);
      assert.equal(r.status, 402);
      assert.deepEqual(r.json, {
        error: { message: 'budget "Key at-limit" is used up for this month', type: 'insufficient_quota' },
      });
    },
  },
  {
    name: 'the 402 refusal in the Anthropic shape on /v1/messages',
    run: async () => {
      const k = await h.newKey({ scopes: ['llm'], budgetUSD: 0.001, name: 'over-anthropic' });
      assert.equal((await chat(k.secret)).status, 200);
      const r = await h.call('/v1/messages', {
        headers: { 'x-api-key': k.secret },
        body: { model: 'oa/m1', max_tokens: 5, messages: [{ role: 'user', content: 'x' }] },
      });
      assert.equal(r.status, 402);
      assert.deepEqual(r.json, {
        type: 'error',
        error: { type: 'invalid_request_error', message: 'budget "Key over-anthropic" is used up for this month' },
      });
    },
  },
  {
    name: 'fail closed: a key with a budget may not use an external model without a price (402), a private one is free',
    run: async () => {
      const k = await h.newKey({ scopes: ['llm'], budgetUSD: 5 });
      const r = await chat(k.secret, 'oa/unpriced');
      assert.equal(r.status, 402);
      assert.equal(r.json.error.type, 'insufficient_quota');
      assert.match(r.json.error.message, /^this key has a budget, and oa\/unpriced has no known price/);
      assert.equal((await chat(k.secret, 'pv/unpriced')).status, 200);
    },
  },
  {
    // v1 behaviour, see P0 spec amendments: budgets fail open; the call that crosses the amount is let through.
    name: 'over the limit: a call may pass the amount, the next one is refused',
    run: async () => {
      const k = await h.newKey({ scopes: ['llm'], budgetUSD: 0.001, name: 'over' });
      assert.equal((await chat(k.secret)).status, 200);
      assert.equal((await chat(k.secret)).status, 402);
    },
  },
  {
    name: 'the 402 refusal in the Gemini shape',
    run: async () => {
      const k = await h.newKey({ scopes: ['llm'], budgetUSD: 0.001, name: 'over-gemini' });
      assert.equal((await chat(k.secret)).status, 200);
      const r = await h.call('/v1beta/models/oa%2Fm1:generateContent', {
        headers: { 'x-goog-api-key': k.secret },
        body: { contents: [{ role: 'user', parts: [{ text: 'x' }] }] },
      });
      assert.equal(r.status, 402);
      assert.deepEqual(r.json, {
        error: { code: 402, message: 'budget "Key over-gemini" is used up for this month', status: 'INVALID_ARGUMENT' },
      });
    },
  },
  {
    name: 'the 402 refusal on /v1/responses',
    run: async () => {
      const k = await h.newKey({ scopes: ['llm'], budgetUSD: 0.001, name: 'over-responses' });
      assert.equal((await chat(k.secret)).status, 200);
      const r = await h.call('/v1/responses', { key: k.secret, body: { model: 'oa/m1', input: 'x' } });
      assert.equal(r.status, 402);
      assert.deepEqual(r.json, {
        error: { message: 'budget "Key over-responses" is used up for this month', type: 'insufficient_quota' },
      });
    },
  },
  {
    name: 'under the limit, several calls all answer',
    run: async () => {
      const k = await h.newKey({ scopes: ['llm'], budgetUSD: 1 });
      for (let i = 0; i < 3; i++) assert.equal((await chat(k.secret)).status, 200);
    },
  },
  {
    name: 'a blocking key budget saved in Budgets: a call answers, the next one is refused by name',
    run: async () => {
      const k = await h.newKey({ scopes: ['llm'] });
      const b = await h.call('/api/cost/budgets', {
        body: {
          id: 'ct-key',
          name: 'Contract key',
          scope: { dim: 'key', value: k.id },
          period: 'month',
          amount: 0.002,
          enforce: 'block',
        },
      });
      assert.equal(b.status, 200, b.text);
      saved.push('ct-key');
      assert.equal((await chat(k.secret)).status, 200);
      const r = await chat(k.secret);
      assert.equal(r.status, 402);
      assert.equal(r.json.error.message, 'budget "Contract key" is used up for this month');
      savedKey = k;
    },
  },
  {
    name: 'deleting that budget lifts the refusal',
    run: async () => {
      assert.ok(savedKey, 'needs the previous case');
      const d = await h.call('/api/cost/budgets/ct-key/delete', { body: {} });
      assert.equal(d.status, 200);
      assert.deepEqual(d.json, { deleted: 'ct-key' });
      saved.splice(saved.indexOf('ct-key'), 1);
      assert.equal((await chat(savedKey.secret)).status, 200);
    },
  },
  {
    name: 'a budget that does not block never refuses',
    run: async () => {
      const k = await h.newKey({ scopes: ['llm'] });
      const b = await h.call('/api/cost/budgets', {
        body: {
          id: 'ct-none',
          name: 'Contract notify',
          scope: { dim: 'key', value: k.id },
          period: 'month',
          amount: 0.001,
          enforce: 'none',
        },
      });
      assert.equal(b.status, 200, b.text);
      saved.push('ct-none');
      assert.equal((await chat(k.secret)).status, 200);
      assert.equal((await chat(k.secret)).status, 200);
    },
  },
  {
    name: 'a refused call leaves no usage record',
    run: async () => {
      const k = await h.newKey({ scopes: ['llm'], budgetUSD: 0.001, name: 'no-record' });
      assert.equal((await chat(k.secret)).status, 200);
      const before = (await h.logs()).length;
      assert.equal((await chat(k.secret)).status, 402);
      assert.equal((await h.logs()).length, before);
    },
  },
  {
    name: 'a saved blocking key budget also requires priced models',
    run: async () => {
      const k = await h.newKey({ scopes: ['llm'] });
      const b = await h.call('/api/cost/budgets', {
        body: {
          id: 'ct-priced',
          name: 'Contract priced',
          scope: { dim: 'key', value: k.id },
          period: 'month',
          amount: 5,
          enforce: 'block',
        },
      });
      assert.equal(b.status, 200, b.text);
      saved.push('ct-priced');
      const r = await chat(k.secret, 'oa/unpriced');
      assert.equal(r.status, 402);
      assert.equal(
        r.json.error.message,
        'this key has a budget, and oa/unpriced has no known price; add one under Settings → Model prices',
      );
    },
  },
];

for (const c of CASES) test(`budgets: ${c.name}`, c.run);
