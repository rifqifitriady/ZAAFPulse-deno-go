// The gateway contract harness (P0 T5). Black-box: it starts the fake upstream (./upstream.js), starts server.js as a
// child process on a temp data folder (or uses a running instance at ZP_CONTRACT_URL), and seeds providers, routes and
// keys through the admin API only, so the same cases hold any engine behind /v1/* to the same contract.
//   ZP_RUNTIME=node|deno|binary   what runs the server (default node); binary needs ZP_BINARY=<path of a built binary>
//   ZP_CONTRACT_URL        a running instance on this machine (sign-in off) instead of spawning one; it must have been
//                          started with SEED_CONFIG in its config.json (prices are read at start, for budgets)
//   ZP_CONTRACT_KEY        with ZP_CONTRACT_URL: the main key to use instead of creating one
//   ZP_ENGINE=front        gateway.engine "front": the Go gateway (built by scripts/ipc/build-stub.js, or ZP_GW_BIN)
//                          holds the port and forwards every case to the server (P4a T6)
//   ZP_ENGINE=go           front, with chat on the go engine (P4b): the gateway serves the chat calls it can itself
//                          and hands the others to the server
// Nothing here reaches the network: the providers point at the fake upstream on 127.0.0.1, price sync is off, and
// the judge ports, notifications and runtime polling are off (as test/helpers.js NO_JUDGE_PORTS).
const fs = require('fs');
const os = require('os');
const net = require('net');
const path = require('path');
const { spawn } = require('child_process');
const { NO_JUDGE_PORTS } = require('../helpers');
const { startUpstream } = require('./upstream');
const { buildStub } = require('../../scripts/ipc/build-stub');
const permissions = require('../../lib/permissions');

const ROOT = path.join(__dirname, '..', '..');
const JSON_HEADERS = { 'content-type': 'application/json' };
// Prices per million tokens. m1: 1000 in + 500 out = US$0.002 = 2,000,000 nano-USD.
const PRICES = {
  m1: { input: 1, output: 2 },
  m2: { input: 1, output: 2 },
  'claude-x': { input: 3, output: 15 },
  'gpt-5-codex': { input: 1, output: 2 },
};
// config.json of a spawned server: no price sync (no network), the contract prices; with ZP_ENGINE=front, the Go
// gateway in front.
const SEED_CONFIG = {
  pricingSync: { enabled: false },
  pricing: PRICES,
  ...(process.env.ZP_ENGINE === 'front' ? { gateway: { engine: 'front' } } : {}),
  ...(process.env.ZP_ENGINE === 'go' ? { gateway: { engine: 'front', engines: { chat: 'go' } } } : {}),
};

// The environment a front-mode server needs: the gateway binary. Throws without Go: a front run is asked for.
function gatewayEnv(config) {
  if (!(config && config.gateway && config.gateway.engine === 'front')) return {};
  const built = buildStub();
  if (built.skip) throw new Error(`gateway.engine "front" needs the gateway binary: ${built.skip}`);
  return { ZAAFPULSE_GW_BIN: built.bin };
}
const UPSTREAM_SECRET = { oa: 'sk-contract-oa', an: 'sk-contract-an', rs: 'sk-contract-rs' };

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

function freePort() {
  return new Promise((resolve, reject) => {
    const s = net.createServer();
    s.once('error', reject);
    s.listen(0, '127.0.0.1', () => {
      const { port } = s.address();
      s.close(() => resolve(port));
    });
  });
}

// The command that runs server.js under a runtime. Under `deno test` process.execPath is deno, so node is found on
// PATH; under `node --test` deno is found on PATH. Deno gets the "server" set of deno.json (never -A) with the write
// scope of this data folder and the temp folder, as a real launch narrows it (lib/permissions.js); never a prompt.
function command(runtime, data, env = {}) {
  const server = path.join(ROOT, 'server.js');
  const underDeno = !!(process.versions && process.versions.deno);
  if (runtime === 'node') return { bin: underDeno ? 'node' : process.execPath, args: [server] };
  if (runtime === 'deno')
    return {
      bin: underDeno ? process.execPath : 'deno',
      args: [
        'run',
        '-q',
        '--no-prompt',
        `--config=${path.join(ROOT, 'deno.json')}`,
        '-P=server',
        `--allow-write=${data},${os.tmpdir()}`,
        // Front mode: the set's programs plus the gateway binary (lib/permissions.js runScope); else the set as it is.
        ...(env.ZAAFPULSE_GW_BIN
          ? [`--allow-run=${permissions.runScope({ set: 'server', env: { ...process.env, ...env } }).join(',')}`]
          : []),
        server,
      ],
    };
  if (runtime === 'binary') {
    if (!process.env.ZP_BINARY) throw new Error('ZP_RUNTIME=binary needs ZP_BINARY, the path of a built binary');
    return { bin: path.resolve(process.env.ZP_BINARY), args: ['start'] };
  }
  throw new Error(`ZP_RUNTIME must be node, deno or binary, not "${runtime}"`);
}

