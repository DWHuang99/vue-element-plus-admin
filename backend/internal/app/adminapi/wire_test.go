// T074 wire smoke: the real composition root (NewWire) against a real
// PostgreSQL, exercising the metrics surface end to end — /metrics serves
// collector gauges, the recorder middleware counts HTTP classes, the measured
// port decorators classify errors, and writeError counts contract codes.
//
// This file holds the shipped-build wire tests (metrics / rollout gate /
// cleanup coordinator). The legacy-router wire tests (shadow reads, legacy
// metrics mount, delete delegation, direct delete) live in wire_legacy_test.go
// behind `//go:build rollback`.
package adminapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/hdw/vue-element-plus-admin/backend/db/migrations"
	"github.com/hdw/vue-element-plus-admin/backend/internal/database"
	"github.com/hdw/vue-element-plus-admin/backend/internal/platform"
	"github.com/hdw/vue-element-plus-admin/backend/internal/server"
)

// wireSmokeDB boots one shared Postgres container for the wire smoke test.
func wireSmokeDB(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("wire_smoke"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		testcontainers.WithWaitStrategyAndDeadline(
			60*time.Second,
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(60*time.Second),
		),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pg.Terminate(ctx) })

	connStr, err := pg.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	source, err := iofs.New(migrations.FS, ".")
	require.NoError(t, err)
	m, err := migrate.NewWithSourceInstance("iofs", source, connStr)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = m.Close() })
	require.NoError(t, m.Up(), "migrate to head")
	return connStr
}

func TestWire_MetricsEndToEnd(t *testing.T) {
	ctx := context.Background()
	connStr := wireSmokeDB(t)

	db, err := database.Connect(ctx, database.Config{URL: connStr, MaxConns: 5, MinConns: 1})
	require.NoError(t, err)
	t.Cleanup(db.Close)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := NewWire(db, logger, Config{
		AdminBFFRoutesEnabled:   true,
		OutboxDispatcherEnabled: true,
	}, nil, server.LegacyRateLimitConfig{})
	router, closeFn := w.Router()
	defer closeFn()

	// 1. /metrics serves collector gauges from the real adapters.
	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, w1.Code)
	require.Contains(t, w1.Body.String(), "outbox_pending ")
	require.Contains(t, w1.Body.String(), "provisioning_count ")
	require.Contains(t, w1.Body.String(), "workflow_failed_retryable ")
	require.Contains(t, w1.Body.String(), "inbox_processed_total ")

	// 2. An invalid bearer token reaches the measured auth port (a missing
	// token is rejected by the middleware without a port call), recording the
	// HTTP class and the typed auth-port error through the decorator.
	unauthReq := httptest.NewRequest(http.MethodGet, "/api/v1/roles", nil)
	unauthReq.Header.Set("Authorization", "Bearer not-a-real-token")
	unauth := httptest.NewRecorder()
	router.ServeHTTP(unauth, unauthReq)
	require.Equal(t, http.StatusUnauthorized, unauth.Code)

	// 3. A validation rejection records the bff per-code counter via writeError.
	bad := httptest.NewRecorder()
	router.ServeHTTP(bad, httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", nil))
	require.Equal(t, http.StatusBadRequest, bad.Code)

	// 4. The next scrape exposes everything recorded so far (this scrape
	// itself counts one more http request on the following one).
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := w2.Body.String()
	require.Contains(t, body, "http_401_total 1\n")
	require.Contains(t, body, "bff_errors_total 1\n")
	require.Contains(t, body, "bff_errors_AUTH_INVALID_INPUT_total 1\n")
	require.Contains(t, body, "port_auth_calls_total 1\n")
	require.Contains(t, body, "port_auth_errors_invalid_token_total 1\n")
}

