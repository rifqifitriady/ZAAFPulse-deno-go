// ipc/v1 (P3 T1): every fixture passes or fails as its name says under the strict validator, an unknown field passes
// the lenient one (D2), the validator implements its JSON Schema subset, and every schema file stays inside it.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const { DIR, TYPES, validate, unsupported, schemas, check } = require('../lib/ipc/schema');

const FIX = path.join(DIR, 'fixtures');
// Fixture folders that are not message types: envelope-level cases, and the budget decisions of Task 5.
const NOT_TYPES = new Set(['envelope', 'budget-decide']);
const read = (...p) => JSON.parse(fs.readFileSync(path.join(FIX, ...p), 'utf8'));

test('every message type has a schema and valid and invalid fixtures; every fixture folder is a type', () => {
  const folders = fs
    .readdirSync(FIX)
    .filter((d) => !NOT_TYPES.has(d))
    .sort();
  assert.deepEqual(folders, [...TYPES].sort());
  for (const t of TYPES) {
    const files = fs.readdirSync(path.join(FIX, t));
    assert.ok(
      files.some((f) => f.startsWith('valid')),
      `${t} has a valid fixture`,
    );
    assert.ok(
      files.some((f) => f.startsWith('invalid')),
      `${t} has an invalid fixture`,
    );
  }
  for (const d of fs.readdirSync(FIX).filter((x) => x !== 'budget-decide'))
    for (const f of fs.readdirSync(path.join(FIX, d)))
      assert.match(
        f,
        /^(valid|invalid)(-[a-z0-9-]+)?\.json$/,
        `${d}/${f} is named valid[-name].json or invalid[-name].json`,
      );
});

for (const folder of fs.readdirSync(FIX).filter((d) => d !== 'budget-decide')) {
  for (const file of fs.readdirSync(path.join(FIX, folder))) {
    test(`fixture ${folder}/${file}`, () => {
      const msg = read(folder, file);
      const r = check(msg);
      if (file.startsWith('valid')) {
        assert.deepEqual(r, { errors: [], unknownType: false });
        if (folder !== 'envelope') assert.equal(msg.type, folder);
        // Decoding and encoding again gives the same JSON value.
        assert.deepEqual(JSON.parse(JSON.stringify(msg)), msg);
      } else {
        assert.ok(r.errors.length > 0, `refused, got ${JSON.stringify(r)}`);
      }
      if (file.startsWith('invalid-unknown-field'))
        assert.deepEqual(
          check(msg, { lenient: true }),
          { errors: [], unknownType: false },
          'the lenient reader ignores an unknown field (D2)',
        );
    });
  }
}

test('validate: the JSON Schema subset', () => {
  const scope = { oneOf: [{ type: 'string', const: 'global' }, { type: 'object' }] };
  const cases = [
    [{ type: 'integer' }, 3, true],
    [{ type: 'integer' }, 3.5, false],
    [{ type: 'number' }, 3, true],
    [{ type: ['string', 'null'] }, null, true],
    [{ type: 'string' }, null, false],
    [{ const: 1 }, 1, true],
    [{ const: 1 }, 2, false],
    [{ enum: ['a', 'b'] }, 'c', false],
    [{ type: 'integer', minimum: 1 }, 0, false],
    [{ type: 'integer', maximum: 1 }, 2, false],
    [{ type: 'string', minLength: 1 }, '', false],
    [{ type: 'string', pattern: '^[0-9a-f]{2}$' }, 'AB', false],
    [{ type: ['string', 'null'], pattern: '^[0-9a-f]{2}$' }, null, true],
    [{ type: 'array', minItems: 1 }, [], false],
    [{ type: 'array', items: { type: 'string' } }, ['a', 1], false],
    [{ type: 'object', required: ['a'] }, {}, false],
    [{ type: 'object', properties: { a: { type: 'string' } }, additionalProperties: false }, { b: 1 }, false],
    [{ type: 'object', additionalProperties: { type: 'integer' } }, { x: 1, y: 'no' }, false],
    [scope, 'global', true],
    [scope, { dim: 'key' }, true],
    [scope, 'all', false],
    [{}, { anything: [1, 'x'] }, true],
  ];
  for (const [schema, value, ok] of cases)
    assert.equal(validate(schema, value).length === 0, ok, `${JSON.stringify(schema)} with ${JSON.stringify(value)}`);
  const closed = { type: 'object', properties: {}, additionalProperties: false };
  assert.deepEqual(validate(closed, { x: 1 }, { lenient: true }), [], 'lenient: an unknown field is ignored');
  assert.deepEqual(
    validate({ type: 'object', additionalProperties: { type: 'integer' } }, { y: 'no' }, { lenient: true }),
    ['$.y: must be integer'],
  );
});

test('every schema file is draft 2020-12 and uses only the keywords lib/ipc/schema.js implements', () => {
  for (const [name, s] of Object.entries(schemas())) {
    assert.equal(s.$schema, 'https://json-schema.org/draft/2020-12/schema', name);
    assert.deepEqual(unsupported(s), [], name);
  }
  assert.deepEqual(unsupported({ type: 'string', format: 'uri', properties: { a: { $ref: '#/x' } } }), [
    '$: format',
    '$.a: $ref',
  ]);
});

test('an unknown message type is reported, not refused (D2)', () => {
  assert.deepEqual(check({ v: 1, type: 'telemetry.v9', seq: 1, ts: 0, body: { any: 1 } }), {
    errors: [],
    unknownType: true,
  });
});
