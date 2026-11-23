// How the final benchmark runner starts programs: never through a shell, only a fixed allow-list of command names or
// this runtime's own executable (checked to be a regular file), every argument as its own array element. No value from
// the environment or a path is ever joined into a command line. findExecutable looks a name up on PATH without
// running anything (it replaces `sh -c "command -v …"`).
const fs = require('fs');
const path = require('path');

// The programs the runner and its preflight start, by name (resolved by the OS on PATH, never through a shell).
const ALLOWED = new Set([
  'ps',
  'powershell',
  'taskkill',
  'pmset',
  'netstat',
  'lsof',
  'ss',
  'df',
  'sysctl',
  'go',
  'git',
  'tar',
  'deno',
  'node',
  'caffeinate',
  'systemd-inhibit',
]);

const isFile = (p) => {
  try {
    return fs.statSync(p).isFile();
  } catch {
    return false;
  }
};

/**
 * The command to spawn, or a throw: an allow-listed name, or exactly this process's own runtime executable (a regular
 * file). Shells (sh, bash, cmd) are never on the list.
 * @param {unknown} cmd
 */
function checkedCommand(cmd) {
  if (typeof cmd === 'string' && ALLOWED.has(cmd)) return cmd;
  if (typeof cmd === 'string' && cmd === process.execPath && path.isAbsolute(cmd) && isFile(cmd)) return cmd;
  throw new Error(`command not allowed: ${JSON.stringify(cmd)} (allowed: this runtime, or ${[...ALLOWED].join(', ')})`);
}

/**
 * The spawn options with the shell off, whatever the caller passed.
 * @param {object} [opts]
 */
const noShell = (opts = {}) => ({ ...opts, shell: false, windowsVerbatimArguments: false });

/**
 * The absolute path of an executable regular file named `name` in a folder of PATH (with PATHEXT on Windows), or null.
 * Only a bare name is looked up.
 * @param {string} name @param {Record<string, string|undefined>} [env]
 */
function findExecutable(name, env = process.env) {
  if (!/^[A-Za-z0-9._-]+$/.test(name) || name.startsWith('.')) return null;
  const win = process.platform === 'win32';
  const exts = win
    ? [
        '',
        ...String(env.PATHEXT || '.EXE;.CMD;.BAT')
          .split(';')
          .filter(Boolean),
      ]
    : [''];
  for (const dir of String(env.PATH || env.Path || '').split(path.delimiter)) {
    if (!dir || !path.isAbsolute(dir)) continue;
    for (const ext of exts) {
      const p = path.join(dir, name + ext.toLowerCase());
      const q = path.join(dir, name + ext);
      for (const f of new Set([p, q])) {
        if (!isFile(f)) continue;
        if (win) return f;
        try {
          fs.accessSync(f, fs.constants.X_OK);
          return f;
        } catch {
          /* not executable */
        }
      }
    }
  }
  return null;
}

module.exports = { ALLOWED, checkedCommand, noShell, findExecutable };
