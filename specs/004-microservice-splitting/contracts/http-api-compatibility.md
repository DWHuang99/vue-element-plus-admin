# Public HTTP Compatibility Contract

**Feature**: `004-microservice-splitting`  
**Base path**: `/api/v1`  
**Owner**: Admin BFF

## Purpose

本契约冻结微服务拆分准备期间的浏览器 API。内部 handler、module、sqlc package 或 adapter 可变化，但 Vue 不应因本 feature 改 URL、payload 或权限逻辑。

可复现 baseline（包含未提交权限闭环工作树，因此 HEAD 不是唯一行为来源）：

- repository HEAD `d4b1bae75b37f894879360099066ad49ab78da91`；
- Auth contract v1.1.0 SHA-256 `3efc6933ffca57596bcb454801434d61a1f811721ab25b32f8d21f3ff5597bee`；
- RBAC contract v1.1.0 SHA-256 `6254e5f4c8b5c64912d400a7b06fb4da09632e3ee6cc2e7b0ffed1ab0dc52a8f`；
- tracked implementation binary diff SHA-256 `aa6aeb6265b00b50bb85cfbc120442fb37322988f10ebdc9bdc3f92c3057867a`；
- 15-file untracked implementation manifest SHA-256 `5b8a82ab168561d7d0bf68dbb4c13afd2210f21f57374604f1296510204524e0`；
- reconstructable implementation patch `specs/004-microservice-splitting/baseline-implementation.patch`, 123047 bytes, SHA-256 `659facfdf93a49bf2caa7e09728451029271ace9b61ab31da47dc5dd0e57e2ee`；
- exact Auth/RBAC v1.1.0 snapshot patch `specs/004-microservice-splitting/baseline-contracts.patch`, 9008 bytes, SHA-256 `f69c52dfefa706d6411c56be1ee90e74f0794c26f48dbc95973a4c9cea9bfa38`。

The two worktree identifiers exclude `specs/**` and `.specify/**` so design-document edits do not change the frozen implementation baseline. Both patches MUST be hash-verified and applied in order with `git apply --binary --check` then `git apply --binary` only in a disposable detached worktree at the frozen HEAD；all baseline bytes are recoverable from frozen HEAD + these artifacts, without copying mutable current contracts. Never apply either patch to the current dirty worktree.

本契约对迁移影响的 endpoint/header/field/null/error 自包含并优先；其余校验继承上述冻结版本，不引用“latest”。

## Edge routing

```text
Browser
  ├── GET /... SPA assets ──> Nginx static Vue build
  └── /api/* ───────────────> Nginx proxy ──> Admin BFF
```

- Nginx MUST 保留 `/api` path prefix。
- Browser MUST NOT 直接访问 IAM、Organization 或 dispatcher。
- Admin BFF → IAM/Organization 调用 MUST NOT 经过边缘 Nginx。
- 生产环境非本地通信 MUST 使用 TLS。

## Common conventions

### Authentication

受保护端点使用：

```http
Authorization: Bearer <opaque-token>
```

原始 token 只在浏览器与 Admin BFF/IAM authentication path 中使用，不传给 Organization，不写日志/事件/workflow。

### Managed-user idempotency header

`POST /users` and `POST /users/delete` accept:

```http
Idempotency-Key: 8f9703d0-04a7-4d4a-a75a-8807eb733961
```

- optional for backward compatibility; official frontend MUST send it for every managed-user write；
- 16–128 chars, ASCII `[A-Za-z0-9._:-]+`；invalid values return `400 AUTH_INVALID_INPUT`；
- generate once per logical submission and reuse across timeout/retry；do not generate a new key merely because the response was lost；
- scope is `(operation_type, actor_user_id, key)`；same key + same safe fingerprint resumes/returns one workflow；same key + conflicting safe fields returns `409 IDEMPOTENCY_CONFLICT`；
- replay evidence is retained at least 24 hours；password/token/hash and password-derived digest are excluded from stored fingerprint；
- authentication and current route authorization occur before idempotency lookup；a completed replay returns the original public status/body only while the actor remains authorized。A revoked actor receives current `403 AUTH_FORBIDDEN`；restored permission may replay stored success but cannot reopen a workflow terminally rejected due to revocation；
- same operation still running returns `409 OPERATION_IN_PROGRESS` plus `Retry-After` integer seconds；`awaiting_client_input` is not worker-running and requires same-key authorized HTTP resubmission of transient credential。
- password is intentionally outside the fingerprint。For unknown-outcome retry the official client reuses the original credential；an intentional credential change uses a new key。The first IAM credential receipt committed for the operation is authoritative；later password bytes for that key are ignored and never stored/compared。

### Success envelope

```json
{"data": {}}
```

读取 payload 位于 `data`。所有写端点必须返回非空 JSON envelope。

### Error envelope

```json
{
  "error": {
    "code": "MACHINE_CODE",
    "message": "safe message",
    "field_errors": [
      {"field": "username", "code": "TOO_SHORT", "message": "safe message"}
    ]
  }
}
```

