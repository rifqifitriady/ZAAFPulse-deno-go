// The transports that moved to the egress client in P5 Task 5 (spec E11–E14), through a fake endpoint written here
// in JS (as test/egress-client.test.js): safeFetch in public mode (lib/net.js), the Postgres tunnel (lib/brain/pg.js),
// the OTLP POST (lib/otel.js) and the local runtimes' calls (lib/runtimes/models.js, state.js). Every outside name
// below only ever reaches the fake endpoint; no test resolves a name or leaves the machine.
const test = require('node:test');
const assert = require('node:assert/strict');
const http = require('http');
const net = require('net');
const egress = require('../lib/egress');
const { safeFetch } = require('../lib/net');
const pg = require('../lib/brain/pg');
const { postOtlp } = require('../lib/otel');
const { getJson, postJson } = require('../lib/runtimes/models');
const { probe } = require('../lib/runtimes/state');

const TOKEN = egress.deriveToken('front-token');

// An HTTP fake endpoint: handler(req, body, res) answers as Go would; requests are kept in `seen`.
async function fakeEndpoint(t, handler) {
  const seen = [];
  const held = [];
  const server = http.createServer((req, res) => {
    held.push(res);
    const chunks = [];
    req.on('data', (c) => chunks.push(c));
    req.on('end', () => {
      seen.push({ method: req.method, url: req.url, headers: req.headers });
      handler(req, Buffer.concat(chunks), res);
    });
  });
  await new Promise((r) => server.listen(0, '127.0.0.1', r));
  t.after(async () => {
    egress.configure(null);
    for (const r of held) r.destroy();
    server.closeAllConnections();
    await new Promise((r) => server.close(r));
  });
  return { seen, port: server.address().port };
}

// Go's answer to a forwarded call: the target's status and headers, plus the final URL.
const answer = (req, res, status, headers = {}, body = '') => {
  res.writeHead(status, { 'x-zp-egress-url': req.headers['x-zp-egress-url'], ...headers });
  res.end(body);
};
// Go's failure: 502 with the code and { code, message }.
const failure = (res, code, message) => {
  res.writeHead(502, { 'x-zp-egress-error': code, 'content-type': 'application/json' });
  res.end(JSON.stringify({ code, message }));
};
const go = (port, cls) => egress.configure({ port, token: TOKEN, engines: { [cls]: 'go' }, lockdown: false });

/* ---------- safeFetch, public mode ---------- */

test('safeFetch on go: each hop through the endpoint in public mode, manual redirects, the final page', async (t) => {
  const ep = await fakeEndpoint(t, (req, body, res) => {
    const u = new URL(req.headers['x-zp-egress-url']);
    if (u.pathname === '/start') return answer(req, res, 302, { location: '/page' });
    answer(req, res, 200, { 'content-type': 'text/plain' }, 'hello');
  });
  go(ep.port, 'public');
  const r = await safeFetch('https://site.example.test/start');
  assert.equal(r.text, 'hello');
  assert.equal(r.url, 'https://site.example.test/page');
  assert.equal(ep.seen.length, 2);
  for (const s of ep.seen) {
    assert.equal(s.headers['x-zp-egress-public'], '1');
    assert.equal(s.headers['x-zp-egress-redirect'], 'manual');
    assert.equal(s.headers['x-zp-egress-purpose'], 'public');
  }
});

test('safeFetch on go: a private literal or a loopback name is refused in JS, before the endpoint', async (t) => {
  const ep = await fakeEndpoint(t, (req, body, res) => answer(req, res, 200, {}, 'no'));
  go(ep.port, 'public');
  for (const u of ['http://10.0.0.1/', 'http://[fd00::1]/', 'http://169.254.169.254/latest', 'http://[::1]:8080/'])
    await assert.rejects(safeFetch(u), /points to a private or local address/, u);
  for (const u of ['http://localhost:8080/', 'http://localhost.:8080/', 'http://127.0.0.1/'])
    await assert.rejects(safeFetch(u), /points to a private or local address/, u);
  assert.equal(ep.seen.length, 0);
});

