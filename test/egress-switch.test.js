// Each egress class's engine (lib/egress-switch.js, P5 spec Decision 11): direct by default, check as asked, go only
// once the gate is met (else check with a note); the gate from the egress log with the shadow compare's semantics
// (lib/front/shadow-log.js): a would-refuse or a check that could not be asked is a diff, and the class needs `min`
// checked calls and `days` days since the last diff (or since its first check).
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const os = require('os');
const path = require('path');
const { egressRouting, gateFromLog, gateRule, createEgressGate, DAY } = require('../lib/egress-switch');
const { createEgressLog } = require('../lib/egress-log');
const { CLASSES } = require('../lib/egress');

const NOW = 1_800_000_000_000;
const checked = (purpose, ts) => ({ ts, purpose, outcome: 'checked', host: 'api.example.test', via: 'check' });
const refused = (purpose, ts) => ({ ts, purpose, outcome: 'would-refuse', host: 'evil.example.test', via: 'check' });
const many = (purpose, n, from, step = 1000) => Array.from({ length: n }, (_, i) => checked(purpose, from + i * step));

test('routing: every class, direct by default; check as asked; an unknown switch is direct', () => {
  const r = egressRouting(undefined);
  assert.deepEqual(Object.keys(r.engines), CLASSES);
  assert.ok(Object.values(r.engines).every((e) => e === 'direct'));
  assert.deepEqual(r.notes, {});
  const r2 = egressRouting({ classes: { mcp: 'check', notify: 'proxy', bogus: 'go' } });
  assert.equal(r2.engines.mcp, 'check');
  assert.equal(r2.engines.notify, 'direct');
  assert.ok(!('bogus' in r2.engines));
  assert.deepEqual(egressRouting('nonsense').engines, r.engines);
});

test('routing: go only when the gate is met, else check with the gate reason', () => {
  const asked = [];
  const gate = (c, rule) => {
    asked.push([c, rule]);
    return c === 'gateway' ? { ok: true } : { ok: false, reason: `${c} not yet` };
  };
  const r = egressRouting({ classes: { gateway: 'go', connectors: 'go', mcp: 'check' } }, { gate });
  assert.equal(r.engines.gateway, 'go');
  assert.equal(r.engines.connectors, 'check');
  assert.equal(r.notes.connectors, 'connectors not yet');
  assert.equal(r.engines.mcp, 'check');
  assert.ok(!('gateway' in r.notes) && !('mcp' in r.notes));
  // The gate is asked only for the classes asked for go, with the default rule.
  assert.deepEqual(asked, [
    ['gateway', { days: 1, min: 20 }],
    ['connectors', { days: 1, min: 20 }],
  ]);
  // A gate without a reason still gives a note.
  const r2 = egressRouting({ classes: { brain: 'go' } }, { gate: () => ({ ok: false }) });
  assert.equal(r2.engines.brain, 'check');
  assert.equal(r2.notes.brain, 'the egress gate is not met');
  // No gate given: go as asked.
  assert.equal(egressRouting({ classes: { brain: 'go' } }).engines.brain, 'go');
});

test('rule: a count of zero or more, else the default (as routing() for the shadow)', () => {
  assert.deepEqual(gateRule(undefined), { days: 1, min: 20 });
  assert.deepEqual(gateRule({ gateDays: 0, gateMinChecks: 0 }), { days: 0, min: 0 });
  assert.deepEqual(gateRule({ gateDays: 2.5, gateMinChecks: 5 }), { days: 2.5, min: 5 });
  for (const bad of [-1, '3', null, NaN, Infinity, true])
    assert.deepEqual(gateRule({ gateDays: bad, gateMinChecks: bad }), { days: 1, min: 20 });
  let seen = null;
  egressRouting({ classes: { mcp: 'go' }, gateDays: 3, gateMinChecks: 7 }, { gate: (c, r) => ((seen = r), { ok: 1 }) });
  assert.deepEqual(seen, { days: 3, min: 7 });
});

