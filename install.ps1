# ZAAFPulse installer for Windows 10 and 11, x64 and arm64 (PowerShell 5.1 or 7). One command in PowerShell:
#
#   irm https://github.com/rifqifitriady/ZAAFPulse-deno-go/releases/latest/download/install.ps1 | iex
#
# With options (server mode, another port, no background task, a local package, a rollback):
#
#   & ([scriptblock]::Create((irm https://github.com/rifqifitriady/ZAAFPulse-deno-go/releases/latest/download/install.ps1))) -Server -Port 4777
#
# Options: -Server  -HostAddress ADDR  -Port N  -Version TAG  -System  -NoService
#          -Archive FILE [-Sums FILE] (a local package, offline)  -VerifyAttestation (needs gh)
#          -Rollback (back to the install this one replaced)  -Uninstall [-Purge]
# What it does: downloads the release package for this system (checksum verified) and puts zaafpulse.exe and
# zaafpulse-gw.exe in %LOCALAPPDATA%\ZAAFPulse\bin; adds that folder to your user PATH; starts it now and at
# every logon (a hidden launcher in your Startup folder) and adds a Start menu shortcut. An earlier install
# (v1.x or v2) is kept for -Rollback. No administrator rights needed, except -System (a Task Scheduler task at
# boot, as SYSTEM, for a server that runs without anyone logged on). 32-bit Windows: use the v1.x installer.
param(
  [switch]$Server,          # listen on the network (0.0.0.0); asks for the dashboard admin password
  [string]$HostAddress = '',
  [int]$Port = 0,
  [string]$Version = '',    # a release tag (vX.Y.Z); the latest when empty
  [string]$Archive = '',    # install a local package (zaafpulse-<v>-win-<arch>.zip) instead of downloading
  [string]$Sums = '',       # the SHA256SUMS.txt that lists -Archive
  [switch]$VerifyAttestation,
  [switch]$Rollback,
  [switch]$System,          # run at boot as SYSTEM, without a logged-on user (elevated PowerShell)
  [switch]$NoService,
  [switch]$Uninstall,
  [switch]$Purge
)
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'   # Invoke-WebRequest is far faster without the progress bar
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

$Repo = 'rifqifitriady/ZAAFPulse-deno-go'
$V1Install = 'irm https://raw.githubusercontent.com/rifqifitriady/ZAAFPulse/main/install.ps1 | iex'
# One string for the install folder, used both to run the binary and on PATH: Deno matches the gateway's folder on
# PATH against the binary's own path case-sensitively, so the two must be spelled the same.
$Root = Join-Path $env:LOCALAPPDATA 'ZAAFPulse'
$BinDir = Join-Path $Root 'bin'
$Exe = Join-Path $BinDir 'zaafpulse.exe'
$OldCmd = Join-Path $BinDir 'zaafpulse.cmd'          # the v1 launcher
$Opener = Join-Path $BinDir 'zaafpulse-open.vbs'
$RollbackDir = Join-Path $Root 'rollback'
$RollforwardDir = Join-Path $Root 'rollforward'
# What an install keeps in bin\: v2's binaries, v1's launcher, and the Start menu opener of either.
$BinFiles = @('zaafpulse.exe', 'zaafpulse-gw.exe', 'zaafpulse.cmd', 'zaafpulse-open.vbs', 'LICENSE')
function Say($m) { Write-Host "==> $m" -ForegroundColor Cyan }
function Warn($m) { Write-Host "!  $m" -ForegroundColor Yellow }

