package database

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/hdw/vue-element-plus-admin/backend/db/migrations"
)

// TestMain boots a shared Postgres container and applies all migrations once.
// Individual tests build on this baseline (schema + seeds present).
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
		fmt.Fprintf(os.Stderr, "failed to start postgres container: %v\n", err)
		os.Exit(1)
	}

	connStr, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to get connection string: %v\n", err)
		os.Exit(1)
	}

	if err := RunMigrations(connStr); err != nil {
		fmt.Fprintf(os.Stderr, "failed to run migrations: %v\n", err)
		os.Exit(1)
	}

	conn, err := pgx.Connect(ctx, connStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to connect: %v\n", err)
		os.Exit(1)
	}

	testConnStr = connStr
	testConn = conn

	code := m.Run()

	conn.Close(context.Background())
	_ = pg.Terminate(ctx)
	os.Exit(code)
}

var (
	testConnStr string
	testConn    *pgx.Conn
)

// swapDB returns a copy of testConnStr pointing at another database on the same server.
func swapDB(dbname string) string {
	// postgres://test:test@host:port/scaffold_test?sslmode=disable
	idx := strings.Index(testConnStr, "/scaffold_test?")
	if idx == -1 {
		return testConnStr
	}
	return testConnStr[:idx] + "/" + dbname + testConnStr[idx+len("/scaffold_test"):]
}

func TestRBAC_TablesCreated(t *testing.T) {
	ctx := context.Background()
	for _, table := range []string{"departments", "roles", "user_roles"} {
		var exists bool
		err := testConn.QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1)",
			table).Scan(&exists)
		require.NoError(t, err)
		assert.True(t, exists, "table %s should exist", table)
	}

	// users got the extended columns.
	for _, col := range []string{"account", "email", "department_id"} {
		var exists bool
		err := testConn.QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'users' AND column_name = $1)",
			col).Scan(&exists)
		require.NoError(t, err)
		assert.True(t, exists, "users.%s should exist", col)
	}
}

func TestRBAC_SeedData(t *testing.T) {
	ctx := context.Background()

	// Default roles seeded.
	var roleCount int
	require.NoError(t, testConn.QueryRow(ctx, "SELECT count(*) FROM roles").Scan(&roleCount))
	assert.Equal(t, 3, roleCount, "three default roles seeded")

	for _, code := range []string{"super_admin", "admin", "user"} {
		var name string
		err := testConn.QueryRow(ctx, "SELECT name FROM roles WHERE code = $1", code).Scan(&name)
		require.NoError(t, err, "role %s seeded", code)
		assert.NotEmpty(t, name)
	}

	// Department tree seeded: 研发部 has children 前端组/后端组.
	var devID int64
	require.NoError(t, testConn.QueryRow(ctx, "SELECT id FROM departments WHERE name = '研发部'").Scan(&devID))
	var children int
	require.NoError(t, testConn.QueryRow(ctx,
		"SELECT count(*) FROM departments WHERE parent_id = $1 AND name IN ('前端组', '后端组')", devID).Scan(&children))
	assert.Equal(t, 2, children, "研发部 should have 前端组 and 后端组 children")
}

func TestRBAC_UniqueConstraints(t *testing.T) {
	ctx := context.Background()

	// Duplicate role name.
	_, err := testConn.Exec(ctx, `INSERT INTO roles (name, code) VALUES ('普通用户', 'user_dup')`)
	require.Error(t, err, "duplicate role name must fail")
	assert.Contains(t, err.Error(), "23505", "unique violation sqlstate")

	// Duplicate role code.
	_, err = testConn.Exec(ctx, `INSERT INTO roles (name, code) VALUES ('重复码', 'user')`)
	require.Error(t, err, "duplicate role code must fail")
	assert.Contains(t, err.Error(), "23505")

	// Duplicate department name.
	_, err = testConn.Exec(ctx, `INSERT INTO departments (name, parent_id) VALUES ('研发部', NULL)`)
	require.Error(t, err, "duplicate department name must fail")
	assert.Contains(t, err.Error(), "23505")
}

