// Contract: the routing state shared by the engines (P4h spec Decision 13). Under ZP_ENGINE=go two servers run against
// one fake upstream: G (the gateway in front with chat and routes on the go engine and the messages classes on js) and
// J (no gateway, every call on js). Under ZP_ENGINE=shadow one server runs every class on js, shadowed.
//   (a) a go-served chat route call gets 429 from provider A; the next js-served /v1/messages route call tries B first;
//   (b) the reverse: a js-served call gets A's 429; the next go-served route call tries B first;
//   (c) the quota readings of GET /api/gateway after a go-served call equal the ones after the same call in js mode
//       (every field but the observer's clock `at`: the times are compared as offsets from it);
//   (d) a gateway restart keeps the cooldown and the rotation counter (route.state);
//   (e) round-robin rotation over consecutive go calls equals js mode;
//   shadow: the same calls end with the routes record at diff 0 and at least one `same`.
// The usage lines prove the provider order: a target that falls back has its own line, in the order tried.
// A replicated note is waited for by its effect, never by a fixed sleep: go to js, the cooldown in GET /api/gateway;
// js to go, a route save, which answers once the gateway applied the config generation it sent after the note (one
// ordered IPC channel, applied in order by the gateway's one reader).
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const { startServer, clientFor, PRICES, SEED_CONFIG } = require('./harness');
const { startUpstream } = require('./upstream');
const { GO_CLASSES } = require('../../lib/front');
const { killHard } = require('../ipc-stub');

const MODE = process.env.ZP_ENGINE;
const GO = MODE === 'go';
const SHADOW = MODE === 'shadow';
const skip = process.env.ZP_CONTRACT_URL
  ? 'needs servers it spawns (a gateway it can restart)'
  : !GO && !SHADOW && 'runs under ZP_ENGINE=go or ZP_ENGINE=shadow';

const BASE_CONFIG = { pricingSync: { enabled: false }, pricing: PRICES };
// Chat and routes (and every go class but the messages ones) on go; /v1/messages on js.
const MIXED_CONFIG = {
  ...BASE_CONFIG,
  gateway: {
    engine: 'front',
    engines: Object.fromEntries(GO_CLASSES.filter((c) => !c.startsWith('messages')).map((c) => [c, 'go'])),
    shadowDays: 0,
    shadowMinCompares: 0,
  },
};

// One OpenAI-format provider per letter, so every case has providers no other case touched.
const IDS = 'abcdefghijklmn'.split('').map((x) => `q${x}`);
const ROUTES = [
  { name: 'router/cool-a', targets: ['qa/limited', 'qb/m1'], strategy: 'fallback' },
  { name: 'router/cool-b', targets: ['qc/limited', 'qd/m1'], strategy: 'fallback' },
  { name: 'router/quota', targets: ['qe/limited', 'qf/metered'], strategy: 'fallback' },
  { name: 'router/rr', targets: ['qg/m1', 'qh/m1', 'qi/m1'], strategy: 'round-robin' },
  { name: 'router/rr-d', targets: ['qj/m1', 'qk/m1', 'ql/m1'], strategy: 'round-robin' },
  { name: 'router/cool-d', targets: ['qm/limited', 'qn/m1'], strategy: 'fallback' },
];

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
async function until(check, what, ms = 5000) {
  const end = Date.now() + ms;
  for (;;) {
    const v = await check().catch(() => null);
    if (v) return v;
    if (Date.now() > end) throw new Error(`timed out after ${ms} ms waiting for ${what}`);
    await sleep(25);
  }
}

let upstream;
const servers = {};
let barriers = 0;

// A server on `config` with the q* providers, the routes, guardrails off and a key.
async function boot(config) {
  const s = await startServer({ config });
  try {
    const c = clientFor(s.base);
    for (const id of IDS)
      await c.admin('providers', {
        action: 'save',
        id,
        preset: 'custom',
        baseUrl: upstream.url,
        tier: 'external',
        approved: true,
        models: ['m1', 'limited', 'metered'],
        secret: { mode: 'store', value: `sk-contract-${id}` },
      });
    for (const r of ROUTES) await c.admin('routes', { action: 'save', ...r });
    await c.setGuardrails({ scope: 'external', secrets: 'off', pii: 'off', injection: 'off' });
    const key = await c.newKey({ scopes: ['llm'] });
    return { s, c, key: key.secret };
  } catch (err) {
    await s.stop();
    throw err;
  }
}

test.before(async () => {
  if (skip) return;
  upstream = await startUpstream();
  if (GO) {
    servers.g = await boot(MIXED_CONFIG);
    servers.j = await boot(BASE_CONFIG);
    // G serves chat on go once its gateway is ready with the asked engines.
    await until(
      async () => {
        const st = await servers.g.c.call('/api/gateway/status', { method: 'GET' });
        return st.json && st.json.state === 'ready' && st.json.engines && st.json.engines.routes === 'go';
      },
      'the go engine on G',
      15000,
    );
  } else servers.s = await boot(SEED_CONFIG);
});
test.after(async () => {
  try {
    for (const x of Object.values(servers)) await x.s.stop();
  } finally {
    if (upstream) await upstream.close();
  }
});

