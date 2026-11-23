// Every fetch caller of the inventory (P5 spec E1–E10) takes its default from lib/egress.js fetchFor(<class>): with a
// fake egress endpoint configured (written here in JS, answering as the Go endpoint would) and the class on 'go', a
// real call through the module's own default fetch reaches the endpoint with the right X-ZP-Egress-Purpose; on
// 'direct' it does not, and goes to the global fetch (stubbed here). No test reaches an outside host: the outside names
// below only ever reach the fake endpoint or the stub.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const http = require('http');
const os = require('os');
const path = require('path');
const egress = require('../lib/egress');

const TOKEN = egress.deriveToken('front-token');

// The fake endpoint: every request kept; answers 200 JSON `{}` for the target in X-ZP-Egress-URL.
async function fakeEndpoint(t) {
  const seen = [];
  const server = http.createServer((req, res) => {
    req.resume();
    req.on('end', () => {
      seen.push({ url: req.headers['x-zp-egress-url'], purpose: req.headers['x-zp-egress-purpose'] });
      res.writeHead(200, { 'content-type': 'application/json', 'x-zp-egress-url': req.headers['x-zp-egress-url'] });
      res.end('{}');
    });
  });
  await new Promise((r) => server.listen(0, '127.0.0.1', r));
  t.after(async () => {
    egress.configure(null);
    server.closeAllConnections?.();
    await new Promise((r) => server.close(r));
  });
  return { seen, port: server.address().port };
}

// globalThis.fetch replaced for the test (the direct route); calls are kept.
function stubFetch(t) {
  const calls = [];
  const real = globalThis.fetch;
  globalThis.fetch = async (input) => {
    calls.push(String(input instanceof Request ? input.url : input));
    return new Response('{}', { status: 200, headers: { 'content-type': 'application/json' } });
  };
  t.after(() => {
    globalThis.fetch = real;
  });
  return calls;
}

const secrets = { resolve: (v) => v, mask: (s) => s };
const tmp = () => fs.mkdtempSync(path.join(os.tmpdir(), 'zp-egress-classes-'));

// Each caller: its class, the purpose it sends, and a call through the module's default fetch (errors after the call
// are the module's own business: the fake answers `{}`).
const CALLERS = [
  {
    name: 'gateway provider call (providers.js request)',
    cls: 'gateway',
    purpose: 'gateway',
    run: async () => {
      const { createProviders } = require('../lib/gateway/providers');
      const p = { id: 'far', baseUrl: 'https://api.example.test/v1', apiKey: 'k' };
      const providers = createProviders({ getConfig: () => ({ gateway: { providers: [p] } }), secrets });
      await providers.request(p, '/models', { method: 'GET' });
    },
  },
  {
    name: 'gateway token refresh (providers.js refreshToken)',
    cls: 'gateway',
    purpose: 'gateway',
    url: 'https://login.example.test/token',
    run: async () => {
      const { createProviders } = require('../lib/gateway/providers');
      const p = {
        id: 'tok',
        baseUrl: 'https://api.example.test/v1',
        apiKey: 'client-secret',
        auth: {
          type: 'oauth2',
          grant: 'client_credentials',
          tokenUrl: 'https://login.example.test/token',
          clientId: 'c',
        },
      };
      const providers = createProviders({ getConfig: () => ({ gateway: { providers: [p] } }), secrets });
      await providers.test(p);
    },
  },
  {
    name: 'connector client (connectors/base.js makeClient), detail = type',
    cls: 'connectors',
    purpose: 'connectors:dynatrace',
    run: async () => {
      const dynatrace = require('../lib/connectors/dynatrace');
      const c = dynatrace.create('dt', { baseUrl: 'https://dt.example.test', token: 'dt0c01.x' }, { secrets });
      const tool = c.tools.find((x) => x.name === 'list_problems');
      await tool.run({});
    },
  },
  {
    name: 'MCP upstream client (mcp/client.js)',
    cls: 'mcp',
    purpose: 'mcp',
    run: async () => {
      const { createClient } = require('../lib/mcp/client');
      await createClient({ url: 'https://mcp.example.test/mcp' }).initialize();
    },
  },
  {
    name: 'Heybrain MCP listing (brain/apps.js mcpList)',
    cls: 'mcp',
    purpose: 'mcp:heybrain',
    run: async () => {
      const apps = require('../lib/brain/apps');
      await apps.mcpList({ url: 'https://heybrain.example.test/mcp', secret: 's' });
    },
  },
  {
    name: 'OmniRoute (omniroute.js)',
    cls: 'routers',
    purpose: 'routers:omniroute',
    run: async () => {
      const { createOmniRoute } = require('../lib/omniroute');
      const cfg = { integrations: { or: { type: 'router', baseUrl: 'https://omni.example.test' } } };
      await createOmniRoute(() => cfg, { secrets }).utilization();
    },
  },
  {
    name: 'OpenRouter page (openrouter.js)',
    cls: 'routers',
    purpose: 'routers:openrouter',
    url: 'https://openrouter.ai/api/v1/models',
    run: async () => {
      const { createOpenRouter } = require('../lib/openrouter');
      await createOpenRouter({ cacheDir: tmp() }).models();
    },
  },
  {
    name: 'price sync (price-sync.js)',
    cls: 'pricing',
    purpose: 'pricing',
    url: 'https://openrouter.ai/api/v1/models',
    run: async () => {
      const { createPriceSync } = require('../lib/price-sync');
      await createPriceSync({ cacheFile: path.join(tmp(), 'prices.json') }).refresh();
    },
  },
  {
    name: 'judge model list (evals/models.js)',
    cls: 'evals',
    purpose: 'evals',
    url: 'http://ollama.example.test:11434/api/tags',
    run: async () => {
      const { createModels } = require('../lib/evals/models');
      const m = createModels({
        settings: () => ({ model: '', provider: 'ollama' }),
        gateway: { judge: { providers: () => [] } },
        ollamaUrl: 'http://ollama.example.test:11434',
        lmstudioUrl: 'http://lmstudio.example.test:1234',
      });
      await m.resolve();
    },
  },
  {
    name: 'remote A2A agent (gateway/orchestrator.js)',
    cls: 'agents',
    purpose: 'agents',
    run: async () => {
      const { createOrchestrator } = require('../lib/gateway/orchestrator');
      const orch = createOrchestrator({
        agents: { enabled: () => [], get: () => null },
        memory: { recall: () => [] },
        integrations: null,
        llm: async () => ({}),
        getDefaults: () => ({ model: 'x' }),
      });
      await orch.runAgent({ id: 'far', name: 'Far', remoteUrl: 'https://agent.example.test/a2a', tools: [] }, 'hi', {});
    },
  },
  {
    name: 'monitor webhook (monitors/channels.js)',
    cls: 'notify',
    purpose: 'notify',
    run: async () => {
      const { createChannels } = require('../lib/monitors/channels');
      const ch = createChannels({
        secrets,
        settings: () => ({ desktop: false, webhook: { url: 'https://hooks.example.test/x' } }),
      });
      await ch.test('webhook');
    },
  },
  {
    name: 'second-brain app (brain/apps.js call)',
    cls: 'brain',
    purpose: 'brain',
    run: async () => {
      const apps = require('../lib/brain/apps');
      await apps.notion({ secret: 'secret_x' });
    },
  },
  {
    name: 'vector store (brain/vectors.js call)',
    cls: 'brain',
    purpose: 'brain',
    run: async () => {
      const { CLIENTS } = require('../lib/brain/vectors');
      await CLIENTS.pinecone({ secret: 'k' });
    },
  },
];

