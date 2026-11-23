// The host check through the Go front (P4a final review I1): a request without a Host header (HTTP/1.0) and one with
// an absolute-form target get the same answer in front mode as in js mode, and the address the server advertises
// (publicBase: the agent card) never names Deno's internal port. Each case is sent as raw bytes to a js-mode server and
// to a front-mode server with the same settings. Under node --test the servers run on Node, under deno test on Deno.
const test = require('node:test');
const assert = require('node:assert/strict');
const net = require('net');
const { startServer, SEED_CONFIG } = require('./contract/harness');
const { stubBin } = require('./ipc-stub');

const RUNTIME = process.versions && process.versions.deno ? 'deno' : 'node';
const FRONT = { ...SEED_CONFIG, gateway: { engine: 'front' } };
const PASSWORD = { ZAAFPULSE_AUTH: '1', ZAAFPULSE_ADMIN_PASSWORD: 'front-host-test-password' };

/** Sends text on a new connection and resolves with { status, body } once the server closes it. */
function raw(base, text) {
  const { hostname, port } = new URL(base);
  return new Promise((resolve, reject) => {
    const c = net.connect(Number(port), hostname);
    const chunks = [];
    c.setTimeout(10000, () => c.destroy(new Error('no answer within 10 s')));
    c.on('connect', () => c.write(text));
    c.on('data', (d) => chunks.push(d));
    c.on('error', reject);
    c.on('close', () => {
      const all = Buffer.concat(chunks).toString('utf8');
      const m = /^HTTP\/1\.[01] (\d{3})/.exec(all);
      // The JSON answer, without the chunk sizes of a chunked body.
      const json = /\{.*\}/s.exec(all.slice(all.indexOf('\r\n\r\n') + 4));
      resolve({ status: m ? Number(m[1]) : 0, body: json ? json[0] : '' });
    });
  });
}

/** A js-mode and a front-mode server with the same environment. */
async function pair(t, env = {}) {
  const js = await startServer({ runtime: RUNTIME, env });
  t.after(() => js.stop());
  const front = await startServer({ runtime: RUNTIME, config: FRONT, env });
  t.after(() => front.stop());
  return { js, front };
}

const HOSTLESS = 'GET /api/platform HTTP/1.0\r\n\r\n';
const ABSOLUTE = 'GET http://evil.example/ HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n';
const ABSOLUTE_LOOPBACK =
  'GET http://127.0.0.1/api/platform HTTP/1.1\r\nHost: evil.example\r\nConnection: close\r\n\r\n';

test('no sign-in: a host-less request and absolute-form targets get the answer js mode gives', async (t) => {
  if (!stubBin(t)) return;
  const { js, front } = await pair(t);
  const hostless = [await raw(js.base, HOSTLESS), await raw(front.base, HOSTLESS)];
  assert.equal(hostless[0].status, 403, 'js mode refuses a request without a Host');
  assert.equal(hostless[1].status, hostless[0].status, `front mode answered ${hostless[1].body}`);
  for (const text of [ABSOLUTE, ABSOLUTE_LOOPBACK]) {
    const [a, b] = [await raw(js.base, text), await raw(front.base, text)];
    assert.equal(a.status, 400, `js mode: ${a.body}`);
    assert.equal(b.status, a.status, `front mode: ${b.body}`);
    assert.deepEqual(JSON.parse(b.body), JSON.parse(a.body));
  }
});

test('sign-in with ZAAFPULSE_ALLOWED_HOSTS: a host-less request is refused in both modes', async (t) => {
  if (!stubBin(t)) return;
  const { js, front } = await pair(t, { ...PASSWORD, ZAAFPULSE_ALLOWED_HOSTS: 'dash.example' });
  const [a, b] = [await raw(js.base, HOSTLESS), await raw(front.base, HOSTLESS)];
  assert.equal(a.status, 403);
  assert.equal(b.status, a.status, `front mode answered ${b.body}`);
});

test('sign-in: a host-less request is advertised the address js mode gives, never the internal port', async (t) => {
  if (!stubBin(t)) return;
  const { js, front } = await pair(t, PASSWORD);
  const card = 'GET /.well-known/agent.json HTTP/1.0\r\n\r\n';
  const [a, b] = [await raw(js.base, card), await raw(front.base, card)];
  assert.equal(a.status, 200, `js mode: ${a.body}`);
  assert.equal(b.status, 200, `front mode: ${b.body}`);
  const port = new URL(front.base).port;
  for (const m of b.body.matchAll(/https?:\/\/[^"/]+/g))
    assert.ok(m[0] === 'http://127.0.0.1' || m[0].endsWith(`:${port}`), `front mode advertises ${m[0]}`);
  assert.equal(b.body, a.body);
});
