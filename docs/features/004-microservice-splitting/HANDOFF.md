# 特性交接 — 004 微服务拆分准备（模块化单体边界）

> 交接时间：2026-08-11
> 分支：`develop-wsl`（Spec Kit 逻辑 feature 为 `004-microservice-splitting`，spec.md 明确「当前 Git 工作分支仍为 develop-wsl」）
> 相关文档：`specs/004-microservice-splitting/`（spec.md / plan.md / tasks.md / data-model.md / research.md / quickstart.md / contracts/）
> 状态：自动化工序全部完成 ✅；仅剩 T032/T081 的浏览器人工 E2E ⚠️ 待用户执行。工作区 62 modified + 64 untracked，**未 commit / 未 push**。

---

## 1. 已完成内容

### 后端（Go 1.23 + Gin + PostgreSQL + sqlc）

**US1 基线（T017–T024）**
- IAM 域骨架 + 契约测试（`internal/iam/`、`internal/iam/postgres/`）：`register`/`login`/`me`/`sessions` 移入 IAM 域，模块日志（T072）、T069 同库门禁、模块 DSN 回退。
- `cmd/admin-init/`：一次性 CLI，仅接受 `--role admin|super_admin`，**不接收/不设置密码**，可安全重复执行；配套运维文档 `backend/docs/operations/admin-init.md`。全仓无默认 `admin/admin`。

**Organization 域（T033–T042）**
- `internal/organization/`：部门/成员关系 + 环检测（prevent 循环 parent）、membership 变更、批量清理、inbox consumer。
- bridge 验证：兼容同步触发器（000008）+ `set_legacy_delete_sync_mode()`；抽取就绪（`users.department_id` + FK + `idx_users_department_id`）。

**IAM 生命周期（T045–T050）**
- provisioning / activate（CAS `provisioning→active`）/ update（profile+credential+roles 原子，带 `version` 乐观锁）/ disable + compensate（CAS + revoke + 补偿删除）。

**Workflow 编排（T051–T060）**
- Admin BFF workflow store adapter（`internal/adminbff/postgres/`）+ create/update/delete 工作流（C0–C8 / U0–U7 / D0–D7）+ compensation/reconciliation。
- `Idempotency-Key` 中间件（T056）、公共错误映射 + `Retry-After` 规则（T057）、权限前置 + reauthorization（T058）、故障注入测试套件（T059）、前端幂等键复用 + axios 重试（T060）。

**Outbox/Inbox 事件化（T061–T068）**
- IAM `OutboxDeliveryPort` postgres adapter、envelope v1 codec（`internal/integration/event.go`）、dispatcher loop（blocked/alert 路径）、Platform requeue 边界、crash-matrix 测试、privacy scan。

**US5 门禁/回滚/可观测（T069–T080）**
- 模块 config + 启动门禁（`cmd/server/gates.go`：删除事件消费/dispatcher 或 BFF `/users/delete` 路由开启前校验 flag 组合 + 实时 bridge mode）；config.Validate 同物理库校验。
- rollout-gate writer（T077，`compatibility_rollout_gates` 证据 + 72h window）、evidence-cleanup ports + coordinator（T078）、metrics 注册表（T074）、rollout/rollback rehearsal 测试（T079/T080）。

**T082 legacy 层停用（build-tag gating，本 feature 收尾核心）**
- 25 个 legacy 文件打 `//go:build rollback`：`internal/auth`(10)、`internal/rbac`(7)、`internal/authorization`(2)、`internal/middleware/{auth,authorization}.go`+测试、`internal/server/router.go`、`internal/app/adminapi/legacy_delete_delegate.go`。
- 默认 build（无 tag）= BFF-only 发货二进制，物理上排除 legacy 编译单元（`go list` 报 "build constraints exclude all Go files"）。
- `-tags rollback` build = 完整 legacy wiring + compat 双 wiring + shadow reads + 4 个 legacy wire tests，供回滚支持窗口。
- 组合根拆分为 untagged `wire.go` + 互斥 `wire_default.go`(`!rollback`)/`wire_legacy.go`(`rollback`)；`LegacyRateLimitConfig` 移到共享 `server.go`。
- compat 套件拆为 untagged `compat_suite_test.go`（遍历 `builders()`）+ 互斥 `compat_builders_default_test.go`/`compat_builders_legacy_test.go`。

