// Builds the single-file ZAAFPulse executables with `deno compile` (P2 T6); replaces the Node SEA build.
//   deno task build                                  this machine's target
//   deno task build --target <triple> [--target …]   any of TARGETS
//   deno task build --all                            all six (needs the denort base binaries, downloaded once)
//   --allow-dirty                                    build the working tree instead of the last commit (local tests)
//   --package                                        also build the Go gateway and pack both binaries (P6 Decision 3)
// What ships is exactly the committed files under INCLUDE: they are taken from `git archive HEAD` into dist/.work, so
// an untracked or edited file never ships, and the build stops when a file that would ship has uncommitted changes.
// The binary runs with the "cli" permission set of deno.json, baked in at compile time (-P=cli, never -A). Its entry is
// the command line, which serves the dashboard, the MCP server, the service install and `doctor`, so it needs the
// superset: the "server" set lacks osRelease (doctor) and the service programs. -q keeps Deno's own notices out of
// the binary's output. A compiled binary serves its files from inside itself and
// unpacks nothing (spec D2): integrity is the release's SHA256SUMS and attestations.
// Output: dist/zaafpulse-<version>-<name>[.exe] and dist/SHA256SUMS.txt.
// With --package (P6 Decisions 1-3) each target also gets zaafpulse-gw, built from gw/ with go (found on PATH), and
// the output is one archive per target instead: dist/zaafpulse-<version>-<name>.tar.gz (.zip for win-*), holding the
// folder zaafpulse-<version>-<name>/ with zaafpulse[.exe], zaafpulse-gw[.exe] and LICENSE. SHA256SUMS.txt then lists
// the archives. The tar.gz is written by `tar -czf`; the zip is written here (writeZip), because the tar on a Windows
// runner's bash is GNU tar, which cannot write a zip. So win-* packages build on any OS.
const { execFileSync } = require('child_process');
const crypto = require('crypto');
const fs = require('fs');
const path = require('path');
const zlib = require('zlib');

const ROOT = path.join(__dirname, '..');
const DIST = path.join(ROOT, 'dist');
// The IPC v1 schemas, which lib/ipc/schema.js reads from disk (P4a E5). deno compile --include takes paths, not globs.
const SCHEMAS = fs
  .readdirSync(path.join(ROOT, 'ipc', 'v1'))
  .filter((f) => f.endsWith('.schema.json'))
  .sort()
  .map((f) => `ipc/v1/${f}`);
const INCLUDE = [
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
  ...SCHEMAS,
];
// deno.json is in the source folder for -P=cli to find; it is not embedded. gw/ is the gateway's Go source, built by
// --package into its own binary; the deno binary does not embed it.
const ARCHIVE = [...INCLUDE, 'deno.json', 'gw'];
// Deno target triple -> the release name used since v1 (install.sh and install.ps1 pick by it).
const TARGETS = {
  'aarch64-apple-darwin': 'darwin-arm64',
  'x86_64-apple-darwin': 'darwin-x64',
  'x86_64-unknown-linux-gnu': 'linux-x64',
  'aarch64-unknown-linux-gnu': 'linux-arm64',
  'x86_64-pc-windows-msvc': 'win-x64',
  'aarch64-pc-windows-msvc': 'win-arm64',
};

