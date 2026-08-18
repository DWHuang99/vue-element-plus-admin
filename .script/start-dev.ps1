[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$projectDir = Split-Path -Parent $PSScriptRoot
$backendDir = Join-Path $projectDir "backend"
$frontendDir = Join-Path $projectDir "frontend"

if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
    throw "Docker command was not found. Please install Docker Desktop first."
}

if (-not (Get-Command pnpm -ErrorAction SilentlyContinue)) {
    throw "pnpm command was not found. Please install pnpm first."
}

if (-not (Test-Path -LiteralPath (Join-Path $frontendDir "node_modules"))) {
    throw "Frontend dependencies are missing. Run 'pnpm install' in the frontend directory first."
}

docker info *> $null
if ($LASTEXITCODE -ne 0) {
    throw "Docker Desktop is not running. Please start Docker Desktop and try again."
}

Write-Host "Starting backend services..." -ForegroundColor Cyan

Push-Location $backendDir
try {
    docker compose up -d --build
    if ($LASTEXITCODE -ne 0) {
		throw "Failed to build or start backend services."
    }
}
finally {
    Pop-Location
}

Write-Host "Gateway started: http://localhost:8080" -ForegroundColor Green
Write-Host "IAM API: http://localhost:8081" -ForegroundColor Green
Write-Host "Department API: http://localhost:8082" -ForegroundColor Green

$frontendConnection = Get-NetTCPConnection -LocalPort 4000 -State Listen -ErrorAction SilentlyContinue |
    Select-Object -First 1
if ($frontendConnection) {
    Write-Host "Frontend is already running: http://localhost:4000" -ForegroundColor Green
    exit 0
}

Write-Host "Starting frontend: http://localhost:4000" -ForegroundColor Cyan
Write-Host "Press Ctrl+C to stop the frontend dev server." -ForegroundColor DarkGray

Push-Location $frontendDir
try {
    pnpm dev
    if ($LASTEXITCODE -ne 0) {
        throw "Frontend dev server exited with an error."
    }
}
finally {
    Pop-Location
}
