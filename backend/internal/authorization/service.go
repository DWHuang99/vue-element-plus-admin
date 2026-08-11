//go:build rollback

// Package authorization resolves effective user permissions from role grants.
package authorization

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hdw/vue-element-plus-admin/backend/internal/database/sqlc"
)

// Permission codes enforced by the first-generation management API.
const (
	RolesRead        = "roles.read"
	RolesWrite       = "roles.write"
	DepartmentsRead  = "departments.read"
	DepartmentsWrite = "departments.write"
	UsersRead        = "users.read"
	UsersWrite       = "users.write"
)

// Service is the authorization boundary used by handlers and middleware.
// Implementations must read current grants rather than cache decisions.
type Service interface {
	EffectivePermissions(ctx context.Context, userID int64) ([]string, error)
	HasPermission(ctx context.Context, userID int64, permissionCode string) (bool, error)
}

type permissionQueries interface {
	ListEffectivePermissionsByUserID(ctx context.Context, userID int64) ([]string, error)
	HasPermissionByUserID(ctx context.Context, arg sqlc.HasPermissionByUserIDParams) (bool, error)
}

// AuthorizationService implements Service with sqlc queries.
type AuthorizationService struct {
	q permissionQueries
}

// NewService creates an authorization service backed by PostgreSQL.
func NewService(pool *pgxpool.Pool) *AuthorizationService {
	return &AuthorizationService{q: sqlc.New(pool)}
}

// EffectivePermissions returns the distinct, sorted union of grants from all
// current roles. Users without grants receive a non-nil empty slice.
func (s *AuthorizationService) EffectivePermissions(ctx context.Context, userID int64) ([]string, error) {
	permissions, err := s.q.ListEffectivePermissionsByUserID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list effective permissions: %w", err)
	}
	if permissions == nil {
		return []string{}, nil
	}
	return permissions, nil
}

// HasPermission checks current database grants on every call.
func (s *AuthorizationService) HasPermission(ctx context.Context, userID int64, permissionCode string) (bool, error) {
	allowed, err := s.q.HasPermissionByUserID(ctx, sqlc.HasPermissionByUserIDParams{
		UserID: userID,
		Code:   permissionCode,
	})
	if err != nil {
		return false, fmt.Errorf("check permission: %w", err)
	}
	return allowed, nil
}
