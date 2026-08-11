// Admin BFF workflow-store contract tests (task T051;
// contracts/consistency-and-compensation.md Durable workflow execution).
//
// The suite lives in the external package postgres_test because it imports
// adminbff (ports/models) and the adapter — the test file sits next to the
// adapter, so the internal visibility rule still holds. Every test runs on
// its own scratch database (fresh + migrated to head), asserting store
// semantics through the public constructor postgres.NewStore: lease/retry
// predicates, CAS staleness, claim transitions, subject exclusions and
// RunInTx atomicity.
//
// ClaimWorkflows predicates compare against the DATABASE clock (now() in
// SQL); the `now` argument only seeds leased_until. Tests therefore control
// eligibility by rewriting deadlines/leases directly on the scratch
// database, never by faking the clock.
package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres" // registers the postgres driver for migrate
	"github.com/golang-migrate/migrate/v4/source/iofs"
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
	"github.com/hdw/vue-element-plus-admin/backend/internal/adminbff/postgres"
)

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

type harness struct {
	pool  *pgxpool.Pool
	store adminbff.WorkflowStore
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	connStr := freshDB(t)
	migrateToHead(t, connStr)

	pool, err := pgxpool.New(ctx, connStr)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return &harness{pool: pool, store: postgres.NewStore(pool)}
}

// --- factories and claim helpers ----------------------------------------------

func newWorkflow(opID string) adminbff.Workflow {
	return adminbff.Workflow{
		OperationID:        opID,
		OperationType:      "managed_user.create",
		IdempotencyKey:     ptr("key-" + opID),
		RequestFingerprint: "fp-" + opID,
		ActorUserID:        7,
		RetryDeadlineAt:    time.Now().Add(24 * time.Hour),
	}
}

// create persists a workflow with a deterministic operation id and returns it.
func create(t *testing.T, h *harness, opID string) adminbff.Workflow {
	t.Helper()
	w := newWorkflow(opID)
	require.NoError(t, h.store.CreateWorkflow(context.Background(), w))
	return w
}

// claim runs the atomic claim scan and requires exactly n rows.
func claim(t *testing.T, h *harness, owner string, n int) []adminbff.Workflow {
	t.Helper()
	ws, err := h.store.ClaimWorkflows(context.Background(), owner, 30*time.Second, 10, time.Now())
	require.NoError(t, err)
	require.Len(t, ws, n, "expected %d claimed workflows", n)
	return ws
}

func claimOne(t *testing.T, h *harness, owner string) adminbff.Workflow {
	t.Helper()
	return claim(t, h, owner, 1)[0]
}

// expireLease rewrites leased_until into the past so a running/compensating
// row becomes claimable again.
func expireLease(t *testing.T, h *harness, opID string) {
	t.Helper()
	_, err := h.pool.Exec(context.Background(),
		"UPDATE admin_workflows SET leased_until = now() - interval '1 minute' WHERE operation_id = $1", opID)
	require.NoError(t, err)
}

func expireClientInput(t *testing.T, h *harness, opID string) {
	t.Helper()
	_, err := h.pool.Exec(context.Background(),
		"UPDATE admin_workflows SET client_input_deadline_at = now() - interval '1 minute' WHERE operation_id = $1", opID)
	require.NoError(t, err)
}

// expireRetryDeadline rewrites the immutable retry deadline (and created_at,
// which the CHECK retry_deadline_at >= created_at requires) into the past so a
// workflow is past its forward-execution window.
func expireRetryDeadline(t *testing.T, h *harness, opID string) {
	t.Helper()
	_, err := h.pool.Exec(context.Background(),
		"UPDATE admin_workflows SET retry_deadline_at = now() - interval '1 minute', created_at = now() - interval '2 minutes' WHERE operation_id = $1", opID)
	require.NoError(t, err)
}

// --- raw row access for DB-truth cross-checks ----------------------------------

type wfRow struct {
	state                 string
	compensationState     string
	currentStep           string
	attemptCount          int
	subjectUserID         pgtype.Int8
	expectedIamVersion    pgtype.Int8
	resultingIamVersion   pgtype.Int8
	leaseOwner            pgtype.Text
	leasedUntil           pgtype.Timestamptz
	claimToken            pgtype.UUID
	nextRetryAt           pgtype.Timestamptz
	clientInputDeadlineAt pgtype.Timestamptz
	retryDeadlineAt       pgtype.Timestamptz
	completedAt           pgtype.Timestamptz
	lastErrorCode         pgtype.Text
	result                []byte
}

func rawWorkflow(t *testing.T, h *harness, opID string) wfRow {
	t.Helper()
	var r wfRow
	err := h.pool.QueryRow(context.Background(), `
		SELECT state, compensation_state, current_step, attempt_count,
		       subject_user_id, expected_iam_version, resulting_iam_version,
		       lease_owner, leased_until, claim_token,
		       next_retry_at, client_input_deadline_at, retry_deadline_at,
		       completed_at, last_error_code, result
		FROM admin_workflows
		WHERE operation_id = $1`, opID).Scan(
		&r.state, &r.compensationState, &r.currentStep, &r.attemptCount,
		&r.subjectUserID, &r.expectedIamVersion, &r.resultingIamVersion,
		&r.leaseOwner, &r.leasedUntil, &r.claimToken,
		&r.nextRetryAt, &r.clientInputDeadlineAt, &r.retryDeadlineAt,
		&r.completedAt, &r.lastErrorCode, &r.result,
	)
	require.NoError(t, err)
	return r
}

func requireLeased(t *testing.T, r wfRow) {
	t.Helper()
	require.True(t, r.leaseOwner.Valid, "lease_owner must be set")
	require.True(t, r.leasedUntil.Valid, "leased_until must be set")
	require.True(t, r.claimToken.Valid, "claim_token must be set")
}

func requireUnleased(t *testing.T, r wfRow) {
	t.Helper()
	require.False(t, r.leaseOwner.Valid, "lease_owner must be NULL")
	require.False(t, r.leasedUntil.Valid, "leased_until must be NULL")
	require.False(t, r.claimToken.Valid, "claim_token must be NULL")
}

func ptr[T any](v T) *T { return &v }

func op(id int) string { return fmt.Sprintf("11111111-0000-0000-0000-0000000000%02d", id) }

// --- create / read -------------------------------------------------------------

