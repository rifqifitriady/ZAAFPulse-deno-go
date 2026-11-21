// Token-auth route fixtures (P4g spec Decision 16): the real JS gateway (lib/gateway createGateway) serves each case's
// client requests in process against OAuth2 and Google service-account providers. Its fetch is stubbed: every outbound
// exchange (the token endpoint and the provider, in the order JS made them) takes the next answer of the case's queue
// and is recorded as sent: { url, method, headers (lower-case), body } (the token form as sent; a provider request
// with the arguments shadow.noteProviderRequest gets). gw/engine/auth_test.go serves the same client requests through
// Engine.Serve, from the config.push, secrets.handoff and budget.snapshot the control plane would send
// (lib/front/push.js), over a replay transport that checks exchange i against out.exchanges[i] and answers
// in.queue[i]; the client answers and usage lines must match.
// Each case runs on a fresh gateway (the token cache is per gateway, keyed by provider id). The clock is lib.js's fixed
// one; a refresh is forced by an expires_in under the 5 minute lead, never by moving the clock. The RS256 JWT is
// deterministic (the test key of gen-provauth.js, RSASSA-PKCS1-v1_5).
// Writes gw/testdata/auth/<name>.json through scripts/fixtures/lib.js (random bytes from a per-case counter, secrets
// encoded, scanner prefixes escaped): { fn, in: { note, configPush, secretsPush, snapshot, requests, queue },
// out: { exchanges, clients, usage } }.
// Run: node scripts/fixtures/gen-auth.js. No network, env, subprocess or sys access; writes only under gw/testdata.
const fs = require('fs');
const path = require('path');
const { writeCases, ROOT, FIXED_NOW } = require('./lib'); // first: the random counter and the fixed clock
const { TEST_KEY } = require('./gen-provauth');
const { createGateway } = require('../../lib/gateway');
const { createSecrets } = require('../../lib/secrets');
const { createGuard } = require('../../lib/cost/guard');
const { configPush, secretsPush, snapshotPush } = require('../../lib/front/push');
const { hash } = require('../../lib/gateway/keys');
const { toNano } = require('../../lib/money');

const SECRET = 'zp_authFixtureSecret0123456789abcdef';
const AZ_SECRET = 'az-client-secret-0123456789';
const AX_SECRET = 'ax-client-secret-9876543210';
const RT_SECRET = JSON.stringify({ client_secret: 'rt-client-secret-1', refresh_token: 'rt-refresh-1' });
const SA_JSON = JSON.stringify({
  type: 'service_account',
  client_email: 'gw@proj.example.test',
  token_uri: 'https://oauth2.example.test/token',
  private_key: TEST_KEY,
});
const secrets = new Set([SECRET, AZ_SECRET, AX_SECRET, RT_SECRET, SA_JSON]);

const OFF = { scope: 'external', secrets: 'off', pii: 'off', injection: 'off' };
const oauth2 = (tokenUrl, clientId, extra = {}) => ({
  type: 'oauth2',
  tokenUrl,
  clientId,
  scope: '',
  grant: 'client_credentials',
  ...extra,
});
const PROVIDERS = [
  {
    id: 'az',
    preset: 'azure-openai',
    baseUrl: 'https://az.example.test/openai/v1',
    approved: true,
    apiKey: { env: 'AZ_SECRET' },
    auth: oauth2('https://login.example.test/t/oauth2/v2.0/token', 'az-app', {
      scope: 'https://cognitiveservices.azure.com/.default',
    }),
  },
  {
    id: 'rt',
    preset: 'custom',
    baseUrl: 'https://rt.example.test/v1',
    tier: 'external',
    approved: true,
    apiKey: { env: 'RT_SECRET' },
    auth: oauth2('https://login.example.test/rt/token', 'rt-app', { grant: 'refresh_token' }),
  },
  {
    id: 'vx',
    preset: 'vertex',
    baseUrl: 'https://vertex.example.test/v1/projects/p1/locations/global/endpoints/openapi',
    approved: true,
    apiKey: { env: 'VX_SA' },
    auth: { type: 'google-sa', scope: 'https://www.googleapis.com/auth/cloud-platform' },
  },
  {
    id: 'ax',
    preset: 'custom',
    format: 'anthropic',
    baseUrl: 'https://ax.example.test/v1',
    tier: 'external',
    approved: true,
    apiKey: { env: 'AX_SECRET' },
    auth: oauth2('https://login.example.test/ax/token', 'ax-app', { scope: 'api://ax/.default' }),
  },
];
const PRICING = {
  'gpt-6.1': { input: 2, output: 8 },
  m1: { input: 1, output: 2 },
  'claude-x': { input: 3, output: 15, cacheRead: 0.3 },
  e1: { input: 0.02, output: 0 },
  'gemini-2.5-pro': { input: 1.25, output: 10 },
};
const KEYS = [
  {
    id: 'k1',
    name: 'k1',
    prefix: 'zp_authFi…',
    hash: hash(SECRET),
    scopes: ['llm'],
    models: [],
    agents: [],
    budgetUSD: null,
    rpm: 10000,
    createdAt: 1,
  },
];

