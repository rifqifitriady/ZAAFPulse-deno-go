// lib/ipc/channel.js and lib/ipc/acker.js (P3 D1, D2, D4): messages in and out over a pair of streams, an unknown type
// logged once and dropped, a bad line or an invalid message closing the channel, strict sending, batched acks.
const test = require('node:test');
const assert = require('node:assert/strict');
const { PassThrough } = require('stream');
const { createChannel } = require('../lib/ipc/channel');
const { createAcker } = require('../lib/ipc/acker');

const S = '0f1e2d3c4b5a69788796a5b4c3d2e1f0';
const ready = (seq) => ({ v: 1, type: 'ready', seq, ts: 0, body: { gen: 1 } });

function pair(opts = {}) {
  const input = new PassThrough();
  const output = new PassThrough();
  const got = [];
  const closes = [];
  const logs = [];
  const ch = createChannel({ input, output, log: (l) => logs.push(l), ...opts });
  ch.onMessage((m) => got.push(m));
  ch.onClose((i) => closes.push(i));
  return { input, output, ch, got, closes, logs };
}

test('messages in, one per line; messages out, one per line', () => {
  const p = pair({ strict: true });
  p.input.write(`${JSON.stringify(ready(1))}\n${JSON.stringify(ready(2))}\n`);
  assert.deepEqual(
    p.got.map((m) => m.seq),
    [1, 2],
  );
  assert.equal(p.ch.send({ v: 1, type: 'ack', seq: 1, ts: 0, body: { stream: S, upTo: 2 } }), true);
  assert.equal(String(p.output.read()), `{"v":1,"type":"ack","seq":1,"ts":0,"body":{"stream":"${S}","upTo":2}}\n`);
});

test('an unknown type is logged once per type and dropped (D2)', () => {
  const p = pair();
  const unknown = (seq) => JSON.stringify({ v: 1, type: 'telemetry.v9', seq, ts: 0, body: {} });
  p.input.write(`${unknown(1)}\n${unknown(2)}\n${JSON.stringify(ready(3))}\n`);
  assert.deepEqual(
    p.got.map((m) => m.type),
    ['ready'],
  );
  assert.deepEqual(p.logs, ['ipc: unknown message type "telemetry.v9" dropped']);
  assert.deepEqual(p.closes, []);
});

test('lenient by default: an unknown field is ignored; strict refuses it', () => {
  const extra = JSON.stringify({ ...ready(1), body: { gen: 1, region: 'eu' } });
  const lenient = pair();
  lenient.input.write(`${extra}\n`);
  assert.equal(lenient.got.length, 1);
  const strict = pair({ strict: true });
  strict.input.write(`${extra}\n`);
  assert.equal(strict.got.length, 0);
  assert.equal(strict.closes[0].reason, 'protocol');
});

test('a line that is not JSON, or an invalid message, closes the channel as a protocol error', () => {
  const bad = pair();
  bad.input.write('not json\n');
  assert.deepEqual(bad.closes, [{ reason: 'protocol', error: 'a line is not JSON' }]);
  assert.equal(bad.ch.send(ready(1)), false, 'a closed channel sends nothing');
  const invalid = pair();
  invalid.input.write(`${JSON.stringify({ ...ready(1), body: {} })}\n`);
  assert.match(invalid.closes[0].error, /^ready: \$\.body\.gen: required$/);
});

test('the end of the input closes the channel with reason eof', async () => {
  const p = pair();
  p.input.end();
  await new Promise((r) => setImmediate(r));
  assert.deepEqual(p.closes, [{ reason: 'eof' }]);
});

test('strict send refuses a message that is not valid v1', () => {
  const p = pair({ strict: true });
  assert.throws(() => p.ch.send({ v: 1, type: 'ack', seq: 1, ts: 0, body: { stream: 'x', upTo: 1 } }), /invalid ack/);
});

test('acks: one per batch with the highest seq, when the timer fires or max events wait', () => {
  const sent = [];
  const timers = [];
  const a = createAcker({
    send: (b) => sent.push(b),
    everyMs: 100,
    max: 3,
    setTimer: (fn) => (timers.push(fn), timers.length),
    clearTimer: () => {},
  });
  a.note(S, 1);
  a.note(S, 2);
  assert.deepEqual(sent, []);
  timers[0]();
  assert.deepEqual(sent, [{ stream: S, upTo: 2 }]);
  a.note(S, 3);
  a.note(S, 4);
  a.note(S, 5);
  assert.deepEqual(sent.at(-1), { stream: S, upTo: 5 }, 'the third waiting event flushes at once');
  a.flush();
  assert.equal(sent.length, 2, 'nothing new: no second ack for the same seq');
});