**最终 gates（T083–T087，全部通过）**
- T083 回滚窗口证据保持：迁移 000001–000005 冻结、8 张 dormant evidence 表保留、`schema_migrations`=13；T080 rehearsal 真库重跑。
- T084 backend gate：`sqlc generate` 39 文件 sha256 前后零改动（no-op）；gofmt/vet/test/git-diff-check 全绿。
- T086 README 重写：模块布局 + 构建变体小节 + rollback 命令 + `make sqlc-generate`。
- T087 quickstart §11 十二条 checklist 逐条映射到已跑绿测试。

### 前端（Vue 3 + Vite + Pinia + Element Plus）
- `frontend/src/utils/accessControl.ts`（access-control 权限断言）+ `frontend/scripts/access-control.test.ts`（esno 脚本）；`frontend/src/utils/idempotency.ts`（幂等键复用）。
- 角色/部门/用户管理页接真实后端：`is_builtin` 内置角色标识展示、动态路由权限刷新（403 保 token 重建）、登录/注册表单、axios 服务层幂等重试。
- T085 frontend gate 全绿：`pnpm test:access-control` / `vue-tsc` / `eslint` / `prettier` / `vite build --mode base`。

### 数据库迁移（`backend/db/migrations/`，000005–000013 均为本 feature 新建）
- `000005_permissions`（权限码/角色-权限矩阵，up+down）
- `000006_lifecycle_and_organization_membership`（用户 lifecycle_state/version、部门成员关系）
- `000007_process_state_tables`（workflow/receipt/outbox/inbox/brige 等 dormant evidence 表）
- `000008_bridge_roles_and_guards`（内置角色保护 + 兼容同步触发器 + `set_legacy_delete_sync_mode()`）
- `000009_workflow_command_fingerprint`、`000010_compensation_budget_and_recovery`、`000011_delivery_observability`、`000012_rollout_gate_functions`、`000013_evidence_cleanup_audit`

### 规格文档
- `specs/004-microservice-splitting/` 全套（spec/plan/tasks/data-model/research/quickstart/contracts/）落盘；`tasks.md` 85/89 已 `[X]`，T082–T087 均附中文 Evidence 块，T009/T010/T012 复选框卫生修复。

---

## 2. 当前实现

### 组合根（单进程 composition root）

`backend/internal/app/adminapi/wire.go`
- `Config`（`wire.go:34`）：`AdminBFFRoutesEnabled` / `OutboxDispatcherEnabled` / `ShadowReadsEnabled` / `LegacyDeleteIAMDelegationEnabled` / `RolloutGate`，逐字段注释指向 T070/T075/T076/T077。
- `Wire` 结构体（`wire.go:58`）：db/logger/cfg/cors/rate + `iamSvc`/`orgSvc`/`health`/`dispatcher`/`metrics`/`rolloutGate`/`bffWorkflows`/`cleanup`。
- `NewWire`（`wire.go:92`）：签名带 `rate server.LegacyRateLimitConfig`（两 build 共用）；构造 IAM/Organization service、T074 指标注册、probes；`w.bffWorkflows` 与 `w.cleanup`（T078 coordinator）只建一次，legacy/BFF/cleanup 三路共享同一 adapter。

**互斥变体**（两个文件都定义了 `Router()` 与 `buildShadowReads()`，靠 build tag 保证只编译一个）：
- `wire_default.go`（`//go:build !rollback`，发货 build）：`Router()`（`:18`）恒返回 `w.adminBFFRouter()`，legacy fallback 与 `AdminBFFRoutesEnabled` 均失效；`buildShadowReads()`（`:26`）返回 `nil, func(){}` stub。
- `wire_legacy.go`（`//go:build rollback`，回滚窗口）：`Router()`（`:23`）按 `AdminBFFRoutesEnabled` 决定 BFF 或 `server.NewLegacyRouter(...)`（含 `legacyDeleteDelegate()`，`:42`，仅 `LegacyDeleteIAMDelegationEnabled` 时非 nil）；`buildShadowReads()`（`:53`）构造 shadow legacy router + `shadowReadsMiddleware`（shadow 不挂 metrics registry）。

