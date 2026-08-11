// US3 managed-user sagas (T052–T054; contracts/consistency-and-compensation.md
// C0–C8 / U0–U7 / D0–D5). The BFF orchestrates the participants — IAM
// (provision/activate/update/delete, compensate), Organization (membership)
// and the workflow store (durable saga state) — so each public write call is a
// single idempotent workflow that always reaches a terminal state.
//
// The machine is resolve-first: participants store a command receipt
// atomically with each side effect, so after a timeout/crash the BFF
// resolves the receipt instead of guessing — committed receipts continue,
// proven non-commits compensate, and only genuinely unresolved outcomes
// park the workflow for a same-key retry or the resume machine.
package adminbff

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/hdw/vue-element-plus-admin/backend/internal/consistency"
	"github.com/hdw/vue-element-plus-admin/backend/internal/iam"
	"github.com/hdw/vue-element-plus-admin/backend/internal/organization"
)

// PermissionUsersWrite is the code the service re-checks immediately before
// every new forward participant side effect (reauthorization; a revocation
// mid-saga reverses the applied side effects). The transport middleware
// checks the same code once at the route boundary.
const PermissionUsersWrite = "users.write"

// Operation types and durable steps (admin_workflows.operation_type /
// current_step). One logical workflow per (operation_type, actor, key).
const (
	OperationTypeCreateUser = "managed_user.create"
	OperationTypeUpdateUser = "managed_user.update"
	OperationTypeDeleteUser = "managed_user.delete"

	StepCreated              = "created"               // C0/U0/D0: no participant side effect yet
	StepIamProvisioned       = "iam_provisioned"       // C4: C3 committed + subject persisted
	StepOrganizationAssigned = "organization_assigned" // C6: C5 committed + membership persisted
	StepDeleteValidated      = "delete_validated"      // D2: subject rows + expected versions durable; D3–D5 re-issuable

	// Update steps (U1/U3/U5 boundaries). Only the same-key client retry can
	// carry the saga past iam_validated — the request fields and any credential
	// are never stored, so every park before U6 is awaiting_client_input.
	StepIamValidated        = "iam_validated"        // U1+U2: user/roles/department validated; subject + expected version + planned department persisted
	StepOrganizationRead    = "organization_read"    // U3: previous department/version (or expected absence) persisted
	StepOrganizationUpdated = "organization_updated" // U5: applied membership version + U6 command fingerprint persisted
)

// Participant command names — the canonical strings the IAM/Organization
// services fingerprint with; ResolveCommand requires an exact match.
const (
	commandCreateProvisioningUser = "create_provisioning_user"
	commandSetUserDepartment      = "set_user_department"
	commandActivateUser           = "activate_user"
	commandRestoreUserDepartment  = "restore_user_department"
	commandCompensateProvisioning = "compensate_provisioning_user"

	commandUpdateManagedUser   = "update_managed_user"
	commandClearUserDepartment = "clear_user_department"
	commandDeleteUsers         = "delete_users"
)

const (
	// leaseDuration is the workflow lease granted to the claim holder.
	leaseDuration = 30 * time.Second
	// retryDeadlineTTL is the immutable forward-execution window measured
	// from creation (default creation + 24h).
	retryDeadlineTTL = 24 * time.Hour
	// clientInputTTL is the immutable awaiting_client_input deadline measured
	// from creation (default creation + 24h); waiting consumes no attempts.
	clientInputTTL = 24 * time.Hour
	// maxAttempts is the automatic-execution ceiling: 10 forward attempts and
	// 10 compensation reclaims, each counted separately, both capped by the
	// original retry_deadline_at (contracts/consistency-and-compensation.md
	// line 75). Exhaustion transitions to failed_manual.
	maxAttempts = 10
	// workerOwner is the lease owner the resume machine claims with.
	workerOwner = "worker"
)

// OperationContext is the BFF-level workflow execution context carried into
// the participant OperationContexts. CorrelationID comes from the inbound
// request; the saga never derives it from credentials or tokens.
type OperationContext struct {
	OperationID    string
	IdempotencyKey *string
	ActorUserID    int64
	CorrelationID  string
}

// CreateUserParams is the create-user request as validated by the transport.
// Password is credential material: it lives only in this request — never in a
// fingerprint, workflow result or log (consistency.FingerprintV1 rejects
// credential-like keys loudly).
type CreateUserParams struct {
	Username     string
	Account      *string
	Email        *string
	Password     string
	RoleIDs      []int64
	DepartmentID *int64 // nil = no department (valid)
}

// CreateUserResult is the create outcome. The public API stays
// 200 {"data":{}} (http-api-compatibility.md); the user id is internal —
// carried in the workflow result for replay.
type CreateUserResult struct {
	UserID int64
}

// UpdateUserParams is the update-user request as validated by the transport.
// Password is credential material: it lives only in this request — never in a
// fingerprint, workflow result or log (consistency.FingerprintV1 rejects
// credential-like keys loudly). Password == "" means no credential change.
type UpdateUserParams struct {
	UserID       int64
	Username     string
	Account      *string
	Email        *string
	Password     string
	RoleIDs      []int64
	DepartmentID *int64 // nil = clear the department (valid)
}

// UpdateUserResult is the update outcome. The public API stays
// 200 {"data":{}}; a succeeded workflow carries no internal result payload
// (the transport owns the response body).
type UpdateUserResult struct{}

// DeleteUserParams is the delete-user request as validated by the transport.
// The saga normalizes the target set (dedupe + sort ascending) before
// fingerprinting, so the request identity is order- and duplication-insensitive.
type DeleteUserParams struct {
	UserIDs []int64
}

// DeleteUserResult is the delete outcome. The public API stays
// 200 {"data":{}}; a succeeded workflow carries no result payload — the
// per-subject tombstone versions live in admin_workflow_subjects (D5).
type DeleteUserResult struct{}

// ResumeResult is the outcome of one resume-machine run (worker entry).
type ResumeResult struct {
	OperationID string
	Terminal    bool
	State       WorkflowState
	Error       error
}

// RecoverParams is the least-privilege manual-recovery request (T055;
// contracts/consistency-and-compensation.md Reconciliation). Authorization is
// users.write — the same principal that created the workflow — and every call
// is durably recorded in the append-only ledger BEFORE it executes. Recovery
// never resumes a revoked/failed actor's forward request: it may only
// finalize proven receipts (receipt_finalize), execute explicitly approved
// compensation (approved_compensation), or record a reconciliation decision
// (reconciliation).
type RecoverParams struct {
	OperationID string
	ActionType  RecoveryActionType
	ReasonCode  string
	ApprovalID  *string
}

// RecoverResult is the recovery outcome; the recorded ledger row is always
// returned (the append-only ledger faithfully records every attempt, success
// or failure).
type RecoverResult struct {
	OperationID string
	Terminal    bool
	State       WorkflowState
	Action      RecoveryAction
}

// --- fingerprints -------------------------------------------------------------

// createRequestFingerprint is the idempotency discriminator for the whole
// request (department included — the same key with a different department is
// a different operation). The password never enters it, so a same-key retry
// with a corrected credential keeps the same request identity.
func createRequestFingerprint(actorUserID int64, p CreateUserParams) (string, error) {
	return consistency.FingerprintV1(OperationTypeCreateUser, actorUserID, map[string]any{
		"username":                  p.Username,
		"account":                   consistency.OptString(p.Account),
		"email":                     consistency.OptString(p.Email),
		"role_ids":                  consistency.SortedIDs(p.RoleIDs),
		"department_id":             consistency.OptInt(p.DepartmentID),
		"password_change_requested": true,
	})
}

// createCommandFingerprint is the C3 participant fingerprint (IAM knows
// nothing about departments) — the expected fingerprint for ResolveCommand.
func createCommandFingerprint(actorUserID int64, p CreateUserParams) (string, error) {
	return consistency.FingerprintV1(commandCreateProvisioningUser, actorUserID, map[string]any{
		"username":                  p.Username,
		"account":                   consistency.OptString(p.Account),
		"email":                     consistency.OptString(p.Email),
		"role_ids":                  consistency.SortedIDs(p.RoleIDs),
		"password_change_requested": true,
	})
}

// setMembershipFingerprint is the C5/U4(set) participant fingerprint —
// rebuilt at resume from the workflow's persisted applied_department_id and
// previous_membership_version. Create passes expected=nil (a fresh user has
// no membership); update passes the U3 read's version so the U4 re-issue is
// replay-or-CAS stable.
func setMembershipFingerprint(actorUserID, userID int64, departmentID *int64, expected *int64) (string, error) {
	return consistency.FingerprintV1(commandSetUserDepartment, actorUserID, map[string]any{
		"user_id":                     userID,
		"department_id":               consistency.OptInt(departmentID),
		"expected_membership_version": consistency.OptInt(expected),
	})
}

// clearMembershipFingerprint is the U4(clear) participant fingerprint.
func clearMembershipFingerprint(actorUserID, userID int64, expected *int64) (string, error) {
	return consistency.FingerprintV1(commandClearUserDepartment, actorUserID, map[string]any{
		"user_id":                     userID,
		"expected_membership_version": consistency.OptInt(expected),
	})
}

// activateFingerprint is the C7 participant fingerprint.
// compensateProvisioningFingerprint rebuilds the compensate_provisioning_user
// fingerprint from workflow-persisted fields only (resume/recovery
// resolve-first — the request itself is never stored).
func compensateProvisioningFingerprint(actorUserID, userID, expectedVersion int64) (string, error) {
	return consistency.FingerprintV1(commandCompensateProvisioning, actorUserID, map[string]any{
		"user_id":          userID,
		"expected_version": expectedVersion,
	})
}

// restoreMembershipFingerprint rebuilds the restore_user_department
// fingerprint from workflow-persisted fields only (resume/recovery
// resolve-first — the request itself is never stored).
func restoreMembershipFingerprint(actorUserID, userID, appliedVersion int64, previousDepartmentID, previousMembershipVersion *int64) (string, error) {
	return consistency.FingerprintV1(commandRestoreUserDepartment, actorUserID, map[string]any{
		"user_id":                             userID,
		"expected_current_membership_version": appliedVersion,
		"previous_department_id":              consistency.OptInt(previousDepartmentID),
		"previous_membership_version":         consistency.OptInt(previousMembershipVersion),
	})
}

func activateFingerprint(actorUserID, userID, expectedVersion int64) (string, error) {
	return consistency.FingerprintV1(commandActivateUser, actorUserID, map[string]any{
		"user_id":          userID,
		"expected_version": expectedVersion,
	})
}

// updateRequestFingerprint is the U0 idempotency discriminator for the whole
// request (department included). The password never enters it, so a same-key
// retry with a corrected credential keeps the same request identity;
// password_change_requested is its only trace.
func updateRequestFingerprint(actorUserID int64, p UpdateUserParams) (string, error) {
	return consistency.FingerprintV1(OperationTypeUpdateUser, actorUserID, map[string]any{
		"user_id":                   p.UserID,
		"username":                  p.Username,
		"account":                   consistency.OptString(p.Account),
		"email":                     consistency.OptString(p.Email),
		"role_ids":                  consistency.SortedIDs(p.RoleIDs),
		"department_id":             consistency.OptInt(p.DepartmentID),
		"password_change_requested": p.Password != "",
	})
}

// updateCommandFingerprint is the U6 participant fingerprint (IAM's own
// update_managed_user digest) — persisted at the U5 boundary so the worker
// resume machine can resolve-only without any request fields.
func updateCommandFingerprint(actorUserID, userID, expectedVersion int64, p UpdateUserParams) (string, error) {
	return consistency.FingerprintV1(commandUpdateManagedUser, actorUserID, map[string]any{
		"user_id":                   userID,
		"expected_version":          expectedVersion,
		"username":                  p.Username,
		"account":                   consistency.OptString(p.Account),
		"email":                     consistency.OptString(p.Email),
		"role_ids":                  consistency.SortedIDs(p.RoleIDs),
		"password_change_requested": p.Password != "",
	})
}

// deleteRequestFingerprint is the D0 idempotency discriminator: the
// normalized target set (dedupe + sorted ascending). No versions appear at D0
// — they are discovered by the D1 preflight and persisted per-subject at D2.
func deleteRequestFingerprint(actorUserID int64, userIDs []int64) (string, error) {
	return consistency.FingerprintV1(OperationTypeDeleteUser, actorUserID, map[string]any{
		"user_ids": consistency.SortedIDs(userIDs),
	})
}

// deleteCommandFingerprint is the D3 participant fingerprint — the same
// normalized targets the IAM DeleteUsers fingerprints with. The BFF rebuilds
// it to resolve the batch receipt after a timeout.
func deleteCommandFingerprint(actorUserID int64, targets []iam.DeleteTarget) (string, error) {
	args := make([]any, 0, len(targets))
	for _, t := range targets {
		args = append(args, map[string]any{
			"user_id":          t.UserID,
			"expected_version": t.ExpectedVersion,
		})
	}
	return consistency.FingerprintV1(commandDeleteUsers, actorUserID, map[string]any{"targets": args})
}

// --- CreateUser: C0–C8 --------------------------------------------------------

