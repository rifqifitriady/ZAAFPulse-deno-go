// Fixtures for the go engine's trace spans (P4f, slice S8): the real lib/otel.js (wrapHandler, wrapUsage, modelSpan,
// finishModelSpan, responseObserver) handles each case in process (a request and a response object, no socket), with the
// local trace sink on and OTLP export off, and this records what the trace store keeps (lib/trace/recorder.js toRecord),
// what OTLP export needs beyond it (P4f D11: the tracestate, a remote parent, the status message), and the trace id the
// usage entry gets (wrapUsage). gw/engine/spans_test.go builds the same spans from the same input and must give the same
// bytes.
// Ids: scripts/fixtures/lib.js makes crypto.randomBytes(n) return n bytes equal to a counter that starts at 1 per case, so
// otel.js newId is deterministic; the Go test draws its ids from the same counter. The server span's times come from
// performance.now: its st and d are written as 0, and the Go test passes StartUs = EndUs = 0. The client span's times come
// from the usage entry (ts, ms) and are exact.
// Writes gw/testdata/spans/<n>-<name>.json as
// { fn: "spans", in: { request, answer, prior?, usage, usageWhen, ends }, out }.
// A route call (P4h Decision 10) records a line per leg it tried before the one that ends the call: `prior`, recorded
// in the handler before any byte of the answer (proxyChat's loop runs before the answer starts), each its own CLIENT
// span.
// Run: node scripts/fixtures/gen-spans.js. No network, environment, subprocess or system access; writes only under
// gw/testdata, so it runs under the CI fixtures job's Deno flags and gives the same bytes on Node and Deno.
const { EventEmitter } = require('events');
const { writeCases } = require('./lib');
const otel = require('../../lib/otel');
const { toRecord } = require('../../lib/trace/recorder');

// The providers the cases name, as config.json holds them (lib/otel.js providerSpanAttrs reads preset and baseUrl).
const PROVIDERS = [
  { id: 'oa', preset: 'custom', baseUrl: 'http://127.0.0.1:9/v1' },
  { id: 'op', preset: 'openai', baseUrl: 'https://api.openai.com/v1' },
  { id: 'v6', preset: 'ollama', baseUrl: 'http://[::1]:11434/v1' },
  { id: 'idn', preset: 'groq', baseUrl: 'https://BÜCHER.example/v1' },
  { id: 'bad', preset: 'mistral', baseUrl: 'not a url' },
  { id: 'proto', preset: 'constructor', baseUrl: 'https://h.example:8443/v1' },
];

function fakeReq({ method = 'POST', path = '/v1/chat/completions', httpVersion = '1.1', headers = {}, tls = false }) {
  const req = new EventEmitter();
  Object.assign(req, {
    method,
    url: path,
    httpVersion,
    headers,
    socket: { encrypted: tls, remoteAddress: '127.0.0.1' },
  });
  return req;
}

function fakeRes() {
  const res = new EventEmitter();
  res.statusCode = 200;
  res.write = () => true;
  res.end = () => res;
  return res;
}

const chunk = (c) => (typeof c === 'string' ? c : Buffer.from(c.b64, 'base64'));

// One case: the handler answers with answer.status and answer.chunks (strings, or { b64 } for raw bytes), records the
// usage entry before the answer ends ("before": a whole answer, lib/gateway/index.js:1030) or right after res.end
// ("after": a stream, :1124), and the response then emits "finish", or only "close" when the client left. The prior
// lines (a route call's earlier legs: 599s, fallbacks) are recorded first, in order.
async function run({ request, answer, prior = [], usage, usageWhen = 'before', ends = 'finish' }) {
  const recs = [];
  otel.init({
    getConfig: () => ({ gateway: { providers: PROVIDERS } }),
    env: {},
    sink: { enabled: () => true, add: (r) => recs.push(r), expect: () => {} },
    log: () => {},
  });
  let entry = null;
  const u = otel.wrapUsage({ record: (e) => (entry = e) });
  const handler = otel.wrapHandler((req, res) => {
    res.statusCode = answer.status;
    for (const p of prior) u.record(p);
    const record = () => {
      if (usage) u.record(usage);
    };
    if (usageWhen === 'before') record();
    const chunks = answer.chunks || [];
    chunks.forEach((c, i) => (i === chunks.length - 1 ? res.end(chunk(c)) : res.write(chunk(c))));
    if (!chunks.length) res.end();
    if (usageWhen === 'after') record();
  });
  const res = fakeRes();
  handler(fakeReq(request), res);
  res.emit(ends);
  await otel.reset();
  const spans = recs.map((r) => {
    const span = toRecord(r);
    if (span.k === 'server') Object.assign(span, { st: 0, d: 0 });
    const x = {};
    if (r.traceState) x.traceState = r.traceState;
    if (r.flags & 0x200) x.remote = true;
    if (r.status.code === 2 && r.status.message) x.message = r.status.message;
    return Object.keys(x).length ? { span, otlp: x } : { span };
  });
  return { traceId: entry && entry.traceId ? entry.traceId : null, spans };
}

