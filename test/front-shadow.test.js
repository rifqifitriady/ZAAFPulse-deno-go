// The shadow compare's JS side (P4b): the capture a marked request leaves (lib/gateway/shadow.js), the internal port's
// endpoints for the gateway's own requests, and the shadow log with the rule that switches a class to go
// (lib/front/shadow-log.js).
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const http = require('http');
const {
  withShadow,
  noteProviderRequest,
  noteAnswer,
  noteClient,
  noteUsage,
  takeCapture,
} = require('../lib/gateway/shadow');
const { createShadowLog, shadowEndpoints, DAY } = require('../lib/front/shadow-log');
const { createInternalListener } = require('../lib/front/internal');
const { signClient } = require('../lib/front/client');
const { routing, shadowOf } = require('../lib/front');
const { tmpDir } = require('./ipc-stub');

test('a capture exists only for a marked request, and is handed out once', async () => {
  noteAnswer({ status: 200, body: 'x' }, { status: 200, body: 'x' }, {});
  await withShadow('00112233445566aa', async () => {
    noteProviderRequest('http://p/v1/chat/completions', { authorization: 'Bearer k' }, '{"model":"m"}');
    await new Promise((r) => setTimeout(r, 1));
    noteAnswer({ status: 200, body: '{}' }, { status: 200, body: '{"model":"x"}' }, { keyId: 'k1' });
  });
  const c = takeCapture('00112233445566aa');
  assert.equal(c.providerRequests.length, 1);
  assert.equal(c.providerRequests[0].body, '{"model":"m"}');
  assert.deepEqual(c.usage, { keyId: 'k1' });
  assert.equal(takeCapture('00112233445566aa'), null, 'once');
  withShadow('not-an-id', () => noteAnswer({ status: 1, body: '' }, { status: 1, body: '' }, {}));
  assert.equal(takeCapture('not-an-id'), null);
  // A marked request refused before any provider call: its status, no provider answer.
  const res = { statusCode: 401 };
  await withShadow('00112233445566bb', async () => {}, res);
  assert.deepEqual(takeCapture('00112233445566bb'), {
    providerRequests: [],
    providerResponse: null,
    upstream: [],
    client: { status: 401, headers: {}, body: null, truncated: false },
    usage: null,
    tape: { random: [], now: [] },
  });
});

test('a refusal leaves its error body and its usage line in the capture (P4e)', async () => {
  const res = { statusCode: 400 };
  const body = '{"error":{"message":"Blocked","type":"invalid_request_error","code":"guardrail_blocked"}}';
  const line = { status: 400, error: 'blocked by guardrails', guard: { action: 'blocked', hits: [] } };
  await withShadow(
    '00112233445566cc',
    async () => {
      noteUsage(line);
      noteClient(body);
    },
    res,
  );
  assert.deepEqual(takeCapture('00112233445566cc'), {
    providerRequests: [],
    providerResponse: null,
    upstream: [],
    client: { status: 400, headers: {}, body, truncated: false },
    usage: line,
    tape: { random: [], now: [] },
  });
  // Outside a marked request both are no-ops.
  noteClient('x');
  noteUsage({});
});

