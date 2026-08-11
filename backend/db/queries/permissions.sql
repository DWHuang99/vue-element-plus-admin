-- permissions.sql
-- Effective permissions are the distinct union of grants from all user roles.

-- name: ListEffectivePermissionsByUserID :many
SELECT DISTINCT p.code
FROM user_roles ur
JOIN role_permissions rp ON rp.role_id = ur.role_id
JOIN permissions p ON p.id = rp.permission_id
WHERE ur.user_id = $1
ORDER BY p.code;

-- name: HasPermissionByUserID :one
SELECT EXISTS (
    SELECT 1
    FROM user_roles ur
    JOIN role_permissions rp ON rp.role_id = ur.role_id
    JOIN permissions p ON p.id = rp.permission_id
    WHERE ur.user_id = $1
      AND p.code = $2
) AS has_permission;