const TP = '00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01';
const body = (o) => JSON.stringify(o);
const ok = (id = 'chatcmpl-1', reason = 'stop') =>
  body({
    id,
    object: 'chat.completion',
    choices: [{ index: 0, message: { role: 'assistant', content: 'hi' }, finish_reason: reason }],
  });
const base = {
  keyId: 'k1',
  provider: 'oa',
  model: 'oa/m1',
  kind: 'chat',
  format: 'openai',
  stream: false,
  local: true,
};
const line = (more) => ({
  ts: 1767225600000,
  ms: 12.345,
  status: 200,
  input: 1000,
  output: 500,
  cost: 0.002,
  ...base,
  ...more,
});
const whole = (more, request = {}) => ({ request, answer: { status: 200, chunks: [ok()] }, usage: line(more) });
const failed = (status, answerStatus, error) => ({
  request: {},
  answer: { status: answerStatus, chunks: [body({ error: { message: 'x' } })] },
  usage: line({ status, input: 0, output: 0, cost: 0, ...(error !== undefined ? { error } : {}) }),
});
// A token shape the secret scanner of lib/signals.js knows, built here so that no such literal sits in the repository.
const GH_TOKEN = `${'g'}hp_${'A1b2C3d4E5'.repeat(4).slice(0, 36)}`;
const sse = (...lines) => lines.map((l) => `data: ${l}\n\n`);
// A route call's lines (lib/gateway/index.js proxyChat): the route's name as model; a leg that threw has no routedTo and
// no tokens, a leg that fell back names its target, and the answering leg is the usual line.
const ROUTE = { model: 'router/r1' };
const PRIOR_TS = 1767225599990;
const attempt = (provider, more) => {
  const { input, output, ...rest } = line({ ...ROUTE, ts: PRIOR_TS, ms: 4.25, cost: 0, provider, ...more });
  return rest;
};
const threw = (provider, error, more = {}) => attempt(provider, { status: 599, error, ...more });
const fellBack = (provider, routedTo, status, more = {}) =>
  attempt(provider, { routedTo, status, error: 'fell back to the next route target', ...more });
const answered = (provider, routedTo, more = {}) => line({ ...ROUTE, provider, routedTo, ...more });
const routed = (more) => ({ request: {}, answer: { status: 200, chunks: [ok('chatcmpl-r')] }, ...more });