test('gate: every class answered; nothing checked yet is not met', () => {
  const g = gateFromLog([], { days: 1, min: 20 }, NOW);
  assert.deepEqual(Object.keys(g), CLASSES);
  assert.equal(g.gateway.ok, false);
  assert.match(g.gateway.reason, /0 of 20 gateway calls checked clean/);
  // min 0 and nothing checked: the days part has nothing to count from.
  assert.match(gateFromLog([], { days: 1, min: 0 }, NOW).gateway.reason, /no gateway call has been checked yet/);
});

test('gate: min checked calls and days clean since the first check, per purpose class', () => {
  const old = NOW - 2 * DAY;
  const recs = [...many('connectors:dynatrace', 15, old), ...many('connectors:n8n', 5, old + DAY), checked('mcp', NOW)];
  const g = gateFromLog(recs, { days: 1, min: 20 }, NOW);
  assert.deepEqual(g.connectors, { ok: true }); // 20 checks across connector types, the first 2 days ago
  assert.equal(g.mcp.ok, false);
  assert.match(g.mcp.reason, /1 of 20/);
  // 19 checks are one short.
  assert.equal(gateFromLog(recs.slice(1), { days: 1, min: 20 }, NOW).connectors.ok, false);
  // 20 checks but the first only half a day ago.
  const young = gateFromLog(many('gateway', 20, NOW - DAY / 2), { days: 1, min: 20 }, NOW).gateway;
  assert.equal(young.ok, false);
  assert.match(young.reason, /clean for 0\.50 of 1 days/);
  // Exactly one day is enough.
  assert.equal(gateFromLog(many('gateway', 20, NOW - DAY), { days: 1, min: 20 }, NOW).gateway.ok, true);
  // Records in any order (the log's tail is newest first).
  assert.deepEqual(gateFromLog([...recs].reverse(), { days: 1, min: 20 }, NOW).connectors, { ok: true });
});

test('gate: a would-refuse restarts the wait (count and days from it), as a shadow diff does', () => {
  const t0 = NOW - 5 * DAY;
  const base = many('notify', 30, t0);
  // The last record is the refusal: not met, the reason names it.
  const last = gateFromLog([...base, refused('notify', t0 + DAY)], { days: 1, min: 20 }, NOW).notify;
  assert.equal(last.ok, false);
  assert.match(last.reason, /the last notify call checked would be refused \(evil\.example\.test\)/);
  // 20 checks after the refusal, which was 2 days ago: met.
  const diff = NOW - 2 * DAY;
  const after = gateFromLog([...base, refused('notify', diff), ...many('notify', 20, diff + 1)], {}, NOW).notify;
  assert.deepEqual(after, { ok: true });
  // 20 checks after a refusal only 12 hours ago: the days count from the refusal.
  const recent = NOW - DAY / 2;
  const r = gateFromLog([...base, refused('notify', recent), ...many('notify', 20, recent + 1)], {}, NOW).notify;
  assert.equal(r.ok, false);
  assert.match(r.reason, /clean for 0\.50 of 1 days/);
  // 10 checks after it: the count starts again.
  const few = gateFromLog([...base, refused('notify', diff), ...many('notify', 10, diff + 1)], {}, NOW).notify;
  assert.match(few.reason, /10 of 20 notify calls checked clean since the last refusal/);
});

