// evidence_cleanup.go — T078 IAM evidence-cleanup adapter
// (data-model.md "Retention and cleanup matrix").
//
// EvaluateEvidenceCleanup is a pure dry-run: per-table eligible counts at the
// requested cutoff (30 calendar days after each row's terminal timestamp,
// published outbox rows held until 30 days after publish AND after the last
// requeue), the blocking unresolved count (any pending/leased/blocked outbox
// event) and the earliest cutoff at which any IAM evidence could become
// eligible. PurgeEligibleEvidence is the approved owner-local purge: it
// re-verifies safety INSIDE its own IAM-only transaction (any unresolved
// outbox event aborts with iam.ErrEvidenceCleanupBlocked and deletes nothing
// — "uncertain → delete nothing"), requires a non-empty approval id, and
// deletes requeues before their events so the FK never blocks the deletion.
// No runtime cross-owner SQL ever runs here.
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hdw/vue-element-plus-admin/backend/internal/iam"
	"github.com/hdw/vue-element-plus-admin/backend/internal/iam/postgres/sqlc"
)

// cleanupRetention is the retention floor shared by every IAM owner-local
// predicate: evidence is never eligible within 30 calendar days of its
// terminal timestamp.
const cleanupRetention = 30 * 24 * time.Hour

var _ iam.EvidenceCleanupService = (*EvidenceCleanup)(nil)

// EvidenceCleanup implements iam.EvidenceCleanupService on the IAM-owned
// schema. Like OutboxDelivery it is a separate adapter from the Store so the
// port surface stays small and the purge transaction never mixes with a
// producer write.
type EvidenceCleanup struct {
	pool *pgxpool.Pool
	q    *sqlc.Queries
}

// NewEvidenceCleanup wires the cleanup adapter to a connection pool.
func NewEvidenceCleanup(pool *pgxpool.Pool) *EvidenceCleanup {
	return &EvidenceCleanup{pool: pool, q: sqlc.New(pool)}
}

// EvaluateEvidenceCleanup returns the conservative dry-run evaluation at
// cutoff. No rows are modified. OldestReplayableAt is min(oldest receipt
// completion, oldest publish) + 30 days — the earliest cutoff that deletes
// anything; nil when no terminal/published IAM evidence exists.
func (c *EvidenceCleanup) EvaluateEvidenceCleanup(ctx context.Context, cutoff time.Time) (iam.EvidenceCleanupEvaluation, error) {
	ts := cutoffPg(cutoff)

	unresolved, err := c.q.CountUnresolvedOutboxEvents(ctx)
	if err != nil {
		return iam.EvidenceCleanupEvaluation{}, fmt.Errorf("count unresolved outbox events: %w", err)
	}
	receipts, err := c.q.CountEligibleCommandReceipts(ctx, ts)
	if err != nil {
		return iam.EvidenceCleanupEvaluation{}, fmt.Errorf("count eligible command receipts: %w", err)
	}
	events, err := c.q.CountEligiblePublishedOutboxEvents(ctx, ts)
	if err != nil {
		return iam.EvidenceCleanupEvaluation{}, fmt.Errorf("count eligible outbox events: %w", err)
	}
	requeues, err := c.q.CountEligibleOutboxRequeues(ctx, ts)
	if err != nil {
		return iam.EvidenceCleanupEvaluation{}, fmt.Errorf("count eligible outbox requeues: %w", err)
	}
	oldest, err := c.q.OldestReplayableEvidenceAt(ctx)
	if err != nil {
		return iam.EvidenceCleanupEvaluation{}, fmt.Errorf("read oldest IAM evidence: %w", err)
	}

	var floor *time.Time
	for _, at := range []*time.Time{nullableTime(oldest.OldestReceiptCompletedAt), nullableTime(oldest.OldestPublishedAt)} {
		if at == nil {
			continue
		}
		t := at.Add(cleanupRetention)
		if floor == nil || t.Before(*floor) {
			floor = &t
		}
	}

	return iam.EvidenceCleanupEvaluation{
		EligibleCounts: map[string]int64{
			"iam.command_receipts": receipts,
			"iam.outbox_events":    events,
			"iam.outbox_requeues":  requeues,
		},
		BlockingUnresolvedCount: unresolved,
		OldestReplayableAt:      floor,
	}, nil
}

// PurgeEligibleEvidence deletes the eligible rows in one IAM-local
// transaction. It requires a trusted recovery context with a non-empty
// approval id (the owner-local "explicit operational approval" gate) and
// re-checks that no outbox event is unresolved inside the same transaction,
// so a concurrent producer can never race the purge into deleting protected
// evidence. Returns per-table deleted counts.
func (c *EvidenceCleanup) PurgeEligibleEvidence(ctx context.Context, cutoff time.Time, trustedCtx iam.TrustedRecoveryContext) (map[string]int64, error) {
	if trustedCtx.ApprovalID == "" {
		return nil, iam.ErrCleanupApprovalRequired
	}
	ts := cutoffPg(cutoff)

	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin cleanup transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := c.q.WithTx(tx)

	unresolved, err := q.CountUnresolvedOutboxEvents(ctx)
	if err != nil {
		return nil, fmt.Errorf("cleanup unresolved recheck: %w", err)
	}
	if unresolved > 0 {
		return nil, iam.ErrEvidenceCleanupBlocked
	}

	requeues, err := q.PurgeEligibleOutboxRequeues(ctx, ts)
	if err != nil {
		return nil, fmt.Errorf("purge eligible outbox requeues: %w", err)
	}
	events, err := q.PurgeEligiblePublishedOutboxEvents(ctx, ts)
	if err != nil {
		return nil, fmt.Errorf("purge eligible outbox events: %w", err)
	}
	receipts, err := q.PurgeEligibleCommandReceipts(ctx, ts)
	if err != nil {
		return nil, fmt.Errorf("purge eligible command receipts: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit cleanup transaction: %w", err)
	}
	return map[string]int64{
		"iam.command_receipts": receipts,
		"iam.outbox_events":    events,
		"iam.outbox_requeues":  requeues,
	}, nil
}

// cutoffPg adapts a time.Time to the pgx driver's timestamptz.
func cutoffPg(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}
