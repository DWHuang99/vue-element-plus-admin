package auth

import (
	"context"

	db "vue-element-plus-admin/backend/internal/database/generated"
)

type AuthRepository struct {
	queries *db.Queries
}

type UserAuth struct {
	PasswordHash string
	IsActive     bool
	RoleCode     string
}

func NewRepository(queries *db.Queries) *AuthRepository {
	return &AuthRepository{queries: queries}
}

func (r *AuthRepository) GetUserByUsername(ctx context.Context, username string) (*db.User, error) {
	userRow, err := r.queries.GetUserByUsername(ctx, username)
	if err != nil {
		return nil, err
	}
	user := &db.User{
		ID:        userRow.ID,
		Username:  userRow.Username,
		RoleID:    userRow.RoleID,
		IsActive:  userRow.IsActive,
		CreatedAt: userRow.CreatedAt,
		UpdatedAt: userRow.UpdatedAt,
	}
	return user, nil
}

func (r *AuthRepository) AddUser(ctx context.Context, userinfo db.AddUserParams) (*db.User, error) {
	user, err := r.queries.AddUser(ctx, userinfo)
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *AuthRepository) GetUserAuthByUsername(ctx context.Context, username string) (*UserAuth, error) {
	row, err := r.queries.GetUserAuthByUsername(ctx, username)
	if err != nil {
		return nil, err
	}
	return &UserAuth{
		PasswordHash: row.PasswordHash,
		IsActive:     row.IsActive,
		RoleCode:     row.RoleCode,
	}, nil
}
