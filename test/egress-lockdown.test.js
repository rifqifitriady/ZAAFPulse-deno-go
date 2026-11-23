// The lock-down proof (P5 spec Decisions 14 and 17, metric M10): a probe launched with exactly the server's
// locked-down `deno run` line (lib/permissions.js selfCommand with egress.lockdown in force) cannot open any direct
// outbound connection: fetch and net.connect to outside hosts throw Deno's NotCapable before a packet is sent (the
// hosts are refused by the permission check, never resolved or dialled), while a loopback call still works and the
// same kind of call leaves through the real gateway's egress endpoint. The launch line itself: no --allow-net unless
// every condition holds, and ZAAFPULSE_EGRESS_LOCKDOWN=0 wins. Needs deno (and Go for the gateway); skipped without.
const test = require('node:test');
const assert = require('node:assert/strict');
const crypto = require('crypto');
const fs = require('fs');
const http = require('http');
const os = require('os');
const path = require('path');
const { spawn, spawnSync } = require('child_process');
const permissions = require('../lib/permissions');
const egress = require('../lib/egress');
const { createSupervisor } = require('../lib/ipc/supervisor');
const { createIngest } = require('../lib/ipc/ingest');
const { openLocalStores } = require('../lib/ipc/local');
const { recoverMarks } = require('../lib/ipc/marks');
const { stubBin, initial, tmpDir, waitFor } = require('./ipc-stub');

const DENO = process.versions.deno ? process.execPath : 'deno';
const hasDeno = !!process.versions.deno || spawnSync('deno', ['--version'], { stdio: 'ignore' }).status === 0;
const skip = !hasDeno && 'deno is not installed';

const allGo = () => Object.fromEntries(egress.CLASSES.map((c) => [c, 'go']));
const lockedConfig = (extra = {}) => ({
  gateway: { engine: 'front' },
  egress: { lockdown: true, classes: allGo(), ...extra },
});
const passedGate = () => Object.fromEntries(egress.CLASSES.map((c) => [c, { passedAt: 1, since: 0 }]));
const netArg = (args) => args.find((a) => a.startsWith('--allow-net='));
const cmd = (opts) =>
  permissions.selfCommand({
    script: 'bin/zaafpulse.js',
    set: 'server',
    dataDir: os.tmpdir(),
    runtime: 'deno',
    singleFile: false,
    approvals: {},
    ...opts,
  }).args;

test('the launch line narrows net only when the lock-down is in force', () => {
  const env = { HOME: os.homedir() };
  assert.equal(
    netArg(cmd({ config: lockedConfig(), gate: passedGate(), env })),
    '--allow-net=127.0.0.1,localhost,[::1]',
  );
  // The listen host joins when it is not loopback.
  assert.equal(
    netArg(cmd({ config: lockedConfig(), gate: passedGate(), env: { ...env, ZAAFPULSE_HOST: '0.0.0.0' } })),
    '--allow-net=127.0.0.1,localhost,[::1],0.0.0.0',
  );
  // Each missing condition keeps the set's own net entry, and says why.
  const cases = [
    [{ config: {}, gate: passedGate(), env }, /egress.lockdown is off/],
    [{ config: lockedConfig(), gate: passedGate(), env: { ...env, ZAAFPULSE_EGRESS_LOCKDOWN: '0' } }, /=0/],
    [{ config: { ...lockedConfig(), gateway: { engine: 'js' } }, gate: passedGate(), env }, /not "front"/],
    [
      { config: lockedConfig({ classes: { ...allGo(), otel: 'check' } }), gate: passedGate(), env },
      /not asked for go: otel/,
    ],
    [{ config: lockedConfig(), gate: { ...passedGate(), brain: { passedAt: null } }, env }, /gate: brain/],
  ];
  for (const [o, why] of cases) {
    assert.equal(netArg(cmd(o)), undefined);
    assert.match(permissions.lockdownState(o).why, why);
  }
  // A lifted gate rule (0 days, 0 checks) needs no pass; other sets are never narrowed.
  assert.ok(netArg(cmd({ config: lockedConfig({ gateDays: 0, gateMinChecks: 0 }), gate: {}, env })));
  assert.equal(netScopeOf('cli', lockedConfig(), passedGate()), null);
  assert.equal(netScopeOf('mcp-stdio', lockedConfig(), passedGate()), null);
  // A listen host that is a name (it would grant every port of whatever it resolves to; tcsh exports HOST as the
  // machine's name) keeps the set's entry and says why; an IPv6 address is bracketed.
  for (const name of ['my-mac.local', 'a,example.com']) {
    const o = { config: lockedConfig(), gate: passedGate(), env: { ...env, HOST: name } };
    assert.equal(netArg(cmd(o)), undefined);
    assert.match(permissions.lockdownState(o).why, /is a name: the lock-down needs an address/);
  }
  assert.equal(
    netArg(cmd({ config: lockedConfig(), gate: passedGate(), env: { ...env, ZAAFPULSE_HOST: '::' } })),
    '--allow-net=127.0.0.1,localhost,[::1],[::]',
  );
});
function netScopeOf(set, config, gate) {
  return permissions.netScope({ set, config, gate, env: {} });
}

