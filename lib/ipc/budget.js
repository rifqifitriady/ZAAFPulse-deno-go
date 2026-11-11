// The control plane's twin of the gateway's budget decision (gw/ipc/budget.go): the product's own guard
// (lib/cost/guard.js) asked against a budget.snapshot instead of the live budget service, plus the snapshot's
// staleness. With no snapshot, or one older than staleMs (now - at > staleMs), a call that no exceeded budget refuses
// is 'unchecked'; a budget the snapshot shows as exceeded is still refused. The shared fixtures in
// ipc/v1/fixtures/budget-decide run here and in Go, so the gateway decides as the code that ships today.
const { createGuard } = require('../cost/guard');

const STALE_AFTER_MS = 5000;

/**
 * @param {{ at: number, budgets: import('../types').BudgetView[], keyCenter: Record<string, string> } | null} snapshot
 * @param {number} now epoch ms
 * @param {import('../types').BudgetCheckInput} input
 * @param {number} [staleMs]
 * @returns {{ action: 'pass' | 'refuse' | 'unchecked', budget?: string, message?: string }}
 */
function decide(snapshot, now, input = {}, staleMs = STALE_AFTER_MS) {
  if (!snapshot) return { action: 'unchecked' };
  const centers = snapshot.keyCenter || {};
  const guard = createGuard({
    service: {
      list: () => ({ budgets: snapshot.budgets }),
      evaluateOne: (b) => b, // a snapshot's budgets are evaluated views already
      keyCenter: (keyId) => (Object.hasOwn(centers, keyId) ? centers[keyId] : undefined),
      reportGuard() {},
    },
    now: () => now,
  });
  const r = guard.check(input);
  if (r) return { action: 'refuse', budget: r.budget, message: r.message };
  return now - snapshot.at > staleMs ? { action: 'unchecked' } : { action: 'pass' };
}

module.exports = { decide, STALE_AFTER_MS };
