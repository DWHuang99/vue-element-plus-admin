package departmentdirectory

import (
	"context"
	"errors"
	"time"

	"vue-element-plus-admin/backend/internal/modules/permission"
	"vue-element-plus-admin/backend/pb"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var ErrNotFound = errors.New("department not found")

type Directory struct {
	client  pb.DepartmentServiceClient
	timeout time.Duration
}

func New(client pb.DepartmentServiceClient, timeout time.Duration) *Directory {
	return &Directory{client: client, timeout: timeout}
}

func (d *Directory) Get(ctx context.Context, id int64) (permission.DepartmentItem, error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()

	result, err := d.client.GetDepartment(ctx, &pb.GetDepartmentRequest{Id: id})
	if status.Code(err) == codes.NotFound {
		return permission.DepartmentItem{}, ErrNotFound
	}
	if err != nil {
		return permission.DepartmentItem{}, err
	}
	return departmentItem(result), nil
}

func (d *Directory) BatchGet(ctx context.Context, ids []int64) ([]permission.DepartmentItem, error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()

	result, err := d.client.BatchGetDepartments(ctx, &pb.BatchGetDepartmentsRequest{Ids: ids})
	if err != nil {
		return nil, err
	}
	items := make([]permission.DepartmentItem, 0, len(result.Departments))
	for _, item := range result.Departments {
		items = append(items, departmentItem(item))
	}
	return items, nil
}

func departmentItem(result *pb.GetDepartmentResponse) permission.DepartmentItem {
	item := permission.DepartmentItem{
		ID:             result.Id,
		DepartmentName: result.DepartmentName,
		Remark:         result.Remark,
		Deleting:       result.Deleting,
	}
	if result.Status {
		item.Status = 1
	}
	return item
}
