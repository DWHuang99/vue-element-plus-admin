# Implementation Plan: 微服务拆分准备（模块化单体边界）

**Branch**: `004-microservice-splitting`（Spec Kit 逻辑 feature；实际 Git 分支为 `develop-wsl`） | **Date**: 2026-08-10 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `/specs/004-microservice-splitting/spec.md`

## Summary

在保持现有 Vue 管理端和 `/api/v1` HTTP/JSON 契约不变的前提下，将当前 Go 单体重构为边界可验证的模块化单体：

- **IAM** 独占账户、身份资料、密码、会话、角色、权限和授权判断。
- **Organization** 独占部门层级及用户部门成员关系。
- **Admin BFF** 独占公开 Gin transport、DTO、`/auth/me`/用户列表聚合和跨领域管理工作流。
- **Integration/Platform** 提供 transactional outbox/inbox、进程内 dispatcher、配置、迁移、健康和可观测性。

本 feature 使用普通 Go ports 和进程内 adapter，不部署独立服务、不加入 gRPC runtime、消息 broker、Redis 或服务发现。通过 Organization-owned 成员表、按 owner 分离的 sqlc 包、禁止跨领域 SQL JOIN/事务、持久化幂等工作流和真实的 `iam.user.deleted` version 1 outbox/inbox 闭环，使下一 feature 可仅替换 Organization adapter 并迁移其数据库完成第一次物理拆分。

## Technical Context

**Language/Version**:

- Backend: Go 1.25.0
- Frontend: TypeScript 5.7.3, Vue 3.5.13
- SQL: PostgreSQL dialect

**Primary Dependencies**:

- Backend: Gin 1.10.0, pgx/v5 5.9.2, golang-migrate 4.18.1, Viper 1.19.0, `log/slog`, `google/uuid` 1.6.0
- Data generation: sqlc（现有 v2 配置；生成代码使用 pgx/v5）
- Security: `golang.org/x/crypto` 0.54.0（Argon2id）
- Testing: Go `testing`, testify 1.11.1, testcontainers-go 0.43.0, httptest
- Frontend: Axios 1.7.9, Pinia 2.3.0, Vue Router 4.5.0, Element Plus 2.9.2, Vite 6.0.7
- Observability: 复用结构化日志；引入主动 OpenTelemetry tracing/metrics 时固定直接依赖版本，不依赖当前仅因 testcontainers 间接出现的 OTel 包

**Storage**:

- PostgreSQL 是身份、授权、组织成员关系、工作流状态和 outbox/inbox 的唯一事实来源。
- 本迭代 IAM、Organization 和 Admin BFF DSN 必须解析到同一个物理 PostgreSQL database；可以使用独立 pool/最小权限数据库角色，但不能指向不同数据库。
- 唯一 Platform migrator 推进统一 migration version history；每个 migration 有 owner/bridge metadata，运行模块不独立竞争 version table。IAM、Organization、Admin BFF workflow 的表/查询所有权仍必须唯一。

**Testing**:

- 模块单元测试和 typed-error/工作流故障注入测试。
- httptest 公开 API 契约回归。
- testcontainers-go 真实 PostgreSQL 迁移、sqlc、事务、outbox/inbox 集成测试。
- Go AST/import 和 SQL ownership 架构测试。
- 前端 `pnpm test:access-control`、`vue-tsc`、ESLint、Prettier、Vite build。

**Target Platform**:

- 单进程 Linux Web 服务；开发期支持 WSL2 + Docker PostgreSQL。
- 现代浏览器通过 Nginx 同源访问 Vue SPA 与 `/api/*`。
- 下一 feature 的 Organization 目标平台仍为 Linux 独立服务，但不在本 feature 部署。

**Project Type**: Web application（Vue SPA + Go Admin BFF/API + PostgreSQL），本迭代为模块化单体架构迁移。

**Performance Goals**:

- 管理读取端点 P95 < 300ms（当前小型后台基线）。
- `/auth/me` 和用户列表的模块调用次数有界，不出现逐用户跨模块 N+1。
- 进程内 IAM/Organization port 不引入网络依赖。
- outbox dispatcher 使用 bounded batch 和退避，不热循环；积压量和 oldest-event age 可观测。
- 权限变更在下一个受保护请求生效，不增加授权缓存延迟。

