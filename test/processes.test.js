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
  linuxLimits,
  linuxExe,
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

test('linuxRows: /proc refused and no ps either: the error stays, said once', async () => {
  const said = [];
  const state = {};
  const gone = Object.assign(new Error('spawn ps ENOENT'), { code: 'ENOENT' });
  const opts = {
    read: () => {
      throw refusal();
    },
    ps: async () => {
      throw gone;
    },
    warn: (m) => said.push(m),
    seen: new Set(),
    state,
  };
  await assert.rejects(linuxRows(opts), gone);
  await assert.rejects(linuxRows(opts), gone);
  assert.equal(state.ps, false);
  assert.equal(said.length, 2, 'the /proc fallback, then the missing ps, each once');
  assert.match(said[1], /ps is not installed, so no process can be listed/);
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

// ---- Linux when the runtime refuses /proc (Deno): argv from ps comm and working folders from lsof, as on macOS ----

// A fresh lib/processes over a fake execFile that answers as Linux procps and lsof would (`lsof: false` = not
// installed), with /proc refused. Returns { fresh, spawned, linux } where `linux` is rows()'s option for that state.
async function onFakeLinuxWithoutProc(fn, { lsof = true } = {}) {
  const cp = require('child_process');
  const file = require.resolve('../lib/processes');
  const cached = require.cache[file];
  const realExecFile = cp.execFile;
  const spawned = [];
  cp.execFile = (cmd, args, opts, cb) => {
    spawned.push(args[0] === '-axo' ? 'ps -axo' : cmd === 'ps' ? 'ps comm' : cmd);
    if (cmd === 'lsof' && !lsof)
      return setImmediate(() => cb(Object.assign(new Error('spawn lsof ENOENT'), { code: 'ENOENT' }), '', ''));
    const out =
      cmd === 'lsof'
        ? 'p4242\nfcwd\nn/home/u/work\np4343\nfcwd\nn/home/u/other\n'
        : args[0] === '-axo'
          ? [
              '    1     0  0.0  1024    10:00 Ss   /sbin/init',
              ' 4242     1  1.0  1024    10:00 S    /home/u/.local/bin/claude --resume',
              ' 4343     1  0.5  2048    05:00 S    node /home/u/.npm/lib/node_modules/@openai/codex/bin/codex.js',
            ].join('\n')
          : // procps comm: the kernel task name, cut at 15 characters
            ' 4242 claude\n 4343 node\n';
    setImmediate(() => cb(null, out, ''));
  };
  delete require.cache[file];
  try {
    const fresh = require('../lib/processes');
    cp.execFile = realExecFile;
    const said = [];
    const linux = {
      read: () => {
        throw refusal();
      },
      state: { procRefused: false, lsof: undefined },
      warn: (m) => said.push(m),
      seen: new Set(),
    };
    return await fn({ fresh, spawned, linux, said });
  } finally {
    cp.execFile = realExecFile;
    require.cache[file] = cached;
  }
}

test('linuxExe: argv[0] is the whole first word when its name starts with the cut task name, else the name', () => {
  assert.equal(
    linuxExe('/opt/x/codex-x86_64-unknown-linux-musl exec', 'codex-x86_64-un'),
    '/opt/x/codex-x86_64-unknown-linux-musl',
  );
  assert.equal(linuxExe('/usr/bin/node /x/cli.js', 'node'), '/usr/bin/node');
  assert.equal(linuxExe('claude --resume', 'claude'), 'claude');
  assert.equal(linuxExe('MainThread', 'claude'), 'claude', 'a rewritten title: the task name');
});

test('Linux without /proc: rows() gets argv from ps comm and working folders from lsof, like macOS', async () => {
  await onFakeLinuxWithoutProc(async ({ fresh, spawned, linux, said }) => {
    const all = await fresh.rows({ platform: 'linux', linux });
    assert.deepEqual(spawned, ['ps -axo', 'lsof', 'ps comm']);
    const by = Object.fromEntries(all.map((r) => [r.pid, r]));
    assert.deepEqual(by[4242].argv, ['/home/u/.local/bin/claude', '--resume']);
    assert.equal(by[4242].cwd, '/home/u/work');
    assert.deepEqual(by[4343].argv, ['node', '/home/u/.npm/lib/node_modules/@openai/codex/bin/codex.js']);
    assert.equal(by[4343].cwd, '/home/u/other');
    assert.equal(by[1].argv, undefined, 'only AI app processes are looked up');
    assert.equal(said.length, 1);
    assert.match(said[0], /\/proc is not readable.*ps instead, and the working folders of AI agents with lsof/);
    assert.equal(linux.state.lsof, true);
  });
});

test('Linux without /proc: agent control links a session to its process and verifies the program', async () => {
  const { createAgentControl } = require('../lib/agents/control');
  await onFakeLinuxWithoutProc(async ({ fresh, linux }) => {
    const signals = [];
    const ctl = createAgentControl({
      processes: { ...fresh, rows: (o) => fresh.rows({ ...o, platform: 'linux', linux }) },
      platform: 'linux',
      inDocker: false,
      kill: (pid, sig) => signals.push([pid, sig]),
    });
    const [c] = await ctl.candidates({ cwd: '/home/u/work', firstTs: Date.now(), source: 'claude' });
    assert.equal(c && c.pid, 4242, 'the Claude Code process in that folder');
    const r = await ctl.pause({ pid: c.pid, startTime: c.startTime }, { by: 'owner' });
    assert.equal(r.ok, true);
    assert.deepEqual(signals.at(-1), [4242, 'SIGSTOP']);
    assert.equal(ctl.limited(), null);
  });
});

test('Linux without /proc and without lsof: no working folders, said once; control says it is limited', async () => {
  await onFakeLinuxWithoutProc(
    async ({ fresh, spawned, linux, said }) => {
      const all = await fresh.rows({ platform: 'linux', linux });
      assert.equal(all.find((r) => r.pid === 4242).cwd, undefined);
      assert.deepEqual(all.find((r) => r.pid === 4242).argv, ['/home/u/.local/bin/claude', '--resume']);
      assert.equal(linux.state.lsof, false);
      spawned.length = 0;
      await fresh.rows({ platform: 'linux', linux });
      assert.deepEqual(spawned, ['ps -axo', 'ps comm'], 'a missing lsof is not tried again');
      assert.equal(said.length, 2);
      assert.match(
        said[1],
        /lsof is not installed.*agent control \(pause, resume, stop\) and linking a session to its process are unavailable/,
      );
      const l = fresh.linuxLimits({ platform: 'linux', state: linux.state });
      assert.equal(l.lsof, false);
      const { createAgentControl } = require('../lib/agents/control');
      const ctl = createAgentControl({
        processes: { ...fresh, linuxLimits: () => l },
        platform: 'linux',
        inDocker: false,
      });
      assert.match(ctl.limited(), /agent control .* unavailable/);
    },
    { lsof: false },
  );
});

test('Linux with /proc (Node): rows() runs neither ps nor lsof', async () => {
  await onFakeLinuxWithoutProc(async ({ fresh, spawned, linux }) => {
    const procRow = { pid: 7, args: '/home/u/.local/bin/claude', argv: ['/home/u/.local/bin/claude'] };
    const all = await fresh.rows({ platform: 'linux', linux: { ...linux, read: () => [procRow] } });
    assert.deepEqual(all, [procRow]);
    assert.deepEqual(spawned, []);
  });
});

test('linuxLimits: null off Linux and with /proc readable; refused /proc names what is left', () => {
  const refusedProbe = () => {
    throw refusal();
  };
  assert.equal(linuxLimits({ platform: 'darwin' }), null);
  assert.equal(linuxLimits({ platform: 'linux', state: {}, probe: () => '1 2' }), null);
  const withLsof = linuxLimits({
    platform: 'linux',
    state: {},
    probe: refusedProbe,
    hasLsof: () => true,
    hasPs: () => true,
  });
  assert.equal(withLsof.lsof, true);
  assert.match(
    withLsof.text,
    /cannot read \/proc on Linux \(Deno has no scoped grant for it\): processes come from ps/,
  );
  const without = linuxLimits({
    platform: 'linux',
    state: {},
    probe: refusedProbe,
    hasLsof: () => false,
    hasPs: () => true,
  });
  assert.equal(without.lsof, false);
  assert.match(without.text, /lsof is not installed.*unavailable/);
  const noPs = linuxLimits({
    platform: 'linux',
    state: {},
    probe: refusedProbe,
    hasLsof: () => true,
    hasPs: () => false,
  });
  assert.equal(noPs.ps, false);
  assert.match(noPs.text, /ps is not installed, so no process can be listed.*unavailable/);
});
