// lib/ipc/framing.js (P3 D2): lines across chunks, a character cut between chunks, one \r removed, empty lines
// skipped, the 8 MiB limit (here a small one), and a stream that ends inside a line.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const { createLineReader, MAX_LINE } = require('../lib/ipc/framing');
const { DIR, check } = require('../lib/ipc/schema');

function reader(maxBytes) {
  const lines = [];
  const errors = [];
  const r = createLineReader({ maxBytes, onLine: (l) => lines.push(l), onError: (e) => errors.push(e.message) });
  return { r, lines, errors };
}

test('the limit is 8 MiB', () => {
  assert.equal(MAX_LINE, 8 * 1024 * 1024);
});

test('lines split across chunks, several in one chunk, and a UTF-8 character cut in two', () => {
  const { r, lines, errors } = reader();
  const text = Buffer.from('{"a":"é"}\n{"b":2}\n{"c":3}\n');
  r.push(text.subarray(0, 7)); // ends inside é
  r.push(text.subarray(7, 15));
  r.push(text.subarray(15));
  assert.deepEqual(lines, ['{"a":"é"}', '{"b":2}', '{"c":3}']);
  assert.deepEqual(errors, []);
});

test('one trailing \\r is removed and an empty line is skipped (ipc/v1/framing/crlf.ndjson)', () => {
  const { r, lines } = reader();
  r.push(fs.readFileSync(path.join(DIR, 'framing', 'crlf.ndjson')));
  r.push('\n\r\nx\r\r\n');
  assert.equal(lines.length, 3);
  for (const l of lines.slice(0, 2)) assert.deepEqual(check(JSON.parse(l), {}), { errors: [], unknownType: false });
  assert.equal(lines[2], 'x\r', 'only one \\r is removed');
});

test('a line of exactly the limit passes; one byte more is an error and the reader stops', () => {
  const ok = reader(10);
  ok.r.push('0123456789\n');
  assert.deepEqual(ok.lines, ['0123456789']);
  const over = reader(10);
  over.r.push('01234567890\n{"after":1}\n');
  assert.deepEqual(over.lines, []);
  assert.equal(over.errors.length, 1);
});

test('a line over the limit is refused before its newline arrives', () => {
  const { r, errors } = reader(10);
  r.push('012345');
  r.push('67890');
  assert.equal(errors.length, 1);
});

test('the stream ending inside a line is an error; a clean end is not', () => {
  const cut = reader();
  cut.r.push('{"a":1}\n{"b"');
  cut.r.end();
  assert.equal(cut.errors.length, 1);
  const clean = reader();
  clean.r.push('{"a":1}\n');
  clean.r.end();
  assert.deepEqual(clean.errors, []);
});
