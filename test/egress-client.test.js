// The egress client (lib/egress.js, P5 Decisions 4, 5, 8, 11, 12) against a fake endpoint written here in JS: it
// records what the client sends and answers as the Go endpoint would. Unconfigured the client is the global fetch;
// loopback targets go direct; on 'go' the runtime's default headers are filled in, the answer becomes a Response
// (null body where fetch has none, url and redirected from the endpoint), an endpoint failure is fetch's TypeError,
// the caller's abort cuts the loopback request; 'check' never delays the direct call; connect opens a CONNECT tunnel.
// No test reaches an outside host: the outside names below only ever reach the fake endpoint or a stubbed fetch.
const test = require('node:test');
const assert = require('node:assert/strict');
const http = require('http');
const net = require('net');
const egress = require('../lib/egress');

const TOKEN = egress.deriveToken('front-token');
const isDeno = typeof globalThis.Deno !== 'undefined';

// A fake endpoint: handler(req, body, res) answers; every request is kept in `seen`.
async function fakeEndpoint(t, handler) {
  const seen = [];
  const server = http.createServer((req, res) => {
    const chunks = [];
    req.on('data', (c) => chunks.push(c));
    req.on('end', () => {
      const body = Buffer.concat(chunks);
      seen.push({ method: req.method, url: req.url, headers: req.headers, body });
      handler(req, body, res);
    });
  });
  const sockets = new Set();
  server.on('connection', (s) => {
    sockets.add(s);
    s.on('close', () => sockets.delete(s));
  });
  await new Promise((r) => server.listen(0, '127.0.0.1', r));
  t.after(async () => {
    egress.configure(null);
    for (const s of sockets) s.destroy();
    await new Promise((r) => server.close(r));
  });
  return { server, seen, port: server.address().port };
}

function useEndpoint(port, engines, extra = {}) {
  egress.configure({ port, token: TOKEN, engines, lockdown: false, ...extra });
}

// globalThis.fetch replaced for the test; calls are kept.
function stubFetch(t, answer = () => new Response('direct')) {
  const calls = [];
  const real = globalThis.fetch;
  globalThis.fetch = async (input, init) => {
    calls.push({ input, init });
    return answer(input, init);
  };
  t.after(() => {
    globalThis.fetch = real;
  });
  return calls;
}

test('switches: direct, check or go per class; anything else is direct', () => {
  const s = egress.switches({ gateway: 'go', mcp: 'check', notify: 'proxy', bogus: 'go' });
  assert.deepEqual(Object.keys(s), egress.CLASSES);
  assert.equal(s.gateway, 'go');
  assert.equal(s.mcp, 'check');
  assert.equal(s.notify, 'direct');
  assert.equal(s.connectors, 'direct');
  assert.equal(egress.switches(null).gateway, 'direct');
});

test('unconfigured, every class is the global fetch as it is at call time (egressPublic left out)', async (t) => {
  egress.configure(null);
  const calls = stubFetch(t);
  const f = egress.fetchFor('gateway');
  const r = await f('https://api.example.test/v1', { method: 'POST', body: 'x', egressPublic: true });
  assert.equal(await r.text(), 'direct');
  assert.equal(calls.length, 1);
  assert.equal(calls[0].input, 'https://api.example.test/v1');
  assert.deepEqual(calls[0].init, { method: 'POST', body: 'x' });
  assert.deepEqual(egress.state(), {
    configured: false,
    port: null,
    engines: egress.switches(null),
    lockdown: false,
  });
});

test('loopback targets go direct even on go; a class on direct goes direct', async (t) => {
  const ep = await fakeEndpoint(t, (req, body, res) => res.end('proxied'));
  useEndpoint(ep.port, { gateway: 'go' });
  const calls = stubFetch(t);
  for (const u of ['http://localhost:1/x', 'http://127.0.0.1:1/x', 'http://[::1]:1/x', new URL('http://LOCALHOST/')])
    await egress.fetchFor('gateway')(u);
  await egress.fetchFor('notify')('https://hooks.example.test/x');
  assert.equal(calls.length, 5);
  assert.equal(ep.seen.length, 0);
  assert.ok(egress.isLoopbackHost('::1') && egress.isLoopbackHost('[::1]') && !egress.isLoopbackHost('127.0.0.2'));
});

