#!/usr/bin/env bash
#
# dev.sh — 一键启动 vue-element-plus-admin 开发环境
#
#   后端 api + PostgreSQL   ->  docker compose（新 project：vue-element-plus-admin，
#                              镜像从本仓库 backend/cmd/server 构建）
#   前端 Vite dev server    ->  本地 pnpm dev（端口 4000）
#
# 浏览器（Windows 侧）通过 localhost 同时访问前端(4000)与后端(8080)，
# 无需设置 CORS 白名单（后端空值 = 全放行，开发模式）。
#
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE_FILE="$ROOT/backend/compose.yaml"
FRONTEND_DIR="$ROOT/frontend"
LOG_DIR="$ROOT/.dev/logs"
VITE_LOG="$LOG_DIR/vite.log"
API_CONTAINER="vue-element-plus-admin-api"
FRONTEND_URL="http://localhost:4000"

REBUILD=1
KEEP=0
# 脚本是否代为启动了 scaffold-postgres（退出时若为 1 且非 --keep 则停掉它）
POSTGRES_STARTED=0

usage() {
  cat <<'EOF'
用法: ./dev.sh [选项]

一键启动：docker 后端(api+postgres) + 本地 Vite 前端。

选项:
  --no-build   跳过后端镜像构建（复用已有镜像）
  --keep       退出时不停止 docker 容器（默认退出时 docker compose stop）
  -h, --help   显示本帮助
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --no-build) REBUILD=0; shift ;;
    --keep) KEEP=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "未知选项: $1" >&2; usage; exit 1 ;;
  esac
done

log()  { printf '\033[1;32m[dev]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[dev] 警告:\033[0m %s\n' "$*" >&2; }
err()  { printf '\033[1;31m[dev] 错误:\033[0m %s\n' "$*" >&2; }

preflight() {
  log "预检环境..."
  command -v docker >/dev/null 2>&1 || { err "docker 不可用，请先安装/启动 Docker Desktop 并启用 WSL 集成。"; exit 1; }
  docker info >/dev/null 2>&1 || { err "docker daemon 不可达，请确认 Docker Desktop 已运行。"; exit 1; }
  command -v pnpm >/dev/null 2>&1 || { err "pnpm 不可用（前端需要本地 pnpm dev）。"; exit 1; }
  [[ -f "$COMPOSE_FILE" ]] || { err "缺少 $COMPOSE_FILE（相对仓库根运行 $0）。"; exit 1; }
  if [[ ! -d "$FRONTEND_DIR/node_modules" ]]; then
    warn "frontend/node_modules 不存在，先执行 pnpm install ..."
    (cd "$FRONTEND_DIR" && pnpm install)
  fi
  if curl -fsS -o /dev/null -m 2 "$FRONTEND_URL" 2>/dev/null; then
    warn "$FRONTEND_URL 已被占用（可能残留的 vite 进程）。如需清理: pkill -f 'vite --mode base'"
  fi
  mkdir -p "$LOG_DIR"
  log "预检通过。"
}

# 复用既有 scaffold-postgres（backend/docker-compose.yml 项目，数据卷 backend_pgdata），
# 不另起空 postgres，保留已有数据。
ensure_postgres() {
  local running
  running="$(docker inspect -f '{{.State.Running}}' scaffold-postgres 2>/dev/null || true)"
  if [[ "$running" == "true" ]]; then
    log "既有 postgres (scaffold-postgres) 已在运行，直接复用（数据卷 backend_pgdata）。"
    return 0
  fi
  if docker inspect scaffold-postgres >/dev/null 2>&1; then
    log "启动既有 postgres 容器 scaffold-postgres（数据卷 backend_pgdata 保留）..."
    docker start scaffold-postgres
    POSTGRES_STARTED=1
  else
    log "scaffold-postgres 不存在，用 backend/docker-compose.yml 重建（数据卷 backend_pgdata 保留）..."
    docker compose -f "$ROOT/backend/docker-compose.yml" up -d
    POSTGRES_STARTED=1
  fi
  log "等待 postgres 就绪..."
  local i=0
  until docker exec scaffold-postgres pg_isready -U scaffold -d scaffold_dev >/dev/null 2>&1; do
    i=$((i+2))
    if [[ $i -ge 60 ]]; then
      err "postgres 60s 内未就绪。诊断: docker logs scaffold-postgres"
      return 1
    fi
    sleep 2
  done
  log "postgres 就绪。"
}

