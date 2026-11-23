# The install test of one release package on Windows (P6 Decision 14; release.yml job install-test):
#
#   ./scripts/install-test.ps1 -Archive FILE -Sums FILE -Name <win-x64|win-arm64> [-NoServiceTest] [-NoUpgrade]
#                              [-M12 auto|on|off] [-InstallerHost powershell.exe]
#
# CI runners only (GITHUB_ACTIONS=true): install.ps1 always writes the user PATH and a Start menu shortcut, and the
# service test writes a Startup entry, all of which reach past the temp folder.
# Everything else runs in a temp folder: USERPROFILE, HOME, LOCALAPPDATA (the install root is
# %LOCALAPPDATA%\ZAAFPulse) and APPDATA, and the data folders (ZAAFPULSE_DATA). The server listens on 127.0.0.1:4877
# (never 4777), with ZAAFPULSE_AUTH=none. A server this script starts runs in the background as the `zaafpulse`
# command, with the install's bin folder first on PATH, spelled as install.ps1 spells it (P6 Decision 4: Deno matches
# it against the binary's own path, case-sensitively), and is always stopped, on failure too.
# The installers run in their own process of -InstallerHost: Windows PowerShell 5.1 (powershell.exe) by default, the
# host of an `irm | iex` install, so the 5.1 path of install.ps1 is the one exercised; started from this pwsh with
# 5.1's own module path. v1.1.0's installer too, without node on its PATH (see Test-Upgrade).
#   1. Clean install: install.ps1 -Archive -Sums with its background service (the Startup launcher; -NoServiceTest:
#      -NoService and a background start); `zaafpulse version` is the package version and `zaafpulse doctor` passes;
#      /login.html answers and /api/gateway/status is front and ready with the package version (gateway.engine "front"
#      is written first); then -Uninstall, and the port is free.
#   2. Upgrade from v1.1.0: the v1.1.0 install.ps1 from the public repo at its tag (-NoService; needs Node 18+ on PATH);
#      v1 starts and a marker setting is written (userName, POST /api/config); the v2 installer from the package; v2
#      starts in front mode and sees the marker, and rollback\VERSION says v1.1.0; -Rollback; v1 starts again and still
#      sees the marker. -NoUpgrade skips it.
#   3. M12: scripts/upgrade-check.js --v2 on the unpacked package (needs Node). By default on win-x64 only.
# Prints one summary line per part (also to $env:GITHUB_STEP_SUMMARY when set) and exits non-zero when a part fails.
# Every wait is bounded: each program (an installer, zaafpulse version|doctor|service status, node) runs through
# Invoke-Bounded, with its output going to files (never a pipe, which a background process it starts could inherit
# and hold open) and shown as it comes, a progress line every 30 s, and a timeout; every HTTP call has -TimeoutSec, and
# every poll (Wait-Until) has its own limit. A timeout kills that program's process tree, prints the logs (the
# server's too) and fails the part; the parts after it are skipped, so the step fails fast and named instead of
# running into the job timeout. At the end anything still running from the temp install is ended.
# PowerShell 5.1 or 7. Set KEEP_TEMP=1 to keep the temp folder.
param(
  [Parameter(Mandatory = $true)][string]$Archive,
  [Parameter(Mandatory = $true)][string]$Sums,
  [Parameter(Mandatory = $true)][string]$Name,
  [switch]$NoServiceTest,
  [switch]$NoUpgrade,
  [ValidateSet('auto', 'on', 'off')][string]$M12 = 'auto',
  [string]$InstallerHost = ''
)
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

$Port = 4877
$V1Tag = 'v1.1.0'
$V1Installer = "https://raw.githubusercontent.com/rifqifitriady/ZAAFPulse/$V1Tag/install.ps1"
function Say($m) { Write-Host "==> $m" -ForegroundColor Cyan }

