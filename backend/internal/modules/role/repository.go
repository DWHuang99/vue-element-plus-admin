package role

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	db "vue-element-plus-admin/backend/internal/database/iam/generated"
)

type MenuAssignment struct {
	MenuID          int64
	PermissionCodes []string
}

type Repository interface {
	WithinTx(ctx context.Context, action func(Repository) error) error
	List(ctx context.Context, page, size int, name string) ([]RoleItem, int, error)
	Get(ctx context.Context, id int64) (RoleItem, error)
	ListMenus(ctx context.Context, roleID int64) ([]MenuItem, error)
	Create(ctx context.Context, input Input) (int64, error)
	Update(ctx context.Context, id int64, input Input) error
	ReplaceAccess(ctx context.Context, roleID int64, assignments []MenuAssignment, globalPermissions []string) error
	CountUsers(ctx context.Context, roleID int64) (int, error)
	Delete(ctx context.Context, id int64) error
}

type SQLCRepository struct {
	database *sql.DB
	queries  *db.Queries
}

func NewRepository(database *sql.DB, queries *db.Queries) *SQLCRepository {
	return &SQLCRepository{database: database, queries: queries}
}

func (r *SQLCRepository) WithinTx(ctx context.Context, action func(Repository) error) error {
	tx, err := r.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	transactional := &SQLCRepository{database: r.database, queries: r.queries.WithTx(tx)}
	if err := action(transactional); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *SQLCRepository) List(ctx context.Context, page, size int, name string) ([]RoleItem, int, error) {
	rows, err := r.queries.ListRoles(ctx, db.ListRolesParams{
		Name:       name,
		PageOffset: int32((page - 1) * size),
		PageLimit:  int32(size),
	})
	if err != nil {
		return nil, 0, err
	}
	items := make([]RoleItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, roleFromRow(row.ID, row.Code, row.Name, row.Status, row.CreatedAt, row.Remark))
	}
	total, err := r.queries.CountRoles(ctx, name)
	if err != nil {
		return nil, 0, err
	}
	return items, int(total), nil
}

func (r *SQLCRepository) Get(ctx context.Context, id int64) (RoleItem, error) {
	row, err := r.queries.GetRole(ctx, id)
	if err != nil {
		return RoleItem{}, err
	}
	return roleFromRow(row.ID, row.Code, row.Name, row.Status, row.CreatedAt, row.Remark), nil
}

func (r *SQLCRepository) ListMenus(ctx context.Context, roleID int64) ([]MenuItem, error) {
	rows, err := r.queries.ListRoleMenus(ctx, roleID)
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
		if err := json.Unmarshal([]byte(row.SelectedPermissionsJson), &item.Meta.Permission); err != nil {
			return nil, err
		}
		if item.PermissionList == nil {
			item.PermissionList = []PermissionItem{}
		}
		items = append(items, item)
	}
	return items, nil
}

func (r *SQLCRepository) Create(ctx context.Context, input Input) (int64, error) {
	return r.queries.CreateRole(ctx, db.CreateRoleParams{
		Code:   input.Code,
		Name:   input.RoleName,
		Status: input.Status != 0,
		Remark: input.Remark,
	})
}

func (r *SQLCRepository) Update(ctx context.Context, id int64, input Input) error {
	_, err := r.queries.UpdateRole(ctx, db.UpdateRoleParams{
		Name:   input.RoleName,
		Status: input.Status != 0,
		Remark: input.Remark,
		ID:     id,
	})
	return err
}

func (r *SQLCRepository) ReplaceAccess(
	ctx context.Context,
	roleID int64,
	assignments []MenuAssignment,
	globalPermissions []string,
) error {
	if err := r.queries.DeleteRolePermissions(ctx, roleID); err != nil {
		return err
	}
	if err := r.queries.DeleteRoleMenus(ctx, roleID); err != nil {
		return err
	}
	for _, assignment := range assignments {
		if err := r.queries.CreateRoleMenu(ctx, db.CreateRoleMenuParams{
			RoleID: roleID,
			MenuID: assignment.MenuID,
		}); err != nil {
			return err
		}
		for _, code := range assignment.PermissionCodes {
			if err := r.queries.CreateRoleMenuPermission(ctx, db.CreateRoleMenuPermissionParams{
				RoleID:         roleID,
				MenuID:         assignment.MenuID,
				PermissionCode: code,
			}); err != nil {
				return err
			}
		}
	}
	for _, code := range globalPermissions {
		if err := r.queries.CreateGlobalRolePermission(ctx, db.CreateGlobalRolePermissionParams{
			RoleID:         roleID,
			PermissionCode: code,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (r *SQLCRepository) CountUsers(ctx context.Context, roleID int64) (int, error) {
	count, err := r.queries.CountRoleUsers(ctx, roleID)
	return int(count), err
}

func (r *SQLCRepository) Delete(ctx context.Context, id int64) error {
	_, err := r.queries.DeleteRole(ctx, id)
	return err
}

func roleFromRow(id int64, code, name string, status bool, createdAt time.Time, remark string) RoleItem {
	item := RoleItem{
		ID:         id,
		Code:       code,
		RoleName:   name,
		CreateTime: createdAt,
		Remark:     remark,
		Menu:       []MenuItem{},
	}
	if status {
		item.Status = 1
	}
	return item
}

func isNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}
