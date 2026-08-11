-- 000012_rollout_gate_functions.up.sql
-- Platform rollout-gate evidence functions (plan Phase 5.9 step 6, T077).
-- compatibility_rollout_gates already exists (000008) with its grants; this
-- migration adds the two SECURITY DEFINER functions that own the append-only
-- window math so no runtime login can write the evidence table directly:
--
--   * record_rollout_gate_sample — the deployed app's writer loop calls it
--     once per sample cadence (GRANT EXECUTE to app_runtime). It appends one
--     evidence row per sample and enforces the window policy: any mismatch
--     (cumulative counter increase) or missing sample (interval beyond the
--     gap tolerance) closes the current record and resets the 72h window;
--     a passing sample extends the window (the new row keeps the window's
--     observation_started_at). observed_through_at is the closure stamp
--     stamped by these functions; evidence rows are never deleted or
--     rewritten.
--   * approve_route_disable — restricted to platform_operations (a
--     short-lived authenticated Platform operation): records the approval
--     row only after one complete continuous passing 72h window. The open
--     window condition IS the completeness criterion: any mismatch or gap
--     would have closed the window and restarted observation_started_at, so
--     an open record whose window started >= 72h ago proves continuous
--     passing. Caller identity fields come from the trusted Platform
--     operations context, never from caller-supplied display text.

CREATE FUNCTION public.record_rollout_gate_sample(
    p_phase TEXT,
    p_capability_manifest_hash TEXT,
    p_bridge_mode_version BIGINT,
    p_bridge_legacy_delete_sync_enabled BOOLEAN,
    p_legacy_rows BIGINT,
    p_new_rows BIGINT,
    p_row_version_checksum TEXT,
    p_mismatch_count BIGINT,
    p_last_mismatch_at TIMESTAMPTZ,
    p_rollback_artifact_id TEXT,
    p_rollback_suite_result TEXT,
    p_principal_id TEXT,
    p_observed_at TIMESTAMPTZ,
    p_max_gap_interval INTERVAL
) RETURNS BIGINT
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    -- Schema-qualified: the function's SET search_path = pg_catalog also
    -- applies at compile time (check_function_bodies), so unqualified
    -- %ROWTYPE references would not resolve.
    v_prev public.compatibility_rollout_gates%ROWTYPE;
    v_window_start TIMESTAMPTZ;
    v_gap BOOLEAN;
    v_mismatch BOOLEAN;
    v_result BIGINT;
BEGIN
    SELECT * INTO v_prev
    FROM public.compatibility_rollout_gates
    WHERE phase = p_phase
    ORDER BY gate_id DESC
    LIMIT 1
    FOR UPDATE;

    -- Missing-sample detection: the interval since the previous record
    -- exceeds the tolerance.
    v_gap := v_prev.gate_id IS NOT NULL
             AND (p_observed_at - v_prev.created_at) > p_max_gap_interval;

    IF v_prev.gate_id IS NULL OR v_prev.observed_through_at IS NOT NULL THEN
        -- No open window (first sample, or the previous window ended with an
        -- approval record): a fresh observation window starts now.
        v_window_start := p_observed_at;
        v_mismatch := false;
    ELSE
        -- A mismatch means the cumulative mismatch counter increased since
        -- the previous sample, or a newer last-mismatch timestamp appeared
        -- (the writer surfaces capability-manifest/bridge-state changes by
        -- recording them as a mismatch — the SQL authority stays minimal).
        v_mismatch := p_mismatch_count > v_prev.mismatch_count
            OR (p_last_mismatch_at IS NOT NULL AND (v_prev.last_mismatch_at IS NULL
                OR p_last_mismatch_at > v_prev.last_mismatch_at));

        IF v_gap OR v_mismatch THEN
            -- Any mismatch/missing sample closes the current record and
            -- resets the 72h window: the new sample starts a fresh window.
            UPDATE public.compatibility_rollout_gates
            SET observed_through_at = p_observed_at
            WHERE gate_id = v_prev.gate_id;
            v_window_start := p_observed_at;
        ELSE
            -- Passing sample: the window continues unchanged.
            v_window_start := v_prev.observation_started_at;
        END IF;
    END IF;

    INSERT INTO public.compatibility_rollout_gates (
        phase, capability_manifest_hash, bridge_mode_version,
        bridge_legacy_delete_sync_enabled, observation_started_at,
        monitoring_gap, legacy_rows, new_rows, row_version_checksum,
        mismatch_count, last_mismatch_at, rollback_artifact_id,
        rollback_suite_result, approving_principal_id)
    VALUES (
        p_phase, p_capability_manifest_hash, p_bridge_mode_version,
        p_bridge_legacy_delete_sync_enabled, v_window_start,
        v_gap, p_legacy_rows, p_new_rows, p_row_version_checksum,
        p_mismatch_count, p_last_mismatch_at, p_rollback_artifact_id,
        p_rollback_suite_result, p_principal_id)
    RETURNING gate_id INTO v_result;

    RETURN v_result;
