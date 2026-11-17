// The S8 proof (P4f D18; program plan §5 S8: "Ledger and audit identical to JS for the same run"). The same calls go
// through a server with every class on js and a server with chat and chat-stream on the go engine, each on a fresh data
// folder, each exporting OTLP (JSON) to a receiver in this test. Then every store is normalized and diffed: the usage
// log, the cost ledger, the governance chain, the MCP audit log, the trace store and the OTLP export.
// Normalized: ids either run makes (keys, span ids, fresh trace ids) become labels in the order they first appear;
// times become markers (ts, ms, ttft, st, d, the OTLP nanoseconds); money is compared at nano-USD (D17); the ipc mark
// is dropped (it only says which engine wrote the line; the go run must have it on every call the engine served,
// asserted on its own).
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const http = require('http');
const path = require('path');
const { startServer, clientFor, providerList, PRICES } = require('./contract/harness');
const { startUpstream } = require('./contract/upstream');
const { buildStub } = require('../scripts/ipc/build-stub');

const RUNTIME = process.versions && process.versions.deno ? 'deno' : 'node';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const hex = (i, n) => i.toString(16).padStart(n, '0');
const TRACE = (i) => hex(0xa000 + i, 32);
const tp = (i, flags = '01') => `00-${TRACE(i)}-${hex(0xb000 + i, 16)}-${flags}`;
const TTFT = 'gen_ai.response.time_to_first_chunk'; // a time, like st and d
const nano = (v) => (typeof v === 'number' ? Math.round(v * 1e9) : v);

function receiver() {
  const spans = [];
  const server = http.createServer((req, res) => {
    let body = '';
    req.on('data', (c) => (body += c));
    req.on('end', () => {
      try {
        for (const rs of JSON.parse(body).resourceSpans || [])
          for (const ss of rs.scopeSpans || []) spans.push(...(ss.spans || []));
      } catch {
        /* not JSON */
      }
      res.writeHead(200, { 'content-type': 'application/json' });
      res.end('{}');
    });
  });
  return new Promise((resolve) =>
    server.listen(0, '127.0.0.1', () =>
      resolve({
        url: `http://127.0.0.1:${server.address().port}`,
        spans,
        close: () => new Promise((r) => server.close(r)),
      }),
    ),
  );
}

const user = [{ role: 'user', content: 'hi' }];
// Left out, as recorded differences of the engine (not normalized away): a provider that refuses the connection is
// "dead: ECONNREFUSED" from the engine and "dead: fetch failed" from Node's fetch here (P4b: network errors keep each
// runtime's own text; Deno's differs from both). The stored lines differ in key order too (the ingest spreads the IPC
// record), which this deep comparison ignores: every field and value is the same.
// One corpus, in order. trace: a fixed traceparent's index, null (a fresh root), or 'off' (an unsampled one). served:
// the go engine answers it in go mode (its usage line must carry ipc there).
const CORPUS = [
  { name: 'whole', trace: 1, served: true, body: { model: 'oa/m1', messages: user } },
  { name: 'fresh root', trace: null, served: true, body: { model: 'oa/m1', messages: user } },
  { name: 'unsampled', trace: 'off', served: true, body: { model: 'oa/m1', messages: user } },
  {
    name: 'stream with usage',
    trace: 4,
    served: true,
    body: { model: 'oa/m1', messages: user, stream: true, stream_options: { include_usage: true } },
  },
  { name: 'stream', trace: 5, served: true, body: { model: 'oa/m1', messages: user, stream: true } },
  { name: 'private provider', trace: 6, served: true, body: { model: 'pv/m1', messages: user } },
  { name: 'provider 4xx', trace: 7, served: true, body: { model: 'oa/fail400', messages: user } },
  { name: 'provider 5xx', trace: 8, served: true, body: { model: 'oa/fail500', messages: user } },
  {
    name: 'claude-code client',
    trace: 10,
    served: true,
    body: { model: 'oa/m1', messages: user },
    headers: { 'user-agent': 'claude-cli/2.0.0 (external)' },
  },
  { name: 'rate limited, first', trace: 11, served: true, key: 'rate', body: { model: 'oa/m1', messages: user } },
  { name: 'rate limited, refused (429)', trace: 12, key: 'rate', body: { model: 'oa/m1', messages: user } },
  { name: 'hand-off: a route', trace: 13, body: { model: 'router/fallback', messages: user } },
  { name: 'embeddings (the server in both)', trace: 14, path: '/v1/embeddings', body: { model: 'oa/m1', input: 'hi' } },
];

