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

# WHO HAS THESE FILES OPEN, from the Windows Restart Manager -- the system's own
# answer, which names services (Windows Defender, the x64 emulator's cache) as
# well as programs. Compiled here, once, so a lock that lasts only moments is
# still there when it is asked about. C# 5, which Windows PowerShell 5.1 compiles.
$rmSource = @'
using System;
using System.Collections.Generic;
using System.Runtime.InteropServices;
using System.Text;
namespace MochiiiSmoke {
    public static class FileHolders {
        [StructLayout(LayoutKind.Sequential)]
        struct RmUniqueProcess { public int ProcessId; public System.Runtime.InteropServices.ComTypes.FILETIME StartTime; }
        [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
        struct RmProcessInfo {
            public RmUniqueProcess Process;
            [MarshalAs(UnmanagedType.ByValTStr, SizeConst = 256)] public string AppName;
            [MarshalAs(UnmanagedType.ByValTStr, SizeConst = 64)] public string ServiceShortName;
            public int ApplicationType;
            public uint AppStatus;
            public uint TSSessionId;
            [MarshalAs(UnmanagedType.Bool)] public bool Restartable;
        }
        [DllImport("rstrtmgr.dll", CharSet = CharSet.Unicode)]
        static extern int RmStartSession(out uint session, int flags, StringBuilder key);
        [DllImport("rstrtmgr.dll")]
        static extern int RmEndSession(uint session);
        [DllImport("rstrtmgr.dll", CharSet = CharSet.Unicode)]
        static extern int RmRegisterResources(uint session, uint nFiles, string[] files, uint nApps, RmUniqueProcess[] apps, uint nServices, string[] services);
        [DllImport("rstrtmgr.dll")]
        static extern int RmGetList(uint session, out uint needed, ref uint count, [In, Out] RmProcessInfo[] infos, ref uint rebootReasons);

        // One entry per holder: "process id|name|service short name".
        public static string[] Of(string[] files) {
            uint session;
            int rc = RmStartSession(out session, 0, new StringBuilder(33));
            if (rc != 0) { throw new System.ComponentModel.Win32Exception(rc); }
            try {
                rc = RmRegisterResources(session, (uint)files.Length, files, 0, null, 0, null);
                if (rc != 0) { throw new System.ComponentModel.Win32Exception(rc); }
                for (int attempt = 0; attempt < 3; attempt++) {
                    uint needed, count = 0, reasons = 0;
                    rc = RmGetList(session, out needed, ref count, null, ref reasons);
                    if (rc == 0) { return new string[0]; }
                    if (rc != 234) { throw new System.ComponentModel.Win32Exception(rc); }
                    RmProcessInfo[] infos = new RmProcessInfo[needed];
                    count = needed;
                    rc = RmGetList(session, out needed, ref count, infos, ref reasons);
                    if (rc == 234) { continue; }
                    if (rc != 0) { throw new System.ComponentModel.Win32Exception(rc); }
                    List<string> result = new List<string>();
                    for (int i = 0; i < count; i++) {
                        result.Add(infos[i].Process.ProcessId + "|" + infos[i].AppName + "|" + infos[i].ServiceShortName);
                    }
                    return result.ToArray();
                }
                throw new InvalidOperationException("the list of holders kept changing");
            } finally {
                RmEndSession(session);
            }
        }
    }
}
'@
$rmReady = $false
try { Add-Type -TypeDefinition $rmSource -Language CSharp; $rmReady = $true } catch { Write-Host "note - the Restart Manager lookup did not compile: $($_.Exception.Message)" }

# The holders of every file in $bin, as "name (process N[, service S])".
function Get-FileHolders {
    if (-not $rmReady) { return @("(Restart Manager unavailable)") }
    $files = @(Get-ChildItem -LiteralPath $bin -File -ErrorAction SilentlyContinue | ForEach-Object { $_.FullName })
    if ($files.Count -eq 0) { return @() }
    $raw = try { [MochiiiSmoke.FileHolders]::Of($files) } catch { return @("(Restart Manager failed: $($_.Exception.Message))") }
    @($raw | ForEach-Object {
            $id, $app, $svc = $_ -split '\|', 3
            $image = try { (Get-Process -Id $id -ErrorAction Stop).ProcessName } catch { $app }
            if ($svc) { "$image (process $id, service $svc)" } else { "$image (process $id)" }
        })
}

# Stops this install's daemon and helper exactly as install.ps1's Stop-Ours does,
# then measures how long until $bin can be renamed -- the upgrade's own move --
# and records who held it at the first refusal.
function Measure-FolderRelease {
    $procs = @(Get-Ours "mochiii-daemon") + @(Get-Ours "mochiii-embedder-helper")
    $procs | Stop-Process -Force -ErrorAction SilentlyContinue
    $procs | Wait-Process -Timeout 10 -ErrorAction SilentlyContinue
    $probeLeaf = (Split-Path $bin -Leaf) + ".probe"
    $probe = Join-Path (Split-Path $bin -Parent) $probeLeaf
    $r = [ordered]@{ stopped = $procs.Count; ms = $null; failures = 0; error = $null; holders = @(); survivors = @() }
    $moved = $false
    $sw = [Diagnostics.Stopwatch]::StartNew()
    while ($sw.Elapsed.TotalSeconds -lt 60) {
        try {
            if (-not $moved) { Rename-Item -LiteralPath $bin -NewName $probeLeaf -ErrorAction Stop; $moved = $true }
            Rename-Item -LiteralPath $probe -NewName (Split-Path $bin -Leaf) -ErrorAction Stop
            $r.ms = [int]$sw.Elapsed.TotalMilliseconds
            break
        } catch {
            if ($r.failures -eq 0) {
                $r.error = $_.Exception.Message
                $r.holders = @(Get-FileHolders)
                $r.survivors = @(@(Get-Ours "mochiii-daemon") + @(Get-Ours "mochiii-embedder-helper") | ForEach-Object { "$($_.ProcessName) (process $($_.Id))" })
            }
            $r.failures++
            Start-Sleep -Milliseconds 100
        }
    }
    $r
}

function Format-Release($r) {
    $free = if ($null -ne $r.ms) { "free after $($r.ms) ms" } else { "NOT free after 60 s" }
    if ($r.failures -eq 0) { return "$free (stopped $($r.stopped) processes; the first rename succeeded)" }
    $held = if ($r.holders.Count) { $r.holders -join "; " } else { "no process, by Restart Manager" }
    $surv = if ($r.survivors.Count) { $r.survivors -join "; " } else { "none" }
    "$free ($($r.failures) renames refused first; stopped $($r.stopped) processes); first refusal: $($r.error) | held by: $held | our processes still running: $surv"
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

# LOCK PROBE (2026-10-19). What holds the install folder just after its programs
# are stopped, and for how long: on Windows on ARM an upgrade once could not
# rename it after stopping them, where x64 could. Measured as is, then with
# Windows Defender told to skip the folder, which separates Defender's part from
# the rest. The holder differs from run to run (Defender, the x64 emulator's
# cache, other system processes), so each run is a sample, not a verdict. A
# report for the record: it never fails the run.
$label = "LOCK PROBE ($env:PROCESSOR_ARCHITECTURE)"
function Write-Probe([string]$line) {
    Write-Host $line
    if ($env:GITHUB_STEP_SUMMARY) { Add-Content -Path $env:GITHUB_STEP_SUMMARY -Value "``$line``" }
}
try {
    Write-Probe "${label}: $(Format-Release (Measure-FolderRelease))"
} catch {
    Write-Probe "${label}: could not run: $($_.Exception.Message)"
}
try {
    $rt = $null
    if (Get-Command Get-MpComputerStatus -ErrorAction SilentlyContinue) {
        $rt = try { (Get-MpComputerStatus).RealTimeProtectionEnabled } catch { $null }
    }
    if ($rt) {
        # Excluded BEFORE the programs start, since Defender looks at a program as
        # it is launched; by the long path, which is the one Defender sees.
        $excluded = Get-LongPath $work
        Add-MpPreference -ExclusionPath $excluded
        try {
            $null = & (Join-Path $bin "mochiii.exe") --workspace $project --prompt "probe?" 2>$null
            Write-Probe "$label with Defender excluding the folder: $(Format-Release (Measure-FolderRelease))"
        } finally {
            Remove-MpPreference -ExclusionPath $excluded
        }
    } else {
        Write-Probe "$label`: Defender real-time protection is off or absent here ($rt), so it cannot be the holder"
    }
} catch {
    Write-Probe "$label with Defender excluding the folder: could not run: $($_.Exception.Message)"
}
# The probe stopped the background service; the steps below need one running.
$out = (& (Join-Path $bin "mochiii.exe") --workspace $project --prompt "after the probe?" 2>$null | Out-String)
if ($out -notmatch "SMOKE-ANSWER-OK") { Fail "no answer after the lock probe: $out" }

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

# Prints the installer's "waiting for ..." notes from a log, so a run records what
# an upgrade or uninstall waited on (see the lock probe above).
function Show-Waits([string]$log, [string]$what) {
    Select-String -Path $log -Pattern "waiting for" | ForEach-Object { Write-Host "note - ${what}: $($_.Line.Trim())" }
}

# Whether a log says $pattern. Windows PowerShell word-wraps an error message at
# its console width (the 1.1.0 rehearsal's logs show it), so line breaks and runs
# of spaces are collapsed before matching.
function Test-LogSays([string]$log, [string]$pattern) {
    ((Get-Content -Raw $log) -replace '\s+', ' ') -match $pattern
}

# The installer's staging and old-version folders (bin.new.N, bin.old.N) beside
# $bin. Matched with -like, not -Filter: a Windows filter "bin.*" also matches
# "bin" itself.
function Get-Staging {
    @(Get-ChildItem -LiteralPath (Split-Path $bin -Parent) -Directory -ErrorAction SilentlyContinue | Where-Object { $_.Name -like "bin.*" })
}

# 6a. an open mochiii -- here one waiting on a slow answer -- is not the
#     installer's to stop. The upgrade must stop before changing anything, and
#     name it.
$open = Start-Process -FilePath (Join-Path $bin "mochiii.exe") -ArgumentList @("--workspace", "`"$project`"", "--prompt", "SMOKE-SLOW") `
    -PassThru -WindowStyle Hidden -RedirectStandardOutput (Join-Path $work "open.out") -RedirectStandardError (Join-Path $work "open.err")
$script:servers += $open
$deadline = (Get-Date).AddSeconds(60)
while (-not (Select-String -Path $requests -Pattern "SMOKE-SLOW" -Quiet)) {
    if ((Get-Date) -gt $deadline) { Fail "the slow question never reached the stand-in provider" }
    Start-Sleep -Milliseconds 200
}
$daemonsBefore = @(Get-Ours "mochiii-daemon" | ForEach-Object { $_.Id })
$code = Invoke-Installer "http://127.0.0.1:18471" (Join-Path $work "open.log")
Stop-Process -Id $open.Id -Force -ErrorAction SilentlyContinue
if ($code -eq 0) { Fail "the installer upgraded while a mochiii was open" }
if (-not (Test-LogSays (Join-Path $work "open.log") "Close mochiii \(process $($open.Id)\)")) {
    Fail ("the upgrade was refused, but not by naming the open mochiii (process $($open.Id)):`n" + (Get-Content -Raw (Join-Path $work "open.log")))
}
foreach ($id in $daemonsBefore) {
    if (@(Get-Ours "mochiii-daemon" | Where-Object { $_.Id -eq $id }).Count -eq 0) { Fail "the refused upgrade stopped the background service (process $id) anyway" }
}
if (@(Get-Staging).Count -gt 0) { Fail "the refused upgrade left a staging folder behind" }
Pass "an open mochiii stopped the upgrade by name, and nothing was changed"

# 6b. a file held by another program -- this test, opening one without letting
#     it be moved, as a security scanner does. The upgrade must wait, say what
#     for, give up after its limit naming the holder, and leave the installed
#     version in place.
$held = [IO.File]::Open((Join-Path $bin "models.json"), [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)
try {
    $code = Invoke-Installer "http://127.0.0.1:18471" (Join-Path $work "held.log")
} finally {
    $held.Dispose()
}
$heldLog = Join-Path $work "held.log"
if ($code -eq 0) { Fail "the installer upgraded while a file in the install was held open" }
foreach ($want in "waiting for pwsh \(process $PID\)", "still in use after 30 seconds, by pwsh \(process $PID\)") {
    if (-not (Test-LogSays $heldLog $want)) {
        Fail ("the held file was not reported as '$want':`n" + (Get-Content -Raw $heldLog))
    }
}
if (((& (Join-Path $bin "mochiii.exe") --version | Out-String).Trim()) -ne "mochiii $Version") { Fail "the refused upgrade broke the installed version" }
if (@(Get-Staging).Count -gt 0) { Fail "the refused upgrade left a staging folder behind" }
Pass "a held file was waited on, named, and the installed version kept"
# 6b stopped the background service before it found the file held; step 6 needs one.
$out = (& (Join-Path $bin "mochiii.exe") --workspace $project --prompt "before the upgrade?" 2>$null | Out-String)
if ($out -notmatch "SMOKE-ANSWER-OK") { Fail "no answer before the upgrade: $out" }

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
Show-Waits (Join-Path $work "reinstall.log") "upgrade"
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
Show-Waits (Join-Path $work "uninstall.log") "uninstall"
if (Test-Path $bin) { Fail "uninstall left $bin" }
if (@(Get-Staging).Count -gt 0) { Fail "uninstall left an old version's folder behind" }
if (-not (Test-Path (Join-Path $env:USERPROFILE ".mochiii\models"))) { Fail "uninstall removed the downloaded models, which it must keep" }
Pass "uninstalled; models and settings kept"

Cleanup
Write-Host "SMOKE PASSED ($Version on Windows $env:PROCESSOR_ARCHITECTURE)"
exit 0
