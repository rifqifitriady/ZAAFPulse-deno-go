// Contract: the translated classes as the go engine serves them (P4d): /v1/messages (translated and the native
// Anthropic passthrough), chat to an Anthropic-format provider, /v1/responses (translated and native), Gemini, and
// /v1/embeddings, whole and streamed. The cases hold in every mode; under ZP_ENGINE=go each call the engine serves must
// have its usage line from the gateway's outbox (the line carries "ipc"), and each call it hands off must not (a route,
// a model only the model cache can map). ZP_ENGINE=shadow: the harness's stop() fails on any shadow diff.
const test = require('node:test');
const assert = require('node:assert/strict');
const { start, sseEvents } = require('./harness');

const GO = process.env.ZP_ENGINE === 'go';
let h;
test.before(async () => {
  h = await start();
});
test.after(async () => {
  if (h) await h.stop();
});

const user = [{ role: 'user', content: 'hi' }];
// The newest usage line once it is there (a line from the gateway reaches the log a few milliseconds after the answer).
async function lineAfter(before) {
  const end = Date.now() + 5000;
  for (;;) {
    const logs = await h.logs();
    if (logs.length > before) return logs[0];
    if (Date.now() > end) throw new Error('no usage line within 5 s');
    await new Promise((r) => setTimeout(r, 20));
  }
}
const xKey = () => ({ 'x-api-key': h.key });
const gKey = () => ({ 'x-goog-api-key': h.key });

// [name, path, headers, body, check(response), served by go?]
const CASES = [
  ['messages, translated, whole', '/v1/messages', xKey, { model: 'oa/m1', max_tokens: 9, messages: user },
    (r) => assert.equal(r.json.type, 'message'), true],
  ['messages, translated, stream', '/v1/messages', xKey, { model: 'oa/m1', max_tokens: 9, stream: true, messages: user },
    (r) => assert.equal(sseEvents(r.text)[0].event, 'message_start'), true],
  ['messages, native Anthropic, whole', '/v1/messages', xKey, { model: 'an/claude-x', max_tokens: 9, messages: user },
    (r) => assert.equal(r.json.model, 'an/claude-x'), true],
  ['messages, native Anthropic, stream', '/v1/messages', xKey, { model: 'an/claude-x', max_tokens: 9, stream: true, messages: user },
    (r) => assert.equal(sseEvents(r.text)[0].event, 'message_start'), true],
  ['chat to an Anthropic-format provider, whole', '/v1/chat/completions', () => ({ authorization: `Bearer ${h.key}` }),
    { model: 'an/claude-x', messages: user }, (r) => assert.equal(r.json.object, 'chat.completion'), true],
  ['chat to an Anthropic-format provider, stream', '/v1/chat/completions', () => ({ authorization: `Bearer ${h.key}` }),
    { model: 'an/claude-x', stream: true, messages: user }, (r) => assert.match(r.text, /data: \[DONE\]/), true],
  ['responses, translated', '/v1/responses', () => ({ authorization: `Bearer ${h.key}` }), { model: 'oa/m1', input: 'hi' },
    (r) => assert.equal(r.json.object, 'response'), true],
  ['responses, native, stream', '/v1/responses', () => ({ authorization: `Bearer ${h.key}` }),
    { model: 'rs/gpt-5-codex', input: 'hi', stream: true }, (r) => assert.ok(r.text.length > 0), true],
  ['gemini generateContent', `/v1beta/models/${encodeURIComponent('oa/m1')}:generateContent`, gKey,
    { contents: [{ role: 'user', parts: [{ text: 'hi' }] }] }, (r) => assert.ok(Array.isArray(r.json.candidates)), true],
  ['gemini streamGenerateContent', `/v1beta/models/${encodeURIComponent('oa/m1')}:streamGenerateContent?alt=sse`, gKey,
    { contents: [{ role: 'user', parts: [{ text: 'hi' }] }] }, (r) => assert.match(r.text, /^data: /), true],
  ['embeddings', '/v1/embeddings', () => ({ authorization: `Bearer ${h.key}` }), { model: 'oa/m1', input: 'hi' },
    (r) => assert.equal(r.status, 200), true],
  // P4h: the routes switch is on go with the classes (the harness puts every GO_CLASSES entry on go).
  ['messages to a route: served on go', '/v1/messages', xKey,
    { model: 'router/fallback', max_tokens: 9, messages: user }, (r) => assert.equal(r.status, 200), true],
];

for (const [name, p, headers, body, check, byGo] of CASES)
  test(`go formats: ${name}`, async () => {
    const before = (await h.logs()).length;
    const r = await h.call(p, { headers: headers(), body });
    assert.equal(r.status, 200, r.text);
    check(r);
    const line = await lineAfter(before);
    if (GO) assert.equal(!!line.ipc, byGo, `${byGo ? 'the go engine' : 'the server'} served it: ${JSON.stringify(line)}`);
  });

test('go formats: count_tokens is answered without a usage line', async () => {
  const before = (await h.logs()).length;
  const r = await h.call('/v1/messages/count_tokens', { headers: xKey(), body: { model: 'oa/m1', messages: user } });
  assert.equal(r.status, 200);
  assert.ok(Number.isInteger(r.json.input_tokens));
  await new Promise((res) => setTimeout(res, 200));
  assert.equal((await h.logs()).length, before);
});