test('go: the request reaches the endpoint with the egress headers, the runtime defaults and the body', async (t) => {
  const ep = await fakeEndpoint(t, (req, body, res) => {
    res.writeHead(201, {
      'x-zp-egress-url': 'https://api.example.test/final',
      'x-zp-egress-redirected': '1',
      'x-answer': 'yes',
      'content-type': 'application/json',
    });
    res.end('{"ok":true}');
  });
  useEndpoint(ep.port, { connectors: 'go' });
  const f = egress.fetchFor('connectors', 'dynatrace');
  const r = await f('https://api.example.test/p?q=1', {
    method: 'POST',
    headers: { 'content-type': 'application/json', 'x-mine': 'a' },
    body: JSON.stringify({ a: 1 }),
  });
  const seen = ep.seen[0];
  assert.equal(seen.method, 'POST');
  assert.equal(seen.url, '/');
  const h = seen.headers;
  assert.equal(h['x-zp-egress-token'], TOKEN);
  assert.equal(h['x-zp-egress-url'], 'https://api.example.test/p?q=1');
  assert.equal(h['x-zp-egress-purpose'], 'connectors:dynatrace');
  assert.equal(h['x-zp-egress-redirect'], 'follow');
  assert.equal(h['x-zp-egress-public'], undefined);
  assert.equal(h.accept, '*/*');
  assert.equal(h['accept-language'], '*');
  assert.equal(h['user-agent'], isDeno ? `Deno/${Deno.version.deno}` : 'node');
  assert.equal(h['sec-fetch-mode'], isDeno ? undefined : 'cors');
  assert.equal(h['x-mine'], 'a');
  assert.equal(h['content-type'], 'application/json');
  assert.equal(h['content-length'], '7');
  assert.equal(seen.body.toString(), '{"a":1}');
  assert.equal(r.status, 201);
  assert.equal(r.url, 'https://api.example.test/final');
  assert.equal(r.redirected, true);
  assert.equal(r.headers.get('x-answer'), 'yes');
  assert.equal(r.headers.get('x-zp-egress-url'), null);
  assert.equal(r.headers.get('x-zp-egress-redirected'), null);
  assert.deepEqual(await r.json(), { ok: true });
});

test('go: the caller’s own accept and user-agent win; manual redirect, public mode and a body-less POST', async (t) => {
  const ep = await fakeEndpoint(t, (req, body, res) => {
    res.writeHead(302, { location: '/next', 'x-zp-egress-url': req.headers['x-zp-egress-url'] });
    res.end();
  });
  useEndpoint(ep.port, { public: 'go' });
  const r = await egress.fetchFor('public')('https://site.example.test/a', {
    method: 'POST',
    redirect: 'manual',
    egressPublic: true,
    headers: { accept: 'text/html', 'user-agent': 'mine' },
  });
  const h = ep.seen[0].headers;
  assert.equal(h.accept, 'text/html');
  assert.equal(h['user-agent'], 'mine');
  assert.equal(h['x-zp-egress-redirect'], 'manual');
  assert.equal(h['x-zp-egress-public'], '1');
  assert.equal(h['x-zp-egress-purpose'], 'public');
  assert.equal(h['content-length'], '0');
  assert.equal(r.status, 302);
  assert.equal(r.headers.get('location'), '/next');
  assert.equal(r.redirected, false);
});

