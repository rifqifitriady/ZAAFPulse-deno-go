// Each caller class's egress engine (P5 spec Decision 11): config.json `egress.classes.<class>` asks for direct (the
// default), check or go; a class asked for go runs on go only once its gate is met, and stays on check until then with
// a note saying why (as lib/front/index.js routing() does for the gateway's route classes).
// The gate is the owner's rule (2026-11-12), with the shadow compare's semantics (lib/front/shadow-log.js gate), where
// a "checked" record is a compare that agreed and a "would-refuse" record (or a check that could not be asked) is a
// diff: since the last diff, at least `min` checked calls (egress.gateMinChecks, default 20) and `days` days (egress.
// gateDays, default 1) from the first check after it (or from the first check, when there was no diff). 0 lifts either
// part; both 0 lift the rule.
const { CLASSES } = require('./egress');

const DAY = 86400000;
const SWITCHES = ['direct', 'check', 'go'];

/**
 * The gate's rule from config.json's egress section: a value that is not a count of zero or more is the default.
 * @param {any} egressCfg
 * @returns {{ days: number, min: number }}
 */
function gateRule(egressCfg) {
  const e = egressCfg && typeof egressCfg === 'object' ? egressCfg : {};
  const count = (v, d) => (Number.isFinite(v) && v >= 0 ? v : d);
  return { days: count(e.gateDays, 1), min: count(e.gateMinChecks, 20) };
}

/**
 * Which engine each class runs on, and why one asked for go stays on check.
 * @param {any} egressCfg config.json's egress section
 * @param {{ gate?: (cls: string, rule: { days: number, min: number }) => { ok: boolean, reason?: string } }} [opts]
 * @returns {{ engines: Record<string, string>, notes: Record<string, string> }}
 */
function egressRouting(egressCfg, { gate = () => ({ ok: true }) } = {}) {
  const e = egressCfg && typeof egressCfg === 'object' ? egressCfg : {};
  const asked = e.classes && typeof e.classes === 'object' ? e.classes : {};
  const rule = gateRule(e);
  /** @type {Record<string, string>} */
  const engines = {};
  /** @type {Record<string, string>} */
  const notes = {};
  for (const c of CLASSES) {
    const a = SWITCHES.includes(asked[c]) ? asked[c] : 'direct';
    if (a !== 'go') {
      engines[c] = a;
      continue;
    }
    const g = gate(c, rule) || { ok: false };
    if (g.ok) engines[c] = 'go';
    else {
      engines[c] = 'check';
      notes[c] = g.reason || 'the egress gate is not met';
    }
  }
  return { engines, notes };
}

// The class of a record's purpose ("connectors:dynatrace" → "connectors").
const classOf = (purpose) => String(purpose).split(':')[0];

/**
 * Each class's gate from the egress log's records (any order): "checked" counts as a clean check, "would-refuse" and
 * "check-error" (a check the client could not ask, kept in memory by lib/egress.js) as a diff; other outcomes are not
 * part of the gate.
 * @param {any[]} records
 * @param {{ days?: number, min?: number }} [rule]
 * @param {number} [now]
 * @returns {Record<string, { ok: boolean, reason?: string }>}
 */
function gateFromLog(records, { days = 1, min = 20 } = {}, now = Date.now()) {
  /** @type {Map<string, { first: number | null, lastDiff: number | null, lastDiffWhy: string, sinceDiff: number, checked: number }>} */
  const stats = new Map();
  const list = (Array.isArray(records) ? records : [])
    .filter(
      (r) =>
        r &&
        typeof r.purpose === 'string' &&
        Number.isFinite(r.ts) &&
        (r.outcome === 'checked' || r.outcome === 'would-refuse' || r.outcome === 'check-error'),
    )
    .sort((a, b) => a.ts - b.ts);
  for (const r of list) {
    const c = classOf(r.purpose);
    if (!stats.has(c)) stats.set(c, { first: null, lastDiff: null, lastDiffWhy: '', sinceDiff: 0, checked: 0 });
    const s = stats.get(c);
    if (r.outcome === 'checked') {
      s.checked++;
      s.sinceDiff++;
      if (s.first === null) s.first = r.ts;
    } else {
      s.lastDiff = r.ts;
      s.lastDiffWhy =
        r.outcome === 'would-refuse' ? `would be refused (${r.host || 'a host'})` : 'could not be checked';
      s.sinceDiff = 0; // the wait starts again at the diff
    }
  }
  /** @type {Record<string, { ok: boolean, reason?: string }>} */
  const out = {};
  for (const c of CLASSES) {
    if (days === 0 && min === 0) {
      out[c] = { ok: true }; // the owner lifted the rule
      continue;
    }
    const s = stats.get(c);
    if (s && s.lastDiff !== null && s.sinceDiff === 0) {
      out[c] = { ok: false, reason: `the last ${c} call checked ${s.lastDiffWhy}` };
      continue;
    }
    const n = s ? s.sinceDiff : 0;
    if (n < min) {
      out[c] = { ok: false, reason: `${n} of ${min} ${c} calls checked clean since the last refusal` };
      continue;
    }
    if (days === 0) {
      out[c] = { ok: true };
      continue;
    }
    if (!s || !s.checked) {
      out[c] = { ok: false, reason: `no ${c} call has been checked yet` };
      continue;
    }
    // As the shadow gate: from the last diff (or the first check when there was none), `days` days with no diff.
    const since = s.lastDiff !== null ? s.lastDiff : /** @type {number} */ (s.first);
    const clean = (now - since) / DAY;
    out[c] =
      clean < days
        ? { ok: false, reason: `the ${c} checks are clean for ${clean.toFixed(2)} of ${days} days` }
        : { ok: true };
  }
  return out;
}

module.exports = { egressRouting, gateFromLog, gateRule, DAY };
