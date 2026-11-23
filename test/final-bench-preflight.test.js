// bench/final/preflight.js: every check with fakes and fixtures (macOS pmset, Linux power_supply, Windows battery
// codes, ps listings); runPreflight end to end with injected deps. Nothing here reads this machine.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const pf = require('../bench/final/preflight');
const procs = require('../bench/final/procs');

const fx = (f) => fs.readFileSync(path.join(__dirname, 'fixtures', 'final-bench', f), 'utf8');
const darwin = () => procs.parsePs(fx('ps-darwin.txt'));

test('load: below 2 over 1 and 5 minutes; Windows has none', () => {
  assert.equal(pf.checkLoad([1.2, 1.9, 4], 'darwin').status, 'pass');
  assert.equal(pf.checkLoad([2.1, 1.0, 1], 'darwin').status, 'fail');
  assert.equal(pf.checkLoad([0.5, 2.0, 1], 'linux').status, 'fail');
  assert.match(pf.checkLoad([1.2, 1.9, 4], 'darwin').detail, /1 min 1\.20, 5 min 1\.90/);
  assert.equal(pf.checkLoad([0, 0, 0], 'win32').status, 'warn');
});

test('power, macOS: pmset fixtures', () => {
  assert.equal(pf.parsePmset(fx('pmset-ac.txt')), 'ac');
  assert.equal(pf.parsePmset(fx('pmset-battery.txt')), 'battery');
  assert.equal(pf.parsePmset(fx('pmset-desktop.txt')), 'ac');
  assert.equal(pf.parsePmset(fx('pmset-ups.txt')), 'ups');
  assert.equal(pf.parsePmset('garbage'), null);
});

test('power, Linux: power_supply entries', () => {
  assert.equal(pf.linuxPower([{ type: 'Mains', online: '1' }, { type: 'Battery', status: 'Charging' }]), 'ac');
  assert.equal(pf.linuxPower([{ type: 'Mains', online: '0' }, { type: 'Battery', status: 'Discharging' }]), 'battery');
  assert.equal(pf.linuxPower([]), 'desktop');
  assert.equal(pf.linuxPower([{ type: 'Battery', status: 'Full' }]), 'ac');
  assert.equal(pf.linuxPower([{ type: 'Battery', status: 'Unknown' }]), null);
  assert.equal(pf.linuxPower(null), null);
});

test('power, Windows: BatteryStatus codes', () => {
  assert.equal(pf.parseWinBattery(fx('win-battery-ac.txt')), 'ac');
  assert.equal(pf.parseWinBattery(fx('win-battery-discharging.txt')), 'battery');
  assert.equal(pf.parseWinBattery(fx('win-battery-none.txt')), 'desktop');
  assert.equal(pf.parseWinBattery('2\r\n1\r\n'), 'battery');
  assert.equal(pf.parseWinBattery('x'), null);
});

test('power check: pass, fail, and warn with a question when unknown', () => {
  assert.equal(pf.checkPower('ac', 'pmset').status, 'pass');
  assert.equal(pf.checkPower('desktop', 'x').status, 'pass');
  assert.equal(pf.checkPower('battery', 'x').status, 'fail');
  assert.equal(pf.checkPower('ups', 'x').status, 'fail');
  const u = pf.checkPower(null, 'x');
  assert.equal(u.status, 'warn');
  assert.equal(u.ask, true);
});

test('heavy apps: the owner list and --heavy, from ps (macOS) and Windows listings', () => {
  const c = pf.checkHeavy(darwin());
  assert.equal(c.status, 'fail');
  assert.match(c.detail, /Football Manager \(1 process\)/);
  assert.match(c.detail, /Google Chrome \(2 processes\)/);
  assert.match(c.detail, /Antigravity/);
  const win = pf.checkHeavy(procs.parseWinProcs(fx('win-procs.json')));
  assert.match(win.detail, /Chrome/);
  assert.match(win.detail, /Football Manager/);
  assert.equal(pf.checkHeavy(procs.parsePs(fx('ps-linux.txt'))).status, 'fail');
  assert.equal(pf.checkHeavy([{ pid: 1, args: '/usr/bin/zoom' }]).status, 'pass');
  assert.equal(pf.checkHeavy([{ pid: 1, args: '/usr/bin/zoom' }], ['Zoom']).status, 'fail');
  assert.equal(pf.checkHeavy([{ pid: 1, args: '/usr/bin/a.b' }], ['a.b']).status, 'fail', 'a name is matched literally');
});

