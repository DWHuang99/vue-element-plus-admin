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
| GET | `/api/v1/auth/me` | 当前用户、角色、部门与有效权限（需 Bearer 令牌） |
| GET | `/api/v1/roles` | 角色列表（需 `roles.read`） |
| POST | `/api/v1/roles` | 新建/编辑角色，`{id?, name, code}`（需 `roles.write`） |
| POST | `/api/v1/roles/delete` | 批量删除角色，`{ids: number[]}`（需 `roles.write`） |
| GET | `/api/v1/departments` | 部门树（需 `departments.read`） |
| POST | `/api/v1/departments` | 新建/编辑部门，`{id?, name, parent_id?}`（需 `departments.write`） |
| POST | `/api/v1/departments/delete` | 批量删除部门，`{ids: number[]}`（需 `departments.write`） |
| GET | `/api/v1/users` | 用户分页列表，`?department_id&username&account&page_index&page_size`（需 `users.read`） |
| POST | `/api/v1/users` | 新建/编辑用户，`{id?, username, account, password?, email?, department_id?, roles}`（需 `users.write`） |
| POST | `/api/v1/users/delete` | 批量删除用户，`{ids: number[]}`（需 `users.write`） |

健康检查契约见 [Health Check API 契约](../specs/001-backend-scaffold/contracts/health-api.md)；
认证契约见 [Auth API 契约](../specs/002-user-auth/contracts/auth-api.md)；
RBAC 管理接口契约见 [RBAC API 契约](../specs/003-rbac-permission-management/contracts/rbac-api.md)。

### 管理接口约定

- **认证**：管理接口均需携带 `Authorization: Bearer <token>`；令牌缺失或无效返回 `401 AUTH_INVALID_TOKEN`。
- **授权**：9 个管理端点按 `roles.*`、`departments.*`、`users.*` 的 read/write 权限实时校验；无权限返回 `403 AUTH_FORBIDDEN`，不会清除登录态。
- **第一代内置策略**：`admin` 和 `super_admin` 同时拥有全部 6 个管理权限；`user` 与自定义角色默认无管理权限。
- **写接口（POST）统一返回 `200 {"data":{}}`**，不返回 204 空响应体——前端 axios 拦截器依赖非空 `data` 判定成功。
- **错误统一封装**：`{"error":{"code","message","field_errors"}}`，字段校验失败时 `field_errors` 携带 `{field, code, message}` 列表。
- **编辑与新建共用一个写接口**：请求体带 `id` 即更新，缺省 `id` 即创建（角色/部门/用户一致）。
- **内置角色保护**：`super_admin`、`admin`、`user` 的角色码不可修改，角色不可删除。

### 初始化首个管理员

系统不提供默认 `admin/admin`。先正常注册用户，再从 `backend/` 运行一次性 CLI：

```bash
DATABASE_URL='postgres://...' go run ./cmd/admin-init --username alice --role admin
```

`--role` 只接受 `admin` 或 `super_admin`；命令不会接收或设置密码，并且可以安全重复执行。受控 SQL 备选步骤见 [管理员初始化运维文档](docs/operations/admin-init.md)。

## 项目结构

```
backend/
├── cmd/
│   ├── server/                  # 应用入口：migrations → startup gates → wire → server
│   │   ├── main.go              # 入口
│   │   └── gates.go             # US5 启动门禁（组合安全校验，Platform-owned US5 读取）
│   └── admin-init/main.go      # 已注册用户管理员角色初始化 CLI（不接受/不设置密码）
├── internal/
│   ├── app/adminapi/           # 单一进程组合根（唯一允许 import 全部具体 adapter 的包）
│   │   ├── wire.go             # 共享组合根：Config/Wire/NewWire/adminBFFRouter/StartBackground
│   │   ├── wire_default.go     # `!rollback` 变体：发货 build 恒 Admin BFF router，shadow 禁用
│   │   ├── wire_legacy.go      # `rollback` 变体：legacy fallback + shadow reads + delete delegate
│   │   └── legacy_delete_delegate.go  # [rollback] legacy 删除委托
│   ├── iam/                     # IAM 域（认证/会话/托管用户生命周期 + outbox 投递）
│   │   └── postgres/            # IAM 持久化适配器 + sqlc 生成代码（勿手工修改）
│   ├── organization/            # Organization 域（部门/成员关系 + inbox consumer）
│   │   └── postgres/            # Organization 持久化适配器 + sqlc 生成代码（勿手工修改）
│   ├── adminbff/                # Admin BFF 编排服务
│   │   ├── postgres/            # workflow store 适配器 + sqlc 生成代码（勿手工修改）
│   │   └── transport/http/      # BFF HTTP 路由/中间件/兼容性测试套件
│   ├── integration/             # outbox dispatcher、envelope codec、crash-matrix 测试
│   ├── platform/                # 平台能力：rollout-gate/evidence-cleanup/requeue
│   │   └── observability/       # 进程内 metrics 注册表（T074）
│   ├── database/                # PostgreSQL 连接池、迁移、sqlc 生成代码（勿手工修改）
│   ├── config/config.go         # 配置加载与校验
│   ├── health/                  # 健康检查处理器
│   ├── logging/                 # 结构化日志
│   ├── middleware/              # 共享中间件：request_id/cors/metrics（untagged）
│   ├── ratelimit/               # 内存滑动窗口限流（BFF 路由共用）
│   ├── server/                  # HTTP 服务器 + 共享类型（LegacyRateLimitConfig）
│   ├── architecture/            # 架构所有权测试（T009）
│   ├── consistency/             # 一致性检查
│   ├── auth/                    # [rollback] legacy 认证服务层（注册/登录/会话/令牌）
│   ├── rbac/                    # [rollback] legacy RBAC 管理服务层（角色/部门/用户）
│   ├── authorization/           # [rollback] legacy 权限检查
│   └── middleware/{auth,authorization}.go  # [rollback] legacy 认证/授权中间件
├── db/
│   ├── migrations/              # SQL 迁移文件（000001–000013，000004/000005 冻结不回滚）
│   └── queries/                 # sqlc 查询文件
├── docker-compose.yml
├── Makefile
└── sqlc.yaml
```