const quiet = (p) => p.catch(() => {}); // the module's own answer to `{}` is not what is tested

for (const c of CALLERS) {
  test(`${c.name}: on go it reaches the endpoint as ${c.purpose}`, async (t) => {
    const ep = await fakeEndpoint(t);
    const direct = stubFetch(t);
    egress.configure({ port: ep.port, token: TOKEN, engines: { [c.cls]: 'go' }, lockdown: false });
    await quiet(c.run());
    assert.ok(ep.seen.length >= 1, 'the call reached the endpoint');
    assert.deepEqual(direct, [], 'nothing went direct');
    for (const s of ep.seen) assert.equal(s.purpose, c.purpose);
    if (c.url) assert.equal(ep.seen[0].url, c.url);
  });

  test(`${c.name}: on direct it does not reach the endpoint`, async (t) => {
    const ep = await fakeEndpoint(t);
    const direct = stubFetch(t);
    // Every other class on go: only this class's own switch decides.
    const engines = Object.fromEntries(egress.CLASSES.map((k) => [k, k === c.cls ? 'direct' : 'go']));
    egress.configure({ port: ep.port, token: TOKEN, engines, lockdown: false });
    await quiet(c.run());
    assert.equal(ep.seen.length, 0);
    assert.ok(direct.length >= 1, 'the call went to the global fetch');
    if (c.url) assert.equal(direct[0], c.url);
  });
}

test('a configure after the module was created applies to its next call', async (t) => {
  const ep = await fakeEndpoint(t);
  const direct = stubFetch(t);
  const { createPriceSync } = require('../lib/price-sync');
  const sync = createPriceSync({ cacheFile: path.join(tmp(), 'prices.json') });
  egress.configure(null);
  await quiet(sync.refresh());
  assert.equal(direct.length, 1);
  egress.configure({ port: ep.port, token: TOKEN, engines: { pricing: 'go' }, lockdown: false });
  await quiet(sync.refresh());
  assert.equal(direct.length, 1);
  assert.equal(ep.seen.length, 1);
});

test('an injected fetchImpl still wins over the egress client', async (t) => {
  const ep = await fakeEndpoint(t);
  stubFetch(t);
  egress.configure({ port: ep.port, token: TOKEN, engines: { pricing: 'go' }, lockdown: false });
  const mine = [];
  const { createPriceSync } = require('../lib/price-sync');
  await quiet(
    createPriceSync({
      cacheFile: path.join(tmp(), 'prices.json'),
      fetchImpl: async (u) => (mine.push(u), new Response('{}')),
    }).refresh(),
  );
  assert.equal(mine.length, 1);
  assert.equal(ep.seen.length, 0);
});
