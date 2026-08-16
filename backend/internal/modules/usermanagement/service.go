package usermanagement

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"vue-element-plus-admin/backend/internal/security"
)

type Service struct {
	repository          Repository
	departmentDirectory DepartmentDirectory
}

func NewService(repository Repository, departmentDirectory DepartmentDirectory) *Service {
	return &Service{repository: repository, departmentDirectory: departmentDirectory}
}

func (s *Service) List(ctx context.Context, filter Filter) ([]UserItem, int, error) {
	items, total, err := s.repository.List(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	ids := make([]int64, 0)
	seen := make(map[int64]struct{})
	for _, item := range items {
		if item.DepartmentID > 0 {
			if _, exists := seen[item.DepartmentID]; !exists {
				seen[item.DepartmentID] = struct{}{}
				ids = append(ids, item.DepartmentID)
			}
		}
	}
	if len(ids) == 0 {
		return items, total, nil
	}
	if s.departmentDirectory == nil {
		return nil, 0, ErrDepartmentUnavailable
	}
	departments, err := s.departmentDirectory.BatchGet(ctx, ids)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %v", ErrDepartmentUnavailable, err)
	}
	byID := make(map[int64]*DepartmentItem, len(departments))
	for index := range departments {
		department := departments[index]
		byID[department.ID] = &department
	}
	for index := range items {
		items[index].Department = byID[items[index].DepartmentID]
	}
	return items, total, nil
}

func (s *Service) Create(ctx context.Context, input Input) error {
	normalizeInput(&input)
	if strings.TrimSpace(input.Username) == "" || input.Password == "" || input.RoleID <= 0 {
		return ErrInvalidInput
	}
	if err := s.validateDepartment(ctx, input.DepartmentID); err != nil {
		return err
	}
	passwordHash, err := security.Hash(input.Password)
	if err != nil {
		return err
	}
	if err := s.repository.Create(ctx, input, passwordHash); err != nil {
		return fmt.Errorf("%w: %v", ErrUserConflict, err)
	}
	return nil
}

func (s *Service) Update(ctx context.Context, id int64, input Input) error {
	normalizeInput(&input)
	if id <= 0 || strings.TrimSpace(input.Username) == "" || input.RoleID <= 0 {
		return ErrInvalidInput
	}
	if err := s.validateDepartment(ctx, input.DepartmentID); err != nil {
		return err
	}
	passwordHash := ""
	if input.Password != "" {
		var err error
		passwordHash, err = security.Hash(input.Password)
		if err != nil {
			return err
		}
	}
	updated, err := s.repository.Update(ctx, id, input, passwordHash)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUserConflict, err)
	}
	if !updated {
		return ErrUserNotFound
	}
	return nil
}

func (s *Service) CountUsersByDepartment(ctx context.Context, departmentID int64) (int64, error) {
	if departmentID <= 0 {
		return 0, ErrInvalidInput
	}
	return s.repository.CountByDepartment(ctx, departmentID)
}

func (s *Service) validateDepartment(ctx context.Context, departmentID int64) error {
	if departmentID == 0 {
		return nil
	}
	if s.departmentDirectory == nil {
		return ErrDepartmentUnavailable
	}
	department, err := s.departmentDirectory.Get(ctx, departmentID)
	if errors.Is(err, ErrDepartmentNotFound) {
		return ErrDepartmentNotFound
	}
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDepartmentUnavailable, err)
	}
	if department.Status == 0 {
		return ErrDepartmentDisabled
	}
	if department.Deleting {
		return ErrDepartmentDeleting
	}
	return nil
}

func (s *Service) Delete(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return ErrInvalidInput
	}
	for _, id := range ids {
		if id <= 0 {
			return ErrInvalidInput
		}
	}
	return s.repository.Delete(ctx, ids)
}

func normalizeInput(input *Input) {
	if strings.TrimSpace(input.Account) == "" {
		input.Account = input.Username
	}
	if input.DepartmentID == 0 {
		input.DepartmentID = input.Department.ID
	}
}
