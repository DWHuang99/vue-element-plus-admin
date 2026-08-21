package oauth

import (
	"context"
	"database/sql"
	"errors"

	db "vue-element-plus-admin/backend/internal/database/iam/generated"
)

type GoogleUserRepository struct {
	queries *db.Queries
}

func NewGoogleUserRepository(queries *db.Queries) *GoogleUserRepository {
	return &GoogleUserRepository{queries: queries}
}

func (r *GoogleUserRepository) WithTx(tx *sql.Tx) *GoogleUserRepository {
	return &GoogleUserRepository{queries: r.queries.WithTx(tx)}
}

func (r *GoogleUserRepository) GetExternalUserID(ctx context.Context, arg db.FindExternalUserParams) (int64, error) {
	userRow, err := r.queries.FindExternalUser(ctx, arg)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return userRow.UserID, nil
}

func (r *GoogleUserRepository) CreateExternalUser(
	ctx context.Context,
	arg db.CreateExternalUserParams,
) error {
	_, err := r.queries.CreateExternalUser(ctx, arg)
	return err
}
