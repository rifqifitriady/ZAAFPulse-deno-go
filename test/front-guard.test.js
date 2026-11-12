// The internal listener's review hand-offs (P4a T5): without sign-in a client Go names from another machine is refused
// even when it writes "Host: 127.0.0.1" (H1); Expect: 100-continue gets its 100 only after the client header checks
// out; a connection may wait for its first request as long as createFront asks (Go's idle pool). Driven through the
// real listener with raw sockets, as Go would connect.
const test = require('node:test');
const assert = require('node:assert/strict');
const crypto = require('crypto');
const fs = require('fs');
const http = require('http');
const net = require('net');
const path = require('path');
const { createInternalListener } = require('../lib/front/internal');
const { CLIENT_HEADER, signClient } = require('../lib/front/client');
const { peerMayConnect } = require('../lib/auth');

const TOKEN = crypto.randomBytes(32).toString('hex');
const LAN = { ip: '192.168.1.20', port: 50000, tls: false };
const LOCAL = { ip: '127.0.0.1', port: 50001, tls: false };

async function listener(t, handler, opts = {}) {
  const app = http.createServer(handler);
  const internal = createInternalListener({ server: app, token: TOKEN, ...opts });
  const port = await internal.listen();
  t.after(() => internal.close());
  return port;
}

/** Sends `text` on a new connection, waits `delayMs` first when given; resolves with everything read until close. */
function raw(port, text, { delayMs = 0, onData } = {}) {
  return new Promise((resolve, reject) => {
    const c = net.connect(port, '127.0.0.1');
    let out = '';
    c.setTimeout(10000, () => c.destroy(new Error('no answer within 10 s')));
    c.on('data', (d) => {
      out += d;
      if (onData) onData(out, c);
    });
    c.on('error', reject);
    c.on('close', () => resolve(out));
    c.on('connect', () => setTimeout(() => c.write(text), delayMs));
  });
}

const get = (client, host = '127.0.0.1') =>
  `GET /api/x HTTP/1.1\r\nHost: ${host}\r\n${CLIENT_HEADER}: ${signClient(client, TOKEN)}\r\nConnection: close\r\n\r\n`;

test('H1: without sign-in a LAN client named by Go is refused, even with Host: 127.0.0.1; a local one passes', async (t) => {
  for (const mode of ['none', 'local']) {
    const port = await listener(t, (req, res) => {
      res.writeHead(peerMayConnect(req, mode) ? 200 : 403);
      res.end();
    });
    assert.match(await raw(port, get(LAN)), /^HTTP\/1\.1 403/, mode);
    assert.match(await raw(port, get(LOCAL)), /^HTTP\/1\.1 200/, mode);
  }
  const port = await listener(t, (req, res) => {
    res.writeHead(peerMayConnect(req, 'password') ? 200 : 403);
    res.end();
  });
  assert.match(await raw(port, get(LAN)), /^HTTP\/1\.1 200/, 'password mode: sign-in decides');
});

test('H1: server.js refuses such a peer right after its host check, before any route', () => {
  const src = fs.readFileSync(path.join(__dirname, '..', 'server.js'), 'utf8');
  const guard = src.indexOf('if (!peerMayConnect(req, auth.mode)) return send(res, 403');
  assert.ok(guard > 0, 'the guard is in the handler');
  assert.ok(guard < src.indexOf('!originAllowed(req, {'), 'before the Origin rule');
  assert.ok(guard < src.indexOf("url.pathname === '/v1/traces'"), 'before the first route');
});

test('Expect: 100-continue: no 100 for a bad client header; 100, then the answer, for a good one', async (t) => {
  const port = await listener(t, (req, res) => {
    let n = 0;
    req.on('data', (c) => (n += c.length));
    req.on('end', () => res.end(`got ${n}`));
  });
  const ask = (header) =>
    `POST /x HTTP/1.1\r\nHost: 127.0.0.1\r\n${CLIENT_HEADER}: ${header}\r\nContent-Length: 5\r\n` +
    'Expect: 100-continue\r\nConnection: close\r\n\r\n';
  assert.equal(await raw(port, ask('127.0.0.1 1 0 ' + '0'.repeat(64))), '', 'closed without a byte');
  const out = await raw(port, ask(signClient(LOCAL, TOKEN)), {
    onData: (seen, c) => {
      if (seen.startsWith('HTTP/1.1 100') && !c.sentBody) {
        c.sentBody = true;
        c.write('hello');
      }
    },
  });
  assert.match(out, /^HTTP\/1\.1 100 Continue\r\n\r\nHTTP\/1\.1 200 OK/);
  assert.match(out, /got 5$/);
});

test('firstRequestMs: a connection opened before its first request is kept open as long as asked', async (t) => {
  const port = await listener(t, (req, res) => res.end('ok'), { firstRequestMs: 30000 });
  // Go's spare pooled connection: opened now, first used 2.5 s later (past the default 2 s).
  assert.match(await raw(port, get(LOCAL), { delayMs: 2500 }), /^HTTP\/1\.1 200[\s\S]*ok$/);
});
