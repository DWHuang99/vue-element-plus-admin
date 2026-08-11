-- workflows.sql
-- Workflow-state queries for the Admin BFF workflow store adapter (T051).
-- admin_workflows is BFF-owned (data-model.md Ownership matrix); the store
-- never reads user/department/role facts from here.
--
-- Every mutation of a claimed workflow is CAS-guarded by
-- (operation_id, state IN ('running','compensating'), claim_token). The CAS
-- statements return the affected row count (`:one` over a count CTE) so the
-- adapter can surface a zero-row miss as ErrStaleClaim — an :exec command tag
-- cannot distinguish "no rows" from "success".

-- name: GetWorkflowByOperationID :one
SELECT operation_id, operation_type, idempotency_key, request_fingerprint,
       actor_user_id, subject_user_id, state, current_step, compensation_state,
       expected_iam_version, resulting_iam_version,
       previous_department_id, previous_membership_version,
       applied_department_id, applied_membership_version,
       result, last_error_code,
       created_at, updated_at, attempt_count,
       next_retry_at, client_input_deadline_at, retry_deadline_at,
       lease_owner, leased_until, claim_token, completed_at,
       command_fingerprint, compensation_attempt_count
FROM admin_workflows
WHERE operation_id = $1;

-- name: GetWorkflowByIdempotencyKey :one
-- Idempotency scope (operation_type, actor_user_id, idempotency_key) ->
-- one logical workflow (admin_workflows_idempotency_unique).
SELECT operation_id, operation_type, idempotency_key, request_fingerprint,
       actor_user_id, subject_user_id, state, current_step, compensation_state,
       expected_iam_version, resulting_iam_version,
       previous_department_id, previous_membership_version,
       applied_department_id, applied_membership_version,
       result, last_error_code,
       created_at, updated_at, attempt_count,
       next_retry_at, client_input_deadline_at, retry_deadline_at,
       lease_owner, leased_until, claim_token, completed_at,
       command_fingerprint, compensation_attempt_count
FROM admin_workflows
WHERE operation_type = $1 AND actor_user_id = $2 AND idempotency_key = $3;

-- name: CreateWorkflow :exec
-- C0/U0/D0: persist a pending workflow BEFORE any participant side effect.
-- retry_deadline_at is supplied by the caller (default creation +24h) and is
-- immutable afterwards; idempotency_key may be NULL for internal operations.
INSERT INTO admin_workflows (
    operation_id, operation_type, idempotency_key, request_fingerprint,
    actor_user_id, state, current_step, retry_deadline_at)
VALUES ($1, $2, $3, $4, $5, 'pending', 'created', $6);

-- name: ClaimWorkflows :many
-- Atomic claim scan (consistency-and-compensation.md lease/retry): leases
-- pending, eligible failed_retryable (next_retry_at due), expired
-- running/compensating (leased_until <= now()) and expired awaiting_client_input
-- (client_input_deadline_at <= now()) rows with owner/lease/fresh claim_token.
-- Forward claims (pending/failed_retryable/expired running) increment
-- attempt_count (capped at 10); expired-input claims transition directly to
-- compensating and do not count as forward attempts; compensation reclaims
-- increment compensation_attempt_count (capped at 10, same retry_deadline
-- ceiling) and never consume the forward budget — a compensation-exhausted or
-- past-deadline compensating row is not claimable (it falls to
-- ExhaustToFailedManual on the resume path).
UPDATE admin_workflows w
SET lease_owner = $1,
    leased_until = $2,
    claim_token = $3,
    state = CASE
        WHEN w.state IN ('pending', 'failed_retryable') THEN 'running'
        WHEN w.state = 'awaiting_client_input' THEN 'compensating'
        ELSE w.state END,
    compensation_state = CASE
        WHEN w.state = 'awaiting_client_input' THEN 'pending'
        ELSE w.compensation_state END,
    updated_at = now(),
    attempt_count = LEAST(w.attempt_count + CASE
        WHEN w.state IN ('pending', 'failed_retryable', 'running') THEN 1
        ELSE 0 END, 10),
    compensation_attempt_count = LEAST(w.compensation_attempt_count + CASE
        WHEN w.state = 'compensating' THEN 1
        ELSE 0 END, 10)
