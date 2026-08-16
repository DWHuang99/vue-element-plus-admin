package usermanagementgrpc

import (
	"context"
	"testing"

	"vue-element-plus-admin/backend/internal/modules/usermanagement"
	"vue-element-plus-admin/backend/pb"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type userCounterStub struct {
	count int64
	err   error
}

func (s userCounterStub) CountUsersByDepartment(context.Context, int64) (int64, error) {
	return s.count, s.err
}

func TestServerCountsUsersByDepartment(t *testing.T) {
	response, err := NewServer(userCounterStub{count: 3}).CountUsersByDepartment(
		t.Context(), &pb.CountUsersByDepartmentRequest{DepartmentId: 9},
	)
	if err != nil {
		t.Fatalf("CountUsersByDepartment() error = %v", err)
	}
	if response.Count != 3 {
		t.Fatalf("count = %d, want 3", response.Count)
	}
}

func TestServerRejectsInvalidDepartmentID(t *testing.T) {
	_, err := NewServer(userCounterStub{err: usermanagement.ErrInvalidInput}).CountUsersByDepartment(
		t.Context(), &pb.CountUsersByDepartmentRequest{},
	)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("status = %s, want InvalidArgument", status.Code(err))
	}
}
