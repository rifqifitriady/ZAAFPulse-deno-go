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

test('isSingleFile() reads Deno.build.standalone, and is false without Deno', () => {
  assert.equal(platform.isSingleFile({ deno: { build: { standalone: true } } }), true);
  assert.equal(platform.isSingleFile({ deno: { build: { standalone: false } } }), false);
  assert.equal(platform.isSingleFile({ deno: { build: {} } }), false);
  assert.equal(platform.isSingleFile({ deno: null }), false);
});

// The Node SEA build went away in P2 (P7 remove-node Decision 3): no single-file check asks node:sea any more, so a
// Node run of this tree is never taken for a single-file build.
test('no single-file check asks node:sea', () => {
  const fs = require('node:fs');
  const path = require('node:path');
  for (const f of ['lib/platform.js', 'lib/front/bin.js'])
    assert.doesNotMatch(fs.readFileSync(path.join(__dirname, '..', f), 'utf8'), /require\(['"]node:sea['"]\)/, f);
});

test('isSea() is gone: every caller asks isSingleFile()', () => {
  assert.equal(platform.isSea, undefined);
});

// Deno asks sys access for uid, gid and userInfo on os.userInfo(), which fullName() and the service setup call.
test('os.userInfo() answers under the permission set the tests run with', () => {
  assert.equal(typeof require('os').userInfo().username, 'string');
});
