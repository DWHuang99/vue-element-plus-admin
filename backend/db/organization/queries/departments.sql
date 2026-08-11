-- departments.sql
-- Queries for the departments table (self-referencing tree).

-- name: ListDepartments :many
SELECT id, name, parent_id, created_at, updated_at
FROM departments
ORDER BY id;

-- name: GetDepartmentByID :one
SELECT id, name, parent_id, created_at, updated_at
FROM departments
WHERE id = $1;

-- name: GetDepartmentByName :one
SELECT id, name, parent_id, created_at, updated_at
FROM departments
WHERE name = $1;

-- name: CreateDepartment :one
INSERT INTO departments (name, parent_id)
VALUES ($1, $2)
RETURNING id, name, parent_id, created_at, updated_at;

-- name: UpdateDepartment :one
UPDATE departments
SET name = $2, parent_id = $3, updated_at = now()
WHERE id = $1
RETURNING id, name, parent_id, created_at, updated_at;

-- name: DeleteDepartment :exec
DELETE FROM departments
WHERE id = $1;

-- name: CountDepartmentsByParentID :one
SELECT count(*) FROM departments WHERE parent_id = $1;

-- name: CountUsersByDepartmentID :one
-- Delete protection counts Organization-owned memberships (data-model.md:
-- Organization SQL must not read the IAM users table). The bridge keeps these
-- state rows 1:1 with users during the compatibility window.
SELECT count(*) FROM organization_user_departments WHERE department_id = $1;
