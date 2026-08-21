package user

import (
	"context"
	"database/sql"

	db "vue-element-plus-admin/backend/internal/database/iam/generated"
)

type UserRepository struct {
	queries *db.Queries
}

type UserAuth struct {
	ID           int64
	PasswordHash string
	IsActive     bool
}

func NewRepository(queries *db.Queries) *UserRepository {
	return &UserRepository{queries: queries}
}

func (r *UserRepository) WithTx(tx *sql.Tx) *UserRepository {
	return &UserRepository{queries: r.queries.WithTx(tx)}
}

func (r *UserRepository) AddUser(
	ctx context.Context,
	username string,
	passwordHash string,
	roleCode string,
) (*db.User, error) {
	user, err := r.queries.AddUserByRoleCode(ctx, db.AddUserByRoleCodeParams{
		Username:     username,
		PasswordHash: passwordHash,
		Code:         roleCode,
	})
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *UserRepository) GetUserAuthByUsername(ctx context.Context, username string) (*UserAuth, error) {
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

func (r *UserRepository) GetUserAuthByID(ctx context.Context, userID int64) (*UserAuth, error) {
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

func (r *UserRepository) GetUserByID(ctx context.Context, userID int64) (*CurrentUser, error) {
	userRow, err := r.queries.GetUserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	user := &CurrentUser{
		ID:        userRow.ID,
		Username:  userRow.Username,
		RoleID:    userRow.RoleID,
		RoleCode:  userRow.RoleCode,
		RoleName:  userRow.RoleName,
		IsActive:  userRow.IsActive,
		CreatedAt: userRow.CreatedAt,
		UpdatedAt: userRow.UpdatedAt,
	}
	return user, nil
}
