// The integration tool endpoint of the go engine's /mcp tools/call (P4g Task 2, lib/front/tools.js): POST /_zp/mcp/call
// answers callTool's value as { r }, a throw as { thrown: true }, a bad body with 400, and nothing else.
const test = require('node:test');
const assert = require('node:assert/strict');
const http = require('http');
const { toolEndpoint } = require('../lib/front/tools');
const govContext = require('../lib/governance/context');
const otel = require('../lib/otel');

// The endpoint on a real server; anything it does not take gets the fallback's 404 (as shadowEndpoints does).
async function serve(t, callTool) {
  const calls = [];
  const tools = toolEndpoint({
    getIntegrations: () => ({
      callTool: async (name, args) => {
        calls.push({ name, args, actor: govContext.actor() });
        return callTool(name, args);
      },
    }),
  });
  const server = http.createServer((req, res) => tools(req, res) || res.writeHead(404).end());
  await new Promise((r) => server.listen(0, '127.0.0.1', r));
  t.after(() => server.close());
  const ask = (method, path, body) =>
    new Promise((resolve, reject) => {
      const req = http.request({ host: '127.0.0.1', port: server.address().port, method, path }, (res) => {
        let b = '';
        res.on('data', (c) => (b += c));
        res.on('end', () => resolve({ status: res.statusCode, type: res.headers['content-type'], body: b }));
      });
      req.on('error', reject);
      req.end(body);
    });
  return { calls, ask };
}

test('a call answers the tool value as { r }', async (t) => {
  const { calls, ask } = await serve(t, () => ({ result: { content: [{ type: 'text', text: 'ok' }] } }));
  const r = await ask('POST', '/_zp/mcp/call', '{"name":"dt_query","arguments":{"q":"x","n":2.5}}');
  assert.deepEqual(r, {
    status: 200,
    type: 'application/json',
    body: '{"r":{"result":{"content":[{"type":"text","text":"ok"}]}}}',
  });
  assert.deepEqual(calls, [{ name: 'dt_query', args: { q: 'x', n: 2.5 }, actor: null }]);
});

test('an undefined tool value is {} (JSON.stringify), a null one { "r": null }', async (t) => {
  let v;
  const { ask } = await serve(t, () => v);
  assert.equal((await ask('POST', '/_zp/mcp/call', '{"name":"x","arguments":{}}')).body, '{}');
  v = null;
  assert.equal((await ask('POST', '/_zp/mcp/call', '{"name":"x","arguments":{}}')).body, '{"r":null}');
});

test('a throw answers { thrown: true }', async (t) => {
  const { ask } = await serve(t, () => {
    throw new Error('the integration broke');
  });
  const r = await ask('POST', '/_zp/mcp/call', '{"name":"x","arguments":{}}');
  assert.deepEqual([r.status, r.body], [200, '{"thrown":true}']);
});

test('the call runs as the key named', async (t) => {
  const { calls, ask } = await serve(t, () => ({}));
  await ask('POST', '/_zp/mcp/call', '{"name":"x","arguments":{},"keyId":"k1"}');
  assert.deepEqual(calls[0].actor, { via: 'apikey', keyId: 'k1' });
});

test('a body that is not { name: string, arguments: object } is 400, and no tool runs', async (t) => {
  const { calls, ask } = await serve(t, () => ({}));
  for (const body of [
    '',
    'not json',
    '[]',
    'null',
    '{"arguments":{}}',
    '{"name":5,"arguments":{}}',
    '{"name":"x"}',
    '{"name":"x","arguments":null}',
    '{"name":"x","arguments":[]}',
    '{"name":"x","arguments":"a"}',
    '{"name":"x","arguments":{},"keyId":5}',
    '{"name":"x","arguments":{},"trace":"t"}',
  ]) {
    const r = await ask('POST', '/_zp/mcp/call', body);
    assert.equal(r.status, 400, body);
  }
  assert.equal(calls.length, 0);
});

test('any other request is not the endpoint’s', async (t) => {
  const { calls, ask } = await serve(t, () => ({}));
  assert.equal((await ask('GET', '/_zp/mcp/call')).status, 404);
  assert.equal((await ask('POST', '/_zp/mcp/call/x', '{"name":"x","arguments":{}}')).status, 404);
  assert.equal((await ask('POST', '/_zp/shadow', '{}')).status, 404);
  assert.equal(calls.length, 0);
});

test('with a trace, a span started inside the call nests under it and is reported as child', async (t) => {
  const records = [];
  otel.init({
    getConfig: () => ({}),
    env: {},
    refreshMs: 0,
    log: () => {},
    sink: { enabled: () => true, add: (rec) => records.push(rec) },
  });
  t.after(() => otel.reset());
  const { ask } = await serve(t, () => {
    otel.toolCall('dt_query', { id: 'dt', type: 'http' }, {}).end(true, { content: [] });
    return { result: { content: [] } };
  });
  const trace = { traceId: '0af7651916cd43dd8448eb211c80319c', spanId: 'b7ad6b7169203331', sampled: true };
  const r = await ask('POST', '/_zp/mcp/call', JSON.stringify({ name: 'dt_query', arguments: {}, trace }));
  assert.equal(r.body, '{"r":{"result":{"content":[]}},"child":true}');
  assert.equal(records.length, 1);
  assert.equal(records[0].traceId, trace.traceId);
  assert.equal(records[0].parentSpanId, trace.spanId);
  // An unsampled trace: no span, nothing reported.
  const off = await ask(
    'POST',
    '/_zp/mcp/call',
    JSON.stringify({ name: 'x', arguments: {}, trace: { ...trace, sampled: false } }),
  );
  assert.equal(off.body, '{"r":{"result":{"content":[]}}}');
  assert.equal(records.length, 1);
  // No trace: the call runs plainly, and its span is not the gateway's child.
  const plain = await ask('POST', '/_zp/mcp/call', '{"name":"x","arguments":{}}');
  assert.equal(plain.body, '{"r":{"result":{"content":[]}}}');
});
