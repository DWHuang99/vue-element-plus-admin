# Data Model: 微服务拆分准备

**Feature**: `004-microservice-splitting`  
**Date**: 2026-08-10

## Design rules

1. 每张业务表只能有一个 owner：IAM、Organization 或 Admin BFF workflow。
2. IAM 与 Organization 之间不建立数据库外键；Organization 只保存稳定、不可复用的 IAM user ID。
3. 运行时领域 SQL 不跨 owner JOIN；只有 reviewed Platform expand/backfill/compatibility bridge 可跨 legacy/new 表示读取或双写。
4. 领域 side effect、对应 owner-local command receipt，以及需要时的本领域 outbox event 在同一本地事务中提交。
5. BFF workflow 状态不复制身份或组织领域事实，不保存密码、原始 token、password hash 或数据库秘密。
6. 已应用迁移 `000001`–`000005` 不重写；以下变化由新的 forward migration 添加。

## Ownership matrix

| Owner | Tables / records | Notes |
|---|---|---|
| IAM | `users`, `sessions`, `roles`, `user_roles`, `permissions`, `role_permissions`, `iam_command_receipts`, `iam_outbox_events`, `iam_outbox_requeues` | `users.department_id` 仅为 legacy compatibility/rollback 表示，不属于 IAM 新领域模型。 |
| Organization | `departments`, `organization_user_departments`, `organization_command_receipts`, `organization_inbox_messages` | `user_id` 是外部 IAM subject reference，无 FK。 |
| Admin BFF | `admin_workflows`, `admin_workflow_subjects`, `admin_workflow_recovery_actions` | 仅保存编排、幂等、每目标版本/exclusion、补偿和 immutable manual-recovery audit。 |
| Platform | migration version history, `compatibility_bridge_mode`, `compatibility_bridge_mode_changes`, `compatibility_rollout_gates` + reviewed compatibility bridge | 唯一 Platform migrator 推进统一 history；bridge 在回滚窗口同步 legacy/new membership，mode/audit 控制 legacy DELETE safety path，rollout gate 持久化 72h parity/approval evidence。 |

## IAM entities

### User

代表可认证身份及 IAM 管理资料。

| Field | Type | Constraints | Notes |
|---|---|---|---|
| `id` | BIGINT | PK, generated, immutable | 跨模块稳定主体 ID，不复用。 |
| `username` | TEXT/VARCHAR | NOT NULL, UNIQUE | 现有规范化和校验保持。 |
| `password_hash` | TEXT | NOT NULL | Argon2id PHC string；不通过 API/事件暴露。 |
| `account` | TEXT/VARCHAR | existing validation | IAM-owned 管理资料和用户列表筛选字段。 |
| `email` | TEXT/VARCHAR | existing validation | IAM-owned；当前不实现邮箱所有权验证。 |
| `lifecycle_state` | TEXT | NOT NULL, CHECK | `provisioning`, `active`, `disabled`；migration 临时 default `active` 仅用于回填，随后 DROP DEFAULT。 |
| `version` | BIGINT | NOT NULL, CHECK `> 0` | 初始 1；每次 IAM aggregate mutation CAS 递增；删除事件使用删除前值 + 1 作为 tombstone version。 |
| `created_at` | TIMESTAMPTZ | NOT NULL | 现有语义。 |
| `updated_at` | TIMESTAMPTZ | NOT NULL | 每次 IAM 资料变化更新。 |
| `department_id` | BIGINT NULL | legacy compatibility only | BFF 新读模型不读取；回滚窗口内由 compatibility bridge 双写并保留旧 FK/index。 |

**Invariants**:

- 只有 `active` 用户可以登录、创建新 session 或通过 session authentication。
- `provisioning` 用户不出现在正常管理列表中，不能登录。
- `disabled` 用户不能登录；禁用时必须撤销现有 sessions。
- 注册在 IAM 本地事务中直接创建 `active` 用户、默认 `user` 角色和 session。
- BFF 管理创建先建立 `provisioning` 用户，Organization membership 成功后再激活；`ActivateUser` 只允许 `provisioning -> active`。
- workflow identity/profile/credential/role/lifecycle mutations use expected `version` CAS；registration creates version 1；admin-init locks latest user row and increments only on new grant；session-only activity不改变 user version。
- 用户删除由 IAM authoritative batch transaction 执行，写一个含 per-user result/version 的 batch command receipt，并为每个用户写删除事件。

