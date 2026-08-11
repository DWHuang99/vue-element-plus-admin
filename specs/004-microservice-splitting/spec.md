# Feature Specification: 微服务拆分准备（模块化单体边界）

**Feature Branch**: `004-microservice-splitting`（Spec Kit 逻辑 feature；当前 Git 工作分支仍为 `develop-wsl`）

**Created**: 2026-08-10

**Status**: Draft

**Input**: User description: “已完成的 plan 和未完成的 plan 都要阅读，当前属于微服务拆分 feature。”

## Scope

本 feature 不立即部署多个微服务，而是在保持现有 Vue 管理端和 `/api/v1` HTTP/JSON 契约兼容的前提下，把当前后端整理为边界可验证的模块化单体，为后续按进程拆分服务建立安全迁移路径。

目标领域边界：

- **IAM**：账户、密码、会话、角色、权限、用户角色关系以及认证/授权判断。
- **Organization**：部门、部门层级和用户的部门成员关系。
- **Admin BFF**：浏览器侧公共 API、管理端 DTO 适配、`/auth/me` 与用户管理等跨领域聚合及工作流编排；不拥有 IAM 或 Organization 领域数据。

第一个物理拆分候选是 Organization Service。浏览器继续通过 Nginx 访问 Vue 静态资源和 `/api/*`；Nginx 不移除 `/api` 前缀。服务真正拆分后，内部同步调用可由进程内接口替换为 gRPC adapter，跨服务状态传播采用版本化领域事件和 transactional outbox/inbox。

## User Scenarios & Testing *(mandatory)*

### User Story 1 - 保持管理端行为兼容地建立模块边界 (Priority: P1)

作为现有管理后台使用者，我希望登录、恢复会话、查看个人信息以及管理用户、角色和部门的行为保持不变，即使后端内部已按 IAM、Organization 和 Admin BFF 重新组织，以便架构演进不会中断现有工作。

**Why this priority**: 微服务准备工作的首要约束是不能以架构重构为代价破坏已完成的认证和权限闭环。

**Independent Test**: 使用现有前端和公开 HTTP 契约运行完整认证/RBAC 回归；无需了解内部模块变化即可验证用户可继续完成原有操作，且 401、403、写接口响应和动态路由行为不变。

**Acceptance Scenarios**:

1. **Given** 用户拥有有效会话，**When** 请求 `GET /api/v1/auth/me`，**Then** 返回与现有契约兼容的用户、部门、角色和 `effective_permissions` 聚合结果，空权限为 `[]` 而非 `null`。
2. **Given** 用户缺少某个管理权限，**When** 请求受该权限保护的端点，**Then** 返回 `403 AUTH_FORBIDDEN`，且不会撤销会话。
3. **Given** 会话无效或已过期，**When** 请求受保护端点，**Then** 返回 `401 AUTH_INVALID_TOKEN`。
4. **Given** 管理员执行现有角色、部门或用户操作，**When** 操作成功，**Then** 写接口继续返回 `200 {"data":{}}`，不返回空 body 的 204。
5. **Given** Nginx 将浏览器请求转发到 Admin BFF，**When** 请求路径以 `/api` 开头，**Then** BFF 接收到的路径仍包含 `/api` 前缀。

---

### User Story 2 - Organization 可独立抽取 (Priority: P1)

作为后端维护者，我希望 Organization 对部门及成员关系拥有清晰的数据、查询、迁移和服务接口所有权，IAM 和 Admin BFF 不再直接读取或写入其表，以便下一次迭代能够只替换 adapter 就把 Organization 移到独立进程。

**Why this priority**: Organization 是首个物理拆分候选；如果仍存在跨领域 SQL JOIN、共享事务或宽泛 sqlc Querier，拆分会直接暴露为运行时故障和数据一致性问题。

**Independent Test**: 通过包级依赖测试、查询/迁移目录检查和集成测试证明 IAM 无法直接访问 Organization 存储，Admin BFF 仅通过定义的 port 完成部门查询和成员关系操作。

