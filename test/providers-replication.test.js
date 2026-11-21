// The routing state replicated between the engines (P4h Decisions 3, 4): a sequence of provider answers and round-robin
// calls applied on one createProviders, and as the provider.note and route.take bodies it emits on a second, leaves
// both with the same quota readings, cooldowns and rotation counters; nothing applied from the other side is told back.
const test = require('node:test');
const assert = require('node:assert/strict');
const { createProviders, noteHeaders, NOTE_HEADERS } = require('../lib/gateway/providers');

const T0 = 1767225600000;
const PROVIDERS = [
  { id: 'a', preset: 'openai', baseUrl: 'https://a.example.test/v1', approved: true },
  { id: 'b', preset: 'groq', baseUrl: 'https://b.example.test/v1', approved: true },
  { id: 'c', preset: 'anthropic', baseUrl: 'https://c.example.test/v1', approved: true, format: 'anthropic' },
];
const ROUTES = [
  { name: 'router/rr', targets: ['a/m', 'b/m', 'c/m'], strategy: 'round-robin' },
  { name: 'router/two', targets: ['a/m', 'b/m'], strategy: 'round-robin' },
  { name: 'router/fb', targets: ['a/m', 'b/m'], strategy: 'fallback' },
];

function machine() {
  let clock = T0;
  let answer = null;
  const providers = createProviders({
    getConfig: () => ({ gateway: { providers: PROVIDERS, routes: ROUTES } }),
    secrets: { resolve: () => null, mask: (s) => s },
    fetchImpl: async () => new Response(null, answer),
    now: () => clock,
  });
  const told = [];
  providers.onNote((b) => told.push(['provider.note', b]));
  providers.onTake((b) => told.push(['route.take', b]));
  return {
    providers,
    told,
    setClock: (t) => (clock = t),
    async answer(id, status, headers) {
      answer = { status, headers };
      await providers.request(
        providers.list().find((p) => p.id === id),
        '/chat/completions',
        { body: {} },
      );
    },
  };
}

const STEPS = [
  { at: T0, call: 'router/rr' },
  {
    at: T0,
    id: 'a',
    status: 200,
    headers: { 'x-ratelimit-limit-requests': '60', 'x-ratelimit-remaining-requests': '59' },
  },
  { at: T0 + 10, call: 'router/rr' },
  {
    at: T0 + 20,
    id: 'b',
    status: 429,
    headers: { 'x-ratelimit-remaining-tokens': '0', 'x-ratelimit-reset-tokens': '1.0005', 'content-type': 'x' },
  },
  { at: T0 + 30, id: 'b', status: 429, headers: {} },
  { at: T0 + 40, id: 'c', status: 529, headers: { 'retry-after': '3', 'anthropic-ratelimit-tokens-remaining': '0' } },
  { at: T0 + 50, call: 'router/two' },
  { at: T0 + 50, call: 'router/fb' },
  { at: T0 + 60, id: 'a', status: 503, headers: { 'x-ratelimit-reset-requests': '6m0s' } },
  { at: T0 + 70, call: 'router/rr' },
  { at: T0 + 2000, id: 'b', status: 200, headers: {} },
  { at: T0 + 2010, call: 'router/two' },
  { at: T0 + 2020, id: 'c', status: 401, headers: { 'retry-after': '2' } },
];

test('a sequence applied locally and as notes and takes on a second instance gives the same state, with no echo', async () => {
  const one = machine();
  const two = machine();
  for (const step of STEPS) {
    one.setClock(step.at);
    if (step.call) one.providers.resolveAll(step.call);
    else await one.answer(step.id, step.status, step.headers);
  }
  // One note per provider answer, one take per round-robin call of more than one target (router/fb takes nothing).
  assert.equal(one.told.filter(([t]) => t === 'provider.note').length, 7);
  assert.equal(one.told.filter(([t]) => t === 'route.take').length, 5);
  assert.deepEqual(one.told[0], ['route.take', { name: 'router/rr', at: T0 }]);
  assert.deepEqual(one.told[3][1], {
    providerId: 'b',
    status: 429,
    at: T0 + 20,
    headers: { 'x-ratelimit-remaining-tokens': '0', 'x-ratelimit-reset-tokens': '1.0005' },
  });
  // The second instance hears them with another clock: the note's `at` is what counts.
  two.setClock(T0 + 999999);
  for (const [type, body] of one.told)
    assert.equal(type === 'provider.note' ? two.providers.applyNote(body) : two.providers.applyTake(body), true);
  assert.deepEqual(two.told, [], 'nothing applied from the other side is told back');
  for (const m of [one, two]) m.setClock(T0 + 2020);
  assert.deepEqual(two.providers.quota(), one.providers.quota());
  assert.deepEqual(two.providers.routeState(), one.providers.routeState());
  assert.deepEqual(one.providers.routeState(), {
    rr: [
      { name: 'router/rr', n: 3 },
      { name: 'router/two', n: 2 },
    ],
    cooldown: [
      { providerId: 'c', until: T0 + 40 + 3000, strikes: 1, reason: 'HTTP 529' },
      { providerId: 'a', until: T0 + 60 + 1000, strikes: 1, reason: 'HTTP 503' }, // expired, kept with its strikes
    ],
  });
  // The next takes continue alike on both sides.
  assert.deepEqual(
    one.providers.resolveAll('router/rr').map((t) => t.name),
    two.providers.resolveAll('router/rr').map((t) => t.name),
  );
});

