# Research: 权限管理（RBAC）第一阶段技术决策

**Feature**: 003-rbac-permission-management
**Date**: 2026-08-07
**Status**: Complete

## 研究摘要

本文档记录权限管理第一阶段（部门/角色/用户 + 前端对接 + 登录解锁）所有关键技术决策。
决策基于已批准设计文档 `docs/superpowers/specs/2026-08-07-rbac-permission-management-phase1-design.md`、
项目宪章（`.specify/memory/constitution.md`）与已有 `internal/auth` 实现。

现状约束（决定本方案的出发点）：
- 前端 `Authorization` 页面全部依赖 `/mock/*`，而 `VITE_API_BASE_PATH=http://localhost:8080`
  使这些请求直达 Go 后端，后端无 `/mock/*` 路由 → 404。
- `dynamicRouter=true` 走服务端角色下拉（`/mock/role/list` 404），登录后 `getRole()` 无 try/catch
  → 无法跳转；注册则角色列表为空只注册 404 兜底路由。

---

## 决策 1: 数据模型 — RBAC 最小集（departments/roles/user_roles + users 扩展）

**Decision**: 新增 `departments`（自引用树）、`roles`（含 `code` 预留）、`user_roles`（多对多），
`users` 扩展 `account`/`email`/`department_id`。

**Rationale**:
- 用户管理页同时依赖部门树与角色多选（`getRoleListApi`→`/mock/role/table`），三个实体天然联动，
  一次建齐避免二次建表（宪章 II：数据模型按最终 RBAC 设计，能力分阶段交付）。
- `roles.code` 预留角色码，阶段二做服务端驱动路由/按钮权限过滤时无需改表。
- 多对多 `user_roles` 符合"一个用户多个角色"的常规管理语义，且 `ON DELETE CASCADE` 简化删除。

**Alternatives considered**:
- 只建 `users` 单表 + 冗余 `role` 字符串列：无法表达多角色，阶段二必然返工
- 引入 `menus`/`permissions` 表一并建模：超出本阶段需要，违反宪章 II（不得引入未使用抽象）

---

## 决策 2: 代码组织 — `internal/rbac` 包，仿 `internal/auth` 分层

**Decision**: 新增 `backend/internal/rbac`：`service.go`（业务）、`handler.go`（HTTP 适配/校验）、
`sqlc` 查询、`service_test.go` / `handler_test.go`。路由在 `cmd/server/main.go` 注册，全部挂
`/api/v1` 并用 `middleware.Auth` 保护。

**Rationale**:
- 与现有 `internal/auth` 完全一致，降低认知成本与维护偏差（宪章 IV：业务规则在可独立测试的服务层）。
- 部门/角色/用户共享同一连接池与 sqlc Querier，单包内聚、文件聚焦。
- 复用现成 `middleware.Auth`（Bearer 校验 + 上下文注入），零新鉴权代码。

**Alternatives considered**:
- 按资源拆 `internal/department` / `internal/role` / `internal/user`：三包高度耦合（用户依赖角色），
  且各自只是薄 CRUD，拆包徒增文件数与跨包边界
- handler 直连 DB：违反宪章 IV

---

## 决策 3: 命名与契约 — 后端 snake_case + 前端 api 层映射

**Decision**: 后端 JSON 遵循既有 `snake_case`（`create_time`/`department_id`）；前端 `@/api/*`
做 snake→camel 映射（`created_at→createTime`、`name→departmentName/roleName`），**页面组件零改动**。

**Rationale**:
- 后端已全面 `snake_case`（`created_at`、`password_hash`），保持契约一致性（宪章 IV 稳定契约）。
- 前端页面用 `useCrudSchemas` 按 `createTime`/`department.id`/`role` 等 camelCase 字段渲染，
  由 api 层适配最省改动、风险最低。
- 响应信封统一 `{data:...}`，与现有后端/前端拦截器（`defaultResponseInterceptors`）直接兼容。

**Alternatives considered**:
- 后端直接输出 camelCase：破坏后端既有约定，且 sqlc 生成结构体需手工改 tag，违反宪章 III
- 改前端页面模板字段：改动面大、易错，违背"现有页面复用"目标

---

## 决策 4: 写接口统一返回 JSON body（`{data:{}}`）

**Decision**: 所有写接口（save/delete/logout）返回 `200 {"data":{}}`，**不返回 204**。

