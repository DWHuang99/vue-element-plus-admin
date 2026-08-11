# Research: 微服务拆分准备（模块化单体边界）

**Feature**: `004-microservice-splitting`  
**Date**: 2026-08-10

## Research baseline

本研究以当前工作树为实现基线，包括尚未提交的权限目录、授权中间件、`/auth/me.effective_permissions`、前端动态权限过滤和管理员初始化流程。

历史计划状态：

| Feature | 计划/任务状态 | 对本 feature 的影响 |
|---|---|---|
| `001-backend-scaffold` | `tasks.md` 43/43 完成；spec 元数据仍为 Draft | 复用 Go/Gin/PostgreSQL/sqlc、迁移、健康检查、结构化日志和测试基线。 |
| `002-user-auth` | `tasks.md` 40/40 完成 | 认证安全和会话语义是 IAM 不可回退的稳定基线。 |
| `003-rbac-permission-management` | `tasks.md` 0/32 勾选，但功能及后续权限闭环已实际完成 | `tasks.md` 已陈旧；以当前代码、最新 Auth/RBAC contracts 和回归测试为准。 |

可复现 compatibility baseline 冻结为：

- 仓库 HEAD：`d4b1bae75b37f894879360099066ad49ab78da91`（仅仓库基线；权限闭环还包含未提交工作树变化）。
- Auth contract v1.1.0：`specs/002-user-auth/contracts/auth-api.md`，SHA-256 `3efc6933ffca57596bcb454801434d61a1f811721ab25b32f8d21f3ff5597bee`。
- RBAC contract v1.1.0：`specs/003-rbac-permission-management/contracts/rbac-api.md`，SHA-256 `6254e5f4c8b5c64912d400a7b06fb4da09632e3ee6cc2e7b0ffed1ab0dc52a8f`。
- tracked implementation worktree binary diff（排除 `specs/**`、`.specify/**`）SHA-256 `aa6aeb6265b00b50bb85cfbc120442fb37322988f10ebdc9bdc3f92c3057867a`。
- 15 个 untracked implementation files 的 sorted `<sha256><two-spaces><path>` manifest SHA-256 `5b8a82ab168561d7d0bf68dbb4c13afd2210f21f57374604f1296510204524e0`（同样排除 `specs/**`、`.specify/**`）。
- 可重建实现快照：`specs/004-microservice-splitting/baseline-implementation.patch`，123047 bytes，SHA-256 `659facfdf93a49bf2caa7e09728451029271ace9b61ab31da47dc5dd0e57e2ee`。它把上述 tracked binary diff 与 15 个 untracked implementation files 合并为可对 frozen HEAD 执行 `git apply --binary` 的单一 patch。
- exact Auth/RBAC v1.1.0 snapshots：`specs/004-microservice-splitting/baseline-contracts.patch`，9008 bytes，SHA-256 `f69c52dfefa706d6411c56be1ee90e74f0794c26f48dbc95973a4c9cea9bfa38`；它包含 frozen HEAD 到上述两个 exact contract hashes 的 bytes，不依赖 mutable current worktree。

重建验证必须在 disposable detached worktree 中从 frozen HEAD 开始，依次校验并 apply implementation/contract patches（先 `git apply --binary --check`）；不得在当前含未提交改动的工作树中尝试应用。若旧 quickstart/任务描述冲突，以冻结契约、004 自包含 compatibility contract 和同一套回归测试为权威，不使用模糊的“latest contract”。

## Current-state findings

当前后端按包名看似模块化，但还不是按数据所有权隔离的模块化单体：

- `backend/internal/auth/service.go` 的 `GetUserProfile` 同时读取用户、部门、角色和有效权限，是实际的 BFF 聚合。
- `backend/internal/rbac/service.go` 同时管理 IAM 角色、Organization 部门和跨领域用户管理。
- `RBACService.SaveUser` 在一个 PostgreSQL 事务中混合凭据、用户资料、角色和部门关系。
- `users.department_id` 形成 IAM 用户表到 Organization 部门表的跨领域外键。
- `CountUsersByDepartmentID` 等查询允许 Organization 逻辑读取 IAM 用户表。
- 单个 `backend/internal/database/sqlc.Querier` 暴露所有查询，任何模块都可访问任何表。
- `cmd/server/main.go` 直接组装所有领域、迁移、路由、限流和生命周期；公开 HTTP handler 分散在 `auth`/`rbac` 包。
- `cmd/admin-init/main.go` 直接使用全局 sqlc 查询，绕过 IAM 应用边界。
- 当前配置、readiness 和日志只识别一个未分域的数据库依赖；尚无主动 tracing/metrics 初始化。

