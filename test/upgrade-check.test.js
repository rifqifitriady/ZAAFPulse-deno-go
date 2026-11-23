// scripts/upgrade-check.js (P6 Decision 12): the arguments and the config.json compare. Pure: starts nothing; the proof
// itself is a run of the script.
const test = require('node:test');
const assert = require('node:assert/strict');
const { parseArgs, changedKeys } = require('../scripts/upgrade-check');

test('--v2 is required; --v1 is optional; anything else is refused', () => {
  assert.deepEqual(parseArgs(['--v2', 'deno-run']), { v2: 'deno-run', v1: null });
  assert.deepEqual(parseArgs(['--v1', '/x/v1', '--v2', '/x/zaafpulse']), { v2: '/x/zaafpulse', v1: '/x/v1' });
  assert.throws(() => parseArgs([]), /usage/);
  assert.throws(() => parseArgs(['--v1', '/x']), /usage/);
  assert.throws(() => parseArgs(['--v2']), /--v2 needs a value/);
  assert.throws(() => parseArgs(['--v2', '--v1', '/x']), /--v2 needs a value/);
  assert.throws(() => parseArgs(['--v2', 'deno-run', 'extra']), /unknown argument: extra/);
});

test('changedKeys walks v1 keys only: a key v2 adds is fine, a changed or missing one is named', () => {
  const v1 = {
    userName: 'a',
    pricingSync: { enabled: false },
    vaultPaths: ['/v'],
    gateway: { providers: [{ id: 'p' }] },
  };
  assert.deepEqual(
    changedKeys(v1, {
      ...v1,
      gateway: { ...v1.gateway, engine: 'front' },
      governance: { audit: { retentionMonths: 18 } },
    }),
    [],
  );
  assert.deepEqual(changedKeys(v1, { ...v1, userName: 'b', pricingSync: {} }), [
    'userName: "a" -> "b"',
    'pricingSync.enabled: missing',
  ]);
  assert.deepEqual(changedKeys(v1, { ...v1, vaultPaths: ['/v', '/w'] }), ['vaultPaths: ["/v"] -> ["/v","/w"]']);
  assert.deepEqual(changedKeys(v1, { ...v1, gateway: 'x' }), ['gateway: {"providers":[{"id":"p"}]} -> "x"']);
});
