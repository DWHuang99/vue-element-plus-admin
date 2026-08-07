# Implementation Plan: 权限管理（RBAC）第一阶段

**Branch**: `003-rbac-permission-management` | **Date**: 2026-08-07 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `/specs/003-rbac-permission-management/spec.md`

**Note**: This template is filled in by the `/speckit-plan` command; its definition describes the execution workflow.

## Summary

把权限管理从 Mock 迁移到真实后端，使前端 `Authorization` 系列页面（用户管理/角色管理/部门管理）
可以用真实数据联调，并解锁登录/注册后无法跳转的问题。

**核心**：
- 后端新增 `internal/rbac` 包（service/handler 分层，仿 `internal/auth`），提供部门/角色/用户 CRUD 接口。
- 新增 migration `000004_rbac`：`departments` / `roles` / `user_roles` 三张表 + `users` 扩展
  （`account`/`email`/`department_id`）+ 种子数据。
- `auth.Register` 在既有事务中为新用户写入默认角色 `user`。
- 前端：`app.ts` 默认 `dynamicRouter:false` 解锁导航；`@/api/department`、`@/api/role` 改指真实端点，
  api 层做 snake→camel 映射，**页面组件零改动**。
- 所有写接口返回 JSON body（避免前端拦截器对 204 空 body 误弹错误）；`logout` 204 一并改 200。

**阶段边界**（宪章 II）：本阶段不含 `menus`/`permissions`、菜单管理页、服务端驱动路由与按钮级权限；
数据模型预留 `roles.code` 字段。

## Technical Context

**Language/Version**: Go 1.23（后端）· TypeScript/Vue 3（前端接线）

**Primary Dependencies**:
- 后端（复用现有，无新增）: `gin`、`pgx/v5`、`sqlc`、`golang-migrate`、`testify`、`testcontainers`
- 前端（无新增）: `element-plus`、`vue`、`pinia`、`axios`

**Storage**: PostgreSQL（唯一事实来源）。新增 `departments`/`roles`/`user_roles`，`users` 加列；
迁移由 golang-migrate 嵌入执行；查询由 sqlc 生成。

**Testing**: 后端 testify 单元（service，mock 池）+ httptest 契约（handler）+ testcontainers
集成（迁移/种子/端到端）。前端无测试框架，页面手动验证。

**Target Platform**: Linux（后端 API）；现代浏览器（前端 SPA）

**Project Type**: web-service（后端）+ web-app（前端）

**Performance Goals**: 管理后台 CRUD，低流量；分页必需；无硬性性能指标（P95 < 300ms 量级即可）。

**Constraints**:
- 写接口 MUST 返回 JSON body（`{data:{}}`）——前端响应拦截器对空 body 的 2xx 会误弹"请求失败"
- JSON 统一 `snake_case`，前端 api 层负责映射到页面期望的 camelCase 字段
- 数据库变更按"迁移 → sqlc 生成 → service → handler"方向实施（宪章 III/IV）
- 管理接口全部经 `middleware.Auth` 保护
- 多写操作（建用户 + 角色关联）MUST 单事务原子

**Scale/Scope**: 小型后台；初始用户/角色/部门各数十量级

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

**宪章版本**: 1.0.0（2026-08-04 批准）

| 原则 | 合规评估 | 结论 |
|------|----------|------|
| I. 身份安全优先 | 管理接口全部鉴权；新建用户密码 Argon2id 哈希；无秘密入日志 | ✅ 通过 |
| II. 分阶段交付与边界控制 | 有已批准规格；本阶段边界明确（菜单/权限/服务端路由 → 阶段二）；不以未来阶段引入多余抽象 | ✅ 通过 |
| III. PostgreSQL 与 sqlc | 新表经版本化迁移；查询用 sqlc 生成；生成文件不手工修改；多写操作单事务 | ✅ 通过 |
| IV. API 契约与前后端解耦 | RBAC 业务规则在 service 层；handler 只做协议适配；契约含状态码/错误码/认证要求；snake_case 经 api 层映射保证前端兼容 | ✅ 通过 |
| V. 可验证、可观测与隐私 | service 单测 + handler 契约 + 集成测试；结构化日志不记录敏感信息 | ✅ 通过 |

无违规，无需 Complexity Tracking 例外表。

## Project Structure

### Documentation (this feature)

```text
specs/003-rbac-permission-management/
├── plan.md              # 本文档 (/speckit-plan 输出)
├── spec.md              # 已批准规格（源自 docs/superpowers/specs/2026-08-07-rbac-...）
├── research.md          # Phase 0 输出
├── data-model.md        # Phase 1 输出
├── quickstart.md        # Phase 1 输出
├── contracts/
│   └── rbac-api.md      # Phase 1 输出
└── tasks.md             # Phase 2 输出（/speckit-tasks 创建）
```

### Source Code (repository root)

```text
backend/
├── cmd/server/main.go          # 注册 /api/v1/departments|roles|users 路由（Auth 保护）
├── db/
│   ├── migrations/             # 000004_rbac.up.sql / 000004_rbac.down.sql
│   └── queries/                # departments.sql / roles.sql / users_rbac.sql（sqlc 输入）
└── internal/rbac/
    ├── service.go              # 部门树/角色 CRUD/用户分页保存删除/角色关联
    ├── handler.go              # HTTP 适配 + 校验 + 错误映射
    ├── service_test.go         # mock 池单测
    └── handler_test.go         # httptest 契约

frontend/src/
├── api/
│   ├── department/index.ts     # 改指 /api/v1/departments + /api/v1/users，做字段映射
│   ├── department/types.ts     # 按真实返回更新
│   └── role/index.ts           # 改指 /api/v1/roles
├── store/modules/app.ts        # dynamicRouter:false / serverDynamicRouter:false（解锁导航）
└── views/Authorization/        # User.vue / Role.vue / Department.vue（组件零改动）
```

**Structure Decision**: 采用 Web application（前后端分离）结构。后端沿用现有按领域分包模式
（`internal/auth` 先例），新增 `internal/rbac`；前端复用现有 `@/api/*` 与 `views/Authorization/*`
页面结构，只替换 API 层与配置默认值。

## Complexity Tracking

> 宪法检查无违规，本表留空。
