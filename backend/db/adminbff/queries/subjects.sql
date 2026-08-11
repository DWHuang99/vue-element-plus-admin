-- subjects.sql
-- Per-subject workflow rows (single or batch targets). The partial unique
-- index admin_workflow_subjects_active_unique enforces the no-overlapping-
-- active-workflow exclusion: a unique violation during InsertWorkflowSubject
-- surfaces as ErrSubjectExcluded (OPERATION_IN_PROGRESS) and rejects the
-- whole batch.

-- name: InsertWorkflowSubject :one
-- One active subject row per validated target/version. A re-entry of the SAME
-- workflow (crash recovery between the iam_validated persist and the insert)
-- is idempotent: the conflict index targets the subject, so the DO UPDATE
-- clause narrows the conflict to the same operation_id and returns the row.
-- An active row owned by ANOTHER workflow leaves zero rows (ErrSubjectExcluded
-- -> OPERATION_IN_PROGRESS) and rejects the whole batch.
INSERT INTO admin_workflow_subjects (operation_id, subject_user_id, expected_iam_version)
VALUES ($1, $2, $3)
ON CONFLICT (subject_user_id) WHERE active
DO UPDATE SET expected_iam_version = EXCLUDED.expected_iam_version
WHERE admin_workflow_subjects.operation_id = EXCLUDED.operation_id
RETURNING operation_id;

-- name: UpdateSubjectResult :exec
-- D5: persist the per-subject tombstone result and release the exclusion.
UPDATE admin_workflow_subjects
SET resulting_tombstone_version = $3, active = false
WHERE operation_id = $1 AND subject_user_id = $2;

-- name: ReleaseSubjectExclusion :exec
-- Mark a subject row inactive without a tombstone: convergence after
-- compensation (rejected / OPERATION_EXPIRED paths) releases the exclusion.
UPDATE admin_workflow_subjects
SET active = false
WHERE operation_id = $1 AND subject_user_id = $2;

-- name: ListSubjectsByOperation :many
SELECT operation_id, subject_user_id, expected_iam_version,
       resulting_tombstone_version, active
FROM admin_workflow_subjects
WHERE operation_id = $1
ORDER BY subject_user_id;
