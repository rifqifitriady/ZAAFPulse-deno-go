// The egress log (P5 Decision 10): one line per outbound connection the gateway made or refused (egress.event), in
// <dir>/egress.jsonl (the data folder's .data/egress.jsonl), through the MCP audit log's writer (lib/audit.js: mode 0600,
// rotated at 5 MB keeping one previous file). append is strict: it throws when the line was not durably appended, so
// the IPC ingest does not mark or acknowledge the event. Each line carries "ipc": { stream, seq } from the ingest.
const fs = require('fs');
const path = require('path');
const { createAudit } = require('./audit');

const OUTCOMES = ['ok', 'error', 'refused', 'would-refuse', 'checked'];

/** @param {{ dir: string }} opts the data folder's .data */
function createEgressLog({ dir }) {
  const file = path.join(dir, 'egress.jsonl');
  const log = createAudit(file);

  // Every record of the rotated file and the current one, oldest first (a partial line is skipped).
  function all() {
    const out = [];
    for (const f of [`${file}.1`, file]) {
      let text;
      try {
        text = fs.readFileSync(f, 'utf8');
      } catch {
        continue;
      }
      for (const line of text.split('\n')) {
        if (!line) continue;
        try {
          out.push(JSON.parse(line));
        } catch {
          /* a line another writer has not finished */
        }
      }
    }
    return out;
  }

  return {
    file,
    /** Appends one record; throws when it could not be appended. */
    append(rec) {
      log.record(rec, { strict: true });
    },
    /** The newest `n` records, newest first. */
    tail(n = 100) {
      return log.tail(n);
    },
    /**
     * Per purpose (caller class), the records since `sinceMs` by outcome, and their total.
     * @param {number} [sinceMs]
     * @returns {Record<string, Record<string, number>>}
     */
    counts(sinceMs = 0) {
      const out = {};
      for (const r of all()) {
        if (!r || typeof r.purpose !== 'string' || !(Number(r.ts) >= sinceMs)) continue;
        const c = (out[r.purpose] ||= Object.fromEntries([...OUTCOMES, 'total'].map((k) => [k, 0])));
        if (OUTCOMES.includes(r.outcome)) c[r.outcome]++;
        c.total++;
      }
      return out;
    },
  };
}

module.exports = { createEgressLog };
