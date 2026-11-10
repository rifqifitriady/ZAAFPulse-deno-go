// bench/run.js: when the second server start fails, the mock provider is stopped and the run exits non-zero. The server
// is made to crash on its second launch by a preload in NODE_OPTIONS; the preload also records the mock's pid, so the
// test can check it is gone (and kill it itself if the fix ever regresses). Nothing is signalled but what this test spawns.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const os = require('os');
const path = require('path');
const { spawn } = require('child_process');

const RUN = path.join(__dirname, '..', 'bench', 'run.js');
const alive = (pid) => {
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
};

test('bench/run.js: a failed server start stops the mock and exits non-zero', { timeout: 60000 }, async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'zp-benchrun-'));
  const counter = path.join(dir, 'servers');
  const mockPid = path.join(dir, 'mock.pid');
  const preload = path.join(dir, 'preload.js');
  fs.writeFileSync(
    preload,
    `const fs = require('fs');
const main = process.argv[1] || '';
if (main.endsWith('mock-provider.js')) fs.writeFileSync(${JSON.stringify(mockPid)}, String(process.pid));
else if (main.endsWith('server.js') && process.env.ZAAFPULSE_DATA) {
  const n = fs.existsSync(${JSON.stringify(counter)}) ? Number(fs.readFileSync(${JSON.stringify(counter)}, 'utf8')) : 0;
  fs.writeFileSync(${JSON.stringify(counter)}, String(n + 1));
  if (n >= 1) process.exit(3);
}
`,
  );
  const child = spawn(process.versions.deno ? 'node' : process.execPath, [RUN], {
    env: {
      ...process.env,
      NODE_OPTIONS: `--require ${JSON.stringify(preload)}`,
      ZP_RUNTIME: 'node',
      ZP_BENCH_STARTS: '1',
      ZP_BENCH_OUT: dir,
    },
    stdio: ['ignore', 'ignore', 'pipe'],
  });
  let stderr = '';
  child.stderr.on('data', (c) => {
    stderr += c;
  });
  let timedOut = false;
  const killer = setTimeout(() => {
    timedOut = true;
    child.kill('SIGKILL');
  }, 40000);
  const code = await new Promise((resolve) => child.once('exit', (c) => resolve(c)));
  clearTimeout(killer);
  try {
    assert.equal(timedOut, false, 'run.js hung after the failed start');
    assert.notEqual(code, 0, `run.js must exit non-zero\n${stderr}`);
    assert.match(stderr, /did not start/);
    assert.ok(fs.existsSync(mockPid), 'the mock provider was started');
    const pid = Number(fs.readFileSync(mockPid, 'utf8'));
    await new Promise((resolve) => setTimeout(resolve, 200));
    const left = alive(pid);
    if (left) process.kill(pid, 'SIGKILL');
    assert.equal(left, false, 'the mock provider is still running');
  } finally {
    // Whatever failed above, the mock this test started does not outlive it.
    try {
      process.kill(Number(fs.readFileSync(mockPid, 'utf8')), 'SIGKILL');
    } catch {
      /* already gone, or never started */
    }
    fs.rmSync(dir, { recursive: true, force: true });
  }
});
