// UpdateManagedUser (task T047; contracts/iam-application.md UpdateManagedUser).
//
// Pins the version-gated profile/credential/role update on a real PostgreSQL
// through the public constructors: atomic full update with receipt, blank
// password preserving the hash, stale/missing/missing-role/username conflicts
// rejecting without partial state, and idempotent replay by operation ID.
package iam_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hdw/vue-element-plus-admin/backend/internal/iam"
	"github.com/hdw/vue-element-plus-admin/backend/internal/iam/postgres"
)

// ctx is the shared background context for store-level assertions.
var ctx = context.Background()

// updateManaged runs the UpdateManagedUser command with the given operation
// context and an optional new password (nil preserves the existing hash).
func updateManaged(t *testing.T, h *harness, opID string, userID, expectedVersion int64,
	username string, account, email, password *string, roleIDs []int64) (int64, error) {
	t.Helper()
	return h.svc.UpdateManagedUser(context.Background(), iam.OperationContext{
		OperationID:   opID,
		ActorUserID:   1,
		CorrelationID: "update-managed-test",
	}, userID, expectedVersion, username, account, email, password, roleIDs)
}

// TestUpdateManagedUser_CommitsAtomically: profile, credential and role
// replacement land together with the receipt; the old credential stops
// working, the new one authenticates, and the receipt carries no secret keys.
func TestUpdateManagedUser_CommitsAtomically(t *testing.T) {
	h := newHarness(t)
	roleA := seedRole(t, h, "upd_role_a", "upd_role_a_code")
	roleB := seedRole(t, h, "upd_role_b", "upd_role_b_code")
	store := postgres.NewStore(h.pool)
	result := provision(t, h, "00000000-0000-0000-0000-00000000a021", "update_one", []int64{roleA})
	// login requires the active state, so the subject is activated first (v2)
	_, err := activate(t, h, "00000000-0000-0000-0000-00000000b021", result.UserID, 1)
	require.NoError(t, err)

	version, err := updateManaged(t, h, "00000000-0000-0000-0000-00000000c001", result.UserID, 2,
		"update_one_renamed", strPtr("new-account"), strPtr("new@example.com"), strPtr("new-secret"),
		[]int64{roleB})
	require.NoError(t, err)
	assert.Equal(t, int64(3), version)

	user, err := store.GetUserByID(ctx, result.UserID)
	require.NoError(t, err)
	assert.Equal(t, "update_one_renamed", user.Username)
	assert.Equal(t, "new-account", *user.Account)
	assert.Equal(t, "new@example.com", *user.Email)
	assert.NotEqual(t, "new-secret", user.PasswordHash, "never plaintext")

	// old credential no longer authenticates, new one does
	_, err = h.svc.Login(ctx, "update_one", "initial-secret", opCtx())
	require.Error(t, err)
	assert.ErrorIs(t, err, iam.ErrInvalidCredentials)
	_, err = h.svc.Login(ctx, "update_one_renamed", "new-secret", opCtx())
	require.NoError(t, err)

	// roles replaced, not appended
	roles, err := store.ListRolesByUserID(ctx, result.UserID)
	require.NoError(t, err)
	require.Len(t, roles, 1)
	assert.Equal(t, roleB, roles[0].ID)

	// receipt committed with the safe result, no credential keys
	receipt, err := store.GetCommandReceipt(ctx, "00000000-0000-0000-0000-00000000c001", "update_managed_user")
	require.NoError(t, err)
	require.NotNil(t, receipt)
	assert.Equal(t, iam.CommandSucceeded, receipt.Status)
	assert.Equal(t, result.UserID, *receipt.SubjectID)
	assert.Equal(t, float64(3), receipt.SafeResult["resulting_version"])
	for key := range receipt.SafeResult {
		assert.NotContains(t, key, "password")
		assert.NotContains(t, key, "hash")
	}
}

