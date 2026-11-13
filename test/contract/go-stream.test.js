// Contract: streamed chat as the go engine serves it (P4c), with guardrails off so the engine may serve: the bytes, the
// headers, one usage line for a normal end, a client that leaves and a provider that cuts off. The cases hold in every
// mode; under ZP_ENGINE=go each line must come from the gateway's outbox (it carries "ipc").
const test = require('node:test');
const assert = require('node:assert/strict');
const { start } = require('./harness');

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
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
// Waits up to `ms` (the JS gateway writes a left stream's line only once the provider's stream ends).
async function linesAfter(before, want = 1, ms = 5000) {
  const end = Date.now() + ms;
  for (;;) {
    const logs = await h.logs();
    if (logs.length >= before + want) {
      await sleep(300); // a second line would show by now
      const again = await h.logs();
      return again.slice(0, again.length - before);
    }
    if (Date.now() > end) throw new Error(`no usage line within ${ms} ms (${logs.length - before})`);
    await sleep(20);
  }
}
const served = (line) => {
  if (GO) assert.ok(line.ipc, `the go engine served it: ${JSON.stringify(line)}`);
};
async function open(body) {
  const ctl = new AbortController();
  const r = await fetch(`${h.base}/v1/chat/completions`, {
    method: 'POST',
    headers: { 'content-type': 'application/json', authorization: `Bearer ${h.key}` },
    body: JSON.stringify(body),
    signal: ctl.signal,
  });
  return { r, ctl };
}

test('a stream: bytes as the server writes them, the headers of a front-mode stream, one line', async () => {
  const before = (await h.logs()).length;
  const { r } = await open({ model: 'oa/m1', stream: true, messages: user });
  assert.equal(r.status, 200);
  assert.equal(r.headers.get('content-type'), 'text/event-stream');
  assert.equal(r.headers.get('cache-control'), 'no-store');
  assert.equal(r.headers.get('access-control-allow-origin'), '*');
  const text = await r.text();
  assert.ok(text.startsWith(': keep-alive\n\n'));
  assert.ok(text.endsWith('data: [DONE]\n\n'));
  const lines = await linesAfter(before);
  assert.equal(lines.length, 1);
  served(lines[0]);
  assert.deepEqual([lines[0].stream, lines[0].status, lines[0].input, lines[0].output], [true, 200, 10, 2]);
});

test('a client that leaves mid-stream: one line, status 200', async () => {
  const before = (await h.logs()).length;
  const { r, ctl } = await open({ model: 'oa/slowstream', stream: true, messages: user });
  const reader = r.body.getReader();
  await reader.read();
  ctl.abort();
  const lines = await linesAfter(before, 1, 15000);
  assert.equal(lines.length, 1);
  served(lines[0]);
  assert.deepEqual([lines[0].stream, lines[0].status], [true, 200]);
});

test('a provider that cuts off: the lines so far, a clean end, one line', async () => {
  const before = (await h.logs()).length;
  const { r } = await open({ model: 'oa/cutstream', stream: true, messages: user });
  const text = await r.text();
  assert.ok(!text.includes('[DONE]'));
  const lines = await linesAfter(before);
  assert.equal(lines.length, 1);
  served(lines[0]);
  assert.equal(lines[0].status, 200);
});