### 构建变体（build-tag gating，T082）

- **默认 build（发货二进制）**：BFF-only。`internal/auth`、`internal/rbac`、`internal/authorization`、legacy router（`server.NewLegacyRouter`）与 legacy middleware 带 `//go:build rollback`，不参与编译——发货二进制物理上无法 serve legacy routes。`go build ./...` / `go test ./...` 即此变体。
- **`-tags rollback` build**：完整 legacy wiring + 兼容套件双 wiring + shadow reads + legacy wire tests，供回滚支持窗口使用：`go build -tags rollback ./...` / `go test -tags rollback ./internal/... ./cmd/...`。
- `internal/auth`/`rbac`/`authorization` 在默认 build 的 `go list ./...` 中报 "build constraints exclude all Go files"（预期）。

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
| `IAM_DATABASE_URL` / `ORGANIZATION_DATABASE_URL` / `ADMIN_BFF_DATABASE_URL` | 同 `DATABASE_URL` | 模块级连接串；未设置时回退 `DATABASE_URL`，三者必须指向同一物理 database（启动校验） |

### US5 微服务拆分开关（全部默认关闭，安全默认）

| 环境变量 | 说明 |
|----------|------|
| `ADMIN_BFF_ROUTES_ENABLED` | master kill switch：关闭时强制关闭全部 granular BFF 能力 |
| `ADMIN_BFF_SHADOW_READS` | BFF 纯读端点 shadow 对比（绝不 shadow 认证路径） |
| `ADMIN_BFF_AUTH_PROFILE_READS_ENABLED` | BFF auth/profile 读能力 |
| `ADMIN_BFF_USER_LIST_READS_ENABLED` | BFF 用户列表读能力 |
| `ADMIN_BFF_DEPARTMENT_WRITES_ENABLED` | BFF 部门写能力 |
| `ADMIN_BFF_ROLE_WRITES_ENABLED` | BFF 角色写能力 |
| `ADMIN_BFF_MANAGED_USER_WRITES_ENABLED` | BFF 托管用户 create/update（不含 delete） |
| `ADMIN_BFF_USER_DELETE_ROUTE_ENABLED` | BFF `/users/delete` 路由，独立于 create/update；须 delegation=true 且 bridge mode=false 且 consumer/dispatcher 就绪才可打开（启动门禁） |
| `LEGACY_DELETE_IAM_DELEGATION_ENABLED` | legacy `/users/delete` 委托 IAM 删除 + outbox |
| `IAM_DELETE_EVENT_CONSUMER_ENABLED` | Organization inbox 接受 `iam.user.deleted` 事件；须 delegation=true 且 bridge mode=false |
| `OUTBOX_DISPATCHER_ENABLED` | 进程内 outbox dispatcher；须 delegation=true 且 bridge mode=false |

启动时（`cmd/server/gates.go`）校验组合安全性；`compatibility_bridge_mode.legacy_delete_sync_enabled=false` 由授权 Platform 操作设置（迁移 000008 的 `set_legacy_delete_sync_mode`）。

## 常用命令

```bash
make build          # 编译（发货 build）
make run            # 运行
make test           # 运行所有测试（默认 build，需要 Docker）
make test-unit      # 运行单元测试
make test-integration # 运行集成测试（需要 Docker）
make lint           # 代码检查
make clean          # 清理产物
make sqlc-generate  # 重新生成 sqlc 代码（唯一合法的 sqlc 更新路径）

# 回滚支持窗口变体（-tags rollback，保留 legacy wiring）
go build -tags rollback ./...
go test -tags rollback ./internal/... ./cmd/... -count=1   # 需 Docker
go vet -tags rollback ./...
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
- **sqlc 1.31** — SQL 代码生成
- **golang-migrate 4.18** — 数据库迁移
- **Viper 1.19** — 配置管理
- **log/slog** — 结构化日志
- **testify** — 测试断言
- **testcontainers-go** — 集成测试

## 安全

- 日志和错误响应不包含密码、令牌或敏感配置值
- 数据库密码仅通过 `DATABASE_URL` 环境变量传入
- `.env` 文件已加入 `.gitignore`，不会被提交
