// The differential proof of the egress path (P5 Decision 8), with the real gateway (gw/cmd/zaafpulse-gw, built here as
// the IPC tests build it; skipped without Go) under the real supervisor and ingest. One local fake records what it
// receives; the same calls are made direct (the global fetch) and through the endpoint (lib/egress.js on 'go'), and
// must give the fake the same method, path, query, body and headers (but accept-encoding and the loopback leg's
// connection header; a multipart boundary is random per call) and give the caller the same status, headers (but date
// and the hop-by-hop ones; a gzip answer's content-encoding and content-length, known difference 1) and body. Every
// call through the endpoint leaves its records in .data/egress.jsonl through the outbox and the ingest.
//
// The fake listens on 127.0.0.1, which the client always sends direct (also as [::ffff:7f00:1], review M2), so the
// client is configured with its test-only testLoopbackGo, which sends loopback targets through the endpoint too. The
// calls name the fake as [::ffff:7f00:1] (the same address, IPv4-mapped), so the NO_PROXY runs below can exempt the
// fake's name without exempting 127.0.0.1, the loopback leg's own address. Run it on Node and on Deno
// (`deno test -A test/egress-diff.test.js`), and with HTTP_PROXY=http://127.0.0.1:9 set: the loopback leg to the
// endpoint (node:http to 127.0.0.1) never takes the proxy from the environment, and Go's proxy rule leaves a loopback
// target direct. Deno's own fetch (and Node's with NODE_USE_ENV_PROXY=1) takes HTTP_PROXY even for 127.0.0.1, so the
// direct baseline needs NO_PROXY for the fake's name only (Deno matches it bare, Node in brackets), never 127.0.0.1:
//   HTTP_PROXY=http://127.0.0.1:9 NO_PROXY='::ffff:7f00:1,[::ffff:7f00:1]' deno test -A test/egress-diff.test.js
//   NODE_USE_ENV_PROXY=1 HTTP_PROXY=http://127.0.0.1:9 NO_PROXY=… node --test test/egress-diff.test.js
const test = require('node:test');
const assert = require('node:assert/strict');
const crypto = require('crypto');
const fs = require('fs');
const http = require('http');
const net = require('net');
const path = require('path');
const zlib = require('zlib');
const egress = require('../lib/egress');
const { createSupervisor } = require('../lib/ipc/supervisor');
const { createIngest } = require('../lib/ipc/ingest');
const { openLocalStores } = require('../lib/ipc/local');
const { recoverMarks } = require('../lib/ipc/marks');
const { stubBin, initial, tmpDir, waitFor, sleep } = require('./ipc-stub');

const PURPOSE = 'connectors';
const DETAIL = 'diff';

// The fake: every request it gets, by run; answers by path.
function startFake() {
  const seen = [];
  const server = http.createServer((req, res) => {
    const chunks = [];
    req.on('data', (c) => chunks.push(c));
    req.on('end', () => {
      seen.push({ method: req.method, url: req.url, rawHeaders: req.rawHeaders, body: Buffer.concat(chunks) });
      const u = new URL(req.url, 'http://x');
      switch (u.pathname) {
        case '/r1':
          res.writeHead(302, { location: '/r2?from=1' });
          return res.end('moved');
        case '/r2':
          res.writeHead(307, { location: '/final' });
          return res.end();
        case '/sse':
          res.writeHead(200, { 'content-type': 'text/event-stream', 'cache-control': 'no-cache' });
          res.write('data: one\n\n');
          setTimeout(() => {
            res.write('data: two\n\n');
            res.end('data: [DONE]\n\n');
          }, 300);
          return;
        case '/gzip': {
          const z = zlib.gzipSync(Buffer.from('hello gzip '.repeat(20)));
          res.writeHead(200, { 'content-type': 'text/plain', 'content-encoding': 'gzip', 'content-length': z.length });
          return res.end(z);
        }
        case '/missing':
          res.writeHead(404, { 'content-type': 'application/json', 'x-trace': 'abc' });
          return res.end('{"error":"not found"}');
        default:
          res.writeHead(200, { 'content-type': 'application/json', 'set-cookie': ['a=1', 'b=2'] });
          return res.end(JSON.stringify({ method: req.method, url: req.url }));
      }
    });
  });
  return new Promise((resolve) => server.listen(0, '127.0.0.1', () => resolve({ server, seen })));
}

