// IAM application contract tests (contracts/iam-application.md; task T018).
//
// Written before the service/adapter implementation: this suite pins the
// application behaviors on a real PostgreSQL (testcontainers) through the
// public constructors iam.NewService / postgres.NewStore, so T019-T021
// (service) and T022 (adapter) are driven to green by this suite.
//
// The suite lives in the external test package iam_test because it imports
// the adapter (internal/iam/postgres), which itself imports iam — an in-
// package test file would create an import cycle.
//
// Every test runs on its own scratch database (fresh + migrated to head), so
// assertions never share state and the shared cluster (cluster-global roles
// created by 000008) is only ever touched by the migrator, not by tests.
package iam_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres" // registers the postgres driver for migrate
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/hdw/vue-element-plus-admin/backend/db/migrations"
	"github.com/hdw/vue-element-plus-admin/backend/internal/consistency"
	"github.com/hdw/vue-element-plus-admin/backend/internal/iam"
	"github.com/hdw/vue-element-plus-admin/backend/internal/iam/postgres"
)

// TestMain boots a shared Postgres container once; each test derives its own
// scratch database from it (freshDB) so assertions never share state.
func TestMain(m *testing.M) {
	ctx := context.Background()

	pg, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("iam_contract"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		testcontainers.WithWaitStrategyAndDeadline(
			60*time.Second,
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to start postgres container: %v\n", err)
		os.Exit(1)
	}
	connStr, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to get connection string: %v\n", err)
		os.Exit(1)
	}
	testConnStr = connStr

	code := m.Run()

	_ = pg.Terminate(ctx)
	os.Exit(code)
}

var testConnStr string

// freshDB creates a scratch database on the shared container and returns a
// connection string pointing at it. The database is dropped on cleanup.
func freshDB(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	dbname := fmt.Sprintf("iam_contract_%d", time.Now().UnixNano())
	conn, err := pgx.Connect(ctx, testConnStr)
	require.NoError(t, err)
	// DDL cannot be parameterized; the name is generated internally.
	_, err = conn.Exec(ctx, "CREATE DATABASE "+dbname)
	require.NoError(t, err)
	_ = conn.Close(ctx)
	t.Cleanup(func() {
		conn, err := pgx.Connect(context.Background(), testConnStr)
		if err == nil {
			_, _ = conn.Exec(context.Background(), "DROP DATABASE IF EXISTS "+dbname+" WITH (FORCE)")
			_ = conn.Close(context.Background())
		}
	})
	// postgres://test:test@host:port/iam_contract?sslmode=disable
	idx := strings.Index(testConnStr, "/iam_contract?")
	if idx == -1 {
		t.Fatal("unexpected connection string shape")
	}
	return testConnStr[:idx] + "/" + dbname + testConnStr[idx+len("/iam_contract"):]
}

// migrateToHead runs the full migration chain on a scratch database.
func migrateToHead(t *testing.T, connStr string) {
	t.Helper()
	source, err := iofs.New(migrations.FS, ".")
	require.NoError(t, err)
	m, err := migrate.NewWithSourceInstance("iofs", source, connStr)
	require.NoError(t, err)
	t.Cleanup(func() { m.Close() })
	require.NoError(t, m.Up(), "migrate to head")
}

// harness wires the service under test to a fresh migrated database, plus
// direct-sql access for seeding and cross-checking DB truth.
type harness struct {
	pool *pgxpool.Pool
	db   *pgx.Conn
	svc  *iam.Service
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	connStr := freshDB(t)
	migrateToHead(t, connStr)

	pool, err := pgxpool.New(ctx, connStr)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	db, err := pgx.Connect(ctx, connStr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close(context.Background()) })

	return &harness{pool: pool, db: db, svc: iam.NewService(postgres.NewStore(pool), slog.New(slog.NewTextHandler(io.Discard, nil)))}
}

// opCtx returns a minimal workflow operation context for auth operations.
func opCtx() iam.OperationContext {
	return iam.OperationContext{
		OperationID:   "00000000-0000-0000-0000-000000000001",
		CorrelationID: "contract-test-correlation",
	}
}

// --- harness helpers --------------------------------------------------------

func (h *harness) register(t *testing.T, username, password string) iam.AuthSession {
	t.Helper()
	session, err := h.svc.Register(context.Background(), username, password, opCtx())
	require.NoError(t, err)
	require.NotEmpty(t, session.Token)
	return session
}

func (h *harness) countRows(t *testing.T, table string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, h.db.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n))
	return n
}

