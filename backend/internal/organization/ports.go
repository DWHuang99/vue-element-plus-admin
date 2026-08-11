package organization

import (
	"context"
	"time"
)

// Application ports (contracts/organization-application.md). Small
// compile-time interfaces; no adapter type leaks through here.

// DepartmentService owns the department hierarchy.
type DepartmentService interface {
	// ListDepartmentTree returns a deterministic tree; Children is [] never null.
	ListDepartmentTree(ctx context.Context) ([]DepartmentNode, error)
	GetDepartment(ctx context.Context, departmentID int64) (Department, error)
	// ValidateDepartment: nil means no department and is valid; a missing
	// non-null ID returns DepartmentNotFound.
	ValidateDepartment(ctx context.Context, departmentID *int64) (*Department, error)
	SaveDepartment(ctx context.Context, requestCtx OperationContext, id *int64, name string, parentID *int64) (int64, error)
	// DeleteDepartments is full-batch: any missing target, child department or
	// membership reference rejects the entire batch.
	DeleteDepartments(ctx context.Context, requestCtx OperationContext, ids []int64) error
}

// MembershipReader is the US1 read-side subset (BFF composition). The full
// MembershipService adds the versioned mutations at T036; until then the
// concrete *Service only asserts this reader, and the BFF participant is
// typed accordingly.
type MembershipReader interface {
	// GetUserDepartment: missing state row is a normal empty result, not an
	// error; null-department state is the durable no-department state.
	GetUserDepartment(ctx context.Context, userID int64) (*MembershipState, error)
	// BatchGetUserDepartments: one bounded query set, no per-user queries;
	// users with no state row are absent from the result map.
	BatchGetUserDepartments(ctx context.Context, userIDs []int64) (map[int64]MembershipState, error)
	// ListUserIDsByDepartment returns the complete deterministic ID set.
	ListUserIDsByDepartment(ctx context.Context, departmentID int64) ([]int64, error)
}

// MembershipService owns user-to-department membership. user_id is opaque.
type MembershipService interface {
	MembershipReader
	// SetUserDepartment: create expects no membership; update uses expected-
	// version CAS; stale version returns MembershipVersionConflict.
	SetUserDepartment(ctx context.Context, requestCtx OperationContext,
		userID, departmentID int64, expectedMembershipVersion *int64) (MembershipMutationResult, error)
	// ClearUserDepartment never deletes the version-bearing state row; it
	// creates/updates department_id = null under expected-state CAS.
	ClearUserDepartment(ctx context.Context, requestCtx OperationContext,
		userID int64, expectedMembershipVersion *int64) (MembershipMutationResult, error)
	// RestoreUserDepartment proceeds only when current membership still equals
	// the workflow's applied version; otherwise CompensationConflict.
	RestoreUserDepartment(ctx context.Context, requestCtx OperationContext,
		userID int64, expectedCurrentMembershipVersion int64,
		previousDepartmentID *int64, previousMembershipVersion *int64) (MembershipMutationResult, error)
	// ClearMembershipsForUsers is a local full-batch/idempotent cleanup.
	ClearMembershipsForUsers(ctx context.Context, requestCtx OperationContext, userIDs []int64) error
}

// InboxConsumer handles IAM domain events idempotently. The side effect and
// the successful dedupe row commit in one transaction; a rollback removes both.
type InboxConsumer interface {
	HandleIAMUserDeletedV1(ctx context.Context, event IAMUserDeletedEvent) error
}

// ReceiptResolver resolves a previously committed command after timeout/crash.
type ReceiptResolver interface {
	ResolveCommand(ctx context.Context, operationID, commandName, expectedFingerprint string) (*OrganizationCommandReceipt, error)
}

// EvidenceCleanupService is the conservative owner-local purge port.
type EvidenceCleanupService interface {
	EvaluateEvidenceCleanup(ctx context.Context, cutoff time.Time) (EvidenceCleanupEvaluation, error)
	PurgeEligibleEvidence(ctx context.Context, cutoff time.Time, trustedCtx TrustedRecoveryContext) (map[string]int64, error)
}

// Store is the Organization data-access port (driven side). The PostgreSQL
// adapter (internal/organization/postgres) implements it against the
// Organization-owned sqlc package; the service never touches pgx/sqlc/
// migrations (boundary rule). US1 adds the read side plus the department
// tree/mutations the BFF needs; the versioned membership mutations
// (Set/Clear/RestoreUserDepartment) extend this port with T036.
type Store interface {
	RunInTx(ctx context.Context, fn func(Store) error) error

	// departments
	ListDepartments(ctx context.Context) ([]Department, error)
	GetDepartmentByID(ctx context.Context, id int64) (Department, error)
	GetDepartmentByName(ctx context.Context, name string) (Department, error)
	CreateDepartment(ctx context.Context, name string, parentID *int64) (Department, error)
	UpdateDepartment(ctx context.Context, id int64, name string, parentID *int64) (Department, error)
	DeleteDepartment(ctx context.Context, id int64) error
	CountDepartmentsByParentID(ctx context.Context, parentID int64) (int64, error)
	CountUsersByDepartmentID(ctx context.Context, departmentID int64) (int64, error)

	// membership reads
	GetUserMembershipByUserID(ctx context.Context, userID int64) (*MembershipState, error)
	BatchGetUserMemberships(ctx context.Context, userIDs []int64) (map[int64]MembershipState, error)
	ListUserIDsByDepartment(ctx context.Context, departmentID int64) ([]int64, error)

	// membership mutations (US2/T036; adapter T037): row-locked CAS primitives.
	// LockMembershipByUserID returns nil when no state row exists; the service
	// decides create/update/tombstone against the expected version inside one
	// Organization-local transaction.
	LockMembershipByUserID(ctx context.Context, userID int64) (*MembershipState, error)
	CreateMembership(ctx context.Context, userID int64, departmentID *int64, version int64) (MembershipState, error)
	UpdateMembership(ctx context.Context, userID int64, departmentID *int64, version int64) (MembershipState, error)

	// command receipts (US2/T038): same-transaction evidence for committed
	// workflow side effects. GetCommandReceipt returns (nil, nil) when the
	// operation never committed.
	SaveCommandReceipt(ctx context.Context, receipt OrganizationCommandReceipt) error
	GetCommandReceipt(ctx context.Context, operationID, commandName string) (*OrganizationCommandReceipt, error)

	// terminal user-deleted consumption (US2/T039-T040): physical cleanup and
	// inbox dedupe, committed together by the service.
	DeleteMembershipsForUsers(ctx context.Context, userIDs []int64) error
	InboxMessageExists(ctx context.Context, eventID, handlerName string) (bool, error)
	SaveInboxMessage(ctx context.Context, eventID, handlerName, eventType string, eventVersion int, aggregateID string, aggregateVersion int64) error
	// IncrementInboxMetric (T066) bumps a cumulative inbox observability
	// counter (processed / duplicates / handler-failure outcomes). Counters
	// never change handler semantics.
	IncrementInboxMetric(ctx context.Context, metricKey string, delta int64) error
}
