// Workflow-state models (T051; contracts/consistency-and-compensation.md).
// The BFF-owned durable workflow aggregate mirrors the admin_workflows and
// admin_workflow_subjects tables. Password/token/hash material never appears
// in any of these fields — the request fingerprint is the canonical safe
// digest, never the request body.
package adminbff

import (
	"time"
)

// WorkflowState is the durable workflow state machine
// (contracts/consistency-and-compensation.md Terminal states).
type WorkflowState string

const (
	WorkflowPending             WorkflowState = "pending"
	WorkflowRunning             WorkflowState = "running"
	WorkflowAwaitingClientInput WorkflowState = "awaiting_client_input"
	WorkflowCompensating        WorkflowState = "compensating"
	WorkflowSucceeded           WorkflowState = "succeeded"
	WorkflowRejected            WorkflowState = "rejected"
	WorkflowFailedRetryable     WorkflowState = "failed_retryable"
	WorkflowFailedManual        WorkflowState = "failed_manual"
)

// CompensationState tracks recovery within a workflow.
type CompensationState string

const (
	CompensationNotRequired CompensationState = "not_required"
	CompensationPending     CompensationState = "pending"
	CompensationRunning     CompensationState = "running"
	CompensationSucceeded   CompensationState = "succeeded"
	CompensationFailed      CompensationState = "failed"
)

// Workflow is the durable BFF-owned workflow record. It is not a source of
// truth for users/departments/roles — only for the cross-domain orchestration
// state, idempotency evidence and compensation progress.
type Workflow struct {
	OperationID               string
	OperationType             string
	IdempotencyKey            *string
	RequestFingerprint        string
	ActorUserID               int64
	SubjectUserID             *int64
	State                     WorkflowState
	CurrentStep               string
	CompensationState         CompensationState
	ExpectedIamVersion        *int64
	ResultingIamVersion       *int64
	PreviousDepartmentID      *int64
	PreviousMembershipVersion *int64
	AppliedDepartmentID       *int64
	AppliedMembershipVersion  *int64
	// CommandFingerprint is the participant command fingerprint persisted
	// immediately before a credential-bearing side effect (update U5). The
	// worker resume machine resolves that receipt with this fingerprint; the
	// request fields themselves are never stored.
	CommandFingerprint *string
	Result             map[string]any
	LastErrorCode      *string
	CreatedAt          time.Time
	UpdatedAt          time.Time
	AttemptCount       int
	// CompensationAttemptCount counts compensation reclaims separately from
	// the forward AttemptCount: both share the same 10-attempt / original
	// retry_deadline_at ceiling, but compensation never consumes the forward
	// budget (T055).
	CompensationAttemptCount int
	NextRetryAt              *time.Time
	ClientInputDeadlineAt    *time.Time
	RetryDeadlineAt          time.Time
	LeaseOwner               *string
	LeasedUntil              *time.Time
	ClaimToken               *string
	CompletedAt              *time.Time
}

// WorkflowSubject is one per-subject workflow row (single or batch target).
// The partial unique index on (subject_user_id) WHERE active enforces the
// no-overlapping-active-workflow exclusion.
type WorkflowSubject struct {
	OperationID               string
	SubjectUserID             int64
	ExpectedIamVersion        int64
	ResultingTombstoneVersion *int64
	Active                    bool
}

// WorkflowEvidenceWatermark is the Admin BFF cleanup watermark (data-model.md
// line 350): the coordinator refuses evidence cleanup while any workflow still
// references evidence. A workflow is unresolved/referencing while it is not
// terminally resolved OR still holds an active subject exclusion (a
// possibly-applied effect awaiting reconciliation).
type WorkflowEvidenceWatermark struct {
	// UnresolvedCount counts workflows that are not succeeded/rejected or that
	// still hold an active subject exclusion. Zero means "no workflow keeps
	// evidence alive".
	UnresolvedCount int64
	// OldestUnresolvedAt is the oldest updated_at among unresolved workflows,
	// nil when UnresolvedCount is zero. The coordinator raises its effective
	// cutoff to at least this watermark.
	OldestUnresolvedAt *time.Time
	// OldestWorkflowAt is the oldest created_at across ALL workflows, nil when
	// none exist. Receipts are never purged shorter than the workflows that
	// reference them.
	OldestWorkflowAt *time.Time
}

// StepUpdate carries the durable fields a claimed worker persists at a step
// boundary. Nil fields leave the stored value untouched (COALESCE in SQL);
// every write is CAS-guarded by (operation_id, state, claim_token). The
// compensation lifecycle uses its own explicit transitions (BeginCompensation /
// SetCompensationSucceeded / SetCompensationFailed), not this struct.
type StepUpdate struct {
	Step                      string
	SubjectUserID             *int64
	ExpectedIamVersion        *int64
	ResultingIamVersion       *int64
	PreviousDepartmentID      *int64
	PreviousMembershipVersion *int64
	AppliedDepartmentID       *int64
	AppliedMembershipVersion  *int64
	// CommandFingerprint is the credential-bearing command fingerprint
	// persisted at the step boundary BEFORE the participant call (update U5).
	CommandFingerprint *string
}

// RecoveryActionType is the immutable manual-recovery ledger classification
// (000007; only these three action types may ever be recorded).
type RecoveryActionType string

const (
	// RecoveryReceiptFinalize finalizes a proven participant receipt without
	// any new side effect.
	RecoveryReceiptFinalize RecoveryActionType = "receipt_finalize"
	// RecoveryApprovedCompensation executes explicitly approved compensation
	// (restore/delete) through the least-privilege owner ports.
	RecoveryApprovedCompensation RecoveryActionType = "approved_compensation"
	// RecoveryReconciliation records a reconciliation decision that needs no
	// workflow-store mutation (e.g. operator review of a failed_manual row).
	RecoveryReconciliation RecoveryActionType = "reconciliation"
)

// RecoveryAction is one immutable append-only manual-recovery ledger row
// (contracts/consistency-and-compensation.md Reconciliation). Recorded before
// it executes; recovery never resumes a revoked/failed actor's forward
// request.
type RecoveryAction struct {
	ActionID            string
	OperationID         string
	RecoveryPrincipalID string
	AuthorizationSource string
	ApprovalID          *string
	ActionType          RecoveryActionType
	ReasonCode          string
	PreviousState       WorkflowState
	ResultingState      WorkflowState
	SafeResultCode      *string
	RequestID           *string
	CorrelationID       *string
	CreatedAt           time.Time
}
