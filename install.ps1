# Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
# Author: Albert <albert@rocareer.com>
#
# install.ps1 — One-line installer for Rolling-Memory on Windows (PowerShell).
#
# Usage:
#   powershell -ExecutionPolicy Bypass -File install.ps1 [--with-hooks] [--with-instructions]
#   irm https://github.com/rocareer/rolling-memory/raw/main/install.ps1 | iex
$ErrorActionPreference = "Stop"

# Windows PowerShell 5.1 compatibility (no pwsh 7 required). Suggested by
# @Buyou27 in GitHub PR #2 and adopted here; the credited changelog entry is
# in CHANGELOG.md.
#   - the progress bar in Invoke-WebRequest slows large downloads down by an order of magnitude;
#   - older Windows defaults to TLS 1.0/1.1, which GitHub rejects;
#   - this file is UTF-8 with a BOM so 5.1 reads the non-ASCII diagnostics correctly.
$ProgressPreference = "SilentlyContinue"
try {
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
} catch {
}

$DefaultRepoUrl = "https://github.com/rocareer/rolling-memory.git"
$DefaultReleaseBase = "https://github.com/rocareer/rolling-memory/releases/latest/download"
# No default mirror: GitHub is the single public source for this project
# (see the project policy on external terms). Inject a mirror explicitly with
# ROLLING_MEMORY_RELEASE_MIRROR=<base>/releases/latest/download if you run one.
$DefaultReleaseMirror = ""
$RepoUrl = if ($env:ROLLING_MEMORY_REPO_URL) { $env:ROLLING_MEMORY_REPO_URL } else { $DefaultRepoUrl }
# Same rule for the source fallback: no third-party default mirror.
$RepoMirrorUrl = if ($env:ROLLING_MEMORY_REPO_MIRROR) { $env:ROLLING_MEMORY_REPO_MIRROR } else { "" }

$ReleaseBaseExplicit = $false
if (Test-Path Env:ROLLING_MEMORY_RELEASE_BASE) {
    $ReleaseBase = $env:ROLLING_MEMORY_RELEASE_BASE
    if ($ReleaseBase -ne "") { $ReleaseBaseExplicit = $true }
} else {
    $ReleaseBase = $DefaultReleaseBase
}
if ($ReleaseBase -in @("off", "none", "0")) {
    $ReleaseBase = ""
}
$ReleaseMirror = if (Test-Path Env:ROLLING_MEMORY_RELEASE_MIRROR) { $env:ROLLING_MEMORY_RELEASE_MIRROR } else { $DefaultReleaseMirror }
if ($ReleaseMirror -in @("off", "none", "0")) {
    $ReleaseMirror = ""
}

function Resolve-GiteeReleaseBase([string]$Base) {
    if ($Base -notmatch 'gitee\.com/([^/]+/[^/]+)/releases/latest/download') {
        return $Base
    }
    $Repo = $Matches[1]
    try {
        $Latest = Invoke-RestMethod -Uri "https://gitee.com/api/v5/repos/$Repo/releases/latest" -TimeoutSec 20
        if ($Latest.tag_name) {
            return "https://gitee.com/$Repo/releases/download/$($Latest.tag_name)"
        }
    } catch {
    }
    return $Base
}

$InstallDir = if ($env:ROLLING_MEMORY_INSTALL_DIR) { $env:ROLLING_MEMORY_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA "rolling-memory\bin" }
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
$TargetBin = Join-Path $InstallDir "rolling-memory.exe"

$SourceRef = if ($env:ROLLING_MEMORY_SOURCE_REF) { $env:ROLLING_MEMORY_SOURCE_REF } else { "" }
$HarnessArgs = @()
for ($i = 0; $i -lt $args.Count; $i++) {
    if ($args[$i] -eq "--ref") {
        if ($i + 1 -ge $args.Count) { throw "install.ps1: --ref requires a tag" }
        $SourceRef = $args[$i + 1]
        $i++
    } elseif ($args[$i] -like "--ref=*") {
        $SourceRef = $args[$i].Substring(6)
    } else {
        $HarnessArgs += $args[$i]
    }
}
if ($SourceRef -and $SourceRef.StartsWith("-")) { throw "install.ps1: invalid --ref value: $SourceRef" }
if ($SourceRef) {
    $ReleaseBase = ""
} else {
    if ($ReleaseBase) { $ReleaseBase = Resolve-GiteeReleaseBase $ReleaseBase }
    if ($ReleaseMirror) { $ReleaseMirror = Resolve-GiteeReleaseBase $ReleaseMirror }
}

