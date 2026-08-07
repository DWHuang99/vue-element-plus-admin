# Quickstart: 最小化后端脚手架验证指南

**Feature**: 001-backend-scaffold
**Date**: 2026-08-04

## 概述

本指南提供从零开始启动、验证和停止后端脚手架的完整步骤。
目标是让一名熟悉项目先决条件的开发者在 **10 分钟内** 完成全部验证操作,
全程无需修改源码。

---

## 前置条件

| 依赖 | 最低版本 | 验证命令 | 用途 |
|------|----------|----------|------|
| Go | 1.23+ | `go version` | 编译和运行 |
| Docker | 24+ | `docker --version` | 本地 PostgreSQL 和集成测试 |
| Docker Compose | v2+ | `docker compose version` | 编排本地 PostgreSQL |
| git | 2.40+ | `git --version` | 获取源码 |
| curl | 7.0+ | `curl --version` | 手动验证 API |

> **注意**: 如果你已安装并配置好 PostgreSQL 17 实例，可以跳过 Docker 步骤,
> 直接设置 `DATABASE_URL` 环境变量。本文档使用 Docker Compose 作为推荐方式。

---

## 1. 获取源码

```bash
# 在仓库根目录执行
cd /path/to/vue-element-plus-admin

# 确认分支
git branch
# 应显示: * 001-backend-scaffold (或 develop-wsl)
```

## 2. 启动本地 PostgreSQL

```bash
cd backend

# 启动 PostgreSQL 17 容器
docker compose up -d

# 验证数据库已就绪
docker compose ps
# postgres 服务状态应为 Up

# 验证连接
docker compose exec postgres pg_isready -U scaffold
# 输出: /var/run/postgresql:5432 - accepting connections
```

**首次启动**: Docker 可能需要拉取 `postgres:17-alpine` 镜像（约 50MB），请预留额外时间。

## 3. 配置环境变量

```bash
# 在 backend/ 目录创建 .env 文件
cat > .env << 'EOF'
SERVER_HOST=0.0.0.0
SERVER_PORT=8080
DATABASE_URL=postgres://scaffold:scaffold_dev@localhost:5432/scaffold_dev?sslmode=disable
LOG_LEVEL=info
LOG_FORMAT=text
EOF

# .env 已写入 .gitignore，不会被提交
```

> ⚠️ **秘密安全**: `DATABASE_URL` 包含口令。`.env` 文件只用于本地开发。
> 生产环境应通过平台提供的受控机制注入环境变量。

## 4. 安装依赖并编译

```bash
cd backend

# 下载 Go 模块依赖
go mod download

# 编译（验证代码可构建）
go build -o bin/server ./cmd/server/
```

## 5. 启动服务

```bash
# 启动服务器（自动加载 .env）
./bin/server

# 预期输出（示例）:
# 2026-08-04T10:00:00Z INFO server starting addr=0.0.0.0:8080
# 2026-08-04T10:00:00Z INFO migration completed files=1 version=1722765600
# 2026-08-04T10:00:01Z INFO database connection established max_conns=25
# 2026-08-04T10:00:01Z INFO server ready addr=0.0.0.0:8080
```

服务现在在前台运行，占用终端。打开另一个终端继续后续步骤。

## 6. 验证状态检查

### 6.1 存活检查

```bash
curl -s http://localhost:8080/health/live | python3 -m json.tool
```

**预期输出**:
```json
{
    "status": "ok",
    "timestamp": "2026-08-04T10:00:00Z"
}
```

### 6.2 就绪检查

```bash
curl -s http://localhost:8080/health/ready | python3 -m json.tool
```

**预期输出**:
```json
{
    "status": "ok",
    "timestamp": "2026-08-04T10:00:05Z",
    "checks": {
        "database": "ok"
    }
}
```

### 6.3 性能验证（可选）

```bash
# 对存活端点进行 100 次快速调用
for i in $(seq 1 100); do curl -s -o /dev/null -w "%{http_code}\n" http://localhost:8080/health/live; done | sort | uniq -c
# 预期: 100 次全部返回 200
```

## 7. 验证错误场景

### 7.1 未匹配路径

```bash
curl -s http://localhost:8080/nonexistent | python3 -m json.tool
```