func TestRBAC_FKConstraints(t *testing.T) {
	ctx := context.Background()

	// user_roles requires existing users/roles.
	_, err := testConn.Exec(ctx, `INSERT INTO user_roles (user_id, role_id) VALUES (999999, 1)`)
	require.Error(t, err, "user_roles must reject unknown user")
	assert.Contains(t, err.Error(), "23503", "foreign key violation sqlstate")

	// role FK: use a real user with a nonexistent role id.
	var uid int64
	require.NoError(t, testConn.QueryRow(ctx,
		`INSERT INTO users (username, password_hash) VALUES ('fk_role_user', 'x') RETURNING id`).Scan(&uid))
	_, err = testConn.Exec(ctx, `INSERT INTO user_roles (user_id, role_id) VALUES ($1, 999999)`, uid)
	require.Error(t, err, "user_roles must reject unknown role")

	// users.department_id requires an existing department.
	_, err = testConn.Exec(ctx, `INSERT INTO users (username, password_hash, department_id) VALUES ('fk_test_user', 'x', 999999)`)
	require.Error(t, err, "users.department_id must reject unknown department")
}

func TestRBAC_CascadeDeleteUser(t *testing.T) {
	ctx := context.Background()

	// Create a user + role link, then delete the user and expect the link to cascade.
	var uid int64
	require.NoError(t, testConn.QueryRow(ctx,
		`INSERT INTO users (username, password_hash) VALUES ('cascade_user', 'x') RETURNING id`).Scan(&uid))

	var rid int64
	require.NoError(t, testConn.QueryRow(ctx, `SELECT id FROM roles WHERE code = 'user'`).Scan(&rid))
	_, err := testConn.Exec(ctx, `INSERT INTO user_roles (user_id, role_id) VALUES ($1, $2)`, uid, rid)
	require.NoError(t, err)

	_, err = testConn.Exec(ctx, `DELETE FROM users WHERE id = $1`, uid)
	require.NoError(t, err)

	var n int
	require.NoError(t, testConn.QueryRow(ctx,
		`SELECT count(*) FROM user_roles WHERE user_id = $1`, uid).Scan(&n))
	assert.Zero(t, n, "user_roles row must cascade-delete with user")
}

// TestRBAC_DefaultRoleForExistingUsers verifies the seed that assigns the
// default 'user' role to users created before migration 000004 ran.
// It runs on a dedicated database: migrate to 000003, insert a user, then
// apply 000004 and assert the user gained the default role.
func TestRBAC_DefaultRoleForExistingUsers(t *testing.T) {
	ctx := context.Background()

	// Create a fresh database on the same container.
	_, err := testConn.Exec(ctx, `CREATE DATABASE scaffold_partial`)
	require.NoError(t, err)
	defer func() {
		_, _ = testConn.Exec(ctx, `DROP DATABASE scaffold_partial WITH (FORCE)`)
	}()

	partialURL := swapDB("scaffold_partial")
	require.NotEqual(t, testConnStr, partialURL, "partial DB URL should differ")

	src, err := iofs.New(migrations.FS, ".")
	require.NoError(t, err)
	m, err := migrate.NewWithSourceInstance("iofs", src, partialURL)
	require.NoError(t, err)
	defer m.Close()

	// Migrate only through 000003 (users/sessions exist, RBAC does not).
	require.NoError(t, m.Migrate(3))

	pconn, err := pgx.Connect(ctx, partialURL)
	require.NoError(t, err)
	defer pconn.Close(ctx)

	var uid int64
	require.NoError(t, pconn.QueryRow(ctx,
		`INSERT INTO users (username, password_hash) VALUES ('pre_rbac_user', 'x') RETURNING id`).Scan(&uid))

	// Apply the RBAC migration; its seed must assign the default role.
	require.NoError(t, m.Up())

	var roleCode string
	err = pconn.QueryRow(ctx,
		`SELECT r.code FROM user_roles ur JOIN roles r ON r.id = ur.role_id WHERE ur.user_id = $1`, uid).
		Scan(&roleCode)
	require.NoError(t, err, "pre-existing user should gain a role from the seed")
	assert.Equal(t, "user", roleCode)
}
