// The control plane's supervisor of the gateway process (P3 D5). It starts a session (lib/ipc/session.js) with the
// first config, secrets and budget snapshot, pushes a snapshot every snapshotEveryMs, and restarts the gateway with
// back-off (firstMs, doubling, at most maxMs; back to firstMs after resetAfterMs ready) when it exits, sends no health
// for healthTimeoutMs, or breaks the protocol (the session kills it). reload() pushes a new config generation and its
// secrets; stop() sends shutdown and kills after graceMs + killAfterMs. Usage that passed without a budget check
// (budget: 'unchecked') is counted once, when the ingest stores it (a resend after a restart is a duplicate and not
// counted), and logged once a minute as an alert; status() reports it with the restarts.
// A session that ends because the control plane could not store an event (session.failure.kind 'store') is not a
// crash: its cause is logged and kept in status().lastFailure, and it restarts with the same back-off. Like any run,
// it resets the back-off only when it was ready for resetAfterMs; a lasting store failure fails again within moments
// of the next ready (the unstored event is replayed first), so it backs off up to maxMs instead of looping.
// Once stop() has begun, the health watchdog is off: the shutdown path (graceMs + killAfterMs) alone ends the gateway.
// P4a (E8): an exit that a restart cannot fix ends in the `failed` state, with no restart: exit 2 after this side
// refused the hello, or exit 1, 2, 4 or 5 with the gateway's "fatal: " line (a hard kill on Windows exits 1 without
// one, and is restarted). So does an exit when maxRestarts.count restarts already happened within maxRestarts.windowMs
// (server.js: 5 within 60 s). onFailed(reason, { kind }) is called once (kind 'fatal' or 'restarts'); start() begins
// again. onReady() is called on each ready.
// reload() while a session starts is queued and sent once it is ready (E9). beforeAck goes to each session (E4).
// server.js starts it when gateway.engine is "front" (lib/front/index.js).
const { startSession } = require('./session');

const ALERT_EVERY_MS = 60000;

/**
 * @param {{ bin: string, args?: string[], env?: Record<string, string | undefined>, cpVersion: string,
 *   sources: { config: () => object, secrets: () => object, snapshot: () => object },
 *   ingest: { handle: (msg: any, stream: string) => unknown }, log?: (line: string) => void,
 *   now?: () => number, healthTimeoutMs?: number, snapshotEveryMs?: number,
 *   backoff?: { firstMs?: number, maxMs?: number, resetAfterMs?: number }, killAfterMs?: number,
 *   start?: typeof startSession }} opts
 * sources return bodies without gen (config, secrets) or at (snapshot): the supervisor adds them. ingest.handle
 * returns false for a duplicate (lib/ipc/ingest.js). ingest is required: an event is acknowledged only once it was
 * handed to it, so a supervisor without one would acknowledge (and the gateway delete) events nobody stored.
 */
