// Public HTTP compatibility suite (quickstart Checkpoint A, T005).
//
// One table-driven suite runs against the routers reported by builders():
// the Admin BFF router (every build) and, behind `-tags rollback`, the legacy
// monolith router. The suite asserts the frozen public contract from
// contracts/http-api-compatibility.md — auth behavior, permission matrix,
// built-in role protection, batch atomicity and department-filtered
// pagination with explicit department:null. Workflow/idempotency errors and
// Retry-After assertions (T056/T057) run fully on the Admin BFF wiring and
// characterize the legacy wiring's pre-idempotency behavior.
package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/hdw/vue-element-plus-admin/backend/internal/adminbff"
	adminbffpostgres "github.com/hdw/vue-element-plus-admin/backend/internal/adminbff/postgres"
	"github.com/hdw/vue-element-plus-admin/backend/internal/consistency"
	"github.com/hdw/vue-element-plus-admin/backend/internal/database"
	"github.com/hdw/vue-element-plus-admin/backend/internal/iam"
	iampostgres "github.com/hdw/vue-element-plus-admin/backend/internal/iam/postgres"
	"github.com/hdw/vue-element-plus-admin/backend/internal/organization"
	orgpostgres "github.com/hdw/vue-element-plus-admin/backend/internal/organization/postgres"
)

// TestMain boots a shared Postgres container and applies all migrations once
// (mirrors internal/database/rbac_migration_test.go).
func TestMain(m *testing.M) {
	ctx := context.Background()

	pg, err := postgres.Run(ctx, "postgres:17-alpine",
		postgres.WithDatabase("scaffold_test"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		testcontainers.WithWaitStrategyAndDeadline(
			60*time.Second,
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "compat suite: failed to start postgres container: %v\n", err)
		os.Exit(1)
	}

	connStr, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintf(os.Stderr, "compat suite: failed to get connection string: %v\n", err)
		os.Exit(1)
	}

	if err := database.RunMigrations(connStr); err != nil {
		fmt.Fprintf(os.Stderr, "compat suite: failed to run migrations: %v\n", err)
		os.Exit(1)
	}

	db, err := database.Connect(ctx, database.Config{
		URL:             connStr,
		MaxConns:        10,
		MinConns:        0,
		MaxConnLifetime: time.Hour,
		MaxConnIdleTime: 30 * time.Minute,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "compat suite: failed to connect: %v\n", err)
		os.Exit(1)
	}

	testDB = db

	code := m.Run()

	db.Close()
	_ = pg.Terminate(ctx)
	os.Exit(code)
}

var testDB *database.DB

// --- router builders ---

// routerBuilder names a router constructor the suite must stay compatible with.
type routerBuilder struct {
	name  string
	build func(t *testing.T, env *testEnv) (*gin.Engine, func())
}

// buildLegacyRouter lives in compat_builders_legacy_test.go behind
// `//go:build rollback` — the shipped build decommissions the legacy monolith
// router, so the legacy builder is a rollback-window capability.

// buildBFFRouter wires the Admin BFF router (T031): IAM and Organization
// adapters through the application services and the BFF composition. The
// managed-user write endpoints run the workflow sagas (T056) with the
// Idempotency-Key header. This test file is exempt from the adminbff
// boundary rules — it is the baseline arbiter wiring real adapters.
func buildBFFRouter(t *testing.T, env *testEnv) (*gin.Engine, func()) {
	t.Helper()
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	iamSvc := iam.NewService(iampostgres.NewStore(env.db.Pool), discard)
	orgSvc := organization.NewService(orgpostgres.NewStore(env.db.Pool), discard)

	bffSvc := adminbff.NewService(adminbff.IAMParticipants{
		Auth:     iamSvc,
		Identity: iamSvc,
		Roles:    iamSvc,
		Managed:  iamSvc,
		Receipts: iamSvc,
	}, adminbff.OrganizationParticipants{
		Departments: orgSvc,
		Membership:  orgSvc,
		Receipts:    orgSvc,
		Inbox:       orgSvc,
	}, adminbffpostgres.NewStore(env.db.Pool), testLogger())

	router, closeFn := NewRouter(RouterDeps{
		Service:     bffSvc,
		Auth:        iamSvc,
		Identity:    iamSvc,
		Roles:       iamSvc,
		Departments: orgSvc,
		Logger:      testLogger(),
		CORSOrigins: nil,               // allow all (dev convenience)
		RateLimit:   RateLimitConfig{}, // disabled in tests
	})
	return router, closeFn
}

// --- test environment ---

type authedUser struct {
	id       int64
	username string
	token    string
}

type testEnv struct {
	db     *database.DB
	prefix string
	router *gin.Engine
	admin  *authedUser // admin role (all six permissions)
	plain  *authedUser // default user role (no permissions)
	deptID int64       // seeded "兼容测试部门"
}

// newTestEnv creates an empty environment bound to the shared migrated DB.
func newTestEnv(t *testing.T, prefix string) *testEnv {
	t.Helper()
	return &testEnv{db: testDB, prefix: prefix}
}

