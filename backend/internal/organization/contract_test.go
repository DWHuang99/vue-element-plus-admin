// T033 contract cases (contracts/organization-application.md).
//
// GREEN today (US1 surface): deterministic tree, delete-batch atomicity,
// membership reads, bounded batch queries, no IAM FK.
// RED today (US2, stubs in us2_stubs.go): deep hierarchy cycles, missing-
// department listing error, membership mutations + CAS + receipts, restore
// compensation, inbox consumption. T034-T040 drive these green.
package organization_test

import (
	"context"
	"os"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hdw/vue-element-plus-admin/backend/internal/organization"
)

// TestContractDepartmentTree pins the deterministic hierarchy surface.
func TestContractDepartmentTree(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	a := h.saveDept(t, "A", nil)
	b := h.saveDept(t, "B", &a)
	c := h.saveDept(t, "C", &a)
	d := h.saveDept(t, "D", &b)

	tree, err := h.svc.ListDepartmentTree(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, tree)

	// The migration seeds demo departments; the tree includes them. The
	// contract surface pinned here — determinism + non-nil children — is
	// asserted on the subtree this test owns.
	var root organization.DepartmentNode
	for _, node := range tree {
		if node.ID == a {
			root = node
		}
	}
	require.Equal(t, a, root.ID, "test root present in tree")
	assert.Nil(t, root.ParentID)
	require.Len(t, root.Children, 2, "children ordered deterministically")
	assert.Equal(t, b, root.Children[0].ID)
	assert.Equal(t, c, root.Children[1].ID)
	require.Len(t, root.Children[0].Children, 1)
	assert.Equal(t, d, root.Children[0].Children[0].ID)
	require.NotNil(t, root.Children[1].Children)
	assert.Empty(t, root.Children[1].Children, "leaf children is non-nil empty")
}

// TestContractDepartmentSaveValidation pins hierarchy validation. The deep
// cycle case is RED today (only the direct self-parent is checked).
func TestContractDepartmentSaveValidation(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	a := h.saveDept(t, "A", nil)
	b := h.saveDept(t, "B", &a)
	c := h.saveDept(t, "C", &b)

	// unknown parent rejected
	missing := int64(99999)
	_, err := h.svc.SaveDepartment(ctx, opCtx(1), nil, "X", &missing)
	require.ErrorIs(t, err, organization.ErrDepartmentNotFound)

	// self-parent rejected
	_, err = h.svc.SaveDepartment(ctx, opCtx(2), &a, "A", &a)
	require.ErrorIs(t, err, organization.ErrHierarchyConflict)

	// deep cycle rejected: moving A under its own descendant C would create a
	// cycle that spans three levels — RED until T034 walks the parent chain
	_, err = h.svc.SaveDepartment(ctx, opCtx(3), &a, "A", &c)
	require.ErrorIs(t, err, organization.ErrHierarchyConflict)
}

// TestContractDepartmentDeleteAtomicity pins full-batch preflight semantics.
func TestContractDepartmentDeleteAtomicity(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	a := h.saveDept(t, "A", nil)
	b := h.saveDept(t, "B", &a)

	// a batch of only-missing targets rejects with DepartmentNotFound
	err := h.svc.DeleteDepartments(ctx, opCtx(1), []int64{99998, 99999})
	require.ErrorIs(t, err, organization.ErrDepartmentNotFound)

	// missing alongside an existing target rejects the whole batch; nothing
	// deleted (preflight is per-item in order, so the missing id determines
	// the kind deterministically)
	err = h.svc.DeleteDepartments(ctx, opCtx(2), []int64{99999, a})
	require.ErrorIs(t, err, organization.ErrDepartmentNotFound)
	_, err = h.svc.GetDepartment(ctx, a)
	require.NoError(t, err, "A survives a rejected batch")

	// child department blocks the parent
	err = h.svc.DeleteDepartments(ctx, opCtx(3), []int64{a})
	require.ErrorIs(t, err, organization.ErrDeleteProtected)

	// real membership blocks the department
	h.seedMembership(t, 1, b, 1)
	err = h.svc.DeleteDepartments(ctx, opCtx(4), []int64{b})
	require.ErrorIs(t, err, organization.ErrDeleteProtected)

	// a null-department tombstone alone does NOT protect
	h.seedTombstone(t, 2, 1)
	err = h.svc.DeleteDepartments(ctx, opCtx(5), []int64{b})
	require.ErrorIs(t, err, organization.ErrDeleteProtected, "real member still blocks")

	// removing the real member unblocks; tombstone alone is not a reference
	_, err = h.db.Exec(ctx, "DELETE FROM organization_user_departments WHERE user_id = 1")
	require.NoError(t, err)
	err = h.svc.DeleteDepartments(ctx, opCtx(6), []int64{b})
	require.NoError(t, err)
	_, err = h.svc.GetDepartment(ctx, b)
	require.ErrorIs(t, err, organization.ErrDepartmentNotFound)

	// the now-childless root deletes cleanly
	err = h.svc.DeleteDepartments(ctx, opCtx(7), []int64{a})
	require.NoError(t, err)
}

