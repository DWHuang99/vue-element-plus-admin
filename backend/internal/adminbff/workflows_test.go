// Admin BFF create/update/delete workflow contract tests
// (contracts/consistency-and-compensation.md C0–C8 / U0–U7 / D0–D5,
// compensation + reconciliation T055; tasks T052–T055).
//
// The suite runs the real IAM/Organization services and the real workflow
// store on a fresh migrated PostgreSQL (testcontainers) through the public
// constructor adminbff.NewService. Timeout/crash scenarios are injected with
// fake participants that wrap the real services: the real side effect (and
// its receipt) commits, then the fake returns the participant timeout — the
// unknown outcome the saga must resolve before continuing or compensating.
// "Before-commit" fakes return the timeout without touching the real
// service, the proven-non-commit case.
//
// The suite lives in the external test package adminbff_test because it
// imports the adapter (internal/adminbff/postgres), which itself imports
// adminbff.
package adminbff_test

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
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/hdw/vue-element-plus-admin/backend/db/migrations"
	"github.com/hdw/vue-element-plus-admin/backend/internal/adminbff"
	adminbffpostgres "github.com/hdw/vue-element-plus-admin/backend/internal/adminbff/postgres"
	"github.com/hdw/vue-element-plus-admin/backend/internal/consistency"
	"github.com/hdw/vue-element-plus-admin/backend/internal/iam"
	iampostgres "github.com/hdw/vue-element-plus-admin/backend/internal/iam/postgres"
	"github.com/hdw/vue-element-plus-admin/backend/internal/organization"
	orgpostgres "github.com/hdw/vue-element-plus-admin/backend/internal/organization/postgres"
)

// TestMain boots a shared Postgres container once; each test derives its own
// scratch database from it.
func TestMain(m *testing.M) {
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("adminbff_contract"),
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

func freshDB(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	dbname := fmt.Sprintf("adminbff_contract_%d", time.Now().UnixNano())
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
	idx := strings.Index(testConnStr, "/adminbff_contract?")
	if idx == -1 {
		t.Fatal("unexpected connection string shape")
	}
	return testConnStr[:idx] + "/" + dbname + testConnStr[idx+len("/adminbff_contract"):]
}

func migrateToHead(t *testing.T, connStr string) {
	t.Helper()
	source, err := iofs.New(migrations.FS, ".")
	require.NoError(t, err)
	m, err := migrate.NewWithSourceInstance("iofs", source, connStr)
	require.NoError(t, err)
	t.Cleanup(func() { m.Close() })
	require.NoError(t, m.Up(), "migrate to head")
}

// --- harness -------------------------------------------------------------------

type harness struct {
	pool *pgxpool.Pool
	db   *pgx.Conn
	svc  *adminbff.Service
	work adminbff.WorkflowStore
}

// newHarness wires the service under test with the real participants.
func newHarness(t *testing.T) *harness {
	return newHarnessWithParts(t, nil, nil, nil, nil, nil)
}

// newHarnessWithParts wires the service with participant overrides; nil parts
// use the real services. Overrides wrap the real services (the real side
// effect still commits) unless the fake's mode says otherwise.
func newHarnessWithParts(t *testing.T, managed iam.ManagedUserService, iamReceipts iam.ReceiptResolver, membership organization.MembershipService, orgReceipts organization.ReceiptResolver, identity iam.IdentityService) *harness {
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

	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	iamSvc := iam.NewService(iampostgres.NewStore(pool), discard)
	orgSvc := organization.NewService(orgpostgres.NewStore(pool), discard)
	// Fakes wrap the real services (the side effect still commits through the
	// embedded interface); backfill the embedding so a nil real participant
	// never slips through.
	if f, ok := managed.(*fakeManaged); ok {
		f.ManagedUserService = iamSvc
	}
	if f, ok := iamReceipts.(*fakeReceipts); ok {
		f.ReceiptResolver = iamSvc
	}
	if f, ok := membership.(*fakeMembership); ok {
		f.MembershipService = orgSvc
	}
	if f, ok := orgReceipts.(*fakeOrgReceipts); ok {
		f.ReceiptResolver = orgSvc
	}
	if f, ok := identity.(*fakeIdentity); ok {
		f.IdentityService = iamSvc
	}
	if managed == nil {
		managed = iamSvc
	}
	if iamReceipts == nil {
		iamReceipts = iamSvc
	}
	if membership == nil {
		membership = orgSvc
	}
	if orgReceipts == nil {
		orgReceipts = orgSvc
	}
	if identity == nil {
		identity = iamSvc
	}
	work := adminbffpostgres.NewStore(pool)
	svc := adminbff.NewService(adminbff.IAMParticipants{
		Auth:     iamSvc,
		Identity: identity,
		Roles:    iamSvc,
		Managed:  managed,
		Receipts: iamReceipts,
	}, adminbff.OrganizationParticipants{
		Departments: orgSvc,
		Membership:  membership,
		Receipts:    orgReceipts,
		Inbox:       orgSvc,
	}, work, testLogger())
	return &harness{pool: pool, db: db, svc: svc, work: work}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// --- seeding -------------------------------------------------------------------

// seedRole creates a role directly on the store and returns its id.
func (h *harness) seedRole(t *testing.T, name, code string) int64 {
	t.Helper()
	role, err := iampostgres.NewStore(h.pool).CreateRole(context.Background(), name, code)
	require.NoError(t, err)
	return role.ID
}

// seedDepartment creates a department directly and returns its id.
func (h *harness) seedDepartment(t *testing.T, name string) int64 {
	t.Helper()
	var id int64
	require.NoError(t, h.db.QueryRow(context.Background(),
		"INSERT INTO departments (name, parent_id) VALUES ($1, NULL) RETURNING id", name).Scan(&id))
	return id
}

// seedActor registers an active user holding the built-in admin role
// (000005 grants the full permission catalog to admin/super_admin), so the
// saga's users.write reauthorization checks pass.
func (h *harness) seedActor(t *testing.T, username string) int64 {
	t.Helper()
	_, err := iam.NewService(iampostgres.NewStore(h.pool), slog.New(slog.NewTextHandler(io.Discard, nil))).Register(context.Background(), username, "pw-123456",
		iam.OperationContext{OperationID: "00000000-0000-0000-0000-000000000001", CorrelationID: "seed-actor"})
	require.NoError(t, err)
	id := h.userIDByUsername(t, username)
	_, err = h.db.Exec(context.Background(), `
		INSERT INTO user_roles (user_id, role_id)
		SELECT $1, id FROM roles WHERE code = 'admin'`, id)
	require.NoError(t, err)
	return id
}

// --- db-truth helpers -----------------------------------------------------------

func (h *harness) userIDByUsername(t *testing.T, username string) int64 {
	t.Helper()
	var id int64
	require.NoError(t, h.db.QueryRow(context.Background(),
		"SELECT id FROM users WHERE username = $1", username).Scan(&id))
	return id
}

func (h *harness) countUsersWithUsername(t *testing.T, username string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, h.db.QueryRow(context.Background(),
		"SELECT count(*) FROM users WHERE username = $1", username).Scan(&n))
	return n
}

func (h *harness) lifecycleState(t *testing.T, userID int64) string {
	t.Helper()
	var state string
	require.NoError(t, h.db.QueryRow(context.Background(),
		"SELECT lifecycle_state FROM users WHERE id = $1", userID).Scan(&state))
	return state
}

// membershipDepartment returns the membership department id (nil when the row
// is a NULL-department tombstone) and whether a membership state row exists.
func (h *harness) membershipDepartment(t *testing.T, userID int64) (*int64, bool) {
	t.Helper()
	var dept *int64
	err := h.db.QueryRow(context.Background(),
		"SELECT department_id FROM organization_user_departments WHERE user_id = $1", userID).Scan(&dept)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false
	}
	require.NoError(t, err)
	return dept, true
}

func (h *harness) countReceipts(t *testing.T, table, operationID string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, h.db.QueryRow(context.Background(),
		"SELECT count(*) FROM "+table+" WHERE operation_id = $1", operationID).Scan(&n))
	return n
}

func (h *harness) countReceiptsByCommand(t *testing.T, table, operationID, command string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, h.db.QueryRow(context.Background(),
		"SELECT count(*) FROM "+table+" WHERE operation_id = $1 AND command_name = $2", operationID, command).Scan(&n))
	return n
}

func (h *harness) workflowByKey(t *testing.T, actorID int64, key string) adminbff.Workflow {
	t.Helper()
	w, err := h.work.GetWorkflowByIdempotencyKey(context.Background(), adminbff.OperationTypeCreateUser, actorID, key)
	require.NoError(t, err)
	require.NotNil(t, w, "workflow for key %q must exist", key)
	return *w
}

// expireLease rewrites leased_until into the past so a running/compensating
// row becomes claimable again (time control via SQL, as in T051).
func (h *harness) expireLease(t *testing.T, operationID string) {
	t.Helper()
	_, err := h.db.Exec(context.Background(),
		"UPDATE admin_workflows SET leased_until = now() - interval '1 minute' WHERE operation_id = $1", operationID)
	require.NoError(t, err)
}

func (h *harness) expireClientInput(t *testing.T, operationID string) {
	t.Helper()
	_, err := h.db.Exec(context.Background(),
		"UPDATE admin_workflows SET client_input_deadline_at = now() - interval '1 minute' WHERE operation_id = $1", operationID)
	require.NoError(t, err)
}

// --- request builders ------------------------------------------------------------

func op(actorID int64, key string) adminbff.OperationContext {
	return adminbff.OperationContext{
		IdempotencyKey: &key,
		ActorUserID:    actorID,
		CorrelationID:  "t052-contract",
	}
}

func createParams(username string, roleIDs []int64, departmentID *int64) adminbff.CreateUserParams {
	return adminbff.CreateUserParams{
		Username:     username,
		Password:     "Secret-123!",
		RoleIDs:      roleIDs,
		DepartmentID: departmentID,
	}
}

// --- timeout-injecting fakes ------------------------------------------------------

// fakeManaged wraps iam.ManagedUserService: the real side effect commits,
// then the configured step returns the participant timeout (unknown outcome
// the saga must resolve). The before-commit variants return the timeout
// without touching the real service — the proven-non-commit case.
type fakeManaged struct {
	iam.ManagedUserService
	failCreateAfterCommit    bool
	failCreateBeforeCommit   bool
	failActivateAfterCommit  bool
	failActivateBeforeCommit bool
	failUpdateAfterCommit    bool
	failUpdateBeforeCommit   bool
	failDeleteBeforeCommit   int // D3: fail the next N attempts without committing
	failDeleteAfterCommit    int // D3: commit the real deletion, then time out
	failDeleteReject         bool
	skipCompensate           bool
}

func (f *fakeManaged) CreateProvisioningUser(ctx context.Context, requestCtx iam.OperationContext, username string, account, email *string, password string, roleIDs []int64) (iam.CreateUserResult, error) {
	if f.failCreateBeforeCommit {
		return iam.CreateUserResult{}, iam.ErrTimeout
	}
	res, err := f.ManagedUserService.CreateProvisioningUser(ctx, requestCtx, username, account, email, password, roleIDs)
	if err == nil && f.failCreateAfterCommit {
		return iam.CreateUserResult{}, iam.ErrTimeout
	}
	return res, err
}

func (f *fakeManaged) ActivateUser(ctx context.Context, requestCtx iam.OperationContext, userID, expectedVersion int64) (int64, error) {
	if f.failActivateBeforeCommit {
		return 0, iam.ErrTimeout
	}
	v, err := f.ManagedUserService.ActivateUser(ctx, requestCtx, userID, expectedVersion)
	if err == nil && f.failActivateAfterCommit {
		return 0, iam.ErrTimeout
	}
	return v, err
}

func (f *fakeManaged) UpdateManagedUser(ctx context.Context, requestCtx iam.OperationContext, userID, expectedVersion int64, username string, account, email *string, password *string, roleIDs []int64) (int64, error) {
	if f.failUpdateBeforeCommit {
		return 0, iam.ErrTimeout
	}
	v, err := f.ManagedUserService.UpdateManagedUser(ctx, requestCtx, userID, expectedVersion, username, account, email, password, roleIDs)
	if err == nil && f.failUpdateAfterCommit {
		return 0, iam.ErrTimeout
	}
	return v, err
}

// CompensateProvisioningUser override: skipCompensate answers success without
// touching the real store — the proven-compensation-committed case. The real
// CAS-delete semantics are covered by the IAM lifecycle suite (T049/T050).
func (f *fakeManaged) CompensateProvisioningUser(ctx context.Context, requestCtx iam.OperationContext, userID, expectedVersion int64) error {
	if f.skipCompensate {
		return nil
	}
	return f.ManagedUserService.CompensateProvisioningUser(ctx, requestCtx, userID, expectedVersion)
}

// DeleteUsers failure injection: the D3 modes. The counters fail the next N
// calls (0 = never) — the before-commit mode times out without touching the
// real service, the after-commit mode commits the real deletion first.
// failDeleteReject returns a proven rejection without any side effect.
func (f *fakeManaged) DeleteUsers(ctx context.Context, requestCtx iam.OperationContext, targets []iam.DeleteTarget) ([]iam.BatchDeleteResultItem, error) {
	if f.failDeleteReject {
		return nil, iam.ErrUserNotFound
	}
	if f.failDeleteBeforeCommit > 0 {
		f.failDeleteBeforeCommit--
		return nil, iam.ErrTimeout
	}
	results, err := f.ManagedUserService.DeleteUsers(ctx, requestCtx, targets)
	if err == nil && f.failDeleteAfterCommit > 0 {
		f.failDeleteAfterCommit--
		return nil, iam.ErrTimeout
	}
	return results, err
}

// fakeMembership wraps organization.MembershipService with the same modes.
// failRead makes the U3 membership READ return a retryable dependency error
// (no write is attempted — the unknown-outcome-park at iam_validated case).
type fakeMembership struct {
	organization.MembershipService
	failSetAfterCommit   bool
	failSetBeforeCommit  bool
	failClearAfterCommit bool
	failRead             bool
}

func (f *fakeMembership) GetUserDepartment(ctx context.Context, userID int64) (*organization.MembershipState, error) {
	if f.failRead {
		return nil, organization.ErrTimeout
	}
	return f.MembershipService.GetUserDepartment(ctx, userID)
}

func (f *fakeMembership) SetUserDepartment(ctx context.Context, requestCtx organization.OperationContext, userID, departmentID int64, expected *int64) (organization.MembershipMutationResult, error) {
	if f.failSetBeforeCommit {
		return organization.MembershipMutationResult{}, organization.ErrTimeout
	}
	res, err := f.MembershipService.SetUserDepartment(ctx, requestCtx, userID, departmentID, expected)
	if err == nil && f.failSetAfterCommit {
		return organization.MembershipMutationResult{}, organization.ErrTimeout
	}
	return res, err
}

func (f *fakeMembership) ClearUserDepartment(ctx context.Context, requestCtx organization.OperationContext, userID int64, expected *int64) (organization.MembershipMutationResult, error) {
	res, err := f.MembershipService.ClearUserDepartment(ctx, requestCtx, userID, expected)
	if err == nil && f.failClearAfterCommit {
		return organization.MembershipMutationResult{}, organization.ErrTimeout
	}
	return res, err
}

// fakeReceipts fails the next resolve — the unresolved outcome that must
// leave the workflow running for the resume machine. forceCommitted answers
// "committed" without touching the real store — the proven-receipt case the
// resume/recovery machines finalize on.
type fakeReceipts struct {
	iam.ReceiptResolver
	failNext       bool
	forceCommitted bool
}

func (f *fakeReceipts) ResolveCommand(ctx context.Context, operationID, commandName, expectedFingerprint string) (*iam.IAMCommandReceipt, error) {
	if f.failNext {
		f.failNext = false
		return nil, iam.ErrUnavailable
	}
	if f.forceCommitted {
		return &iam.IAMCommandReceipt{
			OperationID:        operationID,
			CommandName:        commandName,
			RequestFingerprint: expectedFingerprint,
			Status:             iam.CommandSucceeded,
		}, nil
	}
	return f.ReceiptResolver.ResolveCommand(ctx, operationID, commandName, expectedFingerprint)
}

type fakeOrgReceipts struct {
	organization.ReceiptResolver
	failNext bool
}

func (f *fakeOrgReceipts) ResolveCommand(ctx context.Context, operationID, commandName, expectedFingerprint string) (*organization.OrganizationCommandReceipt, error) {
	if f.failNext {
		f.failNext = false
		return nil, organization.ErrUnavailable
	}
	return f.ReceiptResolver.ResolveCommand(ctx, operationID, commandName, expectedFingerprint)
}

// fakeIdentity grants the first N users.write checks, then denies — the
// mid-saga revocation case.
type fakeIdentity struct {
	iam.IdentityService
	grantedChecks int
	calls         int
}

func (f *fakeIdentity) HasPermission(ctx context.Context, userID int64, permissionCode string) (bool, error) {
	f.calls++
	if f.calls > f.grantedChecks {
		return false, nil
	}
	return f.IdentityService.HasPermission(ctx, userID, permissionCode)
}

// --- C0–C8 happy paths -----------------------------------------------------------

func TestCreateUser_SuccessWithDepartment(t *testing.T) {
	h := newHarness(t)
	actor := h.seedActor(t, "actor_ok")
	role := h.seedRole(t, "t052_role", "t052_role_code")
	dept := h.seedDepartment(t, "t052_dept")

	res, err := h.svc.CreateUser(context.Background(), op(actor, "key-1"), createParams("newbie", []int64{role}, &dept))
	require.NoError(t, err)
	require.Positive(t, res.UserID)

	assert.Equal(t, "active", h.lifecycleState(t, res.UserID), "C7 must activate the user")
	gotDept, ok := h.membershipDepartment(t, res.UserID)
	require.True(t, ok, "C5 must assign the membership state row")
	require.NotNil(t, gotDept, "C5 must set the department")
	assert.Equal(t, dept, *gotDept)

	w := h.workflowByKey(t, actor, "key-1")
	assert.Equal(t, adminbff.WorkflowSucceeded, w.State)
	assert.Equal(t, adminbff.StepOrganizationAssigned, w.CurrentStep)
	replayedID, ok := w.Result["user_id"].(float64)
	require.True(t, ok, "succeeded workflow must store the safe user id")
	assert.Equal(t, float64(res.UserID), replayedID)

	// exactly one receipt per committed participant side effect: C3 + C7 on
	// the IAM side, C5 on the Organization side
	assert.Equal(t, int64(2), h.countReceipts(t, "iam_command_receipts", w.OperationID))
	assert.Equal(t, int64(1), h.countReceipts(t, "organization_command_receipts", w.OperationID))
}