**Acceptance Scenarios**:

1. **Given** IAM 需要返回账户、角色或权限信息，**When** 执行业务逻辑，**Then** 不查询部门表或部门成员关系表。
2. **Given** Organization 需要显示部门成员，**When** 执行业务逻辑，**Then** 不读取密码、会话、角色或权限表。
3. **Given** Admin BFF 生成用户列表或 `/auth/me`，**When** 需要跨领域数据，**Then** 分别调用 IAM 和 Organization port 并在 BFF 内聚合，不使用跨领域 SQL JOIN。
4. **Given** 用户部门关系发生变更，**When** 更新完成，**Then** 变更在 Organization 自己的事务中原子提交并维护单调 version。本 feature 不要求为每次成员关系变更生产版本化事件；Organization membership 事件的生产明确延期到物理拆分 feature，按届时消费者实际需要定义。

---

### User Story 3 - 跨模块写操作具有明确一致性语义 (Priority: P2)

作为管理员，我希望用户保存、删除等跨 IAM 与 Organization 的操作在部分失败时给出可预测结果并自动补偿或可安全重试，而不是依赖一个无法跨服务保留的数据库事务。

**Why this priority**: 当前用户保存混合账户、密码、角色和部门写入；如果不先定义工作流、幂等和补偿，物理拆分后会产生半完成数据。

**Independent Test**: 在 IAM 或 Organization 调用的每个故障点注入失败，验证 BFF 工作流不会静默成功、不会产生无法识别的半完成状态，并可通过同一幂等键安全重试或通过已记录的补偿恢复。

**Acceptance Scenarios**:

1. **Given** 创建用户工作流的 Organization 成员分配失败，**When** IAM 已创建待完成账户，**Then** BFF 按已定义策略补偿或保留可识别、不可登录的待处理状态，并返回稳定错误码。
2. **Given** 更新现有用户时第二个领域写入失败，**When** BFF 执行补偿，**Then** 已成功的前序变更恢复到调用前状态，或产生可观测、可重试的待修复操作记录。
3. **Given** 客户端因超时重复提交同一写操作，**When** 使用相同幂等键重试，**Then** 不重复创建用户、角色关联或部门成员关系。
4. **Given** 删除操作包含不存在、受保护或不可删除的对象，**When** 进行全批次预检，**Then** 不发生部分删除。

---

### User Story 4 - 可靠传播领域变化 (Priority: P2)

作为服务维护者，我希望领域数据变更和对应事件在同一个本地数据库事务中写入，并由具备重试和幂等能力的发布/消费机制处理，以便未来进程拆分不会因双写导致状态丢失或重复应用。

**Why this priority**: 直接在提交数据库后同步发送消息存在丢事件窗口；没有 inbox 或幂等键则重试会重复处理。

**Independent Test**: 模拟发布器崩溃、重复投递和消费者重启，验证 outbox 事件不会丢失、重复事件不会重复改变领域状态、失败记录可观测且可重试。

**Acceptance Scenarios**:

1. **Given** 领域写入成功，**When** 事务提交，**Then** 同一事务中存在对应 outbox 记录。
2. **Given** 发布器在发送前或发送后崩溃，**When** 重新启动，**Then** 未确认事件被重试且不会丢失。
3. **Given** 消费者收到相同事件多次，**When** inbox 检测到已处理事件 ID，**Then** 后续投递不重复应用业务副作用。
4. **Given** 事件 schema 发生兼容扩展，**When** 新旧消费者并存，**Then** 通过事件类型和版本明确解释 payload。

---

### User Story 5 - 可独立运行、观察和逐步发布 (Priority: P3)

作为运维人员，我希望 IAM、Organization 和 Admin BFF 的配置、健康状态、日志与指标具有独立命名和边界，即使当前仍运行在同一进程中，也能提前验证未来拆分后的运行条件。

