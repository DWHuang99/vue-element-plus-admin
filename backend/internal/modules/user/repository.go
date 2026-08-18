package user

import (
	"context"

	db "vue-element-plus-admin/backend/internal/database/iam/generated"
)

type UserRepository struct {
	queries *db.Queries
}

func NewRepository(queries *db.Queries) *UserRepository {
	return &UserRepository{queries: queries}
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
