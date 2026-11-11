// The Deno permission matrix in code (P2 spec D3, D4). The sets themselves live in deno.json. This module:
//   - says how this program starts one of its own entry points under the running runtime (selfCommand);
//   - computes the folders a `deno run` launch may write (writeScope) and the programs it may run (runScope), which
//     deno.json cannot hold: its paths are fixed when the file is read (relative to deno.json; `~` and $VARS are not
//     expanded), and a compiled binary bakes them in at build time, so the binary keeps an unscoped write;
//   - names the reason for every allow-listed entry and every broad one (WHY; test/permissions.test.js keeps it and
//     deno.json in step);
//   - tells the Compliance page what the running process holds (describe).
// Under Node nothing here restricts anything.
const fs = require('fs');
const os = require('os');
const path = require('path');
const { spawnSync } = require('child_process');
const platform = require('./platform');

const APP_DIR = platform.APP_DIR;
const CONFIG = path.join(APP_DIR, 'deno.json');
const SETS = ['server', 'cli', 'mcp-stdio', 'test'];

const WHY = {
  run: {
    ps: 'the process list and argv on macOS, and on Linux when Deno refuses /proc (lib/processes.js psRows, withArgv)',
    lsof: 'the working folder of a running agent on macOS, and on Linux when Deno refuses /proc (lib/processes.js withCwds)',
    vm_stat: 'used memory on macOS (lib/processes.js usedMemory)',
    scutil: 'the computer name on macOS (lib/platform.js computerName)',
    id: 'the first name for the greeting on macOS (lib/platform.js fullName)',
    hostnamectl: 'the computer name on Linux (lib/platform.js computerName)',
    getent: 'the first name on Linux (lib/platform.js fullName)',
    which: 'whether secret-tool is installed on Linux (lib/secret-store.js has)',
    where: 'the same check on Windows (lib/secret-store.js has)',
    security: 'secrets in the macOS Keychain (lib/secret-store.js keychainBackend)',
    'secret-tool': 'secrets in the Linux keyring (lib/secret-store.js libsecretBackend)',
    'powershell.exe': 'the Windows process list and DPAPI (lib/processes.js winRows, lib/secret-store.js dpapi)',
    powershell: 'Windows notifications (lib/monitors/channels.js)',
    osascript: 'macOS notifications (lib/monitors/channels.js)',
    'notify-send': 'Linux notifications (lib/monitors/channels.js)',
    taskkill: 'stop an agent or a model server on Windows (lib/agents/control.js, lib/runtimes/control.js)',
    tasklist:
      "whether a process is alive on Windows: Deno's process.kill(pid, 0) needs an unscoped run grant (lib/permissions.js kill)",
    launchctl: 'whether a model server is a LaunchAgent (lib/runtimes/state.js); the macOS service (lib/service.js)',
    open: 'open the dashboard and start desktop apps on macOS (lib/platform.js openUrl, lib/runtimes/state.js)',
    'xdg-open': 'open the dashboard on Linux (lib/platform.js openUrl)',
    cmd: 'open the dashboard on Windows (lib/platform.js openUrl)',
    claude: 'the Claude Code judge and MCP registration (lib/evals/claude-cli.js, scripts/register-mcp.js)',
    codex: 'MCP registration through `codex mcp add` (scripts/register-mcp.js)',
    node: 'a Claude Code installed as cli.js runs through node (lib/evals/claude-cli.js nodeBin)',
    ollama: 'the Ollama preset (lib/runtimes/registry.js)',
    lms: 'the LM Studio preset (lib/runtimes/registry.js)',
    'llama-server': 'the llama.cpp preset (lib/runtimes/registry.js)',
    'local-ai': 'the LocalAI preset (lib/runtimes/registry.js)',
    brew: 'brew services stop for a Homebrew model server (lib/runtimes/control.js)',
    systemctl: 'install the Linux service (lib/service.js); cli only',
    loginctl: 'keep the Linux user service running after logout (lib/service.js); cli only',
    useradd: 'the service account of a Linux system install (lib/service.js --system); cli only',
    chown: 'give the data folder to that account (lib/service.js --system); cli only',
    schtasks: 'the Windows boot task (lib/service.js --system); cli only',
    'wscript.exe': 'start the hidden Windows launcher (lib/service.js); cli only',
    'explorer.exe': 'open the local sign-in page on Windows (bin/zaafpulse.js openFile); cli only',
    kill: "send a signal: Deno's process.kill needs an unscoped run grant (lib/permissions.js kill; agent control, model servers, the judge)",
  },
  sys: {
    homedir: 'every per-user folder (os.homedir)',
    uid: 'root and ownership checks, the login name (process.getuid, os.userInfo)',
    gid: 'os.userInfo() needs uid, gid and userInfo together (lib/platform.js fullName, lib/service.js)',
    userInfo: 'the login name (os.userInfo in lib/platform.js fullName, lib/service.js loginctl)',
    hostname: 'the computer name when the OS tool gives none (lib/platform.js computerName)',
    systemMemoryInfo: 'the system card (os.totalmem, os.freemem)',
    osUptime: 'the system card (os.uptime)',
    loadavg: 'the system card (os.loadavg)',
    cpus: 'the system card and worker count (os.cpus)',
    umask: 'private files: process.umask(0o077) in mcp.js and server.js',
    osRelease: 'zaafpulse doctor prints the OS version (os.release); cli only',
  },
  // The entries that are not an allow-list, and why.
  broad: {
    read: 'all: the indexer reads agent logs under the home folder and owner-chosen folders (Obsidian vaults, ZAAFPULSE_CLAUDE_DIRS); runtime detection reads /opt/homebrew, /usr/local, /Applications. Linux /proc is refused anyway: Deno has no scoped route to it, only all permissions',
    write:
      'all in deno.json, narrowed at every `deno run` launch to writeScope() (a CLI --allow-write replaces the set entry); unscoped only in the compiled binary, whose paths are fixed at build time. It guards against accidents, not a determined attacker: run holds interpreters (WHY.launchers) that write anywhere',
    net: 'all but cloud metadata: providers, connectors and price sync go out until P5 moves egress to Go; 169.254.169.254 and metadata.google.internal are refused',
    env: 'all names (owner decision O1): a v1 secret reference ({ "env": "DT_TOKEN" }) may name any variable',
  },
  // The run entries that start any program or run any code they are given. Each is needed (see WHY.run), and each
  // means the run allow-list is not a sandbox boundary: code that can spawn one of these can do anything the user can,
  // outside Deno's permissions, including writing outside writeScope(). The run and write scopes guard against
  // accidents and against code paths that were never meant to spawn, not against a determined attacker inside the
  // process. The Compliance control says so (lib/governance/controls.js runtime_permissions).
  launchers: {
    node: 'runs any JavaScript file',
    osascript: 'runs any AppleScript, including `do shell script`',
    powershell: 'runs any PowerShell command',
    'powershell.exe': 'runs any PowerShell command',
    cmd: 'runs any cmd.exe command line',
    open: 'opens any file or app with its default handler',
    'xdg-open': 'opens any file with its default handler',
    'explorer.exe': 'opens any file with its default handler',
    'wscript.exe': 'runs any VBScript file',
    brew: 'installs and runs any Homebrew formula',
    claude: 'an agent that runs shell commands',
    codex: 'an agent that runs shell commands',
    launchctl: 'loads any launchd job',
    systemctl: 'starts any systemd unit',
    schtasks: 'schedules any command',
  },
};

