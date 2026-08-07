---

description: "Task list for RBAC permission management phase 1 implementation"
---

# Tasks: 权限管理（RBAC）第一阶段

**Input**: Design documents from `/specs/003-rbac-permission-management/`

**Prerequisites**: plan.md (required), spec.md (required for user stories), research.md, data-model.md, contracts/rbac-api.md

**Tests**: spec.md 测试策略明确要求（单元/契约/迁移）→ 本任务单含测试任务，遵循"先写测试、确保失败后再实现"。

**Organization**: Tasks are grouped by user story to enable independent implementation and testing of each story.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to (e.g., US1, US2, US3)
- Include exact file paths in descriptions

## 用户故事（源自 spec.md 四项目标）

| Story | 目标 | 独立测试 |
|-------|------|----------|
| US1 | 登录/注册跳转解锁（静态路由） | 前端：登录/注册后跳转 Dashboard |
| US2 | 角色与部门管理接口 | 后端：roles/departments 端点契约测试 |
| US3 | 用户管理接口 + auth 扩展（/me 角色、注册默认角色、logout 200） | 后端：users 端点 + auth 扩展契约测试 |
| US4 | 前端管理页对接真实后端 | 前端：quickstart 场景 2–8 |

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: 确认改动前基线全绿，便于区分本特性引入的问题。

- [ ] T001 运行后端基线检查：`cd backend && go test ./... -count=1`；前端：`cd frontend && pnpm exec vue-tsc --noEmit`。记录当前 `git status`（改动前基线）

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: RBAC 数据层地基（迁移 + sqlc 查询 + rbac 包骨架）。阻塞 US2/US3/US4；US1 不依赖。

**⚠️ CRITICAL**: 本阶段完成前 US2/US3/US4 无法开始。

- [ ] T002 [P] 编写 `backend/db/migrations/000004_rbac.up.sql`：建 `departments`（id/name UNIQUE/parent_id 自引用 FK/created_at/updated_at，含 parent_id 索引）、`roles`（id/name UNIQUE/code UNIQUE，含 created_at/updated_at）、`user_roles`（联合主键 (user_id,role_id)，双 FK ON DELETE CASCADE，role_id 索引）；ALTER `users` 增 `account`/`email`/`department_id`（FK → departments(id)，含索引）；种子数据：部门树（研发部→前端组/后端组、产品部、运营部、市场部、销售部、客服部）、角色（超级管理员 super_admin / 管理员 admin / 普通用户 user，均幂等 INSERT）、给既有用户补默认角色（`INSERT INTO user_roles SELECT id, (SELECT id FROM roles WHERE code='user') FROM users ON CONFLICT DO NOTHING`）
- [ ] T003 [P] 编写 `backend/db/migrations/000004_rbac.down.sql`：逆序删除（DROP TABLE user_roles, departments, roles；ALTER TABLE users DROP COLUMN account, email, department_id）。注：`migrations.go` 用 `//go:embed *.sql` 自动纳入新文件，无需改动
- [ ] T004 [P] 编写 `backend/db/queries/departments.sql`（sqlc 输入，查询名全局唯一，勿与既有 CreateUser/GetUserByID 等冲突）：`ListDepartments :many`、`GetDepartmentByID :one`、`GetDepartmentByName :one`、`CreateDepartment :one`（name, parent_id）、`UpdateDepartment :one`（id, name, parent_id）、`DeleteDepartment :exec`、`CountDepartmentsByParentID :one`（删除保护：子部门数）、`CountUsersByDepartmentID :one`（删除保护：部门用户数）
- [ ] T005 [P] 编写 `backend/db/queries/roles.sql`：`ListRoles :many`、`GetRoleByID :one`、`GetRoleByName :one`、`GetRoleByCode :one`、`CreateRole :one`（name, code）、`UpdateRole :one`（id, name, code）、`DeleteRole :exec`、`CountUserRolesByRoleID :one`（删除保护：关联用户数）
- [ ] T006 [P] 编写 `backend/db/queries/users_rbac.sql`：`CreateRbacUser :one`（username, password_hash, account, email, department_id）、`UpdateRbacUser :one`（id, account, email, department_id）、`UpdateUserPassword :exec`（id, password_hash）、`ListUsersByDepartment :many`（可空过滤 department_id/username/account + LIMIT/OFFSET，用 `($1::bigint IS NULL OR department_id = $1)` 模式）、`CountUsersByDepartment :one`（同过滤，返回总数）、`DeleteUser :exec`、`DeleteUserRolesByUserID :exec`、`InsertUserRole :exec`（user_id, role_id）、`ListRolesByUserID :many`（JOIN user_roles+roles → id, name, code）
- [ ] T007 运行 `cd backend && sqlc generate`（生成 `internal/database/sqlc/`），确认新查询与 models.go 新增 departments/roles/user_roles 结构体；`go build ./...` 编译通过。⚠️ sqlc 以 000004 迁移后 schema 生成，`users` 模型将含新列，需确认既有代码（auth）不破坏
- [ ] T008 [P] 编写迁移集成测试 `backend/internal/database/rbac_migration_test.go`（`//go:build integration`，仿 db_test.go testcontainers 模式）：验证 000004 建表成功、UNIQUE/FK/CASCADE 约束生效、种子部门/角色存在、既有用户被补默认角色
- [ ] T009 [P] 创建 rbac 包骨架 `backend/internal/rbac/service.go` + `handler.go`：定义错误 sentinel（ErrRoleNotFound/ErrDepartmentNotFound/ErrUserNotFound/ErrNameTaken）与错误信封类型（形状同 auth 私有 `errorEnvelope`：`{"error":{"code","message","field_errors"}}`，因 auth 类型私有需在 rbac 复刻）；`NewService(pool)`/`NewHandler(svc, logger)` 签名对齐 `internal/auth`；main.go 暂不注册

