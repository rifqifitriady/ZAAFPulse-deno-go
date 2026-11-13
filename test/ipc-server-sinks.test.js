// The IPC ingest's writers in server.js (P4a E3): strict and unwrapped. server.js passes none of its wrapped writers,
// a failed append is not acknowledged, and the budget report runs once, after the stored usage line. Usage goes
// through the gateway's own writer, so the gateway's in-memory spend counts it (P4b entry condition 1).
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const { serverSinks } = require('../lib/ipc/server-sinks');
const { createIngest } = require('../lib/ipc/ingest');
const { createAudit } = require('../lib/audit');
const { tmpDir } = require('./ipc-stub');
const { createGateway } = require('../lib/gateway');
const { secretsFor } = require('./helpers');

// A gateway on a temp data folder with one key, k (the usage lines name it), and US$1/US$2 per 1M tokens for m.
function gatewayIn(dir, { budgetUSD = null } = {}) {
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(
    path.join(dir, 'keys.json'),
    JSON.stringify({
      keys: [{ id: 'k', name: 'k', hash: '0'.repeat(64), scopes: ['llm'], models: [], agents: [], budgetUSD, rpm: 60 }],
    }),
  );
  return createGateway({
    dataDir: dir,
    getConfig: () => ({ gateway: {} }),
    writeGatewayConfig: () => {},
    secrets: secretsFor(),
    resolveSecret: () => undefined,
    getPricing: () => ({ m: { input: 1, output: 2 } }),
    getIntegrations: () => ({ listTools: async () => [], callTool: async () => ({}) }),
  });
}

const STREAM = 'b'.repeat(32);
const usageMsg = (seq) => ({
  type: 'usage.report',
  seq,
  body: { record: { ts: 1, keyId: 'k', provider: 'p', model: 'm', status: 200, ms: 1, cost: 2000000 } },
});

test('server.js builds the ingest from strict writers, never from its wrapped ones', () => {
  const src = fs.readFileSync(path.join(__dirname, '..', 'server.js'), 'utf8');
  const at = src.indexOf('serverSinks({');
  assert.ok(at > 0, 'server.js calls serverSinks');
  const call = src.slice(at, src.indexOf('});', at));
  for (const wrapped of ['govRecord', 'govWrite', 'gateway.usage', 'usage.record', 'recorder', 'createUsage'])
    assert.ok(!call.includes(wrapped), `the ingest gets ${wrapped}`);
  assert.match(call, /recordUsage: \(e\) => gateway\.ingestUsage\(e\),/);
  assert.match(call, /\bgovernance,/);
  assert.match(call, /mcpAudit: audit,/);
  assert.match(call, /\btraceStore,/);
});

test('usage: stored strictly with its mark, then the budget report once', (t) => {
  const dir = tmpDir(t, 'sinks');
  const reported = [];
  const gateway = gatewayIn(dir);
  const sinks = serverSinks({
    recordUsage: (e) => gateway.ingestUsage(e),
    budgetRecord: (e) => reported.push(e),
    governance: { record: () => ({}) },
    mcpAudit: createAudit(path.join(dir, 'mcp-audit.jsonl')),
    traceStore: null,
    traceDir: path.join(dir, 'traces'),
  });
  const ingest = createIngest({ ...sinks, log: () => {} });
  assert.equal(ingest.handle(usageMsg(1), STREAM), true);
  const lines = fs.readFileSync(path.join(dir, 'gateway-usage.jsonl'), 'utf8').trim().split('\n');
  assert.equal(lines.length, 1);
  assert.deepEqual(JSON.parse(lines[0]).ipc, { stream: STREAM, seq: 1 });
  assert.equal(reported.length, 1);
  assert.equal(reported[0].cost, 0.002);
});

test('P4b entry 1: a Go engine line counts in the gateway spend, the recent list and the key last use', (t) => {
  const dir = tmpDir(t, 'sinks-spend');
  const gateway = gatewayIn(dir, { budgetUSD: 0.001 });
  const sinks = serverSinks({
    recordUsage: (e) => gateway.ingestUsage(e),
    budgetRecord: () => {},
    governance: { record: () => ({}) },
    mcpAudit: createAudit(path.join(dir, 'mcp-audit.jsonl')),
    traceStore: null,
    traceDir: path.join(dir, 'traces'),
  });
  const ingest = createIngest({ ...sinks, log: () => {} });
  const ts = Date.now();
  const msg = { ...usageMsg(1), body: { record: { ...usageMsg(1).body.record, ts } } };
  assert.equal(ingest.handle(msg, STREAM), true);
  assert.equal(gateway.usage.monthSpend('k'), 0.002, 'the per-key spend a budget reads');
  assert.equal(gateway.usage.recent(1)[0].ipc.seq, 1);
  assert.equal(gateway.keys.list()[0].lastUsedAt, ts);
});

test('serverSinks refuses to start without the gateway writer', () => {
  assert.throws(() => serverSinks({ budgetRecord: () => {} }), /recordUsage \(gateway\.ingestUsage\) is required/);
});

test('a write that fails is not marked: governance null, an unwritable usage log, no trace store', (t) => {
  const dir = tmpDir(t, 'sinks-fail');
  const gateway = gatewayIn(dir);
  fs.mkdirSync(path.join(dir, 'gateway-usage.jsonl')); // a folder: the append fails
  const sinks = serverSinks({
    recordUsage: (e) => gateway.ingestUsage(e),
    budgetRecord: () => {},
    governance: { record: () => null }, // the chain could not write
    mcpAudit: createAudit(path.join(dir, 'mcp-audit.jsonl')),
    traceStore: null,
    traceDir: path.join(dir, 'traces'),
  });
  const ingest = createIngest({ ...sinks, log: () => {} });
  assert.throws(() => ingest.handle(usageMsg(1), STREAM));
  const gov = { type: 'audit.event', seq: 1, body: { log: 'governance', event: { action: 'key.update' } } };
  assert.throws(
    () => createIngest({ ...sinks, log: () => {} }).handle(gov, STREAM),
    /governance audit could not write/,
  );
  const span = {
    type: 'trace.span',
    seq: 1,
    body: {
      span: {
        t: '1'.repeat(32),
        s: '1'.repeat(16),
        p: null,
        n: 'x',
        k: 'client',
        st: 1,
        d: 1,
        ok: true,
        src: 'gateway',
        svc: 'z',
        a: {},
      },
    },
  };
  assert.throws(() => createIngest({ ...sinks, log: () => {} }).handle(span, STREAM), /trace store is off/);
  assert.equal(ingest.mark(STREAM), 0);
});
