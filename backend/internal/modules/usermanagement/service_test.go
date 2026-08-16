package usermanagement

import (
	"context"
	"testing"

	"vue-element-plus-admin/backend/internal/security"
)

type repositoryStub struct {
	createdInput Input
	passwordHash string
	updated      bool
	listed       []UserItem
}

func (s *repositoryStub) List(context.Context, Filter) ([]UserItem, int, error) {
	return s.listed, len(s.listed), nil
}
func (s *repositoryStub) Create(_ context.Context, input Input, passwordHash string) error {
	s.createdInput = input
	s.passwordHash = passwordHash
	return nil
}
func (s *repositoryStub) Update(context.Context, int64, Input, string) (bool, error) {
	return s.updated, nil
}
func (s *repositoryStub) Delete(context.Context, []int64) error { return nil }
func (s *repositoryStub) CountByDepartment(context.Context, int64) (int64, error) {
	return int64(len(s.listed)), nil
}

type departmentDirectoryStub struct {
	requestedIDs []int64
	items        []DepartmentItem
	getItem      DepartmentItem
	getErr       error
}

func (s *departmentDirectoryStub) Get(_ context.Context, _ int64) (DepartmentItem, error) {
	return s.getItem, s.getErr
}

func (s *departmentDirectoryStub) BatchGet(_ context.Context, ids []int64) ([]DepartmentItem, error) {
	s.requestedIDs = ids
	return s.items, nil
}

func TestServiceCreateNormalizesAndHashes(t *testing.T) {
	repository := &repositoryStub{}
	service := NewService(repository, &departmentDirectoryStub{getItem: DepartmentItem{ID: 9, Status: 1}})
	input := Input{Username: "alice", Password: "secret123", RoleID: 2}
	input.Department.ID = 9
	if err := service.Create(context.Background(), input); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if repository.createdInput.Account != "alice" || repository.createdInput.DepartmentID != 9 {
		t.Fatalf("normalized input = %#v", repository.createdInput)
	}
	if repository.passwordHash == input.Password || !security.Verify(input.Password, repository.passwordHash) {
		t.Fatal("password was not securely hashed")
	}
}

func TestServiceRejectsDisabledDepartment(t *testing.T) {
	repository := &repositoryStub{}
	directory := &departmentDirectoryStub{getItem: DepartmentItem{ID: 9, Status: 0}}
	err := NewService(repository, directory).Create(context.Background(), Input{
		Username: "alice", Password: "secret123", RoleID: 2, DepartmentID: 9,
	})
	if err != ErrDepartmentDisabled {
		t.Fatalf("Create() error = %v, want %v", err, ErrDepartmentDisabled)
	}
	if repository.passwordHash != "" {
		t.Fatal("disabled department reached user repository")
	}
}

func TestServiceRejectsDeletingDepartment(t *testing.T) {
	repository := &repositoryStub{}
	directory := &departmentDirectoryStub{getItem: DepartmentItem{ID: 9, Status: 1, Deleting: true}}
	err := NewService(repository, directory).Create(context.Background(), Input{
		Username: "alice", Password: "secret123", RoleID: 2, DepartmentID: 9,
	})
	if err != ErrDepartmentDeleting {
		t.Fatalf("Create() error = %v, want %v", err, ErrDepartmentDeleting)
	}
	if repository.passwordHash != "" {
		t.Fatal("deleting department reached user repository")
	}
}

func TestServiceUpdateNotFound(t *testing.T) {
	repository := &repositoryStub{}
	err := NewService(repository, nil).Update(context.Background(), 3, Input{
		Username: "alice",
		RoleID:   2,
	})
	if err != ErrUserNotFound {
		t.Fatalf("Update() error = %v, want %v", err, ErrUserNotFound)
	}
}

func TestServiceEnrichesDepartmentsWithoutDatabaseJoin(t *testing.T) {
	repository := &repositoryStub{listed: []UserItem{
		{ID: 1, DepartmentID: 9},
		{ID: 2, DepartmentID: 9},
		{ID: 3},
	}}
	directory := &departmentDirectoryStub{items: []DepartmentItem{{ID: 9, DepartmentName: "研发部"}}}
	items, total, err := NewService(repository, directory).List(
		context.Background(), Filter{Page: 1, Size: 10},
	)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if total != 3 || len(directory.requestedIDs) != 1 || directory.requestedIDs[0] != 9 {
		t.Fatalf("total = %d, requested IDs = %v", total, directory.requestedIDs)
	}
	if items[0].Department == nil || items[0].Department.DepartmentName != "研发部" {
		t.Fatalf("first department = %#v", items[0].Department)
	}
	if items[1].Department == nil || items[1].Department.ID != 9 {
		t.Fatalf("second department = %#v", items[1].Department)
	}
	if items[2].Department != nil {
		t.Fatalf("third department = %#v, want nil", items[2].Department)
	}
}
