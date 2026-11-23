// The M12 proof (P6 spec Decision 12): v1.1.0 → v2 → v1.1.0 on one data folder, through each version's HTTP API.
//   node scripts/upgrade-check.js --v2 <binary | deno-run> [--v1 DIR]
// --v2 is how v2 starts: the path of the built zaafpulse (zaafpulse-gw must sit next to it, as the unpacked archive has
// it; its folder goes first on PATH, P6 Decision 4), or `deno-run` for `deno run` of bin/zaafpulse.js with the "cli"
// set of this tree and the gateway from `npm run gw:build` (ZAAFPULSE_GW_BIN). --v1 is an unpacked v1.1.0 source folder;
// without it the source package is downloaded from the public v1.1.0 release and checked against its SHA256SUMS.txt.
// Each run gets a temp data folder (ZAAFPULSE_DATA, HOME, USERPROFILE, APPDATA, LOCALAPPDATA), ZAAFPULSE_AUTH=none and
// a free loopback port (never 4777, 4787 or 4797), and a local fake provider answers OpenAI chat completions with fixed
// usage. Steps:
//   1. v1: a provider pointing at the fake, a gateway key, a budget, one /v1/chat/completions call with the key, one
//      settings change (userName: an audit entry); the audit chain verifies.
//   2. v2 with gateway.engine "front": the gateway in front and ready, one call with the same key through it, one
//      settings change (the audit retention), the audit chain verifies.
//   3. v1 again: /login.html 200; the audit chain verifies; the gateway usage counts both calls with a cost; the key,
//      the provider and the budget are still there; every config.json key v1 wrote has the same value.
// Prints one JSON line { ok, steps: [...] } and exits non-zero on a failure. Kills only the processes it started.
const { spawn, execFileSync } = require('child_process');
const crypto = require('crypto');
const fs = require('fs');
const http = require('http');
const net = require('net');
const os = require('os');
const path = require('path');

const ROOT = path.join(__dirname, '..');
const V1_URL = 'https://github.com/rifqifitriady/ZAAFPulse/releases/download/v1.1.0/';
const V1_PACKAGE = 'zaafpulse-1.1.0-source.tar.gz';
const NEVER = [4777, 4787, 4797];
const KEEP = ['PATH', 'SystemRoot', 'SYSTEMROOT', 'PATHEXT', 'TEMP', 'TMP', 'TMPDIR', 'windir', 'ComSpec'];
const USAGE = { prompt_tokens: 1000, completion_tokens: 500, total_tokens: 1500 };
const MODEL = 'gpt-4o-mini'; // a model with a list price in v1's snapshot, so each call has a cost
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function parseArgs(argv) {
  const out = { v2: null, v1: null };
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a !== '--v2' && a !== '--v1') throw new Error(`unknown argument: ${a}`);
    const v = argv[i + 1];
    if (!v || v.startsWith('--')) throw new Error(`${a} needs a value`);
    out[a.slice(2)] = v;
    i++;
  }
  if (!out.v2) throw new Error('usage: upgrade-check.js --v2 <binary | deno-run> [--v1 DIR]');
  return out;
}

const freePort = async () => {
  for (;;) {
    const port = await new Promise((resolve, reject) => {
      const s = net.createServer();
      s.once('error', reject);
      s.listen(0, '127.0.0.1', () => {
        const { port: p } = s.address();
        s.close(() => resolve(p));
      });
    });
    if (!NEVER.includes(port)) return port;
  }
};
const portFree = (port) =>
  new Promise((resolve) => {
    const s = net.createServer();
    s.once('error', () => resolve(false));
    s.listen(port, '127.0.0.1', () => s.close(() => resolve(true)));
  });
async function until(fn, ms, what, exited = () => false) {
  const end = Date.now() + ms;
  for (;;) {
    if (exited()) throw new Error(`the server exited before ${what}`);
    try {
      const v = await fn();
      if (v) return v;
    } catch {
      /* not yet */
    }
    if (Date.now() > end) throw new Error(`no ${what} within ${ms / 1000} s`);
    await sleep(100);
  }
}

