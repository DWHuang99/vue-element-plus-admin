// Rollout-gate evidence verification (T077; plan Phase 5.9 step 6). The
// window math lives in the migration-000012 SECURITY DEFINER functions:
//
//   - the first sample opens a window (observation_started_at = observed_at);
//   - a passing sample extends it (the new row keeps the window's start);
//   - any mismatch (cumulative counter increase or a newer last-mismatch
//     timestamp) or missing sample (interval beyond the gap tolerance)
//     closes the current record (observed_through_at) and resets the 72h
//     window (the new row starts a fresh one);
//   - approve_route_disable records the approval row only after one
//     complete continuous passing 72h window; the open head IS the window —
//     any failure would have restarted observation_started_at.
//
// The tests drive the public Go wrappers (RecordRolloutGateSample /
// ApproveRouteDisable) so the SQL and the Go layer are verified together.
// All timestamps are explicit (observed_at), so the window math is
// deterministic — no sleeps. Each test uses a unique phase and removes its
// own rows at the end (the append-only rule binds runtime code, not tests).
package database

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gateRow is one evidence row as read back for assertions.
type gateRow struct {
	gateID               int64
	observationStartedAt time.Time
	observedThroughAt    *time.Time
	monitoringGap        bool
	mismatchCount        int64
	lastMismatchAt       *time.Time
	approvalID           *string
}

func gateRowsForPhase(t *testing.T, phase string) []gateRow {
	t.Helper()
	rows, err := testConn.Query(context.Background(), `
		SELECT gate_id, observation_started_at, observed_through_at, monitoring_gap,
		       mismatch_count, last_mismatch_at, approval_id
		FROM compatibility_rollout_gates
		WHERE phase = $1
		ORDER BY gate_id`, phase)
	require.NoError(t, err)
	defer rows.Close()
	var out []gateRow
	for rows.Next() {
		var r gateRow
		require.NoError(t, rows.Scan(&r.gateID, &r.observationStartedAt, &r.observedThroughAt,
			&r.monitoringGap, &r.mismatchCount, &r.lastMismatchAt, &r.approvalID))
		// pgx returns timestamptz in the session's local zone; normalize so
		// assert.Equal compares instants against the UTC test inputs.
		r.observationStartedAt = r.observationStartedAt.UTC()
		if r.observedThroughAt != nil {
			utc := r.observedThroughAt.UTC()
			r.observedThroughAt = &utc
		}
		if r.lastMismatchAt != nil {
			utc := r.lastMismatchAt.UTC()
			r.lastMismatchAt = &utc
		}
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

// cleanupGatePhase removes the test's own evidence rows (runtime code never
// writes the table directly, but tests own their rows).
func cleanupGatePhase(t *testing.T, phase string) {
	t.Helper()
	_, err := testConn.Exec(context.Background(),
		"DELETE FROM compatibility_rollout_gates WHERE phase = $1", phase)
	require.NoError(t, err)
}

// newGatePool returns a pool for the wrapper calls (the wrappers take a
// pool; testConn is a plain Conn, so each test builds one like the
// bridge_test.go wrapper tests do). The pool is closed at test end.
func newGatePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), testConnStr)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

// sample builds a passing RolloutGateSample with the phase's defaults.
func sample(phase string, observedAt time.Time) RolloutGateSample {
	return RolloutGateSample{
		Phase:                    phase,
		CapabilityManifestHash:   "manifest-hash",
		BridgeModeVersion:        1,
		BridgeLegacyDeleteSyncOn: false,
		LegacyRows:               2,
		NewRows:                  2,
		RowVersionChecksum:       "checksum",
		MismatchCount:            0,
		RollbackArtifactID:       "artifact-004",
		RollbackSuiteResult:      "passed",
		PrincipalID:              "rollout-gate-test",
		ObservedAt:               observedAt,
		MaxGapInterval:           5 * time.Minute,
	}
}

