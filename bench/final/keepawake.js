// Keeps the machine awake while the final benchmarks run (scripts/final-bench.js), as a child process that also ends
// on its own when the runner's process is gone, so it can never be left running:
//   macOS    caffeinate -dims -w <pid>
//   Linux    systemd-inhibit --what=idle:sleep … tail --pid=<pid> -f /dev/null
//   Windows  PowerShell holding SetThreadExecutionState(ES_CONTINUOUS | ES_SYSTEM_REQUIRED | ES_DISPLAY_REQUIRED)
//            until Wait-Process -Id <pid> returns
const childProcess = require('child_process');
const { checkedCommand, noShell } = require('./spawnsafe');

// By allow-listed name, without a shell (bench/final/spawnsafe.js).
const spawn = (cmd, args, opts) => childProcess.spawn(checkedCommand(cmd), [...args], noShell(opts));

/** The command for a platform, or null. @param {string} platform @param {number} pid */
function keepAwakeCommand(platform, pid) {
  if (platform === 'darwin') return { name: 'caffeinate', cmd: 'caffeinate', args: ['-dims', '-w', String(pid)] };
  if (platform === 'linux')
    return {
      name: 'systemd-inhibit',
      cmd: 'systemd-inhibit',
      args: [
        '--what=idle:sleep',
        '--who=zaafpulse-final-bench',
        '--why=final benchmarks running',
        '--mode=block',
        'tail',
        `--pid=${pid}`,
        '-f',
        '/dev/null',
      ],
    };
  if (platform === 'win32') {
    const ps =
      "$t = Add-Type -MemberDefinition '[DllImport(\"kernel32.dll\")] public static extern uint SetThreadExecutionState(uint f);' " +
      `-Name Awake -Namespace ZaafPulse -PassThru; $null = $t::SetThreadExecutionState([uint32]2147483651); Wait-Process -Id ${pid}`;
    return { name: 'SetThreadExecutionState', cmd: 'powershell', args: ['-NoProfile', '-NonInteractive', '-Command', ps] };
  }
  return null;
}

/**
 * Starts the keep-awake child; { name, pid, stop() } or null when there is none (or it failed to start).
 * @param {{ platform?: string, pid?: number, spawnFn?: Function }} [o]
 */
function startKeepAwake({ platform = process.platform, pid = process.pid, spawnFn = spawn } = {}) {
  const c = keepAwakeCommand(platform, pid);
  if (!c) return null;
  let child;
  try {
    child = spawnFn(c.cmd, c.args, { stdio: 'ignore' });
  } catch {
    return null;
  }
  let exited = false;
  child.on('error', () => {
    exited = true;
  });
  child.on('exit', () => {
    exited = true;
  });
  return {
    name: c.name,
    pid: child.pid,
    running: () => !exited,
    stop: () => {
      if (!exited) {
        try {
          child.kill();
        } catch {
          /* gone */
        }
      }
    },
  };
}

module.exports = { keepAwakeCommand, startKeepAwake };