func TestCreateUser_SuccessWithoutDepartment(t *testing.T) {
	h := newHarness(t)
	actor := h.seedActor(t, "actor_nodept")
	role := h.seedRole(t, "t052_role2", "t052_role_code2")

	res, err := h.svc.CreateUser(context.Background(), op(actor, "key-n"), createParams("lonely", []int64{role}, nil))
	require.NoError(t, err)
	require.Positive(t, res.UserID)

	assert.Equal(t, "active", h.lifecycleState(t, res.UserID))
	// The 000008 users_insert_bridge trigger gives every new user a
	// NULL-department tombstone row; C5 is skipped for a nil department, so
	// the row stays a tombstone and NO Organization command ever runs.
	dept, exists := h.membershipDepartment(t, res.UserID)
	assert.True(t, exists, "the bridge seeds a membership state row for every user")
	assert.Nil(t, dept, "C5 skipped: the row remains a NULL-department tombstone")

	w := h.workflowByKey(t, actor, "key-n")
	assert.Equal(t, adminbff.WorkflowSucceeded, w.State)
	assert.Equal(t, int64(2), h.countReceipts(t, "iam_command_receipts", w.OperationID), "create + activate only")
	assert.Equal(t, int64(0), h.countReceipts(t, "organization_command_receipts", w.OperationID), "no membership side effect")
}

// --- idempotency ------------------------------------------------------------------

func TestCreateUser_SameKeyReplaysSuccess(t *testing.T) {
	h := newHarness(t)
	actor := h.seedActor(t, "actor_replay")
	role := h.seedRole(t, "t052_replay_role", "t052_replay_code")
	params := createParams("replayer", []int64{role}, nil)

	first, err := h.svc.CreateUser(context.Background(), op(actor, "key-rep"), params)
	require.NoError(t, err)

	second, err := h.svc.CreateUser(context.Background(), op(actor, "key-rep"), params)
	require.NoError(t, err)
	assert.Equal(t, first.UserID, second.UserID, "same key replays the same user")
	assert.Equal(t, int64(1), h.countUsersWithUsername(t, "replayer"))

	w := h.workflowByKey(t, actor, "key-rep")
	assert.Equal(t, int64(2), h.countReceipts(t, "iam_command_receipts", w.OperationID), "no duplicate side effects on replay")
}

// T059 Checkpoint E: 24h retention. The replay evidence of a succeeded
// workflow is NOT tied to the retry window — even after retry_deadline_at
// passes, the same-key replay returns the stored success and never re-runs
// a side effect (the row is evidence, not a retry slot).
func TestCreateUser_SucceededReplayEvidenceRetainedPastRetryDeadline(t *testing.T) {
	h := newHarness(t)
	actor := h.seedActor(t, "actor_ret24h")
	role := h.seedRole(t, "t059_role_ret24h", "t059_role_code_ret24h")
	params := createParams("retention_user", []int64{role}, nil)

	first, err := h.svc.CreateUser(context.Background(), op(actor, "key-ret24h"), params)
	require.NoError(t, err)

	_, err = h.db.Exec(context.Background(),
		"UPDATE admin_workflows SET retry_deadline_at = now() - interval '1 hour', created_at = now() - interval '2 hours' WHERE idempotency_key = $1",
		"key-ret24h")
	require.NoError(t, err)

	second, err := h.svc.CreateUser(context.Background(), op(actor, "key-ret24h"), params)
	require.NoError(t, err)
	assert.Equal(t, first.UserID, second.UserID, "the 24h-old replay evidence is still authoritative")
	assert.Equal(t, int64(1), h.countUsersWithUsername(t, "retention_user"))
	w := h.workflowByKey(t, actor, "key-ret24h")
	// C3 create_user + C7 activate_user — exactly the original two commands,
	// nothing re-issued past the deadline.
	assert.Equal(t, int64(2), h.countReceipts(t, "iam_command_receipts", w.OperationID), "no side effect is re-run past the deadline either")
}

func TestCreateUser_SameKeyDifferentRequestConflicts(t *testing.T) {
	h := newHarness(t)
	actor := h.seedActor(t, "actor_conflict")
	role := h.seedRole(t, "t052_conflict_role", "t052_conflict_code")

	_, err := h.svc.CreateUser(context.Background(), op(actor, "key-c"), createParams("original", []int64{role}, nil))
	require.NoError(t, err)

	_, err = h.svc.CreateUser(context.Background(), op(actor, "key-c"), createParams("intruder", []int64{role}, nil))
	require.ErrorIs(t, err, adminbff.ErrIdempotencyConflict)
	assert.Equal(t, int64(0), h.countUsersWithUsername(t, "intruder"))
}

func TestCreateUser_DifferentKeyCreatesSecondUser(t *testing.T) {
	h := newHarness(t)
	actor := h.seedActor(t, "actor_two")
	role := h.seedRole(t, "t052_two_role", "t052_two_code")

	first, err := h.svc.CreateUser(context.Background(), op(actor, "key-a"), createParams("user_a", []int64{role}, nil))
	require.NoError(t, err)
	second, err := h.svc.CreateUser(context.Background(), op(actor, "key-b"), createParams("user_b", []int64{role}, nil))
	require.NoError(t, err)
	assert.NotEqual(t, first.UserID, second.UserID)
}

// --- C1/C2 validation rejections ---------------------------------------------------

func TestCreateUser_InvalidRoleRejected(t *testing.T) {
	h := newHarness(t)
	actor := h.seedActor(t, "actor_badrole")

	_, err := h.svc.CreateUser(context.Background(), op(actor, "key-br"), createParams("badrole_user", []int64{999999}, nil))
	require.ErrorIs(t, err, adminbff.ErrInvalidInput)
	assert.Equal(t, int64(0), h.countUsersWithUsername(t, "badrole_user"))

	// same-key replay returns the stored rejection
	_, err = h.svc.CreateUser(context.Background(), op(actor, "key-br"), createParams("badrole_user", []int64{999999}, nil))
	require.ErrorIs(t, err, adminbff.ErrInvalidInput)
	assert.Equal(t, adminbff.WorkflowRejected, h.workflowByKey(t, actor, "key-br").State)
}

func TestCreateUser_InvalidDepartmentRejected(t *testing.T) {
	h := newHarness(t)
	actor := h.seedActor(t, "actor_baddept")
	role := h.seedRole(t, "t052_bd_role", "t052_bd_code")
	missing := int64(999999)

	_, err := h.svc.CreateUser(context.Background(), op(actor, "key-bd"), createParams("baddept_user", []int64{role}, &missing))
	require.ErrorIs(t, err, adminbff.ErrInvalidInput)
	assert.Equal(t, int64(0), h.countUsersWithUsername(t, "baddept_user"))
	assert.Equal(t, adminbff.WorkflowRejected, h.workflowByKey(t, actor, "key-bd").State)
}

func TestCreateUser_UsernameTakenRejected(t *testing.T) {
	h := newHarness(t)
	actor := h.seedActor(t, "actor_taken")
	role := h.seedRole(t, "t052_taken_role", "t052_taken_code")

	_, err := h.svc.CreateUser(context.Background(), op(actor, "key-t1"), createParams("clash", []int64{role}, nil))
	require.NoError(t, err)

	_, err = h.svc.CreateUser(context.Background(), op(actor, "key-t2"), createParams("clash", []int64{role}, nil))
	require.ErrorIs(t, err, adminbff.ErrInvalidInput, "username uniqueness is enforced at C3")
	assert.Equal(t, adminbff.WorkflowRejected, h.workflowByKey(t, actor, "key-t2").State)
	assert.Equal(t, int64(1), h.countUsersWithUsername(t, "clash"))
}

// --- C3 resolve-first and parking ---------------------------------------------------

func TestCreateUser_C3TimeoutCommittedResolvesAndContinues(t *testing.T) {
	managed := &fakeManaged{}
	h := newHarnessWithParts(t, managed, nil, nil, nil, nil)
	actor := h.seedActor(t, "actor_c3c")
	role := h.seedRole(t, "t052_c3c_role", "t052_c3c_code")
	dept := h.seedDepartment(t, "t052_c3c_dept")

	managed.failCreateAfterCommit = true // side effect + receipt committed, then timeout
	res, err := h.svc.CreateUser(context.Background(), op(actor, "key-c3c"), createParams("c3_committed", []int64{role}, &dept))
	require.NoError(t, err, "a resolved committed C3 must continue the saga")
	assert.Equal(t, "active", h.lifecycleState(t, res.UserID))
	assert.Equal(t, adminbff.WorkflowSucceeded, h.workflowByKey(t, actor, "key-c3c").State)
}

func TestCreateUser_C3TimeoutAbsentParksAwaitingThenSameKeyRetrySucceeds(t *testing.T) {
	managed := &fakeManaged{}
	h := newHarnessWithParts(t, managed, nil, nil, nil, nil)
	actor := h.seedActor(t, "actor_c3p")
	role := h.seedRole(t, "t052_c3p_role", "t052_c3p_code")

	managed.failCreateBeforeCommit = true // proven non-commit, credential lost
	_, err := h.svc.CreateUser(context.Background(), op(actor, "key-c3p"), createParams("parked_user", []int64{role}, nil))
	require.ErrorIs(t, err, adminbff.ErrDependencyTimeout)

	w := h.workflowByKey(t, actor, "key-c3p")
	assert.Equal(t, adminbff.WorkflowAwaitingClientInput, w.State, "non-committed credential parks awaiting input")
	assert.Equal(t, adminbff.StepCreated, w.CurrentStep)
	assert.Nil(t, w.SubjectUserID)
	assert.Nil(t, w.LeaseOwner, "waiting parks clear the lease")
	require.NotNil(t, w.ClientInputDeadlineAt)
	assert.WithinDuration(t, time.Now().Add(24*time.Hour), *w.ClientInputDeadlineAt, time.Minute,
		"immutable deadline = creation + 24h")
	assert.Equal(t, int64(0), h.countUsersWithUsername(t, "parked_user"))

	// Same-key retry with a corrected password (same safe request — the
	// password is excluded from the fingerprint) continues the parked C3.
	managed.failCreateBeforeCommit = false
	params := createParams("parked_user", []int64{role}, nil)
	params.Password = "Corrected-456!"
	res, err := h.svc.CreateUser(context.Background(), op(actor, "key-c3p"), params)
	require.NoError(t, err)
	assert.Equal(t, "active", h.lifecycleState(t, res.UserID))
	assert.Equal(t, adminbff.WorkflowSucceeded, h.workflowByKey(t, actor, "key-c3p").State)
	assert.Equal(t, int64(1), h.countUsersWithUsername(t, "parked_user"))
}

// T059 Checkpoint E: before the input deadline, a same-key authorized retry
// supplies the credential in memory and burns NO forward attempt — each
// parked retry claims via ClaimAwaitingClientInput (which never increments
// attempt_count) and parks again at the same boundary; only the original
// forward claim counted. The 24h client deadline, not the 10-attempt budget,
// bounds client retries.
func TestCreateUser_AwaitingRetryBeforeDeadlineBurnsNoForwardAttempt(t *testing.T) {
	managed := &fakeManaged{failCreateBeforeCommit: true} // C3 provably non-commit -> park
	h := newHarnessWithParts(t, managed, nil, nil, nil, nil)
	actor := h.seedActor(t, "actor_noburn")
	role := h.seedRole(t, "t059_role_noburn", "t059_role_code_noburn")

	_, err := h.svc.CreateUser(context.Background(), op(actor, "key-noburn"), createParams("noburn_user", []int64{role}, nil))
	require.ErrorIs(t, err, adminbff.ErrDependencyTimeout)
	w := h.workflowByKey(t, actor, "key-noburn")
	assert.Equal(t, adminbff.WorkflowAwaitingClientInput, w.State)

	// Same-key retry, deadline still open, but C3 fails again: the retry
	// re-attempts and parks again without consuming the forward budget.
	_, err = h.svc.CreateUser(context.Background(), op(actor, "key-noburn"), createParams("noburn_user", []int64{role}, nil))
	require.ErrorIs(t, err, adminbff.ErrDependencyTimeout)
	w = h.workflowByKey(t, actor, "key-noburn")
	assert.Equal(t, adminbff.WorkflowAwaitingClientInput, w.State)
	var attempts int64
	require.NoError(t, h.db.QueryRow(context.Background(),
		"SELECT attempt_count FROM admin_workflows WHERE operation_id = $1", w.OperationID).Scan(&attempts))
	assert.Equal(t, int64(1), attempts, "the awaiting retry parked without burning a forward attempt")
	assert.Equal(t, int64(0), h.countUsersWithUsername(t, "noburn_user"))

	// A corrected attempt (same key, same safe request — the password is
	// outside the fingerprint) finally succeeds; the budget is unchanged by
	// the parked retries.
	managed.failCreateBeforeCommit = false
	_, err = h.svc.CreateUser(context.Background(), op(actor, "key-noburn"), createParams("noburn_user", []int64{role}, nil))
	require.NoError(t, err)
	assert.Equal(t, int64(1), h.countUsersWithUsername(t, "noburn_user"))
	var after int64
	require.NoError(t, h.db.QueryRow(context.Background(),
		"SELECT attempt_count FROM admin_workflows WHERE operation_id = $1", w.OperationID).Scan(&after))
	assert.Equal(t, int64(1), after, "the successful continuation still holds the single forward claim")
}

func TestCreateUser_AwaitingRetryAfterDeadlineExpired(t *testing.T) {
	managed := &fakeManaged{}
	h := newHarnessWithParts(t, managed, nil, nil, nil, nil)
	actor := h.seedActor(t, "actor_c3late")
	role := h.seedRole(t, "t052_late_role", "t052_late_code")

	managed.failCreateBeforeCommit = true
	_, err := h.svc.CreateUser(context.Background(), op(actor, "key-late"), createParams("late_user", []int64{role}, nil))
	require.ErrorIs(t, err, adminbff.ErrDependencyTimeout)
	w := h.workflowByKey(t, actor, "key-late")

	h.expireClientInput(t, w.OperationID)
	managed.failCreateBeforeCommit = false
	_, err = h.svc.CreateUser(context.Background(), op(actor, "key-late"), createParams("late_user", []int64{role}, nil))
	require.ErrorIs(t, err, adminbff.ErrOperationExpired, "after the deadline only the expiry finalizer may continue")
}

func TestCreateUser_ExpiredInputFinalizedRejected(t *testing.T) {
	managed := &fakeManaged{}
	h := newHarnessWithParts(t, managed, nil, nil, nil, nil)
	actor := h.seedActor(t, "actor_c3exp")
	role := h.seedRole(t, "t052_exp_role", "t052_exp_code")

	managed.failCreateBeforeCommit = true
	_, err := h.svc.CreateUser(context.Background(), op(actor, "key-exp"), createParams("expired_user", []int64{role}, nil))
	require.ErrorIs(t, err, adminbff.ErrDependencyTimeout)
	w := h.workflowByKey(t, actor, "key-exp")

	h.expireClientInput(t, w.OperationID)
	outcome, err := h.svc.ResumeWorkflow(context.Background(), w.OperationID)
	require.NoError(t, err)
	require.True(t, outcome.Terminal)
	assert.Equal(t, adminbff.WorkflowRejected, outcome.State)

	rejected := h.workflowByKey(t, actor, "key-exp")
	assert.Equal(t, adminbff.WorkflowRejected, rejected.State)
	assert.Equal(t, int64(0), h.countUsersWithUsername(t, "expired_user"), "no orphan user")

	// The finalizer outcome is replayable for the client and idempotent for
	// the worker.
	_, err = h.svc.CreateUser(context.Background(), op(actor, "key-exp"), createParams("expired_user", []int64{role}, nil))
	require.ErrorIs(t, err, adminbff.ErrOperationExpired)
	outcome, err = h.svc.ResumeWorkflow(context.Background(), w.OperationID)
	require.NoError(t, err)
	require.True(t, outcome.Terminal)
	assert.Equal(t, adminbff.WorkflowRejected, outcome.State)
}

// --- C5/C7 resolve-first, compensation and reauthorization --------------------------

func TestCreateUser_C5TimeoutCommittedResolvesAndContinues(t *testing.T) {
	membership := &fakeMembership{}
	h := newHarnessWithParts(t, nil, nil, membership, nil, nil)
	actor := h.seedActor(t, "actor_c5c")
	role := h.seedRole(t, "t052_c5c_role", "t052_c5c_code")
	dept := h.seedDepartment(t, "t052_c5c_dept")

	membership.failSetAfterCommit = true // membership + receipt committed, then timeout
	res, err := h.svc.CreateUser(context.Background(), op(actor, "key-c5c"), createParams("c5_committed", []int64{role}, &dept))
	require.NoError(t, err)
	assert.Equal(t, "active", h.lifecycleState(t, res.UserID))
	gotDept, _ := h.membershipDepartment(t, res.UserID)
	require.NotNil(t, gotDept, "C5 must set the department")
	assert.Equal(t, dept, *gotDept)
	assert.Equal(t, adminbff.WorkflowSucceeded, h.workflowByKey(t, actor, "key-c5c").State)
}

func TestCreateUser_C5ProvenNonCommitCompensatesProvisioningUser(t *testing.T) {
	membership := &fakeMembership{}
	h := newHarnessWithParts(t, nil, nil, membership, nil, nil)
	actor := h.seedActor(t, "actor_c5nc")
	role := h.seedRole(t, "t052_c5nc_role", "t052_c5nc_code")
	dept := h.seedDepartment(t, "t052_c5nc_dept")

	membership.failSetBeforeCommit = true // no membership side effect at all
	_, err := h.svc.CreateUser(context.Background(), op(actor, "key-c5nc"), createParams("c5_nocommit", []int64{role}, &dept))
	require.ErrorIs(t, err, adminbff.ErrDependencyTimeout)

	w := h.workflowByKey(t, actor, "key-c5nc")
	assert.Equal(t, adminbff.WorkflowRejected, w.State)
	assert.Equal(t, int64(0), h.countUsersWithUsername(t, "c5_nocommit"), "the provisioning user must be compensated away")
	// The proven non-commit left no Organization side effect: zero membership
	// receipts; the IAM side committed create + compensate.
	assert.Equal(t, int64(0), h.countReceipts(t, "organization_command_receipts", w.OperationID))
	assert.Equal(t, int64(2), h.countReceipts(t, "iam_command_receipts", w.OperationID))
	assert.Equal(t, int64(1), h.countReceiptsByCommand(t, "iam_command_receipts", w.OperationID, "create_provisioning_user"))
	assert.Equal(t, int64(1), h.countReceiptsByCommand(t, "iam_command_receipts", w.OperationID, "compensate_provisioning_user"))
}

