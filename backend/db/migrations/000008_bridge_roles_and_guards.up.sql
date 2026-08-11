-- 000008_bridge_roles_and_guards.up.sql
-- Owner/role matrix + compatibility bridge (plan Phase 2, T013;
-- data-model.md "Compatibility bridge control and PostgreSQL role matrix").
--
--   * NOLOGIN owner roles own domain objects; never granted to runtime logins.
--   * SECURITY DEFINER bridge functions (owned by compatibility_bridge_owner)
--     keep legacy users columns and Organization state in sync during the
--     compatibility window, with a recursion guard + distinct-value checks.
--   * compatibility_bridge_mode is a single Platform-owned row; the users
--     DELETE trigger stays installed and reads it: legacy-direct deletes
--     physically remove Organization state only while legacy_delete_sync_enabled.
--
-- admin_workflows* are held by platform_owner until a dedicated BFF store
-- adapter role is introduced (plan Phase 5); this keeps every domain table
-- under a NOLOGIN owner so no runtime login has ALTER/trigger capability.

-- ============================ 1. Owner roles ==============================
-- Roles are cluster-global: multiple databases may share one cluster, and a
-- migration run on any of them must not fail because another database already
-- created the role. PostgreSQL has no CREATE ROLE IF NOT EXISTS, so every
-- role is guarded by a pg_roles check inside a DO block.

DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'iam_owner') THEN
        CREATE ROLE iam_owner NOLOGIN;
    END IF;
END
$$;

DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'organization_owner') THEN
        CREATE ROLE organization_owner NOLOGIN;
    END IF;
END
$$;

DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'platform_owner') THEN
        CREATE ROLE platform_owner NOLOGIN;
    END IF;
END
$$;

DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'compatibility_bridge_owner') THEN
        CREATE ROLE compatibility_bridge_owner NOLOGIN;
    END IF;
END
$$;

-- Runtime login baseline: schema USAGE + minimum table DML/sequences only.
-- No password here — credentials are deployment-supplied, never a migration.
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'app_runtime') THEN
        CREATE ROLE app_runtime LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE;
    END IF;
END
$$;

-- Short-lived Platform migrator/operations role: SET ROLE only during
-- approved migration / bridge-mode operation (data-model.md role matrix).
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'platform_operations') THEN
        CREATE ROLE platform_operations LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE;
    END IF;
END
$$;

REVOKE ALL ON SCHEMA public FROM PUBLIC;
-- Owner roles need schema USAGE to act as SECURITY DEFINER function owners
-- (bridge functions run as compatibility_bridge_owner; the mode-change
-- function runs as platform_owner). NOLOGIN roles cannot log in regardless.
GRANT USAGE ON SCHEMA public TO iam_owner, organization_owner,
                              platform_owner, compatibility_bridge_owner;
GRANT USAGE ON SCHEMA public TO app_runtime;
GRANT USAGE ON SCHEMA public TO platform_operations;

-- ============================ 2. Table ownership ==========================

ALTER TABLE users OWNER TO iam_owner;
ALTER TABLE sessions OWNER TO iam_owner;
ALTER TABLE roles OWNER TO iam_owner;
ALTER TABLE user_roles OWNER TO iam_owner;
ALTER TABLE permissions OWNER TO iam_owner;
ALTER TABLE role_permissions OWNER TO iam_owner;
ALTER TABLE iam_command_receipts OWNER TO iam_owner;
ALTER TABLE iam_outbox_events OWNER TO iam_owner;
ALTER TABLE iam_outbox_requeues OWNER TO iam_owner;

ALTER TABLE departments OWNER TO organization_owner;
ALTER TABLE organization_user_departments OWNER TO organization_owner;
ALTER TABLE organization_command_receipts OWNER TO organization_owner;
ALTER TABLE organization_inbox_messages OWNER TO organization_owner;

ALTER TABLE admin_workflows OWNER TO platform_owner;
ALTER TABLE admin_workflow_subjects OWNER TO platform_owner;
ALTER TABLE admin_workflow_recovery_actions OWNER TO platform_owner;

-- schema_migrations is created lazily by the migrator on its first run, so it
-- may not exist yet when this migration executes; hand ownership over when it
-- does. The migrator/operations role keeps writing the version history.
DO $$
BEGIN
    IF pg_catalog.to_regclass('public.schema_migrations') IS NOT NULL THEN
        EXECUTE 'ALTER TABLE public.schema_migrations OWNER TO platform_owner';
    END IF;
END
$$;

