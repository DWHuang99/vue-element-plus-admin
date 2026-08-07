-- 000001_init.up.sql
-- Initial migration for backend scaffold.
-- This migration validates the migration system is functional.
-- Per FR-013, no business domain data models are created.

-- golang-migrate automatically creates the schema_migrations table.
-- This empty migration serves as a baseline verification that the
-- migration system applies and tracks migrations correctly.

-- If future migrations need to create tables, they follow this pattern:
-- CREATE TABLE example (
--     id   BIGSERIAL PRIMARY KEY,
--     data TEXT NOT NULL
-- );
