-- 000007_process_state_tables.up.sql
-- Process/consistency state tables (plan Phase 2, T012), owned per
-- data-model.md Ownership matrix:
--   IAM:          iam_command_receipts, iam_outbox_events, iam_outbox_requeues
--   Organization: organization_command_receipts, organization_inbox_messages
--   Admin BFF:    admin_workflows, admin_workflow_subjects, admin_workflow_recovery_actions
--
-- None of these are user/department/role facts: they hold durable command
-- proof, outbox delivery state, inbox dedupe and workflow/compensation state.
-- user_id/subject references are opaque IAM subject IDs, never FKs into users.

-- ============================== IAM domain ==============================

-- Durable proof for an IAM workflow command, written in the same transaction
-- as the command side effect. Never stores credentials or their digests.
CREATE TABLE iam_command_receipts (
    operation_id      UUID        NOT NULL,
    command_name      TEXT        NOT NULL,
    request_fingerprint TEXT      NOT NULL,
    status            TEXT        NOT NULL,
    subject_id        BIGINT,
    result            JSONB,
    error_code        TEXT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at      TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (operation_id, command_name),
    CHECK (status IN ('succeeded', 'rejected'))
);

-- Outbox for IAM domain events (first value: iam.user.deleted). Claims use
-- lease + claim_token CAS; every retry/backoff/block is a single transition.
CREATE TABLE iam_outbox_events (
    event_id           UUID        PRIMARY KEY,
    event_type         TEXT        NOT NULL,
    event_version      INTEGER     NOT NULL,
    producer           TEXT        NOT NULL,
    aggregate_type     TEXT        NOT NULL,
    aggregate_id       TEXT        NOT NULL,
    aggregate_version  BIGINT      NOT NULL,
    payload            JSONB       NOT NULL,
    correlation_id     TEXT        NOT NULL,
    occurred_at        TIMESTAMPTZ NOT NULL,
    status             TEXT        NOT NULL,
    available_at       TIMESTAMPTZ NOT NULL,
    delivery_epoch     INTEGER     NOT NULL DEFAULT 1,
    epoch_started_at   TIMESTAMPTZ NOT NULL,
    attempt_count      INTEGER     NOT NULL DEFAULT 0,
    total_attempt_count BIGINT     NOT NULL DEFAULT 0,
    lease_owner        TEXT,
    leased_until       TIMESTAMPTZ,
    claim_token        UUID,
    last_error_code    TEXT,
    blocked_at         TIMESTAMPTZ,
    blocked_reason_code TEXT,
    published_at       TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (status IN ('pending', 'leased', 'published', 'blocked')),
    CHECK (delivery_epoch >= 1),
    CHECK (attempt_count >= 0),
    CHECK (total_attempt_count >= 0),
    CHECK (available_at >= epoch_started_at),
    -- status/lease/blocked/published field combination stays consistent.
    CHECK (
        (status = 'pending'   AND lease_owner IS NULL AND leased_until IS NULL AND claim_token IS NULL
                               AND blocked_at IS NULL AND blocked_reason_code IS NULL AND published_at IS NULL)
     OR (status = 'leased'    AND lease_owner IS NOT NULL AND leased_until IS NOT NULL AND claim_token IS NOT NULL
                               AND blocked_at IS NULL AND blocked_reason_code IS NULL AND published_at IS NULL)
     OR (status = 'published' AND published_at IS NOT NULL
                               AND lease_owner IS NULL AND leased_until IS NULL AND claim_token IS NULL
                               AND blocked_at IS NULL AND blocked_reason_code IS NULL)
     OR (status = 'blocked'   AND blocked_at IS NOT NULL AND blocked_reason_code IS NOT NULL
                               AND lease_owner IS NULL AND leased_until IS NULL AND claim_token IS NULL
                               AND published_at IS NULL)
    )
);

-- Claim scan: eligible pending rows ordered by availability then creation.
CREATE INDEX idx_iam_outbox_events_claim
    ON iam_outbox_events (available_at, created_at)
    WHERE status = 'pending';

-- Lease recovery scan: expired leases reclaimable by any worker.
CREATE INDEX idx_iam_outbox_events_lease_recovery
    ON iam_outbox_events (leased_until)
    WHERE status = 'leased';

CREATE INDEX idx_iam_outbox_events_blocked
    ON iam_outbox_events (blocked_at)
    WHERE status = 'blocked';

CREATE INDEX idx_iam_outbox_events_published
    ON iam_outbox_events (published_at)
    WHERE status = 'published';

-- Aggregate dedupe/tombstone evidence lookup.
CREATE INDEX idx_iam_outbox_events_aggregate
    ON iam_outbox_events (aggregate_type, aggregate_id, aggregate_version);

-- Immutable audit trail for audited requeues (one row per resulting epoch).
CREATE TABLE iam_outbox_requeues (
    event_id                     UUID        NOT NULL REFERENCES iam_outbox_events(event_id),
    delivery_epoch               INTEGER     NOT NULL,
    recovery_principal_id        TEXT        NOT NULL,
    reason_code                  TEXT        NOT NULL,
    previous_epoch_started_at    TIMESTAMPTZ,
    previous_epoch_deadline_at   TIMESTAMPTZ,
    previous_blocked_at          TIMESTAMPTZ,
    previous_blocked_reason      TEXT,
    previous_last_error_code     TEXT,
    previous_epoch_attempts      INTEGER,
    previous_total_attempts      BIGINT,
    authorization_source         TEXT,
    approval_id                  TEXT,
    request_id                   TEXT,
    correlation_id               TEXT,
    requeued_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (event_id, delivery_epoch),
    CHECK (delivery_epoch >= 1)
);

-- =========================== Organization domain ==========================

