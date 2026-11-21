// Contract: /mcp tools/call as the go engine serves it (P4g). A call whose arguments the guardrails redact writes its
// usage line, a call they block answers JSON-RPC -32602 with a line, and a clean call answers the tool's result with no
// line. The cases hold in every mode; under ZP_ENGINE=go the lines come from the gateway (they carry "ipc", as
// go-formats.test.js asserts); under ZP_ENGINE=shadow the gateway must have compared these calls (shadow.mcp) with no
// diff. The tool itself runs in the control plane (lib/front/tools.js); the integration "ct" is the fake upstream's MCP
// server, as in mcp.test.js.
const test = require('node:test');
const assert = require('node:assert/strict');
const { start } = require('./harness');

const GO = process.env.ZP_ENGINE === 'go';
const SECRET = 'sk-ant-api03-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789abcd';
const BLOCKED =
  'Blocked by ZAAFPulse guardrails: secret (Anthropic API key). Remove it, or ask the owner to change the policy.';
const DEFAULTS = { scope: 'external', secrets: 'redact', pii: 'off', injection: 'log' };

let h;
test.before(async () => {
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

test('go mcp: a redacted tools/call is served by the engine, its line from the gateway', async () => {
  await h.setGuardrails(DEFAULTS);
  const before = (await h.logs()).length;
  const r = await rpc('tools/call', { name: 'ct__echo', arguments: { q: `x ${SECRET}` } });
  assert.equal(r.status, 200, r.text);
  assert.equal(toolCalls().at(-1).body.params.arguments.q, 'x ‹secret: Anthropic API key›');
  assert.match(r.json.result.content[0].text, /‹secret: Anthropic API key›/);
  const line = await h.lineAfter(before);
  assert.equal(line.kind, 'mcp');
  assert.deepEqual(line.guard, {
    action: 'redacted',
    hits: [{ check: 'secrets', label: 'Anthropic API key', count: 1 }],
  });
  if (GO) assert.ok(line.ipc, `the go engine served it: ${JSON.stringify(line)}`);
  assert.doesNotMatch(JSON.stringify(line), /sk-ant-/);
});

test('go mcp: a blocked tools/call answers -32602 from the engine; the tool is not called', async () => {
  await h.setGuardrails({ ...DEFAULTS, secrets: 'block' });
  try {
    const before = (await h.logs()).length;
    const calls = toolCalls().length;
    const r = await rpc('tools/call', { name: 'ct__echo', arguments: { q: `x ${SECRET}` } }, 7);
    assert.equal(r.status, 200, r.text);
    assert.deepEqual(r.json, { jsonrpc: '2.0', id: 7, error: { code: -32602, message: BLOCKED } });
    assert.equal(toolCalls().length, calls);
    const line = await h.lineAfter(before);
    assert.deepEqual([line.kind, line.status, line.guard.action], ['mcp', 400, 'blocked']);
    if (GO) assert.ok(line.ipc, `the go engine served it: ${JSON.stringify(line)}`);
  } finally {
    await h.setGuardrails(DEFAULTS);
  }
});

test('go mcp: a clean tools/call answers the tool result and writes no line', async () => {
  const before = (await h.logs()).length;
  const calls = toolCalls().length;
  const r = await rpc('tools/call', { name: 'ct__echo', arguments: { q: 'hi' } });
  assert.equal(r.status, 200, r.text);
  assert.deepEqual(r.json.result, { content: [{ type: 'text', text: 'echo: {"q":"hi"}' }], isError: false });
  assert.equal(toolCalls().length, calls + 1);
  await new Promise((res) => setTimeout(res, 300));
  assert.equal((await h.logs()).length, before);
});

// ZP_ENGINE=shadow: the three calls above were compared with the go engine (the blocked one included), with no diff; a
// switch that stopped shadowing mcp would leave the diff log empty and pass without this.
test(
  'go mcp: the shadow compared the tools/call calls, with no diff',
  { skip: process.env.ZP_ENGINE !== 'shadow' },
  async () => {
    const st = await h.shadowStats('mcp', 3);
    assert.ok(st && st.compared >= 3, JSON.stringify(st));
    assert.equal(st.diffs, 0, JSON.stringify(st));
  },
);
