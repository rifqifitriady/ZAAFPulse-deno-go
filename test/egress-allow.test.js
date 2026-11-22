// The egress allow-list from config (P5 spec Decision 13, lib/egress-allow.js): every source in one config gives the
// expected sorted, unique list with exact ports; an empty config gives only openrouter.ai (always reachable); loopback
// hosts and URLs that do not parse add nothing; the public class adds nothing.
const test = require('node:test');
const assert = require('node:assert/strict');
const { allowList, ruleOf } = require('../lib/egress-allow');

const EVERY = {
  gateway: {
    providers: [
      { id: 'openai', preset: 'openai', baseUrl: 'https://api.openai.com/v1' },
      { id: 'groq', preset: 'groq' }, // no baseUrl: the preset's
      {
        id: 'az',
        preset: 'azure-openai',
        baseUrl: 'https://my.openai.azure.com/openai/v1',
        auth: { type: 'oauth2', tokenUrl: 'https://login.microsoftonline.com/t1/oauth2/v2.0/token' },
      },
      { id: 'vx', preset: 'vertex', baseUrl: 'https://aiplatform.googleapis.com/v1', auth: { type: 'google-sa' } },
      { id: 'off', preset: 'custom', baseUrl: 'https://disabled.example.com', enabled: false },
      { id: 'local', preset: 'ollama', baseUrl: 'http://127.0.0.1:11434/v1' },
      { id: 'bad', preset: 'custom', baseUrl: 'not a url' },
    ],
  },
  integrations: {
    dt: { type: 'dynatrace', baseUrl: 'https://ABC.live.dynatrace.com', platformUrl: 'https://abc.apps.dynatrace.com' },
    ax: { type: 'arize', flavor: 'ax' },
    phoenix: { type: 'arize' }, // localhost default: left out
    n8n: { type: 'n8n', baseUrl: 'https://n8n.example.com:5678', webhooks: { a: 'https://hooks.example.com/w/a' } },
    omni: { type: 'router', baseUrl: 'http://router.lan:20128' },
    api: { type: 'http', baseUrl: 'http://api.example.com' },
    or: { type: 'openrouter' },
    up: { type: 'mcp', url: 'https://mcp.example.com/mcp' },
    gone: { type: 'http', baseUrl: 'https://off.example.com', enabled: false },
  },
  brain: {
    sources: [
      { id: 'n', type: 'notion' },
      { id: 'r', type: 'roam' },
      { id: 'm', type: 'mem' },
      { id: 'f', type: 'reflect' },
      { id: 'h', type: 'heybrain', url: 'https://heybrain.io/mcp/x' },
      { id: 'p', type: 'pinecone' },
      { id: 'q', type: 'qdrant', url: 'https://q.example.com:6333' },
      { id: 'pg', type: 'pgvector', secret: { keychain: 'acc.pg' } },
      { id: 'w', type: 'weaviate', url: 'http://localhost:8080' },
    ],
  },
  monitors: { channels: { webhook: { url: { keychain: 'acc.monitors-webhook-url' } } } },
  otel: { enabled: true, endpoint: 'https://otlp.example.com:4318' },
  localRuntimes: [{ health: 'http://127.0.0.1:8080/' }],
};
const SECRETS = {
  'acc.pg': 'postgres://u:p@db.example.com/vectors',
  'acc.monitors-webhook-url': 'https://hooks.slack.com/services/T/B/X',
};
const resolveSecret = (ref) => SECRETS[ref.keychain] ?? null;