// TestRolloutGate_FirstSampleOpensWindow: no prior evidence → the sample
// opens a fresh window at its own observed_at.
func TestRolloutGate_FirstSampleOpensWindow(t *testing.T) {
	phase := "gate_first_" + uniqueSuffix(t)
	cleanupGatePhase(t, phase)
	defer cleanupGatePhase(t, phase)

	now := time.Now().UTC().Truncate(time.Microsecond)
	gateID, err := RecordRolloutGateSample(context.Background(), newGatePool(t), sample(phase, now))
	require.NoError(t, err)
	require.Positive(t, gateID)

	rows := gateRowsForPhase(t, phase)
	require.Len(t, rows, 1)
	assert.Equal(t, now, rows[0].observationStartedAt, "first sample opens the window at its own time")
	assert.Nil(t, rows[0].observedThroughAt, "the window head stays open")
	assert.False(t, rows[0].monitoringGap)
}

// TestRolloutGate_PassingSampleExtendsWindow: a second clean sample keeps
// the window start; neither record is closed.
func TestRolloutGate_PassingSampleExtendsWindow(t *testing.T) {
	phase := "gate_extend_" + uniqueSuffix(t)
	cleanupGatePhase(t, phase)
	defer cleanupGatePhase(t, phase)

	start := time.Now().UTC().Truncate(time.Microsecond)
	_, err := RecordRolloutGateSample(context.Background(), newGatePool(t), sample(phase, start))
	require.NoError(t, err)

	_, err = RecordRolloutGateSample(context.Background(), newGatePool(t), sample(phase, start.Add(1*time.Minute)))
	require.NoError(t, err)

	rows := gateRowsForPhase(t, phase)
	require.Len(t, rows, 2)
	assert.Equal(t, start, rows[0].observationStartedAt)
	assert.Equal(t, start, rows[1].observationStartedAt, "passing sample extends the same window")
	assert.Nil(t, rows[0].observedThroughAt)
	assert.Nil(t, rows[1].observedThroughAt)
}

// TestRolloutGate_MismatchClosesAndResets: a cumulative mismatch-counter
// increase closes the previous record and starts a fresh window.
func TestRolloutGate_MismatchClosesAndResets(t *testing.T) {
	phase := "gate_mismatch_" + uniqueSuffix(t)
	cleanupGatePhase(t, phase)
	defer cleanupGatePhase(t, phase)

	start := time.Now().UTC().Truncate(time.Microsecond)
	_, err := RecordRolloutGateSample(context.Background(), newGatePool(t), sample(phase, start))
	require.NoError(t, err)

	// The shadow/parity surface reports one mismatch: counter increased.
	bad := sample(phase, start.Add(1*time.Minute))
	bad.MismatchCount = 1
	mismatchAt := start.Add(1 * time.Minute)
	bad.LastMismatchAt = &mismatchAt
	_, err = RecordRolloutGateSample(context.Background(), newGatePool(t), bad)
	require.NoError(t, err)

	rows := gateRowsForPhase(t, phase)
	require.Len(t, rows, 2)
	assert.NotNil(t, rows[0].observedThroughAt, "mismatch closes the current record")
	assert.Equal(t, start.Add(1*time.Minute), rows[1].observationStartedAt, "mismatch resets the 72h window")
	assert.Equal(t, int64(1), rows[1].mismatchCount)
	assert.Equal(t, &mismatchAt, rows[1].lastMismatchAt)
}

// TestRolloutGate_LastMismatchStampCloses: a newer last-mismatch timestamp
// with an unchanged counter is still a mismatch.
func TestRolloutGate_LastMismatchStampCloses(t *testing.T) {
	phase := "gate_stamp_" + uniqueSuffix(t)
	cleanupGatePhase(t, phase)
	defer cleanupGatePhase(t, phase)

	start := time.Now().UTC().Truncate(time.Microsecond)
	// First sample already carries a historical mismatch stamp (e.g. a
	// shadow divergence before the window opened).
	first := sample(phase, start)
	old := start.Add(-24 * time.Hour)
	first.LastMismatchAt = &old
	_, err := RecordRolloutGateSample(context.Background(), newGatePool(t), first)
	require.NoError(t, err)

	// Same counter, but a newer stamp: a NEW divergence — window resets.
	second := sample(phase, start.Add(1*time.Minute))
	second.MismatchCount = first.MismatchCount
	newer := start.Add(1 * time.Minute)
	second.LastMismatchAt = &newer
	_, err = RecordRolloutGateSample(context.Background(), newGatePool(t), second)
	require.NoError(t, err)

	rows := gateRowsForPhase(t, phase)
	require.Len(t, rows, 2)
	assert.NotNil(t, rows[0].observedThroughAt, "newer last-mismatch stamp closes the record")
	assert.Equal(t, start.Add(1*time.Minute), rows[1].observationStartedAt, "window reset")
}

