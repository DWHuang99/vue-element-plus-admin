-- 000007_process_state_tables.down.sql
-- Revert 000007 up in reverse dependency order: child tables (FKs) first,
-- then the state tables that reference them.
DROP TABLE IF EXISTS admin_workflow_recovery_actions;

DROP TABLE IF EXISTS admin_workflow_subjects;

DROP TABLE IF EXISTS admin_workflows;

DROP TABLE IF EXISTS organization_inbox_messages;

DROP TABLE IF EXISTS organization_command_receipts;

DROP TABLE IF EXISTS iam_outbox_requeues;

DROP TABLE IF EXISTS iam_outbox_events;

DROP TABLE IF EXISTS iam_command_receipts;
