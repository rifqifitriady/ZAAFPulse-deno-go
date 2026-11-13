// The Go gateway in front (P4a spec D1, D5, D6). gateway.engine in config.json picks the mode:
//   "js" (the default): no gateway process; the server listens on the main port itself, exactly as before P4a;
//   "front": the server's request handler answers on an internal port on 127.0.0.1 (lib/front/internal.js, which
//     takes the client from the signed X-ZP-Client header), and the supervisor (lib/ipc/supervisor.js) starts
//     zaafpulse-gw, which holds the main port and forwards everything.
// When the gateway is in the failed state (an exit a restart cannot fix, or 5 restarts within 60 s) the server falls
// back: it listens on the main port itself ("fallback"), after waiting up to waitPortMs for the port to be free.
// reload() pushes a new config generation in front mode and tries front again in fallback mode.
// onServing(mode) is called once, when the main port first answers (Go ready, or the server's own listen).
const crypto = require('crypto');
const { createSupervisor } = require('../ipc/supervisor');
const { createInternalListener } = require('./internal');

// The route classes of zaafpulse-gw (gw/front/route.go Classes). P4a: every one on js.
const CLASSES = ['chat', 'messages', 'responses', 'embeddings', 'models', 'gemini', 'mcp', 'a2a', 'skills', 'agents'];
const allJs = () => Object.fromEntries(CLASSES.map((c) => [c, 'js']));
// The classes the go engine serves (gw/front/front.go GoClasses). P4b: chat, not streaming; the engine hands the
// requests of the class it does not serve to js itself.
const GO_CLASSES = ['chat'];

/**
 * The engine of each route class (P4b) and why a class asked for go stays on js. config.json `gateway.engines` asks
 * (e.g. { "chat": "go" }); a class runs on go only when the go engine has it, the shadow gate lets it (`gate(class,
 * { days, min })`, lib/front/shadow-log.js; `gateway.shadowDays`, default 1, and `gateway.shadowMinCompares`, default
 * 20: the owner's rule of 2026-11-12) and OTLP export is off (the go engine sends no spans until P4f).
 * @param {any} gateway config.json's gateway section
 * @param {{ gate?: (c: string, rule: { days: number, min: number }) => { ok: boolean, reason?: string },
 *   otel?: boolean }} [opts]
 * @returns {{ engines: Record<string, string>, notes: Record<string, string> }}
 */
function routing(gateway, { gate = () => ({ ok: true }), otel = false } = {}) {
  const asked = gateway && gateway.engines && typeof gateway.engines === 'object' ? gateway.engines : {};
  // The owner's rule (2026-11-12): 1 clean day and 20 compared calls; a value that is not a count of zero or more is the
  // default.
  const count = (v, d) => (Number.isFinite(v) && v >= 0 ? v : d);
  const rule = { days: count(gateway && gateway.shadowDays, 1), min: count(gateway && gateway.shadowMinCompares, 20) };
  const engines = allJs();
  /** @type {Record<string, string>} */
  const notes = {};
  for (const c of GO_CLASSES) {
    if (asked[c] !== 'go') continue;
    const g = gate(c, rule);
    if (otel) notes[c] = 'OpenTelemetry export is on: the go engine sends no spans yet (P4f)';
    else if (!g.ok) notes[c] = g.reason || 'the shadow gate is not met';
    else engines[c] = 'go';
  }
  return { engines, notes };
}
/** The engines alone (routing().engines). */
const enginesOf = (gateway, opts) => routing(gateway, opts).engines;

/**
 * The classes the gateway shadows: config.json `gateway.shadow` (e.g. ["chat"]), for a class the go engine has that
 * runs on js (P4b compares in that direction only).
 */
function shadowOf(gateway, engines) {
  const asked = gateway && Array.isArray(gateway.shadow) ? gateway.shadow : [];
  return GO_CLASSES.filter((c) => asked.includes(c) && engines[c] === 'js');
}

/** The mode config.json asks for: "front" or "js" (anything else is js). */
function engineOf(config) {
  const e = config && config.gateway && config.gateway.engine;
  return e === 'front' ? 'front' : 'js';
}