// The calls: [name, (base) => [url, init]] (a fresh init each time: a body is read once).
const CALLS = [
  ['GET with a query', (b) => [`${b}/get?q=1&x=a%20b`]],
  [
    'POST JSON',
    (b) => [
      `${b}/json`,
      { method: 'POST', headers: { 'content-type': 'application/json', authorization: 'Bearer t' }, body: '{"a":1}' },
    ],
  ],
  ['POST URLSearchParams', (b) => [`${b}/form`, { method: 'POST', body: new URLSearchParams({ a: '1', b: 'x y' }) }]],
  [
    'POST FormData',
    (b) => {
      const fd = new FormData();
      fd.append('field', 'value');
      fd.append('file', new Blob(['file body'], { type: 'text/plain' }), 'f.txt');
      return [`${b}/multipart`, { method: 'POST', body: fd }];
    },
  ],
  [
    'PUT a stream body (duplex half)',
    (b) => {
      const enc = new TextEncoder();
      const body = new ReadableStream({
        start(c) {
          c.enqueue(enc.encode('chunk one,'));
          c.enqueue(enc.encode('chunk two'));
          c.close();
        },
      });
      return [`${b}/stream`, { method: 'PUT', body, duplex: 'half', headers: { 'content-type': 'text/plain' } }];
    },
  ],
  ['a redirect chain (302 then 307)', (b) => [`${b}/r1`, { method: 'POST', body: 'payload' }]],
  ['SSE', (b) => [`${b}/sse`, { headers: { accept: 'text/event-stream' } }]],
  ['a gzip answer', (b) => [`${b}/gzip`]],
  ['a 404', (b) => [`${b}/missing`]],
];

// What the fake saw of one request, comparable across the two paths.
function received(r) {
  const headers = [];
  let boundary = null;
  for (let i = 0; i < r.rawHeaders.length; i += 2) {
    const k = r.rawHeaders[i].toLowerCase();
    let v = r.rawHeaders[i + 1];
    if (k === 'accept-encoding' || k === 'connection') continue; // Go asks for gzip itself; the loopback leg's own
    if (k === 'content-type' && v.startsWith('multipart/form-data')) {
      boundary = /boundary=(.*)$/.exec(v)[1];
      v = 'multipart/form-data; boundary=<b>';
    }
    headers.push([k, v]);
  }
  headers.sort((a, b) => (a[0] < b[0] ? -1 : a[0] > b[0] ? 1 : 0));
  let body = r.body.toString('latin1');
  if (boundary) {
    body = body.split(boundary).join('<b>');
    // The two boundaries may differ in length: the multipart length moves with them.
    const i = headers.findIndex(([k]) => k === 'content-length');
    if (i >= 0) headers[i] = ['content-length', '<n>'];
  }
  return { method: r.method, url: r.url, headers, body };
}

// What the caller got, comparable across the two paths.
async function answered(res, { gzip = false } = {}) {
  const skip = new Set(['date', 'connection', 'keep-alive', 'transfer-encoding']);
  if (gzip) ['content-encoding', 'content-length'].forEach((k) => skip.add(k));
  const headers = [...res.headers].filter(([k]) => !skip.has(k));
  const chunks = [];
  const reader = res.body.getReader();
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    chunks.push(new TextDecoder().decode(value));
  }
  return {
    status: res.status,
    statusText: res.statusText,
    url: res.url,
    redirected: res.redirected,
    headers,
    body: chunks.join(''),
    first: chunks[0],
  };
}