**Constraints**:

- `/api/v1` 路径、JSON envelope、snake_case 字段、状态码、机器错误码和权限码保持兼容。
- Nginx 不移除 `/api` 前缀；前端只访问 Admin BFF。
- 写接口及 logout 保持 `200 {"data":{}}`，不改为 204 空 body。
- 401 为 `AUTH_INVALID_TOKEN`；403 为 `AUTH_FORBIDDEN`，且 403 不清理登录态。
- `effective_permissions` 必须为去重、有序、非 null 数组。
- `admin`/`super_admin` 第一代同权；`user`/自定义角色默认无管理权限。
- 内置角色 code 不可修改且角色不可删除。
- 密码、原始 token、密码 hash、数据库秘密不得进入日志、事件或工作流存储。
- sqlc 生成文件只能由 `sqlc generate` 更新。
- 不重写已应用迁移 `000001`–`000005`。
- 本 feature 不加入 gRPC runtime、broker、Redis、服务发现、菜单表、权限配置 UI/API、行级/字段级权限。

**Scale/Scope**:

- 当前为小型管理后台，用户/角色/部门预计数十到低千量级。
- 一次后端架构重构，主要涉及 `cmd/server`、`internal/auth`、`internal/authorization`、`internal/rbac`、数据库迁移/queries/sqlc、配置/健康/可观测性及管理员 CLI。
- 前端 URL/payload/交互保持兼容；为跨超时安全重试，用户写请求增加可选 `Idempotency-Key` header，官方前端为一次逻辑提交生成并复用该 header。
- 第一个真实事件：`iam.user.deleted` version 1；不预建无消费者的事件体系。

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-checked after Phase 1 design.*

**Constitution version**: 1.0.0（2026-08-04）

| # | 原则 | Research 前 | Design 后 | 说明 |
|---|---|---|---|---|
| I | **身份安全优先** | ✅ PASS | ✅ PASS | Argon2id、opaque session、token hash、滑动/绝对过期、撤销、统一登录失败、限流和实时授权语义均保持。受管用户创建使用不可登录的 `provisioning` 状态，避免补偿窗口暴露有效账户。事件/日志/工作流不保存密码、token、hash 或不必要 PII。 |
| II | **分阶段交付与边界控制** | ✅ PASS | ✅ PASS | 当前只交付可运行的模块化单体；Go ports、工作流和 outbox/inbox 都由当前跨领域用例使用。gRPC、独立进程、broker、Redis、菜单/权限配置及细粒度授权明确延期。 |
| III | **PostgreSQL 与 sqlc 契约化数据访问** | ✅ PASS | ✅ PASS | 新 schema 使用版本化迁移，按 owner 拆分 SQL/sqlc 生成包；生成代码不手改。事务严格限定在模块内部；跨模块通过工作流和事件一致性处理。历史迁移冻结并提供升级/回滚验证。 |
| IV | **API 契约与前后端解耦** | ✅ PASS | ✅ PASS | Admin BFF 独占公开 DTO 和错误映射；现有 `/api/v1`、状态码、字段和权限语义不变。领域模型不包含 Gin/sqlc/HTTP 类型，后续 adapter 替换不影响 Vue。 |
| V | **可验证、可观测与隐私保护** | ✅ PASS | ✅ PASS | 计划包含架构、契约、迁移、真实 PostgreSQL、补偿、outbox/inbox、故障注入和前端回归测试；模块调用具有 request/trace 关联，补偿和事件积压可观测，日志遵守秘密和 PII 最小化。 |

**Gate Result**: ✅ ALL GATES PASS — no constitution exceptions required.

说明：一次性回填迁移可以受控读取旧 IAM 与 Organization 列，这是显式数据迁移，不是运行时跨领域依赖；迁移完成后运行时查询必须遵守所有权。

## Architecture and Ownership

### Target dependency direction