func TestCreateWorkflow_PersistsPendingWithNoLease(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	w := newWorkflow(op(1))
	w.IdempotencyKey = ptr("client-key-1")
	require.NoError(t, h.store.CreateWorkflow(ctx, w))

	got, err := h.store.GetWorkflowByOperationID(ctx, w.OperationID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, adminbff.WorkflowPending, got.State)
	assert.Equal(t, "created", got.CurrentStep)
	assert.Equal(t, adminbff.CompensationNotRequired, got.CompensationState)
	assert.Equal(t, 0, got.AttemptCount)
	assert.Equal(t, "fp-"+w.OperationID, got.RequestFingerprint)
	assert.Equal(t, "client-key-1", *got.IdempotencyKey)
	assert.Equal(t, int64(7), got.ActorUserID)
	assert.Nil(t, got.LeaseOwner)
	assert.Nil(t, got.ClaimToken)
	assert.WithinDuration(t, time.Now().Add(24*time.Hour), got.RetryDeadlineAt, 2*time.Minute)

	// Idempotency scope: same type + actor + key resolves the same workflow.
	byKey, err := h.store.GetWorkflowByIdempotencyKey(ctx, "managed_user.create", 7, "client-key-1")
	require.NoError(t, err)
	require.NotNil(t, byKey)
	assert.Equal(t, got.OperationID, byKey.OperationID)

	// Different actor — different scope, absent.
	other, err := h.store.GetWorkflowByIdempotencyKey(ctx, "managed_user.create", 8, "client-key-1")
	require.NoError(t, err)
	assert.Nil(t, other)
}

func TestCreateWorkflow_NoIdempotencyKey(t *testing.T) {
	h := newHarness(t)
	w := newWorkflow(op(2))
	w.IdempotencyKey = nil
	require.NoError(t, h.store.CreateWorkflow(context.Background(), w))

	got, err := h.store.GetWorkflowByOperationID(context.Background(), w.OperationID)
	require.NoError(t, err)
	assert.Nil(t, got.IdempotencyKey)
}

func TestGetWorkflow_AbsentAndInvalidIDs(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	absent, err := h.store.GetWorkflowByOperationID(ctx, op(99))
	require.NoError(t, err)
	assert.Nil(t, absent, "absence must be (nil, nil), never fabricated")

	absent, err = h.store.GetWorkflowByIdempotencyKey(ctx, "managed_user.create", 7, "never-created")
	require.NoError(t, err)
	assert.Nil(t, absent)

	_, err = h.store.GetWorkflowByOperationID(ctx, "not-a-uuid")
	require.ErrorIs(t, err, adminbff.ErrInvalidOperationID)
}

// --- claims -------------------------------------------------------------------

func TestClaimWorkflows_PendingClaimedWithAttempt(t *testing.T) {
	h := newHarness(t)
	create(t, h, op(1))

	got := claimOne(t, h, "worker-1")
	assert.Equal(t, op(1), got.OperationID)
	assert.Equal(t, adminbff.WorkflowRunning, got.State)
	assert.Equal(t, 1, got.AttemptCount)
	assert.Equal(t, "worker-1", *got.LeaseOwner)
	require.NotNil(t, got.ClaimToken)

	r := rawWorkflow(t, h, op(1))
	requireLeased(t, r)
	assert.Equal(t, "running", r.state)
	assert.Equal(t, 1, r.attemptCount)

	// A live lease is not reclaimable by anyone.
	claim(t, h, "worker-2", 0)
}

func TestClaimWorkflows_ExpiredRunningReclaimedWithAttempt(t *testing.T) {
	h := newHarness(t)
	create(t, h, op(1))

	first := claimOne(t, h, "worker-1")
	expireLease(t, h, op(1))

	second := claimOne(t, h, "worker-2")
	assert.Equal(t, 2, second.AttemptCount)
	assert.Equal(t, "worker-2", *second.LeaseOwner)
	require.NotNil(t, second.ClaimToken)
	assert.NotEqual(t, first.ClaimToken, second.ClaimToken, "fresh claim token per claim")
	assert.Equal(t, adminbff.WorkflowRunning, second.State)
}

func TestClaimWorkflows_ExpiredCompensatingReclaimedWithoutAttempt(t *testing.T) {
	h := newHarness(t)
	create(t, h, op(1))

	first := claimOne(t, h, "worker-1")
	require.NoError(t, h.store.BeginCompensation(context.Background(), op(1), *first.ClaimToken))
	expireLease(t, h, op(1))

	second := claimOne(t, h, "worker-2")
	assert.Equal(t, adminbff.WorkflowCompensating, second.State, "compensating is preserved across reclaims")
	assert.Equal(t, 1, second.AttemptCount, "compensation reclaims never consume the forward budget")
	assert.Equal(t, "worker-2", *second.LeaseOwner)
}

func TestClaimWorkflows_RetryableClaimedWhenDue(t *testing.T) {
	h := newHarness(t)
	create(t, h, op(1))

	first := claimOne(t, h, "worker-1")
	require.NoError(t, h.store.RescheduleRetryable(context.Background(), op(1), *first.ClaimToken,
		time.Now().Add(-time.Minute), "IAM_CONFLICT"))

	second := claimOne(t, h, "worker-2")
	assert.Equal(t, adminbff.WorkflowRunning, second.State)
	assert.Equal(t, 2, second.AttemptCount, "retry counts as a forward attempt")
}

func TestClaimWorkflows_RetryableSkippedWhenNotDue(t *testing.T) {
	h := newHarness(t)
	create(t, h, op(1))

	first := claimOne(t, h, "worker-1")
	require.NoError(t, h.store.RescheduleRetryable(context.Background(), op(1), *first.ClaimToken,
		time.Now().Add(time.Hour), "IAM_CONFLICT"))

	claim(t, h, "worker-2", 0)
}

func TestClaimWorkflows_ExpiredClientInputCompensatesWithoutAttempt(t *testing.T) {
	h := newHarness(t)
	create(t, h, op(1))

	first := claimOne(t, h, "worker-1")
	require.NoError(t, h.store.EnterAwaitingClientInput(context.Background(), op(1), *first.ClaimToken,
		time.Now().Add(24*time.Hour)))
	expireClientInput(t, h, op(1))

	second := claimOne(t, h, "worker-2")
	assert.Equal(t, adminbff.WorkflowCompensating, second.State, "expired input goes straight to compensating")
	assert.Equal(t, adminbff.CompensationPending, second.CompensationState)
	assert.Equal(t, 1, second.AttemptCount, "expired-input claims do not count as forward attempts")
}

