-- name: Ping :one
SELECT 1;

-- name: GetUserByUsername :one
SELECT
    u.id,
    u.username,
    u.role_id,
    u.is_active,
    u.created_at,
    u.updated_at,
    r.code AS role_code,
    r.name AS role_name,
    r.permissions
FROM users AS u
JOIN roles AS r ON r.id = u.role_id
WHERE u.username = $1;

-- name: AddUser :one
INSERT INTO
    users (
        username,
        password_hash,
        role_id
    )
VALUES ($1, $2, $3) RETURNING *;

-- name: GetUserAuthByUsername :one
SELECT
    u.password_hash,
    u.is_active,
    r.code AS role_code
FROM users AS u
JOIN roles AS r ON r.id = u.role_id
WHERE u.username = $1;
