package iam

import "fmt"

// Kind identifies an IAM application error without parsing human messages.
type Kind string

const (
	KindInvalidInput               Kind = "invalid_input"
	KindInvalidCredentials         Kind = "invalid_credentials"
	KindInvalidToken               Kind = "invalid_token"
	KindUsernameTaken              Kind = "username_taken"
	KindNameTaken                  Kind = "name_taken"
	KindUserNotFound               Kind = "user_not_found"
	KindRoleNotFound               Kind = "role_not_found"
	KindBuiltinRoleCodeImmutable   Kind = "builtin_role_code_immutable"
	KindBuiltinRoleDeleteProtected Kind = "builtin_role_delete_protected"
	KindDeleteProtected            Kind = "delete_protected"
	KindOperationConflict          Kind = "operation_conflict"
	KindVersionConflict            Kind = "version_conflict"
	KindInvalidLifecycleTransition Kind = "invalid_lifecycle_transition"
	KindUnavailable                Kind = "unavailable"
	KindTimeout                    Kind = "timeout"
	KindInternal                   Kind = "internal"
	// Outbox delivery (T061): claim-token CAS misses and requeue preconditions.
	KindOutboxStaleClaim Kind = "outbox_stale_claim"
	KindOutboxNotBlocked Kind = "outbox_not_blocked"
	// Evidence cleanup (T078): the approved owner-local purge refuses without an
	// operational approval or while any outbox event is unresolved.
	KindCleanupApprovalRequired Kind = "cleanup_approval_required"
	KindEvidenceCleanupBlocked  Kind = "evidence_cleanup_blocked"
)

// FieldError is one validation failure within an InvalidInput error.
type FieldError struct {
	Field   string
	Code    string
	Message string
}

// Error is the IAM application error. Match it with errors.Is against the
// sentinels below; Fields is populated only for KindInvalidInput.
type Error struct {
	Kind    Kind
	Message string
	Fields  []FieldError
}

func (e *Error) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("iam: %s: %s", e.Kind, e.Message)
	}
	return fmt.Sprintf("iam: %s", e.Kind)
}

// Is lets errors.Is match by Kind against the package sentinels.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && e.Kind == t.Kind
}

// Sentinel errors; concrete instances carry context (IDs, fields).
var (
	ErrInvalidInput               = &Error{Kind: KindInvalidInput, Message: "validation failed"}
	ErrInvalidCredentials         = &Error{Kind: KindInvalidCredentials, Message: "invalid credentials"}
	ErrInvalidToken               = &Error{Kind: KindInvalidToken, Message: "invalid token"}
	ErrUsernameTaken              = &Error{Kind: KindUsernameTaken, Message: "username already taken"}
	ErrNameTaken                  = &Error{Kind: KindNameTaken, Message: "name already taken"}
	ErrUserNotFound               = &Error{Kind: KindUserNotFound, Message: "user not found"}
	ErrRoleNotFound               = &Error{Kind: KindRoleNotFound, Message: "role not found"}
	ErrBuiltinRoleCodeImmutable   = &Error{Kind: KindBuiltinRoleCodeImmutable, Message: "built-in role code is immutable"}
	ErrBuiltinRoleDeleteProtected = &Error{Kind: KindBuiltinRoleDeleteProtected, Message: "built-in role cannot be deleted"}
	ErrDeleteProtected            = &Error{Kind: KindDeleteProtected, Message: "entity has protected references"}
	ErrOperationConflict          = &Error{Kind: KindOperationConflict, Message: "operation conflict"}
	ErrVersionConflict            = &Error{Kind: KindVersionConflict, Message: "stale expected version"}
	ErrInvalidLifecycleTransition = &Error{Kind: KindInvalidLifecycleTransition, Message: "invalid lifecycle transition"}
	ErrUnavailable                = &Error{Kind: KindUnavailable, Message: "dependency unavailable"}
	ErrTimeout                    = &Error{Kind: KindTimeout, Message: "timeout with unresolved outcome"}
	ErrInternal                   = &Error{Kind: KindInternal, Message: "internal error"}
	ErrOutboxStaleClaim           = &Error{Kind: KindOutboxStaleClaim, Message: "stale outbox claim token"}
	ErrOutboxNotBlocked           = &Error{Kind: KindOutboxNotBlocked, Message: "outbox event is not blocked"}
	ErrCleanupApprovalRequired    = &Error{Kind: KindCleanupApprovalRequired, Message: "evidence cleanup requires an operational approval"}
	ErrEvidenceCleanupBlocked     = &Error{Kind: KindEvidenceCleanupBlocked, Message: "evidence cleanup blocked by unresolved evidence"}
)

// NewInvalidInput builds a validation error with field details.
func NewInvalidInput(message string, fields []FieldError) *Error {
	return &Error{Kind: KindInvalidInput, Message: message, Fields: fields}
}

// Wrap creates a typed error with context for the given kind.
func Wrap(kind Kind, message string) *Error {
	return &Error{Kind: kind, Message: message}
}
