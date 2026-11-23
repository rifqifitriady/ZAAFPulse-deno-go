// scripts/build-deno.js: what ships, the target names, and the compile command (P2 T6). Pure: compiles nothing.
const test = require('node:test');
const assert = require('node:assert/strict');
const build = require('../scripts/build-deno');

test('the binary embeds the app, never the tests, docs or build scripts', () => {
  assert.deepEqual(build.INCLUDE, [
    'bin',
    'lib',
    'public',
    'skills',
    'scripts/register-mcp.js',
    'server.js',
    'mcp.js',
    'package.json',
    'config.example.json',
    'LICENSE',
    ...build.SCHEMAS,
  ]);
  assert.deepEqual(build.ARCHIVE, [...build.INCLUDE, 'deno.json', 'gw']);
  // gw/ is archived for the go build (P6 Decision 3) but never embedded in the deno binary.
  assert.ok(build.ARCHIVE.includes('gw') && !build.INCLUDE.includes('gw'));
});

test('the binary carries the 20 IPC v1 schemas lib/ipc/schema.js reads (P4a E5)', () => {
  const { TYPES } = require('../lib/ipc/schema');
  assert.deepEqual(build.SCHEMAS, ['envelope', ...TYPES].map((t) => `ipc/v1/${t}.schema.json`).sort());
});

test('the six targets keep the v1 release names', () => {
  assert.deepEqual(Object.values(build.TARGETS).sort(), [
    'darwin-arm64',
    'darwin-x64',
    'linux-arm64',
    'linux-x64',
    'win-arm64',
    'win-x64',
  ]);
  assert.equal(build.outName('2.0.0', 'aarch64-pc-windows-msvc'), 'zaafpulse-2.0.0-win-arm64.exe');
  assert.equal(build.outName('2.0.0', 'x86_64-unknown-linux-gnu'), 'zaafpulse-2.0.0-linux-x64');
  assert.throws(() => build.outName('2.0.0', 'riscv64-unknown-linux-gnu'), /unknown target/);
  assert.equal(build.hostTarget('darwin', 'arm64'), 'aarch64-apple-darwin');
  assert.equal(build.hostTarget('win32', 'x64'), 'x86_64-pc-windows-msvc');
  assert.equal(build.hostTarget('linux', 'arm64'), 'aarch64-unknown-linux-gnu');
});

test('compile: quiet, never prompting, the cli set baked in (the entry is the command line, a superset of server), no -A', () => {
  const a = build.compileArgs({ target: 'x86_64-unknown-linux-gnu', out: '/o/z', entry: 'bin/zaafpulse.js' });
  assert.deepEqual(a.slice(0, 6), ['compile', '-q', '--no-prompt', '-P=cli', '--no-check', '--target']);
  assert.ok(!a.includes('-A') && !a.includes('--allow-all'));
  assert.equal(a.filter((x) => x === '--include').length, build.INCLUDE.length);
  assert.deepEqual(a.slice(-3), ['-o', '/o/z', 'bin/zaafpulse.js']);
});

test('arguments: --target (repeatable), --all, --allow-dirty; anything else is refused', () => {
  assert.deepEqual(build.parseArgs(['--target', 'aarch64-apple-darwin']), {
    targets: ['aarch64-apple-darwin'],
    allowDirty: false,
    package: false,
  });
  assert.equal(build.parseArgs(['--package']).package, true);
  assert.deepEqual(build.parseArgs(['--package', '--target', 'x86_64-pc-windows-msvc']), {
    targets: ['x86_64-pc-windows-msvc'],
    allowDirty: false,
    package: true,
  });
  assert.equal(build.parseArgs(['--all']).targets.length, 6);
  assert.equal(build.parseArgs(['--allow-dirty']).allowDirty, true);
  assert.deepEqual(build.parseArgs([]).targets, [build.hostTarget()]);
  assert.throws(() => build.parseArgs(['--target', 'nope']), /unknown target/);
  assert.throws(() => build.parseArgs(['--fast']), /unknown option/);
});

test('package: one archive per target, tar.gz on unix and zip on Windows, under the v1 names (P6 Decision 1)', () => {
  const want = {
    'aarch64-apple-darwin': 'zaafpulse-2.0.0-darwin-arm64.tar.gz',
    'x86_64-apple-darwin': 'zaafpulse-2.0.0-darwin-x64.tar.gz',
    'x86_64-unknown-linux-gnu': 'zaafpulse-2.0.0-linux-x64.tar.gz',
    'aarch64-unknown-linux-gnu': 'zaafpulse-2.0.0-linux-arm64.tar.gz',
    'x86_64-pc-windows-msvc': 'zaafpulse-2.0.0-win-x64.zip',
    'aarch64-pc-windows-msvc': 'zaafpulse-2.0.0-win-arm64.zip',
  };
  for (const [t, name] of Object.entries(want)) assert.equal(build.archiveName('2.0.0', t), name);
  assert.throws(() => build.archiveName('2.0.0', 'riscv64-unknown-linux-gnu'), /unknown target/);
});

test('package: every target has a Go target', () => {
  assert.deepEqual(Object.keys(build.GO_TARGETS).sort(), Object.keys(build.TARGETS).sort());
  assert.deepEqual(build.GO_TARGETS['x86_64-pc-windows-msvc'], { goos: 'windows', goarch: 'amd64' });
  assert.deepEqual(build.GO_TARGETS['aarch64-unknown-linux-gnu'], { goos: 'linux', goarch: 'arm64' });
});

test('package: the gateway build is trimmed, stripped and versioned, never -race (P6 Decision 2)', () => {
  const a = build.gwBuildArgs({ version: '1.1.0', out: '/o/zaafpulse-gw' });
  assert.equal(a[0], 'build');
  assert.ok(a.includes('-trimpath'));
  assert.ok(a.includes('-buildvcs=false'));
  assert.ok(a.includes('-ldflags=-s -w -X main.version=1.1.0'), 'the ldflags are one argument');
  assert.ok(!a.includes('-race'));
  assert.deepEqual(a.slice(-3), ['-o', '/o/zaafpulse-gw', './cmd/zaafpulse-gw']);
});

test('package: the go child sees only the allowed names, plus the target and CGO_ENABLED=0', () => {
  const env = build.gwEnv('aarch64-pc-windows-msvc', { PATH: '/bin', HOME: '/h', SECRET: 'x', GOOS: 'linux' });
  assert.deepEqual(env, { PATH: '/bin', HOME: '/h', GOOS: 'windows', GOARCH: 'arm64', CGO_ENABLED: '0' });
  const script = require('../package.json').scripts.build;
  assert.ok(script.includes(`--allow-env=${build.GO_ENV.join(',')} `), 'the task allows exactly GO_ENV');
  assert.ok(script.includes('--allow-run=git,tar,deno,go ') && script.includes('--ignore-env'));
});

test('package: a zip needs bsdtar', () => {
  assert.doesNotThrow(() => build.checkZipTar('bsdtar 3.5.3 - libarchive 3.7.4'));
  assert.throws(
    () => build.checkZipTar('tar (GNU tar) 1.35'),
    /zip packages need bsdtar \(macOS or Windows\); build win-\* targets there/,
  );
});
