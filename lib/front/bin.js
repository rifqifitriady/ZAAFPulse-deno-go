// Where the gateway binary (zaafpulse-gw) is (P4a D7): ZAAFPULSE_GW_BIN when set; next to the executable in a
// single-file build (P6 ships it there); else gw/bin/ in the source folder (npm run gw:build).
const fs = require('fs');
const path = require('path');

// Not lib/platform.js: requiring it from here pulls its whole module graph into `deno task typecheck` (lib/front is
// one of its roots), which then fails on files outside the checked folders. Same rules as platform.isSingleFile()
// and platform.APP_DIR.
const APP_DIR = path.join(__dirname, '..', '..');
function isSingleFile() {
  const deno = /** @type {any} */ (globalThis).Deno;
  if (deno) return !!(deno.build && deno.build.standalone);
  try {
    return require('node:sea').isSea();
  } catch {
    return false;
  }
}

/**
 * @param {{ env?: Record<string, string | undefined>, singleFile?: boolean, execPath?: string, appDir?: string,
 *   os?: string }} [opts]
 * @returns {string} an absolute path (it may not exist: the caller says so)
 */
function gwBinary({
  env = process.env,
  singleFile = isSingleFile(),
  execPath = process.execPath,
  appDir = APP_DIR,
  os = process.platform,
} = {}) {
  if (env.ZAAFPULSE_GW_BIN) return path.resolve(env.ZAAFPULSE_GW_BIN);
  const name = os === 'win32' ? 'zaafpulse-gw.exe' : 'zaafpulse-gw';
  return singleFile ? path.join(path.dirname(execPath), name) : path.join(appDir, 'gw', 'bin', name);
}

/** The binary's path when the file is there, else null (a deno run launch grants it then: lib/permissions.js). */
function existingGwBinary(opts) {
  const p = gwBinary(opts);
  return fs.existsSync(p) ? p : null;
}

module.exports = { gwBinary, existingGwBinary };