**Rationale**:
- 前端 `defaultResponseInterceptors`（`frontend/src/axios/config.ts`）对空 body 的 2xx 会走
  兜底分支弹 `ElMessage.error('请求失败')` —— 204 会被误报为失败。
- `logout` 现有 `c.Status(http.StatusNoContent)`（204）同样会触发误报，本阶段一并改为 200。

**Alternatives considered**:
- 改前端拦截器识别空 body：需处理 blob/流等边界，且是全局行为变更，影响面大于后端一处契约修正

---

## 决策 5: 登录/注册解锁 — `dynamicRouter:false`

**Decision**: `frontend/src/store/modules/app.ts` 默认 `dynamicRouter:false`、`serverDynamicRouter:false`，
登录/注册走静态路由（`generateRoutes('static')`）。

**Rationale**:
- `LoginForm.vue` / `RegisterForm.vue` 已有 static 分支（`if (appStore.getDynamicRouter)` else），
  只改默认值即可，零逻辑改动。
- 静态路由下登录不触发 `getRole()`（不再依赖 `/mock/role/list`），导航立即恢复。
- 阶段二做服务端驱动路由时再回切该开关（见 spec 非目标）。

**Alternatives considered**:
- 修 vite 代理把 `/mock/*` 转到 mock server：治标不治本，mock 数据不可持久化、无真实鉴权
- 在后端实现 `/mock/*` 路由：临时伪装，与"真实 RBAC"目标矛盾

---

## 决策 6: 注册默认角色

**Decision**: `auth.Register` 在既有事务中额外写入一条 `user_roles`（角色 `user`/普通用户）。

**Rationale**:
- 保证所有用户至少有一个角色，`/auth/me` 角色字段非空，阶段二可直接用
- 复用注册既有事务（user + session 已原子），追加一次插入无额外一致性风险（宪章 III 事务原子）

**Alternatives considered**:
- 注册后由管理端手动分配角色：新用户无角色，`/auth/me` 与权限逻辑需处理空角色特例

---

## 决策 7: 删除引用保护

**Decision**: 删除部门/角色时若存在子部门、关联用户或角色引用，返回 `400`（含 `field_errors` 提示），
不做级联静默删除。

**Rationale**:
- 部门树与角色分配是管理数据，误删波及面大；显式提示比静默级联更安全、可预期
- 物理删除 + 受控引用检查，避免软删除的复杂度（阶段二审计日志可再评估）

**Alternatives considered**:
- 软删除（`deleted_at`）：本阶段无审计需求，增加查询复杂度，违反 YAGNI

---

## 决策 8: 分页与列表

**Decision**: 用户列表按 `department_id` + `username`/`account` 关键词分页
（`page_index`/`page_size`，从 1 起）；角色/部门列表整表返回（数据量小）。

**Rationale**:
- 前端 `useTable` 传入 `pageSize/currentPage`（1-based）与 `searchParams`，直接对齐
- 角色/部门初始数十量级，整表返回免去前端复杂分页，符合低流量后台实际

---

## 决策 9: 事务边界

**Decision**: 用户保存（建/改用户 + 替换角色关联）在单个事务中完成；删除用户（含 `user_roles`
级联）单语句。

**Rationale**:
- 用户与角色关联是两个写操作，非原子会导致"有用户无角色"或半写入（宪章 III）
- 角色关联用"先删后插"实现替换语义，简单明确

**Alternatives considered**:
- upsert 逐条比对角色集：逻辑复杂，收益低（数据量小）

---

## 未知项（已解决）

| 原不确定点 | 结论 | 依据 |
|------------|------|------|
| 登录/注册为何无法跳转 | 动态路由依赖的 `/mock/role/list` 404；`getRole()` 无错误捕获 | `LoginForm.vue:309`、`permission.ts:45`、`main.go` |
| `/mock/*` 为何 404 | `VITE_API_BASE_PATH=:8080` 直达 Go 后端，vite-plugin-mock 只在 dev server 生效 | `.env.base:5`、`vite.config.ts:79` |
| 写接口 204 是否误报 | 是，`defaultResponseInterceptors` 空 body 兜底弹错误 | `frontend/src/axios/config.ts:52` |
| 前端页面零改动是否可行 | 可行，api 层做 snake→camel 映射 | `User.vue`/`Role.vue`/`Department.vue` 字段分析 |