start_backend() {
  ensure_postgres

  if [[ "$REBUILD" -eq 1 ]]; then
    log "构建后端镜像（源码: backend/cmd/server）..."
    docker compose -f "$COMPOSE_FILE" build api
  else
    log "跳过构建，使用已有镜像。"
  fi

  log "启动 api 容器（连接既有 scaffold-postgres）..."
  docker compose -f "$COMPOSE_FILE" up -d

  # 就绪检查：直接在容器内探测 /health/ready，与 WSL 网络模式无关。
  log "等待后端就绪（容器内探测 /health/ready）..."
  local i=0
  until docker compose -f "$COMPOSE_FILE" exec -T api wget -qO- http://localhost:8080/health/ready >/dev/null 2>&1; do
    i=$((i+2))
    if [[ $i -ge 120 ]]; then
      err "后端 120s 内未就绪。诊断: docker compose -f $COMPOSE_FILE logs api"
      return 1
    fi
    sleep 2
  done
  log "后端就绪（约 ${i}s），API 位于 http://localhost:8080"
}

start_frontend() {
  log "启动前端 (pnpm dev) -> $FRONTEND_URL ..."
  # 开启 job control，让前端进程拥有独立进程组，便于整组退出。
  set -m
  (cd "$FRONTEND_DIR" && exec pnpm dev) >"$VITE_LOG" 2>&1 &
  VITE_PID=$!
  set +m

  log "等待前端就绪（日志: $VITE_LOG）..."
  local i=0
  until curl -fsS -o /dev/null "$FRONTEND_URL" 2>/dev/null; do
    if ! kill -0 "$VITE_PID" 2>/dev/null; then
      err "前端进程已退出，见日志: $VITE_LOG"
      return 1
    fi
    i=$((i+2))
    if [[ $i -ge 60 ]]; then
      err "前端 60s 内未就绪，见日志: $VITE_LOG"
      return 1
    fi
    sleep 2
  done
  log "前端就绪（约 ${i}s）。"
}

open_browser() {
  local url="$1"
  if command -v cmd.exe >/dev/null 2>&1; then
    timeout 10 cmd.exe /c "start \"\" \"$url\"" >/dev/null 2>&1 && return 0
  fi
  if command -v powershell.exe >/dev/null 2>&1; then
    timeout 10 powershell.exe -NoProfile -Command "Start-Process '$url'" >/dev/null 2>&1 && return 0
  fi
  if command -v xdg-open >/dev/null 2>&1; then
    xdg-open "$url" >/dev/null 2>&1 && return 0
  fi
  return 1
}

cleanup() {
  log "正在停止..."
  if [[ -n "${VITE_PID:-}" ]]; then
    kill -- -"$VITE_PID" 2>/dev/null || kill "$VITE_PID" 2>/dev/null || true
    wait "$VITE_PID" 2>/dev/null || true
  fi
  if [[ "$KEEP" -eq 0 ]]; then
    log "docker compose stop（加 --keep 可保留容器运行）..."
    docker compose -f "$COMPOSE_FILE" stop >/dev/null 2>&1 || true
    if [[ "$POSTGRES_STARTED" -eq 1 ]]; then
      log "停止脚本代为启动的 scaffold-postgres ..."
      docker stop scaffold-postgres >/dev/null 2>&1 || true
    fi
  else
    log "按 --keep 保留容器运行：docker compose -f $COMPOSE_FILE ps"
  fi
  log "已退出。"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

main() {
  preflight
  start_backend
  start_frontend

  log "--------------------------------------------------"
  log "  前端: $FRONTEND_URL"
  log "  后端: http://localhost:8080 （/health/live, /health/ready）"
  log "  退出: Ctrl+C （停止前端，docker 容器默认一并 stop）"
  log "--------------------------------------------------"

  if [[ "$REBUILD" -eq 0 ]]; then
    log "提示: 后端镜像未重建；如需包含最新后端代码请去掉 --no-build 或执行 ./dev.sh。"
  fi

  open_browser "$FRONTEND_URL" || log "已打印地址，请手动打开浏览器访问 $FRONTEND_URL"

  set +e
  wait "$VITE_PID"
  set -e
  log "前端已停止，退出。"
}

main "$@"
