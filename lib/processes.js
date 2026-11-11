// Snapshot of AI apps running right now, grouped by app (helpers count toward their parent).
// macOS uses ps and vm_stat, Linux reads /proc (works on minimal images too), Windows asks CIM.
const { execFile } = require('child_process');
const fs = require('fs');
const os = require('os');
const path = require('path');
const { DIR_PREFIX, runningPids } = require('./evals/claude-cli');

// First match wins, so specific patterns come before broad ones.
// Patterns cover macOS app bundles, Windows .exe paths and Linux binaries/AppImages.
const APPS = [
  {
    id: 'claude-desktop',
    name: 'Claude Desktop',
    re: /[\\/]Claude\.app[\\/]|AnthropicClaude[\\/]/i,
    paths: ['/Applications/Claude.app', '%LOCALAPPDATA%/AnthropicClaude'],
  },
  {
    id: 'claude',
    name: 'Claude Code',
    re: /native-binary[\\/]claude\b|claude-code|(^|[\\/"])claude(\.exe)?("|\s|$)/,
    paths: [], // the log folders, from lib/claude (installed())
  },
  { id: 'codex', name: 'Codex', re: /codex/i, paths: [] }, // the log folders, from lib/codex (installed())
  {
    id: 'chatgpt',
    name: 'ChatGPT',
    re: /[\\/]ChatGPT(\.app[\\/]|\.exe)/i,
    paths: ['/Applications/ChatGPT.app', '%LOCALAPPDATA%/Programs/ChatGPT'],
  },
  {
    id: 'antigravity',
    name: 'Antigravity / Gemini',
    re: /Antigravity|gemini-cli|[\\/]gemini(\.exe)?(\s|$)/i,
    paths: ['/Applications/Antigravity.app', '/Applications/Antigravity IDE.app', '~/.gemini'],
  },
  {
    id: 'cursor',
    name: 'Cursor',
    re: /[\\/]Cursor(\.app[\\/]|\.exe)|cursor[^\\/]*\.appimage/i,
    paths: ['/Applications/Cursor.app', '~/.cursor', '%LOCALAPPDATA%/Programs/cursor'],
  },
  {
    id: 'kiro',
    name: 'Kiro',
    // Kiro.app (the IDE), Kiro CLI.app (its main program is kiro_cli_desktop) and the kiro-cli sessions.
    re: /[\\/]Kiro(\.app[\\/]|\.exe)|[\\/]Kiro CLI\.app[\\/]|kiro-cli|(^|[\\/])kiro_cli\w*(\s|$)/i,
    paths: ['/Applications/Kiro.app', '/Applications/Kiro CLI.app', '~/.kiro', '%LOCALAPPDATA%/Programs/Kiro'],
  },
  {
    id: 'ollama',
    name: 'Ollama',
    re: /ollama/i,
    paths: [
      '/Applications/Ollama.app',
      '~/.ollama',
      '/usr/local/bin/ollama',
      '/usr/bin/ollama',
      '%LOCALAPPDATA%/Programs/Ollama',
    ],
  },
  {
    id: 'lmstudio',
    name: 'LM Studio',
    re: /LM Studio|lm-studio/i,
    paths: ['/Applications/LM Studio.app', '~/.lmstudio', '~/.cache/lm-studio'],
  },
];

function installed() {
  const local = process.env.LOCALAPPDATA || '';
  const expand = (p) =>
    p.startsWith('~')
      ? path.join(os.homedir(), p.slice(1))
      : p.startsWith('%LOCALAPPDATA%')
        ? local
          ? path.join(local, p.slice(15))
          : ''
        : p;
  // Claude Code and Codex are installed when any of the folders their logs are read from exists.
  const logs = { claude: require('./claude').roots(), codex: require('./codex').roots() };
  // A %LOCALAPPDATA% path where that variable is unset expands to '' and is skipped: under Deno, existsSync('') throws
  // once read permission is granted by flag instead of -A (Node returns false).
  return APPS.filter((a) =>
    (logs[a.id] || a.paths).some((p) => {
      const x = expand(p);
      return x !== '' && fs.existsSync(x);
    }),
  ).map((a) => a.id);
}

function parseEtime(s) {
  // [[dd-]hh:]mm:ss
  const [days, rest] = s.includes('-') ? s.split('-') : [0, s];
  const parts = rest.split(':').map(Number);
  while (parts.length < 3) parts.unshift(0);
  return ((Number(days) * 24 + parts[0]) * 60 + parts[1]) * 60 + parts[2];
}

// Whole-machine context for the live page: how much of the laptop the AI apps are using.
// usedMB, when given, replaces os.freemem(), which on macOS counts only never-touched pages.
function system(usedMB) {
  const total = Math.round(os.totalmem() / 1048576);
  return {
    cores: os.cpus().length,
    load: os.loadavg().map((v) => +v.toFixed(2)),
    memTotalMB: total,
    memFreeMB: usedMB == null ? Math.round(os.freemem() / 1048576) : Math.max(0, total - usedMB),
    uptimeSec: Math.round(os.uptime()),
  };
}

// "Memory Used" the way Activity Monitor counts it: app memory + wired + compressed.
function parseVmStat(out) {
  const page = Number((out.match(/page size of (\d+) bytes/) || [])[1]) || 4096;
  const get = (label) => Number((out.match(new RegExp(`${label}:\\s+(\\d+)`)) || [])[1]) || 0;
  const app = get('Anonymous pages') ? get('Anonymous pages') - get('Pages purgeable') : get('Pages active');
  const pages = app + get('Pages wired down') + get('Pages occupied by compressor');
  return pages ? Math.round((pages * page) / 1048576) : null;
}

// Linux: MemTotal - MemAvailable, the "used" figure free(1) and most monitors show.
function parseMeminfo(text) {
  const kb = (k) => Number((text.match(new RegExp(`^${k}:\\s+(\\d+)`, 'm')) || [])[1]);
  const total = kb('MemTotal');
  const avail = kb('MemAvailable');
  return total && Number.isFinite(avail) ? Math.round((total - avail) / 1024) : null;
}

// Deno refuses every read under /proc on Linux unless the process holds all permissions: a scoped grant does not
// help, not even --allow-read=/proc, nor --allow-sys (measured, Deno 2.9.8). Deno throws NotCapable for that (or
// PermissionDenied after a prompt); Node throws neither, so under Node nothing below changes.
const refused = (err) => !!err && (err.name === 'NotCapable' || err.name === 'PermissionDenied');
// Each fallback is said once per process, on stderr, with Deno's own reason. `seen` and `warn` are for the tests.
const WARNED = new Set();
function warnOnce(key, msg, { warn = (m) => console.warn(m), seen = WARNED } = {}) {
  if (seen.has(key)) return;
  seen.add(key);
  warn(msg);
}

// Linux used memory in MB. If /proc/meminfo is refused, os.totalmem() - os.freemem() is the same figure: on Linux
// both runtimes give MemAvailable as the free memory (Node through libuv, Deno through its sys API).
function linuxUsedMemory({
  read = () => fs.readFileSync('/proc/meminfo', 'utf8'),
  total = os.totalmem,
  free = os.freemem,
  warn,
  seen,
} = {}) {
  try {
    return parseMeminfo(read());
  } catch (err) {
    if (!refused(err)) return null; // no /proc: the live page shows os.freemem() instead
    warnOnce(
      'meminfo',
      `processes: /proc/meminfo is not readable by this runtime (${err.message}); used memory comes from the OS memory totals instead`,
      { warn, seen },
    );
    try {
      return Math.round((total() - free()) / 1048576);
    } catch {
      return null;
    }
  }
}

function usedMemory() {
  if (process.platform === 'linux') return Promise.resolve(linuxUsedMemory());
  if (process.platform !== 'darwin') return Promise.resolve(null);
  return new Promise((resolve) => execFile('vm_stat', (err, out) => resolve(err ? null : parseVmStat(out))));
}

// Every process as { pid, ppid, cpu, rssKB, upSec, args }. cpu is the lifetime average share of one core,
// the way ps reports %CPU, so the numbers mean the same thing on every OS. On POSIX a row also says whether the
// process is stopped (SIGSTOP or job control), which agent control reads back after a restart, and its parent's pid,
// which tells a local model server's own helpers from a server of their own (lib/runtimes/state.js).
function parsePsRows(stdout) {
  return String(stdout)
    .split('\n')
    .map((raw) => raw.trim().match(/^(\d+)\s+(\d+)\s+([\d.]+)\s+(\d+)\s+(\S+)\s+(\S+)\s+(.*)$/))
    .filter(Boolean)
    .map((m) => ({
      pid: Number(m[1]),
      ppid: Number(m[2]),
      cpu: Number(m[3]),
      rssKB: Number(m[4]),
      upSec: parseEtime(m[5]),
      stopped: m[6].includes('T'),
      args: m[7],
    }));
}
function psRows() {
  return new Promise((resolve, reject) =>
    execFile(
      'ps',
      ['-axo', 'pid=,ppid=,pcpu=,rss=,etime=,stat=,args='],
      { maxBuffer: 32 * 1024 * 1024 },
      (err, stdout) => (err ? reject(err) : resolve(parsePsRows(stdout))),
    ),
  );
}
function procRows(root = '/proc', hz = 100) {
  const uptime = Number(fs.readFileSync(path.join(root, 'uptime'), 'utf8').split(/\s+/)[0]);
  const rows = [];
  for (const d of fs.readdirSync(root)) {
    if (!/^\d+$/.test(d)) continue;
    try {
      // The arguments one by one, so a path with spaces stays whole (agent control reads the program from argv[0]).
      const argv = fs
        .readFileSync(path.join(root, d, 'cmdline'), 'utf8')
        .split('\0')
        .filter(Boolean);
      const args = argv.join(' ');
      if (!args) continue; // kernel threads
      const stat = fs.readFileSync(path.join(root, d, 'stat'), 'utf8');
      const f = stat.slice(stat.lastIndexOf(')') + 2).split(' ');
      const cpuSec = (Number(f[11]) + Number(f[12])) / hz;
      const upSec = Math.max(1, uptime - Number(f[19]) / hz);
      const rssKB =
        Number((fs.readFileSync(path.join(root, d, 'status'), 'utf8').match(/^VmRSS:\s+(\d+)/m) || [])[1]) || 0;
      const row = {
        pid: Number(d),
        ppid: Number(f[1]), // the field after the state letter
        cpu: (cpuSec / upSec) * 100,
        rssKB,
        upSec: Math.round(upSec),
        stopped: f[0] === 'T', // the state letter after the name: T stopped by a signal; t (a debugger's stop) is not
        args,
        argv,
      };
      try {
        row.cwd = fs.readlinkSync(path.join(root, d, 'cwd')); // only for this user's own processes
      } catch {
        /* another user's process, or gone */
      }
      rows.push(row);
    } catch {
      /* the process ended while we read it */
    }
  }
  return rows;
}
// Windows: one WMI query; CPU from kernel + user time (100 ns units) over the process's age. It loads no cmdlet module:
// module auto-loading is off and the query goes through [wmisearcher], not Get-CimInstance. On a fresh Windows on Arm
// machine loading the CimCmdlets module (or any command discovery) took 22-35 s per PowerShell start, past the 15 s
// limit below, so every read was killed; this query takes about 0.5 s there (under 5 s cold) and gives the same rows.
const WIN_PS =
  "$PSModuleAutoLoadingPreference='None'; [Console]::OutputEncoding=[Text.Encoding]::UTF8; $n=[DateTimeOffset]::UtcNow.ToUnixTimeSeconds(); foreach ($p in ([wmisearcher]'SELECT ProcessId,KernelModeTime,UserModeTime,WorkingSetSize,CreationDate,CommandLine,ExecutablePath FROM Win32_Process').Get()) { if ($p.CommandLine -and $p.CreationDate) { '{0}`t{1}`t{2}`t{3}`t{4}`t{5}' -f $p.ProcessId,($p.KernelModeTime+$p.UserModeTime),$p.WorkingSetSize,($n-([DateTimeOffset][Management.ManagementDateTimeConverter]::ToDateTime($p.CreationDate)).ToUnixTimeSeconds()),$p.CommandLine,$p.ExecutablePath } }";
function parseWinRows(out) {
  return out
    .split(/\r?\n/)
    .map((l) => l.split('\t'))
    .filter((f) => f.length >= 5 && /^\d+$/.test(f[0]))
    .map((f) => {
      const upSec = Math.max(1, Number(f[3]));
      // The query ends with ExecutablePath (a path holds no tab); a row of 5 fields has none. CommandLine quotes a
      // spaced program, so the path goes in argv[0] (the program only; the rest stays in args).
      const exe = f.length >= 6 ? f[f.length - 1].trim() : '';
      const args = (f.length >= 6 ? f.slice(4, -1) : f.slice(4)).join('\t');
      return {
        pid: Number(f[0]),
        cpu: (Number(f[1]) / 1e7 / upSec) * 100,
        rssKB: Number(f[2]) / 1024,
        upSec,
        args,
        ...(exe ? { argv: [exe] } : {}),
      };
    });
}
function winRows() {
  return new Promise((resolve, reject) =>
    execFile(
      'powershell.exe',
      ['-NoProfile', '-NonInteractive', '-Command', WIN_PS],
      { maxBuffer: 32 * 1024 * 1024, windowsHide: true, timeout: 15000 },
      (err, stdout) => (err ? reject(err) : resolve(parseWinRows(stdout))),
    ),
  );
}
// What this process learnt about Linux without /proc: `procRefused` once the runtime refused it, `ps` and `lsof` true
// or false once known (the process list and the working folders come from them then). The tests pass their own.
const LINUX = { procRefused: false, ps: undefined, lsof: undefined };

// Linux: /proc, which works on minimal images without ps. If the runtime refuses /proc (Deno without all permissions,
// see `refused`), ps instead, said once. Those rows have no argv split argument by argument and no working folder;
// rows() adds both for the AI app processes the way it does on macOS (ps comm, lsof).
function linuxRows({ read = procRows, ps = psRows, warn, seen, state = LINUX } = {}) {
  try {
    return Promise.resolve(read());
  } catch (err) {
    if (!refused(err)) return Promise.reject(err);
    state.procRefused = true;
    warnOnce(
      'proc',
      `processes: /proc is not readable by this runtime (${err.message}); listing processes with ps instead, and the working folders of AI agents with lsof`,
      { warn, seen },
    );
    return ps().catch((e) => {
      // No ps either (a minimal image): nothing can be listed; said once, the error still reaches the caller.
      if (e && e.code === 'ENOENT') {
        state.ps = false;
        warnOnce('ps', `processes: ${NO_PS}`, { warn, seen });
      }
      throw e;
    });
  }
}
const rowsForOs = (platform = process.platform, linux = {}) =>
  platform === 'linux' ? linuxRows(linux) : platform === 'win32' ? winRows() : psRows();

// Whether `name` is an executable file in a PATH folder (no program is run for it).
function onPath(name, env = process.env) {
  return String(env.PATH || '')
    .split(path.delimiter)
    .filter((d) => path.isAbsolute(d))
    .some((d) => {
      try {
        fs.accessSync(path.join(d, name), fs.constants.X_OK);
        return fs.statSync(path.join(d, name)).isFile();
      } catch {
        return false;
      }
    });
}
const NO_LSOF =
  'this runtime cannot read /proc on Linux and lsof is not installed, so the working folder of an agent is unknown: agent control (pause, resume, stop) and linking a session to its process are unavailable; install lsof to restore them';
const NO_PS =
  'this runtime cannot read /proc on Linux and ps is not installed, so no process can be listed: the live page shows no AI app, and agent control (pause, resume, stop) and linking a session to its process are unavailable; install procps (ps, kill) and lsof to restore them';
// Linux under a runtime that refuses /proc (Deno: no scoped grant reaches /proc, only all permissions do). null when
// /proc is readable (Node, or not Linux); else { proc: 'refused', ps, lsof, text }. With ps or lsof missing agent
// control and session linking are unavailable (lib/agents/control.js limited(), the Compliance page).
function linuxLimits({
  platform = process.platform,
  state = LINUX,
  probe = () => fs.readFileSync('/proc/uptime'),
  hasLsof = () => onPath('lsof'),
  hasPs = () => onPath('ps'),
} = {}) {
  if (platform !== 'linux') return null;
  if (!state.procRefused) {
    try {
      probe();
    } catch (err) {
      if (refused(err)) state.procRefused = true;
    }
  }
  if (!state.procRefused) return null;
  if (state.lsof === undefined) state.lsof = !!hasLsof();
  if (state.ps === undefined) state.ps = !!hasPs();
  return {
    proc: 'refused',
    ps: state.ps,
    lsof: state.lsof,
    text: !state.ps
      ? NO_PS
      : state.lsof
        ? 'this runtime cannot read /proc on Linux (Deno has no scoped grant for it): processes come from ps, the working folders of agents from lsof'
        : NO_LSOF,
  };
}

// The judge's Claude Code calls (lib/evals/claude-cli) run in a zaafpulse-judge-* temp folder: they are ZAAFPulse
// at work, not an app the owner runs. Known by that folder in argv or as the working folder, or by pid (`skip`, the
// calls this process started, for systems where the working folder is not cheap to read).
const isJudge = (r, skip) =>
  skip.has(r.pid) || [r.args, r.cwd].some((x) => typeof x === 'string' && x.includes(DIR_PREFIX));

// ZAAFPulse itself (this process, or anything run from its folder) and the judge's calls are never an AI app the owner
// runs. One rule for the live page and for agent control, so neither can classify a process differently.
const isOwn = (r, skip = new Set()) =>
  r.pid === process.pid || /ai-command-center|zaafpulse/i.test(r.args) || isJudge(r, skip);

function groupApps(rows, { skip = new Set() } = {}) {
  const apps = {};
  for (const r of rows) {
    if (isOwn(r, skip)) continue;
    const app = APPS.find((a) => a.re.test(r.args));
    if (!app) continue;
    const a = (apps[app.id] ||= { id: app.id, name: app.name, processes: 0, cpu: 0, memMB: 0, uptimeSec: 0 });
    a.processes++;
    a.cpu += r.cpu;
    a.memMB += r.rssKB / 1024;
    a.uptimeSec = Math.max(a.uptimeSec, r.upSec);
  }
  const list = Object.values(apps).map((a) => ({ ...a, cpu: +a.cpu.toFixed(1), memMB: Math.round(a.memMB) }));
  return list.sort((x, y) => y.cpu - x.cpu || y.memMB - x.memMB);
}

// macOS: lsof -Fn prints "p<pid>", then "f<fd>" and "n<path>" lines; with -d cwd, the path is the working folder.
function parseLsofCwd(out) {
  const cwds = new Map();
  let pid = null;
  for (const line of String(out).split('\n')) {
    if (line[0] === 'p') pid = Number(line.slice(1));
    else if (line[0] === 'n' && pid) cwds.set(pid, line.slice(1));
  }
  return cwds;
}
// The working folders of the rows `wanted` picks (by default the ones that look like Claude Code), on macOS (ps
// cannot show them) and on Linux when the rows came from ps (`viaPs`, /proc refused). Others are left as they are; a
// failure leaves every row without one. On Linux a missing lsof (or one the runtime may not run) is recorded in
// `state` and said once: without working folders agent control and session linking are unavailable.
const CLAUDE = APPS.find((a) => a.id === 'claude');
const isAgent = (r) => APPS.some((a) => a.re.test(r.args));
function withCwds(
  rows,
  wanted = (r) => CLAUDE.re.test(r.args),
  platform = process.platform,
  { viaPs = false, state = LINUX, warn, seen } = {},
) {
  const pids = rows.filter((r) => !r.cwd && wanted(r)).map((r) => r.pid);
  const linux = platform === 'linux' && viaPs;
  if (!(platform === 'darwin' || linux) || !pids.length || (linux && state.lsof === false))
    return Promise.resolve(rows);
  return new Promise((resolve) =>
    execFile(
      'lsof',
      ['-a', '-d', 'cwd', '-Fn', '-p', pids.join(',')],
      { timeout: 3000, maxBuffer: 1024 * 1024 },
      (err, stdout) => {
        if (linux) {
          const missing = !!err && (err.code === 'ENOENT' || /Requires run access/.test(String(err.message)));
          state.lsof = !missing;
          if (missing) warnOnce('lsof', `processes: ${NO_LSOF}`, { warn, seen });
        }
        const cwds = parseLsofCwd(stdout || ''); // lsof exits 1 when a pid has gone; what it printed still counts
        resolve(rows.map((r) => (cwds.has(r.pid) ? { ...r, cwd: cwds.get(r.pid) } : r)));
      },
    ),
  );
}

// macOS: `ps -o pid=,comm=` prints each process's argv[0] as the process was started (or rewrote it), read whole:
// a path with spaces stays one, and a program run from PATH shows its bare name (`node`).
function parsePsComm(out) {
  const exes = new Map();
  for (const line of String(out).split('\n')) {
    const m = line.match(/^\s*(\d+) (.+)$/);
    if (m && m[2].trim()) exes.set(Number(m[1]), m[2].trim());
  }
  return exes;
}
// An argv from a ps command line whose argv[0] is known (ps comm): that whole, then the other arguments split on
// spaces. `args` normally starts with it; if not, its first word is dropped in favour of comm.
function argvFrom(args, exe) {
  const text = String(args || '');
  const rest = text.startsWith(exe) ? text.slice(exe.length) : text.replace(/^\S*/, '');
  return [exe, ...rest.trim().split(/\s+/).filter(Boolean)];
}
// Linux ps comm is the kernel's task name, cut at 15 characters, not argv[0]. argv[0] is the first word of the command
// line when that word's file name starts with it (a whole path, not cut), else the name itself (a rewritten title).
function linuxExe(args, comm) {
  const first = String(args || '').split(/\s+/)[0] || '';
  return first && first.split('/').pop().startsWith(comm) ? first : comm;
}
// On macOS, an argv for the rows `wanted` picks, from their argv[0] (ps args cannot tell where a path with spaces
// ends). Linux rows have one from /proc already; rows from ps (`viaPs`, /proc refused) get one the same way, with
// argv[0] by linuxExe. A failure leaves the rows without one.
function withArgv(rows, wanted, platform = process.platform, { viaPs = false } = {}) {
  const pids = rows.filter((r) => !r.argv && wanted(r)).map((r) => r.pid);
  const linux = platform === 'linux' && viaPs;
  if (!(platform === 'darwin' || linux) || !pids.length) return Promise.resolve(rows);
  return new Promise((resolve) =>
    execFile(
      'ps',
      ['-ww', '-o', 'pid=,comm=', '-p', pids.join(',')],
      { timeout: 3000, maxBuffer: 1024 * 1024 },
      (err, stdout) => {
        const exes = parsePsComm(stdout || ''); // ps exits 1 when a pid has gone; what it printed still counts
        const exe = (r) => (linux ? linuxExe(r.args, exes.get(r.pid)) : exes.get(r.pid));
        resolve(rows.map((r) => (exes.has(r.pid) ? { ...r, argv: argvFrom(r.args, exe(r)) } : r)));
      },
    ),
  );
}

// Every process row, with the working folder and argv of each AI app process where the OS can tell (agent control).
// `cwds: false` skips the working folders (lsof on macOS) for reads that only check a process before a signal; the
// argv is always read, since that check needs the program.
// `platform` and `linux` (linuxRows' options) are for the tests (a pretend macOS or Linux); they default to this
// machine's.
const rows = async ({ cwds = true, platform = process.platform, linux = {} } = {}) => {
  const all = await rowsForOs(platform, linux);
  const viaPs = platform === 'linux' && (linux.state || LINUX).procRefused;
  const opts = { viaPs, state: linux.state || LINUX, warn: linux.warn, seen: linux.seen };
  return withArgv(cwds ? await withCwds(all, isAgent, platform, opts) : all, isAgent, platform, opts);
};

async function snapshot() {
  const used = await usedMemory();
  try {
    const all = await rowsForOs();
    const rows = await withCwds(all, undefined, process.platform, {
      viaPs: process.platform === 'linux' && LINUX.procRefused,
    });
    const skip = runningPids();
    return { ts: Date.now(), apps: groupApps(rows, { skip }), installed: installed(), system: system(used) };
  } catch (err) {
    return { ts: Date.now(), apps: [], installed: installed(), system: system(used), error: err.message };
  }
}

module.exports = {
  installed,
  snapshot,
  system,
  parseVmStat,
  parseMeminfo,
  linuxUsedMemory,
  procRows,
  linuxRows,
  linuxLimits,
  linuxExe,
  refused,
  warnOnce,
  parsePsRows,
  parseWinRows,
  WIN_PS,
  parseLsofCwd,
  parsePsComm,
  argvFrom,
  groupApps,
  isJudge,
  isOwn,
  rows,
  APPS,
};