### 启动门禁

`backend/cmd/server/gates.go`
- `readBridgeMode`（`:45`）：读 `compatibility_bridge_mode` 单行，缺失行 = schema 未就绪 → gate 报 not ready。
- `checkStartupGates`（`:60`）：按 plan step 4 顺序校验——delegation 先开 → bridge mode=false 后才允许 consumer/dispatcher → BFF delete 路由额外要求两者就绪；nil mode + 任何 delete-path 组件启用 = 失败。错误只含配置 key 名 + reason code，不含值。
- `gates_test.go`（160 行）覆盖 flag 组合与 mode 快照逻辑。

### 兼容性套件（build 变体提供 router 列表）

`backend/internal/adminbff/transport/http/compat_suite_test.go`
- `TestCompatibilitySuite` 遍历 `builders()`，对每个 router 跑同一张表驱动套件（认证行为、权限矩阵、内置角色保护、批量原子性、`department:null` 分页；T056/T057 错误与 Retry-After 在 BFF wiring 全跑、legacy wiring 只刻画 pre-idempotency 行为）。
- `compat_builders_default_test.go:13`：`builders()` = `[admin_bff]`（发货 build 只有 BFF）。
- `compat_builders_legacy_test.go:32`：`builders()` = `[legacy, admin_bff]`（rollback build 双 wiring），`:19` `buildLegacyRouter` 用 `server.NewLegacyRouter` + 禁用的 `server.LegacyRateLimitConfig{}`。

### 共享类型

`backend/internal/server/server.go:14-26`：`LegacyRateLimitConfig`（`Enabled`/`RegisterIPHour`/`LoginIP15Min`/`LoginUser15Min`）移到共享位置——`NewWire` 签名、`main.go`、`wire_test.go`、`rehearsal_test.go`、`compat_suite_test.go` 在两种 build 都引用它。

### 模块划分（所有 US5 代码保持 untagged，参与默认 build）

`internal/iam`/`internal/organization`/`internal/adminbff`/`internal/integration`/`internal/platform` + 各自 `postgres/sqlc` 生成包；sqlc 源查询按 owner 拆到 `backend/db/iam|organization|adminbff/queries/`（各带 `sqlc.yaml`），`internal/database/sqlc`（global）仅剩 legacy auth/rbac 消费（Makefile 注释说明）。

---

## 3. 重要技术决定及原因

| # | 决定 | 原因 |
|---|------|------|
| 1 | 用 `//go:build rollback` build-tag gating 停用 legacy 层，**而非删除文件** | plan Phase 8 step 4「本 feature 不做破坏性删除」；默认 build 物理上排除 legacy 编译单元（无法 serve legacy routes），`-tags rollback` 保留完整回滚窗口 wiring |
| 2 | 互斥变体用 `//go:build !rollback` vs `rollback`（`wire_default.go`/`wire_legacy.go`、`compat_builders_default_test.go`/`compat_builders_legacy_test.go`） | 两种 build 都编译 untagged 共享文件 + 恰好一个变体文件，无重复符号。**变体文件顶部必须真实存在 build 约束行**（§7 #1 曾踩坑） |
| 3 | `LegacyRateLimitConfig` 移到共享 `server.go`（`server.go:14-26`） | 它是配置值载体，`NewWire` 签名 / `main.go` / `wire_test.go` / `rehearsal_test.go` / `compat_suite_test.go` 在两种 build 都引用，不能放进 rollback-tagged `router.go` |
| 4 | 默认 build 中 `Router()` 恒返回 BFF；`AdminBFFRoutesEnabled` 字段保留但失效 | 发货 build 语义确定（恒 BFF）；rollback build 尊重该 flag（false→legacy fallback）；capability manifest 继续如实反映配置（post-rollout 即 `ADMIN_BFF_ROUTES_ENABLED=true`） |
| 5 | `shadow.go`/`shadow_test.go` 保持 untagged | `shadowReadsMiddleware` 只接收 `*gin.Engine`，不构造 legacy router；只 import `middleware.RequestIDHeader` + observability——default build 编译它不拉入 legacy 依赖 |
| 6 | compat 套件 `builders()` 由 build 变体提供 | default 只对 admin_bff 跑公共契约；rollback 对 legacy + admin_bff 双 wiring 跑，证明公共契约在回滚窗口内双端成立 |
| 7 | sqlc 生成文件只能经 `sqlc generate` 更新 | T053 教训：sqlc 按列位置生成 Row struct，手改必漂移；T084 确认 generate 对当前查询是 no-op（sha256 快照零改动） |
| 8 | 迁移 000001–000005 冻结不回滚；8 张 workflow/receipt/outbox/inbox/brige 表保留为 dormant additive evidence | 无破坏性删除 + 回滚窗口兼容；`schema_migrations`=13 证明迁移链在 head |
| 9 | 全仓不提供默认 `admin/admin`；admin-init 不接收/不创建 passwords | 安全基线（003 phase 确立，本 feature 延续）；真实登录先注册 |
| 10 | gopls 对互斥 build 变体与 rollback 文件的诊断视为 stale | gopls 默认 view 忽略 build-tag 取反，会报 DuplicateMethod/BrokenImport/"No packages found"；信任 `go build`/`go test`/`go vet` |