**Checkpoint**: 数据层地基就绪 —— 迁移可执行、查询已生成、rbac 包可编译。

---

## Phase 3: User Story 1 - 登录/注册跳转解锁 (Priority: P1) 🎯 MVP

**Goal**: 静态路由模式解锁导航，登录/注册后正常跳转 Dashboard。

**Independent Test**: 启动前后端，注册→跳转 Dashboard；退出→回登录页。原问题（卡登录页）不再复现。

### Implementation for User Story 1

- [ ] T010 [P] [US1] 修改 `frontend/src/store/modules/app.ts`：state 中 `dynamicRouter: true → false`、`serverDynamicRouter: true → false`（`LoginForm.vue`/`RegisterForm.vue` 已有 static 分支，零逻辑改动）
- [ ] T011 [US1] 同一文件 `frontend/src/store/modules/app.ts` 处理 `persist: true` 对旧值的覆盖：改为 `persist: { omit: ['dynamicRouter', 'serverDynamicRouter'] }`（pinia-plugin-persistedstate v4.2 支持 omit），否则旧 localStorage 持久化的 `dynamicRouter:true` 会覆盖新默认值、本故事无效
- [ ] T012 [US1] 手工验证 `specs/003-rbac-permission-management/quickstart.md` 场景 1（登录/注册跳转、刷新保持登录、退出回登录页）

**Checkpoint**: 登录/注册正常跳转，MVP 可交付。

---

## Phase 4: User Story 2 - 角色与部门管理接口 (Priority: P1)

**Goal**: `GET/POST /api/v1/roles`、`POST /api/v1/roles/delete`；`GET/POST /api/v1/departments`、`POST /api/v1/departments/delete`，全部经 `middleware.Auth`。

**Independent Test**: 契约测试覆盖端点 + 错误码（401 无令牌 / 409 重名 / 400 删除保护 / 404 不存在），见 contracts/rbac-api.md。

### Tests for User Story 2 (spec 要求 TDD) ⚠️

- [ ] T013 [P] [US2] 编写 `backend/internal/rbac/service_test.go`（单元，mock 池，仿 auth/service_test.go）：角色 CRUD、部门树构建、保存/更新、删除保护（有子部门/有关联用户返回错误）、重名冲突
- [ ] T014 [P] [US2] 编写 `backend/internal/rbac/handler_test.go`（httptest 契约，仿 auth/handler_test.go）：roles/departments 各端点请求/响应/状态码/错误码/鉴权（401）、字段校验失败 `400 AUTH_INVALID_INPUT`（含 field_errors）

