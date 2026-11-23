// bench/final/runner.js and bench/final/keepawake.js: the children's scrubbed environment, the commands, the noise
// sampler (fake process listings) and the keep-awake command per platform. Nothing is spawned here.
const test = require('node:test');
const assert = require('node:assert/strict');
const runner = require('../bench/final/runner');
const { keepAwakeCommand, startKeepAwake } = require('../bench/final/keepawake');

test('childEnv: no inherited bench knobs, instance URL, OTLP or proxy; loopback exempt', () => {
  const env = runner.childEnv(
    { ZP_ENGINE: 'go' },
    {
      PATH: '/bin',
      HOME: '/home/me',
      ZP_BENCH_LEVELS: '9',
      ZP_CONTRACT_URL: 'http://127.0.0.1:4777',
      ZP_ENGINE: 'front',
      ZAAFPULSE_PORT: '4777',
      ZAAFPULSE_DATA: '/home/me/.zaafpulse',
      OTEL_EXPORTER_OTLP_ENDPOINT: 'https://collector',
      HTTPS_PROXY: 'http://proxy:8080',
      http_proxy: 'http://proxy:8080',
      NODE_OPTIONS: '--require x',
    },
  );
  assert.deepEqual(env, {
    PATH: '/bin',
    HOME: '/home/me',
    NO_PROXY: '127.0.0.1,localhost,::1',
    no_proxy: '127.0.0.1,localhost,::1',
    ZP_ENGINE: 'go',
  });
});

test('jsCommand and withFileLimit', () => {
  const c = runner.jsCommand('/r/bench/run.js', ['--x'], '/r');
  assert.equal(c.cmd, process.execPath);
  if (process.versions.deno) assert.deepEqual(c.args.slice(-3), ['-P=test', '/r/bench/run.js', '--x']);
  else assert.deepEqual(c.args, ['/r/bench/run.js', '--x']);
  if (process.platform !== 'win32') {
    const w = runner.withFileLimit({ cmd: 'node', args: ['a b.js'] });
    assert.equal(w.cmd, 'sh');
    assert.match(w.args[1], /ulimit -n 8192/);
    assert.deepEqual(w.args.slice(2), ['node', 'a b.js']);
  }
});

test('lastJson: the last JSON object line of an output', () => {
  assert.deepEqual(runner.lastJson('noise\n{"a":1}\n{"b":2}\ntrailing text\n'), { b: 2 });
  assert.equal(runner.lastJson('nothing'), null);
  assert.equal(runner.lastJson('3\n"str"'), null);
});

test('platformName: the release names', () => {
  assert.equal(runner.platformName('darwin', 'arm64'), 'darwin-arm64');
  assert.equal(runner.platformName('win32', 'x64'), 'win-x64');
  assert.equal(runner.platformName('linux', 'arm64'), 'linux-arm64');
});

test('sampler: foreign CPU outside the tree, noisy past the limits, every tree process remembered', () => {
  let t = 0;
  const snaps = [
    [
      { pid: 1, ppid: 0, cpuSeconds: 0, args: 'init', name: 'init' },
      { pid: 10, ppid: 1, cpuSeconds: 0, args: 'runner', name: 'runner' },
      { pid: 11, ppid: 10, cpuSeconds: 0, args: 'server', name: 'server' },
      { pid: 20, ppid: 1, cpuSeconds: 0, args: 'chrome', name: 'chrome' },
      { pid: 30, ppid: 1, cpuSeconds: 0, args: 'kernel_task', name: 'kernel_task' },
    ],
  ];
  const list = () => snaps[Math.min(t, snaps.length - 1)];
  const realNow = Date.now;
  let clock = 1_000_000;
  Date.now = () => clock;
  try {
    const s = runner.createSampler({ selfPid: 10, list, loadavg: () => [1.5, 1, 1] });
    s.take();
    const from = s.mark();
    // 2 s later: the server used 2 s of CPU (ours), chrome 0.4 s (20%), kernel_task 4 s (ignored).
    snaps.push([
      { pid: 1, ppid: 0, cpuSeconds: 0, args: 'init', name: 'init' },
      { pid: 10, ppid: 1, cpuSeconds: 0, args: 'runner', name: 'runner' },
      { pid: 11, ppid: 10, cpuSeconds: 2, args: 'server', name: 'server' },
      { pid: 20, ppid: 1, cpuSeconds: 0.4, args: 'chrome', name: 'chrome' },
      { pid: 30, ppid: 1, cpuSeconds: 4, args: 'kernel_task', name: 'kernel_task' },
    ]);
    t = 1;
    clock += 2000;
    s.take();
    const quiet = s.window(from);
    assert.equal(quiet.noisy, false);
    assert.equal(quiet.noise.foreignMean, 20);
    assert.deepEqual(quiet.noise.busiest, ['chrome 20%', 'init 0%']);
    assert.equal(s.window(from, { meanLimit: 10 }).noisy, true);
    assert.deepEqual(
      s.seen().map((p) => p.pid),
      [11],
    );
    assert.deepEqual(s.loadStats(), { min: 1.5, mean: 1.5, max: 1.5 });
    assert.deepEqual(s.window(99), { noisy: null, noise: null });
  } finally {
    Date.now = realNow;
  }
});

test('keep-awake: the command per platform, each ending with the runner', () => {
  assert.deepEqual(keepAwakeCommand('darwin', 42), { name: 'caffeinate', cmd: 'caffeinate', args: ['-dims', '-w', '42'] });
  const l = keepAwakeCommand('linux', 42);
  assert.equal(l.cmd, 'systemd-inhibit');
  assert.ok(l.args.includes('--pid=42'));
  const w = keepAwakeCommand('win32', 42);
  assert.equal(w.cmd, 'powershell');
  assert.match(w.args.at(-1), /SetThreadExecutionState\(\[uint32\]2147483651\); Wait-Process -Id 42$/);
  assert.equal(keepAwakeCommand('aix', 42), null);
  // A fake spawn: started with the command, stopped once.
  const calls = [];
  const child = { pid: 7, handlers: {}, on(ev, fn) { this.handlers[ev] = fn; }, kill() { calls.push('kill'); this.handlers.exit(); } };
  const k = startKeepAwake({ platform: 'darwin', pid: 42, spawnFn: (cmd, args) => (calls.push([cmd, ...args]), child) });
  assert.equal(k.name, 'caffeinate');
  assert.equal(k.running(), true);
  k.stop();
  k.stop();
  assert.deepEqual(calls, [['caffeinate', '-dims', '-w', '42'], 'kill']);
  assert.equal(k.running(), false);
  assert.equal(startKeepAwake({ platform: 'aix' }), null);
});