**Why this priority**: 代码边界成立并不代表服务可拆；配置、迁移、健康检查和可观测性仍共享时，独立发布不可验证。

**Independent Test**: 在单进程模式下分别检查三个模块的就绪状态、数据库依赖、关键调用指标和关联日志；通过故障注入确认某一模块不可用时 BFF 返回稳定错误且保留 request/trace 关联。

**Acceptance Scenarios**:

1. **Given** Organization 存储不可用，**When** readiness 检查运行，**Then** 明确指出 Organization 依赖未就绪，而不是只返回模糊的全局数据库错误。
2. **Given** BFF 聚合请求跨越多个模块，**When** 查看结构化日志和指标，**Then** 可通过 request ID/trace ID 关联调用并区分 IAM 与 Organization 延迟或错误。
3. **Given** 迁移被执行，**When** 检查迁移所有权，**Then** 只有 Platform migrator 推进统一 migration history；每个变更都有 IAM、Organization、Admin BFF 或 bridge owner 标记，运行模块本身不越权执行或访问其他领域 schema。
4. **Given** Organization adapter 从进程内实现切换为远程实现，**When** 运行公开契约测试，**Then** 前端和公开 HTTP API 无需修改。

### Edge Cases

- 用户没有部门、部门已删除或成员关系暂时不可用时，BFF 必须按公开契约返回明确的空值/降级错误，不得伪造部门数据。
- 用户拥有多个角色或权限重复授予时，有效权限仍为去重、有序并集，并在每个受保护请求上使用当前授权状态。
- `admin` 与 `super_admin` 在第一代仍拥有六个管理权限；`user` 和新建自定义角色默认不拥有管理权限。
- 内置角色 `super_admin`、`admin`、`user` 的 code 不可修改且角色不可删除；模块拆分不得绕过该规则。
- BFF 聚合中 IAM 成功但 Organization 超时，或反之时，必须返回稳定、可观测的依赖错误；不得缓存或返回可能被误认为完整的新鲜结果。
- outbox 记录长时间无法发布时，必须有重试上限/退避、失败状态和运维可见性，不能无限热循环。
- 事件可能乱序、重复或延迟到达；消费者必须依据聚合版本或幂等记录避免旧事件覆盖新状态。
- 迁移升级中存在旧 `users.department_id` 数据时，必须先 expand/backfill，再在兼容窗口双写和持续校验；BFF 切换读取后旧列仍保持同步，直到后续 cleanup feature 结束回滚窗口。
- 管理员初始化必须继续只提升已注册用户，不接受密码、不创建默认账户，也不记录数据库秘密。

## Security Threat Model

