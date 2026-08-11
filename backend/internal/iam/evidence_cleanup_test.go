// IAM evidence-cleanup adapter tests (T078; data-model.md "Retention and
// cleanup matrix"). The adapter is the owner-local side of the Platform
// coordinator: Evaluate is a pure dry-run (per-table eligible counts at the
// cutoff, blocking unresolved count, earliest replayable cutoff) and Purge is
// the approved owner-local deletion — it re-verifies safety inside its own
// IAM-only transaction, requires an operational approval, and deletes requeues
// before their events so the FK never blocks the deletion. All timestamps are
// explicit so the 30-day predicates are deterministic.
package iam_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hdw/vue-element-plus-admin/backend/internal/iam"
	"github.com/hdw/vue-element-plus-admin/backend/internal/iam/postgres"
)

// cleanupHarness wires the cleanup adapter to a fresh migrated database plus
// direct-sql seeding of receipts/outbox/requeues.
type cleanupHarness struct {
	pool *pgxpool.Pool
	svc  iam.EvidenceCleanupService
}

func newCleanupHarness(t *testing.T) *cleanupHarness {
	t.Helper()
	ctx := context.Background()
	connStr := freshDB(t)
	migrateToHead(t, connStr)

	pool, err := pgxpool.New(ctx, connStr)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	return &cleanupHarness{pool: pool, svc: postgres.NewEvidenceCleanup(pool)}
}

// seedReceipt inserts one terminal command receipt.
func (h *cleanupHarness) seedReceipt(t *testing.T, status string, completedAt time.Time) {
	t.Helper()
	_, err := h.pool.Exec(context.Background(), `
		INSERT INTO iam_command_receipts (operation_id, command_name, request_fingerprint, status, completed_at)
		VALUES ($1::uuid, 'cleanup-test', 'fingerprint', $2, $3)`,
		uuid.NewString(), status, completedAt)
	require.NoError(t, err, "seed command receipt")
}

// seedEvent inserts one outbox event row with an explicit publish time.
func (h *cleanupHarness) seedEvent(t *testing.T, status string, publishedAt *time.Time) string {
	t.Helper()
	eventID := uuid.NewString()
	now := time.Now().UTC()
	_, err := h.pool.Exec(context.Background(), `
		INSERT INTO iam_outbox_events (
		    event_id, event_type, event_version, producer, aggregate_type, aggregate_id,
		    aggregate_version, payload, correlation_id, occurred_at, status, available_at,
		    delivery_epoch, epoch_started_at, attempt_count, total_attempt_count,
		    lease_owner, leased_until, claim_token, last_error_code, blocked_at,
		    blocked_reason_code, published_at
		) VALUES ($1::uuid, 'iam.user.deleted', 1, 'iam', 'user', '42', 1, '{}'::jsonb,
		          'cleanup-correlation', $2, $3, $2, 1, $2, 0, 0, NULL, NULL, NULL, NULL, NULL, NULL, $4)`,
		eventID, now, status, publishedAt)
	require.NoError(t, err, "seed outbox event")
	return eventID
}

// seedRequeue inserts one requeue evidence row for an event.
func (h *cleanupHarness) seedRequeue(t *testing.T, eventID string, requeuedAt time.Time) {
	t.Helper()
	_, err := h.pool.Exec(context.Background(), `
		INSERT INTO iam_outbox_requeues (
		    event_id, delivery_epoch, recovery_principal_id, reason_code, requeued_at)
		VALUES ($1::uuid, 2, 'ops-test', 'blocked_recovery', $2)`,
		eventID, requeuedAt)
	require.NoError(t, err, "seed requeue")
}

// countTable returns the live row count of an IAM evidence table.
func (h *cleanupHarness) countTable(t *testing.T, table string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, h.pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n))
	return n
}

func cleanupTrusted() iam.TrustedRecoveryContext {
	return iam.TrustedRecoveryContext{
		PrincipalID:         7,
		AuthorizationSource: "platform_operator_session",
		ApprovalID:          "appr-evidence",
		CorrelationID:       "corr-evidence",
		RequestID:           "req-evidence",
	}
}