WHERE w.operation_id IN (
    SELECT c.operation_id
    FROM admin_workflows c
    WHERE (c.state = 'pending' AND c.attempt_count < 10 AND c.retry_deadline_at > now())
       OR (c.state = 'failed_retryable'
           AND (c.next_retry_at IS NULL OR c.next_retry_at <= now())
           AND c.attempt_count < 10 AND c.retry_deadline_at > now())
       OR (c.state = 'running' AND c.leased_until <= now())
       OR (c.state = 'compensating' AND c.leased_until <= now()
           AND c.compensation_attempt_count < 10 AND c.retry_deadline_at > now())
       OR (c.state = 'awaiting_client_input' AND c.client_input_deadline_at <= now())
    ORDER BY c.created_at
    LIMIT $4
    FOR UPDATE SKIP LOCKED
)
RETURNING operation_id, operation_type, idempotency_key, request_fingerprint,
          actor_user_id, subject_user_id, state, current_step, compensation_state,
          expected_iam_version, resulting_iam_version,
          previous_department_id, previous_membership_version,
          applied_department_id, applied_membership_version,
          result, last_error_code,
          created_at, updated_at, attempt_count,
          next_retry_at, client_input_deadline_at, retry_deadline_at,
          lease_owner, leased_until, claim_token, completed_at,
          command_fingerprint, compensation_attempt_count;

-- name: ClaimWorkflowByOperationID :one
-- Keyed claim (synchronous HTTP path, worker resume, expiry finalization):
-- one specific workflow transitions per the same predicates as
-- ClaimWorkflows — pending/failed_retryable -> running (forward attempt),
-- expired running -> running (reclaim; attempt capped at 10), expired
-- awaiting_client_input -> compensating (expiry finalization, no attempt),
-- expired compensating -> compensating (reclaim; compensation attempt capped
-- at 10, past-deadline rows not claimable — ExhaustToFailedManual covers
-- them). Zero rows = not claimable (live lease, exhausted, past retry
-- deadline, or absent) and surface as ErrStaleClaim; the caller re-reads the
-- workflow to classify.
UPDATE admin_workflows w
SET lease_owner = $2,
    leased_until = $3,
    claim_token = $4,
    state = CASE
        WHEN w.state IN ('pending', 'failed_retryable') THEN 'running'
        WHEN w.state = 'awaiting_client_input' THEN 'compensating'
        ELSE w.state END,
    compensation_state = CASE
        WHEN w.state = 'awaiting_client_input' THEN 'pending'
        ELSE w.compensation_state END,
    updated_at = now(),
    attempt_count = LEAST(w.attempt_count + CASE
        WHEN w.state IN ('pending', 'failed_retryable', 'running') THEN 1
        ELSE 0 END, 10),
    compensation_attempt_count = LEAST(w.compensation_attempt_count + CASE
        WHEN w.state = 'compensating' THEN 1
        ELSE 0 END, 10)
WHERE w.operation_id = $1
  AND (
    (w.state = 'pending' AND w.attempt_count < 10 AND w.retry_deadline_at > now())
    OR (w.state = 'failed_retryable'
        AND (w.next_retry_at IS NULL OR w.next_retry_at <= now())
        AND w.attempt_count < 10 AND w.retry_deadline_at > now())
    OR (w.state = 'running' AND w.leased_until <= now())
    OR (w.state = 'compensating' AND w.leased_until <= now()
        AND w.compensation_attempt_count < 10 AND w.retry_deadline_at > now())
    OR (w.state = 'awaiting_client_input' AND w.client_input_deadline_at <= now())
  )
RETURNING operation_id, operation_type, idempotency_key, request_fingerprint,
          actor_user_id, subject_user_id, state, current_step, compensation_state,
          expected_iam_version, resulting_iam_version,
          previous_department_id, previous_membership_version,
          applied_department_id, applied_membership_version,
          result, last_error_code,
          created_at, updated_at, attempt_count,
          next_retry_at, client_input_deadline_at, retry_deadline_at,
          lease_owner, leased_until, claim_token, completed_at,
          command_fingerprint, compensation_attempt_count;

