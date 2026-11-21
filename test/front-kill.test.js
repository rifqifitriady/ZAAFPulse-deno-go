// Kill test of the Go front (P4a T6): the gateway is killed while a streamed chat answer goes through it. The client
// sees the stream cut, the supervisor restarts the gateway and the main port answers again, and the call is recorded
// once in the usage log (the server answered it; the gateway records nothing in P4a): no line lost, none twice.
// P4c: the same with the stream served by the go engine (chat and chat-stream on go). P4f D14: the gateway dies with the
// call, and the restarted gateway writes its line from the call journal (status 599, "the gateway stopped before the
// call ended"): exactly one line, never two. Also for a whole (non-streamed) answer the gateway dies waiting for. The
// call after the restart has exactly one line too (P4a T6-2). P4h Decision 10: a route call the go engine serves is
// journalled per leg; killed during its second leg, it leaves the first leg's fallback line and exactly one recovered
// line for the second.
const test = require('node:test');
const assert = require('node:assert/strict');
const http = require('http');
const { startServer, clientFor, SEED_CONFIG } = require('./contract/harness');
const { stubBin, killHard } = require('./ipc-stub');

const RUNTIME = process.versions && process.versions.deno ? 'deno' : 'node';

/** A provider that streams one chunk (slowm), or answers nothing (slowwhole), until release(); arrived resolves when a
 * slow call reached it. */
async function slowProvider(t) {
  let release;
  let arrive;
  const released = new Promise((r) => (release = r));
  const arrived = new Promise((r) => (arrive = r));
  const server = http.createServer(async (req, res) => {
    let body = '';
    for await (const c of req) body += c;
    const model = JSON.parse(body || '{}').model;
    const chunk = (delta, extra = {}) =>
      `data: ${JSON.stringify({ id: 's1', object: 'chat.completion.chunk', model, choices: [{ index: 0, delta, ...extra }] })}\n\n`;
    if (model === 'fail429') {
      res.writeHead(429, { 'content-type': 'application/json' });
      res.end(JSON.stringify({ error: { message: 'slow down' } }));
      return;
    }
    if (model === 'slowwhole') {
      arrive();
      await released;
    }
    if (model === 'slowm') {
      arrive();
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
  return { url: `http://127.0.0.1:${server.address().port}`, release, arrived };
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

const GO_GATEWAY = {
  engine: 'front',
  engines: { chat: 'go', 'chat-stream': 'go' },
  shadowDays: 0,
  shadowMinCompares: 0,
};

async function killDuringStream(t, { goEngine, stream = true, route = false }) {
  if (!stubBin(t)) return;
  const up = await slowProvider(t);
  const gateway = !goEngine
    ? { engine: 'front' }
    : route
      ? { ...GO_GATEWAY, engines: { ...GO_GATEWAY.engines, routes: 'go' } }
      : GO_GATEWAY;
  const s = await startServer({ runtime: RUNTIME, config: { ...SEED_CONFIG, gateway } });
  t.after(() => s.stop());
  // The gateway in front must be up before the stream starts: one that failed to start (Deno serving the fallback)
  // would leave nothing to kill, and the stream would wait for a cut that never comes (P4a final review T6-1).
  const before = await (await fetch(`${s.base}/api/gateway/status`)).json();
  assert.deepEqual([before.mode, before.state], ['front', 'ready'], JSON.stringify(before));
  assert.ok(Number.isInteger(before.pid) && before.pid > 0, `no gateway pid: ${JSON.stringify(before)}`);
  const pid = before.pid;
  const c = clientFor(s.base);
  await c.admin('providers', {
    action: 'save',
    id: 'sl',
    preset: 'custom',
    baseUrl: up.url,
    tier: 'external',
    approved: true,
    models: ['slowm', 'slowwhole', 'fail429', 'm1'],
    secret: { mode: 'store', value: 'sk-slow' },
  });
  // A route whose first leg falls back (429) and whose second leg is held until the gateway is killed.
  if (route)
    await c.admin('routes', {
      action: 'save',
      name: 'router/killer',
      targets: ['sl/fail429', 'sl/slowwhole'],
      strategy: 'fallback',
    });
  const key = await c.newKey({ scopes: ['llm'] });

  if (stream) {
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
  } else {
    // A whole answer: the provider holds the call; the gateway dies while it waits.
    const pending = fetch(`${s.base}/v1/chat/completions`, {
      method: 'POST',
      headers: { 'content-type': 'application/json', authorization: `Bearer ${key.secret}` },
      body: JSON.stringify({
        model: route ? 'router/killer' : 'sl/slowwhole',
        messages: [{ role: 'user', content: 'hi' }],
      }),
    }).then(
      (r) => r.status,
      () => 'cut',
    );
    await up.arrived;
    killHard(pid);
    assert.equal(await pending, 'cut');
  }
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
  if (route) {
    // The first leg's fallback line (written before the kill) and one recovered line for the leg in flight.
    const lines = logs.filter((e) => e.model === 'router/killer');
    assert.equal(lines.length, 2, `two lines for the route call: ${JSON.stringify(logs)}`);
    const fell = lines.filter((e) => e.status === 429);
    assert.equal(fell.length, 1, JSON.stringify(lines));
    assert.equal(fell[0].routedTo, 'sl/fail429');
    assert.equal(fell[0].error, 'fell back to the next route target');
    assert.ok(fell[0].ipc, `the go engine wrote it: ${JSON.stringify(fell[0])}`);
    const cut = lines.filter((e) => e.status === 599);
    assert.equal(cut.length, 1, JSON.stringify(lines));
    assert.ok(cut[0].ipc, `the go engine's journal wrote it: ${JSON.stringify(cut[0])}`);
    assert.equal(cut[0].error, 'the gateway stopped before the call ended');
    assert.equal(cut[0].cost, 0);
    assert.equal(cut[0].provider, 'sl');
  }
  const slow = route ? 'slowwhole' : stream ? 'slowm' : 'slowwhole';
  const cut = logs.filter((e) => String(e.model).endsWith(slow) || String(e.routedTo || '').endsWith(slow));
  assert.equal(cut.length, 1, `exactly one line for the cut call: ${JSON.stringify(logs)}`);
  if (goEngine) {
    // Written by the restarted gateway from its call journal (P4f D14, D15): through the IPC ingest, status 599.
    assert.ok(cut[0].ipc, `the go engine's journal wrote it: ${JSON.stringify(cut[0])}`);
    assert.equal(cut[0].status, 599);
    assert.equal(cut[0].error, 'the gateway stopped before the call ended');
    assert.equal(cut[0].cost, 0);
  }
  // P4a T6-2: the call after the restart has exactly one line, not just one.
  const again = logs.filter((e) => String(e.model).endsWith('/m1') || String(e.routedTo || '').endsWith('/m1'));
  assert.equal(again.length, 1, JSON.stringify(logs));
}

test('the gateway killed during a stream: the client sees the cut, the port returns, the call is recorded once', (t) =>
  killDuringStream(t, { goEngine: false }));

test('the gateway killed during a stream it serves itself: the cut, the port returns, exactly one line (P4f D14)', (t) =>
  killDuringStream(t, { goEngine: true }));

test('the gateway killed while it waits for a whole answer it serves: exactly one line, from the journal (P4f D14)', (t) =>
  killDuringStream(t, { goEngine: true, stream: false }));

test('the gateway killed during the second leg of a route call it serves: the fallback line and one recovered line (P4h D10)', (t) =>
  killDuringStream(t, { goEngine: true, stream: false, route: true }));
