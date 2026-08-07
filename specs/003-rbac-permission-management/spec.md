# 权限管理（RBAC）第一阶段设计文档

**Date**: 2026-08-07
**Status**: Approved
**Scope**: 真实后端 RBAC 数据层（部门/角色/用户）+ 管理接口 + 前端 `User / Role / Department` 页对接 + 登录跳转解锁

## 背景与范围

在已有认证（注册/登录/退出/会话）基础上，把权限管理从 Mock 迁移到真实后端，使前端
`Authorization` 系列页面（用户管理/角色管理/部门管理）可以用真实数据联调。

现状问题：

- 前端管理页面（`User.vue` / `Role.vue` / `Department.vue`）全部依赖 `/mock/*` 接口；
  由于 `VITE_API_BASE_PATH=http://localhost:8080`，这些请求直达 Go 后端，而后端无 `/mock/*`
  路由 → 全部 404，页面拉不到数据。
- 登录/注册后无法跳转：`dynamicRouter=true` 且走服务端角色下拉（`/mock/role/list` 404），
  注册则因角色列表为空只注册了 404 兜底路由。
- 后端仅 `users` + `sessions` 两张表，无角色/部门/权限模型。

本阶段目标：

- 解锁登录/注册跳转（静态路由模式）。
- 后端建立 `departments` / `roles` / `user_roles` 模型，扩展 `users`。
- 实现部门/角色/用户管理接口，替换前端对应 Mock 调用。
- `/auth/me` 返回用户角色等扩展信息。

**不包含（阶段二）**：`menus`/`permissions` 表、菜单管理页、服务端驱动路由
（登录按角色下发菜单）、按钮级权限。阶段一为这些预留 `roles.code` 字段。

## 架构决策

- **方案一（采纳）**：真实后端 RBAC + 现有页面直接对接（分阶段）。
  数据层一次按 RBAC 设计好，避免返工；页面结构现成，只换数据来源。
- 拒绝方案二（仅最小闭环）：数据模型仍按完整 RBAC 设计，避免二次建表。
- 拒绝方案三（Mock 中转）：数据不可持久化、无真实鉴权，与目标矛盾。

### 代码组织

新增 `backend/internal/rbac` 包，仿现有 `internal/auth` 分层：
`service.go`（业务）、`handler.go`（HTTP）、`sqlc` 查询、`service_test.go` / `handler_test.go`。
路由注册在 `cmd/server/main.go`，全部挂 `/api/v1` 并用 `middleware.Auth` 保护。

### 命名映射策略

后端 JSON 遵循既有 `snake_case` 约定；前端 api 层做 snake→camel 映射
（`created_at→createTime`、`name→departmentName/roleName`），**页面模板零改动**。

## 数据模型

新 migration `000004_rbac.up.sql` / `000004_rbac.down.sql`（golang-migrate 自动执行）。

### departments 表

| 字段 | 类型 | 约束 |
|------|------|------|
| `id` | BIGSERIAL | PRIMARY KEY |
| `name` | TEXT | UNIQUE NOT NULL |
| `parent_id` | BIGINT | NULL, FK → departments(id) |
| `created_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |
| `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

### roles 表

| 字段 | 类型 | 约束 |
|------|------|------|
| `id` | BIGSERIAL | PRIMARY KEY |
| `name` | TEXT | UNIQUE NOT NULL |
| `code` | TEXT | UNIQUE NOT NULL — 预留角色码，阶段二权限过滤用 |
| `created_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |
| `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

### user_roles 表

| 字段 | 类型 | 约束 |
|------|------|------|
| `user_id` | BIGINT | FK → users(id) ON DELETE CASCADE |
| `role_id` | BIGINT | FK → roles(id) ON DELETE CASCADE |
| 联合主键 | (user_id, role_id) | |

### users 表扩展（ALTER）

| 字段 | 类型 | 约束 |
|------|------|------|
| `account` | TEXT | NULL — 前端账号字段 |
| `email` | TEXT | NULL |
| `department_id` | BIGINT | NULL, FK → departments(id) |

### 种子数据（随 migration 插入）

- 默认部门树：研发部（含子部门）、产品部、运营部、市场部、销售部、客服部等。
- 默认角色：超级管理员（`super_admin`）、管理员（`admin`）、普通用户（`user`）。
- 给既有用户补默认角色（普通用户）。

## API 契约

