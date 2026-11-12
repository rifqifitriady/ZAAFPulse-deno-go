// The provauth fixtures are generated from the JS token code (scripts/fixtures/gen-provauth.js) and replayed in Go
// (gw/provauth/fixtures_test.go). This is the drift check (P4 stage 1 spec, "Equivalence by generated fixtures"):
// regenerating them here, on Node and on Deno, must give the committed files byte for byte, so a change to
// lib/gateway/providers.js that changes a token request or result fails until the fixtures are regenerated and the Go
// port matches them again.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const { generate, DIR } = require('../scripts/fixtures/gen-provauth');

test('the provauth fixtures equal a fresh run of the JS token code (regenerate: node scripts/fixtures/gen-provauth.js)', async () => {
  const files = await generate();
  const onDisk = fs
    .readdirSync(DIR)
    .filter((f) => f.endsWith('.json'))
    .sort();
  assert.deepEqual(onDisk, [...files.keys()].sort(), 'the same files');
  for (const [name, text] of files) assert.equal(fs.readFileSync(path.join(DIR, name), 'utf8'), text, name);
});
