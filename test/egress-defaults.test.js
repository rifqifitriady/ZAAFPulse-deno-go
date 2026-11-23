// The egress defaults at the v2.0.0 cut-over (owner decision 2026-11-23, plan §11.8): with no setting of its own an
// install asks for `egress.allowList: "enforce"` and `egress.lockdown: true`. Only "report" reports and only `false`
// turns the lock-down off, so an explicit value keeps its meaning; the rollback (`"allowList": "report"`,
// `"lockdown": false`) gives the behaviour every install had before. A v1.1 config (no egress section, js mode) still
// asks for both, but neither has anything to act on: no gateway runs, every class is direct.
const test = require('node:test');
const assert = require('node:assert/strict');
const os = require('os');
const egress = require('../lib/egress');
const permissions = require('../lib/permissions');
const { configPush } = require('../lib/front/push');
const { createEgressStatus } = require('../lib/egress-status');
const { controls } = require('../lib/governance/controls');

const allGo = () => Object.fromEntries(egress.CLASSES.map((c) => [c, 'go']));
const passedGate = () => Object.fromEntries(egress.CLASSES.map((c) => [c, { passedAt: 1, since: 0 }]));
const env = { HOME: os.homedir() };
const netArg = (config, gate = passedGate(), e = env) =>
  permissions
    .selfCommand({
      script: 'bin/zaafpulse.js',
      set: 'server',
      dataDir: os.tmpdir(),
      runtime: 'deno',
      singleFile: false,
      approvals: {},
      config,
      gate,
      env: e,
    })
    .args.find((a) => a.startsWith('--allow-net='));

// A v1.1.x config.json as the v1 line writes it: no egress section, no gateway.engine.
const V11 = {
  gateway: {
    providers: [{ id: 'openai', preset: 'openai', baseUrl: 'https://api.openai.com/v1', apiKey: { ref: 'x' } }],
  },
  integrations: { dt: { type: 'dynatrace', baseUrl: 'https://abc12345.live.dynatrace.com' } },
  monthlyBudgetUSD: 50,
};

test('the defaults: enforce and lock-down asked when the config says nothing', () => {
  assert.deepEqual(egress.EGRESS_DEFAULTS, { allowList: 'enforce', lockdown: true });
  for (const cfg of [undefined, null, {}, { classes: {} }, { allowList: null, lockdown: null }]) {
    assert.equal(egress.allowListMode(cfg), 'enforce', JSON.stringify(cfg));
    assert.equal(egress.lockdownAsked(cfg), true, JSON.stringify(cfg));
  }
});

test('an explicit value keeps its meaning; only "report" reports and only false turns the lock-down off', () => {
  assert.equal(egress.allowListMode({ allowList: 'report' }), 'report');
  assert.equal(egress.allowListMode({ allowList: 'enforce' }), 'enforce');
  // A value that is not one of the two is the default (the safe side), as before a junk value was the old default.
  for (const v of ['Report', 'off', true, false, 1]) assert.equal(egress.allowListMode({ allowList: v }), 'enforce');
  assert.equal(egress.lockdownAsked({ lockdown: false }), false);
  assert.equal(egress.lockdownAsked({ lockdown: true }), true);
  for (const v of ['false', 0, 'no']) assert.equal(egress.lockdownAsked({ lockdown: v }), true, String(v));
});

test('the push to the gateway carries enforce by default and report when asked', () => {
  assert.equal(configPush({ gateway: {}, keys: [], pricing: {} }).egress.allowList, 'enforce');
  assert.equal(configPush({ gateway: {}, keys: [], pricing: {}, egress: {} }).egress.allowList, 'enforce');
  assert.equal(
    configPush({ gateway: {}, keys: [], pricing: {}, egress: { allowList: 'report' } }).egress.allowList,
    'report',
  );
});

