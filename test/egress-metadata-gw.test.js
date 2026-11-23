// P7 review M1 end to end: the real gateway (gw/cmd/zaafpulse-gw built here as the IPC tests build it, skipped without
// Go) in report mode with an empty allow-list, a class on 'go', and a local fake on 127.0.0.1 that answers every call
// with a redirect to the cloud metadata service. The caller follows it (fetch's default) and must get a fetch failure
// with cause.code EGRESS_PRIVATE, the metadata hop recorded "refused" and never "ok". Nothing leaves the machine: the
// gateway refuses the hop before any connect.
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

const TARGETS = [
  ['169.254.169.254', 'http://169.254.169.254/latest/meta-data/iam/security-credentials/'],
  ['fd00:ec2::254', 'http://[fd00:ec2::254]/latest/meta-data/'],
  ['metadata.google.internal', 'http://metadata.google.internal/computeMetadata/v1/'],
];

function redirector() {
  const server = http.createServer((req, res) => {
    req.resume();
    req.on('end', () => {
      const to = new URL(req.url, 'http://x').searchParams.get('to');
      res.writeHead(302, { location: to });
      res.end();
    });
  });
  return new Promise((resolve) => server.listen(0, '127.0.0.1', () => resolve(server)));
}

test('a followed redirect to the cloud metadata service is refused EGRESS_PRIVATE in report mode', async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  const fake = await redirector();
  // node:test runs after-hooks in the order they were added: the gateway must stop before tmpDir removes the outbox
  // whose lock it holds (Windows refuses to delete an open file), so its stop goes through tmpDir's own list.
  const stops = [];
  const root = tmpDir(t, 'egress-metadata-gw', stops);
  const stores = openLocalStores({ root });
  const ingest = createIngest({ ...stores.sinks, marks: recoverMarks(stores.files) });
  const frontToken = crypto.randomBytes(32).toString('hex');
  const first = initial();
  let port = null;
  const sup = createSupervisor({
    bin,
    args: ['--outbox', stores.files.outboxDir],
    cpVersion: 'egress-metadata-gw',
    sources: {
      config: () => ({ ...first.config, egress: egressSection({ allowList: 'report' }, []) }),
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
              engines: { agents: 'go' },
              lockdown: false,
              testLoopbackGo: true,
            }
          : null,
      );
    },
  });
  stops.push(async () => {
    egress.configure(null);
    await sup.stop(2000);
    fake.closeAllConnections();
    await new Promise((r) => fake.close(r));
  });
  sup.start();
  await waitFor(() => port !== null, 20000, 'the gateway ready with its egress port');

  const via = egress.fetchFor('agents', 'metadata');
  const records = () => stores.egress.tail(1000).filter((r) => r.purpose === 'agents:metadata');
  for (const [host, to] of TARGETS) {
    const url = `http://[::ffff:7f00:1]:${fake.address().port}/a2a?to=${encodeURIComponent(to)}`;
    const err = await via(url, { method: 'POST', headers: { authorization: 'Bearer k' }, body: '{}' }).then(
      () => null,
      (e) => e,
    );
    assert.ok(err instanceof TypeError, `${host}: a fetch failure, not an answer`);
    assert.equal(err.cause.code, 'EGRESS_PRIVATE', host);
    await waitFor(
      () => records().some((r) => r.host === host && r.outcome === 'refused' && r.error === 'EGRESS_PRIVATE'),
      10000,
      `the refused record for ${host}`,
    );
    assert.equal(records().filter((r) => r.host === host && r.outcome === 'ok').length, 0, `${host} never reached`);
  }
});