// TestContractMembershipReads pins the read-side surface. The missing-
// department listing case is RED today (empty list instead of a typed error).
func TestContractMembershipReads(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	deptA := h.saveDept(t, "A", nil)
	deptB := h.saveDept(t, "B", nil)
	h.seedMembership(t, 1, deptA, 1)
	h.seedMembership(t, 2, deptB, 1)
	h.seedTombstone(t, 3, 1)

	// single lookup resolves the department name
	st, err := h.svc.GetUserDepartment(ctx, 1)
	require.NoError(t, err)
	require.NotNil(t, st)
	assert.Equal(t, deptA, *st.DepartmentID)
	assert.Equal(t, int64(1), st.MembershipVersion)
	assert.NotNil(t, st.DepartmentName, "department name resolved for display")

	// missing state row is a normal empty result, not an error
	st, err = h.svc.GetUserDepartment(ctx, 404)
	require.NoError(t, err)
	assert.Nil(t, st)

	// tombstone: state exists, no department
	st, err = h.svc.GetUserDepartment(ctx, 3)
	require.NoError(t, err)
	require.NotNil(t, st)
	assert.Nil(t, st.DepartmentID)
	assert.Equal(t, int64(1), st.MembershipVersion)

	// batch: map presence for existing state, absence for unknown users
	m, err := h.svc.BatchGetUserDepartments(ctx, []int64{1, 2, 404, 3})
	require.NoError(t, err)
	require.Len(t, m, 3)
	assert.Equal(t, deptA, *m[1].DepartmentID)
	assert.Equal(t, deptB, *m[2].DepartmentID)
	_, ok := m[3]
	assert.True(t, ok, "tombstone user present in batch result")
	_, ok = m[404]
	assert.False(t, ok)

	// empty batch → empty map, not an error
	m, err = h.svc.BatchGetUserDepartments(ctx, []int64{})
	require.NoError(t, err)
	assert.Empty(t, m)

	// deterministic ascending listing
	ids, err := h.svc.ListUserIDsByDepartment(ctx, deptA)
	require.NoError(t, err)
	assert.Equal(t, []int64{1}, ids)

	// missing department must be a typed error, not a silent empty list —
	// RED until T035
	_, err = h.svc.ListUserIDsByDepartment(ctx, 99999)
	require.ErrorIs(t, err, organization.ErrDepartmentNotFound)
}

// TestContractMembershipBatchBounds proves bounded query sets (no N+1).
func TestContractMembershipBatchBounds(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	deptA := h.saveDept(t, "A", nil)
	for i := int64(1); i <= 50; i++ {
		h.seedMembership(t, i, deptA, 1)
	}

	// one bounded batch query, no per-user queries
	m, err := h.svc.BatchGetUserDepartments(ctx, []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11})
	require.NoError(t, err)
	require.Len(t, m, 11)
	assert.Equal(t, 1, h.counting.counts["BatchGetUserMemberships"])
	assert.Equal(t, 0, h.counting.counts["GetUserMembershipByUserID"])

	// single lookup uses exactly one query
	_, err = h.svc.GetUserDepartment(ctx, 1)
	require.NoError(t, err)
	assert.Equal(t, 1, h.counting.counts["GetUserMembershipByUserID"])

	// duplicate user ids are normalized without extra queries
	m, err = h.svc.BatchGetUserDepartments(ctx, []int64{1, 1, 2, 2, 3, 3})
	require.NoError(t, err)
	require.Len(t, m, 3)
	assert.Equal(t, 2, h.counting.counts["BatchGetUserMemberships"])

	// listing is one query and does not resolve membership rows per user
	ids, err := h.svc.ListUserIDsByDepartment(ctx, deptA)
	require.NoError(t, err)
	require.Len(t, ids, 50)
	assert.Equal(t, 1, h.counting.counts["ListUserIDsByDepartment"])
	assert.Equal(t, 1, h.counting.counts["GetUserMembershipByUserID"], "listing must not fall back to per-user queries")

	// empty batch short-circuits before the store
	_, err = h.svc.BatchGetUserDepartments(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, h.counting.counts["BatchGetUserMemberships"], "empty batch must not reach the store")
}