const read = (file) =>
  fs.existsSync(file)
    ? fs
        .readFileSync(file, 'utf8')
        .split('\n')
        .filter(Boolean)
        .map((l) => JSON.parse(l))
    : [];
const readDir = (dir, re) =>
  fs.existsSync(dir)
    ? fs
        .readdirSync(dir)
        .filter((f) => re.test(f))
        .sort()
        .flatMap((f) => read(path.join(dir, f)))
    : [];

async function until(fn, what, ms) {
  const end = Date.now() + ms;
  for (;;) {
    const v = await fn();
    if (v) return v;
    if (Date.now() > end) throw new Error(`timed out waiting for ${what}`);
    await sleep(100);
  }
}

async function runOnce(engine, otlp, upstream) {
  const config = {
    pricingSync: { enabled: false },
    pricing: PRICES,
    otel: { enabled: true, endpoint: otlp.url, protocol: 'http/json' },
    ...(engine === 'go'
      ? {
          gateway: {
            engine: 'front',
            engines: { chat: 'go', 'chat-stream': 'go' },
            shadowDays: 0,
            shadowMinCompares: 0,
          },
        }
      : {}),
  };
  const server = await startServer({ runtime: RUNTIME, config });
  try {
    const c = clientFor(server.base);
    for (const p of providerList(upstream.url)) await c.admin('providers', { action: 'save', ...p });
    await c.admin('routes', {
      action: 'save',
      name: 'router/fallback',
      targets: ['oa/fail503', 'oa/m1'],
      strategy: 'fallback',
    });
    await c.setGuardrails({ scope: 'external', secrets: 'off', pii: 'off', injection: 'off' });
    const keys = { main: await c.newKey({ scopes: ['llm'] }), rate: await c.newKey({ scopes: ['llm'], rpm: 1 }) };
    if (engine === 'go') {
      // The gateway enforces the new keys and the routing once the push that carries them is in.
      await until(
        async () => {
          const st = (await c.call('/api/gateway/status', { method: 'GET' })).json;
          return st && st.state === 'ready' && st.engines && st.engines.chat === 'go';
        },
        'the go engine',
        15000,
      );
    }
    const statuses = [];
    for (const k of CORPUS) {
      const headers = { ...(k.headers || {}) };
      if (typeof k.trace === 'number') headers.traceparent = tp(k.trace);
      if (k.trace === 'off') headers.traceparent = tp(3, '00');
      const r = await c.call(k.path || '/v1/chat/completions', {
        key: keys[k.key || 'main'].secret,
        body: k.body,
        headers,
      });
      statuses.push([k.name, r.status]);
      await sleep(150); // a go call's events reach the stores through the ingest a few ms after the answer
    }
    await sleep(1500); // the JS recorder writes its spans once a second
    await c.call('/api/rescan', { method: 'POST', body: {} }); // one ledger pass (costTick)
    const q = '/api/cost/ledger?groupBy=source,provider,model,kind';
    const ledger = await until(
      async () => {
        const r = (await c.call(q, { method: 'GET' })).json;
        const rows = r && Array.isArray(r.rows) ? r.rows.filter((x) => x.source === 'gateway') : [];
        return rows.length ? rows : null;
      },
      'gateway rows in the ledger',
      20000,
    );
    // The OTLP exporter sends in batches: wait until the receiver has had nothing new for 2 s.
    let n = -1;
    await until(
      async () => {
        const now = otlp.spans.length;
        const stable = now === n && now > 0;
        n = now;
        if (!stable) await sleep(1900);
        return stable;
      },
      'the OTLP export',
      30000,
    );
    const d = path.join(server.data, '.data');
    return {
      statuses,
      keys: { [keys.main.id]: 'key:main', [keys.rate.id]: 'key:rate' },
      usage: read(path.join(d, 'gateway-usage.jsonl')),
      ledger,
      governance: readDir(path.join(d, 'audit'), /\.jsonl$/),
      mcp: read(path.join(server.data, '.cache', 'mcp-audit.jsonl')),
      traces: readDir(path.join(d, 'traces'), /\.ndjson$/),
      otlp: [...otlp.spans],
    };
  } finally {
    await server.stop();
  }
}

