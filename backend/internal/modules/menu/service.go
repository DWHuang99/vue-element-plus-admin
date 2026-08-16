package menu

import (
	"context"
	"errors"
	"sort"
	"strings"

	"vue-element-plus-admin/backend/internal/modules/permission"
)

var (
	ErrInvalidInput = errors.New("invalid menu input")
	ErrNotFound     = errors.New("menu not found")
	ErrHasChildren  = errors.New("menu has children")
)

type MenuItem = permission.MenuItem
type PermissionItem = permission.PermissionItem
type MenuMeta = permission.MenuMeta

type Input struct {
	ParentID       int64            `json:"parentId"`
	Type           int              `json:"type"`
	Path           string           `json:"path"`
	Name           string           `json:"name"`
	Component      string           `json:"component"`
	Status         int              `json:"status"`
	Meta           MenuMeta         `json:"meta"`
	PermissionList []PermissionItem `json:"permissionList"`
}

type Service struct {
	repository Repository
}

func NewService(repository Repository) *Service {
	return &Service{repository: repository}
}

func (s *Service) Tree(ctx context.Context) ([]MenuItem, error) {
	items, err := s.repository.All(ctx)
	if err != nil {
		return nil, err
	}
	return buildMenuTree(items), nil
}

func (s *Service) Create(ctx context.Context, input Input) error {
	if !validInput(input) {
		return ErrInvalidInput
	}
	return s.repository.Create(ctx, input)
}

func (s *Service) Update(ctx context.Context, id int64, input Input) error {
	if id <= 0 || input.ParentID == id || !validInput(input) {
		return ErrInvalidInput
	}
	updated, err := s.repository.Update(ctx, id, input)
	if err != nil {
		return err
	}
	if !updated {
		return ErrNotFound
	}
	return nil
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	if id <= 0 {
		return ErrInvalidInput
	}
	count, err := s.repository.CountChildren(ctx, id)
	if err != nil {
		return err
	}
	if count > 0 {
		return ErrHasChildren
	}
	deleted, err := s.repository.Delete(ctx, id)
	if err != nil {
		return err
	}
	if !deleted {
		return ErrNotFound
	}
	return nil
}

func validInput(input Input) bool {
	return strings.TrimSpace(input.Path) != "" && strings.TrimSpace(input.Meta.Title) != ""
}

func buildMenuTree(items []MenuItem) []MenuItem {
	byID := make(map[int64]MenuItem, len(items))
	children := make(map[int64][]int64)
	roots := make([]int64, 0)
	for _, item := range items {
		item.Children = nil
		byID[item.ID] = item
	}
	for _, item := range items {
		if item.ParentID != 0 {
			if _, exists := byID[item.ParentID]; exists {
				children[item.ParentID] = append(children[item.ParentID], item.ID)
				continue
			}
		}
		roots = append(roots, item.ID)
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i] < roots[j] })
	for parentID := range children {
		sort.Slice(children[parentID], func(i, j int) bool { return children[parentID][i] < children[parentID][j] })
	}
	var build func(int64) MenuItem
	build = func(id int64) MenuItem {
		item := byID[id]
		for _, childID := range children[id] {
			child := build(childID)
			child.ParentName = item.Meta.Title
			item.Children = append(item.Children, child)
		}
		return item
	}
	tree := make([]MenuItem, 0, len(roots))
	for _, rootID := range roots {
		tree = append(tree, build(rootID))
	}
	return tree
}
