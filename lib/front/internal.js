// @ts-check
// Deno's internal listener for the Go front (P4a spec D1, D3 header fallback). It binds 127.0.0.1 on a port the OS
// picks; only Go connects. Every request must carry an X-ZP-Client header signed with the frontToken
// (lib/front/client.js). A request without one, or with a wrong one, gets no reply: the connection is closed. A
// connection that sends no complete request within 2 s is closed too. A good request goes to the dashboard server's
// own request handler (server.emit('request')) with the client stored on it for peerAddress() and peerEncrypted(),
// and with the header removed, so nothing downstream (a provider the gateway forwards to) ever sees it.
const http = require('http');
const { CLIENT_HEADER, FRONT_PEER, parseClientHeader } = require('./client');

// Go's flag on a request that came without a Host header (gw/front/front.go NoHostHeader).
const NO_HOST_HEADER = 'x-zp-no-host';

const FIRST_REQUEST_MS = 2000;
// Longer than Go's idle timeout for its upstream connections (120 s, spec D2), so Go always closes an idle
// connection first and never sends a request on one Deno is closing.
const KEEP_ALIVE_MS = 130_000;

/**
 * @param {{ server: import('events').EventEmitter, token: string, log?: (msg: string) => void,
 *   firstRequestMs?: number }} opts firstRequestMs: how long a connection may stay open before its first request
 * @returns {{ listen(): Promise<number>, close(): Promise<void>, server: import('http').Server }}
 */
function createInternalListener({ server, token, log = () => {}, firstRequestMs = FIRST_REQUEST_MS }) {
  if (!server || typeof server.emit !== 'function') throw new Error('createInternalListener needs the HTTP server');
  if (typeof token !== 'string' || token.length < 32) throw new Error('createInternalListener needs the frontToken');
  const waiting = new WeakMap(); // socket -> the timer that closes it if no request comes
  // The header check for a request, then onOk() (the 100 Continue, when asked for) and the dashboard's handler.
  /** @param {import('http').IncomingMessage} req @param {import('http').ServerResponse} res @param {() => void} onOk */
  const accept = (req, res, onOk) => {
    const timer = waiting.get(req.socket);
    if (timer) {
      clearTimeout(timer);
      waiting.delete(req.socket);
    }
    const client = parseClientHeader(req.headers[CLIENT_HEADER], token);
    if (!client) {
      log(`front: closed a connection with ${req.headers[CLIENT_HEADER] === undefined ? 'no' : 'a bad'} client header`);
      req.socket.destroy();
      return;
    }
    // @ts-ignore: a symbol slot of our own on the request
    req[FRONT_PEER] = client;
    // The client sent no Host (HTTP/1.0): the Host Go had to write is its own 127.0.0.1:<this port>, and the host
    // check must see none, as on the main port in js mode (P4a final review I1). Go always sets or removes the flag.
    const drop = [CLIENT_HEADER, NO_HOST_HEADER, ...(req.headers[NO_HOST_HEADER] === '1' ? ['host'] : [])];
    for (const h of drop) delete req.headers[h];
    for (let i = req.rawHeaders.length - 2; i >= 0; i -= 2)
      if (drop.includes(req.rawHeaders[i].toLowerCase())) req.rawHeaders.splice(i, 2);
    onOk();
    server.emit('request', req, res);
  };
  const front = http.createServer((req, res) => accept(req, res, () => {}));
  // Expect: 100-continue gets its 100 only once the client header checks out, never before (P4a review).
  front.on('checkContinue', (req, res) => accept(req, res, () => res.writeContinue()));
  front.keepAliveTimeout = KEEP_ALIVE_MS;
  front.on('connection', (socket) => {
    const timer = setTimeout(() => {
      waiting.delete(socket);
      log(`front: closed a connection that sent no request within ${firstRequestMs / 1000} s`);
      socket.destroy();
    }, firstRequestMs);
    waiting.set(socket, timer);
    socket.once('close', () => {
      clearTimeout(timer);
      waiting.delete(socket);
    });
  });
  // A malformed request: close without the default 400 reply, like a bad header.
  front.on('clientError', (_err, socket) => socket.destroy());
  return {
    server: front,
    listen: () =>
      new Promise((resolve, reject) => {
        front.once('error', reject);
        front.listen(0, '127.0.0.1', () => {
          front.removeListener('error', reject);
          const addr = front.address();
          resolve(typeof addr === 'object' && addr ? addr.port : 0);
        });
      }),
    close: () =>
      new Promise((resolve) => {
        if (!front.listening) return resolve();
        front.closeAllConnections?.();
        front.close(() => resolve());
      }),
  };
}

module.exports = { createInternalListener, FIRST_REQUEST_MS, NO_HOST_HEADER };
