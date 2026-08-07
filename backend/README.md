# Backend - 最小化后端脚手架

基于 Go 1.23 + Gin + PostgreSQL + sqlc 的最小化后端服务基线。

## 前置条件

| 依赖 | 最低版本 | 验证命令 |
|------|----------|----------|
| Go | 1.23+ | `go version` |
| Docker | 24+ | `docker --version` |
| Docker Compose | v2+ | `docker compose version` |

## 快速开始

### 1. 启动 PostgreSQL

```bash
docker compose up -d
```

### 2. 配置环境

```bash
cp .env.example .env
# 编辑 .env 按需修改配置
```

### 3. 构建并运行

```bash
go mod tidy
make build
./bin/server
```

或直接运行：

```bash
go run ./cmd/server/
```

预期输出：
```
INFO configuration loaded
INFO database connection established max_conns=25 min_conns=5
INFO database migrations completed
INFO server ready addr=0.0.0.0:8080
```

### 4. 验证服务

```bash
# 存活检查
curl http://localhost:8080/health/live

# 就绪检查
curl http://localhost:8080/health/ready
```

详细验证步骤见 [Quickstart 验证指南](../specs/001-backend-scaffold/quickstart.md)。

## API 端点

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/health/live` | 存活检查 — 进程是否运行 |
| GET | `/health/ready` | 就绪检查 — 依赖是否可用 |
| POST | `/api/v1/auth/register` | 注册（注册即登录，返回会话令牌） |
| POST | `/api/v1/auth/login` | 登录（返回会话令牌 + 用户信息） |
| POST | `/api/v1/auth/logout` | 退出（撤销当前会话，幂等） |
| GET | `/api/v1/auth/me` | 当前用户信息（需 Bearer 令牌） |
| GET | `/api/v1/roles` | 角色列表（需 Bearer 令牌） |
| POST | `/api/v1/roles` | 新建/编辑角色，`{id?, name, code}`（需 Bearer 令牌） |
| POST | `/api/v1/roles/delete` | 批量删除角色，`{ids: number[]}`（需 Bearer 令牌） |
| GET | `/api/v1/departments` | 部门树（需 Bearer 令牌） |
| POST | `/api/v1/departments` | 新建/编辑部门，`{id?, name, parent_id?}`（需 Bearer 令牌） |
| POST | `/api/v1/departments/delete` | 批量删除部门，`{ids: number[]}`（需 Bearer 令牌） |
| GET | `/api/v1/users` | 用户分页列表，`?id&keyword&page_index&page_size`（需 Bearer 令牌） |
| POST | `/api/v1/users` | 新建/编辑用户，`{id?, username, account, password?, email?, department_id?}`（需 Bearer 令牌） |
| POST | `/api/v1/users/delete` | 批量删除用户，`{ids: number[]}`（需 Bearer 令牌） |

健康检查契约见 [Health Check API 契约](../specs/001-backend-scaffold/contracts/health-api.md)；
认证契约见 [Auth API 契约](../specs/002-user-auth/contracts/auth-api.md)；
RBAC 管理接口契约见 [RBAC API 契约](../specs/003-rbac-permission-management/contracts/rbac-api.md)。

### 管理接口约定

- **鉴权**：除 `/api/v1/auth/*`（register/login/logout）外，所有 `/api/v1/*` 接口均需携带 `Authorization: Bearer <token>`。
- **写接口（POST）统一返回 `200 {"data":{}}`**，不返回 204 空响应体——前端 axios 拦截器依赖非空 `data` 判定成功。
- **错误统一封装**：`{"error":{"code","message","field_errors"}}`，字段校验失败时 `field_errors` 携带 `{field, code, message}` 列表。
- **编辑与新建共用一个写接口**：请求体带 `id` 即更新，缺省 `id` 即创建（角色/部门/用户一致）。

## 项目结构

```
backend/
├── cmd/server/main.go          # 应用入口
├── internal/
│   ├── auth/                   # 认证服务层（注册/登录/退出/令牌）
│   │   ├── service.go          # 业务逻辑（服务层）
│   │   ├── password.go         # Argon2id 密码哈希
│   │   ├── token.go            # 会话令牌生成/哈希/生命周期
│   │   ├── handler.go          # Gin HTTP 适配器
│   │   └── errors.go           # 哨兵错误（契约错误码映射）
│   ├── config/config.go         # 配置加载与校验
│   ├── rbac/                    # RBAC 管理服务层（角色/部门/用户）
│   ├── database/
│   │   ├── db.go                # PostgreSQL 连接池
│   │   ├── migrate.go           # 数据库迁移
│   │   └── sqlc/                # sqlc 生成代码（勿手工修改）
│   ├── health/handler.go        # 健康检查处理器
│   ├── logging/logger.go        # 结构化日志
│   ├── middleware/
│   │   ├── auth.go              # Bearer 令牌认证中间件
│   │   ├── request_id.go        # 请求 ID 中间件
│   │   └── cors.go              # 跨域中间件
│   ├── ratelimit/               # 内存滑动窗口限流
│   └── server/server.go         # HTTP 服务器
├── db/
│   ├── migrations/              # SQL 迁移文件
│   └── queries/                 # sqlc 查询文件
├── docker-compose.yml
├── Makefile
└── sqlc.yaml
```

## 配置

所有配置通过环境变量提供，支持 `.env` 文件加载。

| 环境变量 | 默认值 | 说明 |
|----------|--------|------|
| `SERVER_HOST` | `0.0.0.0` | 监听地址 |
| `SERVER_PORT` | `8080` | 监听端口 |
| `DATABASE_URL` | (必填) | PostgreSQL 连接 URL |
| `DATABASE_MAX_CONNS` | `25` | 最大连接数 |
| `DATABASE_MIN_CONNS` | `5` | 最小连接数 |
| `LOG_LEVEL` | `info` | 日志级别 (debug/info/warn/error) |
| `LOG_FORMAT` | `text` | 日志格式 (text/json) |
| `RATE_LIMIT_ENABLED` | `true` | 是否启用注册/登录限流 |
| `CORS_ALLOWED_ORIGINS` | 空 | 跨域白名单（逗号分隔；空=允许所有，仅开发） |

## 常用命令

```bash
make build          # 编译
make run            # 运行
make test           # 运行所有测试
make test-unit      # 运行单元测试
make test-integration # 运行集成测试（需要 Docker）
make lint           # 代码检查
make clean          # 清理产物
```

## 迁移

数据库迁移在服务启动时自动执行。手动迁移：

```bash
# 创建新迁移
make migrate-create NAME=add_users_table

# 应用迁移
make migrate-up

# 回滚迁移
make migrate-down
```

## 技术栈

- **Go 1.23** — 语言
- **Gin 1.10** — HTTP 框架
- **PostgreSQL 17** — 数据库
- **pgx v5** — PostgreSQL 驱动
- **sqlc 1.28** — SQL 代码生成
- **golang-migrate 4.18** — 数据库迁移
- **Viper 1.19** — 配置管理
- **log/slog** — 结构化日志
- **testify** — 测试断言
- **testcontainers-go** — 集成测试

## 安全

- 日志和错误响应不包含密码、令牌或敏感配置值
- 数据库密码仅通过 `DATABASE_URL` 环境变量传入
- `.env` 文件已加入 `.gitignore`，不会被提交