test('every source in one config gives the expected list, sorted and unique, ports exact', () => {
  const got = allowList(EVERY, {
    env: { ZAAFPULSE_OLLAMA_URL: 'http://gpu.lan:11434', ZAAFPULSE_LMSTUDIO_URL: 'http://127.0.0.1:1234' },
    agents: [
      { id: 'a', remoteUrl: 'https://agent.example.com/a2a' },
      { id: 'b', remoteUrl: 'https://agent.example.com/other' },
      { id: 'c', enabled: false, remoteUrl: 'https://off-agent.example.com' },
      { id: 'd' },
    ],
    resolveSecret,
  });
  assert.deepEqual(
    got.map((r) => `${r.host}:${r.port}`),
    [
      '*.pinecone.io:443',
      '*.roamresearch.com:undefined',
      'abc.apps.dynatrace.com:443',
      'abc.live.dynatrace.com:443',
      'agent.example.com:443',
      'aiplatform.googleapis.com:443',
      'api.arize.com:443',
      'api.example.com:80',
      'api.groq.com:443',
      'api.mem.ai:443',
      'api.notion.com:443',
      'api.openai.com:443',
      'api.pinecone.io:443',
      'api.roamresearch.com:443',
      'db.example.com:5432',
      'gpu.lan:11434',
      'heybrain.io:443',
      'hooks.example.com:443',
      'hooks.slack.com:443',
      'login.microsoftonline.com:443',
      'mcp.example.com:443',
      'my.openai.azure.com:443',
      'n8n.example.com:5678',
      'oauth2.googleapis.com:443',
      'openrouter.ai:443',
      'otlp.example.com:4318',
      'q.example.com:6333',
      'reflect.app:443',
      'router.lan:20128',
    ],
  );
});

test('an empty config gives openrouter.ai only; no secret resolver leaves secret-held URLs out', () => {
  assert.deepEqual(allowList({}), [{ host: 'openrouter.ai', port: 443 }]);
  assert.deepEqual(allowList(null, {}), [{ host: 'openrouter.ai', port: 443 }]);
  const noSecrets = allowList(EVERY).map((r) => r.host);
  assert.ok(!noSecrets.includes('db.example.com') && !noSecrets.includes('hooks.slack.com'));
  const throwing = allowList(EVERY, {
    resolveSecret: () => {
      throw new Error('locked');
    },
  });
  assert.deepEqual(throwing, allowList(EVERY));
});

test('loopback, unparsable and unknown-scheme URLs are skipped; IPv6 without brackets; wildcards kept', () => {
  for (const u of [
    'http://localhost:1',
    'http://127.0.0.1/',
    'http://[::1]:9/',
    'http://[::ffff:7f00:1]/',
    'http://LOCALHOST./',
    'nope',
    'ftp://x.example.com/',
    '',
    null,
  ])
    assert.equal(ruleOf(u), null, String(u));
  assert.deepEqual(ruleOf('https://[2001:DB8::1]/x'), { host: '2001:db8::1', port: 443 });
  assert.deepEqual(ruleOf('http://Example.COM./'), { host: 'example.com', port: 80 });
  assert.deepEqual(ruleOf('https://127.0.0.2:8443'), { host: '127.0.0.2', port: 8443 }, 'only the granted loopback');
  const wild = allowList({ brain: { sources: [{ type: 'roam' }, { type: 'pinecone' }] } });
  assert.ok(wild.some((r) => r.host === '*.roamresearch.com' && r.port === undefined)); // any port (peer redirect)
  assert.ok(wild.some((r) => r.host === '*.pinecone.io' && r.port === 443));
});

test('OTLP from the environment when config does not turn it off; the public class adds nothing', () => {
  const env = { OTEL_EXPORTER_OTLP_ENDPOINT: 'https://env-otlp.example.com' };
  assert.ok(allowList({}, { env }).some((r) => r.host === 'env-otlp.example.com'));
  assert.ok(!allowList({ otel: { enabled: false } }, { env }).some((r) => r.host === 'env-otlp.example.com'));
  assert.ok(
    !allowList({}, { env: { ...env, OTEL_SDK_DISABLED: 'true' } }).some((r) => r.host === 'env-otlp.example.com'),
  );
  assert.deepEqual(allowList({ egress: { classes: { public: 'go' } } }), allowList({}));
});
