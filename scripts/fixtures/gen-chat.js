// Fixtures for the go engine's chat route (P4b, slice S2): the real JS gateway (lib/gateway createGateway) answers each
// case over HTTP on 127.0.0.1, against a provider stubbed at fetch, and this records what it sent the provider, what
// it answered the client and the usage line it wrote. gw/engine/chat_test.go plans and answers the same case from the
// config.push and secrets.handoff the control plane would send (lib/front/push.js) and must give the same bytes.
// Writes gw/testdata/chat/<n>-<name>.json as { fn: "chat", in: { config, secrets, request, provider }, out }.
// Only cases the engine serves are here (guardrails off, no budget, one OpenAI-format target, not streaming); the
// hand-offs are Go tests of their own.
// Run: node scripts/fixtures/gen-chat.js (CI runs every scripts/fixtures/gen-*.js and fails on a diff).
const fs = require('fs');
const os = require('os');
const path = require('path');
const http = require('http');
const { createGateway } = require('../../lib/gateway');
const { createSecrets } = require('../../lib/secrets');
const { configPush, secretsPush } = require('../../lib/front/push');
const { hash } = require('../../lib/gateway/keys');
const { toNano } = require('../../lib/money');
const { PRESETS } = require('../../lib/gateway/providers');

const OUT = path.join(__dirname, '..', '..', 'gw', 'testdata', 'chat');
const SECRET = 'zp_chatFixtureSecret0123456789abcdef';
const OA_KEY = 'sk-oa-fixture-123456';
const GATEWAY = {
  providers: [
    {
      id: 'oa',
      preset: 'custom',
      baseUrl: 'http://127.0.0.1:9/v1',
      tier: 'external',
      approved: true,
      apiKey: { env: 'OA_KEY' },
    },
    { id: 'pv', preset: 'ollama', baseUrl: 'http://127.0.0.1:11434/v1', aliases: { fast: 'llama-9' } },
    { id: 'op', preset: 'openai', baseUrl: 'http://127.0.0.1:8/v1', approved: true, apiKey: { env: 'OA_KEY' } },
    { id: 'tp', preset: 'openai', baseUrl: 'http://127.0.0.1:7/v1', approved: true, tokenParam: 'max_tokens' },
  ],
  routes: [],
  guardrails: { scope: 'external', secrets: 'off', pii: 'off', injection: 'off' },
};
const PRICING = {
  m1: { input: 1, output: 2 },
  'llama-9': { input: 0.1, output: 0.3, cacheRead: 0.01 },
  o3: { input: 2, output: 8 },
};
const KEYS = [
  {
    id: 'k1',
    name: 'k1',
    prefix: 'zp_chatFi…',
    hash: hash(SECRET),
    scopes: ['llm'],
    models: [],
    agents: [],
    budgetUSD: null,
    rpm: 10000,
    createdAt: 1,
  },
  {
    id: 'k2',
    name: 'k2',
    prefix: 'zp_chatFi…',
    hash: hash(`${SECRET}2`),
    scopes: ['llm'],
    models: ['pv/*', 'fast'],
    agents: [],
    budgetUSD: null,
    rpm: 10000,
    privateOnly: true,
    createdAt: 1,
  },
];
const ok = (extra = {}) => ({
  status: 200,
  body: JSON.stringify({
    id: 'c1',
    object: 'chat.completion',
    model: 'upstream-model',
    choices: [{ index: 0, message: { role: 'assistant', content: 'hi' }, finish_reason: 'stop' }],
    usage: { prompt_tokens: 1000, completion_tokens: 500 },
    ...extra,
  }),
});
const user = [{ role: 'user', content: 'hello' }];
const CASES = [
  ['basic', {}, { model: 'oa/m1', messages: user }, ok()],
  [
    'alias-private',
    {},
    { model: 'fast', messages: user, temperature: 0.2 },
    ok({ usage: { prompt_tokens: 50, completion_tokens: 5, prompt_tokens_details: { cached_tokens: 20 } } }),
  ],
  ['max-tokens-renamed-for-openai', {}, { model: 'op/gpt-6.1', max_tokens: 77, messages: user }, ok()],
  ['max-tokens-renamed-for-reasoning-model', {}, { messages: user, model: 'oa/o3', max_tokens: 5 }, ok()],
  ['token-param-override-keeps-max-tokens', {}, { model: 'tp/gpt-6.1', max_tokens: 9, messages: user }, ok()],
  [
    'both-token-fields-kept',
    {},
    { model: 'op/gpt-6.1', max_tokens: 9, max_completion_tokens: 10, messages: user },
    ok(),
  ],
  [
    'numbers-and-unicode',
    {},
    '{"model":"oa/m1","temperature":1.0,"seed":12345678901234567890,"top_p":1e-7,"2":"x","messages":[{"role":"user","content":"caf\\u00e9 \\ud83d\\ude00  "}]}',
    ok(),
  ],
  ['duplicate-model-key', {}, '{"model":"zz/none","messages":[],"model":"oa/m1"}', ok()],
  [
    'tools-and-extra-fields',
    {},
    {
      model: 'oa/m1',
      messages: user,
      tools: [{ type: 'function', function: { name: 'f', parameters: { type: 'object' } } }],
      tool_choice: 'auto',
      user: 'u1',
    },
    ok({
      choices: [
        {
          index: 0,
          message: {
            role: 'assistant',
            content: null,
            tool_calls: [{ id: 'call_1', type: 'function', function: { name: 'f', arguments: '{}' } }],
          },
          finish_reason: 'tool_calls',
        },
      ],
    }),
  ],
  [
    'upstream-400-json',
    {},
    { model: 'oa/m1', messages: user },
    { status: 400, body: '{"error":{"message":"bad parameter temperature","type":"invalid_request_error"}}' },
  ],
  ['upstream-500-text', {}, { model: 'oa/m1', messages: user }, { status: 500, body: 'upstream exploded' }],
  [
    'upstream-429',
    {},
    { model: 'oa/m1', messages: user },
    { status: 429, body: '{"error":{"message":"slow down"}}', headers: { 'retry-after': '7' } },
  ],
  ['upstream-200-not-json', {}, { model: 'oa/m1', messages: user }, { status: 200, body: 'not json at all' }],
  ['upstream-200-array', {}, { model: 'oa/m1', messages: user }, { status: 200, body: '[1,2]' }],
  ['upstream-200-zero', {}, { model: 'oa/m1', messages: user }, { status: 200, body: '0' }],
  [
    'upstream-200-bom',
    {},
    { model: 'oa/m1', messages: user },
    { status: 200, body: '﻿{"model":"x","usage":{"prompt_tokens":3}}' },
  ],
  [
    'error-names-the-secret',
    {},
    { model: 'oa/m1', messages: user },
    { status: 401, body: `{"error":{"message":"invalid key ${OA_KEY}"}}` },
  ],
  [
    'long-error-cut-at-200',
    {},
    { model: 'oa/m1', messages: user },
    { status: 503, body: JSON.stringify({ error: { message: `${'é'.repeat(150)}${'x'.repeat(100)}` } }) },
  ],
  [
    'error-message-not-a-string',
    {},
    { model: 'oa/m1', messages: user },
    { status: 500, body: '{"error":{"message":""},"x":1}' },
  ],
  ['private-only-key', { key: `${SECRET}2` }, { model: 'pv/llama-9', messages: user }, ok()],
  [
    'claude-code-client',
    { headers: { 'user-agent': 'claude-cli/2.1.0 (external, cli)' } },
    { model: 'oa/m1', messages: user },
    ok(),
  ],
  ['x-api-key', { auth: 'x-api-key' }, { model: 'oa/m1', messages: user }, ok()],
  ['connection-refused', {}, { model: 'oa/m1', messages: user }, { throw: 'ECONNREFUSED' }],
  ['timed-out', {}, { model: 'oa/m1', messages: user }, { throw: 'timeout' }],
];