**State transitions**:

```text
registration ──────────────> active
admin create ─> provisioning ─> active
                         └────> deleted (failed-create compensation only)
active ───────────────────> disabled
active/disabled/provisioning ─> deleted
```

### Session

保持 002 Auth 契约：

- 关联 IAM `users.id`，域内 FK 允许 `ON DELETE CASCADE`。
- 只保存 SHA-256 token hash，不保存原始 opaque token。
- 具有 created、last activity、absolute expiry、sliding expiry/revocation 字段。
- session authentication 同时要求 session 有效且 user 为 `active`。

### Role

| Invariant | Rule |
|---|---|
| Built-in codes | `super_admin`, `admin`, `user` |
| Code mutation | 内置角色 code 不可修改 |
| Deletion | 内置角色不可删除 |
| Default permission | `admin`/`super_admin` 拥有六个管理权限；`user`/自定义角色默认无管理权限 |

### UserRole

- IAM-only many-to-many relation。
- `(user_id, role_id)` 唯一。
- managed-user role replacement 在单个 IAM transaction 中原子完成。
- 用户/角色删除的 FK/cascade 语义保持现有契约。

### Permission / RolePermission

固定第一代 permission codes：

- `roles.read`
- `roles.write`
- `departments.read`
- `departments.write`
- `users.read`
- `users.write`

有效权限是用户所有角色授权的去重、有序并集。权限判断每个受保护请求读取当前 IAM 状态，不增加授权决定缓存。

### IAMCommandReceipt (`iam_command_receipts`)

Owner-local durable proof for an IAM workflow command. The receipt is written in the same transaction as the command side effect.

| Field | Type | Constraints / purpose |
|---|---|---|
| `operation_id` | UUID | with `command_name` primary/unique identity. |
| `command_name` | TEXT | e.g. `create_provisioning_user`, `activate_user`, `update_managed_user`, `delete_users`. |
| `request_fingerprint` | TEXT | safe canonical digest; excludes password/token/hash and password-derived material. |
| `status` | TEXT | `succeeded` or owner-classified terminal rejection; no ambiguous `running` row. |
| `subject_id` | BIGINT NULL | safe result identity when applicable. |
| `result` | JSONB NULL | minimal replay result/version; no PII or secret. |
| `error_code` | TEXT NULL | stable safe typed code. |
| `created_at` / `completed_at` | TIMESTAMPTZ | durable resolution timestamps. |

`ResolveCommand(operation_id, command_name, expected_fingerprint)` reads this record and verifies the caller's safe fingerprint. A mismatch returns `OperationConflict`; absence means no committed side effect is proven and the workflow applies its command-specific retry policy.

### IAMOutboxEvent (`iam_outbox_events`)

| Field | Type | Constraints / purpose |
|---|---|---|
| `event_id` | UUID | PK；由 producer 生成。 |
| `event_type` | TEXT | NOT NULL；首个值 `iam.user.deleted`。 |
| `event_version` | INTEGER | NOT NULL；首版 `1`。 |
| `producer` | TEXT | NOT NULL；`iam`。 |
| `aggregate_type` | TEXT | NOT NULL；首个值 `user`。 |
| `aggregate_id` | TEXT/BIGINT representation | NOT NULL；被删除 user ID。 |
| `aggregate_version` | BIGINT | NOT NULL；删除前 `users.version + 1` tombstone version。 |
| `payload` | JSONB | NOT NULL；首版只含 `user_id`。 |
| `correlation_id` | TEXT/UUID | NOT NULL；与 HTTP request/workflow 关联，不是 token。 |
| `occurred_at` | TIMESTAMPTZ | NOT NULL；领域变化发生时间。 |
| `status` | TEXT | NOT NULL CHECK；`pending`, `leased`, `published`, `blocked`。 |
| `available_at` | TIMESTAMPTZ | NOT NULL；pending/retry 下次可 claim 时间。 |
| `delivery_epoch` | INTEGER | NOT NULL DEFAULT 1；每次受控 requeue 递增。 |
| `epoch_started_at` | TIMESTAMPTZ | NOT NULL；等于该 epoch 初始 `available_at`，即首次资格时间；后续 failure backoff 不改变；24h ceiling 基于此字段。 |
| `attempt_count` | INTEGER | NOT NULL DEFAULT 0；当前 epoch lease acquisition/delivery attempts；每次 fresh claim/reclaim 原子递增。 |
| `total_attempt_count` | BIGINT | NOT NULL DEFAULT 0；每次 fresh claim/reclaim 同步递增，跨 requeue 永不重置。 |
| `lease_owner` | TEXT NULL | dispatcher instance safe ID；仅 leased 时非 null。 |
| `leased_until` | TIMESTAMPTZ NULL | lease 到期后可被其他 worker 恢复。 |
| `claim_token` | UUID NULL | 每次 claim 新生成；ack/record-failure/block 必须 CAS 匹配。 |
| `last_error_code` | TEXT NULL | safe classification only；不保存 SQL、秘密或完整响应。 |
| `blocked_at` | TIMESTAMPTZ NULL | non-retryable/exhausted failure 时间。 |
| `blocked_reason_code` | TEXT NULL | stable safe code，例如 `unsupported_version`, `delivery_exhausted`。 |
| `published_at` | TIMESTAMPTZ NULL | published 时非 null。 |
| `created_at` | TIMESTAMPTZ | NOT NULL；审计/积压计算。 |

