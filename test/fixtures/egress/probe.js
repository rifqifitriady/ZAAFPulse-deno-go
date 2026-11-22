// The probe of test/egress-lockdown.test.js, launched with exactly the server's locked-down `deno run` line
// (lib/permissions.js selfCommand). It tries direct outbound connects that the lock-down must refuse before any packet
// (Deno's NotCapable), a direct loopback call that must still work, and calls through the gateway's egress endpoint
// (EGRESS_PORT, EGRESS_TOKEN from the test). It prints one JSON line.
const net = require('net');
const egress = require('../../../lib/egress');

const FAKE = Number(process.env.FAKE_PORT);
const OUTSIDE = [
  'http://example.com/',
  'http://10.0.0.1/',
  'http://169.254.169.254/',
  'http://[2001:db8::1]/',
  `http://127.0.0.2:${FAKE}/`,
];
const TCP = [
  ['example.com', 443],
  ['10.0.0.1', 80],
  ['2001:db8::1', 80],
  ['127.0.0.2', FAKE],
];

const errOf = (e) => ({
  name: e && e.name,
  code: e && (e.code || (e.cause && e.cause.code)),
  message: String(e && e.message),
});

async function main() {
  const out = { fetch: {}, tcp: {} };
  for (const u of OUTSIDE) {
    try {
      const r = await fetch(u, { signal: AbortSignal.timeout(3000) });
      out.fetch[u] = { ok: true, status: r.status };
    } catch (e) {
      out.fetch[u] = errOf(e);
    }
  }
  for (const [h, p] of TCP) {
    out.tcp[`${h}:${p}`] = await new Promise((resolve) => {
      try {
        const s = net.connect(p, h);
        s.once('connect', () => {
          s.destroy();
          resolve({ ok: true });
        });
        s.once('error', (e) => resolve(errOf(e)));
      } catch (e) {
        resolve(errOf(e));
      }
    });
  }
  const loop = await fetch(`http://127.0.0.1:${FAKE}/direct`);
  out.loopback = { status: loop.status, body: await loop.text() };
  // Locked down, no endpoint: a non-loopback call fails loudly, never direct.
  egress.configure({ lockdown: true });
  try {
    await egress.fetchFor('connectors', 'probe')('http://example.com/');
    out.unavailable = { ok: true };
  } catch (e) {
    out.unavailable = errOf(e);
  }
  // Locked down, with the endpoint: the call leaves through the gateway (the fake stands for a remote host).
  egress.configure({
    port: Number(process.env.EGRESS_PORT),
    token: process.env.EGRESS_TOKEN,
    engines: {},
    lockdown: true,
    testLoopbackGo: true,
  });
  const via = await egress.fetchFor('connectors', 'probe')(`http://127.0.0.1:${FAKE}/via`);
  out.via = { status: via.status, body: await via.text() };
  egress.configure(null);
  console.log(JSON.stringify(out));
}
main().catch((e) => {
  console.log(JSON.stringify({ crashed: errOf(e) }));
});
