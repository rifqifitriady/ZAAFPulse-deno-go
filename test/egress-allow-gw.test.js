// The allow-list in the real gateway (P5 Decision 13; gw/cmd/zaafpulse-gw built here as the IPC tests build it,
// skipped without Go): config.push.egress (lib/front/push.js egressSection) sets the endpoint's policy on each push.
// Two local fakes on 127.0.0.1, the list naming only the first's port: in report mode a call to the second passes and
// is recorded "would-refuse"; after a push with enforce it fails EGRESS_REFUSED and is recorded "refused"; the listed
// one passes in both. The rule names 127.0.0.1 while the calls name [::ffff:7f00:1] (the same address IPv4-mapped,
// which the client's test-only testLoopbackGo sends through the endpoint): Go compares addresses by value.
const test = require('node:test');
const assert = require('node:assert/strict');
const crypto = require('crypto');
const http = require('http');
const egress = require('../lib/egress');
const { egressSection } = require('../lib/front/push');
const { createSupervisor } = require('../lib/ipc/supervisor');
const { createIngest } = require('../lib/ipc/ingest');
const { openLocalStores } = require('../lib/ipc/local');
const { recoverMarks } = require('../lib/ipc/marks');
const { stubBin, initial, tmpDir, waitFor } = require('./ipc-stub');

function fake() {
  const server = http.createServer((req, res) => {
    req.resume();
    req.on('end', () => res.end('hi'));
  });
  return new Promise((resolve) => server.listen(0, '127.0.0.1', () => resolve(server)));
}

test('the pushed allow-list: off the list is would-refuse in report and EGRESS_REFUSED in enforce', async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  const listed = await fake();
  const other = await fake();
  const root = tmpDir(t, 'egress-allow-gw');
  const stores = openLocalStores({ root });
  const ingest = createIngest({ ...stores.sinks, marks: recoverMarks(stores.files) });
  const frontToken = crypto.randomBytes(32).toString('hex');
  const first = initial();
  let mode = 'report';
  const rules = [{ host: '127.0.0.1', port: listed.address().port }];
  let port = null;
  const sup = createSupervisor({
    bin,
    args: ['--outbox', stores.files.outboxDir],
    cpVersion: 'egress-allow-gw',
    sources: {
      config: () => ({ ...first.config, egress: egressSection({ allowList: mode }, rules) }),
      secrets: () => ({ providers: [], mask: [], frontToken }),
      snapshot: () => ({ budgets: [], keyCenter: {} }),
    },
    ingest,
    log: () => {},
    onEgress: (p) => {
      port = p;
      egress.configure(
        p
          ? {
              port: p,
              token: egress.deriveToken(frontToken),
              engines: { connectors: 'go' },
              lockdown: false,
              testLoopbackGo: true,
            }
          : null,
      );
    },
  });
  t.after(async () => {
    egress.configure(null);
    await sup.stop(2000);
    for (const s of [listed, other]) {
      s.closeAllConnections();
      await new Promise((r) => s.close(r));
    }
  });
  sup.start();
  await waitFor(() => port !== null, 20000, 'the gateway ready with its egress port');

  const via = egress.fetchFor('connectors', 'allow');
  const url = (s) => `http://[::ffff:7f00:1]:${s.address().port}/x`;
  const records = () => stores.egress.tail(1000).filter((r) => r.purpose === 'connectors:allow');
  const of = (s, outcome) => records().filter((r) => r.port === s.address().port && r.outcome === outcome).length;

  // Report: both pass; only the one off the list is recorded would-refuse.
  assert.equal(await (await via(url(listed))).text(), 'hi');
  assert.equal(await (await via(url(other))).text(), 'hi');
  await waitFor(() => of(listed, 'ok') === 1 && of(other, 'ok') === 1, 10000, 'the report-mode records');
  assert.equal(of(other, 'would-refuse'), 1);
  assert.equal(of(listed, 'would-refuse'), 0);

  // Enforce, by a new push: the one off the list is refused before anything is sent; the listed one still passes.
  mode = 'enforce';
  assert.ok(sup.reload());
  assert.ok(await sup.synced(5000), 'the new generation applied');
  const err = await via(url(other)).then(
    () => null,
    (e) => e,
  );
  assert.ok(err instanceof TypeError, 'a fetch failure');
  assert.equal(err.cause.code, 'EGRESS_REFUSED');
  assert.equal(await (await via(url(listed))).text(), 'hi');
  await waitFor(() => of(other, 'refused') === 1 && of(listed, 'ok') === 2, 10000, 'the enforce-mode records');
  assert.equal(of(other, 'ok'), 1, 'the refused call never reached the fake');
});
