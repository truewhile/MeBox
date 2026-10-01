param(
    [string]$Output = "dist\mebox-windows-amd64.exe",
    [string]$Version = "dev",
    [switch]$SkipFrontend
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Push-Location $root
try {
    if (-not $SkipFrontend) {
        Push-Location web
        try {
            npm ci
            npm run build
        }
        finally {
            Pop-Location
        }
    }

    if (-not (Test-Path "web\dist\index.html")) {
        throw "web\dist\index.html is missing. Run without -SkipFrontend or build the frontend first."
    }

    $outputDir = Split-Path -Parent $Output
    if ($outputDir) {
        New-Item -ItemType Directory -Force -Path $outputDir | Out-Null
    }
    $env:CGO_ENABLED = "0"
    $env:GOOS = "windows"
    $env:GOARCH = "amd64"
    go build -trimpath `
        -ldflags="-s -w -H=windowsgui -X main.version=$Version" `
        -o $Output `
        ./cmd/server

    Write-Host "Built $Output"
}
finally {
    Pop-Location
}