func (h *harness) userIDByUsername(t *testing.T, username string) int64 {
	t.Helper()
	var id int64
	require.NoError(t, h.db.QueryRow(context.Background(),
		"SELECT id FROM users WHERE username = $1", username).Scan(&id))
	return id
}

func (h *harness) setLifecycle(t *testing.T, userID int64, state string) {
	t.Helper()
	_, err := h.db.Exec(context.Background(),
		"UPDATE users SET lifecycle_state = $1 WHERE id = $2", state, userID)
	require.NoError(t, err)
}

func (h *harness) roleIDByCode(t *testing.T, code string) int64 {
	t.Helper()
	var id int64
	require.NoError(t, h.db.QueryRow(context.Background(),
		"SELECT id FROM roles WHERE code = $1", code).Scan(&id))
	return id
}

func (h *harness) assignRole(t *testing.T, userID, roleID int64) {
	t.Helper()
	_, err := h.db.Exec(context.Background(),
		"INSERT INTO user_roles (user_id, role_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", userID, roleID)
	require.NoError(t, err)
}

// rolePermissionCodes returns the DB truth for a role's grants, sorted.
func (h *harness) rolePermissionCodes(t *testing.T, roleCode string) []string {
	t.Helper()
	rows, err := h.db.Query(context.Background(), `
		SELECT DISTINCT p.code
		FROM role_permissions rp
		JOIN permissions p ON p.id = rp.permission_id
		JOIN roles r ON r.id = rp.role_id
		WHERE r.code = $1
		ORDER BY p.code`, roleCode)
	require.NoError(t, err)
	defer rows.Close()
	codes := []string{} // non-nil, mirroring the service's normalization
	for rows.Next() {
		var c string
		require.NoError(t, rows.Scan(&c))
		codes = append(codes, c)
	}
	return codes
}

// sortedUnion merges sorted lists keeping each element once (both inputs
// sorted, as the catalog query orders them).
func sortedUnion(a, b []string) []string {
	out := make([]string, 0, len(a)+len(b))
	for _, s := range append(append([]string{}, a...), b...) {
		if len(out) == 0 || out[len(out)-1] != s {
			out = append(out, s)
		}
	}
	return out
}

func assertSortedUnique(t *testing.T, codes []string) {
	t.Helper()
	for i := 1; i < len(codes); i++ {
		if codes[i-1] >= codes[i] {
			t.Fatalf("permissions not strictly sorted/unique: %v", codes)
		}
	}
}

// --- contract tests ---------------------------------------------------------

// Register creates an active user at version 1, assigns the default 'user'
// role and issues a session in one transaction; the database stores only the
// token hash and the issued token authenticates (register implies login).
func TestRegister_CreatesActiveUserDefaultRoleAndSession(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	session, err := h.svc.Register(ctx, "alice", "Correct-Horse-1", opCtx())
	require.NoError(t, err)
	assert.Equal(t, "Bearer", session.TokenType)
	assert.NotEmpty(t, session.Token)
	assert.Greater(t, session.ExpiresIn, time.Duration(0))
	assert.Greater(t, session.User.UserID, int64(0))
	assert.Equal(t, "alice", session.User.Username)
	assert.NotEmpty(t, session.User.SessionID, "internal stable session reference")

	var state string
	var version int64
	require.NoError(t, h.db.QueryRow(ctx,
		"SELECT lifecycle_state, version FROM users WHERE username = 'alice'").Scan(&state, &version))
	assert.Equal(t, "active", state)
	assert.Equal(t, int64(1), version)

	userID := h.userIDByUsername(t, "alice")
	var roleCode string
	require.NoError(t, h.db.QueryRow(ctx, `
		SELECT r.code FROM user_roles ur JOIN roles r ON r.id = ur.role_id
		WHERE ur.user_id = $1`, userID).Scan(&roleCode))
	assert.Equal(t, "user", roleCode)
	assert.Equal(t, int64(1), h.countRows(t, "sessions"))

	// Only the token hash is stored — never the raw token.
	var storedHash string
	require.NoError(t, h.db.QueryRow(ctx,
		"SELECT token_hash FROM sessions WHERE user_id = $1", userID).Scan(&storedHash))
	assert.Equal(t, iam.HashToken(session.Token), storedHash)
	assert.NotEqual(t, session.Token, storedHash)

	// Round trip: the issued token authenticates and carries the same
	// stable session reference.
	principal, err := h.svc.Authenticate(ctx, session.Token, opCtx())
	require.NoError(t, err)
	assert.Equal(t, userID, principal.UserID)
	assert.Equal(t, "alice", principal.Username)
	assert.Equal(t, session.User.SessionID, principal.SessionID)
}