// The v1.1.0 source package: downloaded and checked against the release's SHA256SUMS.txt, then unpacked into `work`.
async function fetchV1(work) {
  const get = async (name) => {
    const r = await fetch(V1_URL + name, { signal: AbortSignal.timeout(120000) });
    if (!r.ok) throw new Error(`download ${name}: HTTP ${r.status}`);
    return Buffer.from(await r.arrayBuffer());
  };
  const sums = (await get('SHA256SUMS.txt')).toString('utf8');
  const line = sums.split(/\r?\n/).find((l) => l.trim().endsWith(`  ${V1_PACKAGE}`));
  if (!line) throw new Error(`SHA256SUMS.txt names no ${V1_PACKAGE}`);
  const want = line.trim().split(/\s+/)[0].toLowerCase();
  const pkg = await get(V1_PACKAGE);
  const got = crypto.createHash('sha256').update(pkg).digest('hex');
  if (got !== want) throw new Error(`${V1_PACKAGE}: sha256 ${got}, SHA256SUMS.txt says ${want}`);
  const dir = path.join(work, 'v1');
  fs.mkdirSync(dir);
  const file = path.join(work, V1_PACKAGE);
  fs.writeFileSync(file, pkg);
  execFileSync('tar', ['-xzf', file, '-C', dir], { stdio: 'ignore' });
  return path.join(dir, 'zaafpulse-1.1.0');
}

// A provider that answers every chat completion with USAGE and lists one model.
function fakeProvider() {
  let calls = 0;
  const server = http.createServer((req, res) => {
    let body = '';
    req.on('data', (c) => (body += c));
    req.on('end', () => {
      res.setHeader('content-type', 'application/json');
      if (req.method === 'GET' && /\/models$/.test(req.url))
        return res.end(JSON.stringify({ object: 'list', data: [{ id: MODEL, object: 'model' }] }));
      if (req.method === 'POST' && /\/chat\/completions$/.test(req.url)) {
        calls++;
        return res.end(
          JSON.stringify({
            id: `chatcmpl-m12-${calls}`,
            object: 'chat.completion',
            created: Math.floor(Date.now() / 1000),
            model: MODEL,
            choices: [{ index: 0, message: { role: 'assistant', content: 'ok' }, finish_reason: 'stop' }],
            usage: USAGE,
          }),
        );
      }
      res.statusCode = 404;
      res.end(JSON.stringify({ error: { message: 'not found' } }));
    });
  });
  return new Promise((resolve) =>
    server.listen(0, '127.0.0.1', () =>
      resolve({ port: server.address().port, calls: () => calls, close: () => new Promise((r) => server.close(r)) }),
    ),
  );
}

// How each version starts: { command, args, env, cwd }.
function v1Command(v1Dir) {
  const node = globalThis.Deno ? 'node' : process.execPath;
  return { command: node, args: [path.join(v1Dir, 'bin', 'zaafpulse.js'), 'start'], cwd: v1Dir, env: {} };
}
function v2Command(v2, env) {
  if (v2 === 'deno-run') {
    const gw = path.join(ROOT, 'gw', 'bin', process.platform === 'win32' ? 'zaafpulse-gw.exe' : 'zaafpulse-gw');
    if (!fs.existsSync(gw)) throw new Error(`${gw} is missing: run npm run gw:build`);
    const permissions = require('../lib/permissions');
    const full = { ...env, ZAAFPULSE_GW_BIN: gw };
    const home = env.HOME;
    return {
      command: 'deno',
      args: [
        'run',
        '-q',
        '--no-prompt',
        `--config=${path.join(ROOT, 'deno.json')}`,
        '-P=cli',
        `--allow-write=${permissions.writeScope({ set: 'cli', dataDir: env.ZAAFPULSE_DATA, env: full, home }).join(',')}`,
        `--allow-run=${permissions.runScope({ set: 'cli', env: full, home }).join(',')}`,
        path.join(ROOT, 'bin', 'zaafpulse.js'),
        'start',
      ],
      cwd: ROOT,
      env: { ZAAFPULSE_GW_BIN: gw },
    };
  }
  const bin = path.resolve(v2);
  const gw = path.join(path.dirname(bin), process.platform === 'win32' ? 'zaafpulse-gw.exe' : 'zaafpulse-gw');
  if (!fs.existsSync(bin)) throw new Error(`${bin} does not exist`);
  if (!fs.existsSync(gw)) throw new Error(`${gw} is missing: the gateway must sit next to the binary`);
  return {
    command: bin,
    args: ['start'],
    cwd: path.dirname(bin),
    env: { PATH: `${path.dirname(bin)}${path.delimiter}${env.PATH || ''}` },
  };
}