func TestClaimWorkflows_AwaitingInputWithinDeadlineNotClaimed(t *testing.T) {
	h := newHarness(t)
	create(t, h, op(1))

	first := claimOne(t, h, "worker-1")
	require.NoError(t, h.store.EnterAwaitingClientInput(context.Background(), op(1), *first.ClaimToken,
		time.Now().Add(time.Hour)))

	claim(t, h, "worker-2", 0)
}

func TestClaimWorkflows_SkipsPastRetryDeadline(t *testing.T) {
	h := newHarness(t)
	create(t, h, op(1))
	expireRetryDeadline(t, h, op(1))

	claim(t, h, "worker-1", 0)
}

func TestClaimWorkflows_AttemptBudgetCap(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	create(t, h, op(1))

	for i := 1; i <= 10; i++ {
		got := claimOne(t, h, "worker-1")
		assert.Equal(t, i, got.AttemptCount, "attempt %d", i)
		expireLease(t, h, op(1))
	}

	// Budget exhausted. Expired-running rows stay reclaimable so the new
	// owner can resolve the last receipt and finalize/compensate (recovery
	// must not deadlock), but the counter never exceeds the 10-attempt cap.
	got := claimOne(t, h, "worker-2")
	assert.Equal(t, 10, got.AttemptCount, "recovery claims never push the counter past the cap")

	// At the cap, a retryable row is NOT claimable: there is no attempt 11.
	require.NoError(t, h.store.RescheduleRetryable(ctx, op(1), *got.ClaimToken,
		time.Now().Add(-time.Minute), "BUDGET_EXHAUSTED"))
	claim(t, h, "worker-2", 0)
}

func TestClaimWorkflows_EmptyStore(t *testing.T) {
	h := newHarness(t)
	ws, err := h.store.ClaimWorkflows(context.Background(), "worker-1", 30*time.Second, 10, time.Now())
	require.NoError(t, err)
	assert.Empty(t, ws)
}

// --- claim via same-key HTTP retry ----------------------------------------------

func TestClaimAwaitingClientInput_BeforeDeadline(t *testing.T) {
	h := newHarness(t)
	create(t, h, op(1))

	first := claimOne(t, h, "worker-1")
	require.NoError(t, h.store.EnterAwaitingClientInput(context.Background(), op(1), *first.ClaimToken,
		time.Now().Add(time.Hour)))

	got, err := h.store.ClaimAwaitingClientInput(context.Background(), op(1), *newWorkflow(op(1)).IdempotencyKey,
		"worker-2", 30*time.Second, time.Now())
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, adminbff.WorkflowRunning, got.State)
	assert.Equal(t, 1, got.AttemptCount, "no attempt counted until the participant command is attempted")
	assert.Equal(t, "worker-2", *got.LeaseOwner)
	assert.NotEqual(t, first.ClaimToken, got.ClaimToken)

	r := rawWorkflow(t, h, op(1))
	requireLeased(t, r)
	assert.Equal(t, "running", r.state)
}

func TestClaimAwaitingClientInput_AfterDeadlineStale(t *testing.T) {
	h := newHarness(t)
	create(t, h, op(1))

	first := claimOne(t, h, "worker-1")
	require.NoError(t, h.store.EnterAwaitingClientInput(context.Background(), op(1), *first.ClaimToken,
		time.Now().Add(-time.Hour)))

	_, err := h.store.ClaimAwaitingClientInput(context.Background(), op(1), *newWorkflow(op(1)).IdempotencyKey,
		"worker-2", 30*time.Second, time.Now())
	require.ErrorIs(t, err, adminbff.ErrStaleClaim, "deadline passed: only the compensation path may continue")
}

func TestClaimAwaitingClientInput_WrongKeyStale(t *testing.T) {
	h := newHarness(t)
	create(t, h, op(1))

	first := claimOne(t, h, "worker-1")
	require.NoError(t, h.store.EnterAwaitingClientInput(context.Background(), op(1), *first.ClaimToken,
		time.Now().Add(time.Hour)))

	_, err := h.store.ClaimAwaitingClientInput(context.Background(), op(1), "wrong-key",
		"worker-2", 30*time.Second, time.Now())
	require.ErrorIs(t, err, adminbff.ErrStaleClaim, "a different client must never continue this workflow")
}

// --- keyed claim (ClaimWorkflowByOperationID) -------------------------------------
// The synchronous HTTP path and the resume machine must claim ONE specific
// workflow — a limit=1 batch scan would steal whichever workflow is oldest.

func TestClaimWorkflowByOperationID_PendingClaimedWithAttempt(t *testing.T) {
	h := newHarness(t)
	create(t, h, op(1))

	got, err := h.store.ClaimWorkflowByOperationID(context.Background(), op(1), "actor:7", 30*time.Second, time.Now())
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, adminbff.WorkflowRunning, got.State)
	assert.Equal(t, 1, got.AttemptCount)
	assert.Equal(t, "actor:7", *got.LeaseOwner)

	r := rawWorkflow(t, h, op(1))
	requireLeased(t, r)
	assert.Equal(t, "running", r.state)
}

func TestClaimWorkflowByOperationID_AlreadyClaimedStale(t *testing.T) {
	h := newHarness(t)
	create(t, h, op(1))

	_, err := h.store.ClaimWorkflowByOperationID(context.Background(), op(1), "actor:7", 30*time.Second, time.Now())
	require.NoError(t, err)

	_, err = h.store.ClaimWorkflowByOperationID(context.Background(), op(1), "worker-2", 30*time.Second, time.Now())
	require.ErrorIs(t, err, adminbff.ErrStaleClaim, "a live lease is owned by the current claim holder")
}

func TestClaimWorkflowByOperationID_ExpiredRunningReclaimedAttemptCapped(t *testing.T) {
	h := newHarness(t)
	create(t, h, op(1))

	// Drive the forward budget to the cap first: 10 claims each count one.
	first, err := h.store.ClaimWorkflowByOperationID(context.Background(), op(1), "w1", 30*time.Second, time.Now())
	require.NoError(t, err)
	for i := 2; i <= 10; i++ {
		expireLease(t, h, op(1))
		next, err := h.store.ClaimWorkflowByOperationID(context.Background(), op(1), fmt.Sprintf("w%d", i), 30*time.Second, time.Now())
		require.NoError(t, err)
		require.Equal(t, i, next.AttemptCount)
		require.NotEqual(t, first.ClaimToken, next.ClaimToken)
	}

	// At the cap, an expired-running recovery claim is allowed (a reclaim must
	// never deadlock the row) but the counter stays pinned at 10.
	expireLease(t, h, op(1))
	got, err := h.store.ClaimWorkflowByOperationID(context.Background(), op(1), "w11", 30*time.Second, time.Now())
	require.NoError(t, err)
	assert.Equal(t, 10, got.AttemptCount, "recovery reclaim at the cap must not count a further attempt")
	assert.Equal(t, adminbff.WorkflowRunning, got.State)
}