// CreateUser runs the create saga (C0–C8). Idempotency: the scope
// (operation_type, actor, key) resolves to at most one logical workflow; a
// same-key replay returns the stored outcome without re-running side
// effects, and a same-key retry with a corrected password continues a parked
// workflow before its input deadline.
func (s *Service) CreateUser(ctx context.Context, op OperationContext, p CreateUserParams) (CreateUserResult, error) {
	fp, err := createRequestFingerprint(op.ActorUserID, p)
	if err != nil {
		return CreateUserResult{}, ErrInternal
	}

	// Idempotency resolution first: the same key may replay or continue.
	if op.IdempotencyKey != nil {
		existing, err := s.work.GetWorkflowByIdempotencyKey(ctx, OperationTypeCreateUser, op.ActorUserID, *op.IdempotencyKey)
		if err != nil {
			return CreateUserResult{}, err
		}
		if existing != nil {
			return s.replayOrContinue(ctx, op, p, fp, *existing)
		}
	}

	// C0: persist the pending workflow before any participant side effect.
	op.OperationID = uuid.NewString()
	if err := s.work.CreateWorkflow(ctx, Workflow{
		OperationID:        op.OperationID,
		OperationType:      OperationTypeCreateUser,
		IdempotencyKey:     op.IdempotencyKey,
		RequestFingerprint: fp,
		ActorUserID:        op.ActorUserID,
		State:              WorkflowPending,
		CurrentStep:        StepCreated,
		RetryDeadlineAt:    time.Now().Add(retryDeadlineTTL),
	}); err != nil {
		return CreateUserResult{}, err
	}

	// Claim our own row — keyed, because a limit=1 batch scan could steal an
	// older workflow. A fresh pending row has no contention.
	claimed, err := s.work.ClaimWorkflowByOperationID(ctx, op.OperationID, leaseOwner(op.ActorUserID), leaseDuration, time.Now())
	if err != nil {
		return CreateUserResult{}, s.classifyMiss(ctx, op.OperationID, err)
	}
	return s.runCreateForward(ctx, op, p, *claimed.ClaimToken)
}

// replayOrContinue classifies the workflow already scoped to this key.
func (s *Service) replayOrContinue(ctx context.Context, op OperationContext, p CreateUserParams, fp string, existing Workflow) (CreateUserResult, error) {
	switch existing.State {
	case WorkflowSucceeded:
		if existing.RequestFingerprint != fp {
			return CreateUserResult{}, ErrIdempotencyConflict
		}
		return replayCreateResult(existing), nil
	case WorkflowRejected:
		if existing.RequestFingerprint != fp {
			return CreateUserResult{}, ErrIdempotencyConflict
		}
		return CreateUserResult{}, replayError(existing)
	case WorkflowAwaitingClientInput:
		return s.retryAwaiting(ctx, op, p, fp, existing)
	case WorkflowFailedManual:
		return CreateUserResult{}, ErrReconciliationRequired
	case WorkflowFailedRetryable:
		// A worker retry owns this workflow; the client backs off and
		// re-checks (the transport maps Retry-After).
		return CreateUserResult{}, ErrWorkflowRetryable.WithRetryAfter(5)
	default: // pending / running / compensating — an in-flight claim owns it
		return CreateUserResult{}, ErrOperationInProgress.WithRetryAfter(1)
	}
}

// retryAwaiting continues a parked workflow on a same-key authorized retry
// before the input deadline (ClaimAwaitingClientInput). The password is
// excluded from the fingerprint, so a corrected credential keeps the same
// request identity; the C3 re-attempt replays a committed receipt or records
// the fresh one under the same operation ID (participant resolve-first).
func (s *Service) retryAwaiting(ctx context.Context, op OperationContext, p CreateUserParams, fp string, existing Workflow) (CreateUserResult, error) {
	if existing.RequestFingerprint != fp {
		return CreateUserResult{}, ErrIdempotencyConflict
	}
	if existing.ClientInputDeadlineAt == nil || !existing.ClientInputDeadlineAt.After(time.Now()) {
		return CreateUserResult{}, ErrOperationExpired
	}
	claimed, err := s.work.ClaimAwaitingClientInput(ctx, existing.OperationID, *op.IdempotencyKey,
		leaseOwner(op.ActorUserID), leaseDuration, time.Now())
	if err != nil {
		if errors.Is(err, ErrStaleClaim) {
			// Deadline passed or the state moved on between read and claim:
			// re-classify against the fresh row.
			w, rerr := s.work.GetWorkflowByOperationID(ctx, existing.OperationID)
			if rerr != nil {
				return CreateUserResult{}, rerr
			}
			if w == nil {
				return CreateUserResult{}, ErrWorkflowNotFound
			}
			return s.replayOrContinue(ctx, op, p, fp, *w)
		}
		return CreateUserResult{}, err
	}
	// The parked workflow keeps its operation identity: the forward machine
	// continues under the stored operation ID so the C3 retry's receipts and
	// every step persistence stay scoped to the original workflow.
	op.OperationID = existing.OperationID
	// Re-run the forward machine from the parked C3: preflight is re-validated
	// (roles/departments may have changed) and C3 continues resolve-first.
	return s.runCreateForward(ctx, op, p, *claimed.ClaimToken)
}

// runCreateForward executes the forward saga from the claimed workflow's
// durable step, starting at the C1 preflight. Called from the HTTP path
// (fresh C0 or same-key retry); the resume machine re-enters at C5/C7 with
// the persisted fields.
func (s *Service) runCreateForward(ctx context.Context, op OperationContext, p CreateUserParams, claimToken string) (CreateUserResult, error) {
	// C1: the role set must still be valid (side-effect free).
	if _, err := s.iam.Roles.ValidateRoleIDs(ctx, p.RoleIDs); err != nil {
		bffErr := mapParticipantError(err)
		return CreateUserResult{}, s.reject(ctx, op.OperationID, claimToken, bffErr)
	}
	// C2: the department must still exist (nil is valid).
	if _, err := s.org.Departments.ValidateDepartment(ctx, p.DepartmentID); err != nil {
		bffErr := mapParticipantError(err)
		return CreateUserResult{}, s.reject(ctx, op.OperationID, claimToken, bffErr)
	}
	// Reauthorization immediately before the first forward side effect.
	if ok, err := s.requireUsersWrite(ctx, op.ActorUserID); err != nil {
		return CreateUserResult{}, mapParticipantError(err)
	} else if !ok {
		return CreateUserResult{}, s.reject(ctx, op.OperationID, claimToken, ErrForbidden)
	}

	// C3: provision the user. The password lives only in this request.
	createResult, err := s.iam.Managed.CreateProvisioningUser(ctx, iamOperationContext(op), p.Username, p.Account, p.Email, p.Password, p.RoleIDs)
	if err != nil {
		if isUnknownOutcome(err) {
			// The commit may or may not have happened: resolve the receipt.
			createFp, ferr := createCommandFingerprint(op.ActorUserID, p)
			if ferr != nil {
				return CreateUserResult{}, ErrInternal
			}
			committed, rerr := s.iam.Receipts.ResolveCommand(ctx, op.OperationID, commandCreateProvisioningUser, createFp)
			if rerr == nil && committed != nil {
				createResult, ferr = createResultFromReceipt(*committed)
				if ferr != nil {
					return CreateUserResult{}, ErrInternal
				}
			} else {
				// Credential not durably committed (proven or unknown) and the
				// request is ending: park awaiting input. A same-key retry
				// re-runs C3 resolve-first; the expiry finalizer rejects the
				// parked row at the deadline (a C3 that committed but could
				// not be proven here is resolved by that same-key retry; an
				// orphaned provisioning user cannot authenticate and is
				// bounded to the 24h input window).
				if rerr != nil {
					return CreateUserResult{}, s.park(ctx, op.OperationID, claimToken, mapParticipantError(rerr))
				}
				return CreateUserResult{}, s.park(ctx, op.OperationID, claimToken, ErrDependencyTimeout)
			}
		} else {
			// Proven rejection: no receipt was stored, so no side effect
			// exists (receipts are atomic with their side effect).
			bffErr := mapParticipantError(err)
			return CreateUserResult{}, s.reject(ctx, op.OperationID, claimToken, bffErr)
		}
	}

	// C4: persist subject + expected version + the planned department. The
	// department is persisted BEFORE C5 so a resumed owner rebuilds the C5
	// fingerprint from applied_department_id.
	if err := s.work.PersistStep(ctx, op.OperationID, claimToken, StepUpdate{
		Step:                StepIamProvisioned,
		SubjectUserID:       &createResult.UserID,
		ExpectedIamVersion:  &createResult.ResultingVersion,
		AppliedDepartmentID: p.DepartmentID,
	}); err != nil {
		return CreateUserResult{}, s.classifyMiss(ctx, op.OperationID, err)
	}

	// C5: apply the membership (a fresh user has no membership: expected nil).
	membershipAttempt := s.applyMembership(ctx, op, createResult.UserID, p.DepartmentID)
	if membershipAttempt.err != nil {
		if !membershipAttempt.resolved {
			// Outcome unresolved while the request is alive: leave the
			// workflow running for the resume machine (participant
			// resolve-first replays a committed C5); the client retries.
			return CreateUserResult{}, membershipAttempt.err
		}
		// Proven C5 failure (or reauthorization revocation): no membership
		// side effect exists; reverse the provisioning user and reject.
		return s.compensateProvisioningOnly(ctx, op, claimToken, createResult.UserID, createResult.ResultingVersion, membershipAttempt.err)
	}

	// C6: persist the applied membership version (resume rebuilds the C7
	// fingerprint from subject + expected version; the restore CAS uses the
	// applied version).
	if err := s.work.PersistStep(ctx, op.OperationID, claimToken, StepUpdate{
		Step:                     StepOrganizationAssigned,
		AppliedMembershipVersion: versionPtr(membershipAttempt.result),
	}); err != nil {
		return CreateUserResult{}, s.classifyMiss(ctx, op.OperationID, err)
	}

	// C7: activate the provisioning user (expected-version CAS).
	activation := s.activateUser(ctx, op, createResult.UserID, createResult.ResultingVersion)
	if activation.err != nil {
		if !activation.resolved {
			return CreateUserResult{}, activation.err // leave running for the resume machine
		}
		// Proven C7 failure: reverse the applied membership (version CAS)
		// and the provisioning user, then reject.
		return s.compensateApplied(ctx, op, claimToken, createResult.UserID, createResult.ResultingVersion, versionPtr(membershipAttempt.result), activation.err)
	}

	// C8: complete succeeded with the safe result.
	if err := s.work.CompleteWorkflow(ctx, op.OperationID, claimToken, WorkflowSucceeded, map[string]any{"user_id": createResult.UserID}); err != nil {
		return CreateUserResult{}, s.classifyMiss(ctx, op.OperationID, err)
	}
	return CreateUserResult{UserID: createResult.UserID}, nil
}

// membershipAttempt is the C5 outcome. `resolved` is the caller's decision
// signal: false means the outcome is genuinely unknown (receipt resolution
// failed) and the workflow must be left running for the resume machine;
// true means either the membership is durably applied (result != nil) or
// the failure is proven — compensate, never retry blindly.
type membershipAttempt struct {
	result   *organization.MembershipMutationResult // non-nil when durably applied
	resolved bool                                   // false = outcome unknown (leave running)
	err      error                                  // mapped error when not applied
}

// activationAttempt is the C7 outcome with the same resolved semantics.
type activationAttempt struct {
	version  int64
	resolved bool
	err      error
}

// applyMembership runs C5: reauthorize, then SetUserDepartment with
// resolve-first continuation on unknown outcomes. A nil department means no
// membership side effect (nil attempt with no error).
func (s *Service) applyMembership(ctx context.Context, op OperationContext, userID int64, departmentID *int64) membershipAttempt {
	if departmentID == nil {
		return membershipAttempt{resolved: true}
	}
	// Reauthorization immediately before the membership side effect.
	if ok, err := s.requireUsersWrite(ctx, op.ActorUserID); err != nil {
		return membershipAttempt{resolved: true, err: mapParticipantError(err)}
	} else if !ok {
		return membershipAttempt{resolved: true, err: ErrForbidden}
	}
	result, err := s.org.Membership.SetUserDepartment(ctx, organizationOperationContext(op), userID, *departmentID, nil)
	if err == nil {
		return membershipAttempt{result: &result, resolved: true}
	}
	if isUnknownOutcome(err) {
		fp, ferr := setMembershipFingerprint(op.ActorUserID, userID, departmentID, nil)
		if ferr != nil {
			return membershipAttempt{resolved: true, err: ErrInternal}
		}
		committed, rerr := s.org.Receipts.ResolveCommand(ctx, op.OperationID, commandSetUserDepartment, fp)
		if rerr != nil {
			return membershipAttempt{resolved: false, err: mapParticipantError(rerr)}
		}
		if committed == nil {
			return membershipAttempt{resolved: true, err: mapParticipantError(err)} // proven non-commit
		}
		result, ferr = membershipResultFromReceipt(*committed)
		if ferr != nil {
			return membershipAttempt{resolved: true, err: ErrInternal}
		}
		return membershipAttempt{result: &result, resolved: true}
	}
	return membershipAttempt{resolved: true, err: mapParticipantError(err)} // proven failure
}

