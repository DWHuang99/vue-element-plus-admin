# IAM Application Contract

**Feature**: `004-microservice-splitting`  
**Owner**: IAM  
**Current adapter**: in-process Go  
**Future adapter**: transport-neutral; Organization extraction may leave IAM in-process initially

## Boundary rules

IAM owns identity, credentials, sessions, roles, permissions and authorization. IAM MUST NOT:

- import Organization or Admin BFF packages；
- query `departments` or `organization_user_departments`；
- expose Gin, HTTP status, pgx, pgtype or sqlc-generated rows in application models；
- accept or return an Organization department model；
- write password/token/hash into event payloads or logs。

Admin BFF maps IAM typed results/errors to public HTTP.

## Core models

### Principal

```text
Principal
- user_id: int64
- username: string
- session_id: internal stable reference (not raw token)
```

Only produced after successful session authentication. Raw token is consumed by IAM authentication adapter and is not included in Principal.

### IdentityProfile

```text
IdentityProfile
- id: int64
- username: string
- account: string?
- email: string?
- lifecycle_state: provisioning | active | disabled
- version: int64  # monotonic IAM aggregate version
- created_at
- updated_at
```

### RoleSummary

```text
RoleSummary
- id: int64
- name: string
- code: string
- is_builtin: bool
- created_at
```

### AuthorizationProfile

```text
AuthorizationProfile
- roles: RoleSummary[]
- effective_permissions: string[]  # sorted, unique, non-null
```

### ManagedIdentity

IdentityProfile plus role IDs/names needed by BFF user list and edit use cases. It never contains password hash or session token.

## Authentication operations

### Register

```text
Register(username, password, request_context) -> AuthSession
```

**Guarantees**:

- validates current username/password rules；
- hashes password with reviewed Argon2id parameters and independent salt；
- atomically creates active user, default `user` role assignment and session；
- returns raw opaque token exactly once in `AuthSession`；database stores only token hash；
- does not create Organization membership；
- duplicate username returns `UsernameTaken`；
- no partial user without default role/session on failure。

### Login

```text
Login(username, password, request_context) -> AuthSession
```

**Guarantees**:

- nonexistent username, wrong password, provisioning and disabled states return equivalent `InvalidCredentials` semantics；
- only active users can receive sessions；
- rate limiting remains applied at BFF/transport boundary with current dimensions；
- logs security outcome without username/email/password/token leakage。

### RevokeSession

```text
RevokeSession(raw_token, request_context) -> success
```

- correctly formatted unknown/expired/revoked token is idempotent success；
- malformed/missing Bearer syntax is handled by BFF protocol layer before invocation；
- no token-validity detail is returned。

### Authenticate

```text
Authenticate(raw_token, request_context) -> Principal
```

- validates token hash, revocation, sliding expiry, absolute expiry and active user state；
- returns `InvalidToken` for all invalid-state variants；
- preserves current sliding-renewal behavior；
- raw token is never returned。

## Identity and authorization reads

### GetIdentity

```text
GetIdentity(user_id) -> IdentityProfile
```

No Organization fields are returned.

### GetAuthorizationProfile

```text
GetAuthorizationProfile(user_id) -> AuthorizationProfile
```

- roles and permissions are computed only from IAM tables；
- permissions are unique and sorted；
- empty list is `[]`；
- built-in and custom roles follow current catalog rules。

### HasPermission

```text
HasPermission(user_id, permission_code) -> bool
```

- queries current grants on every invocation；
- no authorization-decision cache；
- unknown permission code returns `false` and never grants access；this behavior is fixed and contract-tested。

### BatchGetManagedIdentities

```text
BatchGetManagedIdentities(
  candidate_user_ids?, username_filter?, account_filter?, page_index, page_size
) -> {items: ManagedIdentity[], total: int64}
```

**Semantics**:

- `candidate_user_ids` absent means all normal managed users；present empty means empty result without query expansion；
- only `active` and `disabled` users appear; `provisioning` is excluded from normal list；
- username/account filtering and pagination occur inside IAM before return；
- role names/IDs are aggregated in bounded SQL/batch logic, not per-row BFF calls；
- ordering remains deterministic and compatible with current endpoint。