// A duplicate username is rejected with UsernameTaken and leaves the
// first registration untouched (no duplicate user/role/session).
func TestRegister_DuplicateUsernameNoPartialState(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	h.register(t, "bob", "Correct-Horse-1")
	_, err := h.svc.Register(ctx, "bob", "Other-Pass-1", opCtx())
	require.ErrorIs(t, err, iam.ErrUsernameTaken)

	userID := h.userIDByUsername(t, "bob")
	var users, sessions, roleLinks int64
	require.NoError(t, h.db.QueryRow(ctx,
		"SELECT count(*) FROM users WHERE username = 'bob'").Scan(&users))
	require.NoError(t, h.db.QueryRow(ctx,
		"SELECT count(*) FROM sessions WHERE user_id = $1", userID).Scan(&sessions))
	require.NoError(t, h.db.QueryRow(ctx,
		"SELECT count(*) FROM user_roles WHERE user_id = $1", userID).Scan(&roleLinks))
	assert.Equal(t, int64(1), users, "no duplicate user")
	assert.Equal(t, int64(1), sessions, "no duplicate session")
	assert.Equal(t, int64(1), roleLinks, "no duplicate role assignment")
}

// Registration is a three-write transaction (user, default role, session).
// When the default 'user' role cannot be resolved mid-transaction, NOTHING
// may persist — no user without its default role and session.
func TestRegister_Atomicity_NoPartialUserWithoutDefaultRole(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// Remove the default role (and its grants) so the register transaction
	// fails after the user insert.
	_, err := h.db.Exec(ctx, `
		DELETE FROM role_permissions WHERE role_id = (SELECT id FROM roles WHERE code = 'user');
		DELETE FROM roles WHERE code = 'user'`)
	require.NoError(t, err)

	_, err = h.svc.Register(ctx, "carol", "Correct-Horse-1", opCtx())
	require.Error(t, err, "register must fail when the default role is missing")
	require.False(t, errors.Is(err, iam.ErrInvalidCredentials), "must not mask the failure as bad credentials")

	assert.Zero(t, h.countRows(t, "users"), "no partial user")
	assert.Zero(t, h.countRows(t, "sessions"), "no partial session")
	assert.Zero(t, h.countRows(t, "user_roles"), "no partial role assignment")
}

// Nonexistent username, wrong password, provisioning and disabled states all
// return the same InvalidCredentials semantics; only an active user with the
// correct password receives a session.
func TestLogin_UniformInvalidCredentials(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	_, err := h.svc.Login(ctx, "nobody", "whatever-1", opCtx())
	require.ErrorIs(t, err, iam.ErrInvalidCredentials, "nonexistent username")

	h.register(t, "dave", "Correct-Horse-1")
	userID := h.userIDByUsername(t, "dave")

	_, err = h.svc.Login(ctx, "dave", "wrong-password", opCtx())
	require.ErrorIs(t, err, iam.ErrInvalidCredentials, "wrong password")

	for _, state := range []string{"provisioning", "disabled"} {
		h.setLifecycle(t, userID, state)
		_, err = h.svc.Login(ctx, "dave", "Correct-Horse-1", opCtx())
		require.ErrorIs(t, err, iam.ErrInvalidCredentials, "state %s with correct password", state)
	}

	h.setLifecycle(t, userID, "active")
	session, err := h.svc.Login(ctx, "dave", "Correct-Horse-1", opCtx())
	require.NoError(t, err, "control: active user with correct password")
	assert.Equal(t, userID, session.User.UserID)
	assert.NotEmpty(t, session.Token)
}

