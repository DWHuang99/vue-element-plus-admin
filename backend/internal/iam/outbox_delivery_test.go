// IAM OutboxDeliveryPort contract tests (contracts/domain-events.md §Outbox
// delivery state; task T061 — 契约先行, written before the adapter).
//
// Written before the adapter implementation: this suite pins the delivery
// state machine on a real PostgreSQL through the public constructor
// postgres.NewOutboxDelivery, so the T061 adapter is driven to green by it.
// It lives in the external test package iam_test because it imports the
// adapter (internal/iam/postgres), which itself imports iam.
package iam_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hdw/vue-element-plus-admin/backend/internal/iam"
	"github.com/hdw/vue-element-plus-admin/backend/internal/iam/postgres"
)

// outboxHarness wires the delivery adapter to a fresh migrated database plus
// direct-sql access for seeding and cross-checking DB truth.
type outboxHarness struct {
	pool *pgxpool.Pool
	dlv  iam.OutboxService
}

func newOutboxHarness(t *testing.T) *outboxHarness {
	t.Helper()
	ctx := context.Background()
	connStr := freshDB(t)
	migrateToHead(t, connStr)

	pool, err := pgxpool.New(ctx, connStr)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	return &outboxHarness{pool: pool, dlv: postgres.NewOutboxDelivery(pool)}
}

// outboxSeed controls every durable field of a seeded event row; empty values
// fall back to contract defaults that satisfy the DDL CHECKs.
type outboxSeed struct {
	eventID          string
	eventType        string // default "iam.user.deleted"
	eventVersion     int    // default 1
	producer         string // default "iam"
	aggregateType    string // default "user"
	aggregateID      string // default "42"
	aggregateVersion int64  // default 1
	correlationID    string // default "contract-test-correlation"
	payload          string // default `{"user_id":42}`
	status           string // default "pending"
	availableAt      *time.Time
	epoch            int
	epochStartedAt   *time.Time
	attemptCount     int
	totalAttempts    int64
	leaseOwner       *string
	leasedUntil      *time.Time
	claimToken       *string
	lastErrorCode    *string
	blockedAt        *time.Time
	blockedReason    *string
	publishedAt      *time.Time
}

// seedOutboxEvent inserts one row with defaults that satisfy the DDL CHECKs.
func (h *outboxHarness) seedOutboxEvent(t *testing.T, s outboxSeed) string {
	t.Helper()
	now := time.Now().UTC()
	eventID := s.eventID
	if eventID == "" {
		eventID = uuid.NewString()
	}
	status := s.status
	if status == "" {
		status = "pending"
	}
	availableAt := now
	if s.availableAt != nil {
		availableAt = *s.availableAt
	}
	epochStartedAt := now
	if s.epochStartedAt != nil {
		epochStartedAt = *s.epochStartedAt
	}
	if availableAt.Before(epochStartedAt) {
		availableAt = epochStartedAt // satisfy CHECK (available_at >= epoch_started_at)
	}
	payload := s.payload
	if payload == "" {
		payload = `{"user_id":42}`
	}
	eventType := s.eventType
	if eventType == "" {
		eventType = "iam.user.deleted"
	}
	version := s.eventVersion
	if version == 0 {
		version = 1
	}
	producer := s.producer
	if producer == "" {
		producer = "iam"
	}
	aggType := s.aggregateType
	if aggType == "" {
		aggType = "user"
	}
	aggID := s.aggregateID
	if aggID == "" {
		aggID = "42"
	}
	aggVersion := s.aggregateVersion
	if aggVersion == 0 {
		aggVersion = 1
	}
	corr := s.correlationID
	if corr == "" {
		corr = "contract-test-correlation"
	}
	epoch := s.epoch
	if epoch == 0 {
		epoch = 1
	}
	_, err := h.pool.Exec(context.Background(), `
		INSERT INTO iam_outbox_events (
		    event_id, event_type, event_version, producer, aggregate_type, aggregate_id,
		    aggregate_version, payload, correlation_id, occurred_at, status, available_at,
		    delivery_epoch, epoch_started_at, attempt_count, total_attempt_count,
		    lease_owner, leased_until, claim_token, last_error_code, blocked_at,
		    blocked_reason_code, published_at
		) VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $10, $11, $12,
		          $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23)`,
		eventID, eventType, version, producer, aggType, aggID, aggVersion, payload,
		corr, now, status, availableAt, epoch, epochStartedAt, s.attemptCount,
		s.totalAttempts, s.leaseOwner, s.leasedUntil, s.claimToken, s.lastErrorCode,
		s.blockedAt, s.blockedReason, s.publishedAt)
	require.NoError(t, err, "seed outbox event")
	return eventID
}

