// Organization application service (US1 + US2 membership core).
//
// US1 implements the read side the Admin BFF composes (department tree,
// per-user/batch membership reads, by-department user listing) plus the
// department mutations the compat suite exercises (save with parent checks,
// full-batch delete preflight). US2 adds the versioned membership mutations
// (Set/Clear/RestoreUserDepartment) with expected-version CAS, receipts and
// the inbox consumer (T036-T040).
//
// Boundary rules: no pgx/sqlc/migrations imports, no IAM queries, and the
// service never verifies IAM user existence — user_id is opaque.
package organization

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/hdw/vue-element-plus-admin/backend/internal/consistency"
)

// Service implements the US1-relevant application ports against a Store.
type Service struct {
	store  Store
	logger *slog.Logger
}

// NewService wires the service to its data-access port. The logger is
// module-scoped by the composition root (T072); it is used for port-call
// observability, never for credentials or payload content.
func NewService(store Store, logger *slog.Logger) *Service {
	return &Service{store: store, logger: logger}
}

// Compile-time guarantee: the concrete service implements the application
// ports it is wired as. US1 covers the reader subset; the full
// MembershipService (versioned mutations) assertion lands with T036.
var (
	_ DepartmentService = (*Service)(nil)
	_ MembershipReader  = (*Service)(nil)
	_ MembershipService = (*Service)(nil)
	_ InboxConsumer     = (*Service)(nil)
	_ ReceiptResolver   = (*Service)(nil)
)

// --- department tree and lookups ---------------------------------------------

// ListDepartmentTree returns the hierarchy in deterministic (id) order with
// Children always a non-nil slice.
func (s *Service) ListDepartmentTree(ctx context.Context) ([]DepartmentNode, error) {
	flat, err := s.store.ListDepartments(ctx)
	if err != nil {
		return nil, err
	}
	return buildTree(flat), nil
}

// GetDepartment returns one department; absence is ErrDepartmentNotFound.
func (s *Service) GetDepartment(ctx context.Context, departmentID int64) (Department, error) {
	return s.store.GetDepartmentByID(ctx, departmentID)
}

// ValidateDepartment: nil means no department and is valid; a missing
// non-nil ID returns DepartmentNotFound.
func (s *Service) ValidateDepartment(ctx context.Context, departmentID *int64) (*Department, error) {
	if departmentID == nil {
		return nil, nil
	}
	dept, err := s.store.GetDepartmentByID(ctx, *departmentID)
	if err != nil {
		return nil, err
	}
	return &dept, nil
}

// SaveDepartment creates (nil id) or updates (with id) a department. The
// parent must exist; a self-parent update is a hierarchy conflict, and
// moving a department under one of its own descendants closes a cycle and is
// rejected by walking the parent chain from the new parent.
func (s *Service) SaveDepartment(ctx context.Context, requestCtx OperationContext,
	id *int64, name string, parentID *int64) (int64, error) {
	if parentID != nil {
		if id != nil && *parentID == *id {
			return 0, fmt.Errorf("%w: department cannot be its own parent", ErrHierarchyConflict)
		}
		// Walk the parent chain from the new parent: if it reaches id, the
		// new parent is inside id's own subtree and the update would close a
		// cycle. The walk also proves the parent exists.
		ancestor, err := s.store.GetDepartmentByID(ctx, *parentID)
		if err != nil {
			return 0, fmt.Errorf("parent department: %w", err)
		}
		for {
			if id != nil && ancestor.ID == *id {
				return 0, fmt.Errorf("%w: department cannot be moved under its own descendant", ErrHierarchyConflict)
			}
			if ancestor.ParentID == nil {
				break
			}
			ancestor, err = s.store.GetDepartmentByID(ctx, *ancestor.ParentID)
			if err != nil {
				return 0, fmt.Errorf("walk parent chain: %w", err)
			}
		}
	}

	if id != nil {
		updated, err := s.store.UpdateDepartment(ctx, *id, name, parentID)
		if err != nil {
			return 0, err
		}
		return updated.ID, nil
	}
	created, err := s.store.CreateDepartment(ctx, name, parentID)
	if err != nil {
		return 0, err
	}
	return created.ID, nil
}

