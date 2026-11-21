// Contract: /mcp tools/call as the go engine serves it (P4g). Only under ZP_ENGINE=go: a call whose arguments the
// guardrails redact writes its usage line from the gateway (the line carries "ipc", as go-formats.test.js asserts), a
// call they block answers JSON-RPC -32602 with its line from the gateway, and a clean call answers the tool's result
// with no line. The tool itself runs in the control plane (lib/front/tools.js); the integration "ct" is the fake
// upstream's MCP server, as in mcp.test.js. ZP_ENGINE=shadow compares mcp.test.js's calls.
const test = require('node:test');
const assert = require('node:assert/strict');
const { start } = require('./harness');

const GO = process.env.ZP_ENGINE === 'go';
const skip = !GO && 'the go engine serves /mcp only under ZP_ENGINE=go';
const SECRET = 'sk-ant-api03-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789abcd';
const BLOCKED =
  'Blocked by ZAAFPulse guardrails: secret (Anthropic API key). Remove it, or ask the owner to change the policy.';
const DEFAULTS = { scope: 'external', secrets: 'redact', pii: 'off', injection: 'log' };

let h;
test.before(async () => {
  if (!GO) return;
  h = await start();
  const r = await h.call('/api/integrations/save', {
    body: { id: 'ct', type: 'mcp', values: { url: `${h.upstream.url}/mcp` } },
  });
  assert.equal(r.status, 200, r.text);
  assert.equal(r.json.status.state, 'ok', r.text);
});
test.after(async () => {
  if (h) await h.stop();
});

const rpc = (method, params, id = 1) =>
  h.call('/mcp', { key: h.key, body: { jsonrpc: '2.0', id, method, ...(params ? { params } : {}) } });
const toolCalls = () =>
  h.upstream.requests.filter((r) => r.path === '/mcp' && r.body && r.body.method === 'tools/call');
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

test('go mcp: a redacted tools/call is served by the engine, its line from the gateway', { skip }, async () => {
  await h.setGuardrails(DEFAULTS);
  const before = (await h.logs()).length;
  const r = await rpc('tools/call', { name: 'ct__echo', arguments: { q: `x ${SECRET}` } });
  assert.equal(r.status, 200, r.text);
  assert.equal(toolCalls().at(-1).body.params.arguments.q, 'x ‹secret: Anthropic API key›');
  assert.match(r.json.result.content[0].text, /‹secret: Anthropic API key›/);
  const line = await lineAfter(before);
  assert.equal(line.kind, 'mcp');
  assert.deepEqual(line.guard, {
    action: 'redacted',
    hits: [{ check: 'secrets', label: 'Anthropic API key', count: 1 }],
  });
  assert.ok(line.ipc, `the go engine served it: ${JSON.stringify(line)}`);
  assert.doesNotMatch(JSON.stringify(line), /sk-ant-/);
});

test('go mcp: a blocked tools/call answers -32602 from the engine; the tool is not called', { skip }, async () => {
  await h.setGuardrails({ ...DEFAULTS, secrets: 'block' });
  try {
    const before = (await h.logs()).length;
    const calls = toolCalls().length;
    const r = await rpc('tools/call', { name: 'ct__echo', arguments: { q: `x ${SECRET}` } }, 7);
    assert.equal(r.status, 200, r.text);
    assert.deepEqual(r.json, { jsonrpc: '2.0', id: 7, error: { code: -32602, message: BLOCKED } });
    assert.equal(toolCalls().length, calls);
    const line = await lineAfter(before);
    assert.deepEqual([line.kind, line.status, line.guard.action], ['mcp', 400, 'blocked']);
    assert.ok(line.ipc, `the go engine served it: ${JSON.stringify(line)}`);
  } finally {
    await h.setGuardrails(DEFAULTS);
  }
});

test('go mcp: a clean tools/call answers the tool result and writes no line', { skip }, async () => {
  const before = (await h.logs()).length;
  const calls = toolCalls().length;
  const r = await rpc('tools/call', { name: 'ct__echo', arguments: { q: 'hi' } });
  assert.equal(r.status, 200, r.text);
  assert.deepEqual(r.json.result, { content: [{ type: 'text', text: 'echo: {"q":"hi"}' }], isError: false });
  assert.equal(toolCalls().length, calls + 1);
  await new Promise((res) => setTimeout(res, 300));
  assert.equal((await h.logs()).length, before);
});