func TestClaimWorkflowByOperationID_RetryableDueClaimedWhenDue(t *testing.T) {
	h := newHarness(t)
	create(t, h, op(1))
	first := claimOne(t, h, "worker-1")
	require.NoError(t, h.store.RescheduleRetryable(context.Background(), op(1), *first.ClaimToken,
		time.Now().Add(-time.Minute), "dependency_timeout"))

	got, err := h.store.ClaimWorkflowByOperationID(context.Background(), op(1), "worker-2", 30*time.Second, time.Now())
	require.NoError(t, err)
	assert.Equal(t, adminbff.WorkflowRunning, got.State)
	assert.Equal(t, 2, got.AttemptCount)
}

func TestClaimWorkflowByOperationID_RetryableNotDueStale(t *testing.T) {
	h := newHarness(t)
	create(t, h, op(1))
	first := claimOne(t, h, "worker-1")
	require.NoError(t, h.store.RescheduleRetryable(context.Background(), op(1), *first.ClaimToken,
		time.Now().Add(time.Minute), "dependency_timeout"))

	_, err := h.store.ClaimWorkflowByOperationID(context.Background(), op(1), "worker-2", 30*time.Second, time.Now())
	require.ErrorIs(t, err, adminbff.ErrStaleClaim, "not due for retry yet")
}

func TestClaimWorkflowByOperationID_ExpiredClientInputCompensatesWithoutAttempt(t *testing.T) {
	h := newHarness(t)
	create(t, h, op(1))
	first := claimOne(t, h, "worker-1")
	require.NoError(t, h.store.EnterAwaitingClientInput(context.Background(), op(1), *first.ClaimToken,
		time.Now().Add(-time.Hour)))

	got, err := h.store.ClaimWorkflowByOperationID(context.Background(), op(1), "worker-2", 30*time.Second, time.Now())
	require.NoError(t, err)
	assert.Equal(t, adminbff.WorkflowCompensating, got.State)
	assert.Equal(t, adminbff.CompensationPending, got.CompensationState)
	assert.Equal(t, 1, got.AttemptCount, "expiry finalization consumes no forward attempt")

	r := rawWorkflow(t, h, op(1))
	requireLeased(t, r)
	assert.Equal(t, "compensating", r.state)
	assert.Equal(t, "pending", r.compensationState)
}

func TestClaimWorkflowByOperationID_ExpiredCompensatingReclaimed(t *testing.T) {
	h := newHarness(t)
	create(t, h, op(1))
	first := claimOne(t, h, "worker-1")
	require.NoError(t, h.store.BeginCompensation(context.Background(), op(1), *first.ClaimToken))
	expireLease(t, h, op(1))

	got, err := h.store.ClaimWorkflowByOperationID(context.Background(), op(1), "worker-2", 30*time.Second, time.Now())
	require.NoError(t, err)
	assert.Equal(t, adminbff.WorkflowCompensating, got.State)
	assert.Equal(t, 1, got.AttemptCount, "compensation reclaims never consume the forward budget")
}

func TestClaimWorkflowByOperationID_PastRetryDeadlineStale(t *testing.T) {
	h := newHarness(t)
	create(t, h, op(1))
	expireRetryDeadline(t, h, op(1))

	_, err := h.store.ClaimWorkflowByOperationID(context.Background(), op(1), "worker-2", 30*time.Second, time.Now())
	require.ErrorIs(t, err, adminbff.ErrStaleClaim, "past the forward-execution window: nothing may continue")
}

func TestClaimWorkflowByOperationID_AbsentStale(t *testing.T) {
	h := newHarness(t)
	_, err := h.store.ClaimWorkflowByOperationID(context.Background(), op(99), "worker-2", 30*time.Second, time.Now())
	require.ErrorIs(t, err, adminbff.ErrStaleClaim, "absent workflow is not claimable")
}

// --- CAS transitions ------------------------------------------------------------

func TestPersistStep_KeepsUnsetFieldsAndRejectsStale(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	create(t, h, op(1))

	// No claim at all: nothing is persisted.
	err := h.store.PersistStep(ctx, op(1), "00000000-0000-0000-0000-000000000000", adminbff.StepUpdate{Step: "x"})
	require.ErrorIs(t, err, adminbff.ErrStaleClaim)

	claimed := claimOne(t, h, "worker-1")
	token := *claimed.ClaimToken

	// Step 1 sets the subject identity fields.
	err = h.store.PersistStep(ctx, op(1), token, adminbff.StepUpdate{
		Step:               "subjects_committed",
		SubjectUserID:      ptr(int64(42)),
		ExpectedIamVersion: ptr(int64(5)),
	})
	require.NoError(t, err)
	r := rawWorkflow(t, h, op(1))
	assert.Equal(t, "subjects_committed", r.currentStep)
	require.True(t, r.subjectUserID.Valid)
	assert.Equal(t, int64(42), r.subjectUserID.Int64)
	require.True(t, r.expectedIamVersion.Valid)
	assert.Equal(t, int64(5), r.expectedIamVersion.Int64)

	// Step 2 records the tombstone only: nil fields keep the stored values.
	err = h.store.PersistStep(ctx, op(1), token, adminbff.StepUpdate{
		Step:                     "tombstoned",
		ResultingIamVersion:      ptr(int64(6)),
		AppliedDepartmentID:      ptr(int64(9)),
		AppliedMembershipVersion: ptr(int64(2)),
	})
	require.NoError(t, err)
	r = rawWorkflow(t, h, op(1))
	assert.Equal(t, "tombstoned", r.currentStep)
	require.True(t, r.subjectUserID.Valid, "subject must be kept across steps")
	assert.Equal(t, int64(42), r.subjectUserID.Int64)
	require.True(t, r.expectedIamVersion.Valid)
	assert.Equal(t, int64(5), r.expectedIamVersion.Int64)
	require.True(t, r.resultingIamVersion.Valid)
	assert.Equal(t, int64(6), r.resultingIamVersion.Int64)

	// A stale token is rejected and changes nothing.
	err = h.store.PersistStep(ctx, op(1), "00000000-0000-0000-0000-000000000001", adminbff.StepUpdate{Step: "evil"})
	require.ErrorIs(t, err, adminbff.ErrStaleClaim)
	r = rawWorkflow(t, h, op(1))
	assert.Equal(t, "tombstoned", r.currentStep, "stale CAS must not mutate the row")
}

