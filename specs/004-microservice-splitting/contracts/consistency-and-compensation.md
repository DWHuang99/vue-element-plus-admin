# Cross-Domain Consistency and Compensation Contract

**Feature**: `004-microservice-splitting`  
**Orchestrator**: Admin BFF  
**Participants**: IAM, Organization  
**Consistency model**: local ACID transactions + persisted saga/workflow + idempotent commands + outbox/inbox

## Global rules

1. Admin BFF MUST NOT call `Begin`, `Commit` or `Rollback` across IAM and Organization。
2. Each participant owns its local transaction and exposes typed outcome/idempotency semantics。
3. Every cross-domain command has a stable `operation_id`; public `Idempotency-Key` is optional for backward compatibility but official frontend sends/reuses it。
4. Each participant writes an owner-local command receipt in the same transaction as its side effect and exposes `ResolveCommand(operation_id, command_name, expected_fingerprint)`。
5. Workflow progress is durable before/after side-effect boundaries needed for recovery。
6. Password/raw token/hash are never stored in workflow/receipt state or fingerprints。
7. Timeout is an unknown outcome, not automatic failure；orchestrator resolves participant receipt before retry/compensation。
8. Compensation is idempotent, version-guarded and observable；failed/conflicting compensation moves workflow to reconciliation state。
9. Public HTTP never reports success until synchronous contract completion；asynchronous deletion cleanup may continue after IAM authoritative success as explicitly defined。

## Workflow identity

### Idempotency scope

```text
(operation_type, actor_user_id, idempotency_key) -> one logical workflow
```

- public key validation: 16–128 chars `[A-Za-z0-9._:-]+`; minimum replay retention 24 hours；
- same key + same safe request fingerprint returns/continues same workflow；
- same key + conflicting non-secret fields returns 409 `IDEMPOTENCY_CONFLICT`；
- running same workflow returns 409 `OPERATION_IN_PROGRESS` with `Retry-After`；
- password is excluded from stored fingerprint；before IAM credential write, a response-ending failure transitions the workflow to `awaiting_client_input` with immutable `client_input_deadline_at` (default workflow creation +24h)。Before the deadline only a same-key authenticated HTTP retry may supply transient credential input and reacquire forward execution；waiting consumes no automatic attempt budget。At/after the deadline a worker may claim only for expiry finalization/compensation, never to synthesize credential or continue forward；
- for one idempotency key, the credential that first successfully commits in the IAM receipt is authoritative。Official clients MUST reuse the original credential for an unknown-outcome retry；an intentional credential change requires a new key. After the receipt exists, replacement password material for the old operation is ignored and never compared/stored。

### Canonical participant command fingerprint v1

All BFF/participant adapters compute the same fingerprint:

```text
sha256(RFC8785_JCS(UTF-8 JSON object))
```

Canonical object contains `fingerprint_version: 1`, `command_name`, `actor_user_id`, and command-specific `args`. Rules: validated strings are encoded exactly with no later normalization; optional fields are explicit JSON null; integers are JSON numbers; IDs/role IDs/delete targets are deduplicated and sorted ascending; object keys use RFC 8785 ordering. Password/token/hash/password-derived bytes are excluded; only `password_change_requested: true|false` is included.

Command args:

- IAM create: username/account/email, sorted role IDs, `password_change_requested=true`；
- IAM activate/disable/compensate: user ID, expected version, safe reason where applicable；
- IAM update: user ID/version, username/account/email, sorted role IDs, password-change boolean；
- IAM delete: sorted `{user_id, expected_version}` targets；
- Organization set/clear/restore: user ID, department/null, expected current version/absence, and previous state fields for restore；
- department commands: normalized IDs/name/parent fields。

Workflow stores this fingerprint before calling a participant; `ResolveCommand` receives it and rejects mismatch. Any fingerprint schema change requires a new fingerprint version while old receipts remain resolvable.

### Terminal states

