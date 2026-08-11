//go:build rollback

package rbac

import "errors"

// Sentinel errors produced by the rbac service layer.
// The handler maps each to a contract error code (see contracts/rbac-api.md).
var (
	// ErrRoleNotFound is returned when a referenced role id does not exist.
	ErrRoleNotFound = errors.New("role not found")
	// ErrDepartmentNotFound is returned when a referenced department id does not exist.
	ErrDepartmentNotFound = errors.New("department not found")
	// ErrUserNotFound is returned when a referenced user id does not exist.
	ErrUserNotFound = errors.New("user not found")
	// ErrNameTaken is returned when a unique name/code/username already exists.
	ErrNameTaken = errors.New("name already taken")
	// ErrDeleteProtected is returned when a delete is blocked by live references.
	ErrDeleteProtected = errors.New("delete blocked by references")
	// ErrBuiltinRoleCodeImmutable protects the stable codes used by authorization.
	ErrBuiltinRoleCodeImmutable = errors.New("built-in role code is immutable")
	// ErrBuiltinRoleDeleteProtected prevents removal of required built-in roles.
	ErrBuiltinRoleDeleteProtected = errors.New("built-in role cannot be deleted")
	// ErrInvalidInput is returned for inputs the handler cannot validate alone.
	ErrInvalidInput = errors.New("invalid input")
)
