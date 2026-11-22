// The egress client (P5, docs/superpowers/specs/2026-11-22-p5-egress-design.md Decisions 1, 4, 5, 8, 11, 12, 14):
// the one way the control plane's outbound calls leave the process. Unconfigured (tests, CLI commands, js mode, no
// ready gateway) it is the global fetch and net.connect, looked up at each call. server.js configures it on each ready
// of the gateway with the egress endpoint's port, the derived token and each caller class's switch:
//   direct  the global fetch, as before P5;
//   check   the global fetch, started at once, plus a POST /check to the endpoint (fire and forget) for the allow-list
//           verdict, which the gateway records as "checked" or "would-refuse";
//   go      the request goes to http://127.0.0.1:<port>/ with the target in X-ZP-Egress-URL, and Go makes it.
// A target on localhost, 127.0.0.1 or [::1] always goes direct (the hosts the lock-down grants). Under the lock-down
// (Task 8) a non-loopback call never goes direct: with no endpoint it fails with EGRESS_UNAVAILABLE.
const crypto = require('crypto');
const http = require('http');
const net = require('net');
const { Readable } = require('stream');

const CLASSES = [
  'gateway',
  'connectors',
  'mcp',
  'routers',
  'pricing',
  'evals',
  'agents',
  'notify',
  'brain',
  'public',
  'otel',
  'runtimes',
];
const SWITCHES = ['direct', 'check', 'go'];
const LOOPBACK = ['localhost', '127.0.0.1', '[::1]'];
const TOKEN_LABEL = 'zaafpulse egress v1';
// Answers that carry no body (fetch's null body statuses).
const NULL_BODY = new Set([101, 204, 205, 304]);
// Headers of the loopback leg itself, never the target's.
const HOP = new Set(['connection', 'keep-alive', 'transfer-encoding', 'proxy-connection', 'te', 'trailer', 'upgrade']);
const isDeno = typeof globalThis.Deno !== 'undefined' && !!globalThis.Deno.version;

/** The derived egress token: hex HMAC-SHA256 keyed with the front token over TOKEN_LABEL (gw/egress DeriveToken). */
function deriveToken(frontToken) {
  return crypto.createHmac('sha256', String(frontToken)).update(TOKEN_LABEL).digest('hex');
}

/** A loopback host as URL.hostname writes it (also an IPv6 literal without brackets, as net.connect takes it). */
function isLoopbackHost(hostname) {
  const h = String(hostname || '').toLowerCase();
  return LOOPBACK.includes(h) || h === '::1';
}

/** The fetch failure under the lock-down when no gateway is ready (Decision 14). */
class EgressUnavailable extends TypeError {
  constructor() {
    super('fetch failed', {
      cause: {
        code: 'EGRESS_UNAVAILABLE',
        message: 'the gateway is not running; network egress is locked to it',
      },
    });
    this.name = 'TypeError';
  }
}

/**
 * Each class's switch from config.json `egress.classes`: direct, check or go; anything else (or absent) is direct.
 * @param {any} classes
 * @returns {Record<string, string>}
 */
function switches(classes) {
  const asked = classes && typeof classes === 'object' ? classes : {};
  return Object.fromEntries(CLASSES.map((c) => [c, SWITCHES.includes(asked[c]) ? asked[c] : 'direct']));
}

let st = { configured: false, port: null, token: null, engines: switches(null), lockdown: false };
let agent = new http.Agent({ keepAlive: true });
let log = (line) => console.error(line);
let checkFailed = false;
// The last time a check of each class could not be asked (the endpoint unreachable, or an answer that is not a
// verdict): the gate counts it as a diff (lib/egress-switch.js). In memory only.
let checkErrors = {};

/**
 * Points the client at the gateway's egress endpoint, or back to direct (null).
 * @param {{ port?: number | null, token?: string | null, engines?: Record<string, string>, lockdown?: boolean,
 *   log?: (line: string) => void } | null} opts
 */
function configure(opts) {
  const o = opts || {};
  const port = Number.isInteger(o.port) && o.port > 0 && o.port < 65536 ? o.port : null;
  const token = typeof o.token === 'string' && o.token ? o.token : null;
  if (st.port !== port) {
    agent.destroy(); // pooled connections to an endpoint that is gone
    agent = new http.Agent({ keepAlive: true });
  }
  if (typeof o.log === 'function') log = o.log;
  st = {
    configured: !!(port && token),
    port: port && token ? port : null,
    token: port && token ? token : null,
    engines: switches(o.engines),
    lockdown: o.lockdown === true,
  };
  if (!st.configured) checkFailed = false;
}

/** Per class, when its last check could not be asked (ms), as gate records: [{ ts, purpose, outcome: 'check-error' }]. */
function checkErrorRecords() {
  return Object.entries(checkErrors).map(([purpose, ts]) => ({ ts, purpose, outcome: 'check-error' }));
}