# Map the host architecture to the release asset suffix.
$Arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    "AMD64" { "amd64" }
    "ARM64" { "arm64" }
    default { throw "install.ps1: unsupported architecture: $env:PROCESSOR_ARCHITECTURE" }
}

$Built = $false
$PrebuiltErr = ""
$PrebuiltVersion = ""
$PrebuiltAsset = ""
$InstallMethod = ""
$InstallRef = ""
$InstallCommit = ""
$InstallAsset = ""
$DataDir = if ($env:XDG_DATA_HOME) { Join-Path $env:XDG_DATA_HOME "rolling-memory" } elseif ($env:USERPROFILE) { Join-Path $env:USERPROFILE ".local\share\rolling-memory" } else { Join-Path $env:LOCALAPPDATA "rolling-memory" }
$EvalDataTarget = Join-Path (Split-Path -Parent $InstallDir) "evaldata"
if ((-not $SourceRef) -and (Test-Path "cmd\rolling-memory\main.go") -and (Get-Command go -ErrorAction SilentlyContinue)) {
    Write-Host "[rolling-memory] Building from local checkout..."
    $env:CGO_ENABLED = "0"
    go build -trimpath -o $TargetBin .\cmd\rolling-memory
    $InstallMethod = "local"
    $InstallRef = (git describe --tags --always 2>$null)
    $InstallCommit = (git rev-parse HEAD 2>$null)
    if ((Test-Path "evaldata\MANIFEST.json") -and (-not (Test-Path (Join-Path $EvalDataTarget "MANIFEST.json")))) {
        Copy-Item -Recurse -Force "evaldata" $EvalDataTarget -ErrorAction SilentlyContinue
    }
    $Built = $true
}

function Try-DownloadPrebuilt([string]$BaseUrl) {
    $SumsUrl = "$($BaseUrl.TrimEnd('/'))/SHA256SUMS"
    $Sums = $null
    try {
        $Sums = (Invoke-WebRequest -UseBasicParsing -TimeoutSec 20 -Uri $SumsUrl).Content
        if ($Sums -is [byte[]]) {
            $Sums = [System.Text.Encoding]::UTF8.GetString($Sums)
        }
    } catch {
        $script:PrebuiltErr = "无法连接 $SumsUrl"
        if ($ReleaseBaseExplicit) {
            throw "无法获取 $SumsUrl 的 SHA256SUMS，拒绝安装未校验的二进制。"
        }
        return $false
    }
    if (-not $Sums) { return $false }

    # Prefer native architecture asset; on ARM64 allow fallback to windows-amd64.exe
    # (Windows 11 ARM runs x64 binaries natively) if the mirror only carries amd64.
    $CandidateAssets = @("rolling-memory-windows-$Arch.exe")
    if ($Arch -eq "arm64" -and (-not $ReleaseBaseExplicit)) {
        $CandidateAssets += "rolling-memory-windows-amd64.exe"
    }

    $SelectedAsset = $null
    $Want = $null
    foreach ($Cand in $CandidateAssets) {
        foreach ($Line in ($Sums -split "`r?`n")) {
            $Fields = $Line.Trim() -split '\s+'
            if ($Fields.Count -ge 2 -and $Fields[1].TrimStart('*') -eq $Cand) {
                $SelectedAsset = $Cand
                $Want = $Fields[0].ToLower()
                break
            }
        }
        if ($Want) { break }
    }

    if (-not $Want) {
        $script:PrebuiltErr = "SHA256SUMS 中缺少 $($CandidateAssets[0]) 的校验和"
        if ($ReleaseBaseExplicit) {
            throw "$($script:PrebuiltErr)，拒绝安装未校验的二进制。"
        }
        return $false
    }

    $script:PrebuiltAsset = $SelectedAsset
    $AssetUrl = "$($BaseUrl.TrimEnd('/'))/$SelectedAsset"
    Write-Host "[rolling-memory] Downloading prebuilt binary ($SelectedAsset)..."
    $TmpBin = "$TargetBin.tmp.$PID"
    try {
        Invoke-WebRequest -UseBasicParsing -TimeoutSec 120 -Uri $AssetUrl -OutFile $TmpBin
        $Got = (Get-FileHash -Algorithm SHA256 -Path $TmpBin).Hash.ToLower()
        if ($Got -ne $Want) {
            Remove-Item -Force -ErrorAction SilentlyContinue $TmpBin
            throw "SHA256 校验失败：期望 $Want，实得 $Got。"
        }
        Move-Item -Force -Path $TmpBin -Destination $TargetBin
        $script:PrebuiltVersion = ""
        try {
            $VersionText = (Invoke-WebRequest -UseBasicParsing -TimeoutSec 10 -Uri "$($BaseUrl.TrimEnd('/'))/VERSION").Content
            if ($VersionText -is [byte[]]) { $VersionText = [System.Text.Encoding]::UTF8.GetString($VersionText) }
            $script:PrebuiltVersion = ([string]$VersionText).Trim()
        } catch {
            $script:PrebuiltVersion = ""
        }
        Write-Host "[rolling-memory] SHA256 verified ($Got)"
        return $true
    } catch {
        Remove-Item -Force -ErrorAction SilentlyContinue $TmpBin
        $script:PrebuiltErr = $_.Exception.Message
        if ($ReleaseBaseExplicit -or ($_.Exception.Message -match "SHA256")) {
            throw
        }
        return $false
    }
}

