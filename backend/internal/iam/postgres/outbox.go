// outbox.go — T061 IAM OutboxDeliveryPort adapter (contracts/domain-events.md
// §Outbox delivery state).
//
// Delivery runs on its own pool with short transactions: claim selects rows
// FOR UPDATE SKIP LOCKED and persists the lease in the SAME transaction, so
// no producer transaction is ever held across a consumer call. Every
// ack/failure/block is a single-statement claim-token CAS — stale tokens
// match no row and are rejected; failure recording evaluates the epoch
// ceiling atomically inside the one transition statement, so there is no
// reschedule-then-block race.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hdw/vue-element-plus-admin/backend/internal/iam"
	"github.com/hdw/vue-element-plus-admin/backend/internal/iam/postgres/sqlc"
)

// Delivery ceilings per epoch (contract rules 1 and 5): an epoch auto-blocks
// as delivery_exhausted after 20 failed claims or 24h from its start.
const (
	maxEpochAttempts = 20
	epochCeiling     = 24 * time.Hour
)

// OutboxDelivery implements iam.OutboxService on the IAM-owned schema. The
// producer side (EnqueueOutboxEvent) stays on the Store port; delivery is a
// separate adapter so a claim transaction never mixes with a producer write.
type OutboxDelivery struct {
	pool *pgxpool.Pool
	q    *sqlc.Queries
}

var _ iam.OutboxService = (*OutboxDelivery)(nil)

// NewOutboxDelivery wires the delivery adapter to a connection pool.
func NewOutboxDelivery(pool *pgxpool.Pool) *OutboxDelivery {
	return &OutboxDelivery{pool: pool, q: sqlc.New(pool)}
}

