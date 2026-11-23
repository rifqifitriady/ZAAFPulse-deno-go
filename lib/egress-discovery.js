// The egress discovery file (P5 spec Decision 15): how the stdio MCP server (mcp.js, a separate process an AI client
// starts) finds the dashboard's egress endpoint. server.js writes <data>/.data/egress.json while its gateway is ready
// ({ pid, port, token, engines, lockdown }: the derived egress token, never the front token; mode 0600, written to a
// temporary file and renamed) and removes it when the gateway is gone or the server stops. mcp.js loads it before each
// tool call (re-read when its mtime changed) and uses it only while the endpoint on its port proves it holds the
// token (P7 review L1): GET /hello?n=<fresh nonce> must answer hex HMAC-SHA256(token, "zaafpulse egress hello <port>
// <nonce>") (gw/egress/hello.go). The token is never sent before the proof passed, so a process that took the port
// after the dashboard died (pid reuse made the old pid check pass) gets neither the token nor the calls; without the
// file, or with a proof that fails, the client goes direct. The pid in the file only says which server may remove it.
const crypto = require('crypto');
const fs = require('fs');
const http = require('http');
const path = require('path');

/**
 * Writes the file (0600, then a rename); unchanged content is not written again (its mtime stays).
 * @param {string} file
 * @param {{ port: number, token: string, engines: Record<string, string>, lockdown: boolean }} st
 * @param {number} [pid]
 * @returns {boolean} whether it was written
 */
function writeDiscovery(file, { port, token, engines, lockdown }, pid = process.pid) {
  const text = JSON.stringify({ pid, port, token, engines, lockdown: lockdown === true }) + '\n';
  try {
    if (fs.readFileSync(file, 'utf8') === text) return false;
  } catch {
    /* none yet */
  }
  fs.mkdirSync(path.dirname(file), { recursive: true, mode: 0o700 });
  const tmp = `${file}.${pid}.tmp`;
  try {
    fs.writeFileSync(tmp, text, { mode: 0o600 });
    fs.renameSync(tmp, file);
  } catch (e) {
    try {
      fs.unlinkSync(tmp);
    } catch {
      /* never written */
    }
    throw e;
  }
  return true;
}

/**
 * Removes the file when this process wrote it (another server's file is left alone).
 * @param {string} file
 * @param {number} [pid]
 */
function removeDiscovery(file, pid = process.pid) {
  try {
    const cur = JSON.parse(fs.readFileSync(file, 'utf8'));
    if (cur && cur.pid !== pid) return false;
  } catch (e) {
    if (e && e.code === 'ENOENT') return false; // nothing to remove; an unreadable file is removed below
  }
  try {
    fs.unlinkSync(file);
    return true;
  } catch {
    return false;
  }
}

const HELLO_LABEL = 'zaafpulse egress hello ';

/** The endpoint's proof for `nonce` (gw/egress/hello.go HelloProof). */
function helloProof(token, port, nonce) {
  return crypto.createHmac('sha256', String(token)).update(`${HELLO_LABEL}${port} ${nonce}`).digest('hex');
}

/**
 * Asks the endpoint on 127.0.0.1:`port` to prove it holds `token`, without sending it: a fresh 64-hex nonce, the
 * answer compared in constant time. Resolves false on any failure (refused, timeout, a wrong or long answer). node:http
 * with its own agent: no proxy from the environment, no pooled connection to whatever held the port before.
 * @param {number} port
 * @param {string} token
 * @param {{ timeoutMs?: number }} [opts]
 * @returns {Promise<boolean>}
 */
function proveEndpoint(port, token, { timeoutMs = 2000 } = {}) {
  const nonce = crypto.randomBytes(32).toString('hex');
  const want = Buffer.from(helloProof(token, port, nonce));
  return new Promise((resolve) => {
    let done = false;
    const finish = (ok) => {
      if (done) return;
      done = true;
      clearTimeout(timer);
      req.destroy();
      resolve(ok);
    };
    const req = http.request(
      {
        host: '127.0.0.1',
        port,
        method: 'GET',
        path: `/hello?n=${nonce}`,
        agent: false,
        headers: { connection: 'close' },
      },
      (res) => {
        const chunks = [];
        let size = 0;
        res.on('data', (c) => {
          size += c.length;
          if (size > 256) return finish(false);
          chunks.push(c);
        });
        res.on('end', () => {
          const got = Buffer.concat(chunks);
          finish(res.statusCode === 200 && got.length === want.length && crypto.timingSafeEqual(got, want));
        });
        res.on('error', () => finish(false));
      },
    );
    const timer = setTimeout(() => finish(false), timeoutMs);
    req.on('error', () => finish(false));
    req.end();
  });
}

/**
 * The mcp.js side: refresh() before each tool call configures the egress client from the file, or back to direct.
 * @param {{ file: string, configure: (opts: any) => void,
 *   prove?: (port: number, token: string) => Promise<boolean>, log?: (line: string) => void }} opts
 */
