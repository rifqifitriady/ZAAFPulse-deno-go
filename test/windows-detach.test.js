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
  assert.doesNotMatch(src, /&\s*\$hostExe/);
  assert.match(src, /Invoke-Bounded \$what \$seconds \$hostExe /);
  assert.match(src, /\[string\]\$hostExe = \$InstallerHost\)/);
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

// Second v2.0.0 dry run (run 45181932316): the hang was gone, and three failures showed. A powershell.exe started from
// pwsh through Start-Process kept pwsh's module folders (Get-FileHash "not recognized"); GetFolderPath('Programs')
// was '' in a temp profile without a Start menu folder; v1.1.0's installer cannot run in Windows PowerShell 5.1 with
// Node on PATH.
const platform = require('../lib/platform');

test('windowsPowerShellModulePath: drops PowerShell 7 folders, keeps the rest in order, adds 5.1 system folder', () => {
  const pwshDll = new Set(['C:\\Program Files\\PowerShell\\7\\pwsh.dll']);
  const got = platform.windowsPowerShellModulePath(
    [
      'C:\\Users\\u\\Documents\\PowerShell\\Modules',
      'C:\\Program Files\\PowerShell\\Modules\\',
      'C:\\Program Files\\PowerShell\\7\\Modules',
      'C:\\Users\\u\\Documents\\WindowsPowerShell\\Modules',
      'C:\\Program Files\\WindowsPowerShell\\Modules',
      '',
      'c:\\program files\\windowspowershell\\modules\\',
      'C:\\Modules',
    ].join(';'),
    { systemRoot: 'C:\\Windows', exists: (f) => pwshDll.has(f) },
  );
  assert.equal(
    got,
    [
      'C:\\Users\\u\\Documents\\WindowsPowerShell\\Modules',
      'C:\\Program Files\\WindowsPowerShell\\Modules',
      'C:\\Modules',
      'C:\\Windows\\System32\\WindowsPowerShell\\v1.0\\Modules',
    ].join(';'),
  );
  // A clean 5.1 path stays as it is.
  const clean =
    'C:\\Users\\u\\Documents\\WindowsPowerShell\\Modules;C:\\Program Files\\WindowsPowerShell\\Modules;C:\\WINDOWS\\system32\\WindowsPowerShell\\v1.0\\Modules';
  assert.equal(platform.windowsPowerShellModulePath(clean, { systemRoot: 'C:\\WINDOWS', exists: () => false }), clean);
});

test('lib/platform.js: on Windows the process module path is fixed once, for every powershell.exe it starts', () => {
  assert.match(
    read('lib/platform.js'),
    /if \(OS === 'windows' && process\.env\.PSModulePath\) \{\s+const fixed = windowsPowerShellModulePath\(process\.env\.PSModulePath\);\s+if \(fixed !== process\.env\.PSModulePath\) process\.env\.PSModulePath = fixed;\s+\}/,
  );
});

