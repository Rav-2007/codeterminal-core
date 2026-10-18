# run-smoke.ps1 -Dist <dir> -Version <version>
#
# The install smoke test for Windows, the twin of run-smoke.sh. Against the
# release files in -Dist, served on 127.0.0.1 so the real installer runs
# unmodified, and in an isolated profile, it checks that:
#
#   0. an elevated PowerShell is refused (where this runs elevated, as CI does);
#   1. install.ps1, run by Windows PowerShell 5.1, installs, and all three
#      programs report -Version;
#   2. the code-search model downloads (real network, pinned checksums);
#   3. `mochiii-daemon index` indexes a small project -- the embedder helper and
#      onnxruntime really run here;
#   4. `mochiii --prompt` starts its own background service and answers through
#      a stand-in provider, and the project's code reached the request;
#   5. a tampered archive is refused and the installed version keeps working;
#   6. re-installing over a running service stops it and works;
#   7. `mochiii --stop` ends the service and its embedder helper, and
#      uninstalling removes the programs and keeps %USERPROFILE%\.mochiii.

param(
    [Parameter(Mandatory = $true)][string]$Dist,
    [Parameter(Mandatory = $true)][string]$Version
)

Set-StrictMode -Version 3
$ErrorActionPreference = "Stop"

$here = Split-Path -Parent $MyInvocation.MyCommand.Path
$installer = (Resolve-Path (Join-Path $here "..\install.ps1")).Path
$dist = (Resolve-Path $Dist).Path
$python = (Get-Command python -ErrorAction SilentlyContinue).Source
if (-not $python) { $python = (Get-Command python3).Source }

$work = Join-Path ([IO.Path]::GetTempPath()) ("msmk-" + [Guid]::NewGuid().ToString("N").Substring(0, 8))
New-Item -ItemType Directory -Path $work | Out-Null
$env:USERPROFILE = Join-Path $work "home"
$env:LOCALAPPDATA = Join-Path $work "local"
New-Item -ItemType Directory -Force -Path $env:USERPROFILE, $env:LOCALAPPDATA | Out-Null
$env:MOCHIII_NO_VSCODE = "1"
$env:MOCHIII_NO_MODIFY_PATH = "1"
Remove-Item Env:MOCHIII_API_KEY, Env:MOCHIII_API_BASE, Env:MOCHIII_ALLOW_ADMIN -ErrorAction SilentlyContinue

$bin = Join-Path $env:LOCALAPPDATA "Programs\Mochiii\bin"
$project = Join-Path $work "project"
$servers = @()

function Fail([string]$message) {
    Write-Host "SMOKE FAILED: $message"
    Cleanup
    exit 1
}
function Pass([string]$message) { Write-Host "ok - $message" }

function Cleanup {
    if (Test-Path (Join-Path $bin "mochiii.exe")) {
        & (Join-Path $bin "mochiii.exe") --workspace $project --stop *> $null
    }
    foreach ($s in $script:servers) { Stop-Process -Id $s.Id -Force -ErrorAction SilentlyContinue }
}

