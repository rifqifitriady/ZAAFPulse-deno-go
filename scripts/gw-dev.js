// Dev command (P3 D5): builds the gateway stub, supervises it on a throwaway data folder and prints its health, its
// events and its log until Ctrl-C (then shutdown). Not used by the server.
//   node scripts/gw-dev.js [--emit N]
//   deno run -q --no-prompt --allow-read --allow-write --allow-run --allow-env scripts/gw-dev.js [--emit N]
const fs = require('fs');
const os = require('os');
const path = require('path');
const { buildStub } = require('./ipc/build-stub');
const { createSupervisor } = require('../lib/ipc/supervisor');
const { createIngest } = require('../lib/ipc/ingest');
const { openLocalStores } = require('../lib/ipc/local');
const { recoverMarks } = require('../lib/ipc/marks');
const { DIR } = require('../lib/ipc/schema');

const built = buildStub();
if (built.skip) {
  console.error(built.skip);
  process.exit(1);
}
const root = fs.mkdtempSync(path.join(os.tmpdir(), 'zp-gw-dev-'));
const stores = openLocalStores({ root });
const ingest = createIngest({ ...stores.sinks, marks: recoverMarks(stores.files) });
const body = (rel) => JSON.parse(fs.readFileSync(path.join(DIR, 'fixtures', rel), 'utf8')).body;
const emit = process.argv.indexOf('--emit');
const sup = createSupervisor({
  bin: built.bin,
  args: [
    '--outbox',
    stores.files.outboxDir,
    ...(emit > 0 ? ['--emit', process.argv[emit + 1], '--emit-every-ms', '200'] : []),
  ],
  cpVersion: 'gw-dev',
  sources: {
    config: () => body('config.push/valid-full.json'),
    secrets: () => body('secrets.handoff/valid.json'),
    snapshot: () => ({ budgets: body('budget.snapshot/valid.json').budgets, keyCenter: { k1: 'platform' } }),
  },
  ingest: {
    handle: (msg, stream) => {
      const stored = ingest.handle(msg, stream);
      console.log(`${stored ? 'stored' : 'duplicate'} ${msg.type} #${msg.seq}`);
    },
  },
  log: (l) => console.log(l),
});
console.log(`data folder: ${root}`);
sup.start();
const timer = setInterval(() => console.log(JSON.stringify(sup.status())), 5000);
process.on('SIGINT', async () => {
  clearInterval(timer);
  console.log(JSON.stringify(await sup.stop(2000)));
  process.exit(0);
});
