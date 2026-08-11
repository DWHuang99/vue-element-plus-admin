// Migration upgrade/rollback tests (quickstart Checkpoint C, T014).
//
// Each test drives its own scratch database on the shared container:
// migrate to 000005, seed a representative 000005-era dataset (users with
// and without departments, multi-role users, built-in roles), migrate to
// head (000006-000008) and verify the backfill/state/bridge invariants,
// then roll back to 000005 and prove the legacy schema and seeds survive.
//
// The shared TestMain (rbac_migration_test.go) already migrated scaffold_test
// to head; these tests deliberately re-derive the 000005 state themselves so
// the upgrade path is exercised from the real pre-feature baseline.
package database

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hdw/vue-element-plus-admin/backend/db/migrations"
)

// freshDB creates a scratch database on the shared container and returns a
// connection string pointing at it. The database is dropped on test cleanup.
func freshDB(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	dbname := fmt.Sprintf("upgrade_%d", time.Now().UnixNano())
	// DDL cannot be parameterized; the name is generated internally.
	_, err := testConn.Exec(ctx, "CREATE DATABASE "+dbname)
	require.NoError(t, err, "create scratch database")
	t.Cleanup(func() {
		_, _ = testConn.Exec(context.Background(), "DROP DATABASE IF EXISTS "+dbname+" WITH (FORCE)")
	})
	return swapDB(dbname)
}

// migrateTo runs the chain to version (1-based), up or down as needed
// (golang-migrate's Migrate(version) is bidirectional), returning the
// migrator so the caller can roll back afterwards.
func migrateTo(t *testing.T, connStr string, version uint) *migrate.Migrate {
	t.Helper()
	source, err := iofs.New(migrations.FS, ".")
	require.NoError(t, err)
	m, err := migrate.NewWithSourceInstance("iofs", source, connStr)
	require.NoError(t, err)
	t.Cleanup(func() { m.Close() })
	require.NoError(t, m.Migrate(version), "migrate to version %d", version)
	return m
}

// seedLegacyData populates a 000005-state database: two departments
// (research + one child), four users (with/without department, single/multi
// role), built-in roles are already seeded by 000004. Uses only the
// pre-feature columns; password hashes are inert test fixtures.
func seedLegacyData(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	ctx := context.Background()

	_, err := conn.Exec(ctx, `
		INSERT INTO users (username, password_hash, account, email, department_id) VALUES
			('with_dept', 'hash_fixture_1', '带部门用户', 'with@example.com',
			 (SELECT id FROM departments WHERE name = '研发部')),
			('no_dept', 'hash_fixture_2', NULL, NULL, NULL),
			('multi_role', 'hash_fixture_3', '多角色用户', 'multi@example.com',
			 (SELECT id FROM departments WHERE name = '前端组')),
			('admin_user', 'hash_fixture_4', '管理员', 'admin@example.com',
			 (SELECT id FROM departments WHERE name = '研发部'));
	`)
	require.NoError(t, err)

	// Multi-role: multi_role gets admin + user; admin_user gets super_admin.
	_, err = conn.Exec(ctx, `
		INSERT INTO user_roles (user_id, role_id)
		SELECT u.id, r.id FROM users u, roles r
		WHERE u.username = 'multi_role' AND r.code IN ('admin', 'user')
		ON CONFLICT DO NOTHING;
		INSERT INTO user_roles (user_id, role_id)
		SELECT u.id, r.id FROM users u, roles r
		WHERE u.username = 'admin_user' AND r.code = 'super_admin';
	`)
	require.NoError(t, err)
}

// connect opens a pgx connection to a scratch database.
func connect(t *testing.T, connStr string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), connStr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