test('the same calls direct and through the gateway reach the fake and the caller alike, each recorded', async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  const fake = await startFake();
  // node:test runs after-hooks in the order they were added: the gateway must stop before tmpDir removes the outbox
  // whose lock it holds (Windows refuses to delete an open file), so its stop goes through tmpDir's own list.
  const stops = [];
  const root = tmpDir(t, 'egress-diff', stops);
  const stores = openLocalStores({ root });
  const ingest = createIngest({ ...stores.sinks, marks: recoverMarks(stores.files) });
  const frontToken = crypto.randomBytes(32).toString('hex');
  const first = initial();
  let port = null;
  const sup = createSupervisor({
    bin,
    args: ['--outbox', stores.files.outboxDir],
    cpVersion: 'egress-diff',
    sources: {
      config: () => ({ ...first.config, egress: { allow: [], allowList: 'report' } }),
      secrets: () => ({ providers: [], mask: [], frontToken }),
      snapshot: () => ({ budgets: [], keyCenter: {} }),
    },
    ingest,
    log: () => {},
    onEgress: (p) => {
      port = p;
      egress.configure(
        p
          ? {
              port: p,
              token: egress.deriveToken(frontToken),
              engines: { [PURPOSE]: 'go' },
              lockdown: false,
              testLoopbackGo: true,
            }
          : null,
      );
    },
  });
  stops.push(async () => {
    egress.configure(null);
    await sup.stop(2000);
    assert.equal(port, null, 'the supervisor clears the egress port when the gateway exits');
    fake.server.closeAllConnections();
    await new Promise((r) => fake.server.close(r));
  });
  sup.start();
  await waitFor(() => port !== null, 20000, 'the gateway ready with its egress port');
  assert.equal(sup.status().egressPort, port);
  assert.equal(egress.state().engines[PURPOSE], 'go');

  const base = `http://[::ffff:7f00:1]:${fake.server.address().port}`;
  const via = egress.fetchFor(PURPOSE, DETAIL);
  let proxiedRequests = 0;
  for (const [name, call] of CALLS) {
    const gzip = name === 'a gzip answer';
    const before = fake.seen.length;
    const direct = await answered(await fetch(...call(base)), { gzip });
    const mid = fake.seen.length;
    const proxied = await answered(await via(...call(base)), { gzip });
    const after = fake.seen.length;
    assert.equal(after - mid, mid - before, `${name}: as many requests reach the fake`);
    proxiedRequests += after - mid;
    assert.deepEqual(
      fake.seen.slice(mid, after).map(received),
      fake.seen.slice(before, mid).map(received),
      `${name}: what the fake received`,
    );
    assert.deepEqual(proxied, direct, `${name}: what the caller got`);
    if (name === 'SSE') assert.equal(proxied.first, 'data: one\n\n', 'SSE: the first event arrives on its own');
    if (name.startsWith('a redirect')) assert.ok(proxied.redirected && proxied.url.endsWith('/final'));
  }

  // A refused port: both are fetch's TypeError; through the endpoint the cause is { code: 'ECONNREFUSED' } (direct on
  // Node too; Deno's own fetch names it differently, known difference 2).
  const free = net.createServer();
  await new Promise((r) => free.listen(0, '127.0.0.1', r));
  const closed = free.address().port;
  await new Promise((r) => free.close(r));
  const refused = `http://[::ffff:7f00:1]:${closed}/`;
  const directErr = await fetch(refused).then(
    () => null,
    (e) => e,
  );
  const proxiedErr = await via(refused).then(
    () => null,
    (e) => e,
  );
  assert.ok(directErr instanceof TypeError && proxiedErr instanceof TypeError);
  assert.equal(proxiedErr.message, 'fetch failed');
  assert.equal(proxiedErr.cause.code, 'ECONNREFUSED');
  if (typeof globalThis.Deno === 'undefined') assert.equal(directErr.cause.code, 'ECONNREFUSED');

  // The records: one "ok" per request that reached the fake (each redirect hop is its own), one "error" for the refused
  // port, and a "would-refuse" before each hop (the allow-list is empty, in report mode).
  const records = () => stores.egress.tail(1000).filter((r) => r.purpose === `${PURPOSE}:${DETAIL}`);
  await waitFor(
    () =>
      records().filter((r) => r.outcome === 'ok').length === proxiedRequests &&
      records().some((r) => r.outcome === 'error'),
    10000,
    'the egress records in .data/egress.jsonl',
  );
  const recs = records();
  const ok = recs.filter((r) => r.outcome === 'ok');
  assert.ok(ok.every((r) => r.via === 'proxy' && r.host === '::ffff:7f00:1' && r.scheme === 'http' && r.status));
  assert.deepEqual(
    recs.filter((r) => r.outcome === 'error').map((r) => [r.error, r.port]),
    [['ECONNREFUSED', closed]],
  );
  assert.equal(recs.filter((r) => r.outcome === 'would-refuse').length, proxiedRequests + 1);
  assert.ok(
    recs.every((r) => r.ipc && r.ipc.seq > 0),
    'each record came through the outbox with its seq',
  );
  const gz = ok.find((r) => r.status === 200 && r.bytesIn === 'hello gzip '.repeat(20).length);
  assert.ok(gz, 'a gzip answer counts its decoded bytes');
  await sleep(0);
});

test('a gateway without the egress section names no port, and the client stays direct', async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  const stops = [];
  const root = tmpDir(t, 'egress-diff-none', stops); // the gateway stops before the folder goes (Windows)
  const stores = openLocalStores({ root });
  const ingest = createIngest({ ...stores.sinks, marks: recoverMarks(stores.files) });
  const calls = [];
  let readyCalled = false;
  const sup = createSupervisor({
    bin,
    args: ['--outbox', stores.files.outboxDir],
    cpVersion: 'egress-diff',
    sources: {
      config: () => initial().config,
      secrets: () => ({ providers: [], mask: [], frontToken: 'f'.repeat(64) }),
      snapshot: () => ({ budgets: [], keyCenter: {} }),
    },
    ingest,
    log: () => {},
    onReady: () => (readyCalled = true),
    onEgress: (p) => calls.push(p),
  });
  stops.push(() => sup.stop(2000));
  sup.start();
  await waitFor(() => readyCalled, 20000, 'the gateway ready');
  assert.deepEqual(calls, [null]);
  assert.equal(sup.status().egressPort, null);
  assert.ok(!fs.existsSync(path.join(root, '.data', 'egress.jsonl')));
});