// TestIAMCleanupEvaluate_CountsAndFloor: dry-run counts eligible rows at the
// cutoff, counts unresolved outbox events as blocking, and reports the
// earliest replayable cutoff (min oldest terminal + 30 days).
func TestIAMCleanupEvaluate_CountsAndFloor(t *testing.T) {
	h := newCleanupHarness(t)
	now := time.Now().UTC().Truncate(time.Microsecond)

	// Two receipts: one old enough to be eligible, one too recent.
	h.seedReceipt(t, "succeeded", now.Add(-45*24*time.Hour))
	h.seedReceipt(t, "rejected", now.Add(-10*24*time.Hour))

	// E1: published 40d ago, no requeues → eligible event.
	// E2: published 40d ago but with a requeue 5d ago → protected by the young requeue.
	// E3: pending → unresolved, blocks any purge.
	// E4: published 40d ago with a requeue 35d ago → event + requeue both eligible.
	e1 := h.seedEvent(t, "published", tsPtr(now.Add(-40*24*time.Hour)))
	e2 := h.seedEvent(t, "published", tsPtr(now.Add(-40*24*time.Hour)))
	h.seedRequeue(t, e2, now.Add(-5*24*time.Hour))
	h.seedEvent(t, "pending", nil)
	e4 := h.seedEvent(t, "published", tsPtr(now.Add(-40*24*time.Hour)))
	h.seedRequeue(t, e4, now.Add(-35*24*time.Hour))
	_ = e1

	ev, err := h.svc.EvaluateEvidenceCleanup(context.Background(), now)
	require.NoError(t, err)
	assert.Equal(t, map[string]int64{
		"iam.command_receipts": 1, // only the 45d receipt
		"iam.outbox_events":    2, // E1 + E4; E2 protected, E3 not published
		"iam.outbox_requeues":  1, // only E4's 35d requeue
	}, ev.EligibleCounts)
	assert.Equal(t, int64(1), ev.BlockingUnresolvedCount, "pending event blocks")
	require.NotNil(t, ev.OldestReplayableAt)
	// min(45d receipt, 40d publish) + 30d = 15d ago. Compare instants: the
	// adapter returns the DB timestamp in the session location, the expectation
	// is built in UTC.
	assert.True(t, ev.OldestReplayableAt.Equal(now.Add(-15*24*time.Hour)),
		"replay floor = min(oldest terminal) + 30d")
}

// TestIAMCleanupEvaluate_NoEvidence: an empty IAM schema reports zero counts,
// zero blocking and a nil floor.
func TestIAMCleanupEvaluate_NoEvidence(t *testing.T) {
	h := newCleanupHarness(t)
	ev, err := h.svc.EvaluateEvidenceCleanup(context.Background(), time.Now())
	require.NoError(t, err)
	assert.Equal(t, int64(0), ev.BlockingUnresolvedCount)
	assert.Nil(t, ev.OldestReplayableAt)
	for _, count := range ev.EligibleCounts {
		assert.Zero(t, count)
	}
}

// TestIAMCleanupPurge_ApprovalGate: without an approval the purge refuses and
// deletes nothing.
func TestIAMCleanupPurge_ApprovalGate(t *testing.T) {
	h := newCleanupHarness(t)
	now := time.Now().UTC()
	h.seedReceipt(t, "succeeded", now.Add(-45*24*time.Hour))
	h.seedEvent(t, "published", tsPtr(now.Add(-40*24*time.Hour)))

	_, err := h.svc.PurgeEligibleEvidence(context.Background(), now, iam.TrustedRecoveryContext{})
	require.ErrorIs(t, err, iam.ErrCleanupApprovalRequired)
	assert.Equal(t, int64(1), h.countTable(t, "iam_command_receipts"), "nothing deleted without approval")
	assert.Equal(t, int64(1), h.countTable(t, "iam_outbox_events"))
}