// Session lifecycle: valid token authenticates, unknown/revoked/cap-exceeded
// tokens are uniformly invalid, authenticated use slides expires_at forward,
// revoke is idempotent, and a disabled user's session stops authenticating.
func TestSessionLifecycle_AuthenticateRevokeRenewal(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	session := h.register(t, "erin", "Correct-Horse-1")
	userID := h.userIDByUsername(t, "erin")

	principal, err := h.svc.Authenticate(ctx, session.Token, opCtx())
	require.NoError(t, err)
	assert.Equal(t, userID, principal.UserID)

	_, err = h.svc.Authenticate(ctx, "not-a-real-token", opCtx())
	require.ErrorIs(t, err, iam.ErrInvalidToken, "unknown token")

	// Sliding renewal: an authenticated use extends expires_at.
	var before, after time.Time
	require.NoError(t, h.db.QueryRow(ctx,
		"SELECT expires_at FROM sessions WHERE user_id = $1", userID).Scan(&before))
	_, err = h.svc.Authenticate(ctx, session.Token, opCtx())
	require.NoError(t, err)
	require.NoError(t, h.db.QueryRow(ctx,
		"SELECT expires_at FROM sessions WHERE user_id = $1", userID).Scan(&after))
	assert.True(t, after.After(before), "authenticated use must slide expires_at forward")

	// Revocation kills the session; unknown tokens revoke idempotently.
	require.NoError(t, h.svc.RevokeSession(ctx, session.Token, opCtx()))
	_, err = h.svc.Authenticate(ctx, session.Token, opCtx())
	require.ErrorIs(t, err, iam.ErrInvalidToken, "revoked session")
	require.NoError(t, h.svc.RevokeSession(ctx, "some-unknown-token", opCtx()), "idempotent revoke")

	// Absolute lifetime cap: a session older than 7 days is invalid even
	// when never revoked and still inside the idle window.
	session2 := h.register(t, "frank", "Correct-Horse-1")
	_, err = h.db.Exec(ctx, `
		UPDATE sessions
		SET created_at = now() - interval '8 days', expires_at = now() + interval '1 hour'
		WHERE user_id = $1`, h.userIDByUsername(t, "frank"))
	require.NoError(t, err)
	_, err = h.svc.Authenticate(ctx, session2.Token, opCtx())
	require.ErrorIs(t, err, iam.ErrInvalidToken, "absolute lifetime cap")

	// A disabled user's valid session stops authenticating.
	session3 := h.register(t, "george", "Correct-Horse-1")
	georgeID := h.userIDByUsername(t, "george")
	_, err = h.svc.Authenticate(ctx, session3.Token, opCtx())
	require.NoError(t, err)
	h.setLifecycle(t, georgeID, "disabled")
	_, err = h.svc.Authenticate(ctx, session3.Token, opCtx())
	require.ErrorIs(t, err, iam.ErrInvalidToken, "disabled user's session")
}

// GetIdentity returns the identity aggregate (no Organization fields) and
// GetAuthorizationProfile reflects exactly the IAM tables: the default role
// plus the union of its grants, sorted and unique.
func TestGetIdentityAndAuthorizationProfile(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	h.register(t, "heidi", "Correct-Horse-1")
	userID := h.userIDByUsername(t, "heidi")

	identity, err := h.svc.GetIdentity(ctx, userID)
	require.NoError(t, err)
	assert.Equal(t, userID, identity.ID)
	assert.Equal(t, "heidi", identity.Username)
	assert.Equal(t, iam.LifecycleActive, identity.LifecycleState)
	assert.Equal(t, int64(1), identity.Version)

	_, err = h.svc.GetIdentity(ctx, 999999)
	require.ErrorIs(t, err, iam.ErrUserNotFound)

	// Profile matches DB truth: the default 'user' role and its grants.
	profile, err := h.svc.GetAuthorizationProfile(ctx, userID)
	require.NoError(t, err)
	require.Len(t, profile.Roles, 1)
	assert.Equal(t, "user", profile.Roles[0].Code)
	assert.True(t, profile.Roles[0].IsBuiltin)
	assert.Equal(t, h.rolePermissionCodes(t, "user"), profile.EffectivePermissions)
	assertSortedUnique(t, profile.EffectivePermissions)

	// Granting the admin role extends the profile; grants come only from
	// IAM tables and deduplicate across roles.
	adminRoleID := h.roleIDByCode(t, "admin")
	h.assignRole(t, userID, adminRoleID)
	profile, err = h.svc.GetAuthorizationProfile(ctx, userID)
	require.NoError(t, err)
	require.Len(t, profile.Roles, 2)
	expected := sortedUnion(h.rolePermissionCodes(t, "user"), h.rolePermissionCodes(t, "admin"))
	assert.Equal(t, expected, profile.EffectivePermissions)
	assertSortedUnique(t, profile.EffectivePermissions)
}