这些是 Organization 物理抽取前的阻塞点。

## Decision 1: 先建立模块化单体，不立即部署微服务

**Decision**: 本 feature 只在单进程内建立 IAM、Organization、Admin BFF 和 integration/platform 边界，模块间使用普通 Go 接口与进程内 adapter。

**Rationale**:

- 当前耦合主要来自数据所有权、跨领域事务和公共 DTO 归属，而非进程数量。
- 先加入网络会把本地耦合转化为分布式故障，不能消除根因。
- 进程内接口可用相同契约测试稳定语义，后续只替换 Organization adapter。
- 符合宪章 II：当前阶段形成可运行闭环，不引入未使用的基础设施。

**Alternatives considered**:

- **立即拆 IAM 和 Organization 进程**：拒绝；`SaveUser`、`GetUserProfile`、`users.department_id` 和全局 sqlc 仍要求共享数据库/事务。
- **先拆 IAM**：拒绝；IAM 是认证信任根且被所有管理请求依赖，变化面和发布风险更大。
- **保持现状，仅新增目录**：拒绝；无法通过数据访问和依赖测试证明边界，目录重命名不构成可抽取性。

## Decision 2: 领域边界与依赖方向

**Decision**:

- **IAM** 拥有：`users` 的身份资料（username/account/email）、密码、sessions、roles、user_roles、permissions、role_permissions、认证和授权。
- **Organization** 拥有：departments、部门层级、用户到部门成员关系。
- **Admin BFF** 拥有：所有公开 `/api/v1` Gin transport、HTTP DTO、错误映射、认证/权限中间件、跨领域读取聚合和写工作流状态；不拥有身份或组织领域事实。
- **Platform/Integration** 提供：进程配置、连接池、迁移执行、日志/Tracing/Metrics、健康检查、outbox dispatcher 通用运行机制。

依赖方向：

```text
cmd/server (composition root)
  ├── Admin BFF HTTP adapter
  ├── IAM application + IAM PostgreSQL adapter
  ├── Organization application + Organization PostgreSQL adapter
  └── in-process dispatcher

Admin BFF ──> IAM ports
Admin BFF ──> Organization ports
IAM         -X-> Organization
Organization-X-> IAM
Admin BFF   -X-> pgx/sqlc/migrations
```

**Rationale**: 公开 API 是管理端专用聚合接口，放在 BFF 能使领域模型不受 Vue DTO、Gin 和状态码约束。

**Alternatives considered**:

- **Organization 拥有 account/email**：拒绝；它们是登录主体的身份资料，现有筛选和用户名管理均由 IAM 更自然地拥有；Organization 只保存不带跨库 FK 的稳定 `iam_user_id` 与部门关系。
- **保留通用 RBAC 模块**：拒绝；当前 `rbac` 同时包含角色、部门、用户聚合，正是边界不清的来源。
- **建立通用 repository framework**：拒绝；只为现有用例定义窄 port，避免无用抽象。

## Decision 3: Admin BFF 保持唯一浏览器入口

**Decision**: Vue 始终访问同一 origin 的 `/api/v1`。生产入口为 Nginx（静态 Vue + `/api/*` 反代）到 Admin BFF，代理不得移除 `/api`。IAM/Organization 的进程内或未来远程调用不经过边缘 Nginx。

**Rationale**:

- 保持当前 Axios、Pinia、路由守卫、401/403 和 CORS 假设。
- 避免浏览器持有多个服务地址、内部凭据或服务拓扑。
- BFF 可稳定现有 DTO，并独立演进内部接口。

**Alternatives considered**:

- **Vue 直接访问多个服务**：拒绝；增加 CORS、认证传播和前端聚合复杂度，暴露内部边界。
- **BFF 到服务也经过 Nginx**：拒绝；边缘代理不应成为内部调用必经路径。

## Decision 4: 公共 HTTP 契约零破坏迁移