test('the shadow log: counts per class, and the switch rule (owner: 1 clean day and 20 compares; a diff restarts it)', (t) => {
  const dir = tmpDir(t, 'shadow-log');
  let now = 1_000_000_000_000;
  const log = createShadowLog({ file: path.join(dir, 's.jsonl'), now: () => now });
  assert.match(log.gate('chat').reason, /0 of 20 chat calls compared/);
  assert.match(log.gate('chat', { days: 1, min: 0 }).reason, /compared no chat call/);
  assert.equal(log.append({ class: 'chat', outcome: 'nonsense' }), false);
  assert.equal(log.append({ class: 'chat', outcome: 'handoff', reason: 'stream' }), true);
  assert.equal(log.append({ class: 'chat', outcome: 'skipped', reason: 'the provider call failed' }), true);
  for (let i = 0; i < 19; i++) log.append({ class: 'chat', outcome: 'same' });
  now += 2 * DAY;
  assert.match(log.gate('chat').reason, /19 of 20 chat calls compared/, 'hand-offs and skips do not count');
  log.append({ class: 'chat', outcome: 'same' });
  assert.deepEqual(log.gate('chat'), { ok: true }, 'the defaults: 1 day, 20 compares');
  assert.equal(
    log.append({ class: 'chat', outcome: 'diff', diffs: [{ field: 'client.status', js: 200, go: 500 }] }),
    true,
  );
  assert.match(log.gate('chat').reason, /last chat compare differed/);
  for (let i = 0; i < 20; i++) log.append({ class: 'chat', outcome: 'same' });
  assert.match(log.gate('chat').reason, /clean for 0\.00 of 1 days/, 'the wait starts again at the diff');
  now += DAY;
  assert.deepEqual(log.gate('chat'), { ok: true });
  assert.match(log.gate('chat', { days: 3 }).reason, /clean for 1\.00 of 3 days/);
  assert.match(log.gate('chat', { min: 25 }).reason, /20 of 25 chat calls compared/);
  assert.deepEqual(log.gate('chat', { days: 0, min: 0 }), { ok: true });
  assert.deepEqual(log.gate('messages', { days: 0, min: 0 }), { ok: true }, 'both 0 lift the rule');
  assert.match(log.gate('messages', { days: 0, min: 1 }).reason, /0 of 1 messages calls compared/);
  const st = log.stats().chat;
  assert.deepEqual([st.compared, st.diffs, st.handoffs], [41, 1, 2]);
  // Read back after a restart: the same counts and the same rule.
  const again = createShadowLog({ file: path.join(dir, 's.jsonl'), now: () => now });
  assert.deepEqual(again.stats(), log.stats());
  assert.deepEqual(again.gate('chat'), { ok: true });
  assert.ok(!fs.readFileSync(path.join(dir, 's.jsonl'), 'utf8').includes('undefined'));
});

test('the shadow log: a compare in which a token saver shortened something also counts for token-saver (rtk spec D13)', (t) => {
  const dir = tmpDir(t, 'shadow-log-saver');
  const now = 1_000_000_000_000;
  const log = createShadowLog({ file: path.join(dir, 's.jsonl'), now: () => now });
  log.append({ class: 'chat', outcome: 'same' });
  assert.equal(log.stats()['token-saver'], undefined, 'a line without saver leaves token-saver alone');
  log.append({ class: 'chat', outcome: 'same', saver: true });
  log.append({ class: 'messages', outcome: 'same', saver: true });
  log.append({ class: 'chat', outcome: 'handoff', reason: 'token saver: over its step budget', saver: true });
  let st = log.stats();
  assert.deepEqual([st.chat.compared, st.messages.compared, st['token-saver'].compared], [2, 1, 2]);
  assert.equal(st['token-saver'].handoffs, 1, 'a saver hand-off is shown for the switch, not counted as a compare');
  log.append({ class: 'messages', outcome: 'handoff', reason: 'token saver: the engine\'s saver panicked' });
  log.append({ class: 'chat', outcome: 'handoff', reason: 'model: a route' });
  assert.equal(log.stats()['token-saver'].handoffs, 2, 'a token saver hand-off without the saver mark counts too');
  log.append({ class: 'chat', outcome: 'diff', saver: true, diffs: [{ field: 'providerRequest.body', js: 'a', go: 'b' }] });
  st = log.stats();
  assert.equal(st['token-saver'].diffs, 1);
  assert.match(log.gate('token-saver').reason, /last token-saver compare differed/);
  const again = createShadowLog({ file: path.join(dir, 's.jsonl'), now: () => now });
  assert.deepEqual(again.stats(), log.stats(), 'read back after a restart');
});

test('routing: token-saver is a go class with its own gate and note (rtk spec D13)', () => {
  const gw = { engines: { chat: 'go', 'token-saver': 'go' }, shadow: ['token-saver'] };
  const r = routing(gw, { gate: (c) => (c === 'token-saver' ? { ok: false, reason: 'r' } : { ok: true }) });
  assert.deepEqual([r.engines.chat, r.engines['token-saver'], r.notes['token-saver']], ['go', 'js', 'r']);
  assert.deepEqual(shadowOf(gw, r.engines), ['token-saver']);
  assert.equal(routing(gw).engines['token-saver'], 'go');
});

