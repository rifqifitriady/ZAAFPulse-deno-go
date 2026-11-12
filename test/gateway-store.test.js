// lib/gateway/store.js jsonStore: a debounced save that fails is logged, never thrown from its timer (P4b review m7:
// keys.authenticate and gateway.ingestUsage save lastUsedAt that way, and an exception there would end the process).
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const { jsonStore } = require('../lib/gateway/store');
const { tmpDir } = require('./ipc-stub');

test('a debounced save that fails is logged once and does not throw from its timer', async (t) => {
  const dir = tmpDir(t, 'json-store');
  fs.writeFileSync(path.join(dir, 'blocker'), ''); // a file where the store's folder should be: every write fails
  const logged = [];
  const s = jsonStore(path.join(dir, 'blocker', 'keys.json'), { keys: [] }, { log: (l) => logged.push(l) });
  s.get().keys.push({ id: 'k' });
  s.save();
  await new Promise((r) => setTimeout(r, 1300));
  s.save();
  await new Promise((r) => setTimeout(r, 1300));
  assert.equal(logged.length, 1);
  assert.match(logged[0], /^store: .*keys\.json could not be saved/);
  assert.throws(() => s.save(true), 'an immediate save still throws to its caller');
});
