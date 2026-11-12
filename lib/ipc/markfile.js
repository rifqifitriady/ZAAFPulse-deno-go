// The IPC mark file (P4a spec E4): .data/ipc-marks.json holds { <stream>: <mark> }, the highest seq of each gateway
// event stream that the control plane stored and is about to acknowledge. It is written (a temp file, then rename)
// before each ack is sent and after the events it covers were stored, so it never runs ahead of the stores while the
// operating system runs (a killed process included). On start, the larger of it and the stores' tails
// (lib/ipc/marks.js recoverMarks) is the mark, so a mark that fell out of the tail window is not lost.
// Neither the stores nor this file are fsynced (P4f decision, docs/migration/2026-11-12-p4f-mark-durability.md): the
// target is a killed process, not a power loss. After a power loss the mark can lead a store's lost last lines, and
// those events are lost (counted twice only in an improbable case past the 4 MiB tail window: see the record); the
// outbox has the same window, so fsyncing the stores alone would not close it.
const crypto = require('crypto');
const fs = require('fs');
const path = require('path');

const HEX32 = /^[0-9a-f]{32}$/;

/** The marks in `file` (an empty Map when it is missing or cannot be read; entries that are not a mark are skipped). */
function readMarkFile(file) {
  const marks = new Map();
  let data;
  try {
    data = JSON.parse(fs.readFileSync(file, 'utf8'));
  } catch {
    return marks;
  }
  if (!data || typeof data !== 'object' || Array.isArray(data)) return marks;
  for (const [stream, seq] of Object.entries(data))
    if (HEX32.test(stream) && Number.isSafeInteger(seq) && seq > 0) marks.set(stream, seq);
  return marks;
}

/** @param {string} file */
function createMarkFile(file) {
  let marks = readMarkFile(file);
  return {
    /** A copy of the marks as last written. */
    get: () => new Map(marks),
    /**
     * Raises the mark of `stream` to `upTo` on disk; throws when the write fails (the ack then waits).
     * @param {string} stream
     * @param {number} upTo
     */
    save(stream, upTo) {
      // Another server on the same data folder may have written since: keep the larger mark of every stream, so a
      // server never lowers or drops a mark another one wrote (P4f entry 4). That holds with one writer at a time:
      // the read, merge and rename are not atomic across processes, so two saves that overlap can still drop the
      // other's newest entry. The outbox lock allows one gateway, so one session and one acker, per data folder, and
      // the stores' tails still hold a dropped entry. The temp file is per process and per save, so two writers never
      // rename each other's temp file.
      const merged = new Map(marks);
      for (const [s, m] of readMarkFile(file)) if (m > (merged.get(s) || 0)) merged.set(s, m);
      marks = merged;
      if (!(upTo > (merged.get(stream) || 0))) return;
      const next = new Map(merged).set(stream, upTo);
      fs.mkdirSync(path.dirname(file), { recursive: true });
      const tmp = `${file}.${process.pid}.${crypto.randomBytes(6).toString('hex')}.tmp`;
      try {
        fs.writeFileSync(tmp, `${JSON.stringify(Object.fromEntries(next))}\n`, { mode: 0o600 });
        fs.renameSync(tmp, file); // replaces the old file on Windows too
      } catch (err) {
        fs.rmSync(tmp, { force: true });
        throw err;
      }
      marks = next; // only once it is on disk: a failed write leaves memory at what the file holds
    },
  };
}

module.exports = { createMarkFile, readMarkFile };
