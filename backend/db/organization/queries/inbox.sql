-- inbox.sql
-- Terminal IAM user-deleted consumption (US2, contracts/organization-application.md).
-- The service deletes the membership state rows and inserts the successful
-- dedupe row in one Organization transaction; a handler failure rolls both
-- back. Never references IAM tables.

-- name: DeleteMembershipsForUsers :exec
-- Full-batch physical cleanup (terminal deletion path); absent rows are a
-- normal no-op.
DELETE FROM organization_user_departments
WHERE user_id = ANY($1::bigint[]);

-- name: GetInboxMessage :one
-- Dedupe check for (event_id, handler_name). Missing = not yet processed.
SELECT event_id, handler_name, event_type, event_version, aggregate_id,
       aggregate_version, received_at, processed_at
FROM organization_inbox_messages
WHERE event_id = $1 AND handler_name = $2;

-- name: InsertInboxMessage :exec
-- Successful-processing evidence; ON CONFLICT DO NOTHING is belt-and-braces
-- for a racing redelivery, never an overwrite.
INSERT INTO organization_inbox_messages (
    event_id, handler_name, event_type, event_version, aggregate_id,
    aggregate_version, received_at, processed_at)
VALUES ($1, $2, $3, $4, $5, $6, now(), now())
ON CONFLICT (event_id, handler_name) DO NOTHING;

-- name: IncrementInboxMetric :exec
-- Cumulative inbox observability counter (T066): processed / duplicates /
-- handler-failure outcomes. Atomic delta upsert on the Organization-owned
-- metrics table; never affects handler semantics.
INSERT INTO organization_inbox_metrics(metric_key, count) VALUES ($1, $2)
ON CONFLICT (metric_key) DO UPDATE
    SET count = organization_inbox_metrics.count + EXCLUDED.count;

-- name: GetInboxMetrics :many
-- Read the Organization-owned cumulative inbox counters (T066 table) for
-- metrics exposition (T074). Key-value rows; absent keys simply don't exist.
SELECT metric_key, count
FROM organization_inbox_metrics
ORDER BY metric_key;