ALTER SEQUENCE users_id_seq OWNER TO iam_owner;
ALTER SEQUENCE sessions_id_seq OWNER TO iam_owner;
ALTER SEQUENCE roles_id_seq OWNER TO iam_owner;
ALTER SEQUENCE permissions_id_seq OWNER TO iam_owner;
ALTER SEQUENCE departments_id_seq OWNER TO organization_owner;

-- ==================== 3. Runtime grants (minimum DML) =====================

GRANT SELECT, INSERT, UPDATE, DELETE ON
    users, sessions, roles, user_roles, permissions, role_permissions,
    iam_command_receipts, iam_outbox_events, iam_outbox_requeues,
    departments, organization_user_departments,
    organization_command_receipts, organization_inbox_messages,
    admin_workflows, admin_workflow_subjects, admin_workflow_recovery_actions
    TO app_runtime;

GRANT USAGE, SELECT ON SEQUENCE
    users_id_seq, sessions_id_seq, roles_id_seq, permissions_id_seq, departments_id_seq
    TO app_runtime;

-- ======================= 4. Platform control tables =======================

-- Single-row mode: legacy DELETE bridge reads it; controlled mode changes
-- go through set_legacy_delete_sync_mode (atomic with audit row).
CREATE TABLE compatibility_bridge_mode (
    id                        INT         PRIMARY KEY CHECK (id = 1),
    legacy_delete_sync_enabled BOOLEAN    NOT NULL,
    version                   BIGINT      NOT NULL,
    updated_at                TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE compatibility_bridge_mode OWNER TO platform_owner;

INSERT INTO compatibility_bridge_mode (id, legacy_delete_sync_enabled, version)
VALUES (1, true, 1);

-- Immutable audit trail for every mode transition.
CREATE TABLE compatibility_bridge_mode_changes (
    change_id                    BIGSERIAL   PRIMARY KEY,
    principal_id                 TEXT        NOT NULL,
    authorization_source         TEXT        NOT NULL,
    approval_id                  TEXT,
    reason_code                  TEXT        NOT NULL,
    previous_version             BIGINT      NOT NULL,
    previous_legacy_delete_sync_enabled BOOLEAN NOT NULL,
    new_version                  BIGINT      NOT NULL,
    new_legacy_delete_sync_enabled BOOLEAN   NOT NULL,
    request_id                   TEXT,
    correlation_id               TEXT,
    changed_at                   TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE compatibility_bridge_mode_changes OWNER TO platform_owner;

-- Append-only rollout evidence; a mismatch/monitoring gap closes the current
-- record and resets the 72h window (data-model.md).
CREATE TABLE compatibility_rollout_gates (
    gate_id                       BIGSERIAL   PRIMARY KEY,
    phase                         TEXT        NOT NULL,
    capability_manifest_hash      TEXT        NOT NULL,
    bridge_mode_version           BIGINT      NOT NULL,
    bridge_legacy_delete_sync_enabled BOOLEAN NOT NULL,
    observation_started_at        TIMESTAMPTZ NOT NULL,
    observed_through_at           TIMESTAMPTZ,
    monitoring_gap                BOOLEAN     NOT NULL DEFAULT false,
    legacy_rows                   BIGINT      NOT NULL,
    new_rows                      BIGINT      NOT NULL,
    row_version_checksum          TEXT,
    mismatch_count                BIGINT      NOT NULL DEFAULT 0,
    last_mismatch_at              TIMESTAMPTZ,
    rollback_artifact_id          TEXT,
    rollback_suite_result         TEXT,
    approving_principal_id        TEXT        NOT NULL,
    approval_id                   TEXT,
    created_at                    TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE compatibility_rollout_gates OWNER TO platform_owner;

REVOKE ALL ON compatibility_bridge_mode,
              compatibility_bridge_mode_changes,
              compatibility_rollout_gates FROM PUBLIC;

-- Owned sequences of the control tables follow the table owner (BIGSERIAL
-- sequences do not change owner with ALTER TABLE ... OWNER, so transfer
-- explicitly alongside the platform_owner ALTERs above).
ALTER SEQUENCE compatibility_bridge_mode_changes_change_id_seq OWNER TO platform_owner;
ALTER SEQUENCE compatibility_rollout_gates_gate_id_seq OWNER TO platform_owner;

-- Bridge functions read the mode; ops writes gates and reads audit.
GRANT SELECT ON compatibility_bridge_mode TO compatibility_bridge_owner;
GRANT SELECT, INSERT ON compatibility_rollout_gates TO platform_operations;
GRANT SELECT ON compatibility_bridge_mode_changes TO platform_operations;
GRANT USAGE, SELECT ON SEQUENCE
    compatibility_rollout_gates_gate_id_seq TO platform_operations;

-- ================== 5. Controlled mode-change function ====================

-- Restricted CAS switch: expected_version guards concurrency; an idempotent
-- replay whose target is already achieved succeeds; anything else raises.
-- Caller identity fields come from the trusted Platform operations context,
-- never from caller-supplied display text. REVOKE'd from PUBLIC; only
-- platform_operations (or an owner) may execute.
CREATE FUNCTION public.set_legacy_delete_sync_mode(
    p_expected_version BIGINT,
    p_enabled BOOLEAN,
    p_principal_id TEXT,
    p_authorization_source TEXT,
    p_approval_id TEXT,
    p_reason_code TEXT,
    p_request_id TEXT,
    p_correlation_id TEXT
) RETURNS BIGINT
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    -- Must be schema-qualified: the function's SET search_path = pg_catalog
    -- also applies at compile time (check_function_bodies), so unqualified
    -- %ROWTYPE references would not resolve.
    v_cur public.compatibility_bridge_mode%ROWTYPE;
BEGIN
    SELECT * INTO v_cur
    FROM public.compatibility_bridge_mode
    WHERE id = 1
    FOR UPDATE;

    IF v_cur.version = p_expected_version THEN
        UPDATE public.compatibility_bridge_mode
        SET legacy_delete_sync_enabled = p_enabled,
            version = v_cur.version + 1,
            updated_at = pg_catalog.now()
        WHERE id = 1;

        INSERT INTO public.compatibility_bridge_mode_changes (
            principal_id, authorization_source, approval_id, reason_code,
            previous_version, previous_legacy_delete_sync_enabled,
            new_version, new_legacy_delete_sync_enabled, request_id, correlation_id)
        VALUES (
            p_principal_id, p_authorization_source, p_approval_id, p_reason_code,
            v_cur.version, v_cur.legacy_delete_sync_enabled,
            v_cur.version + 1, p_enabled, p_request_id, p_correlation_id);

        RETURN v_cur.version + 1;
    ELSIF v_cur.legacy_delete_sync_enabled = p_enabled THEN
        -- Idempotent replay: target already achieved at a later version.
        RETURN v_cur.version;
    END IF;

    RAISE EXCEPTION 'legacy_delete_sync_mode_version_conflict'
        USING DETAIL = 'expected ' || p_expected_version
            || ', current version ' || v_cur.version
            || ', enabled ' || v_cur.legacy_delete_sync_enabled;
END;
$$;

ALTER FUNCTION public.set_legacy_delete_sync_mode(BIGINT, BOOLEAN, TEXT, TEXT, TEXT, TEXT, TEXT, TEXT)
    OWNER TO platform_owner;
REVOKE ALL ON FUNCTION public.set_legacy_delete_sync_mode(BIGINT, BOOLEAN, TEXT, TEXT, TEXT, TEXT, TEXT, TEXT)
    FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.set_legacy_delete_sync_mode(BIGINT, BOOLEAN, TEXT, TEXT, TEXT, TEXT, TEXT, TEXT)
    TO platform_operations;

-- Migrator/operations role keeps writing the version history; guarded like
-- the ownership hand-over above because the table may not exist yet.
DO $$
BEGIN
    IF pg_catalog.to_regclass('public.schema_migrations') IS NOT NULL THEN
        EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON public.schema_migrations TO platform_operations';
    END IF;
END
$$;

-- ===================== 6. Bridge functions + triggers =====================
-- SECURITY DEFINER, owned by compatibility_bridge_owner, search_path locked
-- to pg_catalog, every object schema-qualified, EXECUTE revoked from PUBLIC.
-- Recursion guard: each function sets app.bridge_guard around its own writes
-- and restores it afterwards; an exception rolls the guard back with the
-- transaction. Combined with distinct-value checks the write chains always
-- terminate (a sync write never re-triggers its own source representation).

CREATE FUNCTION public.users_insert_bridge() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog
AS $$
BEGIN
    IF pg_catalog.current_setting('app.bridge_guard', true) = 'on' THEN
        RETURN NEW;
    END IF;
    PERFORM pg_catalog.set_config('app.bridge_guard', 'on', true);
    -- New IAM user → Organization state row (legacy department or tombstone).
    INSERT INTO public.organization_user_departments
        (user_id, department_id, membership_version, created_at, updated_at)
    VALUES (NEW.id, NEW.department_id, 1, pg_catalog.now(), pg_catalog.now());
    PERFORM pg_catalog.set_config('app.bridge_guard', 'off', true);
    RETURN NEW;
END;
$$;

CREATE FUNCTION public.users_department_bridge() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog
AS $$
BEGIN
    IF pg_catalog.current_setting('app.bridge_guard', true) = 'on' THEN
        RETURN NEW;
    END IF;
    IF NEW.department_id IS NOT DISTINCT FROM OLD.department_id THEN
        RETURN NEW;
    END IF;
    PERFORM pg_catalog.set_config('app.bridge_guard', 'on', true);
    INSERT INTO public.organization_user_departments
        (user_id, department_id, membership_version, created_at, updated_at)
    VALUES (NEW.id, NEW.department_id, 1, pg_catalog.now(), pg_catalog.now())
    ON CONFLICT (user_id) DO UPDATE
    SET department_id = EXCLUDED.department_id,
        membership_version = organization_user_departments.membership_version + 1,
        updated_at = pg_catalog.now();
    PERFORM pg_catalog.set_config('app.bridge_guard', 'off', true);
    RETURN NEW;
END;
$$;

CREATE FUNCTION public.organization_state_legacy_bridge() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog
AS $$
BEGIN
    IF pg_catalog.current_setting('app.bridge_guard', true) = 'on' THEN
        RETURN NEW;
    END IF;
    PERFORM pg_catalog.set_config('app.bridge_guard', 'on', true);
    -- Sync the legacy column only when the value differs; the guarded users
    -- UPDATE trigger skips its own sync, so the chain terminates.
    UPDATE public.users
    SET department_id = NEW.department_id, updated_at = pg_catalog.now()
    WHERE id = NEW.user_id
      AND department_id IS DISTINCT FROM NEW.department_id;
    PERFORM pg_catalog.set_config('app.bridge_guard', 'off', true);
    RETURN NEW;
END;
$$;

CREATE FUNCTION public.users_delete_bridge() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog
AS $$
DECLARE
    v_sync BOOLEAN;
BEGIN
    IF pg_catalog.current_setting('app.bridge_guard', true) = 'on' THEN
        RETURN OLD;
    END IF;
    SELECT legacy_delete_sync_enabled INTO v_sync
    FROM public.compatibility_bridge_mode
    WHERE id = 1;
    IF v_sync IS NOT FALSE THEN
        PERFORM pg_catalog.set_config('app.bridge_guard', 'on', true);
        DELETE FROM public.organization_user_departments WHERE user_id = OLD.id;
        PERFORM pg_catalog.set_config('app.bridge_guard', 'off', true);
    END IF;
    RETURN OLD;
END;
$$;

ALTER FUNCTION public.users_insert_bridge() OWNER TO compatibility_bridge_owner;
ALTER FUNCTION public.users_department_bridge() OWNER TO compatibility_bridge_owner;
ALTER FUNCTION public.organization_state_legacy_bridge() OWNER TO compatibility_bridge_owner;
ALTER FUNCTION public.users_delete_bridge() OWNER TO compatibility_bridge_owner;
REVOKE ALL ON FUNCTION public.users_insert_bridge() FROM PUBLIC;
REVOKE ALL ON FUNCTION public.users_department_bridge() FROM PUBLIC;
REVOKE ALL ON FUNCTION public.organization_state_legacy_bridge() FROM PUBLIC;
REVOKE ALL ON FUNCTION public.users_delete_bridge() FROM PUBLIC;

-- Minimum cross-representation grants for the bridge owner only. SELECT on
-- departments is required by the FK checks its INSERTs/UPDATEs trigger.
GRANT SELECT, INSERT, UPDATE, DELETE ON organization_user_departments
    TO compatibility_bridge_owner;
GRANT SELECT, UPDATE ON users TO compatibility_bridge_owner;
GRANT SELECT ON departments TO compatibility_bridge_owner;

CREATE TRIGGER trg_users_insert_bridge
AFTER INSERT ON users
FOR EACH ROW EXECUTE FUNCTION public.users_insert_bridge();

CREATE TRIGGER trg_users_department_bridge
AFTER UPDATE OF department_id ON users
FOR EACH ROW EXECUTE FUNCTION public.users_department_bridge();

CREATE TRIGGER trg_organization_state_insert_bridge
AFTER INSERT ON organization_user_departments
FOR EACH ROW EXECUTE FUNCTION public.organization_state_legacy_bridge();

CREATE TRIGGER trg_organization_state_update_bridge
AFTER UPDATE OF department_id ON organization_user_departments
FOR EACH ROW EXECUTE FUNCTION public.organization_state_legacy_bridge();

-- Always installed; reads compatibility_bridge_mode per delete.
CREATE TRIGGER trg_users_delete_bridge
AFTER DELETE ON users
FOR EACH ROW EXECUTE FUNCTION public.users_delete_bridge();