// Deno target triple -> the Go target the gateway is built for (P6 Decision 2).
const GO_TARGETS = {
  'aarch64-apple-darwin': { goos: 'darwin', goarch: 'arm64' },
  'x86_64-apple-darwin': { goos: 'darwin', goarch: 'amd64' },
  'x86_64-unknown-linux-gnu': { goos: 'linux', goarch: 'amd64' },
  'aarch64-unknown-linux-gnu': { goos: 'linux', goarch: 'arm64' },
  'x86_64-pc-windows-msvc': { goos: 'windows', goarch: 'amd64' },
  'aarch64-pc-windows-msvc': { goos: 'windows', goarch: 'arm64' },
};
// The only environment names the go child sees (with GOOS, GOARCH and CGO_ENABLED). The build task allows reading
// exactly these with --ignore-env, so any other name reads as undefined (package.json scripts.build).
const GO_ENV = [
  'PATH',
  'HOME',
  'GOCACHE',
  'GOPATH',
  'GOROOT',
  'GOTOOLCHAIN',
  'GOFLAGS',
  'GOMODCACHE',
  'LOCALAPPDATA',
  'USERPROFILE',
  'TMPDIR',
  'TEMP',
  'TMP',
  'SystemRoot',
  'APPDATA',
  // A proxy and the module download settings, for a go that must fetch through them (go.mod has no requirements
  // today, but the toolchain may still be fetched); XDG_CACHE_HOME is where go puts its build cache on Linux.
  'HTTPS_PROXY',
  'HTTP_PROXY',
  'NO_PROXY',
  'GOPROXY',
  'GOPRIVATE',
  'GONOSUMDB',
  'GOSUMDB',
  'XDG_CACHE_HOME',
];

function hostTarget(platform = process.platform, arch = process.arch) {
  const cpu = arch === 'arm64' ? 'aarch64' : 'x86_64';
  const os = platform === 'darwin' ? 'apple-darwin' : platform === 'win32' ? 'pc-windows-msvc' : 'unknown-linux-gnu';
  return `${cpu}-${os}`;
}

function outName(version, target) {
  const name = TARGETS[target];
  if (!name) throw new Error(`unknown target ${target}; one of ${Object.keys(TARGETS).join(', ')}`);
  return `zaafpulse-${version}-${name}${name.startsWith('win') ? '.exe' : ''}`;
}

// The release archive's file name; its top folder is the same name without the extension.
function archiveName(version, target) {
  const name = TARGETS[target];
  if (!name) throw new Error(`unknown target ${target}; one of ${Object.keys(TARGETS).join(', ')}`);
  return `zaafpulse-${version}-${name}${name.startsWith('win') ? '.zip' : '.tar.gz'}`;
}

// The `go build` argv for the gateway (P6 Decision 2), run with cwd gw/. Never -race: the release binary is stripped.
function gwBuildArgs({ version, out }) {
  return [
    'build',
    '-trimpath',
    '-buildvcs=false',
    `-ldflags=-s -w -X main.version=${version}`,
    '-o',
    out,
    './cmd/zaafpulse-gw',
  ];
}

// The go child's environment: the allowed names that are set, plus the target. Nothing else is passed on.
function gwEnv(target, env = process.env) {
  const out = {};
  for (const k of GO_ENV) if (env[k] !== undefined) out[k] = env[k];
  const { goos, goarch } = GO_TARGETS[target];
  return { ...out, GOOS: goos, GOARCH: goarch, CGO_ENABLED: '0' };
}

// CRC-32 (IEEE, the zip one) from a table, so it does not depend on zlib.crc32 (newer Node and Deno only).
const CRC_TABLE = (() => {
  const t = new Uint32Array(256);
  for (let n = 0; n < 256; n++) {
    let c = n;
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    t[n] = c >>> 0;
  }
  return t;
})();

function crc32(buf) {
  let c = 0xffffffff;
  for (let i = 0; i < buf.length; i++) c = CRC_TABLE[(c ^ buf[i]) & 0xff] ^ (c >>> 8);
  return (c ^ 0xffffffff) >>> 0;
}

// A fixed DOS time (1980-01-01 00:00), so the same files make the same zip.
const DOS_TIME = 0;
const DOS_DATE = (1 << 5) | 1;
const ZIP_MAX = 0xffffffff;