// activateUser runs C7: reauthorize, then ActivateUser with resolve-first
// continuation.
func (s *Service) activateUser(ctx context.Context, op OperationContext, userID, expectedVersion int64) activationAttempt {
	// Reauthorization immediately before the activation side effect.
	if ok, err := s.requireUsersWrite(ctx, op.ActorUserID); err != nil {
		return activationAttempt{resolved: true, err: mapParticipantError(err)}
	} else if !ok {
		return activationAttempt{resolved: true, err: ErrForbidden}
	}
	version, err := s.iam.Managed.ActivateUser(ctx, iamOperationContext(op), userID, expectedVersion)
	if err == nil {
		return activationAttempt{version: version, resolved: true}
	}
	if isUnknownOutcome(err) {
		fp, ferr := activateFingerprint(op.ActorUserID, userID, expectedVersion)
		if ferr != nil {
			return activationAttempt{resolved: true, err: ErrInternal}
		}
		committed, rerr := s.iam.Receipts.ResolveCommand(ctx, op.OperationID, commandActivateUser, fp)
		if rerr != nil {
			return activationAttempt{resolved: false, err: mapParticipantError(rerr)}
		}
		if committed != nil && committed.ResultingVersion != nil {
			return activationAttempt{version: *committed.ResultingVersion, resolved: true}
		}
		return activationAttempt{resolved: true, err: mapParticipantError(err)} // proven non-commit
	}
	return activationAttempt{resolved: true, err: mapParticipantError(err)} // proven rejection
}

// --- compensation ---------------------------------------------------------------

// compensateProvisioningOnly reverses C3 when no membership side effect
// exists (C5 reauthorization/proven failure).
func (s *Service) compensateProvisioningOnly(ctx context.Context, op OperationContext, claimToken string, userID, expectedVersion int64, cause error) (CreateUserResult, error) {
	return s.runCompensation(ctx, op, claimToken, userID, expectedVersion, nil, cause)
}

// compensateApplied reverses the applied membership (version CAS) and the
// provisioning user after a proven C7 failure.
func (s *Service) compensateApplied(ctx context.Context, op OperationContext, claimToken string, userID, expectedVersion int64, appliedMembershipVersion *int64, cause error) (CreateUserResult, error) {
	return s.runCompensation(ctx, op, claimToken, userID, expectedVersion, appliedMembershipVersion, cause)
}

// runCompensation executes the compensation sequence for a claimed workflow:
// the decision is durable first (BeginCompensation), then the side effects
// (restore membership if applied, delete the provisioning user), then the
// durable outcome, then the terminal rejection. A compensation failure or
// conflict is durable and visible — failed_manual, never a silent success.
// Successful compensation converges to rejected AND releases the subject
// exclusion in the same transaction (T055; the exclusion must not outlive a
// proven-compensated workflow).
func (s *Service) runCompensation(ctx context.Context, op OperationContext, claimToken string, userID, expectedVersion int64, appliedMembershipVersion *int64, cause error) (CreateUserResult, error) {
	// The compensation decision is durable before any compensation side effect.
	if err := s.work.BeginCompensation(ctx, op.OperationID, claimToken); err != nil {
		return CreateUserResult{}, s.classifyMiss(ctx, op.OperationID, err)
	}
	w := Workflow{OperationID: op.OperationID, OperationType: OperationTypeCreateUser, SubjectUserID: &userID, ExpectedIamVersion: &expectedVersion, AppliedMembershipVersion: appliedMembershipVersion}
	if err := s.compensateSideEffects(ctx, op, w); err != nil {
		// The failure is durable and visible; reconciliation follows.
		_ = s.work.SetCompensationFailed(ctx, op.OperationID, claimToken)
		_ = s.work.FailManual(ctx, op.OperationID, claimToken, "compensation_failed")
		return CreateUserResult{}, err
	}
	if err := s.work.SetCompensationSucceeded(ctx, op.OperationID, claimToken); err != nil {
		return CreateUserResult{}, s.classifyMiss(ctx, op.OperationID, err)
	}
	if err := s.finalizeCompensated(ctx, op, w, claimToken, cause); err != nil {
		return CreateUserResult{}, s.classifyMiss(ctx, op.OperationID, err)
	}
	return CreateUserResult{}, cause
}

// compensateSideEffects executes the durable compensation sequence of a
// workflow from its persisted fields (create: restore the applied membership
// if any, then delete the still-provisioning user; update: restore the
// membership to its previous state). Participants are resolve-first, so
// re-entry replays any partially committed step instead of re-executing it.
// A restore conflict — the membership changed after the workflow applied it
// — is reconciliation, never a silent success. Delete workflows are never
// compensated (the IAM deletion is authoritative and irreversible).
func (s *Service) compensateSideEffects(ctx context.Context, op OperationContext, w Workflow) error {
	switch w.OperationType {
	case OperationTypeUpdateUser:
		return s.restoreUpdateMembership(ctx, op, w)
	case OperationTypeCreateUser:
		if w.SubjectUserID == nil || w.ExpectedIamVersion == nil {
			return ErrReconciliationRequired // inconsistent row
		}
		if w.AppliedMembershipVersion != nil {
			if _, err := s.org.Membership.RestoreUserDepartment(ctx, organizationOperationContext(op), *w.SubjectUserID, *w.AppliedMembershipVersion, nil, nil); err != nil {
				if errors.Is(err, organization.ErrCompensationConflict) {
					return fmt.Errorf("%w: membership changed after the workflow applied it", ErrReconciliationRequired)
				}
				return mapParticipantError(err)
			}
		}
		return mapParticipantError(s.iam.Managed.CompensateProvisioningUser(ctx, iamOperationContext(op), *w.SubjectUserID, *w.ExpectedIamVersion))
	}
	return ErrReconciliationRequired
}

// --- UpdateUser: U0–U7 ---------------------------------------------------------

// UpdateUser runs the update saga (U0–U7). Membership is applied FIRST (U4)
// because it is the only reversible side effect — the IAM update (U6) may
// carry a password change that can never be reversed, so it commits last.
// Idempotency: the scope (operation_type, actor, key) resolves to at most one
// logical workflow; a same-key replay returns the stored outcome without
// re-running side effects, and a same-key retry continues a parked workflow
// before its input deadline.
func (s *Service) UpdateUser(ctx context.Context, op OperationContext, p UpdateUserParams) (UpdateUserResult, error) {
	fp, err := updateRequestFingerprint(op.ActorUserID, p)
	if err != nil {
		return UpdateUserResult{}, ErrInternal
	}

	// Idempotency resolution first: the same key may replay or continue.
	if op.IdempotencyKey != nil {
		existing, err := s.work.GetWorkflowByIdempotencyKey(ctx, OperationTypeUpdateUser, op.ActorUserID, *op.IdempotencyKey)
		if err != nil {
			return UpdateUserResult{}, err
		}
		if existing != nil {
			return s.replayOrContinueUpdate(ctx, op, p, fp, *existing)
		}
	}

	// U0: persist the pending workflow before any participant side effect.
	op.OperationID = uuid.NewString()
	if err := s.work.CreateWorkflow(ctx, Workflow{
		OperationID:        op.OperationID,
		OperationType:      OperationTypeUpdateUser,
		IdempotencyKey:     op.IdempotencyKey,
		RequestFingerprint: fp,
		ActorUserID:        op.ActorUserID,
		State:              WorkflowPending,
		CurrentStep:        StepCreated,
		RetryDeadlineAt:    time.Now().Add(retryDeadlineTTL),
	}); err != nil {
		return UpdateUserResult{}, err
	}

	// Claim our own row — keyed, because a limit=1 batch scan could steal an
	// older workflow. A fresh pending row has no contention.
	claimed, err := s.work.ClaimWorkflowByOperationID(ctx, op.OperationID, leaseOwner(op.ActorUserID), leaseDuration, time.Now())
	if err != nil {
		return UpdateUserResult{}, s.classifyMiss(ctx, op.OperationID, err)
	}
	return s.runUpdateForward(ctx, op, p, *claimed.ClaimToken)
}

// replayOrContinueUpdate classifies the workflow already scoped to this key.
func (s *Service) replayOrContinueUpdate(ctx context.Context, op OperationContext, p UpdateUserParams, fp string, existing Workflow) (UpdateUserResult, error) {
	switch existing.State {
	case WorkflowSucceeded:
		if existing.RequestFingerprint != fp {
			return UpdateUserResult{}, ErrIdempotencyConflict
		}
		return UpdateUserResult{}, nil
	case WorkflowRejected:
		if existing.RequestFingerprint != fp {
			return UpdateUserResult{}, ErrIdempotencyConflict
		}
		return UpdateUserResult{}, replayError(existing)
	case WorkflowAwaitingClientInput:
		return s.retryAwaitingUpdate(ctx, op, p, fp, existing)
	case WorkflowFailedManual:
		return UpdateUserResult{}, ErrReconciliationRequired
	case WorkflowFailedRetryable:
		// A worker retry owns this workflow; the client backs off and
		// re-checks (the transport maps Retry-After).
		return UpdateUserResult{}, ErrWorkflowRetryable.WithRetryAfter(5)
	default: // pending / running / compensating — an in-flight claim owns it
		return UpdateUserResult{}, ErrOperationInProgress.WithRetryAfter(1)
	}
}

// retryAwaitingUpdate continues a parked update workflow on a same-key
// authorized retry before the input deadline (ClaimAwaitingClientInput). The
// password is excluded from the fingerprint, so a corrected credential keeps
// the same request identity; the forward machine dispatches from the
// persisted step (U6's command fingerprint was already persisted at U5).
func (s *Service) retryAwaitingUpdate(ctx context.Context, op OperationContext, p UpdateUserParams, fp string, existing Workflow) (UpdateUserResult, error) {
	if existing.RequestFingerprint != fp {
		return UpdateUserResult{}, ErrIdempotencyConflict
	}
	if existing.ClientInputDeadlineAt == nil || !existing.ClientInputDeadlineAt.After(time.Now()) {
		return UpdateUserResult{}, ErrOperationExpired
	}
	claimed, err := s.work.ClaimAwaitingClientInput(ctx, existing.OperationID, *op.IdempotencyKey,
		leaseOwner(op.ActorUserID), leaseDuration, time.Now())
	if err != nil {
		if errors.Is(err, ErrStaleClaim) {
			// Deadline passed or the state moved on between read and claim:
			// re-classify against the fresh row.
			w, rerr := s.work.GetWorkflowByOperationID(ctx, existing.OperationID)
			if rerr != nil {
				return UpdateUserResult{}, rerr
			}
			if w == nil {
				return UpdateUserResult{}, ErrWorkflowNotFound
			}
			return s.replayOrContinueUpdate(ctx, op, p, fp, *w)
		}
		return UpdateUserResult{}, err
	}
	// The parked workflow keeps its operation identity: the forward machine
	// continues under the stored operation ID so every step persistence and
	// receipt stays scoped to the original workflow.
	op.OperationID = existing.OperationID
	return s.runUpdateForward(ctx, op, p, *claimed.ClaimToken)
}

// runUpdateForward executes the forward saga from the claimed workflow's
// durable step. Called from the HTTP path (fresh U0 or a same-key retry);
// the resume machine re-enters only at organization_updated (resolve-only).
func (s *Service) runUpdateForward(ctx context.Context, op OperationContext, p UpdateUserParams, claimToken string) (UpdateUserResult, error) {
	w, err := s.work.GetWorkflowByOperationID(ctx, op.OperationID)
	if err != nil {
		return UpdateUserResult{}, err
	}
	if w == nil {
		return UpdateUserResult{}, ErrWorkflowNotFound
	}
	switch w.CurrentStep {
	case StepCreated, StepIamValidated:
		// Fresh U0, or a retry after a U3 dependency failure: re-validate
		// everything and re-read the membership (the expected version may
		// have moved since the first attempt).
		return s.updateU1(ctx, op, p, claimToken)
	case StepOrganizationRead:
		// Retry after a U4 failure/unknown: continue with the PERSISTED
		// previous state — the U4 re-issue is replay-or-CAS stable.
		return s.updateFromOrganizationRead(ctx, op, p, claimToken)
	case StepOrganizationUpdated:
		// Retry after U4 committed but the request ended before U6 (or U6
		// outcome unknown with a credential): re-issue U6, never U4.
		return s.updateFromOrganizationUpdated(ctx, op, p, claimToken)
	}
	return UpdateUserResult{}, ErrInternal
}