// Starts a server and waits for /login.html 200. stop() ends it (SIGTERM, then SIGKILL after 15 s) and waits for the
// port to be free, so the next version can take it.
async function startServer(name, cmd, env, port, children) {
  const child = spawn(cmd.command, cmd.args, {
    cwd: cmd.cwd,
    env: { ...env, ...cmd.env },
    stdio: ['ignore', 'ignore', 'pipe'],
  });
  children.add(child);
  let stderr = '';
  child.stderr.on('data', (c) => (stderr = (stderr + c).slice(-3000)));
  let exited = false;
  const gone = new Promise((r) => child.once('exit', r));
  child.once('exit', () => {
    exited = true;
    children.delete(child);
  });
  child.once('error', () => (exited = true));
  const base = `http://127.0.0.1:${port}`;
  const t0 = Date.now();
  try {
    await until(
      async () => {
        const r = await fetch(`${base}/login.html`, { signal: AbortSignal.timeout(2000) });
        await r.body?.cancel();
        return r.status === 200;
      },
      90000,
      `200 on /login.html (${name})`,
      () => exited,
    );
  } catch (err) {
    err.message += stderr ? `; stderr: ${stderr.slice(-1500)}` : '';
    throw err;
  }
  return {
    base,
    startMs: Date.now() - t0,
    stderr: () => stderr,
    // { stopMs, killed }: killed when SIGTERM did not end it within 15 s.
    async stop() {
      const t1 = Date.now();
      let killed = false;
      if (!exited) {
        child.kill('SIGTERM');
        const t = setTimeout(() => {
          if (!exited) {
            killed = true;
            child.kill('SIGKILL');
          }
        }, 15000);
        await gone;
        clearTimeout(t);
      }
      await until(() => portFree(port), 15000, `a free port ${port} after ${name} stopped`);
      return { stopMs: Date.now() - t1, killed };
    },
  };
}

