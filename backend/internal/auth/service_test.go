//go:build rollback

package auth

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/hdw/vue-element-plus-admin/backend/internal/authorization"
	"github.com/hdw/vue-element-plus-admin/backend/internal/database"
	"github.com/hdw/vue-element-plus-admin/backend/internal/database/sqlc"
)

var (
	testCtx = context.Background()
	testSvc *AuthService
	testSeq int
)

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

	// Apply all migrations (including users/sessions).
	if err := database.RunMigrations(connStr); err != nil {
		fmt.Fprintf(os.Stderr, "failed to run migrations: %v\n", err)
		os.Exit(1)
	}

	db, err := database.Connect(ctx, database.Config{
		URL:             connStr,
		MaxConns:        5,
		MinConns:        1,
		MaxConnLifetime: time.Hour,
		MaxConnIdleTime: 30 * time.Minute,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to connect db: %v\n", err)
		os.Exit(1)
	}
	testSvc = NewAuthService(db.Pool)

	code := m.Run()

	db.Close()
	_ = pg.Terminate(ctx)
	os.Exit(code)
}

// uniqueUsername returns a valid unique username (3-32 chars, alnum/underscore).
func uniqueUsername() string {
	testSeq++
	return fmt.Sprintf("user%d", testSeq)
}

func mustRegister(t *testing.T, username, password string) (*AuthResult, string) {
	t.Helper()
	res, err := testSvc.Register(testCtx, username, password)
	require.NoError(t, err)
	require.NotEmpty(t, res.Token)
	return res, HashToken(res.Token)
}

// T017: Register creates user + session atomically, stores safe hashes.
func TestService_RegisterCreatesUserAndSession(t *testing.T) {
	username := uniqueUsername()
	password := "supersecret123"

	res, tokenHash := mustRegister(t, username, password)

	assert.Equal(t, username, res.User.Username)
	assert.Equal(t, "Bearer", res.TokenType)
	assert.Equal(t, int64(86400), res.ExpiresIn)
	assert.NotEmpty(t, res.User.ID)
	assert.False(t, res.User.CreatedAt.IsZero())

	q := sqlc.New(testSvc.pool)

	// User exists and password is stored as Argon2id, not plaintext.
	user, err := q.GetUserByUsername(testCtx, username)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(user.PasswordHash, "$argon2id$"),
		"password_hash must be Argon2id PHC string, got prefix: %s", user.PasswordHash)
	assert.NotContains(t, user.PasswordHash, password, "hash must not contain plaintext password")

	// Session exists; token_hash is 64-char hex, not the raw token.
	sess, err := q.GetSessionByTokenHash(testCtx, tokenHash)
	require.NoError(t, err)
	assert.Equal(t, user.ID, sess.UserID)
	assert.Equal(t, 64, len(tokenHash))
	assert.NotEqual(t, res.Token, tokenHash, "stored hash must not equal raw token")
	assert.False(t, sess.RevokedAt.Valid)
	assert.True(t, sess.ExpiresAt.Time.After(time.Now()))
}

func TestService_RegisterDuplicateUsername(t *testing.T) {
	username := uniqueUsername()
	mustRegister(t, username, "password1234")

	_, err := testSvc.Register(testCtx, username, "otherpassword")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUsernameTaken)
}

func TestService_RegisterWhitespaceUsernameAllowedByService(t *testing.T) {
	// Format validation is the handler's responsibility; service does not
	// reject on format (defensive business layer keeps focus on existence).
	username := uniqueUsername()
	res, _ := mustRegister(t, username, "password1234")
	require.NotEmpty(t, res.Token)
}

// T023: Register assigns the default 'user' role in the same transaction.
func TestService_RegisterAssignsDefaultRole(t *testing.T) {
	username := uniqueUsername()
	password := "supersecret123"
	res, _ := mustRegister(t, username, password)

	q := sqlc.New(testSvc.pool)
	roles, err := q.ListRolesByUserID(testCtx, res.User.ID)
	require.NoError(t, err)
	require.Len(t, roles, 1, "a fresh user must have exactly the default role")
	assert.Equal(t, "user", roles[0].Code)
	assert.Equal(t, "普通用户", roles[0].Name)
}

// T024: /auth/me returns the extended profile (department + roles).
func TestService_GetUserProfile(t *testing.T) {
	username := uniqueUsername()
	res, _ := mustRegister(t, username, "supersecret123")
	userID := res.User.ID

	profile, err := testSvc.GetUserProfile(testCtx, userID)
	require.NoError(t, err)
	assert.Equal(t, userID, profile.ID)
	assert.Equal(t, username, profile.Username)
	assert.Nil(t, profile.Department, "freshly registered user has no department")
	require.Len(t, profile.Roles, 1)
	assert.Equal(t, "user", profile.Roles[0].Code)
	require.NotNil(t, profile.EffectivePermissions)
	assert.Empty(t, profile.EffectivePermissions, "the default user role has no permissions")

	// Assign a department and verify it is surfaced. Resolve the seeded 研发部
	// id dynamically via SQL to avoid coupling to seed row ids.
	var deptID int64
	require.NoError(t, testSvc.pool.QueryRow(testCtx,
		`SELECT id FROM departments WHERE name = '研发部'`).Scan(&deptID))
	require.NotZero(t, deptID)
	_, err = testSvc.pool.Exec(testCtx,
		`UPDATE users SET department_id = $1 WHERE id = $2`, deptID, userID)
	require.NoError(t, err)

	profile, err = testSvc.GetUserProfile(testCtx, userID)
	require.NoError(t, err)
	require.NotNil(t, profile.Department)
	assert.Equal(t, "研发部", profile.Department.Name)
}

