package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hdw/vue-element-plus-admin/backend/internal/database/sqlc"
)

// UserView is the user representation exposed over the API.
type UserView struct {
	ID        int64     `json:"id"`
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"created_at"`
}

// AuthResult is the success payload returned by register and login.
type AuthResult struct {
	Token     string   `json:"token"`
	TokenType string   `json:"token_type"`
	ExpiresIn int64    `json:"expires_in"`
	User      UserView `json:"user"`
}

// AuthUser is the authenticated principal injected into request context.
type AuthUser struct {
	ID       int64
	Username string
}

// DepartmentProfile is the department reference in a user profile.
type DepartmentProfile struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// RoleProfile is a role reference in a user profile.
type RoleProfile struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Code string `json:"code"`
}

// UserProfile is the extended /auth/me payload: user + department + roles.
type UserProfile struct {
	ID         int64              `json:"id"`
	Username   string             `json:"username"`
	Account    string             `json:"account"`
	Email      string             `json:"email"`
	CreatedAt  time.Time          `json:"created_at"`
	Department *DepartmentProfile `json:"department"`
	Roles      []RoleProfile      `json:"roles"`
}

// Service is the auth business logic boundary (independent, testable service layer).
type Service interface {
	Register(ctx context.Context, username, password string) (*AuthResult, error)
	Login(ctx context.Context, username, password string) (*AuthResult, error)
	Logout(ctx context.Context, tokenHash string) error
	Authenticate(ctx context.Context, tokenHash string) (*AuthUser, error)
	GetUserProfile(ctx context.Context, userID int64) (*UserProfile, error)
}

// AuthService implements Service against PostgreSQL via sqlc.
type AuthService struct {
	pool *pgxpool.Pool
	q    sqlc.Querier
}

// dummyHash is used to burn a comparable amount of work when a login targets
// a nonexistent user, keeping response timing uniform (anti-enumeration).
var dummyHash = func() string {
	h, err := HashPassword("dummy-password-for-timing")
	if err != nil {
		panic(err)
	}
	return h
}()

// NewAuthService creates an AuthService.
// q may be nil; when nil, the pool is used to derive queries per call.
func NewAuthService(pool *pgxpool.Pool) *AuthService {
	return &AuthService{pool: pool, q: sqlc.New(pool)}
}

// Register creates a new user and a session in a single transaction, then
// returns the session token (register implies login).
func (s *AuthService) Register(ctx context.Context, username, password string) (*AuthResult, error) {
	// Early uniqueness check for a clean 409 (the UNIQUE constraint is the final guard).
	if _, err := s.q.GetUserByUsername(ctx, username); err == nil {
		return nil, ErrUsernameTaken
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("check username: %w", err)
	}

	hash, err := HashPassword(password)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	token, err := GenerateToken()
	if err != nil {
		return nil, fmt.Errorf("generate token: %w", err)
	}

	now := time.Now()
	tokenHash := HashToken(token)

	// Register is multi-write: user + session must be atomic.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := sqlc.New(tx)

	user, err := qtx.CreateUser(ctx, sqlc.CreateUserParams{
		Username:     username,
		PasswordHash: hash,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrUsernameTaken
		}
		return nil, fmt.Errorf("create user: %w", err)
	}

	if _, err := qtx.CreateSession(ctx, sqlc.CreateSessionParams{
		TokenHash: tokenHash,
		UserID:    user.ID,
		ExpiresAt: pgtype.Timestamptz{Time: SessionExpiry(now), Valid: true},
	}); err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	// Assign the default 'user' role so every account has at least one role
	// (phase-2 permission filtering depends on it). Same transaction as the user.
	defaultRole, err := qtx.GetRoleByCode(ctx, "user")
	if err != nil {
		return nil, fmt.Errorf("get default role: %w", err)
	}
	if err := qtx.InsertUserRole(ctx, sqlc.InsertUserRoleParams{UserID: user.ID, RoleID: defaultRole.ID}); err != nil {
		return nil, fmt.Errorf("assign default role: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}

	return &AuthResult{
		Token:     token,
		TokenType: "Bearer",
		ExpiresIn: int64(SessionTTL.Seconds()),
		User: UserView{
			ID:        user.ID,
			Username:  user.Username,
			CreatedAt: user.CreatedAt.Time,
		},
	}, nil
}

// Login verifies credentials and issues a session token.
// Both "username not found" and "wrong password" return ErrInvalidCredentials
// with uniform timing to prevent account enumeration.
func (s *AuthService) Login(ctx context.Context, username, password string) (*AuthResult, error) {
	user, err := s.q.GetUserByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Burn comparable work so timing does not reveal whether the user exists.
			_, _ = VerifyPassword(dummyHash, password)
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("get user: %w", err)
	}

	ok, err := VerifyPassword(user.PasswordHash, password)
	if err != nil || !ok {
		return nil, ErrInvalidCredentials
	}

	token, err := GenerateToken()
	if err != nil {
		return nil, fmt.Errorf("generate token: %w", err)
	}

	now := time.Now()
	tokenHash := HashToken(token)

	if _, err := s.q.CreateSession(ctx, sqlc.CreateSessionParams{
		TokenHash: tokenHash,
		UserID:    user.ID,
		ExpiresAt: pgtype.Timestamptz{Time: SessionExpiry(now), Valid: true},
	}); err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	return &AuthResult{
		Token:     token,
		TokenType: "Bearer",
		ExpiresIn: int64(SessionTTL.Seconds()),
		User: UserView{
			ID:        user.ID,
			Username:  user.Username,
			CreatedAt: user.CreatedAt.Time,
		},
	}, nil
}

// Logout revokes a session token. Idempotent: revoking an already-revoked or
// unknown token succeeds without error (see contracts/auth-api.md).
func (s *AuthService) Logout(ctx context.Context, tokenHash string) error {
	return s.q.RevokeSessionByTokenHash(ctx, tokenHash)
}

// Authenticate validates a session token and returns the authenticated user.
// Invalid/expired/revoked tokens all map to ErrInvalidToken. Valid sessions
// are slid forward (renewed) per the lifecycle rule in data-model.md.
func (s *AuthService) Authenticate(ctx context.Context, tokenHash string) (*AuthUser, error) {
	row, err := s.q.GetSessionByTokenHash(ctx, tokenHash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrInvalidToken
		}
		return nil, fmt.Errorf("get session: %w", err)
	}

	if row.RevokedAt.Valid {
		return nil, ErrInvalidToken
	}

	now := time.Now()
	if !SessionIsValid(now, row.CreatedAt.Time, row.ExpiresAt.Time) {
		return nil, ErrInvalidToken
	}

	// Sliding renewal: extend expires_at and update last_used_at.
	if err := s.q.TouchSession(ctx, sqlc.TouchSessionParams{
		TokenHash: tokenHash,
		ExpiresAt: pgtype.Timestamptz{Time: RenewExpiry(now), Valid: true},
	}); err != nil {
		return nil, fmt.Errorf("touch session: %w", err)
	}

	return &AuthUser{ID: row.UserID, Username: row.UserUsername}, nil
}

// GetUserProfile returns the extended /auth/me payload for a user id:
// user fields plus department and roles (roles are always non-empty after
// the register default-role assignment).
func (s *AuthService) GetUserProfile(ctx context.Context, userID int64) (*UserProfile, error) {
	u, err := s.q.GetUserFullByID(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrInvalidToken
		}
		return nil, fmt.Errorf("get user: %w", err)
	}

	profile := &UserProfile{
		ID:        u.ID,
		Username:  u.Username,
		CreatedAt: u.CreatedAt.Time,
	}
	if u.Account.Valid {
		profile.Account = u.Account.String
	}
	if u.Email.Valid {
		profile.Email = u.Email.String
	}

	if u.DepartmentID.Valid {
		dept, err := s.q.GetDepartmentByID(ctx, u.DepartmentID.Int64)
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return nil, fmt.Errorf("get department: %w", err)
			}
		} else {
			profile.Department = &DepartmentProfile{ID: dept.ID, Name: dept.Name}
		}
	}

	roles, err := s.q.ListRolesByUserID(ctx, u.ID)
	if err != nil {
		return nil, fmt.Errorf("list roles: %w", err)
	}
	profile.Roles = make([]RoleProfile, 0, len(roles))
	for _, r := range roles {
		profile.Roles = append(profile.Roles, RoleProfile{ID: r.ID, Name: r.Name, Code: r.Code})
	}
	return profile, nil
}

// isUniqueViolation reports whether err is a PostgreSQL unique-violation (23505).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