async function main() {
  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), 'zp-gen-chat-'));
  fs.writeFileSync(path.join(dataDir, 'keys.json'), JSON.stringify({ keys: KEYS }));
  let provider = null;
  let sent = null;
  const fetchImpl = async (url, init) => {
    sent = { url, headers: init.headers, body: init.body };
    if (provider.throw === 'timeout') throw Object.assign(new Error('aborted'), { name: 'AbortError' });
    if (provider.throw) throw Object.assign(new TypeError('fetch failed'), { cause: { code: provider.throw } });
    return new Response(Buffer.from(provider.body, 'utf8'), {
      status: provider.status,
      headers: provider.headers || {},
    });
  };
  const secrets = createSecrets({
    env: { OA_KEY },
    exec: () => {
      throw new Error('no keychain');
    },
  });
  const gateway = createGateway({
    dataDir,
    getConfig: () => ({ gateway: GATEWAY }),
    writeGatewayConfig: () => {},
    secrets,
    resolveSecret: () => undefined,
    getPricing: () => PRICING,
    getIntegrations: () => ({ listTools: async () => [], callTool: async () => ({}) }),
    fetchImpl,
  });
  const server = http.createServer(async (req, res) => {
    if (!(await gateway.handle(req, res, new URL(req.url, 'http://localhost')))) res.end();
  });
  await new Promise((r) => server.listen(0, '127.0.0.1', r));
  const base = `http://127.0.0.1:${server.address().port}`;
  const config = configPush({ gateway: GATEWAY, keys: KEYS, pricing: PRICING });
  const handoff = secretsPush({ gateway: GATEWAY, resolve: (ref, f) => secrets.resolve(ref, f) });
  fs.rmSync(OUT, { recursive: true, force: true });
  fs.mkdirSync(OUT, { recursive: true });
  // The preset flags the engine reads (gw/engine/providers.go Presets): only presets with one.
  const presets = {};
  for (const [id, p] of Object.entries(PRESETS)) {
    const f = {
      ...(p.local ? { local: true } : {}),
      ...(p.forwards ? { forwards: true } : {}),
      ...(p.maxCompletionTokens ? { maxCompletionTokens: true } : {}),
      ...(p.format ? { format: p.format } : {}),
    };
    if (Object.keys(f).length) presets[id] = f;
  }
  fs.writeFileSync(path.join(OUT, '00-presets.json'), `${JSON.stringify({ fn: 'presets', in: null, out: presets })}\n`);
  let n = 0;
  for (const [name, opt, body, prov] of CASES) {
    provider = prov;
    sent = null;
    const key = opt.key || SECRET;
    const headers = {
      'content-type': 'application/json',
      ...(opt.auth === 'x-api-key' ? { 'x-api-key': key } : { authorization: `Bearer ${key}` }),
      ...(opt.headers || {}),
    };
    const text = typeof body === 'string' ? body : JSON.stringify(body);
    const r = await fetch(`${base}/v1/chat/completions`, { method: 'POST', headers, body: text });
    const client = {
      status: r.status,
      headers: Object.fromEntries(
        ['content-type', 'cache-control', 'access-control-allow-origin', 'retry-after'].map((h) => [
          h,
          r.headers.get(h),
        ]),
      ),
      body: await r.text(),
    };
    const { ts: _ts, ms: _ms, cost, ...usage } = gateway.usage.recent(1)[0];
    const out = {
      request: sent && { url: sent.url, headers: sent.headers, body: sent.body },
      client,
      usage: { ...usage, cost: toNano(cost) },
    };
    const input = { config, secrets: handoff, request: { headers, body: text }, provider: prov };
    fs.writeFileSync(
      path.join(OUT, `${String(++n).padStart(2, '0')}-${name}.json`),
      `${JSON.stringify({ fn: 'chat', in: input, out })}\n`,
    );
  }
  server.close();
  gateway.flush();
  fs.rmSync(dataDir, { recursive: true, force: true });
  console.log(`gen-chat: ${n} cases in ${path.relative(process.cwd(), OUT)}`);
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
