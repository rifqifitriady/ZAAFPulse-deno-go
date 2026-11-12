// Smoke test of the Go front on this machine (P4a T6, CI build-deno): server.js under `deno run` with the "server"
// set and gateway.engine "front", supervising the given zaafpulse-gw on a throwaway data folder. Checks: /login.html
// 200 through the main port, /api/gateway/status says front and ready with a pid; then the server is stopped and the
// main port must be free again within 15 s (the gateway exited with it). Prints one JSON line; exits non-zero on any
// failure. Kills only the process it started.
//   deno run -q --no-prompt -P=test scripts/smoke-front.js <zaafpulse-gw binary>
const { spawn } = require('child_process');
const fs = require('fs');
const net = require('net');
const os = require('os');
const path = require('path');
const permissions = require('../lib/permissions');

const ROOT = path.join(__dirname, '..');
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const freePort = () =>
  new Promise((resolve, reject) => {
    const s = net.createServer();
    s.once('error', reject);
    s.listen(0, '127.0.0.1', () => {
      const { port } = s.address();
      s.close(() => resolve(port));
    });
  });
// Whether this process can listen on the port (the gateway has let go of it).
const portFree = (port) =>
  new Promise((resolve) => {
    const s = net.createServer();
    s.once('error', () => resolve(false));
    s.listen(port, '127.0.0.1', () => s.close(() => resolve(true)));
  });

async function smoke(gwBin) {
  const bin = path.resolve(gwBin);
  const work = fs.mkdtempSync(path.join(os.tmpdir(), 'zp-smoke-front-'));
  const out = { ok: false, gateway: path.basename(bin), os: `${process.platform}-${process.arch}` };
  const port = await freePort();
  const env = {
    ...process.env,
    HOME: work,
    USERPROFILE: work,
    ZAAFPULSE_DATA: work,
    ZAAFPULSE_PORT: String(port),
    ZAAFPULSE_HOST: '127.0.0.1',
    ZAAFPULSE_AUTH: 'none',
    ZAAFPULSE_RUNTIME_WATCH: 'off',
    ZAAFPULSE_GW_BIN: bin,
  };
  fs.writeFileSync(
    path.join(work, 'config.json'),
    JSON.stringify({ pricingSync: { enabled: false }, gateway: { engine: 'front' } }),
  );
  const args = [
    'run',
    '-q',
    '--no-prompt',
    `--config=${path.join(ROOT, 'deno.json')}`,
    '-P=server',
    `--allow-write=${work},${os.tmpdir()}`,
    `--allow-run=${permissions.runScope({ set: 'server', env }).join(',')}`,
    path.join(ROOT, 'server.js'),
  ];
  let child;
  let stderr = '';
  try {
    const t0 = Date.now();
    child = spawn(process.execPath, args, { cwd: ROOT, env, stdio: ['ignore', 'ignore', 'pipe'] });
    child.stderr.on('data', (c) => (stderr = (stderr + c).slice(-4000)));
    let exited = false;
    child.on('exit', () => (exited = true));
    const base = `http://127.0.0.1:${port}`;
    for (;;) {
      if (exited) throw new Error(`the server exited before answering:\n${stderr}`);
      if (Date.now() - t0 > 60000) throw new Error(`no 200 on /login.html within 60 s:\n${stderr}`);
      try {
        const r = await fetch(`${base}/login.html`, { signal: AbortSignal.timeout(1000) });
        await r.body?.cancel();
        if (r.status === 200) break;
      } catch {
        /* not up yet */
      }
      await sleep(50);
    }
    out.startupMs = Date.now() - t0;
    const st = await (await fetch(`${base}/api/gateway/status`)).json();
    out.mode = st.mode;
    out.state = st.state;
    if (st.mode !== 'front' || st.state !== 'ready' || !(st.pid > 0)) throw new Error(`status: ${JSON.stringify(st)}`);
    const gone = new Promise((r) => child.once('exit', r));
    child.kill();
    await gone;
    const t1 = Date.now();
    while (!(await portFree(port))) {
      if (Date.now() - t1 > 15000) throw new Error('the main port is still held 15 s after the server stopped');
      await sleep(100);
    }
    out.portFreeMs = Date.now() - t1;
    out.ok = true;
  } catch (err) {
    out.error = String(err.message || err).slice(0, 2000);
  } finally {
    if (child && child.exitCode === null && child.signalCode === null) {
      const gone = new Promise((r) => child.once('exit', r));
      child.kill('SIGKILL');
      await gone;
    }
    fs.rmSync(work, { recursive: true, force: true, maxRetries: 10, retryDelay: 200 });
  }
  return out;
}

smoke(process.argv[2]).then((r) => {
  console.log(JSON.stringify(r));
  process.exit(r.ok ? 0 : 1);
});