// TestContractMembershipMutations pins SetUserDepartment: create, CAS
// replace, stale rejection, idempotent same-department, receipt replay. RED
// until T036/T038.
func TestContractMembershipMutations(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	deptA := h.saveDept(t, "A", nil)
	deptB := h.saveDept(t, "B", nil)
	const user int64 = 1000

	// create: no membership expected, version starts at 1, previous is nil
	r1, err := h.svc.SetUserDepartment(ctx, opCtx(1), user, deptA, nil)
	require.NoError(t, err)
	assert.Equal(t, user, r1.UserID)
	assert.Nil(t, r1.PreviousDepartmentID)
	assert.Nil(t, r1.PreviousMembershipVersion)
	require.NotNil(t, r1.ResultingDepartmentID)
	assert.Equal(t, deptA, *r1.ResultingDepartmentID)
	assert.Equal(t, int64(1), r1.ResultingMembershipVersion)
	got, _ := h.membershipRow(t, user)
	assert.Equal(t, deptA, *got)

	// replace under CAS: previous captured, version increments
	r2, err := h.svc.SetUserDepartment(ctx, opCtx(2), user, deptB, int64ptr(1))
	require.NoError(t, err)
	require.NotNil(t, r2.PreviousDepartmentID)
	assert.Equal(t, deptA, *r2.PreviousDepartmentID)
	assert.Equal(t, int64(1), *r2.PreviousMembershipVersion)
	require.NotNil(t, r2.ResultingDepartmentID)
	assert.Equal(t, deptB, *r2.ResultingDepartmentID)
	assert.Equal(t, int64(2), r2.ResultingMembershipVersion)

	// stale CAS rejected without state change
	_, err = h.svc.SetUserDepartment(ctx, opCtx(3), user, deptA, int64ptr(1))
	require.ErrorIs(t, err, organization.ErrMembershipVersionConflict)
	got, v := h.membershipRow(t, user)
	assert.Equal(t, deptB, *got)
	assert.Equal(t, int64(2), v, "rejected CAS must not change state")

	// nil expected on an existing state is stale (create-then-set)
	_, err = h.svc.SetUserDepartment(ctx, opCtx(4), user, deptA, nil)
	require.ErrorIs(t, err, organization.ErrMembershipVersionConflict)

	// same-department set is idempotent: succeeds, version does NOT increment
	r5, err := h.svc.SetUserDepartment(ctx, opCtx(5), user, deptB, int64ptr(2))
	require.NoError(t, err)
	require.NotNil(t, r5.ResultingDepartmentID)
	assert.Equal(t, deptB, *r5.ResultingDepartmentID)
	assert.Equal(t, int64(2), r5.ResultingMembershipVersion, "idempotent same-department set keeps version")
	_, v = h.membershipRow(t, user)
	assert.Equal(t, int64(2), v)

	// receipt replay: same operation returns the committed result without
	// re-applying (no version bump), even though the CAS input is stale
	r6, err := h.svc.SetUserDepartment(ctx, opCtx(6), user, deptA, int64ptr(2))
	require.NoError(t, err)
	assert.Equal(t, int64(3), r6.ResultingMembershipVersion)
	r7, err := h.svc.SetUserDepartment(ctx, opCtx(6), user, deptA, int64ptr(2))
	require.NoError(t, err)
	assert.Equal(t, r6.ResultingMembershipVersion, r7.ResultingMembershipVersion, "replay returns the committed result")
	_, v = h.membershipRow(t, user)
	assert.Equal(t, int64(3), v, "replay does not re-apply")
}