---

## 4. 修改的文件

> 以 `git status` + `git diff --name-only` 为准。本 feature 的未提交改动覆盖整个工作区（62 M + 64 ??）；`backend/server` 为 `make build` 编译产物（untracked，非源码）。

### backend/ — 模块化单体 + US5 基础设施（untagged，参与默认 build）

| 文件/目录 | 作用 |
|-----------|------|
| `cmd/server/gates.go` + `gates_test.go` | US5 启动门禁（flag 组合 + bridge mode 校验）+ 单元测试 |
| `cmd/admin-init/`（main.go + main_test.go）| 已注册用户管理员角色初始化 CLI（不接受/不设置密码）|
| `internal/iam/` + `internal/iam/postgres/` | IAM 域服务 + 持久化 adapter + 模块 sqlc 生成代码 |
| `internal/organization/` + `internal/organization/postgres/` | Organization 域（部门/成员关系 + inbox consumer）+ adapter + sqlc 生成代码 |
| `internal/adminbff/` + `postgres/` + `transport/http/` | BFF 编排、workflow store adapter、HTTP 路由/中间件/兼容性套件 |
| `internal/integration/` | outbox dispatcher、envelope v1 codec、crash-matrix、privacy、rollback rehearsal 测试 |
| `internal/platform/` + `observability/` | rollout-gate writer、evidence-cleanup coordinator、requeue、metrics registry |
| `internal/architecture/` | T009 架构所有权测试 |
| `internal/consistency/` | 一致性检查 |
| `internal/authorization/` | legacy 权限检查（`//go:build rollback`，`internal/authorization/*` 新建）|
| `internal/app/adminapi/wire.go` | 共享组合根（Config/Wire/NewWire/adminBFFRouter/StartBackground）|
| `internal/app/adminapi/wire_default.go` | `!rollback` 变体：Router 恒 BFF + shadow stub |
| `internal/app/adminapi/wire_legacy.go` | `rollback` 变体：legacy fallback + 真实 shadow + legacyDeleteDelegate |
| `internal/app/adminapi/wire_legacy_test.go` | rollback：4 个 legacy wire 测试（ShadowReads/LegacyMetricsMount/DeleteDelegation/DirectNoDelegate）|
| `internal/app/adminapi/wire_test.go` | untagged：wireSmokeDB/Metrics/RolloutGate/CleanupCoordinator/assertWorkflowRows |
| `internal/app/adminapi/rollout_gate.go` + `legacy_delete_delegate.go` | rollout-gate writer adapter（untagged）/ legacy 删除委托（rollback）|
| `internal/server/server.go` | 新增共享 `LegacyRateLimitConfig`（server.go:14-26）|
| `internal/server/router.go` | `NewLegacyRouter` + `LegacyRouterDeps`（`//go:build rollback`）|
| `internal/database/` + `sqlc/` | 连接池/迁移 + global sqlc 生成代码（仅 legacy 消费）|
| `internal/database/bridge_mode.go` + `bridge_test.go`、`evidence_cleanup.go` + 测试 | bridge mode 读取、evidence cleanup 查询适配 |
| `internal/middleware/metrics.go` + 测试 | T074 指标中间件（untagged）|
| `internal/middleware/{auth,authorization}.go` + 测试 | legacy 认证/授权中间件（`//go:build rollback`）|
| `internal/database/rollback_rehearsal_test.go` | T080 回滚演练：FK/index/8 dormant 表/schema_migrations=13 |
| `db/migrations/000005–000013` | 本 feature 全部新迁移（up+down 成对）|
| `db/iam/`、`db/organization/`、`db/adminbff/` | 模块 sqlc 源查询 + 各自 sqlc.yaml |
| `db/queries/{bridge_mode,evidence_cleanup,permissions,rollout_gate}.sql` | 新增 Platform/global 查询 |
| `db/queries/{departments,users,users_rbac}.sql` | 数据模型变更（如 users.sql 注册显式 `lifecycle_state='active', version=1`）|
| `Makefile` | clean 目标 + `sqlc-generate` 增加三个 owner 包 |
| `README.md` | 项目结构重写 + 构建变体小节 + rollback 命令 + sqlc 1.31 |
| `docs/operations/admin-init.md` | admin-init 运维文档 |
| `cmd/server/main.go`、`internal/config/config.go` + 测试 | 模块 DSN 回退、US5 开关配置 |
| `internal/auth/*`、`internal/rbac/*`（各 10/7 文件）| legacy 服务层，本 feature 只 prepend `//go:build rollback`（T082）|
| `internal/health/`、`internal/logging/`、`internal/middleware/request_id.go` + 测试 | 共享组件（T072 模块日志/请求 id）|