-- name: ClaimAwaitingClientInput :one
-- Same-key authorized HTTP retry before the input deadline
-- (consistency-and-compensation.md): reacquires forward execution by
-- transitioning awaiting_client_input -> running with a fresh lease/token.
-- No attempt is counted until the participant command is actually attempted.
-- Zero rows (deadline passed, state moved on, or key mismatch) surface as
-- ErrStaleClaim and the service re-reads the workflow to classify.
UPDATE admin_workflows
SET state = 'running',
    lease_owner = $3,
    leased_until = $4,
    claim_token = $5,
    updated_at = now()
WHERE operation_id = $1
  AND idempotency_key = $2
  AND state = 'awaiting_client_input'
  AND client_input_deadline_at > now()
RETURNING operation_id, operation_type, idempotency_key, request_fingerprint,
          actor_user_id, subject_user_id, state, current_step, compensation_state,
          expected_iam_version, resulting_iam_version,
          previous_department_id, previous_membership_version,
          applied_department_id, applied_membership_version,
          result, last_error_code,
          created_at, updated_at, attempt_count,
          next_retry_at, client_input_deadline_at, retry_deadline_at,
          lease_owner, leased_until, claim_token, completed_at,
          command_fingerprint, compensation_attempt_count;

-- name: PersistStep :one
-- Durable step persistence (C4/C6/U5/U7/D5): only the current claim holder
-- may persist progress. Nil update fields keep the stored value (COALESCE),
-- so one statement records exactly the fields this step produced. Returns
-- the affected row count: 0 = stale claim.
WITH updated AS (
    UPDATE admin_workflows
    SET current_step = $3,
        subject_user_id = COALESCE($4, subject_user_id),
        expected_iam_version = COALESCE($5, expected_iam_version),
        resulting_iam_version = COALESCE($6, resulting_iam_version),
        previous_department_id = COALESCE($7, previous_department_id),
        previous_membership_version = COALESCE($8, previous_membership_version),
        applied_department_id = COALESCE($9, applied_department_id),
        applied_membership_version = COALESCE($10, applied_membership_version),
        command_fingerprint = COALESCE($11, command_fingerprint),
        updated_at = now()
    WHERE operation_id = $1 AND state IN ('running', 'compensating') AND claim_token = $2
    RETURNING operation_id
)
SELECT count(*) FROM updated;

-- name: BeginCompensation :one
-- The compensation decision is durable before any compensation side effect:
-- running -> compensating with compensation_state pending, lease kept.
-- Returns the affected row count: 0 = stale claim.
WITH updated AS (
    UPDATE admin_workflows
    SET state = 'compensating',
        compensation_state = 'pending',
        updated_at = now()
    WHERE operation_id = $1 AND state = 'running' AND claim_token = $2
    RETURNING operation_id
)
SELECT count(*) FROM updated;

-- name: SetCompensationSucceeded :one
-- The restore/compensation side effect committed: compensation_state
-- succeeded, lease kept until the workflow resolves. The outcome is final:
-- once recorded, neither outcome can be overwritten (0 rows = stale claim or
-- already-resolved compensation).
WITH updated AS (
    UPDATE admin_workflows
    SET compensation_state = 'succeeded',
        updated_at = now()
    WHERE operation_id = $1 AND state = 'compensating' AND claim_token = $2
      AND compensation_state IN ('pending', 'running')
    RETURNING operation_id
)
SELECT count(*) FROM updated;

-- name: SetCompensationFailed :one
-- The restore/compensation side effect failed or conflicted:
-- compensation_state failed (reconciliation follows via FailManual). Like
-- success, the outcome is final — once recorded, neither outcome can be
-- overwritten. 0 rows = stale claim or already-resolved compensation.
WITH updated AS (
    UPDATE admin_workflows
    SET compensation_state = 'failed',
        updated_at = now()
    WHERE operation_id = $1 AND state = 'compensating' AND claim_token = $2
      AND compensation_state IN ('pending', 'running')
    RETURNING operation_id
)
SELECT count(*) FROM updated;

