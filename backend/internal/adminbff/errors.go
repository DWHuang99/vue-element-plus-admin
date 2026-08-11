// Package adminbff is the Admin BFF application layer: public HTTP DTOs,
// cross-domain orchestration and the workflow store. It never imports pgx,
// generated sqlc packages or migrations (architecture rule; the only
// sanctioned exception is the workflow-store adapter internal/adminbff/postgres).
package adminbff

import "fmt"

// Kind identifies a BFF application error. Kinds map to public HTTP codes in
// the transport layer; error types themselves stay HTTP-status-free.
type Kind string

const (
	KindInvalidInput           Kind = "invalid_input"           // 400 AUTH_INVALID_INPUT
	KindUserNotFound           Kind = "user_not_found"          // 404 USER_NOT_FOUND
	KindUnauthorized           Kind = "unauthorized"            // 401 AUTH_INVALID_TOKEN
	KindForbidden              Kind = "forbidden"               // 403 AUTH_FORBIDDEN
	KindIdempotencyConflict    Kind = "idempotency_conflict"    // 409 IDEMPOTENCY_CONFLICT
	KindOperationInProgress    Kind = "operation_in_progress"   // 409 OPERATION_IN_PROGRESS + Retry-After
	KindOperationExpired       Kind = "operation_expired"       // 409 OPERATION_EXPIRED
	KindDependencyUnavailable  Kind = "dependency_unavailable"  // 503 DEPENDENCY_UNAVAILABLE + Retry-After
	KindDependencyTimeout      Kind = "dependency_timeout"      // 504 DEPENDENCY_TIMEOUT + Retry-After
	KindWorkflowRetryable      Kind = "workflow_retryable"      // 503 WORKFLOW_RETRYABLE + Retry-After
	KindReconciliationRequired Kind = "reconciliation_required" // 500 RECONCILIATION_REQUIRED
	KindInternal               Kind = "internal"                // 500 INTERNAL_ERROR
)

// FieldError is one validation failure within an InvalidInput error.
type FieldError struct {
	Field   string
	Code    string
	Message string
}

// Error is the BFF application error. The transport layer maps Kind to the
// public contract status/code and Retry-After rules
// (contracts/http-api-compatibility.md workflow and dependency mapping).
type Error struct {
	Kind       Kind
	Message    string
	Fields     []FieldError
	RetryAfter int // optional integer delta-seconds; presence/range enforced by transport
}

func (e *Error) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("adminbff: %s: %s", e.Kind, e.Message)
	}
	return fmt.Sprintf("adminbff: %s", e.Kind)
}

// Is lets errors.Is match by Kind against the package sentinels.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && e.Kind == t.Kind
}

var (
	ErrInvalidInput           = &Error{Kind: KindInvalidInput, Message: "validation failed"}
	ErrUserNotFound           = &Error{Kind: KindUserNotFound, Message: "user not found"}
	ErrUnauthorized           = &Error{Kind: KindUnauthorized, Message: "unauthorized"}
	ErrForbidden              = &Error{Kind: KindForbidden, Message: "forbidden"}
	ErrIdempotencyConflict    = &Error{Kind: KindIdempotencyConflict, Message: "idempotency conflict"}
	ErrOperationInProgress    = &Error{Kind: KindOperationInProgress, Message: "operation in progress"}
	ErrOperationExpired       = &Error{Kind: KindOperationExpired, Message: "operation expired"}
	ErrDependencyUnavailable  = &Error{Kind: KindDependencyUnavailable, Message: "dependency unavailable"}
	ErrDependencyTimeout      = &Error{Kind: KindDependencyTimeout, Message: "dependency timeout"}
	ErrWorkflowRetryable      = &Error{Kind: KindWorkflowRetryable, Message: "workflow retryable"}
	ErrReconciliationRequired = &Error{Kind: KindReconciliationRequired, Message: "reconciliation required"}
	ErrInternal               = &Error{Kind: KindInternal, Message: "internal error"}

	// Workflow-store level errors (T051): the service maps these to the public
	// kinds above — they never escape the transport as-is.
	// ErrStaleClaim: a persist/complete/reschedule CAS missed because the
	// workflow is no longer claimed by this worker (expired/reclaimed/terminal);
	// the caller re-reads the workflow to decide.
	ErrStaleClaim = &Error{Kind: KindInternal, Message: "workflow claim is stale"}
	// ErrSubjectExcluded: another active workflow already covers the subject
	// (admin_workflow_subjects partial unique exclusion).
	ErrSubjectExcluded = &Error{Kind: KindOperationInProgress, Message: "subject has an active workflow"}
	// ErrWorkflowNotFound: no workflow row exists for the operation.
	ErrWorkflowNotFound = &Error{Kind: KindInternal, Message: "workflow not found"}
	// ErrInvalidOperationID: the operation id is not a valid UUID.
	ErrInvalidOperationID = &Error{Kind: KindInvalidInput, Message: "invalid operation id"}
)

// NewInvalidInput builds a validation error with field details.
func NewInvalidInput(message string, fields []FieldError) *Error {
	return &Error{Kind: KindInvalidInput, Message: message, Fields: fields}
}

// WithRetryAfter returns a copy of the error carrying the contract's integer
// delta-seconds. The copy semantics keep the package sentinels immutable: a
// decorated error must never mutate the shared Err* value visible to other
// call sites and errors.Is comparisons (which match by Kind).
func (e *Error) WithRetryAfter(seconds int) *Error {
	copied := *e
	copied.RetryAfter = seconds
	return &copied
}