// TestUpdateManagedUser_BlankPasswordPreservesHash: absent or blank password
// leaves the stored hash untouched and the old credential authenticates.
func TestUpdateManagedUser_BlankPasswordPreservesHash(t *testing.T) {
	h := newHarness(t)
	role := seedRole(t, h, "upd_preserve_role", "upd_preserve_role_code")
	store := postgres.NewStore(h.pool)
	result := provision(t, h, "00000000-0000-0000-0000-00000000a022", "update_preserve", []int64{role})
	// login requires the active state, so the subject is activated first (v2)
	_, err := activate(t, h, "00000000-0000-0000-0000-00000000b022", result.UserID, 1)
	require.NoError(t, err)

	before, err := store.GetUserByID(ctx, result.UserID)
	require.NoError(t, err)

	// absent password
	version, err := updateManaged(t, h, "00000000-0000-0000-0000-00000000c002", result.UserID, 2,
		"update_preserve", strPtr("acct2"), nil, nil, []int64{role})
	require.NoError(t, err)
	assert.Equal(t, int64(3), version)
	after, err := store.GetUserByID(ctx, result.UserID)
	require.NoError(t, err)
	assert.Equal(t, before.PasswordHash, after.PasswordHash, "absent password preserves the hash")

	// blank password (empty string) also preserves
	version, err = updateManaged(t, h, "00000000-0000-0000-0000-00000000c003", result.UserID, 3,
		"update_preserve", nil, nil, strPtr(""), []int64{role})
	require.NoError(t, err)
	assert.Equal(t, int64(4), version)
	final, err := store.GetUserByID(ctx, result.UserID)
	require.NoError(t, err)
	assert.Equal(t, before.PasswordHash, final.PasswordHash, "blank password preserves the hash")

	_, err = h.svc.Login(ctx, "update_preserve", "initial-secret", opCtx())
	require.NoError(t, err)
}

// TestUpdateManagedUser_StaleVersionConflicts: a stale expected version rejects
// the whole update, leaving the row untouched and writing no receipt.
func TestUpdateManagedUser_StaleVersionConflicts(t *testing.T) {
	h := newHarness(t)
	role := seedRole(t, h, "upd_stale_role", "upd_stale_role_code")
	store := postgres.NewStore(h.pool)
	result := provision(t, h, "00000000-0000-0000-0000-00000000a023", "update_stale", []int64{role})

	_, err := updateManaged(t, h, "00000000-0000-0000-0000-00000000c004", result.UserID, 2,
		"update_stale_renamed", nil, nil, nil, []int64{role})
	require.Error(t, err)
	assert.ErrorIs(t, err, iam.ErrVersionConflict)

	user, err := store.GetUserByID(ctx, result.UserID)
	require.NoError(t, err)
	assert.Equal(t, "update_stale", user.Username, "row untouched")
	assert.Equal(t, int64(1), user.Version, "row untouched")
	assert.Equal(t, int64(0), h.countRows(t, "iam_command_receipts WHERE command_name = 'update_managed_user'"))
}

// TestUpdateManagedUser_MissingUser: an unknown target returns UserNotFound
// with no receipt.
func TestUpdateManagedUser_MissingUser(t *testing.T) {
	h := newHarness(t)

	_, err := updateManaged(t, h, "00000000-0000-0000-0000-00000000c005", 999999, 1,
		"update_missing", nil, nil, nil, []int64{})
	require.Error(t, err)
	assert.ErrorIs(t, err, iam.ErrUserNotFound)
	assert.Equal(t, int64(0), h.countRows(t, "iam_command_receipts WHERE command_name = 'update_managed_user'"))
}

// TestUpdateManagedUser_MissingRoleRejectsWholeBatch: one missing role leaves
// profile, version, roles and receipts untouched.
func TestUpdateManagedUser_MissingRoleRejectsWholeBatch(t *testing.T) {
	h := newHarness(t)
	role := seedRole(t, h, "upd_batch_role", "upd_batch_role_code")
	store := postgres.NewStore(h.pool)
	result := provision(t, h, "00000000-0000-0000-0000-00000000a024", "update_batch", []int64{role})

	_, err := updateManaged(t, h, "00000000-0000-0000-0000-00000000c006", result.UserID, 1,
		"update_batch_renamed", nil, nil, nil, []int64{role, 999999})
	require.Error(t, err)
	assert.ErrorIs(t, err, iam.ErrRoleNotFound)

	user, err := store.GetUserByID(ctx, result.UserID)
	require.NoError(t, err)
	assert.Equal(t, "update_batch", user.Username, "profile untouched")
	assert.Equal(t, int64(1), user.Version, "version untouched")
	roles, err := store.ListRolesByUserID(ctx, result.UserID)
	require.NoError(t, err)
	require.Len(t, roles, 1)
	assert.Equal(t, role, roles[0].ID, "roles untouched")
	assert.Equal(t, int64(0), h.countRows(t, "iam_command_receipts WHERE command_name = 'update_managed_user'"))
}