- `field_errors` 仅在适用时出现。
- 不暴露 SQL、stack、内部包路径、服务拓扑、密码或完整 token。
- 每个响应支持 `X-Request-Id` 关联。

## Endpoint ownership and compatibility matrix

| Method | Path | Auth / permission | Success | Internal behavior |
|---|---|---|---|---|
| POST | `/auth/register` | public + register rate limit | `201` auth token response | BFF delegates IAM register；注册在 IAM 本地事务。 |
| POST | `/auth/login` | public + login rate limit | `200` auth token response | BFF delegates IAM login。 |
| POST | `/auth/logout` | Bearer header | `200 {"data":{}}` | BFF delegates IAM revoke；保持幂等。 |
| GET | `/auth/me` | valid session | `200` current user profile | BFF 聚合 IAM + Organization。 |
| GET | `/roles` | `roles.read` | `200 {data:{list,total}}` | BFF delegates IAM。 |
| POST | `/roles` | `roles.write` | `200 {"data":{}}` | BFF delegates IAM。 |
| POST | `/roles/delete` | `roles.write` | `200 {"data":{}}` | IAM 本地全批次预检和原子删除。 |
| GET | `/departments` | `departments.read` | `200 {data:{list}}` | BFF delegates Organization。 |
| POST | `/departments` | `departments.write` | `200 {"data":{}}` | BFF delegates Organization。 |
| POST | `/departments/delete` | `departments.write` | `200 {"data":{}}` | Organization 本地全批次预检和原子删除。 |
| GET | `/users` | `users.read` | `200 {data:{list,total}}` | BFF IAM + Organization bounded composition。 |
| POST | `/users` | `users.write` | `200 {"data":{}}` | BFF 持久化 workflow 编排 create/update。 |
| POST | `/users/delete` | `users.write` | `200 {"data":{}}` | IAM authoritative batch delete + outbox。 |

## Stable auth behavior

### Register

Request and validation remain:

```json
{"username":"alice","password":"supersecret123"}
```

- username: required, 3–32, `^[a-zA-Z0-9_]+$`
- password: required, 8–72 bytes
- success: 201, token/token_type/expires_in/summary user
- duplicate: 409 `AUTH_USERNAME_TAKEN`
- invalid: 400 `AUTH_INVALID_INPUT`
- limit: 429 `RATE_LIMITED` + `Retry-After`

### Login

- nonexistent username and wrong password MUST both return 401 `AUTH_INVALID_CREDENTIALS` with equivalent safe structure.
- `provisioning` or `disabled` user MUST NOT authenticate and MUST use the same uniform invalid-credentials response.
- success response shape remains unchanged.

### Logout

- missing/malformed Bearer header: 401 `AUTH_INVALID_TOKEN`。
- correctly formatted but invalid/expired/revoked token: `200 {"data":{}}`，不暴露 token 状态。
- valid token: revoke then `200 {"data":{}}`。
- NEVER return 204。

### `/auth/me`

Public response remains:

```json
{
  "data": {
    "user": {
      "id": 1,
      "username": "alice",
      "account": "Alice",
      "email": "alice@example.com",
      "created_at": "2026-08-05T10:00:00Z",
      "department": {"id": 1, "name": "研发部"},
      "roles": [{"id": 2, "name": "管理员", "code": "admin"}],
      "effective_permissions": ["departments.read", "departments.write"]
    }
  }
}
```

- `department` 字段 MUST 存在；无关系时固定为 JSON `null`，不得省略或返回 `{}`。
- `roles` 与 `effective_permissions` MUST 为数组。
- `effective_permissions` MUST 去重、有序；无权限为 `[]`，不为 `null`。
- IAM 成功而 Organization dependency 失败时，不返回伪装成完整的新鲜 profile；返回稳定 dependency failure，并记录 request correlation。

## Stable RBAC behavior

### Permission matrix

| Permission | Endpoints |
|---|---|
| `roles.read` | `GET /roles` |
| `roles.write` | `POST /roles`, `POST /roles/delete` |
| `departments.read` | `GET /departments` |
| `departments.write` | `POST /departments`, `POST /departments/delete` |
| `users.read` | `GET /users` |
| `users.write` | `POST /users`, `POST /users/delete` |

- `admin` and `super_admin` receive all six in generation 1。
- `user` and newly created custom roles receive none by default。
- 每个受保护请求实时查询 current IAM grants；不缓存 authorization decision。

### Roles

`GET /roles` item retains:

```json
{"id":1,"name":"超级管理员","code":"super_admin","is_builtin":true,"created_at":"2026-08-07T03:00:00Z"}
```

Stable errors include:

- `AUTH_INVALID_INPUT` 400
- `ROLE_NOT_FOUND` 404
- `NAME_TAKEN` 409
- `BUILTIN_ROLE_CODE_IMMUTABLE` 409
- `BUILTIN_ROLE_DELETE_PROTECTED` 409
- `DELETE_PROTECTED` 400