// TestContractSetUserDepartment_AdoptsBridgeTombstone pins the create path
// against the migration-000008 users_insert_bridge trigger: every new IAM
// user gets a NULL-department version-1 state row, and "create expects no
// membership" must adopt that tombstone instead of conflicting (the T052
// create saga's C5 runs against real bridge-created users).
func TestContractSetUserDepartment_AdoptsBridgeTombstone(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	deptA := h.saveDept(t, "A2", nil)
	deptB := h.saveDept(t, "B2", nil)

	// A fresh IAM user INSERT fires users_insert_bridge: NULL-department
	// tombstone at version 1 (the legacy users.department_id column is NULL).
	const user int64 = 2001
	_, err := h.db.Exec(ctx,
		`INSERT INTO users (id, username, password_hash, lifecycle_state, version) VALUES ($1, 'bridge_user', 'x', 'provisioning', 1)`, user)
	require.NoError(t, err)
	got, v := h.membershipRow(t, user)
	assert.Nil(t, got, "the bridge seeds a NULL-department tombstone")
	assert.Equal(t, int64(1), v)

	// create adopts the tombstone: department set in place, version stays 1
	r1, err := h.svc.SetUserDepartment(ctx, opCtx(1), user, deptA, nil)
	require.NoError(t, err)
	assert.Nil(t, r1.PreviousDepartmentID, "the tombstone has no previous department")
	require.NotNil(t, r1.PreviousMembershipVersion)
	assert.Equal(t, int64(1), *r1.PreviousMembershipVersion)
	require.NotNil(t, r1.ResultingDepartmentID)
	assert.Equal(t, deptA, *r1.ResultingDepartmentID)
	assert.Equal(t, int64(1), r1.ResultingMembershipVersion)
	got, v = h.membershipRow(t, user)
	require.NotNil(t, got)
	assert.Equal(t, deptA, *got)
	assert.Equal(t, int64(1), v, "the tombstone version becomes the first versioned assignment")

	// the assigned state then obeys the normal CAS contract
	r2, err := h.svc.SetUserDepartment(ctx, opCtx(2), user, deptB, int64ptr(1))
	require.NoError(t, err)
	assert.Equal(t, int64(2), r2.ResultingMembershipVersion)
	// expected-absence on an ASSIGNED department is still a conflict
	_, err = h.svc.SetUserDepartment(ctx, opCtx(3), user, deptA, nil)
	require.ErrorIs(t, err, organization.ErrMembershipVersionConflict)
}

