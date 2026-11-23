// The runner of the final benchmarks (scripts/final-bench.js): starts each scenario's bench script as a child on loopback
// with fake keys, its own temp data folders and free ports (the harness's), samples the machine while it runs (noise),
// keeps track of every process in its tree, and stops them all at the end, on a failure or on Ctrl-C.
const fs = require('fs');
const os = require('os');
const path = require('path');
const { spawn, spawnSync } = require('child_process');
const procs = require('./procs');
const sc = require('./scenarios');

const ROOT = path.join(__dirname, '..', '..');
const underDeno = !!(process.versions && process.versions.deno);
const IS_WIN = process.platform === 'win32';

// Inherited variables a child must not see: they would point a bench at the owner's instance, change its knobs, turn
// OTLP export on to a real collector, or send loopback calls through a proxy.
const SCRUB = [
  /^ZP_BENCH_/,
  /^ZP_CONTRACT_/,
  /^ZP_ENGINE$/,
  /^ZP_RUNTIME$/,
  /^ZP_BINARY$/,
  /^ZP_GW_BIN$/,
  /^ZP_SHADOW_TALLY$/,
  /^ZAAFPULSE_/,
  /^OTEL_/,
  /^(HTTPS?|ALL|NO)_PROXY$/i,
  /^NODE_OPTIONS$/,
];

/** The environment of every child: this one without SCRUB, loopback exempt from proxies, plus extra. */
function childEnv(extra = {}, from = process.env) {
  const env = {};
  for (const [k, v] of Object.entries(from)) if (!SCRUB.some((re) => re.test(k))) env[k] = v;
  return { ...env, NO_PROXY: '127.0.0.1,localhost,::1', no_proxy: '127.0.0.1,localhost,::1', ...extra };
}

/**
 * How to run a JS file of a tree as a child with this runner's runtime: Node as is, Deno with the tree's "test"
 * permission set (as the bench and test scripts run under Deno).
 * @param {string} script @param {string[]} args @param {string} [root]
 */
function jsCommand(script, args = [], root = ROOT) {
  if (!underDeno) return { cmd: process.execPath, args: [script, ...args] };
  return { cmd: process.execPath, args: ['run', '-q', '--no-prompt', `--config=${path.join(root, 'deno.json')}`, '-P=test', script, ...args] };
}

/** POSIX: raise the open-files limit first (500 streams need about 2,000), then exec the command. */
function withFileLimit(c) {
  if (IS_WIN) return c;
  return {
    cmd: 'sh',
    args: ['-c', `ulimit -n ${8192} 2>/dev/null || ulimit -n "$(ulimit -Hn)" 2>/dev/null; exec "$0" "$@"`, c.cmd, ...c.args],
  };
}

/**
 * Samples every process every intervalMs: the CPU used outside this runner's tree (percent of one core) and the load
 * average; remembers every process seen in the tree, so cleanup can stop orphans too.
 */
