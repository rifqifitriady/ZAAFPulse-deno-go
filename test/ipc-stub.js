// Helpers of the cross-language IPC tests (not a test file): the gateway stub binary, the first config, secrets and
// snapshot it needs (from the shared fixtures), a hard kill (taskkill /F on Windows), waiting, temp folders.
const fs = require('fs');
const os = require('os');
const path = require('path');
const { spawnSync } = require('child_process');
const { buildStub } = require('../scripts/ipc/build-stub');
const { DIR } = require('../lib/ipc/schema');

let built; // one build per test process: deno test runs every file in one process
/** The stub's path, or null after t.skip() when there is no Go. */
function stubBin(t) {
  if (built === undefined) built = buildStub();
  if (built.skip) {
    t.skip(built.skip);
    return null;
  }
  return built.bin;
}

const fixtureBody = (rel) => JSON.parse(fs.readFileSync(path.join(DIR, 'fixtures', rel), 'utf8')).body;
/** config.push, secrets.handoff (both gen 1) and a budget snapshot taken now, without budgets. */
const initial = (at = Date.now()) => ({
  config: fixtureBody('config.push/valid-minimal.json'),
  secrets: fixtureBody('secrets.handoff/valid.json'),
  snapshot: { ...fixtureBody('budget.snapshot/valid-empty.json'), at },
});

// The JS twin of gw/ipc/synthetic.go Synthetic(): the event the stub's --emit sends as `seq`, as [type, body].
const EMIT_BASE = 1767225600000;
function synthetic(seq, base = EMIT_BASE) {
  const round = Math.floor((seq - 1) / 3) + 1;
  const ts = base + round;
  const kind = (seq - 1) % 3;
  if (kind === 0)
    return [
      'usage.report',
      {
        record: {
          ts,
          keyId: 'k-emit',
          provider: 'stub',
          model: 'stub/model-a',
          kind: 'chat',
          format: 'openai',
          stream: false,
          status: 200,
          ms: 12,
          input: 1000,
          output: 500,
          cached: 0,
          cost: 1500000,
          sessionId: `emit-${round}`,
        },
      },
    ];
  if (kind === 1)
    return round % 2 === 1
      ? [
          'audit.event',
          {
            log: 'governance',
            event: {
              action: 'key.update',
              target: { type: 'key', id: 'k-emit', name: `emit ${round}` },
              actor: { via: 'apikey', keyId: 'k-emit' },
            },
          },
        ]
      : [
          'audit.event',
          {
            log: 'mcp',
            event: { ts, integration: 'stub', tool: 'echo', ok: true, ms: 3, args: ['text'], keyId: 'k-emit' },
          },
        ];
  return [
    'trace.span',
    {
      span: {
        t: round.toString(16).padStart(32, '0'),
        s: round.toString(16).padStart(16, '0'),
        p: null,
        n: 'chat stub/model-a',
        k: 'client',
        st: ts * 1000,
        d: 12000,
        ok: true,
        src: 'gateway',
        svc: 'zaafpulse',
        a: { 'gen_ai.request.model': 'model-a' },
      },
    },
  ];
}

function killHard(pid) {
  if (!pid) return;
  if (process.platform === 'win32') spawnSync('taskkill', ['/F', '/T', '/PID', String(pid)], { stdio: 'ignore' });
  else
    try {
      process.kill(pid, 'SIGKILL');
    } catch {
      /* already gone */
    }
}

function alive(pid) {
  try {
    process.kill(pid, 0);
    return true;
  } catch (err) {
    return err.code === 'EPERM';
  }
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
async function waitFor(fn, ms = 10000, what = 'a condition') {
  const end = Date.now() + ms;
  while (!fn()) {
    if (Date.now() > end) throw new Error(`timed out after ${ms} ms waiting for ${what}`);
    await sleep(10);
  }
}

function tmpDir(t, name) {
  const d = fs.mkdtempSync(path.join(os.tmpdir(), `zp-ipc-${name}-`));
  t.after(() => fs.rmSync(d, { recursive: true, force: true }));
  return d;
}

module.exports = { stubBin, initial, fixtureBody, synthetic, EMIT_BASE, killHard, alive, sleep, waitFor, tmpDir };