// outboxRowState is the durable state of one event row after a transition.
type outboxRowState struct {
	status         string
	availableAt    time.Time
	deliveryEpoch  int
	epochStartedAt time.Time
	attemptCount   int
	totalAttempts  int64
	leaseOwner     *string
	leasedUntil    *time.Time
	claimToken     *string
	lastErrorCode  *string
	blockedAt      *time.Time
	blockedReason  *string
	publishedAt    *time.Time
}

func (h *outboxHarness) outboxRow(t *testing.T, eventID string) outboxRowState {
	t.Helper()
	var r outboxRowState
	row := h.pool.QueryRow(context.Background(), `
		SELECT status, available_at, delivery_epoch, epoch_started_at, attempt_count,
		       total_attempt_count, lease_owner, leased_until, claim_token::text,
		       last_error_code, blocked_at, blocked_reason_code, published_at
		FROM iam_outbox_events WHERE event_id = $1::uuid`, eventID)
	require.NoError(t, row.Scan(&r.status, &r.availableAt, &r.deliveryEpoch,
		&r.epochStartedAt, &r.attemptCount, &r.totalAttempts, &r.leaseOwner,
		&r.leasedUntil, &r.claimToken, &r.lastErrorCode, &r.blockedAt, &r.blockedReason,
		&r.publishedAt))
	return r
}

func (h *outboxHarness) requeueRows(t *testing.T, eventID string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, h.pool.QueryRow(context.Background(),
		"SELECT count(*) FROM iam_outbox_requeues WHERE event_id = $1::uuid", eventID).Scan(&n))
	return n
}

// expireLease forces the row's lease into the past (simulating a crashed worker
// whose lease lapsed).
func (h *outboxHarness) expireLease(t *testing.T, eventID string) {
	t.Helper()
	_, err := h.pool.Exec(context.Background(),
		"UPDATE iam_outbox_events SET leased_until = now() - interval '1 minute' WHERE event_id = $1::uuid",
		eventID)
	require.NoError(t, err)
}

// --- Contract: claim -------------------------------------------------------

func TestClaim_FreshPendingLeasedWithFullEnvelope(t *testing.T) {
	h := newOutboxHarness(t)
	id := h.seedOutboxEvent(t, outboxSeed{payload: `{"user_id":7}`})

	events, err := h.dlv.ClaimOutbox(context.Background(), 10, "worker-1", 30*time.Second)
	require.NoError(t, err)
	require.Len(t, events, 1)

	e := events[0]
	assert.Equal(t, id, e.EventID)
	assert.Equal(t, "iam.user.deleted", e.EventType)
	assert.Equal(t, 1, e.EventVersion)
	assert.Equal(t, "iam", e.Producer)
	assert.Equal(t, "user", e.AggregateType)
	assert.Equal(t, "42", e.AggregateID)
	assert.Equal(t, int64(1), e.AggregateVersion)
	assert.Equal(t, "contract-test-correlation", e.CorrelationID)
	assert.Equal(t, float64(7), e.Payload["user_id"])
	assert.False(t, e.OccurredAt.IsZero())
	assert.Equal(t, "worker-1", e.LeaseOwner)
	assert.True(t, e.LeasedUntil.After(time.Now()))
	assert.Len(t, e.ClaimToken, 36) // UUID
	assert.Equal(t, 1, e.AttemptCount)
	assert.Equal(t, 1, e.TotalAttemptCount)

	row := h.outboxRow(t, id)
	assert.Equal(t, "leased", row.status)
	assert.Equal(t, "worker-1", *row.leaseOwner)
	require.NotNil(t, row.claimToken)
	assert.Equal(t, e.ClaimToken, *row.claimToken)
	assert.Equal(t, 1, row.attemptCount)
	assert.Equal(t, int64(1), row.totalAttempts)
	assert.Nil(t, row.blockedAt)
	assert.Nil(t, row.publishedAt)
}