## Role operations

### ListRoles

Returns current public role fields and deterministic order.

### ValidateRoleIDs

```text
ValidateRoleIDs(ids[]) -> RoleSummary[]
```

- validates entire set；any missing role returns `RoleNotFound` with no writes；
- duplicate input IDs do not create duplicate assignments。

### SaveRole

```text
SaveRole(id?, name, code) -> role_id
```

- create/update semantics match public contract；
- name/code uniqueness enforced in database；
- built-in code immutable；
- one IAM-local transaction where multiple writes are required。

### DeleteRoles

```text
DeleteRoles(ids[]) -> success
```

- full-batch preflight before delete；
- missing, built-in or referenced role rejects the entire batch；
- deletion is IAM-local atomic；
- preserves `BUILTIN_ROLE_DELETE_PROTECTED` and reference protection semantics。

## Managed-user commands

All mutating commands accept `OperationContext`:

```text
OperationContext
- operation_id: UUID
- idempotency_key: string?  # optional public key; operation_id is always present
- actor_user_id: int64
- correlation_id: string
```

IAM stores a command receipt for every workflow mutation. The side effect and receipt commit in the same IAM transaction; BFF remains workflow owner.

### CreateProvisioningUser

```text
CreateProvisioningUser(
  context,
  username,
  account?,
  email?,
  password,
  role_ids[]
) -> {user_id, resulting_version}
```

**Transaction**:

1. validate username/profile and all roles；
2. hash password；
3. create `provisioning` user；
4. replace/insert role assignments；
5. record successful IAM command receipt with user ID/version；
6. commit side effect and receipt atomically。

**Guarantees**:

- provisioning user cannot login and is excluded from normal lists；
- retry with same operation ID returns the same user ID/resulting version without duplicate user/roles；
- username conflicts from a different operation return `NameTaken`；
- password is never persisted outside password hash。

### ActivateUser

```text
ActivateUser(context, user_id, expected_version) -> resulting_version
```

- only valid new transition is `provisioning -> active` with expected-version CAS；
- replay of the same committed operation returns its receipt/result, not a second transition；
- an already-active user from another operation or a disabled user returns typed state/version conflict；re-enable is outside this feature；
- side effect + receipt are one transaction；missing user returns `UserNotFound`。

### DisableUser

```text
DisableUser(context, user_id, expected_version, reason_code) -> resulting_version
```

- expected-version CAS changes lifecycle state, increments user version, revokes all sessions and writes receipt in one IAM transaction；
- `reason_code` is safe classification, not free-form secret/PII。

### UpdateManagedUser

```text
UpdateManagedUser(
  context,
  user_id,
  expected_version,
  username,
  account?,
  email?,
  password?,
  role_ids[]
) -> resulting_version
```

- validates user and all role IDs before writes；
- updates only when `users.version = expected_version`, then increments version；stale expected version returns `VersionConflict`；
- side effect and command receipt commit atomically；
- blank/absent password preserves existing hash；
- if password supplied, hashes then applies with profile and role replacement in one IAM transaction；
- no Organization membership parameter；
- idempotent by operation ID；
- no partial profile/credential/role update。

### DeleteUsers

```text
DeleteUsers(context, targets[{user_id, expected_version}]) -> BatchDeleteResult[{user_id, tombstone_version}]
```

**Transaction**:

1. normalize IDs and full-batch validate all users plus expected versions；stale target rejects the whole batch；
2. apply current protection rules；
3. delete users (sessions and user_roles use IAM-local cascade/explicit cleanup)；
4. assign each event aggregate tombstone version = deleted user's prior `version + 1` and insert one (`event_type=iam.user.deleted`, `event_version=1`) outbox row per user；
5. write the delete command receipt；
6. commit users/outbox/receipt atomically。

- any invalid ID rejects entire batch；
- synchronous result and one batch receipt keyed by `(operation_id, delete_users)` contain the same normalized per-user expected/tombstone versions；retry/resolve returns that result and does not duplicate outbox events；
- committed deletion is authoritative even if Organization cleanup is pending。

### CompensateProvisioningUser

