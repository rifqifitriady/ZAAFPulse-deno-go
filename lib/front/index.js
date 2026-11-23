// The Go gateway in front (P4a spec D1, D5, D6). gateway.engine in config.json picks the mode:
//   "js" (the default): no gateway process; the server listens on the main port itself, exactly as before P4a;
//   "front": the server's request handler answers on an internal port on 127.0.0.1 (lib/front/internal.js, which
//     takes the client from the signed X-ZP-Client header), and the supervisor (lib/ipc/supervisor.js) starts
//     zaafpulse-gw, which holds the main port and forwards everything.
// When the gateway is in the failed state (an exit a restart cannot fix, or 5 restarts within 60 s) the server falls
// back: it listens on the main port itself ("fallback"), after waiting up to waitPortMs for the port to be free.
// reload() pushes a new config generation in front mode and tries front again in fallback mode.
// onServing(mode) is called once, when the main port first answers (Go ready, or the server's own listen).
// P5: every config.push carries an egress section (lib/front/push.js egressSection; the default one when the body has
// none), so the gateway opens its egress endpoint; onEgress({ port, token }) is called on each ready that names its
// port (token: the front token, from which lib/egress.js deriveToken makes the endpoint's), and onEgress(null) when
// the gateway is gone (an exit, the fallback, a stop).
const crypto = require('crypto');
const { createSupervisor } = require('../ipc/supervisor');
const { createInternalListener } = require('./internal');
const { gwStatus: binStatus, gwRefusal } = require('./bin');

// The route classes of zaafpulse-gw (gw/front/route.go Classes). P4a: every one on js.
const CLASSES = ['chat', 'messages', 'responses', 'embeddings', 'models', 'gemini', 'mcp', 'a2a', 'skills', 'agents'];
// Sub-classes are not path classes: the gateway reads a request's body and serves it under the sub-class it asks for,
// each with its own engine entry, shadow entry and switch-rule record: chat-stream (P4c), and the P4d sub-classes of
// chat (an Anthropic-format target), messages (count_tokens, streams), responses and gemini (gw/front/front.go Subs).
const SUB_CLASSES = [
  'chat-stream',
  'chat-anthropic',
  'chat-anthropic-stream',
  'messages-stream',
  'messages-count',
  'responses-stream',
  'gemini-stream',
  'gemini-count',
  // The token saver's switch (docs/superpowers/specs/2026-11-17-rtk-go-design.md D13): not a path, the engine reads it
  // for every call the saver looks at; it has its own engine entry, shadow entry and switch-rule record.
  'token-saver',
  // The provider-auth switch (docs/superpowers/specs/2026-11-21-p4g-mcp-auth-design.md Decision 14): not a path, the
  // engine reads it for every call to an OAuth2 or service-account provider; its own engine entry, shadow entry and
  // switch-rule record.
  'provider-auth',
  // The routes switch (docs/superpowers/specs/2026-11-21-p4h-router-design.md Decision 2): not a path, the engine reads
  // it for every route call (router/<name>); its own engine entry, shadow entry and switch-rule record.
  'routes',
];
const allJs = () => Object.fromEntries([...CLASSES, ...SUB_CLASSES].map((c) => [c, 'js']));
// The classes the go engine serves (gw/front/front.go GoClasses); the engine hands the requests of a class it does not
// serve to js itself.
const GO_CLASSES = [
  'chat',
  'chat-stream',
  'chat-anthropic',
  'chat-anthropic-stream',
  'messages',
  'messages-stream',
  'messages-count',
  'responses',
  'responses-stream',
  'gemini',
  'gemini-stream',
  'gemini-count',
  'embeddings',
  'mcp', // /mcp tools/call (P4g); the tool itself runs here (lib/front/tools.js)
  'token-saver',
  'provider-auth',
  'routes',
];

/**
 * The engine of each route class (P4b) and why a class asked for go stays on js. config.json `gateway.engines` asks
 * (e.g. { "chat": "go" }); a class runs on go only when the go engine has it, the shadow gate lets it (`gate(class,
 * { days, min })`, lib/front/shadow-log.js; `gateway.shadowDays`, default 1, and `gateway.shadowMinCompares`, default
 * 20: the owner's rule of 2026-11-12) and OTLP export does not capture content (P4f D10: the go engine sends its own
 * spans, but prompts and answers would wait in its outbox on disk; owner answer 2026-11-16).
 * @param {any} gateway config.json's gateway section
 * @param {{ gate?: (c: string, rule: { days: number, min: number }) => { ok: boolean, reason?: string },
 *   otel?: boolean | { enabled?: boolean, captureContent?: boolean } }} [opts] otel: otel.status() (a boolean: on or
 *   off, without content)
 * @returns {{ engines: Record<string, string>, notes: Record<string, string> }}
 */