```text
cmd/server (composition root)
├── Admin BFF HTTP transport
├── IAM application + PostgreSQL adapter
├── Organization application + PostgreSQL adapter
└── Integration dispatcher

Admin BFF ──> IAM application ports
Admin BFF ──> Organization application ports
IAM          ─X─> Organization
Organization ─X─> IAM
Admin BFF    ─X─> pgx / global sqlc / migrations   （workflow-store adapter 例外，见下）
```

- Admin BFF 应用层（`internal/adminbff` 及其 `transport/http`）MUST NOT import pgx、任何 sqlc 生成包或 migrations。
- **唯一已记录例外**：Admin BFF 独占 `admin_workflows`/`admin_workflow_subjects`/`admin_workflow_recovery_actions`（见 data-model.md Ownership matrix），因此其 workflow-store PostgreSQL adapter 位于 `internal/adminbff/postgres/`，允许 import pgx 与 BFF 自身生成的 workflow sqlc 包（`db/adminbff`），与 `internal/iam/postgres`、`internal/organization/postgres` adapter 同构。架构测试以该包为白名单例外。
- **例外记录**（宪章开发流程 4）：理由=BFF 独占 workflow 状态表，应用层须保持无数据库访问；影响=仅 adapter 子包接触 pgx/自身 sqlc，领域模型、ports、transport 与公开契约不受影响，物理拆分时可整包迁移；责任人=Admin BFF/Platform 实现团队；失效日期=004 完成评审时复审，届时续期或转化为正式修订。

### Capability ownership

| Capability | Owner | Notes |
|---|---|---|
| Register/login/logout/session authentication | IAM | 保留现有安全和事务语义。 |
| User identity and account/email profile | IAM | 不含部门成员关系。 |
| Roles, user-role assignments, permissions | IAM | 授权每请求实时查询。 |
| Department tree and department CRUD | Organization | 删除保护只查询 Organization-owned memberships。 |
| User department membership | Organization | 通过 opaque IAM user ID 引用，无跨域 FK。 |
| Public `/api/v1` routes and DTOs | Admin BFF | 所有浏览器入口统一归属。 |
| `/auth/me` and user list composition | Admin BFF | 禁止跨域 SQL JOIN。 |
| User create/update/delete workflow | Admin BFF + module ports | BFF 编排；本地事务由 owner 执行。 |
| Outbox production | IAM | 第一事件为用户删除。 |
| Inbox consumption | Organization | 幂等清理成员关系。 |
| Process config/health/logging/tracing | Platform | 输出模块级信号。 |

## Project Structure

### Documentation (this feature)

```text
specs/004-microservice-splitting/
├── spec.md
├── plan.md
├── research.md
├── data-model.md
├── quickstart.md
├── baseline-implementation.patch      # frozen HEAD 的可重建 implementation worktree patch
├── baseline-contracts.patch           # frozen HEAD → exact Auth/RBAC v1.1.0 snapshots
├── contracts/
│   ├── http-api-compatibility.md
│   ├── iam-application.md
│   ├── organization-application.md
│   ├── domain-events.md
│   └── consistency-and-compensation.md
└── tasks.md                         # 由 /speckit-tasks 生成的 87 项任务清单
```

### Source Code (repository root)

