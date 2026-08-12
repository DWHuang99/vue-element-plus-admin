Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$backendDir = Split-Path -Parent $PSScriptRoot

Push-Location $backendDir
try {
    docker compose up -d postgres
    if ($LASTEXITCODE -ne 0) {
        throw "Failed to start PostgreSQL."
    }

    docker compose run --rm migrate
    if ($LASTEXITCODE -ne 0) {
        throw "Database migration failed."
    }

    docker run --rm `
        --volume "${backendDir}:/src" `
        --workdir /src/internal/database `
        sqlc/sqlc:1.31.1 generate
    if ($LASTEXITCODE -ne 0) {
        throw "sqlc generation failed."
    }
}
finally {
    Pop-Location
}