const CASES = [
  ['whole-200', whole()],
  ['traceparent-sampled', whole({}, { headers: { traceparent: TP, tracestate: 'a=1,b=2' } })],
  ['traceparent-unsampled', whole({}, { headers: { traceparent: TP.replace(/-01$/, '-00') } })],
  ['traceparent-version-ff', whole({}, { headers: { traceparent: TP.replace(/^00/, 'ff') } })],
  ['traceparent-zero-trace', whole({}, { headers: { traceparent: `00-${'0'.repeat(32)}-b7ad6b7169203331-01` } })],
  ['traceparent-future-version', whole({}, { headers: { traceparent: `cc-${TP.slice(3)}-more` } })],
  ['traceparent-00-with-more', whole({}, { headers: { traceparent: `${TP}-more` } })],
  ['traceparent-spaces', whole({}, { headers: { traceparent: `  ${TP}\t` } })],
  ['traceparent-uppercase', whole({}, { headers: { traceparent: TP.toUpperCase() } })],
  ['traceparent-flags-03', whole({}, { headers: { traceparent: TP.replace(/-01$/, '-03') } })],
  ['tracestate-nonprintable', whole({}, { headers: { traceparent: TP, tracestate: 'a=1\u0007' } })],
  ['tracestate-trimmed', whole({}, { headers: { traceparent: TP, tracestate: '  a=1  ' } })],
  ['tracestate-too-long', whole({}, { headers: { traceparent: TP, tracestate: 'a='.padEnd(513, 'x') } })],
  ['tracestate-without-traceparent', whole({}, { headers: { tracestate: 'a=1' } })],
  [
    'user-agent-long',
    whole({ client: 'claude-code' }, { headers: { 'user-agent': `claude-cli/${'x'.repeat(243)}😀😀` } }),
  ],
  ['user-agent-empty', whole({}, { headers: { 'user-agent': '' } })],
  ['tls-http10', whole({}, { tls: true, httpVersion: '1.0' })],
  ['provider-openai-https', whole({ provider: 'op' })],
  ['provider-ipv6', whole({ provider: 'v6' })],
  ['provider-idn', whole({ provider: 'idn' })],
  ['provider-bad-url', whole({ provider: 'bad' })],
  ['provider-unknown', whole({ provider: 'nope' })],
  ['provider-empty-id', whole({ provider: '' })],
  ['provider-proto-preset', whole({ provider: 'proto' })],
  ['routed-requested-saved-cached', whole({ routedTo: 'oa/m2', requested: 'fast', savedTokens: 40, cached: 300 })],
  ['session-agent', whole({ sessionId: 's-1', agent: 'helper' })],
  ['no-key', whole({ keyId: '' })],
  [
    'upstream-400-masked',
    failed(
      400,
      400,
      'bad request: Authorization: Bearer abcdefgh12345678 api_key=supersecretvalue token: "abcdef123456"',
    ),
  ],
  ['upstream-400-no-error-text', failed(400, 400)],
  ['upstream-500-no-tokens', failed(500, 502)],
  ['network-599', failed(599, 502, 'fetch failed')],
  ['status-0', failed(0, 502, 'odd')],
  ['secret-in-error-long', failed(400, 400, `${'é'.repeat(150)} ${GH_TOKEN} ${'x'.repeat(400)}`)],
  ['error-cut-at-pair', failed(400, 400, `${'a'.repeat(199)}😀 tail`)],
  [
    'rate-limited-429-no-usage',
    { request: {}, answer: { status: 429, chunks: [body({ error: { message: 'rate' } })] }, usage: null },
  ],
  ['zero-tokens-200', whole({ input: 0, output: 0 })],
  ['long-model', whole({ model: `oa/${'m'.repeat(9000)}` })],
  ['ms-rounding', whole({ ts: 1767225600001, ms: 0.0005 })],
  ['ms-zero', whole({ ms: 0 })],
  ['cost-whole-number', whole({ cost: 1 })],
  [
    'stream-usage',
    {
      request: {},
      usageWhen: 'after',
      answer: {
        status: 200,
        chunks: sse(
          '{"id":"c1","choices":[{"delta":{"content":"h"}}]}',
          '{"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}',
          '[DONE]',
        ),
      },
      usage: line({ stream: true, ttft: 3.5 }),
    },
  ],
  [
    'stream-crlf-nospace',
    {
      request: {},
      usageWhen: 'after',
      answer: {
        status: 200,
        chunks: [
          'data:{"id":"c2","choices":[{"finish_reason":"length"}]}\r\n',
          'data: {"choices":[{"finish_reason":"length"}]}\r\n',
        ],
      },
      usage: line({ stream: true, ttft: null }),
    },
  ],
  [
    'stream-split-utf8',
    {
      request: {},
      usageWhen: 'after',
      answer: {
        status: 200,
        chunks: [
          { b64: Buffer.from('data: {"id":"caf\xc3', 'latin1').toString('base64') },
          { b64: Buffer.from('\xa9","choices":[{"finish_reason":"stop"}]}\n', 'latin1').toString('base64') },
        ],
      },
      usage: line({ stream: true }),
    },
  ],
  [
    'stream-last-line-without-newline',
    {
      request: {},
      usageWhen: 'after',
      answer: { status: 200, chunks: ['data: {"id":"c4","choices":[{"finish_reason":"stop"}]}'] },
      usage: line({ stream: true }),
    },
  ],
  [
    'stream-client-left',
    {
      request: {},
      usageWhen: 'after',
      ends: 'close',
      answer: { status: 200, chunks: ['data: {"id":"c3","choices":[]}\n'] },
      usage: line({ stream: true, output: 3 }),
    },
  ],
  [
    'json-array-body',
    {
      request: {},
      answer: { status: 200, chunks: [`[${ok('a1', 'tool_calls')},${ok('a2', 'stop')}]`] },
      usage: line(),
    },
  ],
  [
    'reasons-dedup-many',
    {
      request: {},
      answer: {
        status: 200,
        chunks: [body({ id: 'r', choices: Array.from({ length: 20 }, (_, i) => ({ finish_reason: `r${i % 18}` })) })],
      },
      usage: line(),
    },
  ],
  [
    'id-with-type',
    {
      request: {},
      answer: { status: 200, chunks: [body({ id: 'skipped', type: 'x', choices: [{ finish_reason: 'stop' }] })] },
      usage: line(),
    },
  ],
  [
    'anthropic-message',
    {
      request: { path: '/v1/messages' },
      answer: {
        status: 200,
        chunks: [
          body({
            type: 'message',
            id: 'msg_1',
            role: 'assistant',
            content: [{ type: 'text', text: 'hi' }],
            stop_reason: 'end_turn',
          }),
        ],
      },
      usage: line({ format: 'anthropic' }),
    },
  ],
  [
    'anthropic-stream',
    {
      request: { path: '/v1/messages' },
      usageWhen: 'after',
      answer: {
        status: 200,
        chunks: sse(
          '{"type":"message_start","message":{"id":"msg_2"}}',
          '{"type":"content_block_delta","delta":{"text":"h"}}',
          '{"type":"message_delta","delta":{"stop_reason":"max_tokens"}}',
        ),
      },
      usage: line({ format: 'anthropic', stream: true }),
    },
  ],
  [
    'responses-incomplete',
    {
      request: { path: '/v1/responses' },
      answer: {
        status: 200,
        chunks: [
          body({
            object: 'response',
            id: 'resp_1',
            status: 'incomplete',
            incomplete_details: { reason: 'max_output_tokens' },
            output: [],
          }),
        ],
      },
      usage: line({ format: 'responses' }),
    },
  ],
  [
    'responses-stream-failed',
    {
      request: { path: '/v1/responses' },
      usageWhen: 'after',
      answer: { status: 200, chunks: sse('{"type":"response.failed","response":{"id":"resp_2","status":"failed"}}') },
      usage: line({ format: 'responses', stream: true }),
    },
  ],
  [
    'gemini',
    {
      request: { path: '/v1beta/models/gemini-x:generateContent' },
      answer: {
        status: 200,
        chunks: [
          body({ responseId: 'g1', candidates: [{ content: { parts: [{ text: 'hi' }] }, finishReason: 'STOP' }] }),
        ],
      },
      usage: line({ format: 'gemini' }),
    },
  ],
  [
    'gemini-number-reason',
    {
      request: { path: '/v1beta/models/gemini-x:streamGenerateContent' },
      answer: { status: 200, chunks: [body({ candidates: [{ finishReason: 7 }, { finishReason: [1, 'B'] }] })] },
      usage: line({ format: 'gemini' }),
    },
  ],
  [
    'choices-not-iterable',
    { request: {}, answer: { status: 200, chunks: [body({ id: 'x1', choices: 5 })] }, usage: line() },
  ],
  [
    'choices-string',
    { request: {}, answer: { status: 200, chunks: [body({ id: 'x2', choices: 'ab' })] }, usage: line() },
  ],
  [
    'choice-null',
    { request: {}, answer: { status: 200, chunks: [body({ id: 'x3', choices: [null] })] }, usage: line() },
  ],
  ['body-not-json', { request: {}, answer: { status: 200, chunks: ['{not json'] }, usage: line() }],
  [
    'body-over-1mib',
    {
      request: {},
      answer: { status: 200, chunks: [body({ id: 'big', pad: 'x'.repeat(1024 * 1024) })] },
      usage: line(),
    },
  ],
  ['body-empty', { request: {}, answer: { status: 200, chunks: [] }, usage: line() }],
  [
    'embeddings',
    {
      request: { path: '/v1/embeddings' },
      answer: { status: 200, chunks: [body({ id: 'e', data: [] })] },
      usage: line({ kind: 'embeddings' }),
    },
  ],
  // Route calls (P4h Decision 10): every line of the call is its own CLIENT span, in the order recorded, after the
  // server span. A leg that threw (599, no routedTo) or fell back (its status, routedTo, no tokens) comes first.
  ['route-fallback-429-then-200', routed({ prior: [fellBack('oa', 'oa/m1', 429)], usage: answered('op', 'op/m2') })],
  ['route-throw-then-200', routed({ prior: [threw('v6', 'fetch failed')], usage: answered('oa', 'oa/m1') })],
  [
    'route-stream-fallback',
    {
      request: {},
      usageWhen: 'after',
      prior: [fellBack('oa', 'oa/m1', 503, { stream: true })],
      answer: {
        status: 200,
        chunks: sse(
          '{"id":"c5","choices":[{"delta":{"content":"h"}}]}',
          '{"id":"c5","choices":[{"delta":{},"finish_reason":"stop"}]}',
          '[DONE]',
        ),
      },
      usage: answered('op', 'op/m2', { stream: true, ttft: 2.5 }),
    },
  ],
  [
    'route-anthropic-throw-fallback-200',
    {
      request: { path: '/v1/messages' },
      prior: [
        threw('oa', 'fetch failed', { format: 'anthropic' }),
        fellBack('op', 'op/m2', 500, { format: 'anthropic' }),
      ],
      answer: {
        status: 200,
        chunks: [body({ type: 'message', id: 'msg_3', role: 'assistant', content: [], stop_reason: 'end_turn' })],
      },
      usage: answered('v6', 'v6/m3', { format: 'anthropic' }),
    },
  ],
  [
    'route-all-threw-502',
    {
      request: {},
      prior: [threw('oa', 'fetch failed'), threw('op', 'connect ECONNREFUSED 127.0.0.1:9')],
      answer: { status: 502, chunks: [body({ error: { message: 'connect ECONNREFUSED 127.0.0.1:9' } })] },
      usage: null,
    },
  ],
  [
    'route-fallback-then-guard-block',
    {
      request: {},
      prior: [fellBack('oa', 'oa/m1', 429)],
      answer: { status: 400, chunks: [body({ error: { message: 'blocked' } })] },
      usage: line({ ...ROUTE, provider: 'op', status: 400, input: 0, output: 0, cost: 0, error: 'blocked: a secret' }),
    },
  ],
  [
    'route-fallback-then-throw-502',
    {
      request: {},
      prior: [fellBack('oa', 'oa/m1', 429), threw('op', 'fetch failed')],
      answer: { status: 502, chunks: [body({ error: { message: 'Body already consumed' } })] },
      usage: null,
    },
  ],
];

// Each case also carries the span attributes the control plane pushes for its provider (lib/front/push.js, P4f D4), as
// the Go side reads them from config.push.
// A route call's prior lines carry theirs in priorProviderSpans, in order.
const providerSpan = (u) =>
  otel.providerSpanAttrs(
    PROVIDERS.find((p) => p.id === u.provider),
    u.provider,
  );
const withProvider = (args) => ({
  ...args,
  ...(args.usage ? { providerSpan: providerSpan(args.usage) } : {}),
  ...(args.prior ? { priorProviderSpans: args.prior.map(providerSpan) } : {}),
});

function generate() {
  return writeCases(
    'spans',
    CASES.map(([name, args], i) => ({
      name: `${String(i + 1).padStart(2, '0')}-${name}`,
      fn: 'spans',
      args: withProvider(args),
      call: run,
    })),
  );
}

if (require.main === module)
  generate().catch((err) => {
    console.error(err);
    process.exit(1);
  });
module.exports = { generate };
