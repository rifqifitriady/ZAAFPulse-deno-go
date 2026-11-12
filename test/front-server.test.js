// server.js with gateway.engine (P4a T5): "js" (the default) serves as before and starts no gateway; "front" serves the
// dashboard and the gateway through zaafpulse-gw on the main port; a killed gateway is restarted and the port comes
// back; a gateway in the failed state (here: its outbox held by another process, exit 5) falls back to Deno on the same
// port. Under node --test the server runs on Node, under deno test on Deno.
const test = require('node:test');
const assert = require('node:assert/strict');
const path = require('path');
const { spawn } = require('child_process');
const { startServer, clientFor, SEED_CONFIG } = require('./contract/harness');
const { stubBin, killHard } = require('./ipc-stub');

const RUNTIME = process.versions && process.versions.deno ? 'deno' : 'node';
const FRONT = { ...SEED_CONFIG, gateway: { engine: 'front' } };

async function status(base) {
  try {
    const r = await fetch(`${base}/api/gateway/status`, { signal: AbortSignal.timeout(5000) });
    return r.status === 200 ? await r.json() : null;
  } catch {
    return null;
  }
}
async function answers(url) {
  try {
    const r = await fetch(url, { signal: AbortSignal.timeout(2000) });
    await r.arrayBuffer();
    return r.status;
  } catch {
    return 0;
  }
}
/** Polls the async check every 100 ms until it returns a truthy value (returned), or fails after ms. */
async function until(check, ms, what) {
  const end = Date.now() + ms;
  for (;;) {
    const v = await check();
    if (v) return v;
    if (Date.now() > end) throw new Error(`timed out after ${ms} ms waiting for ${what}`);
    await new Promise((r) => setTimeout(r, 100));
  }
}

test('js mode (the default): the server holds the port, no gateway process', async (t) => {
  const s = await startServer({ runtime: RUNTIME, env: { ZAAFPULSE_GW_BIN: path.join(__dirname, 'no-such-gateway') } });
  t.after(() => s.stop());
  assert.equal(await answers(`${s.base}/login.html`), 200);
  const st = await status(s.base);
  assert.equal(st.mode, 'js');
  assert.equal(st.state, 'off');
  assert.equal(st.pid, null);
  assert.ok(Object.values(st.engines).every((e) => e === 'js'));
});

test('front mode: dashboard and gateway through Go; /v1/traces sees a loopback client and its forwarding headers', async (t) => {
  if (!stubBin(t)) return;
  const s = await startServer({ runtime: RUNTIME, config: FRONT });
  t.after(() => s.stop()); // the gateway exits when the server's end of its stdin closes
  const st = await status(s.base);
  assert.equal(st.mode, 'front');
  assert.equal(st.state, 'ready');
  assert.ok(st.pid > 0);
  const c = clientFor(s.base);
  const key = await c.newKey({ scopes: ['llm'] });
  assert.equal((await c.call('/v1/models', { method: 'GET', key: key.secret })).status, 200);
  // The loopback client reaches the trace channel (415: not JSON); a forwarding header refuses it (403).
  assert.equal((await c.call('/v1/traces', { raw: 'x', headers: { 'content-type': 'text/plain' } })).status, 415);
  assert.equal((await c.call('/v1/traces', { raw: '{}', headers: { 'x-forwarded-for': '203.0.113.9' } })).status, 403);
});

test('front mode: a killed gateway is restarted and the port comes back', async (t) => {
  if (!stubBin(t)) return;
  const s = await startServer({ runtime: RUNTIME, config: FRONT });
  t.after(() => s.stop());
  const first = await status(s.base);
  killHard(first.pid);
  const now = await until(
    async () => {
      if ((await answers(`${s.base}/login.html`)) !== 200) return null;
      const st = await status(s.base);
      return st && st.state === 'ready' && st.pid !== first.pid ? st : null;
    },
    30000,
    'a new gateway on the main port',
  );
  assert.ok(now.restarts >= 1);
  assert.equal(now.mode, 'front');
});

test('front mode: a gateway that cannot run (its outbox held) falls back to Deno on the same port', async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  let holder = null;
  let holderGone = null;
  const s = await startServer({
    runtime: RUNTIME,
    config: FRONT,
    // Another gateway holds the server's outbox before the server starts: the server's gateway exits 5.
    before: async (data) => {
      holder = spawn(bin, ['--outbox', path.join(data, '.data', 'gw-outbox')], { stdio: ['pipe', 'pipe', 'ignore'] });
      holderGone = new Promise((r) => holder.once('close', r));
      await new Promise((r) => holder.stdout.once('data', r)); // its hello: the outbox is locked
    },
  });
  // One hook, in this order: the holder is gone before stop() removes the data folder (Windows).
  t.after(async () => {
    holder.stdin.end();
    killHard(holder.pid);
    await holderGone;
    await s.stop();
  });
  const st = await until(
    async () => {
      const x = await status(s.base);
      return x && x.mode === 'fallback' ? x : null;
    },
    30000,
    'the fallback',
  );
  assert.equal(st.state, 'failed');
  assert.match(st.reason, /in use/);
  assert.equal(st.alert, true);
  assert.equal(await answers(`${s.base}/login.html`), 200);
});
