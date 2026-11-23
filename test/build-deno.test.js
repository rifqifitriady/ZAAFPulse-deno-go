// scripts/build-deno.js: what ships, the target names, and the compile command (P2 T6). Pure: compiles nothing.
const test = require('node:test');
const assert = require('node:assert/strict');
const { spawnSync } = require('node:child_process');
const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const zlib = require('node:zlib');
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

test('package: a Windows host packs only the win-* zips; a tar.gz there would lose the execute bit', () => {
  const all = Object.keys(build.TARGETS);
  assert.throws(
    () => build.checkPackHost(['x86_64-pc-windows-msvc', 'x86_64-unknown-linux-gnu'], 'win32'),
    /--package on Windows can build only win-\* packages.*x86_64-unknown-linux-gnu.*build those on macOS or Linux/,
  );
  assert.throws(() => build.checkPackHost(['aarch64-apple-darwin'], 'win32'), /execute bit/);
  build.checkPackHost(['x86_64-pc-windows-msvc', 'aarch64-pc-windows-msvc'], 'win32');
  for (const p of ['darwin', 'linux']) build.checkPackHost(all, p);
  for (const k of [
    'HTTPS_PROXY',
    'HTTP_PROXY',
    'NO_PROXY',
    'GOPROXY',
    'GOPRIVATE',
    'GONOSUMDB',
    'GOSUMDB',
    'XDG_CACHE_HOME',
  ])
    assert.ok(build.GO_ENV.includes(k), k);
});

// Reads a zip back in plain JS: the end record, the central directory, and each entry's local header and data.
function readZip(buf) {
  const end = buf.length - 22;
  assert.equal(buf.readUInt32LE(end), 0x06054b50, 'end record');
  const count = buf.readUInt16LE(end + 10);
  let p = buf.readUInt32LE(end + 16);
  const out = [];
  for (let i = 0; i < count; i++) {
    assert.equal(buf.readUInt32LE(p), 0x02014b50, 'central header');
    const madeBy = buf.readUInt16LE(p + 4);
    const method = buf.readUInt16LE(p + 10);
    const crc = buf.readUInt32LE(p + 16);
    const csize = buf.readUInt32LE(p + 20);
    const usize = buf.readUInt32LE(p + 24);
    const nlen = buf.readUInt16LE(p + 28);
    const xlen = buf.readUInt16LE(p + 30);
    const clen = buf.readUInt16LE(p + 32);
    const attr = buf.readUInt32LE(p + 38);
    const at = buf.readUInt32LE(p + 42);
    const name = buf.toString('utf8', p + 46, p + 46 + nlen);
    assert.equal(buf.readUInt32LE(at), 0x04034b50, 'local header');
    const start = at + 30 + buf.readUInt16LE(at + 26) + buf.readUInt16LE(at + 28);
    const raw = buf.subarray(start, start + csize);
    const data = method === 8 ? zlib.inflateRawSync(raw) : Buffer.from(raw);
    assert.equal(data.length, usize);
    assert.equal(build.crc32(data), crc, `crc of ${name}`);
    out.push({ name, madeBy, method, mode: attr >>> 16, data });
    p += 46 + nlen + xlen + clen;
  }
  return out;
}

test('package: the zip is written in JS, readable, executable on unix, and the same every time', () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'zip-test-'));
  try {
    const a = path.join(dir, 'a.exe');
    const b = path.join(dir, 'LICENSE');
    const big = Buffer.from('zaafpulse '.repeat(5000) + crypto.randomBytes(3000).toString('hex'));
    fs.writeFileSync(a, big);
    fs.writeFileSync(b, 'MIT\n');
    const z1 = path.join(dir, 'one.zip');
    const z2 = path.join(dir, 'two.zip');
    const entries = [
      { name: 'LICENSE', file: b, mode: 0o644 },
      { name: 'a.exe', file: a, mode: 0o755 },
    ];
    build.writeZip(z1, 'pkg-1', entries);
    build.writeZip(z2, 'pkg-1', entries);
    const buf = fs.readFileSync(z1);
    assert.ok(buf.equals(fs.readFileSync(z2)), 'reproducible');
    const got = readZip(buf);
    assert.deepEqual(
      got.map((e) => [e.name, e.madeBy >> 8, e.mode.toString(8)]),
      [
        ['pkg-1/', 3, '40755'],
        ['pkg-1/LICENSE', 3, '100644'],
        ['pkg-1/a.exe', 3, '100755'],
      ],
    );
    assert.ok(got[2].data.equals(big) && got[2].method === 8);
    assert.equal(got[1].data.toString(), 'MIT\n');
    const probe = spawnSync('unzip', ['-v'], { stdio: 'ignore' });
    if (probe.error) return; // no unzip on PATH: the JS read-back above is the check
    const t = spawnSync('unzip', ['-t', z1], { encoding: 'utf8' });
    assert.equal(t.status, 0, t.stdout + t.stderr);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('package: crc32 is the zip CRC-32', () => {
  assert.equal(build.crc32(Buffer.from('123456789')), 0xcbf43926);
  assert.equal(build.crc32(Buffer.alloc(0)), 0);
});
