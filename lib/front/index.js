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

/** The mode config.json asks for: "front" or "js" (anything else is js). */
function engineOf(config) {
  const e = config && config.gateway && config.gateway.engine;
  return e === 'front' ? 'front' : 'js';
}

/** The config.push front section. A go engine is refused here: P4a has none (the gateway would exit 2). */
function frontSection({ host, port, tls = null, upstreamPort, engines = allJs() }) {
  for (const [c, e] of Object.entries(engines))
    if (e !== 'js') throw new Error(`gateway engine "${e}" for ${c} is not available yet: every route runs on js`);
  return {
    listen: { host, port },
    ...(tls ? { tls: { certPath: tls.certPath, keyPath: tls.keyPath } } : {}),
    upstream: { port: upstreamPort },
    engines,
    shadow: {},
  };
}

// P4a pushes no providers, keys or prices: every route is on js, so the gateway forwards and decides nothing. The real
// converter comes with the first go route (P4b).
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
 *   supervisor?: object, internal?: typeof createInternalListener, token?: string }} opts
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
  onServing = () => {},
  onListenError = (err) => {
    throw err;
  },
  waitPortMs = 5000,
  supervisor: supOpts = {},
  internal = createInternalListener,
  token = newToken(),
}) {
  let mode = engine === 'front' ? 'front' : 'js';
  let reason = null;
  let sup = null;
  let inner = null;
  let served = false;
  const serving = (m) => {
    if (served) return;
    served = true;
    onServing(m);
  };

  // The server's own listen on the main port; with waitMs, a port still taken (the gateway exiting) is tried again.
  async function listenMain(waitMs) {
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

  async function startFront() {
    mode = 'front';
    reason = null;
    // The server's own request handler answers the internal port; the main port stays the gateway's.
    // A connection may wait for its first request as long as Go keeps an idle pooled connection (120 s): the front's
    // spare connections must never be closed under it.
    inner = internal({ server: mainServer, token, log, firstRequestMs: 130000 });
    const upstreamPort = await inner.listen();
    const front = frontSection({ host, port, tls, upstreamPort });
    sup = createSupervisor({
      bin,
      env,
      cpVersion,
      ingest,
      beforeAck,
      log,
      sources: {
        config: () => configBody(front),
        secrets: () => ({ providers: [], mask: [], frontToken: token }),
        snapshot: () => ({ budgets: [], keyCenter: {} }),
      },
      maxRestarts: { count: 5, windowMs: 60000 },
      onReady: () => serving('front'),
      onFailed: (why) => {
        fallback(why).catch(onListenError);
      },
      ...supOpts,
    });
    sup.start();
  }

  async function fallback(why) {
    if (mode !== 'front') return;
    mode = 'fallback';
    reason = why;
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
    /** config.json's gateway section changed: a new generation in front mode; front again in fallback mode. */
    async reload() {
      if (mode === 'front' && sup) return sup.reload();
      if (mode !== 'fallback') return false;
      await new Promise((resolve) => {
        mainServer.close(() => resolve());
        if (typeof mainServer.closeAllConnections === 'function') mainServer.closeAllConnections();
      });
      await startFront();
      return true;
    },
    /** Stops the gateway (shutdown, then a kill after graceMs + its kill delay) and the internal listener. */
    async stop(graceMs = 2000) {
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
        engines: allJs(),
        ...(reason ? { reason } : {}),
        alert: !!base.alert || mode === 'fallback',
      };
    },
    get mode() {
      return mode;
    },
  };
}

module.exports = { createFront, engineOf, frontSection, configBody, CLASSES };
