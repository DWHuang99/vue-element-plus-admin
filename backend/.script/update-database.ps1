Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$backendDir = Split-Path -Parent $PSScriptRoot

Push-Location $backendDir
try {
    docker compose up -d iam-postgres department-postgres
    if ($LASTEXITCODE -ne 0) {
        throw "Failed to start IAM and Department PostgreSQL services."
    }

    docker compose run --rm iam-migrate
    if ($LASTEXITCODE -ne 0) {
        throw "IAM database migration failed."
    }

    docker compose run --rm department-migrate
    if ($LASTEXITCODE -ne 0) {
        throw "Department database migration failed."
    }

    docker run --rm `
        --volume "${backendDir}:/src" `
        --workdir /src/internal/database/iam `
        sqlc/sqlc:1.31.1 generate
    if ($LASTEXITCODE -ne 0) {
        throw "IAM sqlc generation failed."
    }

    docker run --rm `
        --volume "${backendDir}:/src" `
        --workdir /src/internal/database/department `
        sqlc/sqlc:1.31.1 generate
    if ($LASTEXITCODE -ne 0) {
        throw "Department sqlc generation failed."
    }
}
finally {
    Pop-Location
}