/** The client's state: { configured, port, engines, lockdown } (never the token). */
function state() {
  return { configured: st.configured, port: st.port, engines: { ...st.engines }, lockdown: st.lockdown };
}

const urlOf = (input) =>
  typeof Request !== 'undefined' && input instanceof Request
    ? input.url
    : input instanceof URL
      ? input.href
      : String(input);

// How a call of this class to this host goes: 'direct', 'check', 'go' or 'unavailable'.
function routeOf(cls, hostname) {
  if (isLoopbackHost(hostname)) return 'direct';
  if (st.lockdown) return st.configured ? 'go' : 'unavailable';
  if (!st.configured) return 'direct';
  return st.engines[cls] || 'direct';
}

const purposeOf = (cls, detail) => (detail ? `${cls}:${detail}` : String(cls));

function abortReason(signal) {
  if (signal && signal.reason !== undefined) return signal.reason;
  return new DOMException('This operation was aborted', 'AbortError');
}

function fetchFailed(cause) {
  return new TypeError('fetch failed', { cause });
}

// The headers the runtime's own fetch adds when the caller did not (Decision 8), measured: Node's undici sends accept,
// accept-language, sec-fetch-mode and user-agent "node"; Deno sends accept, accept-language and "Deno/<version>".
function addDefaults(h) {
  if (!h.has('accept')) h.set('accept', '*/*');
  if (!h.has('accept-language')) h.set('accept-language', '*');
  if (!isDeno && !h.has('sec-fetch-mode')) h.set('sec-fetch-mode', 'cors');
  if (!h.has('user-agent')) h.set('user-agent', isDeno ? `Deno/${globalThis.Deno.version.deno}` : 'node');
}

// The egress headers of a call.
function egressHeaders(url, purpose, redirect, pub) {
  return {
    'x-zp-egress-token': st.token,
    'x-zp-egress-url': url,
    'x-zp-egress-purpose': purpose,
    'x-zp-egress-redirect': redirect,
    ...(pub ? { 'x-zp-egress-public': '1' } : {}),
  };
}

// Whether a request body is a stream (sent as it is read, chunked) rather than bytes with a known length.
const isStreamBody = (b) =>
  b != null &&
  ((typeof ReadableStream !== 'undefined' && b instanceof ReadableStream) ||
    (typeof b === 'object' && typeof b[Symbol.asyncIterator] === 'function' && !(b instanceof URLSearchParams)));

/** One call on 'go': the request to the endpoint, the answer as a WHATWG Response. */
async function goFetch(input, init, purpose) {
  const { egressPublic, ...rest } = init || {};
  const req = new Request(input, rest);
  const signal = rest.signal || (input instanceof Request ? input.signal : null);
  if (signal && signal.aborted) throw abortReason(signal);
  const h = new Headers(req.headers);
  addDefaults(h);
  const method = req.method;
  let body = null;
  if (req.body) {
    if (isStreamBody(rest.body)) body = Readable.fromWeb(/** @type {any} */ (req.body));
    else body = Buffer.from(await req.arrayBuffer());
  }
  if (Buffer.isBuffer(body)) h.set('content-length', String(body.length));
  else if (!body && (method === 'POST' || method === 'PUT')) h.set('content-length', '0');
  const headers = {};
  for (const [k, v] of h) headers[k] = k === 'set-cookie' ? h.getSetCookie() : v;
  Object.assign(headers, egressHeaders(req.url, purpose, rest.redirect || 'follow', egressPublic === true));
  return new Promise((resolve, reject) => {
    let settled = false;
    let res = null;
    const onAbort = () => {
      const reason = abortReason(signal);
      if (res) res.destroy(reason);
      creq.destroy(reason);
      if (!settled) {
        settled = true;
        reject(reason);
      }
    };
    const creq = http.request({ host: '127.0.0.1', port: st.port, method, path: '/', headers, agent }, (answer) => {
      res = answer;
      const ok = (r) => {
        settled = true;
        resolve(r);
      };
      const fail = (e) => {
        settled = true;
        if (signal) signal.removeEventListener('abort', onAbort);
        reject(e);
      };
      const status = answer.statusCode || 0;
      const code = answer.headers['x-zp-egress-error'];
      if (code && !answer.headers['x-zp-egress-url']) {
        // A failure of the endpoint (Decision 5): fetch's TypeError with { code, message }.
        const chunks = [];
        answer.on('data', (c) => chunks.push(c));
        answer.on('error', (e) => fail(fetchFailed(e)));
        answer.on('end', () => {
          let message = String(code);
          try {
            message = JSON.parse(Buffer.concat(chunks).toString('utf8')).message || message;
          } catch {
            /* the code alone */
          }
          fail(fetchFailed({ code: String(code), message }));
        });
        return;
      }
      if (status < 200 || status > 599) {
        answer.destroy();
        return fail(fetchFailed({ code: 'EPROTO', message: `the target answered status ${status}` }));
      }
      const out = new Headers();
      const raw = answer.rawHeaders;
      for (let i = 0; i < raw.length; i += 2) {
        const k = raw[i].toLowerCase();
        if (HOP.has(k) || k.startsWith('x-zp-egress-')) continue;
        out.append(raw[i], raw[i + 1]);
      }
      let stream = null;
      if (NULL_BODY.has(status) || method === 'HEAD') answer.resume();
      else stream = Readable.toWeb(answer);
      answer.on('close', () => {
        if (signal) signal.removeEventListener('abort', onAbort);
      });
      let response;
      try {
        response = new Response(/** @type {any} */ (stream), {
          status,
          statusText: answer.statusMessage || '',
          headers: out,
        });
      } catch (e) {
        answer.destroy();
        return fail(fetchFailed(e));
      }
      Object.defineProperty(response, 'url', { value: String(answer.headers['x-zp-egress-url'] || req.url) });
      Object.defineProperty(response, 'redirected', { value: answer.headers['x-zp-egress-redirected'] === '1' });
      ok(response);
    });
    creq.on('error', (e) => {
      if (settled) return;
      settled = true;
      if (signal) signal.removeEventListener('abort', onAbort);
      reject(signal && signal.aborted ? abortReason(signal) : fetchFailed(e));
    });
    if (signal) signal.addEventListener('abort', onAbort, { once: true });
    if (body && !Buffer.isBuffer(body)) {
      body.on('error', (e) => creq.destroy(e));
      body.pipe(creq);
    } else creq.end(body || undefined);
  });
}

