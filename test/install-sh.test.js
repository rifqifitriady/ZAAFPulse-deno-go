// install.sh (P6 Task 6, Decisions 9–11): install, upgrade, rollback and uninstall against a fake package. Every run
// has HOME and ZAAFPULSE_PREFIX in a temp folder, --no-service and --no-modify-path. The fake binaries record every
// argv to a log, so the tests prove no service command ever ran.
const test = require('node:test');
const assert = require('node:assert/strict');
const { spawnSync } = require('node:child_process');
const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const SCRIPT = path.join(__dirname, '..', 'install.sh');
const has = (cmd) => spawnSync('sh', ['-c', `command -v ${cmd}`]).status === 0;
const SKIP =
  process.platform === 'win32'
    ? 'POSIX sh only'
    : !has('tar')
      ? 'needs tar'
      : process.getuid && process.getuid() === 0
        ? 'as root install.sh ignores ZAAFPULSE_PREFIX'
        : false;
const PLAT = process.platform === 'darwin' ? 'darwin' : 'linux';
const NARCH = process.arch === 'arm64' ? 'arm64' : 'x64';

// The fake zaafpulse: `version` prints 2.0.0-test, `path` prints $PATH, anything else its args; every call is logged.
const FAKE_BIN = `#!/bin/sh
echo "v2 $*" >> "$ZP_FAKE_LOG"
case "$1" in version) echo 2.0.0-test ;; path) echo "$PATH" ;; *) echo "args: $*" ;; esac
`;
const FAKE_GW = '#!/bin/sh\necho "gw $*" >> "$ZP_FAKE_LOG"\n';
// A v1 node that logs its argv, so a v1 launcher's service call shows in the log too.
const FAKE_NODE = '#!/bin/sh\necho "v1 $*" >> "$ZP_FAKE_LOG"\n';
// The launcher the v1.1.0 installer wrote.
const v1Launcher = (prefix) => `#!/bin/sh\nexec "${prefix}/node/bin/node" "${prefix}/app/bin/zaafpulse.js" "\$@"\n`;

const ROOTS = [];
test.after(() => {
  for (const r of ROOTS) fs.rmSync(r, { recursive: true, force: true });
});
// SHELL_UNDER_TEST runs the installer with another POSIX shell (dash, busybox sh …).
const SH = process.env.SHELL_UNDER_TEST || 'sh';

function setup({ bin = FAKE_BIN } = {}) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'zp-install-'));
  ROOTS.push(root);
  const home = path.join(root, 'home');
  const prefix = path.join(root, 'prefix');
  fs.mkdirSync(home);
  const name = `zaafpulse-2.0.0-test-${PLAT}-${NARCH}`;
  const stage = path.join(root, 'stage', name);
  fs.mkdirSync(stage, { recursive: true });
  fs.writeFileSync(path.join(stage, 'zaafpulse'), bin, { mode: 0o755 });
  fs.writeFileSync(path.join(stage, 'zaafpulse-gw'), FAKE_GW, { mode: 0o755 });
  fs.writeFileSync(path.join(stage, 'LICENSE'), 'MIT\n');
  const archive = path.join(root, `${name}.tar.gz`);
  const t = spawnSync('tar', ['-czf', archive, '-C', path.join(root, 'stage'), name]);
  assert.equal(t.status, 0, String(t.stderr));
  const hex = crypto.createHash('sha256').update(fs.readFileSync(archive)).digest('hex');
  const sums = path.join(root, 'SHA256SUMS.txt');
  fs.writeFileSync(sums, `${hex}  ${path.basename(archive)}\n`);
  const log = path.join(root, 'calls.log');
  const launcher = path.join(home, '.local', 'bin', 'zaafpulse');
  return { root, home, prefix, archive, sums, log, launcher, hex };
}

function run(ctx, args, { stdin } = {}) {
  const env = { PATH: process.env.PATH, HOME: ctx.home, ZAAFPULSE_PREFIX: ctx.prefix, ZP_FAKE_LOG: ctx.log };
  const argv = stdin ? ['-s', '--', ...args] : [SCRIPT, ...args];
  return spawnSync(SH, argv, { env, encoding: 'utf8', input: stdin });
}
const install = (ctx, extra = []) =>
  run(ctx, ['--archive', ctx.archive, '--sums', ctx.sums, '--no-service', '--no-modify-path', ...extra]);
