// Kill test of the Go front (P4a T6): the gateway is killed while a streamed chat answer goes through it. The client
// sees the stream cut, the supervisor restarts the gateway and the main port answers again, and the call is recorded
// once in the usage log (the server answered it; the gateway records nothing in P4a): no line lost, none twice.
const test = require('node:test');
const assert = require('node:assert/strict');
const http = require('http');
const { startServer, clientFor, SEED_CONFIG } = require('./contract/harness');
const { stubBin, killHard } = require('./ipc-stub');

const RUNTIME = process.versions && process.versions.deno ? 'deno' : 'node';

/** A provider that streams one chunk, then waits for release() before the rest. */
async function slowProvider(t) {
  let release;
  const released = new Promise((r) => (release = r));
  const server = http.createServer(async (req, res) => {
    let body = '';
    for await (const c of req) body += c;
    const model = JSON.parse(body || '{}').model;
    const chunk = (delta, extra = {}) =>
      `data: ${JSON.stringify({ id: 's1', object: 'chat.completion.chunk', model, choices: [{ index: 0, delta, ...extra }] })}\n\n`;
    if (model === 'slowm') {
      res.writeHead(200, { 'content-type': 'text/event-stream' });
      res.write(chunk({ role: 'assistant', content: 'Hel' }));
      await released;
      res.write(chunk({ content: 'lo' }, { finish_reason: 'stop' }));
      res.end('data: [DONE]\n\n');
      return;
    }
    res.writeHead(200, { 'content-type': 'application/json' });
    res.end(
      JSON.stringify({
        id: 'c1',
        object: 'chat.completion',
        model,
        choices: [{ index: 0, message: { role: 'assistant', content: 'hi' }, finish_reason: 'stop' }],
        usage: { prompt_tokens: 10, completion_tokens: 2 },
      }),
    );
  });
  await new Promise((r) => server.listen(0, '127.0.0.1', r));
  t.after(() => {
    release();
    server.closeAllConnections?.();
    return new Promise((r) => server.close(r));
  });
  return { url: `http://127.0.0.1:${server.address().port}`, release };
}

async function until(check, ms, what) {
  const end = Date.now() + ms;
  for (;;) {
    const v = await check();
    if (v) return v;
    if (Date.now() > end) throw new Error(`timed out after ${ms} ms waiting for ${what}`);
    await new Promise((r) => setTimeout(r, 100));
  }
}

test('the gateway killed during a stream: the client sees the cut, the port returns, the call is recorded once', async (t) => {
  if (!stubBin(t)) return;
  const up = await slowProvider(t);
  const s = await startServer({ runtime: RUNTIME, config: { ...SEED_CONFIG, gateway: { engine: 'front' } } });
  t.after(() => s.stop());
  const c = clientFor(s.base);
  await c.admin('providers', {
    action: 'save',
    id: 'sl',
    preset: 'custom',
    baseUrl: up.url,
    tier: 'external',
    approved: true,
    models: ['slowm', 'm1'],
    secret: { mode: 'store', value: 'sk-slow' },
  });
  const key = await c.newKey({ scopes: ['llm'] });
  const pid = (await (await fetch(`${s.base}/api/gateway/status`)).json()).pid;

  const r = await fetch(`${s.base}/v1/chat/completions`, {
    method: 'POST',
    headers: { 'content-type': 'application/json', authorization: `Bearer ${key.secret}` },
    body: JSON.stringify({ model: 'sl/slowm', stream: true, messages: [{ role: 'user', content: 'hi' }] }),
  });
  assert.equal(r.status, 200);
  const reader = r.body.getReader();
  const first = new TextDecoder().decode((await reader.read()).value);
  assert.match(first, /Hel/);
  killHard(pid);
  // The cut: the stream ends early or errors; it never brings the rest of the answer.
  let rest = '';
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      rest += new TextDecoder().decode(value);
    }
  } catch {
    /* cut */
  }
  assert.ok(!rest.includes('[DONE]'), `the stream was not cut: ${rest}`);
  up.release(); // the server's own call to the provider ends now and is recorded

  await until(
    async () => {
      try {
        const st = await (await fetch(`${s.base}/api/gateway/status`, { signal: AbortSignal.timeout(2000) })).json();
        return st.state === 'ready' && st.pid !== pid;
      } catch {
        return false;
      }
    },
    30000,
    'the restarted gateway',
  );
  // A call after the restart; once its line is in the log, the cut call's line is long written (lines are in order).
  const after = await c.call('/v1/chat/completions', {
    key: key.secret,
    body: { model: 'sl/m1', messages: [{ role: 'user', content: 'again' }] },
  });
  assert.equal(after.status, 200);
  const logs = await until(
    async () => {
      const list = await c.logs();
      return list.some((e) => e.model === 'sl/m1' || e.routedTo === 'sl/m1') ? list : null;
    },
    15000,
    'the usage line of the call after the restart',
  );
  const cut = logs.filter((e) => String(e.model).endsWith('slowm') || String(e.routedTo || '').endsWith('slowm'));
  assert.equal(cut.length, 1, JSON.stringify(logs));
});