test('go: a stream body is sent as it is read (duplex half)', async (t) => {
  const ep = await fakeEndpoint(t, (req, body, res) => res.end(`got ${body.length}`, () => {}));
  useEndpoint(ep.port, { brain: 'go' });
  const enc = new TextEncoder();
  const body = new ReadableStream({
    start(c) {
      c.enqueue(enc.encode('abc'));
      c.enqueue(enc.encode('def'));
      c.close();
    },
  });
  const r = await egress.fetchFor('brain')('https://db.example.test/x', { method: 'PUT', body, duplex: 'half' });
  assert.equal(await r.text(), 'got 6');
  assert.equal(ep.seen[0].headers['transfer-encoding'], 'chunked');
  assert.equal(ep.seen[0].body.toString(), 'abcdef');
});

test('go: an endpoint failure is fetch’s TypeError with { code, message }; a target’s 502 is an answer', async (t) => {
  const ep = await fakeEndpoint(t, (req, body, res) => {
    if (req.headers['x-zp-egress-url'].includes('down')) {
      res.writeHead(502, { 'x-zp-egress-error': 'ECONNREFUSED', 'content-type': 'application/json' });
      res.end(JSON.stringify({ code: 'ECONNREFUSED', message: 'connect: connection refused' }));
      return;
    }
    // The target's own 502 carrying the header: the endpoint's answers always name the final URL.
    res.writeHead(502, { 'x-zp-egress-error': 'spoof', 'x-zp-egress-url': req.headers['x-zp-egress-url'] });
    res.end('bad gateway');
  });
  useEndpoint(ep.port, { routers: 'go' });
  await assert.rejects(egress.fetchFor('routers')('https://down.example.test/'), (err) => {
    assert.ok(err instanceof TypeError);
    assert.equal(err.message, 'fetch failed');
    assert.deepEqual(err.cause, { code: 'ECONNREFUSED', message: 'connect: connection refused' });
    return true;
  });
  const r = await egress.fetchFor('routers')('https://up.example.test/');
  assert.equal(r.status, 502);
  assert.equal(await r.text(), 'bad gateway');
});

test('go: an endpoint that is gone is a fetch failure with the connect error as its cause', async (t) => {
  const free = net.createServer();
  await new Promise((r) => free.listen(0, '127.0.0.1', r));
  const port = free.address().port;
  await new Promise((r) => free.close(r));
  t.after(() => egress.configure(null));
  useEndpoint(port, { pricing: 'go' });
  await assert.rejects(egress.fetchFor('pricing')('https://openrouter.example.test/'), (err) => {
    assert.equal(err.message, 'fetch failed');
    assert.equal(err.cause.code, 'ECONNREFUSED');
    return true;
  });
});

test('go: 204, 205, 304 and a HEAD answer have a null body', async (t) => {
  const ep = await fakeEndpoint(t, (req, body, res) => {
    const status = Number(new URL(req.headers['x-zp-egress-url']).pathname.slice(1));
    res.writeHead(status, { 'x-zp-egress-url': req.headers['x-zp-egress-url'], 'content-length': '0' });
    res.end();
  });
  useEndpoint(ep.port, { evals: 'go' });
  for (const s of [204, 205, 304]) {
    const r = await egress.fetchFor('evals')(`https://x.example.test/${s}`);
    assert.equal(r.status, s);
    assert.equal(r.body, null, `status ${s}`);
  }
  const r = await egress.fetchFor('evals')('https://x.example.test/200', { method: 'HEAD' });
  assert.equal(r.status, 200);
  assert.equal(r.body, null);
});