test('a fresh config asks for the lock-down; it is in force only when its conditions hold', () => {
  const fresh = permissions.lockdownState({ config: {}, env, gate: {} });
  assert.equal(fresh.asked, true);
  assert.equal(fresh.inForce, false);
  assert.match(fresh.why, /not "front"/);
  assert.equal(netArg({}), undefined, 'no narrowed grant for a fresh install');
  // Front mode and every class on go with its gate met: in force with no egress.lockdown written at all.
  const ready = { gateway: { engine: 'front' }, egress: { classes: allGo() } };
  assert.equal(permissions.lockdownState({ config: ready, env, gate: passedGate() }).inForce, true);
  assert.equal(netArg(ready), '--allow-net=127.0.0.1,localhost,[::1]');
  // The environment fallback still wins over the default.
  const off = permissions.lockdownState({
    config: ready,
    env: { ...env, ZAAFPULSE_EGRESS_LOCKDOWN: '0' },
    gate: passedGate(),
  });
  assert.equal(off.inForce, false);
  assert.match(off.why, /ZAAFPULSE_EGRESS_LOCKDOWN=0/);
});

test('a v1.1-style config keeps working: js mode, nothing narrowed, every class direct', () => {
  const st = permissions.lockdownState({ config: V11, env, gate: {} });
  assert.deepEqual([st.asked, st.inForce], [true, false]);
  assert.match(st.why, /not "front"/);
  assert.equal(netArg(V11), undefined);
  const push = configPush({ gateway: V11.gateway, keys: [], pricing: {}, egress: V11.egress });
  assert.ok(Object.values(push.egress.classes).every((v) => v === 'direct'));
});

test('the rollback (report, lockdown false) restores the P5 behaviour', () => {
  const ready = { gateway: { engine: 'front' }, egress: { classes: allGo(), allowList: 'report', lockdown: false } };
  const st = permissions.lockdownState({ config: ready, env, gate: passedGate() });
  assert.deepEqual(st, { asked: false, inForce: false, why: 'egress.lockdown is false', quiet: [] });
  assert.equal(netArg(ready), undefined);
  assert.equal(configPush({ gateway: {}, keys: [], pricing: {}, egress: ready.egress }).egress.allowList, 'report');
});

test('GET /api/egress and the Compliance control show the default mode', () => {
  const read = (allowList) =>
    createEgressStatus({
      log: { tail: () => [], counts: () => ({}) },
      state: () => ({ mode: 'front', port: 1, engines: {}, notes: {}, allow: [], allowList, lockdown: {} }),
    })();
  assert.equal(read(undefined).allowList, 'enforce');
  assert.equal(read('report').allowList, 'report');
  const ev = (allowList) =>
    controls({ egress: { mode: 'front', port: 1, engines: { gateway: 'go' }, allowList, lockdown: {} } })
      .find((c) => c.id === 'network_egress')
      .evidence.join('\n');
  assert.match(ev('enforce'), /allow-list: enforce/);
  assert.match(ev('report'), /allow-list: report only/);
});

// A remote agent's URL is on the allow-list; with the allow-list enforcing by default a new agent must not wait for the
// minute loop: the gateway tells server.js at once (onAgentsChange), which pushes the new list.
test('saving, deleting or resetting an agent tells the server at once; a refused save does not', async (t) => {
  const fs = require('fs');
  const path = require('path');
  const { createGateway } = require('../lib/gateway');
  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), 'egress-agents-'));
  t.after(() => fs.rmSync(dataDir, { recursive: true, force: true }));
  let told = 0;
  const gw = createGateway({
    dataDir,
    getConfig: () => ({ gateway: {} }),
    writeGatewayConfig: () => {},
    secrets: { resolve: () => null },
    resolveSecret: () => undefined,
    getPricing: () => ({}),
    getIntegrations: () => ({ listTools: async () => [], callTool: async () => ({}) }),
    onAgentsChange: () => told++,
  });
  const r = await gw.admin('agents', 'POST', {
    action: 'save',
    id: 'far',
    name: 'Far',
    remoteUrl: 'https://a2a.example.com/a2a',
  });
  assert.ok(!r.errors, JSON.stringify(r));
  assert.equal(told, 1);
  const bad = await gw.admin('agents', 'POST', { action: 'save', id: 'bad', name: 'Bad', remoteUrl: 'ftp://x' });
  assert.ok(bad.errors);
  assert.equal(told, 1, 'a refused save changes nothing');
  await gw.admin('agents', 'POST', { action: 'delete', id: 'far' });
  assert.equal(told, 2);
});