if ($env:GITHUB_ACTIONS -ne 'true') {
  throw 'install-test.ps1 runs on a CI runner only: install.ps1 changes the user PATH and the Start menu'
}
if (-not (Test-Path -LiteralPath $Archive -PathType Leaf)) { throw "No such archive: $Archive" }
if (-not (Test-Path -LiteralPath $Sums -PathType Leaf)) { throw "No such sums file: $Sums" }
$Archive = (Resolve-Path -LiteralPath $Archive).Path
$Sums = (Resolve-Path -LiteralPath $Sums).Path
$RepoRoot = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..')).Path
$Base = Split-Path -Leaf $Archive
$suffix = "-$Name.zip"
if (-not ($Base.StartsWith('zaafpulse-') -and $Base.EndsWith($suffix))) {
  throw "$Base is not the zip package of $Name (zaafpulse-<version>-$Name.zip)"
}
$Version = $Base.Substring('zaafpulse-'.Length, $Base.Length - 'zaafpulse-'.Length - $suffix.Length)
$Top = $Base.Substring(0, $Base.Length - '.zip'.Length)
$ServiceTest = -not $NoServiceTest
if ($M12 -eq 'auto') { $M12 = if ($Name -eq 'win-x64') { 'on' } else { 'off' } }
if (-not $InstallerHost) {
  $wp = Get-Command powershell.exe -ErrorAction SilentlyContinue
  $InstallerHost = if ($wp) { $wp.Source } else { (Get-Process -Id $PID).Path }
}
$Url = "http://127.0.0.1:$Port"

# The port must be free: never fight another server.
function Test-Listening { return [bool](Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue) }
if (Test-Listening) { throw "Port $Port is in use; stop what listens there first" }

# ---- the temp world ----
$tempBase = if ($env:RUNNER_TEMP) { $env:RUNNER_TEMP } else { [IO.Path]::GetTempPath() }
$T = Join-Path $tempBase ('zp-install-test-' + [guid]::NewGuid().ToString('N').Substring(0, 8))
New-Item -ItemType Directory -Path $T, (Join-Path $T 'logs') | Out-Null
$env:USERPROFILE = Join-Path $T 'home'
$env:HOME = $env:USERPROFILE
$env:LOCALAPPDATA = Join-Path $env:USERPROFILE 'AppData\Local'
$env:APPDATA = Join-Path $env:USERPROFILE 'AppData\Roaming'
# A real profile has the Start menu folders; [Environment]::GetFolderPath('Programs') is '' while they are missing.
New-Item -ItemType Directory -Force -Path $env:LOCALAPPDATA, $env:APPDATA,
  (Join-Path $env:APPDATA 'Microsoft\Windows\Start Menu\Programs\Startup') | Out-Null
$env:ZAAFPULSE_PORT = "$Port"
$env:ZAAFPULSE_AUTH = 'none'
foreach ($v in 'ZAAFPULSE_HOST', 'ZAAFPULSE_DATA', 'ZAAFPULSE_PUBLIC_URL', 'ZAAFPULSE_SECRET_STORE', 'CLAUDE_CONFIG_DIR',
  'ZAAFPULSE_CLAUDE_DIRS', 'CODEX_HOME', 'ZAAFPULSE_CLAUDE_BIN') {
  Remove-Item -LiteralPath "Env:$v" -ErrorAction SilentlyContinue
}
# As install.ps1 computes them, from the same LOCALAPPDATA string.
$Root = Join-Path $env:LOCALAPPDATA 'ZAAFPulse'
$BinDir = Join-Path $Root 'bin'
$Exe = Join-Path $BinDir 'zaafpulse.exe'
$V1Cmd = Join-Path $BinDir 'zaafpulse.cmd'
$env:Path = "$BinDir;$env:Path"
$Marker = "install-test-$PID-$([DateTimeOffset]::UtcNow.ToUnixTimeSeconds())"
$script:ServerProc = $null
$script:ServiceInstalled = $false
$script:PartLine = $null
$script:TimedOut = $false
$script:Step = 0