function Serve([string]$dir, [int]$port) {
    $p = Start-Process -FilePath $python -ArgumentList @("-m", "http.server", "$port", "--bind", "127.0.0.1", "--directory", $dir) `
        -PassThru -WindowStyle Hidden -RedirectStandardError (Join-Path $work "serve-$port.log")
    $script:servers += $p
    for ($i = 0; $i -lt 50; $i++) {
        try { Invoke-WebRequest -UseBasicParsing -Uri "http://127.0.0.1:$port/" -TimeoutSec 2 | Out-Null; return } catch { Start-Sleep -Milliseconds 200 }
    }
    Fail "the file server on port $port did not start"
}

# The long form of a path, as install.ps1 computes it. GitHub's runners give a
# short TEMP (C:\Users\RUNNER~1\...), so $bin is short here while the programs
# run under the long form: this run is the hard case for matching them.
function Get-LongPath([string]$path) {
    $full = [IO.Path]::GetFullPath($path)
    $long = [IO.Path]::GetPathRoot($full)
    foreach ($part in @($full.Substring($long.Length) -split '\\' | Where-Object { $_ })) {
        $entry = Get-ChildItem -LiteralPath $long -Filter $part -Force -ErrorAction SilentlyContinue | Select-Object -First 1
        $long = Join-Path $long $(if ($entry) { $entry.Name } else { $part })
    }
    return $long
}

# The processes of <name> started from the install, in either spelling of $bin.
function Get-Ours([string]$name) {
    $dirs = @($bin, (Get-LongPath $bin)) | ForEach-Object { $_.TrimEnd("\") + "\" }
    @(Get-Process -Name $name -ErrorAction SilentlyContinue | Where-Object {
            $path = $_.Path
            $path -and @($dirs | Where-Object { $path.StartsWith($_, [StringComparison]::OrdinalIgnoreCase) }).Count -gt 0
        })
}

# Runs install.ps1 the way a user does: in Windows PowerShell, a fresh process.
function Invoke-Installer([string]$base, [string]$log) {
    $env:MOCHIII_DOWNLOAD_BASE = $base
    & powershell.exe -NoProfile -ExecutionPolicy Bypass -File $installer *> $log
    return $LASTEXITCODE
}

Serve $dist 18471

# 0. an elevated PowerShell is refused unless MOCHIII_ALLOW_ADMIN says otherwise.
#    GitHub's Windows runners run elevated (an Administrator with UAC off), so
#    there the refusal is checked for real and the rest of the run sets the
#    override; elevated or not, everything after runs at one elevation.
$elevated = (New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())).IsInRole(
    [Security.Principal.WindowsBuiltInRole]::Administrator)
if ($elevated) {
    if ((Invoke-Installer "http://127.0.0.1:18471" (Join-Path $work "admin.log")) -eq 0) {
        Fail "the installer ran in an elevated PowerShell without MOCHIII_ALLOW_ADMIN"
    }
    if (-not (Select-String -Path (Join-Path $work "admin.log") -Pattern "not as Administrator" -Quiet)) {
        Fail ("the elevated install was refused, but not for being elevated:`n" + (Get-Content -Raw (Join-Path $work "admin.log")))
    }
    if (Test-Path $bin) { Fail "a refused elevated install left $bin behind" }
    Pass "an elevated PowerShell is refused without MOCHIII_ALLOW_ADMIN"
    $env:MOCHIII_ALLOW_ADMIN = "1"
} else {
    Write-Host "note - not elevated here, so the elevated-PowerShell refusal was not exercised"
}

# 1. install
if ((Invoke-Installer "http://127.0.0.1:18471" (Join-Path $work "install.log")) -ne 0) {
    Fail ("the installer failed:`n" + (Get-Content -Raw (Join-Path $work "install.log")))
}
foreach ($b in "mochiii", "mochiii-daemon", "mochiii-embedder-helper") {
    $got = (& (Join-Path $bin "$b.exe") --version | Out-String).Trim()
    if ($got -ne "$b $Version") { Fail "$b --version said '$got', want '$b $Version'" }
}
Pass "installed $Version; all three programs report it"

# 2. the model
$check = (& (Join-Path $bin "mochiii-daemon.exe") download-model --check 2>$null | Select-Object -Last 1)
if ("$check" -notmatch '"cached":true') { Fail "the model is not in place after install: $check" }
Pass "code-search model downloaded and verified"

# 3. index a project
New-Item -ItemType Directory -Path $project | Out-Null
Copy-Item (Join-Path $here "fixture\quokka.go") $project
& (Join-Path $bin "mochiii-daemon.exe") index $project *> (Join-Path $work "index.log")
if ($LASTEXITCODE -ne 0) { Fail ("indexing failed:`n" + (Get-Content -Raw (Join-Path $work "index.log"))) }
if (-not (Test-Path (Join-Path $project ".mochiii\index\embedder_stamp.json"))) { Fail "the index finished without its stamp" }
Pass "indexed a project: the embedder helper and onnxruntime run here"

# 4. the whole chain
$requests = Join-Path $work "requests.jsonl"
$stub = Start-Process -FilePath $python -ArgumentList @((Join-Path $here "stub_provider.py"), "18472", $requests) `
    -PassThru -WindowStyle Hidden -RedirectStandardError (Join-Path $work "stub.log")
$script:servers += $stub
Start-Sleep -Seconds 1
$env:MOCHIII_API_BASE = "http://127.0.0.1:18472/v1"
$out = (& (Join-Path $bin "mochiii.exe") --workspace $project --prompt "where is the burrow survey folded into one number?" 2> (Join-Path $work "client.err") | Out-String)
if ($out -notmatch "SMOKE-ANSWER-OK") { Fail ("the answer did not come back: $out`n" + (Get-Content -Raw (Join-Path $work "client.err"))) }
if (-not (Select-String -Path $requests -Pattern "quokkaChecksum" -Quiet)) { Fail "the project's code never reached the provider: code search did not run" }
Pass "mochiii started its own background service, searched the project and answered"

