package menu

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"

	db "vue-element-plus-admin/backend/internal/database/iam/generated"
)

type Repository interface {
	All(ctx context.Context) ([]MenuItem, error)
	AssignedToRoles(ctx context.Context, roleCodes []string) ([]MenuItem, error)
	Create(ctx context.Context, input Input) error
	Update(ctx context.Context, id int64, input Input) (bool, error)
	CountChildren(ctx context.Context, id int64) (int, error)
	Delete(ctx context.Context, id int64) (bool, error)
}

func (r *SQLCRepository) AssignedToRoles(ctx context.Context, roleCodes []string) ([]MenuItem, error) {
	byID := make(map[int64]MenuItem)
	permissionsByMenu := make(map[int64]map[string]struct{})
	for _, roleCode := range roleCodes {
		var roleID int64
		err := r.database.QueryRowContext(
			ctx, "SELECT id FROM roles WHERE code = $1 AND status = TRUE", roleCode,
		).Scan(&roleID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		rows, err := r.queries.ListRoleMenus(ctx, roleID)
		if err != nil {
			return nil, err
		}
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
			var selectedPermissions []string
			if err := json.Unmarshal([]byte(row.SelectedPermissionsJson), &selectedPermissions); err != nil {
				return nil, err
			}
			if item.PermissionList == nil {
				item.PermissionList = []PermissionItem{}
			}
			if _, exists := byID[item.ID]; !exists {
				byID[item.ID] = item
			}
			if permissionsByMenu[item.ID] == nil {
				permissionsByMenu[item.ID] = make(map[string]struct{})
			}
			for _, permissionCode := range selectedPermissions {
				permissionsByMenu[item.ID][permissionCode] = struct{}{}
			}
		}
	}
	items := make([]MenuItem, 0, len(byID))
	for menuID, item := range byID {
		for permissionCode := range permissionsByMenu[menuID] {
			item.Meta.Permission = append(item.Meta.Permission, permissionCode)
		}
		sort.Strings(item.Meta.Permission)
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, nil
}

type SQLCRepository struct {
	database *sql.DB
	queries  *db.Queries
}

func NewRepository(database *sql.DB, queries *db.Queries) *SQLCRepository {
	return &SQLCRepository{database: database, queries: queries}
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
