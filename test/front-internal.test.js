// The internal listener behind the Go front (P4a spec D3, header fallback): a fake Go client connects to it over a raw
// socket and sends HTTP with an X-ZP-Client header. The checks run through a minimal server built from the real
// library code (createAuth, clientKey, originAllowed, isLocalRequest, isTraceCaller), not a spawned server.js.
const test = require('node:test');
const assert = require('node:assert/strict');
const crypto = require('crypto');
const fs = require('fs');
const http = require('http');
const net = require('net');
const os = require('os');
const path = require('path');
const { createInternalListener, NO_HOST_HEADER } = require('../lib/front/internal');
const { CLIENT_HEADER, signClient, parseClientHeader, peerOf } = require('../lib/front/client');
const { createAuth, clientKey, originAllowed } = require('../lib/auth');
const { isLocalRequest, isTraceCaller, isLoopback } = require('../lib/gateway/client');

const TOKEN = crypto.randomBytes(32).toString('hex');
const OUTSIDE = { ip: '203.0.113.7', port: 51234, tls: false };

// Polls a condition; a test that waits on it never asserts on elapsed time.
async function until(cond, ms = 4000, what = 'condition') {
  const end = Date.now() + ms;
  while (!cond()) {
    if (Date.now() > end) throw new Error(`timed out waiting for ${what}`);
    await new Promise((r) => setTimeout(r, 5));
  }
}

// The internal listener in front of a dashboard server that never listens itself (as in front mode).
async function front(t, handler) {
  const app = http.createServer(handler);
  const logs = [];
  const internal = createInternalListener({ server: app, token: TOKEN, log: (m) => logs.push(m) });
  const port = await internal.listen();
  let connections = 0;
  internal.server.on('connection', () => connections++);
  t.after(() => internal.close());
  return { port, logs, connections: () => connections };
}

// A fake Go: one raw connection, everything it receives, and whether it closed.
async function goConn(t, port) {
  const socket = net.connect(port, '127.0.0.1');
  const conn = { socket, text: '', closed: false };
  socket.on('data', (d) => (conn.text += d));
  socket.on('close', () => (conn.closed = true));
  socket.on('error', () => {});
  t.after(() => socket.destroy());
  await new Promise((r) => socket.once('connect', r));
  return conn;
}
const rawRequest = (pathname, headers = {}) =>
  `GET ${pathname} HTTP/1.1\r\nHost: 127.0.0.1\r\n${Object.entries(headers)
    .map(([k, v]) => `${k}: ${v}\r\n`)
    .join('')}\r\n`;
const signed = (client = OUTSIDE) => ({ [CLIENT_HEADER]: signClient(client, TOKEN) });

// An answer over http.request (one connection per call, no agent), for the tests that read a whole response.
function ask(port, { method = 'GET', pathname = '/', headers = {} } = {}) {
  return new Promise((resolve, reject) => {
    const req = http.request({ host: '127.0.0.1', port, method, path: pathname, headers, agent: false }, (res) => {
      let body = '';
      res.on('data', (c) => (body += c));
      res.on('end', () => resolve({ status: res.statusCode, headers: res.headers, body }));
    });
    req.on('error', reject);
    req.end();
  });
}

