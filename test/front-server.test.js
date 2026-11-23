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

test('status: the egress classes after their gate (P5 Decision 11); no endpoint in js mode', async (t) => {
  const config = { ...SEED_CONFIG, egress: { classes: { gateway: 'go', mcp: 'check', notify: 'bogus' } } };
  const s = await startServer({
    runtime: RUNTIME,
    config,
    env: { ZAAFPULSE_GW_BIN: path.join(__dirname, 'no-such-gateway') },
  });
  t.after(() => s.stop());
  const st = await status(s.base);
  assert.equal(st.egress.port, null);
  assert.equal(st.egress.lockdown, false);
  assert.equal(st.egress.engines.gateway, 'check'); // asked go, nothing checked yet
  assert.match(st.egress.notes.gateway, /0 of 20 gateway calls checked clean/);
  assert.equal(st.egress.engines.mcp, 'check');
  assert.equal(st.egress.engines.notify, 'direct');
  assert.equal(st.egress.engines.brain, 'direct');
  assert.deepEqual(Object.keys(st.egress.notes), ['gateway']);
  // GET /api/egress (P5 Decision 10) and the network_egress control (Decision 18): js mode, so off.
  const eg = await (await fetch(`${s.base}/api/egress`, { signal: AbortSignal.timeout(5000) })).json();
  assert.equal(eg.mode, 'js');
  assert.equal(eg.port, null);
  assert.equal(eg.engines.gateway, 'check');
  assert.equal(eg.allowList, 'report');
  assert.ok(eg.allow.some((r) => r.host === 'openrouter.ai' && r.port === 443));
  assert.equal(eg.allowCount, eg.allow.length);
  assert.deepEqual(eg.lockdown, { asked: false, inForce: false, why: 'egress.lockdown is off' });
  assert.deepEqual(eg.last, []);
  assert.deepEqual(eg.counts24h, { perClass: {}, total: 0, refused: 0, wouldRefuse: 0 });
  const ctl = await (await fetch(`${s.base}/api/governance/controls`, { signal: AbortSignal.timeout(10000) })).json();
  const ne = ctl.controls.find((c) => c.id === 'network_egress');
  assert.equal(ne.status, 'off');
  assert.match(ne.evidence[1], /child processes/);
});