// DeleteDepartments is full-batch: any missing target, child department or
// membership reference rejects the entire batch inside one Organization-local
// transaction (delete protection scans Organization-owned memberships only,
// never IAM users).
func (s *Service) DeleteDepartments(ctx context.Context, requestCtx OperationContext, ids []int64) error {
	return s.store.RunInTx(ctx, func(tx Store) error {
		for _, id := range ids {
			if _, err := tx.GetDepartmentByID(ctx, id); err != nil {
				return err
			}
			children, err := tx.CountDepartmentsByParentID(ctx, id)
			if err != nil {
				return fmt.Errorf("count children: %w", err)
			}
			if children > 0 {
				return fmt.Errorf("%w: department %d still has children", ErrDeleteProtected, id)
			}
			members, err := tx.CountUsersByDepartmentID(ctx, id)
			if err != nil {
				return fmt.Errorf("count members: %w", err)
			}
			if members > 0 {
				return fmt.Errorf("%w: department %d still has members", ErrDeleteProtected, id)
			}
		}
		for _, id := range ids {
			if err := tx.DeleteDepartment(ctx, id); err != nil {
				return fmt.Errorf("delete department: %w", err)
			}
		}
		return nil
	})
}

// --- membership reads (US1 subset) -------------------------------------------

// GetUserDepartment: a missing state row is a normal empty result (nil), not
// an error; a null-department row is the durable no-department state. IAM
// user existence is never verified here.
func (s *Service) GetUserDepartment(ctx context.Context, userID int64) (*MembershipState, error) {
	return s.store.GetUserMembershipByUserID(ctx, userID)
}

// BatchGetUserDepartments: one bounded query set, no per-user queries; users
// with no state row are absent from the result map.
func (s *Service) BatchGetUserDepartments(ctx context.Context, userIDs []int64) (map[int64]MembershipState, error) {
	if len(userIDs) == 0 {
		return map[int64]MembershipState{}, nil
	}
	return s.store.BatchGetUserMemberships(ctx, userIDs)
}

// ListUserIDsByDepartment returns the complete deterministic ID set.
func (s *Service) ListUserIDsByDepartment(ctx context.Context, departmentID int64) ([]int64, error) {
	// A missing department is a typed error, never a silent empty list: the
	// Admin BFF maps it to 404, and a caller cannot distinguish "department
	// has no members" from "department never existed" otherwise.
	if _, err := s.store.GetDepartmentByID(ctx, departmentID); err != nil {
		return nil, err
	}
	return s.store.ListUserIDsByDepartment(ctx, departmentID)
}

// --- membership mutations (US2, T036) ----------------------------------------

