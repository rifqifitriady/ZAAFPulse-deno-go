// Runs ZAAFPulse in the background and at startup, on each OS:
//   macOS    a LaunchAgent (com.zaaf.aipulse.server), restarted if it exits
//   Linux    a systemd unit: per user (~/.config/systemd/user) or, as root, a system service that runs
//            as the "zaafpulse" user with data in /var/lib/zaafpulse (RHEL, Debian, Ubuntu, SUSE …)
//   Windows  a hidden launcher in the Startup folder (or, with --system, a Task Scheduler task at boot
//            as SYSTEM) that restarts the server if it exits
// Only non-secret settings go into these files (host, port, data folder, TLS paths …). The admin
// password is a hash saved by `zaafpulse set-password`; secrets stay in the OS secret store.
const fs = require('fs');
const os = require('os');
const path = require('path');
const { spawnSync } = require('child_process');
const platform = require('./platform');
const permissions = require('./permissions');

const LABEL = 'com.zaaf.aipulse.server';
const UNIT = 'zaafpulse';
const TASK = 'ZAAFPulse';
const CARRY = [
  'ZAAFPULSE_HOST',
  'ZAAFPULSE_PORT',
  'ZAAFPULSE_DATA',
  'ZAAFPULSE_PUBLIC_URL',
  'ZAAFPULSE_TLS_CERT',
  'ZAAFPULSE_TLS_KEY',
  'ZAAFPULSE_ALLOWED_HOSTS',
  'ZAAFPULSE_TRUST_PROXY',
  'ZAAFPULSE_SECRET_STORE',
  'ZAAFPULSE_AUTH',
  // Where the Claude Code and Codex logs are; a service does not inherit the shell profile that sets them.
  'CLAUDE_CONFIG_DIR',
  'ZAAFPULSE_CLAUDE_DIRS',
  'CODEX_HOME',
  'XDG_CONFIG_HOME',
  // The Claude Code judge's binary when named by hand; a service has no shell PATH to find it on.
  'ZAAFPULSE_CLAUDE_BIN',
];

// A program the runtime permissions refuse is an error here, not Deno's silent empty result.
const sh = (cmd, args, opts = {}) => {
  const r = spawnSync(cmd, args, { encoding: 'utf8', windowsHide: true, ...opts });
  if (permissions.spawnRefused(r))
    throw new Error(`${cmd}: not allowed by this build's run permissions (deno.json "cli")`);
  return r;
};
const isRoot = () => typeof process.getuid === 'function' && process.getuid() === 0;

// The command that starts the server: the single-file binary, node + bin/zaafpulse.js, or deno run with the server
// permission set and the write and run scopes of this data folder (lib/permissions.js).
// `opts` ({ runtime, singleFile }) is for the tests.
function startCommand(env = process.env, opts = {}) {
  const { command, args } = permissions.selfCommand({
    script: path.join('bin', 'zaafpulse.js'),
    sub: 'start',
    set: 'server',
    dataDir: platform.dataDir(env),
    env,
    ...opts,
  });
  return platform.isSingleFile() ? [command, ...args] : [command, ...args, 'start'];
}
function settings(env) {
  return Object.fromEntries(CARRY.filter((k) => env[k]).map((k) => [k, env[k]]));
}
const xml = (s) => String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');