// The budget service the guard and snapshotPush read: no budgets, no cost centers.
const SERVICE = {
  list: () => ({ budgets: [] }),
  evaluateOne: (b) => ({ ...b }),
  keyCenter: () => null,
  reportGuard: () => {},
};

/* ---------- answers (the queue) ---------- */
const json = (status, body, headers = {}) => ({
  status,
  headers: { 'content-type': 'application/json', ...headers },
  body: JSON.stringify(body),
});
const token = (access, expiresIn = 3600, extra = {}) =>
  json(200, { access_token: access, token_type: 'Bearer', expires_in: expiresIn, ...extra });
const chatOk = () =>
  json(200, {
    id: 'chatcmpl-up',
    object: 'chat.completion',
    model: 'upstream-model',
    choices: [{ index: 0, message: { role: 'assistant', content: 'hi' }, finish_reason: 'stop' }],
    usage: { prompt_tokens: 1000, completion_tokens: 500, prompt_tokens_details: { cached_tokens: 200 } },
  });
const chunk = (delta, finish = null) => ({
  id: 'chatcmpl-up',
  object: 'chat.completion.chunk',
  model: 'upstream-model',
  choices: [{ index: 0, delta, finish_reason: finish }],
});
const chatStream = () => ({
  status: 200,
  headers: { 'content-type': 'text/event-stream' },
  chunks: [
    chunk({ role: 'assistant', content: '' }),
    chunk({ content: 'Hel' }),
    chunk({ content: 'lo' }),
    chunk({}, 'stop'),
    {
      id: 'chatcmpl-up',
      object: 'chat.completion.chunk',
      choices: [],
      usage: { prompt_tokens: 12, completion_tokens: 3 },
    },
  ]
    .map((c) => `data: ${JSON.stringify(c)}\n\n`)
    .concat(['data: [DONE]\n\n']),
});
const anMsg = () =>
  json(200, {
    id: 'msg_up',
    type: 'message',
    role: 'assistant',
    model: 'claude-x',
    content: [{ type: 'text', text: 'hi there' }],
    stop_reason: 'end_turn',
    usage: { input_tokens: 10, output_tokens: 5, cache_read_input_tokens: 2 },
  });
const embOk = () =>
  json(200, {
    object: 'list',
    data: [{ object: 'embedding', index: 0, embedding: [0.1, -0.2] }],
    model: 'e1-up',
    usage: { prompt_tokens: 4, total_tokens: 4 },
  });
const unauthorized = () => json(401, { error: { message: 'token expired', type: 'invalid_token' } });

/* ---------- client requests ---------- */
const user = (content = 'hi') => [{ role: 'user', content }];
const chat = (model, extra = {}) => ({ path: '/v1/chat/completions', body: { model, messages: user(), ...extra } });
const messages = (model) => ({
  path: '/v1/messages',
  auth: 'x-api-key',
  body: { model, max_tokens: 64, messages: user() },
});

