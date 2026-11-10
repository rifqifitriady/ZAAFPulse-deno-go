// Contract: /mcp through the gateway (MCP Streamable HTTP, stateless JSON-RPC) with a gateway key: initialize, the tools
// of an MCP integration at the fake upstream (integration "ct": tools ct__echo, ct__inject, ct__broken), and the
// guardrails on tool arguments and results.
const test = require('node:test');
const assert = require('node:assert/strict');
const { start } = require('./harness');
const { INJECTION } = require('./upstream');

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
  if (h) {
    await h.setGuardrails(DEFAULTS);
    await h.call('/api/integrations/delete', { body: { id: 'ct' } });
    await h.stop();
  }
});

const rpc = (method, params, id = 1, key = h.key) =>
  h.call('/mcp', { key, body: { jsonrpc: '2.0', id, method, ...(params ? { params } : {}) } });
const toolCalls = () =>
  h.upstream.requests.filter((r) => r.path === '/mcp' && r.body && r.body.method === 'tools/call');

const CASES = [
  {
    name: 'initialize: the gateway names itself and speaks the version asked for',
    run: async () => {
      const r = await rpc('initialize', { protocolVersion: '2025-06-18' });
      assert.equal(r.status, 200);
      assert.equal(r.json.result.serverInfo.name, 'zaafpulse-gateway');
      assert.equal(r.json.result.protocolVersion, '2025-06-18');
    },
  },
  {
    name: 'tools/call: an integration tool answers through the gateway with its arguments',
    run: async () => {
      const r = await rpc('tools/call', { name: 'ct__echo', arguments: { q: 'hi' } });
      assert.equal(r.status, 200);
      assert.deepEqual(r.json.result, { content: [{ type: 'text', text: 'echo: {"q":"hi"}' }], isError: false });
    },
  },
  {
    name: 'refused content: a prompt injection in a tool result is replaced by the block message under block',
    run: async () => {
      await h.setGuardrails({ ...DEFAULTS, injection: 'block' });
      const r = await rpc('tools/call', { name: 'ct__inject', arguments: {} });
      assert.equal(r.status, 200);
      assert.equal(r.json.result.isError, true);
      assert.match(r.json.result.content[0].text, /^Blocked by ZAAFPulse guardrails: prompt injection \(/);
      const e = (await h.logs())[0];
      assert.deepEqual([e.kind, e.status, e.guard.action], ['mcp', 400, 'blocked']);
    },
  },
  {
    name: 'no mcp scope: 403',
    run: async () => {
      const k = await h.newKey({ scopes: ['llm'] });
      const r = await rpc('tools/list', undefined, 1, k.secret);
      assert.equal(r.status, 403);
    },
  },
  {
    name: 'tools/list: the integration tools are named <id>__<tool>',
    run: async () => {
      const r = await rpc('tools/list');
      assert.equal(r.status, 200);
      assert.deepEqual(
        r.json.result.tools.map((t) => t.name).filter((n) => n.startsWith('ct')),
        ['ct__echo', 'ct__inject', 'ct__broken'],
      );
    },
  },
  {
    name: 'a secret in the arguments under redact: the integration gets the placeholder; recorded without the text',
    run: async () => {
      await h.setGuardrails(DEFAULTS);
      const r = await rpc('tools/call', { name: 'ct__echo', arguments: { q: `x ${SECRET}` } });
      assert.equal(r.status, 200);
      assert.equal(toolCalls().at(-1).body.params.arguments.q, 'x ‹secret: Anthropic API key›');
      assert.match(r.json.result.content[0].text, /‹secret: Anthropic API key›/);
      const e = (await h.logs())[0];
      assert.equal(e.kind, 'mcp');
      assert.deepEqual(e.guard, {
        action: 'redacted',
        hits: [{ check: 'secrets', label: 'Anthropic API key', count: 1 }],
      });
      assert.doesNotMatch(JSON.stringify(e), /sk-ant-/);
    },
  },
  {
    name: 'a secret in the arguments under block: JSON-RPC -32602 and the integration is not called',
    run: async () => {
      await h.setGuardrails({ ...DEFAULTS, secrets: 'block' });
      const before = toolCalls().length;
      const r = await rpc('tools/call', { name: 'ct__echo', arguments: { q: `x ${SECRET}` } }, 7);
      assert.equal(r.status, 200);
      assert.deepEqual(r.json, { jsonrpc: '2.0', id: 7, error: { code: -32602, message: BLOCKED } });
      assert.equal(toolCalls().length, before);
    },
  },
  {
    name: 'an injection in a result under log: passed unchanged, recorded as logged',
    run: async () => {
      await h.setGuardrails({ ...DEFAULTS, injection: 'log' });
      const r = await rpc('tools/call', { name: 'ct__inject', arguments: {} });
      assert.equal(r.status, 200);
      assert.equal(r.json.result.isError, false);
      assert.equal(r.json.result.content[0].text, INJECTION);
      const e = (await h.logs())[0];
      assert.equal(e.guard.action, 'logged');
      const labels = e.guard.hits.filter((x) => x.check === 'injection').map((x) => x.label);
      assert.ok(labels.includes('Exfiltration request') && labels.includes('Instruction override'), String(labels));
    },
  },
  {
    name: 'a tool error result passes through with its error flag',
    run: async () => {
      await h.setGuardrails(DEFAULTS);
      const r = await rpc('tools/call', { name: 'ct__broken', arguments: {} });
      assert.equal(r.status, 200);
      assert.deepEqual(r.json.result, { content: [{ type: 'text', text: 'tool failed' }], isError: true });
    },
  },
  {
    name: 'an unknown tool: JSON-RPC -32602 Unknown tool',
    run: async () => {
      const r = await rpc('tools/call', { name: 'ct__nope', arguments: {} });
      assert.equal(r.status, 200);
      assert.deepEqual(r.json, { jsonrpc: '2.0', id: 1, error: { code: -32602, message: 'Unknown tool: ct__nope' } });
    },
  },
  {
    name: 'an unsupported MCP-Protocol-Version header: 400 -32600',
    run: async () => {
      const r = await h.call('/mcp', {
        key: h.key,
        headers: { 'mcp-protocol-version': '1999-01-01' },
        body: { jsonrpc: '2.0', id: 1, method: 'tools/list' },
      });
      assert.equal(r.status, 400);
      assert.equal(r.json.jsonrpc, '2.0');
      assert.equal(r.json.id, null);
      assert.equal(r.json.error.code, -32600);
      assert.ok(r.json.error.message.startsWith('unsupported MCP-Protocol-Version "1999-01-01"'), r.json.error.message);
    },
  },
  {
    name: 'a JSON-RPC batch under 2025-06-18: 400 -32600',
    run: async () => {
      const r = await h.call('/mcp', {
        key: h.key,
        body: [
          { jsonrpc: '2.0', id: 1, method: 'initialize', params: { protocolVersion: '2025-06-18' } },
          { jsonrpc: '2.0', id: 2, method: 'tools/list' },
        ],
      });
      assert.equal(r.status, 400);
      assert.equal(r.json.error.code, -32600);
      assert.equal(r.json.error.message, 'JSON-RPC batches are not part of MCP 2025-06-18; send one message per POST');
    },
  },
  {
    name: 'GET /mcp is 405; a notification is 202 with an empty body',
    run: async () => {
      const g = await h.call('/mcp', { method: 'GET', key: h.key });
      assert.equal(g.status, 405);
      const n = await h.call('/mcp', { key: h.key, body: { jsonrpc: '2.0', method: 'notifications/initialized' } });
      assert.equal(n.status, 202);
      assert.equal(n.text, '');
    },
  },
];

for (const c of CASES) test(`mcp: ${c.name}`, c.run);
