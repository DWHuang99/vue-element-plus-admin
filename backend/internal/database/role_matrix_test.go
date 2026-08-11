// Role matrix catalog + runtime boundary tests (quickstart Checkpoint B/C,
// T016; data-model.md "Compatibility bridge control and PostgreSQL role
// matrix").
//
// Every domain table/sequence is owned by its NOLOGIN owner role; the
// single runtime login (app_runtime) holds schema USAGE + table DML only —
// no GRANT OPTION, no owner membership, no schema CREATE, no DDL/trigger/
// owner/function/grant capability — and cannot read Platform control tables.
// platform_operations is the only role that may execute the controlled
// mode-change function (positive control included below).
package database

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ownerByTable is the Ownership matrix applied to public tables + sequences.
var ownerByTable = map[string]string{
	"users": "iam_owner", "sessions": "iam_owner", "roles": "iam_owner",
	"user_roles": "iam_owner", "permissions": "iam_owner", "role_permissions": "iam_owner",
	"iam_command_receipts": "iam_owner", "iam_outbox_events": "iam_owner", "iam_outbox_requeues": "iam_owner",
	"users_id_seq": "iam_owner", "sessions_id_seq": "iam_owner",
	"roles_id_seq": "iam_owner", "permissions_id_seq": "iam_owner",

	"departments": "organization_owner", "organization_user_departments": "organization_owner",
	"organization_command_receipts": "organization_owner", "organization_inbox_messages": "organization_owner",
	"departments_id_seq": "organization_owner",

	"admin_workflows": "platform_owner", "admin_workflow_subjects": "platform_owner",
	"admin_workflow_recovery_actions": "platform_owner",
	"compatibility_bridge_mode":       "platform_owner", "compatibility_bridge_mode_changes": "platform_owner",
	"compatibility_rollout_gates":                     "platform_owner",
	"compatibility_bridge_mode_changes_change_id_seq": "platform_owner",
	"compatibility_rollout_gates_gate_id_seq":         "platform_owner",
	// schema_migrations is migrator-created; 000008 hands it over when present.
	"schema_migrations": "platform_owner",
}

// ownerByFunction is the Ownership matrix applied to public functions
// present at migration 8. The 000012 rollout-gate functions (T077) are
// asserted in section 10 after migrating to head.
var ownerByFunction = map[string]string{
	"users_insert_bridge":              "compatibility_bridge_owner",
	"users_department_bridge":          "compatibility_bridge_owner",
	"organization_state_legacy_bridge": "compatibility_bridge_owner",
	"users_delete_bridge":              "compatibility_bridge_owner",
	"set_legacy_delete_sync_mode":      "platform_owner",
}

// runtimeDMLTables are the tables app_runtime was granted DML on by 000008.
var runtimeDMLTables = []string{
	"users", "sessions", "roles", "user_roles", "permissions", "role_permissions",
	"iam_command_receipts", "iam_outbox_events", "iam_outbox_requeues",
	"departments", "organization_user_departments",
	"organization_command_receipts", "organization_inbox_messages",
	"admin_workflows", "admin_workflow_subjects", "admin_workflow_recovery_actions",
}

