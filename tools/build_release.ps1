# Builds the release archives in dist/ from the current source tree.
# Usage: .\tools\build_release.ps1
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$dist = Join-Path $root "dist"
New-Item -ItemType Directory -Force -Path $dist | Out-Null
$stageRoot = Join-Path $env:TEMP "opencode\tt-release"
$ndk = "C:\Users\Julian\AppData\Local\Android\Sdk\ndk\30.0.15729638\toolchains\llvm\prebuilt\windows-x86_64\bin"

$readme = @"
TurtleTavern (Go) 0.1.2-beta
============================

Run the server binary and open http://127.0.0.1:8000/ in your browser.

  public/       the web frontend
  default/      per-user data templates
  config.yaml   server settings (edit before starting)

Windows: gotavern.exe
Linux/macOS/Termux: ./gotavern  (chmod +x first on unix-like systems)

Migrating from the Node.js version? Export a backup in the old server
(User Settings -> Account -> Download Backup) and import that zip in the
Go server at the same place. Formats are compatible in both directions.
"@

function New-Stage {
    param([string]$name)
    $stage = Join-Path $stageRoot $name
    Remove-Item -LiteralPath $stage -Recurse -Force -ErrorAction SilentlyContinue
    New-Item -ItemType Directory -Force -Path $stage | Out-Null
    robocopy (Join-Path $root "public") (Join-Path $stage "public") /E /NFL /NDL /NJH /NJS /NP | Out-Null
    robocopy (Join-Path $root "default") (Join-Path $stage "default") /E /NFL /NDL /NJH /NJS /NP | Out-Null
    Copy-Item (Join-Path $root "config.yaml") $stage -Force
    Set-Content -Path (Join-Path $stage "README.txt") -Value $readme -Encoding UTF8
    return $stage
}

function Build-Go {
    param([string]$goos, [string]$goarch, [string]$outFile, [string]$cc = "")
    $env:CGO_ENABLED = if ($cc) { "1" } else { "0" }
    $env:GOOS = $goos
    $env:GOARCH = $goarch
    if ($cc) { $env:CC = $cc }
    & go build -trimpath -ldflags "-s -w" -o $outFile ./cmd/server
    if ($LASTEXITCODE -ne 0) { throw "build failed for $goos/$goarch" }
    $env:CGO_ENABLED = ""
    $env:GOOS = ""
    $env:GOARCH = ""
    $env:CC = ""
}

Push-Location $root
try {
    # Windows x64
    $stage = New-Stage "windows-x64"
    Build-Go "windows" "amd64" (Join-Path $stage "gotavern.exe")
    Remove-Item (Join-Path $dist "GoTavern-Windows-x64.zip") -Force -ErrorAction SilentlyContinue
    Compress-Archive -Path (Join-Path $stage "*") -DestinationPath (Join-Path $dist "GoTavern-Windows-x64.zip") -Force

    # Linux x64 / arm64
    foreach ($arch in @(@("amd64", "x64"), @("arm64", "arm64"))) {
        $stage = New-Stage "linux-$($arch[1])"
        Build-Go "linux" $arch[0] (Join-Path $stage "gotavern")
        Remove-Item (Join-Path $dist "GoTavern-Linux-$($arch[1]).tar.gz") -Force -ErrorAction SilentlyContinue
        tar -czf (Join-Path $dist "GoTavern-Linux-$($arch[1]).tar.gz") -C $stage .
    }

    # macOS x64 / arm64
    foreach ($arch in @(@("amd64", "x64"), @("arm64", "arm64"))) {
        $stage = New-Stage "macos-$($arch[1])"
        Build-Go "darwin" $arch[0] (Join-Path $stage "gotavern")
        Remove-Item (Join-Path $dist "GoTavern-macOS-$($arch[1]).tar.gz") -Force -ErrorAction SilentlyContinue
        tar -czf (Join-Path $dist "GoTavern-macOS-$($arch[1]).tar.gz") -C $stage .
    }

    # Termux / Android arm64 (CGO required: DNS + TLS via Bionic)
    $stage = New-Stage "termux-arm64"
    Build-Go "android" "arm64" (Join-Path $stage "gotavern") (Join-Path $ndk "aarch64-linux-android21-clang.cmd")
    Remove-Item (Join-Path $dist "GoTavern-Termux-arm64.tar.gz") -Force -ErrorAction SilentlyContinue
    tar -czf (Join-Path $dist "GoTavern-Termux-arm64.tar.gz") -C $stage .
} finally {
    Pop-Location
    Remove-Item -LiteralPath $stageRoot -Recurse -Force -ErrorAction SilentlyContinue
}

Get-ChildItem $dist | Select-Object Name, @{n = "MB"; e = { [math]::Round($_.Length / 1MB, 1) } }