// SetUserDepartment: create expects no membership (expected nil); replace
// expects the current version (CAS). Stale expectations return
// MembershipVersionConflict with no state change; re-setting the same
// department is idempotent and keeps the version. The decision runs inside
// one Organization-local transaction under the row lock.
func (s *Service) SetUserDepartment(ctx context.Context, requestCtx OperationContext,
	userID, departmentID int64, expectedMembershipVersion *int64) (MembershipMutationResult, error) {
	const commandName = "set_user_department"
	fingerprint, err := consistency.FingerprintV1(commandName, requestCtx.ActorUserID, map[string]any{
		"user_id":                     userID,
		"department_id":               departmentID,
		"expected_membership_version": consistency.OptInt(expectedMembershipVersion),
	})
	if err != nil {
		return MembershipMutationResult{}, err
	}

	var result MembershipMutationResult
	err = s.store.RunInTx(ctx, func(tx Store) error {
		// Resolve-first: a previously committed operation replays its stored
		// result instead of re-running CAS (crash/timeout retry path). A
		// replay whose request differs from the committed evidence is an
		// operation conflict, never a re-application.
		committed, err := tx.GetCommandReceipt(ctx, requestCtx.OperationID, commandName)
		if err != nil {
			return err
		}
		if committed != nil {
			if committed.RequestFingerprint != fingerprint {
				return fmt.Errorf("%w: replay fingerprint does not match committed receipt", ErrOperationConflict)
			}
			result = receiptMutationResult(*committed, userID)
			return nil
		}

		cur, err := tx.LockMembershipByUserID(ctx, userID)
		if err != nil {
			return err
		}
		if _, err := tx.GetDepartmentByID(ctx, departmentID); err != nil {
			return fmt.Errorf("membership department: %w", err)
		}

		switch {
		case cur == nil && expectedMembershipVersion == nil:
			// create: no previous state, version starts at 1
			if _, err := tx.CreateMembership(ctx, userID, &departmentID, 1); err != nil {
				return err
			}
			result = MembershipMutationResult{
				UserID: userID, ResultingDepartmentID: &departmentID, ResultingMembershipVersion: 1,
			}
		case cur != nil && expectedMembershipVersion == nil && cur.DepartmentID == nil:
			// adopt the bridge tombstone: users_insert_bridge creates a
			// NULL-department version-1 state row for every new IAM user
			// (legacy users.department_id compatibility, migration 000008),
			// so "create expects no membership" is satisfied by the
			// no-department tombstone. The department is set in place and
			// the tombstone version becomes the row's first versioned
			// assignment — the same version the create path produces. An
			// assigned department is still a conflict, never an overwrite.
			if _, err := tx.UpdateMembership(ctx, userID, &departmentID, cur.MembershipVersion); err != nil {
				return err
			}
			result = MembershipMutationResult{
				UserID:                     userID,
				PreviousDepartmentID:       cur.DepartmentID,
				PreviousMembershipVersion:  &cur.MembershipVersion,
				ResultingDepartmentID:      &departmentID,
				ResultingMembershipVersion: cur.MembershipVersion,
			}
		case cur == nil || expectedMembershipVersion == nil ||
			*expectedMembershipVersion != cur.MembershipVersion:
			// stale: expected absence vs existing state, expected presence vs
			// no state, or version mismatch — nothing changes
			return fmt.Errorf("%w: user %d: expected version %s, current %s",
				ErrMembershipVersionConflict, userID, versionText(expectedMembershipVersion), versionText(versionOf(cur)))
		case cur.DepartmentID != nil && *cur.DepartmentID == departmentID:
			// idempotent re-set of the same department: no version bump
			result = MembershipMutationResult{
				UserID:                     userID,
				PreviousDepartmentID:       cur.DepartmentID,
				PreviousMembershipVersion:  &cur.MembershipVersion,
				ResultingDepartmentID:      cur.DepartmentID,
				ResultingMembershipVersion: cur.MembershipVersion,
			}
		default:
			// replace: previous captured, version increments
			next := cur.MembershipVersion + 1
			if _, err := tx.UpdateMembership(ctx, userID, &departmentID, next); err != nil {
				return err
			}
			result = MembershipMutationResult{
				UserID:                     userID,
				PreviousDepartmentID:       cur.DepartmentID,
				PreviousMembershipVersion:  &cur.MembershipVersion,
				ResultingDepartmentID:      &departmentID,
				ResultingMembershipVersion: next,
			}
		}
		// Commit the evidence in the same transaction as the side effect.
		return tx.SaveCommandReceipt(ctx, OrganizationCommandReceipt{
			OperationID:                requestCtx.OperationID,
			CommandName:                commandName,
			RequestFingerprint:         fingerprint,
			Status:                     CommandSucceeded,
			SubjectID:                  &userID,
			PreviousDepartmentID:       result.PreviousDepartmentID,
			PreviousMembershipVersion:  result.PreviousMembershipVersion,
			ResultingDepartmentID:      result.ResultingDepartmentID,
			ResultingMembershipVersion: &result.ResultingMembershipVersion,
		})
	})
	if err != nil {
		return MembershipMutationResult{}, err
	}
	return result, nil
}

