package department

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	db "vue-element-plus-admin/backend/internal/database/department/generated"
)

type Repository interface {
	List(ctx context.Context, page, size int, name string) ([]DepartmentItem, int, error)
	All(ctx context.Context) ([]DepartmentItem, error)
	Get(ctx context.Context, id int64) (DepartmentItem, error)
	BatchGet(ctx context.Context, ids []int64) ([]DepartmentItem, error)
	Create(ctx context.Context, input Input) error
	Update(ctx context.Context, id int64, input Input) (bool, error)
	CountChildren(ctx context.Context, id int64) (int, error)
	BeginDeletion(ctx context.Context, id int64) (bool, error)
	CancelDeletions(ctx context.Context, ids []int64) error
	Delete(ctx context.Context, ids []int64) (int, error)
}

type SQLCRepository struct {
	queries *db.Queries
}

func NewRepository(queries *db.Queries) *SQLCRepository {
	return &SQLCRepository{queries: queries}
}

func (r *SQLCRepository) List(ctx context.Context, page, size int, name string) ([]DepartmentItem, int, error) {
	rows, err := r.queries.ListDepartments(ctx, db.ListDepartmentsParams{
		Name:       name,
		PageOffset: int32((page - 1) * size),
		PageLimit:  int32(size),
	})
	if err != nil {
		return nil, 0, err
	}
	items := make([]DepartmentItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, departmentFromRow(
			row.ID, row.ParentID, row.DepartmentName, row.Status, row.Deleting, row.CreatedAt, row.Remark,
		))
	}
	total, err := r.queries.CountDepartments(ctx, name)
	if err != nil {
		return nil, 0, err
	}
	return items, int(total), nil
}

func (r *SQLCRepository) All(ctx context.Context) ([]DepartmentItem, error) {
	rows, err := r.queries.ListAllDepartments(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]DepartmentItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, departmentFromRow(
			row.ID, row.ParentID, row.DepartmentName, row.Status, row.Deleting, row.CreatedAt, row.Remark,
		))
	}
	return items, nil
}

func (r *SQLCRepository) Get(ctx context.Context, id int64) (DepartmentItem, error) {
	row, err := r.queries.GetDepartmentByID(ctx, id)
	if err != nil {
		return DepartmentItem{}, err
	}
	return departmentFromRow(
		row.ID, row.ParentID, row.DepartmentName, row.Status, row.Deleting, row.CreatedAt, row.Remark,
	), nil
}

func (r *SQLCRepository) BatchGet(ctx context.Context, ids []int64) ([]DepartmentItem, error) {
	encodedIDs, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	rows, err := r.queries.GetDepartmentsByIDs(ctx, encodedIDs)
	if err != nil {
		return nil, err
	}
	items := make([]DepartmentItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, departmentFromRow(
			row.ID, row.ParentID, row.DepartmentName, row.Status, row.Deleting, row.CreatedAt, row.Remark,
		))
	}
	return items, nil
}

func (r *SQLCRepository) Create(ctx context.Context, input Input) error {
	return r.queries.CreateDepartment(ctx, db.CreateDepartmentParams{
		ParentID:       input.ParentID,
		DepartmentName: input.DepartmentName,
		Status:         input.Status != 0,
		Remark:         input.Remark,
	})
}

func (r *SQLCRepository) Update(ctx context.Context, id int64, input Input) (bool, error) {
	_, err := r.queries.UpdateDepartment(ctx, db.UpdateDepartmentParams{
		ParentID:       input.ParentID,
		DepartmentName: input.DepartmentName,
		Status:         input.Status != 0,
		Remark:         input.Remark,
		ID:             id,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (r *SQLCRepository) CountChildren(ctx context.Context, id int64) (int, error) {
	count, err := r.queries.CountDepartmentChildren(ctx, id)
	return int(count), err
}

func (r *SQLCRepository) BeginDeletion(ctx context.Context, id int64) (bool, error) {
	_, err := r.queries.BeginDepartmentDeletion(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (r *SQLCRepository) CancelDeletions(ctx context.Context, ids []int64) error {
	encodedIDs, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	return r.queries.CancelDepartmentDeletions(ctx, encodedIDs)
}

func (r *SQLCRepository) Delete(ctx context.Context, ids []int64) (int, error) {
	encodedIDs, err := json.Marshal(ids)
	if err != nil {
		return 0, err
	}
	deletedIDs, err := r.queries.DeleteDepartments(ctx, encodedIDs)
	return len(deletedIDs), err
}

func departmentFromRow(
	id int64,
	parentID sql.NullInt64,
	name string,
	status bool,
	deleting bool,
	createdAt time.Time,
	remark string,
) DepartmentItem {
	item := DepartmentItem{
		ID:             id,
		DepartmentName: name,
		CreateTime:     createdAt,
		Remark:         remark,
		Deleting:       deleting,
	}
	if parentID.Valid {
		item.ParentID = parentID.Int64
	}
	if status {
		item.Status = 1
	}
	return item
}
