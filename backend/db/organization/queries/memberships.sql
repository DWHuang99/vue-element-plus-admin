-- memberships.sql
-- Read-side queries for organization_user_departments (US1 BFF composition;
-- the full versioned membership mutations land with US2/T035-T036).
-- All references stay inside Organization-owned tables (ownership matrix T016):
-- organization_user_departments + departments only — never IAM tables.

-- name: GetUserMembershipByUserID :one
-- Missing row means "Organization state never established" (normal empty
-- result for the BFF, not an error). The department name is joined so the
-- profile composition needs no per-row lookup.
SELECT m.user_id, m.department_id, d.name AS department_name,
       m.membership_version, m.created_at, m.updated_at
FROM organization_user_departments m
LEFT JOIN departments d ON d.id = m.department_id
WHERE m.user_id = $1;

-- name: BatchGetUserMemberships :many
-- One bounded query for the current list page; users without a state row are
-- simply absent from the result (the BFF maps absence to no-department).
SELECT m.user_id, m.department_id, d.name AS department_name,
       m.membership_version, m.created_at, m.updated_at
FROM organization_user_departments m
LEFT JOIN departments d ON d.id = m.department_id
WHERE m.user_id = ANY($1::bigint[])
ORDER BY m.user_id;

-- name: ListUserIDsByDepartment :many
-- Complete deterministic candidate-id set for the department filter; the IAM
-- side applies its own filters and pagination over these ids.
SELECT user_id
FROM organization_user_departments
WHERE department_id = $1
ORDER BY user_id;