// [name, fn, note, client requests, queue]
const CASES = [
  [
    'az-chat-whole',
    'chat',
    'OAuth2 client_credentials: one token request, then the chat call with its Bearer token',
    [chat('az/gpt-6.1')],
    [token('az-tok-1'), chatOk()],
  ],
  [
    'az-chat-stream',
    'chat',
    'a streamed chat call after the token request',
    [chat('az/gpt-6.1', { stream: true })],
    [token('az-tok-1'), chatStream()],
  ],
  [
    'az-messages',
    'messages',
    '/v1/messages translated to chat for an OpenAI-format token provider',
    [messages('az/gpt-6.1')],
    [token('az-tok-1'), chatOk()],
  ],
  [
    'az-embeddings',
    'embeddings',
    '/v1/embeddings with a token',
    [{ path: '/v1/embeddings', body: { model: 'az/e1', input: 'hello' } }],
    [token('az-tok-1'), embOk()],
  ],
  [
    'ax-chat',
    'chatAnthropic',
    'an Anthropic-format token provider: the token goes in x-api-key',
    [chat('ax/claude-x')],
    [token('ax-tok-1'), anMsg()],
  ],
  [
    'ax-count',
    'count',
    'native count_tokens on an Anthropic-format token provider',
    [{ path: '/v1/messages/count_tokens', auth: 'x-api-key', body: { model: 'ax/claude-x', messages: user('hello') } }],
    [token('ax-tok-1'), json(200, { input_tokens: 42 })],
  ],
  [
    'rt-rotation',
    'chat',
    'refresh_token grant over two calls: the first token expires within the 5 min lead and rotates the refresh token, so the second call refreshes again with the rotated one',
    [chat('rt/m1'), chat('rt/m1')],
    [token('rt-tok-1', 60, { refresh_token: 'rt-refresh-2' }), chatOk(), token('rt-tok-2', 3600), chatOk()],
  ],
  [
    'vx-chat',
    'chat',
    'Google service account: the RS256 JWT bearer assertion, then the call with its token',
    [chat('vx/gemini-2.5-pro')],
    [token('vx-tok-1'), chatOk()],
  ],
  [
    'az-shared',
    'chat',
    'two calls share one cached token: one token request',
    [chat('az/gpt-6.1'), chat('az/gpt-6.1')],
    [token('az-tok-1'), chatOk(), chatOk()],
  ],
  [
    'az-replay-ok',
    'chat',
    'the provider answers 401: the body is cancelled, the token force-refreshed and the call sent once more',
    [chat('az/gpt-6.1')],
    [token('az-tok-1'), unauthorized(), token('az-tok-2'), chatOk()],
  ],
  [
    'az-replay-401',
    'chat',
    'a 401 after the replay too: the second 401 is the answer',
    [chat('az/gpt-6.1')],
    [token('az-tok-1'), unauthorized(), token('az-tok-2'), unauthorized()],
  ],
  [
    'az-token-401',
    'chat',
    'the token endpoint answers 401 with an error code: a failed call, no provider request',
    [chat('az/gpt-6.1')],
    [json(401, { error: 'invalid_client', error_description: 'bad secret' })],
  ],
  [
    'az-token-not-json',
    'chat',
    'the token endpoint answers 200 with text that is not JSON',
    [chat('az/gpt-6.1')],
    [{ status: 200, headers: { 'content-type': 'text/html' }, body: '<html>sign in</html>' }],
  ],
  [
    'az-token-redirect',
    'chat',
    'the token endpoint answers with a redirect: refused, not followed',
    [chat('az/gpt-6.1')],
    [{ status: 302, headers: { location: 'https://elsewhere.example.test/token' }, body: '' }],
  ],
  [
    'az-token-fetch-failed',
    'chat',
    'the token endpoint cannot be reached: fetch rejects with TypeError "fetch failed", the call fails with no provider request',
    [chat('az/gpt-6.1')],
    [{ throws: 'fetch failed' }],
  ],
  [
    'az-token-no-access',
    'chat',
    'the token endpoint answers 200 JSON without access_token',
    [chat('az/gpt-6.1')],
    [json(200, { token_type: 'Bearer', expires_in: 3600 })],
  ],
];

// A request as gateway.handle reads it: headers, the target, a loopback client, the body as one chunk.
const requestOf = (url, headers, text) => ({
  method: 'POST',
  url,
  headers,
  socket: { remoteAddress: '127.0.0.1', encrypted: false },
  on() {},
  once() {},
  async *[Symbol.asyncIterator]() {
    yield Buffer.from(text, 'utf8');
  },
});
// A response that keeps what the gateway writes.
function responseOf() {
  const res = {
    statusCode: 200,
    headers: {},
    body: '',
    headersSent: false,
    writableEnded: false,
    writeHead(status, h = {}) {
      res.statusCode = status;
      res.headersSent = true;
      for (const [k, v] of Object.entries(h)) res.headers[k.toLowerCase()] = String(v);
      return res;
    },
    setHeader: (k, v) => (res.headers[k.toLowerCase()] = String(v)),
    write: (c) => ((res.body += String(c)), true),
    end(c) {
      if (c !== undefined && typeof c !== 'function') res.body += String(c);
      res.writableEnded = true;
      return res;
    },
    on() {},
    once() {},
  };
  return res;
}

const HEADERS = ['content-type', 'cache-control', 'access-control-allow-origin', 'retry-after', 'x-zp-dropped'];
const lower = (h) => Object.fromEntries(Object.entries(h || {}).map(([k, v]) => [k.toLowerCase(), String(v)]));

// A queue entry as the fixture holds it: a stream's chunks joined into its body text; { throws } when fetch rejects.
const answerOf = (a) => (a.throws ? { throws: a.throws } : answerOfResponse(a));
const answerOfResponse = (a) => ({
  status: a.status,
  headers: a.headers || {},
  body: a.chunks ? a.chunks.join('') : a.body,
  ...(a.chunks ? { stream: true } : {}),
});

