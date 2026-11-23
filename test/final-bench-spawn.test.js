// The final benchmark runner starts programs without a shell (CodeQL js/shell-command-injection-from-environment and
// js/incomplete-sanitization on bench/final): only allow-listed command names or this runtime's own executable, every
// argument as one array element, never interpreted. And its report's table cells escape backslashes before pipes.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const os = require('os');
const path = require('path');
const spawnsafe = require('../bench/final/spawnsafe');
const runner = require('../bench/final/runner');
const pf = require('../bench/final/preflight');
const report = require('../bench/final/report');

// An argument a shell would split, expand or run: spaces, both quotes, ;, $(), backticks, a pipe and a redirect.
const NASTY = `/tmp/a dir/it's "x"; echo PWNED $(echo PWNED) \`echo PWNED\` | cat > /tmp/zp-pwned`;
const ECHO = 'process.stdout.write(JSON.stringify(process.argv.slice(1)))';
const echoArgs = (args) => (process.versions.deno ? ['eval', ECHO, ...args] : ['-e', ECHO, ...args]);

test('checkedCommand: allow-listed names and this runtime only; never a shell', () => {
  assert.equal(spawnsafe.checkedCommand(process.execPath), process.execPath);
  for (const name of ['ps', 'go', 'git', 'tar', 'caffeinate', 'powershell'])
    assert.equal(spawnsafe.checkedCommand(name), name);
  for (const bad of [
    'sh',
    'bash',
    '/bin/sh',
    'cmd',
    'cmd.exe',
    'node; rm -rf /',
    '/tmp/x',
    '',
    null,
    path.join(os.tmpdir(), 'deno'),
  ])
    assert.throws(() => spawnsafe.checkedCommand(bad), /not allowed/, String(bad));
});

test('runner.runChild: a nasty path is one argument and is not interpreted', async () => {
  const r = await runner.runChild(
    { cmd: process.execPath, args: echoArgs([NASTY, 'second']) },
    { env: process.env, timeoutMs: 30000 },
  );
  assert.equal(r.code, 0, r.stderr);
  assert.deepEqual(JSON.parse(r.stdout).slice(-2), [NASTY, 'second']);
  assert.equal(fs.existsSync('/tmp/zp-pwned'), false);
});

test('runner.runChild: a shell is refused, and withFileLimit no longer wraps in one', async () => {
  const r = await runner.runChild({ cmd: 'sh', args: ['-c', 'echo hi'] }, { env: process.env, timeoutMs: 5000 });
  assert.equal(r.code, null);
  assert.match(r.stderr, /not allowed/);
  const c = { cmd: process.execPath, args: ['x.js'] };
  assert.deepEqual(runner.withFileLimit(c), c);
});

test('preflight.run: a nasty path is one argument; a shell is refused', () => {
  const r = pf.run(process.execPath, echoArgs([NASTY]));
  assert.equal(r.status, 0, String(r.stderr));
  assert.deepEqual(JSON.parse(r.stdout).slice(-1), [NASTY]);
  const sh = pf.run('sh', ['-c', 'ulimit -Hn']);
  assert.equal(sh.status, null);
  assert.match(String(sh.error), /not allowed/);
});

test('findExecutable: a regular executable file on PATH, read without running anything', () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'zp-path-'));
  try {
    const exe = path.join(dir, process.platform === 'win32' ? 'fakeawake.exe' : 'fakeawake');
    fs.writeFileSync(exe, '');
    fs.chmodSync(exe, 0o755);
    fs.mkdirSync(path.join(dir, 'adir'));
    const env = { PATH: `${path.join(dir, 'nope')}${path.delimiter}${dir}`, PATHEXT: '.EXE' };
    assert.equal(spawnsafe.findExecutable('fakeawake', env), exe);
    assert.equal(spawnsafe.findExecutable('adir', env), null, 'a folder is not an executable');
    assert.equal(spawnsafe.findExecutable('missing', env), null);
    assert.equal(spawnsafe.findExecutable('../fakeawake', env), null, 'a name, never a path');
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('openFilesReach: the limit proved by opening files in this process, no shell', () => {
  assert.equal(pf.openFilesReach(64), true);
});

// GFM splits a row on every pipe that a backslash does not escape (an escaped backslash does not escape it).
function cells(row) {
  const out = [];
  let cur = '';
  for (let i = 0; i < row.length; i++) {
    if (row[i] === '\\' && i + 1 < row.length) {
      cur += row[i] + row[i + 1];
      i++;
    } else if (row[i] === '|') {
      out.push(cur);
      cur = '';
    } else cur += row[i];
  }
  out.push(cur);
  return out.slice(1, -1).map((s) => s.trim());
}

test('report cells: a backslash before a pipe stays inside one cell', () => {
  assert.equal(report.cell('a\\|b'), 'a\\\\\\|b');
  assert.equal(report.cell('C:\\dir\\'), 'C:\\\\dir\\\\');
  assert.equal(report.cell('x\ny'), 'x y');
  assert.equal(report.cell(null), '—');
  const row = report.table(['A', 'B'], [['a\\|b', 'end\\']]).split('\n')[2];
  assert.deepEqual(cells(row).length, 2, row);
});

test('psDriveName: only a single drive letter reaches the PowerShell command line', () => {
  assert.equal(pf.psDriveName('C:\\Users\\me\\out'), 'C');
  assert.equal(pf.psDriveName('d:/bench'), 'D');
  for (const bad of ['\\\\server\\share\\x', '/tmp/x', 'C:; Remove-Item x', '$(evil):\\x', ''])
    assert.equal(pf.psDriveName(bad), null, bad);
});
