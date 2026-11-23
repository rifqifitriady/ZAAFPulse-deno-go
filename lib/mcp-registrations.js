// ZAAFPulse's entries in the AI clients' MCP configs, as they are on disk (P7): which clients hold one, and which of
// those start a program or a script that no longer exists. An entry a v1.x install registered starts
// <prefix>/node/bin/node <prefix>/app/mcp.js, which the v2 installers move into <prefix>/rollback/; a rollback moves
// a v2 entry's <prefix>/bin/zaafpulse into rollforward/ the same way. `zaafpulse doctor` names such clients, and
// `zaafpulse register-mcp registered` (scripts/register-mcp.js) writes every client that holds an entry again.
const fs = require('fs');
const path = require('path');
const { clients, isOurs, NAME, OLD_NAMES, toml } = require('./mcp-clients');

const isObj = (v) => !!v && typeof v === 'object' && !Array.isArray(v);
// A TOML bare value is kept as { raw } (lib/mcp-clients.js tomlBody).
const plain = (v) => (isObj(v) && v.raw !== undefined ? v.raw : v);
const readText = (f) => {
  try {
    return fs.readFileSync(f, 'utf8');
  } catch (e) {
    if (e && e.code === 'ENOENT') return null;
    throw e;
  }
};

/**
 * Every entry of ours (under NAME, or an old name that is ours) in each client's config: { id, client, entry, scope }.
 * scope is "user" for the entry `register-mcp` writes and "project" for one in a Claude Code project. A config that
 * cannot be read or parsed adds nothing.
 * @param {{ home?: string, env?: Record<string, string | undefined>, platform?: string,
 *   read?: (file: string) => string | null }} [opts]
 */
function ourEntries({ home, env = process.env, platform = process.platform, read = readText } = {}) {
  const { parseConfig } = require('./governance/mcp-scan');
  const out = [];
  const add = (id, c, servers, scope) => {
    if (!isObj(servers)) return;
    for (const n of [NAME, ...OLD_NAMES]) {
      const e = servers[n];
      if (isObj(e) && (n === NAME || isOurs(normal(e)))) out.push({ id, client: c.name, entry: normal(e), scope });
    }
  };
  for (const [id, c] of Object.entries(clients(home, /** @type {any} */ ({ env, platform })))) {
    let text;
    try {
      text = read(c.file);
    } catch {
      continue;
    }
    if (text == null) continue;
    try {
      if (c.kind === 'codex') {
        const t = String(text).replace(/^﻿/, '');
        const servers = {};
        for (const n of [NAME, ...OLD_NAMES]) {
          const { value } = toml.find(t, n);
          if (value) servers[n] = value;
        }
        add(id, c, servers, 'user');
      } else {
        const data = parseConfig(text);
        add(id, c, data[c.key || 'mcpServers'], 'user');
        if (c.kind === 'claude' && isObj(data.projects))
          for (const p of Object.values(data.projects)) if (isObj(p)) add(id, c, p.mcpServers, 'project');
      }
    } catch {
      /* unreadable: not named */
    }
  }
  return out;
}

// An entry with plain strings: command, args and env as the client reads them.
function normal(e) {
  const args = Array.isArray(e.args) ? e.args.map((a) => String(plain(a))) : [];
  const env = isObj(e.env) ? Object.fromEntries(Object.entries(e.env).map(([k, v]) => [k, String(plain(v))])) : {};
  return { ...e, command: String(plain(e.command) || ''), args, env };
}

/**
 * The paths an entry starts that do not exist: its command when that is an absolute path (a bare name is looked up
 * on the client's PATH, which is not known here), and an absolute mcp.js among its arguments.
 * @param {{ command: string, args: string[] }} entry
 * @param {(p: string) => boolean} [exists]
 */
function missingPaths(entry, exists = (p) => fs.existsSync(p)) {
  const abs = (p) => path.isAbsolute(p) || path.win32.isAbsolute(p);
  const paths = [entry.command, ...entry.args.filter((a) => /(^|[\\/])mcp\.js$/.test(a))].filter((p) => p && abs(p));
  return [...new Set(paths)].filter((p) => !exists(p));
}

/**
 * The clients whose ZAAFPulse entry starts a program or script that no longer exists: [{ client, scope, paths }], one
 * per client and scope (its entries' missing paths together). `register-mcp registered` rewrites the "user" ones; a
 * "project" one (a Claude Code project's own entry) is the project's to fix.
 * @param {{ home?: string, env?: Record<string, string | undefined>, platform?: string,
 *   read?: (file: string) => string | null, exists?: (p: string) => boolean }} [opts]
 */
function brokenRegistrations({ exists, ...opts } = {}) {
  /** @type {Map<string, { client: string, scope: string, paths: Set<string> }>} */
  const by = new Map();
  for (const { client, entry, scope } of ourEntries(opts)) {
    const gone = missingPaths(entry, exists);
    if (!gone.length) continue;
    const k = `${client}\n${scope}`;
    if (!by.has(k)) by.set(k, { client, scope, paths: new Set() });
    for (const p of gone) by.get(k).paths.add(p);
  }
  return [...by.values()].map((b) => ({ client: b.client, scope: b.scope, paths: [...b.paths] }));
}

module.exports = { ourEntries, missingPaths, brokenRegistrations };
