// End to end: scripts/final-bench.js --smoke at tiny scale (a few scenarios; about a minute), the report and raw JSON
// stamped "SMOKE RUN, NOT A MEASUREMENT", and no process of the run left behind. Slow and machine-wide (it starts
// servers, the gateway and a keep-awake child), so it runs only with ZP_FINAL_BENCH_SMOKE=1, as the repo's other slow
// test (trace-perf, ZAAFPULSE_PERF=1):
//   ZP_FINAL_BENCH_SMOKE=1 node --test --test-timeout=600000 test/final-bench-smoke.test.js
// ZP_FINAL_BENCH_ONLY overrides the scenario list (all of them: every id from `--list`).
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const os = require('os');
const path = require('path');
const { spawn } = require('child_process');
const procs = require('../bench/final/procs');

const ROOT = path.join(__dirname, '..');
const ONLY = process.env.ZP_FINAL_BENCH_ONLY || 'private-go,route-go,p4f-otlp,egress,ipc,front-proxy,binary';

test(
  'final-bench --smoke: runs, writes a stamped report, leaves nothing running',
  { skip: process.env.ZP_FINAL_BENCH_SMOKE !== '1' && 'set ZP_FINAL_BENCH_SMOKE=1 to run', timeout: 900000 },
  async () => {
    const out = fs.mkdtempSync(path.join(os.tmpdir(), 'zp-final-smoke-'));
    const script = path.join(ROOT, 'scripts', 'final-bench.js');
    const args = ['--smoke', '--yes', '--skip-preflight', '--no-build', '--only', ONLY, '--out', out];
    const cmd = process.versions.deno
      ? ['run', '-q', '--no-prompt', '--allow-read', '--allow-write', '--allow-net=127.0.0.1,localhost', '--allow-env', '--allow-run', '--allow-sys', script, ...args]
      : [script, ...args];
    const child = spawn(process.execPath, cmd, { cwd: ROOT, stdio: ['ignore', 'pipe', 'pipe'] });
    let stdout = '';
    let stderr = '';
    child.stdout.on('data', (d) => (stdout += d));
    child.stderr.on('data', (d) => (stderr += d));
    const code = await new Promise((r) => child.once('close', r));
    try {
      assert.equal(code, 0, `${stdout}\n${stderr}`);
      assert.match(stdout, /SMOKE RUN, NOT A MEASUREMENT/);
      const md = fs.readFileSync(path.join(out, 'report.md'), 'utf8');
      assert.match(md, /^# Final benchmarks \(SMOKE RUN, NOT A MEASUREMENT\)/);
      assert.doesNotMatch(md, /\| yes \|$/m, 'nothing is quiet-confirmed in a smoke run');
      for (const id of ONLY.split(',')) {
        const j = JSON.parse(fs.readFileSync(path.join(out, `${id}.json`), 'utf8'));
        assert.equal(j.stamp, 'SMOKE RUN, NOT A MEASUREMENT', id);
        assert.equal(j.smoke, true);
        assert.equal(j.quietConfirmed, false);
        assert.equal(j.runs.length, 1, id);
        // The binary scenario has no package with --no-build: it is recorded as not run, never as measured.
        if (id === 'binary') assert.equal(j.runs[0].ok, false);
        else assert.equal(j.runs[0].ok, true, `${id}: ${j.runs[0].error}`);
      }
      const session = JSON.parse(fs.readFileSync(path.join(out, 'session.json'), 'utf8'));
      assert.equal(session.stamp, 'SMOKE RUN, NOT A MEASUREMENT');
      // Nothing of the run left: no keep-awake child of it, no server or bench of this tree.
      await new Promise((r) => setTimeout(r, 500));
      const left = procs
        .listProcesses()
        .filter((p) => p.args.includes(`-w ${child.pid}`) || p.args.includes(path.join(ROOT, 'server.js')) || p.args.includes(path.join(ROOT, 'bench')));
      assert.deepEqual(left, [], 'processes left running');
    } finally {
      fs.rmSync(out, { recursive: true, force: true });
    }
  },
);
