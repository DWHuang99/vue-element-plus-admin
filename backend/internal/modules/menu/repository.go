package menu

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	db "vue-element-plus-admin/backend/internal/database/iam/generated"
)

type Repository interface {
	All(ctx context.Context) ([]MenuItem, error)
	Create(ctx context.Context, input Input) error
	Update(ctx context.Context, id int64, input Input) (bool, error)
	CountChildren(ctx context.Context, id int64) (int, error)
	Delete(ctx context.Context, id int64) (bool, error)
}

type SQLCRepository struct {
	queries *db.Queries
}

func NewRepository(queries *db.Queries) *SQLCRepository {
	return &SQLCRepository{queries: queries}
}

func (r *SQLCRepository) All(ctx context.Context) ([]MenuItem, error) {
	rows, err := r.queries.ListAllMenus(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]MenuItem, 0, len(rows))
	for _, row := range rows {
		var item MenuItem
		item.ID = row.ID
		if row.ParentID.Valid {
			item.ParentID = row.ParentID.Int64
		}
		item.Type = int(row.Type)
		item.Path = row.Path
		item.Name = row.Name
		item.Component = row.Component
		if row.Status {
			item.Status = 1
		}
		if err := json.Unmarshal(row.Meta, &item.Meta); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(row.PermissionList, &item.PermissionList); err != nil {
			return nil, err
		}
		if item.PermissionList == nil {
			item.PermissionList = []PermissionItem{}
		}
		items = append(items, item)
	}
	return items, nil
}

func (r *SQLCRepository) Create(ctx context.Context, input Input) error {
	meta, permissions, err := encodeMenuInput(input)
	if err != nil {
		return err
	}
	return r.queries.CreateMenu(ctx, db.CreateMenuParams{
		ParentID:       input.ParentID,
		Type:           int16(input.Type),
		Path:           input.Path,
		Name:           input.Name,
		Component:      input.Component,
		Status:         input.Status != 0,
		Meta:           meta,
		PermissionList: permissions,
	})
}

func (r *SQLCRepository) Update(ctx context.Context, id int64, input Input) (bool, error) {
	meta, permissions, err := encodeMenuInput(input)
	if err != nil {
		return false, err
	}
	_, err = r.queries.UpdateMenu(ctx, db.UpdateMenuParams{
		ParentID:       input.ParentID,
		Type:           int16(input.Type),
		Path:           input.Path,
		Name:           input.Name,
		Component:      input.Component,
		Status:         input.Status != 0,
		Meta:           meta,
		PermissionList: permissions,
		ID:             id,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (r *SQLCRepository) CountChildren(ctx context.Context, id int64) (int, error) {
	count, err := r.queries.CountMenuChildren(ctx, id)
	return int(count), err
}

func (r *SQLCRepository) Delete(ctx context.Context, id int64) (bool, error) {
	_, err := r.queries.DeleteMenu(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func encodeMenuInput(input Input) (json.RawMessage, json.RawMessage, error) {
	meta, err := json.Marshal(input.Meta)
	if err != nil {
		return nil, nil, err
	}
	permissions, err := json.Marshal(input.PermissionList)
	if err != nil {
		return nil, nil, err
	}
	return meta, permissions, nil
}
