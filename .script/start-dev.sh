#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
project_dir="$(cd -- "$script_dir/.." && pwd)"
backend_dir="$project_dir/backend"
frontend_dir="$project_dir/frontend"

command -v docker >/dev/null 2>&1 || {
  echo "Docker command was not found. Please install Docker Desktop first."
  exit 1
}

command -v pnpm >/dev/null 2>&1 || {
  echo "pnpm command was not found. Please install pnpm first."
  exit 1
}

if [[ ! -d "$frontend_dir/node_modules" ]]; then
  echo "Frontend dependencies are missing. Run 'pnpm install' in the frontend directory first."
  exit 1
fi

if ! docker info >/dev/null 2>&1; then
  echo "Docker Desktop is not running. Please start Docker Desktop and try again."
  exit 1
fi

echo "Starting backend services..."
cd "$backend_dir"
docker compose up -d --build

echo "Gateway started: http://localhost:8080"
echo "IAM API: http://localhost:8081"
echo "Department API: http://localhost:8082"

if curl --noproxy "*" --fail --silent http://127.0.0.1:4000/ >/dev/null 2>&1; then
  echo "Frontend is already running: http://localhost:4000"
  exit 0
fi

echo "Starting frontend: http://localhost:4000"
echo "Press Ctrl+C to stop the frontend dev server."
cd "$frontend_dir"
exec pnpm dev