// updateU1 runs U1–U3 (validations + the membership read), then the shared
// U4–U7 tail. U1 persists the subject + expected IAM version + the planned
// department BEFORE the read, so a parked retry always resumes from a
// durable point and the planned department survives the request.
func (s *Service) updateU1(ctx context.Context, op OperationContext, p UpdateUserParams, claimToken string) (UpdateUserResult, error) {
	// U1: the user must exist (expected version = current version) and the
	// role set must still be valid.
	profile, err := s.iam.Identity.GetIdentity(ctx, p.UserID)
	if err != nil {
		bffErr := mapParticipantError(err)
		return UpdateUserResult{}, s.completeUpdateReject(ctx, op.OperationID, claimToken, p.UserID, bffErr)
	}
	if _, err := s.iam.Roles.ValidateRoleIDs(ctx, p.RoleIDs); err != nil {
		bffErr := mapParticipantError(err)
		return UpdateUserResult{}, s.completeUpdateReject(ctx, op.OperationID, claimToken, p.UserID, bffErr)
	}
	expected := profile.Version
	if err := s.work.PersistStep(ctx, op.OperationID, claimToken, StepUpdate{
		Step:                StepIamValidated,
		SubjectUserID:       &p.UserID,
		ExpectedIamVersion:  &expected,
		AppliedDepartmentID: p.DepartmentID,
	}); err != nil {
		return UpdateUserResult{}, s.classifyMiss(ctx, op.OperationID, err)
	}
	// The subject exclusion row is inserted AFTER the persist so a retry from
	// StepCreated never collides with its own row; a re-entry of THIS
	// workflow is idempotent (store ON CONFLICT). Another active workflow
	// covering the user rejects this one as OPERATION_IN_PROGRESS.
	if err := s.work.InsertWorkflowSubject(ctx, WorkflowSubject{
		OperationID:        op.OperationID,
		SubjectUserID:      p.UserID,
		ExpectedIamVersion: expected,
	}); err != nil {
		if errors.Is(err, ErrSubjectExcluded) {
			return UpdateUserResult{}, s.completeUpdateReject(ctx, op.OperationID, claimToken, p.UserID, ErrOperationInProgress)
		}
		return UpdateUserResult{}, err
	}

	// U2: the target department must still exist (nil is valid).
	if _, err := s.org.Departments.ValidateDepartment(ctx, p.DepartmentID); err != nil {
		bffErr := mapParticipantError(err)
		return UpdateUserResult{}, s.completeUpdateReject(ctx, op.OperationID, claimToken, p.UserID, bffErr)
	}

	// U3: read the current versioned membership state (a missing row is the
	// expected first absence — both previous values stay nil).
	cur, err := s.org.Membership.GetUserDepartment(ctx, p.UserID)
	if err != nil {
		if isUnknownOutcome(err) {
			// Retryable dependency failure and no writes yet: park for the
			// same-key retry (which re-reads U3 fresh); the worker cannot
			// decide park-vs-reject without the request's password-ness.
			return UpdateUserResult{}, s.park(ctx, op.OperationID, claimToken, mapParticipantError(err))
		}
		bffErr := mapParticipantError(err)
		return UpdateUserResult{}, s.completeUpdateReject(ctx, op.OperationID, claimToken, p.UserID, bffErr)
	}
	var prevDept *int64
	var prevVersion *int64
	if cur != nil {
		prevDept = cur.DepartmentID
		prevVersion = &cur.MembershipVersion
	}
	if err := s.work.PersistStep(ctx, op.OperationID, claimToken, StepUpdate{
		Step:                      StepOrganizationRead,
		PreviousDepartmentID:      prevDept,
		PreviousMembershipVersion: prevVersion,
	}); err != nil {
		return UpdateUserResult{}, s.classifyMiss(ctx, op.OperationID, err)
	}
	return s.updateFromOrganizationRead(ctx, op, p, claimToken)
}

// updateFromOrganizationRead runs U4–U7 with the persisted previous state
// (U3) as the CAS baseline — the fresh path and the organization_read retry
// share it, so a re-issued U4 is replay-or-CAS stable under the persisted
// expected membership version.
func (s *Service) updateFromOrganizationRead(ctx context.Context, op OperationContext, p UpdateUserParams, claimToken string) (UpdateUserResult, error) {
	w, err := s.work.GetWorkflowByOperationID(ctx, op.OperationID)
	if err != nil {
		return UpdateUserResult{}, err
	}
	if w == nil {
		return UpdateUserResult{}, ErrWorkflowNotFound
	}
	if w.SubjectUserID == nil || w.ExpectedIamVersion == nil {
		return UpdateUserResult{}, ErrReconciliationRequired // inconsistent row
	}

	// U4: CAS set/clear the target department (set when a department is
	// planned, clear when it is nil). U4 always runs: a clear of an absent
	// state still creates the NULL-department tombstone and bumps the version
	// (Organization contract), so an applied version always exists at U5.
	attempt := s.applyUpdateMembership(ctx, op, *w)
	if attempt.err != nil {
		if !attempt.resolved {
			// Outcome genuinely unknown while the request is alive: park
			// awaiting input — the same-key retry re-resolves (replay-or-CAS)
			// and only the client can carry U6.
			return UpdateUserResult{}, s.park(ctx, op.OperationID, claimToken, attempt.err)
		}
		// Proven U4 failure: IAM is unchanged, so no compensation exists. With
		// a requested password change the same-key retry may recover; without
		// one the client has nothing to re-send — reject.
		if p.Password != "" {
			return UpdateUserResult{}, s.park(ctx, op.OperationID, claimToken, attempt.err)
		}
		return UpdateUserResult{}, s.completeUpdateReject(ctx, op.OperationID, claimToken, *w.SubjectUserID, attempt.err)
	}

	// U5: persist the applied membership version AND the U6 command
	// fingerprint BEFORE the credential-bearing call — a crash between U5
	// and U6 leaves the worker able to resolve-only (the request fields are
	// never stored).
	updateFp, ferr := updateCommandFingerprint(op.ActorUserID, *w.SubjectUserID, *w.ExpectedIamVersion, p)
	if ferr != nil {
		return UpdateUserResult{}, ErrInternal
	}
	if err := s.work.PersistStep(ctx, op.OperationID, claimToken, StepUpdate{
		Step:                     StepOrganizationUpdated,
		AppliedMembershipVersion: &attempt.result.ResultingMembershipVersion,
		CommandFingerprint:       &updateFp,
	}); err != nil {
		return UpdateUserResult{}, s.classifyMiss(ctx, op.OperationID, err)
	}
	// Re-read the workflow: U6 needs the just-persisted command fingerprint
	// (this read predates U5, so its copy is nil).
	w, err = s.work.GetWorkflowByOperationID(ctx, op.OperationID)
	if err != nil {
		return UpdateUserResult{}, err
	}
	if w == nil {
		return UpdateUserResult{}, ErrWorkflowNotFound
	}
	return s.updateCredentialAndComplete(ctx, op, p, claimToken, *w)
}

// updateFromOrganizationUpdated re-issues U6 on a same-key retry parked at
// organization_updated (U4 committed + U5 persisted); the fingerprint stored
// at U5 is re-derived from the same request, so the IAM resolve-first replay
// matches it exactly.
func (s *Service) updateFromOrganizationUpdated(ctx context.Context, op OperationContext, p UpdateUserParams, claimToken string) (UpdateUserResult, error) {
	w, err := s.work.GetWorkflowByOperationID(ctx, op.OperationID)
	if err != nil {
		return UpdateUserResult{}, err
	}
	if w == nil {
		return UpdateUserResult{}, ErrWorkflowNotFound
	}
	return s.updateCredentialAndComplete(ctx, op, p, claimToken, *w)
}

// updateCredentialAndComplete runs U6–U7 for a workflow whose membership
// side effect is durably applied: reauthorize, re-issue the IAM update
// resolve-first, then succeed — or restore the membership and reject.
func (s *Service) updateCredentialAndComplete(ctx context.Context, op OperationContext, p UpdateUserParams, claimToken string, w Workflow) (UpdateUserResult, error) {
	if w.SubjectUserID == nil || w.ExpectedIamVersion == nil || w.CommandFingerprint == nil {
		return UpdateUserResult{}, ErrReconciliationRequired // inconsistent row
	}
	upd := s.updateManagedUser(ctx, op, *w.SubjectUserID, *w.ExpectedIamVersion, p)
	if upd.err != nil {
		if !upd.resolved {
			// Outcome genuinely unknown: with a requested password change the
			// same-key retry re-resolves (park awaiting); without one, leave
			// the workflow running — the resume machine resolves-only with
			// the persisted fingerprint (never re-issues, never restores
			// blindly).
			if p.Password != "" {
				return UpdateUserResult{}, s.park(ctx, op.OperationID, claimToken, upd.err)
			}
			return UpdateUserResult{}, upd.err
		}
		// Proven U6 failure/non-commit: reverse the membership (CAS against
		// the applied version) and reject with the IAM outcome.
		_, cerr := s.runUpdateCompensation(ctx, op, claimToken, w, upd.err)
		return UpdateUserResult{}, cerr
	}
	// U7: terminal success, releasing the subject exclusion atomically.
	if err := s.completeUpdateWorkflow(ctx, op.OperationID, claimToken, WorkflowSucceeded, nil, *w.SubjectUserID); err != nil {
		return UpdateUserResult{}, s.classifyMiss(ctx, op.OperationID, err)
	}
	return UpdateUserResult{}, nil
}

// updateMembershipAttempt is the U4 outcome with the same resolved semantics
// as membershipAttempt (create).
type updateMembershipAttempt struct {
	result   *organization.MembershipMutationResult // non-nil when durably applied
	resolved bool                                   // false = outcome unknown (park)
	err      error                                  // mapped error when not applied
}

// updateAttempt is the U6 outcome with the same resolved semantics.
type updateAttempt struct {
	version  int64
	resolved bool
	err      error
}

// applyUpdateMembership runs U4: reauthorize, then CAS set/clear the planned
// department with the persisted previous membership version as the expected
// state. Unknown outcomes continue resolve-first (replay-or-CAS).
func (s *Service) applyUpdateMembership(ctx context.Context, op OperationContext, w Workflow) updateMembershipAttempt {
	if ok, err := s.requireUsersWrite(ctx, op.ActorUserID); err != nil {
		return updateMembershipAttempt{resolved: true, err: mapParticipantError(err)}
	} else if !ok {
		return updateMembershipAttempt{resolved: true, err: ErrForbidden}
	}
	userID := *w.SubjectUserID
	expected := w.PreviousMembershipVersion // U3's persisted read
	if w.AppliedDepartmentID == nil {
		result, err := s.org.Membership.ClearUserDepartment(ctx, organizationOperationContext(op), userID, expected)
		if err == nil {
			return updateMembershipAttempt{result: &result, resolved: true}
		}
		if isUnknownOutcome(err) {
			fp, ferr := clearMembershipFingerprint(op.ActorUserID, userID, expected)
			if ferr != nil {
				return updateMembershipAttempt{resolved: true, err: ErrInternal}
			}
			return s.resolveMembershipUnknown(ctx, op, commandClearUserDepartment, fp, err)
		}
		return updateMembershipAttempt{resolved: true, err: mapParticipantError(err)} // proven failure
	}
	result, err := s.org.Membership.SetUserDepartment(ctx, organizationOperationContext(op), userID, *w.AppliedDepartmentID, expected)
	if err == nil {
		return updateMembershipAttempt{result: &result, resolved: true}
	}
	if isUnknownOutcome(err) {
		fp, ferr := setMembershipFingerprint(op.ActorUserID, userID, w.AppliedDepartmentID, expected)
		if ferr != nil {
			return updateMembershipAttempt{resolved: true, err: ErrInternal}
		}
		return s.resolveMembershipUnknown(ctx, op, commandSetUserDepartment, fp, err)
	}
	return updateMembershipAttempt{resolved: true, err: mapParticipantError(err)} // proven failure
}

// resolveMembershipUnknown continues a U4 unknown outcome: a committed
// receipt replays the result, absence proves non-commit, a resolve failure
// leaves the outcome unknown (the caller parks).
func (s *Service) resolveMembershipUnknown(ctx context.Context, op OperationContext, command, fp string, cause error) updateMembershipAttempt {
	committed, rerr := s.org.Receipts.ResolveCommand(ctx, op.OperationID, command, fp)
	if rerr != nil {
		return updateMembershipAttempt{resolved: false, err: mapParticipantError(rerr)}
	}
	if committed == nil {
		return updateMembershipAttempt{resolved: true, err: mapParticipantError(cause)} // proven non-commit
	}
	result, ferr := membershipResultFromReceipt(*committed)
	if ferr != nil {
		return updateMembershipAttempt{resolved: true, err: ErrInternal}
	}
	return updateMembershipAttempt{result: &result, resolved: true}
}

// updateManagedUser runs U6: reauthorize, then UpdateManagedUser
// (expected-version CAS) with resolve-first continuation on unknown
// outcomes. The credential lives only in this request.
func (s *Service) updateManagedUser(ctx context.Context, op OperationContext, userID, expectedVersion int64, p UpdateUserParams) updateAttempt {
	if ok, err := s.requireUsersWrite(ctx, op.ActorUserID); err != nil {
		return updateAttempt{resolved: true, err: mapParticipantError(err)}
	} else if !ok {
		return updateAttempt{resolved: true, err: ErrForbidden}
	}
	var password *string
	if p.Password != "" {
		password = &p.Password
	}
	version, err := s.iam.Managed.UpdateManagedUser(ctx, iamOperationContext(op), userID, expectedVersion, p.Username, p.Account, p.Email, password, p.RoleIDs)
	if err == nil {
		return updateAttempt{version: version, resolved: true}
	}
	if isUnknownOutcome(err) {
		fp, ferr := updateCommandFingerprint(op.ActorUserID, userID, expectedVersion, p)
		if ferr != nil {
			return updateAttempt{resolved: true, err: ErrInternal}
		}
		committed, rerr := s.iam.Receipts.ResolveCommand(ctx, op.OperationID, commandUpdateManagedUser, fp)
		if rerr != nil {
			return updateAttempt{resolved: false, err: mapParticipantError(rerr)}
		}
		if committed != nil && committed.ResultingVersion != nil {
			return updateAttempt{version: *committed.ResultingVersion, resolved: true}
		}
		return updateAttempt{resolved: true, err: mapParticipantError(err)} // proven non-commit
	}
	return updateAttempt{resolved: true, err: mapParticipantError(err)} // proven rejection
}