| State | Meaning | Client retry behavior |
|---|---|---|
| `succeeded` | contract completed | return same safe result, no new side effects |
| `rejected` | terminal validation/not-found/protection result | replay original safe HTTP error; corrected payload requires a new key |
| `awaiting_client_input` | required credential was not durably committed and is intentionally absent from storage | before input deadline only same-key authorized HTTP retry may continue；after deadline worker may claim only to reject/compensate, returning terminal 409 `OPERATION_EXPIRED` on safe convergence |
| `failed_retryable` | dependency/step may continue without missing client-only input | resume same operation |
| `failed_manual` | automatic compensation/recovery cannot safely continue | return safe failure + operation/request ID; alert operator; no automatic forward retry |
| `compensating` | recovery in progress | do not start duplicate workflow |

### Durable workflow execution lease/retry

- a worker atomically claims `pending`, eligible `failed_retryable`, `running AND leased_until <= now()`, `compensating AND leased_until <= now()`, or expired `awaiting_client_input AND client_input_deadline_at <= now()` workflows in a short BFF-store transaction, setting/replacing owner/lease/fresh `claim_token`。Normal/reclaim forward attempts increment `attempt_count`；expired-input claims transition directly to `compensating`/expiry-finalization and do not count as forward attempts；
- reclaiming expired `running` or `compensating` invalidates the old owner/token。Before any further participant side effect, the new owner resolves the command associated with the last persisted step/fingerprint; it may only finalize a proven receipt, retry a proven non-commit, or apply the normal compensation decision table。A reclaim of `compensating` MUST first resolve the receipt of the last compensation command (e.g. the version-CAS restore) before retrying or finalizing that compensation, and MUST NOT reset or consume the forward-attempt budget——compensation reclaim attempts are bounded by the same 10-attempt/24h ceiling but counted separately from forward attempts；
- participant calls occur outside the workflow-store transaction；step persist/complete/reschedule uses `(operation_id, state=running|compensating, claim_token)` CAS, so an expired/stale worker cannot overwrite a newer owner；state/lease checks require non-null owner/expiry/token only while `running`/`compensating` is actively claimed；
- before `client_input_deadline_at`, a same-key HTTP retry may claim `awaiting_client_input` only after current authentication/route authorization, supply credential in memory, create a fresh claim token and transition to `running`；it does not increment automatic attempts until the participant command is actually attempted；
- at/after input deadline: if no participant side effect exists, terminally `rejected` with 409 `OPERATION_EXPIRED`；if reversible Organization membership was applied, transition to `compensating` and restore by applied-version CAS。Successful restore ends `rejected`/`OPERATION_EXPIRED` and releases exclusion；conflict/failure ends `failed_manual`/`RECONCILIATION_REQUIRED` and keeps exclusion active；
- retry persists `next_retry_at`; HTTP `Retry-After` is `ceil(next_retry_at-now)` clamped 1–60, default 3 when a precise time is unavailable；
- automatic workflow execution is capped at 10 attempts and the original immutable `retry_deadline_at` (default 24h after creation), whichever occurs first. Exhaustion transitions to `failed_manual`/`RECONCILIATION_REQUIRED`; any possibly-applied subject exclusion remains active；
- this feature does not reset the automatic attempt budget or extend the deadline。Manual reconciliation cannot resume the revoked/failed actor's forward request；it appends an immutable `admin_workflow_recovery_actions` record and may only finalize proven receipts or execute explicitly approved compensation/reconciliation through least-privilege owner ports。

## Managed-user create

### Goal

Create IAM identity/credential/roles and optional Organization membership without making an incomplete account login-capable.

### Ordered steps

```text
C0 persist workflow(pending)
C1 IAM.ValidateRoleIDs
C2 Organization.ValidateDepartment (nullable allowed)
C3 IAM.CreateProvisioningUser(operation_id, ...) -> user_id/version + atomic receipt
C4 resolve receipt if timeout; persist subject_user_id/version + step=iam_provisioned
C5 Organization.Set/Clear(expected absence/version) -> resulting membership version + atomic receipt
C6 resolve receipt if timeout; persist applied membership version + step=organization_assigned
C7 IAM.ActivateUser(expected provisioning version) -> active version + atomic receipt
C8 resolve receipt if timeout; persist succeeded
```