const ok = (r) => assert.equal(r.status, 0, `${r.stdout}\n${r.stderr}`);
const calls = (ctx) => (fs.existsSync(ctx.log) ? fs.readFileSync(ctx.log, 'utf8') : '');
const noService = (ctx) => assert.ok(!/\bservice\b/.test(calls(ctx)), `a service command ran:\n${calls(ctx)}`);
const real = (p) => fs.realpathSync(p);
// Every file under a folder, with its content, to prove a failed run changed nothing.
function tree(dir) {
  if (!fs.existsSync(dir)) return null;
  const out = {};
  const walk = (d) => {
    for (const e of fs.readdirSync(d, { withFileTypes: true })) {
      const f = path.join(d, e.name);
      if (e.isDirectory()) walk(f);
      else out[path.relative(dir, f)] = fs.readFileSync(f, 'utf8');
    }
  };
  walk(dir);
  return out;
}
// A v1.1.0 install as the v1 installer left it: app/, node/ and a launcher that runs node.
function fakeV1(ctx) {
  fs.mkdirSync(path.join(ctx.prefix, 'app', 'bin'), { recursive: true });
  fs.writeFileSync(path.join(ctx.prefix, 'app', 'bin', 'zaafpulse.js'), '// v1\n');
  fs.writeFileSync(
    path.join(ctx.prefix, 'app', 'package.json'),
    '{\n  "name": "zaafpulse",\n  "version": "1.1.0"\n}\n',
  );
  fs.mkdirSync(path.join(ctx.prefix, 'node', 'bin'), { recursive: true });
  fs.writeFileSync(path.join(ctx.prefix, 'node', 'bin', 'node'), FAKE_NODE, { mode: 0o755 });
  fs.mkdirSync(path.dirname(ctx.launcher), { recursive: true });
  fs.writeFileSync(ctx.launcher, v1Launcher(ctx.prefix), { mode: 0o755 });
}

test('install.sh parses (sh -n)', { skip: SKIP }, () => {
  ok(spawnSync(SH, ['-n', SCRIPT], { encoding: 'utf8' }));
});

test(
  'clean install: both binaries in <prefix>/bin, a launcher that puts that folder first on PATH, no rollback',
  { skip: SKIP },
  () => {
    const ctx = setup();
    ok(install(ctx));
    const bin = path.join(real(ctx.prefix), 'bin');
    for (const f of ['zaafpulse', 'zaafpulse-gw', 'LICENSE']) assert.ok(fs.existsSync(path.join(bin, f)), f);
    assert.ok(!fs.existsSync(path.join(ctx.prefix, 'rollback')));
    assert.ok(!fs.existsSync(path.join(ctx.prefix, 'bin.new')));
    const launcher = fs.readFileSync(ctx.launcher, 'utf8');
    assert.ok(launcher.includes(`PATH="${bin}:$PATH"; export PATH`), launcher);
    const p = spawnSync(ctx.launcher, ['path'], {
      env: { PATH: '/usr/bin:/bin', ZP_FAKE_LOG: ctx.log },
      encoding: 'utf8',
    });
    assert.equal(p.stdout.trim().split(':')[0], bin);
    const v = spawnSync(ctx.launcher, ['version'], {
      env: { PATH: '/usr/bin:/bin', ZP_FAKE_LOG: ctx.log },
      encoding: 'utf8',
    });
    assert.equal(v.stdout.trim(), '2.0.0-test');
    noService(ctx);
  },
);

test('piped into sh (curl … | sh): installs without its own file; --help then prints nothing', { skip: SKIP }, () => {
  const ctx = setup();
  const src = fs.readFileSync(SCRIPT, 'utf8');
  ok(run(ctx, ['--archive', ctx.archive, '--sums', ctx.sums, '--no-service', '--no-modify-path'], { stdin: src }));
  assert.ok(fs.existsSync(path.join(ctx.prefix, 'bin', 'zaafpulse')));
  const h = run(ctx, ['--help'], { stdin: src });
  ok(h);
  assert.equal(h.stdout, '');
  const f = run(ctx, ['--help']);
  ok(f);
  assert.match(f.stdout, /--archive FILE/);
  assert.ok(!f.stdout.includes('set -eu'));
  noService(ctx);
});

