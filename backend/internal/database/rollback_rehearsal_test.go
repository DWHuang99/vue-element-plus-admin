// T080 rollback rehearsal tests (quickstart §10, plan Rollback §4-6). The
// cross-owner rehearsal runs against the real bridge installed by migration
// 000008: Organization membership is the source of truth while the new code
// is active, so the rollback first final-syncs Organization state back into
// legacy users.department_id (with exact bidirectional mapping, count/value
// parity, and legacy FK/index verified), and only then — if the pinned
// 004-compatible rollback handler can direct-delete users — sets the delete
// bridge mode=true through the controlled Platform operation and verifies the
// immutable mode audit and trigger behavior. Dormant additive evidence is
// preserved (no schema dropped), and permission/RBAC migrations 000004 and
// 000005 are never rolled back (quickstart §10.9 / "Never roll back").
//
// The bridge itself is continuously verified in bridge_test.go (T041-T042);
// this file rehearses the rollback procedure those tests do not compose.
package database

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRollbackRehearsal_FinalSyncBidirectionalParity (quickstart §10.4-5):
// verifies the exact bidirectional mapping (legacy insert → org membership,
// org membership → legacy column, legacy column → org membership), then
// rehearses the final-sync while the new code is still active: during a
// bridge-down window a membership change is not mirrored to legacy and a
// legacy delete does not cascade (both simulated via the bridge's own
// recursion guard, app.bridge_guard, which suspends the SECURITY DEFINER
// functions exactly as an inoperative bridge would). The idempotent final-sync
// reconciles legacy users.department_id to the Organization state, and the
// stale-Organization exclusion reconcile removes memberships for IAM users
// that no longer exist. After the sync, count/value parity holds, the legacy
// FK is still enforced, and the legacy index still exists.
func TestRollbackRehearsal_FinalSyncBidirectionalParity(t *testing.T) {
	ctx := context.Background()
	u := uniqueSuffix(t)

	deptA := createDepartment(t, "rb_deptA_"+u)
	deptB := createDepartment(t, "rb_deptB_"+u)
	userID := insertLegacyUser(t, "rb_u_"+u, &deptA)
	assertDualWrite(t, userID, &deptA, 1) // legacy insert → org membership v1
	// A second user is deleted while the bridge is down (§10.4); its org
	// membership survives and becomes the stale-Organization exclusion.
	staleUserID := insertLegacyUser(t, "rb_stale_"+u, &deptA)
	assertDualWrite(t, staleUserID, &deptA, 1)

	// Bidirectional mapping (§10.5: "verify exact bidirectional mapping"):
	//   org → legacy: a membership update mirrors into users.department_id.
	_, err := testConn.Exec(ctx,
		"UPDATE organization_user_departments SET department_id = $1 WHERE user_id = $2", deptB, userID)
	require.NoError(t, err)
	assertLegacyDepartment(t, userID, deptB)
	//   legacy → org: a legacy department change mirrors into the membership.
	_, err = testConn.Exec(ctx,
		"UPDATE users SET department_id = $1 WHERE id = $2", deptA, userID)
	require.NoError(t, err)
	assertOrgDepartment(t, userID, deptA)

	// Final-sync: Organization state is the source of truth while the new
	// code runs. A bridge-down window is simulated with the bridge's own
	// recursion guard (app.bridge_guard): the org→legacy mirror is suspended
	// and the legacy delete cascade is suppressed — exactly the stale state
	// a rollback reconciles. The guard is transaction-local, so the shared DB
	// is never left with the guard stuck or the representations half-synced.
	tx, err := testConn.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx) //nolint:errcheck // rollback on failure leaves the DB clean

	_, err = tx.Exec(ctx, `SELECT pg_catalog.set_config('app.bridge_guard', 'on', true)`)
	require.NoError(t, err)

	// Drift: the membership change is not mirrored to legacy.
	_, err = tx.Exec(ctx,
		"UPDATE organization_user_departments SET department_id = $1 WHERE user_id = $2", deptB, userID)
	require.NoError(t, err)
	// Stale exclusion: the legacy delete does not cascade while the bridge is
	// down, so the org membership survives a now-deleted IAM user.
	_, err = tx.Exec(ctx, "DELETE FROM users WHERE id = $1", staleUserID)
	require.NoError(t, err)

	var legacyDept int64
	require.NoError(t, tx.QueryRow(ctx,
		"SELECT department_id FROM users WHERE id = $1", userID).Scan(&legacyDept))
	assert.Equal(t, deptA, legacyDept, "drift: legacy department is stale while the bridge is down")

	// Bridge back up; final-sync legacy department values to the Organization
	// state, then reconcile stale-Organization exclusions (memberships whose
	// IAM user no longer exists).
	_, err = tx.Exec(ctx, `SELECT pg_catalog.set_config('app.bridge_guard', 'off', true)`)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `
		UPDATE users u
		SET department_id = m.department_id, updated_at = now()
		FROM organization_user_departments m
		WHERE u.id = m.user_id AND u.department_id IS DISTINCT FROM m.department_id`)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `
		DELETE FROM organization_user_departments m
		WHERE NOT EXISTS (SELECT 1 FROM users u WHERE u.id = m.user_id)`)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))

	// Parity restored: legacy matches org in value and count; the stale
	// membership exclusion is reconciled away.
	assertLegacyDepartment(t, userID, deptB)
	assertOrgDepartment(t, userID, deptB)
	var orgRows int64
	require.NoError(t, testConn.QueryRow(ctx,
		"SELECT count(*) FROM organization_user_departments WHERE user_id = $1", userID).Scan(&orgRows))
	assert.Equal(t, int64(1), orgRows, "count parity: one membership per legacy user")
	var staleRows int64
	require.NoError(t, testConn.QueryRow(ctx,
		"SELECT count(*) FROM organization_user_departments WHERE user_id = $1", staleUserID).Scan(&staleRows))
	assert.Zero(t, staleRows, "the stale-Organization exclusion for the deleted IAM user is reconciled")

	// Legacy FK still enforced: a department that does not exist is rejected.
	_, err = testConn.Exec(ctx,
		"UPDATE users SET department_id = $1 WHERE id = $2", 999999, userID)
	require.Error(t, err, "legacy FK users.department_id → departments.id is enforced")

	// Legacy index still present (000004 idx_users_department_id).
	var idxExists bool
	require.NoError(t, testConn.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_indexes
			WHERE schemaname = 'public' AND tablename = 'users' AND indexname = 'idx_users_department_id'
		)`).Scan(&idxExists))
	assert.True(t, idxExists, "legacy index on users.department_id is not dropped")
}

// TestRollbackRehearsal_DeleteSyncModeTrueDormantEvidence (quickstart
// §10.6/§10.9): if the pinned rollback handler can direct-delete users, the
// delete bridge mode flips to true through the short-lived authenticated
// Platform operation — the immutable audit row records the transition and the
// delete trigger once again cascades a legacy direct delete into the
// Organization membership. Finally the rehearsal asserts the dormant additive
// evidence is preserved (no schema dropped) and permission migration 000004 /
// RBAC migration 000005 are never rolled back.
func TestRollbackRehearsal_DeleteSyncModeTrueDormantEvidence(t *testing.T) {
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, testConnStr)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	t.Cleanup(func() { setLegacyDeleteSyncMode(t, bridgeModeVersion(t), true) })

	// Force a real false→true transition (the rollback direction). If the
	// shared DB is currently true, flip to false first (feature direction),
	// then back to true — the final transition always records a false→true
	// audit row. A same-value request is idempotent (no audit), which is why
	// the pre-flip is needed.
	if bridgeModeEnabled(t) {
		_, err := SetLegacyDeleteSyncMode(ctx, pool, BridgeModeTransition{
			ExpectedVersion:     bridgeModeVersion(t),
			Enabled:             false,
			PrincipalID:         "platform-ops",
			AuthorizationSource: "t080_rehearsal",
			ApprovalID:          "approval-feature",
			ReasonCode:          "feature-active",
			RequestID:           "req-feature",
			CorrelationID:       "corr-feature",
		})
		require.NoError(t, err)
	}
	_, err = SetLegacyDeleteSyncMode(ctx, pool, BridgeModeTransition{
		ExpectedVersion:     bridgeModeVersion(t),
		Enabled:             true,
		PrincipalID:         "platform-ops-rollback",
		AuthorizationSource: "t080_rehearsal",
		ApprovalID:          "approval-rollback",
		ReasonCode:          "rollback-rehearsal",
		RequestID:           "req-rollback",
		CorrelationID:       "corr-rollback",
	})
	require.NoError(t, err)

	// Immutable audit: the false→true rollback transition is recorded with the
	// Platform-operation identity (packages in this suite run sequentially, so
	// the latest row is this test's own transition).
	var audit struct {
		Principal string
		Source    string
		Approval  string
		Prev, New bool
	}
	require.NoError(t, testConn.QueryRow(ctx, `
		SELECT principal_id, authorization_source, approval_id,
		       previous_legacy_delete_sync_enabled, new_legacy_delete_sync_enabled
		FROM compatibility_bridge_mode_changes
		ORDER BY change_id DESC LIMIT 1`).Scan(
		&audit.Principal, &audit.Source, &audit.Approval, &audit.Prev, &audit.New))
	assert.Equal(t, "platform-ops-rollback", audit.Principal)
	assert.Equal(t, "t080_rehearsal", audit.Source)
	assert.Equal(t, "approval-rollback", audit.Approval)
	assert.False(t, audit.Prev, "previous mode was false (feature active)")
	assert.True(t, audit.New, "rollback mode is true (legacy direct delete re-enabled)")

	// Trigger behavior: with mode=true a legacy direct delete cascades into
	// the Organization membership (users_delete_bridge).
	u := uniqueSuffix(t)
	dept := createDepartment(t, "rb_del_"+u)
	userID := insertLegacyUser(t, "rb_del_u_"+u, &dept)
	assertDualWrite(t, userID, &dept, 1)
	_, err = testConn.Exec(ctx, "DELETE FROM users WHERE id = $1", userID)
	require.NoError(t, err)
	var orgRows int64
	require.NoError(t, testConn.QueryRow(ctx,
		"SELECT count(*) FROM organization_user_departments WHERE user_id = $1", userID).Scan(&orgRows))
	assert.Zero(t, orgRows, "mode=true: the legacy direct delete cascades to the membership")

	// Dormant additive evidence: the workflow/receipt/outbox/inbox/
	// compatibility schema is preserved — no table is dropped as part of the
	// rollback rehearsal (quickstart §10.9).
	for _, table := range []string{
		"admin_workflows", "iam_command_receipts", "iam_outbox_events",
		"iam_outbox_requeues", "organization_inbox_messages",
		"organization_command_receipts", "compatibility_rollout_gates",
		"compatibility_bridge_mode_changes",
	} {
		var exists bool
		require.NoError(t, testConn.QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1)",
			table).Scan(&exists))
		assert.True(t, exists, "dormant evidence table %s is preserved", table)
	}

	// Never roll back: the RBAC migration (000004) and permission migration
	// (000005) objects must still be present. golang-migrate's schema_migrations
	// tracks only the current version, so the strongest evidence that no
	// migration was rewound is (a) the migrator is still at version 13 (the
	// head of the chain) and (b) every table each migration created still
	// exists with its seed rows intact.
	var currentVersion int64
	require.NoError(t, testConn.QueryRow(ctx,
		"SELECT version FROM schema_migrations").Scan(&currentVersion))
	assert.Equal(t, int64(13), currentVersion,
		"the migration chain is still at head — nothing was rolled back")

	// RBAC migration 000004 objects (roles, user_roles) and permission
	// migration 000005 objects (permissions, role_permissions).
	rbacTables := []string{"roles", "user_roles", "departments"}
	permissionTables := []string{"permissions", "role_permissions"}
	var exists bool
	for _, table := range append(append([]string{}, rbacTables...), permissionTables...) {
		require.NoError(t, testConn.QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1)",
			table).Scan(&exists))
		assert.True(t, exists, "migration object table %s is still present", table)
	}
	// 000005 seed rows: at least one permission code and one role_permission
	// grant survive.
	var permissionRows, rolePermissionRows int64
	require.NoError(t, testConn.QueryRow(ctx,
		"SELECT count(*) FROM permissions").Scan(&permissionRows))
	require.NoError(t, testConn.QueryRow(ctx,
		"SELECT count(*) FROM role_permissions").Scan(&rolePermissionRows))
	assert.Positive(t, permissionRows, "permission migration 000005 seed data is still present")
	assert.Positive(t, rolePermissionRows, "permission migration 000005 grants are still present")
}

// assertLegacyDepartment reads the legacy users.department_id for the user.
func assertLegacyDepartment(t *testing.T, userID int64, want int64) {
	t.Helper()
	ctx := context.Background()
	var dept int64
	require.NoError(t, testConn.QueryRow(ctx,
		"SELECT department_id FROM users WHERE id = $1", userID).Scan(&dept))
	assert.Equal(t, want, dept, "legacy users.department_id matches the Organization state")
}

// assertOrgDepartment reads the Organization membership department for the user.
func assertOrgDepartment(t *testing.T, userID int64, want int64) {
	t.Helper()
	ctx := context.Background()
	var dept int64
	require.NoError(t, testConn.QueryRow(ctx,
		"SELECT department_id FROM organization_user_departments WHERE user_id = $1", userID).Scan(&dept))
	assert.Equal(t, want, dept, "Organization membership matches the legacy column")
}

// bridgeModeEnabled reads the current delete-bridge mode on the shared DB.
func bridgeModeEnabled(t *testing.T) bool {
	t.Helper()
	var enabled bool
	require.NoError(t, testConn.QueryRow(context.Background(),
		"SELECT legacy_delete_sync_enabled FROM compatibility_bridge_mode WHERE id = 1").Scan(&enabled))
	return enabled
}
