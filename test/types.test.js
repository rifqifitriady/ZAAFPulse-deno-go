// lib/types.js (P0 T2): the IPC typedefs exist, have no runtime code, and each is used by the code that produces it;
// every file of the three type-checked areas opts in with // @ts-check.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');

const ROOT = path.join(__dirname, '..');
const read = (f) => fs.readFileSync(path.join(ROOT, f), 'utf8');
// Each typedef and the file that produces that shape (its JSDoc names the type).
const PRODUCERS = [
  ['Money', 'lib/money.js'],
  ['UsageRecord', 'lib/gateway/usage.js'],
  ['GuardrailOutcome', 'lib/gateway/index.js'],
  ['GuardrailHit', 'lib/gateway/guardrails.js'],
  ['GuardrailsPolicy', 'lib/gateway/guardrails.js'],
  ['AuditEvent', 'lib/gateway/index.js'],
  ['GatewayConfigSection', 'lib/gateway/index.js'],
  ['GatewayProviderConfig', 'lib/gateway/providers.js'],
  ['GatewayRouteConfig', 'lib/gateway/providers.js'],
  ['GatewayKeyConfig', 'lib/gateway/keys.js'],
  ['BudgetCheckInput', 'lib/cost/guard.js'],
  ['BudgetRefusal', 'lib/cost/guard.js'],
  ['BudgetView', 'lib/cost/budget-service.js'],
];
// Typedefs that are parts of others, or whose producer comes with the IPC itself (P3).
const NO_PRODUCER_YET = ['AuditTarget', 'BudgetSnapshot'];

test('lib/types.js holds typedefs only', () => {
  assert.deepEqual(require('../lib/types'), {});
  const names = [...read('lib/types.js').matchAll(/@typedef \{[^}]*\} (\w+)/g)].map((m) => m[1]).sort();
  assert.deepEqual(names, [...PRODUCERS.map(([n]) => n), ...NO_PRODUCER_YET].sort());
});

test('each IPC shape is named by the JSDoc of its producer', () => {
  for (const [name, file] of PRODUCERS)
    assert.match(read(file), new RegExp(`import\\('\\.\\.?/types'\\)\\.${name}\\b`), `${name} in ${file}`);
});

test('every file of lib/gateway, lib/cost and lib/governance starts with // @ts-check', () => {
  for (const dir of ['lib/gateway', 'lib/cost', 'lib/governance'])
    for (const f of fs.readdirSync(path.join(ROOT, dir)).filter((x) => x.endsWith('.js')))
      assert.ok(read(`${dir}/${f}`).startsWith('// @ts-check\n'), `${dir}/${f}`);
});
