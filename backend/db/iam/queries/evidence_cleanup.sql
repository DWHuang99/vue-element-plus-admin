-- evidence_cleanup.sql
-- IAM owner-local evidence cleanup (data-model.md "Retention and cleanup
-- matrix", T078). Every predicate is conservative: rows are eligible only 30
-- calendar days after their terminal timestamp, published outbox rows are
-- held until 30 days after publish AND 30 days after the last requeue
-- (whichever is later), and nothing is ever deleted while any outbox event is
-- pending/leased/blocked. All timestamps are explicit; the caller's cutoff
-- drives the predicates row-by-row, so a too-early cutoff simply matches
-- nothing.

-- name: CountUnresolvedOutboxEvents :one
SELECT count(*) FROM iam_outbox_events
WHERE status IN ('pending', 'leased', 'blocked');

-- name: CountEligibleCommandReceipts :one
SELECT count(*) FROM iam_command_receipts
WHERE status IN ('succeeded', 'rejected')
  AND completed_at <= $1::timestamptz - interval '30 days';

-- name: CountEligiblePublishedOutboxEvents :one
-- A published event is eligible only when it is also past the last-requeue
-- retention: any requeue row younger than 30 days keeps the event (and all of
-- its requeues) protected.
SELECT count(*) FROM iam_outbox_events e
WHERE e.status = 'published'
  AND e.published_at <= $1::timestamptz - interval '30 days'
  AND NOT EXISTS (
      SELECT 1 FROM iam_outbox_requeues r
      WHERE r.event_id = e.event_id
        AND r.requeued_at > $1::timestamptz - interval '30 days');

-- name: CountEligibleOutboxRequeues :one
SELECT count(*) FROM iam_outbox_requeues r
JOIN iam_outbox_events e USING (event_id)
WHERE e.status = 'published'
  AND e.published_at <= $1::timestamptz - interval '30 days'
  AND r.requeued_at <= $1::timestamptz - interval '30 days';

-- name: OldestReplayableEvidenceAt :one
-- The earliest retained IAM terminal timestamps (oldest receipt completion and
-- oldest publish, each NULL when absent). The adapter computes the replay
-- floor as min(these) + 30 days — the earliest cutoff at which any IAM
-- evidence becomes eligible. Requeues are covered by their event's
-- published_at (requeued_at < final published_at for a published event), so
-- only receipts and published events contribute to the floor.
SELECT
    (SELECT min(completed_at)::timestamptz FROM iam_command_receipts
      WHERE status IN ('succeeded', 'rejected')) AS oldest_receipt_completed_at,
    (SELECT min(published_at)::timestamptz FROM iam_outbox_events
      WHERE status = 'published') AS oldest_published_at;

-- name: PurgeEligibleOutboxRequeues :execrows
-- Requeues go first so the FK into iam_outbox_events never blocks the event
-- deletion below; every requeue of an eligible published event is itself
-- eligible because requeued_at < final published_at.
DELETE FROM iam_outbox_requeues r
USING iam_outbox_events e
WHERE r.event_id = e.event_id
  AND e.status = 'published'
  AND e.published_at <= $1::timestamptz - interval '30 days'
  AND r.requeued_at <= $1::timestamptz - interval '30 days';

-- name: PurgeEligiblePublishedOutboxEvents :execrows
DELETE FROM iam_outbox_events e
WHERE e.status = 'published'
  AND e.published_at <= $1::timestamptz - interval '30 days'
  AND NOT EXISTS (
      SELECT 1 FROM iam_outbox_requeues r
      WHERE r.event_id = e.event_id
        AND r.requeued_at > $1::timestamptz - interval '30 days');

-- name: PurgeEligibleCommandReceipts :execrows
DELETE FROM iam_command_receipts
WHERE status IN ('succeeded', 'rejected')
  AND completed_at <= $1::timestamptz - interval '30 days';
