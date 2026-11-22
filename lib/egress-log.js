// The egress log (P5 Decision 10): one line per outbound connection the gateway made or refused (egress.event), in
// <dir>/egress.jsonl (the data folder's .data/egress.jsonl), through the MCP audit log's writer (lib/audit.js: mode 0600,
// rotated at 5 MB keeping one previous file). append is strict: it throws when the line was not durably appended, so
// the IPC ingest does not mark or acknowledge the event. Each line carries "ipc": { stream, seq } from the ingest.
const fs = require('fs');
const path = require('path');
const { createAudit } = require('./audit');

const OUTCOMES = ['ok', 'error', 'refused', 'would-refuse', 'checked'];

// The most a read goes back, across the current and the rotated file: a call never reads the 10 MB the two files can
// hold. counts covers the records in it (the newest ~2 MB, some thousands of records), which the Compliance page reads.
const READ_LIMIT = 2 * 1024 * 1024;
const CHUNK = 64 * 1024;

// The complete lines of the last `budget` bytes of a file, newest first, stopping once `want` lines are in (a line cut
// by the start of the window, or not yet finished by its writer, is left out).
function linesFromEnd(file, budget, want) {
  let fd;
  try {
    fd = fs.openSync(file, 'r');
  } catch {
    return { lines: [], used: 0 };
  }
  try {
    const size = fs.fstatSync(fd).size;
    const floor = Math.max(0, size - budget);
    const lines = [];
    let pos = size;
    let rest = '';
    let open = true; // still inside the bytes after the last newline: empty, or a line being written
    while (pos > floor && lines.length < want) {
      const len = Math.min(CHUNK, pos - floor);
      pos -= len;
      const buf = Buffer.alloc(len);
      fs.readSync(fd, buf, 0, len, pos);
      let text = buf.toString('latin1') + rest; // latin1: one char per byte, so a split never cuts a UTF-8 sequence
      if (open) {
        const k = text.lastIndexOf('\n');
        if (k < 0) {
          rest = '';
          continue;
        }
        text = text.slice(0, k);
        open = false;
      }
      const parts = text.split('\n');
      rest = parts.shift();
      for (let i = parts.length - 1; i >= 0 && lines.length < want; i--)
        if (parts[i]) lines.push(Buffer.from(parts[i], 'latin1').toString('utf8'));
    }
    if (pos === 0 && !open && rest && lines.length < want) lines.push(Buffer.from(rest, 'latin1').toString('utf8'));
    return { lines, used: size - pos };
  } finally {
    fs.closeSync(fd);
  }
}

/** @param {{ dir: string }} opts the data folder's .data */
function createEgressLog({ dir }) {
  const file = path.join(dir, 'egress.jsonl');
  const log = createAudit(file);

  // Up to `want` records, newest first, from the current file then the rotated one, within READ_LIMIT bytes in all.
  function newest(want) {
    const out = [];
    let budget = READ_LIMIT;
    for (const f of [file, `${file}.1`]) {
      if (out.length >= want || budget <= 0) break;
      const { lines, used } = linesFromEnd(f, budget, want - out.length);
      budget -= used;
      for (const line of lines) {
        try {
          out.push(JSON.parse(line));
        } catch {
          /* a cut line */
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
    /**
     * Every record of both files (the rotated one first, so oldest first) whose line holds `needle`, parsed: a filter
     * applied before anything is kept, so no other record can push a matching one out (the egress gate's evidence at
     * start, needle '"via":"check"' plus its check errors). Reads the files whole, once.
     * @param {string} needle
     * @returns {any[]}
     */
    scan(needle) {
      const out = [];
      for (const f of [`${file}.1`, file]) {
        let text = '';
        try {
          text = fs.readFileSync(f, 'utf8');
        } catch {
          continue;
        }
        for (const line of text.split('\n')) {
          if (!line.includes(needle)) continue;
          try {
            out.push(JSON.parse(line));
          } catch {
            /* a cut line */
          }
        }
      }
      return out;
    },
    /** The newest `n` records, newest first, the rotated file's too when the current one holds fewer. */
    tail(n = 100) {
      return newest(n);
    },
    /**
     * Per purpose (caller class), the records since `sinceMs` by outcome, and their total; over the newest READ_LIMIT
     * (2 MB) of the log only.
     * @param {number} [sinceMs]
     * @returns {Record<string, Record<string, number>>}
     */
    counts(sinceMs = 0) {
      const out = {};
      for (const r of newest(Infinity)) {
        if (!r || typeof r.purpose !== 'string' || !(Number(r.ts) >= sinceMs)) continue;
        const c = (out[r.purpose] ||= Object.fromEntries([...OUTCOMES, 'total'].map((k) => [k, 0])));
        if (OUTCOMES.includes(r.outcome)) c[r.outcome]++;
        c.total++;
      }
      return out;
    },
  };
}

module.exports = { createEgressLog, READ_LIMIT };