func TestClaim_NotYetAvailableSkipped(t *testing.T) {
	h := newOutboxHarness(t)
	future := time.Now().Add(time.Hour)
	id := h.seedOutboxEvent(t, outboxSeed{availableAt: &future})

	events, err := h.dlv.ClaimOutbox(context.Background(), 10, "worker-1", 30*time.Second)
	require.NoError(t, err)
	assert.Empty(t, events)

	row := h.outboxRow(t, id)
	assert.Equal(t, "pending", row.status)
	assert.Equal(t, 0, row.attemptCount)
}

func TestClaim_BatchSizeRespected(t *testing.T) {
	h := newOutboxHarness(t)
	a := h.seedOutboxEvent(t, outboxSeed{})
	b := h.seedOutboxEvent(t, outboxSeed{})
	c := h.seedOutboxEvent(t, outboxSeed{})

	events, err := h.dlv.ClaimOutbox(context.Background(), 2, "worker-1", 30*time.Second)
	require.NoError(t, err)
	require.Len(t, events, 2)

	got := map[string]bool{}
	for _, e := range events {
		got[e.EventID] = true
	}
	assert.True(t, got[a] && got[b] && !got[c])
	assert.Equal(t, "pending", h.outboxRow(t, c).status)
}

func TestClaim_ExpiredLeaseReclaimCountsAttemptCrashBudget(t *testing.T) {
	// Worker crashed after claim: the first claim already counted. After lease
	// expiry another worker reclaims and counts again; the stale token cannot
	// ack (contract rule 8/9, Checkpoint F window 9).
	h := newOutboxHarness(t)
	id := h.seedOutboxEvent(t, outboxSeed{})

	first, err := h.dlv.ClaimOutbox(context.Background(), 10, "worker-1", 30*time.Second)
	require.NoError(t, err)
	require.Len(t, first, 1)

	h.expireLease(t, id)

	second, err := h.dlv.ClaimOutbox(context.Background(), 10, "worker-2", 30*time.Second)
	require.NoError(t, err)
	require.Len(t, second, 1)
	assert.NotEqual(t, first[0].ClaimToken, second[0].ClaimToken)
	assert.Equal(t, 2, second[0].AttemptCount) // crash-after-claim already burned budget
	assert.Equal(t, 2, second[0].TotalAttemptCount)

	// Stale original token cannot ack or record failure.
	err = h.dlv.AckOutbox(context.Background(), id, first[0].ClaimToken)
	assert.ErrorIs(t, err, iam.ErrOutboxStaleClaim)
	_, err = h.dlv.RecordOutboxFailure(context.Background(), id, first[0].ClaimToken, "consumer_error", time.Now().Add(time.Minute))
	assert.ErrorIs(t, err, iam.ErrOutboxStaleClaim)

	// Fresh token still works.
	require.NoError(t, h.dlv.AckOutbox(context.Background(), id, second[0].ClaimToken))
	assert.Equal(t, "published", h.outboxRow(t, id).status)
}

func TestClaim_EpochPast24hBlockedWithoutLease(t *testing.T) {
	// Row past epoch_started_at+24h is atomically blocked during the claim
	// scan WITHOUT a lease and WITHOUT burning an attempt (no consumer call).
	h := newOutboxHarness(t)
	past := time.Now().Add(-25 * time.Hour)
	id := h.seedOutboxEvent(t, outboxSeed{availableAt: &past, epochStartedAt: &past})

	events, err := h.dlv.ClaimOutbox(context.Background(), 10, "worker-1", 30*time.Second)
	require.NoError(t, err)
	assert.Empty(t, events)

	row := h.outboxRow(t, id)
	assert.Equal(t, "blocked", row.status)
	assert.Equal(t, "delivery_exhausted", *row.blockedReason)
	assert.Nil(t, row.leaseOwner)
	assert.Nil(t, row.claimToken)
	assert.Equal(t, 0, row.attemptCount)
	assert.Equal(t, int64(0), row.totalAttempts)
}