```text
backend/
├── cmd/
│   ├── server/
│   │   └── main.go                 # 薄 composition root
│   └── admin-init/
│       └── main.go                 # 通过 IAM admin port，不直接使用 sqlc
├── internal/
│   ├── app/
│   │   └── adminapi/
│   │       └── wire.go             # 单进程模块组装与 route switch
│   ├── iam/
│   │   ├── service.go
│   │   ├── ports.go
│   │   ├── models.go
│   │   ├── errors.go
│   │   ├── password.go
│   │   ├── token.go
│   │   └── postgres/
│   │       ├── repository.go
│   │       ├── transactions.go
│   │       └── sqlc/               # IAM-owned generated code
│   ├── organization/
│   │   ├── service.go
│   │   ├── ports.go
│   │   ├── models.go
│   │   ├── errors.go
│   │   └── postgres/
│   │       ├── repository.go
│   │       └── sqlc/               # Organization-owned generated code
│   ├── adminbff/
│   │   ├── service.go
│   │   ├── ports.go
│   │   ├── workflows.go
│   │   ├── errors.go
│   │   ├── postgres/           # workflow-store adapter（唯一 pgx/自身 sqlc 例外，见 Architecture）
│   │   │   ├── repository.go
│   │   │   └── sqlc/           # BFF-owned workflow generated code
│   │   └── transport/http/
│   │       ├── dto.go
│   │       ├── middleware.go
│   │       ├── auth.go
│   │       ├── roles.go
│   │       ├── departments.go
│   │       ├── users.go
│   │       └── routes.go
│   ├── integration/
│   │   ├── event.go
│   │   ├── dispatcher.go
│   │   └── dispatcher_test.go
│   ├── platform/
│   │   ├── observability/
│   │   └── migrations/
│   ├── config/                     # 扩展模块级配置
│   ├── database/                   # pool/migrator 基础设施
│   ├── health/                     # 聚合模块 readiness
│   ├── logging/
│   └── server/
├── db/
│   ├── migrations/                 # 冻结 000001–000005 + 新增 forward migration
│   ├── iam/
│   │   ├── queries/
│   │   └── sqlc.yaml
│   ├── organization/
│   │   ├── queries/
│   │   └── sqlc.yaml
│   └── adminbff/
│       ├── queries/                # 仅 workflow 状态，不含领域读取
│       └── sqlc.yaml
└── README.md

frontend/
├── src/                            # 现有 API/store/router 行为保持
└── scripts/access-control.test.ts  # 权限回归
```

**Structure Decision**: 采用务实的 ports-and-adapters 模块化单体，不为每个概念建立额外 clean-architecture 层。领域/application model 不包含 Gin、pgtype、sqlc row 或 HTTP 状态；PostgreSQL adapter 负责生成类型映射和本地事务；`cmd/server` 是唯一可 import 所有 concrete adapter 的 composition root。

## Implementation Strategy

### Phase 0 — Baseline and characterization

1. 冻结 baseline：Git HEAD `d4b1bae75b37f894879360099066ad49ab78da91`；Auth contract v1.1.0 SHA-256 `3efc6933ffca57596bcb454801434d61a1f811721ab25b32f8d21f3ff5597bee`；RBAC contract v1.1.0 SHA-256 `6254e5f4c8b5c64912d400a7b06fb4da09632e3ee6cc2e7b0ffed1ab0dc52a8f`；tracked implementation binary diff SHA-256 `aa6aeb6265b00b50bb85cfbc120442fb37322988f10ebdc9bdc3f92c3057867a`；15-file untracked implementation manifest SHA-256 `5b8a82ab168561d7d0bf68dbb4c13afd2210f21f57374604f1296510204524e0`。后两者排除 `specs/**`、`.specify/**`。可重建实现 patch 为 `baseline-implementation.patch`（123047 bytes，SHA-256 `659facfdf93a49bf2caa7e09728451029271ace9b61ab31da47dc5dd0e57e2ee`）；exact Auth/RBAC snapshot patch 为 `baseline-contracts.patch`（9008 bytes，SHA-256 `f69c52dfefa706d6411c56be1ee90e74f0794c26f48dbc95973a4c9cea9bfa38`）。两者只能在 frozen HEAD 的 disposable detached worktree 中依次经 `git apply --binary --check` 后应用，不得依赖 mutable current contract files 或应用到当前工作树。
2. 让 `http-api-compatibility.md` 对本次迁移影响的 endpoint、字段、空值、错误和 idempotency header 自包含，并增加可同时运行于 legacy router 与 Admin BFF router 的表驱动契约测试。
3. 记录并以新契约覆盖旧文档冲突，例如旧 logout 204、旧“只鉴权不鉴权授权”等描述。
4. 建立后端、迁移、权限闭环和前端 access-control 基线结果。

**Gate**: 路径、envelope、状态码、错误码、权限矩阵和 `/auth/me` 排序/空数组行为都有自动化特征测试。

### Phase 1 — Boundary skeleton and enforcement