### frontend/ — US5 集成 + 早期 RBAC 阶段遗留未提交改动

| 文件/目录 | 作用 |
|-----------|------|
| `src/utils/accessControl.ts` + `scripts/access-control.test.ts` | access-control 权限断言 + 测试脚本（T085 gate 的一部分）|
| `src/utils/idempotency.ts` | 幂等键复用（T060）|
| `src/api/role/index.ts`、`src/api/department/index.ts`、`src/api/role/types.ts` | 管理端接口接真实后端（`is_builtin` 等字段映射）|
| `src/api/login/types.ts`、`src/axios/service.ts`、`src/permission.ts`、`src/router/index.ts`、`src/store/modules/{permission,user}.ts`、`src/components/Permission/**`、`src/directives/permission/hasPermi.ts`、`src/views/Authorization/**`、`src/views/Login/**`、`mock/role/index.mock.ts`、`package.json`、`types/router.d.ts` | **早前 RBAC/auth 阶段遗留的未提交改动**（动态路由权限、登录/注册、CRUD 页面）；非本 feature 新增，但随工作区一起交接 |

### specs/ — 本 feature 规格

| 文件/目录 | 作用 |
|-----------|------|
| `004-microservice-splitting/`（spec/plan/tasks/data-model/research/quickstart/contracts/ + 2 个 baseline patch）| 本 feature 全套规格；tasks.md 85/89 `[X]`，T032/T081 待手动 E2E |
| `002-user-auth/contracts/auth-api.md`、`003-rbac-permission-management/contracts/rbac-api.md` | 早期阶段遗留的未提交 contract 修订 |

---

## 5. 当前问题

### 环境问题
1. **工作区全部 US5 改动未提交**（62 M + 64 ??，含本 feature 全部代码）。未 commit / 未 push（遵守指令）；接手者若需提交先与用户确认。
2. **gopls stale 诊断**：编辑 rollback-tagged 文件或互斥变体时，LSP 报 DuplicateMethod / BrokenImport / "No packages found ... build tags" —— 预期（gopls 默认 view 不含 `-tags rollback`），**不是真实错误**，以 `go build`/`go test` 为准。
3. `backend/server`（untracked 编译产物）与可能的 `__debug_bin*` 不应提交。
4. Browserslist 数据 20 个月旧 + mockjs eval 警告：非阻塞。

### 待用户确认的事项
1. **T081 + T032 手动浏览器 E2E 未执行**（`tasks.md:213`、`:88`）。真实后端无默认 `admin/admin`——先注册（密码 ≥8 位）。E2E 通过后把两项勾为 `[X]` 并附中文 Evidence。
2. **提交策略未定**：是否把当前 feature 改动分批提交（backend 生成文件随源查询 / frontend / specs）由用户决定。

