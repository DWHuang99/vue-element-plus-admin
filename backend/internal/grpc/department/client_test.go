package departmentgrpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"vue-element-plus-admin/backend/internal/modules/usermanagement"
	"vue-element-plus-admin/backend/pb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type clientStub struct {
	requestedIDs []int64
	batchCalls   int
}

func TestDirectoryMapsDepartmentNotFound(t *testing.T) {
	_, err := NewDirectory(&clientStub{}, time.Second).Get(t.Context(), 2)
	if !errors.Is(err, usermanagement.ErrDepartmentNotFound) {
		t.Fatalf("Get() error = %v, want %v", err, usermanagement.ErrDepartmentNotFound)
	}
}

func (s *clientStub) BatchGetDepartments(
	_ context.Context,
	request *pb.BatchGetDepartmentsRequest,
	_ ...grpc.CallOption,
) (*pb.BatchGetDepartmentsResponse, error) {
	s.batchCalls++
	s.requestedIDs = append([]int64(nil), request.Ids...)
	return &pb.BatchGetDepartmentsResponse{Departments: []*pb.GetDepartmentResponse{{
		Id:             1,
		DepartmentName: "研发部",
		Status:         true,
	}}}, nil
}

func (s *clientStub) GetDepartment(
	_ context.Context,
	request *pb.GetDepartmentRequest,
	_ ...grpc.CallOption,
) (*pb.GetDepartmentResponse, error) {
	s.requestedIDs = append(s.requestedIDs, request.Id)
	if request.Id == 2 {
		return nil, status.Error(codes.NotFound, "department not found")
	}
	return &pb.GetDepartmentResponse{
		Id:             request.Id,
		DepartmentName: "研发部",
		Status:         true,
		Deleting:       request.Id == 3,
	}, nil
}

func TestDirectoryUsesSingleDepartmentRPC(t *testing.T) {
	client := &clientStub{}
	directory := NewDirectory(client, time.Second)
	items, err := directory.BatchGet(t.Context(), []int64{1, 2})
	if err != nil {
		t.Fatalf("BatchGet() error = %v", err)
	}
	if client.batchCalls != 1 || len(client.requestedIDs) != 2 || client.requestedIDs[0] != 1 || client.requestedIDs[1] != 2 {
		t.Fatalf("requested IDs = %v", client.requestedIDs)
	}
	if len(items) != 1 || items[0].ID != 1 || items[0].DepartmentName != "研发部" {
		t.Fatalf("items = %#v", items)
	}
}

func TestDirectoryMapsDeletingState(t *testing.T) {
	department, err := NewDirectory(&clientStub{}, time.Second).Get(t.Context(), 3)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !department.Deleting {
		t.Fatal("deleting state was not mapped")
	}
}
