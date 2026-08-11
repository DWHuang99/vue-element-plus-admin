-- outbox.sql
-- Producer-side domain event enqueue (T048 DeleteUsers, T049 compensation):
-- the first real event is iam.user.deleted version 1. Rows commit in the same
-- IAM transaction as their side effect — rollback removes the event too.

-- name: InsertOutboxEvent :exec
-- Fresh pending event at delivery epoch 1, immediately available
-- (available_at = occurred_at = epoch_started_at satisfies the schema CHECK).
INSERT INTO iam_outbox_events (
    event_id, event_type, event_version, producer, aggregate_type, aggregate_id,
    aggregate_version, payload, correlation_id, occurred_at, status, available_at,
    delivery_epoch, epoch_started_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'pending', $10, 1, $10);

-- ==========================================================================
-- Delivery side (T061 OutboxDeliveryPort, contracts/domain-events.md §Outbox
-- delivery state). Every transition is a single atomic statement or one short
-- transaction — never read-then-write — and every ack/failure/block is a
-- claim-token CAS (stale tokens match no row and are rejected).

-- name: ListClaimableOutboxEvents :many
-- Eligible rows for one claim batch: pending rows whose backoff elapsed plus
-- leased rows whose lease expired. Rows are locked in the caller's short
-- transaction (FOR UPDATE SKIP LOCKED) so concurrent claimers skip them.
SELECT event_id, event_type, event_version, producer, aggregate_type, aggregate_id,
       aggregate_version, payload, correlation_id, occurred_at, status, available_at,
       delivery_epoch, epoch_started_at, attempt_count, total_attempt_count,
       lease_owner, leased_until, claim_token, last_error_code, blocked_at,
       blocked_reason_code, published_at, created_at
FROM iam_outbox_events
WHERE (status = 'pending' AND available_at <= now())
   OR (status = 'leased'  AND leased_until <= now())
ORDER BY available_at, created_at
LIMIT $1
FOR UPDATE SKIP LOCKED;

-- name: LeaseOutboxEvent :execrows
-- Atomically hand out a lease: fresh claim or expired-lease reclaim. Every
-- lease counts one attempt, so a worker crash after claim already consumed
-- budget. The row is locked by ListClaimableOutboxEvents; the status guard is
-- defensive.
UPDATE iam_outbox_events
SET status = 'leased',
    lease_owner = $2,
    leased_until = $3,
    claim_token = $4,
    attempt_count = attempt_count + 1,
    total_attempt_count = total_attempt_count + 1
WHERE event_id = $1
  AND status IN ('pending', 'leased');

-- name: BlockOutboxExhausted :execrows
-- Epoch ceiling hit at claim time (attempts >= 20 or epoch_started_at + 24h
-- past): atomically blocked WITHOUT a new lease and WITHOUT burning an
-- attempt — no consumer call is made for it.
UPDATE iam_outbox_events
SET status = 'blocked',
    blocked_at = now(),
    blocked_reason_code = 'delivery_exhausted',
    last_error_code = 'delivery_exhausted',
    lease_owner = NULL,
    leased_until = NULL,
    claim_token = NULL
WHERE event_id = $1
  AND status IN ('pending', 'leased');

-- name: AckOutboxEvent :execrows
-- Claim-token CAS to published; stale tokens match no row.
UPDATE iam_outbox_events
SET status = 'published',
    published_at = now(),
    lease_owner = NULL,
    leased_until = NULL,
    claim_token = NULL
WHERE event_id = $1
  AND status = 'leased'
  AND claim_token = $2;

-- name: RecordOutboxFailure :one
-- ONE statement atomically chooses pending/backoff or blocked(delivery_exhausted)
-- by evaluating the already-counted attempt/time ceiling; stale tokens match
-- no row. No reschedule-then-block race exists because the transition is a
-- single UPDATE, and the blocked branch keeps availability = epoch start so
-- the schema CHECK (available_at >= epoch_started_at) always holds.
UPDATE iam_outbox_events
SET status = CASE
        WHEN attempt_count >= 20 OR epoch_started_at + interval '24 hours' <= now()
            THEN 'blocked'
        ELSE 'pending'
    END,
    available_at = CASE
        WHEN attempt_count >= 20 OR epoch_started_at + interval '24 hours' <= now()
            THEN epoch_started_at
        ELSE $3
    END,
    blocked_at = CASE
        WHEN attempt_count >= 20 OR epoch_started_at + interval '24 hours' <= now()
            THEN now()
        ELSE NULL
    END,
    blocked_reason_code = CASE
        WHEN attempt_count >= 20 OR epoch_started_at + interval '24 hours' <= now()
            THEN 'delivery_exhausted'
        ELSE NULL
    END,
    last_error_code = $2,
    lease_owner = NULL,
    leased_until = NULL,
    claim_token = NULL
