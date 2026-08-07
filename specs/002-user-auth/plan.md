# Implementation Plan: 注册功能第一阶段（用户认证）

**Branch**: `002-user-auth` | **Date**: 2026-08-05 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `/specs/002-user-auth/spec.md`

**Note**: This template is filled in by the `/speckit-plan` command; its definition describes the execution workflow.

## Summary

在已完成的最小化后端脚手架（Go + Gin + PostgreSQL + sqlc）基础上，实现注册功能第一阶段：
基础注册、密码登录、退出与必要的服务端会话管理，并将前端认证页面（登录/注册/退出）从 Mock
切换到真实后端。会话采用服务端不透明令牌（Bearer）；密码使用 Argon2id 自适应哈希 + 独立盐；
注册/登录接口内置内存滑动窗口限流防暴力破解与账户枚举。本阶段明确不含验证码、邮箱验证、
角色权限授权与刷新令牌。

**技术决策核心**（详见 research.md）:
- 认证逻辑置于可独立测试的服务层（宪章 IV）
- Argon2id 密码哈希（OWASP 首选），PHC 编码格式便于参数升级
- 服务端不透明令牌 + SHA-256 存储哈希（泄漏安全）+ 24h 绝对过期 + 滑动续期 + 撤销
- 登录失败统一响应防账户枚举（宪章 I）
- 内存滑动窗口限流（单实例，配置化开关）
- 前端仅认证相关接口接线，角色/权限保持 Mock（宪章 II 阶段边界）

## Technical Context

**Language/Version**: Go 1.23（后端）· TypeScript/Vue 3（前端接线）

**Primary Dependencies**:
- 新增: `golang.org/x/crypto` (argon2)
- 复用现有: gin v1.10, pgx/v5, sqlc v1.28, golang-migrate v4.18, viper v1.19, testify, testcontainers-go
- 前端复用现有: axios（已有拦截器架构）、pinia user store

**Storage**: PostgreSQL 17 — 新增 `users` 与 `sessions` 表，由 sqlc 生成类型安全访问代码

**Testing**: testify 单元测试 + testcontainers-go 真实 PostgreSQL 集成测试 + httptest 契约测试

**Target Platform**: Linux 服务器（开发期 WSL2/本机 Docker PostgreSQL）

**Project Type**: Web 服务（后端 API + 前端 SPA 接线）

**Performance Goals**:
- 认证接口（注册/登录）P95 < 200ms（含 Argon2id 哈希计算）
- 认证中间件对受保护请求的额外开销 < 5ms
- 登录/注册响应在 1 秒内可判定（SC-008）

**Constraints**:
- 密码哈希计算应平衡安全与响应时间（Argon2id 参数: t=3, m=64MB, p=4）
- 零秘密泄露：日志、错误响应不含密码/令牌/内部路径
- 不引入本阶段未使用的抽象（宪章 II）
- 数据库完整性约束兜底应用层校验（宪章 III）

**Scale/Scope**:
- 单实例部署；内存限流足够
- 预计新增 ~20 个源文件（后端 auth/ratelimit/middleware + 前端 api/store 修改）
- 2 个新迁移文件、2 组 sqlc 查询

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| # | 原则 | 合规状态 | 说明 |
|---|------|----------|------|
| I | **身份安全优先** | ✅ PASS | Argon2id 自适应哈希 + 每账户独立盐（FR-003）；登录统一失败防枚举（FR-005）；注册/登录限流（FR-006）；令牌绝对过期 + 滑动续期 + 撤销（FR-007/008/009）；会话凭据仅存哈希（FR-013）；安全事件可审计且隐私最小化（FR-014）。所有宪章 I 关键控制项在本阶段落地。 |
| II | **分阶段交付与边界控制** | ✅ PASS | 本功能是宪章规定的认证第一阶段，形成可独立运行的最小闭环。不含验证码/邮箱验证（阶段 2）、OAuth/SSO（阶段 3）、角色权限（独立功能）。服务层不引入未来阶段抽象；限流基于内存（后续扩展可换）。 |
| III | **PostgreSQL 与 sqlc 契约化数据访问** | ✅ PASS | users/sessions 表由版本化迁移描述，sqlc 生成类型安全代码。唯一约束、外键（sessions.user_id → users.id ON DELETE CASCADE）在 SQL 中显式表达。多写操作（注册=建用户+建会话）在单事务中保持原子。生成文件不手工修改。 |
| IV | **API 契约与前后端解耦** | ✅ PASS | 明确的请求/响应/错误模型（`/api/v1/auth/*`），不暴露 DB 行结构。统一机器可读错误码。认证与业务规则在服务层，HTTP 处理器只做协议适配与校验。契约稳定使前端从 Mock 迁移。 |
| V | **可验证、可观测与隐私保护** | ✅ PASS | 单元（password/token/service）+ 集成（testcontainers 全流程 + 安全场景）+ 契约（httptest 错误码）三层测试。结构化日志 + 请求关联，认证事件可审计，日志不含秘密。 |