Built-in role codes remain immutable and built-in roles remain undeletable after module migration.

### Departments

- `GET /departments` remains a nested tree under `data.list`。
- Save fields remain `id?`, `name`, `parent_id?`。
- Self-parent and invalid parent are rejected。
- Delete batch is fully prevalidated; child departments or memberships block the entire batch。
- Stable errors: `AUTH_INVALID_INPUT`, `DEPARTMENT_NOT_FOUND`, `NAME_TAKEN`, `DELETE_PROTECTED`。

### Users

`GET /users` query remains:

- `department_id?`
- `page_index` from 1, default 1
- `page_size` default 10, max 100
- `username?` fuzzy filter
- `account?` fuzzy filter

Response item remains:

```json
{
  "id": 1,
  "username": "admin",
  "account": "管理员",
  "email": "admin@example.com",
  "create_time": "2026-08-07T03:00:00Z",
  "role": "超级管理员,管理员",
  "department": {"id": 1, "name": "研发部"}
}
```

- `role` remains comma-separated role names for current frontend compatibility。
- `department` field MUST always be present；no membership is JSON `null`。
- filtering MUST happen before pagination；BFF 不得分别分页后再内存过滤。
- BFF composition call count MUST be bounded and avoid per-row Organization calls。

`POST /users` retains fields and validation:

- `id?`
- required `username`
- optional `account`, `email`, `department_id`
- password required on create; blank on update means unchanged
- `roles` replacement list

Stable errors include `AUTH_INVALID_INPUT`, `NAME_TAKEN`, `USER_NOT_FOUND`, `ROLE_NOT_FOUND`, `DEPARTMENT_NOT_FOUND`。

`POST /users/delete` remains full-batch semantics: any nonexistent/invalid ID rejects without partial IAM deletion.

## Authentication vs authorization failures

| Condition | HTTP | Code | Frontend effect |
|---|---|---|---|
| missing/malformed/invalid/expired/revoked session on protected request | 401 | `AUTH_INVALID_TOKEN` | clear auth state and redirect login according to existing interceptor/guard |
| valid session, missing endpoint permission | 403 | `AUTH_FORBIDDEN` | keep token; refresh `/auth/me`; rebuild dynamic routes |

Admin BFF MUST NOT map dependency, workflow, SQL or transport errors to 401/403 unless the actual authentication/authorization condition applies.

## Workflow and dependency failure mapping

| HTTP | Code | Contract meaning | `Retry-After` contract |
|---|---|---|---|
| 409 | `IDEMPOTENCY_CONFLICT` | same scoped key used with conflicting safe request fields | MUST NOT be present; do not retry that key/payload combination |
| 409 | `OPERATION_IN_PROGRESS` | same logical workflow is currently owned/running | MUST be integer delta-seconds 1–60; use known lease/next-check delay, default 3 |
| 409 | `OPERATION_EXPIRED` | client-only credential was not resubmitted before the durable input deadline and any reversible side effect was safely compensated | MUST NOT be present; submit corrected/new credential as a new logical operation/key |
| 503 | `DEPENDENCY_UNAVAILABLE` | IAM/Organization/workflow store temporarily unavailable | MUST be integer delta-seconds 1–60; use health backoff, default 5 |
| 504 | `DEPENDENCY_TIMEOUT` | internal deadline expired and outcome resolution did not complete within HTTP deadline | MUST be integer delta-seconds 1–60; default 3; retry same key because timeout is not rollback proof |
| 503 | `WORKFLOW_RETRYABLE` | durable workflow can safely resume but has not reached public success | MUST be integer delta-seconds 1–60; use persisted next retry, default 3 |
| 500 | `RECONCILIATION_REQUIRED` | automatic recovery/compensation cannot safely proceed (including CAS conflict) | MUST NOT be present; do not blind retry with a new key |

All use the normal non-secret error envelope and `X-Request-Id`. These presence/range/default rules apply to every route wiring that can emit the code and are exact contract-test assertions. No dependency/workflow error maps to 401/403 unless authentication/authorization actually failed. Compensation/receipt/internal step details are not exposed. HTTP retry with the same idempotency key returns/continues the same logical operation rather than duplicating side effects.

## Contract verification

The same table-driven suite MUST run against:

1. legacy route wiring before cutover；
2. Admin BFF route wiring；
3. rollback route wiring in the exact pre-pinned/hashed **004-compatible rollback artifact** during the supported window。The frozen pre-004 characterization baseline is not an eligible rollback binary because it does not implement this feature's idempotency/workflow additions。

Required checks include exact method/path/status/error code, JSON field names/types, `department: null`, non-null arrays, 200 write bodies, permission matrix, logout idempotency, 403 session preservation, built-in role protection, department-filtered pagination, idempotency header validation/replay/conflict/running/awaiting-client behavior, current authorization before completed replay, revocation before later side effects, exact workflow/dependency codes and `Retry-After`.