// TestRolloutGate_GapClosesAndResets: a sample arriving beyond the gap
// tolerance is a monitoring gap — the record closes and the window resets.
func TestRolloutGate_GapClosesAndResets(t *testing.T) {
	phase := "gate_gap_" + uniqueSuffix(t)
	cleanupGatePhase(t, phase)
	defer cleanupGatePhase(t, phase)

	start := time.Now().UTC().Truncate(time.Microsecond)
	_, err := RecordRolloutGateSample(context.Background(), newGatePool(t), sample(phase, start))
	require.NoError(t, err)

	// 30 minutes later with a 5-minute tolerance: missing samples in between.
	_, err = RecordRolloutGateSample(context.Background(), newGatePool(t), sample(phase, start.Add(30*time.Minute)))
	require.NoError(t, err)

	rows := gateRowsForPhase(t, phase)
	require.Len(t, rows, 2)
	assert.NotNil(t, rows[0].observedThroughAt, "gap closes the current record")
	assert.True(t, rows[1].monitoringGap, "the new head carries the monitoring-gap flag")
	assert.Equal(t, start.Add(30*time.Minute), rows[1].observationStartedAt, "window reset")
}

// TestRolloutGate_WithinToleranceExtends: a late-but-within-tolerance sample
// is still a passing extension.
func TestRolloutGate_WithinToleranceExtends(t *testing.T) {
	phase := "gate_tolerance_" + uniqueSuffix(t)
	cleanupGatePhase(t, phase)
	defer cleanupGatePhase(t, phase)

	start := time.Now().UTC().Truncate(time.Microsecond)
	_, err := RecordRolloutGateSample(context.Background(), newGatePool(t), sample(phase, start))
	require.NoError(t, err)

	_, err = RecordRolloutGateSample(context.Background(), newGatePool(t), sample(phase, start.Add(4*time.Minute)))
	require.NoError(t, err)

	rows := gateRowsForPhase(t, phase)
	require.Len(t, rows, 2)
	assert.Equal(t, start, rows[1].observationStartedAt, "within tolerance: window continues")
	assert.Nil(t, rows[1].observedThroughAt)
}

// TestRolloutGate_StableCounterAfterMismatchExtends: the cumulative counter
// stays raised after a reset (it never decrements), so later clean samples
// with the same count must extend — not re-fire.
func TestRolloutGate_StableCounterAfterMismatchExtends(t *testing.T) {
	phase := "gate_stable_" + uniqueSuffix(t)
	cleanupGatePhase(t, phase)
	defer cleanupGatePhase(t, phase)

	start := time.Now().UTC().Truncate(time.Microsecond)
	bad := sample(phase, start)
	bad.MismatchCount = 3
	stamp := start.Add(-time.Hour)
	bad.LastMismatchAt = &stamp
	_, err := RecordRolloutGateSample(context.Background(), newGatePool(t), bad)
	require.NoError(t, err)

	// Same cumulative count, no new stamp: no new mismatch → extend.
	clean := sample(phase, start.Add(1*time.Minute))
	clean.MismatchCount = 3
	clean.LastMismatchAt = &stamp
	_, err = RecordRolloutGateSample(context.Background(), newGatePool(t), clean)
	require.NoError(t, err)

	rows := gateRowsForPhase(t, phase)
	require.Len(t, rows, 2)
	assert.Nil(t, rows[0].observedThroughAt, "no new mismatch: record stays open")
	assert.Equal(t, start, rows[1].observationStartedAt, "window continues")
}