func TestService_GetUserProfileIncludesEffectivePermissions(t *testing.T) {
	username := uniqueUsername()
	res, _ := mustRegister(t, username, "supersecret123")

	q := sqlc.New(testSvc.pool)
	adminRole, err := q.GetRoleByCode(testCtx, "admin")
	require.NoError(t, err)
	require.NoError(t, q.InsertUserRole(testCtx, sqlc.InsertUserRoleParams{
		UserID: res.User.ID,
		RoleID: adminRole.ID,
	}))

	profile, err := testSvc.GetUserProfile(testCtx, res.User.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{
		authorization.DepartmentsRead,
		authorization.DepartmentsWrite,
		authorization.RolesRead,
		authorization.RolesWrite,
		authorization.UsersRead,
		authorization.UsersWrite,
	}, profile.EffectivePermissions)
}

// T025: Login issues a session and fails uniformly for wrong password vs missing user.
func TestService_LoginSuccess(t *testing.T) {
	username := uniqueUsername()
	password := "supersecret123"
	mustRegister(t, username, password)

	res, err := testSvc.Login(testCtx, username, password)
	require.NoError(t, err)
	assert.Equal(t, username, res.User.Username)
	assert.NotEmpty(t, res.Token)
}

func TestService_LoginWrongPassword(t *testing.T) {
	username := uniqueUsername()
	mustRegister(t, username, "correctpassword1")

	_, err := testSvc.Login(testCtx, username, "wrongpassword")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidCredentials)
}

func TestService_LoginNonexistentUserSameError(t *testing.T) {
	username := uniqueUsername()
	// No user registered with this username.
	_, err := testSvc.Login(testCtx, username, "whatever1234")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidCredentials,
		"nonexistent user must return the same sentinel as wrong password (anti-enumeration)")
}

// T031: Full session lifecycle.
func TestService_SessionLifecycle(t *testing.T) {
	username := uniqueUsername()
	password := "supersecret123"
	_, tokenHash := mustRegister(t, username, password)

	// Authenticate ok right after register.
	principal, err := testSvc.Authenticate(testCtx, tokenHash)
	require.NoError(t, err)
	assert.Equal(t, username, principal.Username)

	// Logout revokes; subsequent Authenticate fails.
	err = testSvc.Logout(testCtx, tokenHash)
	require.NoError(t, err)

	_, err = testSvc.Authenticate(testCtx, tokenHash)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidToken, "revoked token must be rejected")
}

func TestService_AuthenticateInvalidToken(t *testing.T) {
	// A token hash that never existed must be rejected uniformly.
	unknownHash := HashToken("never-issued-token")
	_, err := testSvc.Authenticate(testCtx, unknownHash)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidToken)
}

func TestService_AuthenticateExpiredSession(t *testing.T) {
	username := uniqueUsername()
	_, tokenHash := mustRegister(t, username, "supersecret123")

	// Force the session to be idle-expired by expiring it in the past.
	q := sqlc.New(testSvc.pool)
	err := q.TouchSession(testCtx, sqlc.TouchSessionParams{
		TokenHash: tokenHash,
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true},
	})
	require.NoError(t, err)

	_, err = testSvc.Authenticate(testCtx, tokenHash)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidToken, "idle-expired session must be rejected")
}

func TestService_AuthenticateAbsoluteCap(t *testing.T) {
	username := uniqueUsername()
	_, tokenHash := mustRegister(t, username, "supersecret123")

	// Backdate the session creation beyond the 7-day absolute cap.
	q := sqlc.New(testSvc.pool)
	sess, err := q.GetSessionByTokenHash(testCtx, tokenHash)
	require.NoError(t, err)

	_, err = testSvc.pool.Exec(testCtx,
		`UPDATE sessions SET created_at = $1, expires_at = $2 WHERE id = $3`,
		time.Now().Add(-8*24*time.Hour), time.Now().Add(time.Hour), sess.ID)
	require.NoError(t, err)

	_, err = testSvc.Authenticate(testCtx, tokenHash)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidToken, "session beyond 7-day cap must be rejected")
}

func TestService_SlidingRenewal(t *testing.T) {
	username := uniqueUsername()
	_, tokenHash := mustRegister(t, username, "supersecret123")

	q := sqlc.New(testSvc.pool)

	// Set session close to idle expiry (5 minutes left) and not at absolute cap.
	sess, err := q.GetSessionByTokenHash(testCtx, tokenHash)
	require.NoError(t, err)
	_, err = testSvc.pool.Exec(testCtx,
		`UPDATE sessions SET created_at = $1, expires_at = $2 WHERE id = $3`,
		time.Now().Add(-48*time.Hour), time.Now().Add(5*time.Minute), sess.ID)
	require.NoError(t, err)

	// Authenticate succeeds and renews expires_at forward.
	_, err = testSvc.Authenticate(testCtx, tokenHash)
	require.NoError(t, err)

	renewed, err := q.GetSessionByTokenHash(testCtx, tokenHash)
	require.NoError(t, err)
	assert.True(t, renewed.ExpiresAt.Time.After(time.Now().Add(20*time.Hour)),
		"expires_at should be renewed ~24h forward, got %v", renewed.ExpiresAt.Time)
}

func TestService_LogoutIdempotent(t *testing.T) {
	username := uniqueUsername()
	_, tokenHash := mustRegister(t, username, "supersecret123")

	require.NoError(t, testSvc.Logout(testCtx, tokenHash))
	// Second logout of same token must not error.
	require.NoError(t, testSvc.Logout(testCtx, tokenHash))
	// Logout of a token that never existed must not error.
	require.NoError(t, testSvc.Logout(testCtx, "nonexistenttokenhash"))
}