test('install.ps1: no script-module cmdlet on the critical path, 5.1 module path, a Start menu folder or a warning', () => {
  const src = code(read('install.ps1'));
  // The checksum and the unpack with .NET alone.
  assert.doesNotMatch(src, /\bGet-FileHash\b|\bExpand-Archive\b/);
  assert.match(src, /function Get-Sha256\b[\s\S]*?\[Security\.Cryptography\.SHA256\]::Create\(\)/);
  assert.match(src, /if \(\(Get-Sha256 \$pkg\) -ne \$want\)/);
  assert.match(src, /\[IO\.Compression\.ZipFile\]::ExtractToDirectory\(\$pkg, \$x\)/);
  // In Windows PowerShell its own module path first thing; in pwsh the launcher gets 5.1's, and pwsh's comes back.
  assert.match(
    src,
    /\nif \(\$PSVersionTable\.PSEdition -ne 'Core' -and \$env:PSModulePath\) \{ \$env:PSModulePath = Get-WinPSModulePath \$env:PSModulePath \}/,
  );
  const svc = src.match(/function Invoke-Service\b[\s\S]*?\n\}/)[0];
  assert.match(svc, /PSEdition -eq 'Core'[^\n]*\$env:PSModulePath = Get-WinPSModulePath \$saved/);
  assert.match(svc, /try \{ \$p = Start-Process [^\n]*\}\s+finally \{ \$env:PSModulePath = \$saved \}/);
  // GetFolderPath('Programs') only in Get-ProgramsDir, which falls back to APPDATA; every use is guarded.
  assert.equal((src.match(/GetFolderPath\('Programs'\)/g) || []).length, 1);
  assert.match(
    src,
    /function Get-ProgramsDir\b[\s\S]*?Join-Path \$env:APPDATA 'Microsoft\\Windows\\Start Menu\\Programs'/,
  );
  const uses = src.match(/\$programs = Get-ProgramsDir\s+if \(\$programs\) \{/g) || [];
  assert.equal(uses.length, 2, 'the uninstall and the install both check the folder');
  assert.match(src, /New-Item -ItemType Directory -Force -Path \$programs/);
  assert.match(src, /\} else \{ Warn 'No Start menu folder[^\n]*shortcut was skipped/);
});

test('install-test.ps1: 5.1 installers get 5.1 module path, v1.1.0 runs in pwsh, the temp profile has a Start menu', () => {
  const src = code(read('scripts/install-test.ps1'));
  assert.match(src, /Join-Path \$env:APPDATA 'Microsoft\\Windows\\Start Menu\\Programs\\Startup'/);
  const inst = src.match(/function Invoke-Installer\b[\s\S]*?\n\}/)[0];
  assert.match(inst, /-ieq 'powershell\.exe'\) \{ \$set\.PSModulePath = Get-WinPSModulePath \$env:PSModulePath \}/);
  assert.match(inst, /Invoke-Bounded \$what \$seconds \$hostExe \([^\n]*\) \$set/);
  const bounded = src.match(/function Invoke-Bounded\b[\s\S]*?\n\}/)[0];
  assert.match(bounded, /try \{ \$p = Start-Process @sp \}\s+finally \{ foreach \(\$k in \$saved\.Keys\)/);
  assert.match(src, /\$V1Host = if \(\$PSVersionTable\.PSEdition -eq 'Core'\) \{ \(Get-Process -Id \$PID\)\.Path \}/);
  assert.match(src, /Invoke-Installer \$v1 @\('-NoService', '-Version', \$V1Tag\) 600 \$V1Host/);
  // v2's installers still run in -InstallerHost (Windows PowerShell 5.1 by default).
  for (const m of src.matchAll(/Invoke-Installer \(Join-Path \$RepoRoot 'install\.ps1'\)[^\n]*/g))
    assert.doesNotMatch(m[0], /V1Host/, m[0]);
});

// Runs functions taken from a script (by its AST) and then `body`, in pwsh.
function runPs(file, names, body, env = {}) {
  const script = [
    '$e = $null; $t = $null',
    '$ast = [System.Management.Automation.Language.Parser]::ParseFile($env:ZP_PS_FILE, [ref]$t, [ref]$e)',
    `foreach ($f in $ast.FindAll({ param($n) $n -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $n.Name -in @(${names.map((n) => `'${n}'`).join(', ')}) }, $true)) { . ([scriptblock]::Create($f.Extent.Text)) }`,
    body,
  ].join('; ');
  return spawnSync(pwsh, ['-NoProfile', '-NonInteractive', '-Command', script], {
    encoding: 'utf8',
    env: { ...process.env, ...env, ZP_PS_FILE: path.join(ROOT, file) },
  });
}

test('Get-WinPSModulePath in both scripts matches lib/platform.js', { skip: !pwsh && 'pwsh is not installed' }, () => {
  const value = [
    'C:\\Users\\u\\Documents\\PowerShell\\Modules',
    'C:\\Program Files\\PowerShell\\Modules\\',
    'C:\\Users\\u\\Documents\\WindowsPowerShell\\Modules',
    '',
    'C:\\Program Files\\WindowsPowerShell\\Modules',
    'c:\\program files\\windowspowershell\\modules\\',
    'D:\\Tools\\Modules',
  ].join(';');
  // No SystemRoot (pwsh on macOS and Linux joins with /), no pwsh.dll: the folder rules only.
  const want = platform.windowsPowerShellModulePath(value, { systemRoot: '', exists: () => false });
  for (const f of ['install.ps1', 'scripts/install-test.ps1']) {
    const r = runPs(f, ['Get-WinPSModulePath'], '$env:SystemRoot = $null; Get-WinPSModulePath $env:ZP_VALUE', {
      ZP_VALUE: value,
    });
    assert.equal(r.status, 0, r.stderr);
    assert.equal(r.stdout.trim(), want, f);
  }
});

test('install.ps1 Get-Sha256 is the file SHA-256', { skip: !pwsh && 'pwsh is not installed' }, () => {
  const file = path.join(ROOT, 'install.ps1');
  const want = require('crypto').createHash('sha256').update(fs.readFileSync(file)).digest('hex');
  const r = runPs('install.ps1', ['Get-Sha256'], 'Get-Sha256 $env:ZP_PS_FILE');
  assert.equal(r.status, 0, r.stderr);
  assert.equal(r.stdout.trim(), want);
});
