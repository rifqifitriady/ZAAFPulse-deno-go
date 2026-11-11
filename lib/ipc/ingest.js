// Stores the gateway's events on the control plane (P3 D4), through the same writers the in-process gateway uses, so
// the control plane stays the only writer of the usage log, the ledger's input, the governance chain, the MCP audit
// log and the trace store (program plan §3 rule 1):
//   usage.report -> recordUsage (lib/gateway/usage.js record, then the budget report, as lib/gateway/index.js wraps it),
//                   cost converted from nano-USD to the stored float USD;
//   audit.event  -> recordGovernance (server.js govRecord) or recordMcp (lib/audit.js record), by `log`;
//   trace.span   -> writeSpans (lib/trace/store.js), one record.
// Each stored event carries "ipc": { stream, seq }. An event at or below its stream's mark is a resend of one already
// stored: it is skipped (and still acknowledged by the session). A writer that throws is not caught: the session does
// not acknowledge the event, kills the gateway, and the event is sent again after the restart.
const { fromNano } = require('../money');
const { EVENTS } = require('./schema');

/**
 * @param {{ recordUsage: (e: object) => unknown, recordGovernance: (e: object) => unknown,
 *   recordMcp: (e: object) => unknown, writeSpans: (list: object[]) => unknown, marks?: Map<string, number>,
 *   now?: () => number }} sinks
 */
function createIngest({ recordUsage, recordGovernance, recordMcp, writeSpans, marks = new Map(), now = Date.now }) {
  const hwm = new Map(marks);
  const stats = { stored: 0, duplicates: 0, unchecked: 0, lastUncheckedAt: null };

  /**
   * Stores one event of `stream`. Returns true when it was stored, false when it was a duplicate.
   * @param {{ type: string, seq: number, body: any }} msg
   * @param {string} stream
   */
  function handle(msg, stream) {
    if (!EVENTS.includes(msg.type)) throw new Error(`ipc ingest: ${msg.type} is not an event`);
    if (msg.seq <= (hwm.get(stream) || 0)) {
      stats.duplicates++;
      return false;
    }
    const ipc = { stream, seq: msg.seq };
    const b = msg.body;
    if (msg.type === 'usage.report') {
      recordUsage({ ...b.record, cost: fromNano(b.record.cost), ...(b.budget ? { budget: b.budget } : {}), ipc });
      if (b.budget === 'unchecked') {
        stats.unchecked++;
        stats.lastUncheckedAt = now();
      }
    } else if (msg.type === 'audit.event') {
      if (b.log === 'governance') recordGovernance({ ...b.event, ipc });
      else recordMcp({ ...b.event, ipc });
    } else writeSpans([{ ...b.span, ipc }]);
    hwm.set(stream, msg.seq);
    stats.stored++;
    return true;
  }

  return {
    handle,
    /** The highest seq stored for `stream` (0: none). */
    mark: (stream) => hwm.get(stream) || 0,
    stats: () => ({ ...stats }),
  };
}

module.exports = { createIngest };
