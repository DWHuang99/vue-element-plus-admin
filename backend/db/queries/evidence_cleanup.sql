-- evidence_cleanup.sql
-- Platform evidence-cleanup audit (T078): the immutable
-- platform_evidence_cleanup_audit table (migration 000013) is written only
-- through the two SECURITY DEFINER functions, so no runtime login can touch
-- the audit directly. Caller identity fields come from the trusted Platform
-- operations context, never from caller-supplied display text.

-- name: RecordCleanupAudit :one
SELECT public.record_cleanup_audit(
    $1::uuid, $2, $3, $4, $5, $6, $7::timestamptz, $8::timestamptz, $9::jsonb);

-- name: FinalizeCleanupAudit :one
SELECT public.finalize_cleanup_audit($1::uuid, $2, $3::jsonb, $4);
