-- 000004_rbac.down.sql
-- Revert 000004_rbac.up.sql in reverse dependency order:
--   user_roles → users.department_id (FK to departments) → departments → roles.
DROP TABLE IF EXISTS user_roles;

ALTER TABLE users DROP COLUMN IF EXISTS department_id;

DROP TABLE IF EXISTS departments;

DROP TABLE IF EXISTS roles;

ALTER TABLE users DROP COLUMN IF EXISTS account;

ALTER TABLE users DROP COLUMN IF EXISTS email;
