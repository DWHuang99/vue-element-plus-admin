# Organization Application Contract

**Feature**: `004-microservice-splitting`  
**Owner**: Organization  
**Current adapter**: in-process Go  
**First physical extraction candidate**: yes

## Boundary rules

Organization owns department hierarchy and user-to-department membership. It MUST NOT:

- import IAM or Admin BFF packages；
- query `users`, `sessions`, `roles`, `user_roles`, `permissions` or `role_permissions`；
- create a database FK from membership `user_id` to IAM users；
- authenticate raw tokens or call IAM for every request；
- expose Gin, HTTP status, pgx, pgtype or sqlc-generated rows in application models。

`user_id` is an opaque, stable external subject reference. BFF validates current IAM existence before synchronous membership commands; IAM deletion events asynchronously remove stale membership.

## Core models

### Department

```text
Department
- id: int64
- name: string
- parent_id: int64?
- created_at
- updated_at
```

### DepartmentNode

```text
DepartmentNode
- Department fields
- children: DepartmentNode[]  # non-null
```

### MembershipState

```text
MembershipState
- user_id: int64
- department_id: int64?  # null is durable versioned no-department state
- department_name: string?  # null when department_id is null
- membership_version: int64
- created_at
- updated_at
```

A state row is retained across clear; only terminal IAM user deletion physically removes it. This preserves CAS through absent-state ABA cycles.

### OperationContext

```text
OperationContext
- operation_id: UUID
- idempotency_key: string?  # optional public key; operation_id always present
- actor_user_id: int64
- correlation_id: string
```

Organization does not interpret actor permissions; it trusts the authenticated/authorized Admin BFF boundary in the in-process phase and records minimum audit context.

## Department operations

### ListDepartmentTree

```text
ListDepartmentTree() -> DepartmentNode[]
```

- deterministic ordering compatible with current public response；
- `children` is `[]`, never null；
- reads only Organization-owned tables。

### GetDepartment

```text
GetDepartment(department_id) -> Department
```

Missing ID returns `DepartmentNotFound`.

### ValidateDepartment

```text
ValidateDepartment(department_id?) -> Department?
```

- null means no department and is valid；
- non-null missing ID returns `DepartmentNotFound`；
- used by BFF preflight before managed-user workflow starts。

### SaveDepartment

```text
SaveDepartment(context, id?, name, parent_id?) -> department_id
```

**Rules**:

- create/update semantics preserve public contract；
- name 1–64 and unique under current global rule；
- parent must exist and differ from self；
- cycle prevention must cover more than direct self-parent if hierarchy permits deeper updates；
- multiple writes occur in one Organization-local transaction；
- idempotent by operation ID when invoked by BFF workflow。

### DeleteDepartments

```text
DeleteDepartments(context, ids[]) -> success
```

**Full-batch semantics**:

1. normalize and validate all IDs；
2. verify every department exists；
3. if any target has any child department (whether or not the child is also in the batch), reject the entire batch under the frozen compatibility rule；
4. verify no row with non-null `department_id` references any target；nullable tombstone rows do not protect a department；
5. delete all in one Organization transaction。

Any failure results in zero deleted departments. IAM users are never queried.

## Membership operations

### GetUserDepartment

```text
GetUserDepartment(user_id) -> MembershipState?
```

- missing state row means Organization has never established state and is a normal empty result, not `UserNotFound`；
- an existing row with null department is the durable no-department state；public BFF maps both to `department: null` while workflow retains version；
- Organization does not know whether IAM user exists。

### BatchGetUserDepartments

```text
BatchGetUserDepartments(user_ids[]) -> map<user_id, MembershipState>
```

- empty input returns empty map without SQL error；
- duplicate IDs are normalized；
- one bounded query or fixed bounded query set；
- users with no state row are absent; null-department state may be present and BFF maps it to public null；
- no per-user queries。

### ListUserIDsByDepartment

```text
ListUserIDsByDepartment(department_id) -> int64[]
```

- missing department returns `DepartmentNotFound`；
- returns deterministic IDs；
- used by BFF before IAM filtering/pagination；
- for current scale, complete ID set is acceptable；future physical service may add cursor/batch contract without changing public API。

### SetUserDepartment

```text
SetUserDepartment(context, user_id, department_id, expected_membership_version?) -> MembershipMutationResult
```

- department must exist；
- create expects no membership; update uses expected version CAS; stale version returns `MembershipVersionConflict`；
- result/receipt includes previous and resulting department/version；
- upsert by `user_id`；
- setting same department is idempotent；
- changing department increments `membership_version` and updates timestamp；
- no IAM FK/existence query；
- transaction remains Organization-local。

### ClearUserDepartment

```text
ClearUserDepartment(context, user_id, expected_membership_version?) -> MembershipMutationResult
```

- clear never deletes the version-bearing state row；it creates/updates `department_id = null` and increments version under expected-state CAS；
- same operation replay returns its receipt/result; stale expected absence/version conflicts；
- result/receipt records previous department/version and resulting null-state version；
- used by compensation or explicit null department update。

### RestoreUserDepartment

```text
RestoreUserDepartment(
  context,
  user_id,
  expected_current_membership_version,
  previous_department_id?,
  previous_membership_version?
) -> MembershipMutationResult
```

- restore proceeds only when current membership still equals the workflow's applied version/state；otherwise `CompensationConflict` and no overwrite；
- null previous value clears membership；non-null validates and restores previous department with a new monotonic resulting version；
- idempotent through command receipt；
- if previous department no longer exists, returns typed compensation conflict and does not invent data。

### ClearMembershipsForUsers

```text
ClearMembershipsForUsers(context, user_ids[]) -> success
```

