// What GET /api/egress and the network_egress control show (P5 spec Decisions 10, 18): each class's engine, the
// allow-list's mode and size, the lock-down state and why it is not in force, the newest records and the last 24 h of
// records per class. The log reads are bounded (lib/egress-log.js: at most 2 MB per read) and the answer is kept for
// CACHE_MS, so a page that polls never reads the log more than once per window.
const { CLASSES } = require('./egress');

const DAY_MS = 864e5;
const CACHE_MS = 10000;
const LAST = 50;

// The class of a record's purpose ("connectors:dynatrace" is "connectors").
const classOf = (purpose) => String(purpose).split(':')[0];

/**
 * Per class, the records since `since` by outcome (lib/egress-log.js counts, which is per purpose), and the totals.
 * @param {Record<string, Record<string, number>>} byPurpose
 */
function countsByClass(byPurpose) {
  /** @type {Record<string, Record<string, number>>} */
  const perClass = {};
  let total = 0;
  let refused = 0;
  let wouldRefuse = 0;
  for (const [purpose, c] of Object.entries(byPurpose || {})) {
    const k = classOf(purpose);
    const into = (perClass[k] ||= {});
    for (const [outcome, n] of Object.entries(c)) into[outcome] = (into[outcome] || 0) + n;
    total += c.total || 0;
    refused += c.refused || 0;
    wouldRefuse += c['would-refuse'] || 0;
  }
  return { perClass, total, refused, wouldRefuse };
}

/**
 * The lock-down as this process runs it: lib/permissions.js lockdownState says whether the config asks for it and its
 * conditions hold; it is in force only when this launch's net grant is actually scoped (a Deno launch with the narrowed
 * --allow-net, describe().grants.net === 'scoped'). Node has no grant to narrow; the single-file build keeps its baked
 * grant (spec Decision 16).
 * @param {{ asked: boolean, inForce: boolean, why: string }} wanted
 * @param {{ runtime?: string, singleFile?: boolean, grants?: Record<string, string> }} perms
 */
function lockdownNow(wanted, perms) {
  const asked = !!(wanted && wanted.asked);
  if (!wanted || !wanted.inForce) return { asked, inForce: false, why: (wanted && wanted.why) || 'not asked' };
  const p = perms || {};
  if (p.runtime !== 'deno')
    return { asked, inForce: false, why: 'runs on Node.js: there is no Deno net grant to narrow' };
  if (p.singleFile)
    return {
      asked,
      inForce: false,
      why: "enforced by the egress client; Deno's grant is not narrowed in the single-file build",
    };
  if ((p.grants || {}).net !== 'scoped')
    return {
      asked,
      inForce: false,
      why: 'its conditions are met, but this process was not launched with the narrowed net grant: restart it (a service: install it again)',
    };
  return { asked, inForce: true, why: '' };
}

/**
 * The /api/egress reader, cached for CACHE_MS.
 * @param {{ log: { tail(n: number): any[], counts(since: number): Record<string, Record<string, number>> },
 *   state: () => { mode: string, port: number | null, engines: Record<string, string>, notes: Record<string, string>,
 *     allow: { host: string, port?: number }[], allowList: string, lockdown: { asked: boolean, inForce: boolean,
 *     why: string } }, now?: () => number }} deps
 */
function createEgressStatus({ log, state, now = Date.now }) {
  let memo = null;
  return function read() {
    const t = now();
    if (memo && t - memo.at < CACHE_MS) return memo.value;
    const s = state();
    const allow = Array.isArray(s.allow) ? s.allow : [];
    const value = {
      mode: s.mode,
      port: s.port ?? null,
      engines: s.engines,
      notes: s.notes,
      allowList: s.allowList === 'enforce' ? 'enforce' : 'report',
      allowCount: allow.length,
      allow,
      lockdown: s.lockdown,
      last: log.tail(LAST),
      counts24h: countsByClass(log.counts(t - DAY_MS)),
    };
    memo = { at: t, value };
    return value;
  };
}

module.exports = { createEgressStatus, countsByClass, lockdownNow, classOf, CLASSES, CACHE_MS, LAST };