/** 'check': the allow-list verdict for a call made direct, asked in the background; a failure is logged once. */
function askCheck(cls, input, init, purpose) {
  const failed = (message) => {
    checkErrors[cls] = Date.now();
    if (checkFailed) return;
    checkFailed = true;
    log(`egress: the check of a ${purpose} call could not be asked: ${message}`);
  };
  try {
    const { egressPublic, ...rest } = init || {};
    const req = new Request(input, { method: rest.method, headers: rest.headers });
    const headers = {
      ...egressHeaders(req.url, purpose, rest.redirect || 'follow', egressPublic === true),
      'x-zp-egress-method': req.method,
      'content-length': '0',
    };
    const creq = http.request({ host: '127.0.0.1', port: st.port, method: 'POST', path: '/check', headers, agent });
    creq.on('response', (res) => {
      res.resume();
      // 204 checked, 403 would-refuse (both recorded by the gateway); anything else is no verdict.
      if (res.statusCode !== 204 && res.statusCode !== 403) failed(`the endpoint answered ${res.statusCode}`);
    });
    creq.on('error', (e) => failed(e.message));
    creq.end();
  } catch (e) {
    failed(e.message);
  }
}

// The global fetch at call time (a test's stub of globalThis.fetch applies), without the client's own init field.
function direct(input, init) {
  if (init && 'egressPublic' in init) {
    const { egressPublic, ...rest } = init;
    return globalThis.fetch(input, rest);
  }
  return globalThis.fetch(input, init);
}

/**
 * A fetch-compatible function for a caller class; the route is decided at each call, so a later configure applies.
 * init may carry egressPublic: true (public mode: Go refuses private addresses at connect, Decision 7).
 * @param {string} cls one of CLASSES
 * @param {string} [detail] e.g. the connector type, sent as "<cls>:<detail>"
 * @returns {(input: any, init?: any) => Promise<Response>}
 */
function fetchFor(cls, detail) {
  const purpose = purposeOf(cls, detail);
  return async (input, init) => {
    let hostname = '';
    try {
      hostname = new URL(urlOf(input)).hostname;
    } catch {
      return direct(input, init); // fetch gives the error for an URL it cannot parse
    }
    switch (routeOf(cls, hostname)) {
      case 'go':
        return goFetch(input, init, purpose);
      case 'check': {
        const p = direct(input, init);
        askCheck(cls, input, init, purpose);
        return p;
      }
      case 'unavailable':
        throw new EgressUnavailable();
      default:
        return direct(input, init);
    }
  };
}

/**
 * A Response's headers as node:http gives them on res.headers: lower-case names, repeated values joined with ', ',
 * set-cookie an array. For the callers that moved from node:http to fetchFor (P5 E12–E14) and keep their shapes.
 * @param {Headers} h
 * @returns {Record<string, string | string[]>}
 */
function nodeHeaders(h) {
  const out = {};
  for (const [k, v] of h) if (k !== 'set-cookie') out[k] = v;
  const cookies = typeof h.getSetCookie === 'function' ? h.getSetCookie() : [];
  if (cookies.length) out['set-cookie'] = cookies;
  return out;
}