**Indexes / checks**:

- claim：`(available_at, created_at) WHERE status = 'pending'`。
- lease recovery：`(leased_until) WHERE status = 'leased'`。
- blocked/published operational indexes；aggregate `(aggregate_type, aggregate_id, aggregate_version)`。
- status 与 lease/blocked/published 字段组合必须通过 CHECK 保持一致。
- claim transaction first atomically converts any eligible pending/expired-lease row whose epoch deadline has elapsed to `blocked(delivery_exhausted)` without issuing a lease；otherwise a fresh claim/reclaim increments both counters and may create at most the 20th attempt。
- `RecordOutboxFailure(event_id, claim_token, safe_error_code, next_available_at)` is one IAM transaction that evaluates count/time ceiling and performs exactly one claim-token-CAS transition: either `pending` with bounded backoff or `blocked(delivery_exhausted)`。There is no reschedule-then-separate-block race。A crash after claim is already counted because attempts increment on lease acquisition。
- audited requeue increments `delivery_epoch`, resets epoch `attempt_count=0`, sets `epoch_started_at = available_at` for the new epoch, clears lease/blocked fields, preserves `total_attempt_count`, and writes immutable `iam_outbox_requeues` history。

### IAMOutboxRequeue (`iam_outbox_requeues`)

| Field | Type | Purpose |
|---|---|---|
| `event_id`, `delivery_epoch` | UUID, INTEGER | composite identity / resulting epoch. |
| `recovery_principal_id` | TEXT | authenticated identity derived from trusted Platform operations context, not caller-supplied text/raw token. |
| `reason_code` | TEXT | approved safe reason. |
| `previous_epoch_started_at`, `previous_epoch_deadline_at`, `previous_blocked_at` | TIMESTAMPTZ | prove the prior time budget and block point. |
| `previous_blocked_reason`, `previous_last_error_code`, `previous_epoch_attempts`, `previous_total_attempts` | safe values | preserve complete exhaustion/contract evidence. |
| `authorization_source`, `approval_id`, `request_id`, `correlation_id` | TEXT/UUID | bind the action to a trusted operations authorization and trace. |
| `requeued_at` | TIMESTAMPTZ | immutable audit time. |

**Transaction/port rules**: 用户删除、IAM command receipt 与 outbox insert 同一 IAM transaction。Integration 只能通过 IAM `OutboxDeliveryPort` claim/ack/record-failure/block/requeue，不能 import IAM sqlc。受控 requeue 只接受 Platform operations adapter 构造的 trusted `RecoveryContext`，并与 immutable audit row 在同一 IAM transaction；consumer 调用发生在 producer DB transaction 之外。

## Organization entities

### Department

保持现有部门树语义：

| Field | Type | Constraints |
|---|---|---|
| `id` | BIGINT | PK |
| `name` | TEXT/VARCHAR | existing uniqueness/validation |
| `parent_id` | BIGINT NULL | FK to `departments.id` only |
| timestamps | TIMESTAMPTZ | existing semantics |

