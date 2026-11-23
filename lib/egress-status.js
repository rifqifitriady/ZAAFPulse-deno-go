// What GET /api/egress and the network_egress control show (P5 spec Decisions 10, 18): each class's engine, the
// allow-list's mode and size, the lock-down state and why it is not in force, the newest records and the last 24 h of
// records per class. The log reads are bounded (lib/egress-log.js: at most 2 MB per read) and the answer is kept for
// CACHE_MS, so a page that polls never reads the log more than once per window.
const { CLASSES, allowListMode } = require('./egress');

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
 * @param {{ runtime?: string, singleFile?: boolean, grants?: Record<string, string>, outsideNet?: boolean }} perms
 *   outsideNet: whether an outside sentinel (example.com:443) is granted
 */
function lockdownNow(wanted, perms) {
  const r = lockdownBody(wanted, perms);
  // The classes lockdownState counted clear because nothing is configured for them (P7 defaults design, Decision B).
  const quiet = wanted && Array.isArray(wanted.quiet) ? wanted.quiet : [];
  return quiet.length ? { ...r, quiet } : r;
}
function lockdownBody(wanted, perms) {
  const asked = !!(wanted && wanted.asked);
  const p = perms || {};
  const narrowed = p.runtime === 'deno' && !p.singleFile && (p.grants || {}).net === 'scoped';
  if (!wanted || !wanted.inForce) {
    const why = (wanted && wanted.why) || 'not asked';
    // The fallback was taken (lock-down off, js mode) but this process still runs under a narrowed grant (a service
    // line computed at install): every outbound call fails until it is launched again.
    return narrowed
      ? {
          asked,
          inForce: false,
          why: `${why}; but this launch's net grant is still narrowed, so outbound calls fail while no gateway is ready: restart it (a service: run \`zaafpulse service install\` again)`,
        }
      : { asked, inForce: false, why };
  }
  if (p.runtime !== 'deno')
    return { asked, inForce: false, why: 'runs on Node.js: there is no Deno net grant to narrow' };
  if (p.singleFile)
    return {
      asked,
      inForce: false,
      why: "enforced by the egress client; Deno's grant is not narrowed in the single-file build",
    };
  // "scoped" is any partial grant; the lock-down's is loopback only (an outside host must not be granted).
  if ((p.grants || {}).net === 'scoped' && p.outsideNet === true)
    return {
      asked,
      inForce: false,
      why: "this launch's net grant is scoped but not to loopback only: an outside host is granted",
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
 * Whether the egress client must never send a non-loopback call direct (P5 Decisions 14, 16): always under a Deno
 * launch whose net grant is scoped (the locked-down line: a direct call would be denied anyway, so it fails loudly with
 * EGRESS_UNAVAILABLE while no gateway is ready); in the single-file build, whose grant is baked, while the lock-down is
 * in force by config (the client's lock alone); never on Node or an unscoped `deno run`.
 * @param {{ deno: boolean, scoped: boolean, singleFile: boolean }} grant
 * @param {() => boolean} inForce lib/permissions.js lockdownState(...).inForce, asked only for the single-file build
 */
function clientLock(grant, inForce) {
  if (!grant.deno) return false;
  if (grant.scoped) return true;
  return grant.singleFile ? !!inForce() : false;
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
      allowList: allowListMode({ allowList: s.allowList }), // config.json's value: report, else enforce (the default)
      allowCount: allow.length,
      allow,
      lockdown: s.lockdown,
      last: log.tail(LAST),
      counts24h: (() => {
        const raw = log.counts(t - DAY_MS);
        return { ...countsByClass(raw), ...(raw && raw.truncated ? { truncated: true } : {}) };
      })(),
    };
    memo = { at: t, value };
    return value;
  };
}

module.exports = {
  clientLock,
  createEgressStatus,
  countsByClass,
  lockdownNow,
  classOf,
  CLASSES,
  CACHE_MS,
  LAST,
};