// seed registers fresh users per router builder so username uniqueness holds
// when both constructors run against the shared container. Requires router.
func (e *testEnv) seed(t *testing.T) {
	t.Helper()
	e.admin = e.register(t, e.prefix+"_admin", "admin_pass_123")
	e.plain = e.register(t, e.prefix+"_plain", "plain_pass_123")

	// Grant the admin role directly (mirrors cmd/admin-init semantics).
	var adminRoleID, userRoleID int64
	require.NoError(t, e.db.Pool.QueryRow(context.Background(),
		`SELECT id FROM roles WHERE code = 'admin'`).Scan(&adminRoleID))
	require.NoError(t, e.db.Pool.QueryRow(context.Background(),
		`SELECT id FROM roles WHERE code = 'user'`).Scan(&userRoleID))
	_, err := e.db.Pool.Exec(context.Background(),
		`INSERT INTO user_roles (user_id, role_id) VALUES ($1, $2)`, e.admin.id, adminRoleID)
	require.NoError(t, err)

	// Fill account/email through the managed-user endpoint so /auth/me
	// carries the contract's full profile (register only sets username).
	status, body := doJSON(t, e.router, http.MethodPost, "/api/v1/users", e.admin.token, map[string]any{
		"id":       e.admin.id,
		"username": e.admin.username,
		"account":  "测试管理员",
		"email":    "admin_suite@example.com",
		"roles":    []int64{adminRoleID, userRoleID},
	})
	require.Equal(t, http.StatusOK, status, "update admin profile: %v", body)

	// Seed a dedicated department for pagination assertions. Each builder
	// gets its own row on the shared container (idempotent by name) so the
	// filtered totals count only this wiring's members.
	require.NoError(t, e.db.Pool.QueryRow(context.Background(),
		`INSERT INTO departments (name, parent_id) VALUES ($1, NULL)
		 ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name
		 RETURNING id`, fmt.Sprintf("兼容测试部门_%s", e.prefix)).Scan(&e.deptID))
}

// register creates a user through the public endpoint and returns the session.
func (e *testEnv) register(t *testing.T, username, password string) *authedUser {
	t.Helper()
	status, body := doJSON(t, e.router, http.MethodPost, "/api/v1/auth/register", "", map[string]any{
		"username": username,
		"password": password,
	})
	require.Equal(t, http.StatusCreated, status, "register %s", username)
	data, ok := body["data"].(map[string]any)
	require.True(t, ok)
	token, ok := data["token"].(string)
	require.True(t, ok)
	require.NotEmpty(t, token)
	user, ok := data["user"].(map[string]any)
	require.True(t, ok)
	id, ok := user["id"].(float64)
	require.True(t, ok)
	return &authedUser{id: int64(id), username: username, token: token}
}

// login obtains a fresh session token for username.
func (e *testEnv) login(t *testing.T, username, password string) string {
	t.Helper()
	status, body := doJSON(t, e.router, http.MethodPost, "/api/v1/auth/login", "", map[string]any{
		"username": username,
		"password": password,
	})
	require.Equal(t, http.StatusOK, status)
	data, ok := body["data"].(map[string]any)
	require.True(t, ok)
	token, ok := data["token"].(string)
	require.True(t, ok)
	require.NotEmpty(t, token)
	return token
}

// revokeAdminRole removes the admin role grant: the session token stays
// valid but every route authorization now reads the live permission set and
// fails (T058 current-authorization-before-replay).
func (e *testEnv) revokeAdminRole(t *testing.T) {
	t.Helper()
	_, err := e.db.Pool.Exec(context.Background(),
		`DELETE FROM user_roles WHERE user_id = $1
		 AND role_id = (SELECT id FROM roles WHERE code = 'admin')`, e.admin.id)
	require.NoError(t, err)
}

// restoreAdminRole re-grants the admin role (idempotent).
func (e *testEnv) restoreAdminRole(t *testing.T) {
	t.Helper()
	_, err := e.db.Pool.Exec(context.Background(),
		`INSERT INTO user_roles (user_id, role_id)
		 SELECT $1, id FROM roles WHERE code = 'admin'
		 ON CONFLICT DO NOTHING`, e.admin.id)
	require.NoError(t, err)
}

// --- HTTP helpers ---

func doJSON(t *testing.T, r *gin.Engine, method, path, token string, body any) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, path, &buf)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var parsed map[string]any
	if w.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &parsed))
	}
	return w.Code, parsed
}

