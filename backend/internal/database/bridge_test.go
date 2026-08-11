// Bridge dual-write verification (task T041; data-model.md "Compatibility
// bridge control and PostgreSQL role matrix").
//
// Four initial write paths must keep users.department_id and
// organization_user_departments in sync:
//
//  1. legacy users INSERT → Organization state row (department or tombstone, v1);
//  2. legacy users.department_id change → upsert with version increment;
//  3. Organization state INSERT/UPDATE → legacy column write-back;
//  4. legacy users DELETE → physical state delete, only while
//     compatibility_bridge_mode.legacy_delete_sync_enabled.
//
// Every path is asserted with a continuous count/value/version comparison of
// the two representations, and the recursion guard is proven by the fact
// that a bridge write never re-triggers its own source representation.
//
// These tests share the package's container database (TestMain); all rows use
// per-test unique names and the bridge mode is restored to its default.
package database

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- helpers -----------------------------------------------------------------

// uniqueSuffix makes per-test row names collision-free on the shared DB.
func uniqueSuffix(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("%s_%d", t.Name(), time.Now().UnixNano())
}

// createDepartment inserts a department and returns its id.
func createDepartment(t *testing.T, name string) int64 {
	t.Helper()
	var id int64
	err := testConn.QueryRow(context.Background(),
		"INSERT INTO departments (name) VALUES ($1) RETURNING id", name).Scan(&id)
	require.NoError(t, err)
	return id
}

// insertLegacyUser inserts a user the legacy way (INSERT with department_id)
// and returns the new id; the bridge must create the state row. Since 000006
// every INSERT states lifecycle_state and version explicitly.
func insertLegacyUser(t *testing.T, username string, departmentID *int64) int64 {
	t.Helper()
	var id int64
	err := testConn.QueryRow(context.Background(),
		"INSERT INTO users (username, password_hash, lifecycle_state, version, department_id) VALUES ($1, 'bridge-test-hash', 'active', 1, $2) RETURNING id",
		username, departmentID).Scan(&id)
	require.NoError(t, err)
	return id
}

// stateRow reads the Organization membership state for a user.
func stateRow(t *testing.T, userID int64) (*int64, int64) {
	t.Helper()
	var dept *int64
	var version int64
	require.NoError(t, testConn.QueryRow(context.Background(),
		"SELECT department_id, membership_version FROM organization_user_departments WHERE user_id = $1",
		userID).Scan(&dept, &version))
	return dept, version
}

// legacyDepartment reads users.department_id.
func legacyDepartment(t *testing.T, userID int64) *int64 {
	t.Helper()
	var dept *int64
	require.NoError(t, testConn.QueryRow(context.Background(),
		"SELECT department_id FROM users WHERE id = $1", userID).Scan(&dept))
	return dept
}

// assertDualWrite compares both representations for one user: department
// values must agree and the state row must exist at the expected version.
func assertDualWrite(t *testing.T, userID int64, wantDepartment *int64, wantVersion int64) {
	t.Helper()
	dept, version := stateRow(t, userID)
	assert.Equal(t, wantDepartment, dept, "organization state department")
	assert.Equal(t, wantVersion, version, "organization state version")
	assert.Equal(t, wantDepartment, legacyDepartment(t, userID), "legacy users.department_id")
}

// bridgeModeVersion reads the current compatibility_bridge_mode version.
func bridgeModeVersion(t *testing.T) int64 {
	t.Helper()
	var v int64
	require.NoError(t, testConn.QueryRow(context.Background(),
		"SELECT version FROM compatibility_bridge_mode WHERE id = 1").Scan(&v))
	return v
}

// setLegacyDeleteSyncMode flips the bridge mode through the controlled CAS
// function (the shared DB's version drifts, so callers pass the current one)
// and returns the new version.
func setLegacyDeleteSyncMode(t *testing.T, expectedVersion int64, enabled bool) int64 {
	t.Helper()
	var newVersion int64
	err := testConn.QueryRow(context.Background(),
		`SELECT set_legacy_delete_sync_mode($1, $2, 'bridge-test-principal',
		                                  'bridge_test', NULL, 'task-T041', NULL, 'correlation-T041')`,
		expectedVersion, enabled).Scan(&newVersion)
	require.NoError(t, err)
	return newVersion
}

// --- write paths ---------------------------------------------------------------

// TestBridge_LegacyInsertCreatesStateRow: path 1 — every legacy INSERT gets a
// v1 state row, with the department (or a tombstone when none).
func TestBridge_LegacyInsertCreatesStateRow(t *testing.T) {
	ctx := context.Background()
	u := uniqueSuffix(t)

	dept := createDepartment(t, "bridge_dept_"+u)
	u1 := insertLegacyUser(t, "bridge_u1_"+u, &dept)
	assertDualWrite(t, u1, &dept, 1)

	u2 := insertLegacyUser(t, "bridge_u2_"+u, nil)
	assertDualWrite(t, u2, nil, 1) // tombstone: state exists, department null

	// continuous count check: every legacy row has exactly one state row
	var count int64
	require.NoError(t, testConn.QueryRow(ctx,
		"SELECT count(*) FROM organization_user_departments WHERE user_id IN ($1, $2)", u1, u2).Scan(&count))
	assert.Equal(t, int64(2), count)
}