| Threat | Risk | Required mitigation | Verification |
|---|---|---|---|
| Forged or overprivileged `ActorContext` | 调用者绕过 BFF 权限或冒充管理员 | ActorContext 只能由 BFF authentication/authorization middleware 构造；内部 model 不接受客户端直接反序列化；Organization 接口不接收原始 token。物理拆分时必须再加入服务身份/mTLS。 | handler/port tests 尝试注入客户端 actor 字段；架构测试禁止领域 handler 直接暴露。 |
| Idempotency-key collision/replay across actors | 一个管理员读取或继续另一个管理员的工作流 | key scope 固定为 `(operation_type, actor_user_id, idempotency_key)`；同 key 不同安全 fingerprint 返回 `IDEMPOTENCY_CONFLICT`；最短保留 24h。 | 并发、跨 actor、冲突 payload 和过期后重试测试。 |
| Provisioning activation bypass | Organization 未完成时账户提前登录 | 管理创建必须显式插入 `provisioning`；数据库不保留 `active` 永久默认；Authenticate/Login 只允许 active；Activate 仅允许 provisioning→active 且必须有 command receipt。 | omitted-state insert、provisioning login、activation timeout/crash tests。 |
| Password material crosses workflow boundary | workflow/log/event 泄露密码或可离线猜测摘要 | 密码只在当前请求内传 IAM；workflow fingerprint 排除密码；不保存旧/新 plaintext；日志/event/receipt/result 不含密码/hash。 | 数据库行、日志和 payload secret scan。 |
| Database-role bypass of module ownership | 代码通过共享高权限连接访问其他 owner 表或关闭 bridge | IAM/Organization/Platform/domain table owners 均为独立 NOLOGIN roles；runtime login 不继承 owner、无 schema CREATE/ALTER/trigger/mode-change 权限；bridge owner 仅拥有 SECURITY DEFINER functions 和最小 fully-qualified DML；短期 migrator/ops credential 不进入 runtime。 | catalog/grant tests + module-role integration tests verify cross-query, `ALTER TABLE ... DISABLE TRIGGER`, function replacement/owner change/self-grant all fail. |
| Event tampering/unsupported version | 错误或伪造事件清理错误 membership | 校验 type/version/producer/payload；unsupported version block producer outbox，不写 successful inbox；未来跨网络增加消息认证。 | tampered payload、unsupported version、duplicate delivery tests。 |
| Replay after inbox/workflow retention | 已清理去重记录后旧命令/事件重复生效 | workflow/command receipt/inbox retention 不短于公开幂等和 outbox replay window；清理前确认 producer 不再重放；user ID 不复用。 | retention boundary and archived replay tests。 |
| Request/correlation ID contains credentials | 日志和事件间接泄密 | 只接受受限长度/字符集的生成 ID；不从 Authorization/token 派生；拒绝/替换非法外部值。 | malicious correlation header tests and log scan。 |
| Authorization changes between preflight and command | 已撤权管理员完成后续步骤 | BFF 在 workflow 创建前实时授权，并在每个 durable step boundary 的新 forward participant side effect 前再次查询当前权限（包括原始 HTTP 请求内）；recovery principal 只能 receipt-finalize/补偿，不能继续 forward request；参与模块不信任客户端 permission claims。 | revoke permission between preflight/steps inside original request and after recovery；verify next forward side effect is absent. |
| Participant commit followed by BFF crash | 重试产生重复写或错误补偿 | side effect 与 command receipt 在 owner 本地事务提交；BFF 通过 `ResolveCommand` 解析未知结果后再继续/补偿。 | crash after participant commit before workflow step persist。 |
| Concurrent update followed by delayed compensation | 旧补偿覆盖更新后的部门/身份 | IAM user version、membership version、expected-version CAS 和每 subject active workflow 唯一约束。 | 在失败与补偿之间插入并发成功更新，确认 compensation conflict 而非覆盖。 |

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: 系统 MUST 明确定义 IAM、Organization 和 Admin BFF 的职责、依赖方向和禁止访问规则。
- **FR-002**: IAM MUST 独占账户、密码哈希、会话、角色、权限及用户角色关系的业务规则和数据访问。
- **FR-003**: Organization MUST 独占部门、部门层级和用户部门成员关系的业务规则和数据访问。
- **FR-004**: Admin BFF MUST 作为浏览器管理 API 的唯一入口，负责公开 DTO、跨模块读取聚合和跨模块写工作流编排；BFF MUST NOT 直接访问领域表。
- **FR-005**: 现有 `/api/v1` HTTP/JSON 路径、认证要求、状态码、机器错误码和字段语义 MUST 保持向后兼容。
- **FR-006**: `/api` 代理前缀 MUST 保留；浏览器 MUST NOT 直接访问内部模块或未来内部服务端点。
- **FR-007**: `GET /api/v1/auth/me` MUST 由 Admin BFF 聚合 IAM 身份/角色/权限与 Organization 部门信息，且空权限返回非 null 空数组。
- **FR-008**: 用户列表及用户详情 MUST 由 Admin BFF 聚合，不得通过跨领域 SQL JOIN 生成。
- **FR-009**: IAM 和 Organization MUST 通过窄化的、面向用例的 port 交互；模块业务代码 MUST NOT 依赖全局 sqlc `Querier` 或其他领域的生成查询类型。
- **FR-010**: 每个模块 MUST 拥有独立的 repository/query 接口；sqlc 生成文件 MUST 只通过 `sqlc generate` 更新，禁止手工修改。
- **FR-011**: 跨领域写操作 MUST NOT 依赖共享数据库事务；Admin BFF MUST 使用显式持久化工作流、预检、幂等键及补偿或待处理状态实现可恢复的一致性；IAM 与 Organization 的每个参与者命令 MUST 在本地事务中写入可查询的 command receipt，以解析超时或崩溃后的提交结果。
- **FR-012**: 每个领域内的多个写操作 MUST 保持本地事务原子性。
- **FR-013**: 领域状态变更与**本 feature 定义并交付的** outbox 事件 MUST 在同一本地事务中提交。本 feature 仅交付 `iam.user.deleted` v1；Organization membership 变更事件的生产本 feature 不交付，明确延期到物理拆分 feature 按消费者实际需要定义。
- **FR-014**: 事件 envelope MUST 至少包含全局唯一事件 ID、事件类型、schema 版本、聚合类型、聚合 ID、聚合版本、发生时间、关联 ID 和 payload。
- **FR-015**: 事件消费者 MUST 使用 inbox 或等价持久化机制实现幂等处理，并支持重复投递、重试和失败可观测性。
- **FR-016**: 本迭代 MUST 提供进程内 adapter；port 设计 MUST 允许未来替换为 gRPC adapter，而不修改领域服务和公开 HTTP handler。
- **FR-017**: 本迭代 MUST NOT 启动真实 gRPC 服务、部署独立 IAM/Organization/BFF 进程或要求服务发现基础设施。
- **FR-018**: 普通业务模块 MUST NOT 在每个请求中远程调用 IAM 做权限判定；Admin BFF 负责认证和管理权限检查，内部调用携带经过验证的 actor 上下文。
- **FR-019**: 第一代授权语义 MUST 保持：后端受保护管理请求使用当前数据库授权状态；`admin` 与 `super_admin` 同权拥有六个管理权限，`user` 和自定义角色默认无管理权限。
- **FR-020**: 401 与 403 MUST 保持区分：无效会话返回 `AUTH_INVALID_TOKEN`，有效身份但权限不足返回 `AUTH_FORBIDDEN`。
- **FR-021**: 本 feature MUST 由唯一 Platform migrator 推进统一 migration history；IAM、Organization、Admin BFF workflow 和跨旧/新结构 bridge 迁移 MUST 有明确 owner 清单与升级验证，运行模块不得独立竞争同一 version table。独立 owner migration histories 延期到物理拆分 feature。
- **FR-022**: 从 `users.department_id` 到 Organization 成员关系所有权的迁移 MUST 使用 expand/backfill/dual-write/verify/cutover 顺序：在 legacy router 仍可服务期间不得置空或删除旧列/FK；回滚必须先停止写入并恢复/验证旧表示，再切回 legacy route；破坏性删除延期到回滚窗口后的 cleanup feature。
- **FR-023**: 模块 MUST 提供独立命名的配置、健康状态和结构化可观测信号，并在同一请求链中传播 request ID/trace context。
- **FR-024**: 内部 port 调用 MUST 定义超时、错误分类和可重试性；BFF MUST 将内部错误映射为固定公开错误：幂等冲突 `409 IDEMPOTENCY_CONFLICT`、运行中 `409 OPERATION_IN_PROGRESS`、依赖不可用 `503 DEPENDENCY_UNAVAILABLE`、未知结果超时 `504 DEPENDENCY_TIMEOUT`、可重试工作流失败 `503 WORKFLOW_RETRYABLE`、需人工对账 `500 RECONCILIATION_REQUIRED`；不得泄露数据库、拓扑或秘密信息。
- **FR-025**: 管理员初始化流程 MUST 继续只允许把已注册用户提升为 `admin` 或 `super_admin`，不接受密码、不创建默认凭据，并在数据所有权变化后仅通过 IAM 边界执行。
- **FR-026**: 批量删除和内置角色保护 MUST 保持全批次预检和原子性，不能因模块重构重新出现部分成功。
- **FR-027**: 自动化测试 MUST 覆盖模块依赖规则、port 契约、公开 HTTP 回归、真实 PostgreSQL 迁移、outbox/inbox、幂等重试、补偿工作流和故障注入。
- **FR-028**: 发布方案 MUST 支持先部署 expand schema 和兼容双写、持续验证新旧部门表示一致、再切换 BFF 读取/写入路径；回滚时 MUST 在切回 legacy route 前完成旧表示最终同步和验证。workflow/outbox/inbox 等证据表在支持期内保持为 dormant additive schema，不由即时回滚删除。
- **FR-029**: `POST /api/v1/users` 与 `POST /api/v1/users/delete` MUST 支持可选 `Idempotency-Key` header（16–128 个 `[A-Za-z0-9._:-]` 字符）；官方前端 MUST 为一次逻辑提交生成并在未知结果重试时复用。未提供 header 的旧客户端保持兼容，但不获得跨超时重复抑制保证。server-side replay window MUST 至少 24 小时。
- **FR-030**: IAM user 与 Organization membership state MUST 使用单调 version；无部门必须保存 nullable department tombstone/version，clear 不得删除 version-bearing state。更新/补偿 MUST 使用 expected-version compare-and-set，且通过 per-subject workflow rows 保证同一 subject 同时最多一个 active managed-user mutation workflow（包括批量删除中的每个 subject），防止 ABA 与延迟补偿覆盖较新写入。
- **FR-031**: outbox dispatcher MUST 通过 IAM-owned delivery port 使用 durable lease/claim token 领取事件；Integration MUST NOT import IAM sqlc。unsupported event MUST 进入 blocked 状态而不是热循环。
- **FR-032**: 每个 outbox delivery epoch MUST 最多自动领取 20 次且不超过该 epoch 初始可投递时间后的 24 小时；claim/reclaim 即计 attempt，失败记录 MUST 在一个 claim-token CAS transaction 中原子选择 pending/backoff 或 `blocked(delivery_exhausted)`，过期 epoch 在 claim scan 中直接 blocked。只能通过 authenticated Platform recovery context、approved reason 和 immutable full-epoch audit 的受控 requeue 恢复；blocked event 无 waiver-to-purge path。
- **FR-033**: compatibility dual-write MUST 由 Platform migrator 安装的受限双向 PostgreSQL trigger bridge 实现；domain table/schema owners、bridge function owner 和 Platform mode owner 均为独立 NOLOGIN roles，runtime logins 不得继承 owner 或持有 schema CREATE/ALTER trigger/mode-change 权限。DELETE safety trigger 保持安装并读取 Platform-owned versioned mode；只有 short-lived authorized operation 可原子变更 mode + audit，migration credential 不得作为 runtime credential。
- **FR-034**: legacy route disable 前 MUST 由 append-only Platform rollout gate 证明至少连续健康 72 小时；记录 capability manifest/bridge mode、sample cadence、legacy/new count/version/checksum、mismatch/gap、rollback artifact suite 与 approval，任一 mismatch/monitoring gap 重置窗口。disable 后 rollback support window 为 7 个自然日。workflow/command receipt/published outbox/successful inbox/recovery/requeue 证据最少保留 30 天，pending/leased/blocked 或 unresolved evidence 不得自动清理，inbox dedupe 不得早于 producer 可重放证据清理；cleanup 只能经 owner dry-run/purge ports 使用最保守 watermark。
- **FR-035**: batch managed-user workflow MUST 持久化每个 subject 的 expected/result version 与 active exclusion；IAM delete 使用一个包含规范化 per-user result 的 batch command receipt。
- **FR-036**: workflow 在 preflight 后、每个 durable step boundary 即将执行新的 forward participant side effect 时（包括原始 HTTP 请求内及恢复后），MUST 重新查询当前 actor 权限。若已撤权且无 side effect，进入 terminal `rejected` 并返回/重放 `403 AUTH_FORBIDDEN`；若已有 side effect，进入 `compensating`，受控最小权限 recovery principal 只能回滚已应用步骤，补偿成功后转 `rejected`，失败/冲突转 `failed_manual` + `500 RECONCILIATION_REQUIRED` 并保持 subject exclusion。若 receipts 证明全部业务步骤已在撤权前提交，则只补记 `succeeded`，不新增 side effect。当前认证/route authorization 也优先于 completed idempotency replay。
- **FR-037**: 需要密码但 IAM credential receipt 尚未提交且 HTTP request 已结束的 workflow MUST 转为 `awaiting_client_input` 并写 immutable `client_input_deadline_at`（默认 workflow creation +24h）。期限前只允许同 key、当前已认证授权的 HTTP retry 在内存中重提 credential；等待不消耗 forward attempt。期限后 worker 只能终止/补偿：无 side effect 则 `rejected` + 409 `OPERATION_EXPIRED`；已应用 Organization membership 则按 applied-version CAS 恢复，成功后同样 rejected，冲突/失败转 `failed_manual` + `RECONCILIATION_REQUIRED`。任何 worker 都不得保存、推导或合成密码。
- **FR-038**: expired `running` workflow MUST 可由 atomic claim predicate 重新领取并生成 fresh claim token；新 owner 在任何下一 side effect 前 MUST 解析最后 persisted participant command receipt，stale owner 的 persist/complete MUST 由 claim-token CAS 拒绝。自动 10-attempt/24h budget 不得 reset/extend；manual reconciliation 只能追加 immutable recovery action 并执行 receipt-finalize/approved compensation。
- **FR-039**: staged rollout MUST 使用 granular capability flags/startup gates；delete-event acceptance 不得在 legacy delete delegation 未启用或 DELETE bridge mode 仍同步删除时启动。Rollback support window 内只能切换到预先 pin/hash 并通过 extended HTTP compatibility suite 的 004-compatible rollback artifact；frozen pre-004 baseline 仅用于行为对比，不是可部署 rollback binary。

