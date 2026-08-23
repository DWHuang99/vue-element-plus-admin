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

is_wsl=false
if [[ -r /proc/sys/kernel/osrelease ]] \
  && grep -qi microsoft /proc/sys/kernel/osrelease; then
  is_wsl=true
fi

# The repository is shared with Windows and its node_modules contains Windows
# native packages. Prefer the Windows runtime in WSL even when nvm also exposes
# a Linux Node.js, otherwise Rollup tries to load a Linux optional dependency.
if [[ "$is_wsl" == true ]] \
  && command -v node.exe >/dev/null 2>&1 \
  && command -v pnpm.cmd >/dev/null 2>&1 \
  && command -v cmd.exe >/dev/null 2>&1; then
  use_windows_node=true
elif command -v node >/dev/null 2>&1 && command -v pnpm >/dev/null 2>&1; then
  use_windows_node=false
elif command -v node.exe >/dev/null 2>&1 \
  && command -v pnpm.cmd >/dev/null 2>&1 \
  && command -v cmd.exe >/dev/null 2>&1; then
  use_windows_node=true
else
  echo "Node.js and pnpm commands were not found. Please install them first."
  exit 1
fi

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

if curl --noproxy "*" --connect-timeout 2 --max-time 3 --fail --silent \
  http://127.0.0.1:4000/ >/dev/null 2>&1; then
  echo "Frontend is already running: http://localhost:4000"
  exit 0
fi

echo "Starting frontend: http://localhost:4000"
echo "Press Ctrl+C to stop the frontend dev server."
if [[ "$use_windows_node" == true ]]; then
  frontend_windows_dir="$(wslpath -w "$frontend_dir")"
  exec cmd.exe /d /c "pnpm.cmd --dir $frontend_windows_dir dev"
else
  cd "$frontend_dir"
  exec pnpm dev
fi
