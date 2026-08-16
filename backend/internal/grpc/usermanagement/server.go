package usermanagementgrpc

import (
	"context"
	"errors"
	"net"

	"vue-element-plus-admin/backend/internal/modules/usermanagement"
	"vue-element-plus-admin/backend/pb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type UserCounter interface {
	CountUsersByDepartment(ctx context.Context, departmentID int64) (int64, error)
}

type Server struct {
	pb.UnimplementedUserManagementServiceServer
	service UserCounter
}

func NewServer(service UserCounter) *Server {
	return &Server{service: service}
}

func (s *Server) CountUsersByDepartment(
	ctx context.Context,
	request *pb.CountUsersByDepartmentRequest,
) (*pb.CountUsersByDepartmentResponse, error) {
	count, err := s.service.CountUsersByDepartment(ctx, request.DepartmentId)
	if err != nil {
		if errors.Is(err, usermanagement.ErrInvalidInput) {
			return nil, status.Error(codes.InvalidArgument, "invalid department id")
		}
		return nil, status.Error(codes.Internal, "failed to count department users")
	}
	return &pb.CountUsersByDepartmentResponse{Count: count}, nil
}

func Serve(address string, service UserCounter) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	server := grpc.NewServer()
	pb.RegisterUserManagementServiceServer(server, NewServer(service))
	return server.Serve(listener)
}