1. 创建 IAM、Organization、Admin BFF、Integration 的 module models、typed errors 和窄 ports。
2. 将公开 HTTP DTO 所有权迁入 Admin BFF；暂时保留 legacy facade 以分阶段切流。
3. 将认证 middleware 依赖缩窄为 IAM `Authenticator`，授权 middleware 缩窄为 IAM `Authorizer`。
4. 添加 import boundary 测试：IAM/Organization 互不 import；BFF 应用层与 transport 不 import pgx/sqlc/migrations，唯一白名单为 workflow-store adapter `internal/adminbff/postgres`（仅 import 自身生成的 workflow sqlc），与 IAM/Organization adapter 同构。
5. 把 `cmd/server/main.go` 收敛为 composition root，并加入可控 route switch。

**Gate**: 新模块可编译，依赖规则机械可验证，公开流量尚未改变。

### Phase 2 — Additive schema migration and sqlc ownership

1. 冻结迁移 `000001`–`000005`，由唯一 Platform migrator 执行 additive/bridge migration：
   - `users.lifecycle_state` 与 `users.version`；使用临时 `active` default 回填现有行后删除 default，所有 insert 必须显式状态。
   - `organization_user_departments` 并从 `users.department_id` 回填。
   - `admin_workflows`、per-subject `admin_workflow_subjects`、IAM/Organization command receipts。
   - 带 pending/leased/published/blocked 状态和 lease/claim 字段的 `iam_outbox_events`。
   - 仅保存成功处理 dedupe 记录的 `organization_inbox_messages`。
2. 不置空/删除 legacy `users.department_id` 或其 FK/index。Platform migrator 安装临时 PostgreSQL trigger bridge：`users` INSERT 建 Organization state（department 或 null tombstone，version 1）；legacy department 值变化时 upsert/increment state；Organization state INSERT/UPDATE 值变化时回写 legacy column；`users` DELETE trigger 始终安装并在 versioned Platform mode `legacy_delete_sync_enabled=true` 时为可能 direct-delete 的 legacy binary 物理删除 state。domain schemas/tables、bridge functions、Platform mode/audit 分别由独立 NOLOGIN owners 拥有；runtime login 不继承 owner、无 schema CREATE/ALTER trigger/mode-change 权限。bridge 使用 `SECURITY DEFINER SET search_path=pg_catalog`、fully qualified objects、distinct/recursion guard、`REVOKE ALL FROM PUBLIC` 和最小跨表示 DML。legacy delete handler 先切为 IAM `DeleteUsers`/outbox，之后由 short-lived authorized Platform operation 原子改 mode=false + immutable audit，才允许 event failure injection；rollback artifact 可 direct-delete 时先 mode=true + verify。migration/ops credential 不作为 runtime credential。持续比较 count/value/version。
3. 把 queries/sqlc generation 按 IAM、Organization、Admin BFF workflow owner 拆分；仅 reviewed Platform migration/backfill 与安装后的 temporary bridge trigger functions 允许跨 legacy/new 表示。
4. 增加 SQL ownership、module DB role 和真实 PostgreSQL expand/backfill/dual-write/rollback-compatibility 测试。
5. Platform migrator 独占统一 version history；owner-specific independent histories 延期到 Organization 物理抽取。

**Gate**: BFF 新读路径不读取 `users.department_id`，legacy router 在双写窗口仍正确；两种表示持续一致；任何运行模块都不能访问其他 owner 的生成 query API。

### Phase 3 — IAM module migration

1. 迁移密码、token、sessions、identity、roles、permissions 和 authorization 到 IAM。
2. 将 `rbac` 内角色与受管用户身份/角色操作移入 IAM。
3. 保留注册的用户 + 默认角色 + session 本地事务原子性。
4. 增加 `provisioning`/`active`/`disabled` 生命周期和 `users.version`；认证只允许 active；`ActivateUser` 仅允许 provisioning→active，重新启用不在本 feature。
5. 每个 IAM workflow command 的 side effect 与 command receipt 在同一事务；提供 `ResolveCommand(operation_id, command_name, expected_fingerprint)`，超时后先解析再重试/补偿。
6. 把 `admin-init` 改为调用 IAM 管理 port，继续只提升已注册用户且不接收密码。
7. IAM 用户批量删除在本地事务内全批次预检、删除、写 command receipt 和 outbox；删除事件 aggregate version 为删除前 `users.version + 1`。

**Gate**: IAM 只使用 IAM-owned SQL，认证安全、角色保护、权限即时生效和删除原子性测试全通过。

