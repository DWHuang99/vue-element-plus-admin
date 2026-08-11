package adminbff

import (
	"context"
	"time"

	"github.com/hdw/vue-element-plus-admin/backend/internal/iam"
	"github.com/hdw/vue-element-plus-admin/backend/internal/organization"
)

// IAMParticipants groups the IAM application ports the BFF orchestrates.
// Each port stays small and compile-time; no adapter type appears here.
type IAMParticipants struct {
	Auth     iam.AuthService
	Identity iam.IdentityService
	Roles    iam.RoleService
	Managed  iam.ManagedUserService
	Receipts iam.ReceiptResolver
}

// OrganizationParticipants groups the Organization application ports.
// Membership is the full versioned MembershipService: the US3 workflows need
// SetUserDepartment/RestoreUserDepartment for the C5/D5 side effects and the
// read-side members of the interface for composition.
type OrganizationParticipants struct {
	Departments organization.DepartmentService
	Membership  organization.MembershipService
	Receipts    organization.ReceiptResolver
	Inbox       organization.InboxConsumer
}

// WorkflowStore is the BFF-owned durable workflow store port (T051). The
// PostgreSQL adapter lives in internal/adminbff/postgres — the only sanctioned
// BFF exception to the no-pgx/sqlc rule (plan.md Architecture).
//
// Semantics (contracts/consistency-and-compensation.md Durable workflow
// execution lease/retry):
//   - every mutation of a claimed workflow is CAS-guarded by
//     (operation_id, state IN (running, compensating), claim_token): a stale
//     or expired worker can never overwrite a newer owner — zero rows surface
//     as ErrStaleClaim and the caller re-reads the workflow to decide;
//   - claim transitions pending/failed_retryable -> running and expired
//     awaiting_client_input -> compensating directly; forward claims
//     (pending/failed_retryable/expired running) count one forward attempt,
//     expired-input and compensation reclaims do not;
//   - participant calls happen outside the store transaction; step persistence
//     is durable before/after every side-effect boundary.
type WorkflowStore interface {
	// RunInTx executes fn on a WorkflowStore bound to one BFF-local transaction
	// (commit on nil error, rollback otherwise) — e.g. insert all delete-batch
	// subjects atomically, or persist tombstones + complete in one commit.
	RunInTx(ctx context.Context, fn func(WorkflowStore) error) error

	// CreateWorkflow durably persists a new pending workflow before any
	// participant side effect (C0/U0/D0). RetryDeadlineAt is supplied by the
	// caller (default creation +24h) and is immutable afterwards.
	CreateWorkflow(ctx context.Context, w Workflow) error
	// GetWorkflowByOperationID reads one workflow; absence is (nil, nil) —
	// never fabricated.
	GetWorkflowByOperationID(ctx context.Context, operationID string) (*Workflow, error)
	// GetWorkflowByIdempotencyKey resolves the idempotency scope
	// (operation_type, actor_user_id, idempotency_key); absence is (nil, nil).
	GetWorkflowByIdempotencyKey(ctx context.Context, operationType string, actorUserID int64, idempotencyKey string) (*Workflow, error)

	// ClaimWorkflows atomically leases eligible workflows: pending, retryable
	// (next_retry_at due), expired running/compensating (leased_until <= now()),
	// expired awaiting_client_input (client_input_deadline_at <= now()).
	// Forward claims count one attempt; expired-input claims transition
	// directly to compensating without counting. Rows are bounded by `limit`
	// and never tight-loop (SKIP LOCKED).
	ClaimWorkflows(ctx context.Context, leaseOwner string, leaseDuration time.Duration, limit int, now time.Time) ([]Workflow, error)
	// ClaimWorkflowByOperationID claims ONE specific workflow with the same
	// predicates as ClaimWorkflows — pending/failed_retryable -> running
	// (forward attempt, capped at 10), expired running -> running reclaim,
	// expired awaiting_client_input -> compensating (expiry finalization, no
	// attempt), expired compensating -> compensating reclaim. This is the
	// keyed counterpart the synchronous HTTP path and the resume machine use:
	// a limit=1 batch claim would steal whichever workflow is oldest. Zero
	// rows (live lease, exhausted, past retry deadline, or absent) surface as
	// ErrStaleClaim; the caller re-reads the workflow to classify.
	ClaimWorkflowByOperationID(ctx context.Context, operationID, leaseOwner string, leaseDuration time.Duration, now time.Time) (*Workflow, error)
	// ClaimAwaitingClientInput reacquires forward execution for a same-key
	// authorized HTTP retry before the input deadline: awaiting_client_input ->
	// running with a fresh token, no attempt counted until the participant
	// command is actually attempted. Zero rows (deadline passed, state moved
	// on, or key mismatch) surface as ErrStaleClaim.
	ClaimAwaitingClientInput(ctx context.Context, operationID, idempotencyKey, leaseOwner string, leaseDuration time.Duration, now time.Time) (*Workflow, error)

	// PersistStep durably records progress after a side-effect boundary
	// (C4/C6/U5/U7/D5). Nil StepUpdate fields keep the stored value.
	PersistStep(ctx context.Context, operationID, claimToken string, update StepUpdate) error
	// BeginCompensation transitions a claimed workflow running -> compensating
	// with compensation_state pending (the compensation decision is durable
	// before any compensation side effect).
	BeginCompensation(ctx context.Context, operationID, claimToken string) error
	// SetCompensationSucceeded marks a committed compensation side effect
	// (compensation_state succeeded; lease kept until the workflow resolves).
	SetCompensationSucceeded(ctx context.Context, operationID, claimToken string) error
	// SetCompensationFailed marks a failed/conflicted compensation side effect
	// (compensation_state failed; reconciliation follows via FailManual).
	SetCompensationFailed(ctx context.Context, operationID, claimToken string) error
	// EnterAwaitingClientInput parks a claimed workflow whose required
	// credential was not durably committed: awaiting_client_input with an
	// immutable deadline, lease cleared (waiting consumes no attempt budget).
	EnterAwaitingClientInput(ctx context.Context, operationID, claimToken string, deadlineAt time.Time) error
	// RescheduleRetryable parks a claimed workflow for bounded automatic retry:
	// failed_retryable with next_retry_at, lease cleared.
	RescheduleRetryable(ctx context.Context, operationID, claimToken string, nextRetryAt time.Time, errorCode string) error
	// FailManual parks a claimed workflow in reconciliation: failed_manual,
	// lease cleared, no automatic retry.
	FailManual(ctx context.Context, operationID, claimToken, errorCode string) error
	// CompleteWorkflow terminally resolves a claimed workflow (succeeded or
	// rejected) with the safe result, clears the lease and stamps completed_at.
	CompleteWorkflow(ctx context.Context, operationID, claimToken string, state WorkflowState, result map[string]any) error
	// ExhaustToFailedManual transitions a budget/deadline-exhausted row that
	// no live lease protects to failed_manual (the automatic 10-attempt /
	// original retry_deadline_at ceiling, forward or compensation counter,
	// whichever came first). Any possibly-applied subject exclusion stays
	// active for reconciliation. It reports whether the transition happened:
	// false = not exhausted, live lease, or already terminal — the caller
	// re-reads the workflow to classify.
	ExhaustToFailedManual(ctx context.Context, operationID, errorCode string) (bool, error)
	// CompleteRecoveredWorkflow terminally resolves a failed_manual row
	// directly (no claim token exists there): manual recovery may only
	// finalize proven receipts or execute approved compensation — it never
	// resumes forward execution.
	CompleteRecoveredWorkflow(ctx context.Context, operationID string, state WorkflowState, result map[string]any) error

	// AppendRecoveryAction durably records one manual-recovery action before
	// it executes (append-only ledger; only insert/list ports exist).
	AppendRecoveryAction(ctx context.Context, action RecoveryAction) (RecoveryAction, error)
	// ListRecoveryActions reads the full immutable recovery ledger of one
	// workflow (audit + idempotency evidence).
	ListRecoveryActions(ctx context.Context, operationID string) ([]RecoveryAction, error)

	// InsertWorkflowSubject adds one active subject row (D2). A unique
	// violation — another active workflow covering the subject — surfaces as
	// ErrSubjectExcluded and rejects the whole batch.
	InsertWorkflowSubject(ctx context.Context, subject WorkflowSubject) error
	// UpdateSubjectResult persists the per-subject tombstone and releases the
	// exclusion (D5).
	UpdateSubjectResult(ctx context.Context, operationID string, subjectUserID, tombstoneVersion int64) error
	// ReleaseSubjectExclusion marks a subject row inactive without a tombstone
	// (convergence after compensation: rejected/OPERATION_EXPIRED paths).
	ReleaseSubjectExclusion(ctx context.Context, operationID string, subjectUserID int64) error
	// ListSubjectsByOperation reads all subject rows of one workflow.
	ListSubjectsByOperation(ctx context.Context, operationID string) ([]WorkflowSubject, error)

	// WorkflowEvidenceWatermark returns the Admin BFF cleanup watermark: how
	// many workflows still reference evidence (not terminally resolved, or
	// holding an active subject exclusion) and the oldest unresolved/all
	// workflow timestamps. The Platform coordinator refuses evidence cleanup
	// while UnresolvedCount > 0 and never purges receipts shorter than the
	// oldest referencing workflow.
	WorkflowEvidenceWatermark(ctx context.Context) (WorkflowEvidenceWatermark, error)
}
