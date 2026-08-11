-- name: CreateUser :one
-- 000006 dropped the temporary lifecycle defaults; registration states active
-- explicitly and creates version 1 (data-model.md User invariants).
INSERT INTO users (username, password_hash, lifecycle_state, version)
VALUES ($1, $2, 'active', 1)
RETURNING id, username, password_hash, created_at, updated_at;

-- name: CreateProvisioningUser :one
-- Provisioning identity (US3, iam-application.md CreateProvisioningUser): the
-- password hash commits with the row; lifecycle_state/version are explicit
-- ('provisioning', 1) because 000006 dropped the temporary defaults.
INSERT INTO users (username, password_hash, lifecycle_state, version, account, email)
VALUES ($1, $2, 'provisioning', 1, $3, $4)
RETURNING id, username, password_hash, account, email, lifecycle_state, version,
          created_at, updated_at;

-- name: GetUserByUsername :one
SELECT id, username, password_hash, created_at, updated_at
FROM users
WHERE username = $1;

-- name: GetUserByID :one
SELECT id, username, password_hash, created_at, updated_at
FROM users
WHERE id = $1;

-- name: GetUserByUsernameAuth :one
-- IAM authentication lookup: the credentials plus lifecycle state needed by
-- Login's uniform-failure checks. (The shared GetUserByUsername above predates
-- the lifecycle columns and is left untouched for the legacy services.)
SELECT id, username, password_hash, lifecycle_state
FROM users
WHERE username = $1;

-- name: GetUserByIDFull :one
-- Full identity aggregate for GetIdentity / GetAuthorizationProfile.
SELECT id, username, password_hash, account, email, lifecycle_state, version,
       created_at, updated_at
FROM users
WHERE id = $1;

-- name: GetUserByUsernameForUpdate :one
-- Admin-bootstrap lookup (GrantBuiltInAdminRole): locks the latest user row
-- for the duration of the granting transaction so concurrent grants cannot
-- race the version bump. Password hash is deliberately NOT selected — the
-- bootstrap never touches credentials (least privilege).
SELECT id, username, lifecycle_state, version
FROM users
WHERE username = $1
FOR UPDATE;

-- name: GetUserByIDForUpdate :one
-- DeleteUsers full-batch precheck (iam-application.md DeleteUsers): locks each
-- target row until the deletion transaction commits, so validation and the
-- DELETE cannot race a concurrent update (version drift) or a concurrent
-- delete (row vanishing mid-batch). Password hash is deliberately NOT selected
-- — deletion never touches credentials (least privilege).
SELECT id, username, lifecycle_state, version
FROM users
WHERE id = $1
FOR UPDATE;

-- name: BumpUserVersion :exec
-- Version bump for the admin bootstrap; only called inside a transaction that
-- holds the row lock, so `version = version + 1` is race-free.
UPDATE users
SET version = version + 1
WHERE id = $1;

-- name: ActivateUserCAS :one
-- The only legal transition is provisioning -> active with an expected
-- version (iam-application.md ActivateUser); the single-statement UPDATE is
-- the atomic CAS — zero rows mean the CAS missed and the service
-- distinguishes the failure reason from the current row.
UPDATE users
SET lifecycle_state = 'active', version = version + 1
WHERE id = $1 AND lifecycle_state = 'provisioning' AND version = $2
RETURNING version;

-- name: UpdateManagedUserCAS :one
-- Managed-user profile update (iam-application.md UpdateManagedUser): the
-- statement is the expected-version CAS AND the profile mutation — zero rows
-- mean the CAS missed and the service classifies the failure from the current
-- row. account/email take the request value verbatim (absent -> NULL); the
-- unique username constraint surfaces as a unique violation, never a miss.
-- Role replacement and the optional credential change follow in the same
-- transaction: this UPDATE locks the row until commit, so the password
-- statement below cannot interleave with a concurrent updater.
UPDATE users
SET username = $2, account = $3, email = $4, version = version + 1
WHERE id = $1 AND version = $5
RETURNING version;

-- name: UpdateUserPasswordHash :exec
-- Replaces the stored hash; called only inside the transaction whose
-- UpdateManagedUserCAS already locked the row (blank/absent password simply
-- never calls this — the existing hash is preserved by omission).
UPDATE users
SET password_hash = $2
WHERE id = $1;

-- name: DisableUserCAS :one
-- DisableUser (iam-application.md DisableUser): the only legal transition is
-- active -> disabled with an expected version — provisioning subjects are
-- handled by CompensateProvisioningUser, re-enable is out of scope. Zero rows
-- mean the CAS missed and the service classifies the failure from the current
-- row.
UPDATE users
SET lifecycle_state = 'disabled', version = version + 1
WHERE id = $1 AND lifecycle_state = 'active' AND version = $2
RETURNING version;

-- name: DeleteProvisioningUserCAS :one
-- CompensateProvisioningUser (iam-application.md CompensateProvisioningUser):
-- the single-statement CAS deletes only a still-provisioning subject at the
-- expected version — active/disabled/newer-version rows miss and escalate to
-- reconciliation. sessions/user_roles cascade away; the tombstone version is
-- the deleted prior version + 1.
DELETE FROM users
WHERE id = $1 AND lifecycle_state = 'provisioning' AND version = $2
RETURNING version;

-- name: ListManagedUsers :many
-- Managed-user listing (BatchGetManagedIdentities): provisioning users are
-- excluded; a NULL candidate array means no id filter; empty candidate arrays
-- are short-circuited by the service before reaching the store.
SELECT id, username, password_hash, account, email, lifecycle_state, version,
       created_at, updated_at
FROM users
WHERE lifecycle_state <> 'provisioning'
  AND (array_length($1::bigint[], 1) IS NULL OR id = ANY($1))
  AND ($2::text = '' OR username ILIKE '%' || $2 || '%')
  AND ($3::text = '' OR account ILIKE '%' || $3 || '%')
ORDER BY id
LIMIT $4 OFFSET $5;

-- name: CountManagedUsers :one
SELECT count(*)
FROM users
WHERE lifecycle_state <> 'provisioning'
  AND (array_length($1::bigint[], 1) IS NULL OR id = ANY($1))
  AND ($2::text = '' OR username ILIKE '%' || $2 || '%')
  AND ($3::text = '' OR account ILIKE '%' || $3 || '%');

-- name: GetProvisioningSnapshot :one
-- Metrics snapshot (T074): how many identities are still in the
-- provisioning lifecycle state and when the oldest one was created.
SELECT count(*) AS count, min(created_at) AS oldest_created_at
FROM users
WHERE lifecycle_state = 'provisioning';