-- name: EnterAwaitingClientInput :one
-- The required credential was not durably committed and is intentionally
-- absent from storage: awaiting_client_input with an immutable deadline
-- (default creation +24h), lease cleared. Waiting consumes no attempt budget;
-- before the deadline only a same-key authorized HTTP retry may continue.
-- 0 rows = stale claim.
WITH updated AS (
    UPDATE admin_workflows
    SET state = 'awaiting_client_input',
        client_input_deadline_at = $3,
        lease_owner = NULL,
        leased_until = NULL,
        claim_token = NULL,
        updated_at = now()
    WHERE operation_id = $1 AND state IN ('running', 'compensating') AND claim_token = $2
    RETURNING operation_id
)
SELECT count(*) FROM updated;

-- name: RescheduleRetryable :one
-- Bounded automatic retry: failed_retryable with next_retry_at, lease cleared.
-- 0 rows = stale claim.
WITH updated AS (
    UPDATE admin_workflows
    SET state = 'failed_retryable',
        next_retry_at = $3,
        last_error_code = $4,
        lease_owner = NULL,
        leased_until = NULL,
        claim_token = NULL,
        updated_at = now()
    WHERE operation_id = $1 AND state IN ('running', 'compensating') AND claim_token = $2
    RETURNING operation_id
)
SELECT count(*) FROM updated;

-- name: FailManual :one
-- Reconciliation: failed_manual, lease cleared, no automatic retry.
-- 0 rows = stale claim.
WITH updated AS (
    UPDATE admin_workflows
    SET state = 'failed_manual',
        last_error_code = $3,
        lease_owner = NULL,
        leased_until = NULL,
        claim_token = NULL,
        updated_at = now()
    WHERE operation_id = $1 AND state IN ('running', 'compensating') AND claim_token = $2
    RETURNING operation_id
)
SELECT count(*) FROM updated;

-- name: CompleteWorkflow :one
-- Terminal resolution (succeeded/rejected): safe result, lease cleared,
-- completed_at stamped. Compensation convergence (rejected/OPERATION_EXPIRED)
-- also releases subject exclusions — the service does that in the same
-- transaction via UpdateSubjectResult/ReleaseSubjectExclusion. 0 rows = stale.
WITH updated AS (
    UPDATE admin_workflows
    SET state = $3,
        result = $4,
        completed_at = now(),
        lease_owner = NULL,
        leased_until = NULL,
        claim_token = NULL,
        updated_at = now()
    WHERE operation_id = $1 AND state IN ('running', 'compensating') AND claim_token = $2
    RETURNING operation_id
)
SELECT count(*) FROM updated;

-- name: ExhaustToFailedManual :one
-- Automatic-budget exhaustion (10 attempts / original retry_deadline_at,
-- whichever comes first — forward or compensation counter) transitions a
-- non-live row to failed_manual so reconciliation can proceed; any
-- possibly-applied subject exclusion stays active (recovery decides).
-- Only rows without a live lease transition — a live owner still holds it.
-- 0 rows = not exhausted, live lease, or already terminal; the caller
-- re-reads the workflow to classify.
WITH updated AS (
    UPDATE admin_workflows
    SET state = 'failed_manual',
        last_error_code = $2,
        lease_owner = NULL,
        leased_until = NULL,
        claim_token = NULL,
        updated_at = now()
    WHERE operation_id = $1
      AND state IN ('pending', 'failed_retryable', 'running', 'compensating')
      AND (leased_until IS NULL OR leased_until <= now())
      AND (attempt_count >= 10 OR compensation_attempt_count >= 10 OR retry_deadline_at <= now())
    RETURNING operation_id
)
SELECT count(*) FROM updated;

-- name: CompleteRecoveredWorkflow :one
-- Manual-recovery terminalization of a failed_manual row: no claim token
-- exists there (FailManual clears it), so recovery completes directly with
-- the same terminal semantics. Recovery may only finalize proven receipts or
-- execute explicitly approved compensation — it never resumes forward
-- execution. 0 rows = not failed_manual (stale) or already terminal.
WITH updated AS (
    UPDATE admin_workflows
    SET state = $2,
        result = $3,
        completed_at = now(),
        updated_at = now()
    WHERE operation_id = $1 AND state = 'failed_manual'
    RETURNING operation_id
)
SELECT count(*) FROM updated;

-- name: CountWorkflowsByState :one
-- Metrics snapshot (T074): durable workflow rows in one state — failed
-- (retryable/manual) and compensating states drive the failure gauges.
SELECT count(*) AS count
FROM admin_workflows
WHERE state = $1;