### Phase 4 — Organization module migration

1. 迁移部门树、部门 CRUD 和成员关系到 Organization。
2. 删除保护改为查询 `organization_user_departments`，不访问 `users`。
3. 提供单用户、batch user IDs、按部门列 user IDs、set/clear/restore membership 的粗粒度 ports；update/restore 使用 expected membership version CAS。
4. 每个 Organization workflow command 与 command receipt 同事务，并提供 `ResolveCommand`。
5. 实现 Organization inbox consumer：type/version 校验失败不写 successful inbox；成功 dedupe row 与 membership 删除同事务。失败可观测性由 producer outbox attempt/blocked state 和 metrics/logs承担。

**Gate**: Organization SQL 不出现 users/sessions/roles/permissions 表；部门和 membership 测试仅依赖 Organization-owned schema。

### Phase 5 — Admin BFF routing and composition

1. 将所有公开 `/api/v1` route、validation、DTO、error mapping 和 permission matrix 迁到 Admin BFF。
2. `/auth/me` 组合 IAM identity/roles/permissions 与 Organization department。
3. 用户列表：
   - 无部门筛选：IAM 分页 + Organization 当前页 batch lookup。
   - 有部门筛选：Organization 先给 user IDs，IAM 在集合内筛选/分页，再 batch lookup。
4. 实现持久化幂等 workflow execution lease（owner/expiry/claim token）、expired-`running` atomic reclaim、immutable 10 attempts/original 24h retry budget、non-worker `awaiting_client_input`、persisted `next_retry_at` 和 participant receipt resolution：
   - 创建：preflight → IAM provisioning → Organization membership → IAM activate；activation timeout 必须先 ResolveCommand，不能直接补偿。
   - 更新：保存旧/写后 membership version 与 IAM expected version → Organization CAS update → IAM CAS update → 失败时只在 current version 等于 workflow 写后 version 时恢复。
   - 删除：IAM authoritative delete + outbox；Organization 事件清理。
5. `admin_workflow_subjects` 为单个/批量 workflow 的每个 subject 保存 expected/result version，并对 active subject 建 partial unique；IAM batch delete 使用一个含规范化 per-user results 的 receipt。
6. workflow 增加 terminal `rejected` 与 deadline-bound `awaiting_client_input`；password-bearing command 未提交且 request 结束时，期限前只有 same-key authorized HTTP retry 可在内存重提 credential，等待不消耗 forward attempt。期限后 worker 只能无副作用 reject `OPERATION_EXPIRED` 或 version-CAS 恢复已应用 membership；冲突转 `failed_manual`。工作流/日志/fingerprint 不保存密码/token/hash/password-derived material。
7. 固定公开 workflow 错误码、`Retry-After` matrix 和 `Idempotency-Key` header；authentication/current route authorization 优先于 idempotency lookup，completed replay 只对仍有权限的 actor 返回。官方前端在用户写请求生成/复用 key，未知结果重试复用原 credential；有意修改 credential 使用新 key；最短 replay window 24h。
8. 每个 preflight 后 durable step 的新 forward participant side effect 前（原 HTTP request 内和恢复后）都重新查询 actor 当前权限；撤权则按 no-side-effect reject / partial-side-effect compensate / all-receipts finalize 决策。expired running reclaim 必须先 resolve last command；自动 budget 不 reset/extend；manual recovery 只 append immutable recovery action 并 receipt-finalize/approved compensate。
9. 通过 master kill switch + granular capability matrix 先 shadow 纯读取结果，再独立启用 auth/profile reads、user-list reads、department writes、role writes、managed-user create/update、legacy-delete IAM delegation、delete-event consumer/dispatcher，最后单独切 Admin BFF `/users/delete` route；create/update 与 delete route flags 不合并。Startup tests reject unsafe flag/mode combinations。

**Gate**: 同一契约测试对 legacy 与 BFF router 结果一致；BFF 无数据库领域访问和跨模块 transaction。

### Phase 6 — Transactional outbox/inbox delivery