func TestClaim_AttemptCeilingBlockedWithoutLease(t *testing.T) {
	h := newOutboxHarness(t)
	id := h.seedOutboxEvent(t, outboxSeed{attemptCount: 20, totalAttempts: 20})

	events, err := h.dlv.ClaimOutbox(context.Background(), 10, "worker-1", 30*time.Second)
	require.NoError(t, err)
	assert.Empty(t, events)

	row := h.outboxRow(t, id)
	assert.Equal(t, "blocked", row.status)
	assert.Equal(t, "delivery_exhausted", *row.blockedReason)
	assert.Nil(t, row.leaseOwner)
	assert.Equal(t, 20, row.attemptCount)
	assert.Equal(t, int64(20), row.totalAttempts)
}

func TestClaim_ConcurrentClaimsLeaseEachEventOnce(t *testing.T) {
	// FOR UPDATE SKIP LOCKED: concurrent claimers never double-lease.
	h := newOutboxHarness(t)
	h.seedOutboxEvent(t, outboxSeed{})
	h.seedOutboxEvent(t, outboxSeed{})
	h.seedOutboxEvent(t, outboxSeed{})

	var (
		mu   sync.Mutex
		got  = map[string]int{}
		errs []error
		wg   sync.WaitGroup
	)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			evs, err := h.dlv.ClaimOutbox(context.Background(), 1, fmt.Sprintf("worker-%d", n), 30*time.Second)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			for _, e := range evs {
				got[e.EventID]++
			}
		}(i)
	}
	wg.Wait()
	require.Empty(t, errs)
	assert.Len(t, got, 3, "every event leased exactly once across concurrent claimers")
	for id, c := range got {
		assert.Equal(t, 1, c, "event %s double-leased", id)
	}
}

// --- Contract: ack ---------------------------------------------------------

func TestAck_PublishesWithCAS(t *testing.T) {
	h := newOutboxHarness(t)
	id := h.seedOutboxEvent(t, outboxSeed{})

	events, err := h.dlv.ClaimOutbox(context.Background(), 10, "worker-1", 30*time.Second)
	require.NoError(t, err)
	require.Len(t, events, 1)

	require.NoError(t, h.dlv.AckOutbox(context.Background(), id, events[0].ClaimToken))

	row := h.outboxRow(t, id)
	assert.Equal(t, "published", row.status)
	require.NotNil(t, row.publishedAt)
	assert.Nil(t, row.leaseOwner)
	assert.Nil(t, row.claimToken)

	// Published rows are never claimed again.
	again, err := h.dlv.ClaimOutbox(context.Background(), 10, "worker-1", 30*time.Second)
	require.NoError(t, err)
	assert.Empty(t, again)
}

func TestAck_StaleTokenRejectedLeaseIntact(t *testing.T) {
	h := newOutboxHarness(t)
	id := h.seedOutboxEvent(t, outboxSeed{})

	events, err := h.dlv.ClaimOutbox(context.Background(), 10, "worker-1", 30*time.Second)
	require.NoError(t, err)
	require.Len(t, events, 1)

	err = h.dlv.AckOutbox(context.Background(), id, "00000000-0000-0000-0000-0000000000ff")
	assert.ErrorIs(t, err, iam.ErrOutboxStaleClaim)

	row := h.outboxRow(t, id)
	assert.Equal(t, "leased", row.status)
	assert.Equal(t, events[0].ClaimToken, *row.claimToken)
}

// --- Contract: record failure ----------------------------------------------

func TestRecordFailure_BackoffToPending(t *testing.T) {
	h := newOutboxHarness(t)
	id := h.seedOutboxEvent(t, outboxSeed{})
	events, err := h.dlv.ClaimOutbox(context.Background(), 10, "worker-1", 30*time.Second)
	require.NoError(t, err)
	require.Len(t, events, 1)

	backoff := time.Now().Add(5 * time.Minute)
	status, err := h.dlv.RecordOutboxFailure(context.Background(), id, events[0].ClaimToken, "consumer_error", backoff)
	require.NoError(t, err)
	assert.Equal(t, iam.OutboxPending, status)

	row := h.outboxRow(t, id)
	assert.Equal(t, "pending", row.status)
	assert.WithinDuration(t, backoff, row.availableAt, 2*time.Second)
	assert.Equal(t, "consumer_error", *row.lastErrorCode)
	assert.Nil(t, row.leaseOwner)
	assert.Nil(t, row.claimToken)
	assert.Nil(t, row.blockedAt)
	// Failure recording does not burn an attempt: the claim did.
	assert.Equal(t, 1, row.attemptCount)

	// Before the backoff elapses the row is not claimable.
	again, err := h.dlv.ClaimOutbox(context.Background(), 10, "worker-1", 30*time.Second)
	require.NoError(t, err)
	assert.Empty(t, again)
}

