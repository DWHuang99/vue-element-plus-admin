package usermanagementdirectory

import (
	"context"
	"testing"
	"time"

	"vue-element-plus-admin/backend/pb"

	"google.golang.org/grpc"
)

type clientStub struct {
	departmentID int64
}

func (s *clientStub) CountUsersByDepartment(
	_ context.Context,
	request *pb.CountUsersByDepartmentRequest,
	_ ...grpc.CallOption,
) (*pb.CountUsersByDepartmentResponse, error) {
	s.departmentID = request.DepartmentId
	return &pb.CountUsersByDepartmentResponse{Count: 4}, nil
}

func TestDirectoryCountsUsersByDepartment(t *testing.T) {
	client := &clientStub{}
	count, err := New(client, time.Second).CountUsersByDepartment(t.Context(), 9)
	if err != nil {
		t.Fatalf("CountUsersByDepartment() error = %v", err)
	}
	if client.departmentID != 9 || count != 4 {
		t.Fatalf("department ID = %d, count = %d", client.departmentID, count)
	}
}