test('others: tests, agents and strays; own tree and the live dashboard left out', () => {
  const list = darwin();
  const own = procs.treeOf(list, [2000]);
  const live = procs.treeOf(list, [1000]);
  const c = pf.checkOthers(list, own, live);
  assert.equal(c.status, 'fail');
  for (const want of [
    'a Claude Code session (pid 1100',
    'a node test run (pid 1200',
    'a go test run (pid 1300',
    'a Go test binary (pid 1301',
    'a deno test run (pid 1400',
    'a deno process (pid 1500',
  ])
    assert.ok(c.detail.includes(want), `${want} in ${c.detail}`);
  for (const pid of [1000, 1001, 2000, 2002, 2003, 2004]) assert.ok(!c.detail.includes(`(pid ${pid}:`), String(pid));
  assert.equal(pf.checkOthers(list.filter((p) => own.has(p.pid) || live.has(p.pid)), own, live).status, 'pass');
  // Without knowing the live tree, its deno and gateway are flagged.
  assert.match(pf.checkOthers(list, own, new Set()).detail, /pid 1001/);
  // The Claude desktop app is not the CLI.
  const app = [{ pid: 5, args: '/Applications/Claude.app/Contents/MacOS/Claude' }];
  assert.equal(pf.checkOthers(app, new Set(), new Set()).status, 'pass');
});

test('top CPU: warn above 30% of a core, own processes left out', () => {
  const cpu = [
    { pid: 1, name: 'x', cpu: 80 },
    { pid: 2, name: 'y', cpu: 5 },
  ];
  assert.equal(pf.checkTopCpu(cpu, new Set()).status, 'warn');
  assert.equal(pf.checkTopCpu(cpu, new Set([1])).status, 'pass');
  assert.match(pf.checkTopCpu(cpu, new Set()).detail, /x 80%, y 5%/);
});

test('live dashboard, disk, open files, tools, gateway, keep-awake', () => {
  assert.equal(pf.checkLive([{ port: 4777, pids: [], cpu: 0 }]).detail, 'neither is running');
  assert.equal(pf.checkLive([{ port: 4777, pids: [9], cpu: 2 }]).status, 'pass');
  assert.equal(pf.checkLive([{ port: 4777, pids: [9], cpu: 25 }]).status, 'warn');
  const G = 2 ** 30;
  assert.equal(pf.checkDisk([{ dir: '/a', bytes: 50 * G }, { dir: '/b', bytes: 1.5 * G }]).status, 'fail');
  assert.equal(pf.checkDisk([{ dir: '/a', bytes: 3 * G }]).status, 'warn');
  assert.equal(pf.checkDisk([{ dir: '/a', bytes: 30 * G }]).status, 'pass');
  assert.equal(pf.checkDisk([{ dir: '/a', bytes: null }]).status, 'warn');
  assert.equal(pf.checkFiles(Infinity, 'darwin').status, 'pass');
  assert.equal(pf.checkFiles(4096, 'linux').status, 'fail');
  assert.equal(pf.checkFiles(null, 'linux').status, 'warn');
  assert.equal(pf.checkFiles(null, 'win32').status, 'pass');
  assert.equal(pf.checkTools({ go: '1.27', deno: '2.9', node: '24' }).status, 'pass');
  assert.equal(pf.checkTools({ go: null, deno: '2.9', node: '24' }).status, 'fail');
  assert.equal(pf.checkTools({ go: '1.27', deno: null, node: '24' }).status, 'warn');
  assert.equal(pf.checkGateway({ state: 'fresh' }).status, 'pass');
  assert.equal(pf.checkGateway({ state: 'missing', built: { ok: true, detail: '' } }).status, 'pass');
  assert.equal(pf.checkGateway({ state: 'stale', built: { ok: false, detail: 'boom' } }).status, 'fail');
  assert.equal(pf.checkKeepAwake('caffeinate -dims').status, 'pass');
  assert.equal(pf.checkKeepAwake(null).status, 'warn');
});

