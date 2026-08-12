-- name: Ping :one
SELECT 1;

-- name: GetUserByUsername :one
SELECT * FROM users WHERE username = $1;

-- name: AddUser :one
INSERT INTO
    users (
        username,
        password_hash,
        role_id
    )
VALUES ($1, $2, $3) RETURNING *;

-- name: GetUserPassword :one
select password_hash from users where username = $1;