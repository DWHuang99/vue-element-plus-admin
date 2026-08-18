package department

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	usermanagementdirectory "vue-element-plus-admin/backend/internal/directory/usermanagement"
	"vue-element-plus-admin/backend/internal/modules/permission"
)

var (
	ErrInvalidInput           = errors.New("invalid department input")
	ErrNotFound               = errors.New("department not found")
	ErrHasChildren            = errors.New("department has children")
	ErrHasUsers               = errors.New("department has users")
	ErrUserServiceUnavailable = errors.New("user service unavailable")
	ErrDeletionInProgress     = errors.New("department deletion in progress")
)

type DepartmentItem = permission.DepartmentItem

type Input struct {
	ParentID       int64  `json:"parentId"`
	DepartmentName string `json:"departmentName"`
	Status         int    `json:"status"`
	Remark         string `json:"remark"`
}

type Service struct {
	repository    Repository
	userDirectory *usermanagementdirectory.Directory
}

func NewService(repository Repository, userDirectory ...*usermanagementdirectory.Directory) *Service {
	service := &Service{repository: repository}
	if len(userDirectory) > 0 {
		service.userDirectory = userDirectory[0]
	}
	return service
}

func (s *Service) Tree(ctx context.Context) ([]DepartmentItem, error) {
	items, err := s.repository.All(ctx)
	if err != nil {
		return nil, err
	}
	return buildDepartmentTree(items), nil
}

func (s *Service) List(ctx context.Context, page, size int, name string) ([]DepartmentItem, int, error) {
	return s.repository.List(ctx, page, size, name)
}

func (s *Service) Get(ctx context.Context, id int64) (DepartmentItem, error) {
	if id <= 0 {
		return DepartmentItem{}, ErrInvalidInput
	}
	item, err := s.repository.Get(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return DepartmentItem{}, ErrNotFound
	}
	return item, err
}

func (s *Service) BatchGet(ctx context.Context, ids []int64) ([]DepartmentItem, error) {
	if len(ids) == 0 {
		return []DepartmentItem{}, nil
	}
	for _, id := range ids {
		if id <= 0 {
			return nil, ErrInvalidInput
		}
	}
	return s.repository.BatchGet(ctx, ids)
}

func (s *Service) Create(ctx context.Context, input Input) error {
	if strings.TrimSpace(input.DepartmentName) == "" {
		return ErrInvalidInput
	}
	return s.repository.Create(ctx, input)
}

func (s *Service) Update(ctx context.Context, id int64, input Input) error {
	if id <= 0 || strings.TrimSpace(input.DepartmentName) == "" || input.ParentID == id {
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

func (s *Service) Delete(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return ErrInvalidInput
	}
	uniqueIDs := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return ErrInvalidInput
		}
		if _, exists := seen[id]; !exists {
			seen[id] = struct{}{}
			uniqueIDs = append(uniqueIDs, id)
		}
	}
	sort.Slice(uniqueIDs, func(i, j int) bool { return uniqueIDs[i] < uniqueIDs[j] })
	for _, id := range uniqueIDs {
		count, err := s.repository.CountChildren(ctx, id)
		if err != nil {
			return err
		}
		if count > 0 {
			return ErrHasChildren
		}
	}

	markedIDs := make([]int64, 0, len(uniqueIDs))
	deleted := false
	defer func() {
		if deleted || len(markedIDs) == 0 {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_ = s.repository.CancelDeletions(cleanupCtx, markedIDs)
	}()
	for _, id := range uniqueIDs {
		marked, err := s.repository.BeginDeletion(ctx, id)
		if err != nil {
			return err
		}
		if !marked {
			_, getErr := s.repository.Get(ctx, id)
			if errors.Is(getErr, sql.ErrNoRows) || errors.Is(getErr, ErrNotFound) {
				return ErrNotFound
			}
			if getErr != nil {
				return getErr
			}
			return ErrDeletionInProgress
		}
		markedIDs = append(markedIDs, id)
	}

	for _, id := range uniqueIDs {
		if s.userDirectory == nil {
			return ErrUserServiceUnavailable
		}
		userCount, err := s.userDirectory.CountUsersByDepartment(ctx, id)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrUserServiceUnavailable, err)
		}
		if userCount > 0 {
			return ErrHasUsers
		}
	}
	deletedCount, err := s.repository.Delete(ctx, uniqueIDs)
	if err != nil {
		return err
	}
	if deletedCount != len(uniqueIDs) {
		return ErrDeletionInProgress
	}
	deleted = true
	return nil
}

func buildDepartmentTree(items []DepartmentItem) []DepartmentItem {
	byID := make(map[int64]DepartmentItem, len(items))
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
	var build func(int64) DepartmentItem
	build = func(id int64) DepartmentItem {
		item := byID[id]
		for _, childID := range children[id] {
			item.Children = append(item.Children, build(childID))
		}
		return item
	}
	tree := make([]DepartmentItem, 0, len(roots))
	for _, id := range roots {
		tree = append(tree, build(id))
	}
	return tree
}
