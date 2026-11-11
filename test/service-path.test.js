// A service starts with the minimal PATH of its supervisor (launchd: /usr/bin:/bin:/usr/sbin:/sbin), and Deno resolves
// every --allow-run=<name> on PATH when it starts: a name it cannot find there is not granted, and neither is that
// program by its absolute path. The product starts claude, node, brew and the model servers by the absolute path it
// looked up itself, so the service carries the install-time PATH (serviceEnv) and a `deno run` launch also grants the
// absolute paths those lookups resolve (permissions.runScope).
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const os = require('os');
const path = require('path');
const { spawnSync } = require('child_process');
const service = require('../lib/service');
const permissions = require('../lib/permissions');

const tmp = (p = 'zp-svcpath-') => fs.mkdtempSync(path.join(os.tmpdir(), p));

test('serviceEnv: the install-time PATH, absolute entries only, without unsafe characters, de-duplicated', () => {
  const env = {
    PATH: [
      '/home/me/.local/bin',
      'relative/bin',
      '',
      '/opt/a,b',
      '/opt/"q"',
      '/opt/x\ny',
      '/usr/bin',
      '/home/me/.local/bin',
    ].join(':'),
    ZAAFPULSE_PORT: '4790',
  };
  const mac = service.serviceEnv(env, { os: 'macos' });
  assert.equal(mac.ZAAFPULSE_PORT, '4790');
  assert.deepEqual(mac.PATH.split(':'), [
    '/home/me/.local/bin',
    '/usr/bin',
    '/usr/local/bin',
    '/opt/homebrew/bin',
    '/bin',
    '/usr/sbin',
    '/sbin',
  ]);
  const linux = service.serviceEnv({ PATH: '/snap/bin:/usr/bin' }, { os: 'linux' });
  assert.equal(linux.PATH.split(':')[0], '/snap/bin');
  assert.ok(linux.PATH.split(':').includes('/usr/local/bin'));
  const win = service.serviceEnv(
    { Path: 'C:\\Program Files\\nodejs;relative;C:\\a,b;C:\\Windows\\System32' },
    { os: 'windows' },
  );
  assert.equal(win.PATH, 'C:\\Program Files\\nodejs;C:\\Windows\\System32');
  assert.equal(service.serviceEnv({}, { os: 'windows' }).PATH, undefined, 'no PATH at install: none is written');
  assert.equal(service.serviceEnv({}, { os: 'linux', system: true }).ZAAFPULSE_DATA, '/var/lib/zaafpulse');
});

test('the service files carry that PATH', () => {
  const env = service.serviceEnv({ PATH: '/home/me/.local/bin:/usr/bin' }, { os: 'macos' });
  assert.match(service.plist(['/x/deno'], env, '/l.log'), /<key>PATH<\/key><string>\/home\/me\/\.local\/bin:/);
  assert.match(service.unitText(['/x/deno'], env, { system: false }), /^Environment=PATH=\/home\/me\/\.local\/bin:/m);
  const wenv = service.serviceEnv({ PATH: 'C:\\Program Files\\nodejs;C:\\Windows' }, { os: 'windows' });
  assert.ok(
    service
      .vbsText(['C:\\d\\deno.exe'], wenv, 'C:\\l.log')
      .includes('e("PATH") = "C:\\Program Files\\nodejs;C:\\Windows"'),
  );
});

// The fake Claude Code sits in ~/.local/bin, one of the folders lib/runtimes/state.js findBin and
// lib/evals/claude-cli.js search after PATH; the service PATH below does not hold it.
function fakeClaude(home) {
  const dir = path.join(home, '.local', 'bin');
  fs.mkdirSync(dir, { recursive: true });
  const file = path.join(dir, 'claude');
  fs.writeFileSync(file, '#!/bin/sh\necho "9.9.9 (Claude Code)"\n', { mode: 0o755 });
  return file;
}

test(
  'runScope: a deno run launch also grants the absolute paths the lookups resolve for allowed names',
  { skip: process.platform === 'win32' && 'POSIX folders' },
  () => {
    const home = tmp();
    const file = fakeClaude(home);
    const env = { PATH: '/usr/bin:/bin' };
    const r = permissions.runScope({ set: 'server', env, home, platform: process.platform });
    assert.ok(r.includes('claude'), 'the name stays');
    assert.ok(r.includes(file), `the looked-up path is granted: ${r.join(',')}`);
    assert.ok(
      !permissions.runScope({ set: 'mcp-stdio', env, home, platform: process.platform }).includes(file),
      'only for allowed names',
    );
    assert.equal(new Set(r).size, r.length);
    // A path that would break the launch line is left out; the name is still granted.
    const odd = path.join(home, 'a,b');
    fs.mkdirSync(odd);
    fs.writeFileSync(path.join(odd, 'claude'), '#!/bin/sh\n', { mode: 0o755 });
    const r2 = permissions.runScope({
      set: 'server',
      env: { PATH: `${odd}:/usr/bin` },
      home,
      platform: process.platform,
    });
    assert.ok(!r2.some((x) => x.includes('a,b')) && r2.includes('claude'));
  },
);

// The reproduction: a Deno child with the PATH launchd gives a user agent and a scoped --allow-run.
const DENO = (() => {
  if (process.versions.deno) return process.execPath;
  const r = spawnSync('deno', ['eval', 'console.log(Deno.execPath())'], { encoding: 'utf8' });
  return r.status === 0 ? r.stdout.trim() : null;
})();
const PROBE = path.join(__dirname, 'fixtures', 'permissions', 'probe.js');

test(
  'minimal service PATH: an allowed program found outside it is refused by name alone and granted by runScope',
  { skip: (process.platform === 'win32' && 'POSIX PATH') || (!DENO && 'deno is not installed') },
  () => {
    const home = tmp();
    const file = fakeClaude(home);
    const env = { PATH: '/usr/bin:/bin', HOME: home };
    const launch = (run) =>
      spawnSync(
        DENO,
        [
          'run',
          '-q',
          '--no-prompt',
          `--config=${permissions.CONFIG}`,
          '-P=server',
          `--allow-write=${home}`,
          `--allow-run=${run.join(',')}`,
          PROBE,
          'run',
          file,
        ],
        { encoding: 'utf8', env },
      );
    const answer = (r) => JSON.parse(String(r.stdout).trim().split('\n').pop() || 'null');
    const before = answer(launch(permissions.readSets().server.run));
    assert.equal(before.ok, false, 'names only: refused');
    assert.match(before.error, /Requires run access/);
    const after = answer(launch(permissions.runScope({ set: 'server', env, home, platform: process.platform })));
    assert.deepEqual(after, { ok: true });
  },
);

test('startCommand: opts.singleFile decides the trailing start, not the running build', () => {
  const data = tmp();
  const env = { ZAAFPULSE_DATA: data };
  assert.deepEqual(service.startCommand(env, { singleFile: true }), [process.execPath, 'start']);
  const node = service.startCommand(env, { runtime: 'node', singleFile: false });
  assert.deepEqual(node, [process.execPath, path.join(permissions.APP_DIR, 'bin', 'zaafpulse.js'), 'start']);
});