test('safeFetch on go: a redirect to 127.0.0.1 or [::1] is refused before it is followed', async (t) => {
  const ep = await fakeEndpoint(t, (req, body, res) => {
    const u = new URL(req.headers['x-zp-egress-url']);
    answer(req, res, 302, { location: u.pathname === '/v4' ? 'http://127.0.0.1/admin' : 'http://[::1]:9/admin' });
  });
  go(ep.port, 'public');
  await assert.rejects(safeFetch('https://site.example.test/v4'), /127\.0\.0\.1 points to a private or local address/);
  await assert.rejects(safeFetch('https://site.example.test/v6'), /::1 points to a private or local address/);
  assert.equal(ep.seen.length, 2); // the two first hops only
});

test('safeFetch on go: EGRESS_PRIVATE names the refused address, IPv6 with its port too; ENOTFOUND', async (t) => {
  const ep = await fakeEndpoint(t, (req, body, res) => {
    const host = new URL(req.headers['x-zp-egress-url']).hostname;
    if (host === 'v6.example.test')
      return failure(res, 'EGRESS_PRIVATE', '[fd00::1]:443 is a private or local address; it is not fetched');
    if (host === 'v4.example.test')
      return failure(res, 'EGRESS_PRIVATE', '10.1.2.3:80 is a private or local address; it is not fetched');
    failure(res, 'ENOTFOUND', 'lookup nowhere.example.test: no such host');
  });
  go(ep.port, 'public');
  await assert.rejects(safeFetch('https://v6.example.test/'), {
    message: 'v6.example.test points to a private or local address (fd00::1); it is not fetched',
  });
  await assert.rejects(safeFetch('http://v4.example.test/'), {
    message: 'v4.example.test points to a private or local address (10.1.2.3); it is not fetched',
  });
  await assert.rejects(safeFetch('http://nowhere.example.test/'), { message: 'nowhere.example.test does not resolve' });
});

test('safeFetch on go: a body over maxBytes and a timeout fail as before', async (t) => {
  const ep = await fakeEndpoint(t, (req, body, res) => {
    const u = new URL(req.headers['x-zp-egress-url']);
    if (u.pathname === '/big') return answer(req, res, 200, {}, 'x'.repeat(4096));
    // /slow: never answers.
  });
  go(ep.port, 'public');
  await assert.rejects(safeFetch('https://site.example.test/big', { maxBytes: 1024 }), /larger than/);
  const start = Date.now();
  await assert.rejects(safeFetch('https://site.example.test/slow', { timeoutMs: 150 }), { message: 'timed out' });
  assert.ok(Date.now() - start < 2000);
});

/* ---------- the Postgres tunnel ---------- */

// A raw CONNECT endpoint: onConnect(target, socket, rest) once the CONNECT head is read.
async function tunnelEndpoint(t, onConnect) {
  const sockets = new Set();
  const seen = [];
  const server = net.createServer((c) => {
    sockets.add(c);
    c.on('close', () => sockets.delete(c));
    let buf = Buffer.alloc(0);
    const onData = (d) => {
      buf = Buffer.concat([buf, d]);
      const end = buf.indexOf('\r\n\r\n');
      if (end < 0) return;
      c.off('data', onData);
      const head = buf.subarray(0, end).toString('latin1');
      const target = /^CONNECT (\S+) /.exec(head)[1];
      seen.push({ target, head });
      onConnect(target, c, buf.subarray(end + 4));
    };
    c.on('data', onData);
  });
  await new Promise((r) => server.listen(0, '127.0.0.1', r));
  t.after(async () => {
    egress.configure(null);
    for (const s of sockets) s.destroy();
    await new Promise((r) => server.close(r));
  });
  return { server, seen, sockets, port: server.address().port };
}

// A Postgres server on an open socket: trust authentication, one query answered with one row. `first` is what is
// already buffered.
function fakePostgres(sock, first = Buffer.alloc(0)) {
  const send = (type, body) => {
    const len = Buffer.alloc(4);
    len.writeInt32BE(body.length + 4);
    sock.write(Buffer.concat([Buffer.from(type), len, body]));
  };
  let buf = first;
  let started = false;
  const pump = () => {
    for (;;) {
      if (!started) {
        if (buf.length < 4) return;
        const len = buf.readInt32BE(0);
        if (buf.length < len) return;
        const code = buf.readInt32BE(4);
        buf = buf.subarray(len);
        if (code === 80877103) {
          sock.write('N'); // SSLRequest: no TLS here
          continue;
        }
        started = true;
        const ok = Buffer.alloc(4);
        send('R', ok);
        send('Z', Buffer.from('I'));
        continue;
      }
      if (buf.length < 5) return;
      const len = buf.readInt32BE(1);
      if (buf.length < len + 1) return;
      const type = String.fromCharCode(buf[0]);
      buf = buf.subarray(len + 1);
      if (type === 'Q') {
        const name = Buffer.from('v\0');
        const field = Buffer.concat([Buffer.from([0, 1]), name, Buffer.alloc(18)]);
        send('T', field);
        const val = Buffer.from('42');
        const n = Buffer.alloc(4);
        n.writeInt32BE(val.length);
        send('D', Buffer.concat([Buffer.from([0, 1]), n, val]));
        send('C', Buffer.from('SELECT 1\0'));
        send('Z', Buffer.from('I'));
      }
      if (type === 'X') sock.end();
    }
  };
  sock.on('data', (d) => {
    buf = Buffer.concat([buf, d]);
    pump();
  });
  pump();
}