func TestRecordFailure_ExhaustedCeilingBlocksAtomically(t *testing.T) {
	// The ceiling is evaluated atomically in the SAME statement as the
	// transition: pending/backoff or blocked, never reschedule-then-block.
	h := newOutboxHarness(t)
	id := h.seedOutboxEvent(t, outboxSeed{})
	events, err := h.dlv.ClaimOutbox(context.Background(), 10, "worker-1", 30*time.Second)
	require.NoError(t, err)
	require.Len(t, events, 1)

	// Push the row to the ceiling while leased (e.g. 20 prior attempts).
	_, err = h.pool.Exec(context.Background(),
		"UPDATE iam_outbox_events SET attempt_count = 20 WHERE event_id = $1::uuid", id)
	require.NoError(t, err)

	status, err := h.dlv.RecordOutboxFailure(context.Background(), id, events[0].ClaimToken, "consumer_error", time.Now().Add(time.Minute))
	require.NoError(t, err)
	assert.Equal(t, iam.OutboxBlocked, status)

	row := h.outboxRow(t, id)
	assert.Equal(t, "blocked", row.status)
	assert.Equal(t, "delivery_exhausted", *row.blockedReason)
	assert.Nil(t, row.leaseOwner)
	assert.Nil(t, row.claimToken)
	assert.Equal(t, 20, row.attemptCount)
}

func TestRecordFailure_StaleTokenRejectedNoTransition(t *testing.T) {
	h := newOutboxHarness(t)
	id := h.seedOutboxEvent(t, outboxSeed{})
	events, err := h.dlv.ClaimOutbox(context.Background(), 10, "worker-1", 30*time.Second)
	require.NoError(t, err)
	require.Len(t, events, 1)

	_, err = h.dlv.RecordOutboxFailure(context.Background(), id, "00000000-0000-0000-0000-0000000000ff", "consumer_error", time.Now().Add(time.Minute))
	assert.ErrorIs(t, err, iam.ErrOutboxStaleClaim)

	row := h.outboxRow(t, id)
	assert.Equal(t, "leased", row.status)
	assert.Equal(t, events[0].ClaimToken, *row.claimToken)
	assert.Nil(t, row.lastErrorCode)
}

// --- Contract: block -------------------------------------------------------

func TestBlock_NonRetryableDurableBlocked(t *testing.T) {
	h := newOutboxHarness(t)
	id := h.seedOutboxEvent(t, outboxSeed{})
	events, err := h.dlv.ClaimOutbox(context.Background(), 10, "worker-1", 30*time.Second)
	require.NoError(t, err)
	require.Len(t, events, 1)

	require.NoError(t, h.dlv.BlockOutbox(context.Background(), id, events[0].ClaimToken, "unsupported_version"))

	row := h.outboxRow(t, id)
	assert.Equal(t, "blocked", row.status)
	assert.Equal(t, "unsupported_version", *row.blockedReason)
	assert.Equal(t, "unsupported_version", *row.lastErrorCode)
	assert.Nil(t, row.leaseOwner)
	assert.Nil(t, row.claimToken)
	assert.Nil(t, row.publishedAt)
}

func TestBlock_StaleTokenRejected(t *testing.T) {
	h := newOutboxHarness(t)
	id := h.seedOutboxEvent(t, outboxSeed{})
	events, err := h.dlv.ClaimOutbox(context.Background(), 10, "worker-1", 30*time.Second)
	require.NoError(t, err)
	require.Len(t, events, 1)

	err = h.dlv.BlockOutbox(context.Background(), id, "00000000-0000-0000-0000-0000000000ff", "unsupported_version")
	assert.ErrorIs(t, err, iam.ErrOutboxStaleClaim)
	assert.Equal(t, "leased", h.outboxRow(t, id).status)
}