test('go: the caller’s abort rejects the call with its reason and closes the loopback request', async (t) => {
  let closed;
  const gone = new Promise((r) => (closed = r));
  const held = [];
  const server = http.createServer((req, res) => {
    held.push(res);
    req.on('close', closed);
    if (req.headers['x-zp-egress-url'].endsWith('/body')) {
      res.writeHead(200, { 'x-zp-egress-url': req.headers['x-zp-egress-url'] });
      res.write('part');
    }
  });
  await new Promise((r) => server.listen(0, '127.0.0.1', r));
  t.after(async () => {
    egress.configure(null);
    for (const r of held) r.destroy();
    server.closeAllConnections();
    await new Promise((r) => server.close(r));
  });
  useEndpoint(server.address().port, { agents: 'go' });
  const ac = new AbortController();
  const p = egress.fetchFor('agents')('https://agent.example.test/wait', { signal: ac.signal });
  await new Promise((r) => setTimeout(r, 50));
  ac.abort();
  await assert.rejects(p, (err) => err.name === 'AbortError');
  await gone;
  // Already aborted: no request at all.
  await assert.rejects(egress.fetchFor('agents')('https://agent.example.test/x', { signal: ac.signal }), {
    name: 'AbortError',
  });
  // An abort while the body streams errors the body's read with the reason.
  const ac2 = new AbortController();
  const r = await egress.fetchFor('agents')('https://agent.example.test/body', { signal: ac2.signal });
  const reader = r.body.getReader();
  assert.equal(new TextDecoder().decode((await reader.read()).value), 'part');
  ac2.abort(new Error('stop'));
  await assert.rejects(reader.read(), /stop/);
});

test('check: the direct call starts at once and is never delayed; /check gets the call; a failure is logged once', async (t) => {
  const pending = [];
  const ep = await fakeEndpoint(t, (req, body, res) => pending.push(res)); // never answers
  t.after(() => pending.forEach((r) => r.destroy()));
  const lines = [];
  useEndpoint(ep.port, { mcp: 'check' }, { log: (l) => lines.push(l) });
  const calls = stubFetch(t);
  const start = Date.now();
  const r = await egress.fetchFor('mcp', 'upstream')('https://mcp.example.test/mcp', { method: 'DELETE' });
  assert.equal(await r.text(), 'direct');
  assert.ok(Date.now() - start < 500);
  assert.equal(calls.length, 1);
  for (let i = 0; i < 50 && !ep.seen.length; i++) await new Promise((res) => setTimeout(res, 10));
  const c = ep.seen[0];
  assert.equal(c.method, 'POST');
  assert.equal(c.url, '/check');
  assert.equal(c.headers['x-zp-egress-method'], 'DELETE');
  assert.equal(c.headers['x-zp-egress-purpose'], 'mcp:upstream');
  assert.equal(c.headers['x-zp-egress-url'], 'https://mcp.example.test/mcp');
  assert.equal(c.headers['x-zp-egress-token'], TOKEN);
  // An endpoint that is gone: the call still goes direct, and the failure is logged once.
  const free = net.createServer();
  await new Promise((res) => free.listen(0, '127.0.0.1', res));
  const port = free.address().port;
  await new Promise((res) => free.close(res));
  useEndpoint(port, { mcp: 'check' }, { log: (l) => lines.push(l) });
  for (let i = 0; i < 3; i++) await egress.fetchFor('mcp')('https://mcp.example.test/mcp');
  for (let i = 0; i < 50 && !lines.length; i++) await new Promise((res) => setTimeout(res, 10));
  await new Promise((res) => setTimeout(res, 100));
  assert.equal(lines.length, 1);
  assert.match(lines[0], /^egress: the check of a mcp(:upstream)? call could not be asked/);
  assert.equal(calls.length, 4);
});

test('lock-down without an endpoint: a non-loopback call fails with EGRESS_UNAVAILABLE, loopback still works', async (t) => {
  t.after(() => egress.configure(null));
  egress.configure({ port: null, token: null, lockdown: true });
  const calls = stubFetch(t);
  await assert.rejects(egress.fetchFor('notify')('https://hooks.example.test/'), (err) => {
    assert.ok(err instanceof egress.EgressUnavailable && err instanceof TypeError);
    assert.equal(err.message, 'fetch failed');
    assert.equal(err.cause.code, 'EGRESS_UNAVAILABLE');
    return true;
  });
  await egress.fetchFor('notify')('http://127.0.0.1:9/');
  assert.equal(calls.length, 1);
  await assert.rejects(egress.connect('brain', 'db.example.test', 5432), { code: 'EGRESS_UNAVAILABLE' });
});

