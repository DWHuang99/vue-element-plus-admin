-- name: CreateSession :one
INSERT INTO sessions (token_hash, user_id, expires_at)
VALUES ($1, $2, $3)
RETURNING id, token_hash, user_id, created_at, expires_at, revoked_at, last_used_at;

-- name: GetSessionByTokenHash :one
SELECT s.id, s.token_hash, s.user_id, s.created_at, s.expires_at, s.revoked_at, s.last_used_at,
       u.username AS user_username, u.created_at AS user_created_at
FROM sessions s
JOIN users u ON u.id = s.user_id
WHERE s.token_hash = $1;

-- name: RevokeSessionsByUserID :exec
-- DisableUser session revocation: every live session of the disabled subject
-- is revoked inside the same transaction as the lifecycle CAS.
UPDATE sessions
SET revoked_at = now()
WHERE user_id = $1 AND revoked_at IS NULL;

-- name: RevokeSessionByTokenHash :exec
UPDATE sessions
SET revoked_at = now()
WHERE token_hash = $1;

-- name: TouchSession :exec
UPDATE sessions
SET last_used_at = now(),
    expires_at = $2
WHERE token_hash = $1;

-- name: GetSessionWithUserState :one
-- Session lookup for Authenticate: joins the owner's username and lifecycle
-- state so the service can enforce the active-user check alongside the
-- revocation/expiry checks; s.id is the stable session reference shared with
-- Register/Login. (GetSessionByTokenHash above stays untouched for the legacy
-- services.)
SELECT s.id AS session_id, s.token_hash, s.user_id, s.created_at, s.expires_at, s.revoked_at,
       u.username AS user_username, u.lifecycle_state AS user_lifecycle_state
FROM sessions s
JOIN users u ON u.id = s.user_id
WHERE s.token_hash = $1;
