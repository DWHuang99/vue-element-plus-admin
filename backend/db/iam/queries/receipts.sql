-- receipts.sql
-- IAM owner-local command evidence (US3, contracts/consistency-and-compensation.md).
-- The service commits the receipt in the same IAM transaction as the side
-- effect; a resolved receipt replays the committed result after timeout/crash.
-- result holds transport-neutral safe values only — never credentials.
-- Never references Organization or BFF tables.

-- name: SaveCommandReceipt :exec
-- Only reached through resolve-first (no receipt yet), so a conflict is
-- unreachable; DO NOTHING is belt-and-braces against corrupting committed
-- evidence.
INSERT INTO iam_command_receipts (
    operation_id, command_name, request_fingerprint, status, subject_id,
    result, error_code, completed_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, now())
ON CONFLICT (operation_id, command_name) DO NOTHING;

-- name: GetCommandReceipt :one
SELECT operation_id, command_name, request_fingerprint, status, subject_id,
       result, error_code, created_at, completed_at
FROM iam_command_receipts
WHERE operation_id = $1 AND command_name = $2;