function createSupervisor({
  bin,
  args = [],
  env = process.env,
  cpVersion,
  sources,
  ingest,
  log = console.error,
  now = Date.now,
  healthTimeoutMs = 5000,
  snapshotEveryMs = 1000,
  backoff = {},
  killAfterMs = 2000,
  start = startSession,
  beforeAck = () => {},
  maxRestarts = null,
  onReady = () => {},
  onFailed = () => {},
}) {
  if (!ingest || typeof ingest.handle !== 'function')
    throw new TypeError('createSupervisor: ingest.handle is required (events are acknowledged only once stored)');
  const firstMs = backoff.firstMs ?? 1000;
  const maxMs = backoff.maxMs ?? 30000;
  const resetAfterMs = backoff.resetAfterMs ?? 60000;
  let session = null;
  let ready = false;
  let stopped = true;
  let gen = 0;
  let restarts = 0;
  let delay = firstMs;
  let lastExit = null;
  let lastHealth = null;
  let lastFailure = null;
  let readyAt = null;
  let failed = null; // { reason, code, at } once in the failed state
  let pendingReload = false;
  let restartTimes = [];
  let healthTimer = null;
  let snapTimer = null;
  let retryTimer = null;
  const unchecked = { count: 0, lastAt: null, alertedAt: -Infinity };

  const configBody = () => ({ ...sources.config(), gen });
  const secretsBody = () => ({ ...sources.secrets(), gen });
  const snapshotBody = () => ({ ...sources.snapshot(), at: now() });

  function clearTimers() {
    for (const t of [healthTimer, retryTimer]) clearTimeout(t);
    clearInterval(snapTimer);
    healthTimer = retryTimer = snapTimer = null;
  }
  function armHealth(s) {
    clearTimeout(healthTimer);
    healthTimer = null;
    if (stopped) return; // shutting down: graceMs + killAfterMs decide, not the watchdog
    healthTimer = setTimeout(() => {
      if (session !== s || stopped) return;
      log(`gw: no health for ${healthTimeoutMs} ms: killing the gateway`);
      s.kill();
    }, healthTimeoutMs);
  }
  function noteUnchecked() {
    unchecked.count++;
    unchecked.lastAt = now();
    if (unchecked.lastAt - unchecked.alertedAt >= ALERT_EVERY_MS) {
      unchecked.alertedAt = unchecked.lastAt;
      log(`gw: alert: ${unchecked.count} metered calls passed without a budget check (the budget snapshot was stale)`);
    }
  }

  function launch() {
    gen += 1;
    ready = false;
    readyAt = null;
    const s = start({
      bin,
      args,
      env,
      cpVersion,
      log,
      now,
      killAfterMs,
      beforeAck,
      initial: { config: configBody(), secrets: secretsBody(), snapshot: snapshotBody() },
      // Every seq below outboxFrom was acknowledged, so stored: the ingest's mark is at least outboxFrom - 1 (a mark
      // recovered from bounded store tails can be lower, and the gap check would then refuse the next event forever).
      onHello: (h) => {
        if (typeof ingest.seed === 'function') ingest.seed(h.stream, h.outboxFrom - 1);
      },
      onEvent: (msg, stream) => {
        // Only a stored event counts: a resend of one already stored (false) was counted when it was stored.
        const stored = ingest.handle(msg, stream);
        if (stored !== false && msg.type === 'usage.report' && msg.body.budget === 'unchecked') noteUnchecked();
      },
      onHealth: (h) => {
        lastHealth = { ...h, at: now() };
        armHealth(s);
      },
    });
    session = s;
    armHealth(s); // no health at all within healthTimeoutMs of the start is a hang too
    s.ready.then(
      () => {
        if (session !== s) return;
        ready = true;
        readyAt = now();
        snapTimer = setInterval(pushSnapshot, snapshotEveryMs);
        if (pendingReload) {
          pendingReload = false;
          sendReload();
        }
        try {
          onReady();
        } catch (err) {
          log(`gw: onReady failed: ${err.message}`);
        }
      },
      () => {},
    );
    s.exited.then(({ code, signal }) => {
      if (session !== s) return;
      clearTimers();
      session = null;
      ready = false;
      lastExit = { code, signal, at: now() };
      const failure = s.failure;
      if (failure) lastFailure = { ...failure, at: lastExit.at };
      const storeFailed = !!failure && failure.kind === 'store';
      // A run that stayed ready for resetAfterMs was healthy, however it ended: back to firstMs (D5).
      if (readyAt !== null && lastExit.at - readyAt >= resetAfterMs) delay = firstMs;
      readyAt = null;
      if (stopped) return;
      const why = permanent(code, s);
      if (why) return fail(why, code, 'fatal');
      restartTimes = restartTimes.filter((t) => lastExit.at - t < (maxRestarts ? maxRestarts.windowMs : 0));
      if (maxRestarts && restartTimes.length >= maxRestarts.count)
        return fail(`${restartTimes.length} restarts within ${maxRestarts.windowMs / 1000} s`, code, 'restarts');
      restartTimes.push(lastExit.at);
      restarts++;
      if (storeFailed)
        log(
          `gw: the control plane could not store the gateway's events (${failure.message}); restarting in ${delay} ms`,
        );
      else log(`gw: the gateway exited (code ${code}, signal ${signal}); restarting in ${delay} ms`);
      retryTimer = setTimeout(() => {
        retryTimer = null;
        if (!stopped) launch();
      }, delay);
      delay = Math.min(delay * 2, maxMs);
    });
  }

  // Why this exit cannot be fixed by a restart, or null.
  function permanent(code, s) {
    if (code === 2 && s.failure && s.failure.kind === 'refused')
      return `the handshake was refused (${s.failure.message})`;
    if ([1, 2, 4, 5].includes(code) && s.fatal) return s.fatal;
    return null;
  }
  // kind: 'fatal' (an exit a restart cannot fix) or 'restarts' (the restart limit): onFailed(reason, { kind }).
  function fail(reason, code, kind) {
    stopped = true;
    pendingReload = false;
    failed = { reason, code, at: now() };
    log(`gw: the gateway failed (exit ${code}): ${reason}; not restarting`);
    try {
      onFailed(reason, { kind });
    } catch (err) {
      log(`gw: onFailed failed: ${err.message}`);
    }
  }
  function sendReload() {
    gen += 1;
    session.send('config.push', configBody());
    session.send('secrets.handoff', secretsBody());
  }

  function pushSnapshot() {
    if (session && ready) session.send('budget.snapshot', snapshotBody());
  }

  return {
    start() {
      if (!stopped) return;
      stopped = false;
      failed = null;
      restartTimes = [];
      launch();
    },
    /**
     * config.json, a setting or a key changed: a new generation of config and its secrets. Sent now when the gateway
     * is ready, queued until ready while it starts (E9); false when no gateway runs (the next start reads it anyway).
     */
    reload() {
      if (!session) return false;
      if (!ready) {
        pendingReload = true;
        return true;
      }
      sendReload();
      return true;
    },
    pushSnapshot,
    /** Asks the gateway to stop; resolves with its exit once it is gone (killed after graceMs + killAfterMs). */
    async stop(graceMs = 5000) {
      stopped = true;
      clearTimers();
      const s = session;
      if (!s) return lastExit;
      return s.shutdown(graceMs);
    },
    status() {
      return {
        state: session ? (ready ? 'ready' : 'starting') : failed ? 'failed' : stopped ? 'stopped' : 'restarting',
        pid: session ? session.pid : null,
        gen,
        restarts,
        lastExit,
        lastHealth,
        lastFailure,
        failed,
        unchecked: { count: unchecked.count, lastAt: unchecked.lastAt },
        alert: unchecked.lastAt !== null && now() - unchecked.lastAt < ALERT_EVERY_MS,
      };
    },
  };
}

module.exports = { createSupervisor };