// The sets in deno.json, by name. deno.json is JSON with comments.
function readSets(file = CONFIG) {
  return require('./governance/mcp-scan').parseJsonc(fs.readFileSync(file, 'utf8')).permissions;
}

const unique = (list) => [...new Set(list.filter(Boolean))];

// A character that must never reach a launch line from a scope entry: a comma (it splits one --allow-write or
// --allow-run entry into two grants: a data folder named "x,/" would grant the root), a double quote and a control
// character (a line break). Everything else a folder or user name can hold (O'Brien, AT&T, $x, ^, |, <, >, a backtick,
// %) is passed: the launch is an argument list, never a shell line, and each file that carries it escapes it for its
// own syntax (lib/service.js plist, unitText; scripts/register-mcp.js JSON and TOML). The Windows launcher is the one
// sink that cannot escape a % or a quote inside a quoted cmd.exe argument, so it refuses those itself (service.js
// vbsText), before anything is written. The entries come from config.json, which the dashboard writes, and from the
// environment.
const UNSAFE = /[,"\u0000-\u001f\u007f]/;
const describeChar = (c) =>
  c === ',' ? 'a comma' : /[\u0000-\u001f\u007f]/.test(c) ? 'a control character or line break' : JSON.stringify(c);

// Throws, naming the value and the reason, when an entry of the --allow-<kind> list could widen the grant or break
// the launch line; returns the list otherwise.
function checkScope(kind, list) {
  for (const v of list) {
    const m = String(v).match(UNSAFE);
    if (m)
      throw new Error(
        `--allow-${kind}: ${JSON.stringify(String(v))} holds ${describeChar(m[0])}, which this launch cannot pass safely ` +
          '(a comma splits the grant; a quote or a line break breaks the launch line); rename the folder or program, or move it',
      );
  }
  return list;
}

// The folders a `deno run` launch of `set` may write, as absolute paths. Every set writes the data folder and the OS
// temp folder. server and cli also install skills (lib/gateway/skills.js) and register the MCP server in each client's
// config folder (scripts/register-mcp.js writes <file>.tmp-<pid> and <file>.bak-<time> beside the file, so the folder
// is granted); a client whose file sits directly in the home folder (Claude Code's ~/.claude.json, registered through
// the claude CLI) is left out, so the home folder itself is never granted. cli also writes the service files.
function writeScope({
  set,
  dataDir,
  home = os.homedir(),
  env = process.env,
  platform: os_ = process.platform,
  tmp = os.tmpdir(),
}) {
  const out = [dataDir, tmp];
  if (set === 'mcp-stdio' && env.ACC_CACHE_DIR) out.push(path.resolve(platform.expandHome(env.ACC_CACHE_DIR)));
  if (set === 'server' || set === 'cli') {
    const roots = require('./gateway/skills').skillRoots({ home, env });
    out.push(roots.claude, roots.agents);
    const { clients } = require('./mcp-clients');
    // clients() joins with the path rules of `platform` (posix for darwin and linux, win32 for win32); the grant is read
    // by this host, so each folder is written in the host's own form (the two differ only when a test asks for another
    // OS than the one running).
    for (const c of Object.values(clients(home, { env, platform: os_ }))) {
      const dir = path.normalize(path.dirname(c.file));
      if (dir !== path.normalize(home) && dir !== path.parse(dir).root) out.push(dir);
    }
  }
  if (set === 'cli') {
    if (os_ === 'darwin')
      out.push(path.join(home, 'Library', 'LaunchAgents'), path.join(home, 'Library', 'Logs', 'ZAAFPulse'));
    else if (os_ === 'win32') {
      const appData = env.APPDATA || path.join(home, 'AppData', 'Roaming');
      const local = env.LOCALAPPDATA || path.join(home, 'AppData', 'Local');
      out.push(
        path.join(appData, 'Microsoft', 'Windows', 'Start Menu', 'Programs', 'Startup'),
        path.join(local, 'ZAAFPulse'),
      );
    } else out.push(path.join(env.XDG_CONFIG_HOME || path.join(home, '.config'), 'systemd', 'user'));
  }
  return checkScope('write', unique(out));
}

// The programs a `deno run` launch of `set` may run: the set's list, plus the owner's own model servers from
// config.json "localRuntimes" (their first word) and ZAAFPULSE_CLAUDE_BIN. A CLI --allow-run replaces the set's
// list, so the whole list is passed. A compiled binary has only the set's list (owner decision O2: refused loudly).
// WHY only approved owner runtimes: config.json is written from the dashboard's settings, and an owner entry starts
// only after the owner approved its command as it is now (lib/runtimes/control.js approved: the approval is a hash of
// the command). An entry not approved could never run anyway, so it must not widen the run grant or reach the launch
// line. `approvals` is .data/runtimes/approved.json ({ [id]: commandHash }).
// The list also holds every absolute path the product's own lookups resolve for an allowed program it starts by path
// (lookedUp): Deno grants a name only when it finds it on PATH at start, and neither then grants that program by its
// absolute path; a service starts with its supervisor's minimal PATH, so without these a Claude Code in ~/.local/bin
// or Homebrew's brew would be refused although its name is allowed. `home` and `platform` are for the tests.
function runScope({
  set,
  config = {},
  env = process.env,
  approvals = {},
  home = os.homedir(),
  platform: os_ = process.platform,
}) {
  const listed = readSets()[set].run;
  const own = Array.isArray(listed) ? listed : [];
  const extra = [];
  if (set === 'server' || set === 'cli') {
    const { ownerEntries, commandHash } = require('./runtimes/registry');
    const ok = approvals && typeof approvals === 'object' ? approvals : {};
    for (const e of ownerEntries(config && config.localRuntimes))
      if (!e.error && e.command[0] && Object.hasOwn(ok, e.id) && ok[e.id] === commandHash(e)) extra.push(e.command[0]);
    if (env.ZAAFPULSE_CLAUDE_BIN) extra.push(env.ZAAFPULSE_CLAUDE_BIN);
  }
  const { PRESETS } = require('./runtimes/registry');
  const byPath = new Set(['claude', 'node', 'brew', ...PRESETS.map((x) => x.bin).filter(Boolean)]);
  const resolve = unique([...own.filter((n) => byPath.has(n)), ...extra]);
  return checkScope('run', unique([...own, ...extra, ...lookedUp(resolve, { env, home, platform: os_ })]));
}

// Every trusted match of each bare program name in the folders the product's lookups search, in the form they spawn
// it: PATH, then the usual folders of lib/runtimes/state.js findBin (Homebrew, /usr/local/bin, /usr/bin, ~/.lmstudio/bin,
// ~/.local/bin; brewBin's /opt/homebrew/bin/brew and /usr/local/bin/brew are among them), plus lib/evals/claude-cli.js
// candidates (~/.claude/local, ~/.local/bin) and nodeDirs (%ProgramFiles%\nodejs). Deno matches the literal path, not
// the file behind a link, so each is the path as joined. A match holding a character a scope entry cannot (checkScope:
// a comma, a quote, a control character) is left out: its name stays granted, and the launch never fails over it.
// Not covered (owner decision O2, refused loudly): a Claude Code inside an IDE extension or Claude Desktop folder.
function lookedUp(names, { env, home, platform: os_ }) {
  const { findBin, defaultBinDirs } = require('./runtimes/state');
  const win = os_ === 'win32';
  const p = win ? path.win32 : path.posix;
  const extraDirs = {
    claude: win
      ? [p.join(home, '.local', 'bin'), p.join(home, '.claude', 'local')]
      : [p.join(home, '.claude', 'local'), p.join(home, '.local', 'bin')],
    node: win && env.ProgramFiles ? [p.join(env.ProgramFiles, 'nodejs')] : [],
  };
  const pathDirs = String(env.PATH || env.Path || '')
    .split(win ? ';' : ':')
    .filter((d) => d && p.isAbsolute(d));
  const out = [];
  for (const name of names) {
    if (typeof name !== 'string' || !name || /[\\/]/.test(name)) continue; // an absolute path is granted as it is
    for (const d of unique([...pathDirs, ...defaultBinDirs(os_, home), ...(extraDirs[name] || [])])) {
      if (UNSAFE.test(d)) continue;
      const f = findBin(name, { env: { PATH: '' }, platform: os_, home, dirs: [d] });
      if (f && !UNSAFE.test(f)) out.push(f);
    }
  }
  return out;
}

const readJson = (file) => {
  try {
    const v = JSON.parse(fs.readFileSync(file, 'utf8'));
    return v && typeof v === 'object' && !Array.isArray(v) ? v : {};
  } catch {
    return {};
  }
};
const readConfig = (dataDir) => readJson(path.join(dataDir, 'config.json'));
const readApprovals = (dataDir) => readJson(path.join(dataDir, 'runtimes', 'approved.json'));

// How this program starts one of its own entry points: `script` (relative to the app folder) under the runtime that
// runs now, or `sub` (a subcommand) of the single-file binary. Under `deno run` the launch carries the set and the
// computed write and run scopes, never -A. Every scope entry is checked (checkScope): one that could widen the grant
// or break the launch line throws, naming the value, before anything is written. `runtime` and `singleFile` are for
// the tests.
function selfCommand({
  script,
  sub,
  set,
  dataDir,
  env = process.env,
  config,
  approvals,
  runtime = platform.runtime(),
  singleFile = platform.isSingleFile(),
}) {
  if (singleFile) return { command: process.execPath, args: [sub] };
  const file = path.join(APP_DIR, script);
  if (runtime !== 'deno') return { command: process.execPath, args: [file] };
  const cfg = config || readConfig(dataDir);
  const ok = approvals || readApprovals(dataDir);
  return {
    command: process.execPath,
    args: [
      'run',
      '-q',
      '--no-prompt',
      `--config=${CONFIG}`,
      `-P=${set}`,
      `--allow-write=${writeScope({ set, dataDir, env }).join(',')}`,
      `--allow-run=${runScope({ set, config: cfg, env, approvals: ok }).join(',')}`,
      file,
    ],
  };
}

// What the running process holds, per permission: 'all', 'all but denied' (granted, partial: a deny list, or a set's
// `write: true` in the compiled binary, which Deno reports as partial), 'scoped' (only
// some paths, names or hosts, or none) or 'unknown'. { runtime: 'node' } under Node. On Linux it also says what the
// refused /proc costs (lib/processes.js linuxLimits), as `notes`.
function describe({ deno = globalThis.Deno, limits = () => require('./processes').linuxLimits() } = {}) {
  if (!deno) return { runtime: 'node' };
  const grant = (name) => {
    try {
      const s = deno.permissions.querySync({ name });
      return s.state === 'granted' ? (s.partial ? 'all but denied' : 'all') : 'scoped';
    } catch {
      return 'unknown';
    }
  };
  // The general-purpose launchers this process may run (WHY.launchers): while any is granted, run is no sandbox
  // boundary. Not asked when run is unrestricted anyway.
  let launchers = [];
  if (grant('run') !== 'all')
    launchers = Object.keys(WHY.launchers).filter((command) => {
      try {
        return deno.permissions.querySync({ name: 'run', command }).state === 'granted';
      } catch {
        return false;
      }
    });
  let notes = [];
  try {
    const l = limits();
    if (l) notes = [l.text];
  } catch {
    notes = [];
  }
  return {
    runtime: 'deno',
    version: (deno.version && deno.version.deno) || '',
    singleFile: !!(deno.build && deno.build.standalone),
    grants: Object.fromEntries(['read', 'write', 'net', 'env', 'run', 'sys'].map((k) => [k, grant(k)])),
    ...(launchers.length ? { launchers } : {}),
    ...(notes.length ? { notes } : {}),
  };
}

// spawnSync under Deno returns {} (no status, no signal, no error) when --allow-run refuses the program; Node always
// sets one of them (a program that is not installed gives an ENOENT error on both). Callers turn this into a loud error.
function spawnRefused(r) {
  return !!r && r.status === undefined && r.signal === undefined && r.error === undefined;
}

// process.kill, except under Deno with a scoped run grant: Deno's process.kill needs an unscoped one (measured, Deno
// 2.9.8: a run allow-list refuses every signal, even 0, with NotCapable "Requires run access"). There the signal goes
// through the kill program, which the server and cli sets allow, and its failures come back as the ESRCH and EPERM
// errors process.kill throws, so callers tell a gone process from another user's as before. A negative pid is a
// process group (the judge stops its child's group: lib/evals/claude-cli.js); 0 and -1 (the caller's group, every
// process) are refused, and so is any signal but the ones the callers send. The kill program runs in the C locale, so
// its error text is the English one the mapping reads.
// Windows has no kill program and no process groups. Under a scoped Deno run grant, signal 0 (is it alive?) is
// answered by `tasklist /FI "PID eq <pid>" /NH /FO CSV` (no shell; the pid is a checked whole number), whose CSV rows
// carry the pid in the second field whatever the language: no row means ESRCH. tasklist lists every user's processes,
// so EPERM never comes from it. A real signal there is refused with a clear error: Windows callers stop a process with
// taskkill (lib/agents/control.js, lib/runtimes/control.js). Under Node, or with an unscoped run grant, Windows keeps
// process.kill. `deno`, `spawn` and `platform` are for the tests.
const SIGNALS = new Set([0, 'SIGSTOP', 'SIGCONT', 'SIGINT', 'SIGTERM', 'SIGKILL']);
function kill(
  pid,
  sig = 'SIGTERM',
  { deno = globalThis.Deno, spawn = spawnSync, platform: os_ = process.platform } = {},
) {
  if (!Number.isSafeInteger(pid) || pid === 0 || pid === -1)
    throw new Error(`kill: the pid must be a whole number other than 0 and -1, not ${JSON.stringify(String(pid))}`);
  if (!SIGNALS.has(sig))
    throw new Error(
      `kill: the signal must be one of 0, ${[...SIGNALS].slice(1).join(', ')}, not ${JSON.stringify(String(sig))}`,
    );
  if (!deno || runGranted(deno)) return process.kill(pid, sig);
  if (os_ === 'win32') return winAlive(pid, sig, spawn);
  const name = sig === 0 ? '0' : sig.replace(/^SIG/, '');
  const r = spawn('kill', [`-${name}`, '--', String(pid)], {
    encoding: 'utf8',
    env: { ...process.env, LC_ALL: 'C', LANG: 'C' },
  });
  if (spawnRefused(r)) throw new Error("kill: not allowed by this build's run permissions");
  if (r.error) throw r.error; // no kill program (a minimal Linux image without procps)
  if (r.status === 0) return true;
  const said = String(r.stderr || '').trim();
  const code = /no such process/i.test(said) ? 'ESRCH' : /not permitted/i.test(said) ? 'EPERM' : undefined;
  throw Object.assign(new Error(`kill ${pid}: ${said || `exit ${r.status}`}`), code ? { code } : {});
}
function winAlive(pid, sig, spawn) {
  if (pid < 0) throw new Error(`kill: Windows has no process groups; the pid must be positive, not ${pid}`);
  if (sig !== 0)
    throw new Error(`kill: ${sig} cannot be sent on Windows under this build's run permissions; stop it with taskkill`);
  const r = spawn('tasklist', ['/FI', `PID eq ${pid}`, '/NH', '/FO', 'CSV'], { encoding: 'utf8', windowsHide: true });
  if (spawnRefused(r)) throw new Error("tasklist: not allowed by this build's run permissions");
  if (r.error) throw r.error;
  if (r.status !== 0) throw new Error(`tasklist ${pid}: ${String(r.stderr || '').trim() || `exit ${r.status}`}`);
  const found = String(r.stdout || '')
    .split(/\r?\n/)
    .some((line) => {
      const m = /^"[^"]*","(\d+)"/.exec(line.trim());
      return !!m && Number(m[1]) === pid;
    });
  if (found) return true;
  throw Object.assign(new Error(`kill ${pid}: no such process`), { code: 'ESRCH' });
}
function runGranted(deno) {
  try {
    const s = deno.permissions.querySync({ name: 'run' });
    return s.state === 'granted' && !s.partial;
  } catch {
    return false;
  }
}

module.exports = {
  APP_DIR,
  CONFIG,
  SETS,
  WHY,
  readSets,
  UNSAFE,
  checkScope,
  writeScope,
  runScope,
  selfCommand,
  describe,
  spawnRefused,
  kill,
};
