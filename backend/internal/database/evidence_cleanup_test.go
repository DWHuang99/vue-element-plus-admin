// Evidence-cleanup audit verification (T078). The immutable
// platform_evidence_cleanup_audit table is written ONLY through the two
// migration-000013 SECURITY DEFINER functions — record_cleanup_audit (records
// the approval + dry-run counts before any purge) and finalize_cleanup_audit
// (transitions the approved_pending row to its terminal outcome). These tests
// drive the public Go wrappers so SQL and Go layer are verified together, and
// prove runtime code cannot bypass the functions (no direct table access for
// app_runtime).
package database

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cleanupAuditRow is one audit row as read back for assertions.
type cleanupAuditRow struct {
	approvalID          string
	principalID         string
	authorizationSource string
	requestID           *string
	correlationID       *string
	requestedCutoff     time.Time
	effectiveCutoff     time.Time
	dryRunCounts        map[string]int64
	status              string
	purgedCounts        map[string]int64
	blockedReason       *string
	finalizedAt         *time.Time
}

func cleanupAuditRowFor(t *testing.T, auditID string) cleanupAuditRow {
	t.Helper()
	var r cleanupAuditRow
	var dryRun, purged []byte
	require.NoError(t, testConn.QueryRow(context.Background(), `
		SELECT approval_id, principal_id, authorization_source, request_id, correlation_id,
		       requested_cutoff, effective_cutoff, dry_run_counts, status, purged_counts,
		       blocked_reason, finalized_at
		FROM platform_evidence_cleanup_audit WHERE audit_id = $1`, auditID).Scan(
		&r.approvalID, &r.principalID, &r.authorizationSource, &r.requestID, &r.correlationID,
		&r.requestedCutoff, &r.effectiveCutoff, &dryRun, &r.status, &purged,
		&r.blockedReason, &r.finalizedAt))
	require.NoError(t, jsonToCounts(dryRun, &r.dryRunCounts))
	require.NoError(t, jsonToCounts(purged, &r.purgedCounts))
	r.requestedCutoff = r.requestedCutoff.UTC()
	r.effectiveCutoff = r.effectiveCutoff.UTC()
	if r.finalizedAt != nil {
		utc := r.finalizedAt.UTC()
		r.finalizedAt = &utc
	}
	return r
}

func jsonToCounts(raw []byte, out *map[string]int64) error {
	if raw == nil {
		*out = nil
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return err
	}
	return nil
}

// cleanupAuditTestRecord builds a unique audit record for one test.
func cleanupAuditTestRecord(now time.Time) CleanupAuditRecord {
	return CleanupAuditRecord{
		AuditID:             uuid.NewString(),
		ApprovalID:          "appr-evidence",
		PrincipalID:         "ops-9",
		AuthorizationSource: "platform_operator_session",
		RequestID:           "req-evidence",
		CorrelationID:       "corr-evidence",
		RequestedCutoff:     now,
		EffectiveCutoff:     now.Add(48 * time.Hour),
		DryRunCounts:        map[string]int64{"iam.command_receipts": 3, "iam.outbox_events": 1},
	}
}