/** The config.push front section. go is refused for a class the go engine does not have (the gateway would exit 2). */
function frontSection({ host, port, tls = null, upstreamPort, engines = allJs(), shadow = [] }) {
  for (const [c, e] of Object.entries(engines))
    if (e !== 'js' && !(e === 'go' && GO_CLASSES.includes(c)))
      throw new Error(`gateway engine "${e}" for ${c} is not available: only ${GO_CLASSES.join(', ')} can run on go`);
  return {
    listen: { host, port },
    ...(tls ? { tls: { certPath: tls.certPath, keyPath: tls.keyPath } } : {}),
    upstream: { port: upstreamPort },
    engines,
    shadow: shadow.length ? { classes: shadow } : {},
  };
}

// The body without providers, keys or prices (P4a's, and the default of createFront's `sources`): the gateway forwards
// and decides nothing. server.js passes the real converter (lib/front/push.js, P4b).
const configBody = (front) => ({
  gateway: {
    providers: [],
    routes: [],
    guardrails: { scope: 'external', secrets: 'off', pii: 'off', injection: 'off' },
    defaults: { model: '', plannerModel: '', codeModel: '' },
    tokenSaver: false,
    modelMap: [],
  },
  keys: [],
  pricing: {},
  front,
});

/** 32 random bytes, hex: the token of the X-ZP-Client HMAC, new on every start (D1.2). */
const newToken = () => Buffer.from(crypto.getRandomValues(new Uint8Array(32))).toString('hex');

/**
 * @param {{ engine: 'js' | 'front', host: string, port: number, tls?: { certPath: string, keyPath: string } | null,
 *   mainServer: import('http').Server, bin: string,
 *   env: Record<string, string>, cpVersion: string, ingest: { handle: Function, seed?: Function },
 *   beforeAck?: (b: { stream: string, upTo: number }) => void, log?: (line: string) => void,
 *   onServing?: (mode: string) => void, onListenError?: (err: Error) => void, waitPortMs?: number,
 *   supervisor?: object, internal?: typeof createInternalListener, token?: string,
 *   createSupervisor?: typeof createSupervisor,
 *   sources?: { config: () => object, secrets: () => object, snapshot: () => object },
 *   engines?: () => Record<string, string>, shadow?: (engines: Record<string, string>) => string[],
 *   own?: ((req: any, res: any) => void) | null }} opts
 */
