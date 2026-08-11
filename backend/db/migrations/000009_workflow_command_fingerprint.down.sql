-- 000009_workflow_command_fingerprint.down.sql
ALTER TABLE admin_workflows
    DROP COLUMN command_fingerprint;
