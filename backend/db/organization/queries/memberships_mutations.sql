-- memberships_mutations.sql
-- Versioned membership mutations (US2, contracts/organization-application.md).
-- The service serializes each mutation inside one Organization-local
-- transaction: lock the state row FOR UPDATE, decide against the expected
-- version, then create/update. All references stay inside Organization-owned
-- tables (ownership matrix T016) — never IAM tables.

-- name: LockMembershipByUserID :one
-- Row-level lock for CAS decision-making. Missing row = no state established
-- (a normal empty result for the decision, not an error).
SELECT user_id, department_id, membership_version, created_at, updated_at
FROM organization_user_departments
WHERE user_id = $1
FOR UPDATE;

-- name: CreateMembership :one
-- First state row: real department (set) or null tombstone (clear).
INSERT INTO organization_user_departments (user_id, department_id, membership_version)
VALUES ($1, $2, $3)
RETURNING user_id, department_id, membership_version, created_at, updated_at;

-- name: UpdateMembership :one
-- Version-bearing replacement under the service's CAS decision (the row is
-- locked first; the WHERE is defense in depth, not the decision point).
UPDATE organization_user_departments
SET department_id = $2,
    membership_version = $3,
    updated_at = now()
WHERE user_id = $1
RETURNING user_id, department_id, membership_version, created_at, updated_at;