// ClearUserDepartment never deletes the version-bearing state row; it
// creates (fresh user) or rewrites a null-department tombstone under
// expected-state CAS. A stale expectation leaves the state untouched.
func (s *Service) ClearUserDepartment(ctx context.Context, requestCtx OperationContext,
	userID int64, expectedMembershipVersion *int64) (MembershipMutationResult, error) {
	const commandName = "clear_user_department"
	fingerprint, err := consistency.FingerprintV1(commandName, requestCtx.ActorUserID, map[string]any{
		"user_id":                     userID,
		"expected_membership_version": consistency.OptInt(expectedMembershipVersion),
	})
	if err != nil {
		return MembershipMutationResult{}, err
	}

	var result MembershipMutationResult
	err = s.store.RunInTx(ctx, func(tx Store) error {
		// Resolve-first: same replay contract as SetUserDepartment.
		committed, err := tx.GetCommandReceipt(ctx, requestCtx.OperationID, commandName)
		if err != nil {
			return err
		}
		if committed != nil {
			if committed.RequestFingerprint != fingerprint {
				return fmt.Errorf("%w: replay fingerprint does not match committed receipt", ErrOperationConflict)
			}
			result = receiptMutationResult(*committed, userID)
			return nil
		}

		cur, err := tx.LockMembershipByUserID(ctx, userID)
		if err != nil {
			return err
		}
		switch {
		case cur == nil && expectedMembershipVersion == nil:
			// never-established user: create the null-department tombstone v1
			if _, err := tx.CreateMembership(ctx, userID, nil, 1); err != nil {
				return err
			}
			result = MembershipMutationResult{UserID: userID, ResultingMembershipVersion: 1}
		case cur == nil || expectedMembershipVersion == nil ||
			*expectedMembershipVersion != cur.MembershipVersion:
			return fmt.Errorf("%w: user %d: expected version %s, current %s",
				ErrMembershipVersionConflict, userID, versionText(expectedMembershipVersion), versionText(versionOf(cur)))
		default:
			next := cur.MembershipVersion + 1
			if _, err := tx.UpdateMembership(ctx, userID, nil, next); err != nil {
				return err
			}
			result = MembershipMutationResult{
				UserID:                     userID,
				PreviousDepartmentID:       cur.DepartmentID,
				PreviousMembershipVersion:  &cur.MembershipVersion,
				ResultingMembershipVersion: next,
			}
		}
		// Commit the evidence in the same transaction as the side effect.
		return tx.SaveCommandReceipt(ctx, OrganizationCommandReceipt{
			OperationID:                requestCtx.OperationID,
			CommandName:                commandName,
			RequestFingerprint:         fingerprint,
			Status:                     CommandSucceeded,
			SubjectID:                  &userID,
			PreviousDepartmentID:       result.PreviousDepartmentID,
			PreviousMembershipVersion:  result.PreviousMembershipVersion,
			ResultingDepartmentID:      result.ResultingDepartmentID,
			ResultingMembershipVersion: &result.ResultingMembershipVersion,
		})
	})
	if err != nil {
		return MembershipMutationResult{}, err
	}
	return result, nil
}

// RestoreUserDepartment proceeds only when the current membership still
// equals the workflow's applied version/state; otherwise it returns
// CompensationConflict and never overwrites newer membership. A null
// previous value restores the cleared (tombstone) state; a non-null previous
// department is validated to still exist before it is restored.
func (s *Service) RestoreUserDepartment(ctx context.Context, requestCtx OperationContext,
	userID int64, expectedCurrentMembershipVersion int64,
	previousDepartmentID *int64, previousMembershipVersion *int64) (MembershipMutationResult, error) {
	const commandName = "restore_user_department"
	fingerprint, err := consistency.FingerprintV1(commandName, requestCtx.ActorUserID, map[string]any{
		"user_id":                             userID,
		"expected_current_membership_version": expectedCurrentMembershipVersion,
		"previous_department_id":              consistency.OptInt(previousDepartmentID),
		"previous_membership_version":         consistency.OptInt(previousMembershipVersion),
	})
	if err != nil {
		return MembershipMutationResult{}, err
	}

	var result MembershipMutationResult
	err = s.store.RunInTx(ctx, func(tx Store) error {
		// Resolve-first: same replay contract as SetUserDepartment.
		committed, err := tx.GetCommandReceipt(ctx, requestCtx.OperationID, commandName)
		if err != nil {
			return err
		}
		if committed != nil {
			if committed.RequestFingerprint != fingerprint {
				return fmt.Errorf("%w: replay fingerprint does not match committed receipt", ErrOperationConflict)
			}
			result = receiptMutationResult(*committed, userID)
			return nil
		}

		cur, err := tx.LockMembershipByUserID(ctx, userID)
		if err != nil {
			return err
		}
		if cur == nil || cur.MembershipVersion != expectedCurrentMembershipVersion {
			return fmt.Errorf("%w: user %d: current version %s, workflow applied %d",
				ErrCompensationConflict, userID, versionText(versionOf(cur)), expectedCurrentMembershipVersion)
		}
		if previousDepartmentID != nil {
			if _, err := tx.GetDepartmentByID(ctx, *previousDepartmentID); err != nil {
				if errors.Is(err, ErrDepartmentNotFound) {
					return fmt.Errorf("%w: previous department %d no longer exists",
						ErrCompensationConflict, *previousDepartmentID)
				}
				return err
			}
		}
		next := cur.MembershipVersion + 1
		if _, err := tx.UpdateMembership(ctx, userID, previousDepartmentID, next); err != nil {
			return err
		}
		result = MembershipMutationResult{
			UserID:                     userID,
			PreviousDepartmentID:       cur.DepartmentID,
			PreviousMembershipVersion:  &cur.MembershipVersion,
			ResultingDepartmentID:      previousDepartmentID,
			ResultingMembershipVersion: next,
		}
		// Commit the evidence in the same transaction as the side effect.
		return tx.SaveCommandReceipt(ctx, OrganizationCommandReceipt{
			OperationID:                requestCtx.OperationID,
			CommandName:                commandName,
			RequestFingerprint:         fingerprint,
			Status:                     CommandSucceeded,
			SubjectID:                  &userID,
			PreviousDepartmentID:       result.PreviousDepartmentID,
			PreviousMembershipVersion:  result.PreviousMembershipVersion,
			ResultingDepartmentID:      result.ResultingDepartmentID,
			ResultingMembershipVersion: &result.ResultingMembershipVersion,
		})
	})
	if err != nil {
		return MembershipMutationResult{}, err
	}
	return result, nil
}