# ---- helpers ----
# Windows PowerShell 5.1's module path, as install.ps1 Get-WinPSModulePath (and pwsh's own `&` for powershell.exe)
# computes it: without PowerShell 7's module folders, which a powershell.exe started from this pwsh through
# Start-Process would otherwise search first and fail to load its own modules from (Get-FileHash "not recognized",
# the second v2.0.0 dry run).
function Get-WinPSModulePath([string]$value) {
  $keep = New-Object System.Collections.Generic.List[string]
  foreach ($d in @("$value" -split ';')) {
    $p = $d.Trim().TrimEnd('\', '/')
    if (-not $p -or $p -match '(^|[\\/])PowerShell[\\/]Modules$') { continue }
    $parent = [IO.Path]::GetDirectoryName($p)
    if ($parent -and [IO.File]::Exists([IO.Path]::Combine($parent, 'pwsh.dll'))) { continue }
    if (-not ($keep -contains $p)) { $keep.Add($p) }
  }
  if ($env:SystemRoot) {
    $sys = [IO.Path]::Combine($env:SystemRoot, 'System32\WindowsPowerShell\v1.0\Modules')
    if (-not ($keep -contains $sys)) { $keep.Add($sys) }
  }
  return ($keep -join ';')
}
# One Windows command line from an argument list (the CommandLineToArgvW rules): Start-Process -ArgumentList joins
# its items with spaces and quotes none of them.
function Join-CommandLine([string[]]$argv) {
  $quoted = foreach ($a in $argv) {
    if ($a -and $a -notmatch '[\s"]') { $a }
    else { '"' + (($a -replace '(\\*)"', '$1$1\"') -replace '(\\+)$', '$1$1') + '"' }
  }
  return ($quoted -join ' ')
}
# The text a file gained since $pos bytes; the writer may still hold it open (Start-Process opens it shared).
function Read-Since([string]$file, [ref]$pos) {
  try {
    $fs = [IO.File]::Open($file, 'Open', 'Read', 'ReadWrite, Delete')
    try {
      if ($fs.Length -le $pos.Value) { return '' }
      $null = $fs.Seek($pos.Value, 'Begin')
      $buf = New-Object byte[] ($fs.Length - $pos.Value)
      $n = $fs.Read($buf, 0, $buf.Length)
      $pos.Value += $n
      return [Text.Encoding]::UTF8.GetString($buf, 0, $n)
    } finally { $fs.Dispose() }
  } catch { return '' }
}
# Runs a program to its end within $seconds and returns @{ Code; Out } (Out: its stdout lines). Its stdout and stderr
# go to files under $T\logs, never a pipe: a pipe stays open while any process that inherited it runs (a server it
# started in the background), and reading it to its end would wait for that server. What it prints is shown as it
# comes, with a progress line every 30 s. This waits for the program's own process only, never for its descendants
# (no Start-Process -Wait). On a timeout its process tree is killed and the call throws, naming the step.
# $SetEnv: environment variables the program gets (set around Start-Process, then put back).
function Invoke-Bounded([string]$label, [int]$seconds, [string]$file, [string[]]$argv = @(), [hashtable]$SetEnv = @{}) {
  $script:Step++
  $out = Join-Path $T "logs\step$($script:Step).out.log"
  $err = Join-Path $T "logs\step$($script:Step).err.log"
  $sp = @{ FilePath = $file; NoNewWindow = $true; PassThru = $true; RedirectStandardOutput = $out; RedirectStandardError = $err }
  if ($argv.Count -gt 0) { $sp.ArgumentList = Join-CommandLine $argv }
  Say "$label (at most $seconds s)"
  $start = Get-Date
  $saved = @{}
  foreach ($k in $SetEnv.Keys) { $saved[$k] = [Environment]::GetEnvironmentVariable($k); [Environment]::SetEnvironmentVariable($k, $SetEnv[$k]) }
  try { $p = Start-Process @sp }
  finally { foreach ($k in $saved.Keys) { [Environment]::SetEnvironmentVariable($k, $saved[$k]) } }
  $null = $p.Handle   # Windows PowerShell 5.1: without the handle cached, ExitCode stays empty
  $posOut = 0; $posErr = 0
  $nextNote = $start.AddSeconds(30)
  $done = $false
  while (-not $done) {
    $done = $p.WaitForExit(1000)
    $text = (Read-Since $out ([ref]$posOut)) + (Read-Since $err ([ref]$posErr))
    if ($text) { Write-Host -NoNewline $text }
    if ($done) { break }
    $now = Get-Date
    if ($now -ge $start.AddSeconds($seconds)) {
      Write-Host ''
      Write-Host "x $label timed out after $seconds s; killing process $($p.Id) and what it started" -ForegroundColor Red
      # Its stderr must not end this script (Windows PowerShell 5.1 under Stop makes redirected stderr fatal).
      & { $ErrorActionPreference = 'Continue'; & taskkill.exe /PID $p.Id /T /F 2>&1 | ForEach-Object { "$_" } | Out-Host }
      $script:TimedOut = $true
      throw "$label timed out after $seconds s"
    }
    if ($now -ge $nextNote) {
      Say "... still waiting for $label ($([int]($now - $start).TotalSeconds) s of $seconds s)"
      $nextNote = $now.AddSeconds(30)
    }
  }
  $code = $p.ExitCode
  Say "$label exited $code after $([int]((Get-Date) - $start).TotalSeconds) s"
  $lines = @(Get-Content -LiteralPath $out -ErrorAction SilentlyContinue)
  return [pscustomobject]@{ Code = $code; Out = $lines }
}
function Wait-Until([int]$seconds, [scriptblock]$cond) {
  $end = (Get-Date).AddSeconds($seconds)
  while ($true) {
    $ok = $false
    try { $ok = [bool](& $cond) } catch { $ok = $false }
    if ($ok) { return $true }
    if ((Get-Date) -ge $end) { return $false }
    Start-Sleep -Milliseconds 500
  }
}
function Test-Login {
  try { return ((Invoke-WebRequest -UseBasicParsing -TimeoutSec 3 -Uri "$Url/login.html").StatusCode -eq 200) } catch { return $false }
}
function Get-GatewayStatus { return Invoke-RestMethod -TimeoutSec 3 -Uri "$Url/api/gateway/status" }
function Write-Config([string]$dir, [bool]$front) {
  New-Item -ItemType Directory -Force -Path $dir | Out-Null
  $json = if ($front) { '{ "gateway": { "engine": "front" }, "pricingSync": { "enabled": false } }' } else { '{ "pricingSync": { "enabled": false } }' }
  [IO.File]::WriteAllText((Join-Path $dir 'config.json'), $json + "`n")
}
# gateway.engine "front" in a config.json v1 wrote to, every other key kept (Node: an exact JSON round trip). The
# script goes through a file: PowerShell 5.1 does not escape the double quotes of an argument to a native program.
function Set-Front([string]$dir) {
  $js = Join-Path $T 'set-front.js'
  [IO.File]::WriteAllText($js, 'const fs = require("fs"); const f = process.argv[2];' +
    ' const c = JSON.parse(fs.readFileSync(f, "utf8")); c.gateway = { ...(c.gateway || {}), engine: "front" };' +
    ' fs.writeFileSync(f, JSON.stringify(c, null, 2) + "\n");')
  $node = (Get-Command node).Source
  $r = Invoke-Bounded 'node set-front.js' 60 $node @($js, (Join-Path $dir 'config.json'))
  if ($r.Code -ne 0) { throw "Could not set gateway.engine in $dir\config.json" }
}
# An installer in its own -InstallerHost process. Not `& $InstallerHost ... | Out-Host`: that reads the installer's
# output to its end, and the end never comes while a process the installer started in the background (the service's
# launcher and server) holds the pipe (the v2.0.0 dry run hung there for 29 minutes). Invoke-Bounded waits for the
# installer's process alone, at most $seconds. A Windows PowerShell host gets 5.1's module path (Get-WinPSModulePath),
# as pwsh's own `&` would give it. $setEnv: more environment for the installer (Test-Upgrade: its PATH).
function Invoke-Installer([string]$file, [string[]]$installerArgs, [int]$seconds = 300, [string]$hostExe = $InstallerHost,
  [hashtable]$setEnv = @{}) {
  $what = "$(Split-Path -Leaf $file) $($installerArgs -join ' ')"
  $set = @{} + $setEnv
  if ((Split-Path -Leaf $hostExe) -ieq 'powershell.exe') { $set.PSModulePath = Get-WinPSModulePath $env:PSModulePath }
  $r = Invoke-Bounded $what $seconds $hostExe (@('-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-File', $file) + $installerArgs) $set
  if ($r.Code -ne 0) { throw "$what failed (exit $($r.Code))" }
}
function Install-V2([string[]]$more) {
  Invoke-Installer (Join-Path $RepoRoot 'install.ps1') (@('-Archive', $Archive, '-Sums', $Sums, '-Port', "$Port") + $more)
}
# The zaafpulse command as PATH resolves it here: v2's .exe comes before v1's .cmd in PATHEXT.
function Get-Launcher {
  if (Test-Path -LiteralPath $Exe) { return $Exe }
  if (Test-Path -LiteralPath $V1Cmd) { return $V1Cmd }
  throw "No zaafpulse command in $BinDir"
}
function Assert-Version([string]$want, [string]$what) {
  $r = Invoke-Bounded "$what`: zaafpulse version" 60 (Get-Launcher) @('version')
  if ($r.Code -ne 0) { throw "${what}: zaafpulse version failed (exit $($r.Code))" }
  $v = "$(@($r.Out) | Select-Object -First 1)".Trim()
  if ($v -ne $want) { throw "${what}: zaafpulse version says '$v', expected $want" }
}
# A server this script owns, in the background: its output goes to files, it is waited for through HTTP polls with a
# limit (Wait-Until), and Stop-OwnServer kills its process tree.
function Start-Server([string]$label) {
  $launcher = Get-Launcher
  Say "${label}: starting the server ($launcher start), waiting at most 60 s for /login.html"
  $script:ServerProc = Start-Process -FilePath $launcher -ArgumentList 'start' -NoNewWindow -PassThru `
    -RedirectStandardOutput (Join-Path $T "logs\$label.out.log") -RedirectStandardError (Join-Path $T "logs\$label.err.log")
  if (-not (Wait-Until 60 { Test-Login })) { throw "${label}: no 200 on /login.html within 60 s" }
  Say "${label}: the server answers"
}
# Ends the server this script started and what it started (the gateway; for v1 the cmd.exe and its node).
function Stop-OwnServer {
  $p = $script:ServerProc
  $script:ServerProc = $null
  if ($p -and -not $p.HasExited) {
    $null = Invoke-Bounded "taskkill the server ($($p.Id))" 60 'taskkill.exe' @('/PID', "$($p.Id)", '/T', '/F')
  }
}
# Ends whatever still runs from the temp install, so nothing outlives this script: a program under $Root (the
# server, the gateway, v1's node) or one whose command line names the temp folder (the Startup launcher's wscript.exe
# and its cmd.exe). At most 20 s.
function Stop-TempLeftovers {
  $under = $Root.TrimEnd('\') + '\'
  $procs = @(Get-CimInstance Win32_Process -OperationTimeoutSec 30 | Where-Object {
      $_.ProcessId -ne $PID -and (
        ($_.ExecutablePath -and $_.ExecutablePath.StartsWith($under, [StringComparison]::OrdinalIgnoreCase)) -or
        ($_.CommandLine -and $_.CommandLine.IndexOf($T, [StringComparison]::OrdinalIgnoreCase) -ge 0))
    })
  if ($procs.Count -eq 0) { return }
  Write-Host "Ending what still runs from the temp install: $(($procs | ForEach-Object { "$($_.Name) ($($_.ProcessId))" }) -join ', ')"
  foreach ($p in $procs) { try { Invoke-CimMethod -InputObject $p -MethodName Terminate -OperationTimeoutSec 10 | Out-Null } catch { } }
  $ids = @($procs | ForEach-Object { $_.ProcessId })
  if (-not (Wait-Until 20 { -not (Get-Process -Id $ids -ErrorAction SilentlyContinue) })) {
    Write-Warning "Still running 20 s after the stop: $($ids -join ', ')"
  }
}
function Wait-PortFree {
  if (-not (Wait-Until 20 { -not (Test-Listening) })) { throw "Port $Port still in use 20 s after the stop" }
}
function Stop-Server { Stop-OwnServer; Wait-PortFree }
function Write-Marker([string]$what) {
  $body = ConvertTo-Json @{ userName = $Marker }
  # A server just started can be busy for a while (its first scans run on the event loop): 60 s, tried twice.
  try { Invoke-RestMethod -Method Post -TimeoutSec 60 -Uri "$Url/api/config" -ContentType 'application/json' -Body $body | Out-Null }
  catch { Invoke-RestMethod -Method Post -TimeoutSec 60 -Uri "$Url/api/config" -ContentType 'application/json' -Body $body | Out-Null }
}
function Assert-Marker([string]$what) {
  $cfg = try { Invoke-RestMethod -TimeoutSec 60 -Uri "$Url/api/config" } catch { Invoke-RestMethod -TimeoutSec 60 -Uri "$Url/api/config" }
  $got = $cfg.userName
  if ($got -ne $Marker) { throw "${what}: userName is '$got', not the marker '$Marker'" }
}
function Assert-Front([string]$what) {
  $ready = Wait-Until 60 { $s = Get-GatewayStatus; $s.mode -eq 'front' -and $s.state -eq 'ready' }
  if (-not $ready) {
    $s = try { Get-GatewayStatus | ConvertTo-Json -Depth 6 -Compress } catch { $_.Exception.Message }
    throw "${what}: /api/gateway/status is not front and ready within 60 s: $s"
  }
  $gv = (Get-GatewayStatus).gwVersion
  if ($gv -ne $Version) { throw "${what}: gwVersion '$gv', package $Version" }
}
function Show-Logs {
  $files = @(Get-ChildItem -LiteralPath (Join-Path $T 'logs') -File -ErrorAction SilentlyContinue)
  $svcLog = Join-Path $Root 'logs\server.log'
  if (Test-Path -LiteralPath $svcLog) { $files += Get-Item -LiteralPath $svcLog }
  foreach ($f in $files) {
    Write-Host "--- last lines of $($f.Name)"
    Get-Content -LiteralPath $f.FullName -Tail 30 -ErrorAction SilentlyContinue | Out-Host
  }
}
# After a part, pass or fail: nothing of it may keep running.
function Complete-Part {
  try { Stop-OwnServer } catch { Write-Warning $_.Exception.Message }
  if ($script:ServiceInstalled) {
    $script:ServiceInstalled = $false
    try { Invoke-Installer (Join-Path $RepoRoot 'install.ps1') @('-Uninstall') } catch { Write-Warning $_.Exception.Message }
  }
  try { Wait-PortFree } catch { Write-Warning $_.Exception.Message }
}

# ---- 1. clean install ----
function Test-CleanInstall {
  $d = Join-Path $T 'data-clean'; Write-Config $d $true; $env:ZAAFPULSE_DATA = $d
  if ($ServiceTest) {
    $script:ServiceInstalled = $true
    Install-V2 @()
    $how = 'Startup launcher service'
  } else {
    Install-V2 @('-NoService')
    $how = 'background start'
  }
  Assert-Version $Version 'clean install'
  $r = Invoke-Bounded 'zaafpulse doctor' 120 $Exe @('doctor')
  if ($r.Code -ne 0) { throw "clean install: zaafpulse doctor failed (exit $($r.Code))" }
  if ($ServiceTest) {
    Say 'clean install: waiting at most 90 s for the service to answer /login.html'
    if (-not (Wait-Until 90 { Test-Login })) {
      $null = Invoke-Bounded 'zaafpulse service status' 60 $Exe @('service', 'status')
      throw 'clean install: the service does not answer /login.html within 90 s'
    }
    $null = Invoke-Bounded 'zaafpulse service status' 60 $Exe @('service', 'status')
  } else {
    Start-Server 'clean'
  }
  Assert-Front 'clean install'
  if ($ServiceTest) {
    $script:ServiceInstalled = $false
    Invoke-Installer (Join-Path $RepoRoot 'install.ps1') @('-Uninstall')
    Say "clean install: waiting at most 20 s for port $Port to be free"
    if (-not (Wait-Until 20 { -not (Test-Listening) })) { throw "clean install: port $Port still in use 20 s after -Uninstall" }
  } else {
    Stop-Server
    Invoke-Installer (Join-Path $RepoRoot 'install.ps1') @('-Uninstall', '-NoService')
  }
  if (Test-Path -LiteralPath $BinDir) { throw "clean install: $BinDir is still there after -Uninstall" }
  $script:PartLine = "clean install: PASS ($how; version $Version; doctor passes; gateway in front, ready, gwVersion $Version; uninstalled)"
}

# ---- 2. upgrade from v1.1.0 and roll back ----
function Test-Upgrade {
  if (-not (Get-Command node -ErrorAction SilentlyContinue)) { throw 'upgrade: the v1.1.0 installer needs Node 18+ on PATH' }
  $d = Join-Path $T 'data-upgrade'; Write-Config $d $false; $env:ZAAFPULSE_DATA = $d
  $v1 = Join-Path $T 'install-v1.ps1'
  Say "upgrade: downloading $V1Installer (at most 120 s)"
  Invoke-WebRequest -UseBasicParsing -TimeoutSec 120 -Uri $V1Installer -OutFile $v1
  # v1.1.0's installer (which cannot change) works in one setup only, the one a Windows user has to roll back with:
  # Windows PowerShell 5.1 and no node on PATH (it then downloads Node.js LTS into $Root\node).
  # - 5.1 with any node on PATH: its line 54, `& node -p 'process.versions.node.split(".")[0]' 2>$null`, has double
  #   quotes inside a native argument, which 5.1's legacy argument passing (its only mode) drops: node gets
  #   `split(.)`, a SyntaxError, and under $ErrorActionPreference = 'Stop' the redirected stderr ends the installer
  #   ("node.exe : [eval]:1", the second v2.0.0 dry run).
  # - PowerShell 7: Invoke-WebRequest's .Content is a byte array for SHA256SUMS.txt (GitHub serves release assets as
  #   application/octet-stream), so `.Content -split "`n"` finds no checksum line and line 103 throws "Could not get a
  #   verified package" (the third v2.0.0 dry run).
  # So it runs in -InstallerHost (5.1) with every folder that holds a node command (node.exe, or a .cmd, .bat or .ps1
  # shim) taken off its PATH; this script keeps its own PATH (Set-Front and M12 need node). Downloading and unpacking
  # Node.js takes a while in 5.1: 900 s.
  $noNode = (@($env:Path -split ';') | Where-Object {
      $dir = $_
      if (-not $dir) { return $false }
      try { -not @('node.exe', 'node.cmd', 'node.bat', 'node.ps1' | Where-Object { [IO.File]::Exists([IO.Path]::Combine($dir, $_)) }).Count }
      catch { $true }
    }) -join ';'
  Say "upgrade: v1.1.0's installer runs without node on its PATH ($(@($env:Path -split ';').Count - @($noNode -split ';').Count) folders taken off)"
  Invoke-Installer $v1 @('-NoService', '-Version', $V1Tag) 900 $InstallerHost @{ Path = $noNode }
  if ((Get-Launcher) -ne $V1Cmd) { throw 'upgrade: the zaafpulse command is not v1''s zaafpulse.cmd' }
  Start-Server 'v1-before'
  Write-Marker 'v1'
  Assert-Marker 'v1'
  Stop-Server

  Set-Front $d
  Install-V2 @('-NoService')
  $rb = Join-Path $Root 'rollback\VERSION'
  $saved = if (Test-Path -LiteralPath $rb) { "$(Get-Content -LiteralPath $rb | Select-Object -First 1)".Trim() } else { '' }
  if ($saved -ne $V1Tag) { throw "upgrade: rollback\VERSION is '$saved', not $V1Tag" }
  if ((Get-Launcher) -ne $Exe) { throw 'upgrade: the zaafpulse command is not v2''s zaafpulse.exe' }
  Assert-Version $Version 'upgrade'
  Start-Server 'v2'
  Assert-Front 'upgrade (v2)'
  Assert-Marker 'upgrade (v2)'
  Stop-Server

  Invoke-Installer (Join-Path $RepoRoot 'install.ps1') @('-Rollback', '-NoService')
  if ((Get-Launcher) -ne $V1Cmd) { throw 'rollback: the zaafpulse command is not v1''s zaafpulse.cmd again' }
  Assert-Version $V1Tag.TrimStart('v') 'rollback'
  Start-Server 'v1-after'
  Assert-Marker 'rollback (v1)'
  Stop-Server
  Invoke-Installer (Join-Path $RepoRoot 'install.ps1') @('-Uninstall', '-NoService')
  $script:PartLine = "upgrade: PASS ($V1Tag -> $Version`: marker kept, rollback\VERSION $V1Tag; -Rollback -> $V1Tag`: marker kept)"
}

# ---- 3. M12 ----
function Test-M12 {
  if (-not (Get-Command node -ErrorAction SilentlyContinue)) { throw 'M12: needs Node on PATH' }
  $x = Join-Path $T 'm12'
  Expand-Archive -LiteralPath $Archive -DestinationPath $x
  $node = (Get-Command node).Source
  $r = Invoke-Bounded 'node scripts\upgrade-check.js --v2' 600 $node @((Join-Path $RepoRoot 'scripts\upgrade-check.js'), '--v2', (Join-Path $x "$Top\zaafpulse.exe"))
  $code = $r.Code
  $line = "$(@($r.Out) | Select-Object -Last 1)"
  if ($code -ne 0) { throw "M12: upgrade-check.js failed (exit $code)" }
  $short = if ($line.Length -gt 300) { $line.Substring(0, 300) } else { $line }
  $script:PartLine = "M12: PASS ($short)"
}

$summary = New-Object System.Collections.Generic.List[string]
$failed = $false
function Invoke-Part([string]$label, [scriptblock]$body) {
  # After a timeout the job's time is better spent failing now than waiting out the next part's limits.
  if ($script:TimedOut) {
    $script:failed = $true
    $summary.Add("$label`: SKIPPED (an earlier step timed out)")
    return
  }
  Say "${Name}: $label"
  $script:PartLine = $null
  try {
    & $body
    if (-not $script:PartLine) { throw "$label ended without a result" }
    $summary.Add($script:PartLine)
  } catch {
    $script:failed = $true
    Write-Host "x $label failed: $($_.Exception.Message)" -ForegroundColor Red
    Show-Logs
    $summary.Add("$label`: FAIL ($($_.Exception.Message))")
  } finally {
    Complete-Part
    Remove-Item -Path (Join-Path $T 'logs\*') -Force -ErrorAction SilentlyContinue
  }
}

