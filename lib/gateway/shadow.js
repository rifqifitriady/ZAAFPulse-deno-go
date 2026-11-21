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
// The bodies held at once (final review m9'): a capture holds the provider request and answer and the client answer.
const MAX_BYTES = 64 * 1024 * 1024;
let maxBytes = MAX_BYTES;
const STREAM_CAP = 4 * 1024 * 1024;

const als = new AsyncLocalStorage();
/** @type {Map<string, { at: number, capture: any, bytes: number }>} */
const captures = new Map();
let held = 0;

/** @param {any} c */
const sizeOf = (c) =>
  (c.providerRequests || []).reduce((n, r) => n + (r.body ? r.body.length : 0), 0) +
  (c.providerResponse && c.providerResponse.body ? c.providerResponse.body.length : 0) +
  (c.upstream || []).reduce((n, u) => n + u.body.length, 0) +
  (c.client && c.client.body ? c.client.body.length : 0) +
  (c.tool ? (c.tool.args || '').length + (c.tool.r || '').length : 0);
/** @param {string} k */
const drop = (k) => {
  const v = captures.get(k);
  if (v) held -= v.bytes;
  captures.delete(k);
};
/** @param {string} id @param {any} capture @param {number} now */
const store = (id, capture, now) => {
  for (const [k, v] of captures) if (now - v.at > TTL_MS) drop(k);
  const bytes = sizeOf(capture);
  // The oldest go first, until this one fits in the count and the bytes (one bigger than the limit is not kept).
  while (captures.size && (captures.size >= MAX || held + bytes > maxBytes)) drop(captures.keys().next().value);
  if (bytes > maxBytes) return;
  captures.set(id, { at: now, capture, bytes });
  held += bytes;
};

/** Tests: the byte limit (default 64 MiB). @param {{ maxBytes?: number }} opts */
function configureCaptures({ maxBytes: m = MAX_BYTES } = {}) {
  maxBytes = m;
}

/**
 * Runs fn with a capture for shadow id `id` (a request Go marked), or plainly when there is none. A marked request that
 * got no provider answer (refused before the call, or the call failed) still leaves a capture once fn is done: the
 * provider requests made and the status of the answer `res` got (Go tells a failed call from a refusal by them).
 * @param {unknown} id
 * @param {() => any} fn
 * @param {{ statusCode: number, end?: Function } | null} [res]
 */
async function withShadow(id, fn, res = null) {
  if (typeof id !== 'string' || !ID.test(id)) return fn();
  /** @type {any} */
  const s = {
    id,
    providerRequests: [],
    answered: false,
    clientBody: null,
    usage: null,
    stored: false,
    // P4d: the provider's answers as read (teeResponse), the client's bytes and head, and the tape of the random
    // bytes and clock reads translate.js drew.
    upstream: [],
    client: { status: 0, headers: {}, text: '', truncated: false },
    tape: { random: [], now: [] },
    // P4g: the integration tool a /mcp tools/call ran (noteTool); the capture has none when no tool was called.
    tool: null,
  };
  const unanswered = () => {
    if (s.answered || s.stored) return;
    s.stored = true;
    store(
      id,
      {
        providerRequests: s.providerRequests,
        providerResponse: null,
        upstream: s.upstream,
        // A refusal (P4e): the error body fail() wrote and the line a guardrail refusal recorded, when there are. A P4d
        // class: the client's head and bytes as written.
        client: {
          status: res ? res.statusCode : 0,
          headers: s.client.headers,
          body: s.clientBody ?? (s.client.text || null),
          truncated: s.client.truncated,
        },
        usage: s.usage,
        tape: s.tape,
        ...(s.tool ? { tool: s.tool } : {}),
      },
      Date.now(),
    );
  };
  // P4f final review m3: a refusal's capture is stored as the answer ends, before the gateway can ask for it, not when
  // the handler's promise settles (which on Deno came after the gateway had the answer and asked).
  if (res && typeof res.end === 'function') {
    const end = res.end;
    res.end = function (...args) {
      // Only a refusal before any provider call: a stream ends its answer before noteStream stores its capture, and a
      // failed provider call is stored when the handler settles.
      if (!s.providerRequests.length) unanswered();
      return end.apply(this, args);
    };
  }
  // Wrapped last, so its end runs first: the last bytes are kept before a refusal's capture is stored.
  if (res) keepClient(res, s.client);
  try {
    return await als.run(s, fn);
  } finally {
    unanswered();
  }
}

/** The headers of a client answer the shadow compares (lower case). */
const CLIENT_HEADERS = ['content-type', 'x-zp-dropped'];
/**
 * Keeps what a captured request's response writes: its head's compared headers and its bytes as text, up to
 * STREAM_CAP (P4d). The response object lives as long as the request, so its methods are replaced in place.
 * @param {any} res
 * @param {{ status: number, headers: Record<string, string>, text: string, truncated: boolean }} out
 */
function keepClient(res, out) {
  const keep = (chunk) => {
    if (chunk == null || typeof chunk === 'function' || out.truncated) return;
    const t = typeof chunk === 'string' ? chunk : Buffer.from(chunk).toString('utf8');
    if (out.text.length + t.length > STREAM_CAP) out.truncated = true;
    else out.text += t;
  };
  const { writeHead, write, end } = res;
  if (typeof writeHead === 'function')
    res.writeHead = function (status, ...rest) {
      const h = rest.find((x) => x && typeof x === 'object') || {};
      for (const [k, v] of Object.entries(h))
        if (CLIENT_HEADERS.includes(k.toLowerCase())) out.headers[k.toLowerCase()] = String(v);
      return writeHead.call(this, status, ...rest);
    };
  if (typeof write === 'function')
    res.write = function (chunk, ...rest) {
      keep(chunk);
      return write.call(this, chunk, ...rest);
    };
  if (typeof end === 'function')
    res.end = function (chunk, ...rest) {
      keep(chunk);
      return end.call(this, chunk, ...rest);
    };
}