**Decision**: Admin BFF 完整保留现有 `/api/v1` 路径、JSON envelope、snake_case 字段、状态码、错误码和权限码。

必须保留：

- 401 `AUTH_INVALID_TOKEN` 与 403 `AUTH_FORBIDDEN` 的区分。
- 403 不撤销会话，前端刷新 `/auth/me` 并重建权限路由。
- `/auth/me.effective_permissions` 返回去重、有序、非 null 数组。
- 所有管理写接口和 logout 返回 `200 {"data":{}}`。
- `admin`/`super_admin` 具备六个管理权限，`user`/自定义角色默认无管理权限。
- 内置角色 code 不可修改、角色不可删除。
- 后端每个受保护管理请求使用当前授权状态，不加入授权决定缓存；current authentication/route authorization 先于 idempotency lookup/completed replay。
- staged rollout 使用 master kill switch + granular capabilities（auth/profile reads、user reads、department/role writes、managed-user create/update、legacy-delete delegation、delete-event consumer/dispatcher、独立 Admin BFF `/users/delete` route）。Deletion infrastructure 先在 delegated legacy public route 下验证，最后才切 BFF delete route；startup 拒绝 unsafe combinations。
- rollback support window 只允许预先 pin/hash 且通过 extended compatibility suite 的 004-compatible artifact；frozen pre-004 baseline 是 characterization input，不是 deployable rollback binary。

**Rationale**: 架构迁移不应要求前端同步切换；Admin BFF 的价值之一是隔离内部变化。

## Decision 5: 用窄应用 port 与独立 sqlc 包强制所有权

**Decision**:

- IAM 与 Organization 各自定义面向用例的 application ports 和 repository ports。
- `sqlc` 按 owner 拆分 queries/schema 输入和生成包，生成代码仅在对应 PostgreSQL adapter 内使用。
- Admin BFF 禁止 import pgx、sqlc 生成包或迁移包。
- IAM 和 Organization 互相禁止 import。
- 用 Go AST/import 和 SQL ownership 测试自动检查规则。

**Rationale**: 单个全局 `Querier` 让包边界无法限制数据访问；窄接口使进程内和未来远程 adapter 可共享语义。

**Alternatives considered**:

- **继续一个 sqlc 包，但约定不乱用**：拒绝；无法机械验证，后续回归风险高。
- **ORM 替换 sqlc**：拒绝；违反宪章 III，也不能自动解决领域所有权。
- **每张表一个 repository**：拒绝；接口应围绕用例和事务，而不是机械映射表。

## Decision 6: 用 Organization 成员表消除跨领域外键

**Decision**: 新增 Organization-owned `organization_user_departments`：

- `user_id BIGINT PRIMARY KEY`，表示稳定 IAM 主体 ID。
- `department_id BIGINT NULL REFERENCES departments(id)`；NULL 是持久化、可版本化的无部门 tombstone，只保留域内 FK。
- `membership_version BIGINT NOT NULL`；set/clear/restore CAS 递增，clear 不删除 row，防止 absent-state ABA。
- 不对 IAM `users` 建 FK。
- 第一代保持每个用户零或一个当前部门。

迁移采用 expand → backfill → compatibility dual-write → continuous compare → BFF read cutover → final sync → legacy route disable。回滚窗口内不置空/删除 `users.department_id` 或旧 FK/index；legacy path 仍可正确运行。新 BFF 读模型只读 Organization membership，兼容 bridge 负责双写，后续 cleanup/物理抽取 feature 才删除旧表示。

**Rationale**: 跨服务数据库 FK 在物理拆分后不可保留；Organization 必须仅凭自己的数据判断部门是否仍有成员。

**Alternatives considered**:

- **保留 `users.department_id`**：拒绝；Organization 无法独立迁移或校验删除。
- **立即删除旧列**：拒绝；破坏快速回滚能力。
- **允许一用户多部门**：拒绝；改变现有业务语义，超出本 feature。

## Decision 7: BFF 直接组合查询，不先建设持久化用户投影

**Decision**:

- `/auth/me`：IAM 身份/角色/权限 + Organization 单用户部门查询。
- 无部门筛选的用户列表：IAM 先分页并聚合角色，BFF 对当前页 user IDs 调 Organization batch lookup。
- 有 `department_id` 筛选：Organization 先返回该部门 user IDs，IAM 在此集合上应用 username/account 筛选与分页，BFF 再 batch lookup 当前页部门。