test('front mode: dashboard and gateway through Go; /v1/traces sees a loopback client and its forwarding headers', async (t) => {
  if (!stubBin(t)) return;
  const s = await startServer({ runtime: RUNTIME, config: FRONT });
  t.after(() => s.stop()); // the gateway exits when the server's end of its stdin closes
  const st = await status(s.base);
  assert.equal(st.mode, 'front');
  assert.equal(st.state, 'ready');
  assert.ok(st.pid > 0);
  // P6 Decision 5: the hello's version (the stub is built without -X main.version), ours, the binary and health age.
  assert.equal(st.gwVersion, 'dev');
  assert.equal(st.version, require('../package.json').version);
  assert.equal(st.versionMismatch, false, 'dev never mismatches');
  assert.deepEqual([st.binary.found, st.binary.allowed], [true, true], JSON.stringify(st.binary));
  assert.equal(path.basename(st.binary.path).replace(/\.exe$/, ''), 'zaafpulse-gw');
  // null until the first health (each second), then its age.
  const aged = await until(
    async () => {
      const x = await status(s.base);
      return x && x.healthAgeMs !== null ? x : null;
    },
    10000,
    'a health',
  );
  assert.ok(
    Number.isInteger(aged.healthAgeMs) && aged.healthAgeMs >= 0 && aged.healthAgeMs < 5000,
    String(aged.healthAgeMs),
  );
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

// T5-M3 (P4a final review): a gateway binary that is not there (installed before gw:build, or not granted to deno run)
// is the failed state at once, so Deno holds the port within moments, not after about 31 s of restarts.
test('front mode: a missing gateway binary falls back to Deno at once', async (t) => {
  if (!stubBin(t)) return;
  const s = await startServer({
    runtime: RUNTIME,
    config: FRONT,
    env: { ZAAFPULSE_GW_BIN: path.join(__dirname, 'no-such-gateway') },
  });
  t.after(() => s.stop());
  assert.ok(s.startupMs < 15000, `the port answered after ${Math.round(s.startupMs)} ms`);
  const st = await status(s.base);
  assert.deepEqual([st.mode, st.state, st.restarts], ['fallback', 'failed', 0], JSON.stringify(st));
  assert.deepEqual(st.binary, { path: path.join(__dirname, 'no-such-gateway'), found: false, allowed: false });
  assert.equal(st.gwVersion, null);
  // P6 Decision 4: the binary is checked before the first start, and the reason names the path it looked at.
  assert.equal(st.reason, `zaafpulse-gw not found at ${path.join(__dirname, 'no-such-gateway')}`);
});

// A gateway holding the outbox of the server's data folder before the server starts; the server's own gateway exits 5.
function holdOutbox(t, bin) {
  const h = { proc: null, gone: null };
  h.before = async (data) => {
    h.proc = spawn(bin, ['--outbox', path.join(data, '.data', 'gw-outbox')], { stdio: ['pipe', 'pipe', 'ignore'] });
    h.gone = new Promise((r) => h.proc.once('close', r));
    await new Promise((r) => h.proc.stdout.once('data', r)); // its hello: the outbox is locked
  };
  h.release = async () => {
    if (!h.proc || h.proc.exitCode !== null || h.proc.signalCode !== null) return h.gone;
    h.proc.stdin.end();
    killHard(h.proc.pid);
    await h.gone;
  };
  return h;
}

test('fallback, then a gateway save: the save gets its answer, and front comes back once the outbox is free', async (t) => {
  const bin = stubBin(t);
  if (!bin) return;
  const holder = holdOutbox(t, bin);
  const s = await startServer({ runtime: RUNTIME, config: FRONT, before: holder.before });
  t.after(async () => {
    await holder.release();
    await s.stop();
  });
  await until(async () => ((await status(s.base)) || {}).mode === 'fallback', 30000, 'the fallback');
  await holder.release();
  const c = clientFor(s.base);
  const saved = await c.call('/api/gateway/defaults', { body: { model: 'a/b' } });
  assert.equal(saved.status, 200, saved.text);
  assert.ok(saved.json, 'a JSON answer, not a reset connection');
  const st = await until(
    async () => {
      const x = await status(s.base);
      return x && x.mode === 'front' && x.state === 'ready' ? x : null;
    },
    30000,
    'front again',
  );
  assert.ok(st.pid > 0);
  assert.equal(await answers(`${s.base}/login.html`), 200);
});

test('front mode: 5 restarts within 60 s fall back to Deno; a gateway save then does not try front again', async (t) => {
  if (!stubBin(t)) return;
  const s = await startServer({ runtime: RUNTIME, config: FRONT });
  t.after(() => s.stop());
  // Each gateway that comes up is killed hard (back-off 1, 2, 4, 8, 16 s between runs): the sixth exit is the limit.
  let pid = 0;
  const st = await until(
    async () => {
      const x = await status(s.base);
      if (x && x.mode === 'fallback') return x;
      if (x && x.state === 'ready' && x.pid !== pid) killHard((pid = x.pid));
      return null;
    },
    90000,
    'the fallback after the restart limit',
  );
  assert.match(st.reason, /5 restarts within 60 s/, JSON.stringify(st));
  assert.equal(st.alert, true);
  assert.equal(await answers(`${s.base}/login.html`), 200);
  const saved = await clientFor(s.base).call('/api/gateway/defaults', { body: { model: 'a/b' } });
  assert.equal(saved.status, 200, saved.text);
  await new Promise((r) => setTimeout(r, 1500));
  const after = await status(s.base);
  assert.deepEqual([after.mode, after.state], ['fallback', 'failed'], 'no new gateway after a crash loop');
});
