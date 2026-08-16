package departmentgrpc

import (
	"context"
	"time"

	"vue-element-plus-admin/backend/internal/modules/usermanagement"
	"vue-element-plus-admin/backend/pb"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Directory struct {
	client  pb.DepartmentServiceClient
	timeout time.Duration
}

func NewDirectory(client pb.DepartmentServiceClient, timeout time.Duration) *Directory {
	return &Directory{client: client, timeout: timeout}
}

func (d *Directory) Get(ctx context.Context, id int64) (usermanagement.DepartmentItem, error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()

	result, err := d.client.GetDepartment(ctx, &pb.GetDepartmentRequest{Id: id})
	if status.Code(err) == codes.NotFound {
		return usermanagement.DepartmentItem{}, usermanagement.ErrDepartmentNotFound
	}
	if err != nil {
		return usermanagement.DepartmentItem{}, err
	}
	return departmentItem(result), nil
}

func (d *Directory) BatchGet(ctx context.Context, ids []int64) ([]usermanagement.DepartmentItem, error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()

	result, err := d.client.BatchGetDepartments(ctx, &pb.BatchGetDepartmentsRequest{Ids: ids})
	if err != nil {
		return nil, err
	}
	items := make([]usermanagement.DepartmentItem, 0, len(result.Departments))
	for _, item := range result.Departments {
		items = append(items, departmentItem(item))
	}
	return items, nil
}

func departmentItem(result *pb.GetDepartmentResponse) usermanagement.DepartmentItem {
	item := usermanagement.DepartmentItem{
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
