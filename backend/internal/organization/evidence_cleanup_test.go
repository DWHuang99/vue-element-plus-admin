// Organization evidence-cleanup adapter tests (T078). Organization evidence is
// always terminal (receipts are succeeded/rejected by schema CHECK, inbox
// dedupe rows exist only after successful processing), so BlockingUnresolvedCount
// is always 0 and the purge only enforces the 30-day floor plus the operational
// approval gate. All timestamps are explicit so the predicates are deterministic.
package organization_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hdw/vue-element-plus-admin/backend/internal/organization"
	"github.com/hdw/vue-element-plus-admin/backend/internal/organization/postgres"
)

type orgCleanupHarness struct {
	pool *pgxpool.Pool
	svc  organization.EvidenceCleanupService
}

func newOrgCleanupHarness(t *testing.T) *orgCleanupHarness {
	t.Helper()
	ctx := context.Background()
	connStr := freshDB(t)
	migrateToHead(t, connStr)

	pool, err := pgxpool.New(ctx, connStr)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	return &orgCleanupHarness{pool: pool, svc: postgres.NewEvidenceCleanup(pool)}
}

func (h *orgCleanupHarness) seedReceipt(t *testing.T, status string, completedAt time.Time) {
	t.Helper()
	_, err := h.pool.Exec(context.Background(), `
		INSERT INTO organization_command_receipts (operation_id, command_name, request_fingerprint, status, completed_at)
		VALUES ($1::uuid, 'cleanup-test', 'fingerprint', $2, $3)`,
		uuid.NewString(), status, completedAt)
	require.NoError(t, err, "seed org command receipt")
}

func (h *orgCleanupHarness) seedInbox(t *testing.T, processedAt time.Time) {
	t.Helper()
	_, err := h.pool.Exec(context.Background(), `
		INSERT INTO organization_inbox_messages (
		    event_id, handler_name, event_type, event_version, aggregate_id, aggregate_version,
		    received_at, processed_at)
		VALUES ($1::uuid, 'department_handler', 'iam.user.deleted', 1, '42', 1, $2, $3)`,
		uuid.NewString(), processedAt.Add(-time.Hour), processedAt)
	require.NoError(t, err, "seed inbox message")
}

func (h *orgCleanupHarness) countTable(t *testing.T, table string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, h.pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n))
	return n
}

func orgCleanupTrusted() organization.TrustedRecoveryContext {
	return organization.TrustedRecoveryContext{
		PrincipalID:         7,
		AuthorizationSource: "platform_operator_session",
		ApprovalID:          "appr-evidence",
		CorrelationID:       "corr-evidence",
		RequestID:           "req-evidence",
	}
}

// TestOrgCleanupEvaluate_CountsAndFloor: dry-run counts eligible receipts and
// inbox rows at the cutoff, never reports unresolved evidence (terminal by
// construction), and reports the earliest replayable cutoff.
func TestOrgCleanupEvaluate_CountsAndFloor(t *testing.T) {
	h := newOrgCleanupHarness(t)
	now := time.Now().UTC().Truncate(time.Microsecond)

	h.seedReceipt(t, "succeeded", now.Add(-45*24*time.Hour))
	h.seedReceipt(t, "rejected", now.Add(-10*24*time.Hour))
	h.seedInbox(t, now.Add(-50*24*time.Hour))
	h.seedInbox(t, now.Add(-5*24*time.Hour))

	ev, err := h.svc.EvaluateEvidenceCleanup(context.Background(), now)
	require.NoError(t, err)
	assert.Equal(t, map[string]int64{
		"organization.command_receipts": 1, // only the 45d receipt
		"organization.inbox_messages":   1, // only the 50d inbox row
	}, ev.EligibleCounts)
	assert.Equal(t, int64(0), ev.BlockingUnresolvedCount, "org evidence is always terminal")
	require.NotNil(t, ev.OldestReplayableAt)
	// min(45d receipt, 50d inbox) + 30d = 20d ago — the earliest timestamp wins,
	// so the 50d inbox drives the floor. Compare instants: the adapter returns
	// the DB timestamp in the session location, the expectation is built in UTC.
	assert.True(t, ev.OldestReplayableAt.Equal(now.Add(-20*24*time.Hour)),
		"replay floor = min(oldest terminal) + 30d")
}

// TestOrgCleanupPurge_ApprovalGate: no approval → refuse, delete nothing.
func TestOrgCleanupPurge_ApprovalGate(t *testing.T) {
	h := newOrgCleanupHarness(t)
	now := time.Now().UTC()
	h.seedReceipt(t, "succeeded", now.Add(-45*24*time.Hour))
	h.seedInbox(t, now.Add(-50*24*time.Hour))

	_, err := h.svc.PurgeEligibleEvidence(context.Background(), now, organization.TrustedRecoveryContext{})
	require.ErrorIs(t, err, organization.ErrCleanupApprovalRequired)
	assert.Equal(t, int64(1), h.countTable(t, "organization_command_receipts"))
	assert.Equal(t, int64(1), h.countTable(t, "organization_inbox_messages"))
}

// TestOrgCleanupPurge_DeletesEligible: approved purge deletes eligible rows,
// recent rows survive; a too-early cutoff matches nothing.
func TestOrgCleanupPurge_DeletesEligible(t *testing.T) {
	h := newOrgCleanupHarness(t)
	now := time.Now().UTC().Truncate(time.Microsecond)

	h.seedReceipt(t, "succeeded", now.Add(-45*24*time.Hour))
	h.seedReceipt(t, "rejected", now.Add(-10*24*time.Hour))
	h.seedInbox(t, now.Add(-50*24*time.Hour))
	h.seedInbox(t, now.Add(-5*24*time.Hour))

	counts, err := h.svc.PurgeEligibleEvidence(context.Background(), now, orgCleanupTrusted())
	require.NoError(t, err)
	assert.Equal(t, map[string]int64{
		"organization.command_receipts": 1,
		"organization.inbox_messages":   1,
	}, counts)
	assert.Equal(t, int64(1), h.countTable(t, "organization_command_receipts"), "recent receipt survives")
	assert.Equal(t, int64(1), h.countTable(t, "organization_inbox_messages"), "recent inbox row survives")
}
