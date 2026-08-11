-- 000006_lifecycle_and_organization_membership.down.sql
-- Revert 000006 up in reverse dependency order:
--   organization_user_departments → users.version → users.lifecycle_state.
DROP TABLE IF EXISTS organization_user_departments;

ALTER TABLE users DROP CONSTRAINT IF EXISTS users_version_positive;
ALTER TABLE users DROP COLUMN IF EXISTS version;

ALTER TABLE users DROP CONSTRAINT IF EXISTS users_lifecycle_state_check;
ALTER TABLE users DROP COLUMN IF EXISTS lifecycle_state;
