// S7's kill-IPC test end to end (P4e, spec Decision 21b): the server with chat on the go engine; the control plane is
// stopped (SIGSTOP) for 6 s, longer than the 5 s after which a budget snapshot is stale. Calls through the main port
// still answer (the gateway serves them alone), a model whose budget the last snapshot showed exceeded is still refused,
// and once the control plane runs again (SIGCONT) the calls are stored with budget "unchecked" and the supervisor
// raises its alert. macOS and Linux: Windows has no SIGSTOP (the Go test TestHungControlPlaneBudgetsFailOpen covers
// the gateway's side there).
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const http = require('http');
const path = require('path');
const { startServer, clientFor, SEED_CONFIG } = require('./contract/harness');
const { stubBin } = require('./ipc-stub');

const RUNTIME = process.versions && process.versions.deno ? 'deno' : 'node';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
async function until(check, ms, what) {
  const end = Date.now() + ms;
  for (;;) {
    // After SIGCONT the supervisor's watchdog may restart the gateway (its health lines came late): the main port is
    // away for a moment, so a failed request is a "not yet".
    const v = await check().catch(() => null);
    if (v) return v;
    if (Date.now() > end) throw new Error(`timed out after ${ms} ms waiting for ${what}`);
    await sleep(100);
  }
}

// A provider that answers every chat with 1000 + 500 tokens: US$0.002 at the contract prices of m1 and m2.
async function provider(t) {
  const server = http.createServer(async (req, res) => {
    let body = '';
    for await (const c of req) body += c;
    const model = JSON.parse(body || '{}').model;
    res.writeHead(200, { 'content-type': 'application/json' });
    res.end(
      JSON.stringify({
        id: 'c1',
        object: 'chat.completion',
        model,
        choices: [{ index: 0, message: { role: 'assistant', content: 'hi' }, finish_reason: 'stop' }],
        usage: { prompt_tokens: 1000, completion_tokens: 500 },
      }),
    );
  });
  await new Promise((r) => server.listen(0, '127.0.0.1', r));
  t.after(() => new Promise((r) => server.close(r)));
  return `http://127.0.0.1:${server.address().port}`;
}

test(
  'a hung control plane: metered calls pass marked unchecked and alerted, a spent budget is still refused',
  { skip: process.platform === 'win32' && 'no SIGSTOP on Windows' },
  async (t) => {
    if (!stubBin(t)) return;
    const up = await provider(t);
    const gateway = {
      engine: 'front',
      engines: { chat: 'go', 'chat-stream': 'go' },
      shadowDays: 0,
      shadowMinCompares: 0,
    };
    const s = await startServer({ runtime: RUNTIME, config: { ...SEED_CONFIG, gateway } });
    let stopped = false;
    t.after(() => {
      if (stopped) process.kill(s.pid, 'SIGCONT');
      return s.stop();
    });
    const c = clientFor(s.base);
    await c.admin('providers', {
      action: 'save',
      id: 'hp',
      preset: 'custom',
      baseUrl: `${up}/v1`,
      tier: 'external',
      approved: true,
      models: ['m1', 'm2'],
      secret: { mode: 'store', value: 'sk-hang' },
    });
    const key = await c.newKey({ scopes: ['llm'] });
    const chat = (model) =>
      c.call('/v1/chat/completions', { key: key.secret, body: { model, messages: [{ role: 'user', content: 'x' }] } });
    // A model budget of a millionth of a dollar: one call uses it up; the snapshot then says exceeded.
    const b = await c.call('/api/cost/budgets', {
      body: {
        id: 'hang-m2',
        name: 'Hang m2',
        scope: { dim: 'model', value: 'm2' },
        period: 'month',
        amount: 0.000001,
        enforce: 'block',
      },
    });
    assert.equal(b.status, 200, b.text);
    assert.equal((await chat('hp/m2')).status, 200);
    await until(async () => (await chat('hp/m2')).status === 402, 5000, 'the budget refused');
    await sleep(1500); // a snapshot that says exceeded is in force on the gateway

    process.kill(s.pid, 'SIGSTOP'); // the control plane only: the gateway is its child and keeps running
    stopped = true;
    await sleep(6000); // past the 5 s after which the snapshot is stale
    const answers = [];
    for (let i = 0; i < 3; i++) answers.push((await chat('hp/m1')).status);
    const refused = await chat('hp/m2');
    process.kill(s.pid, 'SIGCONT');
    stopped = false;

    assert.deepEqual(answers, [200, 200, 200]);
    assert.equal(refused.status, 402);
    assert.equal(refused.json.error.message, 'budget "Hang m2" is used up for this month');
    const lines = await until(
      async () => {
        const list = (await c.logs()).filter((e) => e.model === 'hp/m1' && e.keyId === key.id);
        return list.length >= 3 ? list : null;
      },
      30000,
      'the three lines stored',
    );
    assert.equal(lines.length, 3, 'each call stored once');
    assert.ok(
      lines.every((e) => e.budget === 'unchecked' && e.ipc),
      JSON.stringify(lines),
    );
    const st = await until(
      async () => {
        const v = await (await fetch(`${s.base}/api/gateway/status`)).json();
        return v.unchecked && v.unchecked.count >= 3 ? v : null;
      },
      30000,
      'the unchecked calls counted',
    );
    assert.ok(st.alert === true, JSON.stringify(st));
    // P4f D24 (design rule 2: mark, alert and audit): the alert window's "opened" entry, written as the system when its
    // first call is stored.
    const audit = () =>
      fs
        .readdirSync(path.join(s.data, '.data', 'audit'))
        .filter((f) => f.endsWith('.jsonl'))
        .flatMap((f) =>
          fs
            .readFileSync(path.join(s.data, '.data', 'audit', f), 'utf8')
            .split('\n')
            .filter(Boolean),
        )
        .map((l) => JSON.parse(l))
        .filter((e) => e.action === 'gateway.budget_unchecked');
    const entries = await until(async () => (audit().length ? audit() : null), 10000, 'the audit entry');
    assert.equal(entries.length, 1, JSON.stringify(entries));
    assert.deepEqual(
      [
        entries[0].phase,
        entries[0].outcome,
        entries[0].actor,
        entries[0].target,
        entries[0].calls,
        typeof entries[0].since,
      ],
      ['opened', 'error', { via: 'system' }, { type: 'gateway' }, 1, 'string'],
    );
    // The "closed" entry, with the window's count, comes a minute later or at stop (test/ipc-supervisor.test.js).
  },
);
