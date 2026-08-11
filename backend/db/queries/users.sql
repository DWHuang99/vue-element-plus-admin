-- name: CreateUser :one
-- 000006 dropped the temporary lifecycle defaults; registration states active
-- explicitly and creates version 1 (data-model.md User invariants).
INSERT INTO users (username, password_hash, lifecycle_state, version)
VALUES ($1, $2, 'active', 1)
RETURNING id, username, password_hash, created_at, updated_at;

-- name: GetUserByUsername :one
SELECT id, username, password_hash, created_at, updated_at
FROM users
WHERE username = $1;

-- name: GetUserByID :one
SELECT id, username, password_hash, created_at, updated_at
FROM users
WHERE id = $1;
