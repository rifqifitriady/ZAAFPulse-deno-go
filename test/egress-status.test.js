// GET /api/egress's body (lib/egress-status.js, P5 Decisions 10, 18): the newest 50 records, the last 24 h per class
// (purposes grouped by class), refusals, the allow-list's mode and size, the lock-down as this launch runs it; cached
// 10 s, so the log is read at most once per window.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const os = require('os');
const path = require('path');
const { createEgressLog } = require('../lib/egress-log');
const { createEgressStatus, countsByClass, lockdownNow, CACHE_MS } = require('../lib/egress-status');

const NOW = Date.UTC(2026, 10, 22, 12);
const rec = (ts, purpose, outcome) => ({
  ts,
  purpose,
  method: 'GET',
  scheme: 'https',
  host: 'api.example.com',
  port: 443,
  outcome,
  bytesOut: 0,
  bytesIn: 0,
  ms: 1,
  via: 'proxy',
});

test('the body: last 50 newest first, 24 h counts per class, the allow-list mode and size; cached 10 s', (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'egress-status-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const log = createEgressLog({ dir });
  log.append(rec(NOW - 2 * 864e5, 'gateway', 'ok')); // older than 24 h
  for (let i = 0; i < 60; i++) log.append(rec(NOW - 1000 + i, 'connectors:dynatrace', 'ok'));
  log.append(rec(NOW - 10, 'connectors:n8n', 'would-refuse'));
  log.append(rec(NOW - 5, 'brain', 'refused'));
  let now = NOW;
  let reads = 0;
  const counted = { tail: (n) => (reads++, log.tail(n)), counts: (s) => log.counts(s) };
  const read = createEgressStatus({
    log: counted,
    now: () => now,
    state: () => ({
      mode: 'front',
      port: 5555,
      engines: { gateway: 'go' },
      notes: {},
      allow: [
        { host: 'a.example.com', port: 443 },
        { host: '*.b.example.com', port: 443 },
      ],
      allowList: 'bogus',
      lockdown: { asked: false, inForce: false, why: 'egress.lockdown is off' },
    }),
  });
  const b = read();
  assert.deepEqual(Object.keys(b), [
    'mode',
    'port',
    'engines',
    'notes',
    'allowList',
    'allowCount',
    'allow',
    'lockdown',
    'last',
    'counts24h',
  ]);
  assert.equal(b.allowList, 'report', 'anything but enforce reports');
  assert.equal(b.allowCount, 2);
  assert.equal(b.last.length, 50);
  assert.equal(b.last[0].purpose, 'brain', 'newest first');
  assert.deepEqual(b.counts24h.perClass.connectors, {
    ok: 60,
    error: 0,
    refused: 0,
    'would-refuse': 1,
    checked: 0,
    total: 61,
  });
  assert.equal(b.counts24h.perClass.gateway, undefined, 'older than 24 h');
  assert.deepEqual([b.counts24h.total, b.counts24h.refused, b.counts24h.wouldRefuse], [62, 1, 1]);
  now += CACHE_MS - 1;
  assert.equal(read(), b);
  assert.equal(reads, 1, 'cached');
  now += 1;
  assert.notEqual(read(), b);
  assert.equal(reads, 2);
});

test('countsByClass of nothing; the lock-down as this launch runs it', () => {
  assert.deepEqual(countsByClass({}), { perClass: {}, total: 0, refused: 0, wouldRefuse: 0 });
  const met = { asked: true, inForce: true, why: '' };
  const deno = { runtime: 'deno', grants: { net: 'scoped' } };
  assert.deepEqual(lockdownNow(met, deno), { asked: true, inForce: true, why: '' });
  assert.deepEqual(lockdownNow({ asked: false, inForce: false, why: 'egress.lockdown is off' }, deno), {
    asked: false,
    inForce: false,
    why: 'egress.lockdown is off',
  });
  assert.match(lockdownNow(met, { runtime: 'node' }).why, /Node\.js/);
  assert.match(lockdownNow(met, { ...deno, singleFile: true }).why, /single-file build/);
  const wide = lockdownNow(met, { runtime: 'deno', grants: { net: 'all but denied' } });
  assert.equal(wide.inForce, false);
  assert.match(wide.why, /not launched with the narrowed net grant/);
  assert.equal(lockdownNow(null, deno).why, 'not asked');
});