**Invariants**:

- 不能以自身为父部门。
- 父部门必须存在。
- 有子部门或 `department_id IS NOT NULL` 的 current membership state 时不能删除。
- 批量删除先完成全批次预检，再在 Organization 本地事务中统一删除。

### OrganizationUserDepartment (`organization_user_departments`)

表示一个 IAM 用户的版本化部门成员状态；`department_id = NULL` 是持久化的“当前无部门”tombstone，不能通过删除 row 丢失版本。

| Field | Type | Constraints | Notes |
|---|---|---|---|
| `user_id` | BIGINT | PK, NOT NULL, **no FK to users** | opaque IAM subject ID。 |
| `department_id` | BIGINT NULL | FK to `departments.id` | NULL 是版本化无部门状态；仅域内 FK。 |
| `membership_version` | BIGINT | NOT NULL, CHECK `> 0` | 每次实际 set/clear/restore CAS 递增；无部门状态也持续保存 version，防止 ABA。 |
| `created_at` | TIMESTAMPTZ | NOT NULL | 首次成员关系时间。 |
| `updated_at` | TIMESTAMPTZ | NOT NULL | 最近变更时间。 |

**Invariants**:

- 一个 user 最多一个 state row，且当前部门为零或一个；row 不存在仅表示从未建立 Organization state。
- set/clear/restore 都以 current row version 或明确 expected absence 做 CAS；实际变化递增 version 并返回结果。
- clear 使用 upsert/update 将 `department_id` 设为 NULL，不删除 version-bearing row；重复 clear 可由 receipt replay 返回同一结果。
- IAM user deleted event 是唯一可物理删除该 user state row 的普通路径，因为 IAM IDs 不复用且删除为终态。
- Organization 不验证 IAM 用户是否存在；存在性由 BFF preflight/IAM workflow 和 IAM deletion event 收敛。
- department deletion protection 只查询本表的 non-null `department_id`，不查询 IAM users。

**Indexes**:

- `(department_id, user_id)` 用于删除保护和按部门列 user IDs。

### OrganizationCommandReceipt (`organization_command_receipts`)

Same owner-local receipt pattern as IAM. Membership receipts additionally persist `previous_department_id`, `previous_membership_version`, `resulting_department_id`, and `resulting_membership_version` so timeout resolution and compensation do not infer state from a later read. Receipt and membership side effect commit atomically.

### OrganizationInboxMessage (`organization_inbox_messages`)

| Field | Type | Constraints / purpose |
|---|---|---|
| `event_id` | UUID | 与 `handler_name` 组成 PK/UNIQUE。 |
| `handler_name` | TEXT | 首个值 `remove-membership-on-user-deleted`。 |
| `event_type` | TEXT | NOT NULL。 |
| `event_version` | INTEGER | NOT NULL。 |
| `aggregate_id` | TEXT | NOT NULL。 |
| `aggregate_version` | BIGINT | NOT NULL。 |
| `received_at` | TIMESTAMPTZ | NOT NULL。 |
| `processed_at` | TIMESTAMPTZ | NOT NULL；row 只代表成功处理。 |

**Processing transaction**:

1. 在写事务前或事务内验证 event envelope/type/version/payload；unsupported/invalid 直接返回 typed error，不写 successful inbox。
2. 查询 `(event_id, handler_name)`；已存在则幂等成功。
3. 删除对应 `organization_user_departments`（不存在也成功）。
4. 插入 successful inbox dedupe row。
5. side effect 与 dedupe row 同一 Organization transaction 提交。

失败尝试不会留下与 transaction rollback 冲突的 inbox `failed` row；attempt/error/blocked 可观测性由 producer outbox、metrics 和安全日志承担。

## Admin BFF process entities

### AdminWorkflow (`admin_workflows`)

持久化跨模块命令的幂等、步骤和补偿状态；不是用户/部门/角色事实来源。

