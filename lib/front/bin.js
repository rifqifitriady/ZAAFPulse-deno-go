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
  return !!(deno && deno.build && deno.build.standalone);
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

/**
 * Whether this process may start the program at `p` (P6 Decision 4). Always on Node. On Deno, the run grant decides:
 * the compiled binary names zaafpulse-gw in its run list, which Deno resolves through PATH at start, so the binary's
 * folder must have been first on PATH then; a `deno run` launch is granted the absolute path (permissions.runScope).
 * @param {string} p
 * @param {{ deno?: any }} [opts] deno: null stands for Node (no run grant to ask)
 */
function gwAllowed(p, { deno = /** @type {any} */ (globalThis).Deno } = {}) {
  if (!deno) return true;
  try {
    return deno.permissions.querySync({ name: 'run', command: p }).state === 'granted';
  } catch {
    return false;
  }
}

/**
 * The gateway binary this process would start: its path, whether the file is there and whether it may run.
 * @param {{ path?: string, deno?: any, exists?: (p: string) => boolean, env?: Record<string, string | undefined>,
 *   singleFile?: boolean, execPath?: string, appDir?: string, os?: string }} [opts]
 * @returns {{ path: string, found: boolean, allowed: boolean }}
 */
function gwStatus(opts = {}) {
  const { path: given, deno, exists = fs.existsSync, ...where } = opts;
  const p = given || gwBinary(where);
  const found = exists(p);
  return { path: p, found, allowed: found && gwAllowed(p, { deno }) };
}

/**
 * Why the gateway cannot start (P6 Decision 4), or null when it can: the front falls back at once with this reason,
 * and `zaafpulse doctor` names the same check. A `deno run` launch is granted the gateway's absolute path only when
 * the file was there at the launch (permissions.runScope), so there a refusal means it was built afterwards.
 * @param {{ path: string, found: boolean, allowed: boolean }} s
 * @param {{ singleFile?: boolean }} [opts]
 */
function gwRefusal(s, { singleFile = isSingleFile() } = {}) {
  if (!s.found) return `zaafpulse-gw not found at ${s.path}`;
  if (s.allowed) return null;
  return singleFile
    ? "zaafpulse-gw is next to zaafpulse but not allowed to run: put its folder first on PATH (the installer's zaafpulse command does)"
    : 'zaafpulse-gw is not allowed to run: it was built after ZAAFPulse started; restart ZAAFPulse so its path is granted';
}

module.exports = { gwBinary, existingGwBinary, gwAllowed, gwStatus, gwRefusal };