const ESTABLISHED = 'HTTP/1.1 200 Connection Established\r\n\r\n';

test('pg on go: through the tunnel to a Postgres that answers N to SSLRequest', async (t) => {
  const ep = await tunnelEndpoint(t, (target, sock, rest) => {
    sock.write(ESTABLISHED);
    fakePostgres(sock, rest);
  });
  go(ep.port, 'brain');
  const c = await pg.connect('postgres://me@db.example.test:5432/db');
  assert.deepEqual(await c.query('SELECT 42 AS v'), [{ v: '42' }]);
  c.end();
  assert.equal(ep.seen[0].target, 'db.example.test:5432');
  assert.match(ep.seen[0].head, /X-ZP-Egress-Purpose: brain:postgres/);
});

test('pg on go: the 200 arrives together with early bytes (the N), which the session still reads', async (t) => {
  const ep = await tunnelEndpoint(t, (target, sock, rest) => {
    // The tunnel's first byte comes in the same packet as the 200: the answer to the SSLRequest the client is about
    // to send. The fake then swallows that SSLRequest and serves the session.
    sock.write(ESTABLISHED + 'N');
    let buf = rest;
    const take = (d) => {
      buf = Buffer.concat([buf, d]);
      if (buf.length < 8) return;
      sock.off('data', take);
      fakePostgres(sock, buf.subarray(8));
    };
    sock.on('data', take);
    take(Buffer.alloc(0));
  });
  go(ep.port, 'brain');
  const c = await pg.connect('postgres://me@db.example.test:5432/db');
  assert.deepEqual(await c.query('SELECT 42 AS v'), [{ v: '42' }]);
  c.end();
});

test('pg on go: an S followed by bytes before the handshake is refused; sslmode=require needs an S', async (t) => {
  const ep = await tunnelEndpoint(t, (target, sock) => {
    sock.write(ESTABLISHED);
    sock.once('data', () => sock.write(target.startsWith('inject') ? 'Sjunk' : 'N'));
  });
  go(ep.port, 'brain');
  await assert.rejects(pg.connect('postgres://me@inject.example.test:5432/db'), /data before the TLS handshake/);
  await assert.rejects(pg.connect('postgres://me@plain.example.test:5432/db?sslmode=require'), /does not accept TLS/);
});

test('pg on go: a refused tunnel carries the endpoint code', async (t) => {
  const ep = await tunnelEndpoint(t, (target, sock) => {
    const b = JSON.stringify({ code: 'EGRESS_REFUSED', message: `${target} is not on the egress allow-list` });
    sock.end(
      `HTTP/1.1 502 Bad Gateway\r\nX-Zp-Egress-Error: EGRESS_REFUSED\r\nContent-Length: ${b.length}\r\n\r\n${b}`,
    );
  });
  go(ep.port, 'brain');
  await assert.rejects(pg.connect('postgres://me@db.example.test:5432/db'), {
    code: 'EGRESS_REFUSED',
    message: 'db.example.test:5432 is not on the egress allow-list',
  });
});

