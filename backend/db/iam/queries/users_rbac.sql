-- users_rbac.sql
-- User-management queries: create/update/delete users, paginated listing,
-- and the user<->role join. Query names avoid colliding with the existing
-- auth-focused CreateUser / GetUserByUsername / GetUserByID.

-- name: CreateRbacUser :one
-- 000006 dropped the temporary lifecycle defaults; admin-created users state
-- active explicitly and create version 1 (data-model.md User invariants).
INSERT INTO users (username, password_hash, account, email, department_id, lifecycle_state, version)
VALUES ($1, $2, $3, $4, $5, 'active', 1)
RETURNING id, username, password_hash, account, email, department_id, created_at, updated_at;

-- name: UpdateRbacUser :one
UPDATE users
SET account = $2, email = $3, department_id = $4, updated_at = now()
WHERE id = $1
RETURNING id, username, password_hash, account, email, department_id, created_at, updated_at;

-- name: UpdateUserPassword :exec
UPDATE users
SET password_hash = $2, updated_at = now()
WHERE id = $1;

-- name: DeleteUser :exec
DELETE FROM users WHERE id = $1;

-- name: GetUserFullByID :one
SELECT id, username, password_hash, account, email, department_id, created_at, updated_at
FROM users
WHERE id = $1;

-- name: ListUsersByDepartment :many
SELECT id, username, password_hash, account, email, department_id, created_at, updated_at
FROM users
WHERE ($1::bigint = 0 OR department_id = $1)
  AND ($2::text = '' OR username ILIKE '%' || $2 || '%')
  AND ($3::text = '' OR account ILIKE '%' || $3 || '%')
ORDER BY id
LIMIT $4 OFFSET $5;

-- name: CountUsersByDepartment :one
SELECT count(*)
FROM users
WHERE ($1::bigint = 0 OR department_id = $1)
  AND ($2::text = '' OR username ILIKE '%' || $2 || '%')
  AND ($3::text = '' OR account ILIKE '%' || $3 || '%');

-- name: DeleteUserRolesByUserID :exec
DELETE FROM user_roles WHERE user_id = $1;

-- name: InsertUserRole :exec
INSERT INTO user_roles (user_id, role_id)
VALUES ($1, $2)
ON CONFLICT (user_id, role_id) DO NOTHING;

-- name: ListRolesByUserID :many
SELECT r.id, r.name, r.code
FROM user_roles ur
JOIN roles r ON r.id = ur.role_id
WHERE ur.user_id = $1
ORDER BY r.id;

-- name: ListUserRolesByUserIDs :many
-- Aggregated role projection for a user batch (BatchGetManagedIdentities):
-- one bounded query instead of per-row role lookups.
SELECT ur.user_id, r.id AS role_id, r.name AS role_name, r.code AS role_code
FROM user_roles ur
JOIN roles r ON r.id = ur.role_id
WHERE ur.user_id = ANY($1::bigint[])
ORDER BY r.id;