// requireError asserts the exact contract error envelope.
func requireError(t *testing.T, status int, body map[string]any, wantStatus int, wantCode string) {
	t.Helper()
	require.Equal(t, wantStatus, status, "error status for %s", wantCode)
	errObj, ok := body["error"].(map[string]any)
	require.True(t, ok, "error envelope present")
	assert.Equal(t, wantCode, errObj["code"], "error code")
	msg, _ := errObj["message"].(string)
	assert.NotEmpty(t, msg, "safe message present")
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// --- the suite ---

func TestCompatibilitySuite(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// builders() is build-variant: compat_builders_default_test.go returns the
	// Admin BFF router only; compat_builders_legacy_test.go (rollback build)
	// adds the legacy monolith router so the public contract stays proven
	// against both wirings through the rollback support window.
	for _, b := range builders() {
		t.Run(b.name, func(t *testing.T) {
			env := newTestEnv(t, b.name)
			router, closeFn := b.build(t, env)
			env.router = router
			defer closeFn()
			env.seed(t)
			runCases(t, env)
		})
	}
}

type compatCase struct {
	name string
	run  func(t *testing.T, e *testEnv)
}

func runCases(t *testing.T, e *testEnv) {
	cases := []compatCase{
		{name: "register 201 + response shape", run: caseRegister201},
		{name: "register duplicate 409 AUTH_USERNAME_TAKEN", run: caseRegisterDuplicate},
		{name: "register invalid input 400", run: caseRegisterInvalid},
		{name: "login 200", run: caseLogin200},
		{name: "login uniform 401 AUTH_INVALID_CREDENTIALS", run: caseLoginUniform401},
		{name: "logout 200 {\"data\":{}} and revokes session", run: caseLogout200},
		{name: "logout missing header 401", run: caseLogoutMissingHeader},
		{name: "logout garbage token idempotent 200", run: caseLogoutGarbage},
		{name: "GET /auth/me full profile non-null arrays", run: caseMeProfile},
		{name: "GET /auth/me department explicit null", run: caseMeDepartmentNull},
		{name: "GET /auth/me missing token 401", run: caseMeNoToken},
		{name: "GET /auth/me invalid token 401", run: caseMeInvalidToken},
		{name: "403 AUTH_FORBIDDEN preserves session", run: case403PreservesSession},
		{name: "permission matrix (nine endpoints)", run: casePermissionMatrix},
		{name: "built-in role code immutable", run: caseBuiltinCodeImmutable},
		{name: "built-in role delete protected", run: caseBuiltinDeleteProtected},
		{name: "batch atomicity (zero deletion on invalid batch)", run: caseBatchAtomicity},
		{name: "department-filtered pagination + explicit department null", run: caseDepartmentPagination},
		{name: "workflow/idempotency errors + Retry-After", run: caseWorkflow},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.run(t, e)
		})
	}
}

// --- auth cases ---

func caseRegister201(t *testing.T, e *testEnv) {
	// Builder-prefixed so both wirings can register on the shared container.
	username := e.prefix + "_reg"
	status, body := doJSON(t, e.router, http.MethodPost, "/api/v1/auth/register", "", map[string]any{
		"username": username,
		"password": "suite_pass_123",
	})
	require.Equal(t, http.StatusCreated, status)
	data, ok := body["data"].(map[string]any)
	require.True(t, ok)
	assert.NotEmpty(t, data["token"], "token")
	assert.Equal(t, "Bearer", data["token_type"])
	assert.Greater(t, data["expires_in"].(float64), 0.0)
	user, ok := data["user"].(map[string]any)
	require.True(t, ok)
	assert.Greater(t, user["id"].(float64), 0.0)
	assert.Equal(t, username, user["username"])
}

func caseRegisterDuplicate(t *testing.T, e *testEnv) {
	status, body := doJSON(t, e.router, http.MethodPost, "/api/v1/auth/register", "", map[string]any{
		"username": e.prefix + "_reg",
		"password": "suite_pass_123",
	})
	requireError(t, status, body, http.StatusConflict, "AUTH_USERNAME_TAKEN")
}

func caseRegisterInvalid(t *testing.T, e *testEnv) {
	status, body := doJSON(t, e.router, http.MethodPost, "/api/v1/auth/register", "", map[string]any{
		"username": "ab", // too short
		"password": "short",
	})
	requireError(t, status, body, http.StatusBadRequest, "AUTH_INVALID_INPUT")
}

func caseLogin200(t *testing.T, e *testEnv) {
	status, body := doJSON(t, e.router, http.MethodPost, "/api/v1/auth/login", "", map[string]any{
		"username": e.admin.username,
		"password": "admin_pass_123",
	})
	require.Equal(t, http.StatusOK, status)
	data, ok := body["data"].(map[string]any)
	require.True(t, ok)
	assert.NotEmpty(t, data["token"])
}

func caseLoginUniform401(t *testing.T, e *testEnv) {
	// Wrong password and nonexistent username MUST be indistinguishable.
	wrongPW, wrongPWBody := doJSON(t, e.router, http.MethodPost, "/api/v1/auth/login", "", map[string]any{
		"username": e.admin.username,
		"password": "definitely_wrong",
	})
	requireError(t, wrongPW, wrongPWBody, http.StatusUnauthorized, "AUTH_INVALID_CREDENTIALS")

	noUser, noUserBody := doJSON(t, e.router, http.MethodPost, "/api/v1/auth/login", "", map[string]any{
		"username": "no_such_user_xyz",
		"password": "whatever_123",
	})
	requireError(t, noUser, noUserBody, http.StatusUnauthorized, "AUTH_INVALID_CREDENTIALS")
}

func caseLogout200(t *testing.T, e *testEnv) {
	token := e.login(t, e.admin.username, "admin_pass_123")

	status, body := doJSON(t, e.router, http.MethodPost, "/api/v1/auth/logout", token, nil)
	require.Equal(t, http.StatusOK, status, "logout MUST be 200, never 204")
	raw := body
	assert.Equal(t, map[string]any{}, raw["data"], `body MUST be {"data":{}}`)

	// Session is revoked: same token no longer authenticates.
	meStatus, meBody := doJSON(t, e.router, http.MethodGet, "/api/v1/auth/me", token, nil)
	requireError(t, meStatus, meBody, http.StatusUnauthorized, "AUTH_INVALID_TOKEN")
}

