// @ts-check
// Money as integer nano-USD (1e-9 USD) for the control-plane ⇄ gateway IPC and the Go gateway (P0 design D3), and the
// 1e-8 USD rounding every stored cost amount already uses. Pure: no I/O, no clock. The stored files keep their float
// USD fields; toNano/fromNano convert at the IPC boundary. Number.MAX_SAFE_INTEGER nano-USD is about US$9 million.

const NANO = 1e9;
// The largest amount in USD that converts to a safe integer.
const MAX_USD = Number.MAX_SAFE_INTEGER / NANO;

/**
 * A finite amount in USD as a whole number of nano-USD (Math.round(usd * 1e9)). Throws a RangeError on a value that is
 * not a finite number, or whose nano-USD is not a safe integer.
 * @param {number} usd
 * @returns {import('./types').Money}
 */
function toNano(usd) {
  if (typeof usd !== 'number' || !Number.isFinite(usd)) throw new RangeError(`toNano: not a finite number: ${usd}`);
  const n = Math.round(usd * NANO);
  if (!Number.isSafeInteger(n)) throw new RangeError(`toNano: ${usd} USD is out of range (at most ${MAX_USD})`);
  return n === 0 ? 0 : n; // no -0
}

/**
 * A whole number of nano-USD as USD. Throws a RangeError on anything but a safe integer.
 * @param {import('./types').Money} n
 * @returns {number}
 */
function fromNano(n) {
  if (!Number.isSafeInteger(n)) throw new RangeError(`fromNano: not a safe integer: ${n}`);
  return n / NANO;
}

/**
 * The sum of nano-USD amounts. Throws a RangeError on an addend that is not a safe integer, or a sum that is not one.
 * @param {...import('./types').Money} ns
 * @returns {import('./types').Money}
 */
function addNano(...ns) {
  let sum = 0;
  for (const n of ns) {
    if (!Number.isSafeInteger(n)) throw new RangeError(`addNano: not a safe integer: ${n}`);
    sum += n;
    if (!Number.isSafeInteger(sum)) throw new RangeError('addNano: the sum is out of range');
  }
  return sum;
}

/**
 * A USD amount rounded to 1e-8, as every stored cost amount is (the ledger, budgets, previews, reports).
 * @param {number} usd
 * @returns {number}
 */
const round8 = (usd) => Math.round(usd * 1e8) / 1e8;

module.exports = { toNano, fromNano, addNano, round8, MAX_USD };