test('parseClientHeader: a signed address, port and tls flag; anything else is null', () => {
  assert.deepEqual(parseClientHeader(signClient(OUTSIDE, TOKEN), TOKEN), OUTSIDE);
  const v6 = { ip: '2001:db8:1:2:3::4', port: 65535, tls: true };
  assert.deepEqual(parseClientHeader(signClient(v6, TOKEN), TOKEN), v6);
  const other = crypto.randomBytes(32).toString('hex');
  assert.equal(parseClientHeader(signClient(OUTSIDE, other), TOKEN), null, 'another token');
  assert.equal(parseClientHeader(signClient(OUTSIDE, TOKEN), ''), null, 'no token');
  assert.equal(parseClientHeader(undefined, TOKEN), null, 'missing');
  assert.equal(parseClientHeader(['a'], TOKEN), null, 'not a string');
  const mac = signClient(OUTSIDE, TOKEN).split(' ')[3];
  // A valid MAC moved onto other values is refused (the MAC covers all three).
  for (const v of ['203.0.113.8 51234 0', '203.0.113.7 51235 0', '203.0.113.7 51234 1'])
    assert.equal(parseClientHeader(`${v} ${mac}`, TOKEN), null, v);
  const sign = (text) => `${text} ${crypto.createHmac('sha256', TOKEN).update(text).digest('hex')}`;
  for (const text of [
    '999.1.1.1 1 0',
    'example.com 1 0',
    'fe80::1%en0 1 0',
    '1.2.3.4 0 0',
    '1.2.3.4 65536 0',
    '1.2.3.4 0123 0',
    '1.2.3.4 -1 0',
    '1.2.3.4 1 2',
    '1.2.3.4 1 true',
    '1.2.3.4  1 0',
  ])
    assert.equal(parseClientHeader(sign(text), TOKEN), null, text);
  assert.equal(parseClientHeader(`${sign('1.2.3.4 1 0')} extra`, TOKEN), null, 'a fifth field');
  assert.equal(parseClientHeader(sign('1.2.3.4 1 0').toUpperCase(), TOKEN), null, 'upper-case MAC');
  assert.equal(parseClientHeader(`1.2.3.4 1 0 ${'a'.repeat(300)}`, TOKEN), null, 'over 256 bytes');
});

test('peerOf: without the front slot the socket counts, even when the client sent the header itself', () => {
  const req = { headers: signed(), socket: { remoteAddress: '127.0.0.1', remotePort: 4000, encrypted: undefined } };
  assert.deepEqual(peerOf(req), { address: '127.0.0.1', port: 4000, encrypted: false });
});

test('createInternalListener refuses to start without a server or a real token', () => {
  assert.throws(() => createInternalListener({ token: TOKEN }), /HTTP server/);
  assert.throws(() => createInternalListener({ server: http.createServer(), token: 'short' }), /frontToken/);
});

for (const [name, headers, logged] of [
  ['a missing client header', {}, /no client header/],
  ['a wrong token', { [CLIENT_HEADER]: signClient(OUTSIDE, crypto.randomBytes(32).toString('hex')) }, /a bad/],
  ['a malformed client header', { [CLIENT_HEADER]: 'ZPGW1 x 1.2.3.4 1 0' }, /a bad/],
  ['a client header over 256 bytes', { [CLIENT_HEADER]: `1.2.3.4 1 0 ${'a'.repeat(300)}` }, /a bad/],
]) {
  test(`internal listener: ${name} closes the connection with no reply`, async (t) => {
    let called = 0;
    const f = await front(t, (req, res) => {
      called++;
      res.end('should not be reached');
    });
    const c = await goConn(t, f.port);
    c.socket.write(rawRequest('/', headers));
    await until(() => c.closed, 4000, 'the close');
    assert.equal(c.text, '', 'no bytes back');
    assert.equal(called, 0, 'the dashboard handler never ran');
    assert.match(f.logs.join('\n'), logged);
  });
}

test('internal listener: a connection that sends no request within 2 s is closed', async (t) => {
  const f = await front(t, (req, res) => res.end('x'));
  const c = await goConn(t, f.port);
  c.socket.write('GET / HTTP/1.1\r\n'); // a start that never finishes
  await until(() => c.closed, 5000, 'the 2 s close');
  assert.equal(c.text, '');
  assert.match(f.logs.join('\n'), /within 2 s/);
});

test('internal listener: a malformed request line is closed without the default 400', async (t) => {
  const f = await front(t, (req, res) => res.end('x'));
  const c = await goConn(t, f.port);
  c.socket.write('NOT HTTP AT ALL\r\n\r\n');
  await until(() => c.closed, 4000, 'the close');
  assert.equal(c.text, '');
});