### 已知边界
1. `admin`/`super_admin`/`user` 是内置 RBAC 角色码，不是凭据；系统无默认 admin/admin。
2. 本 feature **不做破坏性删除**：legacy 代码保留在 rollback tag 后；迁移 000001–000005 冻结不回滚；8 张 dormant evidence 表不 drop。
3. 不引独立服务 / gRPC / broker（单进程 + outbox/inbox 事件表；`go.mod` 无 grpc/kafka/rabbit/nats）。
4. legacy 层默认 build 不可用是 T082 的设计结果，不是缺陷；回滚支持窗口用 `-tags rollback` 构建。

---

## 6. 测试结果

### 后端默认 build（发货 build）

| 命令（`cd backend`）| 结果 |
|------|------|
| `gofmt -l .` | ✅ 空 |
| `go vet ./...` | ✅ 通过 |
| `go build ./...` | ✅ 通过 |
| `go test ./internal/... ./cmd/... -count=1` | ✅ 全绿（T084 gate `go test ./...` 实测 20 包 ok + 7 包无测试，gate 输出 `GOFMT_CLEAN/VET_OK/TEST_OK/DIFF_CHECK_OK/GATE_T084_OK`；典型耗时 adminbff 42.7s / iam 39.1s / adminapi 16.9s）|
| `go test -race ./internal/app/adminapi/... ./internal/server/... ./internal/middleware/... -count=1` | ✅ 通过 |
| `go list ./internal/auth ./internal/rbac ./internal/authorization ./internal/server` | ✅ auth/rbac/authorization 报 "build constraints exclude all Go files"（预期），server 正常 |

### 后端 `-tags rollback` build（回滚窗口变体）

| 命令（`cd backend`）| 结果 |
|------|------|
| `go vet -tags rollback ./...` | ✅ 通过 |
| `go build -tags rollback ./...` | ✅ 通过 |
| `go test -tags rollback ./internal/... ./cmd/... -count=1` | ✅ 26 包全绿（含 auth 8.2s / rbac 8.7s / compat 双 wiring 11.7s / adminapi 含 4 个 legacy wire tests 29.8s）|
| `go test -tags rollback -race ./internal/app/adminapi/... ./internal/server/... ./internal/middleware/... -count=1` | ✅ 通过 |

### 聚焦真库测试（T083/T087 证据）

| 命令（`cd backend`）| 结果 |
|------|------|
| `go test -tags rollback ./internal/database/ -run 'TestRollbackRehearsal' -count=1` | ✅ 3.263s（FK/index/8 dormant 表/schema_migrations=13）|
| `go test ./internal/database/ -run 'TestUpgrade000005ToHead\|TestPermissionsMigrationUpAndDown\|TestBridgeTriggers\|TestBridgeModeAuditAndRollback' -count=1` | ✅ 4.410s |
| `go test ./internal/organization/ -count=1` | ✅ 6.266s（contract suite）|

### 前端

| 命令（`cd frontend`）| 结果 |
|------|------|
| `pnpm test:access-control` | ✅ "access-control tests passed" |
| `pnpm vue-tsc --noEmit --skipLibCheck` | ✅ 通过 |
| `pnpm eslint . "src/**/*.{js,ts,tsx,vue,html}"` | ✅ 通过 |
| `pnpm prettier --check "src/**/*.{js,ts,json,tsx,css,less,vue,html,md}"` | ✅ 通过 |
| `pnpm vite build --mode base` | ✅ Build successful（仅 Browserslist/mockjs 非阻塞警告）|

### 仓库级

| 检查 | 结果 |
|------|------|
| 仓库根 `git diff --check` | ✅ 干净 |
| `sqlc generate` + 39 文件 sha256 前后快照 | ✅ 零改动（no-op，exit 0）|
| 全仓 secret scan（postgres:// 内嵌凭据 / bcrypt 字面量 / jwt secret / 默认凭据）| ✅ 干净（仅 node_modules keyv README 示例 + localhost dev 占位 + 上游模板 demo 文案）|
| T087 quickstart §11 checklist | ✅ 十二条逐条映射到已跑绿测试 |
| **T032/T081 手动浏览器 E2E** | ⚠️ 未跑（待用户）|