### Local atomicity

- C3 atomically creates provisioning identity, password hash and role assignments。
- C5 atomically changes Organization membership。
- C7 atomically changes lifecycle to active。
- No shared transaction exists。

### Failure policy

| Failure point | Required action |
|---|---|
| C1/C2 validation | mark workflow terminal `rejected`, persist safe original error/completed_at；no domain writes；same key replays it。 |
| before/during C3 known rollback | while the authenticated request still holds the credential, bounded retry is allowed；if the response/request ends, persist `awaiting_client_input` (not worker-retryable)；no Organization write。 |
| C3 timeout unknown | `IAM.ResolveCommand`；do not issue unrelated duplicate create。 |
| C5 timeout/failure | `Organization.ResolveCommand`; if committed, compensate with returned applied version; then compensate IAM provisioning user at expected version；record receipts/results。 |
| C7 timeout unknown | `IAM.ResolveCommand` first. If activation committed, succeed; only a proven non-commit/terminal failure may start version-guarded compensation。 |
| C7 proven failure | clear/restore Organization only if applied version still current, then compensate IAM only if user still provisioning at expected version；conflict becomes `RECONCILIATION_REQUIRED`。 |
| BFF crash after any step | restart loads workflow and participant operation state, resumes next safe idempotent step。 |

### Safety invariant

Until C7 commits, user cannot authenticate and is excluded from normal user list. Therefore an Organization failure cannot expose a usable orphan account.

### Success response

Public API remains `200 {"data":{}}`; workflow may retain safe subject ID internally for replay but does not add a public field.

## Managed-user update

### Goal

Update IAM profile/password/roles and optional Organization membership while avoiding password reversal/storage.

### Ordered steps

```text
U0 persist workflow(pending)
U1 IAM validate user/roles; persist expected_iam_version
U2 Organization validate target department
U3 Organization read current versioned membership state (including null department tombstone); persist previous department/version or expected first absence
U4 Organization CAS set/clear; clear retains null-department state and increments version -> applied version + atomic receipt
U5 resolve receipt if timeout; persist applied version + step=organization_updated
U6 IAM.UpdateManagedUser(expected_iam_version) -> resulting version + atomic receipt
U7 resolve receipt if timeout; persist succeeded
```

### Rationale for ordering

Password changes cannot be safely compensated because old/new plaintext password must not be stored. Organization membership is reversible using only previous department ID, so it is applied first and restored if IAM fails.

### Failure policy

| Failure point | Required action |
|---|---|
| U1/U2 | no domain writes；persist terminal `rejected` with the existing safe public not-found/validation code for replay。 |
| U3 | retryable dependency failure；no writes。 |
| U4 known failure | IAM unchanged；return safe failure。If the client will retry a requested password change, persist `awaiting_client_input` rather than scheduling a worker。 |
| U4 timeout unknown | call `Organization.ResolveCommand`; current membership read alone cannot distinguish a later writer。If membership committed but the HTTP request ends before U6 credential command, persist `awaiting_client_input` with the applied membership version/exclusion。 |
| U6 known failure/non-commit | restore previous membership only with `expected_current_membership_version = applied_membership_version`；then return IAM error。 |
| U6 timeout unknown | call `IAM.ResolveCommand` before restoring；never assume password/profile update failed。 |
| restore failure | workflow `failed_manual`；alert with operation/user/department IDs only；do not claim full success。 |
| crash after U6 before U7 | resolve IAM operation; if committed, mark succeeded；do not restore Organization。 |

### Concurrency

- workflow uses expected versions or equivalent optimistic check for IAM user/membership where concurrent edits are possible。
- stale previous membership must not overwrite a newer independently committed update during compensation；version conflict escalates to `failed_manual`/reconciliation。
- `admin_workflow_subjects` contains every single/batch target and enforces partial unique `(subject_user_id) WHERE active`；participant CAS still protects against non-workflow/admin concurrency and null-state ABA。

## Managed-user delete

### Goal

Make IAM deletion authoritative immediately and reliably remove Organization membership asynchronously.