### Implementation for User Story 2

- [ ] T015 [US2] 实现 `backend/internal/rbac/service.go` 角色部分：`ListRoles`/`SaveRole`（建改合一，id 缺省新建、存在则更新）/`DeleteRoles`（先 `CountUserRolesByRoleID` 检查，有关联用户返回 400 提示）；`GetRoleByCode("user")` 等查询复用 sqlc 生成代码；name/code 冲突映射 ErrNameTaken
- [ ] T016 [US2] 实现 `backend/internal/rbac/service.go` 部门部分：`ListDepartments`（查询全量后在 service 组装树 `children`，对齐 GET /departments 返回）、`SaveDepartment`（parent_id 校验非自身、存在性）、`DeleteDepartments`（先 `CountDepartmentsByParentID` + `CountUsersByDepartmentID` 检查，有引用返回 400）
- [ ] T017 [US2] 实现 `backend/internal/rbac/handler.go` 角色/部门端点：请求解析与校验（name 1–64、code `^[a-z][a-z0-9_]*$`、ids 非空）、错误→HTTP 映射（ROLE_NOT_FOUND 404/DEPARTMENT_NOT_FOUND 404/NAME_TAKEN 409/AUTH_INVALID_INPUT 400）、写接口一律返回 `200 {"data":{}}`（不返回 204）、响应 snake_case
- [ ] T018 [US2] 注册路由 `backend/cmd/server/main.go`：`rbacSvc := rbac.NewService(db.Pool)`；`rbacGroup := router.Group("/api/v1")` 挂 `middleware.Auth(authSvc)`，注册 `GET/POST /roles`、`POST /roles/delete`、`GET/POST /departments`、`POST /departments/delete`

**Checkpoint**: roles/departments 接口可独立测试通过，前端角色/部门页数据可联调。

---

## Phase 5: User Story 3 - 用户管理接口与 auth 扩展 (Priority: P1)

**Goal**: `GET/POST /api/v1/users`、`POST /api/v1/users/delete`（分页+事务+角色替换）；`/auth/me` 返回 department+roles；注册默认角色 `user`；`logout` 204→200。

**Independent Test**: 契约测试覆盖 users 端点 + auth 扩展（/me 角色、注册默认角色、logout 200）。

### Tests for User Story 3 (spec 要求 TDD) ⚠️

- [ ] T019 [P] [US3] 扩展 `backend/internal/rbac/service_test.go`：用户分页列表（部门/关键词过滤、total）、事务保存（建用户+替换角色集原子；编辑不传密码不改）、批量删除
- [ ] T020 [P] [US3] 扩展 `backend/internal/rbac/handler_test.go` + 编写 auth 扩展契约：users 端点（分页参数映射、密码缺失 400、用户名重复 409）；`/auth/me` 返回 roles/department；注册新用户 → `/auth/me` roles 含 `code:"user"`；`logout` 返回 200 `{"data":{}}` 而非 204

### Implementation for User Story 3

- [ ] T021 [US3] 实现 `backend/internal/rbac/service.go` 用户部分：`ListUsers`（分页 + department_id/username/account 过滤，service 组装每行 `role` 角色名拼接串与 `department:{id,name}`）、`SaveUser`（单事务：新建或更新用户 + 先删后插替换 `roles`；新建必传 password 且 Argon2id 哈希，编辑不传不改）、`DeleteUsers`（物理删除，user_roles 级联）；错误映射 USER_NOT_FOUND/ROLE_NOT_FOUND/DEPARTMENT_NOT_FOUND
- [ ] T022 [US3] 实现 `backend/internal/rbac/handler.go` 用户端点：请求解析与校验（username 3–32 `^[a-zA-Z0-9_]+$`、password 8–72、email 格式、roles 数组）、分页查询参数 `page_index`/`page_size`/`department_id`/`username`/`account`、错误映射、写接口返回 `200 {"data":{}}`
- [ ] T023 [US3] 修改 `backend/internal/auth/service.go` `Register`：在既有事务（`tx`/`qtx := sqlc.New(tx)`）内，`CreateUser` 之后追加 `qtx.GetRoleByCode(ctx, "user")` + `qtx.InsertUserRole(...)`，给新用户写默认角色（与 user+session 同事务原子）
- [ ] T024 [US3] 修改 `backend/internal/auth/service.go`：新增 `GetUserProfile(ctx, userID)` 方法并加入 `Service` 接口，查询 GetUserByID + GetDepartmentByID + ListRolesByUserID 组装 profile；修改 `backend/internal/auth/handler.go` `Me` 调用之，返回 `{user:{id, username, account, email, created_at, department:{id,name}, roles:[{id,name,code}]}}`
- [ ] T025 [US3] 修改 `backend/internal/auth/handler.go` `Logout`：`c.Status(http.StatusNoContent)` → `c.JSON(http.StatusOK, gin.H{"data": gin.H{}})`（避免前端拦截器对空 body 2xx 误弹"请求失败"）