```text
CompensateProvisioningUser(context, user_id, expected_version) -> success
```

- only applies to the workflow-created subject while it remains `provisioning` at the expected version；active/disabled/newer-version state returns conflict and requires reconciliation；the command has one outcome: delete, never disable；
- same operation replay resolves through its receipt；side effect and receipt commit atomically；
- physically deletes the still-provisioning user, revokes sessions (normally none), and emits the normal deletion event；it never converts a failed create into visible `disabled` user or permanently occupies the username；
- if deletion path is used, produces normal user-deleted event so any partial Organization membership is cleaned。

## Command resolution

### IAMCommandReceipt

```text
IAMCommandReceipt
- operation_id: UUID
- command_name: string
- request_fingerprint: safe non-secret digest
- status: succeeded | terminal_rejected
- subject_id: int64?
- resulting_version: int64?
- safe_result: transport-neutral minimal result
- error_code: string?
```

```text
ResolveCommand(operation_id, command_name, expected_fingerprint) -> IAMCommandReceipt?
```

- receipt absence means no committed side effect is proven；it is not fabricated as failure/success；
- resolver compares `expected_fingerprint` with stored safe fingerprint; mismatch returns `OperationConflict`；
- fingerprints use the consistency contract's versioned SHA-256/RFC 8785 canonical JSON schema and exclude password/token/hash/password-derived material；
- create/update/activate/delete/compensation side effect and receipt are atomic；
- BFF MUST resolve after timeout/crash before retrying or compensating. In particular, activation timeout is resolved before any membership/user compensation.

## Outbox delivery port

Integration may dispatch IAM events only through:

```text
ClaimOutbox(batch_size, lease_owner, lease_duration) -> ClaimedEvent[]
AckOutbox(event_id, claim_token)
RecordOutboxFailure(event_id, claim_token, safe_error_code, next_available_at) -> pending | blocked(delivery_exhausted)
BlockOutbox(event_id, claim_token, safe_reason_code)
RequeueBlockedOutbox(event_id, trusted_recovery_context, approved_reason_code, available_at) -> new_delivery_epoch
GetOutboxBacklog() -> safe counters/ages
```

- claim transaction first blocks pending/expired-lease rows whose epoch deadline elapsed；otherwise it persists `leased` state/owner/expiry/new claim token and increments current-epoch + total attempts。Each fresh claim/reclaim counts, so crash-after-claim loops consume budget；
- consumer call occurs after that transaction closes；
- ack/record-failure/block are claim-token CAS and reject stale workers；`RecordOutboxFailure` atomically increments no additional attempt, evaluates the already-counted attempt/time ceiling and chooses exactly one transition to pending/backoff or blocked, avoiding reschedule-then-block races；
- unsupported type/version is blocked durably；each delivery epoch auto-blocks as `delivery_exhausted` after the 20th failed claimed attempt or `epoch_started_at + 24h`；
- `trusted_recovery_context` is created only by an authenticated Platform operations adapter and binds principal ID, authorization source, approval ID and correlation/request IDs；IAM rejects arbitrary caller text, unauthorized principals, non-blocked events and unapproved reason codes；there is no public browser requeue API；
- audited requeue increments delivery epoch, sets `epoch_started_at = available_at`, resets epoch attempt/deadline, preserves total attempts and writes immutable full previous-epoch/block evidence in the same transaction；
- blocked events have no waiver-to-purge path in this feature：they remain blocked or are authorized/requeued and eventually published；
- Integration MUST NOT import IAM sqlc or mutate outbox rows directly.

## Evidence cleanup port

```text
EvaluateEvidenceCleanup(cutoff) -> {eligible_counts, blocking_unresolved_count, oldest_replayable_at}
PurgeEligibleEvidence(cutoff, trusted_recovery_context) -> deleted_counts
```

Platform coordinates conservatively with BFF/Organization watermarks；IAM never queries their tables。Purge requires authenticated approval, an immediately preceding passing dry run, owner-local transaction, and MUST NOT delete unresolved receipts, pending/leased/blocked events, requeue audit still linked to retained evidence, or anything inside rollback/replay/30-day minima。Uncertain/unavailable dependency means no deletion。