// TestBridge_LegacyDepartmentUpdateIncrements: path 2 — a legacy department
// change upserts the state row and increments the version; an unchanged value
// is a no-op.
func TestBridge_LegacyDepartmentUpdateIncrements(t *testing.T) {
	ctx := context.Background()
	u := uniqueSuffix(t)

	deptA := createDepartment(t, "bridge_deptA_"+u)
	deptB := createDepartment(t, "bridge_deptB_"+u)
	userID := insertLegacyUser(t, "bridge_u_"+u, &deptA)
	assertDualWrite(t, userID, &deptA, 1)

	_, err := testConn.Exec(ctx, "UPDATE users SET department_id = $1 WHERE id = $2", deptB, userID)
	require.NoError(t, err)
	assertDualWrite(t, userID, &deptB, 2)

	// same-value update: the bridge skips (IS NOT DISTINCT), version stays
	_, err = testConn.Exec(ctx, "UPDATE users SET department_id = $1 WHERE id = $2", deptB, userID)
	require.NoError(t, err)
	assertDualWrite(t, userID, &deptB, 2)
}

// TestBridge_OrgStateWritesBackLegacyColumn: path 3 — Organization state
// INSERT/UPDATE writes the legacy column, and the guarded chain terminates
// (the legacy write does not re-increment the state version).
func TestBridge_OrgStateWritesBackLegacyColumn(t *testing.T) {
	ctx := context.Background()
	u := uniqueSuffix(t)

	deptA := createDepartment(t, "bridge_deptA_"+u)
	deptB := createDepartment(t, "bridge_deptB_"+u)
	userID := insertLegacyUser(t, "bridge_u_"+u, &deptA)
	assertDualWrite(t, userID, &deptA, 1)

	// Organization-side INSERT (state missing for a legacy user, e.g. cleared):
	// legacy column must follow.
	_, err := testConn.Exec(ctx, "DELETE FROM organization_user_departments WHERE user_id = $1", userID)
	require.NoError(t, err)
	_, err = testConn.Exec(ctx,
		"INSERT INTO organization_user_departments (user_id, department_id, membership_version) VALUES ($1, $2, 1)",
		userID, deptB)
	require.NoError(t, err)
	assertDualWrite(t, userID, &deptB, 1)

	// Organization-side UPDATE: legacy column follows; the guarded legacy
	// UPDATE trigger does not re-fire, so the version is not double-bumped.
	_, err = testConn.Exec(ctx,
		`UPDATE organization_user_departments
		 SET department_id = $1, membership_version = 2, updated_at = now()
		 WHERE user_id = $2`, deptA, userID)
	require.NoError(t, err)
	assertDualWrite(t, userID, &deptA, 2)
}

// TestBridge_LegacyDeleteSyncsState: path 4, mode on (the default) — a legacy
// DELETE physically removes the state row.
func TestBridge_LegacyDeleteSyncsState(t *testing.T) {
	ctx := context.Background()
	u := uniqueSuffix(t)

	dept := createDepartment(t, "bridge_dept_"+u)
	userID := insertLegacyUser(t, "bridge_u_"+u, &dept)
	assertDualWrite(t, userID, &dept, 1)

	_, err := testConn.Exec(ctx, "DELETE FROM users WHERE id = $1", userID)
	require.NoError(t, err)

	var count int64
	require.NoError(t, testConn.QueryRow(ctx,
		"SELECT count(*) FROM organization_user_departments WHERE user_id = $1", userID).Scan(&count))
	assert.Equal(t, int64(0), count, "state row removed with the legacy user")
}

// TestBridge_LegacyDeleteModeOffKeepsState: path 4, mode off — the same DELETE
// leaves the state row untouched (the trigger reads the mode per delete).
func TestBridge_LegacyDeleteModeOffKeepsState(t *testing.T) {
	ctx := context.Background()
	u := uniqueSuffix(t)

	dept := createDepartment(t, "bridge_dept_"+u)
	userID := insertLegacyUser(t, "bridge_u_"+u, &dept)
	assertDualWrite(t, userID, &dept, 1)

	offVersion := setLegacyDeleteSyncMode(t, bridgeModeVersion(t), false)
	t.Cleanup(func() { setLegacyDeleteSyncMode(t, bridgeModeVersion(t), true) }) // restore default

	_, err := testConn.Exec(ctx, "DELETE FROM users WHERE id = $1", userID)
	require.NoError(t, err)

	// The legacy row is physically gone; the state row is the surviving
	// representation and must be untouched (department + version intact).
	var userCount int64
	require.NoError(t, testConn.QueryRow(ctx,
		"SELECT count(*) FROM users WHERE id = $1", userID).Scan(&userCount))
	assert.Zero(t, userCount, "legacy user physically deleted")
	stateDept, stateVersion := stateRow(t, userID)
	assert.Equal(t, &dept, stateDept, "state department survives the mode-off delete")
	assert.Equal(t, int64(1), stateVersion, "state version survives the mode-off delete")

	// idempotent replay: stale expected version but already-achieved target —
	// returns the current version without bumping or auditing.
	replayed := setLegacyDeleteSyncMode(t, offVersion-1, false)
	assert.Equal(t, offVersion, replayed, "idempotent replay returns current version")
	assert.Equal(t, offVersion, bridgeModeVersion(t), "replay does not bump the version")

	// stale CAS with a different target raises
	err = testConn.QueryRow(ctx,
		`SELECT set_legacy_delete_sync_mode($1, true, 'principal', 'bridge_test', NULL, 'stale', NULL, 'corr')`,
		offVersion-1).Scan(&replayed)
	require.Error(t, err, "stale version with a different target raises")
}

