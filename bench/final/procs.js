// Process snapshots for the final benchmark runner (scripts/final-bench.js): every process with its parent, its command
// line and its cumulative CPU time, on macOS and Linux (`ps`) and on Windows (`Win32_Process` through PowerShell).
// From two snapshots: the CPU each process used in between (percent of one core), the tree of a process, and the
// stopping of a tree. The parsers are pure and tested with fixtures (test/final-bench-procs.test.js).
const { spawnSync } = require('child_process');

// The POSIX command (args last: it may hold spaces). -ww: no truncation of the command line.
const PS_ARGS = ['-A', '-ww', '-o', 'pid=,ppid=,time=,args='];
// The Windows command: one JSON array of every process (a single process is an object, not an array).
const WIN_PS =
  'Get-CimInstance Win32_Process | Select-Object ProcessId,ParentProcessId,Name,CommandLine,KernelModeTime,UserModeTime | ConvertTo-Json -Compress';

/**
 * ps's cumulative CPU time in seconds: "[[dd-]hh:]mm:ss[.cc]" (Linux "01:02:03", "1-02:03:04"; macOS "12:34.56", minutes
 * may pass 59). NaN for anything else.
 * @param {string} s
 */
function parseCpuTime(s) {
  const m = /^(?:(\d+)-)?([\d:.]+)$/.exec(String(s || '').trim());
  if (!m) return NaN;
  const parts = m[2].split(':');
  if (parts.length > 3 || parts.some((p) => !/^\d+(\.\d+)?$/.test(p))) return NaN;
  let secs = 0;
  for (const p of parts) secs = secs * 60 + Number(p);
  return secs + (m[1] ? Number(m[1]) * 86400 : 0);
}

/** The program name of a command line: the last path segment of its first word (paths with spaces kept whole). */
function nameOf(args) {
  const a = String(args || '');
  const mark = '.app/Contents/MacOS/';
  const at = a.lastIndexOf(mark);
  if (at >= 0) return a.slice(at + mark.length).split(/\s+-/)[0].trim();
  const first = a.split(/\s+/)[0] || '';
  return first.split(/[\\/]/).pop() || first;
}

/**
 * `ps -A -ww -o pid=,ppid=,time=,args=` → [{ pid, ppid, cpuSeconds, args, name }].
 * @param {string} text
 */
function parsePs(text) {
  const out = [];
  for (const line of String(text || '').split('\n')) {
    const m = /^\s*(\d+)\s+(\d+)\s+(\S+)\s+(.*)$/.exec(line);
    if (!m) continue;
    const args = m[4].trim();
    out.push({ pid: Number(m[1]), ppid: Number(m[2]), cpuSeconds: parseCpuTime(m[3]), args, name: nameOf(args) });
  }
  return out;
}

/**
 * The JSON of WIN_PS → the same records (CPU times are in 100 ns units).
 * @param {string} text
 */
function parseWinProcs(text) {
  let list;
  try {
    list = JSON.parse(String(text || '').trim() || '[]');
  } catch {
    return [];
  }
  if (!Array.isArray(list)) list = [list];
  return list
    .filter((p) => p && Number.isInteger(p.ProcessId))
    .map((p) => {
      const name = String(p.Name || '');
      return {
        pid: p.ProcessId,
        ppid: Number(p.ParentProcessId) || 0,
        cpuSeconds: ((Number(p.KernelModeTime) || 0) + (Number(p.UserModeTime) || 0)) / 1e7,
        args: String(p.CommandLine || name),
        name: name.replace(/\.exe$/i, ''),
      };
    });
}

/**
 * Every process now; [] when the listing fails. exec is injectable (spawnSync's shape: { status, stdout }).
 * @param {{ platform?: string, exec?: Function }} [o]
 */
