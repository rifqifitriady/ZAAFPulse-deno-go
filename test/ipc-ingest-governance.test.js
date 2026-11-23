// P7 review L2: a governance audit.event from the gateway is sealed into the HMAC chain only for an action the gateway
// is entitled to (lib/ipc/ingest.js GATEWAY_GOVERNANCE_ACTIONS, none today), and then always with actor
// { via: "gateway" }: the gateway cannot write an entry in a dashboard user's or a key's name. Anything else is dropped
// (logged once, counted, acknowledged: a resend would be dropped again). The stub's --emit rounds keep their entries
// as sent through the explicit test-only option testRawGovernance.
const test = require('node:test');
const assert = require('node:assert/strict');
const { createIngest, GATEWAY_GOVERNANCE_ACTIONS } = require('../lib/ipc/ingest');

const S = 'a'.repeat(32);
const gov = (seq, event) => ({ type: 'audit.event', seq, body: { log: 'governance', event } });
const forged = {
  action: 'keys.revoke',
  target: { type: 'key', id: 'k1', name: 'prod' },
  outcome: 'ok',
  actor: { via: 'dashboard', user: 'admin', keyId: 'k-admin', rule: 'r1' },
};

function sinks() {
  const calls = { governance: [], logs: [] };
  return {
    calls,
    recordUsage: () => {},
    recordGovernance: (e) => calls.governance.push(e),
    recordMcp: () => {},
    writeSpans: () => {},
    log: (l) => calls.logs.push(l),
  };
}

test('no gateway governance action is allowed today', () => {
  assert.ok(GATEWAY_GOVERNANCE_ACTIONS instanceof Set);
  assert.equal(GATEWAY_GOVERNANCE_ACTIONS.size, 0);
});

test('a governance event from the gateway with an action it is not entitled to is dropped, logged once, acknowledged', () => {
  const s = sinks();
  const ingest = createIngest(s);
  assert.equal(ingest.handle(gov(1, forged), S), true, 'handled: marked, so a resend is a duplicate');
  assert.equal(ingest.handle(gov(2, { ...forged, action: 'budget.update' }), S), true);
  assert.equal(s.calls.governance.length, 0, 'nothing reaches the chain');
  assert.equal(ingest.mark(S), 2);
  assert.equal(ingest.governanceRefused(), 2);
  assert.equal(s.calls.logs.length, 1, 'logged once');
  assert.match(s.calls.logs[0], /governance/);
  assert.doesNotMatch(s.calls.logs[0], /admin/, 'the claimed actor is not echoed');
  assert.equal(ingest.handle(gov(1, forged), S), false, 'a resend is a duplicate');
});

test('an allowed action is written with actor { via: "gateway" } whatever actor the gateway sent', () => {
  const s = sinks();
  const ingest = createIngest({ ...s, gatewayGovernance: new Set(['keys.revoke']) });
  ingest.handle(gov(1, forged), S);
  ingest.handle(gov(2, { action: 'keys.revoke' }), S);
  assert.equal(s.calls.governance.length, 2);
  assert.deepEqual(s.calls.governance[0], {
    action: 'keys.revoke',
    target: { type: 'key', id: 'k1', name: 'prod' },
    outcome: 'ok',
    actor: { via: 'gateway' },
    ipc: { stream: S, seq: 1 },
  });
  assert.deepEqual(s.calls.governance[1].actor, { via: 'gateway' });
  assert.equal(ingest.governanceRefused(), 0);
});

test('testRawGovernance (tests only: the stub --emit rounds) keeps the entry as the gateway sent it', () => {
  const s = sinks();
  const ingest = createIngest({ ...s, testRawGovernance: true });
  ingest.handle(gov(1, forged), S);
  assert.deepEqual(s.calls.governance[0], { ...forged, ipc: { stream: S, seq: 1 } });
});

test('the egress.refused entry the control plane builds itself is still written', () => {
  const s = sinks();
  const ingest = createIngest({ ...s, recordEgress: () => {} });
  const ev = {
    ts: 1,
    purpose: 'agents:x',
    method: 'GET',
    scheme: 'http',
    host: '169.254.169.254',
    port: 80,
    outcome: 'refused',
    error: 'EGRESS_PRIVATE',
    bytesOut: 0,
    bytesIn: 0,
    ms: 0,
    via: 'proxy',
  };
  ingest.handle({ type: 'egress.event', seq: 1, body: ev }, S);
  assert.deepEqual(s.calls.governance, [
    {
      action: 'egress.refused',
      target: { type: 'host', name: '169.254.169.254:80' },
      outcome: 'refused',
      actor: { via: 'gateway' },
    },
  ]);
});