// TestWire_RolloutGateEndToEnd (T077): the real composition root's writer
// loop records cadence evidence through the migration-000012 SECURITY
// DEFINER function. The sample source assembles live state — bridge mode,
// legacy/users vs new/organization parity over a seeded row, the manifest
// hash pinned at composition time — and the first two samples must extend
// one window (same observation_started_at), proving the full writer path:
// config → composition root → source assembly → store function → evidence
// row. Cancelling the background context stops the loop.
func TestWire_RolloutGateEndToEnd(t *testing.T) {
	ctx := context.Background()
	connStr := wireSmokeDB(t)

	db, err := database.Connect(ctx, database.Config{URL: connStr, MaxConns: 5, MinConns: 1})
	require.NoError(t, err)
	t.Cleanup(db.Close)

	// Seed one department + one legacy user: the 000008 insert bridge mirrors
	// the membership state (membership_version = users.version = 1), so the
	// parity surface must read 1 == 1 with equal canonical checksums — a real
	// row through the comparison, not just two empty tables.
	var deptID int64
	require.NoError(t, db.Pool.QueryRow(ctx,
		"INSERT INTO departments (name) VALUES ('gate-dept') RETURNING id").Scan(&deptID))
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO users (username, password_hash, lifecycle_state, version, department_id)
		VALUES ('gate_user', 'not-a-real-hash', 'active', 1, $1)`, deptID)
	require.NoError(t, err)

	const manifestHash = "beef0000dead0000manifest0000hash0000"
	bgCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	w := NewWire(db, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{
		RolloutGate: RolloutGateRuntime{
			Enabled:                true,
			SampleCadence:          50 * time.Millisecond,
			MaxGapInterval:         10 * time.Second,
			Phase:                  "wire-test",
			PrincipalID:            "wire-principal",
			RollbackArtifactID:     "artifact-004",
			RollbackSuiteResult:    "passed",
			CapabilityManifestHash: manifestHash,
		},
	}, nil, server.LegacyRateLimitConfig{})
	w.StartBackground(bgCtx)

	// First evidence row: the full writer path completed once.
	require.Eventually(t, func() bool {
		var n int64
		err := db.Pool.QueryRow(ctx,
			"SELECT count(*) FROM compatibility_rollout_gates WHERE phase = 'wire-test'").Scan(&n)
		return err == nil && n >= 1
	}, 10*time.Second, 50*time.Millisecond, "writer loop records the first sample")

	type gateEvidence struct {
		startedAt           time.Time
		manifestHash        string
		bridgeModeVersion   int64
		bridgeDeleteSyncOn  bool
		legacyRows, newRows int64
		rowChecksum         string
		mismatchCount       int64
		artifact, suiteRes  string
		principal           string
	}
	readEvidence := func() []gateEvidence {
		rows, err := db.Pool.Query(ctx, `
			SELECT observation_started_at, capability_manifest_hash, bridge_mode_version,
			       bridge_legacy_delete_sync_enabled, legacy_rows, new_rows,
			       row_version_checksum, mismatch_count, rollback_artifact_id,
			       rollback_suite_result, approving_principal_id
			FROM compatibility_rollout_gates WHERE phase = 'wire-test' ORDER BY gate_id`)
		require.NoError(t, err)
		defer rows.Close()
		var out []gateEvidence
		for rows.Next() {
			var e gateEvidence
			require.NoError(t, rows.Scan(&e.startedAt, &e.manifestHash, &e.bridgeModeVersion,
				&e.bridgeDeleteSyncOn, &e.legacyRows, &e.newRows, &e.rowChecksum,
				&e.mismatchCount, &e.artifact, &e.suiteRes, &e.principal))
			e.startedAt = e.startedAt.UTC()
			out = append(out, e)
		}
		require.NoError(t, rows.Err())
		return out
	}

	first := readEvidence()
	// The live writer keeps appending at the 50ms cadence, so the count is not
	// a stable barrier — read the earliest row (ORDER BY gate_id) and assert
	// on its content rather than requiring exactly one row.
	require.NotEmpty(t, first, "writer loop recorded at least the first sample")
	e := first[0]
	assert.Equal(t, manifestHash, e.manifestHash, "sample carries the composed manifest hash")
	assert.Equal(t, int64(1), e.bridgeModeVersion, "bridge mode row seeded by 000008")
	assert.True(t, e.bridgeDeleteSyncOn,
		"the pre-split seed keeps legacy delete sync on; the sample mirrors the live Platform-owned row")
	assert.Equal(t, int64(1), e.legacyRows, "legacy side of the seeded parity row")
	assert.Equal(t, int64(1), e.newRows, "new side of the seeded parity row")
	assert.NotEmpty(t, e.rowChecksum, "parity checksum over both canonical serializations")
	assert.Zero(t, e.mismatchCount, "seeded parity is identical — no mismatch")
	assert.Equal(t, "artifact-004", e.artifact)
	assert.Equal(t, "passed", e.suiteRes)
	assert.Equal(t, "wire-principal", e.principal)

	// A second sample at the 50ms cadence extends the same window (no gap at
	// 10s tolerance, parity still identical).
	require.Eventually(t, func() bool {
		return len(readEvidence()) >= 2
	}, 10*time.Second, 50*time.Millisecond, "writer keeps appending evidence")
	both := readEvidence()
	assert.Equal(t, both[0].startedAt, both[1].startedAt,
		"passing samples extend one continuous window")

	// Cancelling the background context stops the loop: the row count
	// stabilizes. At most one sample that was already in flight when cancel()
	// fired can still land; after that the count must not grow.
	before := len(readEvidence())
	cancel()
	require.Eventually(t, func() bool {
		return len(readEvidence()) <= before+1
	}, 5*time.Second, 50*time.Millisecond, "writer stops recording after cancellation")
	time.Sleep(200 * time.Millisecond)
	require.LessOrEqual(t, len(readEvidence()), before+1,
		"writer stopped recording after cancellation")
}

// TestWire_CleanupCoordinatorEndToEnd proves the T078 coordinator against the
// real composition root: NewWire wires the IAM + Organization cleanup adapters,
// the Admin BFF watermark source and the SECURITY DEFINER audit store. Seeded
// eligible evidence across both owners is purged through owner-local
// transactions at the raised (most conservative) cutoff, the immutable audit
// row is recorded then finalized as 'succeeded', and an unresolved workflow
// watermark blocks the purge before any deletion.
func TestWire_CleanupCoordinatorEndToEnd(t *testing.T) {
	ctx := context.Background()
	connStr := wireSmokeDB(t)

	db, err := database.Connect(ctx, database.Config{URL: connStr, MaxConns: 5, MinConns: 1})
	require.NoError(t, err)
	t.Cleanup(db.Close)

	w := NewWire(db, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{}, nil, server.LegacyRateLimitConfig{})

	now := time.Now().UTC().Truncate(time.Microsecond)

	// Seed eligible IAM + Organization evidence (all well past 30 days).
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO iam_command_receipts (operation_id, command_name, request_fingerprint, status, completed_at)
		VALUES ($1::uuid, 'cleanup-test', 'fp', 'succeeded', $2)`, uuid.NewString(), now.Add(-45*24*time.Hour))
	require.NoError(t, err)
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO iam_outbox_events (
		    event_id, event_type, event_version, producer, aggregate_type, aggregate_id,
		    aggregate_version, payload, correlation_id, occurred_at, status, available_at,
		    delivery_epoch, epoch_started_at, attempt_count, total_attempt_count,
		    lease_owner, leased_until, claim_token, last_error_code, blocked_at,
		    blocked_reason_code, published_at
		) VALUES ($1::uuid, 'iam.user.deleted', 1, 'iam', 'user', '42', 1, '{}'::jsonb,
		          'cleanup-correlation', $2, 'published', $2, 1, $2, 0, 0, NULL, NULL, NULL, NULL, NULL, NULL, $3)`,
		uuid.NewString(), now.Add(-40*24*time.Hour), now.Add(-40*24*time.Hour))
	require.NoError(t, err)
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO organization_command_receipts (operation_id, command_name, request_fingerprint, status, completed_at)
		VALUES ($1::uuid, 'cleanup-test', 'fp', 'succeeded', $2)`, uuid.NewString(), now.Add(-45*24*time.Hour))
	require.NoError(t, err)
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO organization_inbox_messages (
		    event_id, handler_name, event_type, event_version, aggregate_id, aggregate_version,
		    received_at, processed_at)
		VALUES ($1::uuid, 'department_handler', 'iam.user.deleted', 1, '42', 1, $2, $2)`,
		uuid.NewString(), now.Add(-50*24*time.Hour))
	require.NoError(t, err)

	// Dry-run at a recent cutoff: every seeded row (40-50d old) is eligible,
	// and the coordinator merges both owners' counts plus the BFF watermark
	// (empty workflow table → zero unresolved).
	ev, err := w.cleanup.Evaluate(ctx, now)
	require.NoError(t, err)
	assert.Equal(t, map[string]int64{
		"iam.command_receipts":          1,
		"iam.outbox_events":             1,
		"iam.outbox_requeues":           0, // IAM always reports all three keys
		"organization.command_receipts": 1,
		"organization.inbox_messages":   1,
	}, ev.EligibleCounts)
	// The most conservative cutoff never precedes the requested cutoff.
	assert.False(t, ev.EffectiveCutoff.Before(now), "effective cutoff is raised to the most conservative watermark")

	// An unresolved workflow blocks the purge before any deletion.
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO admin_workflows (
		    operation_id, operation_type, idempotency_key, request_fingerprint, actor_user_id,
		    state, current_step, compensation_state, attempt_count, retry_deadline_at, created_at, updated_at)
		VALUES ($1::uuid, 'managed_user.create', 'ik-1', 'fp-1', 7, 'pending', 'created', 'not_required',
		        0, $2, $2, $2)`, uuid.NewString(), now.Add(-1*time.Hour))
	require.NoError(t, err)
	_, err = w.cleanup.Purge(ctx, now, platform.TrustedCleanupContext{
		PrincipalID: 7, AuthorizationSource: "platform_operator_session", ApprovalID: "appr-wire",
	})
	require.ErrorIs(t, err, platform.ErrCleanupBlocked, "unresolved workflow blocks deletion")
	assertWorkflowRows(t, ctx, db, map[string]int64{
		"iam_command_receipts":          1,
		"iam_outbox_events":             1,
		"organization_command_receipts": 1,
		"organization_inbox_messages":   1,
	})

	// Resolve the workflow (terminal, no active subject) so the watermark clears.
	_, err = db.Pool.Exec(ctx, `
		UPDATE admin_workflows SET state = 'succeeded', current_step = 'done', compensation_state = 'not_required',
		       completed_at = now() WHERE state = 'pending'`)
	require.NoError(t, err)

	// Approved purge: both owners delete their eligible rows and the audit is
	// recorded + finalized as 'succeeded'.
	purged, err := w.cleanup.Purge(ctx, now, platform.TrustedCleanupContext{
		PrincipalID: 7, AuthorizationSource: "platform_operator_session", ApprovalID: "appr-wire",
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]int64{
		"iam.command_receipts":          1,
		"iam.outbox_events":             1,
		"iam.outbox_requeues":           0, // IAM always reports all three keys
		"organization.command_receipts": 1,
		"organization.inbox_messages":   1,
	}, purged)
	assertWorkflowRows(t, ctx, db, map[string]int64{
		"iam_command_receipts":          0,
		"iam_outbox_events":             0,
		"organization_command_receipts": 0,
		"organization_inbox_messages":   0,
	})

	// The immutable audit row records the approval + dry-run counts, then the
	// terminal 'succeeded' outcome with the purged counts.
	var status, approvalID, principal string
	var purgedCounts []byte
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT status, approval_id, principal_id, purged_counts
		FROM platform_evidence_cleanup_audit ORDER BY created_at DESC LIMIT 1`).
		Scan(&status, &approvalID, &principal, &purgedCounts))
	assert.Equal(t, "succeeded", status)
	assert.Equal(t, "appr-wire", approvalID)
	assert.Equal(t, "7", principal)
	require.NotNil(t, purgedCounts)
	require.Contains(t, string(purgedCounts), `"iam.command_receipts": 1`)
}

// assertWorkflowRows checks the live row counts of the evidence tables used by
// the wire cleanup test.
func assertWorkflowRows(t *testing.T, ctx context.Context, db *database.DB, want map[string]int64) {
	t.Helper()
	for table, wantCount := range want {
		var n int64
		require.NoError(t, db.Pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n),
			"count %s", table)
		assert.Equal(t, wantCount, n, "count %s", table)
	}
}