// TestUpgrade000005ToHead_BackfillAndState is Checkpoint C.1-C.4: after
// 000006-000008, every legacy user is active/version 1 with a versioned
// state row (non-null department copied, null as tombstone); counts match;
// no cross-domain FK exists; legacy columns stay usable.
func TestUpgrade000005ToHead_BackfillAndState(t *testing.T) {
	ctx := context.Background()
	connStr := freshDB(t)
	migrateTo(t, connStr, 5)
	conn := connect(t, connStr)
	seedLegacyData(t, conn)

	// Snapshot the 000005-era rows for cross-checks.
	var deptID int64
	require.NoError(t, conn.QueryRow(ctx,
		"SELECT id FROM departments WHERE name = '研发部'").Scan(&deptID))

	// Upgrade to head.
	migrateTo(t, connStr, 8)

	// C.1: all users backfilled active / version 1.
	var totalUsers, badStates, badVersions int64
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE lifecycle_state <> 'active'),
		       count(*) FILTER (WHERE version <> 1)
		FROM users`).Scan(&totalUsers, &badStates, &badVersions))
	require.Equal(t, int64(4), totalUsers)
	assert.Zero(t, badStates, "all users active after backfill")
	assert.Zero(t, badVersions, "all users version 1 after backfill")

	// C.2: every user has exactly one state row; dept copied or tombstone.
	// (o.department_id is qualified: users also carries the legacy column.)
	var stateRows, tombstoneRows, badVersions2, deptMismatch int64
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE o.department_id IS NULL),
		       count(*) FILTER (WHERE membership_version <> 1),
		       count(*) FILTER (WHERE o.department_id IS NOT NULL AND o.department_id <> 0)
		FROM organization_user_departments o
		JOIN users u ON u.id = o.user_id`).Scan(&stateRows, &tombstoneRows, &badVersions2, &deptMismatch))
	assert.Equal(t, totalUsers, stateRows, "one state row per user")
	assert.Equal(t, int64(1), tombstoneRows, "no_dept user gets a tombstone row")
	assert.Zero(t, badVersions2, "all membership versions 1 after backfill")

	// with_dept / admin_user copied the 研发部 department; multi_role copied 前端组.
	var withDeptMatches, noDeptIsTombstone bool
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT count(*) = 2
		FROM organization_user_departments o JOIN users u ON u.id = o.user_id
		WHERE u.username IN ('with_dept', 'admin_user') AND o.department_id = $1`, deptID).Scan(&withDeptMatches))
	assert.True(t, withDeptMatches, "department users copied 研发部")
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT o.department_id IS NULL
		FROM organization_user_departments o JOIN users u ON u.id = o.user_id
		WHERE u.username = 'no_dept'`).Scan(&noDeptIsTombstone))
	assert.True(t, noDeptIsTombstone, "no_dept user is a versioned tombstone")

	// C.3: no cross-domain FK. organization_user_departments must not
	// reference users; users must not reference it. users.department_id's FK
	// to departments (legacy, same domain) stays.
	var crossDomainFKs int
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.key_column_usage k
		JOIN information_schema.table_constraints c
		  ON c.constraint_name = k.constraint_name AND c.table_schema = k.table_schema
		JOIN information_schema.constraint_column_usage r
		  ON r.constraint_name = c.constraint_name AND r.table_schema = c.table_schema
		WHERE c.constraint_type = 'FOREIGN KEY'
		  AND ((k.table_name = 'organization_user_departments' AND r.table_name = 'users')
		    OR (k.table_name = 'users' AND r.table_name = 'organization_user_departments'))`).Scan(&crossDomainFKs))
	assert.Zero(t, crossDomainFKs, "no FK crosses the IAM/Organization boundary")

	var legacyFKs int
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.key_column_usage k
		JOIN information_schema.table_constraints c
		  ON c.constraint_name = k.constraint_name AND c.table_schema = k.table_schema
		JOIN information_schema.constraint_column_usage r
		  ON r.constraint_name = c.constraint_name AND r.table_schema = c.table_schema
		WHERE c.constraint_type = 'FOREIGN KEY'
		  AND k.table_name = 'users' AND r.table_name = 'departments'`).Scan(&legacyFKs))
	assert.Equal(t, 1, legacyFKs, "legacy users.department_id FK to departments preserved")

	// C.4: legacy columns stay writable and the bridge keeps them in sync.
	_, err := conn.Exec(ctx,
		"UPDATE users SET account = '改名用户' WHERE username = 'no_dept'")
	require.NoError(t, err)
	var account string
	require.NoError(t, conn.QueryRow(ctx,
		"SELECT account FROM users WHERE username = 'no_dept'").Scan(&account))
	assert.Equal(t, "改名用户", account)

	// Seeded roles and permissions are untouched by the upgrade.
	var roles, perms int64
	require.NoError(t, conn.QueryRow(ctx, "SELECT count(*) FROM roles").Scan(&roles))
	require.NoError(t, conn.QueryRow(ctx, "SELECT count(*) FROM permissions").Scan(&perms))
	assert.Equal(t, int64(3), roles, "built-in roles preserved")
	assert.Equal(t, int64(6), perms, "permission catalog preserved")
}