async function runCase(dataDir, sec, gw, requests, queue) {
  fs.rmSync(dataDir, { recursive: true, force: true });
  fs.mkdirSync(dataDir, { recursive: true });
  fs.writeFileSync(path.join(dataDir, 'keys.json'), JSON.stringify({ keys: KEYS }));
  const exchanges = [];
  const fetchImpl = async (url, init = {}) => {
    exchanges.push({
      url: String(url),
      method: init.method || 'GET',
      headers: lower(init.headers),
      body: init.body === undefined ? null : String(init.body),
    });
    const a = queue[exchanges.length - 1];
    if (!a) throw new Error(`exchange ${exchanges.length} has no answer in the queue`);
    if (a.throws) throw new TypeError(a.throws); // fetch rejects: a refused connection, a DNS or TLS failure
    if (a.chunks) {
      const pieces = a.chunks.map((c) => new Uint8Array(Buffer.from(c, 'utf8')));
      let i = 0;
      const body = new ReadableStream({
        pull(ctrl) {
          if (i < pieces.length) ctrl.enqueue(pieces[i++]);
          else ctrl.close();
        },
      });
      return new Response(body, { status: a.status, headers: a.headers || {} });
    }
    // A real Response: its body supports cancel(), which the 401 replay and the redirect refusal call.
    return new Response(a.body === '' ? null : Buffer.from(a.body, 'utf8'), {
      status: a.status,
      headers: a.headers || {},
    });
  };
  const guard = createGuard({ service: SERVICE, now: () => FIXED_NOW });
  const gateway = createGateway({
    dataDir,
    getConfig: () => ({ gateway: gw }),
    writeGatewayConfig: () => {},
    secrets: sec,
    resolveSecret: () => undefined,
    getPricing: () => PRICING,
    getIntegrations: () => ({ listTools: async () => [], callTool: async () => ({}) }),
    fetchImpl,
    toolsHome: dataDir,
    budgetGuard: {
      check: (ctx) => guard.check(ctx),
      keyLimited: (id) => guard.keyLimited(id),
      record: () => {},
      failed: () => {},
    },
  });
  const clients = [];
  const usage = [];
  try {
    for (const r of requests) {
      const before = gateway.usage.recent(2000).length;
      const res = responseOf();
      await gateway.handle(requestOf(r.path, r.headers, r.body), res, new URL(r.path, 'http://localhost'));
      clients.push({
        status: res.statusCode,
        headers: Object.fromEntries(HEADERS.map((h) => [h, res.headers[h] ?? null])),
        body: res.body,
      });
      const lines = gateway.usage.recent(2000);
      if (lines.length > before) {
        const { ts: _ts, ms: _ms, ttft, cost, ...rest } = lines[0];
        usage.push({ ...rest, cost: toNano(cost), ttft: ttft === undefined ? 'absent' : ttft === null ? null : 'set' });
      } else usage.push(null);
    }
  } finally {
    gateway.flush();
  }
  if (exchanges.length !== queue.length)
    throw new Error(`${queue.length} answers queued, ${exchanges.length} exchanges made`);
  return { exchanges, clients, usage };
}

function main() {
  const dataDir = path.join(ROOT, 'gw', 'testdata', '.gen-auth-data');
  const sec = createSecrets({
    env: { AZ_SECRET, AX_SECRET, RT_SECRET, VX_SA: SA_JSON },
    exec: () => {
      throw new Error('no keychain');
    },
  });
  const gw = {
    providers: PROVIDERS,
    routes: [],
    guardrails: OFF,
    defaults: { model: '', plannerModel: '', codeModel: '' },
    tokenSaver: true,
    modelMap: [],
  };
  const cases = CASES.map(([name, fn, note, reqs, queue]) => {
    const requests = reqs.map((r) => ({
      method: 'POST',
      path: r.path,
      headers: {
        'content-type': 'application/json',
        'user-agent': 'claude-cli/9.9.9',
        ...(r.auth === 'x-api-key' ? { 'x-api-key': SECRET } : { authorization: `Bearer ${SECRET}` }),
      },
      body: JSON.stringify(r.body),
    }));
    const args = {
      note,
      configPush: configPush({ gateway: gw, keys: KEYS, pricing: PRICING }),
      secretsPush: secretsPush({ gateway: gw, resolve: (ref, f) => sec.resolve(ref, f) }),
      snapshot: { ...snapshotPush({ service: SERVICE, keyIds: KEYS.map((k) => k.id), now: FIXED_NOW }), at: FIXED_NOW },
      requests,
      queue: queue.map(answerOf),
    };
    return { name, fn, args, call: () => runCase(dataDir, sec, gw, requests, queue) };
  });
  return writeCases('auth', cases, { secrets }).finally(() => fs.rmSync(dataDir, { recursive: true, force: true }));
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
