package departmentgrpc

import (
	"context"
	"errors"
	"net"

	"vue-element-plus-admin/backend/internal/modules/department"
	"vue-element-plus-admin/backend/pb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Query interface {
	Get(ctx context.Context, id int64) (department.DepartmentItem, error)
	BatchGet(ctx context.Context, ids []int64) ([]department.DepartmentItem, error)
}

func (s *Server) BatchGetDepartments(
	ctx context.Context,
	request *pb.BatchGetDepartmentsRequest,
) (*pb.BatchGetDepartmentsResponse, error) {
	items, err := s.service.BatchGet(ctx, request.Ids)
	if err != nil {
		if errors.Is(err, department.ErrInvalidInput) {
			return nil, status.Error(codes.InvalidArgument, "invalid department ids")
		}
		return nil, status.Error(codes.Internal, "failed to load departments")
	}
	departments := make([]*pb.GetDepartmentResponse, 0, len(items))
	for _, item := range items {
		departments = append(departments, departmentResponse(item))
	}
	return &pb.BatchGetDepartmentsResponse{Departments: departments}, nil
}

func departmentResponse(item department.DepartmentItem) *pb.GetDepartmentResponse {
	return &pb.GetDepartmentResponse{
		Id:             item.ID,
		DepartmentName: item.DepartmentName,
		Status:         item.Status != 0,
		Remark:         item.Remark,
		Deleting:       item.Deleting,
	}
}

type Server struct {
	pb.UnimplementedDepartmentServiceServer
	service Query
}

func NewServer(service Query) *Server {
	return &Server{service: service}
}

func (s *Server) GetDepartment(
	ctx context.Context,
	request *pb.GetDepartmentRequest,
) (*pb.GetDepartmentResponse, error) {
	item, err := s.service.Get(ctx, request.Id)
	if err != nil {
		switch {
		case errors.Is(err, department.ErrInvalidInput):
			return nil, status.Error(codes.InvalidArgument, "invalid department id")
		case errors.Is(err, department.ErrNotFound):
			return nil, status.Error(codes.NotFound, "department not found")
		default:
			return nil, status.Error(codes.Internal, "failed to load department")
		}
	}
	return departmentResponse(item), nil
}

func Serve(address string, service Query) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	server := grpc.NewServer()
	pb.RegisterDepartmentServiceServer(server, NewServer(service))
	return server.Serve(listener)
}
