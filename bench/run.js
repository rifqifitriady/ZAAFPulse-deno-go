// The gateway benchmark (P0 T4): startup, gateway overhead on non-streaming calls, concurrent SSE streams and RSS, for
// the runtime in ZP_RUNTIME (node or deno), written to bench/results/<date>-<runtime>-<host>.json. Node standard
// library only; runs under Node and Deno. The mock provider runs in its own process, so the load generator here does
// not share an event loop with it; server.js is started and seeded by the contract harness (test/contract/harness.js).
//   node bench/run.js                      (ZP_RUNTIME=deno node bench/run.js for the Deno server)
// Knobs (defaults in brackets): ZP_BENCH_REQUESTS [2000], ZP_BENCH_LEVELS [50,200,500], ZP_BENCH_SECONDS [10],
// ZP_BENCH_DELAY_MS [20], ZP_BENCH_CHUNKS [20], ZP_BENCH_INTERVAL_MS [25], ZP_BENCH_STARTS [5], ZP_BENCH_OUT
// [bench/results], ZP_BENCH_HOST [the host name].
const fs = require('fs');
const os = require('os');
const path = require('path');
const { spawn, spawnSync } = require('child_process');
const { startServer, clientFor } = require('../test/contract/harness');

const ROOT = path.join(__dirname, '..');
const num = (name, def) => {
  const v = Number(process.env[name]);
  return Number.isFinite(v) && v > 0 ? v : def;
};
const CFG = {
  runtime: process.env.ZP_RUNTIME || 'node',
  requests: num('ZP_BENCH_REQUESTS', 2000),
  levels: (process.env.ZP_BENCH_LEVELS || '50,200,500')
    .split(',')
    .map(Number)
    .filter((n) => n > 0),
  seconds: num('ZP_BENCH_SECONDS', 10),
  delayMs: num('ZP_BENCH_DELAY_MS', 20),
  chunks: num('ZP_BENCH_CHUNKS', 20),
  intervalMs: num('ZP_BENCH_INTERVAL_MS', 25),
  starts: num('ZP_BENCH_STARTS', 5),
};
const KEYS = 25; // the stream workers spread over these keys, so no key reaches its rate limit
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const underDeno = !!(process.versions && process.versions.deno);

// Percentile of a list of numbers (nearest rank), rounded to 0.01 ms; null for an empty list.
function pct(list, p) {
  if (!list.length) return null;
  const s = [...list].sort((a, b) => a - b);
  const i = Math.min(s.length - 1, Math.max(0, Math.ceil((p / 100) * s.length) - 1));
  return Math.round(s[i] * 100) / 100;
}
const stats = (list) => ({ n: list.length, p50: pct(list, 50), p95: pct(list, 95), p99: pct(list, 99) });

// Resident set size of a process in MB through ps (macOS and Linux); null elsewhere or when ps fails.
function rssMB(pid) {
  if (process.platform === 'win32') return null;
  const r = spawnSync('ps', ['-o', 'rss=', '-p', String(pid)], { encoding: 'utf8' });
  const kb = Number(String(r.stdout || '').trim());
  return r.status === 0 && kb > 0 ? Math.round((kb / 1024) * 10) / 10 : null;
}

function versionOf(runtime) {
  const bin = runtime === 'deno' ? (underDeno ? process.execPath : 'deno') : underDeno ? 'node' : process.execPath;
  const r = spawnSync(bin, ['--version'], { encoding: 'utf8' });
  return (
    String(r.stdout || '')
      .split('\n')[0]
      .trim() || null
  );
}
function gitSha() {
  const r = spawnSync('git', ['rev-parse', 'HEAD'], { cwd: ROOT, encoding: 'utf8' });
  const dirty = spawnSync('git', ['status', '--porcelain', '--untracked-files=no'], { cwd: ROOT, encoding: 'utf8' });
  return { sha: r.status === 0 ? r.stdout.trim() : null, dirty: dirty.status === 0 ? !!dirty.stdout.trim() : null };
}
const hostName = () =>
  (process.env.ZP_BENCH_HOST || os.hostname().split('.')[0] || 'host').replace(/[^A-Za-z0-9-]/g, '-').slice(0, 40);