func caseLogoutMissingHeader(t *testing.T, e *testEnv) {
	status, body := doJSON(t, e.router, http.MethodPost, "/api/v1/auth/logout", "", nil)
	requireError(t, status, body, http.StatusUnauthorized, "AUTH_INVALID_TOKEN")
}

func caseLogoutGarbage(t *testing.T, e *testEnv) {
	// Well-formed Bearer with an unknown token: idempotent 200, state hidden.
	status, body := doJSON(t, e.router, http.MethodPost, "/api/v1/auth/logout",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, map[string]any{}, body["data"])
}

func caseMeProfile(t *testing.T, e *testEnv) {
	status, body := doJSON(t, e.router, http.MethodGet, "/api/v1/auth/me", e.admin.token, nil)
	require.Equal(t, http.StatusOK, status)
	data, ok := body["data"].(map[string]any)
	require.True(t, ok)
	user, ok := data["user"].(map[string]any)
	require.True(t, ok)

	assert.Equal(t, e.admin.username, user["username"])
	assert.NotEmpty(t, user["account"])
	assert.NotEmpty(t, user["email"])
	assert.NotEmpty(t, user["created_at"])

	// Non-null arrays (contract: MUST be arrays, [] not null).
	roles, ok := user["roles"].([]any)
	require.True(t, ok, "roles MUST be an array")
	perms, ok := user["effective_permissions"].([]any)
	require.True(t, ok, "effective_permissions MUST be an array")
	assert.NotEmpty(t, roles, "admin has roles")
	assert.NotEmpty(t, perms, "admin role grants permissions")
	permSet := map[string]bool{}
	for _, p := range perms {
		permSet[p.(string)] = true
	}
	for _, want := range []string{"roles.read", "roles.write", "departments.read", "departments.write", "users.read", "users.write"} {
		assert.True(t, permSet[want], "admin effective permissions include %s", want)
	}
}

func caseMeDepartmentNull(t *testing.T, e *testEnv) {
	// plain user has no department: field MUST be JSON null, never omitted.
	status, body := doJSON(t, e.router, http.MethodGet, "/api/v1/auth/me", e.plain.token, nil)
	require.Equal(t, http.StatusOK, status)
	user := body["data"].(map[string]any)["user"].(map[string]any)
	val, present := user["department"]
	assert.True(t, present, "department field MUST be present")
	assert.Nil(t, val, "department MUST be JSON null without membership")

	// Non-null empty arrays for a permission-less user.
	perms, ok := user["effective_permissions"].([]any)
	require.True(t, ok, "effective_permissions MUST be an array")
	assert.Empty(t, perms, "plain user has [] permissions")
	roles, ok := user["roles"].([]any)
	require.True(t, ok, "roles MUST be an array")
	assert.NotEmpty(t, roles, "default user role assigned at register")
}

func caseMeNoToken(t *testing.T, e *testEnv) {
	status, body := doJSON(t, e.router, http.MethodGet, "/api/v1/auth/me", "", nil)
	requireError(t, status, body, http.StatusUnauthorized, "AUTH_INVALID_TOKEN")
}

func caseMeInvalidToken(t *testing.T, e *testEnv) {
	status, body := doJSON(t, e.router, http.MethodGet, "/api/v1/auth/me",
		"not-a-real-token-that-is-long-enough-xxxxxxxx", nil)
	requireError(t, status, body, http.StatusUnauthorized, "AUTH_INVALID_TOKEN")
}

// --- authorization cases ---

func case403PreservesSession(t *testing.T, e *testEnv) {
	// Plain user lacks roles.read: 403 AUTH_FORBIDDEN, session NOT revoked.
	status, body := doJSON(t, e.router, http.MethodGet, "/api/v1/roles", e.plain.token, nil)
	requireError(t, status, body, http.StatusForbidden, "AUTH_FORBIDDEN")

	// Same token still authenticates afterwards.
	meStatus, _ := doJSON(t, e.router, http.MethodGet, "/api/v1/auth/me", e.plain.token, nil)
	require.Equal(t, http.StatusOK, meStatus, "403 must not revoke the session")
}

