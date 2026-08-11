// Package organization owns the department hierarchy and user-to-department
// membership. user_id is an opaque, stable external subject reference — no
// IAM FK, no IAM imports. Application models are transport-neutral.
package organization

import "time"

// Department is one node of the hierarchy.
type Department struct {
	ID        int64
	Name      string
	ParentID  *int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// DepartmentNode is a department with its children; Children is never nil.
type DepartmentNode struct {
	Department
	Children []DepartmentNode
}

// MembershipState is the durable versioned membership row. It is retained
// across clear; only terminal IAM user deletion physically removes it.
// DepartmentID nil is the versioned no-department state.
type MembershipState struct {
	UserID            int64
	DepartmentID      *int64
	DepartmentName    *string
	MembershipVersion int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// MembershipMutationResult records previous and resulting membership state.
type MembershipMutationResult struct {
	UserID                     int64
	PreviousDepartmentID       *int64
	PreviousMembershipVersion  *int64
	ResultingDepartmentID      *int64
	ResultingMembershipVersion int64
}

// OperationContext is carried by every workflow mutation. Organization does
// not interpret actor permissions; it trusts the authenticated/authorized
// Admin BFF boundary and records minimum audit context.
type OperationContext struct {
	OperationID    string
	IdempotencyKey *string
	ActorUserID    int64
	CorrelationID  string
}

// IAMUserDeletedEvent is the accepted consumer envelope
// (event_type=iam.user.deleted, event_version=1). EventID is the producer
// outbox's UUID text (inbox dedupe key, organization_inbox_messages.event_id).
type IAMUserDeletedEvent struct {
	EventID          string
	EventType        string
	EventVersion     int
	Producer         string
	AggregateType    string
	AggregateID      string
	AggregateVersion int64
	UserID           int64
}

// CommandStatus classifies a stored command receipt.
type CommandStatus string

const (
	CommandSucceeded        CommandStatus = "succeeded"
	CommandTerminalRejected CommandStatus = "terminal_rejected"
)

// OrganizationCommandReceipt is the owner-local evidence of a committed
// workflow side effect. Membership receipts carry previous/resulting state.
type OrganizationCommandReceipt struct {
	OperationID                string
	CommandName                string
	RequestFingerprint         string
	Status                     CommandStatus
	SubjectID                  *int64
	PreviousDepartmentID       *int64
	PreviousMembershipVersion  *int64
	ResultingDepartmentID      *int64
	ResultingMembershipVersion *int64
	ErrorCode                  *string
}

// TrustedRecoveryContext mirrors iam's platform-adapter-only context.
type TrustedRecoveryContext struct {
	PrincipalID         int64
	AuthorizationSource string
	ApprovalID          string
	CorrelationID       string
	RequestID           string
}

// EvidenceCleanupEvaluation is the conservative dry-run result.
type EvidenceCleanupEvaluation struct {
	EligibleCounts          map[string]int64
	BlockingUnresolvedCount int64
	OldestReplayableAt      *time.Time
}
