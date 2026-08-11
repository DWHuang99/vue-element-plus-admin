// Admin BFF workflow-store adapter (T051).
//
// Implements adminbff.WorkflowStore against PostgreSQL using ONLY the
// BFF-owned generated sqlc package (boundary rule): no global/internal/
// database/sqlc, no IAM or Organization queries, and every transaction
// touches only admin_workflows/admin_workflow_subjects.
//
// CAS semantics (contracts/consistency-and-compensation.md): every mutation
// of a claimed workflow is guarded by (operation_id, state IN (running,
// compensating), claim_token) — a zero-row miss means the claim is stale
// (expired/reclaimed/terminal) and surfaces as adminbff.ErrStaleClaim; the
// service re-reads the workflow to decide. Subject inserts surface unique
// exclusion violations as adminbff.ErrSubjectExcluded. Absent workflow
// lookups return (nil, nil), never fabricated; malformed operation ids
// surface as adminbff.ErrInvalidOperationID.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/hdw/vue-element-plus-admin/backend/internal/adminbff"
	"github.com/hdw/vue-element-plus-admin/backend/internal/adminbff/postgres/sqlc"
)

// queries carries the query set shared by the pool-bound Store and the
// transaction-bound txStore.
type queries struct {
	q sqlc.Querier
}

// --- workflows ---------------------------------------------------------------

func (s *queries) CreateWorkflow(ctx context.Context, w adminbff.Workflow) error {
	operationID, err := uuidFromString(w.OperationID)
	if err != nil {
		return adminbff.ErrInvalidOperationID
	}
	return s.q.CreateWorkflow(ctx, sqlc.CreateWorkflowParams{
		OperationID:        operationID,
		OperationType:      w.OperationType,
		IdempotencyKey:     textFromPtr(w.IdempotencyKey),
		RequestFingerprint: w.RequestFingerprint,
		ActorUserID:        w.ActorUserID,
		RetryDeadlineAt:    timestamptz(w.RetryDeadlineAt),
	})
}