function createSampler({ selfPid = process.pid, intervalMs = 2000, list = () => procs.listProcesses(), loadavg = () => os.loadavg() } = {}) {
  const samples = [];
  const seen = new Map(); // pid -> args, every process seen in this runner's tree
  let prev = null;
  let prevAt = 0;
  let timer = null;
  const ignore = (p) => p.pid === 0 || p.name === 'kernel_task' || /^\[.*\]$/.test(p.args) || /^(System|Idle|System Idle Process)$/.test(p.args);
  const take = () => {
    const now = list();
    const at = Date.now();
    const tree = procs.treeOf(now, [selfPid]);
    for (const p of now) if (tree.has(p.pid) && p.pid !== selfPid) seen.set(p.pid, p.args);
    if (prev && now.length) {
      const cpu = procs.cpuBetween(prev, now, (at - prevAt) / 1000);
      const foreign = cpu.filter((c) => !tree.has(c.pid) && !ignore(c)).reduce((a, c) => a + c.cpu, 0);
      const load = loadavg()[0];
      samples.push({ at, foreign: Math.round(foreign * 10) / 10, load, top: cpu.filter((c) => !tree.has(c.pid) && !ignore(c)).slice(0, 3).map((c) => `${c.name} ${Math.round(c.cpu)}%`) });
    }
    if (now.length) {
      prev = now;
      prevAt = at;
    }
  };
  return {
    start() {
      take();
      timer = setInterval(take, intervalMs);
      timer.unref?.();
    },
    stop() {
      if (timer) clearInterval(timer);
      timer = null;
    },
    take,
    mark: () => samples.length,
    /** Noise stats from sample index `from`; noisy per the thresholds, null when there was no sample. */
    window(from, { meanLimit = 50, maxLimit = 200 } = {}) {
      const w = samples.slice(from);
      if (!w.length) return { noisy: null, noise: null };
      const f = w.map((s) => s.foreign);
      const l = w.map((s) => s.load);
      const mean = (xs) => Math.round((xs.reduce((a, b) => a + b, 0) / xs.length) * 10) / 10;
      const noise = {
        samples: w.length,
        foreignMean: mean(f),
        foreignMax: Math.max(...f),
        loadMean: Math.round(mean(l) * 100) / 100,
        loadMax: Math.round(Math.max(...l) * 100) / 100,
        busiest: w.reduce((a, s) => (s.foreign > a.foreign ? s : a)).top,
      };
      return { noisy: noise.foreignMean > meanLimit || noise.foreignMax > maxLimit, noise };
    },
    loadStats() {
      const l = samples.map((s) => s.load).filter((x) => x > 0);
      if (!l.length) return null;
      return { min: Math.min(...l), mean: Math.round((l.reduce((a, b) => a + b, 0) / l.length) * 100) / 100, max: Math.max(...l) };
    },
    seen: () => [...seen].map(([pid, args]) => ({ pid, args })),
  };
}

const live = new Set(); // children running now

/**
 * Runs one child to its end: { code, stdout, stderr, timedOut }. stderr is passed through when echo is on.
 * @param {{ cmd: string, args: string[] }} c
 */
function runChild(c, { env, cwd = ROOT, timeoutMs = 600000, echo = false } = {}) {
  return new Promise((resolve) => {
    let child;
    try {
      child = spawn(c.cmd, c.args, { cwd, env, stdio: ['ignore', 'pipe', 'pipe'] });
    } catch (e) {
      resolve({ code: null, stdout: '', stderr: String(e.message || e), timedOut: false });
      return;
    }
    live.add(child);
    let stdout = '';
    let stderr = '';
    child.stdout.on('data', (d) => {
      stdout = (stdout + d).slice(-1_000_000);
    });
    child.stderr.on('data', (d) => {
      stderr = (stderr + d).slice(-20000);
      if (echo) process.stderr.write(d);
    });
    let timedOut = false;
    const timer = setTimeout(() => {
      timedOut = true;
      killTree(child.pid);
    }, timeoutMs);
    child.on('error', (e) => {
      stderr += String(e.message || e);
    });
    child.on('close', (code) => {
      clearTimeout(timer);
      live.delete(child);
      resolve({ code, stdout, stderr, timedOut });
    });
  });
}

/** Stops a child and everything under it (this runner's own tree only). */
function killTree(pid) {
  const now = procs.listProcesses();
  const tree = procs.treeOf(now, [pid]);
  const list = now.filter((p) => tree.has(p.pid));
  return procs.stopAll(list.length ? list : [{ pid, args: '' }], { graceMs: 3000 });
}

/** Stops every running child, and every process this runner ever saw in its tree that is still there. */
async function stopEverything(sampler) {
  const now = procs.listProcesses();
  const tree = procs.treeOf(now, [process.pid]);
  const mine = now.filter((p) => tree.has(p.pid) && p.pid !== process.pid);
  const seen = sampler ? sampler.seen() : [];
  const all = new Map([...seen, ...mine].map((p) => [p.pid, p]));
  for (const c of live) {
    try {
      c.kill();
    } catch {
      /* gone */
    }
  }
  return procs.stopAll([...all.values()], { graceMs: 3000 });
}

const lastJson = (text) => {
  const lines = String(text || '')
    .trim()
    .split('\n')
    .reverse();
  for (const l of lines) {
    try {
      const v = JSON.parse(l);
      if (v && typeof v === 'object') return v;
    } catch {
      /* not this line */
    }
  }
  return null;
};