| Field | Type | Constraints / purpose |
|---|---|---|
| `operation_id` | UUID | PK；同时作为 correlation ID 候选。 |
| `operation_type` | TEXT | `managed_user.create`, `.update`, `.delete`。 |
| `idempotency_key` | TEXT NULL | optional legacy compatibility；非 null 时与 operation type/actor scope partial-unique。 |
| `request_fingerprint` | TEXT | 非秘密字段的规范化摘要；不得包含密码/token/hash。 |
| `actor_user_id` | BIGINT | 发起管理员；仅最小审计引用。 |
| `subject_user_id` | BIGINT NULL | IAM 创建成功后写入。 |
| `state` | TEXT | `pending`, `running`, `awaiting_client_input`, `compensating`, `succeeded`, `rejected`, `failed_retryable`, `failed_manual`。`awaiting_client_input` cannot be worker-claimed；`rejected` 是可重放的 terminal validation/not-found/protection/authorization failure。 |
| `current_step` | TEXT | 当前/最后完成步骤。 |
| `compensation_state` | TEXT | `not_required`, `pending`, `running`, `succeeded`, `failed`。 |
| `expected_iam_version` | BIGINT NULL | update/delete preflight observed user version. |
| `resulting_iam_version` | BIGINT NULL | committed IAM command receipt result. |
| `previous_department_id` | BIGINT NULL | update 恢复所需最小旧状态。 |
| `previous_membership_version` | BIGINT NULL | old state version/absence marker. |
| `applied_department_id` | BIGINT NULL | workflow attempted/committed target. |
| `applied_membership_version` | BIGINT NULL | compensation CAS guard; restore only when current still equals this version. |
| `result` | JSONB NULL | 仅保存可安全重复返回的 ID/状态/version，不保存秘密/PII payload。 |
| `last_error_code` | TEXT NULL | typed safe error；不保存 SQL/stack/secret。 |
| `created_at` | TIMESTAMPTZ | NOT NULL。 |
| `updated_at` | TIMESTAMPTZ | NOT NULL。 |
| `attempt_count` | INTEGER | NOT NULL DEFAULT 0；monotonic automatic participant-execution attempts；never reset. Time in `awaiting_client_input` does not increment it. |
| `next_retry_at` | TIMESTAMPTZ NULL | persisted source for retry scheduling/`Retry-After`. |
| `client_input_deadline_at` | TIMESTAMPTZ NULL | immutable deadline only when credential resubmission is needed, default workflow creation +24h；after it, worker may only reject/compensate, not continue forward. |
| `retry_deadline_at` | TIMESTAMPTZ | NOT NULL；immutable automatic recovery horizon, default created_at + 24h；not extended in this feature. |
| `lease_owner` | TEXT NULL | worker identity while running/recovering. |
| `leased_until` | TIMESTAMPTZ NULL | expired lease is reclaimable. |
| `claim_token` | UUID NULL | claim/complete/reschedule CAS guard. |
| `completed_at` | TIMESTAMPTZ NULL | terminal 时写入。 |

**Unique constraints**:

- partial unique `(operation_type, actor_user_id, idempotency_key) WHERE idempotency_key IS NOT NULL`.
- single-subject create/update may mirror `subject_user_id` on parent; concurrency enforcement for all single/batch subjects is owned by `admin_workflow_subjects` below.
- `idempotency_key` may be null only for backward-compatible clients; official frontend sends 16–128 chars matching `[A-Za-z0-9._:-]+` and reuses it for the same logical submission.
- worker claim predicate includes `pending`, eligible `failed_retryable`, expired `running` (`leased_until <= now()`), expired `compensating` (`leased_until <= now()`), and expired-input `awaiting_client_input` only when `client_input_deadline_at <= now()`。Running/compensating reclaim replaces token/owner and resolves the last persisted participant command before a new side effect；a reclaim of `compensating` MUST first resolve the receipt of the last compensation command before retrying or finalizing that compensation, and compensation reclaim attempts are counted separately from and never reset the forward-attempt budget；expired-input claim may only reject/compensate。
- only actively claimed `running`/`compensating` rows have non-null lease owner/expiry/token；all persist/complete/reschedule transitions use `(operation_id, state=running|compensating, claim_token)` CAS and clear lease fields on non-active/terminal states。A worker crash mid-compensation is therefore reclaimable rather than permanently stuck in `compensating`。
- before input deadline, same-key HTTP retry may move `awaiting_client_input -> running` only after current authentication/authorization and with credential held in request memory；background recovery cannot synthesize it。After deadline: no side effect → `rejected(OPERATION_EXPIRED)`；applied membership → version-CAS compensation then rejected, or conflict → `failed_manual`。