第一迭代不建立 BFF 用户目录 read model。

**Rationale**:

- 当前为小型后台，集合和页面规模有限。
- 可以保持过滤发生在分页前，避免错误 totals/page boundaries。
- 调用次数有界（通常 2–3 次）且不保留 N+1。
- 避免为尚未拆出的服务引入投影重建、延迟一致性和额外迁移。

**Alternatives considered**:

- **BFF 持久化 user-directory projection**：暂缓；规模增大、集合过滤成本不可接受或真实拆服务后再引入。
- **先分别分页再内存合并**：拒绝；会破坏 department filter、total 和页面边界。
- **保留跨域 SQL JOIN**：拒绝；阻塞物理拆分。

## Decision 8: 跨领域写使用持久化工作流、幂等和补偿

**Decision**: Admin BFF 不打开跨模块数据库事务，使用 `admin_workflows` 记录 operation ID、可选 public idempotency key、非秘密请求指纹、步骤状态、subject user ID、expected/applied versions、补偿状态和最终结果。每个 participant command 的 side effect 与 owner-local command receipt 同事务；BFF 在 timeout/crash 后先 `ResolveCommand(operation_id, command_name, expected_fingerprint)`。工作流状态不是领域事实。

- **创建受管用户**：预检角色与部门 → IAM 创建 `provisioning` 用户及角色+receipt（本地事务）→ Organization 设置成员关系+receipt → IAM 仅执行 `provisioning -> active`+receipt。activation timeout 先解析 receipt，不能直接补偿。`provisioning` 用户不能登录且不出现在正常用户列表。
- **更新用户**：Organization 以 nullable `department_id` + durable `membership_version` 表示有/无部门；clear 递增 version 而不删除 row，避免 ABA。预检并读取旧部门/version → Organization CAS 改成员状态 → IAM 用 expected user version 原子更新身份/密码/角色 → IAM 已知失败时仅在 current membership version 仍等于 workflow applied version 时恢复。
- **删除用户**：IAM 全批次预检并原子删除用户、sessions、user_roles，同时写 command receipt 和 `event_type=iam.user.deleted`, `event_version=1` outbox；Organization 通过事件幂等清理成员关系。
- **注册**：完全属于 IAM；用户、默认角色和 session 继续在 IAM 本地事务中原子创建，直接为 active。

credential 或原始 token 不写入 `admin_workflows`、日志或事件。若 password-bearing participant receipt 未提交而 HTTP request 已结束，workflow 进入 non-worker `awaiting_client_input`；只有 same-key/currently-authorized HTTP retry 可在内存重提 credential。Expired `running` 可 atomic reclaim，但必须先 resolve 最后 command；自动 10-attempt/original-24h budget 不 reset/extend。每个 durable step 的新 forward side effect 前（包括原 request 内）重新授权；manual recovery 只 receipt-finalize/补偿并写 immutable action。

**Rationale**: 这使故障语义可在单进程阶段测试，并避免将共享事务带入服务化。创建阶段使用 provisioning 状态满足身份安全优先，避免补偿窗口内新用户提前登录。

**Alternatives considered**:

- **继续跨域数据库事务**：拒绝；未来无法跨数据库。
- **仅 best-effort 调用、不记录工作流**：拒绝；崩溃后无法判断完成步骤和安全重试。
- **把密码写入工作流以自动重放**：拒绝；违反秘密最小化。
- **两阶段提交**：拒绝；基础设施和运行复杂度过高，不适合当前规模。

## Decision 9: 只实现一个真实事件的 outbox/inbox 闭环

**Decision**: 第一迭代只实现当前有真实消费者的 `iam.user.deleted` version 1：

