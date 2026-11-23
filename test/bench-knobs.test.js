// bench/run.js knobs added for the final benchmarks (the final-bench design):
// ZP_BENCH_ROUTE (a two-target round-robin route, P4h) and ZP_BENCH_OTLP (OTLP export on, P4f D21), and the mock
// provider's OTLP sink (POST /v1/traces, counted apart from the gateway's calls).
const test = require('node:test');
const assert = require('node:assert/strict');
const { configFromEnv, seedBench, serverConfig, gatewayModel } = require('../bench/run');
const { startMock } = require('../bench/mock-provider');

test('configFromEnv: defaults, and the new knobs', () => {
  const d = configFromEnv({});
  assert.equal(d.route, null);
  assert.equal(d.otlp, false);
  assert.equal(d.tracing, true);
  assert.deepEqual(d.levels, [50, 200, 500]);
  assert.equal(d.requests, 2000);
  const c = configFromEnv({ ZP_BENCH_ROUTE: 'round-robin', ZP_BENCH_OTLP: 'on', ZP_BENCH_TRACING: 'off' });
  assert.equal(c.route, 'round-robin');
  assert.equal(c.otlp, true);
  assert.equal(c.tracing, false);
  assert.equal(configFromEnv({ ZP_BENCH_ROUTE: 'fallback' }).route, 'fallback');
  assert.equal(configFromEnv({ ZP_BENCH_ROUTE: 'bogus' }).route, null);
  assert.equal(configFromEnv({ ZP_BENCH_OTLP: 'yes' }).otlp, false);
});

test('seedBench: one provider; with a route, a second provider and the route over both', async () => {
  const calls = [];
  const c = { admin: async (route, body) => calls.push([route, body]) };
  await seedBench(c, 'http://127.0.0.1:1/v1', configFromEnv({}));
  assert.deepEqual(
    calls.map(([r, b]) => [r, b.id]),
    [['providers', 'bench']],
  );
  calls.length = 0;
  await seedBench(c, 'http://127.0.0.1:1/v1', configFromEnv({ ZP_BENCH_ROUTE: 'round-robin', ZP_BENCH_TIER: 'private' }));
  assert.deepEqual(
    calls.map(([r]) => r),
    ['providers', 'providers', 'routes'],
  );
  assert.equal(calls[1][1].id, 'bench2');
  assert.equal(calls[1][1].tier, 'private');
  assert.equal(calls[1][1].baseUrl, 'http://127.0.0.1:1/v1');
  assert.deepEqual(calls[2][1], {
    action: 'save',
    name: 'router/bench',
    targets: ['bench/bench-1', 'bench2/bench-1'],
    strategy: 'round-robin',
  });
});

test('gatewayModel: the route name with a route, else the provider model', () => {
  assert.equal(gatewayModel(configFromEnv({})), 'bench/bench-1');
  assert.equal(gatewayModel(configFromEnv({ ZP_BENCH_ROUTE: 'round-robin' })), 'router/bench');
});

test('serverConfig: tracing off, OTLP export to the mock, both untouched by default', () => {
  const seed = { pricingSync: { enabled: false } };
  assert.equal(serverConfig(configFromEnv({}), 'http://127.0.0.1:9', seed), seed);
  assert.deepEqual(serverConfig(configFromEnv({ ZP_BENCH_TRACING: 'off' }), 'http://127.0.0.1:9', seed), {
    ...seed,
    tracing: { enabled: false },
  });
  assert.deepEqual(serverConfig(configFromEnv({ ZP_BENCH_OTLP: 'on' }), 'http://127.0.0.1:9', seed), {
    ...seed,
    otel: { enabled: true, endpoint: 'http://127.0.0.1:9', protocol: 'http/json' },
  });
});

test('mock provider: POST /v1/traces answers 200 and counts as otlp, not as a gateway call', async () => {
  const m = await startMock({ delayMs: 1, chunks: 1, intervalMs: 1 });
  try {
    const base = m.url.replace(/\/v1$/, '');
    const r = await fetch(`${base}/v1/traces`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: '{}' });
    assert.equal(r.status, 200);
    await r.arrayBuffer();
    const s = await (await fetch(`${base}/_stats`)).json();
    assert.equal(s.otlp, 1);
    assert.equal(s.js, 0);
    assert.equal(s.go, 0);
    const again = await (await fetch(`${base}/_stats`)).json();
    assert.equal(again.otlp, 0, 'the count resets with the read');
  } finally {
    await m.close();
  }
});
