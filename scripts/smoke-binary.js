// Smoke test of one built binary on this machine (P2 T6, CI build-deno): `<binary> version`; `<binary> start --demo`
// on a throwaway data folder until /login.html answers 200; /api/platform says single-file and deno; `<binary> doctor`
// exits 0. Prints one JSON line and exits non-zero on any failure. Kills only the process it started.
//   deno run -q --no-prompt --allow-read --allow-write=dist --allow-net=127.0.0.1 --allow-run=<binary> \
//     --allow-env=PATH,SystemRoot,SYSTEMROOT,PATHEXT,TEMP,TMP,windir,ComSpec --ignore-env scripts/smoke-binary.js <binary>
const { spawn, execFileSync } = require('child_process');
const fs = require('fs');
const net = require('net');
const path = require('path');

const ROOT = path.join(__dirname, '..');
const KEEP = ['PATH', 'SystemRoot', 'SYSTEMROOT', 'PATHEXT', 'TEMP', 'TMP', 'windir', 'ComSpec'];

const freePort = () =>
  new Promise((resolve, reject) => {
    const s = net.createServer();
    s.once('error', reject);
    s.listen(0, '127.0.0.1', () => {
      const { port } = s.address();
      s.close(() => resolve(port));
    });
  });

async function smoke(binary) {
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
    out.ok = true;
  } catch (err) {
    out.error = String(err.message || err).slice(0, 2000);
  } finally {
    if (child && child.exitCode === null) {
      const gone = new Promise((r) => child.once('exit', r));
      child.kill();
      await gone;
    }
    fs.rmSync(work, { recursive: true, force: true, maxRetries: 3, retryDelay: 200 });
  }
  return out;
}

smoke(process.argv[2]).then((r) => {
  console.log(JSON.stringify(r));
  process.exit(r.ok ? 0 : 1);
});