const user = [{ role: 'user', content: 'hi' }];
const chat = (x, model) => x.c.call('/v1/chat/completions', { key: x.key, body: { model, messages: user } });
const messages = (x, model) =>
  x.c.call('/v1/messages', { key: x.key, body: { model, max_tokens: 16, messages: user } });

// The usage lines a call wrote, oldest first, once there are n of them (and no more).
async function linesOf(x, before, n) {
  const entries = await until(async () => {
    const e = await x.c.logs();
    return e.length >= before + n ? e : null;
  }, `${n} usage line(s)`);
  assert.equal(entries.length - before, n, `exactly ${n} line(s): ${JSON.stringify(entries.slice(0, 4))}`);
  return entries.slice(0, n).reverse();
}
const order = (lines) => lines.map((l) => [l.provider, l.status]);
const fromGo = (lines, go) => {
  for (const l of lines) assert.equal(!!l.ipc, go, `${go ? 'the go engine' : 'js'} wrote ${JSON.stringify(l)}`);
};

const quota = async (x) => (await x.c.call('/api/gateway', { method: 'GET' })).json.quota;
const quotaOf = async (x, id) => (await quota(x)).find((q) => q.id === id);
// js to go: a route save answers once the gateway applied the generation pushed after every earlier control message.
const barrier = (x) =>
  x.c.admin('routes', {
    action: 'save',
    name: `router/barrier-${++barriers}`,
    targets: ['qa/m1'],
    strategy: 'fallback',
  });

test(
  '(a) a go-served 429 from A: the next js-served /v1/messages route call tries B first',
  { skip: skip || (!GO && 'ZP_ENGINE=go only') },
  async () => {
    const g = servers.g;
    let before = (await g.c.logs()).length;
    const r = await chat(g, 'router/cool-a');
    assert.equal(r.status, 200);
    const first = await linesOf(g, before, 2);
    assert.deepEqual(order(first), [
      ['qa', 429],
      ['qb', 200],
    ]);
    fromGo(first, true);
    // The go engine's note reaches the control plane: A cools down there.
    const qa = await until(async () => {
      const q = await quotaOf(g, 'qa');
      return q && q.cooldown ? q : null;
    }, 'the cooldown of qa on the server');
    assert.equal(qa.status, 429);
    assert.equal(qa.retryAfterMs, 30000);
    before = (await g.c.logs()).length;
    const m = await messages(g, 'router/cool-a');
    assert.equal(m.status, 200, m.text);
    assert.equal(m.json.type, 'message');
    const next = await linesOf(g, before, 1);
    assert.deepEqual(order(next), [['qb', 200]]);
    fromGo(next, false);
  },
);

test(
  '(b) a js-served 429 from A: the next go-served route call tries B first',
  { skip: skip || (!GO && 'ZP_ENGINE=go only') },
  async () => {
    const g = servers.g;
    let before = (await g.c.logs()).length;
    const m = await messages(g, 'router/cool-b');
    assert.equal(m.status, 200, m.text);
    const first = await linesOf(g, before, 2);
    assert.deepEqual(order(first), [
      ['qc', 429],
      ['qd', 200],
    ]);
    fromGo(first, false);
    await barrier(g);
    before = (await g.c.logs()).length;
    const r = await chat(g, 'router/cool-b');
    assert.equal(r.status, 200);
    const next = await linesOf(g, before, 1);
    assert.deepEqual(order(next), [['qd', 200]]);
    fromGo(next, true);
  },
);

// A quota reading with every time as an offset from the observer's clock `at`.
function relative(q) {
  const at = q.at;
  const part = (x) =>
    x && { limit: x.limit, remaining: x.remaining, resetIn: x.resetAt == null ? null : x.resetAt - at };
  return {
    id: q.id,
    status: q.status,
    retryAfterMs: q.retryAfterMs,
    requests: part(q.requests),
    tokens: part(q.tokens),
    cooldown: q.cooldown && { in: q.cooldown.until - at, strikes: q.cooldown.strikes, reason: q.cooldown.reason },
    token: q.token,
    balance: q.balance,
  };
}

