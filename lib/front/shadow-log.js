// The shadow compare's log and the rule that lets a route class switch to the go engine (P4b). The Go gateway posts one
// line per shadowed request to the internal port (POST /_zp/shadow, lib/front/internal.js `own`); this appends it to
// .data/gateway-shadow.jsonl and keeps, per route class, the counts and the time of the last diff. A line holds no
// prompt or answer text: a body that differs is named by its length and a hash prefix and the first byte that differs.
//   { ts, class, outcome: 'same' | 'diff' | 'handoff' | 'skipped', reason?, status?, diffs?: [{ field, js, go }] }
// handoff: the engine would have handed the request to js; skipped: nothing to compare (the provider call failed, or
// the control plane's own rate limit answered: the windows are per engine). Both count as not compared.
// The switch rule (spec stream A item 3; owner answer 2026-11-12): a class may run on go only when the shadow has, since
// its last diff, compared at least `min` of its calls (config.json gateway.shadowMinCompares, default 20) and seen no
// diff for `days` days (gateway.shadowDays, default 1). 0 lifts either part; both 0 lift the rule (tests, or an owner
// who decides without it).
const { jsonlLog } = require('../gateway/store');
const { takeCapture } = require('../gateway/shadow');

const DAY = 86400000;
const OUTCOMES = new Set(['same', 'diff', 'handoff', 'skipped']);
const MAX_LINE = 64 * 1024;

/** @param {{ file: string, now?: () => number, maxBytes?: number }} opts */
function createShadowLog({ file, now = Date.now, maxBytes }) {
  const log = jsonlLog(file, maxBytes);
  /** @type {Map<string, { compared: number, diffs: number, handoffs: number, first: number | null, lastDiff: number | null, last: number | null, sinceDiff: number }>} */
  const stats = new Map();
  const statOf = (c) => {
    if (!stats.has(c))
      stats.set(c, { compared: 0, diffs: 0, handoffs: 0, first: null, lastDiff: null, last: null, sinceDiff: 0 });
    return stats.get(c);
  };
  const count = (e) => {
    if (!e || typeof e.class !== 'string' || !OUTCOMES.has(e.outcome) || !Number.isFinite(e.ts)) return false;
    const s = statOf(e.class);
    if (e.outcome === 'handoff' || e.outcome === 'skipped') s.handoffs++;
    else {
      s.compared++;
      if (s.first === null) s.first = e.ts;
      s.last = e.ts;
      if (e.outcome === 'diff') {
        s.diffs++;
        s.lastDiff = e.ts;
        s.sinceDiff = 0;
      } else s.sinceDiff++;
    }
    return true;
  };
  for (const e of log.read()) count(e);
  return {
    /** Appends a line from the gateway (a line that is not one is refused). */
    append(e) {
      const line = { ...e, ts: now() };
      if (!count(line)) return false;
      log.append(line);
      return true;
    },
    /** Per class: compares, diffs, hand-offs, and since when there was no diff (the first compare after the last diff). */
    stats() {
      const out = {};
      for (const [c, s] of stats)
        out[c] = {
          compared: s.compared,
          diffs: s.diffs,
          handoffs: s.handoffs,
          cleanSince: s.lastDiff ?? s.first,
          lastDiffAt: s.lastDiff,
        };
      return out;
    },
    /**
     * Whether class c may run on go: `min` compares and `days` days without a diff since the last one; reason says what
     * is missing.
     * @param {string} c
     * @param {{ days?: number, min?: number }} [rule]
     * @returns {{ ok: boolean, reason?: string }}
     */
    gate(c, { days = 1, min = 20 } = {}) {
      if (days === 0 && min === 0) return { ok: true }; // the owner lifted the rule
      const s = stats.get(c);
      if (s && s.lastDiff !== null && s.sinceDiff === 0) return { ok: false, reason: `the last ${c} compare differed` };
      const n = s ? s.sinceDiff : 0;
      if (n < min) return { ok: false, reason: `${n} of ${min} ${c} calls compared without a diff since the last one` };
      if (days === 0) return { ok: true };
      if (!s || !s.compared) return { ok: false, reason: `the shadow has compared no ${c} call yet` };
      // A diff restarts the wait: from the diff on, the class needs `days` days with compares and no diff.
      const since = s.lastDiff !== null ? s.lastDiff : s.first;
      const clean = (now() - since) / DAY;
      if (clean < days)
        return { ok: false, reason: `the ${c} shadow is clean for ${clean.toFixed(2)} of ${days} days` };
      return { ok: true };
    },
  };
}

/**
 * The internal port's answers to the gateway's own requests (lib/front/internal.js `own`):
 *   GET  /_zp/shadow/<id>  the capture of a shadowed request, once (404 when there is none);
 *   POST /_zp/shadow       one compare line, appended (204; 400 when it is not one).
 * @param {{ shadowLog: ReturnType<typeof createShadowLog> }} deps
 */
function shadowEndpoints({ shadowLog }) {
  return (req, res) => {
    const m = /^\/_zp\/shadow\/([0-9a-f]{16,64})$/.exec(req.url);
    if (req.method === 'GET' && m) {
      const c = takeCapture(m[1]);
      if (!c) return res.writeHead(404).end();
      res.writeHead(200, { 'content-type': 'application/json' });
      return res.end(JSON.stringify(c));
    }
    if (req.method === 'POST' && req.url === '/_zp/shadow') {
      let size = 0;
      const chunks = [];
      req.on('data', (c) => {
        size += c.length;
        if (size <= MAX_LINE) chunks.push(c);
      });
      req.on('end', () => {
        let e = null;
        try {
          e = size <= MAX_LINE ? JSON.parse(Buffer.concat(chunks).toString('utf8')) : null;
        } catch {
          e = null;
        }
        res.writeHead(e && typeof e === 'object' && shadowLog.append(e) ? 204 : 400).end();
      });
      return;
    }
    res.writeHead(404).end();
  };
}

module.exports = { createShadowLog, shadowEndpoints, DAY };
