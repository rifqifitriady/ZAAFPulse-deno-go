// The IPC ingest's writers in server.js (P4a E3): strict and unwrapped. server.js passes none of its wrapped writers,
// a failed append is not acknowledged, and the budget report runs once, after the stored usage line.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const { serverSinks } = require('../lib/ipc/server-sinks');
const { createIngest } = require('../lib/ipc/ingest');
const { createAudit } = require('../lib/audit');
const { tmpDir } = require('./ipc-stub');

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
  for (const wrapped of ['govRecord', 'govWrite', 'gateway.usage', 'usage.record', 'recorder'])
    assert.ok(!call.includes(wrapped), `the ingest gets ${wrapped}`);
  assert.match(call, /\bgovernance,/);
  assert.match(call, /mcpAudit: audit,/);
  assert.match(call, /\btraceStore,/);
});

test('usage: stored strictly with its mark, then the budget report once', (t) => {
  const dir = tmpDir(t, 'sinks');
  const reported = [];
  const sinks = serverSinks({
    usageFile: path.join(dir, 'gateway-usage.jsonl'),
    getPricing: () => ({}),
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

test('a write that fails is not marked: governance null, an unwritable usage log, no trace store', (t) => {
  const dir = tmpDir(t, 'sinks-fail');
  fs.mkdirSync(path.join(dir, 'gateway-usage.jsonl')); // a folder: the append fails
  const sinks = serverSinks({
    usageFile: path.join(dir, 'gateway-usage.jsonl'),
    getPricing: () => ({}),
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