test('a checksum mismatch, or no line for the archive, installs nothing', { skip: SKIP }, () => {
  const ctx = setup();
  fs.writeFileSync(ctx.sums, `${'0'.repeat(64)}  ${path.basename(ctx.archive)}\n`);
  let r = install(ctx);
  assert.notEqual(r.status, 0);
  assert.match(r.stderr, /checksum mismatch/);
  assert.ok(!fs.existsSync(path.join(ctx.prefix, 'bin')));
  assert.ok(!fs.existsSync(ctx.launcher));
  fs.writeFileSync(ctx.sums, `${ctx.hex}  some-other.tar.gz\n`);
  r = install(ctx);
  assert.notEqual(r.status, 0);
  assert.match(r.stderr, /no checksum/);
  assert.ok(!fs.existsSync(path.join(ctx.prefix, 'bin')));
  assert.ok(!fs.existsSync(ctx.launcher));
  assert.equal(calls(ctx), '');
});

test(
  'upgrade over v1.1.0: app/, node/ and the launcher go to rollback/, VERSION is v1.1.0; --rollback restores them',
  { skip: SKIP },
  () => {
    const ctx = setup();
    fakeV1(ctx);
    ok(install(ctx));
    const rb = path.join(ctx.prefix, 'rollback');
    assert.equal(fs.readFileSync(path.join(rb, 'VERSION'), 'utf8'), 'v1.1.0\n');
    assert.ok(fs.existsSync(path.join(rb, 'app', 'bin', 'zaafpulse.js')));
    assert.ok(fs.existsSync(path.join(rb, 'node', 'bin', 'node')));
    assert.equal(fs.readFileSync(path.join(rb, 'launcher'), 'utf8'), v1Launcher(ctx.prefix));
    assert.ok(!fs.existsSync(path.join(ctx.prefix, 'app')));
    assert.ok(fs.existsSync(path.join(ctx.prefix, 'bin', 'zaafpulse')));
    assert.notEqual(fs.readFileSync(ctx.launcher, 'utf8'), v1Launcher(ctx.prefix));

    const r = run(ctx, ['--rollback', '--no-service']);
    ok(r);
    assert.match(r.stdout, /v1\.1\.0 is active again/);
    assert.equal(fs.readFileSync(ctx.launcher, 'utf8'), v1Launcher(ctx.prefix));
    assert.ok(fs.existsSync(path.join(ctx.prefix, 'app', 'bin', 'zaafpulse.js')));
    assert.ok(fs.existsSync(path.join(ctx.prefix, 'node', 'bin', 'node')));
    assert.ok(!fs.existsSync(path.join(ctx.prefix, 'bin')));
    assert.ok(!fs.existsSync(rb));
    assert.ok(fs.existsSync(path.join(ctx.prefix, 'rollforward', 'bin', 'zaafpulse')));
    noService(ctx);
  },
);

test('--rollback with nothing saved fails and changes nothing', { skip: SKIP }, () => {
  const ctx = setup();
  ok(install(ctx));
  const before = { prefix: tree(ctx.prefix), launcher: fs.readFileSync(ctx.launcher, 'utf8') };
  const r = run(ctx, ['--rollback', '--no-service']);
  assert.notEqual(r.status, 0);
  assert.match(r.stderr, /nothing to roll back to/);
  assert.deepEqual({ prefix: tree(ctx.prefix), launcher: fs.readFileSync(ctx.launcher, 'utf8') }, before);
  noService(ctx);
});

test('upgrade v2 → v2 keeps one generation: the bin/ it replaced and its launcher', { skip: SKIP }, () => {
  const ctx = setup();
  ok(install(ctx));
  const first = fs.readFileSync(ctx.launcher, 'utf8');
  ok(install(ctx));
  ok(install(ctx));
  const rb = path.join(ctx.prefix, 'rollback');
  assert.deepEqual(fs.readdirSync(rb).sort(), ['VERSION', 'bin', 'launcher']);
  assert.equal(fs.readFileSync(path.join(rb, 'VERSION'), 'utf8'), 'v2.0.0-test\n');
  assert.equal(fs.readFileSync(path.join(rb, 'launcher'), 'utf8'), first);
  assert.deepEqual(fs.readdirSync(ctx.prefix).sort(), ['bin', 'rollback']);
  noService(ctx);
});