// TestContractClearAndRestore pins ClearUserDepartment (null tombstones,
// expected-state CAS) and RestoreUserDepartment (only-on-applied-version,
// compensation conflicts). RED until T036.
func TestContractClearAndRestore(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	deptA := h.saveDept(t, "A", nil)
	deptB := h.saveDept(t, "B", nil)
	const user = 2000

	// clear on a fresh user creates the null-department tombstone at v1
	r1, err := h.svc.ClearUserDepartment(ctx, opCtx(10), user, nil)
	require.NoError(t, err)
	assert.Nil(t, r1.PreviousDepartmentID)
	assert.Nil(t, r1.ResultingDepartmentID)
	assert.Equal(t, int64(1), r1.ResultingMembershipVersion)
	got, v := h.membershipRow(t, user)
	assert.Nil(t, got, "state row retained as null tombstone")
	assert.Equal(t, int64(1), v)

	// set from tombstone under CAS; previous department is null
	r2, err := h.svc.SetUserDepartment(ctx, opCtx(11), user, deptA, int64ptr(1))
	require.NoError(t, err)
	assert.Equal(t, int64(2), r2.ResultingMembershipVersion)
	assert.Nil(t, r2.PreviousDepartmentID, "tombstone previous is null")

	// clear under CAS: previous captured, tombstone at v3
	r3, err := h.svc.ClearUserDepartment(ctx, opCtx(12), user, int64ptr(2))
	require.NoError(t, err)
	require.NotNil(t, r3.PreviousDepartmentID)
	assert.Equal(t, deptA, *r3.PreviousDepartmentID)
	assert.Equal(t, int64(2), *r3.PreviousMembershipVersion)
	assert.Nil(t, r3.ResultingDepartmentID)
	assert.Equal(t, int64(3), r3.ResultingMembershipVersion)
	got, v = h.membershipRow(t, user)
	assert.Nil(t, got)
	assert.Equal(t, int64(3), v)

	// stale expected-absence rejected
	_, err = h.svc.ClearUserDepartment(ctx, opCtx(13), user, nil)
	require.ErrorIs(t, err, organization.ErrMembershipVersionConflict)
	// stale version rejected
	_, err = h.svc.ClearUserDepartment(ctx, opCtx(14), user, int64ptr(1))
	require.ErrorIs(t, err, organization.ErrMembershipVersionConflict)

	// restore: current matches the applied version, previous department restored
	r5, err := h.svc.RestoreUserDepartment(ctx, opCtx(15), user, 3, int64ptr(deptA), int64ptr(2))
	require.NoError(t, err)
	require.NotNil(t, r5.ResultingDepartmentID)
	assert.Equal(t, deptA, *r5.ResultingDepartmentID)
	assert.Equal(t, int64(4), r5.ResultingMembershipVersion)
	got, v = h.membershipRow(t, user)
	assert.Equal(t, deptA, *got)
	assert.Equal(t, int64(4), v)

	// delayed restore: current (v4, dept A) no longer equals the workflow's
	// applied (v3 tombstone) → compensation conflict, nothing overwritten
	_, err = h.svc.RestoreUserDepartment(ctx, opCtx(16), user, 3, int64ptr(deptA), int64ptr(2))
	require.ErrorIs(t, err, organization.ErrCompensationConflict)
	got, v = h.membershipRow(t, user)
	assert.Equal(t, deptA, *got)
	assert.Equal(t, int64(4), v, "conflict must not overwrite newer membership")

	// restore onto an absent state row: current is not a version → conflict
	_, err = h.svc.RestoreUserDepartment(ctx, opCtx(17), 3000, 1, int64ptr(deptB), int64ptr(1))
	require.ErrorIs(t, err, organization.ErrCompensationConflict)

	// restore with a since-deleted previous department → compensation
	// conflict; the workflow must not invent data
	deletedDept := h.saveDept(t, "doomed", nil)
	_, err = h.svc.ClearUserDepartment(ctx, opCtx(18), user, int64ptr(4))
	require.NoError(t, err)
	got, v = h.membershipRow(t, user)
	assert.Nil(t, got)
	assert.Equal(t, int64(5), v)
	err = h.svc.DeleteDepartments(ctx, opCtx(19), []int64{deletedDept})
	require.NoError(t, err)
	_, err = h.svc.RestoreUserDepartment(ctx, opCtx(20), user, 5, int64ptr(deletedDept), int64ptr(1))
	require.ErrorIs(t, err, organization.ErrCompensationConflict)
	got, v = h.membershipRow(t, user)
	assert.Nil(t, got)
	assert.Equal(t, int64(5), v, "failed restore must not touch membership")
}

// TestContractCommandReceipts pins ResolveCommand: absent → nil (never
// fabricated failure), fingerprint match returns the stored receipt,
// mismatch → OperationConflict. RED until T038.
func TestContractCommandReceipts(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// absent → (nil, nil): resolution never fabricates a failure
	r, err := h.svc.ResolveCommand(ctx, opID(30), "set_user_department", "fp-absent")
	require.NoError(t, err)
	assert.Nil(t, r)

	// seeded succeeded receipt, fingerprint match → receipt with stored fields
	op := opID(31)
	_, err = h.db.Exec(ctx, `INSERT INTO organization_command_receipts
		(operation_id, command_name, request_fingerprint, status, subject_id,
		 previous_department_id, previous_membership_version,
		 resulting_department_id, resulting_membership_version, completed_at)
		VALUES ($1, 'set_user_department', 'fp-31', 'succeeded', 1000, 7, 2, 9, 3, now())`, op)
	require.NoError(t, err)

	r, err = h.svc.ResolveCommand(ctx, op, "set_user_department", "fp-31")
	require.NoError(t, err)
	require.NotNil(t, r)
	assert.Equal(t, organization.CommandSucceeded, r.Status)
	assert.Equal(t, int64(1000), *r.SubjectID)
	require.NotNil(t, r.PreviousDepartmentID)
	assert.Equal(t, int64(7), *r.PreviousDepartmentID)
	assert.Equal(t, int64(2), *r.PreviousMembershipVersion)
	require.NotNil(t, r.ResultingDepartmentID)
	assert.Equal(t, int64(9), *r.ResultingDepartmentID)
	assert.Equal(t, int64(3), *r.ResultingMembershipVersion)
	assert.Equal(t, "fp-31", r.RequestFingerprint)

	// fingerprint mismatch → operation conflict
	_, err = h.svc.ResolveCommand(ctx, op, "set_user_department", "stale-fp")
	require.ErrorIs(t, err, organization.ErrOperationConflict)
}

