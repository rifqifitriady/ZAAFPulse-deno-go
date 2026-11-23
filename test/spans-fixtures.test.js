// The span fixtures are generated from lib/otel.js (scripts/fixtures/gen-spans.js) and replayed in Go
// (gw/engine/spans_test.go). This is the drift check: regenerating them, on Node and on Deno, must give the committed
// files byte for byte, so a change to the gateway's spans fails until the fixtures are regenerated and the Go port
// matches them again (P4f D2).
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const { generate } = require('../scripts/fixtures/gen-spans');

const DIR = path.join(__dirname, '..', 'gw', 'testdata', 'spans');

test('the span fixtures equal a fresh run of lib/otel.js (regenerate: node scripts/fixtures/gen-spans.js)', async () => {
  const before = new Map(
    fs
      .readdirSync(DIR)
      .filter((f) => f.endsWith('.json'))
      .map((f) => [f, fs.readFileSync(path.join(DIR, f), 'utf8')]),
  );
  const files = await generate();
  assert.deepEqual([...files.keys()].sort(), [...before.keys()].sort(), 'the same files');
  for (const [name, text] of files) assert.equal(text, before.get(name), name);
});
