// @ts-check
// Which coding tool sent a gateway request, and whether it came from this machine. Claude Code or Codex here keeps its
// own logs, which the cost ledger counts already, so their gateway lines are tagged and the ledger skips them. Only the
// label is stored, never the raw User-Agent.
const { peerAddress } = require('../front/client');

function clientOf(userAgent) {
  if (typeof userAgent !== 'string' || !userAgent) return null;
  if (/^claude-cli\//i.test(userAgent)) return 'claude-code';
  if (/^codex_cli_rs\//i.test(userAgent) || /^codex\//i.test(userAgent)) return 'codex';
  return null;
}

// 127.0.0.0/8, ::1 and the IPv4-mapped form of 127.x (a dual-stack listener reports that).
const LOOPBACK = /^(?:::ffff:)?127\.\d{1,3}\.\d{1,3}\.\d{1,3}$/i;
const isLoopback = (address) => typeof address === 'string' && (address === '::1' || LOOPBACK.test(address));

// A proxy or tunnel on this host (nginx, Caddy, tailscale serve, cloudflared, ngrok) connects from loopback too, so
// any of these headers means the caller may be elsewhere (the /v1/traces rule in server.js, plus the CDN headers).
const PROXY_HEADERS = [
  'forwarded',
  'x-forwarded-for',
  'x-real-ip',
  'cf-connecting-ip',
  'true-client-ip',
  'x-forwarded-host',
  'x-forwarded-proto',
  'x-forwarded-port',
  'via',
];
// The headers that name the caller's address; `forwarded` names it in each `for=` part.
const ADDRESS_HEADERS = ['x-forwarded-for', 'x-real-ip', 'cf-connecting-ip', 'true-client-ip'];

// One forwarded address as an address: no quotes, no [brackets] and no port.
function bare(v) {
  const a = String(v).trim().replace(/^"|"$/g, '');
  const v6 = /^\[([^\]]*)\](?::\d+)?$/.exec(a);
  if (v6) return v6[1];
  return /^\d{1,3}(?:\.\d{1,3}){3}:\d+$/.test(a) ? a.replace(/:\d+$/, '') : a;
}
const header = (h) => (Array.isArray(h) ? h.join(',') : typeof h === 'string' ? h : '');
function forwardedAddresses(headers) {
  const out = [];
  for (const h of ADDRESS_HEADERS) if (headers[h] !== undefined) out.push(...header(headers[h]).split(','));
  for (const part of header(headers.forwarded).split(/[,;]/)) {
    const m = /^\s*for\s*=\s*(.*)$/i.exec(part);
    if (m) out.push(m[1]);
  }
  return out.map(bare);
}

// Whether a request came from this machine. The socket must be loopback. With no forwarding header that is enough.
// Behind a trusted proxy (ZAAFPULSE_TRUST_PROXY) a request with X-Forwarded-For is local only when every address the
// proxies name is loopback: a tunnel in front of a local proxy leaves the remote address on the left of a loopback hop.
// Anything else counts as remote, so the ledger keeps the line: counting twice is visible, a dropped cost is not.
// Through the Go front the peer is the client Go named (lib/front/client.js), never Go's own loopback connection.
function isLocalRequest(req, trustProxy) {
  if (!isLoopback(peerAddress(req))) return false;
  const headers = (req && req.headers) || {};
  if (!PROXY_HEADERS.some((h) => headers[h] !== undefined)) return true;
  if (!trustProxy || headers['x-forwarded-for'] === undefined) return false;
  const named = forwardedAddresses(headers);
  return named.length > 0 && named.every(isLoopback);
}

// The optional usage-line fields for one request: `client` when it is a known tool, `local: true` from this machine.
function clientTag(req, { trustProxy = false } = {}) {
  const client = clientOf(req && req.headers && req.headers['user-agent']);
  const local = isLocalRequest(req, trustProxy);
  return { ...(client ? { client } : {}), ...(local ? { local: true } : {}) };
}

// The /v1/traces return channel (server.js) answers this machine only: a loopback peer (through the Go front, the
// client Go named) and none of these headers, which mean a proxy on this machine passed the call on.
const TRACE_PROXY_HEADERS = [
  'x-forwarded-for',
  'x-forwarded-host',
  'x-forwarded-proto',
  'x-forwarded-port',
  'forwarded',
  'x-real-ip',
  'via',
];
function isTraceCaller(req) {
  if (!['127.0.0.1', '::1', '::ffff:127.0.0.1'].includes(/** @type {string} */ (peerAddress(req)))) return false;
  const headers = (req && req.headers) || {};
  return !TRACE_PROXY_HEADERS.some((h) => headers[h] !== undefined);
}

module.exports = { clientOf, isLoopback, isLocalRequest, isTraceCaller, clientTag };