// Writes a plain zip (no zip64) of one folder: entries are [{ name, file, mode }], name relative to the folder, mode the
// unix mode (0o755 keeps a file executable under unzip on unix; Windows ignores it). Files are deflated; the folder is
// a stored entry of its own. A file or archive of 4 GiB or more is refused.
function writeZip(out, folder, entries) {
  const items = [{ name: `${folder}/`, data: Buffer.alloc(0), mode: 0o40755, dir: true }];
  for (const e of entries) {
    const data = fs.readFileSync(e.file);
    if (data.length >= ZIP_MAX) throw new Error(`${e.name} is 4 GiB or more; the zip writer has no zip64`);
    items.push({ name: `${folder}/${e.name}`, data, mode: 0o100000 | e.mode, dir: false });
  }
  const fd = fs.openSync(out, 'w');
  try {
    let offset = 0;
    const write = (buf) => {
      fs.writeSync(fd, buf);
      offset += buf.length;
    };
    const central = [];
    for (const it of items) {
      const name = Buffer.from(it.name, 'utf8');
      const method = it.dir ? 0 : 8;
      const body = it.dir ? it.data : zlib.deflateRawSync(it.data, { level: 9 });
      const crc = crc32(it.data);
      if (offset + 30 + name.length + body.length >= ZIP_MAX)
        throw new Error(`${out} would be 4 GiB or more; the zip writer has no zip64`);
      const local = Buffer.alloc(30);
      local.writeUInt32LE(0x04034b50, 0);
      local.writeUInt16LE(20, 4); // version needed: 2.0 (deflate)
      local.writeUInt16LE(0x0800, 6); // flags: the name is UTF-8
      local.writeUInt16LE(method, 8);
      local.writeUInt16LE(DOS_TIME, 10);
      local.writeUInt16LE(DOS_DATE, 12);
      local.writeUInt32LE(crc, 14);
      local.writeUInt32LE(body.length, 18);
      local.writeUInt32LE(it.data.length, 22);
      local.writeUInt16LE(name.length, 26);
      local.writeUInt16LE(0, 28);
      const at = offset;
      write(local);
      write(name);
      write(body);
      const c = Buffer.alloc(46);
      c.writeUInt32LE(0x02014b50, 0);
      c.writeUInt16LE((3 << 8) | 20, 4); // made by: unix, so the high half of the external attributes is the mode
      c.writeUInt16LE(20, 6);
      c.writeUInt16LE(0x0800, 8);
      c.writeUInt16LE(method, 10);
      c.writeUInt16LE(DOS_TIME, 12);
      c.writeUInt16LE(DOS_DATE, 14);
      c.writeUInt32LE(crc, 16);
      c.writeUInt32LE(body.length, 20);
      c.writeUInt32LE(it.data.length, 24);
      c.writeUInt16LE(name.length, 28);
      // extra length, comment length, disk number start, internal attributes: all 0 (30..37)
      c.writeUInt32LE(((it.mode << 16) | (it.dir ? 0x10 : 0)) >>> 0, 38);
      c.writeUInt32LE(at, 42);
      central.push(c, name);
    }
    const cdStart = offset;
    for (const b of central) write(b);
    const cdSize = offset - cdStart;
    if (offset >= ZIP_MAX) throw new Error(`${out} would be 4 GiB or more; the zip writer has no zip64`);
    const end = Buffer.alloc(22);
    end.writeUInt32LE(0x06054b50, 0);
    end.writeUInt16LE(items.length, 8);
    end.writeUInt16LE(items.length, 10);
    end.writeUInt32LE(cdSize, 12);
    end.writeUInt32LE(cdStart, 16);
    write(end);
  } finally {
    fs.closeSync(fd);
  }
}

function compileArgs({ target, out, entry }) {
  return [
    'compile',
    '-q',
    '--no-prompt',
    '-P=cli',
    '--no-check',
    '--target',
    target,
    ...INCLUDE.flatMap((p) => ['--include', p]),
    '-o',
    out,
    entry,
  ];
}