// --- update compensation --------------------------------------------------------

// runUpdateCompensation reverses the applied U4 membership (version CAS) and
// terminally rejects. The IAM update is NEVER compensated — a password cannot
// be reversed, so the membership is applied before U6 and restored only when
// U6 provably did not commit. A restore failure or conflict is durable and
// visible: failed_manual, never a silent success.
func (s *Service) runUpdateCompensation(ctx context.Context, op OperationContext, claimToken string, w Workflow, cause error) (UpdateUserResult, error) {
	// The compensation decision is durable before any compensation side effect.
	if err := s.work.BeginCompensation(ctx, op.OperationID, claimToken); err != nil {
		return UpdateUserResult{}, s.classifyMiss(ctx, op.OperationID, err)
	}
	if err := s.restoreUpdateMembership(ctx, op, w); err != nil {
		// The failure is durable and visible; reconciliation follows.
		_ = s.work.SetCompensationFailed(ctx, op.OperationID, claimToken)
		_ = s.work.FailManual(ctx, op.OperationID, claimToken, "compensation_failed")
		return UpdateUserResult{}, err
	}
	if err := s.work.SetCompensationSucceeded(ctx, op.OperationID, claimToken); err != nil {
		return UpdateUserResult{}, s.classifyMiss(ctx, op.OperationID, err)
	}
	if err := s.completeUpdateWorkflow(ctx, op.OperationID, claimToken, WorkflowRejected, errorResult(cause), *w.SubjectUserID); err != nil {
		return UpdateUserResult{}, s.classifyMiss(ctx, op.OperationID, err)
	}
	return UpdateUserResult{}, cause
}

// restoreUpdateMembership reverses the U4 change with the workflow's
// persisted versions: only when the CURRENT membership still equals the
// applied version (never overwriting a newer independent write); the
// previous state — including expected absence — is restored from the U3
// read. A conflict is reconciliation, never a silent success.
func (s *Service) restoreUpdateMembership(ctx context.Context, op OperationContext, w Workflow) error {
	if w.AppliedMembershipVersion == nil || w.SubjectUserID == nil {
		return ErrReconciliationRequired // inconsistent row
	}
	if _, err := s.org.Membership.RestoreUserDepartment(ctx, organizationOperationContext(op), *w.SubjectUserID, *w.AppliedMembershipVersion, w.PreviousDepartmentID, w.PreviousMembershipVersion); err != nil {
		if errors.Is(err, organization.ErrCompensationConflict) {
			return fmt.Errorf("%w: membership changed after the workflow applied it", ErrReconciliationRequired)
		}
		return mapParticipantError(err)
	}
	return nil
}

// completeUpdateWorkflow terminally resolves an update workflow AND releases
// the subject exclusion in the same transaction — the subject row was
// inserted at U1, so every terminal (succeeded/rejected) must release it;
// failed_manual keeps the exclusion for reconciliation.
func (s *Service) completeUpdateWorkflow(ctx context.Context, operationID, claimToken string, state WorkflowState, result map[string]any, subjectUserID int64) error {
	return s.work.RunInTx(ctx, func(tx WorkflowStore) error {
		if err := tx.CompleteWorkflow(ctx, operationID, claimToken, state, result); err != nil {
			return err
		}
		return tx.ReleaseSubjectExclusion(ctx, operationID, subjectUserID)
	})
}

// completeUpdateReject terminally rejects an update workflow that has no
// membership side effect to reverse, releasing the subject exclusion.
func (s *Service) completeUpdateReject(ctx context.Context, operationID, claimToken string, subjectUserID int64, cause error) error {
	if err := s.completeUpdateWorkflow(ctx, operationID, claimToken, WorkflowRejected, errorResult(cause), subjectUserID); err != nil {
		return s.classifyMiss(ctx, operationID, err)
	}
	return cause
}

// --- DeleteUsers: D0–D5 ---------------------------------------------------------

// DeleteUsers runs the delete saga (D0–D5; D6/D7 are the outbox dispatcher and
// the Organization inbox consumer, already implemented). The IAM deletion is
// authoritative and irreversible — the saga NEVER compensates: a D5
// persistence failure is retried (the D3 batch receipt replays, never a
// second outbox event) and never reverses the deletion.
//
// Idempotency: the scope (operation_type, actor, key) resolves to at most one
// logical workflow; a same-key replay returns the stored outcome without
// re-running side effects.
func (s *Service) DeleteUsers(ctx context.Context, op OperationContext, p DeleteUserParams) (DeleteUserResult, error) {
	fp, err := deleteRequestFingerprint(op.ActorUserID, p.UserIDs)
	if err != nil {
		return DeleteUserResult{}, ErrInternal
	}

	// Idempotency resolution first: the same key may replay or continue.
	if op.IdempotencyKey != nil {
		existing, err := s.work.GetWorkflowByIdempotencyKey(ctx, OperationTypeDeleteUser, op.ActorUserID, *op.IdempotencyKey)
		if err != nil {
			return DeleteUserResult{}, err
		}
		if existing != nil {
			return s.replayOrContinueDelete(ctx, op, p, fp, *existing)
		}
	}

	// D0: persist the pending workflow before any participant side effect.
	op.OperationID = uuid.NewString()
	if err := s.work.CreateWorkflow(ctx, Workflow{
		OperationID:        op.OperationID,
		OperationType:      OperationTypeDeleteUser,
		IdempotencyKey:     op.IdempotencyKey,
		RequestFingerprint: fp,
		ActorUserID:        op.ActorUserID,
		State:              WorkflowPending,
		CurrentStep:        StepCreated,
		RetryDeadlineAt:    time.Now().Add(retryDeadlineTTL),
	}); err != nil {
		return DeleteUserResult{}, err
	}

	// Claim our own row — keyed, because a limit=1 batch scan could steal an
	// older workflow. A fresh pending row has no contention.
	claimed, err := s.work.ClaimWorkflowByOperationID(ctx, op.OperationID, leaseOwner(op.ActorUserID), leaseDuration, time.Now())
	if err != nil {
		return DeleteUserResult{}, s.classifyMiss(ctx, op.OperationID, err)
	}
	return s.runDeleteForward(ctx, op, p, *claimed.ClaimToken)
}

// replayOrContinueDelete classifies the workflow already scoped to this key.
func (s *Service) replayOrContinueDelete(ctx context.Context, op OperationContext, p DeleteUserParams, fp string, existing Workflow) (DeleteUserResult, error) {
	switch existing.State {
	case WorkflowSucceeded:
		if existing.RequestFingerprint != fp {
			return DeleteUserResult{}, ErrIdempotencyConflict
		}
		return DeleteUserResult{}, nil
	case WorkflowRejected:
		if existing.RequestFingerprint != fp {
			return DeleteUserResult{}, ErrIdempotencyConflict
		}
		return DeleteUserResult{}, replayError(existing)
	case WorkflowAwaitingClientInput:
		return s.retryAwaitingDelete(ctx, op, p, fp, existing)
	case WorkflowFailedManual:
		return DeleteUserResult{}, ErrReconciliationRequired
	case WorkflowFailedRetryable:
		// A worker retry owns this workflow; the client backs off and
		// re-checks (the transport maps Retry-After).
		return DeleteUserResult{}, ErrWorkflowRetryable.WithRetryAfter(5)
	default: // pending / running / compensating — an in-flight claim owns it
		return DeleteUserResult{}, ErrOperationInProgress.WithRetryAfter(1)
	}
}

// retryAwaitingDelete continues a parked delete workflow on a same-key
// authorized retry before the input deadline (ClaimAwaitingClientInput). The
// forward machine dispatches from the durable step: StepCreated re-runs
// D1–D5; delete_validated re-issues D3–D5 from the subject rows.
func (s *Service) retryAwaitingDelete(ctx context.Context, op OperationContext, p DeleteUserParams, fp string, existing Workflow) (DeleteUserResult, error) {
	if existing.RequestFingerprint != fp {
		return DeleteUserResult{}, ErrIdempotencyConflict
	}
	if existing.ClientInputDeadlineAt == nil || !existing.ClientInputDeadlineAt.After(time.Now()) {
		return DeleteUserResult{}, ErrOperationExpired
	}
	claimed, err := s.work.ClaimAwaitingClientInput(ctx, existing.OperationID, *op.IdempotencyKey,
		leaseOwner(op.ActorUserID), leaseDuration, time.Now())
	if err != nil {
		if errors.Is(err, ErrStaleClaim) {
			// Deadline passed or the state moved on between read and claim:
			// re-classify against the fresh row.
			w, rerr := s.work.GetWorkflowByOperationID(ctx, existing.OperationID)
			if rerr != nil {
				return DeleteUserResult{}, rerr
			}
			if w == nil {
				return DeleteUserResult{}, ErrWorkflowNotFound
			}
			return s.replayOrContinueDelete(ctx, op, p, fp, *w)
		}
		return DeleteUserResult{}, err
	}
	// The parked workflow keeps its operation identity: the forward machine
	// continues under the stored operation ID so the D3 receipt and every step
	// persistence stay scoped to the original workflow.
	op.OperationID = existing.OperationID
	return s.runDeleteForward(ctx, op, p, *claimed.ClaimToken)
}

// runDeleteForward executes the forward saga from the claimed workflow's
// durable step. Called from the HTTP path (fresh D0 or a same-key retry) and
// the resume machine (worker); StepDeleteValidated re-enters with the subject
// rows as the durable target set.
func (s *Service) runDeleteForward(ctx context.Context, op OperationContext, p DeleteUserParams, claimToken string) (DeleteUserResult, error) {
	w, err := s.work.GetWorkflowByOperationID(ctx, op.OperationID)
	if err != nil {
		return DeleteUserResult{}, err
	}
	if w == nil {
		return DeleteUserResult{}, ErrWorkflowNotFound
	}
	switch w.CurrentStep {
	case StepCreated:
		// Fresh D0 or a retry from before D2: re-run D1–D5 (D1 re-reads the
		// current versions; the D2 subject inserts are same-operation idempotent).
		return s.deleteD1(ctx, op, p.UserIDs, claimToken)
	case StepDeleteValidated:
		// D2 committed: the subject rows are the durable target set — re-issue
		// D3 (resolve-first) and D5, never D1 (the targets may already be gone).
		subjects, lerr := s.work.ListSubjectsByOperation(ctx, op.OperationID)
		if lerr != nil {
			return DeleteUserResult{}, lerr
		}
		if len(subjects) == 0 {
			return DeleteUserResult{}, ErrReconciliationRequired // inconsistent row
		}
		out := s.deleteD3D5(ctx, op, claimToken, subjects)
		return DeleteUserResult{}, out.err
	}
	return DeleteUserResult{}, ErrInternal
}

// deleteD1 runs D1 (full-batch preflight) and D2 (subject rows + durable step)
// then hands off to D3–D5. Any preflight or exclusion error means zero users
// deleted: a missing target rejects the parent with no subject rows; an
// exclusion conflict rolls the whole insert set back (zero subject rows
// remain) and rejects the parent OPERATION_IN_PROGRESS — the conflicting
// workflow's exclusion is untouched.
func (s *Service) deleteD1(ctx context.Context, op OperationContext, userIDs []int64, claimToken string) (DeleteUserResult, error) {
	// D1: every target must exist; its current version becomes the expected
	// CAS version for D2/D3. A missing target rejects the whole batch.
	targets := make([]iam.DeleteTarget, 0, len(userIDs))
	for _, id := range consistency.SortedIDs(userIDs) {
		profile, err := s.iam.Identity.GetIdentity(ctx, id)
		if err != nil {
			bffErr := mapParticipantError(err)
			if rerr := s.reject(ctx, op.OperationID, claimToken, bffErr); rerr != nil {
				return DeleteUserResult{}, rerr
			}
			return DeleteUserResult{}, bffErr
		}
		targets = append(targets, iam.DeleteTarget{UserID: id, ExpectedVersion: profile.Version})
	}

	// D2: one active subject row per validated target + the durable step
	// boundary in a single transaction (all-or-nothing).
	err := s.work.RunInTx(ctx, func(tx WorkflowStore) error {
		for _, t := range targets {
			if err := tx.InsertWorkflowSubject(ctx, WorkflowSubject{
				OperationID:        op.OperationID,
				SubjectUserID:      t.UserID,
				ExpectedIamVersion: t.ExpectedVersion,
			}); err != nil {
				return err
			}
		}
		return tx.PersistStep(ctx, op.OperationID, claimToken, StepUpdate{Step: StepDeleteValidated})
	})
	if err != nil {
		if errors.Is(err, ErrSubjectExcluded) {
			if rerr := s.reject(ctx, op.OperationID, claimToken, ErrOperationInProgress); rerr != nil {
				return DeleteUserResult{}, rerr
			}
			return DeleteUserResult{}, ErrOperationInProgress
		}
		return DeleteUserResult{}, s.classifyMiss(ctx, op.OperationID, err)
	}
	subjects := make([]WorkflowSubject, 0, len(targets))
	for _, t := range targets {
		subjects = append(subjects, WorkflowSubject{SubjectUserID: t.UserID, ExpectedIamVersion: t.ExpectedVersion})
	}
	out := s.deleteD3D5(ctx, op, claimToken, subjects)
	return DeleteUserResult{}, out.err
}