### Non-Goals

- 本 feature 不部署真正的微服务，不改变为多进程生产拓扑。
- 本 feature 不引入 gRPC runtime、服务发现、service mesh 或 Kubernetes 依赖。
- 本 feature 不增加 Redis 或分布式权限缓存。
- 本 feature 不增加菜单数据库模型、服务端菜单配置或自定义角色权限配置 UI/API。
- 本 feature 不实现行级或字段级权限。
- 本 feature 不实现认证第二阶段（邮箱验证）或第三阶段（OAuth/OIDC/SSO）。
- 本 feature 不改变前端为多 origin 调用；前端仍只访问统一 `/api/v1`。
- 本 feature 不把 Nginx 放入服务间调用路径。

### Key Entities *(include if feature involves data)*

- **IAM User**: 登录账户与生命周期；包含稳定用户 ID、用户名、账户资料、密码凭据引用和状态，但不拥有部门关系。
- **Session**: IAM 管理的不透明会话；数据库只存 token hash，具有滑动与绝对过期、撤销状态。
- **Role / Permission / UserRole**: IAM 管理的 RBAC 授权模型和有效权限计算来源。
- **Department**: Organization 管理的层级组织单元，包含稳定 ID、名称、父部门和层级约束。
- **Department Membership**: Organization 管理的用户到部门关系；第一代每个用户最多一个当前部门，用户 ID 作为外部主体引用而不是跨领域数据库外键。
- **Admin Workflow**: BFF 管理的跨领域命令执行记录，包含幂等键、步骤状态、参与者版本、补偿状态、关联 ID 和最终结果；不复制领域事实。
- **Command Receipt**: IAM/Organization 在本地 side effect 同一事务中写入的 `(operation_id, command_name)` 成功凭据，保存非秘密 fingerprint、安全结果和 resulting version，供超时/崩溃后解析。
- **Outbox Event**: 与领域状态同事务持久化、等待发布的版本化事件记录，具有 durable claim/lease、重试、blocked 和 published 状态。
- **Inbox Message**: 消费者已接收/处理事件的幂等记录。
- **Actor Context**: BFF 在认证和授权后传递给内部用例的最小调用者信息，不包含原始会话 token 或密码。

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 现有认证、角色、部门、用户管理和前端权限回归测试 100% 通过，公开 `/api/v1` 契约无不兼容变更。
- **SC-002**: 静态依赖检查证明 IAM 和 Organization 业务包之间不存在直接 import，二者都不依赖全局 sqlc `Querier` 或对方查询类型。
- **SC-003**: 代码搜索和测试证明 `/auth/me`、用户列表与用户详情不存在跨 IAM/Organization SQL JOIN，聚合只发生在 Admin BFF。
- **SC-004**: 所有跨模块写工作流在每个可注入故障点均有自动化测试；重复提交不会创建重复实体或关系，部分失败可补偿或被明确记录为可重试状态。
- **SC-005**: 在真实 PostgreSQL 集成测试中，本 feature 定义的事件（`iam.user.deleted` v1）在其领域写入成功时 outbox 记录生成率为 100%；模拟崩溃和重复投递后无事件丢失、无重复业务副作用。
- **SC-006**: Organization 的进程内 adapter 通过同一套内部契约测试；未来远程 adapter 可复用该套测试而无需修改 BFF 用例。
- **SC-007**: 从现有部门外键到 Organization 成员关系的迁移测试覆盖空部门、有效部门和异常孤儿引用；expand/backfill 后数量与值一致，兼容双写期间新旧表示持续一致，legacy router 在整个切换窗口不丢失部门数据。
- **SC-008**: 单进程模式下可分别观察 IAM、Organization 和 Admin BFF 的就绪状态、调用次数、错误数与延迟，并通过同一 request/trace 关联一次聚合请求。
- **SC-009**: 正常管理读取请求的 BFF 聚合开销保持在当前基线的可接受范围内，目标 P95 < 300ms；内部进程内 port 调用不引入网络依赖。
- **SC-010**: 发布和回滚演练证明在兼容窗口内可切回旧读取路径，且不会丢失用户、角色、权限、部门或成员关系数据。

## Assumptions

- 当前 001 后端脚手架、002 用户认证、003 RBAC 以及随后完成的权限闭环是本 feature 的稳定行为基线。
- 当前仍采用 Go + Gin + PostgreSQL + sqlc；Vue 3 前端继续使用现有 Axios/Pinia/动态路由模型。
- 当前生产入口保持 Nginx → Admin BFF；Vue 构建产物通常由 Nginx 托管。
- 本 feature 的 IAM、Organization 和 Admin BFF DSN MUST 指向同一个物理 PostgreSQL database（可使用不同 pool/数据库角色）；唯一 Platform migrator 负责统一 version history。后续物理拆分再引入独立数据库和 migration history。
- 用户 ID 在 IAM 和 Organization 间作为稳定、不可复用的外部主体标识。
- 当前规模为低到中等流量管理后台；优先保证正确性、可回滚和可观测性，而不是提前引入复杂分布式基础设施。
- 领域事件第一阶段可由数据库 outbox dispatcher 传递给进程内消费者；消息 broker 的选择留到真实服务拆分 feature 决策。
- gRPC proto、TLS/mTLS、服务发现和远程重试策略将在 Organization 物理抽取 feature 中最终确定；本 feature 只稳定语义端口和契约测试。
