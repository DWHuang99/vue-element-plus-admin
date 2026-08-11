-- 000008_bridge_roles_and_guards.down.sql
-- Revert 000008 up in reverse dependency order:
--   triggers → bridge/mode functions → platform control tables →
--   ownership back to the migration executor → roles.
DROP TRIGGER IF EXISTS trg_users_delete_bridge ON users;
DROP TRIGGER IF EXISTS trg_organization_state_update_bridge ON organization_user_departments;
DROP TRIGGER IF EXISTS trg_organization_state_insert_bridge ON organization_user_departments;
DROP TRIGGER IF EXISTS trg_users_department_bridge ON users;
DROP TRIGGER IF EXISTS trg_users_insert_bridge ON users;

DROP FUNCTION IF EXISTS public.users_delete_bridge();
DROP FUNCTION IF EXISTS public.organization_state_legacy_bridge();
DROP FUNCTION IF EXISTS public.users_department_bridge();
DROP FUNCTION IF EXISTS public.users_insert_bridge();
DROP FUNCTION IF EXISTS public.set_legacy_delete_sync_mode(BIGINT, BOOLEAN, TEXT, TEXT, TEXT, TEXT, TEXT, TEXT);

DROP TABLE IF EXISTS compatibility_rollout_gates;
DROP TABLE IF EXISTS compatibility_bridge_mode_changes;
DROP TABLE IF EXISTS compatibility_bridge_mode;

-- Revoke every grant before dropping roles: PostgreSQL refuses to drop a
-- role that still appears in object ACLs.
REVOKE ALL ON SCHEMA public FROM iam_owner, organization_owner,
                             platform_owner, compatibility_bridge_owner,
                             app_runtime, platform_operations;
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM app_runtime, platform_operations,
                                                compatibility_bridge_owner;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM app_runtime, platform_operations;

-- Ownership returns to the migration executor before roles are dropped.
ALTER TABLE users OWNER TO SESSION_USER;
ALTER TABLE sessions OWNER TO SESSION_USER;
ALTER TABLE roles OWNER TO SESSION_USER;
ALTER TABLE user_roles OWNER TO SESSION_USER;
ALTER TABLE permissions OWNER TO SESSION_USER;
ALTER TABLE role_permissions OWNER TO SESSION_USER;
ALTER TABLE iam_command_receipts OWNER TO SESSION_USER;
ALTER TABLE iam_outbox_events OWNER TO SESSION_USER;
ALTER TABLE iam_outbox_requeues OWNER TO SESSION_USER;
ALTER TABLE departments OWNER TO SESSION_USER;
ALTER TABLE organization_user_departments OWNER TO SESSION_USER;
ALTER TABLE organization_command_receipts OWNER TO SESSION_USER;
ALTER TABLE organization_inbox_messages OWNER TO SESSION_USER;
ALTER TABLE admin_workflows OWNER TO SESSION_USER;
ALTER TABLE admin_workflow_subjects OWNER TO SESSION_USER;
ALTER TABLE admin_workflow_recovery_actions OWNER TO SESSION_USER;

DO $$
BEGIN
    IF pg_catalog.to_regclass('public.schema_migrations') IS NOT NULL THEN
        EXECUTE 'ALTER TABLE public.schema_migrations OWNER TO SESSION_USER';
    END IF;
END
$$;

ALTER SEQUENCE users_id_seq OWNER TO SESSION_USER;
ALTER SEQUENCE sessions_id_seq OWNER TO SESSION_USER;
ALTER SEQUENCE roles_id_seq OWNER TO SESSION_USER;
ALTER SEQUENCE permissions_id_seq OWNER TO SESSION_USER;
ALTER SEQUENCE departments_id_seq OWNER TO SESSION_USER;
-- The control tables above are DROPped earlier in this file, which drops their
-- owned sequences; no ownership transfer is needed for objects being removed.

-- Roles are cluster-global: on a shared cluster another database may still
-- own objects as these roles (DROP ROLE then fails), and one database's
-- rollback must not be blocked by that cluster-wide state. Drop when
-- possible; retain with a NOTICE otherwise.
DO $$
BEGIN
    IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'compatibility_bridge_owner') THEN
        BEGIN
            DROP ROLE compatibility_bridge_owner;
        EXCEPTION WHEN dependent_objects_still_exist THEN
            RAISE NOTICE 'role compatibility_bridge_owner retained: other cluster objects depend on it';
        END;
    END IF;
END
$$;

DO $$
BEGIN
    IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'platform_owner') THEN
        BEGIN
            DROP ROLE platform_owner;
        EXCEPTION WHEN dependent_objects_still_exist THEN
            RAISE NOTICE 'role platform_owner retained: other cluster objects depend on it';
        END;
    END IF;
END
$$;

DO $$
BEGIN
    IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'organization_owner') THEN
        BEGIN
            DROP ROLE organization_owner;
        EXCEPTION WHEN dependent_objects_still_exist THEN
            RAISE NOTICE 'role organization_owner retained: other cluster objects depend on it';
        END;
    END IF;
END
$$;

DO $$
BEGIN
    IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'iam_owner') THEN
        BEGIN
            DROP ROLE iam_owner;
        EXCEPTION WHEN dependent_objects_still_exist THEN
            RAISE NOTICE 'role iam_owner retained: other cluster objects depend on it';
        END;
    END IF;
END
$$;

DO $$
BEGIN
    IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'platform_operations') THEN
        BEGIN
            DROP ROLE platform_operations;
        EXCEPTION WHEN dependent_objects_still_exist THEN
            RAISE NOTICE 'role platform_operations retained: other cluster objects depend on it';
        END;
    END IF;
END
$$;

DO $$
BEGIN
    IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'app_runtime') THEN
        BEGIN
            DROP ROLE app_runtime;
        EXCEPTION WHEN dependent_objects_still_exist THEN
            RAISE NOTICE 'role app_runtime retained: other cluster objects depend on it';
        END;
    END IF;
END
$$;