test('gate: a check that could not be asked counts as a diff; other outcomes and bad records are ignored', () => {
  const t0 = NOW - 3 * DAY;
  const recs = [
    ...many('brain', 25, t0),
    { ts: NOW - 1000, purpose: 'brain', outcome: 'check-error' },
    { ts: NOW - 500, purpose: 'brain', outcome: 'ok' },
    { ts: NOW - 400, purpose: 'brain', outcome: 'error' },
    { ts: NOW - 300, purpose: 'brain', outcome: 'refused' },
    // Only /check verdicts are evidence (review I2): a forwarded call's would-refuse, an engine call's, or a verdict
    // without via, never touch the gate.
    { ts: NOW - 200, purpose: 'brain', outcome: 'would-refuse', via: 'proxy' },
    { ts: NOW - 150, purpose: 'brain', outcome: 'would-refuse', via: 'engine' },
    { ts: NOW - 100, purpose: 'brain', outcome: 'would-refuse' },
    { ts: 'x', purpose: 'brain', outcome: 'would-refuse' },
    { purpose: 'brain', outcome: 'would-refuse' },
    { ts: NOW, purpose: 7, outcome: 'would-refuse' },
    null,
  ];
  const g = gateFromLog(recs, { days: 1, min: 20 }, NOW).brain;
  assert.equal(g.ok, false);
  assert.match(g.reason, /the last brain call checked could not be checked/);
  assert.deepEqual(gateFromLog(recs.slice(0, 25), {}, NOW).brain, { ok: true });
  assert.deepEqual(gateFromLog('not a list', { days: 0, min: 0 }, NOW).brain, { ok: true });
});

test('gate: 0 lifts either part; both 0 lift the rule', () => {
  // days 0: the count alone.
  assert.deepEqual(gateFromLog(many('evals', 3, NOW), { days: 0, min: 3 }, NOW).evals, { ok: true });
  assert.equal(gateFromLog(many('evals', 2, NOW), { days: 0, min: 3 }, NOW).evals.ok, false);
  // min 0: the days alone, from the first check.
  assert.deepEqual(gateFromLog(many('evals', 1, NOW - DAY), { days: 1, min: 0 }, NOW).evals, { ok: true });
  // both 0: met even right after a refusal.
  assert.deepEqual(gateFromLog([refused('evals', NOW)], { days: 0, min: 0 }, NOW).evals, { ok: true });
  // min 0, days 0 still lifted when the last record is a refusal; min 0 alone is not.
  assert.equal(gateFromLog([refused('evals', NOW - 2 * DAY)], { days: 1, min: 0 }, NOW).evals.ok, false);
});

test('routing with the log gate: the owner rule end to end', () => {
  const recs = many('pricing', 20, NOW - 2 * DAY);
  const gate = (c, rule) => gateFromLog(recs, rule, NOW)[c];
  const r = egressRouting({ classes: { pricing: 'go', routers: 'go' } }, { gate });
  assert.equal(r.engines.pricing, 'go');
  assert.equal(r.engines.routers, 'check');
  assert.match(r.notes.routers, /0 of 20 routers calls/);
  // A stricter rule from config.
  const r2 = egressRouting({ classes: { pricing: 'go' }, gateMinChecks: 21 }, { gate });
  assert.equal(r2.engines.pricing, 'check');
});

test('only via "check" verdicts count: engine and forwarded records never pass or break the gate', () => {
  const t0 = NOW - 3 * DAY;
  const proxied = Array.from({ length: 30 }, (_, i) => ({ ...checked('gateway', t0 + i), via: 'proxy' }));
  const engine = Array.from({ length: 30 }, (_, i) => ({ ...checked('gateway', t0 + i), via: 'engine' }));
  assert.match(gateFromLog([...proxied, ...engine], {}, NOW).gateway.reason, /0 of 20/);
  const clean = many('gateway', 20, t0);
  const noisy = [
    ...clean,
    { ...refused('gateway', NOW - 10), via: 'engine' },
    { ...refused('gateway', NOW), via: 'proxy' },
  ];
  assert.deepEqual(gateFromLog(noisy, {}, NOW).gateway, { ok: true });
});

