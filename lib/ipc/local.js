// The control plane's stores for the IPC ingest, at the paths server.js uses under the data folder DIR
// (lib/platform.js dataDir()): .data/gateway-usage.jsonl, .data/audit (the governance chain, its key in
// .data/audit/chain.key when no secret store is given), .cache/mcp-audit.jsonl and .data/traces; the gateway's outbox
// is .data/gw-outbox. Used by the IPC tests, the kill tests' control plane and scripts/gw-dev.js. P4 S1 must keep
// strict sinks like these: the server's wrapped usage.record drops the strict option and runs the budget report
// itself, govRecord's refusal limiter returns null (a store failure, so a restart), and the trace recorder buffers
// before the disk (docs/migration/2026-11-11-p3-results.md, P4 S1 entry conditions).
const path = require('path');
const { createUsage } = require('../gateway/usage');
const { createGovernanceAudit, keySource } = require('../governance/audit');
const { createAudit } = require('../audit');
const { createTraceStore } = require('../trace/store');
const { spanInNewestSegment } = require('./marks');

/** @param {string} root the data folder (DIR) */
function localPaths(root) {
  return {
    usageFile: path.join(root, '.data', 'gateway-usage.jsonl'),
    governanceDir: path.join(root, '.data', 'audit'),
    mcpAuditFile: path.join(root, '.cache', 'mcp-audit.jsonl'),
    traceDir: path.join(root, '.data', 'traces'),
    outboxDir: path.join(root, '.data', 'gw-outbox'),
    marksFile: path.join(root, '.data', 'ipc-marks.json'),
  };
}

/**
 * @param {{ root: string, now?: () => number, getPricing?: () => object, onUsage?: (entry: object) => void }} opts
 * onUsage stands for the budget report (lib/gateway/index.js calls budgetGuard.record right after usage.record); the
 * ingest runs it as afterUsage, once the usage line is stored and marked.
 */
function openLocalStores({ root, now = Date.now, getPricing = () => ({}), onUsage = () => {} }) {
  const files = localPaths(root);
  const usage = createUsage({ file: files.usageFile, getPricing, now });
  const governance = createGovernanceAudit({
    dir: files.governanceDir,
    getKey: keySource({ dir: files.governanceDir, store: null, now }),
    now,
  });
  const mcpAudit = createAudit(files.mcpAuditFile);
  const traces = createTraceStore({ dir: files.traceDir, now });
  return {
    files,
    usage,
    governance,
    mcpAudit,
    traces,
    // The strict writers: a failed append throws, so the ingest does not mark or acknowledge the event. The budget
    // report is a post-store hook (afterUsage), run by the ingest after the mark is set.
    sinks: {
      recordUsage: (e) => usage.record(e, { strict: true }),
      afterUsage: onUsage,
      recordGovernance: (e) => governance.record(e),
      recordMcp: (e) => mcpAudit.record(e, { strict: true }),
      writeSpans: (list) => traces.writeSpans(list),
      // writeSpans throws when only its index scan failed after the append: the ingest then looks for the span here.
      spanStored: (ipc) => spanInNewestSegment(files.traceDir, ipc),
    },
  };
}

module.exports = { openLocalStores, localPaths };