/**
 * For a caller that makes its call itself, not through fetchFor (lib/net.js safeFetch, direct): when its class is on
 * 'check', asks /check for the call's verdict in the background, as fetchFor does; otherwise nothing.
 * @param {string} cls
 * @param {string} url
 * @param {any} [init] method, headers, redirect, egressPublic
 * @param {string} [detail]
 */
function check(cls, url, init, detail) {
  let hostname = '';
  try {
    hostname = new URL(String(url)).hostname;
  } catch {
    return;
  }
  if (routeOf(cls, hostname) === 'check') askCheck(cls, String(url), init, purposeOf(cls, detail));
}

// net.connect, resolved once connected.
function plainConnect(host, port) {
  return new Promise((resolve, reject) => {
    const s = net.connect({ host, port });
    s.once('connect', () => {
      s.off('error', reject);
      resolve(s);
    });
    s.once('error', reject);
  });
}

/**
 * A TCP connection for a caller class (E11, Postgres): through the endpoint's CONNECT tunnel on 'go' (or under the
 * lock-down), else net.connect; resolves with the socket once connected. A refusal rejects with an Error whose code
 * is the endpoint's (ECONNREFUSED, EGRESS_REFUSED, ...).
 * @param {string} cls
 * @param {string} host
 * @param {number} port
 * @param {string} [detail]
 * @returns {Promise<import('net').Socket>}
 */
function connect(cls, host, port, detail) {
  const route = routeOf(cls, host);
  if (route === 'unavailable')
    return Promise.reject(Object.assign(new EgressUnavailable(), { code: 'EGRESS_UNAVAILABLE' }));
  if (route !== 'go') return plainConnect(host, port);
  const authority = net.isIPv6(host) ? `[${host}]:${port}` : `${host}:${port}`;
  const purpose = purposeOf(cls, detail);
  return new Promise((resolve, reject) => {
    const s = net.connect({ host: '127.0.0.1', port: st.port });
    let buf = Buffer.alloc(0);
    let done = false;
    const fail = (e) => {
      if (done) return;
      done = true;
      s.destroy();
      reject(e);
    };
    const onError = (e) => fail(e);
    const onClose = () =>
      fail(Object.assign(new Error('the egress endpoint closed the tunnel'), { code: 'ECONNRESET' }));
    s.once('error', onError);
    s.once('close', onClose);
    s.once('connect', () => {
      s.write(
        `CONNECT ${authority} HTTP/1.1\r\nHost: ${authority}\r\nX-ZP-Egress-Token: ${st.token}\r\n` +
          `X-ZP-Egress-Purpose: ${purpose}\r\n\r\n`,
      );
    });
    // Read in paused mode ('readable'): once the listener is gone the socket is neither flowing nor paused, so the
    // caller's own 'data' listener starts it, with the bytes after the answer's head put back first.
    const onReadable = () => {
      let chunk;
      while (!done && (chunk = s.read()) !== null) {
        buf = Buffer.concat([buf, chunk]);
        answer();
      }
    };
    const answer = () => {
      const end = buf.indexOf('\r\n\r\n');
      if (end < 0) {
        if (buf.length > 64 * 1024) fail(Object.assign(new Error('the egress answer is too long'), { code: 'EPROTO' }));
        return;
      }
      const head = buf.subarray(0, end).toString('latin1');
      const status = Number(/^HTTP\/1\.[01] (\d{3})/.exec(head)?.[1] || 0);
      if (status === 200) {
        done = true;
        s.off('readable', onReadable);
        s.off('error', onError);
        s.off('close', onClose);
        const rest = buf.subarray(end + 4);
        if (rest.length) s.unshift(rest);
        resolve(s);
        return;
      }
      // A refusal: the body is {code, message} with its Content-Length.
      const len = Number(/\r\ncontent-length:\s*(\d+)/i.exec(head)?.[1] || -1);
      const body = buf.subarray(end + 4);
      if (len >= 0 && body.length < len) return;
      let info = { code: 'EPROTO', message: `the egress endpoint answered ${status || 'garbage'}` };
      try {
        info = { ...info, ...JSON.parse(body.subarray(0, len >= 0 ? len : body.length).toString('utf8')) };
      } catch {
        /* the status alone */
      }
      fail(Object.assign(new Error(info.message), { code: info.code }));
    };
    s.on('readable', onReadable);
  });
}

module.exports = {
  CLASSES,
  LOOPBACK,
  deriveToken,
  configure,
  state,
  checkErrorRecords,
  fetchFor,
  check,
  nodeHeaders,
  connect,
  isLoopbackHost,
  switches,
  EgressUnavailable,
};
