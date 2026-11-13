// The writers of the IPC ingest inside server.js (P4a spec E3). They must keep the IPC guarantees (store, then mark,
// then acknowledge), which the server's own writers do not:
//   - the gateway's usage.record is wrapped (lib/gateway/index.js: the budget report; lib/otel.js: a span) by
//     functions that drop the { strict: true } argument, so a failed append would be acknowledged, and the budget
//     report would run twice with afterUsage;
//   - govRecord sends some entries through the refusal limiter, which returns null when it drops one (a store failure
//     to the ingest, so a restart, over and over);
//   - the trace recorder buffers spans and writes them later.
// So usage goes through the gateway's own unwrapped writer (gateway.ingestUsage, strict), which also counts the line in
// the gateway's in-memory spend, so per-key budgets see the Go engine's calls (P4b entry condition 1; before P4b a
// second usage writer on the same file skipped it); governance, the MCP audit log and traces get the server's raw
// stores (a second governance chain on the same folder would fork the HMAC chain).
const { spanInNewestSegment } = require('./marks');

/**
 * @param {{ recordUsage: (entry: object) => object, budgetRecord: (entry: any) => void,
 *   governance: { record: (e: object) => unknown }, mcpAudit: { record: (e: object, opts?: object) => unknown },
 *   traceStore: { writeSpans: (list: object[]) => unknown } | null, traceDir: string }} stores
 * recordUsage: the gateway's strict, unwrapped writer (lib/gateway/index.js ingestUsage).
 */
function serverSinks({ recordUsage, budgetRecord, governance, mcpAudit, traceStore, traceDir }) {
  if (typeof recordUsage !== 'function')
    throw new TypeError('serverSinks: recordUsage (gateway.ingestUsage) is required');
  return {
    recordUsage: (e) => recordUsage(e),
    afterUsage: (entry) => budgetRecord(entry),
    recordGovernance: (e) => governance.record(e),
    recordMcp: (e) => mcpAudit.record(e, { strict: true }),
    writeSpans: (list) => {
      if (!traceStore) throw new Error('the local trace store is off');
      return traceStore.writeSpans(list);
    },
    spanStored: (ipc) => spanInNewestSegment(traceDir, ipc),
  };
}

module.exports = { serverSinks };