// ResolveCommand resolves a previously committed command after timeout or
// crash: absent returns (nil, nil) — resolution never fabricates a failure;
// a fingerprint mismatch is OperationConflict.
func (s *Service) ResolveCommand(ctx context.Context, operationID, commandName, expectedFingerprint string) (*OrganizationCommandReceipt, error) {
	receipt, err := s.store.GetCommandReceipt(ctx, operationID, commandName)
	if err != nil {
		return nil, err
	}
	if receipt == nil {
		return nil, nil
	}
	if receipt.RequestFingerprint != expectedFingerprint {
		return nil, fmt.Errorf("%w: fingerprint %q does not match committed %q",
			ErrOperationConflict, expectedFingerprint, receipt.RequestFingerprint)
	}
	return receipt, nil
}

// --- terminal user-deleted consumption (T039/T040) -----------------------------

// ClearMembershipsForUsers is the local full-batch, idempotent cleanup:
// terminal IAM user deletion normally flows through the inbox consumer; this
// port covers reconciliation paths that already hold the authoritative user
// list. Missing state rows are a normal no-op.
func (s *Service) ClearMembershipsForUsers(ctx context.Context, requestCtx OperationContext, userIDs []int64) error {
	return s.store.RunInTx(ctx, func(tx Store) error {
		return tx.DeleteMembershipsForUsers(ctx, userIDs)
	})
}

// inboxHandlerName is the dedupe key for the iam.user.deleted consumer
// (domain-events.md).
const inboxHandlerName = "remove-membership-on-user-deleted"

// HandleIAMUserDeletedV1 consumes the terminal iam.user.deleted event: the
// membership state row is physically removed and the successful dedupe row
// commits in the same Organization transaction (a failure rolls both back).
// Duplicate delivery is an idempotent success; invalid envelopes and
// unsupported versions return typed errors and record nothing.
//
// Inbox observability counters (T066) are cumulative and never change handler
// semantics: processed/duplicates commit atomically with their outcome; every
// non-nil result additionally bumps handler-failures (best-effort, after the
// rollback).
func (s *Service) HandleIAMUserDeletedV1(ctx context.Context, event IAMUserDeletedEvent) (retErr error) {
	defer func() {
		if retErr != nil {
			s.countInboxMetric(ctx, inboxMetricHandlerFailures, 1)
		}
	}()
	if event.EventVersion != 1 {
		return fmt.Errorf("%w: iam.user.deleted version %d", ErrUnsupportedEventVersion, event.EventVersion)
	}
	if err := validateUserDeletedEnvelope(event); err != nil {
		return err
	}
	return s.store.RunInTx(ctx, func(tx Store) error {
		// Idempotent dedupe: already processed successfully → replay success.
		processed, err := tx.InboxMessageExists(ctx, event.EventID, inboxHandlerName)
		if err != nil {
			return err
		}
		if processed {
			return tx.IncrementInboxMetric(ctx, inboxMetricDuplicates, 1)
		}
		// Side effect: remove the membership state row (absent row = success).
		if err := tx.DeleteMembershipsForUsers(ctx, []int64{event.UserID}); err != nil {
			return err
		}
		// Successful dedupe evidence in the same transaction.
		if err := tx.SaveInboxMessage(ctx, event.EventID, inboxHandlerName,
			event.EventType, event.EventVersion, event.AggregateID, event.AggregateVersion); err != nil {
			return err
		}
		return tx.IncrementInboxMetric(ctx, inboxMetricProcessed, 1)
	})
}

