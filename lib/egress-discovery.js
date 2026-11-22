// The egress discovery file (P5 spec Decision 15): how the stdio MCP server (mcp.js, a separate process an AI client
// starts) finds the dashboard's egress endpoint. server.js writes <data>/.data/egress.json while its gateway is ready
// ({ pid, port, token, engines, lockdown }: the derived egress token, never the front token; mode 0600, written to a
// temporary file and renamed) and removes it when the gateway is gone or the server stops. mcp.js loads it before each
// tool call: re-read when its mtime changed, used only while the pid is alive; without it the client goes direct.
const fs = require('fs');
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

// Whether a pid is alive: kill(pid, 0) (lib/permissions.js: Deno's scoped run grant too). ESRCH is gone; EPERM (another
// account's process) and any other failure count as alive, so a check that cannot be made never drops the endpoint.
function aliveBy(kill) {
  return (pid) => {
    try {
      kill(pid, 0);
      return true;
    } catch (e) {
      return !(e && e.code === 'ESRCH');
    }
  };
}

/**
 * The mcp.js side: refresh() before each tool call configures the egress client from the file, or back to direct.
 * @param {{ file: string, configure: (opts: any) => void, kill?: (pid: number, sig: number) => unknown,
 *   log?: (line: string) => void }} opts
 */
function createDiscoveryLoader({ file, configure, kill = require('./permissions').kill, log = () => {} }) {
  const alive = aliveBy(kill);
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
    /** Returns whether the client now goes through the dashboard's endpoint. */
    refresh() {
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
            d && Number.isSafeInteger(d.pid) && d.pid > 0 && Number.isInteger(d.port) && typeof d.token === 'string';
          if (ok && d.token) found = d;
          else log(`egress: ${file} is not a discovery file; going direct`);
        } catch (e) {
          log(`egress: ${file} could not be read (${e.message}); going direct`);
        }
      }
      // The pid is looked at on every call: a server that died without removing the file leaves its mtime unchanged.
      if (!found || !alive(found.pid)) {
        apply(null);
        return false;
      }
      apply({ port: found.port, token: found.token, engines: found.engines, lockdown: found.lockdown === true });
      return true;
    },
  };
}

module.exports = { writeDiscovery, removeDiscovery, createDiscoveryLoader };