前缀 `/api/v1`，管理接口均需 `Authorization: Bearer <token>`。
成功 `{"data": ...}`，失败 `{"error": {"code", "message"}}`。
**所有写接口必须返回 JSON body（含 `{data:{}}`）**——前端响应拦截器对空 body 的 2xx
会误弹"请求失败"。`logout` 现有 204 一并改为返回 `200 {"data":{}}`。

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/v1/roles` | 角色列表 → `{data:{list, total}}` |
| POST | `/api/v1/roles` | 新建/更新角色 `{id?, name, code}` → `{data:{}}` |
| POST | `/api/v1/roles/delete` | 批量删除 `{ids:[]}` → `{data:{}}` |
| GET | `/api/v1/departments` | 部门树 → `{data:{list}}` |
| POST | `/api/v1/departments` | 新建/更新部门 `{id?, name, parent_id}` → `{data:{}}` |
| POST | `/api/v1/departments/delete` | 批量删除 `{ids:[]}` → `{data:{}}` |
| GET | `/api/v1/users` | 分页列表，参数 `department_id, page_index, page_size, username, account` → `{data:{list, total}}` |
| POST | `/api/v1/users` | 新建/更新用户 `{id?, username, account, email, password?, department_id, roles:[]}` → `{data:{}}` |
| POST | `/api/v1/users/delete` | 批量删除 `{ids:[]}` → `{data:{}}` |
| GET | `/api/v1/auth/me` | 扩展返回 `{user:{id, username, account, email, department, roles:[{id,name,code}]}}` |

### 用户列表返回项

`list` 每项：`{id, username, account, email, create_time, role, department:{id,name}}`
（`role` 为角色名拼接字符串，兼容前端表格展示）。

### 错误码

沿用现有 `AUTH_*` 风格，新增：

| 错误码 | HTTP | 场景 |
|--------|------|------|
| `ROLE_NOT_FOUND` | 404 | 角色不存在 |
| `DEPARTMENT_NOT_FOUND` | 404 | 部门不存在 |
| `USER_NOT_FOUND` | 404 | 用户不存在 |
| `NAME_TAKEN` | 409 | 角色/部门名重复 |

校验失败统一 `400 AUTH_INVALID_INPUT`（含 `field_errors`）。

## 安全与数据规则

- 管理接口全部经 `middleware.Auth` 校验会话。
- 创建用户须传 `password`（8–72 字符，Argon2id 哈希）；编辑不传密码则不改。
- 删除为物理删除；`user_roles` 级联清理；删除部门需处理子部门与用户引用
  （存在子部门或关联用户时返回 400，提示先处理下级）。
- 删除角色时若存在关联用户，返回 400 提示先解除关联。
- **注册默认角色**：`auth.Register` 在既有事务中额外写入一条 `user_roles`，
  给新用户分配默认角色 `user`（普通用户），保证所有用户至少有一个角色。

## 前端改动

1. **解锁导航**：`store/modules/app.ts` 默认 `dynamicRouter:false`、`serverDynamicRouter:false`。
   登录/注册走静态路由（`LoginForm.vue` / `RegisterForm.vue` 已有 static 分支，无需改动逻辑）。
2. **API 层替换**：
   - `src/api/department/`：`getDepartmentApi`→`GET /departments`；`getUserByIdApi`→`GET /users`；
     `saveUserApi`→`POST /users`；`deleteUserByIdApi`→`POST /users/delete`；
     其余部门表接口对齐 `/departments` 对应端点。
   - `src/api/role/`：`getRoleListApi`→`GET /roles`。
   - api 层负责 snake→camel 映射与类型断言，页面组件零改动。
3. `src/api/department/types.ts` 按真实返回更新类型。
4. `Menu.vue` 阶段一不动（菜单权限属阶段二），可从 `asyncRouterMap` 暂注释菜单入口。

## 测试策略

| 层 | 内容 | 工具 |
|----|------|------|
| 单元 | rbac service（部门树/分页/保存/删除/角色关联），mock 池 | testify |
| 契约 | 各端点请求/响应/错误码/鉴权 | httptest |
| 迁移 | 新表建表 + 约束 + FK + 种子 | testcontainers |

关键集成场景：未带令牌访问管理接口 401；用户名重复 409；删除有下级部门 400；
`/auth/me` 返回角色；注册新用户默认获得普通用户角色。

## 依赖

- 后端无新第三方依赖（复用 gin/pgx/sqlc/testcontainers）。
- 前端无新依赖。

## 非目标（阶段二）

菜单管理（`menus`/`permissions`）、服务端驱动路由、按钮级权限、
`dynamicRouter` 回切、审计日志、软删除。
