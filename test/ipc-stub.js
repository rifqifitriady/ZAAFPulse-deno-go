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

module.exports = { stubBin, initial, fixtureBody, killHard, alive, sleep, waitFor, tmpDir };
