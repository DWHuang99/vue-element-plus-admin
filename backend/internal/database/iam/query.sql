-- name: Ping :one
SELECT 1;

-- name: GetUserByID :one
SELECT
    u.id,
    u.username,
    u.role_id,
    u.is_active,
    u.created_at,
    u.updated_at,
    r.code AS role_code,
    r.name AS role_name,
    COALESCE(
        jsonb_agg(DISTINCT rp.permission_code)
            FILTER (WHERE rp.permission_code IS NOT NULL),
        '[]'::jsonb
    )::TEXT AS permissions_json
FROM users AS u
JOIN roles AS r ON r.id = u.role_id
LEFT JOIN role_permissions AS rp ON rp.role_id = r.id
WHERE u.id = $1
GROUP BY u.id, r.id;

-- name: AddUserByRoleCode :one
INSERT INTO users (username, password_hash, role_id)
SELECT $1, $2, r.id
FROM roles AS r
WHERE r.code = $3 AND r.status = TRUE
RETURNING id, username, password_hash, role_id, account, email, department_id,
          is_active, created_at, updated_at;

-- name: GetUserAuthByUsername :one
SELECT u.id, u.password_hash, u.is_active, r.code AS role_code,
       COALESCE(jsonb_agg(DISTINCT rp.permission_code)
           FILTER (WHERE rp.permission_code IS NOT NULL), '[]'::jsonb)::TEXT AS permissions_json
FROM users AS u
JOIN roles AS r ON r.id = u.role_id
LEFT JOIN role_permissions rp ON rp.role_id = r.id
WHERE u.username = $1
GROUP BY u.id, r.id;

-- name: GetUserAuthByID :one
SELECT u.id, u.password_hash, u.is_active, r.code AS role_code,
       COALESCE(jsonb_agg(DISTINCT rp.permission_code)
           FILTER (WHERE rp.permission_code IS NOT NULL), '[]'::jsonb)::TEXT AS permissions_json
FROM users AS u
JOIN roles AS r ON r.id = u.role_id
LEFT JOIN role_permissions rp ON rp.role_id = r.id
WHERE u.id = $1
GROUP BY u.id, r.id;

-- name: HasUserPermission :one
SELECT EXISTS (
    SELECT 1
    FROM users u
    JOIN roles r ON r.id = u.role_id
    JOIN role_permissions rp ON rp.role_id = r.id
    WHERE u.id = sqlc.arg('user_id')
      AND u.is_active = TRUE
      AND r.status = TRUE
      AND rp.permission_code IN (
          sqlc.arg('permission_code'), '*', '*:*:*', '*.*.*'
      )
);

-- name: ListAllMenus :many
SELECT id, parent_id, type, path, name, component, status, meta, permission_list
FROM menus
ORDER BY id;

-- name: CreateMenu :exec
INSERT INTO menus (parent_id, type, path, name, component, status, meta, permission_list)
VALUES (
    NULLIF(sqlc.arg('parent_id')::BIGINT, 0), sqlc.arg('type'), sqlc.arg('path'),
    sqlc.arg('name'), sqlc.arg('component'), sqlc.arg('status'),
    sqlc.arg('meta')::JSONB, sqlc.arg('permission_list')::JSONB
);

-- name: UpdateMenu :one
UPDATE menus
SET parent_id = NULLIF(sqlc.arg('parent_id')::BIGINT, 0),
    type = sqlc.arg('type'), path = sqlc.arg('path'), name = sqlc.arg('name'),
    component = sqlc.arg('component'), status = sqlc.arg('status'),
    meta = sqlc.arg('meta')::JSONB, permission_list = sqlc.arg('permission_list')::JSONB,
    updated_at = CURRENT_TIMESTAMP
WHERE id = sqlc.arg('id')
RETURNING id;

-- name: CountMenuChildren :one
SELECT COUNT(*) FROM menus WHERE parent_id = sqlc.arg('parent_id')::BIGINT;

