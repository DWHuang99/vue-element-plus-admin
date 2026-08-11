-- evidence_cleanup.sql
-- Admin BFF cleanup watermark (data-model.md line 350): the BFF exposes its
-- oldest unresolved/referencing workflow watermark so the Platform coordinator
-- can refuse cleanup while any workflow still references evidence. A workflow
-- is "unresolved/referencing" while it is not terminally resolved OR still
-- holds an active subject exclusion (a possibly-applied effect awaiting
-- reconciliation). Receipts are never purged shorter than the workflows that
-- reference them; the zero-unresolved gate plus the shared 30-day completion
-- floor (a receipt's workflow is created in the same operation) uphold that.

-- name: WorkflowCleanupWatermark :one
SELECT
    (SELECT count(*) FROM admin_workflows w
      WHERE w.state NOT IN ('succeeded', 'rejected')
         OR EXISTS (SELECT 1 FROM admin_workflow_subjects s
                    WHERE s.operation_id = w.operation_id AND s.active)) AS unresolved_count,
    (SELECT min(w.updated_at)::timestamptz FROM admin_workflows w
      WHERE w.state NOT IN ('succeeded', 'rejected')
         OR EXISTS (SELECT 1 FROM admin_workflow_subjects s
                    WHERE s.operation_id = w.operation_id AND s.active)) AS oldest_unresolved_at,
    (SELECT min(w.created_at)::timestamptz FROM admin_workflows w) AS oldest_workflow_at;
