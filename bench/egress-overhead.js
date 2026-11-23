// The egress endpoint's overhead (P5: "M3 for a call through the egress endpoint", owed to the final benchmarks): the
// same connector-style call (GET, a 2 KB JSON answer) made direct and through the Go gateway's egress endpoint (the
// `connectors` class on `go`, lib/egress.js), paired one after the other with the order alternated, after a warm-up.
// The gateway is the real one (built, or ZP_GW_BIN) under the real supervisor and ingest, as test/egress-diff.test.js
// runs it; the target is a local fake on 127.0.0.1, which the client sends through the endpoint only with its
// test-only testLoopbackGo (a remote host stands in). Prints one JSON object; nothing is written outside a temp folder.
//   node bench/egress-overhead.js [--count 2000] [--warmup 100]
//   deno run -q --no-prompt -P=test bench/egress-overhead.js [same flags]
const crypto = require('crypto');
const fs = require('fs');
const http = require('http');
const os = require('os');
const path = require('path');
const egress = require('../lib/egress');
const { buildStub } = require('../scripts/ipc/build-stub');
const { createSupervisor } = require('../lib/ipc/supervisor');
const { createIngest } = require('../lib/ipc/ingest');
const { openLocalStores } = require('../lib/ipc/local');
const { recoverMarks } = require('../lib/ipc/marks');
const { initial } = require('../test/ipc-stub');

const arg = (name, dflt) => {
  const i = process.argv.indexOf(`--${name}`);
  const v = i > 0 ? Number(process.argv[i + 1]) : NaN;
  return Number.isFinite(v) && v > 0 ? v : dflt;
};
const COUNT = arg('count', 2000);
const WARMUP = arg('warmup', 100);
const PURPOSE = 'connectors';
const ANSWER = JSON.stringify({ data: Array.from({ length: 40 }, (_, i) => ({ id: i, name: `entity-${i}`, v: i * 3 })) });

// Nearest-rank percentile, rounded to 0.01 ms.
function pct(list, p) {
  if (!list.length) return null;
  const s = [...list].sort((a, b) => a - b);
  const i = Math.min(s.length - 1, Math.max(0, Math.ceil((p / 100) * s.length) - 1));
  return Math.round(s[i] * 100) / 100;
}
const stats = (list) => ({ n: list.length, p50: pct(list, 50), p95: pct(list, 95), p99: pct(list, 99) });

function startFake() {
  const server = http.createServer((req, res) => {
    req.resume();
    req.on('end', () => {
      res.writeHead(200, { 'content-type': 'application/json' });
      res.end(ANSWER);
    });
  });
  server.keepAliveTimeout = 60000;
  return new Promise((resolve) => server.listen(0, '127.0.0.1', () => resolve(server)));
}

async function timed(fn, url) {
  const t0 = performance.now();
  const r = await fn(url, { headers: { accept: 'application/json' }, signal: AbortSignal.timeout(30000) });
  await r.arrayBuffer();
  if (r.status !== 200) throw new Error(`HTTP ${r.status}`);
  return performance.now() - t0;
}

async function main() {
  const built = buildStub();
  if (built.skip) throw new Error(built.skip);
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'zp-egress-bench-'));
  let fake = null;
  let sup = null;
  try {
    fake = await startFake();
    const stores = openLocalStores({ root });
    const ingest = createIngest({ ...stores.sinks, marks: recoverMarks(stores.files) });
    const frontToken = crypto.randomBytes(32).toString('hex');
    const first = initial();
    let port = null;
    sup = createSupervisor({
      bin: built.bin,
      args: ['--outbox', stores.files.outboxDir],
      cpVersion: 'egress-bench',
      sources: {
        config: () => ({ ...first.config, egress: { allow: [], allowList: 'report' } }),
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
                engines: { [PURPOSE]: 'go' },
                lockdown: false,
                testLoopbackGo: true,
              }
            : null,
        );
      },
    });
    sup.start();
    const until = Date.now() + 20000;
    while (port === null) {
      if (Date.now() > until) throw new Error('the gateway did not open its egress endpoint within 20 s');
      await new Promise((r) => setTimeout(r, 25));
    }
    const url = `http://127.0.0.1:${fake.address().port}/api/v2/entities?pageSize=40`;
    const via = egress.fetchFor(PURPOSE, 'bench');
    for (let i = 0; i < WARMUP; i++) {
      await timed(fetch, url);
      await timed(via, url);
    }
    const direct = [];
    const go = [];
    const diff = [];
    for (let i = 0; i < COUNT; i++) {
      let a;
      let b;
      if (i % 2) {
        b = await timed(fetch, url);
        a = await timed(via, url);
      } else {
        a = await timed(via, url);
        b = await timed(fetch, url);
      }
      go.push(a);
      direct.push(b);
      diff.push(a - b);
    }
    // Proof the measured calls left through the endpoint: the gateway records each one (egress.event, through the
    // outbox and the ingest into .data/egress.jsonl), a few milliseconds after its answer.
    const want = COUNT + WARMUP;
    let recorded = 0;
    const end = Date.now() + 10000;
    for (;;) {
      let text = '';
      try {
        text = fs.readFileSync(stores.files.egressFile, 'utf8');
      } catch {
        /* not written yet */
      }
      recorded = text.split('\n').filter((l) => l.includes('"outcome":"ok"')).length;
      if (recorded >= want || Date.now() > end) break;
      await new Promise((r) => setTimeout(r, 100));
    }
    if (recorded < want) throw new Error(`the gateway recorded ${recorded} of ${want} calls through the endpoint`);
    console.log(
      JSON.stringify({
        recorded,
        runtime: process.versions.deno ? `deno ${process.versions.deno}` : `node ${process.versions.node}`,
        count: COUNT,
        warmup: WARMUP,
        class: PURPOSE,
        direct: stats(direct),
        go: stats(go),
        overhead: stats(diff),
      }),
    );
  } finally {
    egress.configure(null);
    if (sup) await sup.stop(2000).catch(() => {});
    if (fake) {
      fake.closeAllConnections?.();
      await new Promise((r) => fake.close(r));
    }
    fs.rmSync(root, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 });
  }
}

main().catch((err) => {
  console.error(err);
  process.exitCode = 1;
});
