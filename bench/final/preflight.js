// The preflight of the final benchmark runner (scripts/final-bench.js): is this machine quiet enough to measure on?
// Each check gives pass, warn or fail (and the note to leave the machine alone); a fail stops the run unless
// --skip-preflight. Every outside reading goes through `deps`, so the checks are tested with fakes and fixtures
// (test/final-bench-preflight.test.js); realDeps() reads this machine.
// Platform-specific parts: power (macOS pmset, Linux /sys/class/power_supply, Windows Win32_Battery), the listener of a
// port (lsof or ss; Windows netstat), free disk (statfs, else df; Windows PowerShell), ulimit (POSIX only), keep-awake
// (bench/final/keepawake.js).
const fs = require('fs');
const os = require('os');
const path = require('path');
const { spawnSync } = require('child_process');
const procs = require('./procs');
const { checkedCommand, noShell, findExecutable } = require('./spawnsafe');

const ROOT = path.join(__dirname, '..', '..');
const GW_DIR = path.join(ROOT, 'gw');
const LIVE_PORTS = [4777, 4787];
const MIN_FILES = 8192;
const GiB = 2 ** 30;

// The heavy apps of the owner's note; matched on the command line, ignoring case.
const HEAVY = [
  ['Football Manager', /football manager/i],
  ['Google Chrome', /google chrome/i],
  ['Chrome', /(^|[\\/"\s])chrome(\.exe)?("|\s|$)/i],
  ['Antigravity', /antigravity/i],
];
// Other benchmarks and agents; the first that matches names the process.
const OTHERS = [
  ['a node test run', /(^|[\\/\s])node(\.exe)?"?\s+(\S+\s+)*--test\b/],
  ['a deno test run', /(^|[\\/\s])deno(\.exe)?"?\s+test\b/],
  ['a go test run', /(^|[\\/\s])go(\.exe)?"?\s+test\b/],
  ['a Go test binary', /\.test(\.exe)?"?(\s+-test\.|\s*$)/],
  ['a Claude Code session', /(^|[\\/\s])claude(\.exe)?"?(\s|$)/],
  ['a Codex session', /(^|[\\/\s])codex(\.exe)?"?(\s|$)/],
  ['a gateway benchmark', /bench[\\/](run|egress-overhead|ipc-latency|front-overhead)\.js|final-bench\.js/],
  ['a deno process', /(^|[\\/\s])deno(\.exe)?"?(\s|$)/],
  ['a zaafpulse-gw', /zaafpulse-gw/],
];

const escapeRe = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
const r1 = (n) => Math.round(n * 10) / 10;
const short = (s, n = 90) => (s.length > n ? `${s.slice(0, n - 1)}…` : s);

/** @param {number[]} load os.loadavg() @param {string} platform */
function checkLoad(load, platform) {
  const label = 'Load average below 2 (1 and 5 min)';
  if (platform === 'win32' || !load || (load[0] === 0 && load[1] === 0 && load[2] === 0))
    return { id: 'load', label, status: 'warn', detail: 'no load average on this platform; the top CPU check stands in' };
  const [one, five] = load;
  const detail = `1 min ${one.toFixed(2)}, 5 min ${five.toFixed(2)}`;
  return { id: 'load', label, status: one < 2 && five < 2 ? 'pass' : 'fail', detail };
}

/** `pmset -g batt` → ac, battery, ups or null. @param {string} text */
function parsePmset(text) {
  const m = /Now drawing from '([^']+)'/.exec(String(text || ''));
  if (!m) return null;
  if (/^AC/i.test(m[1])) return 'ac';
  if (/^Battery/i.test(m[1])) return 'battery';
  if (/^UPS/i.test(m[1])) return 'ups';
  return null;
}

/**
 * /sys/class/power_supply entries ({ type, online, status }) → ac, battery, desktop (no battery) or null.
 * @param {{ type?: string, online?: string, status?: string }[] | null} entries
 */
function linuxPower(entries) {
  if (!Array.isArray(entries)) return null;
  const mains = entries.filter((e) => /^(Mains|USB)/i.test(e.type || ''));
  const batteries = entries.filter((e) => /^Battery/i.test(e.type || ''));
  if (mains.some((e) => String(e.online).trim() === '1')) return 'ac';
  if (batteries.some((e) => /discharging/i.test(e.status || ''))) return 'battery';
  if (!batteries.length) return 'desktop';
  if (batteries.some((e) => /^(charging|full|not charging)/i.test(e.status || ''))) return 'ac';
  return null;
}

/**
 * Win32_Battery's BatteryStatus (one per line) → ac, battery, desktop or null. 1 is "discharging"; 2 and 3–9 are on
 * AC or charging (6–9); 4 and 5 low or critical on battery.
 * @param {string} text
 */
function parseWinBattery(text) {
  const codes = String(text || '')
    .split(/\r?\n/)
    .map((s) => s.trim())
    .filter(Boolean)
    .map(Number);
  if (!codes.length) return 'desktop';
  if (codes.some((c) => !Number.isInteger(c))) return null;
  if (codes.some((c) => c === 1 || c === 4 || c === 5)) return 'battery';
  return 'ac';
}

/** @param {string|null} state ac, battery, ups, desktop or null @param {string} how */
function checkPower(state, how) {
  const label = 'On AC power';
  if (state === 'ac') return { id: 'power', label, status: 'pass', detail: `AC power (${how})` };
  if (state === 'desktop') return { id: 'power', label, status: 'pass', detail: `no battery found (${how})` };
  if (state === 'battery' || state === 'ups')
    return { id: 'power', label, status: 'fail', detail: `on ${state === 'ups' ? 'UPS' : 'battery'} power (${how}): plug in the charger` };
  return { id: 'power', label, status: 'warn', detail: `could not tell (${how}); you will be asked`, ask: true };
}

/**
 * The heavy apps running (one line per app name).
 * @param {{ pid: number, args: string }[]} list
 * @param {string[]} extra more names from --heavy
 */
function checkHeavy(list, extra = []) {
  const patterns = [...HEAVY, ...extra.map((n) => [n, new RegExp(escapeRe(n), 'i')])];
  const found = new Map();
  for (const p of list)
    for (const [name, re] of patterns)
      if (re.test(p.args)) {
        if (!found.has(name)) found.set(name, []);
        found.get(name).push(p.pid);
        break;
      }
  const label = 'No heavy apps (Football Manager, Chrome, Antigravity, --heavy)';
  if (!found.size) return { id: 'heavy', label, status: 'pass', detail: 'none running' };
  const detail = [...found].map(([n, pids]) => `${n} (${pids.length} process${pids.length > 1 ? 'es' : ''})`).join(', ');
  return { id: 'heavy', label, status: 'fail', detail: `running: ${detail}: quit them` };
}

/**
 * The busiest processes that are not this runner's (from procs.cpuBetween), warn above 30% of a core.
 * @param {{ pid: number, name: string, cpu: number }[]} cpu
 * @param {Set<number>} own
 */
function checkTopCpu(cpu, own) {
  const top = cpu.filter((p) => !own.has(p.pid) && p.pid !== 0).slice(0, 5);
  const busy = top.filter((p) => p.cpu > 30);
  const detail = top.length ? top.map((p) => `${p.name} ${r1(p.cpu)}%`).join(', ') : 'no reading';
  return {
    id: 'top-cpu',
    label: 'No busy process (top CPU over 3 s, % of one core)',
    status: busy.length ? 'warn' : 'pass',
    detail,
  };
}

/**
 * Other benchmarks, test runs and agents; this runner's tree and the live dashboard's are left out.
 * @param {{ pid: number, args: string }[]} list
 * @param {Set<number>} own
 * @param {Set<number>} live
 */
function checkOthers(list, own, live) {
  const found = [];
  for (const p of list) {
    if (own.has(p.pid) || live.has(p.pid)) continue;
    const hit = OTHERS.find(([, re]) => re.test(p.args));
    if (hit) found.push(`${hit[0]} (pid ${p.pid}: ${short(p.args, 70)})`);
  }
  const label = 'No other benchmark, test run or agent (node --test, go test, claude, deno, zaafpulse-gw)';
  if (!found.length) return { id: 'others', label, status: 'pass', detail: 'none running' };
  return { id: 'others', label, status: 'fail', detail: `running: ${found.slice(0, 8).join('; ')}${found.length > 8 ? `; and ${found.length - 8} more` : ''}` };
}

/**
 * The live dashboard and the preview: reported, never touched. warn when either used above 10% of a core.
 * @param {{ port: number, pids: number[], cpu: number }[]} found
 */
function checkLive(found) {
  const label = 'The live dashboard (4777) and the preview (4787) idle';
  const up = found.filter((f) => f.pids.length);
  if (!up.length) return { id: 'live', label, status: 'pass', detail: 'neither is running' };
  const detail = up.map((f) => `${f.port}: pid ${f.pids.join(',')}, ${r1(f.cpu)}% CPU with its gateway`).join('; ');
  return { id: 'live', label, status: up.some((f) => f.cpu > 10) ? 'warn' : 'pass', detail: `${detail} (left running)` };
}

/** @param {{ dir: string, bytes: number|null }[]} free */
function checkDisk(free) {
  const label = 'Free disk: at least 2 GB for results and temp folders';
  const known = free.filter((f) => typeof f.bytes === 'number');
  if (!known.length) return { id: 'disk', label, status: 'warn', detail: 'could not read the free space' };
  const least = known.reduce((a, b) => (b.bytes < a.bytes ? b : a));
  const gb = least.bytes / GiB;
  const detail = `${r1(gb)} GB free at ${least.dir}`;
  return { id: 'disk', label, status: gb < 2 ? 'fail' : gb < 5 ? 'warn' : 'pass', detail };
}

/** @param {boolean|null} reach whether this process opened MIN_FILES files @param {string} platform */
function checkFiles(reach, platform) {
  const label = `Open files: the limit can reach ${MIN_FILES} (500 streams)`;
  if (platform === 'win32') return { id: 'files', label, status: 'pass', detail: 'not limited this way on Windows' };
  if (reach === null) return { id: 'files', label, status: 'warn', detail: 'could not test the limit' };
  if (reach) return { id: 'files', label, status: 'pass', detail: `${MIN_FILES} files opened in this process (Node, Deno and Go raise their soft limit to the hard one)` };
  return { id: 'files', label, status: 'fail', detail: `this process could not open ${MIN_FILES} files: raise the hard limit (ulimit -Hn) first` };
}

/** @param {{ go: string|null, deno: string|null, node: string|null }} v */
function checkTools(v) {
  const label = 'Go, Deno and Node';
  const detail = `go ${v.go || 'missing'} · deno ${v.deno || 'missing'} · node ${v.node || 'missing'}`;
  if (!v.go) return { id: 'tools', label, status: 'fail', detail: `${detail}: Go builds the gateway` };
  return { id: 'tools', label, status: v.deno && v.node ? 'pass' : 'warn', detail: v.deno && v.node ? detail : `${detail}: the scenarios on the missing runtime will fail` };
}

/** @param {{ state: string, built?: { ok: boolean, detail: string } }} g */
function checkGateway(g) {
  const label = 'The Go gateway built from this tree (gw/bin/zaafpulse-gw)';
  if (g.state === 'fresh') return { id: 'gateway', label, status: 'pass', detail: 'up to date' };
  if (g.built && g.built.ok) return { id: 'gateway', label, status: 'pass', detail: `${g.state}: built now` };
  return { id: 'gateway', label, status: 'fail', detail: `${g.state}; the build failed: ${g.built ? g.built.detail : 'not tried'}` };
}

/** @param {string|null} tool */
function checkKeepAwake(tool) {
  const label = 'Keep the machine awake during the run';
  if (tool) return { id: 'awake', label, status: 'pass', detail: `${tool} (started with the run, stopped at its end)` };
  return { id: 'awake', label, status: 'warn', detail: 'no keep-awake tool found: turn sleep off by hand' };
}

const NOTE = {
  id: 'untouched',
  label: 'Leave the machine untouched',
  status: 'note',
  detail: 'no typing, no apps, lid open, on power, until the report is written',
};

/**
 * Every check, in order. deps: { platform, loadavg, power() -> { state, how }, snapshot(), sleep(ms), selfPid,
 * listeners(port) -> pids, freeBytes(dir), dirs, heavy, filesReach(), versions(), gateway: { state(), build() },
 * keepAwakeTool() }.
 */
async function runPreflight(deps) {
  const checks = [];
  checks.push(checkLoad(deps.loadavg(), deps.platform));
  const pw = deps.power();
  checks.push(checkPower(pw.state, pw.how));
  const before = deps.snapshot();
  await deps.sleep(3000);
  const after = deps.snapshot();
  // This runner's tree and the chain that started it (the shell, `deno task`, a terminal): none of them is "another".
  const own = procs.treeOf(after, [deps.selfPid]);
  const parent = new Map(after.map((p) => [p.pid, p.ppid]));
  for (let pid = parent.get(deps.selfPid); pid && !own.has(pid); pid = parent.get(pid)) own.add(pid);
  const live = [];
  const liveTree = new Set();
  const cpu = procs.cpuBetween(before, after, 3);
  for (const port of LIVE_PORTS) {
    const pids = deps.listeners(port);
    const tree = procs.treeOf(after, pids);
    for (const p of tree) liveTree.add(p);
    live.push({ port, pids, cpu: cpu.filter((c) => tree.has(c.pid)).reduce((a, c) => a + c.cpu, 0) });
  }
  checks.push(checkHeavy(after, deps.heavy || []));
  checks.push(checkTopCpu(cpu, own));
  checks.push(checkOthers(after, own, liveTree));
  checks.push(checkLive(live));
  checks.push(checkDisk((deps.dirs || []).map((dir) => ({ dir, bytes: deps.freeBytes(dir) }))));
  checks.push(checkFiles(deps.filesReach(), deps.platform));
  const versions = deps.versions();
  checks.push(checkTools(versions));
  const state = deps.gateway.state();
  checks.push(checkGateway(state === 'fresh' || !versions.go ? { state } : { state, built: deps.gateway.build() }));
  checks.push(checkKeepAwake(deps.keepAwakeTool()));
  checks.push(NOTE);
  return { checks, ok: !checks.some((c) => c.status === 'fail'), versions, liveTree };
}

// ---- this machine ----

// Every reading of this machine: an allow-listed program (bench/final/spawnsafe.js), no shell, arguments as elements.
const run = (cmd, args, opts = {}) => {
  try {
    return spawnSync(checkedCommand(cmd), [...args], noShell({ encoding: 'utf8', timeout: 20000, ...opts }));
  } catch (e) {
    return { status: null, stdout: '', error: e };
  }
};
const firstLine = (r) => (r && r.status === 0 ? String(r.stdout || '').split('\n')[0].trim() || null : null);

function powerOf(platform) {
  if (platform === 'darwin') return { state: parsePmset(run('pmset', ['-g', 'batt']).stdout), how: 'pmset -g batt' };
  if (platform === 'linux') {
    const dir = '/sys/class/power_supply';
    let entries = null;
    try {
      entries = fs.readdirSync(dir).map((name) => {
        const read = (f) => {
          try {
            return fs.readFileSync(path.join(dir, name, f), 'utf8').trim();
          } catch {
            return '';
          }
        };
        return { name, type: read('type'), online: read('online'), status: read('status') };
      });
    } catch {
      /* no such folder: a container, or not Linux sysfs */
    }
    return { state: linuxPower(entries), how: dir };
  }
  if (platform === 'win32') {
    const r = run('powershell', ['-NoProfile', '-NonInteractive', '-Command', '(Get-CimInstance Win32_Battery).BatteryStatus']);
    return { state: r.status === 0 ? parseWinBattery(r.stdout) : null, how: 'Win32_Battery' };
  }
  return { state: null, how: platform };
}

function listeners(platform, port) {
  if (platform === 'win32') {
    const r = run('netstat', ['-ano', '-p', 'TCP']);
    if (r.status !== 0) return [];
    const pids = new Set();
    for (const line of String(r.stdout).split(/\r?\n/)) {
      const m = /^\s*TCP\s+\S+:(\d+)\s+\S+\s+LISTENING\s+(\d+)/i.exec(line);
      if (m && Number(m[1]) === port) pids.add(Number(m[2]));
    }
    return [...pids];
  }
  const l = run('lsof', ['-nP', `-iTCP:${port}`, '-sTCP:LISTEN', '-t']);
  if (l.status === 0 || l.status === 1)
    return String(l.stdout || '')
      .split('\n')
      .map(Number)
      .filter((n) => n > 0);
  const s = run('ss', ['-ltnpH', `sport = :${port}`]);
  return [...String(s.stdout || '').matchAll(/pid=(\d+)/g)].map((m) => Number(m[1]));
}

function freeBytes(platform, dir) {
  try {
    if (typeof fs.statfsSync === 'function') {
      const s = fs.statfsSync(dir);
      return Number(s.bavail) * Number(s.bsize);
    }
  } catch {
    /* fall through */
  }
  if (platform === 'win32') {
    const drive = path.parse(path.resolve(dir)).root.replace(/[:\\/]+$/, '');
    const r = run('powershell', ['-NoProfile', '-NonInteractive', '-Command', `(Get-PSDrive ${drive}).Free`]);
    const n = Number(firstLine(r));
    return Number.isFinite(n) ? n : null;
  }
  const r = run('df', ['-kP', dir]);
  const line = String(r.stdout || '').trim().split('\n')[1];
  const n = line ? Number(line.trim().split(/\s+/)[3]) * 1024 : NaN;
  return Number.isFinite(n) ? n : null;
}

/**
 * Whether this process can hold n open files: opens this file n times and closes them all. This runtime raised its soft
 * limit to the hard one at start, as the bench children will, so this proves the limit they get, without a shell.
 * @param {number} n
 */
function openFilesReach(n) {
  const fds = [];
  try {
    for (let i = 0; i < n; i++) fds.push(fs.openSync(__filename, 'r'));
    return true;
  } catch (e) {
    return e && (e.code === 'EMFILE' || e.code === 'ENFILE') ? false : null;
  } finally {
    for (const fd of fds) fs.closeSync(fd);
  }
}

const GW_BIN = path.join(GW_DIR, 'bin', `zaafpulse-gw${process.platform === 'win32' ? '.exe' : ''}`);

// fresh when the binary is newer than every Go source, go.mod and go.sum under gw/ (testdata and bin left out).
function gatewayState() {
  let built;
  try {
    built = fs.statSync(GW_BIN).mtimeMs;
  } catch {
    return 'missing';
  }
  const walk = (dir) => {
    for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
      if (e.isDirectory()) {
        if (e.name === 'bin' || e.name === 'testdata' || e.name.startsWith('.')) continue;
        if (walk(path.join(dir, e.name))) return true;
      } else if (/\.go$|^go\.(mod|sum)$/.test(e.name) && fs.statSync(path.join(dir, e.name)).mtimeMs > built) return true;
    }
    return false;
  };
  return walk(GW_DIR) ? 'stale' : 'fresh';
}

// The same build as `npm run gw:build`, with the toolchain pinned as scripts/ipc/build-stub.js pins it.
function buildGateway() {
  const r = run('go', ['-C', GW_DIR, 'build', '-o', `bin${path.sep}`, './cmd/zaafpulse-gw'], {
    env: { ...process.env, GOTOOLCHAIN: 'local', CGO_ENABLED: '0' },
    timeout: 300000,
  });
  return r.status === 0 ? { ok: true, detail: 'built' } : { ok: false, detail: String(r.stderr || r.error || '').slice(0, 400) };
}

function versions() {
  const v = (cmd, args, re) => {
    const line = firstLine(run(cmd, args));
    const m = line && re.exec(line);
    return m ? m[1] : null;
  };
  return {
    go: v('go', ['version'], /go version go(\S+)/),
    deno: v('deno', ['--version'], /deno (\S+)/),
    node: v('node', ['--version'], /v(\S+)/),
  };
}

function keepAwakeTool(platform) {
  if (platform === 'win32') return 'SetThreadExecutionState (PowerShell)';
  const has = (cmd) => findExecutable(cmd) !== null;
  if (platform === 'darwin' && has('caffeinate')) return 'caffeinate -dims';
  if (platform === 'linux' && has('systemd-inhibit') && has('tail')) return 'systemd-inhibit';
  return null;
}

/** The deps of runPreflight for this machine. @param {{ selfPid: number, dirs: string[], heavy: string[] }} o */
function realDeps({ selfPid, dirs, heavy }) {
  const platform = process.platform;
  return {
    platform,
    loadavg: () => os.loadavg(),
    power: () => powerOf(platform),
    snapshot: () => procs.listProcesses({ platform }),
    sleep: (ms) => new Promise((r) => setTimeout(r, ms)),
    selfPid,
    listeners: (port) => listeners(platform, port),
    freeBytes: (dir) => freeBytes(platform, dir),
    dirs,
    heavy,
    filesReach: () => (platform === 'win32' ? null : openFilesReach(MIN_FILES)),
    versions,
    gateway: { state: gatewayState, build: buildGateway },
    keepAwakeTool: () => keepAwakeTool(platform),
  };
}

module.exports = {
  HEAVY,
  OTHERS,
  LIVE_PORTS,
  MIN_FILES,
  GW_BIN,
  checkLoad,
  parsePmset,
  linuxPower,
  parseWinBattery,
  checkPower,
  checkHeavy,
  checkTopCpu,
  checkOthers,
  checkLive,
  checkDisk,
  checkFiles,
  openFilesReach,
  run,
  checkTools,
  checkGateway,
  checkKeepAwake,
  runPreflight,
  realDeps,
  versions,
  freeBytes,
  listeners,
};