func (s *queries) GetWorkflowByOperationID(ctx context.Context, operationID string) (*adminbff.Workflow, error) {
	opID, err := uuidFromString(operationID)
	if err != nil {
		return nil, adminbff.ErrInvalidOperationID
	}
	row, err := s.q.GetWorkflowByOperationID(ctx, opID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	w := workflowRecord(row)
	return &w, nil
}

func (s *queries) GetWorkflowByIdempotencyKey(ctx context.Context, operationType string, actorUserID int64, idempotencyKey string) (*adminbff.Workflow, error) {
	row, err := s.q.GetWorkflowByIdempotencyKey(ctx, sqlc.GetWorkflowByIdempotencyKeyParams{
		OperationType:  operationType,
		ActorUserID:    actorUserID,
		IdempotencyKey: pgtype.Text{String: idempotencyKey, Valid: true},
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	w := workflowRecord(row)
	return &w, nil
}

// ClaimWorkflows runs the atomic claim scan with a fresh claim token; the
// returned workflows carry their new lease fields and attempt counts.
func (s *queries) ClaimWorkflows(ctx context.Context, leaseOwner string, leaseDuration time.Duration, limit int, now time.Time) ([]adminbff.Workflow, error) {
	rows, err := s.q.ClaimWorkflows(ctx, sqlc.ClaimWorkflowsParams{
		LeaseOwner:  pgtype.Text{String: leaseOwner, Valid: true},
		LeasedUntil: timestamptz(now.Add(leaseDuration)),
		ClaimToken:  pgtype.UUID{Bytes: uuid.New(), Valid: true},
		Limit:       int32(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]adminbff.Workflow, 0, len(rows))
	for _, r := range rows {
		out = append(out, workflowRecord(r))
	}
	return out, nil
}

// ClaimWorkflowByOperationID claims one specific workflow with the
// ClaimWorkflows predicates keyed by operation_id; zero rows (live lease,
// exhausted, past retry deadline, or absent) surface as ErrStaleClaim and
// the service re-reads the workflow to classify.
func (s *queries) ClaimWorkflowByOperationID(ctx context.Context, operationID, leaseOwner string, leaseDuration time.Duration, now time.Time) (*adminbff.Workflow, error) {
	opID, err := uuidFromString(operationID)
	if err != nil {
		return nil, adminbff.ErrInvalidOperationID
	}
	row, err := s.q.ClaimWorkflowByOperationID(ctx, sqlc.ClaimWorkflowByOperationIDParams{
		OperationID: opID,
		LeaseOwner:  pgtype.Text{String: leaseOwner, Valid: true},
		LeasedUntil: timestamptz(now.Add(leaseDuration)),
		ClaimToken:  pgtype.UUID{Bytes: uuid.New(), Valid: true},
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, adminbff.ErrStaleClaim
		}
		return nil, err
	}
	w := workflowRecord(row)
	return &w, nil
}

// ClaimAwaitingClientInput reacquires forward execution for a same-key
// authorized HTTP retry before the input deadline; zero rows (deadline
// passed, state moved on, or key mismatch) surface as ErrStaleClaim and the
// service re-reads the workflow to classify.
func (s *queries) ClaimAwaitingClientInput(ctx context.Context, operationID, idempotencyKey, leaseOwner string, leaseDuration time.Duration, now time.Time) (*adminbff.Workflow, error) {
	opID, err := uuidFromString(operationID)
	if err != nil {
		return nil, adminbff.ErrInvalidOperationID
	}
	row, err := s.q.ClaimAwaitingClientInput(ctx, sqlc.ClaimAwaitingClientInputParams{
		OperationID:    opID,
		IdempotencyKey: pgtype.Text{String: idempotencyKey, Valid: true},
		LeaseOwner:     pgtype.Text{String: leaseOwner, Valid: true},
		LeasedUntil:    timestamptz(now.Add(leaseDuration)),
		ClaimToken:     pgtype.UUID{Bytes: uuid.New(), Valid: true},
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, adminbff.ErrStaleClaim
		}
		return nil, err
	}
	w := workflowRecord(row)
	return &w, nil
}

func (s *queries) PersistStep(ctx context.Context, operationID, claimToken string, update adminbff.StepUpdate) error {
	opID, claim, err := claimIDs(operationID, claimToken)
	if err != nil {
		return err
	}
	return countToStale(s.q.PersistStep(ctx, sqlc.PersistStepParams{
		OperationID:               opID,
		ClaimToken:                claim,
		CurrentStep:               update.Step,
		SubjectUserID:             int8FromPtr(update.SubjectUserID),
		ExpectedIamVersion:        int8FromPtr(update.ExpectedIamVersion),
		ResultingIamVersion:       int8FromPtr(update.ResultingIamVersion),
		PreviousDepartmentID:      int8FromPtr(update.PreviousDepartmentID),
		PreviousMembershipVersion: int8FromPtr(update.PreviousMembershipVersion),
		AppliedDepartmentID:       int8FromPtr(update.AppliedDepartmentID),
		AppliedMembershipVersion:  int8FromPtr(update.AppliedMembershipVersion),
		CommandFingerprint:        textFromPtr(update.CommandFingerprint),
	}))
}

func (s *queries) BeginCompensation(ctx context.Context, operationID, claimToken string) error {
	opID, claim, err := claimIDs(operationID, claimToken)
	if err != nil {
		return err
	}
	return countToStale(s.q.BeginCompensation(ctx, sqlc.BeginCompensationParams{
		OperationID: opID,
		ClaimToken:  claim,
	}))
}

func (s *queries) SetCompensationSucceeded(ctx context.Context, operationID, claimToken string) error {
	opID, claim, err := claimIDs(operationID, claimToken)
	if err != nil {
		return err
	}
	return countToStale(s.q.SetCompensationSucceeded(ctx, sqlc.SetCompensationSucceededParams{
		OperationID: opID,
		ClaimToken:  claim,
	}))
}

func (s *queries) SetCompensationFailed(ctx context.Context, operationID, claimToken string) error {
	opID, claim, err := claimIDs(operationID, claimToken)
	if err != nil {
		return err
	}
	return countToStale(s.q.SetCompensationFailed(ctx, sqlc.SetCompensationFailedParams{
		OperationID: opID,
		ClaimToken:  claim,
	}))
}

func (s *queries) EnterAwaitingClientInput(ctx context.Context, operationID, claimToken string, deadlineAt time.Time) error {
	opID, claim, err := claimIDs(operationID, claimToken)
	if err != nil {
		return err
	}
	return countToStale(s.q.EnterAwaitingClientInput(ctx, sqlc.EnterAwaitingClientInputParams{
		OperationID:           opID,
		ClaimToken:            claim,
		ClientInputDeadlineAt: timestamptz(deadlineAt),
	}))
}

func (s *queries) RescheduleRetryable(ctx context.Context, operationID, claimToken string, nextRetryAt time.Time, errorCode string) error {
	opID, claim, err := claimIDs(operationID, claimToken)
	if err != nil {
		return err
	}
	return countToStale(s.q.RescheduleRetryable(ctx, sqlc.RescheduleRetryableParams{
		OperationID:   opID,
		ClaimToken:    claim,
		NextRetryAt:   timestamptz(nextRetryAt),
		LastErrorCode: pgtype.Text{String: errorCode, Valid: errorCode != ""},
	}))
}

func (s *queries) FailManual(ctx context.Context, operationID, claimToken, errorCode string) error {
	opID, claim, err := claimIDs(operationID, claimToken)
	if err != nil {
		return err
	}
	return countToStale(s.q.FailManual(ctx, sqlc.FailManualParams{
		OperationID:   opID,
		ClaimToken:    claim,
		LastErrorCode: pgtype.Text{String: errorCode, Valid: errorCode != ""},
	}))
}

// CompleteWorkflow terminally resolves a claimed workflow; the safe result is
// marshalled to JSONB (nil result stores NULL). Zero rows are a stale claim.
func (s *queries) CompleteWorkflow(ctx context.Context, operationID, claimToken string, state adminbff.WorkflowState, result map[string]any) error {
	opID, claim, err := claimIDs(operationID, claimToken)
	if err != nil {
		return err
	}
	var resultJSON []byte
	if result != nil {
		b, err := json.Marshal(result)
		if err != nil {
			return err
		}
		resultJSON = b
	}
	return countToStale(s.q.CompleteWorkflow(ctx, sqlc.CompleteWorkflowParams{
		OperationID: opID,
		ClaimToken:  claim,
		State:       string(state),
		Result:      resultJSON,
	}))
}

// ExhaustToFailedManual transitions a budget/deadline-exhausted row WITHOUT a
// live lease to failed_manual. The bool reports whether the transition
// happened: false = not exhausted, live lease, or already terminal — a normal
// outcome, NOT a stale claim (the caller re-reads the workflow to classify).
func (s *queries) ExhaustToFailedManual(ctx context.Context, operationID, errorCode string) (bool, error) {
	opID, err := uuidFromString(operationID)
	if err != nil {
		return false, adminbff.ErrInvalidOperationID
	}
	count, err := s.q.ExhaustToFailedManual(ctx, sqlc.ExhaustToFailedManualParams{
		OperationID:   opID,
		LastErrorCode: pgtype.Text{String: errorCode, Valid: errorCode != ""},
	})
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// CompleteRecoveredWorkflow terminally resolves a failed_manual row directly
// (no claim token exists there). Zero rows = not failed_manual or already
// terminal — a stale recovery — surfaced as ErrStaleClaim.
func (s *queries) CompleteRecoveredWorkflow(ctx context.Context, operationID string, state adminbff.WorkflowState, result map[string]any) error {
	opID, err := uuidFromString(operationID)
	if err != nil {
		return adminbff.ErrInvalidOperationID
	}
	var resultJSON []byte
	if result != nil {
		b, err := json.Marshal(result)
		if err != nil {
			return err
		}
		resultJSON = b
	}
	return countToStale(s.q.CompleteRecoveredWorkflow(ctx, sqlc.CompleteRecoveredWorkflowParams{
		OperationID: opID,
		State:       string(state),
		Result:      resultJSON,
	}))
}

// --- recovery ledger ---------------------------------------------------------

// AppendRecoveryAction durably records one manual-recovery action before it
// executes. The action id is generated here when the caller did not supply one
// (service callers usually pre-generate for cross-request idempotency); the
// returned row carries the server-stamped created_at. The ledger is
// append-only: no update/delete ports exist by construction.
func (s *queries) AppendRecoveryAction(ctx context.Context, action adminbff.RecoveryAction) (adminbff.RecoveryAction, error) {
	opID, err := uuidFromString(action.OperationID)
	if err != nil {
		return adminbff.RecoveryAction{}, adminbff.ErrInvalidOperationID
	}
	actionID := action.ActionID
	if actionID == "" {
		actionID = uuid.NewString()
	}
	row, err := s.q.InsertRecoveryAction(ctx, sqlc.InsertRecoveryActionParams{
		ActionID:            pgtype.UUID{Bytes: uuid.MustParse(actionID), Valid: true},
		OperationID:         opID,
		RecoveryPrincipalID: action.RecoveryPrincipalID,
		AuthorizationSource: action.AuthorizationSource,
		ApprovalID:          textFromPtr(action.ApprovalID),
		ActionType:          string(action.ActionType),
		ReasonCode:          action.ReasonCode,
		PreviousState:       string(action.PreviousState),
		ResultingState:      string(action.ResultingState),
		SafeResultCode:      textFromPtr(action.SafeResultCode),
		RequestID:           textFromPtr(action.RequestID),
		CorrelationID:       textFromPtr(action.CorrelationID),
	})
	if err != nil {
		return adminbff.RecoveryAction{}, err
	}
	return recoveryActionRecord(row), nil
}

func (s *queries) ListRecoveryActions(ctx context.Context, operationID string) ([]adminbff.RecoveryAction, error) {
	opID, err := uuidFromString(operationID)
	if err != nil {
		return nil, adminbff.ErrInvalidOperationID
	}
	rows, err := s.q.ListRecoveryActionsByOperation(ctx, opID)
	if err != nil {
		return nil, err
	}
	out := make([]adminbff.RecoveryAction, 0, len(rows))
	for _, r := range rows {
		out = append(out, recoveryActionRecord(r))
	}
	return out, nil
}

// --- subjects ----------------------------------------------------------------

// InsertWorkflowSubject adds one active subject row. Re-entry of the SAME
// workflow is idempotent (the ON CONFLICT DO UPDATE narrows the partial-index
// conflict to the same operation_id and returns the row); a zero-row result —
// another active workflow covering the subject — surfaces as
// ErrSubjectExcluded. Unique violations (defensive; the PK path is subsumed by
// the idempotent branch) map the same way.
func (s *queries) InsertWorkflowSubject(ctx context.Context, subject adminbff.WorkflowSubject) error {
	opID, err := uuidFromString(subject.OperationID)
	if err != nil {
		return adminbff.ErrInvalidOperationID
	}
	_, err = s.q.InsertWorkflowSubject(ctx, sqlc.InsertWorkflowSubjectParams{
		OperationID:        opID,
		SubjectUserID:      subject.SubjectUserID,
		ExpectedIamVersion: subject.ExpectedIamVersion,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || isUniqueViolation(err) {
			return adminbff.ErrSubjectExcluded
		}
		return err
	}
	return nil
}

func (s *queries) UpdateSubjectResult(ctx context.Context, operationID string, subjectUserID, tombstoneVersion int64) error {
	opID, err := uuidFromString(operationID)
	if err != nil {
		return adminbff.ErrInvalidOperationID
	}
	return s.q.UpdateSubjectResult(ctx, sqlc.UpdateSubjectResultParams{
		OperationID:               opID,
		SubjectUserID:             subjectUserID,
		ResultingTombstoneVersion: pgtype.Int8{Int64: tombstoneVersion, Valid: true},
	})
}

func (s *queries) ReleaseSubjectExclusion(ctx context.Context, operationID string, subjectUserID int64) error {
	opID, err := uuidFromString(operationID)
	if err != nil {
		return adminbff.ErrInvalidOperationID
	}
	return s.q.ReleaseSubjectExclusion(ctx, sqlc.ReleaseSubjectExclusionParams{
		OperationID:   opID,
		SubjectUserID: subjectUserID,
	})
}

func (s *queries) ListSubjectsByOperation(ctx context.Context, operationID string) ([]adminbff.WorkflowSubject, error) {
	opID, err := uuidFromString(operationID)
	if err != nil {
		return nil, adminbff.ErrInvalidOperationID
	}
	rows, err := s.q.ListSubjectsByOperation(ctx, opID)
	if err != nil {
		return nil, err
	}
	out := make([]adminbff.WorkflowSubject, 0, len(rows))
	for _, r := range rows {
		out = append(out, adminbff.WorkflowSubject{
			OperationID:               uuidString(r.OperationID),
			SubjectUserID:             r.SubjectUserID,
			ExpectedIamVersion:        r.ExpectedIamVersion,
			ResultingTombstoneVersion: nullableInt64(r.ResultingTombstoneVersion),
			Active:                    r.Active,
		})
	}
	return out, nil
}

// WorkflowEvidenceWatermark exposes the BFF cleanup watermark to the Platform
// coordinator (T078). A workflow is unresolved/referencing while it is not
// terminally resolved or still holds an active subject exclusion; receipts are
// never purged shorter than the oldest referencing workflow.
func (s *queries) WorkflowEvidenceWatermark(ctx context.Context) (adminbff.WorkflowEvidenceWatermark, error) {
	row, err := s.q.WorkflowCleanupWatermark(ctx)
	if err != nil {
		return adminbff.WorkflowEvidenceWatermark{}, err
	}
	return adminbff.WorkflowEvidenceWatermark{
		UnresolvedCount:    row.UnresolvedCount,
		OldestUnresolvedAt: nullableTime(row.OldestUnresolvedAt),
		OldestWorkflowAt:   nullableTime(row.OldestWorkflowAt),
	}, nil
}

// --- mapping helpers ---------------------------------------------------------

// claimIDs parses the CAS identity pair; either failure is an invalid id.
func claimIDs(operationID, claimToken string) (pgtype.UUID, pgtype.UUID, error) {
	opID, err := uuidFromString(operationID)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, adminbff.ErrInvalidOperationID
	}
	claim, err := uuidFromString(claimToken)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, adminbff.ErrInvalidOperationID
	}
	return opID, claim, nil
}

// countToStale maps a CAS statement's affected-row count to ErrStaleClaim
// when nothing was updated (claim expired/reclaimed/terminal).
func countToStale(count int64, err error) error {
	if err != nil {
		return err
	}
	if count == 0 {
		return adminbff.ErrStaleClaim
	}
	return nil
}

func workflowRecord(r sqlc.AdminWorkflow) adminbff.Workflow {
	return adminbff.Workflow{
		OperationID:               uuidString(r.OperationID),
		OperationType:             r.OperationType,
		IdempotencyKey:            nullableText(r.IdempotencyKey),
		RequestFingerprint:        r.RequestFingerprint,
		ActorUserID:               r.ActorUserID,
		SubjectUserID:             nullableInt64(r.SubjectUserID),
		State:                     adminbff.WorkflowState(r.State),
		CurrentStep:               r.CurrentStep,
		CompensationState:         adminbff.CompensationState(r.CompensationState),
		ExpectedIamVersion:        nullableInt64(r.ExpectedIamVersion),
		ResultingIamVersion:       nullableInt64(r.ResultingIamVersion),
		PreviousDepartmentID:      nullableInt64(r.PreviousDepartmentID),
		PreviousMembershipVersion: nullableInt64(r.PreviousMembershipVersion),
		AppliedDepartmentID:       nullableInt64(r.AppliedDepartmentID),
		AppliedMembershipVersion:  nullableInt64(r.AppliedMembershipVersion),
		CommandFingerprint:        nullableText(r.CommandFingerprint),
		Result:                    resultMap(r.Result),
		LastErrorCode:             nullableText(r.LastErrorCode),
		CreatedAt:                 r.CreatedAt.Time,
		UpdatedAt:                 r.UpdatedAt.Time,
		AttemptCount:              int(r.AttemptCount),
		CompensationAttemptCount:  int(r.CompensationAttemptCount),
		NextRetryAt:               nullableTime(r.NextRetryAt),
		ClientInputDeadlineAt:     nullableTime(r.ClientInputDeadlineAt),
		RetryDeadlineAt:           r.RetryDeadlineAt.Time,
		LeaseOwner:                nullableText(r.LeaseOwner),
		LeasedUntil:               nullableTime(r.LeasedUntil),
		ClaimToken:                nullableUUID(r.ClaimToken),
		CompletedAt:               nullableTime(r.CompletedAt),
	}
}

func uuidFromString(s string) (pgtype.UUID, error) {
	u, err := uuid.Parse(s)
	if err != nil {
		return pgtype.UUID{}, err
	}
	return pgtype.UUID{Bytes: u, Valid: true}, nil
}

func uuidString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return uuid.UUID(u.Bytes).String()
}

func textFromPtr(v *string) pgtype.Text {
	if v == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *v, Valid: true}
}

func nullableText(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

func int8FromPtr(v *int64) pgtype.Int8 {
	if v == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *v, Valid: true}
}

func nullableInt64(v pgtype.Int8) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}

func timestamptz(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

func nullableTime(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	return &t.Time
}

func nullableUUID(u pgtype.UUID) *string {
	if !u.Valid {
		return nil
	}
	s := uuid.UUID(u.Bytes).String()
	return &s
}

// recoveryActionRecord maps one immutable ledger row (all fields NOT NULL
// except the three optional audit references).
func recoveryActionRecord(r sqlc.AdminWorkflowRecoveryAction) adminbff.RecoveryAction {
	return adminbff.RecoveryAction{
		ActionID:            uuidString(r.ActionID),
		OperationID:         uuidString(r.OperationID),
		RecoveryPrincipalID: r.RecoveryPrincipalID,
		AuthorizationSource: r.AuthorizationSource,
		ApprovalID:          nullableText(r.ApprovalID),
		ActionType:          adminbff.RecoveryActionType(r.ActionType),
		ReasonCode:          r.ReasonCode,
		PreviousState:       adminbff.WorkflowState(r.PreviousState),
		ResultingState:      adminbff.WorkflowState(r.ResultingState),
		SafeResultCode:      nullableText(r.SafeResultCode),
		RequestID:           nullableText(r.RequestID),
		CorrelationID:       nullableText(r.CorrelationID),
		CreatedAt:           r.CreatedAt.Time,
	}
}

func resultMap(raw []byte) map[string]any {
	if raw == nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
