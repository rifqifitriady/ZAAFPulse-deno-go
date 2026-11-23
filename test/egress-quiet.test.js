// The lock-down's gate condition for a class with nothing configured to call (P7 defaults design,
// the p7-defaults design, Decision B): such a class counts as clear, because it has
// nothing to send; a class with a configured destination still needs its own pass. Presence decides, never a resolved
// value, and anything unsure counts as configured. The exemption is computed each time, never written to the gate file.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const os = require('os');
const path = require('path');
const egress = require('../lib/egress');
const permissions = require('../lib/permissions');
const { quietClasses } = require('../lib/egress-allow');
const { lockdownNow } = require('../lib/egress-status');
const { controls } = require('../lib/governance/controls');

const env = { HOME: os.homedir() };
const allGo = () => Object.fromEntries(egress.CLASSES.map((c) => [c, 'go']));
const pass = (classes) => Object.fromEntries(classes.map((c) => [c, { passedAt: 1, since: 0 }]));
const front = (extra = {}) => ({ gateway: { engine: 'front' }, egress: { classes: allGo() }, ...extra });
// The classes that can never be quiet with price sync on (the default).
const FLOOR = ['pricing', 'public', 'routers'];

test('an empty install: only routers, pricing and public have something to send', () => {
  const quiet = quietClasses({}, { env: {}, agents: [] });
  assert.deepEqual(
    quiet,
    egress.CLASSES.filter((c) => !FLOOR.includes(c)),
  );
  // Price sync off: pricing is quiet too. routers (the OpenRouter page) and public (URL ingest) never are.
  assert.ok(quietClasses({ pricingSync: { enabled: false } }, { env: {}, agents: [] }).includes('pricing'));
});

test('each class is configured by a non-loopback destination, and only by that', () => {
  const cases = [
    ['gateway', { gateway: { providers: [{ id: 'o', preset: 'openai', baseUrl: 'https://api.openai.com/v1' }] } }],
    // The preset's base URL when the entry has none.
    ['gateway', { gateway: { providers: [{ id: 'g', preset: 'groq' }] } }],
    // A loopback model server with a remote token endpoint.
    [
      'gateway',
      {
        gateway: {
          providers: [{ id: 'l', preset: 'custom', baseUrl: 'http://127.0.0.1:9/v1', auth: { type: 'google-sa' } }],
        },
      },
    ],
    ['connectors', { integrations: { dt: { type: 'dynatrace', baseUrl: 'https://abc.live.dynatrace.com' } } }],
    ['connectors', { integrations: { ax: { type: 'arize', flavor: 'ax' } } }],
    ['mcp', { integrations: { up: { type: 'mcp', url: 'https://mcp.example.com/mcp' } } }],
    ['mcp', { brain: { sources: [{ id: 'h', type: 'heybrain', url: 'https://heybrain.io/mcp/x' }] } }],
    ['notify', { monitors: { channels: { webhook: { url: { keychain: 'acc.monitors-webhook-url' } } } } }],
    ['brain', { brain: { sources: [{ id: 'n', type: 'notion' }] } }],
    ['brain', { brain: { sources: [{ id: 'q', type: 'qdrant', url: 'https://q.example.com:6333' }] } }],
    ['brain', { brain: { sources: [{ id: 'pg', type: 'pgvector', secret: { keychain: 'acc.pg' } }] } }],
    ['otel', { otel: { enabled: true, endpoint: 'https://otlp.example.com:4318' } }],
    ['runtimes', { localRuntimes: [{ health: 'http://10.0.0.5:11434/api/version' }] }],
  ];
  for (const [cls, cfg] of cases) {
    const quiet = quietClasses(cfg, { env: {}, agents: [] });
    assert.ok(!quiet.includes(cls), `${cls} is configured by ${JSON.stringify(cfg)}`);
  }
  assert.ok(!quietClasses({}, { env: { ZAAFPULSE_OLLAMA_URL: 'http://gpu.lan:11434' }, agents: [] }).includes('evals'));
  assert.ok(
    !quietClasses({}, { env: { OTEL_EXPORTER_OTLP_ENDPOINT: 'https://otlp.example.com' }, agents: [] }).includes(
      'otel',
    ),
  );
  assert.ok(
    !quietClasses({}, { env: {}, agents: [{ id: 'far', remoteUrl: 'https://a2a.example.com' }] }).includes('agents'),
  );
});