if ((-not $Built) -and ($ReleaseBase -ne "")) {
    if (Try-DownloadPrebuilt $ReleaseBase) {
        $Built = $true
        $InstallMethod = "release"
        $InstallAsset = $PrebuiltAsset
        $InstallRef = if ($PrebuiltVersion) { $PrebuiltVersion } else { "unknown" }
    } elseif ((-not $ReleaseBaseExplicit) -and ($ReleaseMirror -ne "") -and ($ReleaseMirror -ne $ReleaseBase)) {
        Write-Host "[rolling-memory] Primary release channel unavailable; trying mirror ($ReleaseMirror)..."
        if (Try-DownloadPrebuilt $ReleaseMirror) {
            $Built = $true
            $InstallMethod = "release"
            $InstallAsset = $PrebuiltAsset
            $InstallRef = if ($PrebuiltVersion) { $PrebuiltVersion } else { "unknown" }
        }
    }
    if (-not $Built) {
        Write-Host "[rolling-memory] Prebuilt channel unavailable ($PrebuiltErr)."
    }
}

if ((-not $Built) -and (Get-Command go -ErrorAction SilentlyContinue) -and (Get-Command git -ErrorAction SilentlyContinue)) {
    $TmpRepo = Join-Path ([System.IO.Path]::GetTempPath()) ("rm-src-" + [System.Guid]::NewGuid().ToString("N"))
    try {
        Write-Host "[rolling-memory] Cloning $RepoUrl and building..."
        $CloneArgs = if ($SourceRef) { @("--depth", "1", "--branch", $SourceRef) } else { @("--depth", "1") }
        git clone @CloneArgs $RepoUrl $TmpRepo
        if ($LASTEXITCODE -ne 0 -and $RepoMirrorUrl -and ($RepoUrl -eq $DefaultRepoUrl)) {
            Write-Host "[rolling-memory] Primary source unavailable; retrying mirror $RepoMirrorUrl..."
            Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $TmpRepo
            git clone @CloneArgs $RepoMirrorUrl $TmpRepo
        }
        if ($LASTEXITCODE -ne 0) {
            throw "install.ps1: failed to clone $RepoUrl"
        }
        Push-Location $TmpRepo
        try {
            $env:CGO_ENABLED = "0"
            go build -trimpath -o $TargetBin .\cmd\rolling-memory
            if ($LASTEXITCODE -ne 0) { throw "go build failed" }
            $InstallMethod = "source"
            $InstallRef = if ($SourceRef) { $SourceRef } else { (git describe --tags --always 2>$null) }
            $InstallCommit = (git rev-parse HEAD 2>$null)
            if ((Test-Path "evaldata\MANIFEST.json") -and (-not (Test-Path (Join-Path $EvalDataTarget "MANIFEST.json")))) {
                Copy-Item -Recurse -Force "evaldata" $EvalDataTarget -ErrorAction SilentlyContinue
            }
            $Built = $true
        } finally {
            Pop-Location
        }
    } finally {
        Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $TmpRepo
    }
}

