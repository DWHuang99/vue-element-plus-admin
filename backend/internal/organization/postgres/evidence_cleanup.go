// evidence_cleanup.go — T078 Organization evidence-cleanup adapter
// (data-model.md "Retention and cleanup matrix").
//
// Organization evidence is always terminal: command receipts are written in
// the succeeded/rejected state only (schema CHECK) and inbox dedupe rows exist
// only after successful processing — so BlockingUnresolvedCount is always 0
// and the owner-local purge only needs the operational-approval gate and the
// 30-day processing floor. The inbox minimum ("not shorter than retained
// producer evidence") is upheld by the Platform coordinator's
// most-conservative-cutoff agreement across owners, never by cross-owner SQL.
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hdw/vue-element-plus-admin/backend/internal/organization"
	"github.com/hdw/vue-element-plus-admin/backend/internal/organization/postgres/sqlc"
)

// cleanupRetention is the Organization retention floor: evidence is never
// eligible within 30 calendar days of its terminal timestamp.
const cleanupRetention = 30 * 24 * time.Hour

var _ organization.EvidenceCleanupService = (*EvidenceCleanup)(nil)

// EvidenceCleanup implements organization.EvidenceCleanupService on the
// Organization-owned schema. It is a separate adapter from the Store so the
// purge transaction never mixes with a write.
type EvidenceCleanup struct {
	pool *pgxpool.Pool
	q    *sqlc.Queries
}

// NewEvidenceCleanup wires the cleanup adapter to a connection pool.
func NewEvidenceCleanup(pool *pgxpool.Pool) *EvidenceCleanup {
	return &EvidenceCleanup{pool: pool, q: sqlc.New(pool)}
}

// EvaluateEvidenceCleanup returns the dry-run evaluation at cutoff. No rows
// are modified. BlockingUnresolvedCount is always 0 (Organization evidence is
// terminal by construction); the adapter trusts the Platform coordinator to
// hold every owner to the most conservative cross-owner cutoff.
func (c *EvidenceCleanup) EvaluateEvidenceCleanup(ctx context.Context, cutoff time.Time) (organization.EvidenceCleanupEvaluation, error) {
	ts := cutoffPg(cutoff)

	receipts, err := c.q.CountEligibleCommandReceipts(ctx, ts)
	if err != nil {
		return organization.EvidenceCleanupEvaluation{}, fmt.Errorf("count eligible command receipts: %w", err)
	}
	inbox, err := c.q.CountEligibleInboxMessages(ctx, ts)
	if err != nil {
		return organization.EvidenceCleanupEvaluation{}, fmt.Errorf("count eligible inbox messages: %w", err)
	}
	oldest, err := c.q.OldestReplayableEvidenceAt(ctx)
	if err != nil {
		return organization.EvidenceCleanupEvaluation{}, fmt.Errorf("read oldest organization evidence: %w", err)
	}

	var floor *time.Time
	for _, at := range []*time.Time{nullableTime(oldest.OldestReceiptCompletedAt), nullableTime(oldest.OldestProcessedAt)} {
		if at == nil {
			continue
		}
		t := at.Add(cleanupRetention)
		if floor == nil || t.Before(*floor) {
			floor = &t
		}
	}

	return organization.EvidenceCleanupEvaluation{
		EligibleCounts: map[string]int64{
			"organization.command_receipts": receipts,
			"organization.inbox_messages":   inbox,
		},
		BlockingUnresolvedCount: 0,
		OldestReplayableAt:      floor,
	}, nil
}

// PurgeEligibleEvidence deletes the eligible rows in one Organization-local
// transaction. It requires a trusted recovery context with a non-empty
// approval id (the owner-local "explicit operational approval" gate).
// Returns per-table deleted counts.
func (c *EvidenceCleanup) PurgeEligibleEvidence(ctx context.Context, cutoff time.Time, trustedCtx organization.TrustedRecoveryContext) (map[string]int64, error) {
	if trustedCtx.ApprovalID == "" {
		return nil, organization.ErrCleanupApprovalRequired
	}
	ts := cutoffPg(cutoff)

	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin cleanup transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := c.q.WithTx(tx)

	receipts, err := q.PurgeEligibleCommandReceipts(ctx, ts)
	if err != nil {
		return nil, fmt.Errorf("purge eligible command receipts: %w", err)
	}
	inbox, err := q.PurgeEligibleInboxMessages(ctx, ts)
	if err != nil {
		return nil, fmt.Errorf("purge eligible inbox messages: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit cleanup transaction: %w", err)
	}
	return map[string]int64{
		"organization.command_receipts": receipts,
		"organization.inbox_messages":   inbox,
	}, nil
}

// cutoffPg adapts a time.Time to the pgx driver's timestamptz.
func cutoffPg(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

// nullableTime unwraps a nullable timestamptz into a Go time pointer.
func nullableTime(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}