- IAM 删除用户、写一个包含规范化 per-user result/version 的 batch command receipt 和 `iam_outbox_events` 在同一事务；BFF 用 per-subject workflow rows 锁定批量中的每个 user。删除事件 `aggregate_version` 来自删除前 `users.version + 1` tombstone version。
- IAM 暴露 delivery port；Integration 不 import IAM sqlc。dispatcher 用短事务 `FOR UPDATE SKIP LOCKED`：过期 epoch 直接 blocked，否则 claim/reclaim 持久化 lease/token 并递增 epoch+total attempts；释放事务后调用 Organization，故 crash-after-claim 也计 budget。
- Organization 只在成功消费时将 inbox dedupe row 与删除成员关系同事务提交；失败不留下与回滚算法矛盾的 `failed` inbox row。
- IAM ack/record-failure/block 通过 claim token CAS；consumer success 后标记 published。`RecordOutboxFailure` 在一个 transaction 原子选择 pending/backoff 或 `blocked(delivery_exhausted)`，避免 reschedule 后无法 block；每 epoch 最多 20 claimed attempts 或 initial eligibility +24h；unsupported schema 立即 durable blocked。
- 受控 requeue 只接受 authenticated Platform recovery context/approved reason，递增 `delivery_epoch`、令 epoch start=initial available time、重置本 epoch attempt、保留 `total_attempt_count`，并把完整 previous epoch/block/authorization evidence 写入 immutable `iam_outbox_requeues`。Blocked 无 waiver-to-purge path。
- 崩溃导致的 lease recovery 与重复投递由 outbox lease + inbox 去重收敛。

事件 payload 只包含 `user_id`，不包含 username、email、密码、token、hash 或权限列表。

**Rationale**: 以真实删除用例验证事务性事件机制，避免建设没有消费者的事件总线。

**Alternatives considered**:

- **提交后直接调用 Organization，不写 outbox**：拒绝；存在提交成功但通知丢失窗口。
- **引入 Kafka/RabbitMQ/Redis Streams**：拒绝；当前仍单进程，消息 broker 选择留到物理拆分。
- **预先为所有实体发布事件**：拒绝；违反宪章 II 的当前需求约束。

## Decision 10: gRPC 只作为未来 adapter，不在本 feature 定义 proto/runtime

**Decision**: ports 使用 transport-neutral 模型和 coarse-grained/batch 操作，契约用 Markdown 描述。本 feature 不加入 protobuf、gRPC server/client、服务发现或远程重试库。

**Rationale**: 先稳定业务语义；Organization 物理抽取时再把同一契约映射为 gRPC，并根据真实部署决定 deadline、mTLS、服务发现和兼容策略。

**Alternatives considered**:

- **现在生成 proto 但不运行**：拒绝；容易形成未被测试的第二套契约，且属于未使用基础设施。
- **未来使用 HTTP/JSON 内部调用**：保留为可选 adapter；当前不锁定，但 Organization 抽取时优先评估 gRPC。

## Decision 11: 迁移所有权分阶段演进

**Decision**:

- 已应用的 `000001`–`000005` 冻结，不重写历史。
- 本 feature 的 additive/bridge forward migrations 增加 membership、user lifecycle/version、command receipts、admin workflow、IAM outbox 和 Organization inbox。
- 唯一 Platform migrator 推进统一 version history，并给 migration 标 owner/bridge metadata；运行模块不会各自竞争同一 version table。
- 查询和 sqlc 立即按 owner 拆分。compatibility mechanism 固定为 Platform migrator 安装的临时 PostgreSQL trigger bridge：legacy `users` INSERT 建立 Organization state（department 或 null tombstone，version 1）；`users.department_id` 值变化时 upsert/increment state；Organization state INSERT/UPDATE 值变化时回写 legacy column；初始 `users` DELETE trigger 为仍可能 direct-delete 的 legacy binary 同步物理删除 state。
- `users` DELETE trigger 始终安装并读取 Platform-owned versioned `legacy_delete_sync_enabled`。在接受 BFF delete-event failure semantics 前，legacy delete handler 必须先改为调用 IAM `DeleteUsers`/outbox；确认所有删除入口后，由 short-lived authenticated Platform operation 原子 mode=false + immutable audit，且 failure injection 只能在 mode=false 后运行。Rollback artifact 仍可能 direct-delete 时，切换前 mode=true + verify。
- IAM/Organization schema/tables、Platform mode/audit 和 bridge functions 分别由独立 NOLOGIN owner roles 拥有；runtime login 不继承 owner、无 schema CREATE/ALTER trigger/mode-change privilege。bridge owner 只拥有 `SECURITY DEFINER SET search_path=pg_catalog`、fully qualified/guarded functions 和最小跨表示 DML；PUBLIC 无 execute，migration/ops credential 不作为 runtime credential。Catalog tests 证明 runtime 不能 disable/replace/change-owner trigger/functions 或 self-grant。
- IAM、Organization、Admin BFF DSN 在本 feature 必须指向同一物理 PostgreSQL database，可用不同 pool/role。Organization 物理抽取时再建立独立 migration history/database。