// Deps of a quiet machine; override one reading per test.
function quiet(over = {}) {
  const list = procs.parsePs(fx('ps-darwin.txt')).filter((p) => [1, 401, 1000, 1001, 2000, 2001].includes(p.pid));
  let built = 0;
  const deps = {
    platform: 'darwin',
    loadavg: () => [0.8, 1.1, 1.3],
    power: () => ({ state: 'ac', how: 'pmset -g batt' }),
    snapshot: () => list,
    sleep: async () => {},
    selfPid: 2000,
    listeners: (port) => (port === 4777 ? [1000] : []),
    freeBytes: () => 100 * 2 ** 30,
    dirs: ['/out', '/tmp'],
    heavy: [],
    hardFiles: () => Infinity,
    versions: () => ({ go: '1.27.3', deno: '2.9.8', node: '24.16.0' }),
    gateway: { state: () => 'missing', build: () => ((built += 1), { ok: true, detail: 'built' }) },
    keepAwakeTool: () => 'caffeinate -dims',
    ...over,
  };
  return { deps, builds: () => built };
}

test('runPreflight: a quiet machine passes; the gateway is built when missing; the live tree is known', async () => {
  const { deps, builds } = quiet();
  const r = await pf.runPreflight(deps);
  assert.equal(r.ok, true, JSON.stringify(r.checks, null, 1));
  assert.equal(builds(), 1);
  assert.deepEqual(
    r.checks.map((c) => c.id),
    ['load', 'power', 'heavy', 'top-cpu', 'others', 'live', 'disk', 'files', 'tools', 'gateway', 'awake', 'untouched'],
  );
  assert.deepEqual([...r.liveTree].sort(), [1000, 1001]);
  assert.equal(r.checks.find((c) => c.id === 'untouched').status, 'note');
});

test('runPreflight: the chain that started the runner is not "another" process', async () => {
  const list = [
    { pid: 1, ppid: 0, cpuSeconds: 0, args: '/sbin/launchd', name: 'launchd' },
    { pid: 50, ppid: 1, cpuSeconds: 0, args: '/bin/zsh -c deno task bench:final', name: 'zsh' },
    { pid: 60, ppid: 50, cpuSeconds: 0, args: '/opt/homebrew/bin/deno task bench:final', name: 'deno' },
    { pid: 70, ppid: 60, cpuSeconds: 0, args: '/opt/homebrew/bin/deno run scripts/final-bench.js', name: 'deno' },
  ];
  const { deps } = quiet({ snapshot: () => list, selfPid: 70 });
  const r = await pf.runPreflight(deps);
  assert.equal(r.checks.find((c) => c.id === 'others').status, 'pass');
});

test('runPreflight: a busy machine fails, and says why', async () => {
  const { deps } = quiet({
    loadavg: () => [3.5, 2.5, 2],
    power: () => ({ state: 'battery', how: 'pmset -g batt' }),
    snapshot: () => procs.parsePs(fx('ps-darwin.txt')),
  });
  const r = await pf.runPreflight(deps);
  assert.equal(r.ok, false);
  const failed = r.checks.filter((c) => c.status === 'fail').map((c) => c.id);
  assert.deepEqual(failed, ['load', 'power', 'heavy', 'others']);
});

test('runPreflight: no Go is a fail and no build is tried', async () => {
  const { deps, builds } = quiet({ versions: () => ({ go: null, deno: '2.9.8', node: null }) });
  const r = await pf.runPreflight(deps);
  assert.equal(r.ok, false);
  assert.equal(builds(), 0);
  assert.equal(r.checks.find((c) => c.id === 'gateway').status, 'fail');
});
