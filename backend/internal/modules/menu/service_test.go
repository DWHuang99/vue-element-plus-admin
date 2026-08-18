package menu

import (
	"context"
	"testing"
)

type repositoryStub struct {
	all           []MenuItem
	assigned      []MenuItem
	created       bool
	childrenCount int
	deleted       bool
}

func (s *repositoryStub) All(context.Context) ([]MenuItem, error) { return s.all, nil }
func (s *repositoryStub) AssignedToRoles(context.Context, []string) ([]MenuItem, error) {
	return s.assigned, nil
}
func (s *repositoryStub) Create(context.Context, Input) error {
	s.created = true
	return nil
}

func TestAuthorizedTreeFiltersDisabledMenus(t *testing.T) {
	repository := &repositoryStub{assigned: []MenuItem{
		{ID: 1, Status: 1, Meta: MenuMeta{Title: "enabled"}},
		{ID: 2, Status: 0, Meta: MenuMeta{Title: "disabled"}},
		{ID: 3, ParentID: 2, Status: 1, Meta: MenuMeta{Title: "disabled child"}},
	}}
	tree, err := NewService(repository).AuthorizedTree(context.Background(), []string{"test"}, false)
	if err != nil {
		t.Fatalf("AuthorizedTree() error = %v", err)
	}
	if len(tree) != 1 || tree[0].ID != 1 {
		t.Fatalf("AuthorizedTree() = %#v", tree)
	}
}

func TestAdministratorTreeReceivesAllButtonPermissions(t *testing.T) {
	repository := &repositoryStub{all: []MenuItem{{
		ID:     1,
		Status: 1,
		PermissionList: []PermissionItem{
			{Value: "system:user:create"},
			{Value: "system:user:update"},
		},
	}}}
	tree, err := NewService(repository).AuthorizedTree(context.Background(), []string{"admin"}, true)
	if err != nil {
		t.Fatalf("AuthorizedTree() error = %v", err)
	}
	if len(tree) != 1 || len(tree[0].Meta.Permission) != 2 {
		t.Fatalf("administrator permissions = %#v", tree)
	}
}
func (s *repositoryStub) Update(context.Context, int64, Input) (bool, error) { return true, nil }
func (s *repositoryStub) CountChildren(context.Context, int64) (int, error) {
	return s.childrenCount, nil
}
func (s *repositoryStub) Delete(context.Context, int64) (bool, error) {
	s.deleted = true
	return true, nil
}

func TestServiceTreeAndValidation(t *testing.T) {
	repository := &repositoryStub{all: []MenuItem{
		{ID: 2, ParentID: 1, Meta: MenuMeta{Title: "child"}},
		{ID: 1, Meta: MenuMeta{Title: "root"}},
	}}
	service := NewService(repository)
	tree, err := service.Tree(context.Background())
	if err != nil {
		t.Fatalf("Tree() error = %v", err)
	}
	if len(tree) != 1 || tree[0].Children[0].ParentName != "root" {
		t.Fatalf("Tree() = %#v", tree)
	}
	if err := service.Create(context.Background(), Input{}); err != ErrInvalidInput {
		t.Fatalf("Create() error = %v, want %v", err, ErrInvalidInput)
	}
	if repository.created {
		t.Fatal("invalid create reached repository")
	}
}

func TestServiceRejectsDeletingParent(t *testing.T) {
	repository := &repositoryStub{childrenCount: 1}
	err := NewService(repository).Delete(context.Background(), 1)
	if err != ErrHasChildren {
		t.Fatalf("Delete() error = %v, want %v", err, ErrHasChildren)
	}
	if repository.deleted {
		t.Fatal("parent menu was deleted")
	}
}