1. 实现 canonical tuple `event_type=iam.user.deleted`, `event_version=1`；不把 `.v1` 存入 type。
2. IAM delete、command receipt 与 outbox insert 同事务；aggregate version 为 user tombstone version。
3. IAM 暴露 `OutboxDeliveryPort`（claim/ack/record-failure/block/requeue/backlog），Integration 不 import IAM sqlc。
4. dispatcher 在短 transaction 用 `FOR UPDATE SKIP LOCKED`：expired epoch 直接 block；否则 claim/reclaim 写 durable lease/token 并递增 epoch+total attempts。随后在 transaction 外调用 Organization；ack/record-failure/block 以 claim token CAS，record-failure 在一个 transaction 原子选择 pending/backoff 或 blocked。
5. unsupported event 进入 blocked；每个 delivery epoch 最多 20 个 claimed attempts 或 `epoch_started_at + 24h`；crash-after-claim 已计 attempt。Organization 成功 inbox dedupe 后 IAM 标记 published。受控 requeue 只接受 authenticated Platform `RecoveryContext`/approved reason，令新 epoch start=initial available time、保留 total attempts，并写 immutable full previous-epoch/authorization audit；blocked 无 waiver-to-purge path。
6. Organization inbox 只保存成功处理记录；失败由 outbox attempt/blocked state 及 metrics/logs 表示。
7. 增加 backlog、oldest age、lease recovery、attempts、blocked、dedupe 指标和隐私安全日志。

**Gate**: 每个已提交 IAM 删除都有 durable event；重复事件只产生一次有效 Organization 变化；故障注入无丢事件。

### Phase 7 — Observability, configuration and health

1. 配置区分 IAM、Organization、BFF workflow 和 dispatcher；支持同 URL/不同 pool 或 DB role。
2. readiness 分别报告 IAM DB、Organization DB、workflow store 和 dispatcher 状态。
3. 在公开请求和 port 调用间传播 request ID/correlation/trace context。
4. 添加模块/port latency、error class、compensation、outbox/inbox 指标与 spans。
5. 注入 logger，移除 package-global slog 依赖；统一 module/service 属性。

**Gate**: 可以从一次 BFF 请求定位 IAM/Organization 延迟或失败，且日志中无秘密和不必要 PII。

### Phase 8 — End-to-end validation and legacy removal

1. 验证登录、注册、logout、token recovery、`/auth/me`、动态路由、按钮权限、403 refresh、全部管理 CRUD、部门筛选分页、内置角色保护和 admin-init。
2. 运行全部 Go 测试、vet、sqlc generate/diff、迁移测试、前端类型/lint/format/build 和 `git diff --check`。
3. 稳定观察后停用 legacy public handlers、combined `RBACService`、global sqlc package 和跨域 runtime queries；回滚 binary/route 仍在支持窗口保留。
4. `users.department_id`、legacy FK/index 与兼容同步保持到明确定义的回滚窗口结束；本 feature 不做破坏性删除。后续 Organization 抽取/cleanup feature 在先验证最终一致后移除。

**Gate**: 所有 001/002/003 当前行为回归通过，架构测试通过，route switch 可关闭且数据可回滚。

## Rollout and Rollback

### Rollout

1. Platform migrator 先部署 additive schema/backfill，新 BFF master/granular routes 保持关闭；三个 DSN 必须指向同一物理 database。发布前记录 exact 004-compatible rollback artifact image/Git SHA/config manifest，并要求 extended compatibility suite 通过；pre-004 frozen baseline 不是 rollback binary。
2. 启用 compatibility dual-write bridge（legacy INSERT/department UPDATE ↔ Organization state INSERT/UPDATE，DELETE trigger installed + mode=true），验证 owner/membership/command receipt/workflow/outbox/inbox schema、catalog grants 和 bridge mode audit，并持续比较 legacy/new membership。
3. 只 shadow 无副作用读取；不得 shadow 会滑动 session 的认证调用。
4. 通过 granular flags 依次启用 BFF auth/profile reads、user reads、department writes、role writes、user create/update。删除链路先打开 legacy-delete IAM delegation，确认无 direct-delete 入口后用 short-lived authorized Platform operation mode=false；此后才允许 dispatcher/consumer acceptance 与 failure injection，同时 public `/users/delete` 仍走 delegated legacy handler。全部通过后再单独切 `ADMIN_BFF_USER_DELETE_ROUTE_ENABLED`；每阶段保持 dual-write 和独立可回切。
5. 观察 HTTP 状态/错误码、401/403 比率、idempotency replay/conflict、receipt resolution、CAS conflict、补偿失败、outbox backlog/oldest age/lease/blocked、inbox dedupe 和模块延迟。
6. Platform append-only rollout gate 持久化 capability manifest/bridge mode、sample cadence、row/version/checksum parity、mismatch/gap、rollback artifact suite 和 approval；任一 mismatch/monitoring gap 重置 72h window。只有完整 passing 72h record + 后置 approval 才停用 legacy route；随后保留 7 个自然日 rollback support window。cleanup 由 owner dry-run/purge ports 取最保守 watermark；窗口结束、零 unresolved、parity/approval 全满足前不删除。