test('routing: go only when asked and the gate met, and not while OTLP export captures content; shadow only for a class on js', () => {
  const gw = { engines: { chat: 'go' }, shadow: ['chat', 'messages'] };
  assert.equal(routing(gw, { gate: () => ({ ok: true }) }).engines.chat, 'go');
  const no = routing(gw, { gate: () => ({ ok: false, reason: 'not yet' }) });
  assert.deepEqual([no.engines.chat, no.notes.chat], ['js', 'not yet']);
  // P4f D10: the go engine sends its spans, so OTLP export alone keeps nothing on js; capturing content still does.
  assert.equal(routing(gw, { otel: true }).engines.chat, 'go');
  assert.equal(routing(gw, { otel: { enabled: true } }).engines.chat, 'go');
  assert.equal(routing(gw, { otel: { enabled: true, captureContent: false } }).engines.chat, 'go');
  const content = routing(gw, { otel: { enabled: true, captureContent: true } });
  assert.deepEqual([content.engines.chat, /captureContent/.test(content.notes.chat)], ['js', true]);
  assert.equal(routing(gw, { otel: { enabled: false, captureContent: true } }).engines.chat, 'go');
  let rule = null;
  routing(gw, { gate: (c, r) => ((rule = r), { ok: true }) });
  assert.deepEqual(rule, { days: 1, min: 20 }, 'the owner defaults');
  routing({ ...gw, shadowDays: 0, shadowMinCompares: 5 }, { gate: (c, r) => ((rule = r), { ok: true }) });
  assert.deepEqual(rule, { days: 0, min: 5 });
  routing({ ...gw, shadowDays: -1, shadowMinCompares: 'x' }, { gate: (c, r) => ((rule = r), { ok: true }) });
  assert.deepEqual(rule, { days: 1, min: 20 }, 'a value that is not a count of zero or more is the default');
  assert.deepEqual(shadowOf(gw, { chat: 'js' }), ['chat']);
  assert.deepEqual(shadowOf(gw, { chat: 'go' }), []);
});

test('the internal port answers the gateway own requests on /_zp/, and nothing else does', async (t) => {
  const token = 'a'.repeat(64);
  const dir = tmpDir(t, 'shadow-own');
  const shadowLog = createShadowLog({ file: path.join(dir, 's.jsonl') });
  const dashboard = new (require('events').EventEmitter)();
  const seen = [];
  dashboard.on('request', (req, res) => {
    seen.push(req.url);
    res.end('dashboard');
  });
  const inner = createInternalListener({ server: dashboard, token, own: shadowEndpoints({ shadowLog }) });
  const port = await inner.listen();
  t.after(() => inner.close());
  const ask = (method, p, headers = {}, body) =>
    new Promise((resolve, reject) => {
      const req = http.request(
        {
          host: '127.0.0.1',
          port,
          method,
          path: p,
          headers: { 'x-zp-client': signClient({ ip: '127.0.0.1', port: 4777, tls: false }, token), ...headers },
        },
        (res) => {
          let b = '';
          res.on('data', (c) => (b += c));
          res.on('end', () => resolve({ status: res.statusCode, body: b }));
        },
      );
      req.on('error', reject);
      req.end(body);
    });
  await withShadow('ffeeddccbbaa0099', async () =>
    noteAnswer({ status: 200, body: '' }, { status: 200, body: '' }, {}),
  );
  assert.equal((await ask('GET', '/_zp/shadow/ffeeddccbbaa0099', { 'x-zp-own': '1' })).status, 200);
  assert.equal((await ask('GET', '/_zp/shadow/ffeeddccbbaa0099', { 'x-zp-own': '1' })).status, 404, 'once');
  const line = JSON.stringify({ class: 'chat', outcome: 'same' });
  assert.equal((await ask('POST', '/_zp/shadow', { 'x-zp-own': '1' }, line)).status, 204);
  assert.equal((await ask('POST', '/_zp/shadow', { 'x-zp-own': '1' }, '{"x":1}')).status, 400);
  assert.equal(shadowLog.stats().chat.compared, 1);
  // Without the gateway's own mark, /_zp/ is an ordinary path for the dashboard.
  assert.equal((await ask('GET', '/_zp/shadow/ffeeddccbbaa0099')).body, 'dashboard');
  // The mark on an ordinary path is not passed on to the dashboard.
  assert.equal((await ask('GET', '/login.html', { 'x-zp-own': '1' })).status, 404);
  assert.deepEqual(seen, ['/_zp/shadow/ffeeddccbbaa0099']);
});

