// Builds the single-file ZAAFPulse executables with `deno compile` (P2 T6); replaces the Node SEA build.
//   deno task build                                  this machine's target
//   deno task build --target <triple> [--target …]   any of TARGETS
//   deno task build --all                            all six (needs the denort base binaries, downloaded once)
//   --allow-dirty                                    build the working tree instead of the last commit (local tests)
// What ships is exactly the committed files under INCLUDE: they are taken from `git archive HEAD` into dist/.work, so
// an untracked or edited file never ships, and the build stops when a file that would ship has uncommitted changes.
// The binary runs with the "cli" permission set of deno.json, baked in at compile time (-P=cli, never -A). Its entry is
// the command line, which serves the dashboard, the MCP server, the service install and `doctor`, so it needs the
// superset: the "server" set lacks osRelease (doctor) and the service programs. -q keeps Deno's own notices out of
// the binary's output. A compiled binary serves its files from inside itself and
// unpacks nothing (spec D2): integrity is the release's SHA256SUMS and attestations.
// Output: dist/zaafpulse-<version>-<name>[.exe] and dist/SHA256SUMS.txt.
const { execFileSync } = require('child_process');
const crypto = require('crypto');
const fs = require('fs');
const path = require('path');

const ROOT = path.join(__dirname, '..');
const DIST = path.join(ROOT, 'dist');
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
];
// deno.json is in the source folder for -P=cli to find; it is not embedded.
const ARCHIVE = [...INCLUDE, 'deno.json'];
// Deno target triple -> the release name used since v1 (install.sh and install.ps1 pick by it).
const TARGETS = {
  'aarch64-apple-darwin': 'darwin-arm64',
  'x86_64-apple-darwin': 'darwin-x64',
  'x86_64-unknown-linux-gnu': 'linux-x64',
  'aarch64-unknown-linux-gnu': 'linux-arm64',
  'x86_64-pc-windows-msvc': 'win-x64',
  'aarch64-pc-windows-msvc': 'win-arm64',
};

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
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a === '--allow-dirty') allowDirty = true;
    else if (a === '--all') targets.push(...Object.keys(TARGETS));
    else if (a === '--target') {
      const t = argv[++i];
      if (!TARGETS[t]) throw new Error(`unknown target ${t}; one of ${Object.keys(TARGETS).join(', ')}`);
      targets.push(t);
    } else throw new Error(`unknown option ${a}`);
  }
  return { targets: targets.length ? [...new Set(targets)] : [hostTarget()], allowDirty };
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

function main(argv) {
  const { targets, allowDirty } = parseArgs(argv);
  const version = JSON.parse(fs.readFileSync(path.join(ROOT, 'package.json'), 'utf8')).version;
  fs.mkdirSync(DIST, { recursive: true });
  const src = source(allowDirty);
  const sums = [];
  for (const target of targets) {
    const out = path.join(DIST, outName(version, target));
    const args = compileArgs({ target, out, entry: path.join('bin', 'zaafpulse.js') });
    // One retry: the denort base binary for a target is downloaded on first use, and a download can fail once.
    for (let attempt = 1; ; attempt++) {
      try {
        execFileSync('deno', args, { cwd: src, stdio: 'inherit' }); // by name: `deno task` reports a symlink-resolved execPath that --allow-run=deno does not match
        break;
      } catch (err) {
        if (attempt === 2) throw err;
        console.warn(`deno compile failed for ${target}; trying once more`);
      }
    }
    sums.push(`${sha256(out)}  ${path.basename(out)}`);
    console.log(`built dist/${path.basename(out)} (${(fs.statSync(out).size / 1e6).toFixed(0)} MB)`);
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
} else module.exports = { INCLUDE, ARCHIVE, TARGETS, hostTarget, outName, compileArgs, parseArgs };