## Administrative bootstrap

### GrantBuiltInAdminRole

```text
GrantBuiltInAdminRole(username, role_code) -> {user_id, resulting_version}
```

- role code only `admin` or `super_admin`；
- user must already exist through normal registration；
- no password input；
- transaction locks current user row, idempotently grants while preserving existing roles, and increments `users.version` only when assignment changes；it does not require a caller-supplied stale version because it deliberately operates on the locked latest state；
- used by `cmd/admin-init` and controlled operations；
- adapter does not log `DATABASE_URL` or credentials。

## Typed error taxonomy

| IAM error | Meaning | Typical public mapping |
|---|---|---|
| `InvalidInput(fields)` | IAM validation failed | 400 `AUTH_INVALID_INPUT` |
| `InvalidCredentials` | uniform login failure | 401 `AUTH_INVALID_CREDENTIALS` |
| `InvalidToken` | invalid protected session | 401 `AUTH_INVALID_TOKEN` |
| `UsernameTaken` / `NameTaken` | unique conflict | 409 existing contract code |
| `UserNotFound` | user ID absent | 404 `USER_NOT_FOUND` |
| `RoleNotFound` | role ID/code absent | 404 `ROLE_NOT_FOUND` |
| `BuiltinRoleCodeImmutable` | built-in code mutation | 409 existing code |
| `BuiltinRoleDeleteProtected` | built-in deletion | 409 existing code |
| `DeleteProtected` | role referenced/protected | 400 `DELETE_PROTECTED` |
| `OperationConflict` | scoped idempotency/receipt fingerprint conflict | 409 `IDEMPOTENCY_CONFLICT` |
| `VersionConflict` / `InvalidLifecycleTransition` | stale CAS or forbidden state transition | active workflow resolves/reconciles; public 409 `OPERATION_IN_PROGRESS` or 500 `RECONCILIATION_REQUIRED` by workflow state |
| `Unavailable` | infrastructure dependency unavailable | 503 `DEPENDENCY_UNAVAILABLE` |
| `Timeout` | internal deadline, outcome unresolved in HTTP window | 504 `DEPENDENCY_TIMEOUT` |
| `Internal` | unexpected safe classification | 500 `INTERNAL_ERROR` |

Errors MUST be matchable by type/code without parsing human messages.

## Transaction, version and retry rules

- `users.version` covers identity/profile, credential, lifecycle and user-role assignment mutations；session creation/activity/revocation does not increment user version except when part of a lifecycle command whose user mutation already increments it。
- IAM service owns all IAM transactions；BFF cannot pass a transaction handle。
- retryable classification is explicit；validation, not-found and conflict are not retried automatically。
- passwords are not retained for automatic replay；before a credential command commits, retry requires client resubmission; after commit, receipt resolution continues without retaining/replacing password material。
- module command idempotency/receipts survive process restart and remain at least 30 days/longer than referencing workflow and rollback support；unresolved receipts are not auto-purged。
- timeout does not imply failure；BFF calls `ResolveCommand` before issuing a new side effect or compensation。

## Observability and privacy

Every application operation receives correlation context and records:

- operation name, module=`iam`, duration, outcome/error code；
- actor/subject IDs only where required for audit；
- no password, token, password hash, complete email, DB URL or SQL text with values；
- security events for login success/failure, logout, role change, lifecycle change and admin promotion。

## Contract tests

The PostgreSQL adapter and any future adapter MUST pass the same application contract suite:

- authentication/session lifecycle and uniform failures；
- role/permission invariants；
- managed-user transaction rollback；
- provisioning cannot authenticate；
- idempotent operation replay and receipt resolution after commit-before-response crash；
- expected user-version CAS and strict provisioning→active transition；
- batch atomic deletion；
- user deletion + command receipt + outbox atomicity/tombstone version；
- outbox lease recovery, claim-time attempt accounting, atomic failure→pending/blocked transition, stale claim rejection, durable blocked state, per-epoch exhaustion and authenticated/audited requeue；
- no Organization SQL/import dependencies；
- empty permissions are sorted non-null `[]`。