// TestCleanupAudit_RecordThenFinalize: the full approved-cleanup lifecycle —
// record (approved_pending) → finalize succeeded (purged counts + finalized_at).
func TestCleanupAudit_RecordThenFinalize(t *testing.T) {
	ctx := context.Background()
	pool := newGatePool(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	rec := cleanupAuditTestRecord(now)

	require.NoError(t, RecordCleanupAudit(ctx, pool, rec))

	row := cleanupAuditRowFor(t, rec.AuditID)
	assert.Equal(t, "appr-evidence", row.approvalID)
	assert.Equal(t, "ops-9", row.principalID)
	assert.Equal(t, "platform_operator_session", row.authorizationSource)
	assert.Equal(t, "req-evidence", *row.requestID)
	assert.Equal(t, "corr-evidence", *row.correlationID)
	assert.Equal(t, now, row.requestedCutoff)
	assert.Equal(t, now.Add(48*time.Hour), row.effectiveCutoff)
	assert.Equal(t, map[string]int64{"iam.command_receipts": 3, "iam.outbox_events": 1}, row.dryRunCounts)
	assert.Equal(t, "approved_pending", row.status, "recorded audit starts approved_pending")
	assert.Nil(t, row.purgedCounts)
	assert.Nil(t, row.finalizedAt)

	require.NoError(t, FinalizeCleanupAudit(ctx, pool, rec.AuditID, "succeeded",
		map[string]int64{"iam.command_receipts": 3}, ""))

	row = cleanupAuditRowFor(t, rec.AuditID)
	assert.Equal(t, "succeeded", row.status)
	assert.Equal(t, map[string]int64{"iam.command_receipts": 3}, row.purgedCounts)
	require.NotNil(t, row.finalizedAt)
}

// TestCleanupAudit_FinalizeBlocked: a purge that an owner refused surfaces as
// status 'blocked' with the reason and no purged counts.
func TestCleanupAudit_FinalizeBlocked(t *testing.T) {
	ctx := context.Background()
	pool := newGatePool(t)
	rec := cleanupAuditTestRecord(time.Now().UTC())

	require.NoError(t, RecordCleanupAudit(ctx, pool, rec))
	require.NoError(t, FinalizeCleanupAudit(ctx, pool, rec.AuditID, "blocked", nil, "owner refused: unresolved outbox event"))

	row := cleanupAuditRowFor(t, rec.AuditID)
	assert.Equal(t, "blocked", row.status)
	assert.Nil(t, row.purgedCounts)
	require.NotNil(t, row.blockedReason)
	assert.Equal(t, "owner refused: unresolved outbox event", *row.blockedReason)
	require.NotNil(t, row.finalizedAt)
}

// TestCleanupAudit_FinalizeRequiresPending: a terminal row cannot be finalized
// twice — the SECURITY DEFINER function raises cleanup_audit_not_pending.
func TestCleanupAudit_FinalizeRequiresPending(t *testing.T) {
	ctx := context.Background()
	pool := newGatePool(t)
	rec := cleanupAuditTestRecord(time.Now().UTC())

	require.NoError(t, RecordCleanupAudit(ctx, pool, rec))
	require.NoError(t, FinalizeCleanupAudit(ctx, pool, rec.AuditID, "succeeded", nil, ""))

	err := FinalizeCleanupAudit(ctx, pool, rec.AuditID, "succeeded", nil, "")
	require.Error(t, err, "finalizing a terminal audit row must fail")
	assert.Contains(t, err.Error(), "cleanup_audit_not_pending", "the SQL function names the invariant")

	row := cleanupAuditRowFor(t, rec.AuditID)
	assert.Equal(t, "succeeded", row.status, "first finalize is authoritative")
}

// TestCleanupAudit_RuntimeCannotBypassFunctions: app_runtime has no table
// grants on the audit — a direct INSERT/SELECT is denied; only the SECURITY
// DEFINER functions can write it.
func TestCleanupAudit_RuntimeCannotBypassFunctions(t *testing.T) {
	ctx := context.Background()
	_, err := testConn.Exec(ctx, "SET ROLE app_runtime")
	require.NoError(t, err)
	defer func() { _, _ = testConn.Exec(context.Background(), "RESET ROLE") }()

	_, err = testConn.Exec(ctx, `
		INSERT INTO platform_evidence_cleanup_audit (audit_id, approval_id, principal_id,
			authorization_source, requested_cutoff, effective_cutoff, dry_run_counts, status)
		VALUES (gen_random_uuid(), 'x', 'x', 'x', now(), now(), '{}', 'approved_pending')`)
	require.Error(t, err, "app_runtime cannot INSERT the audit table directly")

	_, err = testConn.Exec(ctx, "SELECT count(*) FROM platform_evidence_cleanup_audit")
	require.Error(t, err, "app_runtime cannot SELECT the audit table directly")
}
