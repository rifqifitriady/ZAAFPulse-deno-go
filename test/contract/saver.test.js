// Contract: the token saver (lib/gateway/rtk.js) on chat and on /v1/messages. A long tool result reaches the
// provider shortened, the usage line carries savedTokens, and the header turns it off. Under ZP_ENGINE=go the go
// engine serves these calls (the line carries "ipc"); under ZP_ENGINE=shadow every compare is the same.
const test = require('node:test');
const assert = require('node:assert/strict');
const { start } = require('./harness');

const GO = process.env.ZP_ENGINE === 'go';
let h;
test.before(async () => {
  h = await start();
});
test.after(async () => {
  if (h) await h.stop();
});

const DIFF = Array.from({ length: 3 }, (_, f) =>
  [`diff --git a/f${f}.js b/f${f}.js`, '@@ -1,90 +1,90 @@', ...Array.from({ length: 90 }, (_, i) => `+ line ${i}`)].join('\n'),
).join('\n');
const chatBody = (extra = {}) => ({
  model: 'oa/m1',
  messages: [
    { role: 'user', content: 'go' },
    { role: 'assistant', content: null, tool_calls: [{ id: 'c1', type: 'function', function: { name: 'Bash', arguments: '{"command":"git diff"}' } }] },
    { role: 'tool', tool_call_id: 'c1', content: DIFF },
  ],
  ...extra,
});
async function lineAfter(before) {
  const end = Date.now() + 5000;
  for (;;) {
    const logs = await h.logs();
    if (logs.length > before) return logs[0];
    if (Date.now() > end) throw new Error('no usage line within 5 s');
    await new Promise((r) => setTimeout(r, 20));
  }
}
const lastUp = (p) => h.upstream.requests.filter((x) => x.path === p).at(-1);

test('chat: the tool result reaches the provider shortened, savedTokens on the line', async () => {
  const before = (await h.logs()).length;
  const r = await h.call('/v1/chat/completions', { key: h.key, body: chatBody() });
  assert.equal(r.status, 200);
  const sent = lastUp('/chat/completions').body.messages[2].content;
  assert.ok(sent.length < DIFF.length && sent.includes('… rest of hunk omitted'));
  const line = await lineAfter(before);
  assert.equal(line.savedTokens, Math.round((DIFF.length - sent.length) / 4));
  if (GO) assert.ok(line.ipc, 'served by the go engine');
});

test('chat stream: shortened as well', async () => {
  const before = (await h.logs()).length;
  const r = await h.call('/v1/chat/completions', { key: h.key, body: chatBody({ stream: true }) });
  assert.equal(r.status, 200);
  assert.ok(lastUp('/chat/completions').body.messages[2].content.length < DIFF.length);
  const line = await lineAfter(before);
  assert.ok(line.savedTokens > 0);
  if (GO) assert.ok(line.ipc);
});

test('x-zp-token-saver: off sends the text unchanged and writes no savedTokens', async () => {
  const before = (await h.logs()).length;
  await h.call('/v1/chat/completions', { key: h.key, body: chatBody(), headers: { 'x-zp-token-saver': 'off' } });
  assert.equal(lastUp('/chat/completions').body.messages[2].content, DIFF);
  assert.equal((await lineAfter(before)).savedTokens, undefined);
});

// JS throws a TypeError in saveTokens and answers it as upstream_error 502 with V8's text; the go engine hands the call
// to the server, so the answer is the same in every mode.
test('a message the saver cannot read: the server answers it (502 upstream_error), the provider is not called', async () => {
  const n = h.upstream.requests.length;
  const r = await h.call('/v1/chat/completions', { key: h.key, body: { model: 'oa/m1', messages: [null] } });
  assert.equal(r.status, 502);
  assert.equal(r.json.error.type, 'upstream_error');
  assert.match(r.json.error.message, /Cannot read properties of null/);
  assert.equal(h.upstream.requests.length, n);
});

test('/v1/messages with a tool_result: shortened on the translated body', async () => {
  const before = (await h.logs()).length;
  const r = await h.call('/v1/messages', {
    key: h.key,
    body: {
      model: 'oa/m1',
      max_tokens: 10,
      messages: [
        { role: 'assistant', content: [{ type: 'tool_use', id: 't1', name: 'Bash', input: { command: 'git diff' } }] },
        { role: 'user', content: [{ type: 'tool_result', tool_use_id: 't1', content: DIFF }] },
      ],
    },
  });
  assert.equal(r.status, 200);
  const tool = lastUp('/chat/completions').body.messages.find((m) => m.role === 'tool');
  assert.ok(tool.content.length < DIFF.length);
  assert.ok((await lineAfter(before)).savedTokens > 0);
});
