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
// the archives. The zip is written by bsdtar (`tar -a`), so win-* packages are built on macOS or Windows.
const { execFileSync } = require('child_process');
const crypto = require('crypto');
const fs = require('fs');
const path = require('path');

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

// A zip is written with `tar -a`, which only bsdtar can do; GNU tar would write a tar file named .zip.
function checkZipTar(versionText) {
  if (!/bsdtar/.test(versionText))
    throw new Error('zip packages need bsdtar (macOS or Windows); build win-* targets there');
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
  fs.copyFileSync(path.join(src, 'LICENSE'), path.join(stage, 'LICENSE'));
  const out = path.join(DIST, file);
  fs.rmSync(out, { force: true });
  // Relative names and cwd: GNU tar on Windows (Git Bash) reads `D:\...` as a host name.
  if (zip) execFileSync('tar', ['-a', '-cf', path.join('..', file), folder], { cwd: pkgDir, stdio: 'inherit' });
  else execFileSync('tar', ['-czf', path.join('..', file), folder], { cwd: pkgDir, stdio: 'inherit' });
  console.log(`built dist/${file} (${mb(out)}; zaafpulse${exe} ${mb(cp)}, zaafpulse-gw${exe} ${mb(gw)})`);
  fs.rmSync(stage, { recursive: true, force: true });
  return out;
}

function main(argv) {
  const { targets, allowDirty, package: pkg } = parseArgs(argv);
  const version = JSON.parse(fs.readFileSync(path.join(ROOT, 'package.json'), 'utf8')).version;
  // Before any compile: a zip without bsdtar would fail only after the long builds.
  if (pkg && targets.some((t) => TARGETS[t].startsWith('win')))
    checkZipTar(execFileSync('tar', ['--version'], { encoding: 'utf8' }));
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
    hostTarget,
    outName,
    archiveName,
    gwBuildArgs,
    gwEnv,
    checkZipTar,
    compileArgs,
    parseArgs,
  };
