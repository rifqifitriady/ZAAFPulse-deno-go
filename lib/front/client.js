// @ts-check
// The real client of a request that came through the Go front (P4a spec D3, header fallback). Go connects to Deno's
// internal listener from loopback, so the TCP peer says nothing about the client. Go names the client in one header:
//
//   X-ZP-Client: <client-ip> <client-port> <tls:0|1> <hmac>
//
// where <hmac> is the lowercase hex HMAC-SHA256 of "<client-ip> <client-port> <tls>" keyed with the frontToken (its
// hex text as UTF-8 bytes). Only the internal listener (lib/front/internal.js) reads the header; it stores the result
// on the request under FRONT_PEER, and peerAddress() and peerEncrypted() are what every check reads: clientKey, Secure cookies, the /v1/traces
// loopback rule, the gateway's local-request tag. A request on the main port never has FRONT_PEER, so a client that
// sends the header itself changes nothing.
//
// Why a header and not a connection preamble: on Deno 2.9.8, an http.Server handed a socket with
// emit('connection') answers the first request and never the next one on that connection (keep-alive breaks, even
// with no preamble at all). Go's connection pool also shares one upstream connection between clients, which a
// per-connection preamble cannot describe. (Probe result of P4a Task 3.)
const crypto = require('crypto');
const net = require('net');

const CLIENT_HEADER = 'x-zp-client';
const MAX_HEADER = 256;
const FRONT_PEER = Symbol.for('zaafpulse.front.peer');

/** @param {string} text @param {string} token */
const macOf = (text, token) => crypto.createHmac('sha256', token).update(text).digest();

/**
 * The header value Go sends (and the tests' fake Go).
 * @param {{ ip: string, port: number, tls: boolean }} client
 * @param {string} token
 */
function signClient({ ip, port, tls }, token) {
  const text = `${ip} ${port} ${tls ? 1 : 0}`;
  return `${text} ${macOf(text, token).toString('hex')}`;
}

/**
 * The client a header names, or null when it is missing, malformed, too long or not signed with this token.
 * @param {unknown} value
 * @param {string} token
 * @returns {{ ip: string, port: number, tls: boolean } | null}
 */
function parseClientHeader(value, token) {
  if (typeof value !== 'string' || !value || value.length > MAX_HEADER) return null;
  if (typeof token !== 'string' || !token) return null;
  const parts = value.split(' ');
  if (parts.length !== 4) return null;
  const [ip, portText, tlsText, macText] = parts;
  if (!net.isIP(ip) || ip.includes('%')) return null;
  if (!/^[1-9]\d{0,4}$/.test(portText) || Number(portText) > 65535) return null;
  if (tlsText !== '0' && tlsText !== '1') return null;
  if (!/^[0-9a-f]{64}$/.test(macText)) return null;
  const want = macOf(`${ip} ${portText} ${tlsText}`, token);
  if (!crypto.timingSafeEqual(want, Buffer.from(macText, 'hex'))) return null;
  return { ip, port: Number(portText), tls: tlsText === '1' };
}

// The peer a request's checks must use: the client Go named, else the TCP peer. Each field is read on its own, so a
// check that needs only one never touches the others.
/** @param {any} req @returns {string | undefined} */
function peerAddress(req) {
  const front = req && req[FRONT_PEER];
  return front ? front.ip : req && req.socket ? req.socket.remoteAddress : undefined;
}
/** @param {any} req @returns {boolean} */
function peerEncrypted(req) {
  const front = req && req[FRONT_PEER];
  return front ? front.tls : !!(req && req.socket && req.socket.encrypted);
}
/** @param {any} req @returns {{ address: string | undefined, port: number | undefined, encrypted: boolean }} */
function peerOf(req) {
  const front = req && req[FRONT_PEER];
  const port = front ? front.port : req && req.socket ? req.socket.remotePort : undefined;
  return { address: peerAddress(req), port, encrypted: peerEncrypted(req) };
}

module.exports = {
  CLIENT_HEADER,
  FRONT_PEER,
  MAX_HEADER,
  signClient,
  parseClientHeader,
  peerAddress,
  peerEncrypted,
  peerOf,
};
