// The Windows background start holds no handle of whoever started it, and nothing in the installers waits on the
// server (v2.0.0 dry run, run 45173734233: the Windows install test hung for 29 minutes after "ZAAFPulse is running").
// A child_process spawn of wscript.exe inherited the stdout pipe of `zaafpulse service install`, which install.ps1 read
// to its end with `| Out-Host`. Windows process and handle behaviour cannot run here; these tests pin the command
// lines and the scripts' shape, and parse both .ps1 files when pwsh is installed.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const { spawnSync } = require('child_process');
const service = require('../lib/service');

const ROOT = path.join(__dirname, '..');
const read = (f) => fs.readFileSync(path.join(ROOT, f), 'utf8').replace(/\r\n/g, '\n');
// PowerShell source without its comments (a # to the end of a line, outside a string, is all these scripts use).
const code = (src) =>
  src
    .split('\n')
    .map((l) => l.replace(/(^|\s)#.*$/, ''))
    .join('\n');
// Start-Process parameters that switch it from ShellExecuteEx (no inherited handles) to CreateProcess with every
// inheritable handle, or make it wait for every descendant.
const INHERITING =
  /-(NoNewWindow|RedirectStandard\w+|Credential|LoadUserProfile|UseNewEnvironment|Environment|Wait)\b/i;

test('winLaunchCommand: wscript.exe starts through Start-Process with ShellExecute, hidden, without waiting', () => {
  const vbs =
    "C:\\Users\\O'Brien & Co (x)\\AppData\\Roaming\\Microsoft\\Windows\\Start Menu\\Programs\\Startup\\ZAAFPulse.vbs";
  const { cmd, args, env } = service.winLaunchCommand(vbs, { SystemRoot: 'C:\\Windows' });
  assert.equal(cmd, 'powershell.exe');
  assert.deepEqual(args.slice(0, 3), ['-NoProfile', '-NonInteractive', '-Command']);
  const ps = args[3];
  assert.match(ps, /Start-Process -FilePath 'wscript\.exe' /);
  assert.match(ps, /-ArgumentList \('\/\/B \/\/NoLogo ' \+ \[char\]34 \+ \$vbs \+ \[char\]34\)/);
  assert.match(ps, /-WindowStyle Hidden/);
  assert.doesNotMatch(ps, INHERITING);
  assert.match(ps, /\$ErrorActionPreference = 'Stop'/);
  // The path never crosses the command line: it comes through the environment, which the script then clears.
  assert.ok(!args.join(' ').includes(vbs));
  assert.ok(!ps.includes('"'), 'no double quote on the powershell.exe command line');
  assert.equal(env.ZAAFPULSE_LAUNCHER_VBS, vbs);
  assert.equal(env.SystemRoot, 'C:\\Windows');
  assert.match(ps, /Remove-Item -LiteralPath Env:ZAAFPULSE_LAUNCHER_VBS/);
});

test('winLaunchCommand: the helper is not taken for a running server or the launcher by win.stop and win.running', () => {
  const { args } = service.winLaunchCommand('C:\\x\\ZAAFPulse.vbs');
  const line = `powershell.exe ${args.join(' ')}`;
  assert.ok(!/ZAAFPulse\.vbs|zaafpulse-service\.vbs/i.test(line));
  // A server is a command line that names zaafpulse and has `start` as an argument of its own.
  const isServer =
    new RegExp(service.WIN_SERVER_RE, 'i').test(line) && new RegExp(service.WIN_START_RE, 'i').test(line);
  assert.equal(isServer, false);
});

test('lib/service.js: no child_process spawn of wscript.exe; the launch goes through winLaunchCommand with a limit', () => {
  const src = read('lib/service.js');
  assert.doesNotMatch(src, /spawn\(\s*['"]wscript/);
  const launch = src.match(/\n {2}launch\(vbs\) \{[\s\S]*?\n {2}\},/);
  assert.ok(launch, 'win.launch');
  assert.match(launch[0], /winLaunchCommand\(vbs\)/);
  assert.match(launch[0], /timeout: \d+/);
  assert.match(launch[0], /throw new Error/);
});

test('vbsText: the launcher runs the server hidden (window style 0) and supervises it in a loop', () => {
  const vbs = service.vbsText(['C:\\Z\\bin\\zaafpulse.exe', 'start'], {}, 'C:\\Z\\logs\\server.log');
  assert.match(vbs, /\nDo\n {2}sh\.Run "cmd\.exe \/c [^\n]*", 0, True\n {2}WScript\.Sleep 10000\nLoop\n/);
  // The launcher itself is started without waiting; only its loop waits, for the server it supervises.
  assert.equal((vbs.match(/\.Run /g) || []).length, 1);
});

test('install.ps1: the service commands run without a pipe and wait for the launcher alone, with a limit', () => {
  const src = code(read('install.ps1'));
  // Nothing reads a launcher's output to its end.
  assert.doesNotMatch(src, /&\s*\$launcher\b[^\n]*\bservice\b/);
  assert.doesNotMatch(src, /&\s*\$launcher\s+@svcArgs/);
  const fn = src.match(/function Invoke-Service\b[\s\S]*?\n\}/);
  assert.ok(fn, 'Invoke-Service');
  assert.match(fn[0], /Start-Process -FilePath \$launcher [^\n]*-PassThru/);
  assert.doesNotMatch(fn[0], /-Wait\b|-RedirectStandard/);
  assert.match(fn[0], /\$null = \$p\.Handle/);
  assert.match(fn[0], /WaitForExit\(\d+\)/);
  // Both service calls go through it.
  assert.match(src, /function Stop-ZaafService[\s\S]*?Invoke-Service \$launcher \$svcArgs[\s\S]*?\n\}/);
  assert.match(src, /function Start-ZaafService[\s\S]*?Invoke-Service \$launcher \$svcArgs[\s\S]*?\n\}/);
  // No Start-Process anywhere waits for descendants.
  for (const line of src.split('\n').filter((l) => /Start-Process\b/.test(l)))
    assert.doesNotMatch(line, /-Wait\b/, line);
  // The Start menu opener runs `zaafpulse open` hidden and does not wait.
  assert.match(read('install.ps1'), /\.Run `"\$run`", 0, False/);
});

test('install-test.ps1: every program runs through Invoke-Bounded with a timeout, never through a pipe', () => {
  const src = code(read('scripts/install-test.ps1'));
  // The installers: no `& $InstallerHost ... | Out-Host`.
  assert.doesNotMatch(src, /&\s*\$InstallerHost/);
  assert.match(src, /Invoke-Bounded \$what \$seconds \$InstallerHost/);
  // The only native call made with & is taskkill on a timeout; the rest of & runs script blocks.
  for (const m of src.matchAll(/&\s+([\w.$]+)/g)) assert.ok(['taskkill.exe', '$cond', '$body'].includes(m[1]), m[0]);
  // Invoke-Bounded: output to files, a progress line, the timeout kills the tree and throws.
  const fn = src.match(/function Invoke-Bounded\b[\s\S]*?\n\}/);
  assert.ok(fn, 'Invoke-Bounded');
  assert.match(fn[0], /RedirectStandardOutput = \$out; RedirectStandardError = \$err/);
  assert.doesNotMatch(fn[0], /\bWait = /);
  assert.match(fn[0], /WaitForExit\(1000\)/);
  assert.match(fn[0], /still waiting for/);
  assert.match(fn[0], /taskkill\.exe \/PID \$p\.Id \/T \/F/);
  assert.match(fn[0], /\$script:TimedOut = \$true\s+throw "\$label timed out after \$seconds s"/);
  // Every call to it names a limit in seconds.
  const calls = [...src.matchAll(/Invoke-Bounded (?:'[^']*'|"[^"]*"|\$\w+) (\S+)/g)];
  assert.ok(calls.length >= 8, `${calls.length} calls`);
  for (const [line, limit] of calls) assert.match(limit, /^(\d+|\$seconds)$/, line);
  // Every wait has a limit: no Start-Process -Wait, no WaitForExit() without one, every HTTP call has -TimeoutSec,
  // every poll a number of seconds.
  for (const line of src.split('\n')) {
    if (/Start-Process\b/.test(line)) assert.doesNotMatch(line, /-Wait\b/, line);
    if (/Invoke-(WebRequest|RestMethod)\b/.test(line)) assert.match(line, /-TimeoutSec \d+/, line);
    if (/Wait-Until\s/.test(line) && !/^function/.test(line.trim())) assert.match(line, /Wait-Until \d+ \{/, line);
  }
  assert.doesNotMatch(src, /WaitForExit\(\)/);
  assert.doesNotMatch(src, /\b(Wait-Process|Wait-Job|Receive-Job)\b/);
});

test('install-test.ps1: a failure (a timeout too) prints the logs with the server log, skips the rest and exits 1', () => {
  const src = code(read('scripts/install-test.ps1'));
  const show = src.match(/function Show-Logs\b[\s\S]*?\n\}/);
  assert.ok(show && /logs\\server\.log/.test(show[0]), 'Show-Logs prints the service log');
  const part = src.match(/function Invoke-Part\b[\s\S]*?\n\}/);
  assert.ok(part, 'Invoke-Part');
  assert.match(part[0], /if \(\$script:TimedOut\) \{[\s\S]*?\$script:failed = \$true[\s\S]*?SKIPPED[\s\S]*?return/);
  assert.match(part[0], /catch \{[\s\S]*?\$script:failed = \$true[\s\S]*?Show-Logs/);
  assert.match(src, /finally \{\s*Complete-Part\s*try \{ Stop-TempLeftovers \}/);
  assert.match(src, /if \(\$failed\) \{ exit 1 \}/);
});

test('install.sh: the Linux and macOS service is started by systemd or launchd, nothing in the background of the installer', () => {
  const src = read('install.sh')
    .split('\n')
    .filter((l) => !/^\s*#/.test(l));
  for (const line of src) {
    assert.doesNotMatch(line, /\bnohup\b|\bsetsid\b|\bdisown\b/, line);
    assert.doesNotMatch(line, /[^&|]&\s*$/, line);
  }
  const svc = read('lib/service.js');
  assert.match(svc, /linux\.ctl\(system, 'enable', '--now', UNIT\)/);
  assert.match(svc, /sh\('launchctl', \['bootstrap', dom, mac\.file\(\)\]\)/);
});

// pwsh, when installed (macOS: brew install powershell; GitHub's ubuntu and windows runners have it).
const pwsh = (() => {
  const r = spawnSync('pwsh', ['-NoProfile', '-NonInteractive', '-Command', '$PSVersionTable.PSVersion.Major'], {
    encoding: 'utf8',
  });
  return r.status === 0 ? 'pwsh' : null;
})();

test('install.ps1 and install-test.ps1 parse', { skip: !pwsh && 'pwsh is not installed' }, () => {
  for (const f of ['install.ps1', 'scripts/install-test.ps1']) {
    const script = [
      '$e = $null; $t = $null',
      `[void][System.Management.Automation.Language.Parser]::ParseFile($env:ZP_PS_FILE, [ref]$t, [ref]$e)`,
      'foreach ($x in $e) { "$($x.Extent.StartLineNumber): $($x.Message)" }',
      'exit $e.Count',
    ].join('; ');
    const r = spawnSync(pwsh, ['-NoProfile', '-NonInteractive', '-Command', script], {
      encoding: 'utf8',
      env: { ...process.env, ZP_PS_FILE: path.join(ROOT, f) },
    });
    assert.equal(r.status, 0, `${f}: ${r.stdout}${r.stderr}`);
  }
});

test(
  'install-test.ps1 Join-CommandLine quotes as CommandLineToArgvW reads',
  { skip: !pwsh && 'pwsh is not installed' },
  () => {
    const script = [
      '$e = $null; $t = $null',
      '$ast = [System.Management.Automation.Language.Parser]::ParseFile($env:ZP_PS_FILE, [ref]$t, [ref]$e)',
      "$f = $ast.Find({ param($n) $n -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $n.Name -eq 'Join-CommandLine' }, $true)",
      '. ([scriptblock]::Create($f.Extent.Text))',
      "Join-CommandLine @('-File', 'D:\\a\\b.ps1', 'a b', '', 'C:\\x y\\', 'a\"b', 'C:\\p\\')",
    ].join('; ');
    const r = spawnSync(pwsh, ['-NoProfile', '-NonInteractive', '-Command', script], {
      encoding: 'utf8',
      env: { ...process.env, ZP_PS_FILE: path.join(ROOT, 'scripts', 'install-test.ps1') },
    });
    assert.equal(r.status, 0, r.stderr);
    assert.equal(r.stdout.trim(), '-File D:\\a\\b.ps1 "a b" "" "C:\\x y\\\\" "a\\"b" C:\\p\\');
  },
);
