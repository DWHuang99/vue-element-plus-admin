package usermanagement

import (
	"context"
	"errors"

	"vue-element-plus-admin/backend/internal/modules/permission"
)

var (
	ErrInvalidInput          = errors.New("invalid user management input")
	ErrUserNotFound          = errors.New("managed user not found")
	ErrUserConflict          = errors.New("managed user conflict")
	ErrDepartmentUnavailable = errors.New("department service unavailable")
	ErrDepartmentNotFound    = errors.New("department not found")
	ErrDepartmentDisabled    = errors.New("department is disabled")
	ErrDepartmentDeleting    = errors.New("department is being deleted")
)

type DepartmentItem = permission.DepartmentItem
type UserItem = permission.UserItem

type Filter struct {
	Page         int
	Size         int
	DepartmentID int64
	Username     string
	Account      string
}

type Input struct {
	Username     string `json:"username"`
	Account      string `json:"account"`
	Password     string `json:"password"`
	Email        string `json:"email"`
	RoleID       int64  `json:"roleId"`
	DepartmentID int64  `json:"departmentId"`
	Department   struct {
		ID int64 `json:"id"`
	} `json:"department"`
}

type Repository interface {
	List(ctx context.Context, filter Filter) ([]UserItem, int, error)
	Create(ctx context.Context, input Input, passwordHash string) error
	Update(ctx context.Context, id int64, input Input, passwordHash string) (bool, error)
	Delete(ctx context.Context, ids []int64) error
	CountByDepartment(ctx context.Context, departmentID int64) (int64, error)
}

type DepartmentDirectory interface {
	Get(ctx context.Context, id int64) (DepartmentItem, error)
	BatchGet(ctx context.Context, ids []int64) ([]DepartmentItem, error)
}
