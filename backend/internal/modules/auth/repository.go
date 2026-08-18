package auth

import (
	"context"

	db "vue-element-plus-admin/backend/internal/database/iam/generated"
)

type AuthRepository struct {
	queries *db.Queries
}

type UserAuth struct {
	ID           int64
	PasswordHash string
	IsActive     bool
}

func NewRepository(queries *db.Queries) *AuthRepository {
	return &AuthRepository{queries: queries}
}

func (r *AuthRepository) AddUser(ctx context.Context, username, passwordHash, roleCode string) (*db.User, error) {
	row, err := r.queries.AddUserByRoleCode(ctx, db.AddUserByRoleCodeParams{
		Username:     username,
		PasswordHash: passwordHash,
		Code:         roleCode,
	})
	if err != nil {
		return nil, err
	}
	return &db.User{
		ID:           row.ID,
		Username:     row.Username,
		PasswordHash: row.PasswordHash,
		RoleID:       row.RoleID,
		IsActive:     row.IsActive,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}, nil
}

func (r *AuthRepository) GetUserAuthByUsername(ctx context.Context, username string) (*UserAuth, error) {
	row, err := r.queries.GetUserAuthByUsername(ctx, username)
	if err != nil {
		return nil, err
	}
	return &UserAuth{
		ID:           row.ID,
		PasswordHash: row.PasswordHash,
		IsActive:     row.IsActive,
	}, nil
}

func (r *AuthRepository) GetUserAuthByID(ctx context.Context, userID int64) (*UserAuth, error) {
	row, err := r.queries.GetUserAuthByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &UserAuth{
		ID:           row.ID,
		PasswordHash: row.PasswordHash,
		IsActive:     row.IsActive,
	}, nil
}
