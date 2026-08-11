-- evidence_cleanup.sql
-- Organization owner-local evidence cleanup (data-model.md "Retention and
-- cleanup matrix", T078). Organization evidence is always terminal: receipts
-- are written in the succeeded/rejected state only (schema CHECK) and inbox
-- dedupe rows exist only after successful processing — so there is no
-- unresolved Organization evidence to block a purge. The inbox minimum is
-- "at least as long as the producer outbox can be replayed"; the 30-day
-- processing floor plus the Platform coordinator's most-conservative-cutoff
-- cross-owner agreement uphold that without any cross-owner SQL.

-- name: CountEligibleCommandReceipts :one
SELECT count(*) FROM organization_command_receipts
WHERE status IN ('succeeded', 'rejected')
  AND completed_at <= $1::timestamptz - interval '30 days';

-- name: CountEligibleInboxMessages :one
SELECT count(*) FROM organization_inbox_messages
WHERE processed_at <= $1::timestamptz - interval '30 days';

-- name: OldestReplayableEvidenceAt :one
-- The earliest retained Organization terminal timestamps (oldest receipt
-- completion and oldest inbox processing, each NULL when absent). The adapter
-- computes the replay floor as min(these) + 30 days — the earliest cutoff at
-- which any Organization evidence becomes eligible.
SELECT
    (SELECT min(completed_at)::timestamptz FROM organization_command_receipts
      WHERE status IN ('succeeded', 'rejected')) AS oldest_receipt_completed_at,
    (SELECT min(processed_at)::timestamptz FROM organization_inbox_messages) AS oldest_processed_at;

-- name: PurgeEligibleCommandReceipts :execrows
DELETE FROM organization_command_receipts
WHERE status IN ('succeeded', 'rejected')
  AND completed_at <= $1::timestamptz - interval '30 days';

-- name: PurgeEligibleInboxMessages :execrows
DELETE FROM organization_inbox_messages
WHERE processed_at <= $1::timestamptz - interval '30 days';