### AdminWorkflowSubject (`admin_workflow_subjects`)

Stores every subject in a single or batch workflow so restart recovery and per-subject exclusion are complete.

| Field | Type | Constraints / purpose |
|---|---|---|
| `operation_id` | UUID | FK to `admin_workflows`, part of PK. |
| `subject_user_id` | BIGINT | part of PK; logical IAM reference, no FK. |
| `expected_iam_version` | BIGINT | NOT NULL；inserted only after full existence/version preflight succeeds. |
| `resulting_tombstone_version` | BIGINT NULL | per-user delete result from the one batch receipt. |
| `active` | BOOLEAN | true while workflow may issue/recover subject mutation. |

- PK `(operation_id, subject_user_id)`；partial unique `(subject_user_id) WHERE active` prevents any overlapping active managed-user update/delete workflow, including each member of a delete batch。
- subject rows are inserted only after IAM full-batch existence/version preflight succeeds; a rejected missing target creates no incomplete subject row。
- `active=false` for succeeded, rejected before any side effect, or rejected after successful compensation. Actor revocation after a side effect first uses `compensating`; `failed_manual` with unresolved effects keeps active=true until audited recovery claims/reconciles it。
- IAM delete uses one receipt keyed `(operation_id, delete_users)` containing normalized per-user target/result versions; it does not create ambiguous multiple receipts with the same key.

### AdminWorkflowRecoveryAction (`admin_workflow_recovery_actions`)

Immutable append-only evidence for manual reconciliation after `failed_manual` or actor-revocation compensation；it never authorizes resuming a forward business request。

| Field | Purpose |
|---|---|
| `action_id`, `operation_id` | unique action and referenced workflow. |
| `recovery_principal_id`, `authorization_source`, `approval_id` | identity bound by the trusted Platform operations boundary, not caller-supplied display text. |
| `action_type`, `reason_code` | enum of receipt-finalize / approved compensation / reconciliation outcomes. |
| `previous_state`, `resulting_state`, `safe_result_code` | state transition evidence. |
| `request_id`, `correlation_id`, `created_at` | trace/audit fields. |

Automatic attempt count/deadline are never reset or extended。Recovery action and claimed state transition commit in one BFF-store transaction where applicable；participant compensation still uses owner-local receipt/CAS semantics。

**Retention**:

- See the shared retention matrix below；workflow cleanup never deletes domain data.

**Credential handling**:

- 密码只存在于当前 HTTP 请求内，并直接传给 IAM command。
- workflow 不保存密码，也不保存密码可离线猜测的摘要。
- 若在 IAM credential write 前崩溃，客户端必须携同一 idempotency key 重试并重新提交 credential；若 IAM 已完成，后续步骤只使用 `subject_user_id`。

## Retention and cleanup matrix

Named rollout windows:

- minimum stable observation before legacy route disable: 72 continuous healthy hours；
- rollback support window: 7 calendar days after legacy route disable；
- idempotency replay minimum: 24 hours。

| Evidence | Minimum retention / cleanup gate |
|---|---|
| `admin_workflows` + subjects + recovery actions | workflow/subjects: 30 days after terminal state and never before rollback window closes；recovery actions: at least 30 days after the last action and no shorter than the referenced workflow。Unresolved/retry/manual rows are not auto-purged. Automatic execution is capped at 10 attempts/original 24h deadline with no reset/extension；exhaustion is `failed_manual`, retaining exclusions for possibly-applied effects. |
| IAM/Organization command receipts | 30 days after completion and longer than every referencing workflow; unresolved rollback/reconciliation blocks purge. |
| pending/leased outbox | never purge；must publish or become blocked. |
| blocked outbox | never auto-purge and has no waiver-to-purge path in this feature；it must remain blocked or be authorized/requeued and eventually published. |
| published outbox | at least 30 days after publish. |
| `iam_outbox_requeues` | immutable；retain at least 30 days after the associated event's final publish and 30 days after the last requeue, whichever is later；never cascade-delete while event/workflow/rollback evidence is unresolved. |
| successful inbox dedupe | at least as long as producer outbox can be replayed, minimum 30 days after processing; never shorter than retained published/blocked producer evidence. |
| compatibility compare/claim/attempt records | through rollback window plus 30 days, and until zero unresolved workflow/event exclusions. |

