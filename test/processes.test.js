const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const os = require('os');
const path = require('path');
const {
  parseVmStat,
  procRows,
  isOwn,
  groupApps,
  parsePsComm,
  argvFrom,
  linuxRows,
  linuxUsedMemory,
} = require('../lib/processes');

test('parseVmStat counts app memory, wired and compressed pages', () => {
  const out = `Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages free:                                     5219.
Pages active:                                 170957.
Pages wired down:                                 64.
Pages purgeable:                                  64.
Anonymous pages:                                 128.
Pages occupied by compressor:                     64.`;
  // (128 - 64) + 64 + 64 = 192 pages of 16 KB = 3 MB
  assert.strictEqual(parseVmStat(out), 3);
});

test('parseVmStat returns null when nothing parses', () => {
  assert.strictEqual(parseVmStat('garbage'), null);
});

test("procRows: only state T (stopped by a signal) is stopped; t (a debugger's stop) and others are not", () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'zp-procstate-'));
  fs.writeFileSync(path.join(root, 'uptime'), '1000.00 900.00');
  for (const [pid, state] of [
    [51, 'T'],
    [52, 't'],
    [53, 'S'],
  ]) {
    const d = path.join(root, String(pid));
    fs.mkdirSync(d);
    fs.writeFileSync(path.join(d, 'cmdline'), '/home/u/.local/bin/claude'.split(' ').join('\0'));
    fs.writeFileSync(path.join(d, 'stat'), `${pid} (claude) ${state} ${'0 '.repeat(10)}100 0 0 0 0 0 0 0 100 0`);
    fs.writeFileSync(path.join(d, 'status'), 'VmRSS:\t1024 kB\n');
  }
  const stopped = Object.fromEntries(procRows(root, 100).map((r) => [r.pid, r.stopped]));
  assert.deepEqual(stopped, { 51: true, 52: false, 53: false });
  fs.rmSync(root, { recursive: true, force: true });
});

test('isOwn: this process, anything run from the ZAAFPulse folder and the judge are not apps; groupApps agrees', () => {
  const r = (pid, args, extra = {}) => ({ pid, cpu: 1, rssKB: 1024, upSec: 5, args, ...extra });
  const claude = '/home/u/.local/bin/claude --resume';
  assert.equal(isOwn(r(process.pid, claude)), true);
  assert.equal(isOwn(r(8, 'node /home/u/ai-command-center/server.js claude')), true);
  assert.equal(isOwn(r(8, 'node /srv/ZAAFPulse/server.js claude')), true);
  assert.equal(isOwn(r(8, claude), new Set([8])), true);
  assert.equal(isOwn(r(8, claude, { cwd: path.join(os.tmpdir(), 'zaafpulse-judge-x1') })), true);
  assert.equal(isOwn(r(8, claude)), false);
  const rows = [r(8, claude), r(9, 'node /srv/zaafpulse/x.js claude'), r(10, claude)];
  assert.deepEqual(
    groupApps(rows, { skip: new Set([10]) }).map((a) => [a.id, a.processes]),
    [['claude', 1]],
  );
});

test('groupApps: Kiro CLI.app and its kiro_cli programs count as Kiro; look-alike paths do not', () => {
  const r = (pid, args) => ({ pid, cpu: 1, rssKB: 1024, upSec: 5, args });
  const id = (args) => (groupApps([r(8, args)])[0] || {}).id || null;
  assert.equal(id('/Applications/Kiro CLI.app/Contents/MacOS/kiro_cli_desktop'), 'kiro');
  assert.equal(id('/Users/u/Applications/Kiro CLI.app/Contents/MacOS/kiro_cli_desktop --hidden'), 'kiro');
  assert.equal(id('/Applications/Kiro CLI.app/Contents/Frameworks/Kiro Helper.app/Contents/MacOS/Kiro Helper'), 'kiro');
  assert.equal(id('/usr/local/bin/kiro_cli chat'), 'kiro');
  assert.equal(id('kiro_cli_desktop'), 'kiro');
  assert.equal(id('/Applications/Kiro.app/Contents/MacOS/Kiro'), 'kiro');
  assert.equal(id('/opt/homebrew/bin/kiro-cli chat'), 'kiro');
  assert.equal(id('/Applications/NotKiro CLI.app/Contents/MacOS/x'), null);
  assert.equal(id('/Users/u/code/my-kiro_cli-fork/run.sh'), null);
  assert.equal(id('vim /Users/u/notes/kiro_cli_notes.md'), null);
  assert.equal(id('/usr/bin/python3 /srv/mykiro_cli/app.py'), null);
  const { APPS } = require('../lib/processes');
  assert.ok(APPS.find((a) => a.id === 'kiro').paths.includes('/Applications/Kiro CLI.app'));
});