// One JSON call to the dashboard API. A non-2xx answer or an `errors` list throws.
async function api(base, method, p, body) {
  let r, text;
  try {
    r = await fetch(base + p, {
      method,
      headers: body === undefined ? {} : { 'content-type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: AbortSignal.timeout(30000),
    });
    text = await r.text();
  } catch (err) {
    throw new Error(`${method} ${p}: ${err.message}`);
  }
  let j;
  try {
    j = JSON.parse(text);
  } catch {
    throw new Error(`${method} ${p}: HTTP ${r.status}, not JSON: ${text.slice(0, 300)}`);
  }
  if (r.status < 200 || r.status > 299 || (j && Array.isArray(j.errors) && j.errors.length))
    throw new Error(`${method} ${p}: HTTP ${r.status} ${JSON.stringify(j).slice(0, 500)}`);
  return j;
}
async function chat(base, secret) {
  let r, text;
  try {
    r = await fetch(`${base}/v1/chat/completions`, {
      method: 'POST',
      headers: { 'content-type': 'application/json', authorization: `Bearer ${secret}` },
      body: JSON.stringify({ model: `m12fake/${MODEL}`, messages: [{ role: 'user', content: 'M12 upgrade check' }] }),
      signal: AbortSignal.timeout(30000),
    });
    text = await r.text();
  } catch (err) {
    throw new Error(`POST /v1/chat/completions: ${err.message}`);
  }
  if (r.status !== 200) throw new Error(`/v1/chat/completions: HTTP ${r.status} ${text.slice(0, 500)}`);
  const j = JSON.parse(text);
  if (!j.usage || j.usage.prompt_tokens !== USAGE.prompt_tokens)
    throw new Error(`/v1/chat/completions: no usage passed back: ${text.slice(0, 500)}`);
  return j;
}
async function verifyAudit(base) {
  const v = await api(base, 'GET', '/api/governance/audit/verify');
  if (v.ok !== true) throw new Error(`audit verify: ${JSON.stringify(v).slice(0, 500)}`);
  return v;
}

// Every key path of `before` (objects walked, anything else compared whole) with its value in `after`, when it differs.
function changedKeys(before, after, at = '') {
  const isObj = (v) => v && typeof v === 'object' && !Array.isArray(v);
  const out = [];
  for (const k of Object.keys(before)) {
    const p = at ? `${at}.${k}` : k;
    if (!isObj(after) || !Object.hasOwn(after, k)) out.push(`${p}: missing`);
    else if (isObj(before[k]) && isObj(after[k])) out.push(...changedKeys(before[k], after[k], p));
    else if (JSON.stringify(before[k]) !== JSON.stringify(after[k]))
      out.push(`${p}: ${JSON.stringify(before[k]).slice(0, 80)} -> ${JSON.stringify(after[k]).slice(0, 80)}`);
  }
  return out;
}

async function check({ v2, v1 }) {
  const out = { ok: false, v2: v2 === 'deno-run' ? 'deno-run' : path.basename(v2), steps: [] };
  const step = (name, extra = {}) => out.steps.push({ step: name, ok: true, ...extra });
  const work = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'zp-upgrade-check-')));
  const children = new Set();
  let fake = null;
  let running = null;
  try {
    const v1Dir = v1 ? path.resolve(v1) : await fetchV1(work);
    if (!fs.existsSync(path.join(v1Dir, 'server.js'))) throw new Error(`${v1Dir} holds no server.js`);
    const v1Version = JSON.parse(fs.readFileSync(path.join(v1Dir, 'package.json'), 'utf8')).version;
    step('v1 source', { version: v1Version, from: v1 ? 'folder' : 'release (sha256 checked)' });
    fake = await fakeProvider();
    const home = path.join(work, 'home');
    const data = path.join(work, 'data');
    fs.mkdirSync(home);
    fs.mkdirSync(data);
    const port = await freePort();
    const env = {
      ...Object.fromEntries(KEEP.filter((k) => process.env[k] !== undefined).map((k) => [k, process.env[k]])),
      HOME: home,
      USERPROFILE: home,
      APPDATA: path.join(home, 'AppData', 'Roaming'),
      LOCALAPPDATA: path.join(home, 'AppData', 'Local'),
      ZAAFPULSE_DATA: data,
      ZAAFPULSE_PORT: String(port),
      ZAAFPULSE_HOST: '127.0.0.1',
      ZAAFPULSE_AUTH: 'none',
      ZAAFPULSE_RUNTIME_WATCH: 'off',
    };
    const configFile = path.join(data, 'config.json');
    // No price list download: the run needs no network beyond the v1 package.
    fs.writeFileSync(configFile, JSON.stringify({ pricingSync: { enabled: false } }, null, 2));

    // 1. v1.1.0 writes.
    running = await startServer('v1', v1Command(v1Dir), env, port, children);
    let base = running.base;
    const plat = await api(base, 'GET', '/api/platform');
    if (plat.version !== v1Version) throw new Error(`v1 /api/platform says ${plat.version}`);
    step('v1 start', { startMs: running.startMs, version: plat.version });
    await api(base, 'POST', '/api/gateway/providers', {
      action: 'save',
      id: 'm12fake',
      preset: 'custom',
      baseUrl: `http://127.0.0.1:${fake.port}/v1`,
      models: [MODEL],
      tier: 'private',
      format: 'openai',
    });
    const key = await api(base, 'POST', '/api/gateway/keys', { action: 'create', name: 'm12' });
    if (!key.secret || !key.key || !key.key.id) throw new Error(`key create: ${JSON.stringify(key).slice(0, 300)}`);
    await api(base, 'POST', '/api/cost/budgets', {
      id: 'm12-budget',
      name: 'M12 budget',
      scope: 'global',
      period: 'month',
      amount: 100,
    });
    step('v1 provider, key, budget', { keyId: key.key.id });
    await chat(base, key.secret);
    step('v1 call');
    await api(base, 'POST', '/api/config', { userName: 'M12 v1' });
    const v1Audit = await verifyAudit(base);
    step('v1 setting and audit', { audit: v1Audit });
    step('v1 stop', await running.stop());
    running = null;
    const v1Config = JSON.parse(fs.readFileSync(configFile, 'utf8'));

    // 2. v2 on the same folder, the gateway in front.
    fs.writeFileSync(
      configFile,
      JSON.stringify({ ...v1Config, gateway: { ...(v1Config.gateway || {}), engine: 'front' } }, null, 2),
    );
    running = await startServer('v2', v2Command(v2, env), env, port, children);
    base = running.base;
    const st = await until(
      async () => {
        const j = await api(base, 'GET', '/api/gateway/status');
        if (j.mode !== 'front') throw new Error(`v2 /api/gateway/status: ${JSON.stringify(j).slice(0, 500)}`);
        return j.state === 'ready' ? j : null;
      },
      60000,
      'v2 gateway ready',
    ).catch(async (err) => {
      const j = await api(base, 'GET', '/api/gateway/status').catch((e) => e.message);
      throw new Error(`${err.message}; status: ${JSON.stringify(j).slice(0, 1000)}`);
    });
    step('v2 start', { startMs: running.startMs, mode: st.mode, state: st.state, gwVersion: st.gwVersion ?? null });
    await chat(base, key.secret);
    step('v2 call (gateway in front)');
    await api(base, 'POST', '/api/governance/settings', { audit: { retentionMonths: 18 } });
    const v2Audit = await verifyAudit(base);
    step('v2 setting and audit', { audit: v2Audit });
    step('v2 stop', await running.stop());
    running = null;

    // 3. v1.1.0 reads what v2 left.
    running = await startServer('v1 again', v1Command(v1Dir), env, port, children);
    base = running.base;
    step('v1 again: /login.html 200', { startMs: running.startMs });
    const audit = await verifyAudit(base);
    step('v1 again: audit verifies', { audit });
    const gw = await api(base, 'GET', '/api/gateway');
    const u = (gw.usage && gw.usage.totals) || {};
    if (!(u.requests >= 2) || !(u.cost > 0) || u.errors !== 0 || u.input < 2 * USAGE.prompt_tokens)
      throw new Error(`v1 usage does not count both calls: ${JSON.stringify(u).slice(0, 500)}`);
    if (fake.calls() < 2) throw new Error(`the fake provider saw ${fake.calls()} calls`);
    step('v1 again: usage counts both calls', {
      requests: u.requests,
      input: u.input,
      output: u.output,
      cost: u.cost,
      fakeCalls: fake.calls(),
      byKey: (gw.usage.byKey || {})[key.key.id] || null,
    });
    if (!(gw.keys || []).some((k) => k.id === key.key.id && !k.revokedAt)) throw new Error('v1 again: the key is gone');
    if (!(gw.providers || []).some((p) => p.id === 'm12fake')) throw new Error('v1 again: the provider is gone');
    const budgets = await api(base, 'GET', '/api/cost/budgets');
    if (!(budgets.budgets || []).some((b) => b.id === 'm12-budget')) throw new Error('v1 again: the budget is gone');
    step('v1 again: key, provider, budget kept');
    step('v1 again: stop', await running.stop());
    running = null;
    const finalConfig = JSON.parse(fs.readFileSync(configFile, 'utf8'));
    const changed = changedKeys(v1Config, finalConfig);
    if (changed.length) throw new Error(`config.json keys v1 wrote changed: ${changed.join('; ')}`);
    step('config.json: every v1 key unchanged', { keys: Object.keys(v1Config).length });
    out.ok = true;
  } catch (err) {
    out.error = String((err && err.message) || err).slice(0, 3000);
  } finally {
    if (running) await running.stop().catch(() => {});
    for (const c of children) {
      c.kill('SIGKILL');
      await new Promise((r) => (c.exitCode !== null || c.signalCode !== null ? r() : c.once('exit', r)));
    }
    if (fake) await fake.close();
    fs.rmSync(work, { recursive: true, force: true, maxRetries: 10, retryDelay: 200 });
  }
  return out;
}

module.exports = { parseArgs, changedKeys };

if (require.main === module) {
  let args;
  try {
    args = parseArgs(process.argv.slice(2));
  } catch (err) {
    console.error(err.message);
    process.exit(2);
  }
  check(args).then((r) => {
    console.log(JSON.stringify(r));
    process.exit(r.ok ? 0 : 1);
  });
}