test('connect: CONNECT through the endpoint with the token, the socket after 200; a refusal carries its code', async (t) => {
  const seen = [];
  const ep = await fakeEndpoint(t, (req, body, res) => res.end());
  ep.server.on('connect', (req, socket, head) => {
    seen.push({ url: req.url, headers: req.headers });
    if (req.url.startsWith('refused')) {
      const b = JSON.stringify({ code: 'EGRESS_REFUSED', message: 'refused.example.test:5432 is not on the list' });
      socket.end(
        `HTTP/1.1 502 Bad Gateway\r\nX-Zp-Egress-Error: EGRESS_REFUSED\r\nContent-Length: ${b.length}\r\n\r\n${b}`,
      );
      return;
    }
    // The first bytes of the tunnel come in the same packet as the 200.
    socket.write('HTTP/1.1 200 Connection Established\r\n\r\nhello ');
    if (head.length) socket.write(head);
    socket.on('data', (d) => socket.write(d));
    socket.on('end', () => socket.end());
  });
  useEndpoint(ep.port, { brain: 'go' });
  const s = await egress.connect('brain', 'db.example.test', 5432, 'postgres');
  assert.equal(seen[0].url, 'db.example.test:5432');
  assert.equal(seen[0].headers['x-zp-egress-token'], TOKEN);
  assert.equal(seen[0].headers['x-zp-egress-purpose'], 'brain:postgres');
  const got = [];
  s.on('data', (d) => got.push(d));
  s.write('ping');
  s.end();
  await new Promise((r) => s.on('close', r));
  assert.equal(Buffer.concat(got).toString(), 'hello ping');
  await assert.rejects(egress.connect('brain', 'refused.example.test', 5432), {
    code: 'EGRESS_REFUSED',
    message: 'refused.example.test:5432 is not on the list',
  });
  await egress.connect('brain', '2001:db8::1', 5432).then((x) => x.destroy());
  assert.equal(seen[2].url, '[2001:db8::1]:5432');
});

test('connect: unconfigured, on direct, or to loopback it is net.connect', async (t) => {
  const echo = net.createServer((c) => c.pipe(c));
  await new Promise((r) => echo.listen(0, '127.0.0.1', r));
  t.after(() => new Promise((r) => echo.close(r)));
  egress.configure(null);
  const s = await egress.connect('brain', '127.0.0.1', echo.address().port);
  s.write('x');
  const d = await new Promise((r) => s.once('data', r));
  assert.equal(d.toString(), 'x');
  s.destroy();
  await assert.rejects(egress.connect('brain', '127.0.0.1', 1), { code: 'ECONNREFUSED' });
});

test('loopback (review M2): one trailing dot and 127.0.0.1 IPv4-mapped are loopback; the rest of 127/8 is not', async (t) => {
  for (const h of ['localhost', 'localhost.', 'LOCALHOST.', '127.0.0.1', '127.0.0.1.', '::1', '[::1]'])
    assert.ok(egress.isLoopbackHost(h), h);
  for (const h of ['::ffff:127.0.0.1', '[::ffff:127.0.0.1]', '::ffff:7f00:1', '[::FFFF:7F00:1]'])
    assert.ok(egress.isLoopbackHost(h), h);
  for (const h of ['127.0.0.2', '::ffff:7f00:2', '::ffff:127.0.0.2', 'localhost..', 'localhost.example.test', '', null])
    assert.ok(!egress.isLoopbackHost(h), String(h));
  const ep = await fakeEndpoint(t, (req, body, res) => res.end('proxied'));
  useEndpoint(ep.port, { gateway: 'go' });
  const calls = stubFetch(t);
  for (const u of ['http://localhost.:1/x', 'http://[::ffff:127.0.0.1]:1/x', 'http://[::ffff:7f00:1]:1/x'])
    await egress.fetchFor('gateway')(u);
  assert.equal(calls.length, 3);
  assert.equal(ep.seen.length, 0);
  assert.equal(await (await egress.fetchFor('gateway')('http://127.0.0.2:1/x')).text(), 'proxied');
  assert.equal(ep.seen[0].headers['x-zp-egress-url'], 'http://127.0.0.2:1/x');
});