func TestCreateUser_C7TimeoutCommittedSucceeds(t *testing.T) {
	managed := &fakeManaged{}
	h := newHarnessWithParts(t, managed, nil, nil, nil, nil)
	actor := h.seedActor(t, "actor_c7c")
	role := h.seedRole(t, "t052_c7c_role", "t052_c7c_code")

	managed.failActivateAfterCommit = true // activation + receipt committed, then timeout
	res, err := h.svc.CreateUser(context.Background(), op(actor, "key-c7c"), createParams("c7_committed", []int64{role}, nil))
	require.NoError(t, err)
	assert.Equal(t, "active", h.lifecycleState(t, res.UserID))
	assert.Equal(t, adminbff.WorkflowSucceeded, h.workflowByKey(t, actor, "key-c7c").State)
}

func TestCreateUser_C7ProvenNonCommitRestoresAndCompensates(t *testing.T) {
	managed := &fakeManaged{}
	h := newHarnessWithParts(t, managed, nil, nil, nil, nil)
	actor := h.seedActor(t, "actor_c7nc")
	role := h.seedRole(t, "t052_c7nc_role", "t052_c7nc_code")
	dept := h.seedDepartment(t, "t052_c7nc_dept")

	managed.failActivateBeforeCommit = true // no activation side effect
	_, err := h.svc.CreateUser(context.Background(), op(actor, "key-c7nc"), createParams("c7_nocommit", []int64{role}, &dept))
	require.ErrorIs(t, err, adminbff.ErrDependencyTimeout)

	w := h.workflowByKey(t, actor, "key-c7nc")
	assert.Equal(t, adminbff.WorkflowRejected, w.State)
	assert.Equal(t, int64(0), h.countUsersWithUsername(t, "c7_nocommit"), "provisioning user compensated")
	// The compensation sequence is observable in the evidence: the applied
	// membership was restored (Organization set + restore receipts), then the
	// provisioning user compensated (IAM create + compensate; the failed
	// activation never committed).
	assert.Equal(t, int64(2), h.countReceipts(t, "organization_command_receipts", w.OperationID))
	assert.Equal(t, int64(1), h.countReceiptsByCommand(t, "organization_command_receipts", w.OperationID, "set_user_department"))
	assert.Equal(t, int64(1), h.countReceiptsByCommand(t, "organization_command_receipts", w.OperationID, "restore_user_department"))
	assert.Equal(t, int64(2), h.countReceipts(t, "iam_command_receipts", w.OperationID))
	assert.Equal(t, int64(1), h.countReceiptsByCommand(t, "iam_command_receipts", w.OperationID, "compensate_provisioning_user"))
}

func TestCreateUser_ReauthRevokedAtC5CompensatesProvisioningOnly(t *testing.T) {
	identity := &fakeIdentity{grantedChecks: 1} // C3 granted, C5 revoked
	h := newHarnessWithParts(t, nil, nil, nil, nil, identity)
	actor := h.seedActor(t, "actor_rev5")
	role := h.seedRole(t, "t052_rev5_role", "t052_rev5_code")
	dept := h.seedDepartment(t, "t052_rev5_dept")

	_, err := h.svc.CreateUser(context.Background(), op(actor, "key-rev5"), createParams("revoked5", []int64{role}, &dept))
	require.ErrorIs(t, err, adminbff.ErrForbidden, "revocation mid-saga rejects with the authorization error")

	w := h.workflowByKey(t, actor, "key-rev5")
	assert.Equal(t, adminbff.WorkflowRejected, w.State)
	assert.Equal(t, int64(0), h.countUsersWithUsername(t, "revoked5"), "the applied C3 side effect must be reversed")
}

func TestCreateUser_ReauthRevokedAtC7RestoresAndCompensates(t *testing.T) {
	identity := &fakeIdentity{grantedChecks: 2} // C3 + C5 granted, C7 revoked
	h := newHarnessWithParts(t, nil, nil, nil, nil, identity)
	actor := h.seedActor(t, "actor_rev7")
	role := h.seedRole(t, "t052_rev7_role", "t052_rev7_code")
	dept := h.seedDepartment(t, "t052_rev7_dept")

	_, err := h.svc.CreateUser(context.Background(), op(actor, "key-rev7"), createParams("revoked7", []int64{role}, &dept))
	require.ErrorIs(t, err, adminbff.ErrForbidden)

	w := h.workflowByKey(t, actor, "key-rev7")
	assert.Equal(t, adminbff.WorkflowRejected, w.State)
	assert.Equal(t, int64(0), h.countUsersWithUsername(t, "revoked7"))
	// The applied C5 membership was restored before the provisioning user was
	// compensated (the revocation is the proven C7 failure).
	assert.Equal(t, int64(2), h.countReceipts(t, "organization_command_receipts", w.OperationID))
	assert.Equal(t, int64(1), h.countReceiptsByCommand(t, "organization_command_receipts", w.OperationID, "set_user_department"))
	assert.Equal(t, int64(1), h.countReceiptsByCommand(t, "organization_command_receipts", w.OperationID, "restore_user_department"))
	assert.Equal(t, int64(1), h.countReceiptsByCommand(t, "iam_command_receipts", w.OperationID, "compensate_provisioning_user"))
}

// --- resume machine -----------------------------------------------------------------

func TestCreateUser_ResumeFromIamProvisioned(t *testing.T) {
	membership := &fakeMembership{}
	orgReceipts := &fakeOrgReceipts{}
	h := newHarnessWithParts(t, nil, nil, membership, orgReceipts, nil)
	actor := h.seedActor(t, "actor_res5")
	role := h.seedRole(t, "t052_res5_role", "t052_res5_code")
	dept := h.seedDepartment(t, "t052_res5_dept")

	// C5 commits but times out, and the resolution itself fails: the request
	// ends with the workflow running at iam_provisioned.
	membership.failSetAfterCommit = true
	orgReceipts.failNext = true
	_, err := h.svc.CreateUser(context.Background(), op(actor, "key-res5"), createParams("resume5", []int64{role}, &dept))
	require.ErrorIs(t, err, adminbff.ErrDependencyUnavailable)
	w := h.workflowByKey(t, actor, "key-res5")
	assert.Equal(t, adminbff.WorkflowRunning, w.State)
	assert.Equal(t, adminbff.StepIamProvisioned, w.CurrentStep)
	require.NotNil(t, w.SubjectUserID)

	// The resume machine reclaims and re-issues C5; the participant's
	// resolve-first replays the committed membership.
	membership.failSetAfterCommit = false
	h.expireLease(t, w.OperationID)
	outcome, err := h.svc.ResumeWorkflow(context.Background(), w.OperationID)
	require.NoError(t, err)
	require.True(t, outcome.Terminal)
	assert.Equal(t, adminbff.WorkflowSucceeded, outcome.State)
	assert.Equal(t, "active", h.lifecycleState(t, *w.SubjectUserID))
}

// T058: the resume machine re-checks current authorization immediately
// before the resumed forward side effect. An actor revoked while the create
// workflow ran without client input (C5 pending) must not get the membership
// side effect on worker resume — the workflow is rejected and the C3
// provisioning side effect is compensated (no-side-effect reject for C5).
func TestCreateUser_ResumeRevokedAtC5CompensatesProvisioningOnly(t *testing.T) {
	identity := &fakeIdentity{grantedChecks: 2} // C3 + C5 granted during the request
	membership := &fakeMembership{}
	orgReceipts := &fakeOrgReceipts{}
	h := newHarnessWithParts(t, nil, nil, membership, orgReceipts, identity)
	actor := h.seedActor(t, "actor_res5r")
	role := h.seedRole(t, "t058_res5r_role", "t058_res5r_code")
	dept := h.seedDepartment(t, "t058_res5r_dept")

	// C5 commits but times out, and the resolution itself fails: the request
	// ends with the workflow running at iam_provisioned.
	membership.failSetAfterCommit = true
	orgReceipts.failNext = true
	_, err := h.svc.CreateUser(context.Background(), op(actor, "key-res5r"), createParams("resume5r", []int64{role}, &dept))
	require.ErrorIs(t, err, adminbff.ErrDependencyUnavailable)
	w := h.workflowByKey(t, actor, "key-res5r")
	assert.Equal(t, adminbff.WorkflowRunning, w.State)
	assert.Equal(t, adminbff.StepIamProvisioned, w.CurrentStep)
	require.NotNil(t, w.SubjectUserID)

	// Revoked before the worker resumes: C5's reauthorization fails on the
	// resume path too, the membership side effect is never issued, and the
	// C3 provisioning user is compensated.
	identity.grantedChecks = identity.calls // deny every further check
	membership.failSetAfterCommit = false
	h.expireLease(t, w.OperationID)
	outcome, rerr := h.svc.ResumeWorkflow(context.Background(), w.OperationID)
	require.ErrorIs(t, rerr, adminbff.ErrForbidden)
	require.True(t, outcome.Terminal)
	assert.Equal(t, adminbff.WorkflowRejected, outcome.State)
	assert.Equal(t, int64(0), h.countUsersWithUsername(t, "resume5r"), "the C3 provisioning user must be compensated")
	_, ok := h.membershipDepartment(t, *w.SubjectUserID)
	assert.False(t, ok, "no membership side effect on a revoked resume")
	assert.Equal(t, int64(1), h.countReceiptsByCommand(t, "iam_command_receipts", w.OperationID, "compensate_provisioning_user"),
		"compensation receipt records the reversal")
}

func TestCreateUser_ResumeFromOrganizationAssigned(t *testing.T) {
	managed := &fakeManaged{}
	iamReceipts := &fakeReceipts{}
	h := newHarnessWithParts(t, managed, iamReceipts, nil, nil, nil)
	actor := h.seedActor(t, "actor_res7")
	role := h.seedRole(t, "t052_res7_role", "t052_res7_code")

	// C7 commits but times out, and the resolution itself fails: the request
	// ends with the workflow running at organization_assigned.
	managed.failActivateAfterCommit = true
	iamReceipts.failNext = true
	_, err := h.svc.CreateUser(context.Background(), op(actor, "key-res7"), createParams("resume7", []int64{role}, nil))
	require.ErrorIs(t, err, adminbff.ErrDependencyUnavailable)
	w := h.workflowByKey(t, actor, "key-res7")
	assert.Equal(t, adminbff.WorkflowRunning, w.State)
	assert.Equal(t, adminbff.StepOrganizationAssigned, w.CurrentStep)

	// The resume machine reclaims and re-issues C7; resolve-first replays
	// the committed activation.
	managed.failActivateAfterCommit = false
	h.expireLease(t, w.OperationID)
	outcome, err := h.svc.ResumeWorkflow(context.Background(), w.OperationID)
	require.NoError(t, err)
	require.True(t, outcome.Terminal)
	assert.Equal(t, adminbff.WorkflowSucceeded, outcome.State)
	assert.Equal(t, "active", h.lifecycleState(t, h.userIDByUsername(t, "resume7")))
}

func TestResumeWorkflow_ParksCreatedWhenWorkerHasNoCredential(t *testing.T) {
	h := newHarness(t)
	// A workflow claimed and abandoned at step=created (crash during C3, no
	// credential in storage): the worker must park it for a same-key retry.
	w := adminbff.Workflow{
		OperationID:        "22222222-0000-0000-0000-000000000001",
		OperationType:      adminbff.OperationTypeCreateUser,
		RequestFingerprint: "fp",
		ActorUserID:        1,
		State:              adminbff.WorkflowPending,
		CurrentStep:        adminbff.StepCreated,
		RetryDeadlineAt:    time.Now().Add(24 * time.Hour),
	}
	require.NoError(t, h.work.CreateWorkflow(context.Background(), w))
	claimed, err := h.work.ClaimWorkflowByOperationID(context.Background(), w.OperationID, "actor:1", 30*time.Second, time.Now())
	require.NoError(t, err)
	require.Equal(t, adminbff.WorkflowRunning, claimed.State)

	h.expireLease(t, w.OperationID)
	outcome, err := h.svc.ResumeWorkflow(context.Background(), w.OperationID)
	require.NoError(t, err)
	assert.False(t, outcome.Terminal, "parking is not a terminal state")

	parked, err := h.work.GetWorkflowByOperationID(context.Background(), w.OperationID)
	require.NoError(t, err)
	assert.Equal(t, adminbff.WorkflowAwaitingClientInput, parked.State)
	require.NotNil(t, parked.ClientInputDeadlineAt)
	assert.WithinDuration(t, time.Now().Add(24*time.Hour), *parked.ClientInputDeadlineAt, time.Minute)
	assert.Nil(t, parked.LeaseOwner)
}

func TestCreateUser_RunningSameKeyIsOperationInProgress(t *testing.T) {
	h := newHarness(t)
	actor := h.seedActor(t, "actor_prog")
	role := h.seedRole(t, "t052_prog_role", "t052_prog_code")

	// A live workflow already owns the key: the request must not touch it.
	// The fingerprint mirrors createRequestFingerprint (workflows.go); the
	// same-key lookup only discriminates on fp in terminal/awaiting states,
	// but the row is built as the real service would build it.
	key := "key-prog"
	fp, err := consistency.FingerprintV1(adminbff.OperationTypeCreateUser, actor, map[string]any{
		"username":                  "prog_user",
		"account":                   consistency.OptString(nil),
		"email":                     consistency.OptString(nil),
		"role_ids":                  consistency.SortedIDs([]int64{role}),
		"department_id":             consistency.OptInt(nil),
		"password_change_requested": true,
	})
	require.NoError(t, err)
	w := adminbff.Workflow{
		OperationID:        "33333333-0000-0000-0000-000000000001",
		OperationType:      adminbff.OperationTypeCreateUser,
		IdempotencyKey:     &key,
		RequestFingerprint: fp,
		ActorUserID:        actor,
		State:              adminbff.WorkflowPending,
		CurrentStep:        adminbff.StepCreated,
		RetryDeadlineAt:    time.Now().Add(24 * time.Hour),
	}
	require.NoError(t, h.work.CreateWorkflow(context.Background(), w))
	_, err = h.work.ClaimWorkflowByOperationID(context.Background(), w.OperationID, "other-owner", 30*time.Second, time.Now())
	require.NoError(t, err)

	_, err = h.svc.CreateUser(context.Background(), op(actor, key), createParams("prog_user", []int64{role}, nil))
	require.ErrorIs(t, err, adminbff.ErrOperationInProgress, "a live claim owns the workflow")
}

// --- U0–U7 update saga ---------------------------------------------------------

// seedTargetUser creates an active user via the create saga with the given
// department (nil = no membership row) and returns its id.
func (h *harness) seedTargetUser(t *testing.T, username string, roleID int64, departmentID *int64) int64 {
	t.Helper()
	actor := h.seedActor(t, "actor-"+username)
	res, err := h.svc.CreateUser(context.Background(), op(actor, "seed-"+username), createParams(username, []int64{roleID}, departmentID))
	require.NoError(t, err)
	require.Positive(t, res.UserID)
	return res.UserID
}

func ptr[T any](v T) *T { return &v }

func updateParams(userID int64, username string, roleIDs []int64, departmentID *int64, password string) adminbff.UpdateUserParams {
	return adminbff.UpdateUserParams{
		UserID:       userID,
		Username:     username,
		RoleIDs:      roleIDs,
		DepartmentID: departmentID,
		Password:     password,
	}
}

func (h *harness) updateWorkflowByKey(t *testing.T, actorID int64, key string) adminbff.Workflow {
	t.Helper()
	w, err := h.work.GetWorkflowByIdempotencyKey(context.Background(), adminbff.OperationTypeUpdateUser, actorID, key)
	require.NoError(t, err)
	require.NotNil(t, w, "update workflow for key %q must exist", key)
	return *w
}

func (h *harness) subjectActive(t *testing.T, operationID string) bool {
	t.Helper()
	subs, err := h.work.ListSubjectsByOperation(context.Background(), operationID)
	require.NoError(t, err)
	require.Len(t, subs, 1, "exactly one subject row per single-target update")
	return subs[0].Active
}

func (h *harness) username(t *testing.T, userID int64) string {
	t.Helper()
	var name string
	require.NoError(t, h.db.QueryRow(context.Background(),
		"SELECT username FROM users WHERE id = $1", userID).Scan(&name))
	return name
}

func (h *harness) userVersion(t *testing.T, userID int64) int64 {
	t.Helper()
	var v int64
	require.NoError(t, h.db.QueryRow(context.Background(),
		"SELECT version FROM users WHERE id = $1", userID).Scan(&v))
	return v
}

// membershipVersion reads the raw organization membership version of a user
// (time-control truth for the ABA/version-CAS assertions).
func (h *harness) membershipVersion(t *testing.T, userID int64) int64 {
	t.Helper()
	var v int64
	require.NoError(t, h.db.QueryRow(context.Background(),
		"SELECT membership_version FROM organization_user_departments WHERE user_id = $1", userID).Scan(&v))
	return v
}

// --- D0–D5 delete saga helpers ------------------------------------------------------

func deleteParams(userIDs ...int64) adminbff.DeleteUserParams {
	return adminbff.DeleteUserParams{UserIDs: userIDs}
}

func (h *harness) deleteWorkflowByKey(t *testing.T, actorID int64, key string) adminbff.Workflow {
	t.Helper()
	w, err := h.work.GetWorkflowByIdempotencyKey(context.Background(), adminbff.OperationTypeDeleteUser, actorID, key)
	require.NoError(t, err)
	require.NotNil(t, w, "delete workflow for key %q must exist", key)
	return *w
}

// subjectRows returns the subject rows of one workflow (store order: user_id asc).
func (h *harness) subjectRows(t *testing.T, operationID string) []adminbff.WorkflowSubject {
	t.Helper()
	subs, err := h.work.ListSubjectsByOperation(context.Background(), operationID)
	require.NoError(t, err)
	return subs
}

// countUserDeletedEvents counts the iam.user.deleted outbox events of a user
// (the aggregate id is the user id as text).
func (h *harness) countUserDeletedEvents(t *testing.T, userID int64) int64 {
	t.Helper()
	var n int64
	require.NoError(t, h.db.QueryRow(context.Background(),
		"SELECT count(*) FROM iam_outbox_events WHERE event_type = 'iam.user.deleted' AND aggregate_id = $1",
		fmt.Sprintf("%d", userID)).Scan(&n))
	return n
}