- local full-batch/idempotent cleanup operation；
- may be used for reconciliation, but authoritative normal deletion path is inbox event consumption。

## Domain-event consumer

### HandleIAMUserDeletedV1

```text
HandleIAMUserDeletedV1(event) -> success
```

Accepted event:

```text
event_type: iam.user.deleted
event_version: 1
payload.user_id: int64
```

**Transaction semantics**:

1. validate envelope/type/version/payload；invalid or unsupported input returns typed error and does not write a successful inbox row；
2. require `producer=iam`, `aggregate_type=user`, positive aggregate version, canonical numeric aggregate ID, and `aggregate_id == string(payload.user_id)`；mismatch is non-retryable contract error；
3. if `(event_id, handler_name)` already exists, return idempotent success；
4. physically delete the terminal user's membership state row (missing is success)；
5. insert the successful inbox dedupe row；
6. side effect and dedupe row commit in one transaction。

There is no transactional `failed` inbox row: a handler rollback removes both side effect and dedupe marker. Failure attempts/blocked state remain observable in the producer outbox and metrics/logs.

A crash after commit but before IAM outbox acknowledgement is recovered by duplicate delivery and inbox deduplication.

Unsupported event version returns `UnsupportedEventVersion` and MUST NOT silently process with guessed semantics.

## Command receipts and resolution

Every workflow mutation writes an `OrganizationCommandReceipt` in the same local transaction as the membership/department side effect. Membership receipts contain previous/resulting department and membership versions.

```text
ResolveCommand(operation_id, command_name, expected_fingerprint) -> OrganizationCommandReceipt?
```

- resolver verifies `expected_fingerprint`; mismatch returns `OperationConflict`；absent receipt means no committed side effect is proven；
- same identity with conflicting safe fingerprint returns `OperationConflict`；
- BFF MUST resolve timeout/crash before retrying/restoring；
- fingerprint uses the consistency contract's versioned SHA-256/RFC 8785 canonical JSON schema；receipt contains no profile, credential, token or password-derived material.

## Typed error taxonomy

| Organization error | Meaning | Typical public/BFF mapping |
|---|---|---|
| `InvalidInput(fields)` | validation failed | 400 `AUTH_INVALID_INPUT` |
| `DepartmentNotFound` | target/parent missing | 404 `DEPARTMENT_NOT_FOUND` |
| `NameTaken` | department name unique conflict | 409 `NAME_TAKEN` |
| `HierarchyConflict` | self-parent/cycle/invalid delete order | 400 existing validation/delete mapping |
| `DeleteProtected` | children or memberships remain | 400 `DELETE_PROTECTED` |
| `OperationConflict` | scoped idempotency/receipt fingerprint mismatch | 409 `IDEMPOTENCY_CONFLICT` |
| `MembershipVersionConflict` | expected membership version/state is stale | 409 `OPERATION_IN_PROGRESS` when another active operation owns it, otherwise workflow reconciliation |
| `CompensationConflict` | applied version changed; previous state cannot safely restore | 500 `RECONCILIATION_REQUIRED` |
| `UnsupportedEventVersion` | consumer cannot interpret event | producer event becomes durable blocked; no acknowledgement/mutation |
| `Unavailable` | datastore/adapter issue | 503 `DEPENDENCY_UNAVAILABLE` |
| `Timeout` | deadline and unresolved outcome | 504 `DEPENDENCY_TIMEOUT` |
| `Internal` | unexpected safe classification | 500 `INTERNAL_ERROR` |

Errors are typed and must not require parsing human messages.

## Transaction and idempotency rules

- Organization owns all transactions touching Organization tables；BFF cannot pass a pgx transaction。
- every workflow mutation is idempotent by operation ID/key and durable receipt across process restart。
- read calls are side-effect free。
- `Set`/`Clear`/`Restore` use expected-version CAS in addition to receipts。
- timeout is ambiguous；BFF calls `ResolveCommand` before repeating or compensating, rather than inferring only from current membership。
- no automatic retry for validation/not-found/hierarchy conflicts。
- receipts remain at least 30 days and longer than referencing workflows/rollback support; unresolved evidence is not auto-purged。

## Evidence cleanup port

```text
EvaluateEvidenceCleanup(cutoff) -> {eligible_counts, blocking_unresolved_count, oldest_replayable_at}
PurgeEligibleEvidence(cutoff, trusted_recovery_context) -> deleted_counts
```

Platform uses the most conservative BFF/IAM/Organization watermarks；Organization does not query other owners。Purge requires authenticated approval, a passing dry run and one Organization-local transaction；it cannot remove unresolved receipts, inbox dedupe while producer evidence can replay, or records inside rollback/30-day minima。Uncertainty means no deletion。

## Observability and privacy

Record module=`organization`, operation, duration, safe outcome, correlation ID and numeric subject/department IDs when needed. Do not record:

- raw auth token；
- password/hash；
- database URL；
- full user profile, username or email；
- event payload beyond safe IDs and type/version。

Required metrics:

- port call count/duration/error class；
- membership set/clear outcomes；
- department delete protection counts；
- inbox processed/duplicate, handler failures, and producer-observed blocked/oldest pending age。

## Contract tests

The in-process PostgreSQL adapter and future remote adapter MUST pass the same suite:

- deterministic department tree and non-null children；
- parent existence, self-parent and cycle rejection；
- batch delete preflight/atomicity；
- membership set/replace/clear/restore receipt replay and expected-version CAS；
- delayed restore cannot overwrite a newer membership；
- batch membership lookup without N+1；
- department user-ID filtering；
- no IAM FK or SQL reference；
- duplicate inbox event causes one effective cleanup；
- inbox rollback on handler failure leaves no successful dedupe row；
- unsupported event version is not acknowledged。