// TestContractInboxConsumer pins HandleIAMUserDeletedV1: envelope validation,
// idempotent dedupe, side-effect + dedupe row in one transaction, physical
// removal of the membership row. RED until T039/T040.
func TestContractInboxConsumer(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	const handler = "remove-membership-on-user-deleted"
	deptA := h.saveDept(t, "A", nil)
	h.seedMembership(t, 4001, deptA, 3)
	h.seedTombstone(t, 4002, 1)

	// valid envelope: membership row physically removed, exactly one dedupe row
	err := h.svc.HandleIAMUserDeletedV1(ctx, deletedEvent(1, 4001))
	require.NoError(t, err)
	assert.Equal(t, int64(0), h.countWhere(t, "organization_user_departments", "user_id = 4001"),
		"terminal user deletion physically removes the membership row")
	assert.Equal(t, int64(1), h.countWhere(t, "organization_inbox_messages",
		"event_id = $1 AND handler_name = $2", uuidEventID(1), handler))

	// duplicate delivery → idempotent success, still one dedupe row
	err = h.svc.HandleIAMUserDeletedV1(ctx, deletedEvent(1, 4001))
	require.NoError(t, err)
	assert.Equal(t, int64(1), h.countWhere(t, "organization_inbox_messages",
		"event_id = $1 AND handler_name = $2", uuidEventID(1), handler))

	// missing state row → success, dedupe row still recorded (no invention,
	// no fabricated failure)
	err = h.svc.HandleIAMUserDeletedV1(ctx, deletedEvent(2, 4001))
	require.NoError(t, err)
	assert.Equal(t, int64(1), h.countWhere(t, "organization_inbox_messages",
		"event_id = $1 AND handler_name = $2", uuidEventID(2), handler))

	// tombstone state is physically removed too
	err = h.svc.HandleIAMUserDeletedV1(ctx, deletedEvent(3, 4002))
	require.NoError(t, err)
	assert.Equal(t, int64(0), h.countWhere(t, "organization_user_departments", "user_id = 4002"))

	// unsupported event version → typed error, nothing recorded
	unsupported := deletedEvent(4, 5000)
	unsupported.EventVersion = 2
	err = h.svc.HandleIAMUserDeletedV1(ctx, unsupported)
	require.ErrorIs(t, err, organization.ErrUnsupportedEventVersion)
	assert.Equal(t, int64(0), h.countWhere(t, "organization_inbox_messages",
		"event_id = $1 AND handler_name = $2", uuidEventID(4), handler))

	// envelope violations → typed invalid input, nothing recorded
	badProducer := deletedEvent(5, 5000)
	badProducer.Producer = "rbac"
	err = h.svc.HandleIAMUserDeletedV1(ctx, badProducer)
	require.ErrorIs(t, err, organization.ErrInvalidInput)
	assert.Equal(t, int64(0), h.countWhere(t, "organization_inbox_messages",
		"event_id = $1 AND handler_name = $2", uuidEventID(5), handler))

	badType := deletedEvent(6, 5000)
	badType.EventType = "iam.user.renamed"
	err = h.svc.HandleIAMUserDeletedV1(ctx, badType)
	require.ErrorIs(t, err, organization.ErrInvalidInput)
	assert.Equal(t, int64(0), h.countWhere(t, "organization_inbox_messages",
		"event_id = $1 AND handler_name = $2", uuidEventID(6), handler))

	badAggregate := deletedEvent(7, 5000)
	badAggregate.AggregateID = "not-the-user-id"
	err = h.svc.HandleIAMUserDeletedV1(ctx, badAggregate)
	require.ErrorIs(t, err, organization.ErrInvalidInput)
	assert.Equal(t, int64(0), h.countWhere(t, "organization_inbox_messages",
		"event_id = $1 AND handler_name = $2", uuidEventID(7), handler))

	// handler failure rolls back the side effect and the dedupe row together
	_, err = h.db.Exec(ctx, `CREATE OR REPLACE FUNCTION contract_fail_inbox_insert() RETURNS trigger AS $$
		BEGIN RAISE EXCEPTION 'injected inbox failure'; END; $$ LANGUAGE plpgsql`)
	require.NoError(t, err)
	_, err = h.db.Exec(ctx, `CREATE TRIGGER contract_fail_inbox_insert_trigger
		BEFORE INSERT ON organization_inbox_messages
		FOR EACH ROW EXECUTE FUNCTION contract_fail_inbox_insert()`)
	require.NoError(t, err)

	h.seedMembership(t, 4003, deptA, 2)
	err = h.svc.HandleIAMUserDeletedV1(ctx, deletedEvent(8, 4003))
	require.Error(t, err)
	assert.Equal(t, int64(1), h.countWhere(t, "organization_user_departments", "user_id = 4003"),
		"side effect must roll back with the failed dedupe insert")
	assert.Equal(t, int64(0), h.countWhere(t, "organization_inbox_messages",
		"event_id = $1 AND handler_name = $2", uuidEventID(8), handler),
		"no successful dedupe row after failure")

	// once the fault clears, redelivery succeeds
	_, err = h.db.Exec(ctx, "DROP TRIGGER contract_fail_inbox_insert_trigger ON organization_inbox_messages")
	require.NoError(t, err)
	err = h.svc.HandleIAMUserDeletedV1(ctx, deletedEvent(8, 4003))
	require.NoError(t, err)
	assert.Equal(t, int64(0), h.countWhere(t, "organization_user_departments", "user_id = 4003"))
	assert.Equal(t, int64(1), h.countWhere(t, "organization_inbox_messages",
		"event_id = $1 AND handler_name = $2", uuidEventID(8), handler))
}

