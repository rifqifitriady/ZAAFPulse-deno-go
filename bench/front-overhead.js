// The Go front's proxy overhead (P4a T6, the M3 baseline): the same requests to server.js in js mode (the server on
// the main port) and in front mode (zaafpulse-gw on the main port, forwarding to the server's internal port), one at
// a time over one keep-alive connection, after a warm-up. Prints one JSON object: per mode p50, p99 and mean in ms,
// and the front's extra per request. Builds the gateway (or uses ZP_GW_BIN); nothing is written outside temp folders.
//   node bench/front-overhead.js [--count 2000] [--runtime node|deno]
//   deno run -q --no-prompt -P=test bench/front-overhead.js [same flags]
const http = require('http');
const { startServer, SEED_CONFIG } = require('../test/contract/harness');

const arg = (name, dflt) => {
  const i = process.argv.indexOf(`--${name}`);
  return i > 0 ? process.argv[i + 1] : dflt;
};
const COUNT = Number(arg('count', 2000));
const RUNTIME = arg('runtime', process.versions.deno ? 'deno' : 'node');
const PATHS = ['/login.html', '/api/platform'];
const pct = (sorted, p) => sorted[Math.min(sorted.length - 1, Math.floor((p / 100) * sorted.length))];

function get(agent, port, p) {
  return new Promise((resolve, reject) => {
    const req = http.get({ host: '127.0.0.1', port, path: p, agent }, (res) => {
      res.resume();
      res.on('end', () => (res.statusCode === 200 ? resolve() : reject(new Error(`${p}: ${res.statusCode}`))));
    });
    req.on('error', reject);
  });
}

async function measure(config) {
  const s = await startServer({ runtime: RUNTIME, config });
  const port = Number(new URL(s.base).port);
  const agent = new http.Agent({ keepAlive: true, maxSockets: 1 });
  try {
    for (let i = 0; i < 200; i++) await get(agent, port, PATHS[i % PATHS.length]);
    const ms = [];
    for (let i = 0; i < COUNT; i++) {
      const t = performance.now();
      await get(agent, port, PATHS[i % PATHS.length]);
      ms.push(performance.now() - t);
    }
    ms.sort((a, b) => a - b);
    const r3 = (x) => Math.round(x * 1000) / 1000;
    return { p50: r3(pct(ms, 50)), p99: r3(pct(ms, 99)), mean: r3(ms.reduce((a, b) => a + b, 0) / ms.length) };
  } finally {
    agent.destroy();
    await s.stop();
  }
}

(async () => {
  const js = await measure(SEED_CONFIG);
  const front = await measure({ ...SEED_CONFIG, gateway: { engine: 'front' } });
  const d = (k) => Math.round((front[k] - js[k]) * 1000) / 1000;
  console.log(
    JSON.stringify({
      runtime: RUNTIME,
      count: COUNT,
      paths: PATHS,
      js,
      front,
      overhead: { p50: d('p50'), p99: d('p99'), mean: d('mean') },
    }),
  );
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