// The run's stores with ids, times and money normalized (see the header).
function normalize(run) {
  const label = new Map(Object.entries(run.keys));
  const fixed = new Set(CORPUS.filter((k) => typeof k.trace === 'number').map((k) => TRACE(k.trace)));
  let traces = 0;
  let spanN = 0;
  const traceLabel = (t) => {
    if (!t || fixed.has(t)) return t;
    if (!label.has(t)) label.set(t, `trace:${++traces}`);
    return label.get(t);
  };
  const relabel = (text) => {
    let s = text;
    for (const [id, l] of label) s = s.split(id).join(l);
    return s;
  };
  const time = (o, keys) => {
    for (const k of keys) if (o[k] !== undefined && o[k] !== null) o[k] = '<time>';
    return o;
  };
  // Usage lines, in order: fresh trace ids get labels in the order of the calls.
  const usage = run.usage.map((l) => {
    const { ipc, ...rest } = l;
    void ipc;
    if (rest.traceId) rest.traceId = traceLabel(rest.traceId);
    rest.cost = nano(rest.cost);
    return time(JSON.parse(relabel(JSON.stringify(rest))), ['ts', 'ms', 'ttft']);
  });
  // Trace records of the corpus' traces, ordered by trace (call order) then kind; span ids by that order.
  const order = new Map(run.usage.filter((l) => l.traceId).map((l, i) => [l.traceId, i]));
  for (const k of CORPUS)
    if (typeof k.trace === 'number' && !order.has(TRACE(k.trace))) order.set(TRACE(k.trace), 100 + k.trace);
  const ours = (t) => order.has(t);
  const byTrace = (a, b) => order.get(a.t) - order.get(b.t) || (a.k === b.k ? 0 : a.k === 'server' ? -1 : 1);
  const recs = run.traces.filter((r) => ours(r.t)).sort(byTrace);
  for (const r of recs) if (!label.has(r.s)) label.set(r.s, `span:${++spanN}`);
  const traceRecs = recs.map((r) => {
    const { ipc, ...rest } = r;
    void ipc;
    const out = JSON.parse(relabel(JSON.stringify(rest)));
    out.t = traceLabel(r.t);
    if (out.a && out.a['zaafpulse.cost.usd'] !== undefined)
      out.a['zaafpulse.cost.usd'] = nano(out.a['zaafpulse.cost.usd']);
    if (out.a && out.a[TTFT] !== undefined) out.a[TTFT] = '<time>';
    return time(out, ['st', 'd']);
  });
  const otlp = run.otlp
    .filter((s) => ours(s.traceId))
    .sort((a, b) => order.get(a.traceId) - order.get(b.traceId) || a.kind - b.kind)
    .map((s) => {
      const out = JSON.parse(relabel(JSON.stringify(s)));
      out.traceId = traceLabel(s.traceId);
      for (const a of out.attributes || [])
        if (a.key === 'zaafpulse.cost.usd' && a.value && a.value.doubleValue !== undefined)
          a.value.doubleValue = nano(a.value.doubleValue);
        else if (a.key === TTFT) a.value = '<time>';
      return time(out, ['startTimeUnixNano', 'endTimeUnixNano']);
    });
  const chain = (e) => {
    const { seq, ts, prev, mac, keyed, ...rest } = e;
    (void seq, ts, prev, mac, keyed);
    return JSON.parse(relabel(JSON.stringify(rest)));
  };
  const ledger = run.ledger
    .map((r) => JSON.parse(JSON.stringify(r), (k, v) => (typeof v === 'number' && !Number.isInteger(v) ? nano(v) : v)))
    .sort((a, b) => (JSON.stringify(a) < JSON.stringify(b) ? -1 : 1));
  return {
    usage,
    ledger,
    governance: run.governance.map(chain),
    mcp: run.mcp.map((e) => time(chain(e), ['ms'])),
    traces: traceRecs,
    otlp,
  };
}

