# TunnelX Client Installer for Windows (PowerShell)
# Usage: irm https://tl.codesky.tech/install.ps1 | iex

$ErrorActionPreference = 'Stop'

$Repo = "Sirtheprogrammer/tunnel"
$BinaryName = "tunnelx.exe"

# 1. Detect Architecture
$Arch = "amd64"
if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") {
    $Arch = "arm64"
}

Write-Host "=> Installing TunnelX CLI for windows-$Arch..." -ForegroundColor Cyan

# 2. Determine Version
$Version = "v1.0.0"
try {
    $Release = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest" -UseBasicParsing
    if ($Release.tag_name) {
        $Version = $Release.tag_name
    }
} catch {
    # Fallback to default version
}

$DownloadUrl = "https://github.com/$Repo/releases/download/$Version/tunnelx-$Version-windows-$Arch.zip"

# 3. Create install directory in LocalAppData
$InstallDir = Join-Path $env:LocalAppData "Programs\tunnelx"
if (-not (Test-Path $InstallDir)) {
    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
}

$TempZip = Join-Path $env:TEMP "tunnelx.zip"
Write-Host "=> Downloading $DownloadUrl..." -ForegroundColor Gray
Invoke-WebRequest -Uri $DownloadUrl -OutFile $TempZip -UseBasicParsing

$TempExtract = Join-Path $env:TEMP "tunnelx_extracted"
Expand-Archive -Path $TempZip -DestinationPath $TempExtract -Force

$FoundBin = Get-ChildItem -Path $TempExtract -Filter "tunnelx.exe" -Recurse | Select-Object -First 1
if ($FoundBin) {
    Copy-Item -Path $FoundBin.FullName -Destination (Join-Path $InstallDir "tunnelx.exe") -Force
    Remove-Item -Path $TempZip -Force -ErrorAction SilentlyContinue
    Remove-Item -Path $TempExtract -Recurse -Force -ErrorAction SilentlyContinue

    # Add to PATH if not present
    $UserPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if ($UserPath -notlike "*$InstallDir*") {
        [Environment]::SetEnvironmentVariable("Path", "$UserPath;$InstallDir", "User")
        $env:Path = "$env:Path;$InstallDir"
        Write-Host "=> Added $InstallDir to user PATH" -ForegroundColor Yellow
    }

    Write-Host "✓ Successfully installed tunnelx to $InstallDir\tunnelx.exe" -ForegroundColor Green
    Write-Host ""
    Write-Host "Get started with:" -ForegroundColor Cyan
    Write-Host "  tunnelx login <YOUR_TOKEN> --server tl.codesky.tech:7835"
    Write-Host "  tunnelx http 3000"
} else {
    Write-Error "Could not locate tunnelx.exe in downloaded archive"
}

