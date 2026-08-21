#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
BACKEND_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"

cd "${BACKEND_DIR}"

echo "Starting IAM and Department PostgreSQL services..."
docker compose up -d iam-postgres department-postgres

echo "Running IAM database migrations..."
docker compose run --rm iam-migrate

echo "Running Department database migrations..."
docker compose run --rm department-migrate

bash "${SCRIPT_DIR}/generate-sqlc.sh"

echo "Database update completed."