test(
  '(c) the quota readings after a go-served call equal the ones after the same call in js mode',
  { skip: skip || (!GO && 'ZP_ENGINE=go only') },
  async () => {
    const read = async (x, go) => {
      const before = (await x.c.logs()).length;
      const r = await chat(x, 'router/quota');
      assert.equal(r.status, 200);
      fromGo(await linesOf(x, before, 2), go);
      return until(async () => {
        const q = await quota(x);
        const e = q.find((y) => y.id === 'qe');
        const f = q.find((y) => y.id === 'qf');
        return e && e.at && e.cooldown && f && f.at ? [relative(e), relative(f)] : null;
      }, 'the quota readings of qe and qf');
    };
    const go = await read(servers.g, true);
    const js = await read(servers.j, false);
    assert.deepEqual(go, js);
    assert.deepEqual(js[0].requests, { limit: 60, remaining: 0, resetIn: 30000 });
    assert.deepEqual(js[0].cooldown, { in: 30000, strikes: 1, reason: 'HTTP 429' });
    assert.deepEqual(js[1].tokens, { limit: 90000, remaining: 88500, resetIn: 360000 });
  },
);

test(
  '(e) round-robin rotation over consecutive go calls equals js mode',
  { skip: skip || (!GO && 'ZP_ENGINE=go only') },
  async () => {
    const rotate = async (x, go) => {
      const out = [];
      for (let i = 0; i < 4; i++) {
        const before = (await x.c.logs()).length;
        assert.equal((await chat(x, 'router/rr')).status, 200);
        const [line] = await linesOf(x, before, 1);
        fromGo([line], go);
        out.push(line.provider);
      }
      return out;
    };
    const go = await rotate(servers.g, true);
    const js = await rotate(servers.j, false);
    assert.deepEqual(go, js);
    assert.deepEqual(js, ['qg', 'qh', 'qi', 'qg']);
  },
);

test(
  '(d) a gateway restart keeps the cooldown and the rotation counter',
  { skip: skip || (!GO && 'ZP_ENGINE=go only') },
  async () => {
    const g = servers.g;
    let before = (await g.c.logs()).length;
    assert.equal((await chat(g, 'router/rr-d')).status, 200);
    assert.deepEqual(order(await linesOf(g, before, 1)), [['qj', 200]]);
    before = (await g.c.logs()).length;
    assert.equal((await chat(g, 'router/cool-d')).status, 200);
    assert.deepEqual(order(await linesOf(g, before, 2)), [
      ['qm', 429],
      ['qn', 200],
    ]);
    // The note of qm's 429 went after the take of rr-d on one ordered channel: once the server shows the cooldown, it
    // holds both.
    await until(async () => {
      const q = await quotaOf(g, 'qm');
      return q && q.cooldown;
    }, 'the cooldown of qm on the server');
    const status = async () => (await g.c.call('/api/gateway/status', { method: 'GET' })).json;
    const old = await status();
    assert.equal(old.state, 'ready');
    killHard(old.pid);
    await until(
      async () => {
        const st = await status();
        return st.state === 'ready' && st.pid && st.pid !== old.pid ? st : null;
      },
      'the restarted gateway',
      30000,
    );
    before = (await g.c.logs()).length;
    // The main port may refuse for a moment after ready; a refused call reaches no provider.
    const r = await until(async () => {
      const x = await chat(g, 'router/cool-d');
      return x.status === 200 ? x : null;
    }, 'a call through the restarted gateway');
    assert.equal(r.status, 200);
    const cooled = await linesOf(g, before, 1);
    assert.deepEqual(order(cooled), [['qn', 200]], 'the new gateway backs off from qm');
    fromGo(cooled, true);
    before = (await g.c.logs()).length;
    assert.equal((await chat(g, 'router/rr-d')).status, 200);
    const rotated = await linesOf(g, before, 1);
    assert.deepEqual(order(rotated), [['qk', 200]], 'the new gateway goes on from the counter');
    fromGo(rotated, true);
  },
);

test(
  'shadow: the same route calls end with the routes record at diff 0 and at least one same',
  { skip: skip || (!SHADOW && 'ZP_ENGINE=shadow only') },
  async () => {
    const s = servers.s;
    const calls = [
      () => chat(s, 'router/cool-a'),
      () => messages(s, 'router/cool-a'),
      () => messages(s, 'router/cool-b'),
      () => chat(s, 'router/cool-b'),
      () => chat(s, 'router/quota'),
      ...Array.from({ length: 4 }, () => () => chat(s, 'router/rr')),
    ];
    for (const call of calls) {
      const r = await call();
      assert.equal(r.status, 200, r.text);
    }
    const st = await s.c.shadowStats('routes', calls.length);
    assert.ok(st && st.compared >= 1, `the shadow compared route calls: ${JSON.stringify(st)}`);
    assert.equal(st.diffs, 0);
    const lines = fs
      .readFileSync(path.join(s.s.data, '.data', 'gateway-shadow.jsonl'), 'utf8')
      .split('\n')
      .filter(Boolean)
      .map((l) => JSON.parse(l))
      .filter((e) => e.route === true);
    assert.ok(
      lines.some((e) => e.outcome === 'same'),
      `at least one same: ${JSON.stringify(lines.map((e) => [e.class, e.outcome, e.reason]))}`,
    );
    assert.ok(!lines.some((e) => e.outcome === 'diff'), JSON.stringify(lines.filter((e) => e.outcome === 'diff')));
  },
);
