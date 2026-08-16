package usermanagement

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	db "vue-element-plus-admin/backend/internal/database/iam/generated"
)

type SQLCRepository struct {
	queries *db.Queries
}

func NewRepository(queries *db.Queries) *SQLCRepository {
	return &SQLCRepository{queries: queries}
}

func (r *SQLCRepository) List(ctx context.Context, filter Filter) ([]UserItem, int, error) {
	rows, err := r.queries.ListManagedUsers(ctx, db.ListManagedUsersParams{
		DepartmentID: filter.DepartmentID,
		Username:     filter.Username,
		Account:      filter.Account,
		PageOffset:   int32((filter.Page - 1) * filter.Size),
		PageLimit:    int32(filter.Size),
	})
	if err != nil {
		return nil, 0, err
	}
	items := make([]UserItem, 0, len(rows))
	for _, row := range rows {
		item := UserItem{
			ID:         row.ID,
			Username:   row.Username,
			Account:    row.Account,
			Email:      row.Email,
			CreateTime: row.CreatedAt,
			RoleID:     row.RoleID,
			Role:       row.RoleName,
		}
		if row.DepartmentID.Valid {
			item.DepartmentID = row.DepartmentID.Int64
		}
		items = append(items, item)
	}
	total, err := r.queries.CountManagedUsers(ctx, db.CountManagedUsersParams{
		DepartmentID: filter.DepartmentID,
		Username:     filter.Username,
		Account:      filter.Account,
	})
	if err != nil {
		return nil, 0, err
	}
	return items, int(total), nil
}

func (r *SQLCRepository) Create(ctx context.Context, input Input, passwordHash string) error {
	return r.queries.CreateManagedUser(ctx, db.CreateManagedUserParams{
		Username:     input.Username,
		PasswordHash: passwordHash,
		RoleID:       input.RoleID,
		Account:      input.Account,
		Email:        input.Email,
		DepartmentID: input.DepartmentID,
	})
}

func (r *SQLCRepository) Update(ctx context.Context, id int64, input Input, passwordHash string) (bool, error) {
	var err error
	if passwordHash != "" {
		_, err = r.queries.UpdateManagedUserWithPassword(ctx, db.UpdateManagedUserWithPasswordParams{
			Username:     input.Username,
			Account:      input.Account,
			Email:        input.Email,
			RoleID:       input.RoleID,
			DepartmentID: input.DepartmentID,
			PasswordHash: passwordHash,
			ID:           id,
		})
	} else {
		_, err = r.queries.UpdateManagedUser(ctx, db.UpdateManagedUserParams{
			Username:     input.Username,
			Account:      input.Account,
			Email:        input.Email,
			RoleID:       input.RoleID,
			DepartmentID: input.DepartmentID,
			ID:           id,
		})
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (r *SQLCRepository) Delete(ctx context.Context, ids []int64) error {
	encodedIDs, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	return r.queries.DeleteManagedUsers(ctx, encodedIDs)
}

func (r *SQLCRepository) CountByDepartment(ctx context.Context, departmentID int64) (int64, error) {
	return r.queries.CountUsersByDepartment(ctx, sql.NullInt64{Int64: departmentID, Valid: true})
}