// ClaimOutbox picks up to batchSize eligible events (pending with backoff
// elapsed, or leased rows whose lease expired) in one short transaction,
// oldest first. Rows past the epoch ceiling are atomically blocked without a
// lease and WITHOUT burning an attempt; every fresh claim/reclaim persists
// lease owner/expiry/fresh claim token and increments the epoch and total
// attempt counters, so a worker crash after claim still consumes budget.
func (d *OutboxDelivery) ClaimOutbox(ctx context.Context, batchSize int, leaseOwner string, leaseDuration time.Duration) ([]iam.ClaimedEvent, error) {
	if batchSize <= 0 {
		return nil, fmt.Errorf("claim batch size must be positive, got %d", batchSize)
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin claim tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := d.q.WithTx(tx)

	rows, err := q.ListClaimableOutboxEvents(ctx, int32(batchSize))
	if err != nil {
		return nil, fmt.Errorf("list claimable outbox events: %w", err)
	}

	now := time.Now()
	claimed := make([]iam.ClaimedEvent, 0, len(rows))
	for _, row := range rows {
		if row.AttemptCount >= maxEpochAttempts || !row.EpochStartedAt.Time.Add(epochCeiling).After(now) {
			if _, err := q.BlockOutboxExhausted(ctx, row.EventID); err != nil {
				return nil, fmt.Errorf("block exhausted outbox event %s: %w", row.EventID.String(), err)
			}
			continue
		}
		token := uuid.NewString()
		tokenUUID, err := uuidParam(token, "claim token")
		if err != nil {
			return nil, err
		}
		leasedUntil := now.Add(leaseDuration)
		n, err := q.LeaseOutboxEvent(ctx, sqlc.LeaseOutboxEventParams{
			EventID:     row.EventID,
			LeaseOwner:  pgtype.Text{String: leaseOwner, Valid: true},
			LeasedUntil: pgtype.Timestamptz{Time: leasedUntil, Valid: true},
			ClaimToken:  tokenUUID,
		})
		if err != nil {
			return nil, fmt.Errorf("lease outbox event %s: %w", row.EventID.String(), err)
		}
		if n == 0 {
			continue // defensive: the row is locked by the claim select, never expected
		}
		// Observability (T066): a claim that took over an expired lease is a
		// lease recovery (rule 9) — counted in the same short transaction.
		if row.Status == "leased" {
			if err := q.IncrementOutboxMetric(ctx, sqlc.IncrementOutboxMetricParams{
				MetricKey: "lease_recoveries",
				Count:     1,
			}); err != nil {
				return nil, fmt.Errorf("increment lease recovery metric: %w", err)
			}
		}
		var payload map[string]any
		if err := json.Unmarshal(row.Payload, &payload); err != nil {
			return nil, fmt.Errorf("unmarshal outbox payload: %w", err)
		}
		claimed = append(claimed, iam.ClaimedEvent{
			EventID:          row.EventID.String(),
			EventType:        row.EventType,
			EventVersion:     int(row.EventVersion),
			Producer:         row.Producer,
			AggregateType:    row.AggregateType,
			AggregateID:      row.AggregateID,
			AggregateVersion: row.AggregateVersion,
			// pgx decodes timestamptz into the session-local zone; the
			// delivery contract pins OccurredAt to UTC (envelope v1 codec
			// rejects non-UTC), so normalize here at the port boundary.
			OccurredAt:        row.OccurredAt.Time.UTC(),
			CorrelationID:     row.CorrelationID,
			Payload:           payload,
			ClaimToken:        token,
			LeaseOwner:        leaseOwner,
			LeasedUntil:       leasedUntil,
			AttemptCount:      int(row.AttemptCount) + 1,
			TotalAttemptCount: int(row.TotalAttemptCount) + 1,
		})
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit claim tx: %w", err)
	}
	return claimed, nil
}

// AckOutbox CASes the claimed event to published; a stale claim token is
// rejected and changes nothing (contract rule 8).
func (d *OutboxDelivery) AckOutbox(ctx context.Context, eventID, claimToken string) error {
	event, err := uuidParam(eventID, "event id")
	if err != nil {
		return err
	}
	token, err := uuidParam(claimToken, "claim token")
	if err != nil {
		return err
	}
	n, err := d.q.AckOutboxEvent(ctx, sqlc.AckOutboxEventParams{EventID: event, ClaimToken: token})
	if err != nil {
		return fmt.Errorf("ack outbox event: %w", err)
	}
	if n == 0 {
		d.countStaleClaim(ctx)
		return iam.ErrOutboxStaleClaim
	}
	return nil
}

// countStaleClaim best-effort increments the cumulative stale-claim-rejection
// counter (rule 8 observability). Observability never changes delivery
// semantics, so a counter failure is swallowed.
func (d *OutboxDelivery) countStaleClaim(ctx context.Context) {
	if err := d.q.IncrementOutboxMetric(ctx, sqlc.IncrementOutboxMetricParams{
		MetricKey: "stale_claim_rejections",
		Count:     1,
	}); err != nil {
		// observability is best-effort; delivery semantics stay unchanged
		_ = err
	}
}

// RecordOutboxFailure records one failed delivery attempt. A single statement
// atomically evaluates the already-counted attempt/time ceiling and performs
// exactly one transition: back to pending with the safe error and bounded
// backoff, or blocked(delivery_exhausted) — never reschedule-then-block. A
// stale claim token is rejected and changes nothing.
func (d *OutboxDelivery) RecordOutboxFailure(ctx context.Context, eventID, claimToken, safeErrorCode string, nextAvailableAt time.Time) (iam.OutboxDeliveryStatus, error) {
	event, err := uuidParam(eventID, "event id")
	if err != nil {
		return "", err
	}
	token, err := uuidParam(claimToken, "claim token")
	if err != nil {
		return "", err
	}
	status, err := d.q.RecordOutboxFailure(ctx, sqlc.RecordOutboxFailureParams{
		EventID:       event,
		LastErrorCode: pgtype.Text{String: safeErrorCode, Valid: true},
		AvailableAt:   pgtype.Timestamptz{Time: nextAvailableAt, Valid: true},
		ClaimToken:    token,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		d.countStaleClaim(ctx)
		return "", iam.ErrOutboxStaleClaim
	}
	if err != nil {
		return "", fmt.Errorf("record outbox failure: %w", err)
	}
	return deliveryStatus(status), nil
}

// deliveryStatus maps the durable row status back to the domain delivery
// status. RecordOutboxFailure only ever returns pending or blocked, and the
// only way it blocks is the exhausted ceiling — so DB 'blocked' is always
// blocked(delivery_exhausted).
func deliveryStatus(status string) iam.OutboxDeliveryStatus {
	switch status {
	case "pending":
		return iam.OutboxPending
	case "blocked":
		return iam.OutboxBlocked
	}
	return iam.OutboxDeliveryStatus(status)
}

// BlockOutbox durably blocks an event after a non-retryable/unsupported
// contract failure; claim-token CAS, stale tokens rejected.
func (d *OutboxDelivery) BlockOutbox(ctx context.Context, eventID, claimToken, safeReasonCode string) error {
	event, err := uuidParam(eventID, "event id")
	if err != nil {
		return err
	}
	token, err := uuidParam(claimToken, "claim token")
	if err != nil {
		return err
	}
	n, err := d.q.BlockOutboxEvent(ctx, sqlc.BlockOutboxEventParams{
		EventID:           event,
		BlockedReasonCode: pgtype.Text{String: safeReasonCode, Valid: true},
		ClaimToken:        token,
	})
	if err != nil {
		return fmt.Errorf("block outbox event: %w", err)
	}
	if n == 0 {
		d.countStaleClaim(ctx)
		return iam.ErrOutboxStaleClaim
	}
	return nil
}

// RequeueBlockedOutbox makes a blocked event pending again at a delayed
// available_at: delivery_epoch+1, epoch_started_at = available_at, epoch
// attempts reset, total attempts preserved, and an immutable
// iam_outbox_requeues row appended with the complete previous-epoch evidence
// and the trusted recovery identity. Non-blocked events are rejected.
func (d *OutboxDelivery) RequeueBlockedOutbox(ctx context.Context, eventID string, trustedCtx iam.TrustedRecoveryContext, approvedReasonCode string, availableAt time.Time) (int64, error) {
	event, err := uuidParam(eventID, "event id")
	if err != nil {
		return 0, err
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin requeue tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := d.q.WithTx(tx)

	old, err := q.GetOutboxEventByIDForRequeue(ctx, event)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, iam.ErrOutboxNotBlocked
	}
	if err != nil {
		return 0, fmt.Errorf("requeue lookup: %w", err)
	}

	available := pgtype.Timestamptz{Time: availableAt, Valid: true}
	if err := q.RequeueOutboxEvent(ctx, sqlc.RequeueOutboxEventParams{EventID: event, AvailableAt: available}); err != nil {
		return 0, fmt.Errorf("requeue outbox event: %w", err)
	}

	newEpoch := old.DeliveryEpoch + 1
	if err := q.InsertOutboxRequeue(ctx, sqlc.InsertOutboxRequeueParams{
		EventID:                 event,
		DeliveryEpoch:           newEpoch,
		RecoveryPrincipalID:     strconv.FormatInt(trustedCtx.PrincipalID, 10),
		ReasonCode:              approvedReasonCode,
		PreviousEpochStartedAt:  old.EpochStartedAt,
		PreviousEpochDeadlineAt: pgtype.Timestamptz{Time: old.EpochStartedAt.Time.Add(epochCeiling), Valid: true},
		PreviousBlockedAt:       old.BlockedAt,
		PreviousBlockedReason:   old.BlockedReasonCode,
		PreviousLastErrorCode:   old.LastErrorCode,
		PreviousEpochAttempts:   pgtype.Int4{Int32: old.AttemptCount, Valid: true},
		PreviousTotalAttempts:   pgtype.Int8{Int64: old.TotalAttemptCount, Valid: true},
		AuthorizationSource:     textOrNull(trustedCtx.AuthorizationSource),
		ApprovalID:              textOrNull(trustedCtx.ApprovalID),
		RequestID:               textOrNull(trustedCtx.RequestID),
		CorrelationID:           textOrNull(trustedCtx.CorrelationID),
	}); err != nil {
		return 0, fmt.Errorf("insert outbox requeue evidence: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit requeue tx: %w", err)
	}
	return int64(newEpoch), nil
}

// GetOutboxBacklog summarizes the durable backlog for observability
// (contracts/domain-events.md §Required observability): counts by state,
// oldest pending age, oldest live lease, and the cumulative lease-recovery
// and stale-claim-rejection counters.
func (d *OutboxDelivery) GetOutboxBacklog(ctx context.Context) (iam.OutboxBacklog, error) {
	row, err := d.q.GetOutboxBacklog(ctx)
	if err != nil {
		return iam.OutboxBacklog{}, fmt.Errorf("get outbox backlog: %w", err)
	}
	return iam.OutboxBacklog{
		PendingCount:             row.PendingCount,
		LeasedCount:              row.LeasedCount,
		BlockedCount:             row.BlockedCount,
		PublishedCount:           row.PublishedCount,
		OldestPendingAt:          nullableTime(row.OldestPendingAt),
		OldestLeasedUntil:        nullableTime(row.OldestLeasedUntil),
		LeaseRecoveryCount:       row.LeaseRecoveryCount,
		StaleClaimRejectionCount: row.StaleClaimRejectionCount,
	}, nil
}

// --- helpers -----------------------------------------------------------------

// uuidParam parses a UUID text into the pgtype form the generated queries
// use; a malformed id/token is an adapter error, never a query.
func uuidParam(s, what string) (pgtype.UUID, error) {
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		return pgtype.UUID{}, fmt.Errorf("invalid %s %q: %w", what, s, err)
	}
	return u, nil
}

// textOrNull stores empty optional text as SQL NULL.
func textOrNull(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}