test('pg on go: the deadline covers the tunnel, the SSL answer and the handshake, and closes the socket', async (t) => {
  let closed = 0;
  const ep = await tunnelEndpoint(t, (target, sock) => {
    sock.on('close', () => closed++);
    if (target.startsWith('silent')) return; // never answers the CONNECT
    sock.write(ESTABLISHED);
    if (target.startsWith('mute')) return; // never answers the SSLRequest
    sock.once('data', () => sock.write('S')); // TLS asked, then no handshake
  });
  go(ep.port, 'brain');
  for (const host of ['silent', 'mute', 'stall']) {
    const start = Date.now();
    await assert.rejects(pg.connect(`postgres://me@${host}.example.test:5432/db`, { timeoutMs: 200 }), {
      message: 'connection timed out',
    });
    assert.ok(Date.now() - start < 2000, host);
  }
  const until = Date.now() + 2000;
  while (closed < 3 && Date.now() < until) await new Promise((r) => setTimeout(r, 10));
  assert.equal(closed, 3, 'each pending socket was closed');
});

/* ---------- OTLP ---------- */

test('OTLP on go: the POST through the endpoint; a 429 with retry-after comes back in node:http shape', async (t) => {
  const ep = await fakeEndpoint(t, (req, body, res) => {
    if (req.headers['x-zp-egress-url'].endsWith('/busy')) return answer(req, res, 429, { 'retry-after': '7' }, 'slow');
    answer(req, res, 200, { 'content-type': 'application/json' }, `{"got":${body.length}}`);
  });
  go(ep.port, 'otel');
  const ok = await postOtlp('https://otlp.example.test/v1/traces', Buffer.from('abc'), { 'x-k': 'v' }, 2000);
  assert.equal(ok.status, 200);
  assert.equal(ok.body.toString(), '{"got":3}');
  assert.equal(ep.seen[0].headers['x-zp-egress-purpose'], 'otel');
  assert.equal(ep.seen[0].headers['x-zp-egress-redirect'], 'manual');
  assert.equal(ep.seen[0].headers['x-k'], 'v');
  const busy = await postOtlp('https://otlp.example.test/busy', Buffer.from('x'), {}, 2000);
  assert.equal(busy.status, 429);
  assert.equal(busy.headers['retry-after'], '7');
  assert.equal(busy.body.toString(), 'slow');
});

test('OTLP on go: an idle answer times out; only the first 64 KiB of an answer are kept', async (t) => {
  const ep = await fakeEndpoint(t, (req, body, res) => {
    const u = req.headers['x-zp-egress-url'];
    if (u.endsWith('/idle')) {
      res.writeHead(200, { 'x-zp-egress-url': u });
      res.write('a'); // then nothing
      return;
    }
    answer(req, res, 200, {}, 'y'.repeat(300 * 1024));
  });
  go(ep.port, 'otel');
  await assert.rejects(postOtlp('https://otlp.example.test/idle', Buffer.from('x'), {}, 150), {
    message: 'no answer within 150 ms',
  });
  const big = await postOtlp('https://otlp.example.test/big', Buffer.from('x'), {}, 2000);
  assert.equal(big.status, 200);
  assert.ok(big.body.length >= 64 * 1024 && big.body.length < 300 * 1024, String(big.body.length));
});

/* ---------- local runtimes ---------- */

test('runtimes on go: JSON through the endpoint; an answer over MAX_BODY is unknown; a 503 probe is not ready', async (t) => {
  const ep = await fakeEndpoint(t, (req, body, res) => {
    const u = new URL(req.headers['x-zp-egress-url']);
    if (u.pathname === '/huge') return answer(req, res, 200, {}, `"${'z'.repeat(1024 * 1024 + 10)}"`);
    if (u.pathname === '/health') return answer(req, res, u.port === '9001' ? 503 : 200, {}, 'x');
    if (req.method === 'POST') return answer(req, res, 200, {}, JSON.stringify({ echo: JSON.parse(body) }));
    answer(req, res, 200, { 'content-type': 'application/json' }, '{"models":[]}');
  });
  go(ep.port, 'runtimes');
  assert.deepEqual(await getJson('http://gpu.example.test:11434/api/tags'), { models: [] });
  assert.deepEqual(await postJson('http://gpu.example.test:11434/api/x', { a: 1 }), {
    status: 200,
    body: { echo: { a: 1 } },
  });
  assert.equal(ep.seen[1].headers['content-type'], 'application/json');
  assert.equal(await getJson('http://gpu.example.test:11434/huge'), null);
  assert.equal(await probe('http://gpu.example.test:9001/health'), false);
  assert.equal(await probe('http://gpu.example.test:9002/health'), true);
  assert.ok(ep.seen.every((s) => s.headers['x-zp-egress-purpose'] === 'runtimes'));
});
