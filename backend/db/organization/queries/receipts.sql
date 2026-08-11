-- receipts.sql
-- Owner-local command evidence (US2, contracts/organization-application.md).
-- The service commits the receipt in the same Organization transaction as the
-- side effect; a resolved receipt replays the committed result after
-- timeout/crash. Never references IAM tables.

-- name: SaveCommandReceipt :exec
-- SaveCommandReceipt is only reached through resolve-first (no receipt yet),
-- so a conflict is unreachable; DO NOTHING is belt-and-braces against
-- corrupting committed evidence.
INSERT INTO organization_command_receipts (
    operation_id, command_name, request_fingerprint, status, subject_id,
    previous_department_id, previous_membership_version,
    resulting_department_id, resulting_membership_version, error_code,
    completed_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now())
ON CONFLICT (operation_id, command_name) DO NOTHING;

-- name: GetCommandReceipt :one
SELECT operation_id, command_name, request_fingerprint, status, subject_id,
       previous_department_id, previous_membership_version,
       resulting_department_id, resulting_membership_version, error_code,
       created_at, completed_at
FROM organization_command_receipts
WHERE operation_id = $1 AND command_name = $2;