// Effective permissions are always a non-null empty slice when there are no
// grants — never nil — for both a roleless profile and a fresh 'user' role
// (the seeded built-in user role intentionally carries no grants).
func TestEmptyPermissions_NonNilEmptySlices(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	h.register(t, "ivan", "Correct-Horse-1")
	userID := h.userIDByUsername(t, "ivan")

	profile, err := h.svc.GetAuthorizationProfile(ctx, userID)
	require.NoError(t, err)
	require.Len(t, profile.Roles, 1, "default 'user' role present")
	assert.Equal(t, "user", profile.Roles[0].Code)
	assert.NotNil(t, profile.Roles)
	assert.Equal(t, []string{}, profile.EffectivePermissions, "built-in user role has no grants")
	assert.NotNil(t, profile.EffectivePermissions)

	// A user with NO role assignments still gets non-null empty projections.
	_, err = h.db.Exec(ctx, "DELETE FROM user_roles WHERE user_id = $1", userID)
	require.NoError(t, err)
	profile, err = h.svc.GetAuthorizationProfile(ctx, userID)
	require.NoError(t, err)
	assert.Equal(t, []iam.RoleSummary{}, profile.Roles)
	assert.Equal(t, []string{}, profile.EffectivePermissions)
	assert.NotNil(t, profile.Roles)
	assert.NotNil(t, profile.EffectivePermissions)
}

// HasPermission queries current grants on every invocation; real codes are
// denied without grants, granted with them, and unknown codes never grant.
func TestHasPermission_UnknownCodesNeverGrant(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	h.register(t, "judy", "Correct-Horse-1")
	userID := h.userIDByUsername(t, "judy")

	// The built-in user role carries no grants: every catalog code is denied.
	for _, code := range []string{"roles.read", "roles.write", "departments.read",
		"departments.write", "users.read", "users.write"} {
		allowed, err := h.svc.HasPermission(ctx, userID, code)
		require.NoError(t, err)
		assert.False(t, allowed, "code %s must be denied without grants", code)
	}

	// Unknown codes are false and never grant.
	allowed, err := h.svc.HasPermission(ctx, userID, "system.not.a.real.permission")
	require.NoError(t, err)
	assert.False(t, allowed)

	// Granting the admin role turns every catalog code on…
	h.assignRole(t, userID, h.roleIDByCode(t, "admin"))
	for _, code := range []string{"roles.read", "users.write"} {
		allowed, err := h.svc.HasPermission(ctx, userID, code)
		require.NoError(t, err)
		assert.True(t, allowed, "code %s must be granted via admin role", code)
	}
	// …but unknown codes still do not.
	allowed, err = h.svc.HasPermission(ctx, userID, "system.not.a.real.permission")
	require.NoError(t, err)
	assert.False(t, allowed)

	// No authorization-decision cache: revoking the grant flips the answer.
	_, err = h.db.Exec(ctx, "DELETE FROM user_roles WHERE user_id = $1", userID)
	require.NoError(t, err)
	allowed, err = h.svc.HasPermission(ctx, userID, "roles.read")
	require.NoError(t, err)
	assert.False(t, allowed)
}

// ListRoles is deterministic (id order, matching DB truth), the seeded
// built-ins are flagged built-in, custom roles append and never are.
func TestListRoles_DeterministicOrderAndBuiltins(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	roles, err := h.svc.ListRoles(ctx)
	require.NoError(t, err)
	require.Len(t, roles, 3, "seeded built-in catalog")
	builtin := map[string]bool{}
	gotCodes := make([]string, 0, len(roles))
	for _, r := range roles {
		builtin[r.Code] = true
		gotCodes = append(gotCodes, r.Code)
		assert.Positive(t, r.ID)
		assert.True(t, r.IsBuiltin, "code %s", r.Code)
	}
	for _, code := range []string{"super_admin", "admin", "user"} {
		require.True(t, builtin[code], "built-in %s missing", code)
	}
	// Deterministic order equals the DB id order.
	rows, err := h.db.Query(ctx, "SELECT code FROM roles ORDER BY id")
	require.NoError(t, err)
	defer rows.Close()
	var dbCodes []string
	for rows.Next() {
		var c string
		require.NoError(t, rows.Scan(&c))
		dbCodes = append(dbCodes, c)
	}
	assert.Equal(t, dbCodes, gotCodes)

	// A custom role appends at the end and is never built-in.
	id, err := h.svc.SaveRole(ctx, opCtx(), nil, "审计员", "auditor")
	require.NoError(t, err)
	roles, err = h.svc.ListRoles(ctx)
	require.NoError(t, err)
	require.Len(t, roles, 4)
	assert.Equal(t, "auditor", roles[3].Code)
	assert.False(t, roles[3].IsBuiltin)
	assert.Equal(t, id, roles[3].ID)
}