test('procRows: argv keeps each argument whole, spaces included', () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'zp-procargv-'));
  fs.writeFileSync(path.join(root, 'uptime'), '1000.00 900.00');
  const argv = ['/opt/My Tools/claude', '--resume', 'a b'];
  const d = path.join(root, '61');
  fs.mkdirSync(d);
  fs.writeFileSync(path.join(d, 'cmdline'), `${argv.join('\0')}\0`);
  fs.writeFileSync(path.join(d, 'stat'), `61 (claude) S ${'0 '.repeat(10)}100 0 0 0 0 0 0 0 100 0`);
  fs.writeFileSync(path.join(d, 'status'), 'VmRSS:\t1024 kB\n');
  const [r] = procRows(root, 100);
  assert.deepEqual(r.argv, argv);
  assert.equal(r.args, argv.join(' '));
  fs.rmSync(root, { recursive: true, force: true });
});

// macOS: ps comm is argv[0] as the process was started (or rewrote it), read whole: a full path when it was run by
// one, so a path with a space stays one word, or a bare name such as `node` when it was run from PATH.
test('parsePsComm and argvFrom: argv[0] whole, then the other arguments', () => {
  const exe = '/Users/u/Library/Application Support/Claude/claude-code/2.1.308/a9/claude.app/Contents/MacOS/claude';
  const exes = parsePsComm(`41415 ${exe}\n  1128 node\n\ngarbage\n`);
  assert.equal(exes.get(41415), exe);
  assert.equal(exes.get(1128), 'node');
  assert.equal(exes.size, 2);
  assert.deepEqual(argvFrom(`${exe} --output-format stream-json`, exe), [exe, '--output-format', 'stream-json']);
  // Run from PATH as `node`: comm is that same first word, and the script follows it.
  assert.deepEqual(argvFrom('node /usr/local/bin/claude -c', 'node'), ['node', '/usr/local/bin/claude', '-c']);
  assert.deepEqual(argvFrom(exe, exe), [exe]);
});

// lib/processes takes execFile when it loads: a fresh copy is loaded with a recording execFile, and rows() is told the
// platform (a pretend macOS), so nothing real is spawned.
async function onFakeMac(fn) {
  const cp = require('child_process');
  const file = require.resolve('../lib/processes');
  const cached = require.cache[file];
  const realExecFile = cp.execFile;
  const spawned = [];
  cp.execFile = (cmd, args, opts, cb) => {
    spawned.push(cmd);
    const out =
      cmd === 'lsof'
        ? 'p4242\nfcwd\nn/home/u/work\n'
        : args[0] === '-axo'
          ? '4242 1 1.0 1024 10:00 S /home/u/.local/bin/claude --resume\n'
          : '4242 /home/u/.local/bin/claude\n';
    setImmediate(() => cb(null, out, ''));
  };
  delete require.cache[file];
  try {
    const fresh = require('../lib/processes');
    cp.execFile = realExecFile;
    return await fn(fresh, spawned);
  } finally {
    cp.execFile = realExecFile;
    require.cache[file] = cached;
  }
}

test('rows({ cwds: false }) reads argv but never runs lsof; rows() does', async () => {
  await onFakeMac(async (fresh, spawned) => {
    const [light] = await fresh.rows({ cwds: false, platform: 'darwin' });
    assert.deepEqual(spawned, ['ps', 'ps']);
    assert.deepEqual(light.argv, ['/home/u/.local/bin/claude', '--resume']);
    assert.equal(light.cwd, undefined);
    spawned.length = 0;
    const [full] = await fresh.rows({ platform: 'darwin' });
    assert.deepEqual(spawned, ['ps', 'lsof', 'ps']);
    assert.equal(full.cwd, '/home/u/work');
  });
});