// TestBridgeTriggers covers the four trigger paths (Checkpoint C.5): INSERT
// creates state or tombstone; legacy department UPDATE upserts+increments;
// Organization state changes write back the legacy column; DELETE (with
// mode=true) physically removes the state row.
func TestBridgeTriggers(t *testing.T) {
	ctx := context.Background()
	connStr := freshDB(t)
	migrateTo(t, connStr, 5)
	conn := connect(t, connStr)
	seedLegacyData(t, conn)
	migrateTo(t, connStr, 8)

	// Path 1a: INSERT with department creates state v1.
	var deptID int64
	require.NoError(t, conn.QueryRow(ctx,
		"SELECT id FROM departments WHERE name = '研发部'").Scan(&deptID))
	_, err := conn.Exec(ctx, `
		INSERT INTO users (username, password_hash, lifecycle_state, version, department_id)
		VALUES ('bridge_a', 'hash_fixture_5', 'active', 1, $1)`, deptID)
	require.NoError(t, err)
	var stateDept int64
	var stateVersion int64
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT o.department_id, o.membership_version FROM organization_user_departments o
		JOIN users u ON u.id = o.user_id WHERE u.username = 'bridge_a'`).
		Scan(&stateDept, &stateVersion))
	assert.Equal(t, deptID, stateDept, "INSERT copies legacy department")
	assert.Equal(t, int64(1), stateVersion)

	// Path 1b: INSERT without department creates tombstone v1.
	_, err = conn.Exec(ctx, `
		INSERT INTO users (username, password_hash, lifecycle_state, version)
		VALUES ('bridge_b', 'hash_fixture_6', 'active', 1)`)
	require.NoError(t, err)
	var tombstoneNull bool
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT o.department_id IS NULL FROM organization_user_departments o
		JOIN users u ON u.id = o.user_id WHERE u.username = 'bridge_b'`).Scan(&tombstoneNull))
	assert.True(t, tombstoneNull, "INSERT without department creates tombstone")

	// Path 2: legacy department change upserts + increments membership version.
	_, err = conn.Exec(ctx, `
		UPDATE users SET department_id = $1 WHERE username = 'bridge_b'`,
		(selectDept(t, conn, "前端组")))
	require.NoError(t, err)
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT o.membership_version FROM organization_user_departments o
		JOIN users u ON u.id = o.user_id WHERE u.username = 'bridge_b'`).Scan(&stateVersion))
	assert.Equal(t, int64(2), stateVersion, "legacy change increments membership version")

	// Same value again: no increment.
	_, err = conn.Exec(ctx, `
		UPDATE users SET department_id = $1 WHERE username = 'bridge_b'`,
		(selectDept(t, conn, "前端组")))
	require.NoError(t, err)
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT o.membership_version FROM organization_user_departments o
		JOIN users u ON u.id = o.user_id WHERE u.username = 'bridge_b'`).Scan(&stateVersion))
	assert.Equal(t, int64(2), stateVersion, "same-value update does not increment")

	// Path 3: Organization state change writes back the legacy column.
	_, err = conn.Exec(ctx, `
		UPDATE organization_user_departments SET department_id = $1
		WHERE user_id = (SELECT id FROM users WHERE username = 'bridge_b')`, deptID)
	require.NoError(t, err)
	var legacyDept int64
	require.NoError(t, conn.QueryRow(ctx,
		"SELECT department_id FROM users WHERE username = 'bridge_b'").Scan(&legacyDept))
	assert.Equal(t, deptID, legacyDept, "Organization state change writes back legacy column")

	// Path 4: mode=true DELETE removes state (default mode at install).
	_, err = conn.Exec(ctx, "DELETE FROM users WHERE username = 'bridge_a'")
	require.NoError(t, err)
	var orphan int
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT count(*) FROM organization_user_departments o
		WHERE NOT EXISTS (SELECT 1 FROM users u WHERE u.id = o.user_id)`).Scan(&orphan))
	assert.Zero(t, orphan, "mode=true delete synchronously removes state")
}

// TestBridgeModeAuditAndRollback covers the controlled mode switch, its audit
// trail, and the full rollback to 000005 (Checkpoint C.6): the mode is
// switched off after final sync, switched back before rollback, the change
// audit row survives the switch sequence, and rolling back removes only the
// feature objects — legacy schema, seeds and data all survive.
func TestBridgeModeAuditAndRollback(t *testing.T) {
	ctx := context.Background()
	connStr := freshDB(t)
	migrateTo(t, connStr, 5)
	conn := connect(t, connStr)
	seedLegacyData(t, conn)
	m := migrateTo(t, connStr, 8)

	// Final sync: BFF delete-event path takes over → mode off.
	_, err := conn.Exec(ctx, `
		SELECT public.set_legacy_delete_sync_mode(
			1::bigint, false, 'ops_test', 'test_authorization', NULL,
			'bff_delete_event_accepted', 'req_final_sync', 'corr_final_sync')`)
	require.NoError(t, err)

	// DELETE while mode=false leaves the state row (dormant evidence).
	_, err = conn.Exec(ctx, "DELETE FROM users WHERE username = 'no_dept'")
	require.NoError(t, err)
	var dormantRows int
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT count(*) FROM organization_user_departments o
		WHERE NOT EXISTS (SELECT 1 FROM users u WHERE u.id = o.user_id)`).Scan(&dormantRows))
	assert.Equal(t, 1, dormantRows, "mode=false delete leaves dormant state evidence")

	// Rollback precondition: switch the mode back on.
	_, err = conn.Exec(ctx, `
		SELECT public.set_legacy_delete_sync_mode(
			2::bigint, true, 'ops_test', 'test_authorization', NULL,
			'rollback_switch_back', 'req_rollback', 'corr_rollback')`)
	require.NoError(t, err)

	// Audit trail: two transitions recorded with immutable values.
	var auditCount int
	var lastReason string
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT count(*), (SELECT reason_code FROM compatibility_bridge_mode_changes ORDER BY change_id DESC LIMIT 1)
		FROM compatibility_bridge_mode_changes`).Scan(&auditCount, &lastReason))
	assert.Equal(t, 2, auditCount, "mode changes are immutably audited")
	assert.Equal(t, "rollback_switch_back", lastReason)

	// Roll back 000008 → 000005.
	require.NoError(t, m.Migrate(5), "roll back to 000005")

	// Feature objects are gone.
	for _, col := range []string{"lifecycle_state", "version"} {
		var exists bool
		require.NoError(t, conn.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM information_schema.columns
			WHERE table_name = 'users' AND column_name = $1)`, col).Scan(&exists))
		assert.False(t, exists, "users.%s removed on rollback", col)
	}
	for _, table := range []string{"organization_user_departments", "compatibility_bridge_mode",
		"compatibility_bridge_mode_changes", "compatibility_rollout_gates",
		"iam_command_receipts", "iam_outbox_events", "iam_outbox_requeues",
		"organization_command_receipts", "organization_inbox_messages",
		"admin_workflows", "admin_workflow_subjects", "admin_workflow_recovery_actions"} {
		var exists bool
		require.NoError(t, conn.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = $1)`, table).Scan(&exists))
		assert.False(t, exists, "table %s removed on rollback", table)
	}
	var triggerCount int
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.triggers
		WHERE event_object_table IN ('users', 'organization_user_departments')`).Scan(&triggerCount))
	assert.Zero(t, triggerCount, "bridge triggers removed on rollback")
	// Roles are cluster-global: scaffold_test (migrated to head by TestMain)
	// still owns tables as these roles, so 000008's down migration cannot drop
	// them here and retains them with a NOTICE. What the rollback contract
	// guarantees per database: the role owns nothing in this database and has
	// no schema grant left (both true whether the role was dropped or kept).
	for _, role := range []string{"iam_owner", "organization_owner", "platform_owner",
		"compatibility_bridge_owner", "app_runtime", "platform_operations"} {
		var owned int64
		require.NoError(t, conn.QueryRow(ctx, `
			SELECT count(*)
			FROM pg_class c JOIN pg_roles r ON r.oid = c.relowner
			WHERE r.rolname = $1 AND c.relnamespace = 'public'::regnamespace`, role).Scan(&owned))
		assert.Zero(t, owned, "role %s owns nothing in this database after rollback", role)
		var schemaGrant bool
		require.NoError(t, conn.QueryRow(ctx, `
			SELECT COALESCE(has_schema_privilege($1, 'public', 'USAGE'), false)`, role).Scan(&schemaGrant))
		assert.False(t, schemaGrant, "role %s has no schema USAGE after rollback", role)
	}

	// 000004/000005 objects survive, with their seeds and data.
	for _, table := range []string{"departments", "roles", "user_roles", "permissions", "role_permissions"} {
		var exists bool
		require.NoError(t, conn.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = $1)`, table).Scan(&exists))
		assert.True(t, exists, "legacy table %s survives rollback", table)
	}
	var users, roles, perms, depts int64
	require.NoError(t, conn.QueryRow(ctx, "SELECT count(*) FROM users").Scan(&users))
	require.NoError(t, conn.QueryRow(ctx, "SELECT count(*) FROM roles").Scan(&roles))
	require.NoError(t, conn.QueryRow(ctx, "SELECT count(*) FROM permissions").Scan(&perms))
	require.NoError(t, conn.QueryRow(ctx, "SELECT count(*) FROM departments").Scan(&depts))
	// 3, not 4: no_dept was deleted earlier as the mode=false evidence check.
	assert.Equal(t, int64(3), users, "users survive rollback")
	assert.Equal(t, int64(3), roles, "built-in roles survive rollback")
	assert.Equal(t, int64(6), perms, "permission catalog survives rollback")
	assert.Equal(t, int64(8), depts, "department seeds survive rollback")

	// Post-rollback the legacy path works again (no lifecycle columns).
	var legacyID int64
	require.NoError(t, conn.QueryRow(ctx, `
		INSERT INTO users (username, password_hash)
		VALUES ('post_rollback', 'hash_fixture_7') RETURNING id`).Scan(&legacyID))
	assert.Greater(t, legacyID, int64(0), "legacy insert works after rollback")
}

// selectDept returns a department id by name.
func selectDept(t *testing.T, conn *pgx.Conn, name string) int64 {
	t.Helper()
	var id int64
	require.NoError(t, conn.QueryRow(context.Background(),
		"SELECT id FROM departments WHERE name = $1", name).Scan(&id))
	return id
}