// countInboxMetric bumps a cumulative observability counter; a counter
// failure never changes handler semantics, so it is swallowed.
func (s *Service) countInboxMetric(ctx context.Context, key string, delta int64) {
	if err := s.store.IncrementInboxMetric(ctx, key, delta); err != nil {
		// observability is best-effort
		_ = err
	}
}

// Inbox observability metric keys (contracts/domain-events.md §Required
// observability): inbox_processed = first successful handling of an event;
// inbox_duplicates = idempotent replays (no side effect re-applied);
// inbox_handler_failures = every non-nil handler result.
const (
	inboxMetricProcessed       = "inbox_processed"
	inboxMetricDuplicates      = "inbox_duplicates"
	inboxMetricHandlerFailures = "inbox_handler_failures"
)

// validateUserDeletedEnvelope rejects envelopes that do not describe the IAM
// user aggregate. The version check happens before this (unsupported
// versions are their own error kind).
func validateUserDeletedEnvelope(event IAMUserDeletedEvent) error {
	var fields []FieldError
	if event.Producer != "iam" {
		fields = append(fields, FieldError{Field: "producer", Code: "invalid", Message: "must be iam"})
	}
	if event.EventType != "iam.user.deleted" {
		fields = append(fields, FieldError{Field: "event_type", Code: "invalid", Message: "must be iam.user.deleted"})
	}
	if event.AggregateType != "user" {
		fields = append(fields, FieldError{Field: "aggregate_type", Code: "invalid", Message: "must be user"})
	}
	if event.AggregateVersion <= 0 {
		fields = append(fields, FieldError{Field: "aggregate_version", Code: "invalid", Message: "must be positive"})
	}
	if want := strconv.FormatInt(event.UserID, 10); event.AggregateID != want {
		fields = append(fields, FieldError{Field: "aggregate_id", Code: "invalid", Message: "must match user_id"})
	}
	if len(fields) > 0 {
		return NewInvalidInput("invalid iam.user.deleted envelope", fields)
	}
	return nil
}

// --- helpers -----------------------------------------------------------------

// versionOf renders the current membership version for conflict messages;
// nil when no state row exists (the state was never established).
func versionOf(st *MembershipState) *int64 {
	if st == nil {
		return nil
	}
	return &st.MembershipVersion
}

// versionText renders a version pointer for conflict messages.
func versionText(v *int64) string {
	if v == nil {
		return "none"
	}
	return fmt.Sprintf("%d", *v)
}

// receiptMutationResult maps a committed receipt back to the mutation result
// it recorded (replay path).
func receiptMutationResult(r OrganizationCommandReceipt, fallbackUserID int64) MembershipMutationResult {
	result := MembershipMutationResult{UserID: fallbackUserID}
	if r.SubjectID != nil {
		result.UserID = *r.SubjectID
	}
	result.PreviousDepartmentID = r.PreviousDepartmentID
	result.PreviousMembershipVersion = r.PreviousMembershipVersion
	result.ResultingDepartmentID = r.ResultingDepartmentID
	if r.ResultingMembershipVersion != nil {
		result.ResultingMembershipVersion = *r.ResultingMembershipVersion
	}
	return result
}

// buildTree assembles the flat deterministic (id-ordered) list into a tree.
// Children is never nil.
func buildTree(flat []Department) []DepartmentNode {
	byParent := make(map[int64][]DepartmentNode, len(flat))
	for _, d := range flat {
		node := DepartmentNode{Department: d, Children: []DepartmentNode{}}
		if d.ParentID != nil {
			byParent[*d.ParentID] = append(byParent[*d.ParentID], node)
		} else {
			byParent[0] = append(byParent[0], node)
		}
	}
	var attach func(nodes []DepartmentNode) []DepartmentNode
	attach = func(nodes []DepartmentNode) []DepartmentNode {
		out := make([]DepartmentNode, 0, len(nodes))
		for _, n := range nodes {
			n.Children = attach(byParent[n.ID])
			out = append(out, n)
		}
		return out
	}
	return attach(byParent[0])
}