// deleteOutcome is the D3–D5 outcome: the workflow is either terminal
// (Succeeded/Rejected — the caller may finish) or still running (the outcome
// is genuinely unknown and the resume machine must re-issue later).
type deleteOutcome struct {
	terminal bool
	state    WorkflowState
	err      error
}

// deleteD3D5 runs D3 (re-issue with resolve-first continuation) then D5 for a
// workflow whose subject rows are durable (StepDeleteValidated). A proven D3
// failure releases the subject exclusions and terminally rejects; a committed
// batch persists every per-subject tombstone and succeeds in one transaction.
func (s *Service) deleteD3D5(ctx context.Context, op OperationContext, claimToken string, subjects []WorkflowSubject) deleteOutcome {
	// Reauthorization immediately before the D3 side effect.
	if ok, err := s.requireUsersWrite(ctx, op.ActorUserID); err != nil {
		return deleteOutcome{err: mapParticipantError(err)}
	} else if !ok {
		if rerr := s.completeDeleteReject(ctx, op.OperationID, claimToken, subjects, ErrForbidden); rerr != nil {
			return deleteOutcome{terminal: true, state: WorkflowRejected, err: rerr}
		}
		return deleteOutcome{terminal: true, state: WorkflowRejected, err: ErrForbidden}
	}

	attempt := s.deleteUsersAttempt(ctx, op, subjects)
	if attempt.err != nil {
		if !attempt.resolved {
			return deleteOutcome{err: attempt.err} // leave running for the resume machine
		}
		// Proven failure: zero users deleted; release the exclusions and reject.
		if rerr := s.completeDeleteReject(ctx, op.OperationID, claimToken, subjects, attempt.err); rerr != nil {
			return deleteOutcome{terminal: true, state: WorkflowRejected, err: rerr}
		}
		return deleteOutcome{terminal: true, state: WorkflowRejected, err: attempt.err}
	}
	// D5: persist every per-subject tombstone (active=false releases the
	// exclusion) and mark the workflow succeeded in one transaction.
	if err := s.completeDeleteSucceeded(ctx, op.OperationID, claimToken, attempt.results); err != nil {
		return deleteOutcome{err: s.classifyMiss(ctx, op.OperationID, err)}
	}
	return deleteOutcome{terminal: true, state: WorkflowSucceeded}
}

// deleteAttempt is the D3 outcome with the shared resolved semantics
// (resolved=false → the outcome is genuinely unknown, leave running).
type deleteAttempt struct {
	results  []iam.BatchDeleteResultItem // non-nil when the batch is durably deleted
	resolved bool
	err      error
}

// deleteUsersAttempt runs D3 for the durable subject set: re-issue DeleteUsers
// (the participant is resolve-first internally, so a committed batch replays
// and never emits a second outbox event). On an unknown outcome the batch
// receipt is resolved before deciding: committed -> continue to D5; absence
// proves a non-commit and is re-issued once; a resolve failure leaves the
// outcome unknown (the resume machine re-issues later).
func (s *Service) deleteUsersAttempt(ctx context.Context, op OperationContext, subjects []WorkflowSubject) deleteAttempt {
	targets := make([]iam.DeleteTarget, 0, len(subjects))
	for _, subj := range subjects {
		targets = append(targets, iam.DeleteTarget{UserID: subj.SubjectUserID, ExpectedVersion: subj.ExpectedIamVersion})
	}
	results, err := s.iam.Managed.DeleteUsers(ctx, iamOperationContext(op), targets)
	if err == nil {
		return deleteAttempt{results: results, resolved: true}
	}
	if !isUnknownOutcome(err) {
		return deleteAttempt{resolved: true, err: mapParticipantError(err)} // proven rejection
	}
	fp, ferr := deleteCommandFingerprint(op.ActorUserID, targets)
	if ferr != nil {
		return deleteAttempt{resolved: true, err: ErrInternal}
	}
	committed, rerr := s.iam.Receipts.ResolveCommand(ctx, op.OperationID, commandDeleteUsers, fp)
	if rerr != nil {
		return deleteAttempt{resolved: false, err: mapParticipantError(rerr)} // leave running
	}
	if committed != nil {
		results, ferr = deleteResultsFromReceipt(*committed)
		if ferr != nil {
			return deleteAttempt{resolved: true, err: ErrInternal}
		}
		return deleteAttempt{results: results, resolved: true}
	}
	// Proven non-commit: one fresh re-issue completes the batch (it either
	// commits or fails); a second unknown outcome stays unresolved.
	results, err = s.iam.Managed.DeleteUsers(ctx, iamOperationContext(op), targets)
	if err != nil {
		if isUnknownOutcome(err) {
			return deleteAttempt{resolved: false, err: mapParticipantError(err)}
		}
		return deleteAttempt{resolved: true, err: mapParticipantError(err)}
	}
	return deleteAttempt{results: results, resolved: true}
}

// completeDeleteSucceeded persists every per-subject tombstone (active=false
// releases the exclusion) and marks the workflow succeeded in one transaction.
func (s *Service) completeDeleteSucceeded(ctx context.Context, operationID, claimToken string, results []iam.BatchDeleteResultItem) error {
	return s.work.RunInTx(ctx, func(tx WorkflowStore) error {
		for _, r := range results {
			if err := tx.UpdateSubjectResult(ctx, operationID, r.UserID, r.TombstoneVersion); err != nil {
				return err
			}
		}
		return tx.CompleteWorkflow(ctx, operationID, claimToken, WorkflowSucceeded, nil)
	})
}

