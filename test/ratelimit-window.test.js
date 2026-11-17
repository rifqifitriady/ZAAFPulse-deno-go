// One requests-per-minute window per key across the engines (P4f D16): each side counts the takes it admits and the
// takes the other side tells it about; a refused request is not a take, and only an admitted one is told.
const test = require('node:test');
const assert = require('node:assert/strict');
const { createKeys } = require('../lib/gateway/keys');

function store(keys = []) {
  const data = { keys };
  return { get: () => data, save: () => {} };
}

test('a take noted from the other engine counts in the window, in time order (P4f D16)', () => {
  let t = 1_000_000;
  const told = [];
  const keys = createKeys(store(), { now: () => t, onTake: (id, at) => told.push([id, at]) });
  const k = { id: 'k1', rpm: 3 };
  assert.equal(keys.rateLimit(k), 0);
  keys.note('k1', t + 5); // arrives first, taken later
  keys.note('k1', t - 10); // the go engine admitted one 10 ms earlier
  t += 20;
  assert.equal(keys.rateLimit(k), 60, 'three in the last minute: refused; the oldest is 30 ms old');
  assert.deepEqual(told, [['k1', 1_000_000]], 'only its own admitted take is told');
  assert.deepEqual(keys.windows(), [{ keyId: 'k1', at: [999_990, 1_000_000, 1_000_005] }]);
});

test('windows() keeps the newest rpm takes per known key and drops takes older than a minute (P4f D16)', () => {
  let t = 0;
  const keys = createKeys(store([{ id: 'k', rpm: 2 }]), { now: () => t });
  for (let i = 0; i < 5; i++) keys.note('k', 100 + i);
  assert.deepEqual(keys.windows(), [{ keyId: 'k', at: [103, 104] }], 'the newest rpm');
  t = 100 + 60_000 + 3; // every take but 104 is a minute old or older
  assert.deepEqual(keys.windows(), [{ keyId: 'k', at: [104] }]);
  keys.note('x', Number.NaN);
  keys.note(7, 1);
  assert.deepEqual(
    keys.windows().map((w) => w.keyId),
    ['k'],
    'a bad take is ignored',
  );
});

test('an onTake that throws never refuses a request', () => {
  const keys = createKeys(store(), {
    now: () => 5,
    onTake: () => {
      throw new Error('ipc down');
    },
  });
  assert.equal(keys.rateLimit({ id: 'k', rpm: 1 }), 0);
});