try {
  Say "install test of $Base (version $Version) on Windows, port $Port, temp $T, installers in $InstallerHost"
  Invoke-Part 'clean install' { Test-CleanInstall }
  if (-not $NoUpgrade) { Invoke-Part 'upgrade' { Test-Upgrade } } else { $summary.Add('upgrade: skipped (-NoUpgrade)') }
  if ($M12 -eq 'on') { Invoke-Part 'M12' { Test-M12 } } else { $summary.Add('M12: skipped (runs on linux-x64 and win-x64)') }
} finally {
  Complete-Part
  try { Stop-TempLeftovers } catch { Write-Warning $_.Exception.Message }
  if ($env:KEEP_TEMP -eq '1') { Write-Host "temp folder kept: $T" }
  else { Remove-Item -LiteralPath $T -Recurse -Force -ErrorAction SilentlyContinue }
}

Write-Host ''
foreach ($l in $summary) { Write-Host "$Name | $l" }
if ($env:GITHUB_STEP_SUMMARY) {
  $text = "### install test: $Name`n`n" + (($summary | ForEach-Object { "- $_" }) -join "`n") + "`n"
  [IO.File]::AppendAllText($env:GITHUB_STEP_SUMMARY, $text)
}
if ($failed) { exit 1 }
exit 0
