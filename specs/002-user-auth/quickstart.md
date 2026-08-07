# Quickstart: 注册功能第一阶段验证指南

**Feature**: 002-user-auth
**Date**: 2026-08-05

## 概述

本指南提供从零验证注册/登录/退出/会话管理的完整步骤。前提：已完成脚手架
（001-backend-scaffold）且依赖可用。

---

## 前置条件

| 依赖 | 验证命令 | 用途 |
|------|----------|------|
| Go 1.23+ | `go version` | 后端 |
| Docker + Compose | `docker compose version` | PostgreSQL |
| Node 18+ | `node --version` | 前端 |
| pnpm | `pnpm --version` | 前端依赖 |
| curl | `curl --version` | API 验证 |

---

## 1. 启动依赖

```bash
# 后端目录
cd backend
docker compose up -d
docker compose ps
# postgres 状态 Up

# 前端目录
cd ../frontend
pnpm install
```

## 2. 后端配置

```bash
cd backend
# .env 已存在则确认 DATABASE_URL；新增限流配置
cat >> .env << 'EOF'
RATE_LIMIT_ENABLED=true
EOF
```

## 3. 运行迁移并启动后端

```bash
# 迁移在启动时自动执行（000002_users, 000003_sessions）
go run ./cmd/server/

# 预期新增日志:
# INFO database migrations completed   (应用 3 个迁移)
```

服务占用终端。另开终端继续。

## 4. 验证后端 API

### 4.1 注册

```bash
curl -s -X POST http://localhost:8080/api/v1/auth/register \
  -H "Content-Type: application/json" \
  -d '{"username":"alice","password":"supersecret123"}' | python3 -m json.tool
```

**预期**: HTTP 201，`data.token` 非空，`data.user.username="alice"`。
保存 token 供后续使用:
```bash
TOKEN=$(curl -s -X POST http://localhost:8080/api/v1/auth/register \
  -H "Content-Type: application/json" \
  -d '{"username":"bob","password":"supersecret123"}' | python3 -c "import sys,json;print(json.load(sys.stdin)['data']['token'])")
```

### 4.2 重复用户名

```bash
curl -s -o /dev/null -w "%{http_code}\n" -X POST http://localhost:8080/api/v1/auth/register \
  -H "Content-Type: application/json" -d '{"username":"alice","password":"supersecret123"}'
# 预期: 409
```

### 4.3 登录成功

```bash
curl -s -X POST http://localhost:8080/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"alice","password":"supersecret123"}' | python3 -m json.tool
# 预期: 200, data.token 非空
```

### 4.4 登录失败统一响应（防枚举）

```bash
# 错误密码 vs 不存在用户名 —— 响应必须完全一致
curl -s -X POST http://localhost:8080/api/v1/auth/login \
  -H "Content-Type: application/json" -d '{"username":"alice","password":"wrongpass"}' \
  -o /tmp/a.json -w "%{http_code}\n"
curl -s -X POST http://localhost:8080/api/v1/auth/login \
  -H "Content-Type: application/json" -d '{"username":"nobody","password":"wrongpass"}' \
  -o /tmp/b.json -w "%{http_code}\n"
diff <(python3 -m json.tool /tmp/a.json) <(python3 -m json.tool /tmp/b.json) && echo "IDENTICAL ✅"
# 预期: 两次 401, 响应结构完全一致
```

### 4.5 当前用户

```bash
curl -s http://localhost:8080/api/v1/auth/me \
  -H "Authorization: Bearer $TOKEN" | python3 -m json.tool
# 预期: 200, data.user.username="bob"
```

### 4.6 无效令牌

```bash
curl -s -o /dev/null -w "%{http_code}\n" http://localhost:8080/api/v1/auth/me \
  -H "Authorization: Bearer invalid-token-xyz"
# 预期: 401
```

### 4.7 退出并验证失效

```bash
curl -s -o /dev/null -w "%{http_code}\n" -X POST http://localhost:8080/api/v1/auth/logout \
  -H "Authorization: Bearer $TOKEN"
# 预期: 204

curl -s -o /dev/null -w "%{http_code}\n" http://localhost:8080/api/v1/auth/me \
  -H "Authorization: Bearer $TOKEN"
# 预期: 401 (令牌已撤销)
```

### 4.8 数据库安全验证

```bash
docker compose exec postgres psql -U scaffold -d scaffold_dev -c \
  "SELECT username, left(password_hash, 20) AS hash_prefix, length(password_hash) AS hash_len FROM users;"
# 预期: password_hash 以 $argon2id$ 开头，非明文

docker compose exec postgres psql -U scaffold -d scaffold_dev -c \
  "SELECT user_id, length(token_hash) AS hash_len, revoked_at FROM sessions;"
# 预期: token_hash 为 64 字符十六进制，非原始令牌
```

---

## 5. 验证前端接线

### 5.1 前端配置

```bash
# frontend/.env.development
cat > frontend/.env.development << 'EOF'
VITE_API_BASE_URL=http://localhost:8080/api/v1
EOF
```

### 5.2 启动前端

```bash
cd frontend
pnpm dev
# 浏览器打开 http://localhost:3000 (或提示的地址)
```

### 5.3 端到端验证

| 场景 | 操作 | 预期 |
|------|------|------|
| 注册 | 登录页切换到"注册"，输入新用户名 + 密码提交 | 直接进入系统（无需再登录） |
| 登录 | 退出后，用刚注册的用户名 + 密码登录 | 进入系统 |
| 错误密码 | 输入错误密码登录 | 提示统一错误信息，不区分账户是否存在 |
| 会话失效 | 手动清除浏览器 localStorage 的 token 后刷新 | 跳回登录页 |
| 角色权限 | 登录后访问菜单 | 角色/权限仍来自 Mock（本阶段不接线） |

---

## 6. 运行自动化测试

```bash
cd backend
go test ./... -v -count=1

# 预期输出包含（新增包）:
# ok  github.com/hdw/vue-element-plus-admin/backend/internal/auth     ...s
# ok  github.com/hdw/vue-element-plus-admin/backend/internal/middleware ...s
# ok  github.com/hdw/vue-element-plus-admin/backend/internal/ratelimit  ...s
```

集成测试使用 testcontainers-go 启动临时 PostgreSQL，需 Docker 运行中。

---

## 验证清单

- [ ] 步骤 3: 启动时自动应用 users/sessions 迁移
- [ ] 步骤 4.1: 注册 → 201 + token
- [ ] 步骤 4.2: 重复用户名 → 409
- [ ] 步骤 4.3/4.4: 登录成功 → 200；失败统一 401（防枚举）
- [ ] 步骤 4.5/4.6: me 有效 → 200；无效令牌 → 401
- [ ] 步骤 4.7: 退出 → 204；原令牌再访问 → 401
- [ ] 步骤 4.8: DB 中密码为 Argon2id 哈希、令牌为哈希，非明文
- [ ] 步骤 5.3: 前端注册/登录/退出/会话失效全部通过
- [ ] 步骤 6: `go test ./...` 全部通过

---

## 故障排除

| 问题 | 可能原因 | 解决方案 |
|------|----------|----------|
| 迁移失败 | users/sessions 已存在 | `docker compose down -v` 清库重建 |
| 401 但令牌正确 | 令牌已过期（24h） | 重新登录 |
| 前端请求 404 | `VITE_API_BASE_URL` 指向错误 | 确认含 `/api/v1` 前缀 |
| 限流 429 | 频繁测试触发限制 | 等待窗口或重启后端（内存限流） |
| Argon2id 内存不足 | 低内存环境 | 调低 research.md 决策 1 的 m 参数并重启 |