/**
 * One repeat of one scenario: { ok, data, error, durationS, file? }. prep: { gwBin, baseTree?, baseGwBin?, binary? }.
 */
async function runScenario(def, { smoke, runtime, prep, tmp, echo = false }) {
  const t0 = Date.now();
  const done = (o) => ({ durationS: Math.round((Date.now() - t0) / 1000), ...o });
  const timeoutMs = (sc.estimateSeconds(def, { smoke }) * 4 + 180) * 1000;
  try {
    if (def.kind === 'run') {
      const tree = def.commit ? prep.baseTree : ROOT;
      if (!tree) return done({ ok: false, error: `the base tree ${def.commit} could not be prepared` });
      const out = fs.mkdtempSync(path.join(tmp, `${def.id}-`));
      const env = childEnv({
        ...sc.runEnv({ ...def, runtime: def.runtime === 'node' ? 'node' : runtime }, { smoke }),
        ZP_GW_BIN: def.commit ? prep.baseGwBin : prep.gwBin,
        ZP_BENCH_OUT: out,
      });
      const r = await runChild(withFileLimit(jsCommand(path.join(tree, 'bench', 'run.js'), [], tree)), { env, cwd: tree, timeoutMs, echo });
      const files = fs.readdirSync(out).filter((f) => f.endsWith('.json'));
      if (r.code !== 0 || files.length !== 1)
        return done({ ok: false, error: r.timedOut ? `timed out after ${timeoutMs / 1000} s` : tail(r.stderr) || `exit ${r.code}` });
      return done({ ok: true, data: JSON.parse(fs.readFileSync(path.join(out, files[0]), 'utf8')) });
    }
    if (def.kind === 'egress' || def.kind === 'json') {
      const script = path.join(ROOT, def.kind === 'egress' ? 'bench/egress-overhead.js' : def.script);
      const env = childEnv({ ZP_GW_BIN: prep.gwBin });
      const r = await runChild(withFileLimit(jsCommand(script, sc.scriptArgs(def, { smoke, runtime }))), { env, timeoutMs, echo });
      const data = lastJson(r.stdout);
      if (r.code !== 0 || !data) return done({ ok: false, error: r.timedOut ? 'timed out' : tail(r.stderr) || `exit ${r.code}` });
      return done({ ok: true, data });
    }
    if (def.kind === 'binary') {
      if (!prep.binary) return done({ ok: false, error: prep.binaryError || 'no packaged binary (--binary, or the build)' });
      fs.mkdirSync(path.join(ROOT, 'dist'), { recursive: true });
      const script = path.join(ROOT, 'scripts', 'smoke-binary.js');
      const args = [prep.binary.bin, '--front', prep.binary.gw];
      const c = underDeno
        ? {
            cmd: process.execPath,
            args: [
              'run',
              '-q',
              '--no-prompt',
              '--allow-read',
              '--allow-write=dist',
              '--allow-net=127.0.0.1',
              `--allow-run=${prep.binary.bin}`,
              '--allow-env=PATH,SystemRoot,SYSTEMROOT,PATHEXT,TEMP,TMP,windir,ComSpec',
              script,
              ...args,
            ],
          }
        : { cmd: process.execPath, args: [script, ...args] };
      const r = await runChild(c, { env: childEnv(), timeoutMs, echo });
      const smokeOut = lastJson(r.stdout);
      if (!smokeOut || !smokeOut.ok) return done({ ok: false, error: (smokeOut && smokeOut.error) || tail(r.stderr) || `exit ${r.code}` });
      return done({ ok: true, data: { smoke: smokeOut, archive: prep.binary.archive || null } });
    }
    return done({ ok: false, error: `unknown kind ${def.kind}` });
  } catch (e) {
    return done({ ok: false, error: String((e && e.message) || e) });
  }
}

const tail = (s) =>
  String(s || '')
    .trim()
    .split('\n')
    .slice(-6)
    .join(' | ')
    .slice(-600);

// ---- preparation (before the countdown) ----