// casePermissionMatrix drives all nine management endpoints with a
// permissioned and a permission-less actor. Delete endpoints delete real
// entities created by the preceding step (empty-id batches would be rejected
// by validation and prove nothing about the write path).
func casePermissionMatrix(t *testing.T, e *testEnv) {
	type step struct {
		name   string
		method string
		path   string
		body   func() map[string]any
	}
	steps := []step{
		{name: "GET /roles", method: http.MethodGet, path: "/api/v1/roles"},
		{name: "POST /roles", method: http.MethodPost, path: "/api/v1/roles",
			body: func() map[string]any { return map[string]any{"name": "矩阵角色", "code": "matrix_role"} }},
		{name: "GET /departments", method: http.MethodGet, path: "/api/v1/departments"},
		{name: "POST /departments", method: http.MethodPost, path: "/api/v1/departments",
			body: func() map[string]any { return map[string]any{"name": "矩阵部门"} }},
		{name: "GET /users", method: http.MethodGet, path: "/api/v1/users"},
		{name: "POST /users", method: http.MethodPost, path: "/api/v1/users",
			body: func() map[string]any {
				return map[string]any{"username": "matrix_user", "password": "matrix_pass_123", "roles": []int64{}}
			}},
	}

	for _, s := range steps {
		t.Run(s.name, func(t *testing.T) {
			var body map[string]any
			if s.body != nil {
				body = s.body()
			}
			// Permissioned actor: 2xx success with non-empty data envelope.
			status, resp := doJSON(t, e.router, s.method, s.path, e.admin.token, body)
			require.Equal(t, http.StatusOK, status, "admin %s %s", s.method, s.path)
			data, ok := resp["data"].(map[string]any)
			require.True(t, ok, "write responses MUST have non-empty data envelope")
			_ = data
			// Permission-less actor: 403 AUTH_FORBIDDEN.
			forbiddenStatus, forbiddenBody := doJSON(t, e.router, s.method, s.path, e.plain.token, body)
			requireError(t, forbiddenStatus, forbiddenBody, http.StatusForbidden, "AUTH_FORBIDDEN")
		})
	}

	// Capture the created entities, then delete each through the matching
	// write endpoint (also verifying the plain actor stays forbidden).
	roleID := e.lookupID(t, "role", "matrix_role")
	require.NotZero(t, roleID, "matrix role created")
	deptID := e.lookupID(t, "department", "矩阵部门")
	require.NotZero(t, deptID, "matrix department created")
	userID := e.lookupID(t, "user", "matrix_user")
	require.NotZero(t, userID, "matrix user created")

	for _, s := range []step{
		{name: "POST /roles/delete", method: http.MethodPost, path: "/api/v1/roles/delete",
			body: func() map[string]any { return map[string]any{"ids": []int64{roleID}} }},
		{name: "POST /departments/delete", method: http.MethodPost, path: "/api/v1/departments/delete",
			body: func() map[string]any { return map[string]any{"ids": []int64{deptID}} }},
		{name: "POST /users/delete", method: http.MethodPost, path: "/api/v1/users/delete",
			body: func() map[string]any { return map[string]any{"ids": []int64{userID}} }},
	} {
		t.Run(s.name, func(t *testing.T) {
			status, resp := doJSON(t, e.router, s.method, s.path, e.admin.token, s.body())
			require.Equal(t, http.StatusOK, status, "admin %s %s", s.method, s.path)
			assert.Equal(t, map[string]any{}, resp["data"])
			forbiddenStatus, forbiddenBody := doJSON(t, e.router, s.method, s.path, e.plain.token, s.body())
			requireError(t, forbiddenStatus, forbiddenBody, http.StatusForbidden, "AUTH_FORBIDDEN")
		})
	}
}

// lookupID finds an entity id by role code / department name / username.
func (e *testEnv) lookupID(t *testing.T, kind, name string) int64 {
	t.Helper()
	switch kind {
	case "role":
		_, body := doJSON(t, e.router, http.MethodGet, "/api/v1/roles", e.admin.token, nil)
		for _, r := range body["data"].(map[string]any)["list"].([]any) {
			if r.(map[string]any)["code"] == name {
				return int64(r.(map[string]any)["id"].(float64))
			}
		}
	case "department":
		_, body := doJSON(t, e.router, http.MethodGet, "/api/v1/departments", e.admin.token, nil)
		var walk func(nodes []any) int64
		walk = func(nodes []any) int64 {
			for _, n := range nodes {
				node := n.(map[string]any)
				if node["name"] == name {
					return int64(node["id"].(float64))
				}
				if children, ok := node["children"].([]any); ok {
					if id := walk(children); id != 0 {
						return id
					}
				}
			}
			return 0
		}
		return walk(body["data"].(map[string]any)["list"].([]any))
	case "user":
		// The shared container accumulates users across builders and cases;
		// request a large page so the lookup never depends on the default
		// page_size (10).
		_, body := doJSON(t, e.router, http.MethodGet, "/api/v1/users?page_index=1&page_size=1000", e.admin.token, nil)
		for _, u := range body["data"].(map[string]any)["list"].([]any) {
			if u.(map[string]any)["username"] == name {
				return int64(u.(map[string]any)["id"].(float64))
			}
		}
	}
	t.Fatalf("lookupID: unknown kind %q", kind)
	return 0
}

// --- RBAC protection cases ---

func builtinRoleIDs(t *testing.T, e *testEnv) (superAdminID, adminID int64) {
	t.Helper()
	_, body := doJSON(t, e.router, http.MethodGet, "/api/v1/roles", e.admin.token, nil)
	list := body["data"].(map[string]any)["list"].([]any)
	for _, r := range list {
		item := r.(map[string]any)
		switch item["code"] {
		case "super_admin":
			superAdminID = int64(item["id"].(float64))
		case "admin":
			adminID = int64(item["id"].(float64))
		}
	}
	require.NotZero(t, superAdminID)
	require.NotZero(t, adminID)
	return superAdminID, adminID
}

func caseBuiltinCodeImmutable(t *testing.T, e *testEnv) {
	superAdminID, adminID := builtinRoleIDs(t, e)
	for _, id := range []int64{superAdminID, adminID} {
		status, body := doJSON(t, e.router, http.MethodPost, "/api/v1/roles", e.admin.token, map[string]any{
			"id":   id,
			"name": "改名尝试",
			"code": "renamed_code",
		})
		requireError(t, status, body, http.StatusConflict, "BUILTIN_ROLE_CODE_IMMUTABLE")
	}
}

