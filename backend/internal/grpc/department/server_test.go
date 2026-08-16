package departmentgrpc

import (
	"context"
	"testing"

	"vue-element-plus-admin/backend/internal/modules/department"
	"vue-element-plus-admin/backend/pb"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type queryStub struct {
	item department.DepartmentItem
	err  error
}

func (s queryStub) Get(context.Context, int64) (department.DepartmentItem, error) {
	return s.item, s.err
}

func (s queryStub) BatchGet(context.Context, []int64) ([]department.DepartmentItem, error) {
	if s.err != nil {
		return nil, s.err
	}
	return []department.DepartmentItem{s.item}, nil
}

func TestServerGetDepartment(t *testing.T) {
	server := NewServer(queryStub{item: department.DepartmentItem{
		ID:             7,
		DepartmentName: "研发部",
		Status:         1,
		Remark:         "核心研发",
		Deleting:       true,
	}})
	response, err := server.GetDepartment(t.Context(), &pb.GetDepartmentRequest{Id: 7})
	if err != nil {
		t.Fatalf("GetDepartment() error = %v", err)
	}
	if response.Id != 7 || response.DepartmentName != "研发部" || !response.Status || !response.Deleting {
		t.Fatalf("response = %#v", response)
	}
}

func TestServerMapsNotFound(t *testing.T) {
	server := NewServer(queryStub{err: department.ErrNotFound})
	_, err := server.GetDepartment(t.Context(), &pb.GetDepartmentRequest{Id: 99})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("status = %s, want NotFound", status.Code(err))
	}
}

func TestServerBatchGetDepartments(t *testing.T) {
	server := NewServer(queryStub{item: department.DepartmentItem{ID: 7, DepartmentName: "研发部"}})
	response, err := server.BatchGetDepartments(t.Context(), &pb.BatchGetDepartmentsRequest{Ids: []int64{7}})
	if err != nil {
		t.Fatalf("BatchGetDepartments() error = %v", err)
	}
	if len(response.Departments) != 1 || response.Departments[0].Id != 7 {
		t.Fatalf("response = %#v", response)
	}
}