Destructive cleanup requires: rollback window closed, zero unresolved workflows/receipts/events, legacy/new parity recorded, and explicit operational approval. Immediate rollback never purges these records.

Cleanup is coordinated through owner ports, never runtime cross-owner SQL：Admin BFF exposes its oldest unresolved/referencing workflow watermark；IAM and Organization expose dry-run `EvaluateEvidenceCleanup(cutoff)` plus approved `PurgeEligibleEvidence(cutoff, trusted_recovery_context)`。Platform chooses the most conservative cutoff only when every owner reports zero unresolved references/replay risk, records dry-run counts and approval, then invokes owner-local transactions。If any owner is unavailable/uncertain, cleanup performs no deletion。

## Aggregate/query models (not persisted BFF domain facts)

### CurrentUserProfile

BFF 运行时组合：

- IAM: user ID, username, account, email, roles, effective permissions。
- Organization: optional department。
- Public shape: 保持现有 `/api/v1/auth/me` contract。

### ManagedUserListItem

BFF 运行时组合：

- IAM page: identity fields + role display/role IDs。
- Organization batch: department ID/name by user IDs。
- 有 department filter 时必须先由 Organization 得到候选 user IDs，再由 IAM 应用其他筛选和分页。

第一迭代不持久化 user-directory projection。

## Cross-domain references

```text
IAM users.id
  ├── Organization organization_user_departments.user_id  (logical reference, NO FK)
  ├── Admin BFF admin_workflows.actor_user_id              (logical reference, NO FK)
  ├── Admin BFF admin_workflows.subject_user_id            (logical reference, NO FK)
  └── Admin BFF admin_workflow_subjects.subject_user_id     (logical reference, NO FK)

Organization departments.id
  └── organization_user_departments.department_id          (domain-local FK)
```

Admin BFF 不依赖数据库 FK 判断 workflow 引用是否有效；每次新操作通过 owner port 验证，历史 workflow 保留原 ID 用于审计。

## Compatibility bridge control and PostgreSQL role matrix

| Role | LOGIN | Ownership / grants |
|---|---|---|
| `iam_owner` | NOLOGIN | owns IAM schema/tables/sequences including legacy `users`; never granted to runtime logins. |
| `organization_owner` | NOLOGIN | owns Organization schema/tables/sequences；never granted to runtime logins. |
| `platform_owner` | NOLOGIN | owns bridge mode/audit tables and controlled mode-change function. |
| `compatibility_bridge_owner` | NOLOGIN | owns only SECURITY DEFINER bridge functions；receives minimum fully-qualified DML/SELECT grants needed across representations, but does not own domain tables. |
| module runtime login roles | LOGIN | schema USAGE + required table DML/sequence use only；no owner-role membership, schema CREATE, bridge/mode function execute, ALTER/trigger/owner/grant capability. |
| short-lived Platform migrator/operations role | controlled LOGIN | may `SET ROLE` only during approved migration/bridge-mode operation；credential is not present in application runtime. |

`compatibility_bridge_mode` is a single Platform-owned row with `legacy_delete_sync_enabled` and version。The `users` DELETE trigger remains installed; its SECURITY DEFINER function reads this mode and synchronously removes Organization state only when enabled。Rollout/rollback uses a restricted `SetLegacyDeleteSyncMode(expected_version, enabled, trusted_recovery_context)` function, not runtime `ALTER TABLE DISABLE TRIGGER`。The mode update and immutable `compatibility_bridge_mode_changes` row (authenticated principal, authorization source, approval/reason, old/new value/version, request/correlation IDs, timestamp) commit atomically。

Catalog tests assert owners/memberships/grants, revoke schema `CREATE` from PUBLIC/runtime roles, and prove runtime roles cannot disable/replace triggers, alter/change owners, execute mode-change functions, or grant themselves privilege。

`compatibility_rollout_gates` is append-only Platform evidence for each rollout phase。A record includes phase/capability manifest hash, bridge mode/version, observation window start/observed-through, monitoring gap flag, compared legacy/new row counts and version/checksum summaries, mismatch count/last mismatch time, rollback artifact identity/suite result, approving trusted principal/approval ID and timestamp。Any mismatch, missing sample or monitoring gap closes the current record and resets the 72-continuous-hour window；route disable requires one complete passing record and an approval appended after the window。

