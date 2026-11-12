// @ts-check
// The JS side of the shadow compare (P4b, owner decision 2 of the P4a spec): while a route class is served by js, the
// Go gateway in front marks some of its requests with X-ZP-Shadow: <id> (lib/front/internal.js keeps the mark only
// after the signed client header checks out). For a marked request the gateway records, without changing anything it
// does, the provider request it sent, the provider's answer, its own answer to the client and the usage line; Go then
// fetches that capture once (GET /_zp/shadow/<id> on the internal port), builds its own provider request and answer from
// the same provider answer, compares, and posts the result line (lib/front/shadow-log.js). Only the serving engine calls
// the provider. A capture lives in memory for at most TTL_MS and is handed out once.
const { AsyncLocalStorage } = require('async_hooks');

const SHADOW_ID = Symbol.for('zaafpulse.front.shadow');
const ID = /^[0-9a-f]{16,64}$/;
const TTL_MS = 60000;
const MAX = 1000;

const als = new AsyncLocalStorage();
/** @type {Map<string, { at: number, capture: any }>} */
const captures = new Map();

const store = (id, capture, now) => {
  for (const [k, v] of captures) if (now - v.at > TTL_MS) captures.delete(k);
  if (captures.size >= MAX) captures.delete(captures.keys().next().value);
  captures.set(id, { at: now, capture });
};

/**
 * Runs fn with a capture for shadow id `id` (a request Go marked), or plainly when there is none. A marked request that
 * got no provider answer (refused before the call, or the call failed) still leaves a capture once fn is done: the
 * provider requests made and the status of the answer `res` got (Go tells a failed call from a refusal by them).
 * @param {unknown} id
 * @param {() => any} fn
 * @param {{ statusCode: number } | null} [res]
 */
async function withShadow(id, fn, res = null) {
  if (typeof id !== 'string' || !ID.test(id)) return fn();
  const s = { id, providerRequests: [], answered: false };
  try {
    return await als.run(s, fn);
  } finally {
    if (!s.answered)
      store(
        id,
        {
          providerRequests: s.providerRequests,
          providerResponse: null,
          client: { status: res ? res.statusCode : 0, body: null },
          usage: null,
        },
        Date.now(),
      );
  }
}

/** providers.request: the request about to go to a provider (credentials included; Go compares and drops them). */
function noteProviderRequest(url, headers, body) {
  const s = als.getStore();
  if (s) s.providerRequests.push({ url, headers: { ...headers }, body: body === undefined ? null : body });
}

/**
 * chatCompletion, not streaming: the provider's answer, the client's answer and the usage line; stores the capture.
 * @param {{ status: number, body: string }} providerResponse
 * @param {{ status: number, body: string }} client
 * @param {object} usage
 */
function noteAnswer(providerResponse, client, usage, now = Date.now()) {
  const s = als.getStore();
  if (!s) return;
  s.answered = true;
  store(s.id, { providerRequests: s.providerRequests, providerResponse, client, usage }, now);
}

/** The capture of id, once (null when there is none: the request never reached a provider, or it was taken). */
function takeCapture(id) {
  const c = captures.get(id);
  captures.delete(id);
  return c ? c.capture : null;
}

module.exports = { withShadow, noteProviderRequest, noteAnswer, takeCapture, SHADOW_ID, TTL_MS };