func TestBeginCompensation_OnlyFromRunning(t *testing.T) {
	h := newHarness(t)
	create(t, h, op(1))

	claimed := claimOne(t, h, "worker-1")
	token := *claimed.ClaimToken
	require.NoError(t, h.store.BeginCompensation(context.Background(), op(1), token))

	r := rawWorkflow(t, h, op(1))
	assert.Equal(t, "compensating", r.state)
	assert.Equal(t, "pending", r.compensationState)
	requireLeased(t, r)

	// A second BeginCompensation from compensating is a stale claim.
	err := h.store.BeginCompensation(context.Background(), op(1), token)
	require.ErrorIs(t, err, adminbff.ErrStaleClaim)
}

func TestBeginCompensation_RejectsUnclaimedPending(t *testing.T) {
	h := newHarness(t)
	create(t, h, op(1))

	err := h.store.BeginCompensation(context.Background(), op(1), "00000000-0000-0000-0000-000000000000")
	require.ErrorIs(t, err, adminbff.ErrStaleClaim)
}

func TestCompensationOutcome_OnlyInCompensating(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	create(t, h, op(1))

	claimed := claimOne(t, h, "worker-1")
	token := *claimed.ClaimToken

	// From running, the compensation outcomes are stale claims.
	require.ErrorIs(t, h.store.SetCompensationSucceeded(ctx, op(1), token), adminbff.ErrStaleClaim)
	require.ErrorIs(t, h.store.SetCompensationFailed(ctx, op(1), token), adminbff.ErrStaleClaim)

	require.NoError(t, h.store.BeginCompensation(ctx, op(1), token))
	require.NoError(t, h.store.SetCompensationSucceeded(ctx, op(1), token))
	assert.Equal(t, "succeeded", rawWorkflow(t, h, op(1)).compensationState)

	// After success the failure outcome is stale.
	require.ErrorIs(t, h.store.SetCompensationFailed(ctx, op(1), token), adminbff.ErrStaleClaim)
}

func TestCompensationFailed_MarksForReconciliation(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	create(t, h, op(1))

	claimed := claimOne(t, h, "worker-1")
	require.NoError(t, h.store.BeginCompensation(ctx, op(1), *claimed.ClaimToken))
	require.NoError(t, h.store.SetCompensationFailed(ctx, op(1), *claimed.ClaimToken))

	r := rawWorkflow(t, h, op(1))
	assert.Equal(t, "compensating", r.state)
	assert.Equal(t, "failed", r.compensationState)
}

func TestEnterAwaitingClientInput_ClearsLease(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	create(t, h, op(1))

	claimed := claimOne(t, h, "worker-1")
	require.NoError(t, h.store.EnterAwaitingClientInput(ctx, op(1), *claimed.ClaimToken,
		time.Now().Add(2*time.Hour)))

	r := rawWorkflow(t, h, op(1))
	assert.Equal(t, "awaiting_client_input", r.state)
	require.True(t, r.clientInputDeadlineAt.Valid)
	assert.WithinDuration(t, time.Now().Add(2*time.Hour), r.clientInputDeadlineAt.Time, 2*time.Minute)
	requireUnleased(t, r)
}

func TestRescheduleRetryable_ClearsLease(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	create(t, h, op(1))

	claimed := claimOne(t, h, "worker-1")
	require.NoError(t, h.store.RescheduleRetryable(ctx, op(1), *claimed.ClaimToken,
		time.Now().Add(5*time.Minute), "IAM_CONFLICT"))

	r := rawWorkflow(t, h, op(1))
	assert.Equal(t, "failed_retryable", r.state)
	require.True(t, r.nextRetryAt.Valid)
	assert.WithinDuration(t, time.Now().Add(5*time.Minute), r.nextRetryAt.Time, 2*time.Minute)
	require.True(t, r.lastErrorCode.Valid)
	assert.Equal(t, "IAM_CONFLICT", r.lastErrorCode.String)
	requireUnleased(t, r)
}

func TestFailManual_ParksForReconciliation(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	create(t, h, op(1))

	claimed := claimOne(t, h, "worker-1")
	require.NoError(t, h.store.FailManual(ctx, op(1), *claimed.ClaimToken, "RECONCILIATION_REQUIRED"))

	r := rawWorkflow(t, h, op(1))
	assert.Equal(t, "failed_manual", r.state)
	require.True(t, r.lastErrorCode.Valid)
	assert.Equal(t, "RECONCILIATION_REQUIRED", r.lastErrorCode.String)
	requireUnleased(t, r)
}

func TestCompleteWorkflow_TerminalResolution(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	create(t, h, op(1))

	claimed := claimOne(t, h, "worker-1")
	token := *claimed.ClaimToken

	require.NoError(t, h.store.CompleteWorkflow(ctx, op(1), token, adminbff.WorkflowSucceeded,
		map[string]any{"user_id": float64(42)}))
	got, err := h.store.GetWorkflowByOperationID(ctx, op(1))
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, adminbff.WorkflowSucceeded, got.State)
	require.NotNil(t, got.Result)
	assert.Equal(t, float64(42), got.Result["user_id"])
	require.NotNil(t, got.CompletedAt)

	r := rawWorkflow(t, h, op(1))
	require.True(t, r.completedAt.Valid)
	requireUnleased(t, r)

	// The same claim token can never resolve twice.
	err = h.store.CompleteWorkflow(ctx, op(1), token, adminbff.WorkflowRejected, nil)
	require.ErrorIs(t, err, adminbff.ErrStaleClaim)
}

func TestCompleteWorkflow_NilResultAndRejectedState(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	create(t, h, op(1))

	claimed := claimOne(t, h, "worker-1")
	require.NoError(t, h.store.CompleteWorkflow(ctx, op(1), *claimed.ClaimToken, adminbff.WorkflowRejected, nil))

	got, err := h.store.GetWorkflowByOperationID(ctx, op(1))
	require.NoError(t, err)
	assert.Equal(t, adminbff.WorkflowRejected, got.State)
	assert.Nil(t, got.Result)
	assert.Nil(t, rawWorkflow(t, h, op(1)).result)
}

// --- subjects ------------------------------------------------------------------

