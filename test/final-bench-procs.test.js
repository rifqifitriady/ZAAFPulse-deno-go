// bench/final/procs.js: the process listings of macOS, Linux and Windows parsed from fixtures, the CPU between two
// snapshots, a process tree, and stopAll (fakes for the listing and the kill: nothing is signalled).
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const procs = require('../bench/final/procs');

const fx = (f) => fs.readFileSync(path.join(__dirname, 'fixtures', 'final-bench', f), 'utf8');

test('parseCpuTime: the macOS and Linux forms', () => {
  assert.equal(procs.parseCpuTime('0:00.03'), 0.03);
  assert.equal(procs.parseCpuTime('125:03.12'), 125 * 60 + 3.12);
  assert.equal(procs.parseCpuTime('00:10:00'), 600);
  assert.equal(procs.parseCpuTime('1-02:03:04'), 86400 + 2 * 3600 + 3 * 60 + 4);
  for (const bad of ['', 'abc', '1:2:3:4', '-1', null]) assert.ok(Number.isNaN(procs.parseCpuTime(bad)), String(bad));
});

test('parsePs: macOS, with app bundles and spaces in paths', () => {
  const list = procs.parsePs(fx('ps-darwin.txt'));
  assert.equal(list.length, 19);
  const fm = list.find((p) => p.pid === 900);
  assert.equal(fm.name, 'fm');
  assert.match(fm.args, /Football Manager 2026\.app/);
  assert.equal(fm.cpuSeconds, 14 * 60 + 2.33);
  assert.equal(list.find((p) => p.pid === 910).name, 'Google Chrome');
  assert.equal(list.find((p) => p.pid === 1001).name, 'zaafpulse-gw');
  assert.equal(list.find((p) => p.pid === 1100).name, 'node');
  assert.equal(list.find((p) => p.pid === 1001).ppid, 1000);
});

test('parsePs: Linux, days in the time, junk lines skipped', () => {
  const list = procs.parsePs(fx('ps-linux.txt'));
  assert.equal(list.length, 5);
  assert.equal(list.find((p) => p.pid === 812).cpuSeconds, 93784);
  assert.equal(list.find((p) => p.pid === 1500).name, 'chrome');
});

test('parseWinProcs: an array, a single object, bad JSON', () => {
  const list = procs.parseWinProcs(fx('win-procs.json'));
  assert.equal(list.length, 4);
  const chrome = list.find((p) => p.pid === 4120);
  assert.equal(chrome.name, 'chrome');
  assert.equal(chrome.cpuSeconds, 5);
  assert.equal(list.find((p) => p.pid === 0).args, 'System Idle Process');
  const one = procs.parseWinProcs(fx('win-procs-one.json'));
  assert.deepEqual(one, [{ pid: 7, ppid: 1, cpuSeconds: 0, args: 'zaafpulse-gw.exe', name: 'zaafpulse-gw' }]);
  assert.deepEqual(procs.parseWinProcs('not json'), []);
  assert.deepEqual(procs.parseWinProcs(''), []);
});

test('listProcesses: the command per platform, [] on a failure', () => {
  const seen = [];
  const exec = (cmd, args) => {
    seen.push(cmd);
    if (cmd === 'ps') return { status: 0, stdout: fx('ps-linux.txt') };
    return { status: 0, stdout: fx('win-procs.json') };
  };
  assert.equal(procs.listProcesses({ platform: 'linux', exec }).length, 5);
  assert.equal(procs.listProcesses({ platform: 'win32', exec }).length, 4);
  assert.deepEqual(seen, ['ps', 'powershell']);
  assert.deepEqual(procs.listProcesses({ platform: 'darwin', exec: () => ({ status: 1, stdout: '' }) }), []);
});

test('treeOf: a process and its descendants', () => {
  const list = procs.parsePs(fx('ps-darwin.txt'));
  assert.deepEqual([...procs.treeOf(list, [2000])].sort(), [2000, 2001, 2002, 2003, 2004]);
  assert.deepEqual([...procs.treeOf(list, [1300])].sort(), [1300, 1301]);
});

test('cpuBetween: percent of one core; a reused pid counts from zero', () => {
  const a = [
    { pid: 1, ppid: 0, cpuSeconds: 10, args: 'x', name: 'x' },
    { pid: 2, ppid: 0, cpuSeconds: 5, args: 'y', name: 'y' },
  ];
  const b = [
    { pid: 1, ppid: 0, cpuSeconds: 12, args: 'x', name: 'x' },
    { pid: 2, ppid: 0, cpuSeconds: 1, args: 'z', name: 'z' },
    { pid: 3, ppid: 0, cpuSeconds: 0.5, args: 'w', name: 'w' },
  ];
  const r = procs.cpuBetween(a, b, 4);
  assert.deepEqual(
    r.map((p) => [p.pid, p.cpu]),
    [
      [1, 50],
      [2, 25],
      [3, 12.5],
    ],
  );
});

test('stopAll: TERM, then KILL what is left; a reused pid is left alone', async () => {
  let alive = new Map([
    [10, 'a'],
    [11, 'b'],
    [12, 'other now'],
  ]);
  const signals = [];
  const kill = (pid, sig) => {
    signals.push([pid, sig]);
    if (sig === 'SIGKILL' || pid === 10) alive.delete(pid);
  };
  const list = () => [...alive].map(([pid, args]) => ({ pid, args }));
  const left = await procs.stopAll(
    [
      { pid: 10, args: 'a' },
      { pid: 11, args: 'b' },
      { pid: 12, args: 'c' },
    ],
    { platform: 'darwin', kill, list, graceMs: 300 },
  );
  assert.deepEqual(left, []);
  assert.deepEqual(signals, [
    [10, 'SIGTERM'],
    [11, 'SIGTERM'],
    [11, 'SIGKILL'],
  ]);
  assert.ok(alive.has(12));
  alive = new Map([[20, 'w']]);
  const calls = [];
  await procs.stopAll([{ pid: 20, args: 'w' }], {
    platform: 'win32',
    list,
    exec: (cmd, args) => {
      calls.push([cmd, ...args]);
      alive.delete(20);
      return { status: 0 };
    },
  });
  assert.deepEqual(calls, [['taskkill', '/PID', '20', '/T', '/F']]);
});
