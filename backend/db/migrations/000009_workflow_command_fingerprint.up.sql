-- 000009_workflow_command_fingerprint.up.sql
-- The update workflow (T053) persists the U6 participant command fingerprint
-- before issuing IAM.UpdateManagedUser: the worker resume machine at
-- step=organization_updated has no request body (credentials never enter
-- storage), so ResolveCommand can only match the committed receipt against
-- the fingerprint recorded at the U5 boundary. Nil = no pending credential
-- command fingerprint on record (create/delete workflows never set it).
ALTER TABLE admin_workflows
    ADD COLUMN command_fingerprint TEXT;
