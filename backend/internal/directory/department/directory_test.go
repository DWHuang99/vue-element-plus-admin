package departmentdirectory

import (
	"context"
	"errors"
	"testing"
	"time"

	"vue-element-plus-admin/backend/pb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type clientStub struct {
	getResult *pb.GetDepartmentResponse
	getErr    error
	batchIDs  []int64
}

func (s *clientStub) GetDepartment(context.Context, *pb.GetDepartmentRequest, ...grpc.CallOption) (*pb.GetDepartmentResponse, error) {
	return s.getResult, s.getErr
}

func (s *clientStub) BatchGetDepartments(
	_ context.Context,
	request *pb.BatchGetDepartmentsRequest,
	_ ...grpc.CallOption,
) (*pb.BatchGetDepartmentsResponse, error) {
	s.batchIDs = request.Ids
	return &pb.BatchGetDepartmentsResponse{Departments: []*pb.GetDepartmentResponse{
		{Id: 1, DepartmentName: "研发部", Status: true},
	}}, nil
}

func TestDirectoryMapsDepartmentNotFound(t *testing.T) {
	_, err := New(&clientStub{getErr: status.Error(codes.NotFound, "missing")}, time.Second).Get(t.Context(), 2)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() error = %v, want %v", err, ErrNotFound)
	}
}

func TestDirectoryUsesBatchRPC(t *testing.T) {
	client := &clientStub{}
	items, err := New(client, time.Second).BatchGet(t.Context(), []int64{1})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.batchIDs) != 1 || len(items) != 1 || items[0].DepartmentName != "研发部" {
		t.Fatalf("ids=%v items=%v", client.batchIDs, items)
	}
}
