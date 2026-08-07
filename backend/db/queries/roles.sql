-- roles.sql
-- Queries for the roles table. `code` is reserved for phase-2 permission filtering.

-- name: ListRoles :many
SELECT id, name, code, created_at, updated_at
FROM roles
ORDER BY id;

-- name: GetRoleByID :one
SELECT id, name, code, created_at, updated_at
FROM roles
WHERE id = $1;

-- name: GetRoleByName :one
SELECT id, name, code, created_at, updated_at
FROM roles
WHERE name = $1;

-- name: GetRoleByCode :one
SELECT id, name, code, created_at, updated_at
FROM roles
WHERE code = $1;

-- name: CreateRole :one
INSERT INTO roles (name, code)
VALUES ($1, $2)
RETURNING id, name, code, created_at, updated_at;

-- name: UpdateRole :one
UPDATE roles
SET name = $2, code = $3, updated_at = now()
WHERE id = $1
RETURNING id, name, code, created_at, updated_at;

-- name: DeleteRole :exec
DELETE FROM roles
WHERE id = $1;

-- name: CountUserRolesByRoleID :one
SELECT count(*) FROM user_roles WHERE role_id = $1;
