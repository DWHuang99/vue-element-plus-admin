package organization

import "fmt"

// Kind identifies an Organization application error without parsing messages.
type Kind string

const (
	KindInvalidInput              Kind = "invalid_input"
	KindDepartmentNotFound        Kind = "department_not_found"
	KindNameTaken                 Kind = "name_taken"
	KindHierarchyConflict         Kind = "hierarchy_conflict"
	KindDeleteProtected           Kind = "delete_protected"
	KindOperationConflict         Kind = "operation_conflict"
	KindMembershipVersionConflict Kind = "membership_version_conflict"
	KindCompensationConflict      Kind = "compensation_conflict"
	KindUnsupportedEventVersion   Kind = "unsupported_event_version"
	KindUnavailable               Kind = "unavailable"
	KindTimeout                   Kind = "timeout"
	KindInternal                  Kind = "internal"
	// Evidence cleanup (T078): the approved owner-local purge refuses without an
	// operational approval. Organization evidence is always terminal (schema
	// CHECK), so there is no unresolved-evidence kind here.
	KindCleanupApprovalRequired Kind = "cleanup_approval_required"
)

// FieldError is one validation failure within an InvalidInput error.
type FieldError struct {
	Field   string
	Code    string
	Message string
}

// Error is the Organization application error. Match it with errors.Is
// against the sentinels below.
type Error struct {
	Kind    Kind
	Message string
	Fields  []FieldError
}

func (e *Error) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("organization: %s: %s", e.Kind, e.Message)
	}
	return fmt.Sprintf("organization: %s", e.Kind)
}

// Is lets errors.Is match by Kind against the package sentinels.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && e.Kind == t.Kind
}

var (
	ErrInvalidInput              = &Error{Kind: KindInvalidInput, Message: "validation failed"}
	ErrDepartmentNotFound        = &Error{Kind: KindDepartmentNotFound, Message: "department not found"}
	ErrNameTaken                 = &Error{Kind: KindNameTaken, Message: "name already taken"}
	ErrHierarchyConflict         = &Error{Kind: KindHierarchyConflict, Message: "hierarchy conflict"}
	ErrDeleteProtected           = &Error{Kind: KindDeleteProtected, Message: "entity has protected references"}
	ErrOperationConflict         = &Error{Kind: KindOperationConflict, Message: "operation conflict"}
	ErrMembershipVersionConflict = &Error{Kind: KindMembershipVersionConflict, Message: "stale expected membership version"}
	ErrCompensationConflict      = &Error{Kind: KindCompensationConflict, Message: "applied version changed; cannot safely restore"}
	ErrUnsupportedEventVersion   = &Error{Kind: KindUnsupportedEventVersion, Message: "unsupported event version"}
	ErrUnavailable               = &Error{Kind: KindUnavailable, Message: "dependency unavailable"}
	ErrTimeout                   = &Error{Kind: KindTimeout, Message: "timeout with unresolved outcome"}
	ErrInternal                  = &Error{Kind: KindInternal, Message: "internal error"}
	ErrCleanupApprovalRequired   = &Error{Kind: KindCleanupApprovalRequired, Message: "evidence cleanup requires an operational approval"}
)

// NewInvalidInput builds a validation error with field details.
func NewInvalidInput(message string, fields []FieldError) *Error {
	return &Error{Kind: KindInvalidInput, Message: message, Fields: fields}
}

// Wrap creates a typed error with context for the given kind.
func Wrap(kind Kind, message string) *Error {
	return &Error{Kind: kind, Message: message}
}
