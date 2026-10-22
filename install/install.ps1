# Mochiii installer for Windows.
#
#   powershell -ExecutionPolicy ByPass -c "irm https://mochiii.in/install.ps1 | iex"
#
# mochiii.in/install.ps1 redirects to this file in the latest GitHub release:
#   https://github.com/Rav-2007/codeterminal-core/releases/latest/download/install.ps1
#
# `irm | iex` cannot pass parameters, so options come from environment variables:
#
#   $env:MOCHIII_VERSION = "1.1.0"      install that release instead of the latest
#   $env:MOCHIII_NO_MODEL = "1"         skip the code-search model download
#   $env:MOCHIII_NO_MODIFY_PATH = "1"   do not add Mochiii to your PATH
#   $env:MOCHIII_UNINSTALL = "1"        remove Mochiii; keeps %USERPROFILE%\.mochiii (keys, models, chats)
#   $env:MOCHIII_PURGE = "1"            with uninstall, remove %USERPROFILE%\.mochiii as well
#   $env:MOCHIII_ALLOW_ADMIN = "1"      install even from an elevated (Administrator) PowerShell
#
# A variable counts only when it is exactly "1". A saved copy takes the same as
# parameters: .\install.ps1 -Version 1.1.0 -NoModel -AllowAdmin
#
# What it does: downloads the Windows build and its checksum from the GitHub
# release and refuses to go on unless they match; unpacks it, checks it runs, and
# swaps it in for any previous version; adds it to your user PATH; downloads the
# code-search model. It never needs Administrator rights, and everything goes
# under your own profile.
#
# It installs the terminal app only. Until 1.2.1 it also installed the VS Code
# extension when it found VS Code; that comes from the Marketplace now, and
# 1.1's -NoVSCode (MOCHIII_NO_VSCODE) is still accepted and does nothing.
#
# Windows on ARM gets the x64 build, which Windows runs under its built-in
# emulation.

param(
    [string]$Version = $env:MOCHIII_VERSION,
    [switch]$NoModel = ($env:MOCHIII_NO_MODEL -eq "1"),
    [switch]$NoVSCode, # 1.1's option: the installer no longer touches VS Code
    [switch]$NoModifyPath = ($env:MOCHIII_NO_MODIFY_PATH -eq "1"),
    [switch]$Uninstall = ($env:MOCHIII_UNINSTALL -eq "1"),
    [switch]$Purge = ($env:MOCHIII_PURGE -eq "1"),
    [switch]$AllowAdmin = ($env:MOCHIII_ALLOW_ADMIN -eq "1")
)