/* ---------- macOS ---------- */
function plist(cmd, env, logFile) {
  return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>${LABEL}</string>
  <key>ProgramArguments</key>
  <array>
${cmd.map((a) => `    <string>${xml(a)}</string>`).join('\n')}
  </array>
  <key>EnvironmentVariables</key>
  <dict>
${Object.entries(env)
  .map(([k, v]) => `    <key>${k}</key><string>${xml(v)}</string>`)
  .join('\n')}
  </dict>
  <key>WorkingDirectory</key><string>${xml(platform.APP_DIR)}</string>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>10</integer>
  <key>ProcessType</key><string>Interactive</string>
  <key>StandardOutPath</key><string>${xml(logFile)}</string>
  <key>StandardErrorPath</key><string>${xml(logFile)}</string>
</dict>
</plist>
`;
}
const mac = {
  file: () => path.join(os.homedir(), 'Library', 'LaunchAgents', `${LABEL}.plist`),
  install(cmd, env, log) {
    const logs = path.join(os.homedir(), 'Library', 'Logs', 'ZAAFPulse');
    fs.mkdirSync(logs, { recursive: true });
    fs.mkdirSync(path.dirname(mac.file()), { recursive: true });
    fs.writeFileSync(
      mac.file(),
      plist(
        cmd,
        { PATH: '/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin', ...env },
        path.join(logs, 'server.log'),
      ),
    );
    const dom = `gui/${process.getuid()}`;
    sh('launchctl', ['bootout', `${dom}/${LABEL}`]);
    const r = sh('launchctl', ['bootstrap', dom, mac.file()]);
    if (r.status !== 0) throw new Error(`launchctl bootstrap failed: ${r.stderr.trim()}`);
    sh('launchctl', ['enable', `${dom}/${LABEL}`]);
    log(`LaunchAgent installed: ${mac.file()}\nLogs: ${path.join(logs, 'server.log')}`);
  },
  uninstall(log) {
    sh('launchctl', ['bootout', `gui/${process.getuid()}/${LABEL}`]);
    fs.rmSync(mac.file(), { force: true });
    log('LaunchAgent removed.');
  },
  status() {
    const r = sh('launchctl', ['print', `gui/${process.getuid()}/${LABEL}`]);
    return r.status === 0 ? (/state = running/.test(r.stdout) ? 'running' : 'installed, not running') : 'not installed';
  },
  restart() {
    return sh('launchctl', ['kickstart', '-k', `gui/${process.getuid()}/${LABEL}`]).status === 0;
  },
};

/* ---------- Linux (systemd) ---------- */
// systemd expands % specifiers in every value below and $VARIABLES in ExecStart: % is written %%, and $ in ExecStart
// $$, so a folder or argument holding them stays literal. A word holding a space, a quote of either kind or a
// backslash is double-quoted (systemd reads ' as a quote too). A control character (a line break would start a new unit
// line) is refused.
function unitText(cmd, env, { system }) {
  const plain = (what, a) => {
    const s = String(a);
    if (/[\u0000-\u001f\u007f]/.test(s))
      throw new Error(`systemd unit: ${what} ${JSON.stringify(s)} holds a line break or another control character`);
    return s.replace(/%/g, '%%');
  };
  const q = (a) => (/[\s"'\\]/.test(a) ? `"${a.replace(/\\/g, '\\\\').replace(/"/g, '\\"')}"` : a);
  const exec = (a) => q(plain('the argument', a).replace(/\$/g, '$$$$'));
  const data = env.ZAAFPULSE_DATA === undefined ? '' : plain('ZAAFPULSE_DATA', env.ZAAFPULSE_DATA);
  return `[Unit]
Description=ZAAFPulse AI command center and agent gateway
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=${cmd.map(exec).join(' ')}
WorkingDirectory=${plain('the app folder', platform.APP_DIR)}
${Object.entries(env)
  .map(([k, v]) => `Environment=${q(`${k}=${plain(k, v)}`)}`)
  .join('\n')}
Restart=on-failure
RestartSec=5
${
  system
    ? `User=zaafpulse