// startServer({ runtime, config, env, before }) -> { base, data, pid, startupMs, stderr(), stop() }. Ready means the
// first 200 on /login.html (the benchmark's startup measure). A port taken in between is retried on another one.
// before(data), when given, runs once the data folder is made and before the server starts.
async function startServer({
  runtime = process.env.ZP_RUNTIME || 'node',
  config = SEED_CONFIG,
  env = {},
  before = async () => {},
} = {}) {
  for (let attempt = 0; attempt < 5; attempt++) {
    const data = fs.mkdtempSync(path.join(os.tmpdir(), 'zp-contract-'));
    fs.writeFileSync(path.join(data, 'config.json'), JSON.stringify(config, null, 2));
    await before(data);
    const port = await freePort();
    const gwEnv = gatewayEnv(config);
    const { bin, args } = command(runtime, data, { ...env, ...gwEnv });
    const started = performance.now();
    const child = spawn(bin, args, {
      cwd: ROOT,
      env: {
        ...process.env,
        ...NO_JUDGE_PORTS,
        ZAAFPULSE_AUTH: 'none',
        ZAAFPULSE_CONTENT: '',
        DEMO: '',
        ...gwEnv,
        ...env,
        ZAAFPULSE_PORT: String(port),
        ZAAFPULSE_HOST: '127.0.0.1',
        ZAAFPULSE_DATA: data,
        HOME: data,
        USERPROFILE: data,
        TMPDIR: data,
        TEMP: data,
        TMP: data,
      },
      stdio: ['ignore', 'ignore', 'pipe'],
    });
    let stderr = '';
    child.stderr.on('data', (c) => {
      stderr = (stderr + c).slice(-8000);
    });
    let exited = false;
    child.on('exit', () => {
      exited = true;
    });
    const base = `http://127.0.0.1:${port}`;
    // Backstop: a child still running when this process exits normally or through process.exit() or an uncaught error
    // is killed. It does not run when this process is killed by a signal (a test runner's timeout kill, SIGKILL).
    const reap = () => {
      if (!exited) child.kill('SIGKILL');
    };
    process.once('exit', reap);
    const stop = async () => {
      if (!exited) {
        let release;
        let giveUp;
        const gone = new Promise((resolve) => {
          release = resolve;
          child.once('exit', resolve);
        });
        child.kill();
        // SIGTERM first; if the child is still there after 5 s, SIGKILL it and wait at most 2 s more.
        const killTimer = setTimeout(() => {
          child.kill('SIGKILL');
          giveUp = setTimeout(release, 2000);
        }, 5000);
        await gone;
        clearTimeout(killTimer);
        clearTimeout(giveUp);
      }
      process.removeListener('exit', reap);
      // Retries: on Windows the folder can stay busy for a moment after the child exits.
      fs.rmSync(data, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 });
    };
    while (!exited && performance.now() - started < 30000) {
      try {
        const r = await fetch(`${base}/login.html`, { signal: AbortSignal.timeout(2000) });
        await r.arrayBuffer();
        if (r.status === 200)
          return { base, data, pid: child.pid, startupMs: performance.now() - started, stderr: () => stderr, stop };
      } catch {
        /* not listening yet */
      }
      await sleep(25);
    }
    await stop();
    if (attempt === 4) throw new Error(`server.js did not start under ${runtime}:\n${stderr}`);
  }
  throw new Error('unreachable');
}