// completeDeleteReject terminally rejects a delete workflow whose subject rows
// must be released (D2 committed but the batch was not deleted), in one
// transaction.
func (s *Service) completeDeleteReject(ctx context.Context, operationID, claimToken string, subjects []WorkflowSubject, cause error) error {
	if err := s.work.RunInTx(ctx, func(tx WorkflowStore) error {
		if err := tx.CompleteWorkflow(ctx, operationID, claimToken, WorkflowRejected, errorResult(cause)); err != nil {
			return err
		}
		for _, subj := range subjects {
			if err := tx.ReleaseSubjectExclusion(ctx, operationID, subj.SubjectUserID); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return s.classifyMiss(ctx, operationID, err)
	}
	return cause
}

// deleteResultsFromReceipt reconstructs the D3 results from a committed batch
// receipt (the receipt carries the same normalized per-user tombstones as the
// synchronous response).
func deleteResultsFromReceipt(r iam.IAMCommandReceipt) ([]iam.BatchDeleteResultItem, error) {
	targets, ok := r.SafeResult["targets"].([]any)
	if !ok {
		return nil, ErrInternal
	}
	out := make([]iam.BatchDeleteResultItem, 0, len(targets))
	for _, t := range targets {
		m, ok := t.(map[string]any)
		if !ok {
			return nil, ErrInternal
		}
		userID, ok1 := m["user_id"].(float64)
		tombstone, ok2 := m["tombstone_version"].(float64)
		if !ok1 || !ok2 {
			return nil, ErrInternal
		}
		out = append(out, iam.BatchDeleteResultItem{UserID: int64(userID), TombstoneVersion: int64(tombstone)})
	}
	return out, nil
}

// --- resume machine (worker entry) ----------------------------------------------

// ResumeWorkflow reclaims and continues one workflow:
//
//   - expired running at a durable step: continue the forward machine from
//     the persisted step (participant resolve-first replays committed side
//     effects under the same operation ID);
//   - a step the worker cannot carry (create step=created; update before
//     organization_updated; delete step=created): park awaiting input so a
//     same-key retry (or the expiry finalizer) continues;
//   - expired awaiting_client_input: the expiry finalizer — no participant
//     side effect -> rejected/OPERATION_EXPIRED; reversible membership
//     applied -> restore then rejected;
//   - expired compensating: compensation reclaim — the compensation state
//     machine lands in T055; until then the row stays parked for manual
//     reconciliation.
//
// The forward dispatch is per operation type; each saga contributes its own
// durable steps.
func (s *Service) ResumeWorkflow(ctx context.Context, operationID string) (ResumeResult, error) {
	claimed, err := s.work.ClaimWorkflowByOperationID(ctx, operationID, workerOwner, leaseDuration, time.Now())
	if err != nil {
		if errors.Is(err, ErrStaleClaim) {
			return s.classifyClaimMiss(ctx, operationID)
		}
		return ResumeResult{}, err
	}
	w := *claimed
	op := OperationContext{OperationID: w.OperationID, IdempotencyKey: w.IdempotencyKey, ActorUserID: w.ActorUserID}

	switch w.State {
	case WorkflowRunning:
		// Budget guard (T055; contracts line 75): a running row is claimable
		// past the forward predicates (expired-running reclaims only check the
		// lease), so an exhausted row must NEVER execute a new forward command.
		if w.AttemptCount >= maxAttempts || !w.RetryDeadlineAt.After(time.Now()) {
			if err := s.work.FailManual(ctx, w.OperationID, *w.ClaimToken, "attempt_budget_exhausted"); err != nil {
				return ResumeResult{}, s.classifyMiss(ctx, w.OperationID, err)
			}
			return ResumeResult{OperationID: w.OperationID}, ErrReconciliationRequired
		}
		switch w.OperationType {
		case OperationTypeCreateUser:
			return s.resumeCreateRunning(ctx, op, w)
		case OperationTypeUpdateUser:
			return s.resumeUpdateRunning(ctx, op, w)
		case OperationTypeDeleteUser:
			return s.resumeDeleteRunning(ctx, op, w)
		}
		return ResumeResult{}, ErrInternal
	case WorkflowCompensating:
		if w.CompensationState == CompensationPending && w.SubjectUserID == nil {
			// Expiry finalization of a parked workflow with no participant
			// side effect: terminally reject OPERATION_EXPIRED.
			if err := s.work.CompleteWorkflow(ctx, w.OperationID, *w.ClaimToken, WorkflowRejected, errorResult(ErrOperationExpired)); err != nil {
				return ResumeResult{}, s.classifyMiss(ctx, w.OperationID, err)
			}
			return ResumeResult{OperationID: w.OperationID, Terminal: true, State: WorkflowRejected}, nil
		}
		// Compensation budget guard (defensive — the claim predicate already
		// requires compensation_attempt_count < 10 and a live retry deadline).
		if w.CompensationAttemptCount >= maxAttempts || !w.RetryDeadlineAt.After(time.Now()) {
			if err := s.work.FailManual(ctx, w.OperationID, *w.ClaimToken, "compensation_budget_exhausted"); err != nil {
				return ResumeResult{}, s.classifyMiss(ctx, w.OperationID, err)
			}
			return ResumeResult{OperationID: w.OperationID}, ErrReconciliationRequired
		}
		return s.resumeCompensation(ctx, op, w)
	}
	return ResumeResult{}, ErrInternal
}

// resumeCompensation continues a compensating workflow whose owner died
// (T055; contracts line 70). The compensation decision is durable
// (compensation_state), so the reclaim NEVER re-issues from scratch blind:
// it resolves the receipt of the LAST compensation command first. A
// committed receipt means the whole sequence committed (commands execute in
// order, the last one last) and only the terminal finalize is missing; an
// absent receipt means nothing provably committed and the full sequence
// re-runs — participants are resolve-first, so partial commits replay
// instead of re-executing. Compensation reclaims count separately from
// forward attempts and never consume the forward budget.
func (s *Service) resumeCompensation(ctx context.Context, op OperationContext, w Workflow) (ResumeResult, error) {
	claimToken := *w.ClaimToken
	switch w.CompensationState {
	case CompensationSucceeded:
		// All compensation commands committed by the dead owner; finalize.
		if err := s.finalizeCompensated(ctx, op, w, claimToken, ErrOperationExpired); err != nil {
			return ResumeResult{}, s.classifyMiss(ctx, w.OperationID, err)
		}
		return ResumeResult{OperationID: w.OperationID, Terminal: true, State: WorkflowRejected}, nil
	case CompensationPending:
		// An update that never applied membership (parked before U5, so U6
		// never ran) has no side effect to compensate: finalize directly.
		if w.OperationType == OperationTypeUpdateUser && w.AppliedMembershipVersion == nil {
			if err := s.finalizeCompensated(ctx, op, w, claimToken, ErrOperationExpired); err != nil {
				return ResumeResult{}, s.classifyMiss(ctx, w.OperationID, err)
			}
			return ResumeResult{OperationID: w.OperationID, Terminal: true, State: WorkflowRejected}, nil
		}
		// Resolve-first: the last compensation command's receipt decides.
		committed, err := s.resolveLastCompensation(ctx, op, w)
		if err != nil {
			// Unknown: leave compensating for a later resolve — never
			// finalize blindly and never re-issue.
			return ResumeResult{OperationID: w.OperationID}, mapParticipantError(err)
		}
		if committed {
			if err := s.finalizeCompensated(ctx, op, w, claimToken, ErrOperationExpired); err != nil {
				return ResumeResult{}, s.classifyMiss(ctx, w.OperationID, err)
			}
			return ResumeResult{OperationID: w.OperationID, Terminal: true, State: WorkflowRejected}, nil
		}
		// Absent: rerun the full compensation sequence (resolve-first replay
		// makes re-entry safe). Success finalizes; failure is durable and
		// visible — failed_manual, exclusion kept.
		if err := s.compensateSideEffects(ctx, op, w); err != nil {
			_ = s.work.SetCompensationFailed(ctx, w.OperationID, claimToken)
			_ = s.work.FailManual(ctx, w.OperationID, claimToken, "compensation_failed")
			return ResumeResult{}, err
		}
		if err := s.work.SetCompensationSucceeded(ctx, w.OperationID, claimToken); err != nil {
			return ResumeResult{}, s.classifyMiss(ctx, w.OperationID, err)
		}
		if err := s.finalizeCompensated(ctx, op, w, claimToken, ErrOperationExpired); err != nil {
			return ResumeResult{}, s.classifyMiss(ctx, w.OperationID, err)
		}
		return ResumeResult{OperationID: w.OperationID, Terminal: true, State: WorkflowRejected}, nil
	case CompensationFailed:
		// Durable compensation failure: automatic retry is over by decision;
		// manual reconciliation follows (the exclusion stays active).
		_ = s.work.FailManual(ctx, w.OperationID, claimToken, "compensation_failed")
		return ResumeResult{OperationID: w.OperationID}, ErrReconciliationRequired
	}
	return ResumeResult{}, ErrInternal
}

// resolveLastCompensation resolves the receipt of the LAST compensation
// command of the sequence (create: compensate_provisioning_user after the
// restore; update: restore_user_department). committed=true means the whole
// compensation sequence durably committed — commands run in order, so the
// last one committing implies the earlier ones did. Receipts are resolved
// with fingerprints rebuilt from workflow-persisted fields only.
func (s *Service) resolveLastCompensation(ctx context.Context, op OperationContext, w Workflow) (bool, error) {
	switch w.OperationType {
	case OperationTypeCreateUser:
		if w.SubjectUserID == nil || w.ExpectedIamVersion == nil {
			return false, ErrReconciliationRequired // inconsistent row
		}
		fp, err := compensateProvisioningFingerprint(op.ActorUserID, *w.SubjectUserID, *w.ExpectedIamVersion)
		if err != nil {
			return false, ErrInternal
		}
		receipt, err := s.iam.Receipts.ResolveCommand(ctx, op.OperationID, commandCompensateProvisioning, fp)
		if err != nil {
			return false, err
		}
		return receipt != nil, nil
	case OperationTypeUpdateUser:
		if w.SubjectUserID == nil || w.AppliedMembershipVersion == nil {
			return false, ErrReconciliationRequired // inconsistent row
		}
		fp, err := restoreMembershipFingerprint(op.ActorUserID, *w.SubjectUserID, *w.AppliedMembershipVersion, w.PreviousDepartmentID, w.PreviousMembershipVersion)
		if err != nil {
			return false, ErrInternal
		}
		receipt, err := s.org.Receipts.ResolveCommand(ctx, op.OperationID, commandRestoreUserDepartment, fp)
		if err != nil {
			return false, err
		}
		return receipt != nil, nil
	}
	return false, ErrReconciliationRequired // delete is never compensated
}

// finalizeCompensated terminally rejects a compensated workflow AND releases
// the subject exclusion in the same transaction (T055; contracts line 73 — a
// successful restore converges to rejected/OPERATION_EXPIRED and must not
// outlive its exclusion). failed_manual keeps the exclusion for
// reconciliation; this helper is only for proven compensation completion.
func (s *Service) finalizeCompensated(ctx context.Context, op OperationContext, w Workflow, claimToken string, cause error) error {
	if w.SubjectUserID == nil {
		return s.work.CompleteWorkflow(ctx, w.OperationID, claimToken, WorkflowRejected, errorResult(cause))
	}
	return s.work.RunInTx(ctx, func(tx WorkflowStore) error {
		if err := tx.CompleteWorkflow(ctx, w.OperationID, claimToken, WorkflowRejected, errorResult(cause)); err != nil {
			return err
		}
		return tx.ReleaseSubjectExclusion(ctx, w.OperationID, *w.SubjectUserID)
	})
}

// resumeCreateRunning dispatches the create forward machine from its durable
// step (the create saga's worker-visible steps).
func (s *Service) resumeCreateRunning(ctx context.Context, op OperationContext, w Workflow) (ResumeResult, error) {
	switch w.CurrentStep {
	case StepCreated:
		// No credential in storage and no durable side effect to resolve:
		// park awaiting input.
		if err := s.work.EnterAwaitingClientInput(ctx, w.OperationID, *w.ClaimToken, time.Now().Add(clientInputTTL)); err != nil {
			return ResumeResult{}, s.classifyMiss(ctx, w.OperationID, err)
		}
		return ResumeResult{OperationID: w.OperationID}, nil
	case StepIamProvisioned:
		return s.resumeCreateFromProvisioned(ctx, op, w)
	case StepOrganizationAssigned:
		return s.resumeCreateFromAssigned(ctx, op, w)
	}
	return ResumeResult{}, ErrInternal
}

// resumeUpdateRunning dispatches the update forward machine from its durable
// step. Before organization_updated the worker has neither the request
// fields nor the password-ness that decides park-vs-reject — park awaiting
// input for the same-key client retry. At organization_updated the U6
// fingerprint was persisted at U5, so the worker resolves-only: committed ->
// succeed (never restore); proven non-commit -> restore + reject; resolve
// failure -> leave running for a later resolve.
func (s *Service) resumeUpdateRunning(ctx context.Context, op OperationContext, w Workflow) (ResumeResult, error) {
	switch w.CurrentStep {
	case StepCreated, StepIamValidated, StepOrganizationRead:
		if err := s.work.EnterAwaitingClientInput(ctx, w.OperationID, *w.ClaimToken, time.Now().Add(clientInputTTL)); err != nil {
			return ResumeResult{}, s.classifyMiss(ctx, w.OperationID, err)
		}
		return ResumeResult{OperationID: w.OperationID}, nil
	case StepOrganizationUpdated:
		return s.resumeUpdateFromOrganizationUpdated(ctx, op, w)
	}
	return ResumeResult{}, ErrInternal
}

// resumeDeleteRunning dispatches the delete forward machine from its durable
// step. At StepCreated nothing durable exists (the target set lives only in
// the request) — park awaiting input for the same-key client retry, exactly
// like the create saga. At StepDeleteValidated the subject rows ARE the
// durable target set, so the worker carries D3–D5 itself: DeleteUsers is
// resolve-first (a committed batch replays, never a second outbox event), a
// proven failure releases the exclusions and rejects, and the authoritative
// IAM deletion is never compensated.
func (s *Service) resumeDeleteRunning(ctx context.Context, op OperationContext, w Workflow) (ResumeResult, error) {
	switch w.CurrentStep {
	case StepCreated:
		if err := s.work.EnterAwaitingClientInput(ctx, w.OperationID, *w.ClaimToken, time.Now().Add(clientInputTTL)); err != nil {
			return ResumeResult{}, s.classifyMiss(ctx, w.OperationID, err)
		}
		return ResumeResult{OperationID: w.OperationID}, nil
	case StepDeleteValidated:
		subjects, err := s.work.ListSubjectsByOperation(ctx, w.OperationID)
		if err != nil {
			return ResumeResult{}, err
		}
		if len(subjects) == 0 {
			return ResumeResult{}, ErrReconciliationRequired // inconsistent row
		}
		out := s.deleteD3D5(ctx, op, *w.ClaimToken, subjects)
		if !out.terminal {
			return ResumeResult{OperationID: w.OperationID}, out.err // leave running
		}
		return ResumeResult{OperationID: w.OperationID, Terminal: true, State: out.state, Error: out.err}, out.err
	}
	return ResumeResult{}, ErrInternal
}

// resumeUpdateFromOrganizationUpdated is the worker's only update step it
// can carry: resolve the U6 receipt with the persisted fingerprint.
func (s *Service) resumeUpdateFromOrganizationUpdated(ctx context.Context, op OperationContext, w Workflow) (ResumeResult, error) {
	claimToken := *w.ClaimToken
	if w.SubjectUserID == nil || w.CommandFingerprint == nil {
		return ResumeResult{}, ErrReconciliationRequired // inconsistent row
	}
	committed, err := s.iam.Receipts.ResolveCommand(ctx, op.OperationID, commandUpdateManagedUser, *w.CommandFingerprint)
	if err != nil {
		// Unknown: leave running for a later resolve — never re-issue U6 and
		// never restore blindly.
		return ResumeResult{OperationID: w.OperationID}, mapParticipantError(err)
	}
	if committed != nil {
		// U6 committed: succeed. The Organization side is NOT restored — a
		// committed profile/password change stands with its membership.
		if err := s.completeUpdateWorkflow(ctx, op.OperationID, claimToken, WorkflowSucceeded, nil, *w.SubjectUserID); err != nil {
			return ResumeResult{}, s.classifyMiss(ctx, op.OperationID, err)
		}
		return ResumeResult{OperationID: w.OperationID, Terminal: true, State: WorkflowSucceeded}, nil
	}
	// Proven non-commit: reverse the applied membership and reject with the
	// dependency outcome (the U6 failure the client observed).
	_, cerr := s.runUpdateCompensation(ctx, op, claimToken, w, ErrDependencyTimeout)
	return ResumeResult{OperationID: w.OperationID, Terminal: true, State: WorkflowRejected, Error: cerr}, cerr
}

// resumeCreateFromProvisioned continues a create workflow at
// StepIamProvisioned: C3 is durably committed (subject persisted), so the
// machine re-enters at C5 with the persisted planned department.
func (s *Service) resumeCreateFromProvisioned(ctx context.Context, op OperationContext, w Workflow) (ResumeResult, error) {
	claimToken := *w.ClaimToken
	if w.SubjectUserID == nil || w.ExpectedIamVersion == nil {
		return ResumeResult{}, ErrReconciliationRequired // inconsistent row
	}
	membershipAttempt := s.applyMembership(ctx, op, *w.SubjectUserID, w.AppliedDepartmentID)
	if membershipAttempt.err != nil {
		if !membershipAttempt.resolved {
			return ResumeResult{OperationID: w.OperationID}, membershipAttempt.err // leave running
		}
		_, cerr := s.compensateProvisioningOnly(ctx, op, claimToken, *w.SubjectUserID, *w.ExpectedIamVersion, membershipAttempt.err)
		return ResumeResult{OperationID: w.OperationID, Terminal: true, State: WorkflowRejected, Error: cerr}, cerr
	}
	if err := s.work.PersistStep(ctx, w.OperationID, claimToken, StepUpdate{
		Step:                     StepOrganizationAssigned,
		AppliedMembershipVersion: versionPtr(membershipAttempt.result),
	}); err != nil {
		return ResumeResult{}, s.classifyMiss(ctx, w.OperationID, err)
	}
	return s.resumeCreateFromAssigned(ctx, op, w)
}

// resumeCreateFromAssigned continues a create workflow at
// StepOrganizationAssigned: C5 is durably committed (or skipped), so the
// machine re-enters at C7 with the persisted expected version.
func (s *Service) resumeCreateFromAssigned(ctx context.Context, op OperationContext, w Workflow) (ResumeResult, error) {
	claimToken := *w.ClaimToken
	if w.SubjectUserID == nil || w.ExpectedIamVersion == nil {
		return ResumeResult{}, ErrReconciliationRequired // inconsistent row
	}
	activation := s.activateUser(ctx, op, *w.SubjectUserID, *w.ExpectedIamVersion)
	if activation.err != nil {
		if !activation.resolved {
			return ResumeResult{OperationID: w.OperationID}, activation.err // leave running
		}
		_, cerr := s.compensateApplied(ctx, op, claimToken, *w.SubjectUserID, *w.ExpectedIamVersion, w.AppliedMembershipVersion, activation.err)
		return ResumeResult{OperationID: w.OperationID, Terminal: true, State: WorkflowRejected, Error: cerr}, cerr
	}
	if err := s.work.CompleteWorkflow(ctx, w.OperationID, claimToken, WorkflowSucceeded, map[string]any{"user_id": *w.SubjectUserID}); err != nil {
		return ResumeResult{}, s.classifyMiss(ctx, w.OperationID, err)
	}
	return ResumeResult{OperationID: w.OperationID, Terminal: true, State: WorkflowSucceeded}, nil
}

// --- helpers -------------------------------------------------------------------

func (s *Service) requireUsersWrite(ctx context.Context, actorUserID int64) (bool, error) {
	return s.iam.Identity.HasPermission(ctx, actorUserID, PermissionUsersWrite)
}

func leaseOwner(actorUserID int64) string {
	return fmt.Sprintf("actor:%d", actorUserID)
}

// reject terminally rejects a claimed workflow that has no participant side
// effects to reverse, persisting the safe error for replay.
func (s *Service) reject(ctx context.Context, operationID, claimToken string, cause error) error {
	if err := s.work.CompleteWorkflow(ctx, operationID, claimToken, WorkflowRejected, errorResult(cause)); err != nil {
		return s.classifyMiss(ctx, operationID, err)
	}
	return cause
}

// park parks a claimed workflow whose credential did not durably commit:
// awaiting_client_input with an immutable deadline, lease cleared. Waiting
// consumes no attempt budget.
func (s *Service) park(ctx context.Context, operationID, claimToken string, cause error) error {
	if err := s.work.EnterAwaitingClientInput(ctx, operationID, claimToken, time.Now().Add(clientInputTTL)); err != nil {
		return s.classifyMiss(ctx, operationID, err)
	}
	return cause
}

// classifyMiss maps a stale-claim CAS miss by re-reading the workflow: a
// terminal row replays its stored outcome; anything else is in progress.
func (s *Service) classifyMiss(ctx context.Context, operationID string, err error) error {
	if !errors.Is(err, ErrStaleClaim) {
		return err
	}
	w, rerr := s.work.GetWorkflowByOperationID(ctx, operationID)
	if rerr != nil {
		return rerr
	}
	if w == nil {
		return ErrWorkflowNotFound
	}
	switch w.State {
	case WorkflowSucceeded, WorkflowRejected, WorkflowFailedManual:
		return replayError(*w)
	default:
		return ErrOperationInProgress.WithRetryAfter(1)
	}
}

// classifyClaimMiss maps a claim miss on the resume path.
func (s *Service) classifyClaimMiss(ctx context.Context, operationID string) (ResumeResult, error) {
	w, err := s.work.GetWorkflowByOperationID(ctx, operationID)
	if err != nil {
		return ResumeResult{}, err
	}
	if w == nil {
		return ResumeResult{}, ErrWorkflowNotFound
	}
	switch w.State {
	case WorkflowSucceeded, WorkflowRejected:
		return ResumeResult{OperationID: operationID, Terminal: true, State: w.State}, nil
	case WorkflowFailedManual:
		// The claim missed because reconciliation owns the row now.
		return ResumeResult{OperationID: operationID}, ErrReconciliationRequired
	case WorkflowPending, WorkflowFailedRetryable, WorkflowRunning, WorkflowCompensating:
		// Budget/deadline exhaustion (10 attempts / original retry_deadline,
		// forward or compensation counter — whichever came first) with no live
		// lease transitions to failed_manual so reconciliation can proceed;
		// any possibly-applied subject exclusion stays active.
		if w.AttemptCount >= maxAttempts || w.CompensationAttemptCount >= maxAttempts || !w.RetryDeadlineAt.After(time.Now()) {
			exhausted, err := s.work.ExhaustToFailedManual(ctx, operationID, "attempt_budget_exhausted")
			if err != nil {
				return ResumeResult{}, err
			}
			if !exhausted {
				// A live lease protects the row: its owner's budget guard is
				// handling the exhaustion.
				return ResumeResult{OperationID: operationID}, ErrOperationInProgress.WithRetryAfter(1)
			}
			return ResumeResult{OperationID: operationID}, ErrReconciliationRequired
		}
		// live lease or not yet claimable: another owner has it
		return ResumeResult{OperationID: operationID}, ErrOperationInProgress.WithRetryAfter(1)
	}
	return ResumeResult{}, ErrInternal
}

// --- manual recovery (T055; contracts Reconciliation) --------------------------

// RecoverWorkflow is the least-privilege manual-recovery port. It requires
// users.write (the same principal that created the workflow), only touches
// failed_manual rows, and appends an immutable ledger row BEFORE every
// action — the append-only ledger faithfully records each attempt, success
// or failure. Execution may only finalize proven receipts or execute
// explicitly approved compensation; it NEVER resumes the revoked/failed
// actor's forward request.
func (s *Service) RecoverWorkflow(ctx context.Context, op OperationContext, p RecoverParams) (RecoverResult, error) {
	authorized, err := s.requireUsersWrite(ctx, op.ActorUserID)
	if err != nil {
		return RecoverResult{}, err
	}
	if !authorized {
		return RecoverResult{}, ErrForbidden
	}
	w, err := s.work.GetWorkflowByOperationID(ctx, p.OperationID)
	if err != nil {
		return RecoverResult{}, err
	}
	if w == nil {
		return RecoverResult{}, ErrWorkflowNotFound
	}
	if w.State != WorkflowFailedManual {
		// Automatic execution (or another recovery) still owns the row.
		return RecoverResult{}, ErrOperationInProgress
	}

	principal := strconv.FormatInt(op.ActorUserID, 10)
	var correlationID *string
	if op.CorrelationID != "" {
		correlationID = &op.CorrelationID
	}
	action := RecoveryAction{
		ActionID:            uuid.NewString(),
		OperationID:         p.OperationID,
		RecoveryPrincipalID: principal,
		AuthorizationSource: "permission:" + PermissionUsersWrite,
		ApprovalID:          p.ApprovalID,
		ActionType:          p.ActionType,
		ReasonCode:          p.ReasonCode,
		PreviousState:       WorkflowFailedManual,
		ResultingState:      WorkflowFailedManual, // refined on terminal completion
		CorrelationID:       correlationID,
	}

	// Append BEFORE executing: the ledger records every attempt durably.
	recorded, err := s.work.AppendRecoveryAction(ctx, action)
	if err != nil {
		return RecoverResult{}, err
	}

	switch p.ActionType {
	case RecoveryReceiptFinalize:
		// Finalize only a PROVEN receipt — never issue a new command.
		committed, err := s.resolveLastCompensation(ctx, op, *w)
		if err != nil {
			return RecoverResult{OperationID: p.OperationID, Action: recorded}, err
		}
		if !committed {
			// No proven receipt: nothing to finalize; re-execution needs a
			// separate approved_compensation action. The ledger row stands.
			return RecoverResult{OperationID: p.OperationID, Action: recorded}, ErrReconciliationRequired
		}
		if err := s.completeRecovered(ctx, *w, WorkflowRejected, errorResult(ErrOperationExpired)); err != nil {
			return RecoverResult{OperationID: p.OperationID, Action: recorded}, s.classifyMiss(ctx, p.OperationID, err)
		}
		return RecoverResult{OperationID: p.OperationID, Terminal: true, State: WorkflowRejected, Action: recorded}, nil
	case RecoveryApprovedCompensation:
		// Explicitly approved compensation through the least-privilege owner
		// ports (resolve-first participants replay partial commits safely).
		if err := s.compensateSideEffects(ctx, op, *w); err != nil {
			// The failure is durable in the ledger; the row stays failed_manual.
			return RecoverResult{OperationID: p.OperationID, Action: recorded}, err
		}
		if err := s.completeRecovered(ctx, *w, WorkflowRejected, errorResult(ErrOperationExpired)); err != nil {
			return RecoverResult{OperationID: p.OperationID, Action: recorded}, s.classifyMiss(ctx, p.OperationID, err)
		}
		return RecoverResult{OperationID: p.OperationID, Terminal: true, State: WorkflowRejected, Action: recorded}, nil
	case RecoveryReconciliation:
		// Decision recorded only; no workflow mutation — the operator can
		// follow up with further actions (all ledgered).
		return RecoverResult{OperationID: p.OperationID, Action: recorded}, nil
	}
	return RecoverResult{OperationID: p.OperationID, Action: recorded}, ErrReconciliationRequired
}

// completeRecovered terminally resolves a failed_manual row AND releases the
// subject exclusion in the same transaction. Recovery never resumes forward
// execution, and a terminal convergence must not leave the exclusion behind
// (failed_manual kept it for reconciliation; it ends here).
func (s *Service) completeRecovered(ctx context.Context, w Workflow, state WorkflowState, result map[string]any) error {
	if w.SubjectUserID == nil {
		return s.work.CompleteRecoveredWorkflow(ctx, w.OperationID, state, result)
	}
	return s.work.RunInTx(ctx, func(tx WorkflowStore) error {
		if err := tx.CompleteRecoveredWorkflow(ctx, w.OperationID, state, result); err != nil {
			return err
		}
		return tx.ReleaseSubjectExclusion(ctx, w.OperationID, *w.SubjectUserID)
	})
}

// isUnknownOutcome reports whether a participant error means "the side
// effect may or may not have committed" (timeout / dependency unavailable)
// as opposed to a proven rejection where no receipt was stored.
func isUnknownOutcome(err error) bool {
	return errors.Is(err, iam.ErrTimeout) || errors.Is(err, organization.ErrTimeout) ||
		errors.Is(err, iam.ErrUnavailable) || errors.Is(err, organization.ErrUnavailable)
}

// mapParticipantError converts an IAM/Organization error into the BFF kind
// the saga finishes with. Sentinels map to their stable BFF equivalents;
// generic participant invalid-input kinds pass through; anything unknown
// becomes KindInternal — never a fabricated public detail. (T057 completes
// the full transport matrix; this is the saga-facing subset.)
func mapParticipantError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, iam.ErrTimeout), errors.Is(err, organization.ErrTimeout):
		return ErrDependencyTimeout
	case errors.Is(err, iam.ErrUnavailable), errors.Is(err, organization.ErrUnavailable):
		return ErrDependencyUnavailable
	case errors.Is(err, organization.ErrCompensationConflict):
		return ErrReconciliationRequired
	case errors.Is(err, organization.ErrMembershipVersionConflict):
		return ErrReconciliationRequired
	case errors.Is(err, iam.ErrVersionConflict):
		return ErrReconciliationRequired
	case errors.Is(err, iam.ErrInvalidLifecycleTransition):
		return ErrReconciliationRequired
	case errors.Is(err, iam.ErrOperationConflict), errors.Is(err, organization.ErrOperationConflict):
		return ErrIdempotencyConflict
	case errors.Is(err, iam.ErrUserNotFound):
		return ErrUserNotFound
	case errors.Is(err, iam.ErrRoleNotFound):
		return NewInvalidInput("one or more roles do not exist", []FieldError{{
			Field: "role_ids", Code: "role_not_found", Message: "a referenced role no longer exists",
		}})
	case errors.Is(err, organization.ErrDepartmentNotFound):
		return NewInvalidInput("department does not exist", []FieldError{{
			Field: "department_id", Code: "department_not_found", Message: "the department was not found",
		}})
	case errors.Is(err, iam.ErrUsernameTaken):
		return NewInvalidInput("username already taken", []FieldError{{
			Field: "username", Code: "username_taken", Message: "the username is already in use",
		}})
	}
	var ie *iam.Error
	if errors.As(err, &ie) && ie.Kind == iam.KindInvalidInput {
		return NewInvalidInput(ie.Message, nil)
	}
	var oe *organization.Error
	if errors.As(err, &oe) && oe.Kind == organization.KindInvalidInput {
		return NewInvalidInput(oe.Message, nil)
	}
	return ErrInternal
}