-- name: DeleteMenu :one
DELETE FROM menus WHERE id = $1 RETURNING id;

-- name: ListRoles :many
SELECT id, code, name, status, created_at, remark
FROM roles
WHERE sqlc.arg('name')::TEXT = '' OR name ILIKE '%' || sqlc.arg('name')::TEXT || '%'
ORDER BY id
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: CountRoles :one
SELECT COUNT(*)
FROM roles
WHERE sqlc.arg('name')::TEXT = '' OR name ILIKE '%' || sqlc.arg('name')::TEXT || '%';

-- name: GetRole :one
SELECT id, code, name, status, created_at, remark FROM roles WHERE id = $1;

-- name: CreateRole :one
INSERT INTO roles (code, name, status, remark)
VALUES (sqlc.arg('code'), sqlc.arg('name'), sqlc.arg('status'), sqlc.arg('remark'))
RETURNING id;

-- name: UpdateRole :one
UPDATE roles
SET name = sqlc.arg('name'), status = sqlc.arg('status'), remark = sqlc.arg('remark'),
    updated_at = CURRENT_TIMESTAMP
WHERE id = sqlc.arg('id')
RETURNING id;

-- name: CountRoleUsers :one
SELECT COUNT(*) FROM users WHERE role_id = $1;

-- name: DeleteRole :one
DELETE FROM roles WHERE id = $1 RETURNING id;

-- name: DeleteRolePermissions :exec
DELETE FROM role_permissions WHERE role_id = $1;

-- name: DeleteRoleMenus :exec
DELETE FROM role_menus WHERE role_id = $1;

-- name: CreateRoleMenu :exec
INSERT INTO role_menus (role_id, menu_id) VALUES ($1, $2);

-- name: CreateRoleMenuPermission :exec
INSERT INTO role_permissions (role_id, menu_id, permission_code)
VALUES ($1, sqlc.arg('menu_id')::BIGINT, sqlc.arg('permission_code'));

-- name: CreateGlobalRolePermission :exec
INSERT INTO role_permissions (role_id, menu_id, permission_code)
VALUES ($1, NULL, $2);

-- name: ListRoleMenus :many
SELECT m.id, m.parent_id, m.type, m.path, m.name, m.component, m.status,
       m.meta, m.permission_list,
       COALESCE(
           (
               SELECT jsonb_agg(rp.permission_code ORDER BY rp.permission_code)
               FROM role_permissions rp
               WHERE rp.role_id = sqlc.arg('role_id') AND rp.menu_id = m.id
           ),
           '[]'::JSONB
       )::TEXT AS selected_permissions_json
FROM role_menus rm
JOIN menus m ON m.id = rm.menu_id
WHERE rm.role_id = sqlc.arg('role_id')
ORDER BY m.id;

-- name: ListManagedUsers :many
SELECT u.id, u.username, u.account, u.email, u.created_at,
       u.role_id, r.name AS role_name, u.department_id
FROM users u
JOIN roles r ON r.id = u.role_id
WHERE (sqlc.arg('department_id')::BIGINT = 0 OR u.department_id = sqlc.arg('department_id'))
  AND (sqlc.arg('username')::TEXT = '' OR u.username ILIKE '%' || sqlc.arg('username')::TEXT || '%')
  AND (sqlc.arg('account')::TEXT = '' OR u.account ILIKE '%' || sqlc.arg('account')::TEXT || '%')
ORDER BY u.id DESC
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: CountManagedUsers :one
SELECT COUNT(*)
FROM users u
WHERE (sqlc.arg('department_id')::BIGINT = 0 OR u.department_id = sqlc.arg('department_id'))
  AND (sqlc.arg('username')::TEXT = '' OR u.username ILIKE '%' || sqlc.arg('username')::TEXT || '%')
  AND (sqlc.arg('account')::TEXT = '' OR u.account ILIKE '%' || sqlc.arg('account')::TEXT || '%');

