package role

import (
	"context"
	"testing"

	"vue-element-plus-admin/backend/internal/modules/permission"
)

type repositoryStub struct {
	role              RoleItem
	createdInput      Input
	assignments       []MenuAssignment
	globalPermissions []string
	userCount         int
	deleted           bool
}

func (s *repositoryStub) WithinTx(ctx context.Context, action func(Repository) error) error {
	return action(s)
}
func (s *repositoryStub) List(context.Context, int, int, string) ([]RoleItem, int, error) {
	return []RoleItem{s.role}, 1, nil
}
func (s *repositoryStub) Get(context.Context, int64) (RoleItem, error) { return s.role, nil }
func (s *repositoryStub) ListMenus(context.Context, int64) ([]MenuItem, error) {
	return nil, nil
}
func (s *repositoryStub) Create(_ context.Context, input Input) (int64, error) {
	s.createdInput = input
	return 8, nil
}
func (s *repositoryStub) Update(context.Context, int64, Input) error { return nil }
func (s *repositoryStub) ReplaceAccess(_ context.Context, _ int64, assignments []MenuAssignment, globalPermissions []string) error {
	s.assignments = assignments
	s.globalPermissions = globalPermissions
	return nil
}
func (s *repositoryStub) CountUsers(context.Context, int64) (int, error) {
	return s.userCount, nil
}
func (s *repositoryStub) Delete(context.Context, int64) error {
	s.deleted = true
	return nil
}

func TestServiceCreateNormalizesRoleAccess(t *testing.T) {
	repository := &repositoryStub{}
	service := NewService(repository)
	err := service.Create(context.Background(), Input{
		Code:     "admin",
		RoleName: "Administrator",
		Menu: []MenuItem{{
			ID:   10,
			Meta: permission.MenuMeta{Permission: []string{" user:read ", "user:read", "user:update"}},
		}},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if repository.createdInput.Code != "admin" {
		t.Fatalf("created code = %q", repository.createdInput.Code)
	}
	if len(repository.assignments) != 1 || len(repository.assignments[0].PermissionCodes) != 2 {
		t.Fatalf("assignments = %#v", repository.assignments)
	}
	if len(repository.globalPermissions) != 1 || repository.globalPermissions[0] != "*.*.*" {
		t.Fatalf("global permissions = %#v", repository.globalPermissions)
	}
}

func TestServiceRejectsDeletingAssignedRole(t *testing.T) {
	repository := &repositoryStub{userCount: 1}
	err := NewService(repository).Delete(context.Background(), 8)
	if err != ErrAssignedUsers {
		t.Fatalf("Delete() error = %v, want %v", err, ErrAssignedUsers)
	}
	if repository.deleted {
		t.Fatal("assigned role was deleted")
	}
}