func caseBuiltinDeleteProtected(t *testing.T, e *testEnv) {
	superAdminID, adminID := builtinRoleIDs(t, e)
	for _, id := range []int64{superAdminID, adminID} {
		status, body := doJSON(t, e.router, http.MethodPost, "/api/v1/roles/delete", e.admin.token,
			map[string]any{"ids": []int64{id}})
		requireError(t, status, body, http.StatusConflict, "BUILTIN_ROLE_DELETE_PROTECTED")
	}

	// Roles still present.
	_, body := doJSON(t, e.router, http.MethodGet, "/api/v1/roles", e.admin.token, nil)
	codes := map[string]bool{}
	for _, r := range body["data"].(map[string]any)["list"].([]any) {
		codes[r.(map[string]any)["code"].(string)] = true
	}
	assert.True(t, codes["super_admin"])
	assert.True(t, codes["admin"])
}

// --- batch semantics ---

func caseBatchAtomicity(t *testing.T, e *testEnv) {
	createUser := func(username string) int64 {
		t.Helper()
		status, body := doJSON(t, e.router, http.MethodPost, "/api/v1/users", e.admin.token, map[string]any{
			"username": username,
			"password": "atomic_pass_123",
			"roles":    []int64{},
		})
		require.Equal(t, http.StatusOK, status)
		_ = body
		// Both wirings share the container DB (the matrix case cleans up its
		// own rows), so the default page of 10 may not reach rows created
		// last: list with the max page size for a deterministic lookup.
		_, userBody := doJSON(t, e.router, http.MethodGet, "/api/v1/users?page_index=1&page_size=100", e.admin.token, nil)
		for _, u := range userBody["data"].(map[string]any)["list"].([]any) {
			if u.(map[string]any)["username"] == username {
				return int64(u.(map[string]any)["id"].(float64))
			}
		}
		t.Fatalf("user %s not found after create", username)
		return 0
	}

	// Builder-prefixed like the other cases, so a rerun on the shared DB
	// never collides with a previous builder's leftovers.
	u1 := createUser(e.prefix + "_atomic_user_1")
	u2 := createUser(e.prefix + "_atomic_user_2")

	// Batch with a nonexistent ID: full-batch preflight rejects, ZERO deleted.
	status, body := doJSON(t, e.router, http.MethodPost, "/api/v1/users/delete", e.admin.token,
		map[string]any{"ids": []int64{u1, 999999}})
	requireError(t, status, body, http.StatusNotFound, "USER_NOT_FOUND")

	_, userBody := doJSON(t, e.router, http.MethodGet, "/api/v1/users?page_index=1&page_size=100", e.admin.token, nil)
	remaining := map[int64]bool{}
	for _, u := range userBody["data"].(map[string]any)["list"].([]any) {
		remaining[int64(u.(map[string]any)["id"].(float64))] = true
	}
	assert.True(t, remaining[u1], "u1 must survive a rejected batch")
	assert.True(t, remaining[u2], "u2 must survive a rejected batch")

	// Valid batch deletes both atomically.
	status, body = doJSON(t, e.router, http.MethodPost, "/api/v1/users/delete", e.admin.token,
		map[string]any{"ids": []int64{u1, u2}})
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, map[string]any{}, body["data"])

	_, userBody = doJSON(t, e.router, http.MethodGet, "/api/v1/users?page_index=1&page_size=100", e.admin.token, nil)
	for _, u := range userBody["data"].(map[string]any)["list"].([]any) {
		id := int64(u.(map[string]any)["id"].(float64))
		assert.NotEqual(t, u1, id, "u1 deleted")
		assert.NotEqual(t, u2, id, "u2 deleted")
	}
}

// --- pagination ---

func caseDepartmentPagination(t *testing.T, e *testEnv) {
	create := func(username string, deptID *int64) {
		t.Helper()
		body := map[string]any{
			"username": username,
			"password": "dept_pass_123",
			"roles":    []int64{},
		}
		if deptID != nil {
			body["department_id"] = *deptID
		}
		status, resp := doJSON(t, e.router, http.MethodPost, "/api/v1/users", e.admin.token, body)
		require.Equal(t, http.StatusOK, status, "create %s", username)
		_ = resp
	}

	// Two users in the seeded department, one user without any department.
	// Names are builder-prefixed so both wirings can create on the shared
	// container (these users persist; the matrix case cleans up its own).
	create(e.prefix+"_dept_user_1", &e.deptID)
	create(e.prefix+"_dept_user_2", &e.deptID)
	create(e.prefix+"_nodept_user_1", nil)

	// Filtered pagination: page_size=1 yields one row, total = 2.
	status, body := doJSON(t, e.router,
		http.MethodGet,
		fmt.Sprintf("/api/v1/users?department_id=%d&page_index=1&page_size=1", e.deptID),
		e.admin.token, nil)
	require.Equal(t, http.StatusOK, status)
	data := body["data"].(map[string]any)
	list := data["list"].([]any)
	assert.Len(t, list, 1, "page_size=1")
	assert.Equal(t, 2.0, data["total"], "total counts only the department members")
	first := list[0].(map[string]any)
	dept := first["department"].(map[string]any)
	assert.Equal(t, float64(e.deptID), dept["id"], "filtered row belongs to the department")

	// Unfiltered: the department-less user has explicit JSON null department.
	status, body = doJSON(t, e.router, http.MethodGet, "/api/v1/users?page_index=1&page_size=100", e.admin.token, nil)
	require.Equal(t, http.StatusOK, status)
	seen := false
	for _, u := range body["data"].(map[string]any)["list"].([]any) {
		item := u.(map[string]any)
		if item["username"] == e.prefix+"_nodept_user_1" {
			seen = true
			val, present := item["department"]
			assert.True(t, present, "department field MUST be present")
			assert.Nil(t, val, "department MUST be JSON null without membership")
		}
	}
	assert.True(t, seen, "nodept_user_1 listed")
}