// TestBridge_ModeChangesAreAudited: the controlled mode switch appends an
// immutable audit row; the audit trail is part of the bridge contract.
func TestBridge_ModeChangesAreAudited(t *testing.T) {
	var before int64
	require.NoError(t, testConn.QueryRow(context.Background(),
		"SELECT count(*) FROM compatibility_bridge_mode_changes").Scan(&before))

	setLegacyDeleteSyncMode(t, bridgeModeVersion(t), false)
	t.Cleanup(func() { setLegacyDeleteSyncMode(t, bridgeModeVersion(t), true) })

	var after int64
	require.NoError(t, testConn.QueryRow(context.Background(),
		"SELECT count(*) FROM compatibility_bridge_mode_changes").Scan(&after))
	assert.Equal(t, before+1, after, "every mode transition appends an audit row")
}

// --- T076 Go-layer Platform mode operation -----------------------------------

// TestSetLegacyDeleteSyncMode_GoWrapper proves the exported Go operation
// (internal/database/bridge_mode.go) drives the same controlled CAS function
// the SQL tests use, appends the immutable audit row, and behaves
// idempotently on replay.
func TestSetLegacyDeleteSyncMode_GoWrapper(t *testing.T) {
	ctx := context.Background()
	// The Go operation takes the pool (the composition root's handle), not
	// testConn; derive a fresh pool from the shared connection string.
	pool, err := pgxpool.New(ctx, testConnStr)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	start := bridgeModeVersion(t)
	t.Cleanup(func() { setLegacyDeleteSyncMode(t, bridgeModeVersion(t), true) })

	var before int64
	require.NoError(t, testConn.QueryRow(ctx,
		"SELECT count(*) FROM compatibility_bridge_mode_changes").Scan(&before))

	newVersion, err := SetLegacyDeleteSyncMode(ctx, pool, BridgeModeTransition{
		ExpectedVersion:     start,
		Enabled:             false,
		PrincipalID:         "platform-ops",
		AuthorizationSource: "t076_test",
		ApprovalID:          "approval-t076",
		ReasonCode:          "delegation-verified",
		RequestID:           "req-t076",
		CorrelationID:       "corr-t076",
	})
	require.NoError(t, err)
	assert.Equal(t, start+1, newVersion, "a real transition bumps the version")

	// Immutable audit row appended with the Platform-operation fields.
	var audit struct {
		Principal   string
		Source      string
		Approval    string
		Prev, New   bool
		PrevV, NewV int64
	}
	require.NoError(t, testConn.QueryRow(ctx, `
		SELECT principal_id, authorization_source, approval_id,
		       previous_legacy_delete_sync_enabled, new_legacy_delete_sync_enabled,
		       previous_version, new_version
		FROM compatibility_bridge_mode_changes
		ORDER BY change_id DESC LIMIT 1`).Scan(
		&audit.Principal, &audit.Source, &audit.Approval,
		&audit.Prev, &audit.New, &audit.PrevV, &audit.NewV))
	assert.Equal(t, "platform-ops", audit.Principal)
	assert.Equal(t, "t076_test", audit.Source)
	assert.Equal(t, "approval-t076", audit.Approval)
	assert.Equal(t, true, audit.Prev, "previous mode was true (the default)")
	assert.Equal(t, false, audit.New)
	assert.Equal(t, start, audit.PrevV)
	assert.Equal(t, start+1, audit.NewV)

	// Idempotent replay: stale expected version, already-achieved target —
	// returns the current version without bumping or appending another row.
	replayed, err := SetLegacyDeleteSyncMode(ctx, pool, BridgeModeTransition{
		ExpectedVersion:     start, // stale — target already achieved
		Enabled:             false,
		PrincipalID:         "platform-ops",
		AuthorizationSource: "t076_test",
	})
	require.NoError(t, err)
	assert.Equal(t, start+1, replayed, "idempotent replay returns the current version")
	var after int64
	require.NoError(t, testConn.QueryRow(ctx,
		"SELECT count(*) FROM compatibility_bridge_mode_changes").Scan(&after))
	assert.Equal(t, before+1, after, "the replay appends no audit row")
}