test('loopback, disabled and unparsable destinations leave a class quiet; unsure counts as configured', () => {
  const cfg = {
    gateway: {
      providers: [
        { id: 'local', preset: 'ollama', baseUrl: 'http://127.0.0.1:11434/v1' },
        { id: 'bad', preset: 'custom', baseUrl: 'not a url' },
      ],
    },
    integrations: {
      phoenix: { type: 'arize' }, // localhost default
      omni: { type: 'router' }, // localhost default
      gone: { type: 'http', baseUrl: 'https://off.example.com', enabled: false },
    },
    brain: {
      sources: [
        { id: 'w', type: 'weaviate', url: 'http://localhost:8080' },
        { id: 'o', type: 'obsidian' },
      ],
    },
    otel: { enabled: false, endpoint: 'https://otlp.example.com' },
    localRuntimes: [{ health: 'http://127.0.0.1:8080/' }],
  };
  const quiet = quietClasses(cfg, {
    env: { ZAAFPULSE_OLLAMA_URL: 'http://localhost:11434' },
    agents: [
      { id: 'near', remoteUrl: 'http://127.0.0.1:9000' },
      { id: 'off', remoteUrl: 'http://localhost:9001', enabled: false },
    ],
  });
  for (const c of ['gateway', 'connectors', 'mcp', 'evals', 'agents', 'notify', 'brain', 'otel', 'runtimes'])
    assert.ok(quiet.includes(c), c);
  // A disabled provider or agent with a remote URL is on the allow-list, so it counts as configured.
  const off = {
    gateway: { providers: [{ id: 'o', preset: 'custom', baseUrl: 'https://x.example.com', enabled: false }] },
  };
  assert.ok(!quietClasses(off, { env: {}, agents: [] }).includes('gateway'));
  assert.ok(
    !quietClasses({}, { env: {}, agents: [{ id: 'a', enabled: false, remoteUrl: 'https://a.example.com' }] }).includes(
      'agents',
    ),
  );
  // agents.json not readable (undefined): agents counts as configured.
  assert.ok(!quietClasses({}, { env: {} }).includes('agents'));
  assert.ok(!quietClasses({}, { env: {}, agents: undefined }).includes('agents'));
});

test('the lock-down: quiet classes need no pass; a configured class without one still blocks it', () => {
  const st = (config, gate, agents = []) => permissions.lockdownState({ config, env, gate, agents });
  // An empty install with the floor classes passed: in force, and the quiet classes are named.
  const empty = st(front(), pass(FLOOR));
  assert.equal(empty.inForce, true, empty.why);
  assert.deepEqual(
    empty.quiet,
    egress.CLASSES.filter((c) => !FLOOR.includes(c)),
  );
  // A remote agent is configured: agents needs its own pass.
  const withAgent = st(front(), pass(FLOOR), [{ id: 'far', remoteUrl: 'https://a2a.example.com' }]);
  assert.equal(withAgent.inForce, false);
  assert.match(withAgent.why, /classes that have not met their gate: agents$/);
  assert.equal(
    st(front(), pass([...FLOOR, 'agents']), [{ id: 'far', remoteUrl: 'https://a2a.example.com' }]).inForce,
    true,
  );
  // A configured class with no evidence blocks it even when every other class is quiet or passed.
  const otel = front({ otel: { enabled: true, endpoint: 'https://otlp.example.com' } });
  assert.match(st(otel, pass(FLOOR)).why, /gate: otel$/);
  // agents.json unreadable: agents is not exempt.
  assert.match(permissions.lockdownState({ config: front(), env, gate: pass(FLOOR) }).why, /gate: agents$/);
  // Still asked for go: a quiet class left on direct keeps the lock-down off.
  const direct = front();
  direct.egress.classes.agents = 'direct';
  assert.match(st(direct, pass(FLOOR)).why, /not asked for go: agents/);
  // The floor classes are never exempt.
  assert.match(st(front(), pass(['pricing', 'public'])).why, /gate: routers$/);
});

test('the exemption is shown on GET /api/egress and in the Compliance evidence', () => {
  const wanted = permissions.lockdownState({ config: front(), env, gate: pass(FLOOR), agents: [] });
  const now = lockdownNow(wanted, { runtime: 'deno', singleFile: false, grants: { net: 'scoped' } });
  assert.equal(now.inForce, true);
  assert.deepEqual(now.quiet, wanted.quiet);
  const single = lockdownNow(wanted, { runtime: 'deno', singleFile: true, grants: { net: 'all but denied' } });
  assert.deepEqual(single.quiet, wanted.quiet);
  const ev = controls({
    egress: {
      mode: 'front',
      port: 1,
      engines: allGo(),
      allowList: 'enforce',
      lockdown: now,
      netGrant: 'scoped',
    },
  })
    .find((c) => c.id === 'network_egress')
    .evidence.join('\n');
  assert.match(ev, /counted clear with nothing configured to call: gateway, connectors/);
});

test('the launch line reads the gate and agents.json from .data/', (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'egress-quiet-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  fs.mkdirSync(path.join(dir, '.data'));
  fs.writeFileSync(path.join(dir, 'config.json'), JSON.stringify(front()));
  const net = () =>
    permissions
      .selfCommand({
        script: 'bin/zaafpulse.js',
        set: 'server',
        dataDir: dir,
        env,
        approvals: {},
        runtime: 'deno',
        singleFile: false,
      })
      .args.find((a) => a.startsWith('--allow-net='));
  assert.equal(net(), undefined, 'no gate file: nothing passed');
  fs.writeFileSync(path.join(dir, '.data', 'egress-gate.json'), JSON.stringify(pass(FLOOR)));
  // No agents.json yet (no agent was ever saved): no remote agent, so agents is quiet.
  assert.equal(net(), '--allow-net=127.0.0.1,localhost,[::1]');
  const agents = path.join(dir, '.data', 'agents.json');
  fs.writeFileSync(agents, JSON.stringify({ agents: [{ id: 'far', remoteUrl: 'https://a2a.example.com' }] }));
  assert.equal(net(), undefined, 'a remote agent needs its own pass');
  fs.writeFileSync(agents, '{ not json');
  assert.equal(net(), undefined, 'an unreadable agents.json counts as configured');
  fs.writeFileSync(agents, JSON.stringify({ agents: [{ id: 'local', systemPrompt: 'x' }] }));
  assert.equal(net(), '--allow-net=127.0.0.1,localhost,[::1]');
});