test('the client reports each check it could not ask (no verdict answer, a timeout, or no endpoint)', async (t) => {
  const http = require('http');
  const egress = require('../lib/egress');
  const real = globalThis.fetch;
  globalThis.fetch = async () => new Response('direct');
  const server = http.createServer((req, res) => {
    req.resume();
    res.writeHead(req.headers['x-zp-egress-purpose'] === 'notify' ? 400 : 204).end();
  });
  await new Promise((r) => server.listen(0, '127.0.0.1', r));
  const reported = [];
  egress.onCheckError((rec) => reported.push(rec));
  t.after(async () => {
    globalThis.fetch = real;
    egress.onCheckError(null);
    egress.configure(null);
    server.closeAllConnections?.();
    await new Promise((r) => server.close(r));
  });
  const lines = [];
  const before = Date.now();
  const engines = { notify: 'check', pricing: 'check' };
  egress.configure({ port: server.address().port, token: 't', engines, log: (l) => lines.push(l) });
  await egress.fetchFor('pricing')('https://api.example.test/a'); // 204: a verdict
  await egress.fetchFor('notify')('https://hooks.example.test/a'); // 400: no verdict
  const until = Date.now() + 2000;
  while (!reported.some((r) => r.purpose === 'notify') && Date.now() < until)
    await new Promise((r) => setTimeout(r, 10));
  const n = reported.find((r) => r.purpose === 'notify');
  assert.ok(n && n.outcome === 'check-error' && n.ts >= before);
  assert.ok(!reported.some((r) => r.purpose === 'pricing'));
  assert.match(lines[0], /the check of a notify call could not be asked: the endpoint answered 400/);
  assert.equal(gateFromLog([...many('notify', 20, n.ts - 2 * DAY - 100), ...reported], {}, NOW).notify.ok, false);
});

test('a /check with no answer within 5 s is a check error (review M5)', async (t) => {
  const http = require('http');
  const egress = require('../lib/egress');
  const real = globalThis.fetch;
  globalThis.fetch = async () => new Response('direct');
  const held = [];
  const server = http.createServer((req, res) => held.push(res)); // never answers
  await new Promise((r) => server.listen(0, '127.0.0.1', r));
  const reported = [];
  egress.onCheckError((rec) => reported.push(rec));
  t.mock.timers.enable({ apis: ['setTimeout'] });
  t.after(async () => {
    globalThis.fetch = real;
    egress.onCheckError(null);
    egress.configure(null);
    server.closeAllConnections?.();
    await new Promise((r) => server.close(r));
  });
  egress.configure({ port: server.address().port, token: 't', engines: { agents: 'check' }, log: () => {} });
  assert.equal(await (await egress.fetchFor('agents')('https://agent.example.test/')).text(), 'direct');
  // Wait (in real time) for the request to reach the fake, then move the clock past the 5 s.
  const until = Date.now() + 3000;
  while (!held.length && Date.now() < until) await new Promise((r) => setImmediate(r));
  assert.equal(held.length, 1);
  t.mock.timers.tick(4999);
  assert.equal(reported.length, 0);
  t.mock.timers.tick(1);
  assert.equal(reported.length, 1);
  assert.equal(reported[0].purpose, 'agents');
});

// The sticky gate (review I1): a temp data folder with the real egress log.
function gateDir(t) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'zp-egress-gate-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const log = createEgressLog({ dir });
  const file = path.join(dir, 'egress-gate.json');
  const open = (now = () => NOW) =>
    createEgressGate({ file, evidence: () => log.scan('"via":"check"'), now, log: () => {} });
  return { dir, log, file, open };
}

test('sticky gate: met, then 20k ok records and a restart, the class stays on go', (t) => {
  const { log, file, open } = gateDir(t);
  for (const r of many('connectors:dynatrace', 20, NOW - 2 * DAY)) log.append(r);
  const g = open();
  assert.deepEqual(g.gate('connectors', { days: 1, min: 20 }), { ok: true });
  assert.equal(fs.statSync(file).mode & 0o777, 0o600);
  const saved = JSON.parse(fs.readFileSync(file, 'utf8'));
  assert.equal(saved.connectors.passedAt, NOW);
  assert.equal(saved.connectors.since, NOW - 2 * DAY);
  // On go, the class writes no more checks: only ok traffic, enough to rotate the log.
  const ok = {
    ts: NOW,
    purpose: 'connectors:dynatrace',
    method: 'GET',
    scheme: 'https',
    host: 'x.example.test',
    port: 443,
    status: 200,
    outcome: 'ok',
    bytesOut: 0,
    bytesIn: 10,
    ms: 1,
    via: 'proxy',
  };
  for (let i = 0; i < 20000; i++) log.append({ ...ok, ts: NOW + i, pad: 'x'.repeat(200) });
  assert.ok(fs.existsSync(`${log.file}.1`), 'the log rotated');
  const later = open(() => NOW + DAY);
  const r = egressRouting({ classes: { connectors: 'go' } }, { gate: (c, rule) => later.gate(c, rule) });
  assert.equal(r.engines.connectors, 'go');
  // The pass file alone is enough: even with the log gone.
  fs.rmSync(log.file);
  fs.rmSync(`${log.file}.1`);
  assert.deepEqual(open().gate('connectors', {}), { ok: true });
});