function createFront({
  engine,
  host,
  port,
  tls = null,
  mainServer,
  bin,
  env,
  cpVersion,
  ingest,
  beforeAck = () => {},
  log = console.error,
  // The bodies of each generation (lib/front/push.js): config.push without front, secrets.handoff without frontToken,
  // budget.snapshot without at. The front section and the token are added here (P4b).
  sources = {
    config: () => configBody(undefined),
    secrets: () => ({ providers: [], mask: [] }),
    snapshot: () => ({ budgets: [], keyCenter: {} }),
  },
  onServing = () => {},
  onListenError = (err) => {
    throw err;
  },
  waitPortMs = 5000,
  supervisor: supOpts = {},
  internal = createInternalListener,
  createSupervisor: makeSupervisor = createSupervisor,
  token = newToken(),
  // The engine of each route class and the classes shadowed, read at each push (routing(), shadowOf()); P4b.
  engines = allJs,
  shadow = () => [],
  // The internal port's answers to the gateway's own requests (lib/front/shadow-log.js shadowEndpoints); P4b.
  own = null,
}) {
  let mode = engine === 'front' ? 'front' : 'js';
  let reason = null;
  let sup = null;
  let inner = null;
  let served = false;
  // A fallback after an exit a restart cannot fix may try front again on a reload; after a crash loop (the restart
  // limit) only a restart of ZAAFPulse does, so a config save never leaves the main port dark for the back-off.
  let retryOnReload = false;
  // A supervisor reported failed and its fallback has not run yet.
  let failing = false;
  // The fallback and a retry of front run one at a time, in order (P4a review I1): never two supervisors, and never a
  // fallback started by an older supervisor over a newer front.
  let chain = Promise.resolve();
  const queue = (fn) => (chain = chain.then(fn, fn));
  let retrying = null;
  // The engines of the last generation built for the gateway (final review I2): status() shows them, and a routing
  // look pushes when the routing differs from them.
  /** @type {Record<string, string> | null} */
  let pushed = null;
  // stop() has begun: a retry still queued does not start a gateway (P4a review T5-N4).
  let stopping = false;
  const serving = (m) => {
    if (served) return;
    served = true;
    onServing(m);
  };

  // The server's own listen on the main port; with waitMs, a port still taken (the gateway exiting) is tried again.
  async function listenMain(waitMs) {
    if (mainServer.listening) return;
    const end = Date.now() + waitMs;
    for (;;) {
      try {
        await new Promise((resolve, reject) => {
          const onError = (err) => {
            mainServer.off('listening', onListening);
            reject(err);
          };
          const onListening = () => {
            mainServer.off('error', onError);
            resolve();
          };
          mainServer.once('error', onError);
          mainServer.once('listening', onListening);
          mainServer.listen(port, host);
        });
        return;
      } catch (err) {
        if (err.code !== 'EADDRINUSE' || Date.now() >= end) throw err;
        await new Promise((r) => setTimeout(r, 100));
      }
    }
  }

  // The server's own listener on the main port stops: no new connections; idle ones are closed now, and requests still
  // running (a stream) get up to 2 s to finish before their connections are closed too.
  function closeMain() {
    if (!mainServer.listening) return Promise.resolve();
    return new Promise((resolve) => {
      const force = setTimeout(() => {
        if (typeof mainServer.closeAllConnections === 'function') mainServer.closeAllConnections();
      }, 2000);
      mainServer.close(() => {
        clearTimeout(force);
        resolve();
      });
      if (typeof mainServer.closeIdleConnections === 'function') mainServer.closeIdleConnections();
    });
  }

  async function startFront() {
    mode = 'front';
    reason = null;
    failing = false;
    // The server's own request handler answers the internal port; the main port stays the gateway's.
    // A connection may wait for its first request as long as Go keeps an idle pooled connection (120 s): the front's
    // spare connections must never be closed under it.
    inner = internal({ server: mainServer, token, log, firstRequestMs: 130000, own });
    const upstreamPort = await inner.listen();
    // Listen, TLS and upstream are fixed for this gateway's life: a later push carries the same ones (P4b entry
    // condition 3; a change of gateway.engine, the host, the port or the TLS files takes a restart of ZAAFPulse).
    const base = { host, port, tls, upstreamPort };
    const mine = makeSupervisor({
      bin,
      env,
      cpVersion,
      ingest,
      beforeAck,
      log,
      sources: {
        config: () => {
          let body;
          let e;
          try {
            body = sources.config();
            e = engines();
          } catch (err) {
            // A generation that cannot be built (final review m10') goes out with no providers, no keys and every class
            // on js: the gateway serves nothing itself, and a key revoked since the last good one is refused at once.
            log(
              `gateway: the gateway configuration could not be built (${err.message}); every route goes to the server`,
            );
            body = configBody(undefined);
            e = allJs();
          }
          pushed = e; // what the gateway runs from this generation on (status, pushIfChanged)
          return { ...body, front: frontSection({ ...base, engines: e, shadow: shadow(e) }) };
        },
        secrets: () => ({ ...sources.secrets(), frontToken: token }),
        snapshot: () => sources.snapshot(),
      },
      maxRestarts: { count: 5, windowMs: 60000 },
      onReady: () => serving('front'),
      onFailed: (why, info) => {
        if (mine !== sup) return; // an older supervisor: the current front is not its to end
        failing = true;
        queue(() => fallback(why, info, mine)).catch(onListenError);
      },
      ...supOpts,
    });
    sup = mine;
    sup.start();
  }

  async function fallback(why, info, from) {
    if (from !== sup || mode !== 'front') return;
    mode = 'fallback';
    failing = false;
    reason = why;
    retryOnReload = !(info && info.kind === 'restarts');
    log(`gateway failed: ${why}; serving on Deno`);
    if (inner) await inner.close();
    inner = null;
    await listenMain(waitPortMs);
    serving('fallback');
  }

  return {
    /** Starts serving in the mode config.json asks for. Rejects when the main port cannot be had (js mode). */
    async start() {
      if (mode === 'js') {
        await listenMain(0);
        serving('js');
        return;
      }
      await startFront();
    },
    /**
     * config.json's gateway section changed: a new generation in front mode; front again in fallback mode (not after a
     * crash loop). Calls while a retry is pending join it. `after`, when given (the response of the save that asked
     * for it), is waited for before the main port is handed back to the gateway, so that answer is not cut off.
     * @param {{ after?: Promise<unknown> | null }} [opts]
     */
    async reload({ after = null } = {}) {
      if (mode === 'js') return false;
      if (mode === 'front' && sup && !failing) return sup.reload();
      if (retrying) return retrying;
      const run = queue(async () => {
        if (stopping || mode !== 'fallback' || !retryOnReload) return false;
        if (after) await Promise.race([after, new Promise((r) => setTimeout(r, 10000))]);
        if (stopping) return false;
        await closeMain();
        try {
          await startFront();
        } catch (err) {
          // The internal listener could not start: the main port is the server's again at once, never dark until a
          // restart (P4a final review m4).
          mode = 'fallback';
          reason = `front could not start again: ${err.message}`;
          log(`gateway: ${reason}; serving on Deno`);
          if (inner) await inner.close().catch(() => {});
          inner = null;
          await listenMain(waitPortMs);
          return false;
        }
        return true;
      });
      retrying = run;
      const clear = () => {
        if (retrying === run) retrying = null;
      };
      run.then(clear, clear);
      return run;
    },
    /**
     * The keys, the prices or the budgets' cost centers changed: a new generation for a gateway in front. Never tries
     * front again from fallback (only a gateway section save does, reload()). False when no gateway runs.
     */
    push() {
      return mode === 'front' && sup && !failing ? sup.reload() : false;
    },
    /**
     * A new generation when `current` (the routing computed now) differs from the engines last pushed; false when it
     * does not, or nothing was pushed yet (the first push carries it).
     * @param {Record<string, string>} current
     */
    pushIfChanged(current) {
      if (!pushed || JSON.stringify(current) === JSON.stringify(pushed)) return false;
      return this.push();
    },
    /**
     * Resolves once the gateway enforces the newest generation (true), or false: no gateway in front, or timeoutMs
     * passed. A save answers after it, so a revoked key is refused by the gateway as soon as the save has answered.
     * @param {number} [timeoutMs]
     */
    async synced(timeoutMs = 2000) {
      if (!(mode === 'front' && sup && typeof sup.synced === 'function')) return false;
      const ok = await sup.synced(timeoutMs);
      // The save is answered anyway; the gateway gets the change with its next push (P4b review m1).
      if (!ok) log(`gateway: the gateway did not confirm the new configuration within ${timeoutMs / 1000} s`);
      return ok;
    },
    /** Stops the gateway (shutdown, then a kill after graceMs + its kill delay) and the internal listener. */
    async stop(graceMs = 2000) {
      stopping = true;
      if (sup) await sup.stop(graceMs);
      if (inner) await inner.close();
      inner = null;
    },
    /** GET /api/gateway/status: the supervisor's status, the mode and the engines (D6). */
    status() {
      const base = sup
        ? sup.status()
        : {
            state: 'off',
            pid: null,
            gen: 0,
            restarts: 0,
            lastExit: null,
            lastHealth: null,
            lastFailure: null,
            failed: null,
          };
      return {
        ...base,
        mode,
        engines: mode === 'front' && pushed ? pushed : allJs(),
        ...(reason ? { reason } : {}),
        // After a crash loop only a restart of ZAAFPulse tries the gateway again (P4a deviation 12, entry condition 8).
        restartNeeded: mode === 'fallback' && !retryOnReload,
        alert: !!base.alert || mode === 'fallback',
      };
    },
    get mode() {
      return mode;
    },
  };
}

module.exports = { createFront, engineOf, enginesOf, routing, shadowOf, frontSection, configBody, CLASSES, GO_CLASSES };
