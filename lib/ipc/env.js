// The environment the gateway process gets (P4a spec E7): the data folder, PATH, the home folder and, on Windows, what
// the OS needs (SystemRoot, TEMP, TMP). Nothing else, except the network settings a provider call needs (P4b entry
// condition 5): the proxy (HTTPS_PROXY, HTTP_PROXY, NO_PROXY, also lower case, which Go's ProxyFromEnvironment and
// Deno's fetch read) and the extra CA certificates (SSL_CERT_FILE, SSL_CERT_DIR, and the ones Node and Deno add to
// their own store: NODE_EXTRA_CA_CERTS, DENO_CERT). Provider keys in environment variables reach the gateway only
// through secrets.handoff.

// The network settings passed on as they are (a proxy URL may hold a user and password: never logged).
const NETWORK = [
  'HTTPS_PROXY',
  'https_proxy',
  'HTTP_PROXY',
  'http_proxy',
  'NO_PROXY',
  'no_proxy',
  'SSL_CERT_FILE',
  'SSL_CERT_DIR',
  'NODE_EXTRA_CA_CERTS',
  'DENO_CERT',
];

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
  const names = ['PATH', 'HOME', 'USERPROFILE', ...(win ? ['SystemRoot', 'TEMP', 'TMP'] : [])];
  // On Windows https_proxy and HTTPS_PROXY are one variable: passed once, upper case.
  for (const n of NETWORK) if (!win || n === n.toUpperCase()) names.push(n);
  for (const name of names) {
    const v = get(name);
    if (typeof v === 'string' && v !== '') out[name] = v;
  }
  return out;
}

module.exports = { gwEnv, NETWORK };