// --- Contract: requeue -----------------------------------------------------

func TestRequeue_TrustedContextBumpsEpochKeepsTotalWritesEvidence(t *testing.T) {
	h := newOutboxHarness(t)
	id := h.seedOutboxEvent(t, outboxSeed{})
	events, err := h.dlv.ClaimOutbox(context.Background(), 10, "worker-1", 30*time.Second)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.NoError(t, h.dlv.BlockOutbox(context.Background(), id, events[0].ClaimToken, "unsupported_version"))
	before := h.outboxRow(t, id)

	eligible := time.Now().Add(10 * time.Minute)
	newEpoch, err := h.dlv.RequeueBlockedOutbox(context.Background(), id, iam.TrustedRecoveryContext{
		PrincipalID:         1,
		AuthorizationSource: "platform-ops",
		ApprovalID:          "approval-77",
		CorrelationID:       "requeue-corr",
		RequestID:           "requeue-req-1",
	}, "approved_reason", eligible)
	require.NoError(t, err)
	assert.Equal(t, int64(2), newEpoch)

	row := h.outboxRow(t, id)
	assert.Equal(t, "pending", row.status)
	assert.Equal(t, 2, row.deliveryEpoch)
	assert.WithinDuration(t, eligible, row.epochStartedAt, 2*time.Second)
	assert.WithinDuration(t, eligible, row.availableAt, 2*time.Second)
	assert.Equal(t, 0, row.attemptCount, "epoch attempts reset")
	assert.Equal(t, int64(1), row.totalAttempts, "total attempts preserved across epochs")
	assert.Nil(t, row.blockedAt)
	assert.Nil(t, row.blockedReason)
	assert.Nil(t, row.leaseOwner)

	// Immutable evidence: complete previous-epoch state + recovery identity.
	var (
		epoch           int
		principal       string
		reason          string
		prevStarted     time.Time
		prevDeadline    time.Time
		prevBlocked     time.Time
		prevBlockReason string
		prevLastError   *string
		prevAttempts    int
		prevTotal       int64
		authSource      string
		approvalID      string
		requestID       string
		correlationID   string
	)
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT delivery_epoch, recovery_principal_id, reason_code,
		       previous_epoch_started_at, previous_epoch_deadline_at, previous_blocked_at,
		       previous_blocked_reason, previous_last_error_code, previous_epoch_attempts,
		       previous_total_attempts, authorization_source, approval_id, request_id,
		       correlation_id
		FROM iam_outbox_requeues WHERE event_id = $1::uuid`, id).Scan(
		&epoch, &principal, &reason, &prevStarted, &prevDeadline, &prevBlocked,
		&prevBlockReason, &prevLastError, &prevAttempts, &prevTotal, &authSource,
		&approvalID, &requestID, &correlationID))
	assert.Equal(t, 2, epoch)
	assert.Equal(t, "1", principal)
	assert.Equal(t, "approved_reason", reason)
	assert.WithinDuration(t, before.epochStartedAt, prevStarted, 2*time.Second)
	assert.WithinDuration(t, before.epochStartedAt.Add(24*time.Hour), prevDeadline, 2*time.Second)
	assert.WithinDuration(t, *before.blockedAt, prevBlocked, 2*time.Second)
	assert.Equal(t, *before.blockedReason, prevBlockReason)
	require.NotNil(t, prevLastError)
	assert.Equal(t, "unsupported_version", *prevLastError)
	assert.Equal(t, 1, prevAttempts)
	assert.Equal(t, int64(1), prevTotal)
	assert.Equal(t, "platform-ops", authSource)
	assert.Equal(t, "approval-77", approvalID)
	assert.Equal(t, "requeue-req-1", requestID)
	assert.Equal(t, "requeue-corr", correlationID)

	// The requeued epoch is NOT claimable before its delayed eligible time…
	early, err := h.dlv.ClaimOutbox(context.Background(), 10, "worker-1", 30*time.Second)
	require.NoError(t, err)
	assert.Empty(t, early)

	// …and claimable with a fresh lease once eligible (bump epoch start too —
	// a real requeue at now() would do the same, satisfying available_at >=
	// epoch_started_at).
	_, err = h.pool.Exec(context.Background(),
		"UPDATE iam_outbox_events SET available_at = now(), epoch_started_at = now() WHERE event_id = $1::uuid", id)
	require.NoError(t, err)
	again, err := h.dlv.ClaimOutbox(context.Background(), 10, "worker-1", 30*time.Second)
	require.NoError(t, err)
	require.Len(t, again, 1)
	assert.Equal(t, 1, again[0].AttemptCount)
	assert.Equal(t, 2, again[0].TotalAttemptCount)
}

func TestRequeue_NotBlockedRejectedNoEvidence(t *testing.T) {
	h := newOutboxHarness(t)
	id := h.seedOutboxEvent(t, outboxSeed{})

	_, err := h.dlv.RequeueBlockedOutbox(context.Background(), id, iam.TrustedRecoveryContext{
		PrincipalID:         1,
		AuthorizationSource: "platform-ops",
		ApprovalID:          "approval-77",
		CorrelationID:       "c",
		RequestID:           "r",
	}, "approved_reason", time.Now().Add(time.Minute))
	assert.ErrorIs(t, err, iam.ErrOutboxNotBlocked)

	assert.Equal(t, "pending", h.outboxRow(t, id).status)
	assert.Equal(t, int64(0), h.requeueRows(t, id))
}

// --- Contract: backlog -----------------------------------------------------

func TestBacklog_CountsAndOldestPending(t *testing.T) {
	h := newOutboxHarness(t)
	h.seedOutboxEvent(t, outboxSeed{}) // A: ~now
	older := time.Now().Add(-time.Hour)
	h.seedOutboxEvent(t, outboxSeed{availableAt: &older}) // B: oldest
	h.seedOutboxEvent(t, outboxSeed{})                    // C: ~now

	// Oldest-first claim takes B.
	events, err := h.dlv.ClaimOutbox(context.Background(), 1, "worker-1", 30*time.Second)
	require.NoError(t, err)
	require.Len(t, events, 1)

	backlog, err := h.dlv.GetOutboxBacklog(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(2), backlog.PendingCount)
	assert.Equal(t, int64(1), backlog.LeasedCount)
	assert.Equal(t, int64(0), backlog.BlockedCount)
	require.NotNil(t, backlog.OldestPendingAt)
	// Remaining pendings are ~now; the -1h row was claimed first.
	assert.WithinDuration(t, time.Now(), *backlog.OldestPendingAt, 5*time.Second)
}

// metricCount reads one cumulative observability counter (T066).
func (h *outboxHarness) metricCount(t *testing.T, key string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, h.pool.QueryRow(context.Background(),
		"SELECT count FROM iam_outbox_metrics WHERE metric_key = $1", key).Scan(&n))
	return n
}

// --- Contract: observability (T066) ----------------------------------------

func TestBacklog_ExpandedCountsAndAges(t *testing.T) {
	h := newOutboxHarness(t)
	oldPending := time.Now().Add(-time.Hour)
	// epoch_started_at is clamped with available_at so the DDL CHECK holds.
	h.seedOutboxEvent(t, outboxSeed{availableAt: &oldPending, epochStartedAt: &oldPending}) // A: pending, oldest
	h.seedOutboxEvent(t, outboxSeed{})                                                      // B: pending, ~now
	expiredLease := time.Now().Add(-20 * time.Minute)
	h.seedOutboxEvent(t, outboxSeed{
		status: "leased", leaseOwner: strPtr("worker-1"),
		leasedUntil: &expiredLease, claimToken: strPtr("00000000-0000-0000-0000-0000000000aa"),
	}) // C: leased (expired lease visible in the summary)
	publishedAt := time.Now().Add(-5 * time.Minute)
	h.seedOutboxEvent(t, outboxSeed{
		status: "published", publishedAt: &publishedAt,
	}) // D: published
	blockedAt := time.Now().Add(-2 * time.Minute)
	h.seedOutboxEvent(t, outboxSeed{
		status: "blocked", blockedAt: &blockedAt,
		blockedReason: strPtr("malformed_envelope"),
	}) // E: blocked

	backlog, err := h.dlv.GetOutboxBacklog(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(2), backlog.PendingCount)
	assert.Equal(t, int64(1), backlog.LeasedCount)
	assert.Equal(t, int64(1), backlog.BlockedCount)
	assert.Equal(t, int64(1), backlog.PublishedCount)
	require.NotNil(t, backlog.OldestPendingAt)
	assert.WithinDuration(t, oldPending, *backlog.OldestPendingAt, 5*time.Second)
	require.NotNil(t, backlog.OldestLeasedUntil)
	assert.WithinDuration(t, time.Now().Add(-20*time.Minute), *backlog.OldestLeasedUntil, 5*time.Second)
	// Fresh database: cumulative counters start at zero.
	assert.Equal(t, int64(0), backlog.LeaseRecoveryCount)
	assert.Equal(t, int64(0), backlog.StaleClaimRejectionCount)
}

func TestBacklog_EmptyIsZeroes(t *testing.T) {
	h := newOutboxHarness(t)
	backlog, err := h.dlv.GetOutboxBacklog(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(0), backlog.PendingCount)
	assert.Equal(t, int64(0), backlog.LeasedCount)
	assert.Equal(t, int64(0), backlog.BlockedCount)
	assert.Equal(t, int64(0), backlog.PublishedCount)
	assert.Nil(t, backlog.OldestPendingAt)
	assert.Nil(t, backlog.OldestLeasedUntil)
	assert.Equal(t, int64(0), backlog.LeaseRecoveryCount)
	assert.Equal(t, int64(0), backlog.StaleClaimRejectionCount)
}

func TestClaim_ExpiredLeaseReclaimCountsRecoveryMetric(t *testing.T) {
	h := newOutboxHarness(t)
	id := h.seedOutboxEvent(t, outboxSeed{})

	// First claim is a fresh pending delivery, not a recovery.
	events, err := h.dlv.ClaimOutbox(context.Background(), 10, "worker-1", 30*time.Second)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, int64(0), h.metricCount(t, "lease_recoveries"))

	// The worker "crashes" (lease lapses); the next claimer recovers the row.
	h.expireLease(t, id)
	again, err := h.dlv.ClaimOutbox(context.Background(), 10, "worker-2", 30*time.Second)
	require.NoError(t, err)
	require.Len(t, again, 1)
	assert.Equal(t, 2, again[0].AttemptCount, "reclaim counts a new attempt")
	assert.Equal(t, int64(1), h.metricCount(t, "lease_recoveries"))

	backlog, err := h.dlv.GetOutboxBacklog(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(1), backlog.LeaseRecoveryCount)
}

func TestStaleRejection_CountedPerCAS(t *testing.T) {
	h := newOutboxHarness(t)
	idA := h.seedOutboxEvent(t, outboxSeed{})
	idB := h.seedOutboxEvent(t, outboxSeed{})
	idC := h.seedOutboxEvent(t, outboxSeed{})

	events, err := h.dlv.ClaimOutbox(context.Background(), 10, "worker-1", 30*time.Second)
	require.NoError(t, err)
	require.Len(t, events, 3)
	stale := "00000000-0000-0000-0000-0000000000ff"

	// Stale ack / failure / block each reject without changing the row.
	assert.ErrorIs(t, h.dlv.AckOutbox(context.Background(), idA, stale), iam.ErrOutboxStaleClaim)
	_, err = h.dlv.RecordOutboxFailure(context.Background(), idB, stale, "consumer_timeout", time.Now().Add(time.Minute))
	assert.ErrorIs(t, err, iam.ErrOutboxStaleClaim)
	assert.ErrorIs(t, h.dlv.BlockOutbox(context.Background(), idC, stale, "contract_mismatch"), iam.ErrOutboxStaleClaim)

	assert.Equal(t, int64(3), h.metricCount(t, "stale_claim_rejections"))

	// Nothing transitioned: all three rows remain leased with intact tokens.
	for _, id := range []string{idA, idB, idC} {
		row := h.outboxRow(t, id)
		assert.Equal(t, "leased", row.status)
	}
	backlog, err := h.dlv.GetOutboxBacklog(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(3), backlog.StaleClaimRejectionCount)
	assert.Equal(t, int64(3), backlog.LeasedCount)
}
