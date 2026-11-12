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
  assert.deepEqual(build.ARCHIVE, [...build.INCLUDE, 'deno.json']);
});

test('the binary carries the 15 IPC v1 schemas lib/ipc/schema.js reads (P4a E5)', () => {
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
  });
  assert.equal(build.parseArgs(['--all']).targets.length, 6);
  assert.equal(build.parseArgs(['--allow-dirty']).allowDirty, true);
  assert.deepEqual(build.parseArgs([]).targets, [build.hostTarget()]);
  assert.throws(() => build.parseArgs(['--target', 'nope']), /unknown target/);
  assert.throws(() => build.parseArgs(['--fast']), /unknown option/);
});