func TestUpdateUser_SuccessWithDepartment(t *testing.T) {
	h := newHarness(t)
	actor := h.seedActor(t, "actor_upd_ok")
	role := h.seedRole(t, "t053_role", "t053_role_code")
	dept1 := h.seedDepartment(t, "t053_dept1")
	dept2 := h.seedDepartment(t, "t053_dept2")
	target := h.seedTargetUser(t, "updatee", role, &dept1)
	beforeVersion := h.userVersion(t, target)

	_, err := h.svc.UpdateUser(context.Background(), op(actor, "upd-key-1"),
		updateParams(target, "updatee_renamed", []int64{role}, &dept2, ""))
	require.NoError(t, err)

	assert.Equal(t, "updatee_renamed", h.username(t, target), "U6 must update the profile")
	gotDept, ok := h.membershipDepartment(t, target)
	require.True(t, ok)
	require.NotNil(t, gotDept, "U4 must keep a department")
	assert.Equal(t, dept2, *gotDept)
	assert.Equal(t, beforeVersion+1, h.userVersion(t, target), "U6 bumps the IAM version exactly once")

	w := h.updateWorkflowByKey(t, actor, "upd-key-1")
	assert.Equal(t, adminbff.WorkflowSucceeded, w.State)
	assert.Equal(t, adminbff.StepOrganizationUpdated, w.CurrentStep, "U5 is the last durable step")
	require.NotNil(t, w.AppliedMembershipVersion)
	assert.False(t, h.subjectActive(t, w.OperationID), "succeeded must release the exclusion")

	// exactly one receipt per committed side effect: U4 set + U6 update
	assert.Equal(t, int64(1), h.countReceiptsByCommand(t, "organization_command_receipts", w.OperationID, "set_user_department"))
	assert.Equal(t, int64(1), h.countReceiptsByCommand(t, "iam_command_receipts", w.OperationID, "update_managed_user"))
	assert.Equal(t, int64(1), h.countReceipts(t, "organization_command_receipts", w.OperationID), "exactly one org side effect: the U4 set")
}

func TestUpdateUser_SuccessClearsDepartment(t *testing.T) {
	h := newHarness(t)
	actor := h.seedActor(t, "actor_upd_clear")
	role := h.seedRole(t, "t053_role2", "t053_role_code2")
	dept1 := h.seedDepartment(t, "t053_dept_clear")
	target := h.seedTargetUser(t, "clear_target", role, &dept1)

	_, err := h.svc.UpdateUser(context.Background(), op(actor, "upd-key-clear"),
		updateParams(target, "clear_target", []int64{role}, nil, ""))
	require.NoError(t, err)

	gotDept, ok := h.membershipDepartment(t, target)
	require.True(t, ok, "U4 clear must keep the NULL-department tombstone")
	assert.Nil(t, gotDept, "clear must null the department, never delete the state row")
	w := h.updateWorkflowByKey(t, actor, "upd-key-clear")
	assert.Equal(t, adminbff.WorkflowSucceeded, w.State)
	assert.Equal(t, int64(1), h.countReceiptsByCommand(t, "organization_command_receipts", w.OperationID, "clear_user_department"))
}

func TestUpdateUser_ClearWhenAbsentStillBumpsVersion(t *testing.T) {
	h := newHarness(t)
	actor := h.seedActor(t, "actor_upd_absent")
	role := h.seedRole(t, "t053_role3", "t053_role_code3")
	target := h.seedTargetUser(t, "absent_target", role, nil) // no department planned

	// The 000008 users_insert_bridge seeds a NULL-department tombstone row
	// (version 1) for every new user; the "absent" state is that tombstone.
	gotDept, ok := h.membershipDepartment(t, target)
	require.True(t, ok, "the bridge seeds a membership state row for every user")
	assert.Nil(t, gotDept)

	_, err := h.svc.UpdateUser(context.Background(), op(actor, "upd-key-absent"),
		updateParams(target, "absent_target", []int64{role}, nil, ""))
	require.NoError(t, err)

	gotDept, ok = h.membershipDepartment(t, target)
	require.True(t, ok, "U4 clear keeps the NULL-department tombstone")
	assert.Nil(t, gotDept)
	w := h.updateWorkflowByKey(t, actor, "upd-key-absent")
	require.NotNil(t, w.AppliedMembershipVersion, "U4 always produces an applied version")
	assert.Equal(t, int64(2), *w.AppliedMembershipVersion, "clear of the v1 tombstone applies v2")
}

func TestUpdateUser_SameKeyReplayReturnsWithoutSideEffects(t *testing.T) {
	h := newHarness(t)
	actor := h.seedActor(t, "actor_upd_replay")
	role := h.seedRole(t, "t053_role4", "t053_role_code4")
	dept1 := h.seedDepartment(t, "t053_dept_rp")
	dept2 := h.seedDepartment(t, "t053_dept_rp2")
	target := h.seedTargetUser(t, "replay_target", role, &dept1)

	_, err := h.svc.UpdateUser(context.Background(), op(actor, "upd-key-replay"),
		updateParams(target, "replay_target", []int64{role}, &dept2, "Secret-123!"))
	require.NoError(t, err)
	w := h.updateWorkflowByKey(t, actor, "upd-key-replay")

	// Same key, corrected credential: the password VALUE is excluded from
	// the request fingerprint (only its presence/absence is), so the replay
	// keeps the same request identity.
	_, err = h.svc.UpdateUser(context.Background(), op(actor, "upd-key-replay"),
		updateParams(target, "replay_target", []int64{role}, &dept2, "New-Secret-456!"))
	require.NoError(t, err)

	assert.Equal(t, "replay_target", h.username(t, target))
	assert.Equal(t, dept2, *mustDept(t, h, target))
	assert.Equal(t, int64(1), h.countReceiptsByCommand(t, "iam_command_receipts", w.OperationID, "update_managed_user"), "replay must not re-run U6")
	assert.Equal(t, int64(1), h.countReceiptsByCommand(t, "organization_command_receipts", w.OperationID, "set_user_department"), "replay must not re-run U4")
}

func mustDept(t *testing.T, h *harness, userID int64) *int64 {
	t.Helper()
	got, ok := h.membershipDepartment(t, userID)
	require.True(t, ok, "membership row must exist")
	return got
}

func TestUpdateUser_SameKeyDifferentRequestConflicts(t *testing.T) {
	h := newHarness(t)
	actor := h.seedActor(t, "actor_upd_conflict")
	role := h.seedRole(t, "t053_role5", "t053_role_code5")
	target := h.seedTargetUser(t, "conflict_target", role, nil)

	_, err := h.svc.UpdateUser(context.Background(), op(actor, "upd-key-conflict"),
		updateParams(target, "conflict_target", []int64{role}, nil, ""))
	require.NoError(t, err)

	_, err = h.svc.UpdateUser(context.Background(), op(actor, "upd-key-conflict"),
		updateParams(target, "conflict_target_OTHER", []int64{role}, nil, ""))
	require.ErrorIs(t, err, adminbff.ErrIdempotencyConflict)
}

func TestUpdateUser_U1UserNotFoundRejects(t *testing.T) {
	h := newHarness(t)
	actor := h.seedActor(t, "actor_upd_notfound")
	role := h.seedRole(t, "t053_role6", "t053_role_code6")

	_, err := h.svc.UpdateUser(context.Background(), op(actor, "upd-key-notfound"),
		updateParams(424242, "ghost", []int64{role}, nil, ""))
	require.ErrorIs(t, err, adminbff.ErrUserNotFound)

	// No domain writes, no subject row, terminal rejected with the safe code.
	w := h.updateWorkflowByKey(t, actor, "upd-key-notfound")
	assert.Equal(t, adminbff.WorkflowRejected, w.State)
	subs, lerr := h.work.ListSubjectsByOperation(context.Background(), w.OperationID)
	require.NoError(t, lerr)
	assert.Len(t, subs, 0, "U1 failure precedes the subject insert")
}

func TestUpdateUser_U1RoleNotFoundRejects(t *testing.T) {
	h := newHarness(t)
	actor := h.seedActor(t, "actor_upd_badrole")
	role := h.seedRole(t, "t053_role7", "t053_role_code7")
	target := h.seedTargetUser(t, "badrole_target", role, nil)

	_, err := h.svc.UpdateUser(context.Background(), op(actor, "upd-key-badrole"),
		updateParams(target, "badrole_target", []int64{999999}, nil, ""))
	require.ErrorIs(t, err, adminbff.ErrInvalidInput)
	w := h.updateWorkflowByKey(t, actor, "upd-key-badrole")
	assert.Equal(t, adminbff.WorkflowRejected, w.State)
}

func TestUpdateUser_U2DepartmentNotFoundRejects(t *testing.T) {
	h := newHarness(t)
	actor := h.seedActor(t, "actor_upd_baddept")
	role := h.seedRole(t, "t053_role8", "t053_role_code8")
	target := h.seedTargetUser(t, "baddept_target", role, nil)

	_, err := h.svc.UpdateUser(context.Background(), op(actor, "upd-key-baddept"),
		updateParams(target, "baddept_target", []int64{role}, intPtr(999999), ""))
	require.ErrorIs(t, err, adminbff.ErrInvalidInput)
	w := h.updateWorkflowByKey(t, actor, "upd-key-baddept")
	assert.Equal(t, adminbff.WorkflowRejected, w.State)
	assert.False(t, h.subjectActive(t, w.OperationID), "rejected must release the U1 exclusion")
}

func intPtr(v int64) *int64 { return &v }

func TestUpdateUser_SubjectExcludedRejectsWithOperationInProgress(t *testing.T) {
	h := newHarness(t)
	actor := h.seedActor(t, "actor_upd_excl")
	role := h.seedRole(t, "t053_role9", "t053_role_code9")
	target := h.seedTargetUser(t, "excl_target", role, nil)

	// A concurrent workflow already covers the subject (simulated with a
	// live lease on its own update workflow whose subject row is active).
	other := adminbff.Workflow{
		OperationID:        "44444444-0000-0000-0000-000000000002",
		OperationType:      adminbff.OperationTypeUpdateUser,
		IdempotencyKey:     ptr("other-upd"),
		RequestFingerprint: "fp-other-upd", // never compared: distinct idempotency key
		ActorUserID:        actor,
		State:              adminbff.WorkflowPending,
		CurrentStep:        adminbff.StepCreated,
		RetryDeadlineAt:    time.Now().Add(24 * time.Hour),
	}
	require.NoError(t, h.work.CreateWorkflow(context.Background(), other))
	_, err := h.work.ClaimWorkflowByOperationID(context.Background(), other.OperationID, "other-owner", 30*time.Second, time.Now())
	require.NoError(t, err)
	require.NoError(t, h.work.InsertWorkflowSubject(context.Background(), adminbff.WorkflowSubject{
		OperationID: other.OperationID, SubjectUserID: target, ExpectedIamVersion: h.userVersion(t, target),
	}))

	_, err = h.svc.UpdateUser(context.Background(), op(actor, "upd-key-excl"),
		updateParams(target, "excl_target", []int64{role}, nil, ""))
	require.ErrorIs(t, err, adminbff.ErrOperationInProgress)

	w := h.updateWorkflowByKey(t, actor, "upd-key-excl")
	assert.Equal(t, adminbff.WorkflowRejected, w.State)
	assert.True(t, h.subjectActive(t, other.OperationID), "the OTHER workflow's exclusion must survive")
}

func TestUpdateUser_U3TimeoutParksAwaitingAndRetryRecovers(t *testing.T) {
	membership := &fakeMembership{failRead: true}
	h := newHarnessWithParts(t, nil, nil, membership, nil, nil)
	actor := h.seedActor(t, "actor_upd_u3")
	role := h.seedRole(t, "t053_role10", "t053_role_code10")
	target := h.seedTargetUser(t, "u3_target", role, nil)

	_, err := h.svc.UpdateUser(context.Background(), op(actor, "upd-key-u3"),
		updateParams(target, "u3_target", []int64{role}, nil, ""))
	require.ErrorIs(t, err, adminbff.ErrDependencyTimeout)

	w := h.updateWorkflowByKey(t, actor, "upd-key-u3")
	assert.Equal(t, adminbff.WorkflowAwaitingClientInput, w.State)
	assert.Equal(t, adminbff.StepIamValidated, w.CurrentStep, "U3 timeout parks before the read")
	assert.True(t, h.subjectActive(t, w.OperationID), "the park keeps the exclusion")

	membership.failRead = false
	_, err = h.svc.UpdateUser(context.Background(), op(actor, "upd-key-u3"),
		updateParams(target, "u3_target", []int64{role}, nil, ""))
	require.NoError(t, err)
	assert.Equal(t, "u3_target", h.username(t, target))
	w = h.updateWorkflowByKey(t, actor, "upd-key-u3")
	assert.Equal(t, adminbff.WorkflowSucceeded, w.State)
}

func TestUpdateUser_U4ReauthRevokedParksAwaitingAndRetryRecovers(t *testing.T) {
	identity := &fakeIdentity{grantedChecks: 10} // the create-seed reauth passes
	h := newHarnessWithParts(t, nil, nil, nil, nil, identity)
	actor := h.seedActor(t, "actor_upd_u4reauth")
	role := h.seedRole(t, "t053_role11", "t053_role_code11")
	dept1 := h.seedDepartment(t, "t053_dept_u4r")
	dept2 := h.seedDepartment(t, "t053_dept_u4r2")
	target := h.seedTargetUser(t, "u4reauth_target", role, &dept1)
	identity.grantedChecks = identity.calls // next check (update U4) is denied

	// A known U4 failure with a requested password change parks awaiting
	// (the same-key retry may recover); IAM stays untouched.
	_, err := h.svc.UpdateUser(context.Background(), op(actor, "upd-key-u4reauth"),
		updateParams(target, "u4reauth_target", []int64{role}, &dept2, "Pw-123456!"))
	require.ErrorIs(t, err, adminbff.ErrForbidden)

	w := h.updateWorkflowByKey(t, actor, "upd-key-u4reauth")
	assert.Equal(t, adminbff.WorkflowAwaitingClientInput, w.State)
	assert.Equal(t, adminbff.StepOrganizationRead, w.CurrentStep, "U3 is durable before the U4 failure")
	assert.Equal(t, dept1, *mustDept(t, h, target), "no membership side effect on a denied U4")
	assert.Equal(t, "u4reauth_target", h.username(t, target))

	identity.grantedChecks = identity.calls + 10
	_, err = h.svc.UpdateUser(context.Background(), op(actor, "upd-key-u4reauth"),
		updateParams(target, "u4reauth_target", []int64{role}, &dept2, "Pw-123456!"))
	require.NoError(t, err)
	assert.Equal(t, dept2, *mustDept(t, h, target))
	w = h.updateWorkflowByKey(t, actor, "upd-key-u4reauth")
	assert.Equal(t, adminbff.WorkflowSucceeded, w.State)
}

func TestUpdateUser_U4KnownFailureWithoutPasswordRejects(t *testing.T) {
	identity := &fakeIdentity{grantedChecks: 10} // the create-seed reauth passes
	h := newHarnessWithParts(t, nil, nil, nil, nil, identity)
	actor := h.seedActor(t, "actor_upd_u4rej")
	role := h.seedRole(t, "t053_role12", "t053_role_code12")
	dept1 := h.seedDepartment(t, "t053_dept_u4rej")
	target := h.seedTargetUser(t, "u4rej_target", role, &dept1)
	identity.grantedChecks = identity.calls // next check (update U4) is denied

	// A known U4 failure WITHOUT a password change has nothing to retry:
	// reject (no compensation exists — IAM never ran).
	_, err := h.svc.UpdateUser(context.Background(), op(actor, "upd-key-u4rej"),
		updateParams(target, "u4rej_target", []int64{role}, nil, ""))
	require.ErrorIs(t, err, adminbff.ErrForbidden)

	w := h.updateWorkflowByKey(t, actor, "upd-key-u4rej")
	assert.Equal(t, adminbff.WorkflowRejected, w.State)
	assert.False(t, h.subjectActive(t, w.OperationID), "rejected must release the exclusion")
	assert.Equal(t, dept1, *mustDept(t, h, target), "membership unchanged")
	assert.Equal(t, int64(0), h.countReceipts(t, "organization_command_receipts", w.OperationID))
}

func TestUpdateUser_U4UnknownCommittedParksAtOrganizationUpdated(t *testing.T) {
	// U4's set commits but returns a timeout (replayed via the receipt);
	// U6 then fails with an unresolved outcome, so the request ends parked
	// at organization_updated with the applied version + fingerprint.
	membership := &fakeMembership{}
	managed := &fakeManaged{}
	iamReceipts := &fakeReceipts{}
	h := newHarnessWithParts(t, managed, iamReceipts, membership, nil, nil)
	actor := h.seedActor(t, "actor_upd_u4unk")
	role := h.seedRole(t, "t053_role13", "t053_role_code13")
	dept1 := h.seedDepartment(t, "t053_dept_u4unk")
	dept2 := h.seedDepartment(t, "t053_dept_u4unk2")
	// The fakes are armed only after the create-seed (they would break it).
	target := h.seedTargetUser(t, "u4unk_target", role, &dept1)
	membership.failSetAfterCommit = true
	managed.failUpdateBeforeCommit = true
	iamReceipts.failNext = true

	_, err := h.svc.UpdateUser(context.Background(), op(actor, "upd-key-u4unk"),
		updateParams(target, "u4unk_target", []int64{role}, &dept2, "Pw-123456!"))
	require.ErrorIs(t, err, adminbff.ErrDependencyUnavailable)

	// The set COMMITTED (receipt persisted), the request ended before U6:
	// awaiting at organization_updated with the applied version + fingerprint.
	w := h.updateWorkflowByKey(t, actor, "upd-key-u4unk")
	assert.Equal(t, adminbff.WorkflowAwaitingClientInput, w.State)
	assert.Equal(t, adminbff.StepOrganizationUpdated, w.CurrentStep)
	require.NotNil(t, w.AppliedMembershipVersion)
	require.NotNil(t, w.CommandFingerprint, "U6 fingerprint persisted at the U5 boundary")
	assert.Equal(t, dept2, *mustDept(t, h, target), "U4 committed despite the timeout")
	assert.Equal(t, int64(0), h.countReceipts(t, "iam_command_receipts", w.OperationID), "U6 not yet issued")

	// Same-key retry re-enters at organization_updated: U6 only.
	membership.failSetAfterCommit = false
	managed.failUpdateBeforeCommit = false
	_, err = h.svc.UpdateUser(context.Background(), op(actor, "upd-key-u4unk"),
		updateParams(target, "u4unk_target", []int64{role}, &dept2, "Pw-123456!"))
	require.NoError(t, err)
	assert.Equal(t, "u4unk_target", h.username(t, target))
	assert.Equal(t, int64(1), h.countReceiptsByCommand(t, "iam_command_receipts", w.OperationID, "update_managed_user"))
	assert.Equal(t, int64(1), h.countReceiptsByCommand(t, "organization_command_receipts", w.OperationID, "set_user_department"), "U4 never re-issued")
	w = h.updateWorkflowByKey(t, actor, "upd-key-u4unk")
	assert.Equal(t, adminbff.WorkflowSucceeded, w.State)
}

