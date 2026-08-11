// Organization application contract suite (contracts/organization-application.md;
// task T033).
//
// Contract-first: written before the US2 application surface exists, so the
// suite runs RED on the not-yet-implemented behaviors (stubs in us2_stubs.go)
// and GREEN on the US1 surface it already pins. T034-T040 are driven to green
// by this suite; us2_stubs.go is deleted with T040.
//
// The suite lives in the external test package organization_test because it
// imports the adapter (internal/organization/postgres), which itself imports
// organization — an in-package test file would create an import cycle.
//
// Every test runs on its own scratch database (fresh + migrated to head), so
// assertions never share state and the shared cluster (cluster-global roles
// created by 000008) is only ever touched by the migrator, not by tests.
package organization_test

import (
	"context"
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
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/hdw/vue-element-plus-admin/backend/db/migrations"
	"github.com/hdw/vue-element-plus-admin/backend/internal/organization"
	orgpostgres "github.com/hdw/vue-element-plus-admin/backend/internal/organization/postgres"
)

// TestMain boots a shared Postgres container once; each test derives its own
// scratch database from it (freshDB) so assertions never share state.
func TestMain(m *testing.M) {
	ctx := context.Background()

	pg, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("organization_contract"),
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
	dbname := fmt.Sprintf("organization_contract_%d", time.Now().UnixNano())
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
	// postgres://test:test@host:port/organization_contract?sslmode=disable
	idx := strings.Index(testConnStr, "/organization_contract?")
	if idx == -1 {
		t.Fatal("unexpected connection string shape")
	}
	return testConnStr[:idx] + "/" + dbname + testConnStr[idx+len("/organization_contract"):]
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

// countingStore wraps the real adapter and counts port calls, so the suite
// can prove bounded query sets (no N+1). Every harness uses it; behavior is
// fully delegated.
type countingStore struct {
	organization.Store
	counts map[string]int
}

func (c *countingStore) GetUserMembershipByUserID(ctx context.Context, userID int64) (*organization.MembershipState, error) {
	c.counts["GetUserMembershipByUserID"]++
	return c.Store.GetUserMembershipByUserID(ctx, userID)
}

func (c *countingStore) BatchGetUserMemberships(ctx context.Context, userIDs []int64) (map[int64]organization.MembershipState, error) {
	c.counts["BatchGetUserMemberships"]++
	return c.Store.BatchGetUserMemberships(ctx, userIDs)
}

func (c *countingStore) ListUserIDsByDepartment(ctx context.Context, departmentID int64) ([]int64, error) {
	c.counts["ListUserIDsByDepartment"]++
	return c.Store.ListUserIDsByDepartment(ctx, departmentID)
}

// harness wires the service under test to a fresh migrated database, plus
// direct-sql access for seeding and cross-checking DB truth.
type harness struct {
	pool     *pgxpool.Pool
	db       *pgx.Conn
	svc      *organization.Service
	counting *countingStore
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

	counting := &countingStore{Store: orgpostgres.NewStore(pool), counts: map[string]int{}}
	return &harness{pool: pool, db: db, svc: organization.NewService(counting, slog.New(slog.NewTextHandler(io.Discard, nil))), counting: counting}
}

// --- domain helpers ---------------------------------------------------------

// opID builds a UUID-shaped operation id from a small int (receipts are keyed
// by UUID in the database).
func opID(n int) string {
	return fmt.Sprintf("00000000-0000-0000-0000-%012d", n)
}

// uuidEventID builds a UUID-shaped event id (inbox dedupe key).
func uuidEventID(n int) string {
	return fmt.Sprintf("00000000-0000-0000-0000-%012d", n)
}

// opCtx returns a workflow operation context for organization mutations.
func opCtx(n int) organization.OperationContext {
	return organization.OperationContext{
		OperationID:   opID(n),
		CorrelationID: "contract-test-correlation",
	}
}

func int64ptr(v int64) *int64 { return &v }

// deletedEvent builds a valid iam.user.deleted envelope (version 1) for userID.
func deletedEvent(n int, userID int64) organization.IAMUserDeletedEvent {
	return organization.IAMUserDeletedEvent{
		EventID:          uuidEventID(n),
		EventType:        "iam.user.deleted",
		EventVersion:     1,
		Producer:         "iam",
		AggregateType:    "user",
		AggregateID:      fmt.Sprintf("%d", userID),
		AggregateVersion: 2,
		UserID:           userID,
	}
}

// --- harness helpers --------------------------------------------------------

// saveDept creates a department through the service and returns its id.
func (h *harness) saveDept(t *testing.T, name string, parentID *int64) int64 {
	t.Helper()
	id, err := h.svc.SaveDepartment(context.Background(), opCtx(900), nil, name, parentID)
	require.NoError(t, err)
	return id
}

// seedMembership inserts a membership state row directly.
func (h *harness) seedMembership(t *testing.T, userID, departmentID, version int64) {
	t.Helper()
	_, err := h.db.Exec(context.Background(),
		"INSERT INTO organization_user_departments (user_id, department_id, membership_version) VALUES ($1, $2, $3)",
		userID, departmentID, version)
	require.NoError(t, err)
}

// seedTombstone inserts a null-department membership state row directly.
func (h *harness) seedTombstone(t *testing.T, userID, version int64) {
	t.Helper()
	_, err := h.db.Exec(context.Background(),
		"INSERT INTO organization_user_departments (user_id, department_id, membership_version) VALUES ($1, NULL, $2)",
		userID, version)
	require.NoError(t, err)
}

// membershipRow reads the state row for userID; the caller must expect it to
// exist (the terminal user-deleted handler physically removes rows).
func (h *harness) membershipRow(t *testing.T, userID int64) (*int64, int64) {
	t.Helper()
	var dept *int64
	var version int64
	require.NoError(t, h.db.QueryRow(context.Background(),
		"SELECT department_id, membership_version FROM organization_user_departments WHERE user_id = $1",
		userID).Scan(&dept, &version))
	return dept, version
}

func (h *harness) countWhere(t *testing.T, table, where string, args ...any) int64 {
	t.Helper()
	var n int64
	require.NoError(t, h.db.QueryRow(context.Background(), "SELECT count(*) FROM "+table+" WHERE "+where, args...).Scan(&n))
	return n
}
