// Contract: one requests-per-minute window per key across the engines (P4f D16). A key with rpm 3 makes a chat call (the
// go engine under ZP_ENGINE=go), an embeddings call (always the server), and a chat call; a fourth request to either is
// refused with 429, in every mode. Before P4f each engine kept its own window, and go mode let the fourth through.
const test = require('node:test');
const assert = require('node:assert/strict');
const { start } = require('./harness');

let h;
test.before(async () => {
  h = await start();
  await h.setGuardrails({ scope: 'external', secrets: 'off', pii: 'off', injection: 'off' });
});
test.after(async () => {
  if (h) await h.stop();
});

const hop = () => new Promise((r) => setTimeout(r, 100)); // one IPC hop for the take to reach the other engine

test('a key with rpm 3 is refused on its fourth request, whichever engine answered the first three', async () => {
  const k = await h.newKey({ scopes: ['llm'], rpm: 3 });
  const chat = () =>
    h.call('/v1/chat/completions', {
      key: k.secret,
      body: { model: 'oa/m1', messages: [{ role: 'user', content: 'hi' }] },
    });
  const embed = () => h.call('/v1/embeddings', { key: k.secret, body: { model: 'oa/m1', input: 'hi' } });
  assert.equal((await chat()).status, 200);
  await hop();
  assert.equal((await embed()).status, 200);
  await hop();
  assert.equal((await chat()).status, 200);
  await hop();
  const fourth = await chat();
  assert.equal(fourth.status, 429);
  assert.equal(fourth.json.error.type, 'rate_limit_error');
  assert.equal((await embed()).status, 429);
});