-- name: CountUsersByDepartment :one
SELECT COUNT(*)
FROM users
WHERE department_id = $1;

-- name: CreateManagedUser :exec
INSERT INTO users (username, password_hash, role_id, account, email, department_id)
VALUES (
    sqlc.arg('username'), sqlc.arg('password_hash'), sqlc.arg('role_id'),
    sqlc.arg('account'), sqlc.arg('email'), NULLIF(sqlc.arg('department_id')::BIGINT, 0)
);

-- name: UpdateManagedUser :one
UPDATE users
SET username = sqlc.arg('username'), account = sqlc.arg('account'), email = sqlc.arg('email'),
    role_id = sqlc.arg('role_id'), department_id = NULLIF(sqlc.arg('department_id')::BIGINT, 0),
    updated_at = CURRENT_TIMESTAMP
WHERE id = sqlc.arg('id')
RETURNING id;

-- name: UpdateManagedUserWithPassword :one
UPDATE users
SET username = sqlc.arg('username'), account = sqlc.arg('account'), email = sqlc.arg('email'),
    role_id = sqlc.arg('role_id'), department_id = NULLIF(sqlc.arg('department_id')::BIGINT, 0),
    password_hash = sqlc.arg('password_hash'), updated_at = CURRENT_TIMESTAMP
WHERE id = sqlc.arg('id')
RETURNING id;

-- name: DeleteManagedUsers :exec
DELETE FROM users
WHERE id IN (
    SELECT value::BIGINT FROM jsonb_array_elements_text(sqlc.arg('ids')::JSONB)
);

-- name: FindExternalUser :one
SELECT id, user_id, provider_issuer, provider_subject, email
FROM user_external_identity
WHERE provider_issuer = sqlc.arg('provider_issuer')
  AND provider_subject = sqlc.arg('provider_subject');

-- name: CreateExternalUser :one
INSERT INTO user_external_identity (
    user_id,
    provider_issuer,
    provider_subject,
    email
)
VALUES (
    sqlc.arg('user_id')::BIGINT,
    sqlc.arg('provider_issuer')::TEXT,
    sqlc.arg('provider_subject')::TEXT,
    sqlc.arg('email')::TEXT
)
RETURNING id, user_id, provider_issuer, provider_subject, email;

-- name: AddGoogleToken :one
INSERT INTO google_integrations (
    user_id,
    provider_subject,
    access_token_encrypted,
    refresh_token_encrypted,
    token_type,
    expiry,
    scopes
)
VALUES (
    sqlc.arg('user_id')::BIGINT,
    sqlc.arg('provider_subject')::TEXT,
    sqlc.arg('access_token_encrypted')::TEXT,
    sqlc.arg('refresh_token_encrypted')::TEXT,
    sqlc.arg('token_type')::TEXT,
    sqlc.arg('expiry')::TIMESTAMPTZ,
    sqlc.arg('scopes')::TEXT[]
)
ON CONFLICT (user_id) DO UPDATE SET
    access_token_encrypted = EXCLUDED.access_token_encrypted,
    refresh_token_encrypted = CASE
        WHEN EXCLUDED.refresh_token_encrypted <> ''
        THEN EXCLUDED.refresh_token_encrypted
        ELSE google_integrations.refresh_token_encrypted
    END,
    token_type = EXCLUDED.token_type,
    expiry = EXCLUDED.expiry,
    scopes = EXCLUDED.scopes,
    updated_at = CURRENT_TIMESTAMP
RETURNING id, user_id, provider_subject, access_token_encrypted, refresh_token_encrypted, token_type, expiry, scopes;

-- name: GetGoogleTokenByUserID :one
SELECT access_token_encrypted, refresh_token_encrypted, expiry,provider_subject,token_type
FROM google_integrations
WHERE user_id = sqlc.arg('user_id')::BIGINT
and token_type = 'Bearer';