**Rationale**: 不修改已发布迁移，同时使未来拆库有清晰输入。迁移本身可临时跨域读取以回填，但运行时查询禁止跨域。

**Alternatives considered**:

- **立即把历史迁移移动并重编号**：拒绝；可能破坏现有 `schema_migrations` 状态。
- **立即维护多个 migration version 表**：暂缓；切换成本和回滚风险高，物理拆分时再完成。
- **永远保持统一迁移流**：拒绝；Organization 无法独立发布。

## Decision 12: 授权在 BFF 实时执行，内部传递最小 actor context

**Decision**:

- BFF 通过 IAM `Authenticator` 验证 opaque session，通过 IAM `Authorizer` 查询当前权限。
- 权限绑定仍在公开路由层；401/403 语义不变。
- BFF 向内部用例传递 `ActorContext`（稳定 user ID、correlation/trace 信息及已验证的调用上下文），不传原始 token。
- Organization 不在每个操作中反向调用 IAM；它信任当前同进程的 BFF 调用边界。物理拆分时用服务身份和可信元数据保护内部端点。

**Rationale**: 避免普通业务服务对 IAM 的逐请求远程依赖，同时保持管理入口的实时授权。

## Decision 13: 模块级配置、健康检查和可观测性先落地

**Decision**:

- 配置区分 IAM、Organization、BFF/integration 依赖；第一迭代所有 DSN 必须指向同一物理 PostgreSQL database，同时支持不同 pool/最小权限 DB role。
- readiness 分别报告 `iam_database`、`organization_database`、`admin_workflow_store` 和 dispatcher 状态。
- 所有公开请求和模块 port 调用传播 request ID/correlation ID；为 IAM/Organization port 添加 span/metric 边界。
- 记录 HTTP/port latency、错误分类、补偿失败、outbox backlog/oldest age、inbox dedupe/failure。
- 日志带 module/service 属性，不记录密码、完整 token、password hash、数据库 URL 或不必要 PII。

**Rationale**: 可抽取性同时要求独立运行信号；进程内 span 边界之后可自然变为 gRPC client/server spans。

## Decision 14: 第一物理拆分目标为 Organization

**Decision**: 完成本 feature 后，下一独立 feature 优先抽取 Organization：独立数据库/迁移进程、远程 adapter（优先评估 gRPC）、mTLS/deadline/重试和部署拓扑。

**Rationale**:

- Organization 对认证主链影响较小。
- 数据模型简单，读写流量低，易验证。
- 本 feature 已消除 Organization 新 runtime model 对 IAM 表、跨域 FK 和共享事务的依赖；物理抽取 feature 先结束 rollback window 并清理 legacy `users.department_id` FK/bridge。
- IAM 留在 BFF 同进程可降低首个物理拆分的安全风险。

## Resolved clarifications

| Topic | Resolution |
|---|---|
| 是否现在拆多个进程 | 否；先模块化单体。 |
| 服务内部通信 | 当前 Go ports；物理拆分时再实现 gRPC adapter。 |
| 第一个物理服务 | Organization。 |
| account/email 所有权 | IAM。 |
| 部门成员关系 | Organization-owned 独立表，无 IAM FK。 |
| 用户列表聚合 | BFF bounded composition，不建持久化 read model。 |
| 跨模块事务 | 禁止；持久化工作流 + provisioning + 补偿。 |
| 异步基础设施 | PostgreSQL outbox/inbox + 进程内 dispatcher；无 broker。 |
| 权限检查 | Admin BFF 调 IAM 实时判定；无授权缓存。 |
| 迁移历史 | 冻结 000001–000005；唯一 Platform migrator 保留统一 history + owner/bridge metadata。 |
| 前端改动 | URL/payload 保持兼容；受管用户写请求新增可选 `Idempotency-Key`，官方前端为一次逻辑提交生成并复用。 |