**Checkpoint**: users 接口 + auth 扩展通过契约测试；所有写接口返回 JSON body。

---

## Phase 6: User Story 4 - 前端管理页对接 (Priority: P1)

**Goal**: `User.vue` / `Role.vue` / `Department.vue` 从 Mock 迁移到真实后端，**页面组件零改动**，api 层做 snake→camel 映射。

**Independent Test**: 浏览器按 quickstart 场景 2–8 验证。

### Implementation for User Story 4

- [ ] T026 [P] [US4] 修改 `frontend/src/api/role/index.ts`：`getRoleListApi` → `request.get({ url: '/api/v1/roles' })`，响应映射 `name→roleName`（User.vue 第 128 行用 `v.roleName` 做选项、Role.vue 用 `res.data.list`）；按需补 `frontend/src/api/role/types.ts`（RoleListResponse：`{list:[{id,name,roleName,code}], total}`）
- [ ] T027 [P] [US4] 修改 `frontend/src/api/department/index.ts` 全部 7 个函数指向真实端点并做字段映射：
  - `getDepartmentApi` → `GET /api/v1/departments`，树形映射 `{id,name,parent_id,children}` → `{id,departmentName,children}`（User.vue/Department.vue 依赖 `departmentName` 与 children）
  - `getDepartmentTableApi` → `GET /api/v1/departments`，把树扁平化/适配为 `{list,total}` 以维持 Department.vue `useTable` 契约（fetchDataApi 取 `res.data.list`/`res.data.total`）
  - `saveDepartmentApi` → `POST /api/v1/departments`，表单 `parentId→parent_id`
  - `deleteDepartmentApi` → `POST /api/v1/departments/delete`（body `{ids}`）
  - `getUserByIdApi` → `GET /api/v1/users`，参数 `id→department_id`、`pageIndex→page_index`、`pageSize→page_size`；响应 `created_at→createTime`、`department.name→department.departmentName`
  - `saveUserApi` → `POST /api/v1/users`，表单 `department→department_id`、`role→roles`
  - `deleteUserByIdApi` → `POST /api/v1/users/delete`（body `{ids}`）
- [ ] T028 [P] [US4] 更新 `frontend/src/api/department/types.ts`：按真实返回补齐 `DepartmentItem`/`DepartmentUserItem`/请求参数类型（`DepartmentUserParams` 的 `id` 即 department_id、`createTime`/`role`/`department.departmentName` 字段），确保 `pnpm exec vue-tsc --noEmit` 通过
- [ ] T029 [US4] 端到端验证 `specs/003-rbac-permission-management/quickstart.md` 场景 2–8：角色/部门/用户页真实数据 CRUD、/auth/me、注册默认角色、401、写接口不误报、退出不误报

**Checkpoint**: 三个管理页面用真实后端数据完成增删改查。

---

## Phase 7: Polish & Cross-Cutting Concerns

**Purpose**: 全量回归、文档、清理。