test('--uninstall removes bin, rollback, rollforward and the launcher, and keeps the data', { skip: SKIP }, () => {
  const ctx = setup();
  fakeV1(ctx);
  ok(install(ctx));
  ok(run(ctx, ['--rollback', '--no-service']));
  ok(install(ctx));
  fs.mkdirSync(path.join(ctx.prefix, 'data'));
  fs.writeFileSync(path.join(ctx.prefix, 'data', 'config.json'), '{}');
  const r = run(ctx, ['--uninstall', '--no-service']);
  ok(r);
  assert.deepEqual(fs.readdirSync(ctx.prefix), ['data']);
  assert.equal(fs.readFileSync(path.join(ctx.prefix, 'data', 'config.json'), 'utf8'), '{}');
  assert.ok(!fs.existsSync(ctx.launcher));
  noService(ctx);
});

test('a new zaafpulse that does not run changes nothing: the old install stays as it was', { skip: SKIP }, () => {
  const ctx = setup({ bin: '#!/bin/sh\necho "broken $*" >> "$ZP_FAKE_LOG"\nexit 1\n' });
  fakeV1(ctx);
  const before = { prefix: tree(ctx.prefix), launcher: fs.readFileSync(ctx.launcher, 'utf8') };
  const r = install(ctx);
  assert.notEqual(r.status, 0);
  assert.match(r.stderr, /does not run on this system; nothing was changed/);
  assert.deepEqual({ prefix: tree(ctx.prefix), launcher: fs.readFileSync(ctx.launcher, 'utf8') }, before);
  assert.equal(calls(ctx), 'broken version\n');
  // A clean install that fails leaves no folder behind.
  const fresh = setup({ bin: '#!/bin/sh\nexit 1\n' });
  assert.notEqual(install(fresh).status, 0);
  assert.ok(!fs.existsSync(fresh.prefix));
  assert.ok(!fs.existsSync(fresh.launcher));
});

test('an upgrade cut off while saving the old install: its rollback.new becomes the rollback', { skip: SKIP }, () => {
  const ctx = setup();
  fakeV1(ctx);
  // As the installer leaves it after the move into rollback.new and before the rename.
  const rbNew = path.join(ctx.prefix, 'rollback.new');
  fs.mkdirSync(rbNew);
  fs.writeFileSync(path.join(rbNew, 'VERSION'), 'v1.1.0\n');
  fs.copyFileSync(ctx.launcher, path.join(rbNew, 'launcher'));
  fs.renameSync(path.join(ctx.prefix, 'app'), path.join(rbNew, 'app'));
  ok(install(ctx));
  const rb = path.join(ctx.prefix, 'rollback');
  assert.ok(!fs.existsSync(rbNew));
  assert.equal(fs.readFileSync(path.join(rb, 'VERSION'), 'utf8'), 'v1.1.0\n');
  assert.ok(fs.existsSync(path.join(rb, 'app', 'bin', 'zaafpulse.js')));
  assert.ok(fs.existsSync(path.join(rb, 'node', 'bin', 'node')), 'the folder not yet moved joins it');
  ok(run(ctx, ['--rollback', '--no-service']));
  assert.equal(fs.readFileSync(ctx.launcher, 'utf8'), v1Launcher(ctx.prefix));
  assert.ok(fs.existsSync(path.join(ctx.prefix, 'app', 'bin', 'zaafpulse.js')));
  noService(ctx);
});

test(
  '--rollback to a v2 install saved without a launcher writes a fresh one; a later install drops rollforward/',
  {
    skip: SKIP,
  },
  () => {
    const ctx = setup();
    ok(install(ctx));
    fs.rmSync(ctx.launcher);
    ok(install(ctx));
    const rb = path.join(ctx.prefix, 'rollback');
    assert.deepEqual(fs.readdirSync(rb).sort(), ['VERSION', 'bin']);
    ok(run(ctx, ['--rollback', '--no-service']));
    const bin = path.join(real(ctx.prefix), 'bin');
    assert.ok(fs.readFileSync(ctx.launcher, 'utf8').includes(`exec "${bin}/zaafpulse" "$@"`));
    assert.ok(fs.existsSync(path.join(ctx.prefix, 'rollforward')));
    ok(install(ctx));
    assert.deepEqual(fs.readdirSync(ctx.prefix).sort(), ['bin', 'rollback']);
    noService(ctx);
  },
);
