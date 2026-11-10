// lib/money.js: integer nano-USD for the IPC (P0 D3) and the shared 1e-8 rounding of stored amounts.
const test = require('node:test');
const assert = require('node:assert/strict');
const { toNano, fromNano, addNano, round8, MAX_USD } = require('../lib/money');

test('toNano/fromNano: typical token costs round-trip exactly', () => {
  // 1000 input + 500 output tokens at $1/$2 per million; one token at $3 per million; a $15 Opus answer.
  for (const [usd, nano] of [
    [0.002, 2_000_000],
    [0.000003, 3_000],
    [0.000014, 14_000],
    [15, 15_000_000_000],
    [0, 0],
  ]) {
    assert.equal(toNano(usd), nano, `toNano(${usd})`);
    assert.equal(fromNano(nano), usd, `fromNano(${nano})`);
  }
  assert.equal(toNano(1000 * (1 / 1e6) + 500 * (2 / 1e6)), 2_000_000, 'the gateway pricer sum');
});

test('toNano: the float error of 0.55 * 100 and of 0.1 + 0.2 is rounded away', () => {
  assert.equal(0.55 * 100, 55.00000000000001);
  assert.equal(toNano(0.55 * 100), 55_000_000_000);
  assert.equal(toNano(0.1 + 0.2), 300_000_000);
  assert.equal(addNano(toNano(0.1), toNano(0.2)), toNano(0.3));
});

test('toNano: sub-micro amounts are kept, which micro-USD would lose', () => {
  assert.equal(Math.round(2e-7 * 1e6), 0, 'micro-USD loses US$0.0000002');
  assert.equal(toNano(2e-7), 200);
  assert.equal(toNano(1e-9), 1);
  assert.equal(toNano(4.9e-10), 0, 'below half a nano-USD rounds to 0');
  assert.equal(Object.is(toNano(-0), 0), true, 'no -0');
  assert.equal(toNano(-0.002), -2_000_000, 'a credit is negative');
});

test('range: about US$9 million is the limit; non-finite input and unsafe integers throw', () => {
  assert.equal(toNano(MAX_USD), Number.MAX_SAFE_INTEGER);
  assert.throws(() => toNano(9_007_199.254740992), RangeError);
  assert.throws(() => toNano(-1e7), RangeError);
  for (const bad of [NaN, Infinity, -Infinity, '1', null, undefined]) assert.throws(() => toNano(bad), RangeError);
  for (const bad of [0.5, NaN, Number.MAX_SAFE_INTEGER + 1, '5']) assert.throws(() => fromNano(bad), RangeError);
  assert.equal(addNano(), 0);
  assert.equal(addNano(1, 2, 3), 6);
  assert.throws(() => addNano(Number.MAX_SAFE_INTEGER, 1), RangeError);
  assert.throws(() => addNano(1, 0.5), RangeError);
});

test('round8: the 1e-8 rounding the cost files store, unchanged', () => {
  assert.equal(round8(0.55 * 100), 55);
  assert.equal(round8(0.123456789), 0.12345679);
  assert.equal(round8(1e-9), 0);
  assert.equal(round8(0.1 + 0.2), 0.3);
  for (const v of [0, 0.002, 1 / 3, 12345.6789012345, -0.000000015])
    assert.equal(round8(v), Math.round(v * 1e8) / 1e8, `round8(${v})`);
});