func TestSubjects_InsertListAndExclusionLifecycle(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	create(t, h, op(1))

	require.NoError(t, h.store.InsertWorkflowSubject(ctx, adminbff.WorkflowSubject{
		OperationID: op(1), SubjectUserID: 42, ExpectedIamVersion: 5,
	}))

	subs, err := h.store.ListSubjectsByOperation(ctx, op(1))
	require.NoError(t, err)
	require.Len(t, subs, 1)
	assert.Equal(t, int64(42), subs[0].SubjectUserID)
	assert.Equal(t, int64(5), subs[0].ExpectedIamVersion)
	assert.True(t, subs[0].Active)
	assert.Nil(t, subs[0].ResultingTombstoneVersion)

	// Re-entry of the SAME workflow is idempotent: the ON CONFLICT DO UPDATE
	// narrows the partial-index conflict to the same operation_id, so the
	// expected version is refreshed in place (crash between the iam_validated
	// persist and the subject insert must not surface as an exclusion).
	err = h.store.InsertWorkflowSubject(ctx, adminbff.WorkflowSubject{
		OperationID: op(1), SubjectUserID: 42, ExpectedIamVersion: 6,
	})
	require.NoError(t, err)
	subs, err = h.store.ListSubjectsByOperation(ctx, op(1))
	require.NoError(t, err)
	require.Len(t, subs, 1)
	assert.Equal(t, int64(6), subs[0].ExpectedIamVersion, "same-op re-entry refreshes expected version")
	assert.True(t, subs[0].Active)

	// D5: persist the tombstone and release the exclusion for OTHER
	// workflows (the per-workflow PK (operation_id, subject_user_id) keeps
	// this workflow's one history row).
	require.NoError(t, h.store.UpdateSubjectResult(ctx, op(1), 42, 6))
	subs, err = h.store.ListSubjectsByOperation(ctx, op(1))
	require.NoError(t, err)
	require.Len(t, subs, 1)
	assert.False(t, subs[0].Active)
	require.NotNil(t, subs[0].ResultingTombstoneVersion)
	assert.Equal(t, int64(6), *subs[0].ResultingTombstoneVersion)

	// The subject is claimable again by a new workflow.
	create(t, h, op(2))
	require.NoError(t, h.store.InsertWorkflowSubject(ctx, adminbff.WorkflowSubject{
		OperationID: op(2), SubjectUserID: 42, ExpectedIamVersion: 6,
	}))
}

func TestSubjects_ExclusionSpansWorkflows(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	create(t, h, op(1))
	create(t, h, op(2))

	require.NoError(t, h.store.InsertWorkflowSubject(ctx, adminbff.WorkflowSubject{
		OperationID: op(1), SubjectUserID: 77, ExpectedIamVersion: 1,
	}))
	err := h.store.InsertWorkflowSubject(ctx, adminbff.WorkflowSubject{
		OperationID: op(2), SubjectUserID: 77, ExpectedIamVersion: 1,
	})
	require.ErrorIs(t, err, adminbff.ErrSubjectExcluded, "no overlapping active workflow per subject")

	// Convergence release (rejected/OPERATION_EXPIRED) frees the subject.
	require.NoError(t, h.store.ReleaseSubjectExclusion(ctx, op(1), 77))
	require.NoError(t, h.store.InsertWorkflowSubject(ctx, adminbff.WorkflowSubject{
		OperationID: op(2), SubjectUserID: 77, ExpectedIamVersion: 1,
	}))
}

// --- transactions ---------------------------------------------------------------

func TestRunInTx_CommitAndRollback(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// Commit on nil error: both the workflow and its subjects land atomically.
	err := h.store.RunInTx(ctx, func(tx adminbff.WorkflowStore) error {
		if err := tx.CreateWorkflow(ctx, newWorkflow(op(1))); err != nil {
			return err
		}
		return tx.InsertWorkflowSubject(ctx, adminbff.WorkflowSubject{
			OperationID: op(1), SubjectUserID: 42, ExpectedIamVersion: 5,
		})
	})
	require.NoError(t, err)
	got, err := h.store.GetWorkflowByOperationID(ctx, op(1))
	require.NoError(t, err)
	require.NotNil(t, got)
	subs, err := h.store.ListSubjectsByOperation(ctx, op(1))
	require.NoError(t, err)
	require.Len(t, subs, 1)

	// Rollback on error: the insert inside the failed transaction is gone.
	err = h.store.RunInTx(ctx, func(tx adminbff.WorkflowStore) error {
		if err := tx.InsertWorkflowSubject(ctx, adminbff.WorkflowSubject{
			OperationID: op(1), SubjectUserID: 43, ExpectedIamVersion: 5,
		}); err != nil {
			return err
		}
		return errors.New("boom: compensation side effect failed")
	})
	require.Error(t, err)
	subs, err = h.store.ListSubjectsByOperation(ctx, op(1))
	require.NoError(t, err)
	assert.Len(t, subs, 1, "the rolled-back subject must not be visible")
	assert.Equal(t, int64(42), subs[0].SubjectUserID)
}

func TestRunInTx_NestedRejected(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	create(t, h, op(1))

	err := h.store.RunInTx(ctx, func(tx adminbff.WorkflowStore) error {
		return tx.RunInTx(ctx, func(inner adminbff.WorkflowStore) error { return nil })
	})
	require.ErrorContains(t, err, "nested transactions are not supported")
}

// --- invalid ids ----------------------------------------------------------------

func TestStore_InvalidOperationIDs(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	_, err := h.store.GetWorkflowByOperationID(ctx, "nope")
	require.ErrorIs(t, err, adminbff.ErrInvalidOperationID)
	_, err = h.store.ListSubjectsByOperation(ctx, "nope")
	require.ErrorIs(t, err, adminbff.ErrInvalidOperationID)
	err = h.store.InsertWorkflowSubject(ctx, adminbff.WorkflowSubject{OperationID: "nope", SubjectUserID: 1, ExpectedIamVersion: 1})
	require.ErrorIs(t, err, adminbff.ErrInvalidOperationID)
	err = h.store.PersistStep(ctx, "nope", "00000000-0000-0000-0000-000000000000", adminbff.StepUpdate{Step: "x"})
	require.ErrorIs(t, err, adminbff.ErrInvalidOperationID)
	err = h.store.BeginCompensation(ctx, op(1), "nope")
	require.ErrorIs(t, err, adminbff.ErrInvalidOperationID)
	err = h.store.CompleteWorkflow(ctx, "nope", "00000000-0000-0000-0000-000000000000", adminbff.WorkflowSucceeded, nil)
	require.ErrorIs(t, err, adminbff.ErrInvalidOperationID)
}

