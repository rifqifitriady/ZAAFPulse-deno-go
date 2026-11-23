// Each caller class's egress engine (P5 spec Decision 11): config.json `egress.classes.<class>` asks for direct (the
// default), check or go; a class asked for go runs on go only once its gate is met, and stays on check until then with
// a note saying why (as lib/front/index.js routing() does for the gateway's route classes).
// The gate is the owner's rule (2026-11-12) with the shadow compare's semantics (lib/front/shadow-log.js gate). Its
// evidence is only the /check verdicts (records with via "check": "checked" is a compare that agreed, "would-refuse" a
// diff) and the checks the client could not ask ("check-error", a diff). Met means: since the last diff, at least `min`
// checked calls (egress.gateMinChecks, default 20) and `days` days (egress.gateDays, default 1) counted from that diff,
// or from the class's first check when there was none. 0 lifts either part; both 0 lift the rule.
// The gate is sticky (createEgressGate): once met, a class stays on go until a new diff of its own or until the owner
// sets it to direct or check, kept in .data/egress-gate.json across restarts.
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
 * @param {{ gate?: (cls: string, rule: { days: number, min: number }) => { ok: boolean, reason?: string },
 *   forget?: (cls: string) => void }} [opts] forget: told each class not asked for go (its sticky pass ends)
 * @returns {{ engines: Record<string, string>, notes: Record<string, string> }}
 */
