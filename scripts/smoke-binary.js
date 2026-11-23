// Smoke test of one built binary on this machine (P2 T6, CI build-deno): `<binary> version`; `<binary> start --demo`
// on a throwaway data folder until /login.html answers 200; /api/platform says single-file and deno; `<binary> doctor`
// exits 0. Prints one JSON line and exits non-zero on any failure. Kills only the process it started.
// With --front <gw> (P6 Task 4; the gateway must sit next to the binary, as the unpacked archive has it) a second pass
// starts the binary (without --demo, which always serves on Deno) with gateway.engine "front" and its folder first on
// PATH (P6 Decision 4): /login.html answers through the gateway on the main port, /api/gateway/status says front,
// ready, a pid and the package version as gwVersion, and after the stop the main port is free within 15 s. The line then carries front: { ok, gwVersion,
// readyMs } (readyMs: spawn to the gateway ready; M7 information, not quiet-confirmed). The gateway is spawned by the
// binary, so the binary's grant is all this script needs.
//   deno run -q --no-prompt --allow-read --allow-write=dist --allow-net=127.0.0.1 --allow-run=<binary> \
//     --allow-env=PATH,SystemRoot,SYSTEMROOT,PATHEXT,TEMP,TMP,windir,ComSpec --ignore-env scripts/smoke-binary.js \
//     <binary> [--front <gw>]
const { spawn, execFileSync } = require('child_process');
const fs = require('fs');
const net = require('net');
const path = require('path');

const ROOT = path.join(__dirname, '..');
const KEEP = ['PATH', 'SystemRoot', 'SYSTEMROOT', 'PATHEXT', 'TEMP', 'TMP', 'windir', 'ComSpec'];

// The IPC v1 schemas the binary must carry: every ipc/v1/*.schema.json of this tree (the envelope and one per message
// type), counted, never written down, so a new message type (P5's egress.event) cannot leave the check behind.
const SCHEMA_COUNT = fs.readdirSync(path.join(ROOT, 'ipc', 'v1')).filter((f) => f.endsWith('.schema.json')).length;
/** Whether `<binary> doctor`'s text says it read every schema. @param {string} doctor */
const doctorHasSchemas = (doctor) => new RegExp(`Gateway IPC\\s+v1, ${SCHEMA_COUNT} schemas`).test(doctor);

const freePort = () =>
  new Promise((resolve, reject) => {
    const s = net.createServer();
    s.once('error', reject);
    s.listen(0, '127.0.0.1', () => {
      const { port } = s.address();
      s.close(() => resolve(port));
    });
  });

// Polls until fn() is truthy (its value) or throws after ms; `exited` ends it early.
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
    await new Promise((r) => setTimeout(r, 50));
  }
}
const answers = async (url) => {
  const r = await fetch(url, { signal: AbortSignal.timeout(1000) });
  await r.body?.cancel();
  return r.status === 200;
};
// Free when a listen on it succeeds.
const portFree = (port) =>
  new Promise((resolve) => {
    const s = net.createServer();
    s.once('error', () => resolve(false));
    s.listen(port, '127.0.0.1', () => s.close(() => resolve(true)));
  });

// The --front pass: the binary with the gateway in front, in its own data folder and on its own port.
async function front(bin, gw, version, baseEnv, work, r) {
  if (path.dirname(path.resolve(gw)) !== path.dirname(bin))
    throw new Error(`--front: ${gw} is not next to ${bin} (unpack the archive and pass both from its folder)`);
  if (!fs.existsSync(gw)) throw new Error(`--front: ${gw} does not exist`);
  const data = path.join(work, 'front-data');
  fs.mkdirSync(data, { recursive: true });
  fs.writeFileSync(path.join(data, 'config.json'), JSON.stringify({ gateway: { engine: 'front' } }));
  const port = await freePort();
  const env = {
    ...baseEnv,
    PATH: `${path.dirname(bin)}${path.delimiter}${baseEnv.PATH || ''}`,
    ZAAFPULSE_DATA: data,
    ZAAFPULSE_PORT: String(port),
  };
  const t0 = Date.now();
  // Not --demo (server.js ENGINE). The throwaway HOME holds no logs to read.
  const child = spawn(bin, ['start'], { env, stdio: 'ignore' });
  let exited = false;
  const gone = new Promise((res) => child.once('exit', res));
  child.on('exit', () => (exited = true));
  const base = `http://127.0.0.1:${port}`;
  try {
    await until(
      () => answers(`${base}/login.html`),
      30000,
      '200 on /login.html (front)',
      () => exited,
    );
    const st = await until(
      async () => {
        const res = await fetch(`${base}/api/gateway/status`, { signal: AbortSignal.timeout(1000) });
        const j = await res.json();
        if (j.mode !== 'front') throw new Error(`/api/gateway/status: ${JSON.stringify(j).slice(0, 500)}`);
        return j.state === 'ready' ? j : null;
      },
      30000,
      'gateway ready',
      () => exited,
    ).catch(async (err) => {
      const res = await fetch(`${base}/api/gateway/status`).catch(() => null);
      const j = res ? await res.json().catch(() => null) : null;
      throw new Error(`${err.message}; status: ${JSON.stringify(j).slice(0, 1000)}`);
    });
    r.readyMs = Date.now() - t0;
    r.gwVersion = st.gwVersion ?? null;
    if (!Number.isInteger(st.pid)) throw new Error(`/api/gateway/status: no pid: ${JSON.stringify(st).slice(0, 500)}`);
    if (r.gwVersion !== version) throw new Error(`gwVersion ${r.gwVersion}, package.json ${version}`);
  } finally {
    if (child.exitCode === null && child.signalCode === null) {
      child.kill();
      await gone;
    }
  }
  await until(() => portFree(port), 15000, `a free main port ${port} after the stop`);
  r.ok = true;
}