---

## 7. 下一步

1. **用户执行 T081 + T032 手动浏览器 E2E**（quickstart §7）：
   ```bash
   cd /home/hdw/vue-element-plus-admin && ./dev.sh
   # 前端 http://localhost:4000/，后端 127.0.0.1:8080，日志 .dev/logs/
   ```
   先注册（不要 `admin/admin`，密码 ≥8 位）。逐项验证：注册即登录 → `/auth/me` 六权限刷新；授权刷新（403 保 token、重建动态路由）；部门/用户组成（Idempotency-Key 发送与复用、IAM 更新失败后 version-guarded 恢复、delayed compensation → `RECONCILIATION_REQUIRED`）；删除事件暂停/恢复/重投递；内置角色保护与批量原子性。
2. **E2E 通过后**：把 `specs/004-microservice-splitting/tasks.md` 的 T032（`:88`）、T081（`:213`）勾为 `[X]`，附中文 Evidence 块。
3. **回归命令**（若改过代码）：
   ```bash
   cd /home/hdw/vue-element-plus-admin/backend && go test ./internal/... ./cmd/... -count=1 && go test -tags rollback ./internal/... ./cmd/... -count=1
   cd /home/hdw/vue-element-plus-admin/frontend && pnpm vue-tsc --noEmit --skipLibCheck && pnpm vite build --mode base
   ```
4. **提交策略（先问用户）**：是否把当前 feature 改动分批提交（backend 生成文件经 `sqlc generate` 后随源查询一起、frontend、specs）。勿把 `backend/server`、`__debug_bin*`、`.dev/` 等产物入库；`docs/claude-handoff.md` 已被 gitignore（勿 `git add -f`）。
5. **可选**：`pnpm exec update-browserslist-db@latest` 消除 Browserslist 警告（会改锁文件，单独评估）。
6. 本 feature 收尾后如需继续，可参考 `specs/004-microservice-splitting/plan.md` 的下一阶段（按进程拆分服务，Phase 7.1 独立模块 DSN）。

---

## 附：曾踩过的坑（防止下个会话重试失败方案）

- **互斥 build 变体文件顶部必须真实存在 build 约束行**。`wire_default.go`/`wire_legacy.go` 曾因 `//go:build` 行缺失同时编译，rollback build 报 `method Wire.Router already declared`。写变体文件后立即 `head -1` 验证；不要回退到把两变体合并进一个文件。
- **不要信任 gopls 对 build-tag 文件的诊断**（DuplicateMethod/BrokenImport/`No packages found ... build tags` 均为 stale）。以 `go build`/`go test`/`go vet` 为准，不要因 LSP 报错改代码。
- **不要用脚本/工具重写 Go 源文件后不校验**。早前 Python 脚本拆分 `wire_test.go` 时整段丢 `wireSmokeDB` + `TestWire_MetricsEndToEnd`，且文件 untracked 无法 git 恢复。已用 Write 工具重建并 `grep -n "^func"` 校验；今后源文件改动用 Write/Edit。
- **`sqlc generate` 是唯一合法的生成文件更新路径**；不要手改 `*.sql.go`（sqlc 按列位置生成 Row struct——追加列必须追加到每处 SELECT/RETURNING 列列表末尾）。
- **不要在默认 build 里找 `auth`/`rbac`/`authorization` 的测试**（被 rollback tag 排除，是设计）；回滚窗口回归用 `go test -tags rollback`。
- **不要用 `admin/admin` 判断登录是否实现**（无默认凭据，且 `admin` 5 位 < 密码最少 8 位）；先注册再用真实账号。
- **不要手工改生成文件 / 重写冻结迁移 000001–000005**；生成文件只经 `sqlc generate`，迁移只追加新版本。
- **不要让日志/错误包含密码、token、hash 或 DB URL 值**；错误只含 key 名与 reason code。
- **Vite proxy 不剥 `/api` 前缀**；写端点保持 `200 {"data":{}}`（不要空 204）；Go 测试在 `backend/` 下运行（仓库根无 go.mod）。
- **Windows Git Credential Manager push 失败用空格路径 wrapper**（见 project 级 `docs/claude-handoff.md` §8）。
