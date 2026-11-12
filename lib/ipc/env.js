// The environment the gateway process gets (P4a spec E7): the data folder, PATH, the home folder and, on Windows, what
// the OS needs (SystemRoot, TEMP, TMP). Nothing else: provider keys in environment variables reach the gateway only
// through secrets.handoff.

/**
 * @param {Record<string, string | undefined>} env the control plane's environment
 * @param {string} dataDir the data folder (lib/platform.js dataDir())
 * @param {string} [platform]
 * @returns {Record<string, string>}
 */
function gwEnv(env, dataDir, platform = process.platform) {
  const win = platform === 'win32';
  // Windows names are case-insensitive (Path, SYSTEMROOT): look them up that way, pass them under one spelling.
  const get = (name) => {
    if (env[name] !== undefined) return env[name];
    if (!win) return undefined;
    const k = Object.keys(env).find((x) => x.toUpperCase() === name.toUpperCase());
    return k === undefined ? undefined : env[k];
  };
  const out = { ZAAFPULSE_DATA: dataDir };
  for (const name of ['PATH', 'HOME', 'USERPROFILE', ...(win ? ['SystemRoot', 'TEMP', 'TMP'] : [])]) {
    const v = get(name);
    if (typeof v === 'string' && v !== '') out[name] = v;
  }
  return out;
}

module.exports = { gwEnv };