function routing(gateway, { gate = () => ({ ok: true }), otel = false } = {}) {
  const asked = gateway && gateway.engines && typeof gateway.engines === 'object' ? gateway.engines : {};
  // The owner's rule (2026-11-12): 1 clean day and 20 compared calls; a value that is not a count of zero or more is the
  // default.
  const count = (v, d) => (Number.isFinite(v) && v >= 0 ? v : d);
  const rule = { days: count(gateway && gateway.shadowDays, 1), min: count(gateway && gateway.shadowMinCompares, 20) };
  const o = otel && typeof otel === 'object' ? otel : { enabled: !!otel };
  const content = o.enabled === true && o.captureContent === true;
  const engines = allJs();
  /** @type {Record<string, string>} */
  const notes = {};
  for (const c of GO_CLASSES) {
    if (asked[c] !== 'go') continue;
    const g = gate(c, rule);
    if (content)
      notes[c] =
        'OpenTelemetry export captures content (captureContent): the go engine does not carry prompts or answers';
    else if (!g.ok) notes[c] = g.reason || 'the shadow gate is not met';
    else engines[c] = 'go';
  }
  return { engines, notes };
}
// The gateway's soft memory limit in MiB (P4c, M4: the gateway's RSS at 500 streams), config.json gateway.memLimitMB; 0
// turns it off. A command-line flag, not GOMEMLIMIT: the gateway's environment is an allow-list (P4a E7).
const MEM_LIMIT_MB = 64;
/** The gateway's command line from config.json's gateway section. @param {any} gateway */
function gwArgs(gateway) {
  const v = gateway && gateway.memLimitMB;
  const mb = Number.isInteger(v) && v >= 0 ? v : MEM_LIMIT_MB;
  return mb > 0 ? ['--mem-limit-mb', String(mb)] : [];
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
 *   sources?: { config: () => object, secrets: () => object, snapshot: () => object, window?: () => object[],
 *     routes?: () => object },
 *   engines?: () => Record<string, string>, shadow?: (engines: Record<string, string>) => string[],
 *   own?: ((req: any, res: any) => void) | null, args?: string[], onControl?: (msg: any) => void,
 *   onUncheckedAlert?: (a: { calls: number, since: number, at: number }) => void,
 *   onEgress?: (e: { port: number, token: string } | null) => void,
 *   gwStatus?: (bin: string) => { path: string, found: boolean, allowed: boolean } }} opts
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
  // The gateway's command line (gwArgs; P4c).
  args = [],
  // P4f D16: a control message from the gateway (ratelimit.take; P4h: provider.note, route.take).
  onControl = () => {},
  // P4f D24: one call per unchecked-budget alert window (server.js writes the governance entry).
  onUncheckedAlert = () => {},
  // P5: the gateway's egress endpoint, { port, token } on each ready that names it, null when it is gone.
  onEgress = () => {},
  // P6 Decision 4: the gateway binary's state, asked before each supervisor's first start. Missing or not allowed to
  // run is a fatal start (the fallback at once, with gwRefusal's reason).
  gwStatus = (p) => binStatus({ path: p }),
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
  // The egress endpoint's port of the current gateway (P5), or null.
  let egressPort = null;
  const egress = (port) => {
    egressPort = port;
    try {
      onEgress(port ? { port, token } : null);
    } catch (err) {
      log(`gateway: onEgress failed: ${err.message}`);
    }
  };
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
      args,
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
          return {
            ...body,
            front: frontSection({ ...base, engines: e, shadow: shadow(e) }),
            // P5: the endpoint opens with the first push; every class is direct until the client is told otherwise.
            egress: body.egress || { allow: [], allowList: 'report' },
          };
        },
        secrets: () => ({ ...sources.secrets(), frontToken: token }),
        snapshot: () => sources.snapshot(),
        // P4f D16: the rate-limit window a new gateway starts with (none without the source).
        window: () => (typeof sources.window === 'function' ? sources.window() : []),
        // P4h Decision 3: the routing state a new gateway starts with (none without the source).
        routes: () => (typeof sources.routes === 'function' ? sources.routes() : null),
      },
      onControl,
      onUncheckedAlert,
      maxRestarts: { count: 5, windowMs: 60000 },
      precheck: () => gwRefusal(gwStatus(bin)),
      onReady: () => serving('front'),
      onEgress: (p) => {
        if (mine === sup) egress(p); // an older supervisor's gateway is not the current one
      },
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
    /** A control message to the gateway in front (ratelimit.take, P4f D16; provider.note, route.take, P4h); false when none is ready. */
    sendControl(type, body) {
      return mode === 'front' && sup && !failing && typeof sup.control === 'function' ? sup.control(type, body) : false;
    },
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
        egressPort,
        alert: !!base.alert || mode === 'fallback',
      };
    },
    get mode() {
      return mode;
    },
    /** The egress endpoint's port of the gateway in front (P5), or null. */
    get egressPort() {
      return egressPort;
    },
  };
}

module.exports = {
  gwArgs,
  MEM_LIMIT_MB,
  createFront,
  engineOf,
  enginesOf,
  routing,
  shadowOf,
  frontSection,
  configBody,
  CLASSES,
  GO_CLASSES,
  SUB_CLASSES,
};