Group=zaafpulse
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=true
ReadWritePaths=${data}
`
    : ''
}
[Install]
WantedBy=${system ? 'multi-user.target' : 'default.target'}
`;
}
const linux = {
  file: (system) =>
    system
      ? `/etc/systemd/system/${UNIT}.service`
      : path.join(
          process.env.XDG_CONFIG_HOME || path.join(os.homedir(), '.config'),
          'systemd',
          'user',
          `${UNIT}.service`,
        ),
  ctl: (system, ...args) => sh('systemctl', system ? args : ['--user', ...args]),
  install(cmd, env, log, { system }) {
    const text = unitText(cmd, env, { system }); // refused before anything is written or created
    if (sh('systemctl', ['--version']).status !== 0)
      throw new Error('systemd was not found. Start ZAAFPulse with your own supervisor: ' + cmd.join(' '));
    if (system) {
      if (sh('id', ['-u', 'zaafpulse']).status !== 0) {
        const nologin = ['/usr/sbin/nologin', '/sbin/nologin'].find((p) => fs.existsSync(p)) || '/bin/false';
        const r = sh('useradd', ['--system', '--home-dir', env.ZAAFPULSE_DATA, '--shell', nologin, 'zaafpulse']);
        if (r.status !== 0) throw new Error(`could not create the zaafpulse user: ${r.stderr.trim()}`);
      }
      fs.mkdirSync(env.ZAAFPULSE_DATA, { recursive: true });
      sh('chown', ['-R', 'zaafpulse:zaafpulse', env.ZAAFPULSE_DATA]);
      fs.chmodSync(env.ZAAFPULSE_DATA, 0o750);
    }
    fs.mkdirSync(path.dirname(linux.file(system)), { recursive: true });
    fs.writeFileSync(linux.file(system), text);
    linux.ctl(system, 'daemon-reload');
    const r = linux.ctl(system, 'enable', '--now', UNIT);
    if (r.status !== 0) throw new Error(`systemctl enable failed: ${r.stderr.trim()}`);
    if (!system) {
      const l = sh('loginctl', ['enable-linger', os.userInfo().username]);
      if (l.status !== 0)
        log('Tip: run `sudo loginctl enable-linger $USER` so ZAAFPulse keeps running after you log out.');
    }
    log(
      `systemd ${system ? 'service' : 'user service'} installed: ${linux.file(system)}\nLogs: journalctl ${system ? '' : '--user '}-u ${UNIT} -f`,
    );
  },
  uninstall(log, { system }) {
    linux.ctl(system, 'disable', '--now', UNIT);
    fs.rmSync(linux.file(system), { force: true });
    linux.ctl(system, 'daemon-reload');
    log('systemd service removed.');
  },
  status({ system }) {
    const r = linux.ctl(system, 'is-active', UNIT);
    if (r.error) return 'no systemd on this machine';
    const state = (r.stdout || '').trim();
    return state === 'active'
      ? 'running'
      : fs.existsSync(linux.file(system))
        ? `installed, ${state || 'inactive'}`
        : 'not installed';
  },
  restart({ system }) {
    return linux.ctl(system, 'restart', UNIT).status === 0;
  },
};