// Final review m9': the captures held in memory are bounded in bytes too; the oldest go first.
test('captures are bounded in bytes: the oldest is dropped first', async () => {
  const { configureCaptures } = require('../lib/gateway/shadow');
  configureCaptures({ maxBytes: 1000 });
  try {
    const big = 'x'.repeat(400);
    for (const id of ['aaaaaaaaaaaaaaa1', 'aaaaaaaaaaaaaaa2', 'aaaaaaaaaaaaaaa3'])
      await withShadow(id, async () => noteAnswer({ status: 200, body: big }, { status: 200, body: '' }, {}));
    assert.equal(takeCapture('aaaaaaaaaaaaaaa1'), null, 'dropped: 1200 bytes do not fit in 1000');
    assert.ok(takeCapture('aaaaaaaaaaaaaaa2'));
    assert.ok(takeCapture('aaaaaaaaaaaaaaa3'));
  } finally {
    configureCaptures({});
  }
});

test('routing: chat-stream is its own go class with its own gate, and is never a path class (P4c)', () => {
  const { CLASSES, GO_CLASSES } = require('../lib/front');
  assert.ok(GO_CLASSES.includes('chat-stream'));
  assert.ok(!CLASSES.includes('chat-stream'));
  const seen = [];
  const gate = (c) => (seen.push(c), c === 'chat' ? { ok: true } : { ok: false, reason: 'not yet' });
  const r = routing({ engines: { chat: 'go', 'chat-stream': 'go' } }, { gate });
  assert.equal(r.engines.chat, 'go');
  assert.equal(r.engines['chat-stream'], 'js');
  assert.equal(r.notes['chat-stream'], 'not yet');
  assert.deepEqual(seen, ['chat', 'chat-stream']);
  assert.deepEqual(shadowOf({ shadow: ['chat', 'chat-stream'] }, r.engines), ['chat-stream']);
  assert.equal(routing({}).engines['chat-stream'], 'js');
});

test('noteStream keeps the provider text, how it ended and the client text; capturing() says when to collect', async () => {
  const shadow = require('../lib/gateway/shadow');
  assert.equal(shadow.capturing(), false);
  const id = 'ab'.repeat(8);
  await shadow.withShadow(id, async () => {
    assert.equal(shadow.capturing(), true);
    shadow.noteProviderRequest('http://p/v1/chat/completions', { a: '1' }, '{"stream":true}');
    shadow.noteStream(
      { status: 200, body: 'data: x\n', ended: 'eof', truncated: false },
      { status: 200, body: 'data: x\n' },
      { stream: true },
    );
  });
  const c = shadow.takeCapture(id);
  assert.equal(c.providerResponse.ended, 'eof');
  assert.equal(c.providerResponse.truncated, false);
  assert.equal(c.client.body, 'data: x\n');
});

// P4f final review m3: a refusal's capture is stored when the answer ends, not when the handler's promise settles
// later (the gateway may ask for it the moment it has the answer; on Deno that came first).
test('a refusal capture is stored before the answer ends, while the handler is still awaiting (P4f m3)', async () => {
  let release;
  const later = new Promise((r) => (release = r));
  let seenAtEnd;
  const res = {
    statusCode: 429,
    end() {
      seenAtEnd = 'pending';
      return this;
    },
  };
  const done = withShadow(
    '00112233445566dd',
    async () => {
      noteClient('{"error":{"message":"rate"}}');
      res.end('{"error":{"message":"rate"}}');
      seenAtEnd = takeCapture('00112233445566dd');
      await later; // the route goes on awaiting after the answer ended
    },
    res,
  );
  assert.deepEqual(seenAtEnd, {
    providerRequests: [],
    providerResponse: null,
    upstream: [],
    client: { status: 429, headers: {}, body: '{"error":{"message":"rate"}}', truncated: false },
    usage: null,
    tape: { random: [], now: [] },
  });
  release();
  await done;
  assert.equal(takeCapture('00112233445566dd'), null, 'stored once');
});