// TestRolloutGate_ApprovalRequiresCompleteWindow: rejection paths — no
// window, incomplete window, and success only after a full 72h passing
// window; the approval row carries the identity and the next sample starts
// a fresh window.
func TestRolloutGate_ApprovalRequiresCompleteWindow(t *testing.T) {
	phase := "gate_approval_" + uniqueSuffix(t)
	cleanupGatePhase(t, phase)
	defer cleanupGatePhase(t, phase)
	ctx := context.Background()
	pool := newGatePool(t)

	// 1. No window at all → rejected.
	_, err := ApproveRouteDisable(ctx, pool, RouteDisableApproval{
		Phase: phase, ApprovalID: "appr-1", PrincipalID: "ops-1", ObservedAt: time.Now(),
	})
	require.Error(t, err, "approval without any window is rejected")

	// 2. Open a window and build a trail of continuous passing samples. The
	//    gap check compares p_observed_at against the previous record's
	//    created_at (the real insert time), so samples must be spaced well
	//    inside the tolerance: 1m spacing with a 10m tolerance keeps all 9
	//    samples (max spread 8m) within it. The 72h requirement is then
	//    proven by the explicit approval observed_at, not by sample spacing.
	start := time.Now().UTC().Truncate(time.Microsecond)
	first := sample(phase, start)
	first.MaxGapInterval = 10 * time.Minute
	_, err = RecordRolloutGateSample(ctx, pool, first)
	require.NoError(t, err)
	for i := 1; i <= 8; i++ {
		s := sample(phase, start.Add(time.Duration(i)*time.Minute))
		s.MaxGapInterval = 10 * time.Minute
		_, err = RecordRolloutGateSample(ctx, pool, s)
		require.NoError(t, err)
	}

	// 3. Incomplete window (71h < 72h) → rejected.
	_, err = ApproveRouteDisable(ctx, pool, RouteDisableApproval{
		Phase: phase, ApprovalID: "appr-2", PrincipalID: "ops-1", ObservedAt: start.Add(71 * time.Hour),
	})
	require.Error(t, err, "approval before 72h is rejected")

	// 4. Complete continuous passing window → approved.
	approvalAt := start.Add(73 * time.Hour)
	gateID, err := ApproveRouteDisable(ctx, pool, RouteDisableApproval{
		Phase: phase, ApprovalID: "appr-3", PrincipalID: "ops-1", ObservedAt: approvalAt,
	})
	require.NoError(t, err)

	rows := gateRowsForPhase(t, phase)
	require.Len(t, rows, 10)
	head := rows[len(rows)-1]
	assert.Equal(t, int64(gateID), head.gateID)
	require.NotNil(t, head.approvalID)
	assert.Equal(t, "appr-3", *head.approvalID, "approval identity recorded")
	require.NotNil(t, head.observedThroughAt, "approval closes the window")
	assert.Equal(t, start, head.observationStartedAt, "the approved window is the complete passing run")

	// 5. The next sample after the approval starts a fresh window.
	_, err = RecordRolloutGateSample(ctx, pool, sample(phase, approvalAt.Add(1*time.Minute)))
	require.NoError(t, err)
	rows = gateRowsForPhase(t, phase)
	require.Len(t, rows, 11)
	assert.Equal(t, approvalAt.Add(1*time.Minute), rows[10].observationStartedAt, "post-approval sampling starts a new window")
}

// TestRolloutGate_ApprovalRejectsResetWindow: a window reset by a mismatch
// restarts the 72h clock — approval based on the OLD window start fails.
func TestRolloutGate_ApprovalRejectsResetWindow(t *testing.T) {
	phase := "gate_resetapproval_" + uniqueSuffix(t)
	cleanupGatePhase(t, phase)
	defer cleanupGatePhase(t, phase)
	ctx := context.Background()
	pool := newGatePool(t)

	start := time.Now().UTC().Truncate(time.Microsecond)
	first := sample(phase, start)
	first.MaxGapInterval = 2 * time.Hour
	_, err := RecordRolloutGateSample(ctx, pool, first)
	require.NoError(t, err)

	// A mismatch resets the window at start+1h (tolerance 2h, so the reset is
	// driven by the counter increase — not by a monitoring gap).
	bad := sample(phase, start.Add(1*time.Hour))
	bad.MaxGapInterval = 2 * time.Hour
	bad.MismatchCount = 1
	_, err = RecordRolloutGateSample(ctx, pool, bad)
	require.NoError(t, err)

	// 72h after the ORIGINAL start — but the window restarted at start+1h,
	// so only 71h of the current window have passed → rejected.
	_, err = ApproveRouteDisable(ctx, pool, RouteDisableApproval{
		Phase: phase, ApprovalID: "appr-x", PrincipalID: "ops-1", ObservedAt: start.Add(72 * time.Hour),
	})
	require.Error(t, err, "approval uses the reset window start, not the original one")
}
