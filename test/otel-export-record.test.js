// A span the go engine stored and the control plane exports (P4f D11) is the OTLP span JS would have exported: a JS
// span is exported, its raw record (the sink's copy) becomes the stored record (toRecord) plus the trace.span otlp
// extras, exportRecord exports that again, and the two encoded spans are equal.
const test = require('node:test');
const assert = require('node:assert/strict');
const { createOtel, KIND } = require('../lib/otel');
const { toRecord } = require('../lib/trace/recorder');

function exporting() {
  const batches = [];
  const raw = [];
  let cfg = { otel: { enabled: true, endpoint: 'http://127.0.0.1:9', protocol: 'http/json' } };
  const o = createOtel({
    getConfig: () => cfg,
    env: {},
    refreshMs: 60000,
    transport: async (url, body) => {
      batches.push(JSON.parse(String(body)));
      return { status: 200 };
    },
    sink: { enabled: () => true, add: (r) => raw.push(r) },
    log: () => {},
  });
  return { o, batches, raw, set: (c) => (cfg = c) };
}
const spanOf = (b) => b.resourceSpans[0].scopeSpans[0].spans[0];
const extraOf = (r) => ({
  ...(r.traceState ? { traceState: r.traceState } : {}),
  ...(r.flags & 0x200 ? { remote: true } : {}),
  ...(r.status.code === 2 && r.status.message ? { message: r.status.message } : {}),
});

test('exportRecord exports a stored server span with a remote parent exactly as JS exported it', async () => {
  const { o, batches, raw } = exporting();
  const parent = {
    traceId: '0af7651916cd43dd8448eb211c80319c',
    spanId: 'b7ad6b7169203331',
    sampled: true,
    traceState: 'a=1',
    remote: true,
  };
  const s = o.startSpan('POST /v1/chat/completions', {
    kind: KIND.SERVER,
    parent,
    attributes: {
      'http.response.status_code': 502,
      'zaafpulse.cost.usd': 0,
      'gen_ai.response.finish_reasons': ['stop'],
    },
  });
  s.fail('502', 'upstream said Bearer abcdefgh12345678');
  s.end();
  await o.flush(1000);
  assert.equal(o.exportRecord(toRecord(raw[0]), extraOf(raw[0])), true);
  await o.flush(1000);
  assert.equal(batches.length, 2);
  assert.deepEqual(spanOf(batches[1]), spanOf(batches[0]));
});

test('exportRecord exports a stored root client span with a whole-number cost as a double', async () => {
  const { o, batches, raw } = exporting();
  const s = o.startSpan('chat oa/m1', {
    kind: KIND.CLIENT,
    attributes: { 'zaafpulse.cost.usd': 1, 'gen_ai.usage.input_tokens': 3 },
  });
  s.end();
  await o.flush(1000);
  assert.equal(o.exportRecord(toRecord(raw[0]), extraOf(raw[0])), true);
  await o.flush(1000);
  assert.deepEqual(spanOf(batches[1]), spanOf(batches[0]));
});

test('exportRecord is false and quiet while export is off', () => {
  const o = createOtel({ getConfig: () => ({}), env: {}, log: () => {} });
  const span = { t: 'a'.repeat(32), s: 'b'.repeat(16), p: null, n: 'x', k: 'server', st: 1, d: 1, ok: true, a: {} };
  assert.equal(o.exportRecord(span, {}), false);
});

test('status({ fresh: true }) reads config.json again inside the refresh window (P4f D12)', () => {
  const { o, set } = exporting();
  assert.equal(o.status().enabled, true);
  set({ otel: { enabled: false } });
  assert.equal(o.status().enabled, true, 'cached for refreshMs');
  assert.equal(o.status({ fresh: true }).enabled, false);
});