// TestIAMCleanupPurge_UnresolvedRefusesInsideTx: even with an approval, any
// unresolved outbox event refuses the purge inside the adapter's own
// transaction — nothing is deleted ("uncertain → delete nothing").
func TestIAMCleanupPurge_UnresolvedRefusesInsideTx(t *testing.T) {
	h := newCleanupHarness(t)
	now := time.Now().UTC()
	h.seedReceipt(t, "succeeded", now.Add(-45*24*time.Hour))
	h.seedEvent(t, "published", tsPtr(now.Add(-40*24*time.Hour)))
	h.seedEvent(t, "pending", nil)

	_, err := h.svc.PurgeEligibleEvidence(context.Background(), now, cleanupTrusted())
	require.ErrorIs(t, err, iam.ErrEvidenceCleanupBlocked)
	assert.Equal(t, int64(1), h.countTable(t, "iam_command_receipts"), "unresolved event aborts the whole purge")
	assert.Equal(t, int64(2), h.countTable(t, "iam_outbox_events"))
}

// TestIAMCleanupPurge_DeletesEligibleFKOrder: approved purge deletes requeues
// before events before receipts; rows younger than 30d (or protected by a
// young requeue) survive.
func TestIAMCleanupPurge_DeletesEligibleFKOrder(t *testing.T) {
	h := newCleanupHarness(t)
	now := time.Now().UTC().Truncate(time.Microsecond)

	// Eligible: receipt 45d, E1 published 40d, E4 published 40d + requeue 35d.
	h.seedReceipt(t, "succeeded", now.Add(-45*24*time.Hour))
	h.seedEvent(t, "published", tsPtr(now.Add(-40*24*time.Hour)))
	e4 := h.seedEvent(t, "published", tsPtr(now.Add(-40*24*time.Hour)))
	h.seedRequeue(t, e4, now.Add(-35*24*time.Hour))

	// Must survive: recent receipt, recent event, event protected by a young requeue.
	h.seedReceipt(t, "rejected", now.Add(-10*24*time.Hour))
	h.seedEvent(t, "published", tsPtr(now.Add(-10*24*time.Hour)))
	eProtected := h.seedEvent(t, "published", tsPtr(now.Add(-40*24*time.Hour)))
	h.seedRequeue(t, eProtected, now.Add(-5*24*time.Hour))

	_, err := h.svc.PurgeEligibleEvidence(context.Background(), now, cleanupTrusted())
	require.NoError(t, err)

	assert.Equal(t, int64(1), h.countTable(t, "iam_command_receipts"), "only the 45d receipt purged")
	// 4 events seeded, 2 eligible (E1 + E4); the recent event and the
	// young-requeue-protected event survive.
	assert.Equal(t, int64(2), h.countTable(t, "iam_outbox_events"), "recent + young-requeue-protected events survive")
	assert.Equal(t, int64(1), h.countTable(t, "iam_outbox_requeues"), "only the 35d requeue purged")
}

// TestIAMCleanupPurge_TooEarlyCutoffMatchesNothing: a cutoff before the
// 30-day floor is safe by construction — the row-level predicates match
// nothing, so a too-aggressive requested cutoff deletes zero rows.
func TestIAMCleanupPurge_TooEarlyCutoffMatchesNothing(t *testing.T) {
	h := newCleanupHarness(t)
	now := time.Now().UTC()
	h.seedReceipt(t, "succeeded", now.Add(-45*24*time.Hour))
	h.seedEvent(t, "published", tsPtr(now.Add(-40*24*time.Hour)))

	// cutoff = now-20d: eligible requires terminal <= -50d → nothing matches.
	cutoff := now.Add(-20 * 24 * time.Hour)
	counts, err := h.svc.PurgeEligibleEvidence(context.Background(), cutoff, cleanupTrusted())
	require.NoError(t, err)
	assert.Equal(t, int64(0), counts["iam.command_receipts"])
	assert.Equal(t, int64(0), counts["iam.outbox_events"])
	assert.Equal(t, int64(1), h.countTable(t, "iam_command_receipts"))
	assert.Equal(t, int64(1), h.countTable(t, "iam_outbox_events"))
}

func tsPtr(t time.Time) *time.Time { return &t }
