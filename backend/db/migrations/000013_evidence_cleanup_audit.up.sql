-- 000013_evidence_cleanup_audit.up.sql
-- Platform evidence-cleanup audit (plan data-model.md "Retention and cleanup
-- matrix", T078). Destructive cleanup is gated on the rollback window being
-- closed, zero unresolved workflows/receipts/events, recorded parity, and
-- explicit operational approval; this table is the durable record of that
-- approval plus the dry-run counts the Platform coordinator evaluated and the
-- per-owner purged counts actually deleted.
--
-- The table is Platform-owned and written ONLY through the two SECURITY
-- DEFINER functions below — record_cleanup_audit (inserts an approved_pending
-- row with the approval identity, requested/effective cutoffs and dry-run
-- counts) and finalize_cleanup_audit (transitions it to succeeded/blocked/
-- failed with the purged counts or the block reason). Both are executable by
-- app_runtime alone (the coordinator is part of the deployed app) and never
-- by platform_operations or PUBLIC; like every other Platform control table
-- (compatibility_bridge_mode_changes, compatibility_rollout_gates) no runtime
-- login holds table grants, so the audit can only be read by the owner role.

CREATE TABLE platform_evidence_cleanup_audit (
    audit_id             UUID        PRIMARY KEY,
    approval_id          TEXT        NOT NULL,
    principal_id         TEXT        NOT NULL,
    authorization_source TEXT        NOT NULL,
    request_id           TEXT,
    correlation_id       TEXT,
    requested_cutoff     TIMESTAMPTZ NOT NULL,
    effective_cutoff     TIMESTAMPTZ NOT NULL,
    dry_run_counts       JSONB       NOT NULL,
    status               TEXT        NOT NULL,
    purged_counts        JSONB,
    blocked_reason       TEXT,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    finalized_at         TIMESTAMPTZ,
    CHECK (status IN ('approved_pending', 'succeeded', 'blocked', 'failed'))
);

ALTER TABLE platform_evidence_cleanup_audit OWNER TO platform_owner;

CREATE FUNCTION public.record_cleanup_audit(
    p_audit_id UUID,
    p_approval_id TEXT,
    p_principal_id TEXT,
    p_authorization_source TEXT,
    p_request_id TEXT,
    p_correlation_id TEXT,
    p_requested_cutoff TIMESTAMPTZ,
    p_effective_cutoff TIMESTAMPTZ,
    p_dry_run_counts JSONB
) RETURNS UUID
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    INSERT INTO public.platform_evidence_cleanup_audit (
        audit_id, approval_id, principal_id, authorization_source,
        request_id, correlation_id, requested_cutoff, effective_cutoff,
        dry_run_counts, status)
    VALUES (
        p_audit_id, p_approval_id, p_principal_id, p_authorization_source,
        p_request_id, p_correlation_id, p_requested_cutoff, p_effective_cutoff,
        p_dry_run_counts, 'approved_pending');
    RETURN p_audit_id;
END;
$$;

ALTER FUNCTION public.record_cleanup_audit(UUID, TEXT, TEXT, TEXT, TEXT, TEXT, TIMESTAMPTZ, TIMESTAMPTZ, JSONB)
    OWNER TO platform_owner;
REVOKE ALL ON FUNCTION public.record_cleanup_audit(UUID, TEXT, TEXT, TEXT, TEXT, TEXT, TIMESTAMPTZ, TIMESTAMPTZ, JSONB)
    FROM PUBLIC;
-- The deployed app's Platform coordinator records the approval before any
-- owner-local purge runs.
GRANT EXECUTE ON FUNCTION public.record_cleanup_audit(UUID, TEXT, TEXT, TEXT, TEXT, TEXT, TIMESTAMPTZ, TIMESTAMPTZ, JSONB)
    TO app_runtime;

CREATE FUNCTION public.finalize_cleanup_audit(
    p_audit_id UUID,
    p_status TEXT,
    p_purged_counts JSONB,
    p_blocked_reason TEXT
) RETURNS UUID
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    UPDATE public.platform_evidence_cleanup_audit
    SET status = p_status,
        purged_counts = p_purged_counts,
        blocked_reason = p_blocked_reason,
        finalized_at = now()
    WHERE audit_id = p_audit_id
      AND status = 'approved_pending';
    IF NOT FOUND THEN
        RAISE EXCEPTION 'cleanup_audit_not_pending'
            USING DETAIL = 'audit ' || p_audit_id || ' is not in approved_pending state';
    END IF;
    RETURN p_audit_id;
END;
$$;

ALTER FUNCTION public.finalize_cleanup_audit(UUID, TEXT, JSONB, TEXT)
    OWNER TO platform_owner;
REVOKE ALL ON FUNCTION public.finalize_cleanup_audit(UUID, TEXT, JSONB, TEXT)
    FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.finalize_cleanup_audit(UUID, TEXT, JSONB, TEXT)
    TO app_runtime;
