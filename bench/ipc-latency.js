// IPC v1 over the gateway's stdio pipes (P3 T7): the round trip of one message each way, and the throughput of the
// gateway's event stream into the control plane's stores. Runs under Node and Deno; builds the Go stub (or uses
// ZP_GW_BIN). Prints one JSON object; nothing is written outside a temp folder.
//   node bench/ipc-latency.js [--count 2000] [--events 30000]
// Round trip: a budget.snapshot sent, the health the stub answers with (--health-on-snapshot) received, timed with
// performance.now(), one at a time. Throughput: --emit with no pause, from ready until the last event is stored by the
// real writers (lib/ipc/local.js), acks included.
const fs = require('fs');
const os = require('os');
const path = require('path');
const { buildStub } = require('../scripts/ipc/build-stub');
const { startSession } = require('../lib/ipc/session');
const { createIngest } = require('../lib/ipc/ingest');
const { openLocalStores } = require('../lib/ipc/local');
const { DIR } = require('../lib/ipc/schema');

const arg = (name, dflt) => {
  const i = process.argv.indexOf(`--${name}`);
  return i > 0 ? Number(process.argv[i + 1]) : dflt;
};
const COUNT = arg('count', 2000);
const EVENTS = arg('events', 30000);
const body = (rel) => JSON.parse(fs.readFileSync(path.join(DIR, 'fixtures', rel), 'utf8')).body;
const initial = () => ({
  config: body('config.push/valid-full.json'),
  secrets: { ...body('secrets.handoff/valid.json'), gen: 3 },
  snapshot: { ...body('budget.snapshot/valid.json'), at: Date.now() },
});
const pct = (sorted, p) => sorted[Math.min(sorted.length - 1, Math.floor((p / 100) * sorted.length))];
const round3 = (x) => Math.round(x * 1000) / 1000;

async function roundTrip(bin, root) {
  let resolveHealth = null;
  const s = startSession({
    bin,
    args: ['--outbox', path.join(root, 'rt-outbox'), '--health-on-snapshot', '--health-ms', '3600000'],
    cpVersion: 'bench',
    initial: initial(),
    log: () => {},
    onHealth: () => resolveHealth && resolveHealth(),
  });
  await s.ready;
  await new Promise((r) => setTimeout(r, 100)); // the health that answers the first snapshot
  const snapshot = body('budget.snapshot/valid.json');
  const times = [];
  for (let i = 0; i < COUNT + 100; i++) {
    const got = new Promise((r) => (resolveHealth = r));
    const t0 = performance.now();
    s.send('budget.snapshot', { ...snapshot, at: Date.now() });
    await got;
    if (i >= 100) times.push(performance.now() - t0); // the first 100 warm up
  }
  await s.shutdown(1000);
  times.sort((a, b) => a - b);
  return { count: COUNT, p50Ms: round3(pct(times, 50)), p95Ms: round3(pct(times, 95)), p99Ms: round3(pct(times, 99)) };
}

async function throughput(bin, root) {
  const stores = openLocalStores({ root: path.join(root, 'tp') });
  const ingest = createIngest(stores.sinks);
  const rounds = Math.ceil(EVENTS / 3);
  let t0 = 0;
  let done;
  const finished = new Promise((r) => (done = r));
  const s = startSession({
    bin,
    args: ['--outbox', stores.files.outboxDir, '--emit', String(rounds)],
    cpVersion: 'bench',
    initial: initial(),
    log: () => {},
    onEvent: (msg, stream) => {
      ingest.handle(msg, stream);
      if (ingest.mark(stream) === 3 * rounds) done(performance.now());
    },
  });
  await s.ready;
  t0 = performance.now();
  const t1 = await finished;
  await s.shutdown(2000);
  const seconds = (t1 - t0) / 1000;
  return { events: 3 * rounds, seconds: round3(seconds), eventsPerSecond: Math.round((3 * rounds) / seconds) };
}

(async () => {
  const built = buildStub();
  if (built.skip) throw new Error(built.skip);
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'zp-ipc-bench-'));
  try {
    const out = {
      runtime: process.versions.deno ? `deno ${process.versions.deno}` : `node ${process.versions.node}`,
      host: `${os.platform()}-${os.arch()}`,
      roundTrip: await roundTrip(built.bin, root),
      throughput: await throughput(built.bin, root),
    };
    console.log(JSON.stringify(out));
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
})().catch((err) => {
  console.error(err.message);
  process.exit(1);
});