test('internal listener: clientKey sees the address Go named, and the header never reaches the handler', async (t) => {
  const seen = [];
  const f = await front(t, (req, res) => {
    seen.push({ header: req.headers[CLIENT_HEADER], raw: req.rawHeaders.some((h) => /x-zp-client/i.test(h)) });
    res.end(JSON.stringify({ key: clientKey(req, false), peer: peerOf(req) }));
  });
  const v4 = JSON.parse((await ask(f.port, { headers: signed() })).body);
  assert.equal(v4.key, '203.0.113.7');
  assert.deepEqual(v4.peer, { address: '203.0.113.7', port: 51234, encrypted: false });
  const v6 = JSON.parse((await ask(f.port, { headers: signed({ ip: '2001:db8:1:2:3::4', port: 9, tls: true }) })).body);
  assert.equal(v6.key, '2001:db8:1:2::/64', 'IPv6 counted per /64');
  assert.deepEqual(seen, [
    { header: undefined, raw: false },
    { header: undefined, raw: false },
  ]);
});

test('internal listener: the no-Host flag of Go drops the Host Go wrote; the flag never reaches the handler', async (t) => {
  const seen = [];
  const f = await front(t, (req, res) => {
    const raw = req.rawHeaders.filter((_, i) => i % 2 === 0).map((h) => h.toLowerCase());
    seen.push({ host: req.headers.host, flag: req.headers[NO_HOST_HEADER], raw: raw.filter((h) => /host/.test(h)) });
    res.end();
  });
  await ask(f.port, { headers: { ...signed(), [NO_HOST_HEADER]: '1' } });
  await ask(f.port, { headers: { ...signed(), host: 'dash.test' } });
  assert.deepEqual(seen, [
    { host: undefined, flag: undefined, raw: [] },
    { host: 'dash.test', flag: undefined, raw: ['host'] },
  ]);
});

test('internal listener: a forged X-Forwarded-For is not trusted without ZAAFPULSE_TRUST_PROXY', async (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'zp-front-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const auth = createAuth({ dataDir: dir, host: '127.0.0.1', env: { ZAAFPULSE_AUTH: 'local' } });
  const trusting = createAuth({ dataDir: dir, host: '127.0.0.1', env: { ZAAFPULSE_TRUST_PROXY: '1' } });
  const f = await front(t, (req, res) =>
    res.end(JSON.stringify({ plain: auth.actorOf(req).ip, trusting: trusting.actorOf(req).ip })),
  );
  const r = JSON.parse((await ask(f.port, { headers: { ...signed(), 'x-forwarded-for': '127.0.0.1' } })).body);
  assert.equal(r.plain, '203.0.113.7', 'the forged loopback address is ignored');
  assert.equal(r.trusting, '127.0.0.1', 'only a trusted proxy setup reads it (Go passes it on only then)');
});

test('internal listener: an outside client is refused by the /v1/traces rule and is not tagged local', async (t) => {
  const host = '127.0.0.1';
  const f = await front(t, (req, res) => {
    // server.js: the trace return channel answers only isTraceCaller(req) on a loopback host.
    if (!isTraceCaller(req) || !isLoopback(host)) {
      res.writeHead(403);
      return res.end(JSON.stringify({ local: isLocalRequest(req, false) }));
    }
    res.end(JSON.stringify({ local: isLocalRequest(req, false) }));
  });
  const outside = await ask(f.port, { method: 'POST', pathname: '/v1/traces', headers: signed() });
  assert.equal(outside.status, 403, 'Go connects from loopback, but the client is outside');
  assert.deepEqual(JSON.parse(outside.body), { local: false });
  const local = await ask(f.port, {
    method: 'POST',
    pathname: '/v1/traces',
    headers: signed({ ip: '127.0.0.1', port: 50000, tls: false }),
  });
  assert.equal(local.status, 200, 'a client on this machine, named by Go, still reaches it');
  assert.deepEqual(JSON.parse(local.body), { local: true });
});