/* ---------- Windows (Startup folder or Task Scheduler, hidden launcher) ---------- */
// The launcher is VBScript; it runs one cmd.exe line. Each argument and the log file are double-quoted for cmd.exe,
// where a quote cannot be escaped inside a quoted argument and %VAR% is expanded even there: an argument holding a
// quote, a % or a control character is refused (Windows file names cannot hold a quote anyway). The whole line then
// becomes a VBScript string literal, in which every quote is doubled. Environment values are VBScript strings only.
function vbsText(cmd, env, logFile) {
  const q = (v) => {
    const s = String(v);
    // A line break would end the string literal and start a VBScript statement.
    if (/[\u0000-\u001f\u007f]/.test(s))
      throw new Error(`Windows launcher: ${JSON.stringify(s)} holds a line break or another control character`);
    return `"${s.replace(/"/g, '""')}"`;
  };
  const cmdArg = (a) => {
    const s = String(a);
    const m = s.match(/["%\u0000-\u001f\u007f]/);
    if (m)
      throw new Error(
        `Windows launcher: ${JSON.stringify(s)} holds ${m[0] === '"' ? 'a quote' : m[0] === '%' ? 'a %' : 'a control character'}, which cmd.exe cannot take safely; move it to a folder without one`,
      );
    return `"${s}"`;
  };
  const line = `cmd.exe /c "${cmd.map(cmdArg).join(' ')} >> ${cmdArg(logFile)} 2>&1"`;
  return `' ZAAFPulse launcher: starts the server hidden and restarts it if it exits.
Set sh = CreateObject("WScript.Shell")
Set e = sh.Environment("Process")
${Object.entries(env)
  .map(([k, v]) => `e(${q(k)}) = ${q(v)}`)
  .join('\n')}
Do
  sh.Run ${q(line)}, 0, True
  WScript.Sleep 10000
Loop
`;
}
// Per user: the launcher goes in the Startup folder (no administrator rights needed). --system: a
// Task Scheduler task at boot, as SYSTEM (elevated terminal).
const win = {
  dir: () => path.join(process.env.LOCALAPPDATA || path.join(os.homedir(), 'AppData', 'Local'), 'ZAAFPulse'),
  startup: () =>
    path.join(
      process.env.APPDATA || path.join(os.homedir(), 'AppData', 'Roaming'),
      'Microsoft',
      'Windows',
      'Start Menu',
      'Programs',
      'Startup',
      'ZAAFPulse.vbs',
    ),
  vbs: (system) => (system ? path.join(win.dir(), 'zaafpulse-service.vbs') : win.startup()),
  launch(vbs) {
    const { spawn } = require('child_process');
    spawn('wscript.exe', ['//B', '//NoLogo', vbs], { detached: true, stdio: 'ignore', windowsHide: true }).unref();
  },
  install(cmd, env, log, { system }) {
    const text = vbsText(cmd, env, path.join(win.dir(), 'logs', 'server.log')); // refused before anything is written
    fs.mkdirSync(path.join(win.dir(), 'logs'), { recursive: true });
    win.stop();
    const vbs = win.vbs(system);
    fs.mkdirSync(path.dirname(vbs), { recursive: true });
    fs.writeFileSync(vbs, text);
    if (system) {
      const r = sh('schtasks', [
        '/Create',
        '/TN',
        TASK,
        '/TR',
        `wscript.exe //B //NoLogo "${vbs}"`,
        '/SC',
        'ONSTART',
        '/RU',
        'SYSTEM',
        '/RL',
        'HIGHEST',
        '/F',
      ]);
      if (r.status !== 0)
        throw new Error(`schtasks failed: ${(r.stderr || r.stdout).trim()} (run from an elevated terminal)`);
      sh('schtasks', ['/Run', '/TN', TASK]);
    } else win.launch(vbs);
    log(
      `${system ? `Scheduled task "${TASK}" installed (at boot, as SYSTEM)` : `Startup entry installed: ${vbs}`}.\nLogs: ${path.join(win.dir(), 'logs', 'server.log')}`,
    );
  },
  // Ends the launcher loop and the server it started.
  stop() {
    sh('schtasks', ['/End', '/TN', TASK]);
    sh('powershell.exe', [
      '-NoProfile',
      '-NonInteractive',
      '-Command',
      "Get-CimInstance Win32_Process | Where-Object { $_.ProcessId -ne $PID -and ($_.CommandLine -like '*ZAAFPulse.vbs*' -or $_.CommandLine -like '*zaafpulse-service.vbs*' -or ($_.CommandLine -like '*zaafpulse*' -and $_.CommandLine -like '* start*')) } | ForEach-Object { Invoke-CimMethod -InputObject $_ -MethodName Terminate | Out-Null }",
    ]);
  },
  running() {
    const r = sh('powershell.exe', [
      '-NoProfile',
      '-NonInteractive',
      '-Command',
      "@(Get-CimInstance Win32_Process | Where-Object { $_.CommandLine -like '*zaafpulse*' -and $_.CommandLine -like '* start*' }).Count",
    ]);
    return Number((r.stdout || '').trim()) > 0;
  },
  uninstall(log, { system }) {
    win.stop();
    if (system) sh('schtasks', ['/Delete', '/TN', TASK, '/F']);
    fs.rmSync(win.vbs(system), { force: true });
    log(system ? 'Scheduled task removed.' : 'Startup entry removed.');
  },
  status({ system }) {
    const installed = system ? sh('schtasks', ['/Query', '/TN', TASK]).status === 0 : fs.existsSync(win.startup());
    return !installed ? 'not installed' : win.running() ? 'running' : 'installed, not running';
  },
  restart({ system }) {
    if (!fs.existsSync(win.vbs(system))) return false;
    win.stop();
    if (system) return sh('schtasks', ['/Run', '/TN', TASK]).status === 0;
    win.launch(win.vbs(system));
    return true;
  },
};

const impl = () => ({ macos: mac, linux, windows: win })[platform.OS];

function install({ env = process.env, system = false, log = console.log } = {}) {
  if (system && platform.OS === 'linux' && !isRoot())
    throw new Error('--system needs root: sudo zaafpulse service install --system');
  const s = settings(env);
  if (system && platform.OS === 'linux') s.ZAAFPULSE_DATA ||= '/var/lib/zaafpulse';
  // The service's own data folder (a Linux system install's /var/lib/zaafpulse) is the one its write scope names.
  impl().install(startCommand({ ...env, ...s }), s, log, { system });
}
const uninstall = ({ system = false, log = console.log } = {}) => impl().uninstall(log, { system });
const status = ({ system = false } = {}) => impl().status({ system });
const restart = ({ system = false } = {}) => impl().restart({ system });

module.exports = { install, uninstall, status, restart, startCommand, plist, unitText, vbsText, settings };
