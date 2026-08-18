package usermanagementdirectory

import (
	"context"
	"time"

	"vue-element-plus-admin/backend/pb"
)

type Directory struct {
	client  pb.UserManagementServiceClient
	timeout time.Duration
}

func New(client pb.UserManagementServiceClient, timeout time.Duration) *Directory {
	return &Directory{client: client, timeout: timeout}
}

func (d *Directory) CountUsersByDepartment(ctx context.Context, departmentID int64) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()

	response, err := d.client.CountUsersByDepartment(ctx, &pb.CountUsersByDepartmentRequest{
		DepartmentId: departmentID,
	})
	if err != nil {
		return 0, err
	}
	return response.Count, nil
}