/** translate.js rid: the random bytes a captured request's translation drew (P4d tape). @param {Uint8Array} buf */
function tapeRandom(buf) {
  const s = als.getStore();
  if (s && s.tape && s.tape.random.length < 4096) s.tape.random.push(Buffer.from(buf).toString('hex'));
}

/** translate.js: a clock read of a captured request's translation (P4d tape). @param {number} ms */
function tapeNow(ms) {
  const s = als.getStore();
  if (s && s.tape && s.tape.now.length < 4096) s.tape.now.push(ms);
}

const NULL_BODY = new Set([101, 204, 205, 304]);
/**
 * providers.request: the provider's answer of a captured request, kept as it is read (status, content type, decoded
 * text up to STREAM_CAP, whether it ended or was cut; P4d). The Response handed back reads the same bytes.
 * @param {Response} r
 * @returns {Response}
 */
function teeResponse(r) {
  const s = als.getStore();
  if (!s || !s.upstream) return r;
  const rec = {
    status: r.status,
    contentType: r.headers.get('content-type') || '',
    body: '',
    ended: 'cut',
    truncated: false,
  };
  s.upstream.push(rec);
  if (!r.body || NULL_BODY.has(r.status)) {
    rec.ended = 'eof';
    return r;
  }
  const dec = new TextDecoder();
  /** @param {string} t */
  const keep = (t) => {
    if (rec.truncated) return;
    if (rec.body.length + t.length > STREAM_CAP) rec.truncated = true;
    else rec.body += t;
  };
  const body = r.body.pipeThrough(
    new TransformStream({
      transform(chunk, ctrl) {
        keep(dec.decode(chunk, { stream: true }));
        ctrl.enqueue(chunk);
      },
      flush() {
        keep(dec.decode());
        rec.ended = 'eof';
      },
    }),
  );
  return new Response(body, { status: r.status, statusText: r.statusText, headers: r.headers });
}

/**
 * The error body fail() writes for a request Go marked (lib/gateway/index.js): kept for a refusal's capture (P4e).
 * @param {string} body
 */
function noteClient(body) {
  const s = als.getStore();
  if (s && !s.answered) s.clientBody = body;
}

/**
 * The usage line a refusal recorded (a guardrail block or failure): kept for a refusal's capture (P4e).
 * @param {any} entry
 */
function noteUsage(entry) {
  const s = als.getStore();
  if (s && !s.answered && entry) s.usage = entry;
}

/** noteTool's value for a callTool that threw. */
const THROWN = Object.freeze({ thrown: true });

/**
 * guardedTool (P4g Decision 7): the integration tool a captured /mcp tools/call ran, in the form the go engine replays:
 * `args` the JSON text of the arguments the tool was given (taken before the call), `r` the text the tool endpoint
 * (lib/front/tools.js) would answer for this value: JSON.stringify({ r }) (`{}` for undefined), or `{"thrown":true}`
 * when callTool threw (r is THROWN: a tool may well answer { thrown: true } itself). A value JSON cannot hold, or a second call in one request, cannot be replayed (`skip`). Outside
 * a capture it does nothing.
 * @param {string} name
 * @param {string} args
 * @param {any} r callTool's value, or THROWN
 */
function noteTool(name, args, r) {
  const s = als.getStore();
  if (!s || s.answered) return;
  if (s.tool) {
    s.tool.skip = 'more than one tool call in the request';
    return;
  }
  let text = null;
  let skip;
  try {
    text = r === THROWN ? '{"thrown":true}' : JSON.stringify({ r });
  } catch {
    skip = "the tool's value is not JSON";
  }
  s.tool = { name, args, r: text, ...(skip ? { skip } : {}) };
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
  store(
    s.id,
    {
      providerRequests: s.providerRequests,
      providerResponse,
      upstream: s.upstream,
      // The head's compared headers as the response writes them (the same object: a head written after this call
      // still reaches the capture before the gateway asks for it; P4d).
      client: { headers: s.client.headers, ...client },
      usage,
      tape: s.tape,
    },
    now,
  );
}

// A stream's capture keeps at most this many UTF-16 units of the provider's text and of the client's (P4c D5).

/** Whether the request being served is one Go marked (the stream branch collects its text only then; P4c). */
const capturing = () => !!als.getStore();

/**
 * chatCompletion, streaming (P4c): the provider's decoded text and how the stream ended ('eof' or 'cut'), whether the
 * capture passed STREAM_CAP, the client's text and the usage line; stores the capture.
 * @param {{ status: number, body: string, ended: 'eof' | 'cut', truncated: boolean }} providerResponse
 * @param {{ status: number, body: string }} client
 * @param {object} usage
 */
function noteStream(providerResponse, client, usage, now = Date.now()) {
  noteAnswer(providerResponse, client, usage, now);
}

/** The capture of id, once (null when there is none: the request never reached a provider, or it was taken). */
function takeCapture(id) {
  const c = captures.get(id);
  drop(id);
  return c ? c.capture : null;
}

module.exports = {
  withShadow,
  noteProviderRequest,
  noteAnswer,
  noteStream,
  noteClient,
  noteUsage,
  noteTool,
  THROWN,
  capturing,
  takeCapture,
  teeResponse,
  tapeRandom,
  tapeNow,
  configureCaptures,
  SHADOW_ID,
  TTL_MS,
  STREAM_CAP,
};