// TestUpdateManagedUser_UsernameConflict: renaming onto another user's username
// returns UsernameTaken with the row untouched.
func TestUpdateManagedUser_UsernameConflict(t *testing.T) {
	h := newHarness(t)
	role := seedRole(t, h, "upd_taken_role", "upd_taken_role_code")
	store := postgres.NewStore(h.pool)
	result := provision(t, h, "00000000-0000-0000-0000-00000000a025", "update_taken", []int64{role})
	h.register(t, "update_owner", "pw-123456")

	_, err := updateManaged(t, h, "00000000-0000-0000-0000-00000000c007", result.UserID, 1,
		"update_owner", nil, nil, nil, []int64{role})
	require.Error(t, err)
	assert.ErrorIs(t, err, iam.ErrUsernameTaken)

	user, err := store.GetUserByID(ctx, result.UserID)
	require.NoError(t, err)
	assert.Equal(t, "update_taken", user.Username, "row untouched")
	assert.Equal(t, int64(1), user.Version)
}

// TestUpdateManagedUser_SameOperationReplays: retry with the same operation ID
// returns the committed resulting version without a second update.
func TestUpdateManagedUser_SameOperationReplays(t *testing.T) {
	h := newHarness(t)
	role := seedRole(t, h, "upd_replay_role", "upd_replay_role_code")
	store := postgres.NewStore(h.pool)
	result := provision(t, h, "00000000-0000-0000-0000-00000000a026", "update_replay", []int64{role})

	first, err := updateManaged(t, h, "00000000-0000-0000-0000-00000000c008", result.UserID, 1,
		"update_replay2", strPtr("acct"), nil, nil, []int64{role})
	require.NoError(t, err)
	second, err := updateManaged(t, h, "00000000-0000-0000-0000-00000000c008", result.UserID, 1,
		"update_replay2", strPtr("acct"), nil, nil, []int64{role})
	require.NoError(t, err)
	assert.Equal(t, first, second, "same operation replays the committed result")

	user, err := store.GetUserByID(ctx, result.UserID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), user.Version, "no second update")
	assert.Equal(t, int64(1), h.countRows(t, "iam_command_receipts WHERE command_name = 'update_managed_user'"))
}

// TestUpdateManagedUser_DifferentRequestOnSameOperation: a replay whose payload
// differs from the committed evidence is an operation conflict and leaves the
// committed update untouched.
func TestUpdateManagedUser_DifferentRequestOnSameOperation(t *testing.T) {
	h := newHarness(t)
	role := seedRole(t, h, "upd_conflict_role", "upd_conflict_role_code")
	store := postgres.NewStore(h.pool)
	result := provision(t, h, "00000000-0000-0000-0000-00000000a027", "update_conflict", []int64{role})

	_, err := updateManaged(t, h, "00000000-0000-0000-0000-00000000c009", result.UserID, 1,
		"update_conflict2", nil, nil, nil, []int64{role})
	require.NoError(t, err)

	_, err = updateManaged(t, h, "00000000-0000-0000-0000-00000000c009", result.UserID, 1,
		"update_conflict3", nil, nil, nil, []int64{role})
	require.Error(t, err)
	assert.ErrorIs(t, err, iam.ErrOperationConflict)

	user, err := store.GetUserByID(ctx, result.UserID)
	require.NoError(t, err)
	assert.Equal(t, "update_conflict2", user.Username, "committed update untouched")
	assert.Equal(t, int64(2), user.Version)
}

// TestUpdateManagedUser_EmptyRoleSetRemovesAllRoles: role replacement with an
// empty set clears every assignment (deterministic replacement semantics).
func TestUpdateManagedUser_EmptyRoleSetRemovesAllRoles(t *testing.T) {
	h := newHarness(t)
	role := seedRole(t, h, "upd_empty_role", "upd_empty_role_code")
	store := postgres.NewStore(h.pool)
	result := provision(t, h, "00000000-0000-0000-0000-00000000a028", "update_empty", []int64{role})

	version, err := updateManaged(t, h, "00000000-0000-0000-0000-00000000c010", result.UserID, 1,
		"update_empty", nil, nil, nil, []int64{})
	require.NoError(t, err)
	assert.Equal(t, int64(2), version)

	roles, err := store.ListRolesByUserID(ctx, result.UserID)
	require.NoError(t, err)
	assert.Empty(t, roles, "empty role set removes all assignments")
}