# 5. a tampered download is refused
$bad = Join-Path $work "bad"
New-Item -ItemType Directory -Path $bad | Out-Null
Copy-Item (Join-Path $dist "*") $bad
foreach ($zip in Get-ChildItem (Join-Path $bad "mochiii-*.zip")) { Add-Content -Path $zip.FullName -Value "x" -NoNewline }
Serve $bad 18473
$env:MOCHIII_NO_MODEL = "1"
if ((Invoke-Installer "http://127.0.0.1:18473" (Join-Path $work "bad.log")) -eq 0) { Fail "the installer accepted a tampered archive" }
if (-not (Select-String -Path (Join-Path $work "bad.log") -Pattern "Checksum mismatch" -Quiet)) {
    Fail ("the tampered archive was refused, but not for its checksum:`n" + (Get-Content -Raw (Join-Path $work "bad.log")))
}
if (((& (Join-Path $bin "mochiii.exe") --version | Out-String).Trim()) -ne "mochiii $Version") { Fail "a refused install broke the installed version" }
Pass "a tampered archive was refused and the installed version still runs"

# 6. re-install while the background service is running. The installer must stop
#    it: Windows will not rename a folder a running program was started from.
$before = @(Get-Ours "mochiii-daemon" | ForEach-Object { $_.Id })
if ($before.Count -eq 0) { Fail "no background service was running before re-installing, so this would prove nothing" }
if ((Invoke-Installer "http://127.0.0.1:18471" (Join-Path $work "reinstall.log")) -ne 0) {
    Fail ("re-installing over a running background service failed:`n" + (Get-Content -Raw (Join-Path $work "reinstall.log")))
}
$now = @(Get-Ours "mochiii-daemon" | ForEach-Object { $_.Id })
foreach ($id in $before) { if ($now -contains $id) { Fail "re-installing left the old background service (process $id) running" } }
$out = (& (Join-Path $bin "mochiii.exe") --workspace $project --prompt "again?" 2>$null | Out-String)
if ($out -notmatch "SMOKE-ANSWER-OK") { Fail "no answer after re-installing: $out" }
Pass "re-installed over a running background service: the old one was stopped, and the new one answers"

# 7. stop, then uninstall. Every stop here is TerminateProcess, which runs none
#    of the service's shutdown; its embedder helper must end with it anyway
#    (daemon/helperlifetime_windows.go), not linger until logoff.
if (@(Get-Ours "mochiii-embedder-helper").Count -eq 0) {
    Fail "no embedder helper was running before --stop, so the check after it would prove nothing"
}
& (Join-Path $bin "mochiii.exe") --workspace $project --stop *> $null
if ($LASTEXITCODE -ne 0) { Fail "mochiii --stop failed" }
$deadline = (Get-Date).AddSeconds(10)
while (@(Get-Ours "mochiii-embedder-helper").Count -gt 0) {
    if ((Get-Date) -gt $deadline) { Fail "mochiii --stop left the embedder helper running: an orphan" }
    Start-Sleep -Milliseconds 200
}
if (@(Get-Ours "mochiii-daemon").Count -gt 0) { Fail "mochiii --stop left the background service running" }
Pass "mochiii --stop ended the background service and its embedder helper"
$env:MOCHIII_UNINSTALL = "1"
if ((Invoke-Installer "http://127.0.0.1:18471" (Join-Path $work "uninstall.log")) -ne 0) { Fail "uninstall failed" }
Remove-Item Env:MOCHIII_UNINSTALL
if (Test-Path $bin) { Fail "uninstall left $bin" }
if (-not (Test-Path (Join-Path $env:USERPROFILE ".mochiii\models"))) { Fail "uninstall removed the downloaded models, which it must keep" }
Pass "uninstalled; models and settings kept"

Cleanup
Write-Host "SMOKE PASSED ($Version on Windows $env:PROCESSOR_ARCHITECTURE)"
exit 0
