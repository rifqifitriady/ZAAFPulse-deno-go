// Stores the gateway's events on the control plane (P3 D4), through the same writers the in-process gateway uses, so
// the control plane stays the only writer of the usage log, the ledger's input, the governance chain, the MCP audit
// log and the trace store (program plan §3 rule 1):
//   usage.report -> recordUsage (lib/gateway/usage.js record, then the budget report, as lib/gateway/index.js wraps it),
//                   cost converted from nano-USD to the stored float USD;
//   audit.event  -> recordGovernance (server.js govRecord) or recordMcp (lib/audit.js record), by `log`;
//   trace.span   -> writeSpans (lib/trace/store.js), one record. writeSpans also throws when its append completed and
//                   only the index scan after it failed; when it throws, spanStored({ stream, seq }) looks for the
//                   span's mark in the tail of the newest trace segment: found, it is stored (the trace store indexes
//                   it on its next write); not found (or no spanStored given), it is a store failure.
// Each stored event carries "ipc": { stream, seq }. An event at or below its stream's mark is a resend of one already
// stored: it is skipped (and still acknowledged by the session).
//
// Store, then mark, then acknowledge (D4). A sink returns once its line is durably appended and throws when it is not
// (the IPC path uses the writers' strict mode; recordGovernance returning null, the governance chain's "could not
// write", counts as a throw). On a failed store the mark stays, the failure is logged once per cause, and handle
// throws: the session does not acknowledge the event, ends and kills the gateway, and the supervisor's restart resends
// it from the outbox after the next handshake. Until it is stored, a later event of that stream is refused too (it
// may still be buffered in the same read), so the mark never passes an unstored seq.
// Seqs of a stream are stored without gaps: once a stream has a mark, an event above mark + 1 is refused like a store
// failure (logged once, not marked, not acknowledged, the session ends), and the restart replays the outbox from
// Acked + 1 in order. A stream without a mark (0) accepts any first seq: the outbox replays from its Acked + 1.
// seed(stream, upTo) raises a mark to at least upTo: the session calls it on hello with outboxFrom - 1, since every seq
// below outboxFrom was acknowledged and therefore stored. A mark recovered from the stores' bounded tails can be lower
// (the newest lines of the stream fell out of the window); without the seed the next event would be a gap every time.
// Post-store hooks (afterUsage: the budget report) run after the mark is set, in a try/catch: a hook that throws is
// logged once per cause and neither undoes nor skips the mark, and the event is still acknowledged (a resend of a
// stored event would count it twice).
const { fromNano } = require('../money');
const { EVENTS } = require('./schema');

/**
 * @param {{ recordUsage: (e: object) => any, recordGovernance: (e: object) => unknown,
 *   recordMcp: (e: object) => unknown, writeSpans: (list: object[]) => unknown,
 *   spanStored?: (ipc: { stream: string, seq: number }) => boolean, afterUsage?: (entry: any) => void,
 *   marks?: Map<string, number>, now?: () => number, log?: (line: string) => void }} sinks
 */
function createIngest({
  recordUsage,
  recordGovernance,
  recordMcp,
  writeSpans,
  spanStored = () => false,
  afterUsage = () => {},
  marks = new Map(),
  now = Date.now,
  log = console.error,
}) {
  const hwm = new Map(marks);
  const unstored = new Map(); // stream -> the seq whose store failed, until it is stored
  const stats = { stored: 0, duplicates: 0, unchecked: 0, lastUncheckedAt: null };
  const logged = new Set();
  const once = (cause, line) => {
    if (logged.has(cause)) return;
    logged.add(cause);
    log(line);
  };
  const causeOf = (err) => (err && (err.code || err.message)) || String(err);

  function store(msg, ipc) {
    const b = msg.body;
    if (msg.type === 'usage.report')
      return recordUsage({
        ...b.record,
        cost: fromNano(b.record.cost),
        ...(b.budget ? { budget: b.budget } : {}),
        ipc,
      });
    if (msg.type === 'audit.event') {
      if (b.log === 'governance') {
        if (recordGovernance({ ...b.event, ipc }) == null)
          throw new Error(
            'the governance audit could not write the event (its key cannot be read, or the write failed)',
          );
      } else if (b.log === 'mcp') recordMcp({ ...b.event, ipc });
      else throw new Error(`ipc ingest: unknown log ${JSON.stringify(b.log)}`);
      return null;
    }
    try {
      writeSpans([{ ...b.span, ipc }]);
    } catch (err) {
      let found = false;
      try {
        found = spanStored(ipc) === true;
      } catch {
        /* cannot tell: a store failure */
      }
      if (!found) throw err;
    }
    return null;
  }

  /**
   * Stores one event of `stream`. Returns true when it was stored, false when it was a duplicate; throws when it could
   * not be stored (it must not be acknowledged).
   * @param {{ type: string, seq: number, body: any }} msg
   * @param {string} stream
   */
  function handle(msg, stream) {
    if (!EVENTS.includes(msg.type)) throw new Error(`ipc ingest: ${msg.type} is not an event`);
    const mark = hwm.get(stream) || 0;
    if (msg.seq <= mark) {
      stats.duplicates++;
      return false;
    }
    const waiting = unstored.get(stream);
    if (waiting !== undefined && msg.seq > waiting)
      throw new Error(`ipc ingest: seq ${msg.seq} of ${stream} arrived before seq ${waiting} was stored`);
    if (mark !== 0 && msg.seq > mark + 1) {
      const err = new Error(`ipc ingest: seq ${msg.seq} of ${stream} arrived right after seq ${mark} (a gap)`);
      once(
        `gap ${stream}`,
        `${err.message}: it is not stored or acknowledged; the gateway restarts and replays its outbox in order`,
      );
      throw err;
    }
    let entry;
    try {
      entry = store(msg, { stream, seq: msg.seq });
    } catch (err) {
      unstored.set(stream, msg.seq);
      once(
        `store ${msg.type} ${causeOf(err)}`,
        `ipc ingest: a ${msg.type} could not be stored, so it is not acknowledged and is sent again after the gateway restarts: ${err && err.message}`,
      );
      throw err;
    }
    unstored.delete(stream);
    hwm.set(stream, msg.seq);
    stats.stored++;
    if (msg.type === 'usage.report') {
      if (msg.body.budget === 'unchecked') {
        stats.unchecked++;
        stats.lastUncheckedAt = now();
      }
      try {
        afterUsage(entry);
      } catch (err) {
        once(
          `hook usage ${causeOf(err)}`,
          `ipc ingest: the budget report after a stored usage.report failed (the event stays stored and acknowledged): ${err && err.message}`,
        );
      }
    }
    return true;
  }

  /** Raises the mark of `stream` to at least `upTo` (seqs the gateway says were acknowledged, so stored). */
  function seed(stream, upTo) {
    if (!Number.isSafeInteger(upTo) || upTo <= (hwm.get(stream) || 0)) return;
    hwm.set(stream, upTo);
    const waiting = unstored.get(stream);
    if (waiting !== undefined && waiting <= upTo) unstored.delete(stream);
  }

  return {
    handle,
    seed,
    /** The highest seq stored for `stream` (0: none). */
    mark: (stream) => hwm.get(stream) || 0,
    stats: () => ({ ...stats }),
  };
}

module.exports = { createIngest };