END;
$$;

ALTER FUNCTION public.record_rollout_gate_sample(TEXT, TEXT, BIGINT, BOOLEAN, BIGINT, BIGINT, TEXT, BIGINT, TIMESTAMPTZ, TEXT, TEXT, TEXT, TIMESTAMPTZ, INTERVAL)
    OWNER TO platform_owner;
REVOKE ALL ON FUNCTION public.record_rollout_gate_sample(TEXT, TEXT, BIGINT, BOOLEAN, BIGINT, BIGINT, TEXT, BIGINT, TIMESTAMPTZ, TEXT, TEXT, TEXT, TIMESTAMPTZ, INTERVAL)
    FROM PUBLIC;
-- The deployed app's writer loop appends the cadence samples.
GRANT EXECUTE ON FUNCTION public.record_rollout_gate_sample(TEXT, TEXT, BIGINT, BOOLEAN, BIGINT, BIGINT, TEXT, BIGINT, TIMESTAMPTZ, TEXT, TEXT, TEXT, TIMESTAMPTZ, INTERVAL)
    TO app_runtime;

CREATE FUNCTION public.approve_route_disable(
    p_phase TEXT,
    p_approval_id TEXT,
    p_principal_id TEXT,
    p_observed_at TIMESTAMPTZ
) RETURNS BIGINT
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    v_prev public.compatibility_rollout_gates%ROWTYPE;
    v_result BIGINT;
BEGIN
    SELECT * INTO v_prev
    FROM public.compatibility_rollout_gates
    WHERE phase = p_phase
    ORDER BY gate_id DESC
    LIMIT 1
    FOR UPDATE;

    IF v_prev.gate_id IS NULL OR v_prev.observed_through_at IS NOT NULL THEN
        RAISE EXCEPTION 'rollout_gate_no_open_window'
            USING DETAIL = 'phase ' || p_phase || ' has no open passing window';
    END IF;

    -- Defensive invariant: an open window head can never carry a gap (a gap
    -- would have closed it); assert it anyway so a corrupted row cannot be
    -- approved silently.
    IF v_prev.monitoring_gap THEN
        RAISE EXCEPTION 'rollout_gate_window_failed'
            USING DETAIL = 'open window for phase ' || p_phase || ' carries a monitoring gap';
    END IF;

    IF v_prev.observation_started_at + INTERVAL '72 hours' > p_observed_at THEN
        RAISE EXCEPTION 'rollout_gate_window_incomplete'
            USING DETAIL = 'continuous passing window for phase ' || p_phase || ' has not reached 72 hours';
    END IF;

    -- Terminal approval record: closes the window (observed_through_at) and
    -- carries the window head's evidence plus the approval identity.
    INSERT INTO public.compatibility_rollout_gates (
        phase, capability_manifest_hash, bridge_mode_version,
        bridge_legacy_delete_sync_enabled, observation_started_at,
        observed_through_at, monitoring_gap, legacy_rows, new_rows,
        row_version_checksum, mismatch_count, last_mismatch_at,
        rollback_artifact_id, rollback_suite_result,
        approving_principal_id, approval_id)
    SELECT p_phase, v_prev.capability_manifest_hash, v_prev.bridge_mode_version,
           v_prev.bridge_legacy_delete_sync_enabled, v_prev.observation_started_at,
           p_observed_at, v_prev.monitoring_gap, v_prev.legacy_rows,
           v_prev.new_rows, v_prev.row_version_checksum, v_prev.mismatch_count,
           v_prev.last_mismatch_at, v_prev.rollback_artifact_id,
           v_prev.rollback_suite_result, p_principal_id, p_approval_id
    RETURNING gate_id INTO v_result;

    RETURN v_result;
END;
$$;

ALTER FUNCTION public.approve_route_disable(TEXT, TEXT, TEXT, TIMESTAMPTZ)
    OWNER TO platform_owner;
REVOKE ALL ON FUNCTION public.approve_route_disable(TEXT, TEXT, TEXT, TIMESTAMPTZ)
    FROM PUBLIC;
-- Route-disable approval is a short-lived authenticated Platform operation.
GRANT EXECUTE ON FUNCTION public.approve_route_disable(TEXT, TEXT, TEXT, TIMESTAMPTZ)
    TO platform_operations;
