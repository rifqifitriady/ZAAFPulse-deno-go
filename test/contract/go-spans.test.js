// Contract: the spans of a chat call (P4f D1, D6). In every mode the trace store holds, for a call with a traceparent,
// the server span "POST /v1/chat/completions" (its parent: the caller's span) and the client span "chat oa/m1" under it,
// and the usage line names the trace. Under ZP_ENGINE=go the spans come through the IPC ingest (their stored lines carry
// "ipc"), so the run proves the engine sent them. A call with an unsampled traceparent has no span and no trace id.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
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

function storedSpans(traceId) {
  if (!h.server.data) return null; // a running instance (ZP_CONTRACT_URL): its folder is not ours to read
  const dir = path.join(h.server.data, '.data', 'traces');
  const out = [];
  for (const f of fs.existsSync(dir) ? fs.readdirSync(dir).filter((x) => x.endsWith('.ndjson')) : [])
    for (const l of fs.readFileSync(path.join(dir, f), 'utf8').split('\n'))
      if (l.includes(traceId)) out.push(JSON.parse(l));
  return out;
}

async function waitFor(fn, what, ms = 5000) {
  const end = Date.now() + ms;
  for (;;) {
    const v = await fn();
    if (v) return v;
    if (Date.now() > end) throw new Error(`timed out waiting for ${what}`);
    await new Promise((r) => setTimeout(r, 50));
  }
}

const user = [{ role: 'user', content: 'hi' }];

test('a traced chat call stores its server and client spans and names its trace on the usage line', async (t) => {
  if (!h.server.data) return t.skip('a running instance: the trace store is not readable from here');
  const traceId = '4bf92f3577b34da6a3ce929d0e0e4736';
  const r = await h.call('/v1/chat/completions', {
    key: h.key,
    body: { model: 'oa/m1', messages: user },
    headers: { traceparent: `00-${traceId}-00f067aa0ba902b7-01` },
  });
  assert.equal(r.status, 200);
  // The JS recorder writes its spans within a second; the ingest writes the go engine's at once.
  const spans = await waitFor(() => {
    const s = storedSpans(traceId);
    return s.length >= 2 ? s : null;
  }, 'two stored spans');
  const server = spans.find((s) => s.k === 'server');
  const client = spans.find((s) => s.k === 'client');
  assert.equal(server.n, 'POST /v1/chat/completions');
  assert.equal(server.p, '00f067aa0ba902b7');
  assert.equal(client.n, 'chat oa/m1');
  assert.equal(client.p, server.s);
  assert.equal(client.a['gen_ai.request.model'], 'oa/m1');
  assert.equal(client.a['gen_ai.response.id'], 'c1');
  if (GO) assert.ok(server.ipc && client.ipc, 'the go engine sent both spans');
  // The usage log on disk (the logs API leaves traceId out).
  const usage = () =>
    fs
      .readFileSync(path.join(h.server.data, '.data', 'gateway-usage.jsonl'), 'utf8')
      .split('\n')
      .filter(Boolean)
      .map((l) => JSON.parse(l));
  const line = await waitFor(() => usage().find((l) => l.traceId === traceId), 'the usage line');
  if (GO) assert.ok(line.ipc, 'the go engine wrote the line');
});

test('an unsampled call stores no span and its usage line names no trace', async (t) => {
  if (!h.server.data) return t.skip('a running instance');
  const traceId = '5bf92f3577b34da6a3ce929d0e0e4736';
  const before = (await h.logs()).length;
  const r = await h.call('/v1/chat/completions', {
    key: h.key,
    body: { model: 'oa/m1', messages: user },
    headers: { traceparent: `00-${traceId}-00f067aa0ba902b7-00` },
  });
  assert.equal(r.status, 200);
  const logs = await waitFor(async () => {
    const l = await h.logs();
    return l.length > before ? l : null;
  }, 'the usage line');
  assert.equal(logs.length, before + 1);
  const last = fs
    .readFileSync(path.join(h.server.data, '.data', 'gateway-usage.jsonl'), 'utf8')
    .trim()
    .split('\n')
    .pop();
  assert.equal(JSON.parse(last).traceId, undefined);
  await new Promise((res) => setTimeout(res, 1500)); // past the JS recorder's flush
  assert.deepEqual(storedSpans(traceId), []);
});