async function smoke(binary, { gw = null } = {}) {
  const bin = path.resolve(binary);
  const version = JSON.parse(fs.readFileSync(path.join(ROOT, 'package.json'), 'utf8')).version;
  const work = fs.mkdtempSync(path.join(ROOT, 'dist', '.smoke-'));
  const port = await freePort();
  const env = {
    ...Object.fromEntries(KEEP.filter((k) => process.env[k] !== undefined).map((k) => [k, process.env[k]])),
    HOME: work,
    USERPROFILE: work,
    APPDATA: path.join(work, 'AppData', 'Roaming'),
    LOCALAPPDATA: path.join(work, 'AppData', 'Local'),
    ZAAFPULSE_DATA: path.join(work, 'data'),
    ZAAFPULSE_PORT: String(port),
    // Demo mode asks for a sign-in on a Linux machine with other login users (a CI runner has some).
    ZAAFPULSE_AUTH: 'none',
  };
  const out = { ok: false, binary: path.basename(bin) };
  let child;
  try {
    out.version = execFileSync(bin, ['version'], { env, encoding: 'utf8', timeout: 30000 }).trim();
    if (out.version !== version) throw new Error(`version says ${out.version}, package.json ${version}`);
    const t0 = Date.now();
    child = spawn(bin, ['start', '--demo'], { env, stdio: 'ignore' });
    let exited = false;
    child.on('exit', () => (exited = true));
    const base = `http://127.0.0.1:${port}`;
    for (;;) {
      if (exited) throw new Error('the server exited before answering');
      if (Date.now() - t0 > 30000) throw new Error('no 200 on /login.html within 30 s');
      try {
        const r = await fetch(`${base}/login.html`, { signal: AbortSignal.timeout(1000) });
        await r.body?.cancel();
        if (r.status === 200) break;
      } catch {
        /* not up yet */
      }
      await new Promise((r) => setTimeout(r, 50));
    }
    out.startupMs = Date.now() - t0;
    const p = await (await fetch(`${base}/api/platform`)).json();
    out.single = p.single;
    out.runtime = p.runtime;
    if (p.single !== true || p.runtime !== 'deno') throw new Error(`/api/platform: ${JSON.stringify(p)}`);
    const doctor = execFileSync(bin, ['doctor'], { env, encoding: 'utf8', timeout: 60000 });
    if (!/\(single-file build\)/.test(doctor) || !/Deno\s+\d/.test(doctor)) throw new Error(`doctor:\n${doctor}`);
    // The binary carries the IPC v1 schemas (P4a E5).
    if (!doctorHasSchemas(doctor)) throw new Error(`doctor: not all ${SCHEMA_COUNT} IPC schemas:\n${doctor}`);
    if (child.exitCode === null && child.signalCode === null) {
      const gone = new Promise((r) => child.once('exit', r));
      child.kill();
      await gone;
    }
    if (gw) {
      out.front = { ok: false, gwVersion: null, readyMs: null };
      await front(bin, path.resolve(gw), version, env, work, out.front);
    }
    out.ok = true;
  } catch (err) {
    out.error = String(err.message || err).slice(0, 2000);
  } finally {
    if (child && child.exitCode === null && child.signalCode === null) {
      const gone = new Promise((r) => child.once('exit', r));
      child.kill();
      await gone;
    }
    fs.rmSync(work, { recursive: true, force: true, maxRetries: 3, retryDelay: 200 });
  }
  return out;
}

function parseArgs(argv) {
  const i = argv.indexOf('--front');
  const gw = i >= 0 ? argv[i + 1] : null;
  if (i >= 0 && (!gw || gw.startsWith('-'))) throw new Error('--front needs the gateway path');
  const rest = argv.filter((_, k) => i < 0 || (k !== i && k !== i + 1));
  if (rest.length !== 1) throw new Error('usage: smoke-binary.js <binary> [--front <gw>]');
  return { binary: rest[0], gw };
}

if (require.main === module) {
  const args = parseArgs(process.argv.slice(2));
  smoke(args.binary, { gw: args.gw }).then((r) => {
    console.log(JSON.stringify(r));
    process.exit(r.ok ? 0 : 1);
  });
}

module.exports = { SCHEMA_COUNT, doctorHasSchemas, parseArgs };