/** The base tree of a commit, from `git archive` (nothing changes in .git), with its gateway built. */
function prepareBaseTree(commit, tmp) {
  const dir = path.join(tmp, `base-${commit}`);
  fs.mkdirSync(dir, { recursive: true });
  const tarFile = path.join(tmp, `base-${commit}.tar`);
  const a = spawnSync('git', ['-C', ROOT, 'archive', '--format=tar', '-o', tarFile, commit], { encoding: 'utf8' });
  if (a.status !== 0) return { error: `git archive ${commit}: ${tail(a.stderr)}` };
  const x = spawnSync('tar', ['-xf', tarFile, '-C', dir], { encoding: 'utf8' });
  fs.rmSync(tarFile, { force: true });
  if (x.status !== 0) return { error: `tar: ${tail(x.stderr)}` };
  const b = spawnSync('go', ['-C', path.join(dir, 'gw'), 'build', '-o', `bin${path.sep}`, './cmd/zaafpulse-gw'], {
    encoding: 'utf8',
    env: { ...process.env, GOTOOLCHAIN: 'local', CGO_ENABLED: '0' },
    timeout: 300000,
  });
  if (b.status !== 0) return { error: `go build of ${commit}: ${tail(b.stderr)}` };
  return { dir, gwBin: path.join(dir, 'gw', 'bin', `zaafpulse-gw${IS_WIN ? '.exe' : ''}`) };
}

/** This machine's release name (scripts/build-deno.js names). */
function platformName(platform = process.platform, arch = process.arch) {
  const p = platform === 'win32' ? 'win' : platform;
  return `${p}-${arch === 'arm64' ? 'arm64' : 'x64'}`;
}

/** The packaged binary: --binary as given, or `deno task build --package` for this machine, unpacked in tmp. */
function prepareBinary({ binary, gw, build }, tmp) {
  const exe = IS_WIN ? '.exe' : '';
  if (binary) {
    const bin = path.resolve(binary);
    const g = path.resolve(gw || path.join(path.dirname(bin), `zaafpulse-gw${exe}`));
    if (!fs.existsSync(bin)) return { error: `--binary ${bin} does not exist` };
    if (!fs.existsSync(g)) return { error: `the gateway ${g} does not exist (pass --gw)` };
    return { bin, gw: g, archive: null };
  }
  if (!build) return { error: 'skipped: --no-build and no --binary' };
  const r = spawnSync('deno', ['task', 'build', '--package'], { cwd: ROOT, encoding: 'utf8', timeout: 900000, env: childEnv() });
  if (r.status !== 0) return { error: `deno task build --package: ${tail(r.stderr || r.stdout)}` };
  const name = platformName();
  const dist = path.join(ROOT, 'dist');
  const found = fs
    .readdirSync(dist)
    .filter((f) => f.startsWith('zaafpulse-') && (f.endsWith(`-${name}.tar.gz`) || f.endsWith(`-${name}.zip`)))
    .map((f) => ({ f, t: fs.statSync(path.join(dist, f)).mtimeMs }))
    .sort((a, b) => b.t - a.t)[0];
  if (!found) return { error: `no ${name} archive in dist/ after the build` };
  const archive = path.join(dist, found.f);
  const into = fs.mkdtempSync(path.join(tmp, 'package-'));
  const x = spawnSync('tar', [found.f.endsWith('.zip') ? '-xf' : '-xzf', archive, '-C', into], { encoding: 'utf8' });
  if (x.status !== 0) return { error: `unpacking ${found.f}: ${tail(x.stderr)}` };
  const folder = path.join(into, found.f.replace(/\.(tar\.gz|zip)$/, ''));
  return {
    bin: path.join(folder, `zaafpulse${exe}`),
    gw: path.join(folder, `zaafpulse-gw${exe}`),
    archive: { file: found.f, bytes: fs.statSync(archive).size },
  };
}

module.exports = {
  ROOT,
  SCRUB,
  childEnv,
  jsCommand,
  withFileLimit,
  createSampler,
  runChild,
  killTree,
  stopEverything,
  lastJson,
  runScenario,
  prepareBaseTree,
  prepareBinary,
  platformName,
};