// The Windows read must load no cmdlet module: on a fresh Windows on Arm machine loading one (CimCmdlets for
// Get-CimInstance, or any command discovery) took 22-35 s per PowerShell start, past the read's 15 s limit.
test('the Windows process query turns module auto-loading off first and calls no cmdlet', () => {
  const { WIN_PS } = require('../lib/processes');
  assert.match(WIN_PS, /^\$PSModuleAutoLoadingPreference='None'; /);
  assert.doesNotMatch(WIN_PS, /\b[A-Z][a-z]+-[A-Z][A-Za-z]+\b/, 'a Verb-Noun cmdlet would load its module');
  assert.match(WIN_PS, /\[wmisearcher\]'SELECT [^']+ FROM Win32_Process'/);
});

test(
  'the real Windows process query answers well within the read limit and prints one line per process',
  { skip: process.platform !== 'win32' },
  async () => {
    const { WIN_PS } = require('../lib/processes');
    const t = Date.now();
    const out = await new Promise((resolve, reject) =>
      require('child_process').execFile(
        'powershell.exe',
        ['-NoProfile', '-NonInteractive', '-Command', WIN_PS],
        { maxBuffer: 32 * 1024 * 1024, windowsHide: true, timeout: 15000 },
        (err, stdout) => (err ? reject(err) : resolve(stdout)),
      ),
    );
    assert.ok(Date.now() - t < 15000, `took ${Date.now() - t} ms`);
    assert.ok(
      out.split(/\r?\n/).some((l) => /^\d+\D/.test(l)),
      'a line per process, led by its pid',
    );
  },
);

// Deno refuses /proc on Linux without all permissions (NotCapable, whatever --allow-read says); Node never does.
const refusal = (name = 'NotCapable') => Object.assign(new Error('Requires all access to "/proc/uptime"'), { name });

test('linuxRows: /proc refused by the runtime falls back to ps, said once', async () => {
  const said = [];
  const opts = {
    read: () => {
      throw refusal();
    },
    ps: async () => [{ pid: 1, args: 'init' }],
    warn: (m) => said.push(m),
    seen: new Set(),
  };
  assert.deepEqual(await linuxRows(opts), [{ pid: 1, args: 'init' }]);
  assert.deepEqual(
    await linuxRows({
      ...opts,
      read: () => {
        throw refusal('PermissionDenied');
      },
    }),
    [{ pid: 1, args: 'init' }],
  );
  assert.equal(said.length, 1, 'once per process');
  assert.match(said[0], /\/proc is not readable.*Requires all access.*ps instead/);
});

test('linuxRows: /proc readable is used as it is; any other error is not hidden behind ps', async () => {
  let psCalls = 0;
  const ps = async () => {
    psCalls++;
    return [];
  };
  const said = [];
  const base = { ps, warn: (m) => said.push(m), seen: new Set() };
  assert.deepEqual(await linuxRows({ ...base, read: () => [{ pid: 7 }] }), [{ pid: 7 }]);
  const gone = Object.assign(new Error('ENOENT: no such file'), { code: 'ENOENT' });
  await assert.rejects(
    linuxRows({
      ...base,
      read: () => {
        throw gone;
      },
    }),
    gone,
  );
  assert.equal(psCalls, 0);
  assert.deepEqual(said, []);
});

test('linuxUsedMemory: MemTotal - MemAvailable; refused /proc uses the OS totals, said once; no /proc is null', () => {
  const meminfo = 'MemTotal:        4194304 kB\nMemFree:          100000 kB\nMemAvailable:    1048576 kB\n';
  const said = [];
  const seen = new Set();
  const warn = (m) => said.push(m);
  assert.equal(linuxUsedMemory({ read: () => meminfo, warn, seen }), 3072);
  const refusedRead = () => {
    throw refusal();
  };
  const totals = { total: () => 4 * 1073741824, free: () => 1073741824 };
  assert.equal(linuxUsedMemory({ read: refusedRead, ...totals, warn, seen }), 3072);
  assert.equal(linuxUsedMemory({ read: refusedRead, ...totals, warn, seen }), 3072);
  assert.equal(said.length, 1, 'once per process');
  assert.match(said[0], /\/proc\/meminfo is not readable.*Requires all access/);
  const missing = () => {
    throw Object.assign(new Error('ENOENT'), { code: 'ENOENT' });
  };
  assert.equal(linuxUsedMemory({ read: missing, ...totals, warn, seen: new Set() }), null);
  assert.equal(said.length, 1, 'a missing /proc is not a refusal');
});
