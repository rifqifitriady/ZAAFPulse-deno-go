// lib/platform.js: which runtime runs this code, and whether it is a single-file build (P2 T1).
const test = require('node:test');
const assert = require('node:assert/strict');
const platform = require('../lib/platform');

test('runtime() names the runtime running the tests', () => {
  assert.equal(platform.runtime(), process.versions.deno ? 'deno' : 'node');
  assert.equal(platform.runtime({ versions: { node: '24.0.0', deno: '2.9.8' } }), 'deno');
  assert.equal(platform.runtime({ versions: { node: '24.0.0' } }), 'node');
});

test('isSingleFile() is false when the tests run from source', () => {
  assert.equal(platform.isSingleFile(), false);
});

test('isSingleFile() reads Deno.build.standalone under Deno and node:sea under Node', () => {
  assert.equal(platform.isSingleFile({ deno: { build: { standalone: true } } }), true);
  assert.equal(platform.isSingleFile({ deno: { build: { standalone: false } } }), false);
  assert.equal(platform.isSingleFile({ deno: null, sea: { isSea: () => true } }), true);
  assert.equal(platform.isSingleFile({ deno: null, sea: { isSea: () => false } }), false);
  assert.equal(
    platform.isSingleFile({
      deno: null,
      sea: {
        isSea: () => {
          throw new Error('no node:sea');
        },
      },
    }),
    false,
  );
});

test('isSea() is gone: every caller asks isSingleFile()', () => {
  assert.equal(platform.isSea, undefined);
});
