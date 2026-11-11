// The control plane's stores for the IPC ingest, at the paths server.js uses under the data folder DIR
// (lib/platform.js dataDir()): .data/gateway-usage.jsonl, .data/audit (the governance chain, its key in
// .data/audit/chain.key when no secret store is given), .cache/mcp-audit.jsonl and .data/traces; the gateway's outbox
// is .data/gw-outbox. Used by the IPC tests, the kill tests' control plane and scripts/gw-dev.js. P4 S1 gives the
// ingest the server's own objects (its wrapped usage.record, govRecord, audit, trace store) instead.
const path = require('path');
const { createUsage } = require('../gateway/usage');
const { createGovernanceAudit, keySource } = require('../governance/audit');
const { createAudit } = require('../audit');
const { createTraceStore } = require('../trace/store');

/** @param {string} root the data folder (DIR) */
function localPaths(root) {
  return {
    usageFile: path.join(root, '.data', 'gateway-usage.jsonl'),
    governanceDir: path.join(root, '.data', 'audit'),
    mcpAuditFile: path.join(root, '.cache', 'mcp-audit.jsonl'),
    traceDir: path.join(root, '.data', 'traces'),
    outboxDir: path.join(root, '.data', 'gw-outbox'),
  };
}

/**
 * @param {{ root: string, now?: () => number, getPricing?: () => object, onUsage?: (entry: object) => void }} opts
 * onUsage stands for the budget report (lib/gateway/index.js calls budgetGuard.record right after usage.record).
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
    sinks: {
      recordUsage: (e) => {
        const entry = usage.record(e);
        onUsage(entry);
        return entry;
      },
      recordGovernance: (e) => governance.record(e),
      recordMcp: (e) => mcpAudit.record(e),
      writeSpans: (list) => traces.writeSpans(list),
    },
  };
}

module.exports = { openLocalStores, localPaths };