function listProcesses({ platform = process.platform, exec = spawnSync } = {}) {
  if (platform === 'win32') {
    const r = exec('powershell', ['-NoProfile', '-NonInteractive', '-Command', WIN_PS], {
      encoding: 'utf8',
      timeout: 30000,
      maxBuffer: 64 * 1024 * 1024,
    });
    return r && r.status === 0 ? parseWinProcs(r.stdout) : [];
  }
  const r = exec('ps', PS_ARGS, { encoding: 'utf8', timeout: 15000, maxBuffer: 64 * 1024 * 1024 });
  return r && r.status === 0 ? parsePs(r.stdout) : [];
}

/**
 * The pids of a process and all its descendants in a snapshot.
 * @param {{ pid: number, ppid: number }[]} procs
 * @param {number[]} roots
 */
function treeOf(procs, roots) {
  const kids = new Map();
  for (const p of procs) {
    if (!kids.has(p.ppid)) kids.set(p.ppid, []);
    kids.get(p.ppid).push(p.pid);
  }
  const seen = new Set();
  const stack = [...roots];
  while (stack.length) {
    const pid = stack.pop();
    if (seen.has(pid)) continue;
    seen.add(pid);
    for (const k of kids.get(pid) || []) if (pid !== k) stack.push(k);
  }
  return seen;
}

/**
 * CPU per process between two snapshots, in percent of one core: [{ pid, name, args, cpu }], highest first. A pid
 * whose command line changed (reused) or that is new counts from zero.
 * @param {any[]} before
 * @param {any[]} after
 * @param {number} seconds the wall time between them
 */
function cpuBetween(before, after, seconds) {
  const prev = new Map(before.map((p) => [p.pid, p]));
  const out = [];
  for (const p of after) {
    const b = prev.get(p.pid);
    const from = b && b.args === p.args && Number.isFinite(b.cpuSeconds) ? b.cpuSeconds : 0;
    const used = Number.isFinite(p.cpuSeconds) ? Math.max(0, p.cpuSeconds - from) : 0;
    out.push({ pid: p.pid, ppid: p.ppid, name: p.name, args: p.args, cpu: seconds > 0 ? (used / seconds) * 100 : 0 });
  }
  return out.sort((x, y) => y.cpu - x.cpu);
}

/**
 * Stops the given processes: SIGTERM, then SIGKILL after graceMs for any still there (Windows: taskkill /T /F).
 * Each entry carries the command line seen, so a pid reused by another program meanwhile is left alone.
 * @param {{ pid: number, args: string }[]} procs
 * @param {{ platform?: string, exec?: Function, list?: Function, graceMs?: number, kill?: Function }} [o]
 */
async function stopAll(procs, o = {}) {
  const platform = o.platform || process.platform;
  const exec = o.exec || spawnSync;
  const list = o.list || (() => listProcesses({ platform, exec }));
  const kill = o.kill || ((pid, sig) => process.kill(pid, sig));
  const still = () => {
    const now = new Map(list().map((p) => [p.pid, p.args]));
    return procs.filter((p) => p.pid !== process.pid && now.get(p.pid) === p.args);
  };
  let left = still();
  if (!left.length) return [];
  if (platform === 'win32') {
    for (const p of left) exec('taskkill', ['/PID', String(p.pid), '/T', '/F'], { encoding: 'utf8', timeout: 15000 });
    return still().map((p) => p.pid);
  }
  for (const p of left) {
    try {
      kill(p.pid, 'SIGTERM');
    } catch {
      /* gone */
    }
  }
  const end = Date.now() + (o.graceMs ?? 3000);
  while (Date.now() < end) {
    await new Promise((r) => setTimeout(r, 200));
    left = still();
    if (!left.length) return [];
  }
  for (const p of left) {
    try {
      kill(p.pid, 'SIGKILL');
    } catch {
      /* gone */
    }
  }
  await new Promise((r) => setTimeout(r, 300));
  return still().map((p) => p.pid);
}

module.exports = { PS_ARGS, WIN_PS, parseCpuTime, nameOf, parsePs, parseWinProcs, listProcesses, treeOf, cpuBetween, stopAll };