test('sticky gate: evidence is filtered before any cap, from both files', (t) => {
  const { log, open, file } = gateDir(t);
  for (const r of many('mcp', 20, NOW - 2 * DAY)) log.append(r);
  const ok = {
    ts: NOW,
    purpose: 'mcp',
    method: 'GET',
    scheme: 'https',
    host: 'x.example.test',
    port: 443,
    status: 200,
    outcome: 'ok',
    bytesOut: 0,
    bytesIn: 1,
    ms: 1,
    via: 'proxy',
    pad: 'x'.repeat(300),
  };
  for (let i = 0; i < 20000; i++) log.append({ ...ok, ts: NOW - DAY + i });
  assert.ok(fs.existsSync(`${log.file}.1`));
  assert.ok(!fs.existsSync(file));
  assert.deepEqual(open().gate('mcp', {}), { ok: true }); // the checks sit in the rotated file, under 20k ok lines
});

test('sticky gate: a new diff of the class, or the owner setting it to check or direct, ends the pass', (t) => {
  const { log, open, file } = gateDir(t);
  for (const r of many('notify', 20, NOW - 2 * DAY)) log.append(r);
  for (const r of many('pricing', 20, NOW - 2 * DAY)) log.append(r);
  const g = open();
  assert.equal(g.gate('notify', {}).ok, true);
  assert.equal(g.gate('pricing', {}).ok, true);
  // Another class's diff or a non-evidence record changes nothing.
  g.note({ ...refused('mcp', NOW), via: 'check' });
  g.note({ ...refused('notify', NOW), via: 'engine' });
  assert.equal(g.gate('notify', {}).ok, true);
  // A /check would-refuse of notify: back to check, and it must earn the gate again.
  const diff = refused('notify', NOW + 1);
  log.append(diff);
  g.note(diff);
  const after = g.gate('notify', {});
  assert.equal(after.ok, false);
  assert.match(after.reason, /would be refused/);
  assert.equal(JSON.parse(fs.readFileSync(file, 'utf8')).notify.passedAt, null);
  // ... also after a restart.
  assert.equal(open(() => NOW + 2).gate('notify', {}).ok, false);
  // A check error is a diff too, and it survives a restart (it is not in the log).
  const g2 = open(() => NOW + 3);
  assert.equal(g2.gate('pricing', {}).ok, true);
  g2.note({ ts: NOW + 3, purpose: 'pricing', outcome: 'check-error' });
  assert.match(open(() => NOW + 4).gate('pricing', {}).reason, /could not be checked/);
  // The owner sets a passed class to check: forgotten.
  for (const r of many('evals', 20, NOW - 2 * DAY)) log.append(r);
  const g3 = open();
  assert.equal(g3.gate('evals', {}).ok, true);
  egressRouting({ classes: { evals: 'check' } }, { gate: (c, rule) => g3.gate(c, rule), forget: (c) => g3.forget(c) });
  assert.equal(JSON.parse(fs.readFileSync(file, 'utf8')).evals.passedAt, null);
  // The lifted rule (0, 0) passes without being remembered.
  const g4 = open();
  assert.equal(g4.gate('agents', { days: 0, min: 0 }).ok, true);
  assert.ok(!('agents' in g4.state()));
  // A broken state file is no pass.
  fs.writeFileSync(file, '{not json');
  assert.equal(open().gate('agents', {}).ok, false);
});