function createDiscoveryLoader({ file, configure, prove = proveEndpoint, log = () => {} }) {
  let warned = null; // the file stamp the failed-proof line was logged for
  let seen = null; // the mtime, inode and size of the file last read (a rename gives a new inode in the same ms)
  let found = null; // its content, when usable
  let applied; // the configuration last given to the client (JSON), undefined before the first refresh
  const apply = (opts) => {
    const key = JSON.stringify(opts);
    if (key === applied) return;
    applied = key;
    configure(opts);
  };
  return {
    /** Resolves to whether the client now goes through the dashboard's endpoint. */
    async refresh() {
      let st = null;
      try {
        st = fs.statSync(file);
      } catch {
        /* no file: direct */
      }
      if (!st) {
        seen = null;
        found = null;
      } else if (`${st.mtimeMs}/${st.ino}/${st.size}` !== seen) {
        seen = `${st.mtimeMs}/${st.ino}/${st.size}`;
        found = null;
        try {
          const d = JSON.parse(fs.readFileSync(file, 'utf8'));
          const ok =
            d &&
            Number.isSafeInteger(d.pid) &&
            d.pid > 0 &&
            Number.isInteger(d.port) &&
            d.port > 0 &&
            d.port < 65536 &&
            typeof d.token === 'string';
          if (ok && d.token) found = d;
          else log(`egress: ${file} is not a discovery file; going direct`);
        } catch (e) {
          log(`egress: ${file} could not be read (${e.message}); going direct`);
        }
      }
      // Proved before every call: a dashboard that died without removing the file leaves its mtime unchanged, and
      // its port may be taken by anything since.
      let proven = false;
      if (found) {
        try {
          proven = (await prove(found.port, found.token)) === true;
        } catch {
          proven = false;
        }
        if (!proven && warned !== seen) {
          warned = seen;
          log(
            `egress: the endpoint named in ${file} did not prove it is the dashboard's gateway; going direct (if the dashboard is running, \`zaafpulse doctor\` says whether to register the MCP client again)`,
          );
        }
      }
      if (!proven) {
        apply(null);
        return false;
      }
      apply({ port: found.port, token: found.token, engines: found.engines, lockdown: found.lockdown === true });
      return true;
    },
  };
}

// The AI clients whose zaafpulse entry was registered before the endpoint proof (P7 review L1): a deno run of the
// mcp-stdio set whose --allow-run still lists kill or tasklist, which the set no longer needs (the pid check is gone).
// Those clients keep the wider run grant until they are registered again (`zaafpulse register-mcp <client>`); doctor
// names them. read(file) returns the text, null when the file does not exist, or throws (skipped).
/**
 * @param {{ home?: string, env?: Record<string, string>, platform?: string, read?: (file: string) => string | null }} [o]
 * @returns {string[]} client names
 */
function oldRegistrations({ home, env = process.env, platform = process.platform, read } = {}) {
  const { clients, isOurs, NAME, toml } = require('./mcp-clients');
  const { parseConfig } = require('./governance/mcp-scan');
  const readText =
    read ||
    ((f) => {
      try {
        return fs.readFileSync(f, 'utf8');
      } catch (e) {
        if (e && e.code === 'ENOENT') return null;
        throw e;
      }
    });
  const plain = (v) => (v && typeof v === 'object' && v.raw !== undefined ? v.raw : v);
  const stale = (e) => {
    if (!e || typeof e !== 'object' || !Array.isArray(e.args)) return false;
    const args = e.args.map((a) => String(plain(a)));
    if (!isOurs({ command: String(plain(e.command) || ''), args }) || !args.includes('-P=mcp-stdio')) return false;
    const run = args.find((a) => a.startsWith('--allow-run='));
    if (!run) return false;
    return run
      .slice('--allow-run='.length)
      .split(',')
      .some((p) => /^(kill|tasklist)(\.exe)?$/i.test(p.split(/[\\/]/).pop()));
  };
  const out = [];
  for (const c of Object.values(clients(home, /** @type {any} */ ({ env, platform })))) {
    let text;
    try {
      text = readText(c.file);
    } catch {
      continue;
    }
    if (text == null) continue;
    try {
      let entries = [];
      if (c.kind === 'codex') {
        const { value } = toml.find(String(text).replace(/^﻿/, ''), NAME);
        if (value) entries = [value];
      } else {
        const data = parseConfig(text);
        const top = data[c.key || 'mcpServers'];
        if (top && typeof top === 'object') entries = Object.values(top);
        if (c.kind === 'claude' && data.projects && typeof data.projects === 'object')
          for (const p of Object.values(data.projects))
            if (p && p.mcpServers && typeof p.mcpServers === 'object') entries.push(...Object.values(p.mcpServers));
      }
      if (entries.some(stale) && !out.includes(c.name)) out.push(c.name);
    } catch {
      /* unreadable: not named */
    }
  }
  return out;
}

module.exports = {
  writeDiscovery,
  removeDiscovery,
  createDiscoveryLoader,
  proveEndpoint,
  helloProof,
  oldRegistrations,
};
