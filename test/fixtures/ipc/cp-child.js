// The control plane of the kill test (test/ipc-kill.test.js), run as a child process so the test can SIGKILL it: it
// opens the stores of <data folder> like the server, recovers its marks, starts the stub with --emit, stores the
// events, and once every event of the run is stored (after a restart: and sent again by the stub) shuts the stub down
// (which acknowledges them) and exits 0 after printing "done". It writes the stub's pid to <data folder>/gw.pid, so the
// test can wait for the stub to exit too.
// argv: <data folder> <stub binary> <rounds>
const fs = require('fs');
const path = require('path');
const { openLocalStores } = require('../../../lib/ipc/local');
const { createIngest } = require('../../../lib/ipc/ingest');
const { recoverMarks } = require('../../../lib/ipc/marks');
const { startSession } = require('../../../lib/ipc/session');

const [root, bin, rounds] = process.argv.slice(2);
const total = 3 * Number(rounds);
const stores = openLocalStores({ root });
const ingest = createIngest({ ...stores.sinks, marks: recoverMarks(stores.files) });
const initial = JSON.parse(fs.readFileSync(path.join(root, 'initial.json'), 'utf8'));
initial.snapshot.at = Date.now();
let finishing = false;
let lastSeen = 0; // the highest seq the stub sent in this run, a resend of an event stored before the kill included
const pending = () => fs.readdirSync(stores.files.outboxDir).some((f) => f.endsWith('.jsonl'));
const s = startSession({
  bin,
  args: [
    '--outbox',
    stores.files.outboxDir,
    '--emit',
    String(rounds),
    '--emit-every-ms',
    '1',
    '--segment-events',
    '25',
  ],
  cpVersion: 'kill-test',
  initial,
  log: () => {},
  onEvent: (msg, stream) => {
    ingest.handle(msg, stream);
    lastSeen = Math.max(lastSeen, msg.seq);
    maybeDone();
  },
});
fs.writeFileSync(path.join(root, 'gw.pid'), String(s.pid));
s.ready.then(maybeDone, () => {});
s.exited.then(() => {
  if (!finishing) process.exit(1);
});
function maybeDone() {
  if (finishing || !s.stream || ingest.mark(s.stream) < total) return;
  // Every event is stored, but a killed predecessor may have stored the last ones without acknowledging them: the stub
  // sends them again after the handshake, and shutting down before they arrive would leave them in the outbox. Wait for
  // the last seq of this run (or for an outbox that has nothing unacknowledged).
  if (lastSeen < total && pending()) return;
  finishing = true;
  s.shutdown(2000).then(({ code }) => {
    process.stdout.write(`done ${code}\n`);
    process.exit(0);
  });
}