// ValidateRoleIDs validates the entire set — any missing role rejects it —
// and duplicate input IDs do not duplicate results.
func TestValidateRoleIDs_WholeSetAndDuplicates(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	adminID := h.roleIDByCode(t, "admin")
	userID := h.roleIDByCode(t, "user")

	summaries, err := h.svc.ValidateRoleIDs(ctx, []int64{adminID, userID})
	require.NoError(t, err)
	require.Len(t, summaries, 2)
	assert.Equal(t, "admin", summaries[0].Code)
	assert.Equal(t, "user", summaries[1].Code)

	_, err = h.svc.ValidateRoleIDs(ctx, []int64{adminID, 999999})
	require.ErrorIs(t, err, iam.ErrRoleNotFound)

	summaries, err = h.svc.ValidateRoleIDs(ctx, []int64{adminID, adminID})
	require.NoError(t, err)
	require.Len(t, summaries, 1, "duplicate input IDs must not duplicate results")

	summaries, err = h.svc.ValidateRoleIDs(ctx, []int64{})
	require.NoError(t, err)
	assert.NotNil(t, summaries)
	assert.Len(t, summaries, 0)
}

// SaveRole: create and update per public contract, name/code uniqueness
// enforced in the database, built-in code immutable (name stays editable).
func TestSaveRole_CreateUpdateAndUniqueness(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	id, err := h.svc.SaveRole(ctx, opCtx(), nil, "运营", "operator")
	require.NoError(t, err)
	assert.Positive(t, id)

	_, err = h.svc.SaveRole(ctx, opCtx(), nil, "运营", "operator2")
	require.ErrorIs(t, err, iam.ErrNameTaken, "duplicate name")
	_, err = h.svc.SaveRole(ctx, opCtx(), nil, "运营2", "operator")
	require.ErrorIs(t, err, iam.ErrNameTaken, "duplicate code")

	adminID := h.roleIDByCode(t, "admin")
	_, err = h.svc.SaveRole(ctx, opCtx(), &adminID, "管理员", "root")
	require.ErrorIs(t, err, iam.ErrBuiltinRoleCodeImmutable, "built-in code immutable")
	// Name stays editable; avoid colliding with super_admin's seed name
	// (name uniqueness is global).
	newID, err := h.svc.SaveRole(ctx, opCtx(), &adminID, "总管理员", "admin")
	require.NoError(t, err, "built-in name stays editable")
	assert.Equal(t, adminID, newID)

	_, err = h.svc.SaveRole(ctx, opCtx(), &id, "运营专员", "operator")
	require.NoError(t, err)
	summaries, err := h.svc.ValidateRoleIDs(ctx, []int64{id})
	require.NoError(t, err)
	require.Len(t, summaries, 1)
	assert.Equal(t, "运营专员", summaries[0].Name)
	assert.Equal(t, "operator", summaries[0].Code)
	assert.False(t, summaries[0].IsBuiltin)
}