-- Same owner-local receipt pattern as IAM; membership receipts persist
-- previous/resulting department+version so timeout resolution and
-- compensation never infer state from a later read.
CREATE TABLE organization_command_receipts (
    operation_id               UUID        NOT NULL,
    command_name               TEXT        NOT NULL,
    request_fingerprint        TEXT        NOT NULL,
    status                     TEXT        NOT NULL,
    subject_id                 BIGINT,
    result                     JSONB,
    error_code                 TEXT,
    previous_department_id     BIGINT,
    previous_membership_version BIGINT,
    resulting_department_id    BIGINT,
    resulting_membership_version BIGINT,
    created_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at               TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (operation_id, command_name),
    CHECK (status IN ('succeeded', 'rejected')),
    CHECK (previous_membership_version IS NULL OR previous_membership_version > 0),
    CHECK (resulting_membership_version IS NULL OR resulting_membership_version > 0)
);

-- Inbox dedupe for consumed IAM events; rows exist only for successful
-- processing (side effect and dedupe row commit in one transaction).
CREATE TABLE organization_inbox_messages (
    event_id         UUID        NOT NULL,
    handler_name     TEXT        NOT NULL,
    event_type       TEXT        NOT NULL,
    event_version    INTEGER     NOT NULL,
    aggregate_id     TEXT        NOT NULL,
    aggregate_version BIGINT     NOT NULL,
    received_at      TIMESTAMPTZ NOT NULL,
    processed_at     TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (event_id, handler_name)
);

-- ============================ Admin BFF domain ===========================

-- Cross-module workflow state for idempotency, steps and compensation.
-- Not a source of truth for users/departments/roles.
CREATE TABLE admin_workflows (
    operation_id          UUID        PRIMARY KEY,
    operation_type        TEXT        NOT NULL,
    idempotency_key       TEXT,
    request_fingerprint   TEXT        NOT NULL,
    actor_user_id         BIGINT      NOT NULL,
    subject_user_id       BIGINT,
    state                 TEXT        NOT NULL,
    current_step          TEXT        NOT NULL,
    compensation_state    TEXT        NOT NULL DEFAULT 'not_required',
    expected_iam_version  BIGINT,
    resulting_iam_version BIGINT,
    previous_department_id BIGINT,
    previous_membership_version BIGINT,
    applied_department_id BIGINT,
    applied_membership_version BIGINT,
    result                JSONB,
    last_error_code       TEXT,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    attempt_count         INTEGER     NOT NULL DEFAULT 0,
    next_retry_at         TIMESTAMPTZ,
    client_input_deadline_at TIMESTAMPTZ,
    retry_deadline_at     TIMESTAMPTZ NOT NULL,
    lease_owner           TEXT,
    leased_until          TIMESTAMPTZ,
    claim_token           UUID,
    completed_at          TIMESTAMPTZ,
    CHECK (operation_type IN ('managed_user.create', 'managed_user.update', 'managed_user.delete')),
    CHECK (state IN ('pending', 'running', 'awaiting_client_input', 'compensating',
                     'succeeded', 'rejected', 'failed_retryable', 'failed_manual')),
    CHECK (compensation_state IN ('not_required', 'pending', 'running', 'succeeded', 'failed')),
    CHECK (attempt_count >= 0),
    CHECK (retry_deadline_at >= created_at),
    -- Only actively claimed running/compensating rows hold lease state.
    CHECK (
        (state IN ('running', 'compensating')
         AND lease_owner IS NOT NULL AND leased_until IS NOT NULL AND claim_token IS NOT NULL)
     OR (state NOT IN ('running', 'compensating')
         AND lease_owner IS NULL AND leased_until IS NULL AND claim_token IS NULL)
    ),
    CHECK (expected_iam_version IS NULL OR expected_iam_version > 0),
    CHECK (resulting_iam_version IS NULL OR resulting_iam_version > 0),
    CHECK (previous_membership_version IS NULL OR previous_membership_version > 0),
    CHECK (applied_membership_version IS NULL OR applied_membership_version > 0)
);

-- Legacy idempotency compatibility: same actor + key + type → same workflow.
CREATE UNIQUE INDEX admin_workflows_idempotency_unique
    ON admin_workflows (operation_type, actor_user_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

-- Per-subject workflow state (single or batch), enabling restart recovery
-- and per-subject exclusion.
CREATE TABLE admin_workflow_subjects (
    operation_id              UUID        NOT NULL REFERENCES admin_workflows(operation_id),
    subject_user_id           BIGINT      NOT NULL,
    expected_iam_version      BIGINT      NOT NULL,
    resulting_tombstone_version BIGINT,
    active                    BOOLEAN     NOT NULL DEFAULT true,
    PRIMARY KEY (operation_id, subject_user_id),
    CHECK (expected_iam_version > 0)
);

-- No overlapping active workflow per subject (including each delete-batch member).
CREATE UNIQUE INDEX admin_workflow_subjects_active_unique
    ON admin_workflow_subjects (subject_user_id)
    WHERE active;

-- Immutable append-only manual-recovery evidence. Never authorizes resuming
-- a forward business request.
CREATE TABLE admin_workflow_recovery_actions (
    action_id             UUID        PRIMARY KEY,
    operation_id          UUID        NOT NULL REFERENCES admin_workflows(operation_id),
    recovery_principal_id TEXT        NOT NULL,
    authorization_source  TEXT        NOT NULL,
    approval_id           TEXT,
    action_type           TEXT        NOT NULL,
    reason_code           TEXT        NOT NULL,
    previous_state        TEXT        NOT NULL,
    resulting_state       TEXT        NOT NULL,
    safe_result_code      TEXT,
    request_id            TEXT,
    correlation_id        TEXT,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (action_type IN ('receipt_finalize', 'approved_compensation', 'reconciliation'))
);