// One HTTP client for a base URL. call(path, { method, body, key, headers }) -> { status, headers, text, json }.
// The key goes as a bearer token unless the caller's headers already carry one (x-api-key, x-goog-api-key).
function clientFor(base) {
  async function call(p, { method = 'POST', body, key, headers = {}, raw } = {}) {
    const h = {
      ...(method === 'GET' ? {} : JSON_HEADERS),
      ...(key ? { authorization: `Bearer ${key}` } : {}),
      ...headers,
    };
    const r = await fetch(base + p, {
      method,
      headers: h,
      body: raw !== undefined ? raw : body === undefined ? undefined : JSON.stringify(body),
      signal: AbortSignal.timeout(20000),
    });
    const text = await r.text();
    let json = null;
    try {
      json = JSON.parse(text);
    } catch {
      /* SSE or text */
    }
    return { status: r.status, headers: r.headers, text, json };
  }
  // The dashboard admin API (sign-in off): POST /api/gateway/<route>.
  const admin = async (route, body) => {
    const r = await call(`/api/gateway/${route}`, { body });
    if (r.status !== 200 || (r.json && r.json.errors)) throw new Error(`admin ${route}: ${r.status} ${r.text}`);
    return r.json;
  };
  return {
    call,
    admin,
    // The usage records, newest first (GET /api/gateway/logs).
    // With chat on the go engine a call's line reaches the log through the IPC ingest, a few milliseconds after the
    // answer (P4b deviation 5): the list is read until two reads 100 ms apart agree (at most 2 s).
    logs: async () => {
      const read = async () => (await call('/api/gateway/logs', { method: 'GET' })).json.entries;
      let entries = await read();
      if (process.env.ZP_ENGINE !== 'go') return entries;
      for (let i = 0; i < 20; i++) {
        await sleep(100);
        const again = await read();
        if (again.length === entries.length && JSON.stringify(again[0]) === JSON.stringify(entries[0])) return again;
        entries = again;
      }
      return entries;
    },
    // A new key: { secret, id }. Defaults: every scope, a high rate limit so cases never limit each other.
    newKey: async (input = {}) => {
      const r = await admin('keys', { action: 'create', name: 'contract', rpm: 10000, ...input });
      return { secret: r.secret, id: r.key.id };
    },
    setGuardrails: (policy) => admin('settings', { guardrails: policy }),
  };
}

// The providers every case can use, all at the fake upstream: oa (OpenAI format, external, approved), an (Anthropic
// Messages format, external), rs (OpenAI format with the native Responses API), pv (private: your own hardware),
// lr (LiteLLM preset on automatic tier: a local router, external for the guardrails), dead (nothing listens).
function providerList(upstreamUrl) {
  const store = (value) => ({ mode: 'store', value });
  return [
    {
      id: 'oa',
      preset: 'custom',
      baseUrl: upstreamUrl,
      tier: 'external',
      approved: true,
      models: ['m1'],
      secret: store(UPSTREAM_SECRET.oa),
    },
    {
      id: 'an',
      preset: 'custom',
      format: 'anthropic',
      baseUrl: upstreamUrl,
      tier: 'external',
      approved: true,
      models: ['claude-x'],
      secret: store(UPSTREAM_SECRET.an),
    },
    {
      id: 'rs',
      preset: 'custom',
      responses: true,
      baseUrl: upstreamUrl,
      tier: 'external',
      approved: true,
      models: ['gpt-5-codex'],
      secret: store(UPSTREAM_SECRET.rs),
    },
    { id: 'pv', preset: 'custom', baseUrl: upstreamUrl, tier: 'private', models: ['m1'] },
    { id: 'lr', preset: 'litellm', baseUrl: upstreamUrl, models: ['m1'] },
    { id: 'dead', preset: 'custom', baseUrl: 'http://127.0.0.1:9', tier: 'external', approved: true, models: ['m1'] },
  ];
}
const ROUTES = [{ name: 'router/fallback', targets: ['oa/fail503', 'oa/m1'], strategy: 'fallback' }];

// start() -> { base, key, keyId, upstream, call, admin, logs, newKey, setGuardrails, stop }. One per test file.
async function start() {
  const upstream = await startUpstream();
  let server = null;
  try {
    const url = process.env.ZP_CONTRACT_URL;
    server = url ? { base: url.replace(/\/+$/, ''), stop: async () => {} } : await startServer();
    const c = clientFor(server.base);
    for (const p of providerList(upstream.url)) await c.admin('providers', { action: 'save', ...p });
    for (const r of ROUTES) await c.admin('routes', { action: 'save', ...r });
    await c.setGuardrails({ scope: 'external', secrets: 'redact', pii: 'off', injection: 'log' });
    const own = url && process.env.ZP_CONTRACT_KEY;
    const main = own ? { secret: own, id: null } : await c.newKey({ scopes: ['llm', 'agents', 'mcp', 'rag'] });
    return {
      ...c,
      base: server.base,
      key: main.secret,
      keyId: main.id,
      upstream,
      server,
      stop: async () => {
        await server.stop();
        await upstream.close();
      },
    };
  } catch (err) {
    if (server) await server.stop();
    await upstream.close();
    throw err;
  }
}

// SSE text -> [{ event, data }] (data parsed as JSON when it is; comments dropped).
function sseEvents(text) {
  return text
    .split('\n\n')
    .filter((b) => b.trim() && !b.startsWith(':'))
    .map((b) => {
      const event = (b.match(/^event: ?(.+)$/m) || [])[1] || null;
      const raw = (b.match(/^data: ?(.+)$/m) || [])[1];
      let data = raw;
      try {
        data = JSON.parse(raw);
      } catch {
        /* [DONE] */
      }
      return { event, data };
    });
}

module.exports = { start, startServer, clientFor, sseEvents, SEED_CONFIG, PRICES, UPSTREAM_SECRET, ROUTES };