// TestContractNoIAMForeignKey pins the boundary rule: Organization state must
// be reachable from, and must not reach, IAM state through any FK.
func TestContractNoIAMForeignKey(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	rows, err := h.db.Query(ctx, `
		SELECT kcu.table_name, ccu.table_name
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu
			ON kcu.constraint_name = tc.constraint_name AND kcu.table_schema = tc.table_schema
		JOIN information_schema.constraint_column_usage ccu
			ON ccu.constraint_name = tc.constraint_name AND ccu.table_schema = tc.table_schema
		WHERE tc.constraint_type = 'FOREIGN KEY'
		  AND kcu.table_schema = 'public'
		  AND kcu.table_name IN ('organization_user_departments', 'departments',
		  	'organization_command_receipts', 'organization_inbox_messages')`)
	require.NoError(t, err)
	defer rows.Close()

	iamTables := map[string]bool{
		"users": true, "sessions": true, "roles": true, "user_roles": true,
		"permissions": true, "role_permissions": true,
	}
	refs := map[string][]string{}
	for rows.Next() {
		var from, to string
		require.NoError(t, rows.Scan(&from, &to))
		refs[from] = append(refs[from], to)
	}
	require.NoError(t, rows.Err())

	assert.Empty(t, refs["organization_command_receipts"], "receipts are owner-local: no FKs")
	assert.Empty(t, refs["organization_inbox_messages"], "inbox messages are owner-local: no FKs")
	assert.Equal(t, []string{"departments"}, refs["organization_user_departments"],
		"membership may reference only departments")
	for _, to := range refs["departments"] {
		assert.False(t, iamTables[to], "no FK into IAM tables")
	}

	// the Organization SQL surface never reads IAM tables
	re := regexp.MustCompile(`(?i)\b(FROM|JOIN|UPDATE|INTO|TABLE)\s+(public\.)?(users|sessions|roles|user_roles|permissions|role_permissions)\b`)
	for _, file := range []string{
		"../../db/organization/queries/departments.sql",
		"../../db/organization/queries/memberships.sql",
	} {
		content, err := os.ReadFile(file)
		require.NoError(t, err, file)
		assert.Nil(t, re.Find(content), file+" must not touch IAM tables")
	}
}

