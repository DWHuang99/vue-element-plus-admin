#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
BACKEND_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
SQLC_IMAGE="sqlc/sqlc:1.31.1"

generate_sqlc() {
    local database="$1"

    echo "Generating sqlc code for ${database}..."
    docker run --rm \
        --volume "${BACKEND_DIR}:/src" \
        --workdir "/src/internal/database/${database}" \
        "${SQLC_IMAGE}" generate
}

generate_sqlc iam
generate_sqlc department

echo "sqlc generation completed."
