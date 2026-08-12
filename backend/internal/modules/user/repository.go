package user

import (
	"context"

	db "vue-element-plus-admin/backend/internal/database/generated"
)

type UserRepository struct {
	queries *db.Queries
}

func NewRepository(queries *db.Queries) *UserRepository {
	return &UserRepository{queries: queries}
}

func (r *UserRepository) GetUserByUsername(ctx context.Context, username string) (*CurrentUser, error) {
	userRow, err := r.queries.GetUserByUsername(ctx, username)
	if err != nil {
		return nil, err
	}
	user := &CurrentUser{
		ID:          userRow.ID,
		Username:    userRow.Username,
		RoleID:      userRow.RoleID,
		RoleCode:    userRow.RoleCode,
		RoleName:    userRow.RoleName,
		Permissions: userRow.Permissions,
		IsActive:    userRow.IsActive,
		CreatedAt:   userRow.CreatedAt,
		UpdatedAt:   userRow.UpdatedAt,
	}
	return user, nil
}