function egressRouting(egressCfg, { gate = () => ({ ok: true }), forget = () => {} } = {}) {
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
      forget(c);
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
 * Whether a record is gate evidence (P5 review I2): a /check verdict (via "check", outcome "checked" or
 * "would-refuse"), or a check the client could not ask ("check-error", lib/egress.js). A call the engine made (via
 * "engine") or a forwarded call's would-refuse (via "proxy") never touches the gate.
 * @param {any} r
 */
function isEvidence(r) {
  if (!r || typeof r.purpose !== 'string' || !Number.isFinite(r.ts)) return false;
  if (r.outcome === 'check-error') return true;
  return r.via === 'check' && (r.outcome === 'checked' || r.outcome === 'would-refuse');
}

/**
 * Cumulative per-class gate statistics, fed one evidence record at a time in time order (as lib/front/shadow-log.js
 * keeps its compare stats): no read of the log after the first.
 */
function createGateStats() {
  /** @type {Map<string, { first: number | null, lastDiff: number | null, lastDiffWhy: string, sinceDiff: number, checked: number }>} */
  const stats = new Map();
  return {
    /** Takes one record; anything that is not gate evidence is ignored. Returns whether it was a diff. */
    note(r) {
      if (!isEvidence(r)) return false;
      const c = classOf(r.purpose);
      if (!stats.has(c)) stats.set(c, { first: null, lastDiff: null, lastDiffWhy: '', sinceDiff: 0, checked: 0 });
      const s = stats.get(c);
      if (r.outcome === 'checked') {
        s.checked++;
        s.sinceDiff++;
        if (s.first === null) s.first = r.ts;
        return false;
      }
      s.lastDiff = r.ts;
      s.lastDiffWhy =
        r.outcome === 'would-refuse' ? `would be refused (${r.host || 'a host'})` : 'could not be checked';
      s.sinceDiff = 0; // the wait starts again at the diff
      return true;
    },
    /** The last diff of a class (ms), or null. */
    lastDiff(c) {
      const s = stats.get(c);
      return s ? s.lastDiff : null;
    },
    /**
     * Whether class c meets the rule now; `since` is when its clean run started (its last diff, or its first check).
     * @param {string} c
     * @param {{ days?: number, min?: number }} [rule]
     * @param {number} [now]
     * @returns {{ ok: boolean, reason?: string, since?: number | null }}
     */
    gate(c, { days = 1, min = 20 } = {}, now = Date.now()) {
      if (days === 0 && min === 0) return { ok: true }; // the owner lifted the rule
      const s = stats.get(c);
      if (s && s.lastDiff !== null && s.sinceDiff === 0)
        return { ok: false, reason: `the last ${c} call checked ${s.lastDiffWhy}` };
      const n = s ? s.sinceDiff : 0;
      if (n < min) return { ok: false, reason: `${n} of ${min} ${c} calls checked clean since the last refusal` };
      const since = s ? (s.lastDiff !== null ? s.lastDiff : s.first) : null;
      if (days === 0) return { ok: true, since };
      if (!s || !s.checked) return { ok: false, reason: `no ${c} call has been checked yet` };
      // As the shadow gate: from the last diff (or the first check when there was none), `days` days with no diff.
      const clean = (now - /** @type {number} */ (since)) / DAY;
      return clean < days
        ? { ok: false, reason: `the ${c} checks are clean for ${clean.toFixed(2)} of ${days} days` }
        : { ok: true, since };
    },
  };
}

/**
 * Each class's gate from egress records (any order): see isEvidence and createGateStats.
 * @param {any[]} records
 * @param {{ days?: number, min?: number }} [rule]
 * @param {number} [now]
 * @returns {Record<string, { ok: boolean, reason?: string }>}
 */
function gateFromLog(records, rule = {}, now = Date.now()) {
  const st = createGateStats();
  const list = (Array.isArray(records) ? records : []).filter(isEvidence).sort((a, b) => a.ts - b.ts);
  for (const r of list) st.note(r);
  /** @type {Record<string, { ok: boolean, reason?: string }>} */
  const out = {};
  for (const c of CLASSES) {
    const { ok, reason } = st.gate(c, rule, now);
    out[c] = ok ? { ok } : { ok, reason };
  }
  return out;
}

/**
 * The sticky egress gate (P5 review I1). A class that met the gate stays passed, in `file` (.data/egress-gate.json,
 * 0600, written with a rename: { <class>: { passedAt, since, diffAt? } }), until a new diff of that class arrives (a
 * /check would-refuse or a check error) or the owner sets it to direct or check (forget). The evidence is read once at
 * start (`evidence()`: the log's gate records, both files, filtered before any cap) and then fed record by record
 * (note) as the IPC ingest stores them, so neither a flood of other records, a rotation nor a restart moves a class
 * back. `diffAt` keeps a check error (not in the log) across a restart.
 * @param {{ file: string, evidence?: () => Iterable<any>, now?: () => number, log?: (line: string) => void }} opts
 */
function createEgressGate({ file, evidence = () => [], now = Date.now, log = (l) => console.error(l) }) {
  const fs = require('fs');
  const path = require('path');
  const st = createGateStats();
  /** @type {Record<string, { passedAt: number | null, since: number | null, diffAt?: number }>} */
  let saved = {};
  try {
    const raw = JSON.parse(fs.readFileSync(file, 'utf8'));
    if (raw && typeof raw === 'object' && !Array.isArray(raw))
      for (const c of CLASSES) {
        const e = raw[c];
        if (!e || typeof e !== 'object') continue;
        const num = (v) => (Number.isFinite(v) ? v : null);
        saved[c] = { passedAt: num(e.passedAt), since: num(e.since) };
        if (Number.isFinite(e.diffAt)) saved[c].diffAt = e.diffAt;
      }
  } catch {
    /* none yet, or unreadable: nothing passed */
  }
  const recs = [];
  for (const r of evidence()) if (isEvidence(r)) recs.push(r);
  for (const [c, e] of Object.entries(saved))
    if (e.diffAt !== undefined) recs.push({ ts: e.diffAt, purpose: c, outcome: 'check-error' });
  recs.sort((a, b) => a.ts - b.ts);
  for (const r of recs) st.note(r);
  let dirty = false;
  // A pass older than a diff the evidence holds is no pass.
  for (const [c, e] of Object.entries(saved)) {
    const d = st.lastDiff(c);
    if (e.passedAt !== null && d !== null && d >= e.passedAt) {
      e.passedAt = null;
      dirty = true;
    }
  }
  const save = () => {
    const tmp = `${file}.${process.pid}.tmp`;
    try {
      fs.mkdirSync(path.dirname(file), { recursive: true, mode: 0o700 });
      fs.writeFileSync(tmp, JSON.stringify(saved) + '\n', { mode: 0o600 });
      fs.renameSync(tmp, file);
    } catch (e) {
      try {
        fs.unlinkSync(tmp);
      } catch {
        /* never written */
      }
      log(`egress: the gate state could not be saved to ${file}: ${e.message}`);
    }
  };
  if (dirty) save();
  return {
    file,
    /** One record the ingest stored (or a client check error): a diff takes the class's pass away. */
    note(r) {
      if (!st.note(r)) return;
      const c = classOf(r.purpose);
      if (!CLASSES.includes(c)) return;
      const e = saved[c] || { passedAt: null, since: null };
      e.passedAt = null;
      e.since = r.ts;
      if (r.outcome === 'check-error') e.diffAt = r.ts;
      else delete e.diffAt;
      saved[c] = e;
      save();
    },
    /**
     * Whether class c may run on go: passed before (sticky), or meets the rule now (then remembered as passed).
     * @param {string} c
     * @param {{ days?: number, min?: number }} [rule]
     * @returns {{ ok: boolean, reason?: string }}
     */
    gate(c, rule) {
      const e = saved[c];
      if (e && e.passedAt !== null) return { ok: true };
      const g = st.gate(c, rule, now());
      if (!g.ok) return { ok: false, reason: g.reason };
      // A lifted rule (both 0) passes without being remembered: a stricter rule later applies again.
      if (!(rule && rule.days === 0 && rule.min === 0)) {
        saved[c] = { ...(e || {}), passedAt: now(), since: g.since ?? null };
        save();
      }
      return { ok: true };
    },
    /** The owner set class c to direct or check: its pass is forgotten. */
    forget(c) {
      const e = saved[c];
      if (!e || e.passedAt === null) return;
      e.passedAt = null;
      save();
    },
    /** The saved state (a copy). */
    state() {
      return JSON.parse(JSON.stringify(saved));
    },
  };
}

module.exports = { egressRouting, gateFromLog, gateRule, isEvidence, createGateStats, createEgressGate, DAY };
