// The capture the Go gateway compares a shadowed translated call with (P4d): the provider's answer as it was read
// (teeResponse), the client's head and bytes, the call's last usage line, and the tape of the random bytes and clock
// reads the translation drew. A request that is not shadowed draws exactly as before.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const os = require('os');
const path = require('path');
const crypto = require('crypto');
const shadow = require('../lib/gateway/shadow');
const { createGateway } = require('../lib/gateway');
const { createSecrets } = require('../lib/secrets');
const { hash } = require('../lib/gateway/keys');

const SECRET = 'zp_captureTestSecret0123456789abcdef';
const ID = '0123456789abcdef0123';
const chunk = (delta, finish = null) => `data: ${JSON.stringify({ choices: [{ index: 0, delta, finish_reason: finish }] })}\n\n`;
const PROVIDER_TEXT = [
  chunk({ role: 'assistant', content: 'Hel' }),
  chunk({ content: 'lo' }),
  chunk({}, 'stop'),
  `data: ${JSON.stringify({ choices: [], usage: { prompt_tokens: 5, completion_tokens: 2 } })}\n\n`,
  'data: [DONE]\n\n',
];

function rig() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'zp-capture-'));
  fs.writeFileSync(
    path.join(dir, 'keys.json'),
    JSON.stringify({ keys: [{ id: 'k1', name: 'k1', hash: hash(SECRET), scopes: ['llm'], models: [], agents: [], rpm: 1000, createdAt: 1 }] }),
  );
  const gw = {
    providers: [{ id: 'oa', preset: 'custom', baseUrl: 'http://127.0.0.1:9/v1', tier: 'external', approved: true }],
    routes: [],
    guardrails: { scope: 'external', secrets: 'off', pii: 'off', injection: 'off' },
  };
  const fetchImpl = async () => {
    let i = 0;
    const body = new ReadableStream({
      pull(ctrl) {
        if (i < PROVIDER_TEXT.length) ctrl.enqueue(new TextEncoder().encode(PROVIDER_TEXT[i++]));
        else ctrl.close();
      },
    });
    return new Response(body, { status: 200, headers: { 'content-type': 'text/event-stream' } });
  };
  const gateway = createGateway({
    dataDir: dir,
    getConfig: () => ({ gateway: gw }),
    writeGatewayConfig: () => {},
    secrets: createSecrets({ env: {}, exec: () => { throw new Error('no keychain'); } }),
    resolveSecret: () => undefined,
    getPricing: () => ({}),
    getIntegrations: () => ({ listTools: async () => [], callTool: async () => ({}) }),
    fetchImpl,
    toolsHome: dir,
  });
  return { gateway, dir };
}

const request = (body, id) => {
  const req = {
    method: 'POST',
    url: '/v1/messages',
    headers: { 'content-type': 'application/json', 'x-api-key': SECRET },
    socket: { remoteAddress: '127.0.0.1', encrypted: false },
    on() {},
    once() {},
    async *[Symbol.asyncIterator]() {
      yield Buffer.from(JSON.stringify(body));
    },
  };
  if (id) req[shadow.SHADOW_ID] = id;
  return req;
};
function response() {
  const res = {
    statusCode: 200,
    headersSent: false,
    writableEnded: false,
    body: '',
    writeHead(status) {
      res.statusCode = status;
      res.headersSent = true;
      return res;
    },
    setHeader() {},
    write(c) {
      res.body += String(c);
      return true;
    },
    end(c) {
      if (c !== undefined && typeof c !== 'function') res.body += String(c);
      res.writableEnded = true;
      return res;
    },
    on() {},
    once() {},
  };
  return res;
}

test('a shadowed translated stream keeps the provider text, the client bytes, the line and the tape', async () => {
  const { gateway, dir } = rig();
  try {
    const res = response();
    const req = request({ model: 'oa/m1', max_tokens: 9, stream: true, messages: [{ role: 'user', content: 'hi' }] }, ID);
    await gateway.handle(req, res, new URL(req.url, 'http://localhost'));
    const c = shadow.takeCapture(ID);
    assert.ok(c, 'a capture');
    assert.equal(c.upstream.length, 1);
    assert.deepEqual(c.upstream[0], {
      status: 200,
      contentType: 'text/event-stream',
      body: PROVIDER_TEXT.join(''),
      ended: 'eof',
      truncated: false,
    });
    assert.equal(c.client.status, 200);
    assert.equal(c.client.headers['content-type'], 'text/event-stream');
    assert.equal(c.client.body, res.body);
    assert.equal(c.client.truncated, false);
    assert.equal(c.usage.format, 'anthropic');
    assert.equal(c.usage.input, 5);
    // One random draw per id in the client's text (msg_…), one clock read for none of them (Anthropic has no times).
    const ids = res.body.match(/msg_[0-9a-f]{24}/g) || [];
    assert.ok(ids.length > 0);
    assert.ok(c.tape.random.length >= 1);
    assert.ok(ids.every((id) => c.tape.random.includes(id.slice(4))));
  } finally {
    gateway.flush();
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('a request that is not shadowed draws exactly as before and keeps nothing', async () => {
  const { gateway, dir } = rig();
  const real = crypto.randomBytes;
  let draws = 0;
  crypto.randomBytes = (n) => {
    draws++;
    return real(n);
  };
  try {
    const res = response();
    const req = request({ model: 'oa/m1', max_tokens: 9, messages: [{ role: 'user', content: 'hi' }] });
    await gateway.handle(req, res, new URL(req.url, 'http://localhost'));
    assert.equal(res.statusCode, 200);
    assert.equal(draws, 1); // the message id
    assert.equal(shadow.takeCapture(ID), null);
  } finally {
    crypto.randomBytes = real;
    gateway.flush();
    fs.rmSync(dir, { recursive: true, force: true });
  }
});