// The mock provider in a child process of the same runtime as this script; resolves with its base URL.
function startMockProcess() {
  const file = path.join(__dirname, 'mock-provider.js');
  const args = underDeno ? ['run', '--allow-net=127.0.0.1', '--allow-env', file] : [file];
  const child = spawn(process.execPath, args, {
    env: {
      ...process.env,
      ZP_MOCK_DELAY_MS: String(CFG.delayMs),
      ZP_MOCK_CHUNKS: String(CFG.chunks),
      ZP_MOCK_INTERVAL_MS: String(CFG.intervalMs),
    },
    stdio: ['ignore', 'pipe', 'inherit'],
  });
  return new Promise((resolve, reject) => {
    let out = '';
    child.stdout.on('data', (c) => {
      out += c;
      const m = out.match(/mock provider at (\S+)/);
      if (m) resolve({ url: m[1], stop: () => child.kill() });
    });
    child.once('exit', (code) => reject(new Error(`the mock provider exited (${code})`)));
  });
}

// One whole request; resolves with its time in ms, or throws on a non-200.
async function timed(url, headers, body) {
  const t0 = performance.now();
  const r = await fetch(url, { method: 'POST', headers, body });
  await r.arrayBuffer();
  if (r.status !== 200) throw new Error(`HTTP ${r.status}`);
  return performance.now() - t0;
}

// One stream read to its end: { ttfb, dur } in ms; throws on a non-200 or a cut stream.
async function streamOnce(url, headers, body) {
  const t0 = performance.now();
  const r = await fetch(url, { method: 'POST', headers, body });
  if (r.status !== 200) {
    await r.arrayBuffer();
    throw new Error(`HTTP ${r.status}`);
  }
  let ttfb = null;
  let tail = '';
  const dec = new TextDecoder();
  for await (const chunk of r.body) {
    if (ttfb === null) ttfb = performance.now() - t0;
    tail = (tail + dec.decode(chunk, { stream: true })).slice(-64);
  }
  if (!tail.includes('[DONE]')) throw new Error('stream ended early');
  return { ttfb, dur: performance.now() - t0 };
}

// `level` workers open streams back to back for CFG.seconds; returns counts and percentiles.
async function streamStep(level, target) {
  const ttfb = [];
  const dur = [];
  let completed = 0;
  let errors = 0;
  const end = performance.now() + CFG.seconds * 1000;
  const worker = async (i) => {
    while (performance.now() < end) {
      try {
        const s = await streamOnce(target.url, target.headers(i), target.body);
        ttfb.push(s.ttfb);
        dur.push(s.dur);
        completed++;
      } catch {
        errors++;
        await sleep(10);
      }
    }
  };
  await Promise.all(Array.from({ length: level }, (_, i) => worker(i)));
  return { completed, errors, ttfbP95: pct(ttfb, 95), durP50: pct(dur, 50), durP95: pct(dur, 95) };
}