// --- T055: compensation budget + manual recovery --------------------------------

// compensationCount reads the separate compensation-reclaim counter directly.
func compensationCount(t *testing.T, h *harness, opID string) int {
	t.Helper()
	var n int
	err := h.pool.QueryRow(context.Background(),
		"SELECT compensation_attempt_count FROM admin_workflows WHERE operation_id = $1", opID).Scan(&n)
	require.NoError(t, err)
	return n
}

// claimCompensating claims an expired-compensating row; the caller must have
// begun compensation and expired the lease first.
func claimCompensating(t *testing.T, h *harness, owner string) adminbff.Workflow {
	t.Helper()
	got, err := h.store.ClaimWorkflowByOperationID(context.Background(), op(1), owner, 30*time.Second, time.Now())
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, adminbff.WorkflowCompensating, got.State, "expired compensating row must be reclaimable")
	return *got
}

// intoCompensating drives a fresh pending row to claimed-and-compensating.
func intoCompensating(t *testing.T, h *harness) {
	t.Helper()
	claimed := claimOne(t, h, "worker-1")
	require.NoError(t, h.store.BeginCompensation(context.Background(), op(1), *claimed.ClaimToken))
	expireLease(t, h, op(1))
}

func TestClaimWorkflows_ExpiredCompensatingCountsCompensationAttempt(t *testing.T) {
	h := newHarness(t)
	create(t, h, op(1))
	intoCompensating(t, h)

	first := claimCompensating(t, h, "worker-2")
	assert.Equal(t, 1, first.CompensationAttemptCount, "first compensation reclaim counts one")
	assert.Equal(t, 1, first.AttemptCount, "compensation reclaims never consume the forward budget")

	expireLease(t, h, op(1))
	second := claimCompensating(t, h, "worker-3")
	assert.Equal(t, 2, second.CompensationAttemptCount, "second compensation reclaim counts separately")
	assert.Equal(t, 1, second.AttemptCount, "forward budget still untouched")
	assert.Equal(t, 2, compensationCount(t, h, op(1)))
}

func TestClaimWorkflows_CompensationBudgetCap(t *testing.T) {
	h := newHarness(t)
	create(t, h, op(1))
	intoCompensating(t, h)

	// Drive the compensation counter to the cap: 10 reclaims.
	for i := 1; i <= 10; i++ {
		got := claimCompensating(t, h, fmt.Sprintf("worker-%d", i))
		assert.Equal(t, i, got.CompensationAttemptCount, "compensation reclaim %d", i)
		expireLease(t, h, op(1))
	}
	assert.Equal(t, 1, compensationCount(t, h, op(1))-9, "forward counter never moves with compensation reclaims")

	// At the cap the compensating row is no longer claimable — there is no
	// compensation attempt 11 (the exhaustion transitions to failed_manual
	// via ExhaustToFailedManual, not a further reclaim).
	_, err := h.store.ClaimWorkflowByOperationID(context.Background(), op(1), "worker-11", 30*time.Second, time.Now())
	require.ErrorIs(t, err, adminbff.ErrStaleClaim, "compensation budget exhausted: no reclaim")
	claim(t, h, "worker-11", 0)
}

func TestClaimWorkflowByOperationID_ExpiredCompensatingPastRetryDeadlineStale(t *testing.T) {
	h := newHarness(t)
	create(t, h, op(1))
	intoCompensating(t, h)
	expireRetryDeadline(t, h, op(1))

	_, err := h.store.ClaimWorkflowByOperationID(context.Background(), op(1), "worker-2", 30*time.Second, time.Now())
	require.ErrorIs(t, err, adminbff.ErrStaleClaim, "the original immutable retry deadline caps compensation reclaims too")
}

func TestExhaustToFailedManual_TransitionsWithoutLiveLease(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	create(t, h, op(1))

	// Not exhausted: no transition.
	exhausted, err := h.store.ExhaustToFailedManual(ctx, op(1), "attempt_budget_exhausted")
	require.NoError(t, err)
	assert.False(t, exhausted)
	assert.Equal(t, "pending", rawWorkflow(t, h, op(1)).state)

	// Past the retry deadline with no lease: transitions.
	expireRetryDeadline(t, h, op(1))
	exhausted, err = h.store.ExhaustToFailedManual(ctx, op(1), "attempt_budget_exhausted")
	require.NoError(t, err)
	assert.True(t, exhausted)
	r := rawWorkflow(t, h, op(1))
	assert.Equal(t, "failed_manual", r.state)
	requireUnleased(t, r)
	require.True(t, r.lastErrorCode.Valid)
	assert.Equal(t, "attempt_budget_exhausted", r.lastErrorCode.String)

	// Idempotent: a second call sees a terminal row and reports no transition.
	exhausted, err = h.store.ExhaustToFailedManual(ctx, op(1), "attempt_budget_exhausted")
	require.NoError(t, err)
	assert.False(t, exhausted)
}

func TestExhaustToFailedManual_RespectsLiveLease(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	create(t, h, op(1))
	claimOne(t, h, "worker-1") // live lease

	expireRetryDeadline(t, h, op(1))
	exhausted, err := h.store.ExhaustToFailedManual(ctx, op(1), "attempt_budget_exhausted")
	require.NoError(t, err)
	assert.False(t, exhausted, "a live lease still owns the row: its budget guard handles it")
	assert.Equal(t, "running", rawWorkflow(t, h, op(1)).state)
}

func TestExhaustToFailedManual_FromExhaustedCompensation(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	create(t, h, op(1))
	intoCompensating(t, h)
	for i := 1; i <= 10; i++ {
		claimCompensating(t, h, fmt.Sprintf("worker-%d", i))
		expireLease(t, h, op(1))
	}

	// The compensation counter is at the cap: exhaustion parks the row for
	// reconciliation; the exclusion (if any) stays active by construction.
	exhausted, err := h.store.ExhaustToFailedManual(ctx, op(1), "compensation_budget_exhausted")
	require.NoError(t, err)
	assert.True(t, exhausted)
	r := rawWorkflow(t, h, op(1))
	assert.Equal(t, "failed_manual", r.state)
	requireUnleased(t, r)
	require.True(t, r.lastErrorCode.Valid)
	assert.Equal(t, "compensation_budget_exhausted", r.lastErrorCode.String)
}

