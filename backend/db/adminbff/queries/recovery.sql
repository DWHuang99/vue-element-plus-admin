-- recovery.sql
-- Append-only manual-reconciliation ledger (T055;
-- contracts/consistency-and-compensation.md Reconciliation; table DDL in
-- 000007). Every manual recovery action is durably recorded before it
-- executes; only insert/list ports exist — the ledger is immutable by
-- construction, and recovery never resumes a revoked/failed actor's forward
-- request.

-- name: InsertRecoveryAction :one
INSERT INTO admin_workflow_recovery_actions (
    action_id, operation_id, recovery_principal_id, authorization_source,
    approval_id, action_type, reason_code, previous_state, resulting_state,
    safe_result_code, request_id, correlation_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING action_id, operation_id, recovery_principal_id, authorization_source,
          approval_id, action_type, reason_code, previous_state, resulting_state,
          safe_result_code, request_id, correlation_id, created_at;

-- name: ListRecoveryActionsByOperation :many
SELECT action_id, operation_id, recovery_principal_id, authorization_source,
       approval_id, action_type, reason_code, previous_state, resulting_state,
       safe_result_code, request_id, correlation_id, created_at
FROM admin_workflow_recovery_actions
WHERE operation_id = $1
ORDER BY created_at, action_id;