**Gate Result**: ✅ ALL GATES PASS — no violations to justify.

## Project Structure

### Documentation (this feature)

```text
specs/002-user-auth/
├── plan.md              # This file (/speckit-plan command output)
├── research.md          # Phase 0 output (/speckit-plan command)
├── data-model.md        # Phase 1 output (/speckit-plan command)
├── quickstart.md        # Phase 1 output (/speckit-plan command)
├── contracts/           # Phase 1 output (/speckit-plan command)
│   └── auth-api.md      # Auth API contract
└── tasks.md             # Phase 2 output (/speckit-tasks command - NOT created by /speckit-plan)
```

### Source Code (repository root)

```text
backend/
├── cmd/server/main.go                # 修改：注册 auth 路由、挂载限流中间件
├── internal/
│   ├── auth/
│   │   ├── service.go                # 注册/登录/退出/校验令牌 业务逻辑（服务层）
│   │   ├── password.go               # Argon2id 哈希/校验
│   │   ├── token.go                  # 不透明令牌生成/哈希/生命周期
│   │   ├── handler.go                # Gin HTTP 适配器
│   │   └── *_test.go                 # 单元测试
│   ├── middleware/
│   │   ├── auth.go                   # 认证中间件（Bearer → 用户上下文）
│   │   └── auth_test.go
│   ├── ratelimit/
│   │   ├── ratelimit.go              # 内存滑动窗口限流
│   │   └── ratelimit_test.go
│   └── database/sqlc/                # sqlc 生成代码（users/sessions 查询）
├── db/
│   ├── migrations/
│   │   ├── 000002_users.down.sql     # users 表
│   │   ├── 000002_users.up.sql
│   │   ├── 000003_sessions.down.sql  # sessions 表
│   │   └── 000003_sessions.up.sql
│   └── queries/
│       ├── users.sql                 # sqlc: 建用户/查用户名/按ID查
│       └── sessions.sql              # sqlc: 建会话/按令牌哈希查/撤销/更新活动时间

frontend/
├── src/
│   ├── api/login/
│   │   ├── index.ts                  # 修改：登录/注册/退出/me 接真实后端
│   │   └── types.ts                  # 修改：UserType/登录注册请求响应类型
│   ├── api/request/
│   │   └── index.ts                  # 修改：拦截器附加 Bearer 令牌
│   ├── store/modules/user.ts         # 修改：登录/注册/退出 action 接真实后端
│   ├── axios/config.ts               # 修改：baseURL 走 VITE_API_BASE_URL
│   └── views/Login/
│       ├── Login.vue                 # 修改：注册/登录切换接真实后端
│       └── components/
│           ├── LoginForm.vue         # 修改
│           └── RegisterForm.vue      # 修改
└── .env.development                  # 新增：VITE_API_BASE_URL
```

**Structure Decision**: 后端遵循已有 `cmd/` + `internal/` 布局，新增 `auth`、`ratelimit` 包，
认证中间件并入已有 `middleware` 包。前端遵循现有 `api/` + `store/` 结构，仅修改认证相关文件，
不触碰角色/权限/菜单 Mock 接口。sqlc 输出到已有 `internal/database/sqlc/` 目录。

## Complexity Tracking

> **No violations** — all Constitution Check gates passed. No complexity justifications required.