// TestContractInboxObservability pins the T066 inbox counters: processed /
// duplicates commit atomically with their outcome, handler failures are
// counted on every non-nil result (best-effort, after rollback), and no
// counter ever changes handler semantics.
func TestContractInboxObservability(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	metric := func(key string) int64 {
		var n int64
		require.NoError(t, h.db.QueryRow(ctx,
			"SELECT count FROM organization_inbox_metrics WHERE metric_key = $1", key).Scan(&n))
		return n
	}
	assert.Equal(t, int64(0), metric("inbox_processed"))
	assert.Equal(t, int64(0), metric("inbox_duplicates"))
	assert.Equal(t, int64(0), metric("inbox_handler_failures"))

	// first handling of an event → processed
	err := h.svc.HandleIAMUserDeletedV1(ctx, deletedEvent(100, 4100))
	require.NoError(t, err)
	assert.Equal(t, int64(1), metric("inbox_processed"))
	assert.Equal(t, int64(0), metric("inbox_duplicates"))

	// duplicate delivery → duplicates counted, processed unchanged
	err = h.svc.HandleIAMUserDeletedV1(ctx, deletedEvent(100, 4100))
	require.NoError(t, err)
	assert.Equal(t, int64(1), metric("inbox_processed"))
	assert.Equal(t, int64(1), metric("inbox_duplicates"))

	// missing state row is still a first success → processed
	err = h.svc.HandleIAMUserDeletedV1(ctx, deletedEvent(102, 4200))
	require.NoError(t, err)
	assert.Equal(t, int64(2), metric("inbox_processed"))

	// unsupported event version → handler failure counted, nothing recorded
	unsupported := deletedEvent(103, 4300)
	unsupported.EventVersion = 2
	err = h.svc.HandleIAMUserDeletedV1(ctx, unsupported)
	require.ErrorIs(t, err, organization.ErrUnsupportedEventVersion)
	assert.Equal(t, int64(1), metric("inbox_handler_failures"))
	assert.Equal(t, int64(0), h.countWhere(t, "organization_inbox_messages",
		"event_id = $1 AND handler_name = $2", uuidEventID(103), "remove-membership-on-user-deleted"))

	// handler failure rolls back the side effect; the failure is still counted
	deptA := h.saveDept(t, "Obs", nil)
	h.seedMembership(t, 4400, deptA, 2)
	_, err = h.db.Exec(ctx, `CREATE OR REPLACE FUNCTION contract_fail_inbox_insert() RETURNS trigger AS $$
		BEGIN RAISE EXCEPTION 'injected inbox failure'; END; $$ LANGUAGE plpgsql`)
	require.NoError(t, err)
	_, err = h.db.Exec(ctx, `CREATE TRIGGER contract_fail_inbox_insert_trigger
		BEFORE INSERT ON organization_inbox_messages
		FOR EACH ROW EXECUTE FUNCTION contract_fail_inbox_insert()`)
	require.NoError(t, err)

	err = h.svc.HandleIAMUserDeletedV1(ctx, deletedEvent(104, 4400))
	require.Error(t, err)
	assert.Equal(t, int64(2), metric("inbox_handler_failures"))
	assert.Equal(t, int64(1), h.countWhere(t, "organization_user_departments", "user_id = 4400"),
		"side effect must roll back with the failed dedupe insert")
	assert.Equal(t, int64(2), metric("inbox_processed"), "failed attempt never counts as processed")

	// once the fault clears, redelivery succeeds and is counted as processed
	_, err = h.db.Exec(ctx, "DROP TRIGGER contract_fail_inbox_insert_trigger ON organization_inbox_messages")
	require.NoError(t, err)
	err = h.svc.HandleIAMUserDeletedV1(ctx, deletedEvent(104, 4400))
	require.NoError(t, err)
	assert.Equal(t, int64(3), metric("inbox_processed"))
	assert.Equal(t, int64(1), metric("inbox_duplicates"))
	assert.Equal(t, int64(2), metric("inbox_handler_failures"))
}