func TestUpdateUser_U4UnknownResolveFailedParksAndRetryReplays(t *testing.T) {
	membership := &fakeMembership{}
	orgReceipts := &fakeOrgReceipts{}
	h := newHarnessWithParts(t, nil, nil, membership, orgReceipts, nil)
	actor := h.seedActor(t, "actor_upd_u4res")
	role := h.seedRole(t, "t053_role14", "t053_role_code14")
	dept1 := h.seedDepartment(t, "t053_dept_u4res")
	dept2 := h.seedDepartment(t, "t053_dept_u4res2")
	// Armed only after the create-seed (the set-failure and the one-shot
	// resolve failure would otherwise break the seed itself).
	target := h.seedTargetUser(t, "u4res_target", role, &dept1)
	membership.failSetAfterCommit = true
	orgReceipts.failNext = true

	// Resolve failed: the outcome is unknown even though U4 committed —
	// park awaiting regardless of the password (the worker cannot decide).
	_, err := h.svc.UpdateUser(context.Background(), op(actor, "upd-key-u4res"),
		updateParams(target, "u4res_target", []int64{role}, &dept2, ""))
	require.ErrorIs(t, err, adminbff.ErrDependencyUnavailable)

	w := h.updateWorkflowByKey(t, actor, "upd-key-u4res")
	assert.Equal(t, adminbff.WorkflowAwaitingClientInput, w.State)
	assert.Equal(t, adminbff.StepOrganizationRead, w.CurrentStep, "no applied version persisted")
	assert.Nil(t, w.AppliedMembershipVersion)

	// The retry re-issues U4; the committed receipt replays the result —
	// no second membership mutation.
	membership.failSetAfterCommit = false
	_, err = h.svc.UpdateUser(context.Background(), op(actor, "upd-key-u4res"),
		updateParams(target, "u4res_target", []int64{role}, &dept2, ""))
	require.NoError(t, err)
	assert.Equal(t, dept2, *mustDept(t, h, target))
	assert.Equal(t, int64(1), h.countReceiptsByCommand(t, "organization_command_receipts", w.OperationID, "set_user_department"), "replay must not mutate twice")
}

func TestUpdateUser_U4UnknownAbsentWithoutPasswordRejects(t *testing.T) {
	membership := &fakeMembership{} // failSetBeforeCommit armed after the seed
	h := newHarnessWithParts(t, nil, nil, membership, nil, nil)
	actor := h.seedActor(t, "actor_upd_u4abs")
	role := h.seedRole(t, "t053_role15", "t053_role_code15")
	dept1 := h.seedDepartment(t, "t053_dept_u4abs")
	dept2 := h.seedDepartment(t, "t053_dept_u4abs2")
	target := h.seedTargetUser(t, "u4abs_target", role, &dept1)
	membership.failSetBeforeCommit = true // timeout, no commit

	// Resolve proves non-commit; no password to retry with -> reject.
	_, err := h.svc.UpdateUser(context.Background(), op(actor, "upd-key-u4abs"),
		updateParams(target, "u4abs_target", []int64{role}, &dept2, ""))
	require.ErrorIs(t, err, adminbff.ErrDependencyTimeout)

	w := h.updateWorkflowByKey(t, actor, "upd-key-u4abs")
	assert.Equal(t, adminbff.WorkflowRejected, w.State)
	assert.False(t, h.subjectActive(t, w.OperationID))
	assert.Equal(t, dept1, *mustDept(t, h, target), "membership untouched")
	assert.Equal(t, int64(0), h.countReceipts(t, "organization_command_receipts", w.OperationID))
}

func TestUpdateUser_U6KnownFailureRestoresAndRejects(t *testing.T) {
	managed := &fakeManaged{failUpdateBeforeCommit: true} // proven non-commit
	h := newHarnessWithParts(t, managed, nil, nil, nil, nil)
	actor := h.seedActor(t, "actor_upd_u6rej")
	role := h.seedRole(t, "t053_role16", "t053_role_code16")
	dept1 := h.seedDepartment(t, "t053_dept_u6rej")
	dept2 := h.seedDepartment(t, "t053_dept_u6rej2")
	target := h.seedTargetUser(t, "u6rej_target", role, &dept1)

	_, err := h.svc.UpdateUser(context.Background(), op(actor, "upd-key-u6rej"),
		updateParams(target, "u6rej_target", []int64{role}, &dept2, "Pw-123456!"))
	require.ErrorIs(t, err, adminbff.ErrDependencyTimeout)

	// U4 applied dept2, U6 provably did not commit: restore to dept1 and reject.
	assert.Equal(t, dept1, *mustDept(t, h, target), "restore must return the previous department")
	assert.Equal(t, "u6rej_target", h.username(t, target), "IAM untouched by a non-commit")
	w := h.updateWorkflowByKey(t, actor, "upd-key-u6rej")
	assert.Equal(t, adminbff.WorkflowRejected, w.State)
	assert.Equal(t, adminbff.CompensationSucceeded, w.CompensationState)
	assert.False(t, h.subjectActive(t, w.OperationID))
	assert.Equal(t, int64(1), h.countReceiptsByCommand(t, "organization_command_receipts", w.OperationID, "set_user_department"))
	assert.Equal(t, int64(1), h.countReceiptsByCommand(t, "organization_command_receipts", w.OperationID, "restore_user_department"))
	assert.Equal(t, int64(0), h.countReceipts(t, "iam_command_receipts", w.OperationID))

	// Same key replays the stored rejection.
	_, err = h.svc.UpdateUser(context.Background(), op(actor, "upd-key-u6rej"),
		updateParams(target, "u6rej_target", []int64{role}, &dept2, "Pw-123456!"))
	require.ErrorIs(t, err, adminbff.ErrDependencyTimeout)
}

func TestUpdateUser_U6TimeoutCommittedSucceeds(t *testing.T) {
	managed := &fakeManaged{failUpdateAfterCommit: true}
	h := newHarnessWithParts(t, managed, nil, nil, nil, nil)
	actor := h.seedActor(t, "actor_upd_u6ok")
	role := h.seedRole(t, "t053_role17", "t053_role_code17")
	dept1 := h.seedDepartment(t, "t053_dept_u6ok")
	target := h.seedTargetUser(t, "u6ok_target", role, &dept1)

	// U6 committed + receipt, timeout returned: the client resolves and
	// succeeds — never restores a committed credential change.
	_, err := h.svc.UpdateUser(context.Background(), op(actor, "upd-key-u6ok"),
		updateParams(target, "u6ok_target", []int64{role}, &dept1, "Pw-123456!"))
	require.NoError(t, err)

	assert.Equal(t, "u6ok_target", h.username(t, target))
	assert.Equal(t, dept1, *mustDept(t, h, target), "no restore after a committed U6")
	w := h.updateWorkflowByKey(t, actor, "upd-key-u6ok")
	assert.Equal(t, adminbff.WorkflowSucceeded, w.State)
	assert.Equal(t, int64(1), h.countReceiptsByCommand(t, "iam_command_receipts", w.OperationID, "update_managed_user"))
	assert.Equal(t, int64(0), h.countReceiptsByCommand(t, "organization_command_receipts", w.OperationID, "restore_user_department"))
}

func TestUpdateUser_U6UnknownResolveFailedParksAwaiting(t *testing.T) {
	managed := &fakeManaged{failUpdateAfterCommit: true}
	iamReceipts := &fakeReceipts{failNext: true}
	h := newHarnessWithParts(t, managed, iamReceipts, nil, nil, nil)
	actor := h.seedActor(t, "actor_upd_u6unk")
	role := h.seedRole(t, "t053_role18", "t053_role_code18")
	dept1 := h.seedDepartment(t, "t053_dept_u6unk")
	target := h.seedTargetUser(t, "u6unk_target", role, &dept1)

	// U6 committed but the resolve failed: with a credential the same-key
	// retry must re-resolve — park awaiting.
	_, err := h.svc.UpdateUser(context.Background(), op(actor, "upd-key-u6unk"),
		updateParams(target, "u6unk_target", []int64{role}, &dept1, "Pw-123456!"))
	require.ErrorIs(t, err, adminbff.ErrDependencyUnavailable)

	w := h.updateWorkflowByKey(t, actor, "upd-key-u6unk")
	assert.Equal(t, adminbff.WorkflowAwaitingClientInput, w.State)
	assert.Equal(t, adminbff.StepOrganizationUpdated, w.CurrentStep)
	assert.Equal(t, "u6unk_target", h.username(t, target), "U6 committed despite the unknown outcome")

	// Retry re-issues U6; the IAM resolve-first replay returns the committed
	// result without a second mutation.
	managed.failUpdateAfterCommit = false
	_, err = h.svc.UpdateUser(context.Background(), op(actor, "upd-key-u6unk"),
		updateParams(target, "u6unk_target", []int64{role}, &dept1, "Pw-123456!"))
	require.NoError(t, err)
	assert.Equal(t, int64(1), h.countReceiptsByCommand(t, "iam_command_receipts", w.OperationID, "update_managed_user"))
	w = h.updateWorkflowByKey(t, actor, "upd-key-u6unk")
	assert.Equal(t, adminbff.WorkflowSucceeded, w.State)
}