if (-not $Built) {
    throw "无法获取预编译二进制（原因：$PrebuiltErr），且本机未安装 Git / Go 编译环境。普通用户请检查网络或设置 `$env:ROLLING_MEMORY_RELEASE_BASE='https://your-mirror/releases/latest/download' 后重试；开发者可安装 Go 1.26+ 与 Git 从源码构建。"
}

# Short alias `rmem` (equivalent to the POSIX symlink pair created by install.sh).
$RmemCmd = Join-Path $InstallDir "rmem.cmd"
Set-Content -Path $RmemCmd -Encoding ASCII -Value "@echo off`r`n`"%~dp0rolling-memory.exe`" %*"
New-Item -ItemType Directory -Force -Path $DataDir | Out-Null
$MetaMethod = if ($InstallMethod) { $InstallMethod } else { "unknown" }
$MetaRef = if ($InstallRef) { $InstallRef } else { "unknown" }
$MetaCommit = if ($InstallCommit) { $InstallCommit } else { "" }
$MetaAsset = if ($InstallAsset) { $InstallAsset } else { "" }
$InstallMeta = @{
    method = $MetaMethod
    repo = $RepoUrl
    ref = $MetaRef
    commit = $MetaCommit
    asset = $MetaAsset
    installed_at = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
} | ConvertTo-Json -Compress -Depth 10
[System.IO.File]::WriteAllText((Join-Path $DataDir "install.json"), $InstallMeta, (New-Object System.Text.UTF8Encoding($false)))
Write-Host "[rolling-memory] Installed binary to $TargetBin (alias: $RmemCmd)"
if (($env:PATH -split ';') -notcontains $InstallDir) {
    $env:PATH = "$InstallDir;$env:PATH"
    if ($env:ROLLING_MEMORY_NO_MODIFY_PATH -ne "1") {
        try {
            $UserPath = [Environment]::GetEnvironmentVariable("Path", "User")
            $Parts = if ($UserPath) { $UserPath -split ';' } else { @() }
            if ($Parts -notcontains $InstallDir) {
                $NewUserPath = if ($UserPath) { "$UserPath;$InstallDir" } else { $InstallDir }
                [Environment]::SetEnvironmentVariable("Path", $NewUserPath, "User")
                Write-Host "[rolling-memory] Added $InstallDir to your Windows user PATH."
            }
        } catch {
            Write-Host "[rolling-memory] Add to PATH if needed: `$env:Path += `";$InstallDir`""
        }
    }
}

try { & $TargetBin init --write-config *> $null } catch {}

# Seed update preferences that match how this binary arrived. A published
# install follows stable releases and keeps itself current; a source build
# tracks main and only notifies, because dev updates require a rebuild.
$InstallChannel = if ($MetaMethod -eq "release") { "stable" } else { "dev" }
$InstallAuto = if ($MetaMethod -eq "release") { "apply" } else { "notify" }
try { & $TargetBin config set update.channel $InstallChannel *> $null } catch {}
try { & $TargetBin config set update.auto $InstallAuto *> $null } catch {}
if ($RepoUrl -match "gitee\.com") {
    try { & $TargetBin config set update.source gitee *> $null } catch {}
}
try { & $TargetBin update --schedule install --quiet *> $null } catch {}

$InstallArgs = if ($HarnessArgs.Count -eq 0) { @("install", "--with-hooks") } else { @("install") + $HarnessArgs }
$PrevEAP = $ErrorActionPreference
$ErrorActionPreference = "Continue"
$InstallOut = & $TargetBin @InstallArgs 2>&1
$InstallRc = $LASTEXITCODE
$ErrorActionPreference = $PrevEAP

if ($InstallOut) {
    $InstallOut | ForEach-Object { Write-Host $_ }
}
if ($InstallRc -eq 0) {
    & $TargetBin install --check
} elseif (($InstallOut -join "`n") -match "no harness detected") {
    Write-Host "[rolling-memory] CLI binary and local vault are installed and ready (no host harness detected yet)."
    Write-Host "[rolling-memory] Wire a harness anytime with: rolling-memory install --with-hooks [<harness-id>]"
} else {
    exit $InstallRc
}

Write-Host "[rolling-memory] Web console: run `rolling-memory panel` to start it (backend :9006 + web frontend :8008; add --desktop-shortcut to install for a desktop launcher)."
Write-Host "[rolling-memory] Self-update: run `rolling-memory update` (or `rolling-memory update --check`) anytime to update the CLI, console and host integrations."