function Install-Mochiii {
    Set-StrictMode -Version 3
    $ErrorActionPreference = "Stop"
    # The progress bar makes Invoke-WebRequest many times slower in Windows PowerShell 5.1.
    $ProgressPreference = "SilentlyContinue"
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

    $repo = "Rav-2007/codeterminal-core"
    $root = Join-Path $env:LOCALAPPDATA "Programs\Mochiii"
    $bin = Join-Path $root "bin"
    $dataDir = Join-Path $env:USERPROFILE ".mochiii"

    function Step([string]$text) { Write-Host "==> $text" }
    function Note([string]$text) { Write-Host "    $text" }
    function Warn([string]$text) { Write-Warning $text }

    # The long form of a path, with every 8.3 short name (C:\Users\RUNNER~1)
    # expanded. A directory listing returns long names, and it finds a component
    # by its short name too.
    function Get-LongPath([string]$path) {
        $full = [IO.Path]::GetFullPath($path)
        $long = [IO.Path]::GetPathRoot($full)
        foreach ($part in @($full.Substring($long.Length) -split '\\' | Where-Object { $_ })) {
            $entry = Get-ChildItem -LiteralPath $long -Filter $part -Force -ErrorAction SilentlyContinue | Select-Object -First 1
            $long = Join-Path $long $(if ($entry) { $entry.Name } else { $part })
        }
        return $long
    }

    # Daemons started from this install hold their files open, and Windows will
    # not rename a folder a running program was started from. Matching the full
    # path leaves alone the daemon VS Code started from its own copy. Both
    # spellings are matched: the terminal app starts the daemon by its long path
    # (Go's filepath.EvalSymlinks expands short names), while %LOCALAPPDATA% can
    # be short.
    function Get-FromBin([string[]]$names) {
        $dirs = @($bin, (Get-LongPath $bin)) | ForEach-Object { $_.TrimEnd("\") + "\" }
        $procs = if ($names) { Get-Process -Name $names -ErrorAction SilentlyContinue } else { Get-Process -ErrorAction SilentlyContinue }
        @($procs | Where-Object {
                $path = try { $_.Path } catch { $null }
                $path -and @($dirs | Where-Object { $path.StartsWith($_, [StringComparison]::OrdinalIgnoreCase) }).Count -gt 0
            })
    }

    # THE ROLE OF A mochiii.exe (1.2.0). One binary is now the terminal app,
    # the background service (`mochiii daemon ...`) and the embedder helper
    # (`mochiii __embedder-helper ...`), so the process name no longer tells
    # them apart; the first argument on its command line does. Read through
    # CIM, which Windows PowerShell 5.1 and PowerShell 7 both have. A command
    # line that cannot be read gives no role, so that process counts as not
    # ours: the installer never stops what it cannot identify.
    function Get-RoleWord([int]$id) {
        $line = try { (Get-CimInstance Win32_Process -Filter "ProcessId = $id" -ErrorAction Stop).CommandLine } catch { $null }
        if (-not $line) { return "" }
        $rest = if ($line.StartsWith('"')) { $line.Substring($line.IndexOf('"', 1) + 1) } else { @($line -split ' ', 2)[1] }
        return @("$rest".Trim() -split '\s+', 2)[0]
    }

    # The background services and embedder helpers this install started: since
    # 1.2.0 mochiii.exe in those roles, and until then the programs of their own
    # (an upgrade from 1.1.0 must stop those too).
    function Get-Ours {
        @(Get-FromBin "mochiii-daemon", "mochiii-embedder-helper") +
        @(Get-FromBin "mochiii" | Where-Object { (Get-RoleWord $_.Id) -in "daemon", "__embedder-helper" })
    }

    function Stop-Ours {
        $procs = @(Get-Ours)
        if ($procs.Count -gt 0) {
            $procs | Stop-Process -Force -ErrorAction SilentlyContinue
            $procs | Wait-Process -Timeout 10 -ErrorAction SilentlyContinue
            Note "stopped the running background service; the next 'mochiii' starts the new one"
        }
    }

    # An open mochiii, or any Mochiii program but the background service, running
    # from $bin. It is not the installer's to stop, and while it runs Windows will
    # not move the folder, so the install stops before changing anything and
    # names it.
    function Assert-NothingElseRunning {
        $ours = @(Get-Ours | ForEach-Object { $_.Id })
        $others = @(Get-FromBin | Where-Object { $_.Id -notin $ours })
        if ($others.Count -gt 0) {
            $names = ($others | ForEach-Object { "$($_.ProcessName) (process $($_.Id))" }) -join ", "
            throw "Close $names, which is running from $bin, and run this again. Nothing was changed."
        }
    }

    # WHO HAS A FOLDER'S FILES OPEN, from the Windows Restart Manager, for the
    # messages below. Compiled only when a wait happens (C# 5, which Windows
    # PowerShell 5.1 compiles); where it cannot be, as in Constrained Language
    # Mode, the names are left out and nothing else changes.
    $rmSource = @'
using System;
using System.Collections.Generic;
using System.Runtime.InteropServices;
using System.Text;
namespace MochiiiInstall {
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

        // One entry per holder: "process id|name".
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
                    for (int i = 0; i < count; i++) { result.Add(infos[i].Process.ProcessId + "|" + infos[i].AppName); }
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
    function Get-FileHolders([string]$dir) {
        if (-not ('MochiiiInstall.FileHolders' -as [type])) {
            try { Add-Type -TypeDefinition $rmSource -Language CSharp -ErrorAction Stop } catch { return @() }
        }
        $files = @(Get-ChildItem -LiteralPath $dir -Recurse -File -ErrorAction SilentlyContinue | ForEach-Object { $_.FullName })
        if ($files.Count -eq 0) { return @() }
        $raw = try { [MochiiiInstall.FileHolders]::Of($files) } catch { return @() }
        @($raw | ForEach-Object {
                $id, $app = $_ -split '\|', 2
                $image = try { (Get-Process -Id $id -ErrorAction Stop).ProcessName } catch { $app }
                "$image (process $id)"
            })
    }

    # Runs $action on $dir until it succeeds, waiting out a hold on the folder's
    # files. WINDOWS ITSELF BRIEFLY HOLDS THE FILES OF A PROGRAM THAT HAS JUST
    # EXITED. Measured on GitHub's Windows on ARM runner by the smoke test's lock
    # probe (2026-10-19, two runs): the folder was refused for 176-316 ms after
    # the service stopped, by a different holder each time and never one of ours
    # -- Windows Defender (which Restart Manager cannot see: the hold vanished
    # with the folder excluded from scanning), the x64 emulator's translation
    # cache (the XtaCache service), and another system process. GitHub's x64
    # image has Defender off and needs no emulator, so it showed none; a real
    # Windows has Defender on. So a refusal is waited out, saying what for, for
    # up to 30 seconds -- about a hundred times the longest measured hold -- and
    # past that it fails with the error and whoever holds the files. $after ends
    # that failure message.
    function Invoke-OnFolder([string]$dir, [scriptblock]$action, [string]$doing, [string]$after) {
        $deadline = (Get-Date).AddSeconds(30)
        $waiting = $false
        while ($true) {
            try { & $action; return } catch { $err = $_.Exception.Message }
            if ((Get-Date) -gt $deadline) {
                $held = @(Get-FileHolders $dir)
                $who = if ($held.Count) { $held -join ", " } else { "Windows, with no program holding them (usually a security scan)" }
                throw "$doing failed: the files in $dir were still in use after 30 seconds, by $who. $after ($err)"
            }
            if (-not $waiting) {
                $held = @(Get-FileHolders $dir)
                if ($held.Count) { Note "waiting for $($held -join ', ') to let go of the files in $dir..." }
                else { Note "waiting for Windows to let go of the files in $dir (usually a security scan)..." }
                $waiting = $true
            }
            Start-Sleep -Milliseconds 250
        }
    }

    # Folders an earlier run could not remove, because their files were still held.
    function Remove-Leftovers {
        # -like, not -Filter: Windows wildcards treat a trailing ".*" as optional.
        Get-ChildItem -LiteralPath $root -Directory -ErrorAction SilentlyContinue | Where-Object { $_.Name -like "bin.old.*" } |
            ForEach-Object { Remove-Item -LiteralPath $_.FullName -Recurse -Force -ErrorAction SilentlyContinue }
    }

    function Remove-FromUserPath {
        $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
        if (-not $userPath) { return }
        $kept = @($userPath.Split(";") | Where-Object { $_ -and ($_.TrimEnd("\") -ne $bin) })
        [Environment]::SetEnvironmentVariable("Path", ($kept -join ";"), "User")
    }

    if ($Uninstall) {
        Step "Removing Mochiii"
        Assert-NothingElseRunning
        Stop-Ours
        if (Test-Path $bin) {
            Invoke-OnFolder $bin { Remove-Item -LiteralPath $bin -Recurse -Force } "Removing Mochiii" "Run this again to finish removing it."
        }
        Remove-Leftovers
        if ((Test-Path $root) -and -not (Get-ChildItem $root -Force)) { Remove-Item -Force $root }
        Remove-FromUserPath
        if ($Purge) {
            if (Test-Path $dataDir) { Remove-Item -Recurse -Force $dataDir }
            Write-Host "Removed Mochiii and everything in $dataDir."
        } else {
            Write-Host "Removed Mochiii. Your keys, models and saved chats are still in $dataDir (MOCHIII_PURGE=1 removes them)."
        }
        return
    }

    # Installing elevated is harmless by itself; what follows it is not. The next
    # thing this script says is "run mochiii", and in this same elevated window
    # that starts the background service elevated, whose socket directory is then
    # owned by Administrators and refused to every normal window afterwards
    # (protocol/socketdir_windows.go). An administrator account in a normal
    # PowerShell is not elevated and is not refused. The override is an
    # environment variable because `irm | iex` cannot pass a parameter.
    $principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
    if ($principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator) -and -not $AllowAdmin) {
        throw "Run this in a normal PowerShell, not as Administrator: Mochiii installs into your own profile. (Set `$env:MOCHIII_ALLOW_ADMIN = `"1`" to do it anyway.)"
    }

    $arch = $env:PROCESSOR_ARCHITEW6432
    if (-not $arch) { $arch = $env:PROCESSOR_ARCHITECTURE }
    switch ($arch) {
        "AMD64" { }
        "ARM64" { Note "Windows on ARM: installing the x64 build, which Windows runs under its built-in emulation" }
        default { throw "There is no Mochiii build for $arch Windows (it needs 64-bit Windows)." }
    }
    $target = "win32-x64"

    $Version = "$Version".TrimStart("v")
    if ($env:MOCHIII_DOWNLOAD_BASE) {
        $base = $env:MOCHIII_DOWNLOAD_BASE.TrimEnd("/")
        if (-not ($base.StartsWith("https://") -or $base -match '^http://(127\.0\.0\.1|localhost):')) {
            throw "MOCHIII_DOWNLOAD_BASE must be an https:// address"
        }
    } elseif ($Version) {
        $base = "https://github.com/$repo/releases/download/v$Version"
    } else {
        $base = "https://github.com/$repo/releases/latest/download"
    }

    $tmp = Join-Path ([IO.Path]::GetTempPath()) ("mochiii-install-" + [Guid]::NewGuid().ToString("N"))
    New-Item -ItemType Directory -Path $tmp | Out-Null
    try {
        $archive = "mochiii-$target.zip"
        $label = if ($Version) { " $Version" } else { "" }
        Step "Downloading Mochiii$label for $target"
        Invoke-WebRequest -UseBasicParsing -Uri "$base/SHA256SUMS-archives" -OutFile (Join-Path $tmp "SHA256SUMS-archives")
        Invoke-WebRequest -UseBasicParsing -Uri "$base/$archive" -OutFile (Join-Path $tmp $archive)

        $expected = $null
        foreach ($line in Get-Content (Join-Path $tmp "SHA256SUMS-archives")) {
            $parts = $line -split '\s+', 2
            if ($parts.Count -eq 2 -and $parts[1].TrimStart("*") -eq $archive) { $expected = $parts[0].ToLowerInvariant() }
        }
        if (-not $expected) { throw "The release's checksums do not list $archive, so it cannot be verified." }
        $actual = (Get-FileHash -Algorithm SHA256 -Path (Join-Path $tmp $archive)).Hash.ToLowerInvariant()
        if ($expected -ne $actual) { throw "Checksum mismatch for $archive (expected $expected, got $actual). Nothing was installed." }
        Note "checksum verified"

        New-Item -ItemType Directory -Force -Path $root | Out-Null
        $staging = Join-Path $root "bin.new.$PID"
        $previous = Join-Path $root "bin.old.$PID"
        if (Test-Path $staging) { Remove-Item -Recurse -Force $staging }
        Expand-Archive -Path (Join-Path $tmp $archive) -DestinationPath $staging -Force
        if (-not (Test-Path (Join-Path $staging "mochiii.exe"))) { throw "$archive has no mochiii.exe in it." }
        Get-ChildItem -Path $staging -Recurse -File | Unblock-File

        $reported = & (Join-Path $staging "mochiii.exe") --version 2>$null
        if ($LASTEXITCODE -ne 0 -or -not "$reported".StartsWith("mochiii ")) {
            throw "The downloaded mochiii.exe does not run on this machine."
        }
        $installed = "$reported".Substring(8).Trim()
        if ($Version -and $installed -ne $Version) { throw "Asked for $Version but the download says it is $installed." }

        try {
            Assert-NothingElseRunning
            Stop-Ours
            if (Test-Path $bin) {
                Invoke-OnFolder $bin { Rename-Item -LiteralPath $bin -NewName (Split-Path $previous -Leaf) } "Upgrading" "The installed version is unchanged."
            }
        } catch {
            Remove-Item -Recurse -Force $staging -ErrorAction SilentlyContinue
            throw
        }
        try {
            Move-Item -Path $staging -Destination $bin
        } catch {
            if (Test-Path $previous) { Rename-Item -Path $previous -NewName "bin" }
            Remove-Item -Recurse -Force $staging -ErrorAction SilentlyContinue
            throw
        }
        # The new version is in place. The old one's files can still be held for a
        # moment, as above; if they stay held, the next run removes the folder.
        if (Test-Path $previous) {
            try {
                Invoke-OnFolder $previous { Remove-Item -LiteralPath $previous -Recurse -Force } "Removing the previous version" ""
            } catch {
                Note "the previous version's folder is still in use, and the next install removes it: $previous"
            }
        }
        Remove-Leftovers
        Step "Installed Mochiii $installed in $bin"
    } finally {
        Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
    }

    $needsNewTerminal = $false
    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    $onPath = $userPath -and (@($userPath.Split(";") | Where-Object { $_.TrimEnd("\") -eq $bin }).Count -gt 0)
    if (-not $onPath) {
        if ($NoModifyPath) {
            Note "add $bin to your PATH to run mochiii from any terminal"
        } else {
            $newPath = if ($userPath) { "$($userPath.TrimEnd(';'));$bin" } else { $bin }
            [Environment]::SetEnvironmentVariable("Path", $newPath, "User")
            $env:Path = "$($env:Path.TrimEnd(';'));$bin"
            Note "added $bin to your PATH"
            $needsNewTerminal = $true
        }
    }

    # onnxruntime, which runs the code-search model, needs the Visual C++ runtime.
    $system32 = Join-Path $env:SystemRoot "System32"
    if (-not ((Test-Path (Join-Path $system32 "vcruntime140.dll")) -and (Test-Path (Join-Path $system32 "msvcp140.dll")))) {
        Warn "The Microsoft Visual C++ runtime is not installed, and code search needs it. Install it from https://aka.ms/vs/17/release/vc_redist.x64.exe -- everything else works without it."
    }

    if (-not $NoModel) {
        Step "Downloading the code-search model (once for this computer)"
        & (Join-Path $bin "mochiii.exe") download-model
        if ($LASTEXITCODE -ne 0) {
            Warn "The model download did not finish; Mochiii offers it again the first time you run it in a project."
        }
    }

    Write-Host ""
    Write-Host "Mochiii $installed is ready."
    if ($needsNewTerminal) {
        Write-Host "Open a new terminal, then run it inside a project folder:"
    } else {
        Write-Host "Run it inside a project folder:"
    }
    Write-Host ""
    Write-Host "    cd your-project; mochiii"
    Write-Host ""
    Write-Host "The first run asks which AI provider to use and for its API key."
}

Install-Mochiii