// T059 Checkpoint E: after the awaiting_client_input deadline the worker only
// rejects no-side-effect work or CAS-compensates applied membership. Here the
// deadline expires while the membership IS applied (U4 committed, U6 outcome
// unknown): the worker reclaims, restores the previous department and
// terminally rejects OPERATION_EXPIRED — the irreversible IAM mutation stays,
// the reversible membership is the only compensation.
func TestUpdateUser_AwaitingExpiryCompensatesAppliedMembership(t *testing.T) {
	managed := &fakeManaged{failUpdateAfterCommit: true} // U6 committed, receipt unresolved
	iamReceipts := &fakeReceipts{failNext: true}         // the U6 resolve fails -> unknown outcome
	h := newHarnessWithParts(t, managed, iamReceipts, nil, nil, nil)
	actor := h.seedActor(t, "actor_upd_awtexp")
	role := h.seedRole(t, "t059_role_awtexp", "t059_role_code_awtexp")
	dept1 := h.seedDepartment(t, "t059_dept_awtexp")
	dept2 := h.seedDepartment(t, "t059_dept_awtexp2")
	target := h.seedTargetUser(t, "awtexp_target", role, &dept1)

	// U4 applied dept2; U6 committed but its outcome is unknown — with a
	// credential the request parks awaiting, membership applied.
	_, err := h.svc.UpdateUser(context.Background(), op(actor, "upd-key-awtexp"),
		updateParams(target, "awtexp_target", []int64{role}, &dept2, "Pw-123456!"))
	require.ErrorIs(t, err, adminbff.ErrDependencyUnavailable)
	w := h.updateWorkflowByKey(t, actor, "upd-key-awtexp")
	assert.Equal(t, adminbff.WorkflowAwaitingClientInput, w.State)
	assert.Equal(t, dept2, *mustDept(t, h, target), "membership applied before the unknown U6")

	// The immutable input deadline passes: the expiry finalizer CAS-compensates.
	h.expireClientInput(t, w.OperationID)
	res, rerr := h.svc.ResumeWorkflow(context.Background(), w.OperationID)
	require.NoError(t, rerr)
	require.True(t, res.Terminal)
	assert.Equal(t, adminbff.WorkflowRejected, res.State)

	w = h.updateWorkflowByKey(t, actor, "upd-key-awtexp")
	assert.Equal(t, adminbff.WorkflowRejected, w.State)
	require.NotNil(t, w.Result)
	errMap, ok := w.Result["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "operation_expired", errMap["kind"])
	assert.Equal(t, dept1, *mustDept(t, h, target), "the expiry compensation restored the previous membership")
	assert.Equal(t, int64(1), h.countReceiptsByCommand(t, "organization_command_receipts", w.OperationID, "restore_user_department"))
	assert.False(t, h.subjectActive(t, w.OperationID), "compensation completion releases the exclusion")
}

func TestUpdateUser_U6ReauthRevokedRestoresAndRejects(t *testing.T) {
	identity := &fakeIdentity{grantedChecks: 10} // the create-seed reauth passes
	h := newHarnessWithParts(t, nil, nil, nil, nil, identity)
	actor := h.seedActor(t, "actor_upd_u6reauth")
	role := h.seedRole(t, "t053_role19", "t053_role_code19")
	dept1 := h.seedDepartment(t, "t053_dept_u6reauth")
	dept2 := h.seedDepartment(t, "t053_dept_u6reauth2")
	target := h.seedTargetUser(t, "u6reauth_target", role, &dept1)
	identity.grantedChecks = identity.calls + 1 // update U4 passes, U6 is denied

	_, err := h.svc.UpdateUser(context.Background(), op(actor, "upd-key-u6reauth"),
		updateParams(target, "u6reauth_target", []int64{role}, &dept2, ""))
	require.ErrorIs(t, err, adminbff.ErrForbidden)

	assert.Equal(t, dept1, *mustDept(t, h, target), "revoked U6 must restore the U4 membership")
	assert.Equal(t, "u6reauth_target", h.username(t, target), "revoked U6 must not touch IAM")
	w := h.updateWorkflowByKey(t, actor, "upd-key-u6reauth")
	assert.Equal(t, adminbff.WorkflowRejected, w.State)
	assert.Equal(t, adminbff.CompensationSucceeded, w.CompensationState)
	assert.False(t, h.subjectActive(t, w.OperationID))
}

func TestUpdateUser_WorkerResolveCommittedSucceeds(t *testing.T) {
	managed := &fakeManaged{failUpdateAfterCommit: true}
	iamReceipts := &fakeReceipts{failNext: true}
	h := newHarnessWithParts(t, managed, iamReceipts, nil, nil, nil)
	actor := h.seedActor(t, "actor_upd_wkcom")
	role := h.seedRole(t, "t053_role20", "t053_role_code20")
	dept1 := h.seedDepartment(t, "t053_dept_wkcom")
	target := h.seedTargetUser(t, "wkcom_target", role, &dept1)

	// No password: an unresolved U6 leaves the workflow running for the
	// resume machine instead of parking.
	_, err := h.svc.UpdateUser(context.Background(), op(actor, "upd-key-wkcom"),
		updateParams(target, "wkcom_target", []int64{role}, &dept1, ""))
	require.ErrorIs(t, err, adminbff.ErrDependencyUnavailable)
	w := h.updateWorkflowByKey(t, actor, "upd-key-wkcom")
	assert.Equal(t, adminbff.WorkflowRunning, w.State, "no-credential unknown outcome leaves running")
	assert.Equal(t, adminbff.StepOrganizationUpdated, w.CurrentStep)

	// The worker reclaims: resolve-only with the persisted fingerprint —
	// committed -> succeed, no restore.
	h.expireLease(t, w.OperationID)
	res, rerr := h.svc.ResumeWorkflow(context.Background(), w.OperationID)
	require.NoError(t, rerr)
	assert.True(t, res.Terminal)
	assert.Equal(t, adminbff.WorkflowSucceeded, res.State)
	assert.Equal(t, "wkcom_target", h.username(t, target))
	assert.Equal(t, int64(1), h.countReceiptsByCommand(t, "iam_command_receipts", w.OperationID, "update_managed_user"))
	assert.Equal(t, int64(0), h.countReceiptsByCommand(t, "organization_command_receipts", w.OperationID, "restore_user_department"), "committed U6 never restores")
}

func TestUpdateUser_WorkerResolveAbsentRestoresAndRejects(t *testing.T) {
	managed := &fakeManaged{failUpdateBeforeCommit: true}
	iamReceipts := &fakeReceipts{failNext: true} // client resolve fails -> leave running
	h := newHarnessWithParts(t, managed, iamReceipts, nil, nil, nil)
	actor := h.seedActor(t, "actor_upd_wkabs")
	role := h.seedRole(t, "t053_role21", "t053_role_code21")
	dept1 := h.seedDepartment(t, "t053_dept_wkabs")
	dept2 := h.seedDepartment(t, "t053_dept_wkabs2")
	target := h.seedTargetUser(t, "wkabs_target", role, &dept1)

	_, err := h.svc.UpdateUser(context.Background(), op(actor, "upd-key-wkabs"),
		updateParams(target, "wkabs_target", []int64{role}, &dept2, ""))
	require.ErrorIs(t, err, adminbff.ErrDependencyUnavailable)
	w := h.updateWorkflowByKey(t, actor, "upd-key-wkabs")
	assert.Equal(t, adminbff.WorkflowRunning, w.State)

	// Worker resolves: absent receipt proves the U6 non-commit -> restore the
	// membership and reject. The machine surfaces the rejection cause.
	h.expireLease(t, w.OperationID)
	res, rerr := h.svc.ResumeWorkflow(context.Background(), w.OperationID)
	require.ErrorIs(t, rerr, adminbff.ErrDependencyTimeout, "the worker reports the U6 rejection cause")
	assert.True(t, res.Terminal)
	assert.Equal(t, adminbff.WorkflowRejected, res.State)
	assert.Equal(t, dept1, *mustDept(t, h, target), "worker restore returns the previous department")
	assert.Equal(t, "wkabs_target", h.username(t, target), "IAM untouched")
	assert.Equal(t, int64(0), h.countReceipts(t, "iam_command_receipts", w.OperationID))
}

func TestUpdateUser_WorkerParksBeforeOrganizationUpdated(t *testing.T) {
	h := newHarness(t)
	actor := h.seedActor(t, "actor_upd_wkpark")
	role := h.seedRole(t, "t053_role22", "t053_role_code22")
	target := h.seedTargetUser(t, "wkpark_target", role, nil)

	// Simulate a crash at each pre-U6 durable step: the worker cannot decide
	// park-vs-reject (no request fields, no password-ness) and cannot compute
	// the U6 fingerprint — park awaiting input for the same-key client retry.
	for _, step := range []adminbff.StepUpdate{
		{Step: adminbff.StepIamValidated, SubjectUserID: &target, ExpectedIamVersion: ptr(h.userVersion(t, target))},
		{Step: adminbff.StepOrganizationRead},
	} {
		key := "upd-key-wkpark-" + step.Step
		w := adminbff.Workflow{
			OperationID:        uuid.NewString(),
			OperationType:      adminbff.OperationTypeUpdateUser,
			IdempotencyKey:     &key,
			RequestFingerprint: "fp-" + key,
			ActorUserID:        actor,
			State:              adminbff.WorkflowPending,
			CurrentStep:        adminbff.StepCreated,
			RetryDeadlineAt:    time.Now().Add(24 * time.Hour),
		}
		require.NoError(t, h.work.CreateWorkflow(context.Background(), w))
		claimed, cerr := h.work.ClaimWorkflowByOperationID(context.Background(), w.OperationID, "worker", 30*time.Second, time.Now())
		require.NoError(t, cerr)
		require.NoError(t, h.work.PersistStep(context.Background(), w.OperationID, *claimed.ClaimToken, step))
		if step.SubjectUserID != nil {
			require.NoError(t, h.work.InsertWorkflowSubject(context.Background(), adminbff.WorkflowSubject{
				OperationID: w.OperationID, SubjectUserID: target, ExpectedIamVersion: *step.ExpectedIamVersion,
			}))
		}
		h.expireLease(t, w.OperationID)

		res, rerr := h.svc.ResumeWorkflow(context.Background(), w.OperationID)
		require.NoError(t, rerr)
		assert.False(t, res.Terminal)
		got := h.updateWorkflowByKey(t, actor, key)
		assert.Equal(t, adminbff.WorkflowAwaitingClientInput, got.State, "worker parks at %s", step.Step)
		assert.Equal(t, step.Step, got.CurrentStep, "the durable step survives the park")
		if step.SubjectUserID != nil {
			assert.True(t, h.subjectActive(t, w.OperationID), "the park keeps the exclusion")
		}
	}
}

// --- D0–D5 delete saga ----------------------------------------------------------------

func TestDeleteUsers_SuccessMultiTarget(t *testing.T) {
	h := newHarness(t)
	actor := h.seedActor(t, "actor_del_ok")
	role := h.seedRole(t, "t054_role", "t054_role_code")
	u1 := h.seedTargetUser(t, "del_ok_1", role, nil)
	u2 := h.seedTargetUser(t, "del_ok_2", role, nil)
	before1, before2 := h.userVersion(t, u1), h.userVersion(t, u2)

	_, err := h.svc.DeleteUsers(context.Background(), op(actor, "del-key-1"), deleteParams(u1, u2))
	require.NoError(t, err)

	assert.Zero(t, h.countUsersWithUsername(t, "del_ok_1"), "D3 must delete the user")
	assert.Zero(t, h.countUsersWithUsername(t, "del_ok_2"))
	w := h.deleteWorkflowByKey(t, actor, "del-key-1")
	assert.Equal(t, adminbff.WorkflowSucceeded, w.State)
	assert.Equal(t, adminbff.StepDeleteValidated, w.CurrentStep, "D2 is the last durable step")
	subs := h.subjectRows(t, w.OperationID)
	require.Len(t, subs, 2)
	for _, s := range subs {
		assert.False(t, s.Active, "D5 must release the exclusion")
		require.NotNil(t, s.ResultingTombstoneVersion)
		if s.SubjectUserID == u1 {
			assert.Equal(t, before1+1, *s.ResultingTombstoneVersion, "tombstone = prior version + 1")
		} else {
			assert.Equal(t, before2+1, *s.ResultingTombstoneVersion)
		}
	}
	assert.Equal(t, int64(1), h.countUserDeletedEvents(t, u1), "exactly one iam.user.deleted event per user")
	assert.Equal(t, int64(1), h.countUserDeletedEvents(t, u2))
	assert.Equal(t, int64(1), h.countReceiptsByCommand(t, "iam_command_receipts", w.OperationID, "delete_users"),
		"one batch receipt for the whole batch")

	// The exclusion is released: a second delete of the same user is a
	// USER_NOT_FOUND rejection, never OPERATION_IN_PROGRESS.
	_, err = h.svc.DeleteUsers(context.Background(), op(actor, "del-key-1b"), deleteParams(u1))
	require.ErrorIs(t, err, adminbff.ErrUserNotFound)
}

func TestDeleteUsers_SameKeyReplayNoDuplicateEvents(t *testing.T) {
	h := newHarness(t)
	actor := h.seedActor(t, "actor_del_replay")
	role := h.seedRole(t, "t054_role2", "t054_role_code2")
	u1 := h.seedTargetUser(t, "del_replay_1", role, nil)
	u2 := h.seedTargetUser(t, "del_replay_2", role, nil)

	_, err := h.svc.DeleteUsers(context.Background(), op(actor, "del-key-replay"), deleteParams(u1, u2))
	require.NoError(t, err)
	w := h.deleteWorkflowByKey(t, actor, "del-key-replay")

	// Same key + same normalized request (reordered): replay the succeeded
	// workflow with zero side effects.
	_, err = h.svc.DeleteUsers(context.Background(), op(actor, "del-key-replay"), deleteParams(u2, u1))
	require.NoError(t, err)
	assert.Equal(t, int64(1), h.countUserDeletedEvents(t, u1), "replay must not emit a second event")
	assert.Equal(t, int64(1), h.countUserDeletedEvents(t, u2))
	assert.Equal(t, int64(1), h.countReceipts(t, "iam_command_receipts", w.OperationID))
	w2 := h.deleteWorkflowByKey(t, actor, "del-key-replay")
	assert.Equal(t, w.OperationID, w2.OperationID, "the same logical workflow")
}

func TestDeleteUsers_DuplicateAndUnsortedTargetsNormalized(t *testing.T) {
	h := newHarness(t)
	actor := h.seedActor(t, "actor_del_norm")
	role := h.seedRole(t, "t054_role3", "t054_role_code3")
	u1 := h.seedTargetUser(t, "del_norm_1", role, nil)
	u2 := h.seedTargetUser(t, "del_norm_2", role, nil)

	_, err := h.svc.DeleteUsers(context.Background(), op(actor, "del-key-norm"), deleteParams(u2, u1, u2))
	require.NoError(t, err)
	assert.Zero(t, h.countUsersWithUsername(t, "del_norm_1"))
	assert.Zero(t, h.countUsersWithUsername(t, "del_norm_2"))
	assert.Equal(t, int64(1), h.countUserDeletedEvents(t, u1), "duplicates must not duplicate events")
	assert.Equal(t, int64(1), h.countUserDeletedEvents(t, u2))
	w := h.deleteWorkflowByKey(t, actor, "del-key-norm")
	assert.Len(t, h.subjectRows(t, w.OperationID), 2, "one subject row per distinct user")
	assert.Equal(t, int64(1), h.countReceiptsByCommand(t, "iam_command_receipts", w.OperationID, "delete_users"))
}

func TestDeleteUsers_SameKeyDifferentSetConflicts(t *testing.T) {
	h := newHarness(t)
	actor := h.seedActor(t, "actor_del_conf")
	role := h.seedRole(t, "t054_role4", "t054_role_code4")
	u1 := h.seedTargetUser(t, "del_conf_1", role, nil)
	u2 := h.seedTargetUser(t, "del_conf_2", role, nil)

	_, err := h.svc.DeleteUsers(context.Background(), op(actor, "del-key-conf"), deleteParams(u1))
	require.NoError(t, err)
	_, err = h.svc.DeleteUsers(context.Background(), op(actor, "del-key-conf"), deleteParams(u2))
	require.ErrorIs(t, err, adminbff.ErrIdempotencyConflict, "the same key with a different target set")
	assert.Equal(t, int64(1), h.countUsersWithUsername(t, "del_conf_2"), "the conflicting request must not delete")
	assert.Equal(t, int64(0), h.countUserDeletedEvents(t, u2))
}

func TestDeleteUsers_MissingTargetRejectsWholeBatch(t *testing.T) {
	h := newHarness(t)
	actor := h.seedActor(t, "actor_del_missing")
	role := h.seedRole(t, "t054_role5", "t054_role_code5")
	u1 := h.seedTargetUser(t, "del_missing_1", role, nil)

	_, err := h.svc.DeleteUsers(context.Background(), op(actor, "del-key-missing"), deleteParams(u1, 999999))
	require.ErrorIs(t, err, adminbff.ErrUserNotFound)
	assert.Equal(t, int64(1), h.countUsersWithUsername(t, "del_missing_1"), "a missing target means zero deletions")
	w := h.deleteWorkflowByKey(t, actor, "del-key-missing")
	assert.Equal(t, adminbff.WorkflowRejected, w.State)
	assert.Empty(t, h.subjectRows(t, w.OperationID), "D1 rejects before any subject row exists")
	assert.Equal(t, int64(0), h.countReceipts(t, "iam_command_receipts", w.OperationID))

	// Same key replays the stored rejection.
	_, err = h.svc.DeleteUsers(context.Background(), op(actor, "del-key-missing"), deleteParams(u1, 999999))
	require.ErrorIs(t, err, adminbff.ErrUserNotFound)
}

func TestDeleteUsers_SubjectExcludedRejectsWithOperationInProgress(t *testing.T) {
	membership := &fakeMembership{failRead: true} // parks the update at iam_validated holding the exclusion
	h := newHarnessWithParts(t, nil, nil, membership, nil, nil)
	actor := h.seedActor(t, "actor_del_excl")
	role := h.seedRole(t, "t054_role6", "t054_role_code6")
	u1 := h.seedTargetUser(t, "del_excl_1", role, nil)

	// A concurrent update workflow parks at iam_validated, holding the subject exclusion.
	_, err := h.svc.UpdateUser(context.Background(), op(actor, "del-excl-upd"), updateParams(u1, "del_excl_1", []int64{role}, nil, ""))
	require.ErrorIs(t, err, adminbff.ErrDependencyTimeout)
	upd := h.updateWorkflowByKey(t, actor, "del-excl-upd")
	assert.True(t, h.subjectActive(t, upd.OperationID), "the update workflow holds the exclusion")

	_, err = h.svc.DeleteUsers(context.Background(), op(actor, "del-key-excl"), deleteParams(u1))
	require.ErrorIs(t, err, adminbff.ErrOperationInProgress)
	assert.Equal(t, int64(1), h.countUsersWithUsername(t, "del_excl_1"), "zero users deleted")
	delw := h.deleteWorkflowByKey(t, actor, "del-key-excl")
	assert.Equal(t, adminbff.WorkflowRejected, delw.State)
	assert.Empty(t, h.subjectRows(t, delw.OperationID), "D2 rolls the whole insert set back")
	assert.True(t, h.subjectActive(t, upd.OperationID), "the other workflow's exclusion is untouched")
	assert.Equal(t, int64(0), h.countReceipts(t, "iam_command_receipts", delw.OperationID))

	// Same key replays the stored OPERATION_IN_PROGRESS.
	_, err = h.svc.DeleteUsers(context.Background(), op(actor, "del-key-excl"), deleteParams(u1))
	require.ErrorIs(t, err, adminbff.ErrOperationInProgress)
}

func TestDeleteUsers_D3TimeoutCommittedContinuesInRequest(t *testing.T) {
	managed := &fakeManaged{failDeleteAfterCommit: 1} // D3 commits, then times out
	h := newHarnessWithParts(t, managed, nil, nil, nil, nil)
	actor := h.seedActor(t, "actor_del_tc")
	role := h.seedRole(t, "t054_role7", "t054_role_code7")
	u1 := h.seedTargetUser(t, "del_tc_1", role, nil)
	u2 := h.seedTargetUser(t, "del_tc_2", role, nil)

	_, err := h.svc.DeleteUsers(context.Background(), op(actor, "del-key-tc"), deleteParams(u1, u2))
	require.NoError(t, err, "the committed batch is resolved and D5 completes in the same request")

	assert.Zero(t, h.countUsersWithUsername(t, "del_tc_1"))
	assert.Zero(t, h.countUsersWithUsername(t, "del_tc_2"))
	assert.Equal(t, int64(1), h.countUserDeletedEvents(t, u1), "one event per user, never two")
	assert.Equal(t, int64(1), h.countUserDeletedEvents(t, u2))
	w := h.deleteWorkflowByKey(t, actor, "del-key-tc")
	assert.Equal(t, adminbff.WorkflowSucceeded, w.State)
	assert.Equal(t, int64(1), h.countReceiptsByCommand(t, "iam_command_receipts", w.OperationID, "delete_users"))
	for _, s := range h.subjectRows(t, w.OperationID) {
		assert.False(t, s.Active, "D5 releases the exclusions")
	}
}

func TestDeleteUsers_D3TimeoutNonCommitReissuesInRequest(t *testing.T) {
	managed := &fakeManaged{failDeleteBeforeCommit: 1} // first D3 attempt times out without committing
	h := newHarnessWithParts(t, managed, nil, nil, nil, nil)
	actor := h.seedActor(t, "actor_del_tnc")
	role := h.seedRole(t, "t054_role8", "t054_role_code8")
	u1 := h.seedTargetUser(t, "del_tnc_1", role, nil)

	_, err := h.svc.DeleteUsers(context.Background(), op(actor, "del-key-tnc"), deleteParams(u1))
	require.NoError(t, err, "resolve proves the non-commit and the one re-issue completes the batch")

	assert.Zero(t, h.countUsersWithUsername(t, "del_tnc_1"))
	assert.Equal(t, int64(1), h.countUserDeletedEvents(t, u1))
	w := h.deleteWorkflowByKey(t, actor, "del-key-tnc")
	assert.Equal(t, adminbff.WorkflowSucceeded, w.State)
	assert.Equal(t, int64(1), h.countReceiptsByCommand(t, "iam_command_receipts", w.OperationID, "delete_users"))
}

func TestDeleteUsers_D3UnresolvedLeaveRunningWorkerCompletes(t *testing.T) {
	managed := &fakeManaged{failDeleteBeforeCommit: 100} // both attempts time out without committing
	h := newHarnessWithParts(t, managed, nil, nil, nil, nil)
	actor := h.seedActor(t, "actor_del_wk")
	role := h.seedRole(t, "t054_role9", "t054_role_code9")
	u1 := h.seedTargetUser(t, "del_wk_1", role, nil)
	u2 := h.seedTargetUser(t, "del_wk_2", role, nil)

	_, err := h.svc.DeleteUsers(context.Background(), op(actor, "del-key-wk"), deleteParams(u1, u2))
	require.ErrorIs(t, err, adminbff.ErrDependencyTimeout)
	assert.Equal(t, int64(1), h.countUsersWithUsername(t, "del_wk_1"), "no delete while the outcome is unknown")
	w := h.deleteWorkflowByKey(t, actor, "del-key-wk")
	assert.Equal(t, adminbff.WorkflowRunning, w.State, "an unknown outcome leaves the workflow running")
	assert.Equal(t, adminbff.StepDeleteValidated, w.CurrentStep)
	subs := h.subjectRows(t, w.OperationID)
	require.Len(t, subs, 2)
	for _, s := range subs {
		assert.True(t, s.Active, "the exclusion stays held while running")
	}

	// The worker reclaims: re-issue D3 (now un-faked), D5, succeed.
	managed.failDeleteBeforeCommit = 0
	h.expireLease(t, w.OperationID)
	res, rerr := h.svc.ResumeWorkflow(context.Background(), w.OperationID)
	require.NoError(t, rerr)
	require.True(t, res.Terminal)
	assert.Equal(t, adminbff.WorkflowSucceeded, res.State)
	assert.Zero(t, h.countUsersWithUsername(t, "del_wk_1"))
	assert.Zero(t, h.countUsersWithUsername(t, "del_wk_2"))
	assert.Equal(t, int64(1), h.countUserDeletedEvents(t, u1), "the re-issue replays, no second event")
	assert.Equal(t, int64(1), h.countUserDeletedEvents(t, u2))
	assert.Equal(t, int64(1), h.countReceiptsByCommand(t, "iam_command_receipts", w.OperationID, "delete_users"))
}

func TestDeleteUsers_ProvenFailureRejectsAndReleases(t *testing.T) {
	managed := &fakeManaged{failDeleteReject: true} // proven rejection, no side effect
	h := newHarnessWithParts(t, managed, nil, nil, nil, nil)
	actor := h.seedActor(t, "actor_del_rej")
	role := h.seedRole(t, "t054_role10", "t054_role_code10")
	u1 := h.seedTargetUser(t, "del_rej_1", role, nil)
	u2 := h.seedTargetUser(t, "del_rej_2", role, nil)

	_, err := h.svc.DeleteUsers(context.Background(), op(actor, "del-key-rej"), deleteParams(u1, u2))
	require.ErrorIs(t, err, adminbff.ErrUserNotFound)
	assert.Equal(t, int64(1), h.countUsersWithUsername(t, "del_rej_1"), "zero users deleted")
	w := h.deleteWorkflowByKey(t, actor, "del-key-rej")
	assert.Equal(t, adminbff.WorkflowRejected, w.State)
	for _, s := range h.subjectRows(t, w.OperationID) {
		assert.False(t, s.Active, "a proven failure releases the exclusions")
	}
	assert.Equal(t, int64(0), h.countReceipts(t, "iam_command_receipts", w.OperationID))

	// Same key replays the stored rejection.
	_, err = h.svc.DeleteUsers(context.Background(), op(actor, "del-key-rej"), deleteParams(u1, u2))
	require.ErrorIs(t, err, adminbff.ErrUserNotFound)
}

func TestDeleteUsers_D3ReauthRevokedRejectsAndReleases(t *testing.T) {
	identity := &fakeIdentity{grantedChecks: 10} // the create-seed reauth passes
	h := newHarnessWithParts(t, nil, nil, nil, nil, identity)
	actor := h.seedActor(t, "actor_del_reauth")
	role := h.seedRole(t, "t054_role11", "t054_role_code11")
	u1 := h.seedTargetUser(t, "del_reauth_1", role, nil)
	identity.grantedChecks = identity.calls // the next users.write check (D3) is denied

	_, err := h.svc.DeleteUsers(context.Background(), op(actor, "del-key-reauth"), deleteParams(u1))
	require.ErrorIs(t, err, adminbff.ErrForbidden, "the revoked actor may not run D3")
	assert.Equal(t, int64(1), h.countUsersWithUsername(t, "del_reauth_1"), "the revoked actor deletes nothing")
	w := h.deleteWorkflowByKey(t, actor, "del-key-reauth")
	assert.Equal(t, adminbff.WorkflowRejected, w.State)
	for _, s := range h.subjectRows(t, w.OperationID) {
		assert.False(t, s.Active, "the rejection releases the exclusions")
	}
	assert.Equal(t, int64(0), h.countReceipts(t, "iam_command_receipts", w.OperationID))
}

func TestDeleteUsers_WorkerParksAtCreatedAndRetryCompletes(t *testing.T) {
	h := newHarness(t)
	actor := h.seedActor(t, "actor_del_wkpark")
	role := h.seedRole(t, "t054_role12", "t054_role_code12")
	u1 := h.seedTargetUser(t, "del_wkpark_1", role, nil)

	// Simulate a crash at D0: nothing durable (no subject rows) — the worker
	// parks awaiting input; the same-key client retry carries D1–D5.
	key := "del-key-wkpark"
	fp, ferr := consistency.FingerprintV1(adminbff.OperationTypeDeleteUser, actor, map[string]any{
		"user_ids": consistency.SortedIDs([]int64{u1}),
	})
	require.NoError(t, ferr)
	w := adminbff.Workflow{
		OperationID:        uuid.NewString(),
		OperationType:      adminbff.OperationTypeDeleteUser,
		IdempotencyKey:     &key,
		RequestFingerprint: fp,
		ActorUserID:        actor,
		State:              adminbff.WorkflowPending,
		CurrentStep:        adminbff.StepCreated,
		RetryDeadlineAt:    time.Now().Add(24 * time.Hour),
	}
	require.NoError(t, h.work.CreateWorkflow(context.Background(), w))

	res, rerr := h.svc.ResumeWorkflow(context.Background(), w.OperationID)
	require.NoError(t, rerr)
	assert.False(t, res.Terminal)
	got := h.deleteWorkflowByKey(t, actor, key)
	assert.Equal(t, adminbff.WorkflowAwaitingClientInput, got.State, "worker parks a D0-only workflow")

	// Same-key retry before the deadline continues and completes the delete.
	_, err := h.svc.DeleteUsers(context.Background(), op(actor, key), deleteParams(u1))
	require.NoError(t, err)
	assert.Zero(t, h.countUsersWithUsername(t, "del_wkpark_1"))
	got = h.deleteWorkflowByKey(t, actor, key)
	assert.Equal(t, adminbff.WorkflowSucceeded, got.State)
	assert.Equal(t, int64(1), h.countUserDeletedEvents(t, u1))
}

func TestDeleteUsers_AwaitingExpiryFinalizesOperationExpired(t *testing.T) {
	h := newHarness(t)
	actor := h.seedActor(t, "actor_del_exp")
	role := h.seedRole(t, "t054_role13", "t054_role_code13")
	u1 := h.seedTargetUser(t, "del_exp_1", role, nil)

	// Park a D0-only workflow awaiting input, then let the immutable input
	// deadline pass — the expiry finalizer terminally rejects it.
	key := "del-key-exp"
	fp, ferr := consistency.FingerprintV1(adminbff.OperationTypeDeleteUser, actor, map[string]any{
		"user_ids": consistency.SortedIDs([]int64{u1}),
	})
	require.NoError(t, ferr)
	w := adminbff.Workflow{
		OperationID:        uuid.NewString(),
		OperationType:      adminbff.OperationTypeDeleteUser,
		IdempotencyKey:     &key,
		RequestFingerprint: fp,
		ActorUserID:        actor,
		State:              adminbff.WorkflowPending,
		CurrentStep:        adminbff.StepCreated,
		RetryDeadlineAt:    time.Now().Add(24 * time.Hour),
	}
	require.NoError(t, h.work.CreateWorkflow(context.Background(), w))
	res, rerr := h.svc.ResumeWorkflow(context.Background(), w.OperationID)
	require.NoError(t, rerr)
	assert.False(t, res.Terminal)
	assert.Equal(t, adminbff.WorkflowAwaitingClientInput, h.deleteWorkflowByKey(t, actor, key).State)

	h.expireClientInput(t, w.OperationID)
	res, rerr = h.svc.ResumeWorkflow(context.Background(), w.OperationID)
	require.NoError(t, rerr)
	require.True(t, res.Terminal)
	assert.Equal(t, adminbff.WorkflowRejected, res.State)

	// The client's same-key retry replays OPERATION_EXPIRED; nothing was deleted.
	_, err := h.svc.DeleteUsers(context.Background(), op(actor, key), deleteParams(u1))
	require.ErrorIs(t, err, adminbff.ErrOperationExpired)
	assert.Equal(t, int64(1), h.countUsersWithUsername(t, "del_exp_1"))
	assert.Equal(t, int64(0), h.countUserDeletedEvents(t, u1))
}

// T059 Checkpoint E security: workflow rows contain no password/token/hash,
// and the deletion outbox payload carries only the opaque aggregate id —
// never username/email/credentials. The create credential existed only in
// the HTTP request; everything durable is secret-free.
func TestWorkflow_NoCredentialsOrPIIInDurableState(t *testing.T) {
	h := newHarness(t)
	actor := h.seedActor(t, "actor_nopii")
	role := h.seedRole(t, "t059_role_nopii", "t059_role_code_nopii")
	dept := h.seedDepartment(t, "t059_dept_nopii")
	const secret = "S3cret-PII-T059!"
	const probeUsername = "pii_probe_user"

	res, err := h.svc.CreateUser(context.Background(), op(actor, "pii-create"), adminbff.CreateUserParams{
		Username: probeUsername, Password: secret, RoleIDs: []int64{role}, DepartmentID: &dept,
	})
	require.NoError(t, err)
	w := h.workflowByKey(t, actor, "pii-create")

	// No credential may be durable in the workflow row or any participant
	// receipt (the org set_user_department receipt is covered too).
	var hits int64
	require.NoError(t, h.db.QueryRow(context.Background(), `
		SELECT count(*) FROM (
			SELECT row_to_json(admin_workflows)::text AS j FROM admin_workflows WHERE operation_id = $1
			UNION ALL
			SELECT row_to_json(iam_command_receipts)::text FROM iam_command_receipts WHERE operation_id = $1
			UNION ALL
			SELECT row_to_json(organization_command_receipts)::text FROM organization_command_receipts WHERE operation_id = $1
		) t WHERE j LIKE '%' || $2 || '%'`, w.OperationID, secret).Scan(&hits))
	assert.Zero(t, hits, "durable workflow/receipt state must never contain the credential")

	// The deletion event payload carries only the opaque user id.
	_, err = h.svc.DeleteUsers(context.Background(), op(actor, "pii-delete"), deleteParams(res.UserID))
	require.NoError(t, err)
	var payload string
	require.NoError(t, h.db.QueryRow(context.Background(),
		"SELECT payload::text FROM iam_outbox_events WHERE event_type = 'iam.user.deleted' AND aggregate_id = $1",
		fmt.Sprintf("%d", res.UserID)).Scan(&payload))
	assert.Equal(t, fmt.Sprintf(`{"user_id": %d}`, res.UserID), payload, "deletion event carries only the opaque user id")
	for _, probe := range []string{probeUsername, secret, "pii_probe_user@example.com"} {
		assert.NotContains(t, payload, probe, "deletion event payload must not contain PII")
	}
}

// --- T055: compensation reclaim + manual recovery --------------------------------

// subjectActiveFor reads the exclusion flag of one subject row directly.
func (h *harness) subjectActiveFor(t *testing.T, operationID string, userID int64) bool {
	t.Helper()
	var active bool
	require.NoError(t, h.db.QueryRow(context.Background(),
		"SELECT active FROM admin_workflow_subjects WHERE operation_id = $1 AND subject_user_id = $2",
		operationID, userID).Scan(&active))
	return active
}

// workflowStateAndBudget reads the raw state, compensation counter and last
// error code of one workflow (time-control truth checks, as in T051).
func (h *harness) workflowStateAndBudget(t *testing.T, operationID string) (state, compState string, compAttempts int, lastError *string) {
	t.Helper()
	var le pgtype.Text
	err := h.db.QueryRow(context.Background(),
		"SELECT state, compensation_state, compensation_attempt_count, last_error_code FROM admin_workflows WHERE operation_id = $1",
		operationID).Scan(&state, &compState, &compAttempts, &le)
	require.NoError(t, err)
	if le.Valid {
		lastError = &le.String
	}
	return state, compState, compAttempts, lastError
}

func (h *harness) countRecoveryActions(t *testing.T, operationID string) int {
	t.Helper()
	var n int
	require.NoError(t, h.db.QueryRow(context.Background(),
		"SELECT count(*) FROM admin_workflow_recovery_actions WHERE operation_id = $1", operationID).Scan(&n))
	return n
}

// createWorkflowRow persists a fresh pending workflow row with the given
// identity (no side effects — the T055 resume/recovery fixtures drive the
// workflow state through the store ports themselves).
func createWorkflowRow(t *testing.T, h *harness, operationType string, actorID int64) adminbff.Workflow {
	t.Helper()
	w := adminbff.Workflow{
		OperationID:        uuid.NewString(),
		OperationType:      operationType,
		RequestFingerprint: "fp-t055",
		ActorUserID:        actorID,
		State:              adminbff.WorkflowPending,
		CurrentStep:        adminbff.StepCreated,
		RetryDeadlineAt:    time.Now().Add(24 * time.Hour),
	}
	require.NoError(t, h.work.CreateWorkflow(context.Background(), w))
	return w
}

// intoCompensating claims a pending row, persists a subject + expected
// version, begins compensation and expires the lease — a compensating row a
// dead owner abandoned.
func intoCompensating(t *testing.T, h *harness, w adminbff.Workflow, subjectUserID, expectedIamVersion int64) {
	t.Helper()
	ctx := context.Background()
	claimed, err := h.work.ClaimWorkflowByOperationID(ctx, w.OperationID, "worker-1", 30*time.Second, time.Now())
	require.NoError(t, err)
	require.NoError(t, h.work.PersistStep(ctx, w.OperationID, *claimed.ClaimToken, adminbff.StepUpdate{
		Step:               adminbff.StepOrganizationAssigned,
		SubjectUserID:      &subjectUserID,
		ExpectedIamVersion: &expectedIamVersion,
	}))
	require.NoError(t, h.work.BeginCompensation(ctx, w.OperationID, *claimed.ClaimToken))
	h.expireLease(t, w.OperationID)
}

// intoUpdateCompensating claims a pending row and persists a full update
// compensation state (U4 applied membership + U3 previous state) with a dead
// owner — the delayed-restore/ABA fixtures of T059. No subject exclusion row
// is inserted: the workflow died before claiming compensation, so a newer
// independent update may proceed in the meantime.
func intoUpdateCompensating(t *testing.T, h *harness, w adminbff.Workflow, subjectUserID, expectedIamVersion int64,
	appliedDepartmentID *int64, appliedMembershipVersion int64, previousDepartmentID *int64, previousMembershipVersion int64) {
	t.Helper()
	ctx := context.Background()
	claimed, err := h.work.ClaimWorkflowByOperationID(ctx, w.OperationID, "worker-1", 30*time.Second, time.Now())
	require.NoError(t, err)
	require.NoError(t, h.work.PersistStep(ctx, w.OperationID, *claimed.ClaimToken, adminbff.StepUpdate{
		Step:                      adminbff.StepOrganizationUpdated,
		SubjectUserID:             &subjectUserID,
		ExpectedIamVersion:        &expectedIamVersion,
		AppliedDepartmentID:       appliedDepartmentID,
		AppliedMembershipVersion:  &appliedMembershipVersion,
		PreviousDepartmentID:      previousDepartmentID,
		PreviousMembershipVersion: &previousMembershipVersion,
	}))
	require.NoError(t, h.work.BeginCompensation(ctx, w.OperationID, *claimed.ClaimToken))
	h.expireLease(t, w.OperationID)
}

// intoFailedManual parks a fresh pending row in failed_manual via
// budget/deadline exhaustion (the store port the exhaustion path uses).
func intoFailedManual(t *testing.T, h *harness, w adminbff.Workflow) {
	t.Helper()
	ctx := context.Background()
	_, err := h.db.Exec(ctx,
		"UPDATE admin_workflows SET retry_deadline_at = now() - interval '1 minute', created_at = now() - interval '2 minutes' WHERE operation_id = $1",
		w.OperationID)
	require.NoError(t, err)
	exhausted, err := h.work.ExhaustToFailedManual(ctx, w.OperationID, "attempt_budget_exhausted")
	require.NoError(t, err)
	require.True(t, exhausted)
}

// intoFailedManualWithSubject parks a claimed row with a persisted subject +
// expected version AND an active exclusion row in failed_manual via
// exhaustion — the recovery fixtures whose compensate/receipt resolution
// needs the workflow-persisted fields and whose finalize must release the
// exclusion.
func intoFailedManualWithSubject(t *testing.T, h *harness, w adminbff.Workflow, subjectUserID, expectedIamVersion int64) {
	t.Helper()
	ctx := context.Background()
	claimed, err := h.work.ClaimWorkflowByOperationID(ctx, w.OperationID, "worker-1", 30*time.Second, time.Now())
	require.NoError(t, err)
	require.NoError(t, h.work.PersistStep(ctx, w.OperationID, *claimed.ClaimToken, adminbff.StepUpdate{
		Step:               adminbff.StepOrganizationAssigned,
		SubjectUserID:      &subjectUserID,
		ExpectedIamVersion: &expectedIamVersion,
	}))
	require.NoError(t, h.work.InsertWorkflowSubject(ctx, adminbff.WorkflowSubject{
		OperationID: w.OperationID, SubjectUserID: subjectUserID, ExpectedIamVersion: expectedIamVersion,
	}))
	h.expireLease(t, w.OperationID)
	_, err = h.db.Exec(ctx,
		"UPDATE admin_workflows SET retry_deadline_at = now() - interval '1 minute', created_at = now() - interval '2 minutes' WHERE operation_id = $1",
		w.OperationID)
	require.NoError(t, err)
	exhausted, err := h.work.ExhaustToFailedManual(ctx, w.OperationID, "attempt_budget_exhausted")
	require.NoError(t, err)
	require.True(t, exhausted)
}

func TestResumeWorkflow_CompensatingCommittedReceiptFinalizes(t *testing.T) {
	iamReceipts := &fakeReceipts{forceCommitted: true}
	h := newHarnessWithParts(t, nil, iamReceipts, nil, nil, nil)
	actor := h.seedActor(t, "actor_t055a")
	w := createWorkflowRow(t, h, adminbff.OperationTypeCreateUser, actor)
	intoCompensating(t, h, w, 42, 5)
	ctx := context.Background()
	require.NoError(t, h.work.InsertWorkflowSubject(ctx, adminbff.WorkflowSubject{
		OperationID: w.OperationID, SubjectUserID: 42, ExpectedIamVersion: 5,
	}))
	require.True(t, h.subjectActiveFor(t, w.OperationID, 42), "exclusion active before compensation completes")

	// The last compensation command's receipt is provably committed: the
	// reclaim must finalize rejected/OPERATION_EXPIRED, NOT re-issue anything
	// and NOT consume the forward budget.
	outcome, err := h.svc.ResumeWorkflow(ctx, w.OperationID)
	require.NoError(t, err)
	require.True(t, outcome.Terminal)
	assert.Equal(t, adminbff.WorkflowRejected, outcome.State)

	state, _, compAttempts, _ := h.workflowStateAndBudget(t, w.OperationID)
	assert.Equal(t, "rejected", state)
	assert.Equal(t, 1, compAttempts, "one compensation reclaim counted, separately from forward attempts")
	assert.False(t, h.subjectActiveFor(t, w.OperationID, 42), "proven compensation releases the exclusion")
}

func TestResumeWorkflow_CompensatingAbsentReceiptRerunsCompensation(t *testing.T) {
	managed := &fakeManaged{skipCompensate: true}
	h := newHarnessWithParts(t, managed, nil, nil, nil, nil)
	actor := h.seedActor(t, "actor_t055b")
	w := createWorkflowRow(t, h, adminbff.OperationTypeCreateUser, actor)
	intoCompensating(t, h, w, 43, 6)
	ctx := context.Background()
	require.NoError(t, h.work.InsertWorkflowSubject(ctx, adminbff.WorkflowSubject{
		OperationID: w.OperationID, SubjectUserID: 43, ExpectedIamVersion: 6,
	}))

	// No receipt: the full compensation sequence re-runs (resolve-first
	// participants replay partial commits) and the workflow converges.
	outcome, err := h.svc.ResumeWorkflow(ctx, w.OperationID)
	require.NoError(t, err)
	require.True(t, outcome.Terminal)
	assert.Equal(t, adminbff.WorkflowRejected, outcome.State)

	state, compState, _, _ := h.workflowStateAndBudget(t, w.OperationID)
	assert.Equal(t, "rejected", state)
	assert.Equal(t, "succeeded", compState, "re-run compensation durably marked succeeded")
	assert.False(t, h.subjectActiveFor(t, w.OperationID, 43), "compensation completion releases the exclusion")
}

func TestResumeWorkflow_CompensatingResolveFailureLeavesCompensating(t *testing.T) {
	iamReceipts := &fakeReceipts{failNext: true}
	h := newHarnessWithParts(t, nil, iamReceipts, nil, nil, nil)
	actor := h.seedActor(t, "actor_t055c")
	w := createWorkflowRow(t, h, adminbff.OperationTypeCreateUser, actor)
	intoCompensating(t, h, w, 44, 7)
	ctx := context.Background()

	// The receipt is unresolvable: never finalize blindly and never re-issue —
	// leave compensating for a later resolve.
	_, err := h.svc.ResumeWorkflow(ctx, w.OperationID)
	require.ErrorIs(t, err, adminbff.ErrDependencyUnavailable)
	state, _, _, _ := h.workflowStateAndBudget(t, w.OperationID)
	assert.Equal(t, "compensating", state, "unresolved receipt must leave the row compensating")
}

func TestResumeWorkflow_CompensationSucceededFinalizes(t *testing.T) {
	h := newHarness(t)
	actor := h.seedActor(t, "actor_t055d")
	w := createWorkflowRow(t, h, adminbff.OperationTypeCreateUser, actor)
	intoCompensating(t, h, w, 45, 8)
	ctx := context.Background()
	require.NoError(t, h.work.InsertWorkflowSubject(ctx, adminbff.WorkflowSubject{
		OperationID: w.OperationID, SubjectUserID: 45, ExpectedIamVersion: 8,
	}))

	// The dead owner's compensation durably succeeded; only the finalize was
	// missing — the reclaim must not re-execute anything.
	claimed, err := h.work.ClaimWorkflowByOperationID(ctx, w.OperationID, "worker-2", 30*time.Second, time.Now())
	require.NoError(t, err)
	require.NoError(t, h.work.SetCompensationSucceeded(ctx, w.OperationID, *claimed.ClaimToken))
	h.expireLease(t, w.OperationID)

	outcome, err := h.svc.ResumeWorkflow(ctx, w.OperationID)
	require.NoError(t, err)
	require.True(t, outcome.Terminal)
	assert.Equal(t, adminbff.WorkflowRejected, outcome.State)
	assert.False(t, h.subjectActiveFor(t, w.OperationID, 45), "succeeded compensation finalizes and releases")
}

func TestResumeWorkflow_CompensationFailedParksForReconciliation(t *testing.T) {
	h := newHarness(t)
	actor := h.seedActor(t, "actor_t055e")
	w := createWorkflowRow(t, h, adminbff.OperationTypeCreateUser, actor)
	intoCompensating(t, h, w, 46, 9)
	ctx := context.Background()
	claimed, err := h.work.ClaimWorkflowByOperationID(ctx, w.OperationID, "worker-2", 30*time.Second, time.Now())
	require.NoError(t, err)
	require.NoError(t, h.work.SetCompensationFailed(ctx, w.OperationID, *claimed.ClaimToken))
	h.expireLease(t, w.OperationID)

	_, err = h.svc.ResumeWorkflow(ctx, w.OperationID)
	require.ErrorIs(t, err, adminbff.ErrReconciliationRequired)
	state, compState, _, lastError := h.workflowStateAndBudget(t, w.OperationID)
	assert.Equal(t, "failed_manual", state)
	assert.Equal(t, "failed", compState)
	require.NotNil(t, lastError)
	assert.Equal(t, "compensation_failed", *lastError)
}

func TestResumeWorkflow_UpdateCompensatingWithoutAppliedMembershipFinalizes(t *testing.T) {
	h := newHarness(t)
	actor := h.seedActor(t, "actor_t055f")
	w := createWorkflowRow(t, h, adminbff.OperationTypeUpdateUser, actor)
	intoCompensating(t, h, w, 47, 10)
	ctx := context.Background()

	// An update parked before U5 has no membership side effect to compensate
	// and U6 never ran: finalize directly, never resolve-then-guess.
	outcome, err := h.svc.ResumeWorkflow(ctx, w.OperationID)
	require.NoError(t, err)
	require.True(t, outcome.Terminal)
	assert.Equal(t, adminbff.WorkflowRejected, outcome.State)
	state, _, _, _ := h.workflowStateAndBudget(t, w.OperationID)
	assert.Equal(t, "rejected", state)
}

// T059 Checkpoint E: set→clear ABA followed by a delayed restore is rejected.
// A dead workflow A applied dept2 (v2) but died before compensating; a newer
// independent update clears the department (tombstone, v3); A's delayed
// restore carries the stale applied version — the version CAS rejects it
// (RECONCILIATION_REQUIRED, nothing resurrected, newer version untouched).
func TestResumeWorkflow_UpdateDelayedRestoreAfterNewerClearRejected(t *testing.T) {
	h := newHarness(t)
	actor := h.seedActor(t, "actor_t059aba")
	role := h.seedRole(t, "t059_role_aba", "t059_role_code_aba")
	dept1 := h.seedDepartment(t, "t059_dept_aba")
	dept2 := h.seedDepartment(t, "t059_dept_aba2")
	target := h.seedTargetUser(t, "aba_target", role, &dept1)

	// Newer workflow B (different key) first SETS dept2 — the state the dead
	// workflow A "applied" at membership v2.
	_, err := h.svc.UpdateUser(context.Background(), op(actor, "aba-newer-set"),
		updateParams(target, "aba_target", []int64{role}, &dept2, ""))
	require.NoError(t, err)
	applied := h.membershipVersion(t, target)

	// A's compensating row with a dead owner: applied dept2 at v2, previous
	// state dept1 at v1 (no subject exclusion — A died before claiming).
	w := createWorkflowRow(t, h, adminbff.OperationTypeUpdateUser, actor)
	intoUpdateCompensating(t, h, w, target, h.userVersion(t, target), &dept2, applied, &dept1, applied-1)

	// The set→clear ABA: B CLEARS the department (tombstone, v3) before A's
	// delayed restore runs.
	_, err = h.svc.UpdateUser(context.Background(), op(actor, "aba-newer-clear"),
		updateParams(target, "aba_target", []int64{role}, nil, ""))
	require.NoError(t, err)
	assert.Nil(t, mustDept(t, h, target), "B's clear left a versioned tombstone")
	after := h.membershipVersion(t, target)
	assert.Greater(t, after, applied)

	// A's delayed restore carries the stale applied version: rejected.
	_, rerr := h.svc.ResumeWorkflow(context.Background(), w.OperationID)
	require.ErrorIs(t, rerr, adminbff.ErrReconciliationRequired)
	assert.Nil(t, mustDept(t, h, target), "the stale restore must not resurrect dept1 or dept2")
	assert.Equal(t, after, h.membershipVersion(t, target), "the newer version is untouched")
	state, compState, _, lastError := h.workflowStateAndBudget(t, w.OperationID)
	assert.Equal(t, "failed_manual", state)
	assert.Equal(t, "failed", compState)
	require.NotNil(t, lastError)
	assert.Equal(t, "compensation_failed", *lastError)
	assert.Equal(t, int64(0), h.countReceiptsByCommand(t, "organization_command_receipts", w.OperationID, "restore_user_department"),
		"the conflict is detected before any restore command")
}

// T059 Checkpoint E: delayed compensation after a newer update. Same shape as
// the ABA test, but the newer workflow SETS dept3 instead of clearing — the
// stale restore must not clobber the newer department.
func TestResumeWorkflow_UpdateDelayedRestoreAfterNewerSetRejected(t *testing.T) {
	h := newHarness(t)
	actor := h.seedActor(t, "actor_t059abas")
	role := h.seedRole(t, "t059_role_abas", "t059_role_code_abas")
	dept1 := h.seedDepartment(t, "t059_dept_abas")
	dept2 := h.seedDepartment(t, "t059_dept_abas2")
	dept3 := h.seedDepartment(t, "t059_dept_abas3")
	target := h.seedTargetUser(t, "abas_target", role, &dept1)

	_, err := h.svc.UpdateUser(context.Background(), op(actor, "abas-set"),
		updateParams(target, "abas_target", []int64{role}, &dept2, ""))
	require.NoError(t, err)
	applied := h.membershipVersion(t, target)

	w := createWorkflowRow(t, h, adminbff.OperationTypeUpdateUser, actor)
	intoUpdateCompensating(t, h, w, target, h.userVersion(t, target), &dept2, applied, &dept1, applied-1)

	// A newer independent update SETS dept3 before A's delayed restore.
	_, err = h.svc.UpdateUser(context.Background(), op(actor, "abas-newer"),
		updateParams(target, "abas_target", []int64{role}, &dept3, ""))
	require.NoError(t, err)
	assert.Equal(t, dept3, *mustDept(t, h, target))
	after := h.membershipVersion(t, target)

	_, rerr := h.svc.ResumeWorkflow(context.Background(), w.OperationID)
	require.ErrorIs(t, rerr, adminbff.ErrReconciliationRequired)
	assert.Equal(t, dept3, *mustDept(t, h, target), "the stale restore must not clobber the newer department")
	assert.Equal(t, after, h.membershipVersion(t, target))
	state, compState, _, lastError := h.workflowStateAndBudget(t, w.OperationID)
	assert.Equal(t, "failed_manual", state)
	assert.Equal(t, "failed", compState)
	require.NotNil(t, lastError)
	assert.Equal(t, "compensation_failed", *lastError)
}

func TestResumeWorkflow_BudgetExhaustedRunningParksForReconciliation(t *testing.T) {
	h := newHarness(t)
	actor := h.seedActor(t, "actor_t055g")
	w := createWorkflowRow(t, h, adminbff.OperationTypeCreateUser, actor)
	ctx := context.Background()

	// Drive the forward budget to the cap; the final reclaim (expired-running
	// reclaims only check the lease) must hit the guard, not a new command.
	for i := 0; i < 10; i++ {
		claimed, err := h.work.ClaimWorkflowByOperationID(ctx, w.OperationID, fmt.Sprintf("worker-%d", i), 30*time.Second, time.Now())
		require.NoError(t, err)
		h.expireLease(t, w.OperationID)
		_ = claimed
	}

	_, err := h.svc.ResumeWorkflow(ctx, w.OperationID)
	require.ErrorIs(t, err, adminbff.ErrReconciliationRequired)
	state, _, _, lastError := h.workflowStateAndBudget(t, w.OperationID)
	assert.Equal(t, "failed_manual", state)
	require.NotNil(t, lastError)
	assert.Equal(t, "attempt_budget_exhausted", *lastError, "exhausted forward rows never execute another command")
}

func TestResumeWorkflow_ClaimMissExhaustionTransitions(t *testing.T) {
	h := newHarness(t)
	actor := h.seedActor(t, "actor_t055h")
	w := createWorkflowRow(t, h, adminbff.OperationTypeCreateUser, actor)
	ctx := context.Background()

	// The workflow is past its original retry deadline: the claim misses (no
	// predicate matches) and the claim-miss path transitions it to
	// failed_manual — the budget cap is never silently ignored.
	_, err := h.db.Exec(ctx,
		"UPDATE admin_workflows SET retry_deadline_at = now() - interval '1 minute', created_at = now() - interval '2 minutes' WHERE operation_id = $1",
		w.OperationID)
	require.NoError(t, err)

	_, err = h.svc.ResumeWorkflow(ctx, w.OperationID)
	require.ErrorIs(t, err, adminbff.ErrReconciliationRequired)
	state, _, _, lastError := h.workflowStateAndBudget(t, w.OperationID)
	assert.Equal(t, "failed_manual", state)
	require.NotNil(t, lastError)
	assert.Equal(t, "attempt_budget_exhausted", *lastError)
}

func TestRecoverWorkflow_RequiresUsersWrite(t *testing.T) {
	identity := &fakeIdentity{grantedChecks: 0}
	h := newHarnessWithParts(t, nil, nil, nil, nil, identity)
	actor := h.seedActor(t, "actor_t055i")
	w := createWorkflowRow(t, h, adminbff.OperationTypeCreateUser, actor)
	intoFailedManual(t, h, w)
	ctx := context.Background()

	_, err := h.svc.RecoverWorkflow(ctx, op(actor, ""), adminbff.RecoverParams{
		OperationID: w.OperationID,
		ActionType:  adminbff.RecoveryReconciliation,
		ReasonCode:  "operator_review",
	})
	require.ErrorIs(t, err, adminbff.ErrForbidden, "recovery requires the same users.write principal")
	assert.Zero(t, h.countRecoveryActions(t, w.OperationID), "unauthorized attempts are never ledgered")
}

func TestRecoverWorkflow_OnlyFailedManualRows(t *testing.T) {
	h := newHarness(t)
	actor := h.seedActor(t, "actor_t055j")
	w := createWorkflowRow(t, h, adminbff.OperationTypeCreateUser, actor) // still pending
	ctx := context.Background()

	_, err := h.svc.RecoverWorkflow(ctx, op(actor, ""), adminbff.RecoverParams{
		OperationID: w.OperationID,
		ActionType:  adminbff.RecoveryReconciliation,
		ReasonCode:  "operator_review",
	})
	require.ErrorIs(t, err, adminbff.ErrOperationInProgress, "automatic execution still owns the row")
	assert.Zero(t, h.countRecoveryActions(t, w.OperationID))
}

func TestRecoverWorkflow_ReceiptFinalizeCommittedCompletes(t *testing.T) {
	iamReceipts := &fakeReceipts{forceCommitted: true}
	h := newHarnessWithParts(t, nil, iamReceipts, nil, nil, nil)
	actor := h.seedActor(t, "actor_t055k")
	w := createWorkflowRow(t, h, adminbff.OperationTypeCreateUser, actor)
	intoFailedManualWithSubject(t, h, w, 51, 3)
	ctx := context.Background()

	outcome, err := h.svc.RecoverWorkflow(ctx, op(actor, ""), adminbff.RecoverParams{
		OperationID: w.OperationID,
		ActionType:  adminbff.RecoveryReceiptFinalize,
		ReasonCode:  "receipt_proven",
		ApprovalID:  ptr("approval-51"),
	})
	require.NoError(t, err)
	require.True(t, outcome.Terminal)
	assert.Equal(t, adminbff.WorkflowRejected, outcome.State)
	assert.Equal(t, adminbff.RecoveryReceiptFinalize, outcome.Action.ActionType)
	assert.Equal(t, "receipt_proven", outcome.Action.ReasonCode)
	require.NotNil(t, outcome.Action.ApprovalID)
	assert.Equal(t, "approval-51", *outcome.Action.ApprovalID)

	state, _, _, _ := h.workflowStateAndBudget(t, w.OperationID)
	assert.Equal(t, "rejected", state)
	assert.False(t, h.subjectActiveFor(t, w.OperationID, 51), "finalizing a proven receipt releases the exclusion")
	assert.Equal(t, 1, h.countRecoveryActions(t, w.OperationID))

	// A second recovery sees a terminal row: nothing more to do.
	_, err = h.svc.RecoverWorkflow(ctx, op(actor, ""), adminbff.RecoverParams{
		OperationID: w.OperationID,
		ActionType:  adminbff.RecoveryReconciliation,
		ReasonCode:  "already_resolved",
	})
	require.ErrorIs(t, err, adminbff.ErrOperationInProgress)
}

func TestRecoverWorkflow_ReceiptFinalizeAbsentRequiresApproval(t *testing.T) {
	h := newHarness(t)
	actor := h.seedActor(t, "actor_t055l")
	w := createWorkflowRow(t, h, adminbff.OperationTypeCreateUser, actor)
	intoFailedManual(t, h, w)
	ctx := context.Background()

	// No proven receipt: finalize is refused (re-execution needs a separate
	// approved_compensation action) but the attempt is durably ledgered.
	_, err := h.svc.RecoverWorkflow(ctx, op(actor, ""), adminbff.RecoverParams{
		OperationID: w.OperationID,
		ActionType:  adminbff.RecoveryReceiptFinalize,
		ReasonCode:  "no_receipt",
	})
	require.ErrorIs(t, err, adminbff.ErrReconciliationRequired)
	state, _, _, _ := h.workflowStateAndBudget(t, w.OperationID)
	assert.Equal(t, "failed_manual", state, "the row stays parked for reconciliation")
	assert.Equal(t, 1, h.countRecoveryActions(t, w.OperationID), "the failed attempt is faithfully recorded")
}

func TestRecoverWorkflow_ApprovedCompensationExecutes(t *testing.T) {
	managed := &fakeManaged{skipCompensate: true}
	h := newHarnessWithParts(t, managed, nil, nil, nil, nil)
	actor := h.seedActor(t, "actor_t055m")
	w := createWorkflowRow(t, h, adminbff.OperationTypeCreateUser, actor)
	intoFailedManualWithSubject(t, h, w, 52, 4)
	ctx := context.Background()

	outcome, err := h.svc.RecoverWorkflow(ctx, op(actor, ""), adminbff.RecoverParams{
		OperationID: w.OperationID,
		ActionType:  adminbff.RecoveryApprovedCompensation,
		ReasonCode:  "approved_restore",
	})
	require.NoError(t, err)
	require.True(t, outcome.Terminal)
	assert.Equal(t, adminbff.WorkflowRejected, outcome.State)
	state, compState, _, _ := h.workflowStateAndBudget(t, w.OperationID)
	assert.Equal(t, "rejected", state)
	// Recovery drives the append-only ledger, NOT the automatic compensation
	// machine: the compensation_state column is untouched (contract — manual
	// recovery may only append recovery actions and execute approved
	// compensation; the audit evidence lives in the ledger).
	assert.Equal(t, "not_required", compState, "recovery never mutates the automatic compensation state")
	assert.False(t, h.subjectActiveFor(t, w.OperationID, 52))
	assert.Equal(t, 1, h.countRecoveryActions(t, w.OperationID))
}

func TestRecoverWorkflow_ReconciliationRecordsOnly(t *testing.T) {
	h := newHarness(t)
	actor := h.seedActor(t, "actor_t055n")
	w := createWorkflowRow(t, h, adminbff.OperationTypeCreateUser, actor)
	intoFailedManual(t, h, w)
	ctx := context.Background()

	// A reconciliation decision mutates nothing but the ledger.
	outcome, err := h.svc.RecoverWorkflow(ctx, op(actor, ""), adminbff.RecoverParams{
		OperationID: w.OperationID,
		ActionType:  adminbff.RecoveryReconciliation,
		ReasonCode:  "operator_review",
	})
	require.NoError(t, err)
	assert.False(t, outcome.Terminal)
	state, _, _, _ := h.workflowStateAndBudget(t, w.OperationID)
	assert.Equal(t, "failed_manual", state, "decision-only recovery never mutates the workflow")

	// The ledger is append-only: every call records, even decisions.
	_, err = h.svc.RecoverWorkflow(ctx, op(actor, ""), adminbff.RecoverParams{
		OperationID: w.OperationID,
		ActionType:  adminbff.RecoveryReconciliation,
		ReasonCode:  "second_review",
	})
	require.NoError(t, err)
	assert.Equal(t, 2, h.countRecoveryActions(t, w.OperationID))

	ledger, err := h.work.ListRecoveryActions(ctx, w.OperationID)
	require.NoError(t, err)
	require.Len(t, ledger, 2)
	assert.Equal(t, "operator_review", ledger[0].ReasonCode)
	assert.Equal(t, "second_review", ledger[1].ReasonCode)
	assert.Equal(t, adminbff.WorkflowFailedManual, ledger[0].PreviousState, "recovery always leaves failed_manual")
	assert.Equal(t, adminbff.WorkflowFailedManual, ledger[0].ResultingState, "decision-only actions keep the state")
}
