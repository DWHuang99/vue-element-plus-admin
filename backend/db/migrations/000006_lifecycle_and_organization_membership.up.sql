-- 000006_lifecycle_and_organization_membership.up.sql
-- Foundational data-model changes (plan Phase 2, T011):
--   IAM:          users.lifecycle_state (provisioning/active/disabled) + users.version CAS.
--   Organization: organization_user_departments (versioned membership; user_id is an
--                 opaque IAM subject reference, deliberately WITHOUT a FK to users).
--
-- Backfill strategy: temporary defaults (active / 1) backfill existing rows, the CHECK
-- constraints are added while every value is valid, then the defaults are dropped so
-- every future INSERT must state lifecycle_state and version explicitly (legacy
-- queries updated in lockstep). Legacy columns/FKs/indexes on users (account, email,
-- department_id) are preserved untouched for the compatibility window.

-- 1. IAM lifecycle + version columns (temporary defaults backfill existing rows).
ALTER TABLE users
    ADD COLUMN lifecycle_state TEXT NOT NULL DEFAULT 'active';

ALTER TABLE users
    ADD COLUMN version BIGINT NOT NULL DEFAULT 1;

ALTER TABLE users
    ADD CONSTRAINT users_lifecycle_state_check
        CHECK (lifecycle_state IN ('provisioning', 'active', 'disabled'));

ALTER TABLE users
    ADD CONSTRAINT users_version_positive
        CHECK (version > 0);

-- 2. Organization versioned membership table.
CREATE TABLE organization_user_departments (
    user_id            BIGINT      NOT NULL PRIMARY KEY,
    department_id      BIGINT      REFERENCES departments(id),
    membership_version BIGINT      NOT NULL CHECK (membership_version > 0),
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Delete protection and by-department user listing both scan this index.
CREATE INDEX idx_organization_user_departments_department_user
    ON organization_user_departments (department_id, user_id);

-- 3. Backfill: every existing IAM user gets an Organization state row.
--    non-null users.department_id → copied; NULL → versioned tombstone.
--    (Row absence means "Organization state never established"; every legacy
--    user is treated as established, matching the old department_id column.
--    The tombstone keeps the membership version so ABA cannot occur.)
INSERT INTO organization_user_departments (user_id, department_id, membership_version)
SELECT u.id, u.department_id, 1
FROM users u;

-- 4. Drop the temporary defaults: all future INSERTs state lifecycle/version.
ALTER TABLE users ALTER COLUMN lifecycle_state DROP DEFAULT;
ALTER TABLE users ALTER COLUMN version DROP DEFAULT;