test('internal listener: the Origin rule reads the Origin header, not the peer', async (t) => {
  // originAllowed has no address input: through Go it decides exactly as it does on the main port. A loopback
  // Origin is allowed only when sign-in is off (local or none mode); in password mode a page must be this site.
  const f = await front(t, (req, res) =>
    res.end(
      JSON.stringify({
        local: originAllowed(req, { mode: 'local' }),
        password: originAllowed(req, { mode: 'password' }),
      }),
    ),
  );
  const loopbackPage = await ask(f.port, { headers: { ...signed(), origin: 'http://localhost:4777' } });
  assert.deepEqual(JSON.parse(loopbackPage.body), { local: true, password: false });
  const otherSite = await ask(f.port, { headers: { ...signed(), origin: 'https://evil.example' } });
  assert.deepEqual(JSON.parse(otherSite.body), { local: false, password: false });
});

test('internal listener: Secure cookies follow the tls flag Go sends', async (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'zp-front-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const auth = createAuth({ dataDir: dir, host: '127.0.0.1', env: { ZAAFPULSE_AUTH: 'local' } });
  const f = await front(t, async (req, res) => {
    if (await auth.handle(req, res, new URL(req.url, 'http://x'))) return;
    res.writeHead(404);
    res.end();
  });
  const logout = (tls) =>
    ask(f.port, {
      method: 'POST',
      pathname: '/api/logout',
      headers: { ...signed({ ...OUTSIDE, tls }), 'content-type': 'application/json' },
    });
  const https = await logout(true);
  assert.equal(https.status, 200);
  assert.ok(https.headers['set-cookie'].length > 0);
  assert.ok(
    https.headers['set-cookie'].every((c) => /; Secure$/.test(c)),
    'tls=1: Secure',
  );
  const plain = await logout(false);
  assert.ok(
    plain.headers['set-cookie'].every((c) => !/Secure/.test(c)),
    'tls=0: not Secure',
  );
});

test('internal listener: keep-alive, three requests from different clients on one connection', async (t) => {
  const f = await front(t, (req, res) => res.end(`<${req.url} ${clientKey(req, false)}>`));
  const c = await goConn(t, f.port);
  const clients = ['203.0.113.7', '198.51.100.20', '127.0.0.1'];
  for (const [i, ip] of clients.entries()) {
    c.socket.write(rawRequest(`/r${i}`, signed({ ip, port: 40000 + i, tls: false })));
    await until(() => c.text.includes(`</r${i} ${ip}>`), 4000, `answer ${i}`);
  }
  assert.equal(c.closed, false, 'still open');
  assert.equal(f.connections(), 1, 'one connection served all three');
});

test('internal listener: SSE events arrive one at a time', async (t) => {
  let release = null;
  const f = await front(t, async (req, res) => {
    res.writeHead(200, { 'content-type': 'text/event-stream', 'cache-control': 'no-store' });
    for (let i = 0; i < 3; i++) {
      res.write(`data: ${i}\n\n`);
      // The next event is written only after the client has seen this one: nothing is held back in a buffer.
      await new Promise((r) => (release = r));
    }
    res.end();
  });
  const got = [];
  let ended = false;
  const req = http.request(
    { host: '127.0.0.1', port: f.port, path: '/events', headers: signed(), agent: false },
    (res) => {
      res.setEncoding('utf8');
      res.on('data', (c) => got.push(c));
      res.on('end', () => (ended = true));
    },
  );
  t.after(() => req.destroy());
  req.end();
  for (let i = 0; i < 3; i++) {
    await until(() => got.join('').includes(`data: ${i}\n\n`), 4000, `event ${i}`);
    assert.ok(!got.join('').includes(`data: ${i + 1}`), `event ${i + 1} not yet sent`);
    await until(() => release, 4000, 'the handler to wait');
    const r = release;
    release = null;
    r();
  }
  await until(() => ended, 4000, 'the end of the stream');
  assert.equal(got.join(''), 'data: 0\n\ndata: 1\n\ndata: 2\n\n');
});