**预期输出** (HTTP 404):
```json
{
    "error": {
        "code": "NOT_FOUND",
        "message": "The requested path was not found"
    }
}
```

### 7.2 依赖不可用 — 就绪失败（手动验证）

```bash
# 停止数据库
docker compose stop postgres

# 就绪检查应返回 503
curl -s -o /dev/null -w "%{http_code}\n" http://localhost:8080/health/ready
# 预期输出: 503

curl -s http://localhost:8080/health/ready | python3 -m json.tool
# 预期输出:
# {
#     "status": "degraded",
#     "timestamp": "...",
#     "checks": {
#         "database": "unavailable"
#     }
# }

# 存活检查仍然正常
curl -s -o /dev/null -w "%{http_code}\n" http://localhost:8080/health/live
# 预期输出: 200

# 恢复数据库
docker compose start postgres
# 等待几秒让数据库恢复
sleep 3

# 就绪检查应恢复 200
curl -s -o /dev/null -w "%{http_code}\n" http://localhost:8080/health/ready
# 预期输出: 200
```

### 7.3 配置缺失 — 快速失败

```bash
# 在新终端中，不设置 DATABASE_URL 启动（预期失败）
DATABASE_URL="" ./bin/server
# 预期输出 (简洁版):
# ERROR server failed to start error="required configuration DATABASE_URL is missing or empty"
# 进程退出，exit code ≠ 0
```

## 8. 验证优雅停止

```bash
# 在运行服务的终端按 Ctrl+C，或发送信号:
kill -TERM $(pgrep server)

# 预期输出:
# 2026-08-04T10:05:00Z INFO shutting down signal=SIGTERM
# 2026-08-04T10:05:01Z INFO server stopped
# 进程在 10 秒内退出
```

## 9. 运行自动化检查

```bash
cd backend

# 运行所有测试
go test ./...

# 预期输出:
# ok  github.com/.../internal/config    0.123s
# ok  github.com/.../internal/database  2.456s
# ok  github.com/.../internal/health     0.089s
# ok  github.com/.../internal/middleware  0.045s
# ok  github.com/.../internal/server     1.234s
# ok  github.com/.../internal/logging    0.067s

# 运行测试并查看覆盖率
go test -cover ./...
```

> **集成测试要求**: 集成测试使用 `testcontainers-go` 启动临时 PostgreSQL 容器,
> 因此需要 Docker 守护进程运行中。如果 Docker 不可用，仅 `config`、`middleware`、
> `logging` 的单元测试可运行。

## 10. 验证清单

完成以下所有步骤即确认脚手架功能完整:

- [ ] 步骤 2: PostgreSQL 容器启动成功
- [ ] 步骤 4: `go build` 编译成功（无错误）
- [ ] 步骤 5: 服务成功启动并报告就绪
- [ ] 步骤 6.1: GET /health/live → 200
- [ ] 步骤 6.2: GET /health/ready → 200, database=ok
- [ ] 步骤 7.1: GET /nonexistent → 404, 机器可读错误码
- [ ] 步骤 7.2: 停止数据库 → /health/ready → 503; 恢复 → 200
- [ ] 步骤 7.3: 缺失 DATABASE_URL → 启动失败，错误不含密码
- [ ] 步骤 8: Ctrl+C → 优雅停止，10 秒内退出
- [ ] 步骤 9: `go test ./...` 全部通过

---

## 清理

```bash
# 停止并删除 PostgreSQL 容器（保留数据卷）
docker compose down

# 停止、删除容器和数据卷（完全清理）
docker compose down -v

# 删除编译产物
rm -f backend/bin/server
```

---

## 故障排除

| 问题 | 可能原因 | 解决方案 |
|------|----------|----------|
| `cannot connect to Docker daemon` | Docker 未运行 | `sudo service docker start` |
| `port 5432 already in use` | 本地已有 PostgreSQL | 修改 compose 端口映射或停用本地 PG |
| `go: module not found` | Go 版本过低 | 确认 `go version` ≥ 1.23 |
| `connection refused` 就绪检查 | 数据库未就绪 | `docker compose ps` 确认容器状态 |
| testcontainers 测试失败 | Docker 权限不足 | 将用户加入 `docker` 组或使用 `sudo` |
| 迁移失败, dirty=true | 上次迁移中断 | 手动删除 `schema_migrations` 中 dirty 行 |