## Migration sequence

### Expand / backfill / cutover

1. 唯一 Platform migrator 为 `users` 添加 nullable lifecycle/version，回填 `active`/`1`，加 NOT NULL/CHECK；若使用 temporary default，回填后 DROP DEFAULT，使所有 insert 显式选择 lifecycle。
2. 创建 nullable-department `organization_user_departments`、owner command receipts、`admin_workflows`/`admin_workflow_subjects`、lease-aware IAM outbox 和 successful-dedupe Organization inbox。
3. 为现有每个 IAM user 回填一个 Organization state row：复制 non-null department，null department 作为 versioned tombstone；验证 source/target count 和逐条 mapping，孤儿引用使 rollout gate 失败。
4. Platform migrator 安装临时 bridge triggers：
   - `users` INSERT creates Organization state (legacy department or null tombstone, version 1)；
   - `users.department_id` UPDATE upserts/increments Organization state only when value differs；
   - Organization state INSERT/UPDATE syncs legacy column only when value differs；
   - initial `users` DELETE trigger physically removes Organization state for legacy-direct-delete safety。
   Functions are owned by dedicated NOLOGIN `compatibility_bridge_owner`, `SECURITY DEFINER SET search_path=pg_catalog` and fully schema-qualify objects；`REVOKE ALL ... FROM PUBLIC`，domain tables remain owned by separate NOLOGIN owner roles, runtime logins receive only minimum DML and no owner membership/schema CREATE/bridge mode privilege, and migration login role is not retained at runtime。Before BFF delete-event acceptance, legacy delete handler first delegates IAM `DeleteUsers`/outbox, then an approved short-lived Platform operation atomically switches `legacy_delete_sync_enabled=false` with immutable audit；event failure injection runs only after that. Rollback switches mode true and verifies it before a binary that can direct-delete。保留旧 FK/index。
5. 持续 compare legacy/new mapping，修复差异；BFF 读路径切到 Organization membership，但 legacy router 保持可运行。
6. 完成 read/write cutover，停止 legacy writes，做 final sync/verify 后才停用 legacy route。
7. 回滚窗口内保留 legacy column/FK/index、bridge 和 additive evidence tables；独立 cleanup/Organization extraction feature 才可 destructive drop。

### Immediate rollback

1. freeze management writes/new producers；dispatcher 进入 drain mode，不再接收新事件但继续处理 retryable backlog。
2. 恢复 expired leases，drain retryable pending；然后 stop new claims，使用 command receipts 解析 running workflows，并 snapshot unresolved leased/blocked evidence。
3. reconcile deleted-IAM/stale-Organization exclusions；在切换 route 前从 Organization state final-sync `users.department_id`，验证 count/value/FK/index。
4. 若 rollback artifact 可能 direct-delete，使用 short-lived authorized Platform operation 将 `legacy_delete_sync_enabled=true` 并验证 mode/audit/trigger behavior；不授予 runtime ALTER privilege。
5. 只有 parity 和 bridge-mode gate 通过后回退应用 route/query path 和 **预先验证的 004-compatible rollback artifact**；否则保持 writes frozen 并 reconciliation。Frozen pre-004 baseline 仅用于行为对比，不是 extended-contract rollback binary。
6. 不 drop `admin_workflows`、command receipts、outbox/inbox 或 compatibility schema；它们作为 dormant reconciliation evidence 保留。
7. 不回滚 `000004`/`000005`。

## Validation rules

- 真实 PostgreSQL migration tests 必须从 migration 000005 状态起步，覆盖已有用户、空部门、有效部门和异常数据。
- Architecture tests 必须证明新 runtime SQL 不引用其他 owner 的表。
- `sqlc generate` 后 generated diff 必须仅来自配置/SQL 变化。
- 所有空列表公共响应保持 `[]`；optional department 字段必须存在且无关系时为 JSON `null`。
- outbox/inbox payload、command receipts 和 workflow rows 需要隐私测试，确保不存在密码、token、password hash 或 credential-derived fingerprint。
- concurrency tests 必须证明 stale expected user/membership version 被拒绝，delayed compensation 不覆盖较新 update。
- lease tests 必须覆盖 worker crash、expiry reclaim、stale claim-token ack rejection 和 durable blocked state。