// GrantBuiltInAdminRole (admin bootstrap, T023): only admin/super_admin codes
// are accepted, the user must already be registered, the grant is idempotent,
// version bumps ONLY on an actual grant change, and other roles are preserved.
func TestGrantBuiltInAdminRole_AdminBootstrap(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// Unknown users are rejected before any write.
	_, err := h.svc.GrantBuiltInAdminRole(ctx, "nobody", "admin")
	require.ErrorIs(t, err, iam.ErrUserNotFound)

	// Only built-in admin codes are accepted.
	h.register(t, "leo", "Correct-Horse-1")
	_, err = h.svc.GrantBuiltInAdminRole(ctx, "leo", "user")
	require.ErrorIs(t, err, iam.ErrInvalidInput, "non-admin code rejected")

	// A new grant bumps version and adds exactly the admin role on top of
	// the existing default 'user' role.
	leoID := h.userIDByUsername(t, "leo")
	var versionBefore int64
	require.NoError(t, h.db.QueryRow(ctx,
		"SELECT version FROM users WHERE id = $1", leoID).Scan(&versionBefore))
	assert.Equal(t, int64(1), versionBefore)

	result, err := h.svc.GrantBuiltInAdminRole(ctx, "leo", "admin")
	require.NoError(t, err)
	assert.Equal(t, leoID, result.UserID)
	assert.Equal(t, int64(2), result.ResultingVersion)

	var versionAfter int64
	require.NoError(t, h.db.QueryRow(ctx,
		"SELECT version FROM users WHERE id = $1", leoID).Scan(&versionAfter))
	assert.Equal(t, int64(2), versionAfter, "new grant bumps version")
	assert.Equal(t, int64(2), h.countRows(t, "user_roles"), "default role preserved")

	// Re-granting the same role is idempotent: no extra link, no bump.
	result, err = h.svc.GrantBuiltInAdminRole(ctx, "leo", "admin")
	require.NoError(t, err)
	assert.Equal(t, int64(2), result.ResultingVersion, "no bump on repeated grant")
	require.NoError(t, h.db.QueryRow(ctx,
		"SELECT version FROM users WHERE id = $1", leoID).Scan(&versionAfter))
	assert.Equal(t, int64(2), versionAfter, "version unchanged on repeated grant")
	assert.Equal(t, int64(2), h.countRows(t, "user_roles"))

	// Granting the second admin code bumps again and preserves both roles.
	result, err = h.svc.GrantBuiltInAdminRole(ctx, "leo", "super_admin")
	require.NoError(t, err)
	assert.Equal(t, int64(3), result.ResultingVersion)
	assert.Equal(t, int64(3), h.countRows(t, "user_roles"))
	profile, err := h.svc.GetAuthorizationProfile(ctx, leoID)
	require.NoError(t, err)
	require.Len(t, profile.Roles, 3, "user + admin + super_admin all preserved")
}

// DeleteRoles rejects built-in and referenced roles; the full batch is
// preflighted before any deletion, so one invalid target deletes nothing.
func TestDeleteRoles_PreflightAndBatchAtomicity(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	adminID := h.roleIDByCode(t, "admin")
	err := h.svc.DeleteRoles(ctx, opCtx(), []int64{adminID})
	require.ErrorIs(t, err, iam.ErrBuiltinRoleDeleteProtected)

	h.register(t, "kate", "Correct-Horse-1")
	userRoleID := h.roleIDByCode(t, "user")
	// Built-in protection wins regardless of references.
	err = h.svc.DeleteRoles(ctx, opCtx(), []int64{userRoleID})
	require.ErrorIs(t, err, iam.ErrBuiltinRoleDeleteProtected, "built-in role with users")

	customA, err := h.svc.SaveRole(ctx, opCtx(), nil, "角色A", "role_a")
	require.NoError(t, err)
	customB, err := h.svc.SaveRole(ctx, opCtx(), nil, "角色B", "role_b")
	require.NoError(t, err)
	h.assignRole(t, h.userIDByUsername(t, "kate"), customB)

	// One referenced target rejects the whole batch: nothing is deleted.
	err = h.svc.DeleteRoles(ctx, opCtx(), []int64{customA, customB})
	require.ErrorIs(t, err, iam.ErrDeleteProtected)
	var still int64
	require.NoError(t, h.db.QueryRow(ctx,
		"SELECT count(*) FROM roles WHERE id = $1", customA).Scan(&still))
	assert.Equal(t, int64(1), still, "valid target must survive a rejected batch")

	// Unreferenced custom roles delete cleanly.
	kateID := h.userIDByUsername(t, "kate")
	_, err = h.db.Exec(ctx,
		"DELETE FROM user_roles WHERE user_id = $1 AND role_id = $2", kateID, customB)
	require.NoError(t, err)
	require.NoError(t, h.svc.DeleteRoles(ctx, opCtx(), []int64{customB}))
	require.NoError(t, h.svc.DeleteRoles(ctx, opCtx(), []int64{customA}))

	// Missing role rejects the batch.
	err = h.svc.DeleteRoles(ctx, opCtx(), []int64{customA})
	require.ErrorIs(t, err, iam.ErrRoleNotFound)
}

// --- US3 lifecycle contract suite (task T050) --------------------------------
//
// Activation-timeout resolve-first decision (contracts/consistency-and-
// compensation.md rule 7 / C7): a timeout is an unknown outcome, never
// automatic failure — the orchestrator resolves the participant receipt BEFORE
// retrying or compensating. Committed evidence means succeed without re-issuing
// the command, and only a proven non-commit may start version-guarded
// compensation; absence is never fabricated into success and a fingerprint
// mismatch is an operation conflict. (The other T050 lifecycle invariants —
// provisioning cannot authenticate, stale-version CAS rejection, operation
// idempotent replay, batch atomicity, delete+receipt+outbox atomicity with
// tombstone versions — are pinned by the per-command suites of T045-T049.)

