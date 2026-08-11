-- 000010_compensation_budget_and_recovery.up.sql
-- T055 (contracts/consistency-and-compensation.md Durable workflow execution
-- lease/retry + Reconciliation): compensation attempts are bounded by the
-- same 10-attempt / original 24h retry_deadline ceiling as forward attempts,
-- but counted separately (compensation_attempt_count) — compensation
-- reclaims never consume the forward budget. Budget/deadline exhaustion
-- transitions to failed_manual (any possibly-applied subject exclusion stays
-- active); manual reconciliation appends the immutable
-- admin_workflow_recovery_actions ledger (created in 000007) and may only
-- finalize proven receipts or execute explicitly approved compensation.
ALTER TABLE admin_workflows
    ADD COLUMN compensation_attempt_count INTEGER NOT NULL DEFAULT 0,
    ADD CONSTRAINT admin_workflows_compensation_attempts_ck
        CHECK (compensation_attempt_count BETWEEN 0 AND 10);