func TestRoleMatrixCatalogAndRuntimeBoundaries(t *testing.T) {
	ctx := context.Background()
	connStr := freshDB(t)
	migrateTo(t, connStr, 8)
	conn := connect(t, connStr)

	// ---- 1. Ownership matrix: every object owned by its NOLOGIN owner. ----
	var mismatched []string
	for _, rel := range pgClassNames(t, conn, ctx, "public", []string{"r", "S"}) {
		expected, ok := ownerByTable[rel.name]
		if !ok {
			t.Fatalf("unexpected public relation %q — extend ownerByTable", rel.name)
		}
		if rel.owner != expected {
			mismatched = append(mismatched, fmt.Sprintf("%s: %s (want %s)", rel.name, rel.owner, expected))
		}
	}
	assert.Empty(t, mismatched, "table/sequence ownership matrix")
	for fn, owner := range ownerByFunction {
		var got string
		require.NoError(t, conn.QueryRow(ctx, `
			SELECT r.rolname FROM pg_proc p JOIN pg_roles r ON r.oid = p.proowner
			WHERE p.pronamespace = 'public'::regnamespace AND p.proname = $1`, fn).Scan(&got))
		assert.Equal(t, owner, got, "function %s ownership", fn)
	}

	// ---- 2. LOGIN flags: owners NOLOGIN, runtime/ops roles can log in. ----
	for _, owner := range []string{"iam_owner", "organization_owner", "platform_owner", "compatibility_bridge_owner"} {
		var canLogin bool
		require.NoError(t, conn.QueryRow(ctx,
			"SELECT rolcanlogin FROM pg_roles WHERE rolname = $1", owner).Scan(&canLogin))
		assert.False(t, canLogin, "%s must be NOLOGIN", owner)
	}
	for _, login := range []string{"app_runtime", "platform_operations"} {
		var canLogin bool
		require.NoError(t, conn.QueryRow(ctx,
			"SELECT rolcanlogin FROM pg_roles WHERE rolname = $1", login).Scan(&canLogin))
		assert.True(t, canLogin, "%s must be a login role", login)
	}

	// ---- 3. app_runtime is not a member of any owner role. ----
	var memberships int
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT count(*) FROM pg_auth_members WHERE member = 'app_runtime'::regrole`).Scan(&memberships))
	assert.Zero(t, memberships, "app_runtime has no role memberships")

	// ---- 4. No schema CREATE: not from PUBLIC, not from runtime roles. ----
	// aclexplode with grantee = 0 is the PUBLIC pseudo-role (has_*_privilege
	// does not accept the literal 'PUBLIC' for the role argument).
	var publicCreateGrants int
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT count(*)
		FROM pg_namespace n
		CROSS JOIN LATERAL aclexplode(COALESCE(n.nspacl, '{}')) a
		WHERE n.nspname = 'public' AND a.grantee = 0 AND a.privilege_type = 'CREATE'`).Scan(&publicCreateGrants))
	assert.Zero(t, publicCreateGrants, "PUBLIC has no CREATE on schema public")
	for _, role := range []string{"app_runtime", "platform_operations"} {
		var createPriv bool
		require.NoError(t, conn.QueryRow(ctx, `
			SELECT COALESCE(has_schema_privilege($1, 'public', 'CREATE'), false)`, role).Scan(&createPriv))
		assert.False(t, createPriv, "%s has no CREATE on schema public", role)
	}

	// ---- 5. Grants: DML without GRANT OPTION; control tables unreadable. ----
	for _, table := range runtimeDMLTables {
		var dml, opt bool
		require.NoError(t, conn.QueryRow(ctx, `
			SELECT COALESCE(has_table_privilege('app_runtime', $1, 'SELECT'), false)`, table).Scan(&dml))
		assert.True(t, dml, "app_runtime SELECT on %s", table)
		require.NoError(t, conn.QueryRow(ctx, `
			SELECT COALESCE(has_table_privilege('app_runtime', $1, 'SELECT WITH GRANT OPTION'), false)`, table).Scan(&opt))
		assert.False(t, opt, "app_runtime must not hold GRANT OPTION on %s", table)
	}
	for _, table := range []string{"compatibility_bridge_mode", "compatibility_bridge_mode_changes", "compatibility_rollout_gates"} {
		var canRead bool
		require.NoError(t, conn.QueryRow(ctx,
			"SELECT COALESCE(has_table_privilege('app_runtime', $1, 'SELECT'), false)", table).Scan(&canRead))
		assert.False(t, canRead, "app_runtime must not read Platform control table %s", table)
	}
	// Sequences: runtime needs USAGE for every id-generating table.
	for _, seq := range []string{"users_id_seq", "sessions_id_seq", "roles_id_seq",
		"permissions_id_seq", "departments_id_seq"} {
		var seqUsage bool
		require.NoError(t, conn.QueryRow(ctx,
			"SELECT COALESCE(has_sequence_privilege('app_runtime', $1, 'USAGE'), false)", seq).Scan(&seqUsage))
		assert.True(t, seqUsage, "app_runtime sequence USAGE on %s", seq)
	}

	// ---- 6. Runtime behavior: every ownership-level operation is denied. ----
	_, err := conn.Exec(ctx, "SET ROLE app_runtime")
	require.NoError(t, err)
	defer func() { _, _ = conn.Exec(context.Background(), "RESET ROLE") }()

	// Control: granted DML works, reads and writes.
	var n int64
	require.NoError(t, conn.QueryRow(ctx, "SELECT count(*) FROM users").Scan(&n))
	assert.Equal(t, int64(0), n, "app_runtime can read its granted table")
	var newUserID int64
	require.NoError(t, conn.QueryRow(ctx, `
		INSERT INTO users (username, password_hash, lifecycle_state, version)
		VALUES ('runtime_user', 'hash_fixture_rt', 'active', 1) RETURNING id`).Scan(&newUserID))
	_, err = conn.Exec(ctx, `
		INSERT INTO sessions (token_hash, user_id, expires_at)
		VALUES ('runtime_session_hash', $1, now() + interval '1 hour')`, newUserID)
	require.NoError(t, err, "app_runtime can write sessions (sequence USAGE path)")

	// Ungranted Platform control table: denied.
	_, err = conn.Exec(ctx, "SELECT legacy_delete_sync_enabled FROM compatibility_bridge_mode")
	require.Error(t, err, "app_runtime must not query Platform control tables")

	// Ownership-level operations: all denied.
	deniedOps := []struct {
		name string
		sql  string
	}{
		{"disable trigger", "ALTER TABLE users DISABLE TRIGGER ALL"},
		{"drop trigger", "DROP TRIGGER trg_users_insert_bridge ON users"},
		{"replace trigger", "CREATE TRIGGER trg_runtime_made AFTER INSERT ON users FOR EACH ROW EXECUTE FUNCTION public.users_insert_bridge()"},
		{"alter table owner", "ALTER TABLE users OWNER TO app_runtime"},
		{"alter function owner", "ALTER FUNCTION public.users_insert_bridge() OWNER TO app_runtime"},
		{"replace function", "CREATE OR REPLACE FUNCTION public.users_insert_bridge() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$"},
		{"add column (DDL)", "ALTER TABLE users ADD COLUMN extra TEXT"},
		{"drop table", "DROP TABLE departments"},
		{"create function", "CREATE FUNCTION public.runtime_fn() RETURNS void LANGUAGE sql AS 'SELECT 1'"},
		{"create schema", "CREATE SCHEMA runtime_schema"},
		{"execute mode-change", "SELECT public.set_legacy_delete_sync_mode(1::bigint, false, 'runtime', 'x', NULL, 'x', 'r', 'c')"},
		{"self-grant", "GRANT SELECT ON compatibility_bridge_mode TO app_runtime"},
		{"alter sequence", "ALTER SEQUENCE users_id_seq OWNER TO app_runtime"},
	}
	for _, op := range deniedOps {
		_, err := conn.Exec(ctx, op.sql)
		require.Error(t, err, "app_runtime must be denied: %s", op.name)
	}
	// Grant-option check: PostgreSQL softens a missing grant option into a
	// "no privileges were granted" warning + no-op rather than an error, so
	// assert the outcome that matters — the privilege never materializes.
	_, _ = conn.Exec(ctx, "GRANT SELECT ON users TO platform_operations")
	var opsSelect bool
	require.NoError(t, conn.QueryRow(ctx,
		"SELECT COALESCE(has_table_privilege('platform_operations', 'users', 'SELECT'), false)").Scan(&opsSelect))
	assert.False(t, opsSelect, "app_runtime grant without grant option must not materialize")

	// ---- 7. platform_operations: positive control for mode-change. ----
	_, err = conn.Exec(ctx, "RESET ROLE")
	require.NoError(t, err)
	_, err = conn.Exec(ctx, "SET ROLE platform_operations")
	require.NoError(t, err)
	var newVersion int64
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT public.set_legacy_delete_sync_mode(
			1::bigint, false, 'ops_test', 'test_authorization', NULL,
			'role_matrix_test', 'req_rm', 'corr_rm')`).Scan(&newVersion))
	assert.Equal(t, int64(2), newVersion, "platform_operations can run the controlled CAS")
	_, err = conn.Exec(ctx, "UPDATE compatibility_bridge_mode SET legacy_delete_sync_enabled = true WHERE id = 1")
	require.Error(t, err, "platform_operations must not write the mode directly")
	_, err = conn.Exec(ctx, "ALTER TABLE users DISABLE TRIGGER ALL")
	require.Error(t, err, "platform_operations must not disable triggers either")
	_, err = conn.Exec(ctx, "RESET ROLE")
	require.NoError(t, err)

	// The CAS ran atomically: one audit row, version bumped.
	var auditRows int64
	require.NoError(t, conn.QueryRow(ctx,
		"SELECT count(*) FROM compatibility_bridge_mode_changes").Scan(&auditRows))
	assert.Equal(t, int64(1), auditRows, "mode change is immutably audited")
	var modeEnabled bool
	require.NoError(t, conn.QueryRow(ctx,
		"SELECT legacy_delete_sync_enabled FROM compatibility_bridge_mode WHERE id = 1").Scan(&modeEnabled))
	assert.False(t, modeEnabled, "mode changed by the controlled function")

	// ---- 8. Bridge triggers installed and enabled. ----
	var disabledTriggers int
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT count(*) FROM pg_trigger
		WHERE NOT tgisinternal AND tgname IN (
			'trg_users_insert_bridge', 'trg_users_department_bridge',
			'trg_organization_state_insert_bridge', 'trg_organization_state_update_bridge',
			'trg_users_delete_bridge') AND tgenabled <> 'O'`).Scan(&disabledTriggers))
	assert.Zero(t, disabledTriggers, "all bridge triggers installed and enabled")

	// ---- 9. Mode-change function executable by nobody but platform_operations. ----
	var publicExecute int
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.routine_privileges
		WHERE routine_name = 'set_legacy_delete_sync_mode' AND grantee = 'PUBLIC'`).Scan(&publicExecute))
	assert.Zero(t, publicExecute, "mode-change function revoked from PUBLIC")
	var opsExecute bool
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT COALESCE(has_function_privilege('platform_operations',
			'public.set_legacy_delete_sync_mode(bigint, boolean, text, text, text, text, text, text)', 'EXECUTE'), false)`).Scan(&opsExecute))
	assert.True(t, opsExecute, "platform_operations can execute the mode-change function")
	var runtimeExecute bool
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT COALESCE(has_function_privilege('app_runtime',
			'public.set_legacy_delete_sync_mode(bigint, boolean, text, text, text, text, text, text)', 'EXECUTE'), false)`).Scan(&runtimeExecute))
	assert.False(t, runtimeExecute, "app_runtime cannot execute the mode-change function")

	// ---- 10. 000012 rollout-gate functions (T077): owned by platform_owner,
	// executable by exactly one role each — the writer loop (app_runtime) may
	// record samples but never approve; route-disable approval is a
	// short-lived authenticated Platform operation (platform_operations).
	migrateTo(t, connStr, 12)
	for fn, owner := range map[string]string{
		"record_rollout_gate_sample": "platform_owner",
		"approve_route_disable":      "platform_owner",
	} {
		var got string
		require.NoError(t, conn.QueryRow(ctx, `
			SELECT r.rolname FROM pg_proc p JOIN pg_roles r ON r.oid = p.proowner
			WHERE p.pronamespace = 'public'::regnamespace AND p.proname = $1`, fn).Scan(&got))
		assert.Equal(t, owner, got, "function %s ownership", fn)
	}
	for _, fn := range []string{"record_rollout_gate_sample", "approve_route_disable"} {
		var publicExecute int
		require.NoError(t, conn.QueryRow(ctx, `
			SELECT count(*) FROM information_schema.routine_privileges
			WHERE routine_name = $1 AND grantee = 'PUBLIC'`, fn).Scan(&publicExecute))
		assert.Zero(t, publicExecute, "%s revoked from PUBLIC", fn)
	}
	var runtimeSamples, runtimeApproves, opsSamples, opsApproves bool
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT COALESCE(has_function_privilege('app_runtime',
			'public.record_rollout_gate_sample(text, text, bigint, boolean, bigint, bigint, text, bigint, timestamptz, text, text, text, timestamptz, interval)', 'EXECUTE'), false)`).Scan(&runtimeSamples))
	assert.True(t, runtimeSamples, "app_runtime can record rollout-gate samples (the writer loop)")
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT COALESCE(has_function_privilege('app_runtime',
			'public.approve_route_disable(text, text, text, timestamptz)', 'EXECUTE'), false)`).Scan(&runtimeApproves))
	assert.False(t, runtimeApproves, "app_runtime cannot approve route disable")
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT COALESCE(has_function_privilege('platform_operations',
			'public.approve_route_disable(text, text, text, timestamptz)', 'EXECUTE'), false)`).Scan(&opsApproves))
	assert.True(t, opsApproves, "platform_operations can approve route disable")
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT COALESCE(has_function_privilege('platform_operations',
			'public.record_rollout_gate_sample(text, text, bigint, boolean, bigint, bigint, text, bigint, timestamptz, text, text, text, timestamptz, interval)', 'EXECUTE'), false)`).Scan(&opsSamples))
	assert.False(t, opsSamples, "platform_operations cannot record samples — sample recording belongs to the deployed app")

	// ---- 11. 000013 evidence-cleanup audit (T078): the audit table and both
	// SECURITY DEFINER functions are platform_owner-owned; the functions are
	// executable by app_runtime ONLY (the Platform coordinator is part of the
	// deployed app). platform_operations and PUBLIC must not execute them, and
	// no runtime login holds table grants on the audit — it is append-only and
	// read only by the owner role.
	migrateTo(t, connStr, 13)
	for fn, owner := range map[string]string{
		"record_cleanup_audit":   "platform_owner",
		"finalize_cleanup_audit": "platform_owner",
	} {
		var got string
		require.NoError(t, conn.QueryRow(ctx, `
			SELECT r.rolname FROM pg_proc p JOIN pg_roles r ON r.oid = p.proowner
			WHERE p.pronamespace = 'public'::regnamespace AND p.proname = $1`, fn).Scan(&got))
		assert.Equal(t, owner, got, "function %s ownership", fn)
	}
	var auditTableOwner string
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT r.rolname FROM pg_class c JOIN pg_roles r ON r.oid = c.relowner
		WHERE c.relname = 'platform_evidence_cleanup_audit'`).Scan(&auditTableOwner))
	assert.Equal(t, "platform_owner", auditTableOwner, "audit table ownership")
	for _, fn := range []string{"record_cleanup_audit", "finalize_cleanup_audit"} {
		var publicExecute int
		require.NoError(t, conn.QueryRow(ctx, `
			SELECT count(*) FROM information_schema.routine_privileges
			WHERE routine_name = $1 AND grantee = 'PUBLIC'`, fn).Scan(&publicExecute))
		assert.Zero(t, publicExecute, "%s revoked from PUBLIC", fn)
	}
	var runtimeRecord, runtimeFinalize, opsRecord, opsFinalize bool
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT COALESCE(has_function_privilege('app_runtime',
			'public.record_cleanup_audit(uuid, text, text, text, text, text, timestamptz, timestamptz, jsonb)', 'EXECUTE'), false)`).Scan(&runtimeRecord))
	assert.True(t, runtimeRecord, "app_runtime can record cleanup audits (the coordinator)")
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT COALESCE(has_function_privilege('app_runtime',
			'public.finalize_cleanup_audit(uuid, text, jsonb, text)', 'EXECUTE'), false)`).Scan(&runtimeFinalize))
	assert.True(t, runtimeFinalize, "app_runtime can finalize cleanup audits")
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT COALESCE(has_function_privilege('platform_operations',
			'public.record_cleanup_audit(uuid, text, text, text, text, text, timestamptz, timestamptz, jsonb)', 'EXECUTE'), false)`).Scan(&opsRecord))
	assert.False(t, opsRecord, "platform_operations cannot record cleanup audits")
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT COALESCE(has_function_privilege('platform_operations',
			'public.finalize_cleanup_audit(uuid, text, jsonb, text)', 'EXECUTE'), false)`).Scan(&opsFinalize))
	assert.False(t, opsFinalize, "platform_operations cannot finalize cleanup audits")
	var runtimeAuditRead bool
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT COALESCE(has_table_privilege('app_runtime', 'platform_evidence_cleanup_audit', 'SELECT'), false)`).Scan(&runtimeAuditRead))
	assert.False(t, runtimeAuditRead, "app_runtime must not read the audit table directly")
}

type pgClassRow struct {
	name  string
	owner string
}

// pgClassNames lists relations in a schema by relkind.
func pgClassNames(t *testing.T, conn *pgx.Conn, ctx context.Context, schema string, kinds []string) []pgClassRow {
	t.Helper()
	var out []pgClassRow
	for _, kind := range kinds {
		rows, err := conn.Query(ctx, `
			SELECT c.relname, r.rolname
			FROM pg_class c
			JOIN pg_roles r ON r.oid = c.relowner
			WHERE c.relnamespace = $1::regnamespace AND c.relkind = $2`, schema, kind)
		require.NoError(t, err)
		for rows.Next() {
			var row pgClassRow
			require.NoError(t, rows.Scan(&row.name, &row.owner))
			out = append(out, row)
		}
		rows.Close()
	}
	return out
}