// errorResult encodes a safe BFF error for the workflow result map. Kinds
// and messages are transport-neutral and secret-free; field details are
// dropped — the message carries the replay detail.
func errorResult(err error) map[string]any {
	var ae *Error
	if errors.As(err, &ae) {
		return map[string]any{"error": map[string]any{"kind": string(ae.Kind), "message": ae.Message}}
	}
	return map[string]any{"error": map[string]any{"kind": string(KindInternal), "message": err.Error()}}
}

// replayError reconstructs the safe stored error of a rejected workflow.
func replayError(w Workflow) error {
	raw, ok := w.Result["error"].(map[string]any)
	if !ok {
		return ErrInternal
	}
	kind, _ := raw["kind"].(string)
	message, _ := raw["message"].(string)
	return &Error{Kind: Kind(kind), Message: message}
}

// replayCreateResult returns the safe stored user id of a succeeded workflow.
func replayCreateResult(w Workflow) CreateUserResult {
	if id, ok := intFromAny(w.Result["user_id"]); ok {
		return CreateUserResult{UserID: id}
	}
	return CreateUserResult{}
}

func intFromAny(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int64:
		return n, true
	case int:
		return int64(n), true
	}
	return 0, false
}

// createResultFromReceipt reconstructs the C3 result from a committed
// receipt (a succeeded receipt always carries subject + version).
func createResultFromReceipt(r iam.IAMCommandReceipt) (iam.CreateUserResult, error) {
	if r.SubjectID == nil || r.ResultingVersion == nil {
		return iam.CreateUserResult{}, ErrInternal
	}
	return iam.CreateUserResult{UserID: *r.SubjectID, ResultingVersion: *r.ResultingVersion}, nil
}

// membershipResultFromReceipt reconstructs the C5 result from a committed
// receipt.
func membershipResultFromReceipt(r organization.OrganizationCommandReceipt) (organization.MembershipMutationResult, error) {
	if r.ResultingMembershipVersion == nil {
		return organization.MembershipMutationResult{}, ErrInternal
	}
	return organization.MembershipMutationResult{
		UserID:                     int64FromPtr(r.SubjectID),
		PreviousDepartmentID:       r.PreviousDepartmentID,
		PreviousMembershipVersion:  r.PreviousMembershipVersion,
		ResultingDepartmentID:      r.ResultingDepartmentID,
		ResultingMembershipVersion: *r.ResultingMembershipVersion,
	}, nil
}

// versionPtr extracts the applied membership version for C6 persistence
// (nil when no membership side effect ran).
func versionPtr(m *organization.MembershipMutationResult) *int64 {
	if m == nil {
		return nil
	}
	v := m.ResultingMembershipVersion
	return &v
}

func int64FromPtr(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func iamOperationContext(op OperationContext) iam.OperationContext {
	return iam.OperationContext{
		OperationID:    op.OperationID,
		IdempotencyKey: op.IdempotencyKey,
		ActorUserID:    op.ActorUserID,
		CorrelationID:  op.CorrelationID,
	}
}

func organizationOperationContext(op OperationContext) organization.OperationContext {
	return organization.OperationContext{
		OperationID:    op.OperationID,
		IdempotencyKey: op.IdempotencyKey,
		ActorUserID:    op.ActorUserID,
		CorrelationID:  op.CorrelationID,
	}
}
