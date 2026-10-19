# Mochiii installer for Windows.
#
#   powershell -ExecutionPolicy ByPass -c "irm https://github.com/Rav-2007/codeterminal-core/releases/latest/download/install.ps1 | iex"
#
# `irm | iex` cannot pass parameters, so options come from environment variables:
#
#   $env:MOCHIII_VERSION = "1.1.0"      install that release instead of the latest
#   $env:MOCHIII_NO_MODEL = "1"         skip the code-search model download
#   $env:MOCHIII_NO_VSCODE = "1"        do not install the VS Code extension
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
# code-search model; installs the VS Code extension if VS Code is here. It never
# needs Administrator rights, and everything goes under your own profile.
#
# Windows on ARM gets the x64 build, which Windows runs under its built-in
# emulation.

param(
    [string]$Version = $env:MOCHIII_VERSION,
    [switch]$NoModel = ($env:MOCHIII_NO_MODEL -eq "1"),
    [switch]$NoVSCode = ($env:MOCHIII_NO_VSCODE -eq "1"),
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
    $extension = "MochiiiAIAgent.mochiii-vscode"
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

    function Stop-Ours {
        $procs = @(Get-FromBin "mochiii-daemon", "mochiii-embedder-helper")
        if ($procs.Count -gt 0) {
            $procs | Stop-Process -Force -ErrorAction SilentlyContinue
            $procs | Wait-Process -Timeout 10 -ErrorAction SilentlyContinue
            Note "stopped the running background service; the next 'mochiii' starts the new one"
        }
    }

    # Runs $action until it succeeds, for up to 15 seconds, and returns the last
    # error, or $null. A program that has just exited can keep its files open a
    # moment longer -- an antivirus scan, or on Windows on ARM the x64 emulator's
    # translation cache -- and the 1.1.0 rehearsal's Windows on ARM run failed its
    # upgrade on exactly that, where x64 did not.
    function Invoke-Retrying([scriptblock]$action) {
        $last = $null
        for ($i = 0; $i -lt 30; $i++) {
            try { & $action; return $null } catch { $last = $_.Exception.Message; Start-Sleep -Milliseconds 500 }
        }
        return $last
    }

    # Why $bin could not be moved: the error, and whatever still runs from it.
    function Get-Why([string]$err) {
        $held = @(Get-FromBin | ForEach-Object { "$($_.ProcessName) (process $($_.Id))" })
        if ($held.Count -gt 0) { return "$err Still running from it: $($held -join ', ')." }
        return "$err Nothing is running from it any more; try again."
    }

    $stillRunning = "Mochiii is still running from $bin -- most likely an open mochiii in another window. Close it and run this again"

    function Remove-FromUserPath {
        $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
        if (-not $userPath) { return }
        $kept = @($userPath.Split(";") | Where-Object { $_ -and ($_.TrimEnd("\") -ne $bin) })
        [Environment]::SetEnvironmentVariable("Path", ($kept -join ";"), "User")
    }

    if ($Uninstall) {
        Step "Removing Mochiii"
        Stop-Ours
        if (Test-Path $bin) {
            $err = Invoke-Retrying { Remove-Item -Recurse -Force $bin }
            if ($err) { throw "$stillRunning. ($(Get-Why $err))" }
        }
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
        foreach ($f in "mochiii.exe", "mochiii-daemon.exe", "mochiii-embedder-helper.exe", "models.json") {
            if (-not (Test-Path (Join-Path $staging $f))) { throw "$archive has no $f in it." }
        }
        Get-ChildItem -Path $staging -Recurse -File | Unblock-File

        $reported = & (Join-Path $staging "mochiii.exe") --version 2>$null
        if ($LASTEXITCODE -ne 0 -or -not "$reported".StartsWith("mochiii ")) {
            throw "The downloaded mochiii.exe does not run on this machine."
        }
        $installed = "$reported".Substring(8).Trim()
        if ($Version -and $installed -ne $Version) { throw "Asked for $Version but the download says it is $installed." }

        Stop-Ours
        if (Test-Path $bin) {
            $err = Invoke-Retrying { Rename-Item -Path $bin -NewName (Split-Path $previous -Leaf) }
            if ($err) {
                Remove-Item -Recurse -Force $staging -ErrorAction SilentlyContinue
                throw "$stillRunning; nothing was changed. ($(Get-Why $err))"
            }
        }
        try {
            Move-Item -Path $staging -Destination $bin
        } catch {
            if (Test-Path $previous) { Rename-Item -Path $previous -NewName "bin" }
            Remove-Item -Recurse -Force $staging -ErrorAction SilentlyContinue
            throw
        }
        if (Test-Path $previous) { Remove-Item -Recurse -Force $previous }
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
        & (Join-Path $bin "mochiii-daemon.exe") download-model
        if ($LASTEXITCODE -ne 0) {
            Warn "The model download did not finish; Mochiii offers it again the first time you run it in a project."
        }
    }

    if (-not $NoVSCode -and (Get-Command code -ErrorAction SilentlyContinue)) {
        if ($arch -eq "ARM64") {
            Note "VS Code found, but the Mochiii extension is not built for Windows on ARM yet; the terminal app is installed"
        } else {
            Step "Installing the VS Code extension"
            & code --install-extension $extension --force *> $null
            if ($LASTEXITCODE -ne 0) {
                Warn "VS Code would not install the extension; you can run: code --install-extension $extension"
            }
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