WHERE event_id = $1
  AND status = 'leased'
  AND claim_token = $4
RETURNING status;

-- name: BlockOutboxEvent :execrows
-- Non-retryable/unsupported contract → durable blocked, claim-token CAS.
UPDATE iam_outbox_events
SET status = 'blocked',
    blocked_at = now(),
    blocked_reason_code = $2,
    last_error_code = $2,
    lease_owner = NULL,
    leased_until = NULL,
    claim_token = NULL
WHERE event_id = $1
  AND status = 'leased'
  AND claim_token = $3;

-- name: GetOutboxEventByIDForRequeue :one
-- Locked blocked-row read for the requeue transaction; the caller bumps the
-- epoch and appends the immutable previous-epoch evidence atomically.
SELECT event_id, delivery_epoch, epoch_started_at, attempt_count,
       total_attempt_count, last_error_code, blocked_at, blocked_reason_code
FROM iam_outbox_events
WHERE event_id = $1
  AND status = 'blocked'
FOR UPDATE;

-- name: RequeueOutboxEvent :exec
-- Controlled requeue: pending again at a delayed available_at, new epoch
-- started at available_at, epoch attempts reset, total attempts preserved,
-- blocked/lease evidence cleared.
UPDATE iam_outbox_events
SET status = 'pending',
    available_at = $2,
    delivery_epoch = delivery_epoch + 1,
    epoch_started_at = $2,
    attempt_count = 0,
    lease_owner = NULL,
    leased_until = NULL,
    claim_token = NULL,
    blocked_at = NULL,
    blocked_reason_code = NULL,
    last_error_code = NULL
WHERE event_id = $1
  AND status = 'blocked';

-- name: InsertOutboxRequeue :exec
-- Immutable audit row: one per resulting epoch, complete previous-epoch
-- evidence + trusted recovery identity/authorization/approval/correlation.
INSERT INTO iam_outbox_requeues (
    event_id, delivery_epoch, recovery_principal_id, reason_code,
    previous_epoch_started_at, previous_epoch_deadline_at, previous_blocked_at,
    previous_blocked_reason, previous_last_error_code, previous_epoch_attempts,
    previous_total_attempts, authorization_source, approval_id, request_id,
    correlation_id
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15);

-- name: IncrementOutboxMetric :exec
-- Cumulative observability counter (T066): atomic delta upsert on the
-- IAM-owned metrics table. Never affects delivery semantics.
INSERT INTO iam_outbox_metrics(metric_key, count) VALUES ($1, $2)
ON CONFLICT (metric_key) DO UPDATE
    SET count = iam_outbox_metrics.count + EXCLUDED.count;

-- name: GetOutboxBacklog :one
-- Required observability summary (contracts/domain-events.md §Failure and
-- reconciliation): pending/leased/published/blocked counts, oldest pending
-- age, oldest live lease, and the cumulative lease-recovery and stale-claim-
-- rejection counters.
SELECT
    (SELECT count(*) FROM iam_outbox_events WHERE status = 'pending')   AS pending_count,
    (SELECT count(*) FROM iam_outbox_events WHERE status = 'leased')    AS leased_count,
    (SELECT count(*) FROM iam_outbox_events WHERE status = 'blocked')   AS blocked_count,
    (SELECT min(available_at)::timestamptz FROM iam_outbox_events WHERE status = 'pending')   AS oldest_pending_at,
    (SELECT count(*) FROM iam_outbox_events WHERE status = 'published') AS published_count,
    (SELECT min(leased_until)::timestamptz FROM iam_outbox_events WHERE status = 'leased')    AS oldest_leased_until,
    (SELECT count FROM iam_outbox_metrics WHERE metric_key = 'lease_recoveries')      AS lease_recovery_count,
    (SELECT count FROM iam_outbox_metrics WHERE metric_key = 'stale_claim_rejections') AS stale_claim_rejection_count;
