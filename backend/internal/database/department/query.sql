-- name: Ping :one
SELECT 1;

-- name: ListDepartments :many
SELECT id, parent_id, department_name, status, deleting, created_at, remark
FROM departments
WHERE sqlc.arg('name')::TEXT = ''
   OR department_name ILIKE '%' || sqlc.arg('name')::TEXT || '%'
ORDER BY id
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: CountDepartments :one
SELECT COUNT(*)
FROM departments
WHERE sqlc.arg('name')::TEXT = ''
   OR department_name ILIKE '%' || sqlc.arg('name')::TEXT || '%';

-- name: ListAllDepartments :many
SELECT id, parent_id, department_name, status, deleting, created_at, remark
FROM departments
ORDER BY id;

-- name: GetDepartmentByID :one
SELECT id, parent_id, department_name, status, deleting, created_at, remark
FROM departments
WHERE id = $1;

-- name: GetDepartmentsByIDs :many
SELECT id, parent_id, department_name, status, deleting, created_at, remark
FROM departments
WHERE id IN (
    SELECT value::BIGINT
    FROM jsonb_array_elements_text(sqlc.arg('ids')::JSONB)
)
ORDER BY id;

-- name: CreateDepartment :exec
INSERT INTO departments (parent_id, department_name, status, remark)
VALUES (NULLIF(sqlc.arg('parent_id')::BIGINT, 0), sqlc.arg('department_name'), sqlc.arg('status'), sqlc.arg('remark'));

-- name: UpdateDepartment :one
UPDATE departments
SET parent_id = NULLIF(sqlc.arg('parent_id')::BIGINT, 0),
    department_name = sqlc.arg('department_name'),
    status = sqlc.arg('status'),
    remark = sqlc.arg('remark'),
    updated_at = CURRENT_TIMESTAMP
WHERE id = sqlc.arg('id') AND deleting = FALSE
RETURNING id;

-- name: CountDepartmentChildren :one
SELECT COUNT(*) FROM departments WHERE parent_id = sqlc.arg('parent_id')::BIGINT;

-- name: BeginDepartmentDeletion :one
UPDATE departments
SET deleting = TRUE
WHERE id = $1 AND deleting = FALSE
RETURNING id;

-- name: CancelDepartmentDeletions :exec
UPDATE departments
SET deleting = FALSE
WHERE id IN (
      SELECT value::BIGINT
      FROM jsonb_array_elements_text(sqlc.arg('ids')::JSONB)
  );

-- name: DeleteDepartments :many
DELETE FROM departments
WHERE deleting = TRUE
  AND id IN (
      SELECT value::BIGINT
      FROM jsonb_array_elements_text(sqlc.arg('ids')::JSONB)
  )
RETURNING id;