### Rollback

1. freeze 管理写入和新 outbox producer；dispatcher 进入 drain mode，而不是立即停止。
2. 恢复 expired leases 并 drain retryable pending；然后 stop new claims。通过 participant command receipts 解析/快照未完成 workflow，snapshot remaining leased/blocked 与 claim/attempt evidence。
3. reconcile 已删除 IAM user 对应的 stale Organization state/exclusions；在仍运行新代码时 final-sync Organization state 到 `users.department_id`，验证双向 mapping、legacy FK/index 和 count/value parity。
4. 如果目标 004-compatible rollback artifact 的 legacy handler 仍可能 direct-delete `users`，通过 short-lived authorized Platform operation 将 DELETE bridge mode=true，验证 immutable mode audit 和 trigger behavior；只有第 3 步和该 safety gate 通过后，才按 granular route matrix 恢复 rollback wiring 并部署**预先 pin/hash 且通过 extended suite**的 artifact。不得在 7-day support window 部署不理解 idempotency/workflow contract 的 pre-004 binary。否则保持 writes frozen 并进入 reconciliation。
5. 验证角色/权限、session、认证、idempotency replay/current authorization precedence 和 exact workflow errors。
6. 不在即时回滚中 drop `admin_workflows`、command receipts、outbox/inbox 或 compatibility schema；将其作为 dormant additive evidence 保留。不得回滚 `000004`/`000005`。

## Preparation for First Physical Extraction

本 feature 完成后，Organization 应满足：

- 新 runtime model 独占 departments 与 membership 数据。
- `organization_user_departments.user_id` 对 IAM user ID 仅做 opaque reference、无跨域 FK；legacy `users.department_id` FK 仅作为限时兼容/回滚结构，物理抽取 feature 必须先结束该窗口并清理。
- SQL 不访问 IAM 表，事务不跨模块。
- Admin BFF 只依赖 coarse-grained/batch Organization ports。
- errors/models 不包含 Gin、pgx、sqlc 类型。
- IAM user deletion 已通过版本化事件跨边界传播。
- 同一 Organization contract tests 可运行于进程内 adapter 和未来远程 adapter。
- 下一 feature 可独立决定 gRPC、deadline、mTLS、服务发现、独立迁移历史和数据库复制/切换。

## Complexity Tracking

> No constitution violations. The following complexity is required by current cross-domain behavior, not speculative infrastructure.

| Complexity | Why Needed | Simpler Alternative Rejected Because |
|---|---|---|
| 持久化 `admin_workflows` | 跨 IAM/Organization create/update 需要跨重启幂等、步骤恢复和补偿可观测性 | 仅内存编排在进程崩溃后无法判断半完成状态；共享事务未来不可用。 |
| `provisioning` 用户状态 | 防止成员关系尚未完成或补偿失败的管理创建用户提前登录 | 先创建 active 再 best-effort 删除存在安全窗口。 |
| transactional outbox/inbox | IAM 删除和 Organization membership 清理必须在崩溃/重复投递下可靠 | 提交后直接调用存在丢事件窗口；broker 超出当前阶段。 |
| 三个 sqlc owner 包 | 用编译和测试强制数据所有权，为拆库准备 | 单一 `Querier` 只能靠约定，当前已造成跨域访问。 |