test('go: a Request input keeps its own redirect (review M3); a clone keeps url and redirected (M4)', async (t) => {
  const ep = await fakeEndpoint(t, (req, body, res) => {
    res.writeHead(200, { 'x-zp-egress-url': 'https://api.example.test/final', 'x-zp-egress-redirected': '1' });
    res.end('body');
  });
  useEndpoint(ep.port, { connectors: 'go' });
  const f = egress.fetchFor('connectors');
  await f(new Request('https://api.example.test/a', { redirect: 'manual' }));
  assert.equal(ep.seen[0].headers['x-zp-egress-redirect'], 'manual');
  await f(new Request('https://api.example.test/a', { redirect: 'manual' }), { redirect: 'error' });
  assert.equal(ep.seen[1].headers['x-zp-egress-redirect'], 'error');
  const r = await f('https://api.example.test/a');
  assert.equal(ep.seen[2].headers['x-zp-egress-redirect'], 'follow');
  const c = r.clone();
  const cc = c.clone();
  for (const x of [r, c, cc]) {
    assert.equal(x.url, 'https://api.example.test/final');
    assert.equal(x.redirected, true);
  }
  assert.equal(await c.text(), 'body');
  assert.equal(await r.text(), 'body');
});

test('go: the endpoint is taken when the call is routed, not after it waits (review M1)', async (t) => {
  const one = await fakeEndpoint(t, (req, body, res) => res.end('one'));
  const two = await fakeEndpoint(t, (req, body, res) => res.end('two'));
  useEndpoint(one.port, { brain: 'go' });
  const p = egress.fetchFor('brain')('https://db.example.test/x', { method: 'POST', body: new Blob(['abc']) });
  useEndpoint(two.port, { brain: 'go' }); // while the body is read
  assert.equal(await (await p).text(), 'one');
  assert.equal(one.seen[0].body.toString(), 'abc');
  assert.equal(two.seen.length, 0);
});

test('connect (review M6): an abort while the tunnel is pending destroys the socket and rejects with the reason', async (t) => {
  // A raw endpoint that reads the CONNECT and never answers.
  let closed;
  const gone = new Promise((r) => (closed = r));
  const silent = net.createServer((c) => c.on('close', closed).resume());
  await new Promise((r) => silent.listen(0, '127.0.0.1', r));
  t.after(async () => {
    egress.configure(null);
    await new Promise((r) => silent.close(r));
  });
  useEndpoint(silent.address().port, { brain: 'go' });
  const ac = new AbortController();
  const p = egress.connect('brain', 'db.example.test', 5432, 'postgres', { signal: ac.signal });
  await new Promise((r) => setTimeout(r, 50));
  ac.abort(Object.assign(new Error('the connect timed out'), { code: 'ETIMEDOUT' }));
  await assert.rejects(p, { code: 'ETIMEDOUT' });
  await gone; // the pending socket was destroyed
  // Aborted before: no connection at all, tunnel or direct.
  await assert.rejects(egress.connect('brain', 'db.example.test', 5432, '', { signal: ac.signal }), {
    code: 'ETIMEDOUT',
  });
  egress.configure(null);
  await assert.rejects(egress.connect('brain', '127.0.0.1', 1, '', { signal: ac.signal }), { code: 'ETIMEDOUT' });
  // A signal that never aborts changes nothing.
  const s = await egress.connect('brain', '127.0.0.1', silent.address().port, '', {
    signal: new AbortController().signal,
  });
  s.destroy();
});