### Ordered steps

```text
D0 persist parent workflow with normalized requested IDs/fingerprint; no subject row/version is invented yet
D1 IAM full-batch existence/protection preflight returns current version for every target; any missing target -> parent `rejected`
D2 BFF workflow-store transaction inserts one active `admin_workflow_subjects` row per validated target/version; any unique conflict -> `OPERATION_IN_PROGRESS`
D3 IAM local transaction:
   - CAS validate all target versions again
   - delete all target users
   - cascade/clear IAM sessions and roles
   - insert one event_type=iam.user.deleted, event_version=1 outbox event per user
   - write one `(operation_id, delete_users)` batch receipt containing normalized per-user tombstone results
D4 commit and return BatchDeleteResult; timeout/crash requires ResolveCommand
D5 persist every subject tombstone result, set subject active=false, mark workflow succeeded and return 200
D6 dispatcher eventually delivers events
D7 Organization inbox idempotently removes memberships
```

### Semantics

- any D1/D2 preflight or exclusion error means zero users deleted。
- D3 failure rolls back users, receipt and outbox together。
- after D4 commit, user cannot authenticate and is absent from IAM-driven user list even if membership cleanup is pending。
- stale Organization membership MUST NOT cause deleted user to reappear: department-filtered candidate IDs are constrained through IAM, which returns no deleted identity。
- D5/D6 failures are retried/alerted and do not reverse authoritative IAM deletion。

### HTTP timeout

If client times out after D4 commit, BFF resolves the batch receipt, persists all per-subject tombstone versions, and retry with the same idempotency key returns the original succeeded workflow without duplicate events.

## Department and role batch deletes

These operations are single-domain but retain consistency guarantees:

- normalize IDs；
- full-batch existence/protection/reference preflight；
- one owner-local transaction；
- any failure means zero target rows deleted。

Built-in role protections remain IAM-local. Department child/membership protections remain Organization-local.

## Error precedence

When preflight can discover multiple failures, BFF uses deterministic order to keep contract tests stable:

1. request shape/field validation (`AUTH_INVALID_INPUT`)；
2. authentication (`AUTH_INVALID_TOKEN`) and current route authorization (`AUTH_FORBIDDEN`) occur before idempotency lookup/use case；a completed result is replayed only while the actor remains authorized for that route；
3. target entity not found；
4. referenced role/department not found；
5. immutable/delete protection/conflict；
6. infrastructure unavailable/timeout；
7. internal/compensation failure。

No compensation/infrastructure error is mapped to 401/403 unless authentication/authorization itself failed.

After preflight and immediately before **every new forward participant side effect at a durable step boundary**, BFF re-runs current IAM authorization for the original actor。This applies both inside the original HTTP request and after restart/worker recovery；one route-level check is not sufficient for a later participant command。Receipt lookup/finalization and approved compensation are not new forward side effects。

- no participant side effect yet: claim-token CAS to terminal `rejected`, clear lease/release subject exclusion, return/replay 403 `AUTH_FORBIDDEN`；
- one or more side effects applied but workflow incomplete: claim-token CAS to `compensating`; a least-privilege audited recovery principal may only reverse those applied effects. Successful compensation ends as `rejected`/403 and releases exclusion；conflict/failure ends `failed_manual`/500 `RECONCILIATION_REQUIRED` and keeps exclusion active；
- receipts prove every required business side effect committed before revocation, with only BFF marker missing: mark `succeeded` without a new participant side effect；
- recovery principal never progresses the revoked actor's forward business request or silently impersonates them；manual action is appended to `admin_workflow_recovery_actions`。

The same authorization precedence applies to idempotency replay: revoked actors receive current 403 even for a previously succeeded key；restoring permission permits the stored terminal result to be replayed, but does not reopen a workflow terminally `rejected` because revocation occurred before/in the middle of execution。

Fixed workflow/dependency public mappings are:

- 409 `IDEMPOTENCY_CONFLICT`；
- 409 `OPERATION_IN_PROGRESS` + `Retry-After`；
- 409 `OPERATION_EXPIRED` without `Retry-After`；
- 503 `DEPENDENCY_UNAVAILABLE`；
- 504 `DEPENDENCY_TIMEOUT`；
- 503 `WORKFLOW_RETRYABLE` + `Retry-After`；
- 500 `RECONCILIATION_REQUIRED`。

## Retry policy

| Error class | Automatic retry? | Notes |
|---|---|---|
| validation/not-found/conflict/protected | no | client must correct request |
| transient DB unavailable/deadlock | bounded | only idempotent operation ID |
| module timeout | resolve then bounded retry | timeout outcome unknown |
| unsupported event version | no hot retry | block/alert until compatible consumer |
| compensation transient failure | bounded + alert | retains durable workflow |
| failed_manual | operator/reconciliation | no blind loop |

Backoff must be bounded exponential with jitter or equivalent. Each outbox epoch is capped at 20 claimed attempts and its initial eligibility +24h before durable `blocked(delivery_exhausted)`；workflow retries are capped at 10 attempts/original 24h deadline and never tight-loop.

## Reconciliation

Required operational views/commands:

- workflows not terminal beyond threshold；
- provisioning users with no active workflow progress；
- active users whose requested membership workflow is incomplete；
- IAM deleted-user outbox events pending beyond threshold；
- Organization memberships for IAM IDs that no longer exist (using controlled BFF/ports comparison, not runtime cross-domain SQL)；
- Organization handler failures plus producer outbox leased/pending/blocked records；successful inbox rows are dedupe evidence only。

Reconciliation actions invoke owner application ports or controlled reviewed SQL. They do not create default credentials, accept plaintext password from workflow storage or modify seeded permission grants ad hoc.

Retention/cleanup follows the shared model: a durable Platform rollout-gate record proves 72 continuous healthy hours (any mismatch/monitoring gap resets the window), then 7-day rollback support；at least 30 days for terminal workflows/receipts/published outbox/successful inbox/recovery and requeue audits；no automatic purge of unresolved/pending/leased/blocked evidence. Platform cleanup coordinator uses BFF/IAM/Organization owner-specific dry-run watermarks and approved purge ports, chooses the most conservative cutoff, and performs no deletion on uncertainty；runtime cross-owner SQL is forbidden.

## Observability

For each workflow/step record:

- operation ID, idempotency key hash/safe representation, operation type；
- actor/subject numeric IDs；
- step start/end, latency, typed result；
- compensation attempt/outcome；
- request/trace correlation。

Never record password, token, password hash, full request body, database URL or raw SQL error values.

Metrics:

- workflow started/succeeded/failed_retryable/failed_manual；
- step duration/error by participant；
- compensation count/failure；
- provisioning age；
- deletion outbox backlog/oldest age；
- inbox duplicate/handler failure and outbox lease/blocked state。

## Failure-injection contract tests

Tests MUST inject failure/timeout/crash after each create/update/delete boundary and verify:

- no duplicate identity/membership/role assignment；
- provisioning user cannot login；
- update password is never stored for compensation；
- Organization restoration uses applied-version CAS and cannot overwrite a newer update；
- IAM update/activation/deletion use expected versions and receipt resolution；
- participant commit followed by BFF crash resolves from receipt using expected fingerprint without duplicate side effect；
- actor permission revoked between preflight/durable steps prevents the next forward side effect both within the original request and after recovery；completed replay is also preceded by current route authorization；
- expired `running` or `compensating` lease is reclaimed with a fresh token, stale persistence is rejected, and the last participant/compensation receipt is resolved before any next command；
- missing credential transitions to `awaiting_client_input`, consumes no forward attempts before deadline, and only same-key authorized HTTP retry can continue forward；deadline expiry rejects with no side effect or version-CAS compensates applied membership, with conflict becoming `failed_manual`；
- batch delete subject exclusion covers every target；
- activation timeout never triggers compensation before receipt resolution；
- delete batch remains atomic；
- every committed delete has one logical event；
- same idempotency key resumes/returns same workflow；
- compensation failure is durable and visible, never silently reported as success；
- no secret appears in workflow rows, logs or event payloads。
