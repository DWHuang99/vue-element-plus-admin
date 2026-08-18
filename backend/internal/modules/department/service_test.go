package department

import (
	"context"
	"testing"
	"time"

	usermanagementdirectory "vue-element-plus-admin/backend/internal/directory/usermanagement"
	"vue-element-plus-admin/backend/pb"

	"google.golang.org/grpc"
)

type repositoryStub struct {
	all           []DepartmentItem
	created       bool
	updated       bool
	childrenCount int
	marked        bool
	canceled      bool
	deleted       bool
}

type userDirectoryStub struct {
	count int64
	err   error
}

func (s userDirectoryStub) CountUsersByDepartment(
	context.Context,
	*pb.CountUsersByDepartmentRequest,
	...grpc.CallOption,
) (*pb.CountUsersByDepartmentResponse, error) {
	return &pb.CountUsersByDepartmentResponse{Count: s.count}, s.err
}

func (s *repositoryStub) List(context.Context, int, int, string) ([]DepartmentItem, int, error) {
	return s.all, len(s.all), nil
}
func (s *repositoryStub) All(context.Context) ([]DepartmentItem, error) { return s.all, nil }
func (s *repositoryStub) Get(_ context.Context, id int64) (DepartmentItem, error) {
	for _, item := range s.all {
		if item.ID == id {
			return item, nil
		}
	}
	return DepartmentItem{}, ErrNotFound
}
func (s *repositoryStub) BatchGet(_ context.Context, ids []int64) ([]DepartmentItem, error) {
	byID := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		byID[id] = struct{}{}
	}
	items := make([]DepartmentItem, 0, len(ids))
	for _, item := range s.all {
		if _, exists := byID[item.ID]; exists {
			items = append(items, item)
		}
	}
	return items, nil
}
func (s *repositoryStub) Create(context.Context, Input) error {
	s.created = true
	return nil
}
func (s *repositoryStub) Update(context.Context, int64, Input) (bool, error) {
	s.updated = true
	return true, nil
}
func (s *repositoryStub) CountChildren(context.Context, int64) (int, error) {
	return s.childrenCount, nil
}
func (s *repositoryStub) BeginDeletion(context.Context, int64) (bool, error) {
	s.marked = true
	return true, nil
}
func (s *repositoryStub) CancelDeletions(context.Context, []int64) error {
	s.canceled = true
	return nil
}
func (s *repositoryStub) Delete(_ context.Context, ids []int64) (int, error) {
	s.deleted = true
	return len(ids), nil
}

func TestServiceTreeAndValidation(t *testing.T) {
	repository := &repositoryStub{all: []DepartmentItem{
		{ID: 2, ParentID: 1, DepartmentName: "child"},
		{ID: 1, DepartmentName: "root"},
	}}
	service := NewService(repository)
	tree, err := service.Tree(context.Background())
	if err != nil {
		t.Fatalf("Tree() error = %v", err)
	}
	if len(tree) != 1 || len(tree[0].Children) != 1 || tree[0].Children[0].ID != 2 {
		t.Fatalf("Tree() = %#v", tree)
	}

	if err := service.Create(context.Background(), Input{}); err != ErrInvalidInput {
		t.Fatalf("Create() error = %v, want %v", err, ErrInvalidInput)
	}
	if repository.created {
		t.Fatal("invalid create reached repository")
	}
	if err := service.Update(context.Background(), 3, Input{ParentID: 3, DepartmentName: "self"}); err != ErrInvalidInput {
		t.Fatalf("Update() error = %v, want %v", err, ErrInvalidInput)
	}
}

func TestServiceRejectsDeletingParent(t *testing.T) {
	repository := &repositoryStub{childrenCount: 1}
	err := NewService(repository).Delete(context.Background(), []int64{1})
	if err != ErrHasChildren {
		t.Fatalf("Delete() error = %v, want %v", err, ErrHasChildren)
	}
	if repository.deleted {
		t.Fatal("parent department was deleted")
	}
}

func TestServiceGetsDepartment(t *testing.T) {
	repository := &repositoryStub{all: []DepartmentItem{{ID: 1, DepartmentName: "root"}}}
	result, err := NewService(repository).Get(context.Background(), 1)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if result.ID != 1 || result.DepartmentName != "root" {
		t.Fatalf("Department = %#v, want department 1", result)
	}
}

func TestServiceRejectsDeletingDepartmentWithUsers(t *testing.T) {
	repository := &repositoryStub{}
	directory := usermanagementdirectory.New(userDirectoryStub{count: 2}, time.Second)
	err := NewService(repository, directory).Delete(context.Background(), []int64{1})
	if err != ErrHasUsers {
		t.Fatalf("Delete() error = %v, want %v", err, ErrHasUsers)
	}
	if repository.deleted {
		t.Fatal("department with users was deleted")
	}
	if !repository.marked || !repository.canceled {
		t.Fatal("deleting state was not set and restored")
	}
}