// In the go run, every call the engine served has its usage line and both spans from the gateway (ipc); each client
// span the engine wrote lies inside its server span (5 ms of slack: the client span's times come from the usage line's
// ms clock). The JS gateway's own spans are not checked: they mix Date.now and performance.now, which drift apart (a
// route's span ended past its server span on windows-11-arm).
function assertShape(run, engine) {
  const served = CORPUS.filter((k) => k.served && k.trace !== 'off');
  for (const k of served) {
    const line = run.usage.find((l) => l.traceId === TRACE(k.trace)) || null;
    if (typeof k.trace === 'number') assert.ok(line, `${engine}: ${k.name}: a usage line names its trace`);
    if (engine === 'go' && line) assert.ok(line.ipc, `go: ${k.name}: the go engine wrote the line`);
  }
  const byTrace = new Map();
  for (const r of run.traces) (byTrace.get(r.t) || byTrace.set(r.t, []).get(r.t)).push(r);
  for (const [, rs] of byTrace) {
    const server = rs.find((r) => r.k === 'server');
    for (const c of rs.filter((r) => r.k === 'client' && r.ipc && server && r.p === server.s))
      assert.ok(
        c.st >= server.st - 5000 && c.st + c.d <= server.st + server.d + 5000,
        `${engine}: ${c.n} inside its server span`,
      );
  }
}

test(
  'S8: the same calls through js and through go leave identical stores (P4f D18)',
  { timeout: 300000 },
  async (t) => {
    const built = buildStub();
    if (built.skip) {
      if (process.env.CI) throw new Error(`the go gateway is needed in CI: ${built.skip}`);
      return t.skip(built.skip);
    }
    const upstream = await startUpstream();
    const otlpJs = await receiver();
    const otlpGo = await receiver();
    try {
      const js = await runOnce('js', otlpJs, upstream);
      const go = await runOnce('go', otlpGo, upstream);
      assert.deepEqual(go.statuses, js.statuses, 'the same answers');
      assertShape(js, 'js');
      assertShape(go, 'go');
      const a = normalize(js);
      const b = normalize(go);
      // Not equal by being empty: each store holds the corpus (12 traced calls with a server and a client span each, but
      // the 429, which has its server span only; the route adds a second client span).
      assert.ok(a.usage.length >= 13, `usage lines: ${a.usage.length}`);
      assert.ok(a.traces.length >= 22, `trace records: ${a.traces.length}`);
      assert.ok(a.otlp.length >= 22, `OTLP spans: ${a.otlp.length}`);
      assert.ok(a.governance.length > 0 && a.ledger.length > 0, 'the governance chain and the ledger have rows');
      for (const store of ['usage', 'ledger', 'governance', 'mcp', 'traces', 'otlp'])
        assert.deepEqual(b[store], a[store], `${store}: the go run differs from the js run`);
    } finally {
      await otlpJs.close();
      await otlpGo.close();
      await upstream.close();
    }
  },
);