- [ ] T030 [P] 更新文档：`README.md` 或 `docs/` 增加管理接口说明（/api/v1/roles|departments|users、鉴权要求、写接口 JSON body 约定）；标注阶段一默认静态路由
- [ ] T031 评估清理 mock：检查 `frontend/mock/` 中已替换的 `/mock/role/*`、`/mock/department/*` 是否仍被引用；不再引用的可移除（不阻塞，菜单权限仍属阶段二）
- [ ] T032 全量回归：`cd backend && go test ./... -count=1` + `go test ./internal/database/ -tags=integration`（Docker）；`cd frontend && pnpm exec vue-tsc --noEmit`；按 quickstart.md 场景 1–8 人工走查

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — 立即执行
- **Foundational (Phase 2)**: Depends on Setup — **阻塞 US2/US3/US4**（US1 不依赖，可与 Phase 2 并行）
- **US1 (Phase 3)**: Depends on Setup only — 可与 Phase 2 并行
- **US2 (Phase 4)**: Depends on Foundational
- **US3 (Phase 5)**: Depends on Foundational；复用 US2 的 service/handler 骨架与错误映射（建议 US2 完成后开始）
- **US4 (Phase 6)**: Depends on US2 + US3（完整 API 面）
- **Polish (Phase 7)**: Depends on US1–US4

### User Story Dependencies

- **US1 (P1)**: 独立，无需后端（静态路由模式）—— 建议最先做（MVP）
- **US2 (P1)**: 依赖 Foundational，与其他故事独立
- **US3 (P1)**: 依赖 Foundational（+ US2 骨架）
- **US4 (P1)**: 依赖 US2 + US3

### Within Each User Story

- 测试（T013/T014、T019/T020）须先写并确认失败，再实现
- service → handler → 路由注册 → 端到端

### Parallel Opportunities

- Foundational 内 T002–T006（迁移与查询文件）完全并行；T008（迁移测试）与 T009（rbac 骨架）并行
- US1 可与整个后端（Phase 2/4/5）并行
- US2 内 T013/T014（两个测试文件）并行；US3 内 T019/T020 并行
- US4 内 T026/T027/T028（三个 api 文件）并行
- 前端工作（US1、US4）与后端工作（US2、US3）可由不同人并行

---

## Parallel Example

```bash
# 后端数据层（并行启动）：
Task: "T004 编写 db/queries/departments.sql"
Task: "T005 编写 db/queries/roles.sql"
Task: "T006 编写 db/queries/users_rbac.sql"

# 前端解锁（与后端并行）：
Task: "T010 [US1] app.ts 默认 dynamicRouter:false"
Task: "T011 [US1] app.ts persist omit dynamicRouter/serverDynamicRouter"

# 前端 API 层（US2/US3 完成后）：
Task: "T026 [US4] src/api/role/index.ts 重接"
Task: "T027 [US4] src/api/department/index.ts 重接"
Task: "T028 [US4] types.ts 更新"
```

---

## Implementation Strategy

### MVP First（US1 优先）

1. Phase 1: Setup 基线
2. **Phase 2 + US1 并行**：数据层地基 + 登录/注册跳转解锁
3. **STOP 验证 US1**：quickstart 场景 1（MVP 可交付）
4. US2 → 验证；US3 → 验证；US4 → 验证
5. Phase 7: 全量回归 + 文档

### Incremental Delivery

1. 数据地基就绪 → US1 解锁导航（MVP）
2. US2 角色/部门接口 → 契约测试绿 → 联调角色/部门页
3. US3 用户接口 + auth 扩展 → 契约测试绿
4. US4 前端页面对接 → 页面全功能

### 关键实现提示（避免返工）

- sqlc 查询名全局唯一；users 相关新查询用 `CreateRbacUser` 等新名，勿改既有 `CreateUser` 签名（auth 依赖）
- 写接口一律 `200 {"data":{}}`，绝不 204（前端拦截器对空 body 误报"请求失败"）
- `app.ts` 有 `persist: true`，US1 必须处理 omit，否则旧 localStorage 覆盖默认值
- 迁移不种子用户：quickstart 场景 4/5 先注册再验证
- 阶段一管理接口仅鉴权、无角色级授权（属阶段二）

---

## Notes

- [P] tasks = different files, no dependencies
- [Story] label maps task to specific user story for traceability
- Commit after each task or logical group
- Verify tests fail before implementing
- Stop at any checkpoint to validate story independently
- 契约细节见 `contracts/rbac-api.md`，表结构/种子见 `data-model.md`
