package auth

import (
	"context"

	db "vue-element-plus-admin/backend/internal/database/generated"
)

type AuthRepository struct {
	queries *db.Queries
}

func NewRepository(queries *db.Queries) *AuthRepository {
	return &AuthRepository{queries: queries}
}

func (r *AuthRepository) GetUserByUsername(ctx context.Context, username string) (*db.User, error) {
	user, err := r.queries.GetUserByUsername(ctx, username)
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *AuthRepository) AddUser(ctx context.Context, userinfo db.AddUserParams) (*db.User, error) {
	user, err := r.queries.AddUser(ctx, userinfo)
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *AuthRepository) GetUserPassword(ctx context.Context, username string) (string, error) {
	hash, err := r.queries.GetUserPassword(ctx, username)
	if err != nil {
		return "", err
	}
	return hash, nil
}