test('routeState carries every cooldown entry, expired ones too, with the fractional until of a fractional reset', async () => {
  const m = machine();
  m.setClock(T0);
  await m.answer('a', 429, { 'x-ratelimit-remaining-requests': '0', 'x-ratelimit-reset-requests': '1.0005' });
  await m.answer('b', 503, {});
  const both = [
    { providerId: 'a', until: T0 + 1000.5, strikes: 1, reason: 'HTTP 429' },
    { providerId: 'b', until: T0 + 1000, strikes: 1, reason: 'HTTP 503' },
  ];
  assert.deepEqual(m.providers.routeState().cooldown, both);
  m.setClock(T0 + 5000);
  assert.deepEqual(m.providers.routeState().cooldown, both, 'expired entries keep their strikes until a 2xx');
  await m.answer('b', 200, {});
  assert.deepEqual(m.providers.routeState().cooldown, [both[0]]);
});

// JS side of the seed case gw/engine/routes_test.go TestRouterSeedExpiredStrikes replays: three 429s, the cooldown
// expires, route.state still carries strikes 3, and the next 429 waits 1000 * 2^3 ms.
test('an expired entry seeds its strikes: the next 429 doubles from them (JS side of the Go seed case)', async () => {
  const m = machine();
  for (const at of [T0, T0 + 10, T0 + 20]) {
    m.setClock(at);
    await m.answer('a', 429, {});
  }
  m.setClock(T0 + 100000);
  assert.equal(m.providers.quota().find((q) => q.id === 'a').cooldown, null, 'expired');
  assert.deepEqual(m.providers.routeState().cooldown, [
    { providerId: 'a', until: T0 + 20 + 4000, strikes: 3, reason: 'HTTP 429' },
  ]);
  await m.answer('a', 429, {});
  assert.deepEqual(m.providers.routeState().cooldown, [
    { providerId: 'a', until: T0 + 100000 + 8000, strikes: 4, reason: 'HTTP 429' },
  ]);
});

test('applyNote reads the headers as Go joins them; bad bodies and takes of other routes change nothing', () => {
  const m = machine();
  assert.equal(
    m.providers.applyNote({ providerId: 'a', status: 429, at: T0, headers: { 'retry-after': '3, 4' } }),
    true,
  );
  // "3, 4" is not seconds; resetIn sums nothing from it: null, so the back-off is 1000 * 2^0.
  assert.deepEqual(m.providers.routeState().cooldown, [
    { providerId: 'a', until: T0 + 1000, strikes: 1, reason: 'HTTP 429' },
  ]);
  for (const bad of [null, {}, { providerId: '', status: 429, at: T0 }, { providerId: 'a', status: '429', at: T0 }])
    assert.equal(m.providers.applyNote(bad), false);
  for (const bad of [null, {}, { name: 'router/fb', at: T0 }, { name: 'router/none', at: T0 }])
    assert.equal(m.providers.applyTake(bad), false);
  assert.deepEqual(m.providers.routeState().rr, []);
  assert.deepEqual(m.told, []);
});

test('noteHeaders keeps the 13 names noteResponse reads, as Headers.get gives them', () => {
  assert.equal(NOTE_HEADERS.length, 13);
  const h = new Headers({ 'Retry-After': '3', 'X-Other': 'x' });
  h.append('x-ratelimit-limit-requests', '1');
  h.append('x-ratelimit-limit-requests', '2');
  assert.deepEqual(noteHeaders(h), { 'retry-after': '3', 'x-ratelimit-limit-requests': '1, 2' });
});
