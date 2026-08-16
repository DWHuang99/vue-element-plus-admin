package role

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"vue-element-plus-admin/backend/internal/modules/permission"
)

var (
	ErrInvalidInput     = errors.New("invalid role input")
	ErrNotFound         = errors.New("role not found")
	ErrAssignedUsers    = errors.New("role is assigned to users")
	ErrRoleCodeConflict = errors.New("role code already exists")
	ErrInvalidAccess    = errors.New("invalid menu or permission assignment")
)

type RoleItem = permission.RoleItem
type MenuItem = permission.MenuItem
type PermissionItem = permission.PermissionItem

type Input struct {
	Code     string     `json:"code"`
	RoleName string     `json:"roleName"`
	Status   int        `json:"status"`
	Remark   string     `json:"remark"`
	Menu     []MenuItem `json:"menu"`
}

type Service struct {
	repository Repository
}

func NewService(repository Repository) *Service {
	return &Service{repository: repository}
}

func (s *Service) List(ctx context.Context, page, size int, name string) ([]RoleItem, int, error) {
	items, total, err := s.repository.List(ctx, page, size, name)
	if err != nil {
		return nil, 0, err
	}
	for i := range items {
		menus, err := s.repository.ListMenus(ctx, items[i].ID)
		if err != nil {
			return nil, 0, err
		}
		items[i].Menu = buildMenuTree(menus)
	}
	return items, total, nil
}

func (s *Service) Get(ctx context.Context, id int64) (RoleItem, error) {
	if id <= 0 {
		return RoleItem{}, ErrInvalidInput
	}
	item, err := s.repository.Get(ctx, id)
	if isNotFound(err) {
		return RoleItem{}, ErrNotFound
	}
	if err != nil {
		return RoleItem{}, err
	}
	menus, err := s.repository.ListMenus(ctx, id)
	if err != nil {
		return RoleItem{}, err
	}
	item.Menu = buildMenuTree(menus)
	return item, nil
}

func (s *Service) Create(ctx context.Context, input Input) error {
	if strings.TrimSpace(input.RoleName) == "" {
		return ErrInvalidInput
	}
	if strings.TrimSpace(input.Code) == "" {
		input.Code = fmt.Sprintf("role_%d", time.Now().UnixNano())
	}
	assignments := collectAssignments(input.Menu)
	globalPermissions := globalPermissionsForRole(input.Code)
	return s.repository.WithinTx(ctx, func(repository Repository) error {
		roleID, err := repository.Create(ctx, input)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrRoleCodeConflict, err)
		}
		if err := repository.ReplaceAccess(ctx, roleID, assignments, globalPermissions); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidAccess, err)
		}
		return nil
	})
}

func (s *Service) Update(ctx context.Context, id int64, input Input) error {
	if id <= 0 || strings.TrimSpace(input.RoleName) == "" {
		return ErrInvalidInput
	}
	assignments := collectAssignments(input.Menu)
	return s.repository.WithinTx(ctx, func(repository Repository) error {
		current, err := repository.Get(ctx, id)
		if isNotFound(err) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		input.Code = current.Code
		if err := repository.Update(ctx, id, input); isNotFound(err) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if err := repository.ReplaceAccess(ctx, id, assignments, globalPermissionsForRole(current.Code)); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidAccess, err)
		}
		return nil
	})
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	if id <= 0 {
		return ErrInvalidInput
	}
	count, err := s.repository.CountUsers(ctx, id)
	if err != nil {
		return err
	}
	if count > 0 {
		return ErrAssignedUsers
	}
	if err := s.repository.Delete(ctx, id); isNotFound(err) {
		return ErrNotFound
	} else {
		return err
	}
}

func collectAssignments(menus []MenuItem) []MenuAssignment {
	byID := make(map[int64]MenuAssignment)
	var collect func([]MenuItem)
	collect = func(items []MenuItem) {
		for _, item := range items {
			if item.ID > 0 {
				seen := make(map[string]struct{})
				codes := make([]string, 0, len(item.Meta.Permission))
				for _, code := range item.Meta.Permission {
					code = strings.TrimSpace(code)
					if code == "" {
						continue
					}
					if _, exists := seen[code]; exists {
						continue
					}
					seen[code] = struct{}{}
					codes = append(codes, code)
				}
				sort.Strings(codes)
				byID[item.ID] = MenuAssignment{MenuID: item.ID, PermissionCodes: codes}
			}
			collect(item.Children)
		}
	}
	collect(menus)
	menuIDs := make([]int64, 0, len(byID))
	for menuID := range byID {
		menuIDs = append(menuIDs, menuID)
	}
	sort.Slice(menuIDs, func(i, j int) bool { return menuIDs[i] < menuIDs[j] })
	assignments := make([]MenuAssignment, 0, len(menuIDs))
	for _, menuID := range menuIDs {
		assignments = append(assignments, byID[menuID])
	}
	return assignments
}

func globalPermissionsForRole(code string) []string {
	if code == "admin" {
		return []string{"*.*.*"}
	}
	return nil
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
