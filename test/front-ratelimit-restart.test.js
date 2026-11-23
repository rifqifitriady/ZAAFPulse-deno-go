// One rate-limit window across a gateway restart (P4f D16, final review m1): the go engine's window is in memory, so a
// restarted gateway starts empty; the control plane, which noted every take the old gateway told it, seeds the new one
// at its hello. A key with rpm 3 makes two calls the go engine serves, the gateway is killed and restarted, then the
// third call passes and the fourth is refused, as if no restart had happened.
const test = require('node:test');
const assert = require('node:assert/strict');
const { startServer, clientFor, providerList, SEED_CONFIG } = require('./contract/harness');
const { startUpstream } = require('./contract/upstream');
const { stubBin, killHard } = require('./ipc-stub');

const RUNTIME = process.versions && process.versions.deno ? 'deno' : 'node';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function until(check, ms, what) {
  const end = Date.now() + ms;
  for (;;) {
    const v = await check().catch(() => null);
    if (v) return v;
    if (Date.now() > end) throw new Error(`timed out after ${ms} ms waiting for ${what}`);
    await sleep(100);
  }
}

test('the rate-limit window survives a gateway restart (P4f D16, review m1)', async (t) => {
  if (!stubBin(t)) return;
  const upstream = await startUpstream();
  t.after(() => upstream.close());
  const gateway = {
    engine: 'front',
    engines: { chat: 'go', 'chat-stream': 'go' },
    shadowDays: 0,
    shadowMinCompares: 0,
  };
  const s = await startServer({ runtime: RUNTIME, config: { ...SEED_CONFIG, gateway } });
  t.after(() => s.stop());
  const c = clientFor(s.base);
  for (const p of providerList(upstream.url)) await c.admin('providers', { action: 'save', ...p });
  await c.setGuardrails({ scope: 'external', secrets: 'off', pii: 'off', injection: 'off' });
  const key = await c.newKey({ scopes: ['llm'], rpm: 3 });
  const chat = () =>
    c.call('/v1/chat/completions', {
      key: key.secret,
      body: { model: 'oa/m1', messages: [{ role: 'user', content: 'hi' }] },
    });
  const status = async () =>
    (await fetch(`${s.base}/api/gateway/status`, { signal: AbortSignal.timeout(2000) })).json();
  const before = await until(
    async () => {
      const st = await status();
      return st.state === 'ready' && st.engines && st.engines.chat === 'go' ? st : null;
    },
    15000,
    'the go engine',
  );
  assert.equal((await chat()).status, 200);
  assert.equal((await chat()).status, 200);
  await sleep(200); // the takes reach the control plane
  killHard(before.pid);
  await until(
    async () => {
      const st = await status();
      return st.state === 'ready' && st.pid !== before.pid ? st : null;
    },
    30000,
    'the restarted gateway',
  );
  const third = await until(
    async () => {
      const r = await chat();
      return r.status === 200 || r.status === 429 ? r : null; // the port may answer 502 for a moment
    },
    10000,
    'the third call',
  );
  assert.equal(third.status, 200, 'the window holds the two calls before the restart, not more');
  const fourth = await chat();
  assert.equal(fourth.status, 429, 'the restarted gateway counts the calls the old one admitted');
  assert.equal(fourth.json.error.type, 'rate_limit_error');
});