function parseArgs(argv) {
  const targets = [];
  let allowDirty = false;
  let pkg = false;
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a === '--allow-dirty') allowDirty = true;
    else if (a === '--package') pkg = true;
    else if (a === '--all') targets.push(...Object.keys(TARGETS));
    else if (a === '--target') {
      const t = argv[++i];
      if (!TARGETS[t]) throw new Error(`unknown target ${t}; one of ${Object.keys(TARGETS).join(', ')}`);
      targets.push(t);
    } else throw new Error(`unknown option ${a}`);
  }
  return { targets: targets.length ? [...new Set(targets)] : [hostTarget()], allowDirty, package: pkg };
}

const git = (args) => execFileSync('git', ['-C', ROOT, ...args], { encoding: 'utf8' });
const sha256 = (file) => crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex');

// The folder to compile from: the committed tree under ARCHIVE, or the working tree with --allow-dirty.
function source(allowDirty) {
  const dirty = git(['status', '--porcelain', '--untracked-files=no', '--', ...ARCHIVE])
    .split('\n')
    .filter(Boolean);
  if (dirty.length && !allowDirty)
    throw new Error(
      `uncommitted changes in files that would ship (${dirty.slice(0, 5).join(', ')}); commit them, or pass --allow-dirty for a local test build`,
    );
  if (allowDirty) {
    if (dirty.length) console.warn(`warning: building with uncommitted changes: ${dirty.join(', ')}`);
    return ROOT;
  }
  const work = path.join(DIST, '.work');
  fs.rmSync(work, { recursive: true, force: true });
  fs.mkdirSync(path.join(work, 'src'), { recursive: true });
  const tar = path.join(work, 'src.tar');
  git(['archive', '--format=tar', '-o', tar, 'HEAD', '--', ...ARCHIVE]);
  // Relative names and cwd: GNU tar on Windows (Git Bash) reads `D:\...` as a host name.
  execFileSync('tar', ['-xf', 'src.tar', '-C', 'src'], { cwd: work, stdio: 'inherit' });
  return path.join(work, 'src');
}

const mb = (file) => `${(fs.statSync(file).size / 1e6).toFixed(1)} MB`;

function compile(src, target, out) {
  const args = compileArgs({ target, out, entry: path.join('bin', 'zaafpulse.js') });
  // One retry: the denort base binary for a target is downloaded on first use, and a download can fail once.
  for (let attempt = 1; ; attempt++) {
    try {
      execFileSync('deno', args, { cwd: src, stdio: 'inherit' }); // by name: `deno task` reports a symlink-resolved execPath that --allow-run=deno does not match
      return;
    } catch (err) {
      if (attempt === 2) throw err;
      console.warn(`deno compile failed for ${target}; trying once more`);
    }
  }
}

// A tar.gz packed on Windows loses the execute bit: the tar of a Windows runner's bash (MSYS) does not keep a mode NTFS
// cannot hold, so the ELF and Mach-O binaries inside would not run once unpacked. Those packages are built on macOS
// or Linux; a Windows host packs only the win-* zips (written here, with their modes).
function checkPackHost(targets, platform = process.platform) {
  if (platform !== 'win32') return;
  const bad = targets.filter((t) => !archiveName('0', t).endsWith('.zip'));
  if (bad.length)
    throw new Error(
      `--package on Windows can build only win-* packages: a tar.gz made here loses the execute bit of its binaries (${bad.join(', ')}); build those on macOS or Linux`,
    );
}

// The ad-hoc signature of a darwin gateway (P6 Decision 7): the Go linker signs darwin/arm64 itself but leaves darwin/amd64
// unsigned (measured in the release dry run 45101126221), so both are signed here with `codesign --sign -` (ad-hoc, no
// identity). codesign exists only on macOS: another host warns and leaves the file as Go wrote it. null: nothing to do.
function gwSignArgs(target, out, platform = process.platform) {
  if (GO_TARGETS[target].goos !== 'darwin') return null;
  if (platform !== 'darwin') {
    console.warn(`warning: ${path.basename(out)} for ${target} is not ad-hoc signed (codesign needs macOS)`);
    return null;
  }
  return ['--force', '--sign', '-', out];
}

