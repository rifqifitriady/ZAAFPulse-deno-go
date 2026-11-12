// The budget decision of the IPC (P3 T5): ipc/v1/fixtures/budget-decide decided by lib/ipc/budget.js, which asks the
// product's own guard (lib/cost/guard.js). gw/ipc/budget_test.go runs the same files against the Go decision.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const { decide, STALE_AFTER_MS } = require('../lib/ipc/budget');
const { DIR, validate, schemas } = require('../lib/ipc/schema');

const FIX = path.join(DIR, 'fixtures', 'budget-decide');

test('the fixtures cover pass, refuse and unchecked, and every snapshot is a valid budget.snapshot body', () => {
  const files = fs.readdirSync(FIX);
  assert.ok(files.length >= 20, `${files.length} fixtures`);
  for (const action of ['pass', 'refuse', 'unchecked'])
    assert.ok(
      files.some((f) => f.startsWith(`${action}-`)),
      action,
    );
  for (const f of files) {
    const fx = JSON.parse(fs.readFileSync(path.join(FIX, f), 'utf8'));
    assert.deepEqual(Object.keys(fx).sort(), ['expect', 'input', 'now', 'snapshot']);
    if (fx.snapshot) assert.deepEqual(validate(schemas()['budget.snapshot'], fx.snapshot), [], f);
  }
  assert.equal(STALE_AFTER_MS, 5000);
});

for (const f of fs.readdirSync(FIX)) {
  test(`budget-decide/${f}`, () => {
    const fx = JSON.parse(fs.readFileSync(path.join(FIX, f), 'utf8'));
    assert.deepEqual(decide(fx.snapshot, fx.now, fx.input), fx.expect);
    assert.ok(f.startsWith(`${fx.expect.action}-`), 'the file name starts with the expected action');
  });
}
