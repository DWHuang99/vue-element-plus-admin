-- 000011_delivery_observability — T066 (contracts/domain-events.md §Failure and
-- reconciliation Required observability). Owner-local cumulative counters so
-- delivery/inbox health is observable without replaying history: IAM counts
-- lease recoveries (expired-lease reclaims) and stale-claim rejections; the
-- Organization side counts processed / duplicate / handler-failure inbox
-- outcomes. Key-value rows are upserted atomically by the owners; no other
-- service reads or writes the other's table (ownership matrix).

CREATE TABLE iam_outbox_metrics (
    metric_key TEXT   PRIMARY KEY,
    count      BIGINT NOT NULL DEFAULT 0 CHECK (count >= 0)
);

INSERT INTO iam_outbox_metrics(metric_key, count) VALUES
    ('lease_recoveries', 0),
    ('stale_claim_rejections', 0);

CREATE TABLE organization_inbox_metrics (
    metric_key TEXT   PRIMARY KEY,
    count      BIGINT NOT NULL DEFAULT 0 CHECK (count >= 0)
);

INSERT INTO organization_inbox_metrics(metric_key, count) VALUES
    ('inbox_processed', 0),
    ('inbox_duplicates', 0),
    ('inbox_handler_failures', 0);