func TestCompleteRecoveredWorkflow_OnlyFromFailedManual(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	create(t, h, op(1))
	expireRetryDeadline(t, h, op(1))
	exhausted, err := h.store.ExhaustToFailedManual(ctx, op(1), "attempt_budget_exhausted")
	require.NoError(t, err)
	require.True(t, exhausted)

	require.NoError(t, h.store.CompleteRecoveredWorkflow(ctx, op(1), adminbff.WorkflowRejected, map[string]any{
		"error": map[string]any{"kind": "operation_expired", "message": "recovered"},
	}))
	r := rawWorkflow(t, h, op(1))
	assert.Equal(t, "rejected", r.state)
	require.True(t, r.completedAt.Valid, "recovery terminalization stamps completed_at")

	// Already terminal: stale.
	err = h.store.CompleteRecoveredWorkflow(ctx, op(1), adminbff.WorkflowSucceeded, nil)
	require.ErrorIs(t, err, adminbff.ErrStaleClaim)

	// A running row is not recoverable.
	create(t, h, op(2))
	claimOne(t, h, "worker-1")
	err = h.store.CompleteRecoveredWorkflow(ctx, op(2), adminbff.WorkflowRejected, nil)
	require.ErrorIs(t, err, adminbff.ErrStaleClaim)
}

// --- T078: cleanup watermark ---------------------------------------------------

func TestWorkflowEvidenceWatermark_EmptyAndResolved(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// Empty store: zero unresolved, no timestamps.
	wm, err := h.store.WorkflowEvidenceWatermark(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), wm.UnresolvedCount)
	assert.Nil(t, wm.OldestUnresolvedAt)
	assert.Nil(t, wm.OldestWorkflowAt)

	// A pending workflow is unresolved and stamps the workflow watermark.
	create(t, h, op(1))
	wm, err = h.store.WorkflowEvidenceWatermark(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), wm.UnresolvedCount)
	require.NotNil(t, wm.OldestUnresolvedAt)
	require.NotNil(t, wm.OldestWorkflowAt)
	// OldestWorkflowAt = min(created_at) ≈ now; OldestUnresolvedAt ≈ now too.
	assert.WithinDuration(t, time.Now(), *wm.OldestUnresolvedAt, 2*time.Minute)
	assert.WithinDuration(t, time.Now(), *wm.OldestWorkflowAt, 2*time.Minute)

	// A terminal (succeeded) workflow no longer resolves.
	claimed := claimOne(t, h, "worker-1")
	require.NoError(t, h.store.CompleteWorkflow(ctx, op(1), *claimed.ClaimToken, adminbff.WorkflowSucceeded, nil))
	wm, err = h.store.WorkflowEvidenceWatermark(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), wm.UnresolvedCount)
}

func TestWorkflowEvidenceWatermark_ActiveSubjectStillUnresolved(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// A succeeded workflow that still holds an active subject exclusion is
	// referencing evidence (a possibly-applied effect awaiting reconciliation),
	// so it must keep the unresolved watermark at 1.
	create(t, h, op(1))
	claimed := claimOne(t, h, "worker-1")
	require.NoError(t, h.store.InsertWorkflowSubject(ctx, adminbff.WorkflowSubject{
		OperationID: op(1), SubjectUserID: 42, ExpectedIamVersion: 5,
	}))
	require.NoError(t, h.store.CompleteWorkflow(ctx, op(1), *claimed.ClaimToken, adminbff.WorkflowSucceeded, nil))

	wm, err := h.store.WorkflowEvidenceWatermark(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), wm.UnresolvedCount, "active subject exclusion keeps the workflow unresolved")
	require.NotNil(t, wm.OldestUnresolvedAt)

	// Releasing the exclusion resolves it.
	require.NoError(t, h.store.ReleaseSubjectExclusion(ctx, op(1), 42))
	wm, err = h.store.WorkflowEvidenceWatermark(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), wm.UnresolvedCount)
}

func TestRecoveryActions_AppendAndList(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	create(t, h, op(1))

	action := adminbff.RecoveryAction{
		ActionID:            "22222222-0000-0000-0000-000000000001",
		OperationID:         op(1),
		RecoveryPrincipalID: "42",
		AuthorizationSource: "permission:users.write",
		ApprovalID:          ptr("approval-7"),
		ActionType:          adminbff.RecoveryApprovedCompensation,
		ReasonCode:          "operator_review",
		PreviousState:       adminbff.WorkflowFailedManual,
		ResultingState:      adminbff.WorkflowRejected,
		SafeResultCode:      ptr("operation_expired"),
		RequestID:           ptr("req-1"),
		CorrelationID:       ptr("corr-1"),
	}
	recorded, err := h.store.AppendRecoveryAction(ctx, action)
	require.NoError(t, err)
	assert.Equal(t, action.ActionID, recorded.ActionID)
	assert.Equal(t, action.ActionType, recorded.ActionType)
	assert.True(t, recorded.CreatedAt.After(time.Now().Add(-time.Minute)), "server stamps created_at")

	// Without a supplied action id the adapter generates one.
	auto, err := h.store.AppendRecoveryAction(ctx, adminbff.RecoveryAction{
		OperationID:         op(1),
		RecoveryPrincipalID: "7",
		AuthorizationSource: "permission:users.write",
		ActionType:          adminbff.RecoveryReconciliation,
		ReasonCode:          "review",
		PreviousState:       adminbff.WorkflowFailedManual,
		ResultingState:      adminbff.WorkflowFailedManual,
	})
	require.NoError(t, err)
	assert.NotEqual(t, action.ActionID, auto.ActionID)

	ledger, err := h.store.ListRecoveryActions(ctx, op(1))
	require.NoError(t, err)
	require.Len(t, ledger, 2, "append-only ledger in insertion order")
	assert.Equal(t, action.ActionID, ledger[0].ActionID)
	assert.Equal(t, action.ReasonCode, ledger[0].ReasonCode)
	assert.Equal(t, action.ApprovalID, ledger[0].ApprovalID)
	assert.Equal(t, auto.ActionID, ledger[1].ActionID)

	// A different workflow has an empty ledger.
	other, err := h.store.ListRecoveryActions(ctx, op(2))
	require.NoError(t, err)
	assert.Empty(t, other)

	// Invalid operation ids are rejected by both ports.
	_, err = h.store.AppendRecoveryAction(ctx, adminbff.RecoveryAction{OperationID: "nope"})
	require.ErrorIs(t, err, adminbff.ErrInvalidOperationID)
	_, err = h.store.ListRecoveryActions(ctx, "nope")
	require.ErrorIs(t, err, adminbff.ErrInvalidOperationID)
}