// One target's archive (P6 Decisions 1-3): stage both binaries and LICENSE in dist/.pkg/<folder>/, pack, clean up.
function pack(src, version, target) {
  const file = archiveName(version, target);
  const zip = file.endsWith('.zip');
  const folder = file.replace(/\.(zip|tar\.gz)$/, '');
  const exe = zip ? '.exe' : '';
  const pkgDir = path.join(DIST, '.pkg');
  const stage = path.join(pkgDir, folder);
  fs.rmSync(stage, { recursive: true, force: true });
  fs.mkdirSync(stage, { recursive: true });
  const cp = path.join(stage, `zaafpulse${exe}`);
  const gw = path.join(stage, `zaafpulse-gw${exe}`);
  compile(src, target, cp);
  execFileSync('go', gwBuildArgs({ version, out: gw }), {
    cwd: path.join(src, 'gw'),
    env: gwEnv(target),
    stdio: 'inherit',
  });
  const sign = gwSignArgs(target, gw);
  if (sign) execFileSync('codesign', sign, { stdio: 'inherit' });
  fs.copyFileSync(path.join(src, 'LICENSE'), path.join(stage, 'LICENSE'));
  const out = path.join(DIST, file);
  fs.rmSync(out, { force: true });
  // Relative names and cwd: GNU tar on Windows (Git Bash) reads `D:\...` as a host name.
  if (zip)
    writeZip(out, folder, [
      { name: 'LICENSE', file: path.join(stage, 'LICENSE'), mode: 0o644 },
      { name: `zaafpulse${exe}`, file: cp, mode: 0o755 },
      { name: `zaafpulse-gw${exe}`, file: gw, mode: 0o755 },
    ]);
  else execFileSync('tar', ['-czf', path.join('..', file), folder], { cwd: pkgDir, stdio: 'inherit' });
  console.log(`built dist/${file} (${mb(out)}; zaafpulse${exe} ${mb(cp)}, zaafpulse-gw${exe} ${mb(gw)})`);
  fs.rmSync(stage, { recursive: true, force: true });
  return out;
}

function main(argv) {
  const { targets, allowDirty, package: pkg } = parseArgs(argv);
  if (pkg) checkPackHost(targets);
  const version = JSON.parse(fs.readFileSync(path.join(ROOT, 'package.json'), 'utf8')).version;
  fs.mkdirSync(DIST, { recursive: true });
  const src = source(allowDirty);
  const sums = [];
  try {
    for (const target of targets) {
      let out;
      if (pkg) out = pack(src, version, target);
      else {
        out = path.join(DIST, outName(version, target));
        compile(src, target, out);
        console.log(`built dist/${path.basename(out)} (${(fs.statSync(out).size / 1e6).toFixed(0)} MB)`);
      }
      sums.push(`${sha256(out)}  ${path.basename(out)}`);
    }
  } finally {
    if (pkg) fs.rmSync(path.join(DIST, '.pkg'), { recursive: true, force: true });
  }
  fs.writeFileSync(path.join(DIST, 'SHA256SUMS.txt'), `${sums.join('\n')}\n`);
  if (src !== ROOT) fs.rmSync(path.join(DIST, '.work'), { recursive: true, force: true });
}

if (require.main === module) {
  try {
    main(process.argv.slice(2));
  } catch (err) {
    console.error(err.message);
    process.exit(1);
  }
} else
  module.exports = {
    INCLUDE,
    ARCHIVE,
    SCHEMAS,
    TARGETS,
    GO_TARGETS,
    GO_ENV,
    checkPackHost,
    gwSignArgs,
    hostTarget,
    outName,
    archiveName,
    gwBuildArgs,
    gwEnv,
    crc32,
    writeZip,
    compileArgs,
    parseArgs,
  };