func TestActivationTimeout_ResolveCommittedThenDecideSucceed(t *testing.T) {
	h := newHarness(t)
	store := postgres.NewStore(h.pool)
	result := provision(t, h, "11111111-0000-0000-0000-000000000001", "timeout_committed", nil)
	_, err := activate(t, h, "11111111-0000-0000-0000-000000000002", result.UserID, 1)
	require.NoError(t, err) // the HTTP request "times out" after commit

	fp, err := consistency.FingerprintV1("activate_user", 1, map[string]any{
		"user_id":          result.UserID,
		"expected_version": int64(1),
	})
	require.NoError(t, err)

	// orchestrator resolves the receipt before any retry/compensation decision
	receipt, err := h.svc.ResolveCommand(context.Background(),
		"11111111-0000-0000-0000-000000000002", "activate_user", fp)
	require.NoError(t, err)
	require.NotNil(t, receipt, "committed activation is proven, never re-issued")
	require.NotNil(t, receipt.ResultingVersion)
	assert.Equal(t, int64(2), *receipt.ResultingVersion)

	// decision: succeed — re-issuing the same command on the same operation
	// replays the committed version without a second transition
	version, err := activate(t, h, "11111111-0000-0000-0000-000000000002", result.UserID, 1)
	require.NoError(t, err)
	assert.Equal(t, int64(2), version)
	state, currentVersion := h.userState(t, result.UserID)
	assert.Equal(t, "active", state)
	assert.Equal(t, int64(2), currentVersion)
	assert.Equal(t, int64(1), h.countRows(t, "iam_command_receipts WHERE command_name = 'activate_user'"))

	// and version-guarded compensation of the now-active subject is rejected —
	// only a proven non-commit may start compensation
	err = compensate(t, h, "11111111-0000-0000-0000-000000000003", result.UserID, 2)
	require.Error(t, err)
	assert.ErrorIs(t, err, iam.ErrInvalidLifecycleTransition)
	_, err = store.GetUserByID(ctx, result.UserID)
	require.NoError(t, err, "subject survives the rejected compensation")
}

func TestActivationTimeout_ResolveAbsentThenDecideCompensate(t *testing.T) {
	h := newHarness(t)
	store := postgres.NewStore(h.pool)
	result := provision(t, h, "11111111-0000-0000-0000-000000000011", "timeout_absent", nil)

	fp, err := consistency.FingerprintV1("activate_user", 1, map[string]any{
		"user_id":          result.UserID,
		"expected_version": int64(1),
	})
	require.NoError(t, err)

	// timeout with nothing committed: absence never fabricates success
	receipt, err := h.svc.ResolveCommand(context.Background(),
		"11111111-0000-0000-0000-000000000012", "activate_user", fp)
	require.NoError(t, err)
	assert.Nil(t, receipt)

	// decision: proven non-commit — version-guarded compensation is legal and
	// consumes exactly the expected version
	require.NoError(t, compensate(t, h, "11111111-0000-0000-0000-000000000013", result.UserID, 1))
	_, err = store.GetUserByID(ctx, result.UserID)
	assert.ErrorIs(t, err, iam.ErrUserNotFound)
	events := h.outboxRows(t)
	require.Len(t, events, 1)
	assert.Equal(t, "iam.user.deleted", events[0].EventType)
	assert.Equal(t, int64(2), events[0].AggregateVersion, "tombstone = prior version + 1")
}

func TestActivationTimeout_ResolveMismatchRejects(t *testing.T) {
	h := newHarness(t)
	result := provision(t, h, "11111111-0000-0000-0000-000000000021", "timeout_mismatch", nil)
	_, err := activate(t, h, "11111111-0000-0000-0000-000000000022", result.UserID, 1)
	require.NoError(t, err)

	// a resolve whose expected fingerprint differs from the committed evidence
	// is an operation conflict, never a re-application
	fpOther, err := consistency.FingerprintV1("activate_user", 1, map[string]any{
		"user_id":          result.UserID,
		"expected_version": int64(2),
	})
	require.NoError(t, err)
	_, err = h.svc.ResolveCommand(context.Background(),
		"11111111-0000-0000-0000-000000000022", "activate_user", fpOther)
	require.Error(t, err)
	assert.ErrorIs(t, err, iam.ErrOperationConflict)

	state, _ := h.userState(t, result.UserID)
	assert.Equal(t, "active", state, "committed activation untouched")
}