// caseWorkflow is the Checkpoint A workflow/idempotency case from
// contracts/http-api-compatibility.md: header validation, replay, conflict,
// running/awaiting/expired continuation and the exact workflow codes with
// Retry-After presence/absence. The legacy wiring predates the idempotency
// additions (contract: the frozen pre-004 baseline is not an eligible
// rollback binary for this feature): it ignores unknown headers and treats
// every submission as an independent operation, which this case
// characterizes. The BFF wiring enforces the full contract. The Retry-After
// default/clamp matrix and the never-401/403 rule for dependency/workflow
// errors are asserted in respond_test.go against the same errorMapping/
// writeError every wiring uses.
func caseWorkflow(t *testing.T, e *testEnv) {
	key := "8f9703d0-04a7-4d4a-a75a-8807eb733961"

	if e.prefix == "legacy" {
		// Characterization: no idempotency middleware, no workflow saga.
		// The same logical submission with the same key is a fresh
		// independent operation — the duplicate create hits username
		// uniqueness, and no Retry-After ever appears.
		body := map[string]any{"username": e.prefix + "_wf_legacy", "password": "password_123", "roles": []int64{}}
		status, headers, resp := doJSONKey(t, e.router, http.MethodPost, "/api/v1/users", e.admin.token, key, body)
		require.Equal(t, http.StatusOK, status, "legacy first create: %v", resp)
		assert.Empty(t, headers.Get("Retry-After"))
		status, headers, resp = doJSONKey(t, e.router, http.MethodPost, "/api/v1/users", e.admin.token, key, body)
		require.Equal(t, http.StatusConflict, status, "legacy duplicate create: %v", resp)
		requireError(t, status, resp, http.StatusConflict, "NAME_TAKEN")
		assert.Empty(t, headers.Get("Retry-After"), "legacy emits no Retry-After")
		return
	}

	// --- header validation (BFF) ---
	// Invalid keys are 400 AUTH_INVALID_INPUT on both write endpoints and
	// carry no Retry-After; authentication precedes idempotency validation
	// so a revoked actor never learns the header rules.
	for _, bad := range []string{"short", "aaaaaaaaaaaaaaaa ", "aaaaaaaaaaaaaaaa+", strings.Repeat("a", 129)} {
		status, headers, resp := doJSONKey(t, e.router, http.MethodPost, "/api/v1/users", e.admin.token, bad,
			map[string]any{"username": e.prefix + "_wf_bad", "password": "password_123", "roles": []int64{}})
		requireError(t, status, resp, http.StatusBadRequest, "AUTH_INVALID_INPUT")
		assert.Empty(t, headers.Get("Retry-After"))
	}
	status, headers, resp := doJSONKey(t, e.router, http.MethodPost, "/api/v1/users/delete", e.admin.token, "bad",
		map[string]any{"ids": []int64{1}})
	requireError(t, status, resp, http.StatusBadRequest, "AUTH_INVALID_INPUT")
	assert.Empty(t, headers.Get("Retry-After"))

	// --- replay, conflict (BFF) ---
	username := e.prefix + "_wf_user"
	createBody := map[string]any{"username": username, "password": "password_123", "roles": []int64{}}
	status, headers, resp = doJSONKey(t, e.router, http.MethodPost, "/api/v1/users", e.admin.token, key, createBody)
	require.Equal(t, http.StatusOK, status, "create: %v", resp)
	assert.Empty(t, headers.Get("Retry-After"), "success carries no Retry-After")
	status, _, resp = doJSONKey(t, e.router, http.MethodPost, "/api/v1/users", e.admin.token, key, createBody)
	require.Equal(t, http.StatusOK, status, "same-key replay: %v", resp)
	var userCount int64
	require.NoError(t, e.db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM users WHERE username = $1`, username).Scan(&userCount))
	assert.Equal(t, int64(1), userCount, "replay must not duplicate the side effect")
	status, headers, resp = doJSONKey(t, e.router, http.MethodPost, "/api/v1/users", e.admin.token, key,
		map[string]any{"username": e.prefix + "_wf_other", "password": "password_123", "roles": []int64{}})
	requireError(t, status, resp, http.StatusConflict, "IDEMPOTENCY_CONFLICT")
	assert.Empty(t, headers.Get("Retry-After"), "IDEMPOTENCY_CONFLICT MUST NOT carry Retry-After")

	// --- running: 409 OPERATION_IN_PROGRESS + Retry-After in 1–60 (BFF) ---
	actor := e.admin.id
	runningKey := e.prefix + "-running-key-0001" // per-env key, charset-safe
	fp, err := consistency.FingerprintV1("managed_user.create", actor, map[string]any{
		"username":                  e.prefix + "_wf_running",
		"account":                   consistency.OptString(nil),
		"email":                     consistency.OptString(nil),
		"role_ids":                  consistency.SortedIDs(nil),
		"department_id":             consistency.OptInt(nil),
		"password_change_requested": true,
	})
	require.NoError(t, err)
	_, err = e.db.Pool.Exec(context.Background(),
		`INSERT INTO admin_workflows
		 (operation_id, operation_type, idempotency_key, request_fingerprint, actor_user_id,
		  state, current_step, retry_deadline_at, lease_owner, leased_until, claim_token)
		 VALUES ($1, 'managed_user.create', $2, $3, $4, 'running', 'created',
		         now() + interval '1 hour', 'worker-x', now() + interval '30 seconds', $5)`,
		uuid.NewString(), runningKey, fp, actor, uuid.NewString())
	require.NoError(t, err)
	status, headers, resp = doJSONKey(t, e.router, http.MethodPost, "/api/v1/users", e.admin.token, runningKey,
		map[string]any{"username": e.prefix + "_wf_running", "password": "password_123", "roles": []int64{}})
	requireError(t, status, resp, http.StatusConflict, "OPERATION_IN_PROGRESS")
	retrySeconds, parseErr := strconv.Atoi(headers.Get("Retry-After"))
	require.NoError(t, parseErr, "OPERATION_IN_PROGRESS MUST carry integer Retry-After, got %q", headers.Get("Retry-After"))
	assert.GreaterOrEqual(t, retrySeconds, 1)
	assert.LessOrEqual(t, retrySeconds, 60)

	// --- awaiting_client_input: same-key resubmission continues the
	// original workflow before the deadline and after it expires (BFF) ---
	awaitKey := e.prefix + "-await-key-0001"
	fp2, err := consistency.FingerprintV1("managed_user.create", actor, map[string]any{
		"username":                  e.prefix + "_wf_await",
		"account":                   consistency.OptString(nil),
		"email":                     consistency.OptString(nil),
		"role_ids":                  consistency.SortedIDs(nil),
		"department_id":             consistency.OptInt(nil),
		"password_change_requested": true,
	})
	require.NoError(t, err)
	_, err = e.db.Pool.Exec(context.Background(),
		`INSERT INTO admin_workflows
		 (operation_id, operation_type, idempotency_key, request_fingerprint, actor_user_id,
		  state, current_step, retry_deadline_at, client_input_deadline_at)
		 VALUES ($1, 'managed_user.create', $2, $3, $4, 'awaiting_client_input', 'created',
		         now() + interval '1 hour', now() + interval '30 minutes')`,
		uuid.NewString(), awaitKey, fp2, actor)
	require.NoError(t, err)
	status, _, resp = doJSONKey(t, e.router, http.MethodPost, "/api/v1/users", e.admin.token, awaitKey,
		map[string]any{"username": e.prefix + "_wf_await", "password": "password_123", "roles": []int64{}})
	require.Equal(t, http.StatusOK, status, "awaiting resubmission continues: %v", resp)
	var awaitCount int64
	require.NoError(t, e.db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM users WHERE username = $1`, e.prefix+"_wf_await").Scan(&awaitCount))
	assert.Equal(t, int64(1), awaitCount, "the resumed workflow created the user")

	// Expired input deadline: 409 OPERATION_EXPIRED, no Retry-After, no side
	// effect, and the workflow is not resumed by a stale-key retry.
	expiredKey := e.prefix + "-expired-key-0001"
	fp3, err := consistency.FingerprintV1("managed_user.create", actor, map[string]any{
		"username":                  e.prefix + "_wf_expired",
		"account":                   consistency.OptString(nil),
		"email":                     consistency.OptString(nil),
		"role_ids":                  consistency.SortedIDs(nil),
		"department_id":             consistency.OptInt(nil),
		"password_change_requested": true,
	})
	require.NoError(t, err)
	_, err = e.db.Pool.Exec(context.Background(),
		`INSERT INTO admin_workflows
		 (operation_id, operation_type, idempotency_key, request_fingerprint, actor_user_id,
		  state, current_step, retry_deadline_at, client_input_deadline_at)
		 VALUES ($1, 'managed_user.create', $2, $3, $4, 'awaiting_client_input', 'created',
		         now() + interval '1 hour', now() - interval '1 minute')`,
		uuid.NewString(), expiredKey, fp3, actor)
	require.NoError(t, err)
	status, headers, resp = doJSONKey(t, e.router, http.MethodPost, "/api/v1/users", e.admin.token, expiredKey,
		map[string]any{"username": e.prefix + "_wf_expired", "password": "password_123", "roles": []int64{}})
	requireError(t, status, resp, http.StatusConflict, "OPERATION_EXPIRED")
	assert.Empty(t, headers.Get("Retry-After"), "OPERATION_EXPIRED MUST NOT carry Retry-After")
	var expiredCount int64
	require.NoError(t, e.db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM users WHERE username = $1`, e.prefix+"_wf_expired").Scan(&expiredCount))
	assert.Equal(t, int64(0), expiredCount, "expired operation must not create the user")
}
