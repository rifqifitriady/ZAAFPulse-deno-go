// scripts/smoke-binary.js checks that a single-file build's `doctor` read every IPC v1 schema. The count it expects
// must be the one lib/ipc/schema.js loads (the envelope and one per message type), whatever message types are added.
const test = require('node:test');
const assert = require('node:assert/strict');
const ipc = require('../lib/ipc/schema');
const { SCHEMA_COUNT, doctorHasSchemas } = require('../scripts/smoke-binary');

test('the smoke test expects exactly the schemas the IPC layer loads', () => {
  const loaded = Object.keys(ipc.schemas()).length;
  assert.equal(SCHEMA_COUNT, loaded);
  assert.equal(loaded, ipc.TYPES.length + 1);
  assert.ok(doctorHasSchemas(`✓ Gateway IPC      v1, ${loaded} schemas\n`));
  assert.ok(!doctorHasSchemas(`✓ Gateway IPC      v1, ${loaded - 1} schemas\n`));
  assert.ok(!doctorHasSchemas('✗ Gateway IPC      the ipc/v1 schemas cannot be read: ENOENT\n'));
});