async function main() {
  const t0 = Date.now();
  const startup = [];
  for (let i = 0; i < CFG.starts; i++) {
    const s = await startServer({ runtime: CFG.runtime });
    startup.push(Math.round(s.startupMs * 10) / 10);
    await s.stop();
  }
  const mock = await startMockProcess();
  const server = await startServer({ runtime: CFG.runtime });
  try {
    const c = clientFor(server.base);
    await c.admin('providers', {
      action: 'save',
      id: 'bench',
      preset: 'custom',
      baseUrl: mock.url,
      tier: 'external',
      approved: true,
      models: ['bench-1'],
    });
    const keys = [];
    for (let i = 0; i < KEYS; i++) keys.push((await c.newKey({ scopes: ['llm'], name: `bench-${i}` })).secret);
    await sleep(5000);
    const idleRssMB = rssMB(server.pid);

    const json = { 'content-type': 'application/json' };
    const gwUrl = `${server.base}/v1/chat/completions`;
    const directUrl = `${mock.url}/chat/completions`;
    const whole = JSON.stringify({ model: 'bench/bench-1', messages: [{ role: 'user', content: 'ping' }] });
    const wholeDirect = JSON.stringify({ model: 'bench-1', messages: [{ role: 'user', content: 'ping' }] });
    const gwHeaders = (i) => ({ ...json, authorization: `Bearer ${keys[i % KEYS]}` });
    for (let i = 0; i < 50; i++) {
      await timed(gwUrl, gwHeaders(0), whole);
      await timed(directUrl, json, wholeDirect);
    } // warm-up, not counted
    const gw = [];
    const direct = [];
    const diff = [];
    for (let i = 0; i < CFG.requests; i++) {
      // Alternate which goes first, so neither side always runs on a warmer connection.
      let a;
      let b;
      if (i % 2) {
        b = await timed(directUrl, json, wholeDirect);
        a = await timed(gwUrl, gwHeaders(0), whole);
      } else {
        a = await timed(gwUrl, gwHeaders(0), whole);
        b = await timed(directUrl, json, wholeDirect);
      }
      gw.push(a);
      direct.push(b);
      diff.push(a - b);
    }
    const nonStream = { gateway: stats(gw), direct: stats(direct), overhead: stats(diff) };

    const streamBody = JSON.stringify({
      model: 'bench/bench-1',
      stream: true,
      messages: [{ role: 'user', content: 'go' }],
    });
    const streamDirect = JSON.stringify({
      model: 'bench-1',
      stream: true,
      messages: [{ role: 'user', content: 'go' }],
    });
    const streams = [];
    let peakRssMB = null;
    const top = Math.max(...CFG.levels);
    for (const level of CFG.levels) {
      let sampler = null;
      if (level === top)
        sampler = setInterval(() => {
          const v = rssMB(server.pid);
          if (v !== null && (peakRssMB === null || v > peakRssMB)) peakRssMB = v;
        }, 250);
      const g = await streamStep(level, {
        url: gwUrl,
        headers: (i) => gwHeaders(1 + (i % (KEYS - 1))),
        body: streamBody,
      });
      if (sampler) clearInterval(sampler);
      const d = await streamStep(level, { url: directUrl, headers: () => json, body: streamDirect });
      streams.push({
        level,
        seconds: CFG.seconds,
        gateway: g,
        direct: d,
        overheadP95: g.durP95 === null || d.durP95 === null ? null : Math.round((g.durP95 - d.durP95) * 100) / 100,
        ttfbOverheadP95:
          g.ttfbP95 === null || d.ttfbP95 === null ? null : Math.round((g.ttfbP95 - d.ttfbP95) * 100) / 100,
      });
    }

    const day = new Date();
    const date = `${day.getFullYear()}-${String(day.getMonth() + 1).padStart(2, '0')}-${String(day.getDate()).padStart(2, '0')}`;
    const result = {
      schema: 1,
      date,
      runtime: CFG.runtime,
      runtimeVersion: versionOf(CFG.runtime),
      git: gitSha(),
      host: {
        name: hostName(),
        platform: process.platform,
        arch: process.arch,
        cpu: (os.cpus()[0] || {}).model || null,
        cores: os.cpus().length,
        memGB: Math.round(os.totalmem() / 2 ** 30),
        osRelease: os.release(),
      },
      config: CFG,
      startup: { ms: startup, p50: pct(startup, 50) },
      idleRssMB,
      nonStream,
      streams,
      peakRssMB,
      durationS: Math.round((Date.now() - t0) / 1000),
    };
    const dir = process.env.ZP_BENCH_OUT || path.join(__dirname, 'results');
    fs.mkdirSync(dir, { recursive: true });
    const file = path.join(dir, `${date}-${CFG.runtime}-${result.host.name}.json`);
    fs.writeFileSync(file, `${JSON.stringify(result, null, 2)}\n`);
    console.log(`wrote ${path.relative(process.cwd(), file)} in ${result.durationS} s`);
    console.log(
      `startup p50 ${result.startup.p50} ms · idle RSS ${idleRssMB} MB · overhead p50/p95/p99 ` +
        `${nonStream.overhead.p50}/${nonStream.overhead.p95}/${nonStream.overhead.p99} ms · peak RSS ${peakRssMB} MB`,
    );
    for (const s of streams)
      console.log(
        `  ${s.level} streams: ${s.gateway.completed} done, ${s.gateway.errors} errors, ` +
          `ttfb p95 ${s.gateway.ttfbP95} ms, overhead p95 ${s.overheadP95} ms`,
      );
  } finally {
    await server.stop();
    mock.stop();
  }
}

main().catch((err) => {
  console.error(err && err.code === 'EMFILE' ? 'too many open files: run `ulimit -n 8192` first' : err);
  process.exitCode = 1;
});