test('a locked-down launch cannot connect out directly; loopback and the gateway still work', { skip }, async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  const fake = http.createServer((req, res) => res.end(`fake ${req.url}`));
  await new Promise((r) => fake.listen(0, '127.0.0.1', r));
  // node:test runs after-hooks in the order they were added: the gateway must stop before tmpDir removes the outbox
  // whose lock it holds (Windows refuses to delete an open file), so its stop goes through tmpDir's own list.
  const stops = [];
  const root = tmpDir(t, 'egress-lockdown', stops);
  const stores = openLocalStores({ root });
  const ingest = createIngest({ ...stores.sinks, marks: recoverMarks(stores.files) });
  const frontToken = crypto.randomBytes(32).toString('hex');
  const first = initial();
  let port = null;
  const sup = createSupervisor({
    bin,
    args: ['--outbox', stores.files.outboxDir],
    cpVersion: 'egress-lockdown',
    sources: {
      config: () => ({ ...first.config, egress: { allow: [], allowList: 'report' } }),
      secrets: () => ({ providers: [], mask: [], frontToken }),
      snapshot: () => ({ budgets: [], keyCenter: {} }),
    },
    ingest,
    log: () => {},
    onEgress: (p) => (port = p),
  });
  stops.push(async () => {
    await sup.stop(2000);
    fake.closeAllConnections();
    await new Promise((r) => fake.close(r));
  });
  sup.start();
  await waitFor(() => port !== null, 20000, 'the gateway ready with its egress port');

  const home = fs.mkdtempSync(path.join(os.tmpdir(), 'zp-lock-home-'));
  const data = path.join(home, 'data');
  fs.mkdirSync(data, { recursive: true });
  fs.writeFileSync(path.join(data, 'config.json'), JSON.stringify(lockedConfig()));
  fs.writeFileSync(path.join(data, 'egress-gate.json'), JSON.stringify(passedGate()));
  const env = { PATH: process.env.PATH, HOME: home, USERPROFILE: home, ZAAFPULSE_DATA: data };
  const { args } = permissions.selfCommand({
    script: path.join('test', 'fixtures', 'egress', 'probe.js'),
    set: 'server',
    dataDir: data,
    env,
    runtime: 'deno',
    singleFile: false,
  });
  assert.equal(netArg(args), '--allow-net=127.0.0.1,localhost,[::1]', 'the probe runs under the locked-down line');
  const out = await new Promise((resolve, reject) => {
    const child = spawn(DENO, args, {
      env: {
        ...env,
        FAKE_PORT: String(fake.address().port),
        EGRESS_PORT: String(port),
        EGRESS_TOKEN: egress.deriveToken(frontToken),
      },
    });
    let text = '';
    let errText = '';
    child.stdout.on('data', (b) => (text += b));
    child.stderr.on('data', (b) => (errText += b));
    child.on('error', reject);
    child.on('close', () => {
      try {
        resolve(JSON.parse(text.trim().split('\n').pop()));
      } catch {
        reject(new Error(`no answer from the probe: ${errText}`));
      }
    });
  });
  assert.equal(out.crashed, undefined, JSON.stringify(out.crashed));
  for (const [target, r] of [...Object.entries(out.fetch), ...Object.entries(out.tcp)]) {
    // net.connect to a name asks the resolver first, and Deno refuses that lookup under the same permission
    // (getaddrinfo EPERM): no query leaves either. Every other attempt is refused at the connect itself.
    const refusedLookup = r.code === 'EPERM' && /^getaddrinfo EPERM /.test(r.message);
    assert.ok(
      refusedLookup || (r.name === 'NotCapable' && /Requires net access/.test(r.message)),
      `${target}: refused by Deno's net permission (${r.name}: ${r.message})`,
    );
  }
  assert.equal(out.tcp['example.com:443'].code, 'EPERM', 'the name is never resolved');
  assert.deepEqual(out.loopback, { status: 200, body: 'fake /direct' });
  assert.equal(out.unavailable.code, 'EGRESS_UNAVAILABLE');
  assert.deepEqual(out.via, { status: 200, body: 'fake /via' });
  // The gateway recorded the call it made for the probe.
  await waitFor(
    () =>
      fs.existsSync(stores.files.egressFile) &&
      fs.readFileSync(stores.files.egressFile, 'utf8').includes('"purpose":"connectors:probe"'),
    10000,
    'the egress record of the probe call',
  );
});

test("a CLI --allow-net never lifts the set's deny of cloud metadata", { skip }, () => {
  const r = spawnSync(
    DENO,
    [
      'run',
      '-q',
      '--no-prompt',
      `--config=${permissions.CONFIG}`,
      '-P=server',
      '--allow-net=169.254.169.254,127.0.0.1',
      path.join(__dirname, 'fixtures', 'egress', 'metadata.js'),
    ],
    { encoding: 'utf8', env: { PATH: process.env.PATH, HOME: os.tmpdir() } },
  );
  const out = JSON.parse(String(r.stdout).trim().split('\n').pop());
  assert.equal(out.name, 'NotCapable', out.message);
  assert.match(out.message, /Requires net access to "169\.254\.169\.254/);
});