# The command that runs the current install: v2's zaafpulse.exe, else v1's zaafpulse.cmd, else none.
function Get-Launcher {
  if (Test-Path -LiteralPath $Exe) { return $Exe }
  if (Test-Path -LiteralPath $OldCmd) { return $OldCmd }
  return $null
}
# Stops the service through a launcher, then ends what still runs from this install (Stop-Leftovers). An old
# launcher that no longer runs is only a warning.
function Stop-ZaafService([string]$launcher) {
  if ($NoService) { return }
  if ($launcher) {
    $svcArgs = @('service', 'uninstall'); if ($System) { $svcArgs += '--system' }
    try {
      & $launcher @svcArgs | Out-Host
      if ($LASTEXITCODE -ne 0) { Warn "Could not stop the running service through $launcher (exit $LASTEXITCODE)" }
    } catch { Warn "Could not stop the running service through $launcher`: $($_.Exception.Message)" }
  }
  Stop-Leftovers
}
# Ends the processes still running from this install, and no others: an executable under $Root, or v1's node running
# $Root\app\bin\zaafpulse.js. Waits up to 10 s for them to exit, since a running .exe cannot be moved or deleted.
function Stop-Leftovers {
  $under = $Root.TrimEnd('\') + '\'
  $v1Js = Join-Path $Root 'app\bin\zaafpulse.js'
  $procs = @(Get-CimInstance Win32_Process | Where-Object {
      $_.ProcessId -ne $PID -and (
        ($_.ExecutablePath -and $_.ExecutablePath.StartsWith($under, [StringComparison]::OrdinalIgnoreCase)) -or
        ($_.CommandLine -and $_.CommandLine.IndexOf($v1Js, [StringComparison]::OrdinalIgnoreCase) -ge 0))
    })
  if ($procs.Count -eq 0) { return }
  foreach ($p in $procs) {
    try { Invoke-CimMethod -InputObject $p -MethodName Terminate | Out-Null } catch { }
  }
  $ids = @($procs | ForEach-Object { $_.ProcessId })
  $deadline = (Get-Date).AddSeconds(10)
  while (Get-Process -Id $ids -ErrorAction SilentlyContinue) {
    if ((Get-Date) -ge $deadline) { Warn "ZAAFPulse processes still running from $Root after 10 s (ids $($ids -join ', '))"; return }
    Start-Sleep -Milliseconds 250
  }
}
function Start-ZaafService([string]$launcher) {
  if ($NoService -or -not $launcher) { return }
  $svcArgs = @('service', 'install'); if ($System) { $svcArgs += '--system' }
  try {
    & $launcher @svcArgs | Out-Host
    if ($LASTEXITCODE -ne 0) { Warn 'Not started in the background; start it yourself with: zaafpulse start' }
  } catch { Warn "Not started in the background ($($_.Exception.Message)); start it yourself with: zaafpulse start" }
}
# Move the folders app and node, and the files of bin\, from one generation folder to another. A folder already in
# the destination stays (an interrupted move is finished, not nested).
function Move-Generation([string]$from, [string]$to) {
  $fromBin = if ($from -eq $Root) { $BinDir } else { Join-Path $from 'bin' }
  $toBin = if ($to -eq $Root) { $BinDir } else { Join-Path $to 'bin' }
  foreach ($d in 'app', 'node') {
    $p = Join-Path $from $d
    $dest = Join-Path $to $d
    if ((Test-Path -LiteralPath $p) -and -not (Test-Path -LiteralPath $dest)) { Move-Item -LiteralPath $p -Destination $dest }
  }
  foreach ($f in $BinFiles) {
    $p = Join-Path $fromBin $f
    if (Test-Path -LiteralPath $p) {
      New-Item -ItemType Directory -Force -Path $toBin | Out-Null
      Move-Item -LiteralPath $p -Destination (Join-Path $toBin $f) -Force
    }
  }
}
# The user PATH holds $BinDir exactly once, spelled as $BinDir; an entry that differs only in letter case (or a
# trailing \) is replaced in place. Returns $true when the PATH changed.
function Set-UserPath {
  $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
  $parts = @(); if ($userPath) { $parts = @($userPath -split ';' | Where-Object { $_ }) }
  $out = New-Object System.Collections.Generic.List[string]
  $placed = $false
  foreach ($e in $parts) {
    if ($e.TrimEnd('\') -ieq $BinDir.TrimEnd('\')) {
      if (-not $placed) { $out.Add($BinDir); $placed = $true }
    } else { $out.Add($e) }
  }
  if (-not $placed) { $out.Add($BinDir) }
  $new = $out -join ';'
  if ($new -ceq $userPath) { return $false }
  [Environment]::SetEnvironmentVariable('Path', $new, 'User')
  return $true
}
function Remove-UserPath {
  $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
  if (-not $userPath) { return }
  $kept = @($userPath -split ';' | Where-Object { $_ -and ($_.TrimEnd('\') -ine $BinDir.TrimEnd('\')) })
  [Environment]::SetEnvironmentVariable('Path', ($kept -join ';'), 'User')
}

if ($Uninstall) {
  Stop-ZaafService (Get-Launcher)
  foreach ($d in 'app', 'node', 'bin', 'bin.new', 'rollback', 'rollback.new', 'rollforward') {
    $p = Join-Path $Root $d
    if (Test-Path -LiteralPath $p) { Remove-Item -LiteralPath $p -Recurse -Force }
  }
  Remove-UserPath
  foreach ($f in 'ZAAFPulse.lnk', 'ZAAFPulse.url') {
    $p = Join-Path ([Environment]::GetFolderPath('Programs')) $f
    if (Test-Path -LiteralPath $p) { Remove-Item -LiteralPath $p -Force }
  }
  Say 'ZAAFPulse removed.'
  if ($Purge) {
    foreach ($p in (Join-Path $env:APPDATA 'ZAAFPulse'), $Root) {
      if (Test-Path -LiteralPath $p) { Remove-Item -LiteralPath $p -Recurse -Force }
    }
    Say 'Data removed too.'
  } else { Say "Your data (config, keys, history) in $env:APPDATA\ZAAFPulse was kept. Add -Purge to delete it." }
  return
}

if ($Rollback) {
  $versionFile = Join-Path $RollbackDir 'VERSION'
  if (-not (Test-Path -LiteralPath $versionFile)) { throw "Nothing to roll back to: $RollbackDir holds no saved install" }
  Stop-ZaafService (Get-Launcher)
  if (Test-Path -LiteralPath $RollforwardDir) { Remove-Item -LiteralPath $RollforwardDir -Recurse -Force }
  New-Item -ItemType Directory -Path $RollforwardDir | Out-Null
  Move-Generation $Root $RollforwardDir
  Move-Generation $RollbackDir $Root
  $old = (Get-Content -LiteralPath $versionFile | Select-Object -First 1)
  Remove-Item -LiteralPath $RollbackDir -Recurse -Force
  $env:Path = "$BinDir;$env:Path"
  Start-ZaafService (Get-Launcher)
  Say "Rolled back: ZAAFPulse $old is active again. The install it replaced is in $RollforwardDir."
  return
}

# ---- platform: the v2 builds ----
if (-not [Environment]::Is64BitOperatingSystem) {
  throw "No v2 build for 32-bit Windows; the v1.x installer still supports it: $V1Install"
}
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64' -or $env:PROCESSOR_ARCHITEW6432 -eq 'ARM64') { 'arm64' } else { 'x64' }

$tmp = Join-Path ([IO.Path]::GetTempPath()) ('zp-' + [guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
  # ---- the package: a local one, or the release's (checksum verified) ----
  $sumsFile = Join-Path $tmp 'SHA256SUMS.txt'
  if ($Archive) {
    if (-not (Test-Path -LiteralPath $Archive -PathType Leaf)) { throw "No such archive: $Archive" }
    $name = Split-Path -Leaf (Resolve-Path -LiteralPath $Archive).Path
    # A copy: the checksum and the unpack read the same bytes, and Expand-Archive (5.1) needs a .zip name.
    $pkg = Join-Path $tmp 'pkg.zip'
    Copy-Item -LiteralPath $Archive -Destination $pkg
    if ($Sums) { Copy-Item -LiteralPath $Sums -Destination $sumsFile }
    else { Warn "$name is not checksum-verified (local archive)" }
  } else {
    if (-not $Version) {
      try { $Version = (Invoke-RestMethod "https://api.github.com/repos/$Repo/releases/latest").tag_name }
      catch { throw 'Could not find the latest release on GitHub; try again, or name one with -Version vX.Y.Z' }
    }
    if ($Version -in @('main', 'master')) { throw 'v2 installs releases only; name one with -Version vX.Y.Z' }
    $name = "zaafpulse-$($Version.TrimStart('v'))-win-$arch.zip"
    $rel = "https://github.com/$Repo/releases/download/$Version"
    Say "Downloading ZAAFPulse $Version (win-$arch)"
    try { Invoke-WebRequest -UseBasicParsing "$rel/SHA256SUMS.txt" -OutFile $sumsFile }
    catch { throw "Could not get SHA256SUMS.txt for $Version; nothing was installed" }
    $pkg = Join-Path $tmp $name
    try { Invoke-WebRequest -UseBasicParsing "$rel/$name" -OutFile $pkg }
    catch { throw "Could not download $name; nothing was installed" }
  }
  if (Test-Path -LiteralPath $sumsFile) {
    $pattern = '^([0-9a-fA-F]{64})  ' + [regex]::Escape($name) + '$'
    $line = Get-Content -LiteralPath $sumsFile | ForEach-Object { $_.TrimEnd() } | Where-Object { $_ -match $pattern } | Select-Object -First 1
    if (-not $line) { throw "No checksum for $name in SHA256SUMS.txt; nothing was installed" }
    $want = ($line -split '\s+')[0].ToLower()
    if ((Get-FileHash -LiteralPath $pkg -Algorithm SHA256).Hash.ToLower() -ne $want) {
      throw "Checksum mismatch for $name`: the download was changed or damaged; nothing was installed"
    }
    Say "Checksum verified ($name)"
  }
  if ($VerifyAttestation) {
    $gh = Get-Command gh -ErrorAction SilentlyContinue
    if (-not $gh) { throw '-VerifyAttestation needs the GitHub CLI (gh); nothing was installed' }
    & $gh.Source attestation verify $pkg --repo $Repo | Out-Host
    if ($LASTEXITCODE -ne 0) { throw "The attestation check failed for $name; nothing was installed" }
    Say "Attestation verified ($name)"
  }

  $x = Join-Path $tmp 'x'
  Expand-Archive -LiteralPath $pkg -DestinationPath $x
  $src = Get-ChildItem -LiteralPath $x -Directory | Select-Object -First 1
  if (-not $src) { throw "$name holds no package folder; nothing was installed" }
  foreach ($f in 'zaafpulse.exe', 'zaafpulse-gw.exe') {
    $p = Join-Path $src.FullName $f
    if (-not (Test-Path -LiteralPath $p -PathType Leaf)) { throw "$name holds no $f; nothing was installed" }
    Unblock-File -LiteralPath $p   # a browser download marks the files as from the internet
  }

  # ---- stage the new binaries next to the install ----
  $newRoot = -not (Test-Path -LiteralPath $Root)
  New-Item -ItemType Directory -Force -Path $Root | Out-Null
  $staged = Join-Path $Root 'bin.new'
  if (Test-Path -LiteralPath $staged) { Remove-Item -LiteralPath $staged -Recurse -Force }
  New-Item -ItemType Directory -Path $staged | Out-Null
  foreach ($f in 'zaafpulse.exe', 'zaafpulse-gw.exe', 'LICENSE') {
    $p = Join-Path $src.FullName $f
    if (Test-Path -LiteralPath $p) { Copy-Item -LiteralPath $p -Destination (Join-Path $staged $f) }
  }
  # Before anything is changed, the new binary must run on this system.
  $runs = $false
  try {
    $newOut = & (Join-Path $staged 'zaafpulse.exe') version
    $runs = ($LASTEXITCODE -eq 0)
  } catch { $runs = $false }
  if (-not $runs) {
    Remove-Item -LiteralPath $staged -Recurse -Force -ErrorAction SilentlyContinue
    if ($newRoot) { Remove-Item -LiteralPath $Root -Force -ErrorAction SilentlyContinue }
    throw 'The new zaafpulse does not run on this system; nothing was changed'
  }
  Say "New version runs: $(@($newOut) | Select-Object -First 1)"
} finally {
  Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
}

# An upgrade cut off while it saved the old install leaves rollback.new. With its VERSION written and no install left
# in place, it is the whole old install: finish the move and keep it as the rollback. Otherwise put back what it holds.
$rbNew = Join-Path $Root 'rollback.new'
if (Test-Path -LiteralPath $rbNew) {
  $noInstall = -not (Test-Path -LiteralPath (Join-Path $Root 'app\bin\zaafpulse.js')) -and -not (Test-Path -LiteralPath $Exe)
  if ((Test-Path -LiteralPath (Join-Path $rbNew 'VERSION')) -and $noInstall) {
    Move-Generation $Root $rbNew
    if (Test-Path -LiteralPath $RollbackDir) { Remove-Item -LiteralPath $RollbackDir -Recurse -Force }
    Move-Item -LiteralPath $rbNew -Destination $RollbackDir
    Warn "An earlier upgrade was cut off; the install it saved is in $RollbackDir."
  } else {
    Move-Generation $rbNew $Root
    Remove-Item -LiteralPath $rbNew -Recurse -Force
  }
}

# ---- the current install: v1 (app\, run by node) or v2 (bin\zaafpulse.exe) ----
$oldKind = $null; $oldVersion = $null
if (Test-Path -LiteralPath (Join-Path $Root 'app\bin\zaafpulse.js')) {
  $oldKind = 'v1'
  try { $oldVersion = (Get-Content -Raw -LiteralPath (Join-Path $Root 'app\package.json') | ConvertFrom-Json).version } catch { $oldVersion = $null }
} elseif (Test-Path -LiteralPath $Exe) {
  $oldKind = 'v2'
  try {
    $out = & $Exe version
    if ($LASTEXITCODE -eq 0) { $oldVersion = (@($out) | Select-Object -First 1) }
  } catch { $oldVersion = $null }
}
$oldTag = if ("$oldVersion" -match '^\d+\.\d+') { "v$oldVersion" } else { 'unknown' }

if ($oldKind) {
  # Stop the old service through the old launcher: it must not run while its files move, and a running .exe
  # cannot be replaced. A v1 app wins over a leftover zaafpulse.exe (the v1 installer run after a v2 one).
  $oldLauncher = if ($oldKind -eq 'v1' -and (Test-Path -LiteralPath $OldCmd)) { $OldCmd } else { Get-Launcher }
  Stop-ZaafService $oldLauncher
  # One generation for -Rollback: v1's app, node and launcher, or v2's binaries, with the Start menu opener.
  # VERSION first: a rollback.new with it is known to hold the old install (see above).
  New-Item -ItemType Directory -Path $rbNew | Out-Null
  Set-Content -LiteralPath (Join-Path $rbNew 'VERSION') -Encoding ASCII -Value $oldTag
  try {
    Move-Generation $Root $rbNew
    if (Test-Path -LiteralPath $RollbackDir) { Remove-Item -LiteralPath $RollbackDir -Recurse -Force }
    Move-Item -LiteralPath $rbNew -Destination $RollbackDir
  } catch {
    $why = $_.Exception.Message
    # Put back what was moved, and start the old install again.
    try {
      Move-Generation $rbNew $Root
      Remove-Item -LiteralPath $rbNew -Recurse -Force
    } catch { Warn "Could not put everything back from $rbNew`: $($_.Exception.Message)" }
    Remove-Item -LiteralPath $staged -Recurse -Force -ErrorAction SilentlyContinue
    Start-ZaafService $oldLauncher
    throw "Could not save the old install ($why); it was put back and nothing was installed"
  }
  Say "The $oldKind install ($oldTag) was saved in $RollbackDir`: -Rollback brings it back."
}
New-Item -ItemType Directory -Force -Path $BinDir | Out-Null
foreach ($f in Get-ChildItem -LiteralPath $staged -File) {
  Move-Item -LiteralPath $f.FullName -Destination (Join-Path $BinDir $f.Name) -Force
}
Remove-Item -LiteralPath $staged -Recurse -Force
# A v1 launcher left behind would shadow nothing (.exe comes first in PATHEXT), but it runs the old app.
if (Test-Path -LiteralPath $OldCmd) { Remove-Item -LiteralPath $OldCmd -Force }

# ---- the zaafpulse command, on the user PATH (spelled as $BinDir), and first on this session's PATH ----
if (Set-UserPath) { Say 'Added the zaafpulse command to your PATH (open a new terminal to use it).' }
$env:Path = "$BinDir;$env:Path"

# ---- server mode: an admin password before the first start ----
if ($Server -and -not $HostAddress) { $HostAddress = '0.0.0.0' }
if ($HostAddress) { $env:ZAAFPULSE_HOST = $HostAddress }
if ($Port) { $env:ZAAFPULSE_PORT = "$Port" }
$serverMode = $HostAddress -and $HostAddress -notin @('127.0.0.1', 'localhost')
if ($serverMode) {
  if ($env:ZAAFPULSE_ADMIN_PASSWORD) { $env:ZAAFPULSE_ADMIN_PASSWORD | & $Exe set-password | Out-Host }
  else {
    Say 'Server mode: choose the dashboard admin password (10+ characters)'
    $s = Read-Host -AsSecureString 'Admin password'
    $plain = [Runtime.InteropServices.Marshal]::PtrToStringBSTR([Runtime.InteropServices.Marshal]::SecureStringToBSTR($s))
    $plain | & $Exe set-password | Out-Host
    $plain = $null
  }
  if ($LASTEXITCODE -ne 0) { Warn 'The admin password was not set: run `zaafpulse set-password`, then `zaafpulse service restart`.' }
}

# ---- background launcher (written by `zaafpulse service install`) and Start menu shortcut ----
Start-ZaafService $Exe
$p = if ($Port) { $Port } else { 4777 }
# The shortcut runs `zaafpulse open`, which signs the browser in with a fresh one-time code, so it still works
# after the 30-day local sign-in cookie expires (a link to the URL would land on the sign-in page). It points at
# wscript.exe and a hidden launcher in $BinDir, so no console window flashes. The launcher is UTF-16 (Unicode),
# which Windows Script Host reads, so a user folder with non-ASCII letters still works.
$programs = [Environment]::GetFolderPath('Programs')
$oldUrl = Join-Path $programs 'ZAAFPulse.url'   # earlier installs linked the URL
if (Test-Path -LiteralPath $oldUrl) { Remove-Item -LiteralPath $oldUrl -Force }
$run = "`"$Exe`" open --port $p" -replace '"', '""'
Set-Content -LiteralPath $Opener -Encoding Unicode -Value "' ZAAFPulse: opens the dashboard signed in (zaafpulse open), without a console window.`r`nCreateObject(`"WScript.Shell`").Run `"$run`", 0, False"
$lnk = (New-Object -ComObject WScript.Shell).CreateShortcut((Join-Path $programs 'ZAAFPulse.lnk'))
$lnk.TargetPath = Join-Path $env:SystemRoot 'System32\wscript.exe'
$lnk.Arguments = "//B //NoLogo `"$Opener`""
$lnk.Description = 'Open the ZAAFPulse dashboard'
$lnk.Save()

$newVersion = & $Exe version
if ($LASTEXITCODE -ne 0) { throw "$Exe does not run (exit $LASTEXITCODE)" }
# A rollback's leftover: this install replaces it.
if (Test-Path -LiteralPath $RollforwardDir) { Remove-Item -LiteralPath $RollforwardDir -Recurse -Force }
Write-Host ''
Say "ZAAFPulse $(@($newVersion) | Select-Object -First 1) is installed."
if ($serverMode) {
  Write-Host "    Dashboard:  http://$($env:COMPUTERNAME):$p   (sign in with the admin password)"
  Write-Host "    Firewall:   New-NetFirewallRule -DisplayName ZAAFPulse -Direction Inbound -Protocol TCP -LocalPort $p -Action Allow   (elevated)"
  Write-Host '    Use HTTPS in production: a reverse proxy, or ZAAFPULSE_TLS_CERT and ZAAFPULSE_TLS_KEY.'
} else { Write-Host "    Dashboard:  http://localhost:$p   (also in the Start menu)" }
Write-Host '    Commands:   zaafpulse status | doctor | open | service restart | help'
$self = "https://github.com/$Repo/releases/latest/download/install.ps1"
if ($oldKind) { Write-Host "    Roll back:  & ([scriptblock]::Create((irm $self))) -Rollback" }
Write-Host "    Uninstall:  & ([scriptblock]::Create((irm $self))) -Uninstall"
