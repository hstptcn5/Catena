param(
    [string]$Version = "0.3.0",
    [string]$OutputDir = "dist"
)

$ErrorActionPreference = "Stop"

$root = Split-Path -Parent $PSScriptRoot
$dist = Join-Path $root $OutputDir

if (Test-Path -LiteralPath $dist) {
    Remove-Item -LiteralPath $dist -Recurse -Force
}
New-Item -ItemType Directory -Path $dist | Out-Null

$targets = @(
    @{ GOOS = "windows"; GOARCH = "amd64"; Ext = ".exe" },
    @{ GOOS = "linux"; GOARCH = "amd64"; Ext = "" },
    @{ GOOS = "darwin"; GOARCH = "amd64"; Ext = "" },
    @{ GOOS = "darwin"; GOARCH = "arm64"; Ext = "" }
)

foreach ($target in $targets) {
    $name = "catena-$Version-$($target.GOOS)-$($target.GOARCH)$($target.Ext)"
    $out = Join-Path $dist $name

    Write-Host "Building $name"
    $env:CGO_ENABLED = "0"
    $env:GOOS = $target.GOOS
    $env:GOARCH = $target.GOARCH
    go build -trimpath -ldflags="-s -w" -o $out .
}

$env:GOOS = ""
$env:GOARCH = ""
$env:CGO_ENABLED = ""

Get-ChildItem -LiteralPath $dist -File | Get-FileHash -Algorithm SHA256 |
    ForEach-Object { "$($_.Hash.ToLower())  $(Split-Path -Leaf $_.Path)" } |
    Set-Content -LiteralPath (Join-Path $dist "SHA256SUMS.txt")

Write-Host "Release artifacts written to $dist"
