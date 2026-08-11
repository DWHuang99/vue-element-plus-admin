// ActivateUser (task T046; contracts/iam-application.md ActivateUser).
//
// Pins the provisioning -> active CAS on a real PostgreSQL through the public
// constructors: the atomic transition with receipt, same-operation replay,
// typed conflicts for already-active/disabled/stale/missing targets, and the
// operation-conflict guard for a replay whose request differs.
package iam_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hdw/vue-element-plus-admin/backend/internal/iam"
	"github.com/hdw/vue-element-plus-admin/backend/internal/iam/postgres"
)

// activate runs the ActivateUser command with the given operation context.
func activate(t *testing.T, h *harness, opID string, userID, expectedVersion int64) (int64, error) {
	t.Helper()
	return h.svc.ActivateUser(context.Background(), iam.OperationContext{
		OperationID:   opID,
		ActorUserID:   1,
		CorrelationID: "activate-test",
	}, userID, expectedVersion)
}

// TestActivateUser_TransitionsToActive: a provisioning user at version 1
// becomes active at version 2, with the receipt committed in the same
// transaction; the activated credentials now authenticate.
func TestActivateUser_TransitionsToActive(t *testing.T) {
	h := newHarness(t)
	result := provision(t, h, "00000000-0000-0000-0000-00000000a011", "activate_one", []int64{})

	version, err := activate(t, h, "00000000-0000-0000-0000-00000000b001", result.UserID, 1)
	require.NoError(t, err)
	assert.Equal(t, int64(2), version, "provisioning v1 becomes active v2")

	user, err := postgres.NewStore(h.pool).GetUserByID(context.Background(), result.UserID)
	require.NoError(t, err)
	assert.Equal(t, iam.LifecycleActive, user.LifecycleState)
	assert.Equal(t, int64(2), user.Version)

	// receipt records the safe result alongside the transition
	receipt, err := postgres.NewStore(h.pool).GetCommandReceipt(
		context.Background(), "00000000-0000-0000-0000-00000000b001", "activate_user")
	require.NoError(t, err)
	require.NotNil(t, receipt)
	assert.Equal(t, iam.CommandSucceeded, receipt.Status)
	assert.Equal(t, result.UserID, *receipt.SubjectID)
	assert.Equal(t, float64(2), receipt.SafeResult["resulting_version"])
	for key := range receipt.SafeResult {
		assert.NotContains(t, key, "password")
		assert.NotContains(t, key, "hash")
	}

	// the activated identity can log in (provisioning blocked it, T045)
	_, err = h.svc.Login(context.Background(), "activate_one", "initial-secret", opCtx())
	require.NoError(t, err)
}

// TestActivateUser_SameOperationReplays: retry with the same operation ID
// returns the committed resulting version without a second transition.
func TestActivateUser_SameOperationReplays(t *testing.T) {
	h := newHarness(t)
	result := provision(t, h, "00000000-0000-0000-0000-00000000a012", "activate_replay", []int64{})

	first, err := activate(t, h, "00000000-0000-0000-0000-00000000b002", result.UserID, 1)
	require.NoError(t, err)
	second, err := activate(t, h, "00000000-0000-0000-0000-00000000b002", result.UserID, 1)
	require.NoError(t, err)
	assert.Equal(t, first, second, "same operation replays the committed result")

	user, err := postgres.NewStore(h.pool).GetUserByID(context.Background(), result.UserID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), user.Version, "no second transition")
	assert.Equal(t, int64(1), h.countRows(t, "iam_command_receipts WHERE command_name = 'activate_user'"))
}

// TestActivateUser_AlreadyActiveIsInvalidTransition: a user registered active
// by another operation cannot be activated again (re-enable is out of scope).
func TestActivateUser_AlreadyActiveIsInvalidTransition(t *testing.T) {
	h := newHarness(t)
	session := h.register(t, "activate_active", "pw-123456")

	_, err := activate(t, h, "00000000-0000-0000-0000-00000000b003", session.User.UserID, 1)
	require.Error(t, err)
	assert.ErrorIs(t, err, iam.ErrInvalidLifecycleTransition)
}

// TestActivateUser_DisabledIsInvalidTransition: a disabled user cannot be
// activated; activation only accepts provisioning -> active.
func TestActivateUser_DisabledIsInvalidTransition(t *testing.T) {
	h := newHarness(t)
	result := provision(t, h, "00000000-0000-0000-0000-00000000a013", "activate_disabled", []int64{})
	h.setLifecycle(t, result.UserID, "disabled")

	_, err := activate(t, h, "00000000-0000-0000-0000-00000000b004", result.UserID, 1)
	require.Error(t, err)
	assert.ErrorIs(t, err, iam.ErrInvalidLifecycleTransition)
}

// TestActivateUser_StaleVersionConflicts: a stale expected version leaves the
// row untouched and writes no receipt.
func TestActivateUser_StaleVersionConflicts(t *testing.T) {
	h := newHarness(t)
	result := provision(t, h, "00000000-0000-0000-0000-00000000a014", "activate_stale", []int64{})

	_, err := activate(t, h, "00000000-0000-0000-0000-00000000b005", result.UserID, 2)
	require.Error(t, err)
	assert.ErrorIs(t, err, iam.ErrVersionConflict)

	user, err := postgres.NewStore(h.pool).GetUserByID(context.Background(), result.UserID)
	require.NoError(t, err)
	assert.Equal(t, iam.LifecycleProvisioning, user.LifecycleState, "row untouched")
	assert.Equal(t, int64(1), user.Version, "row untouched")
	assert.Equal(t, int64(0), h.countRows(t, "iam_command_receipts WHERE command_name = 'activate_user'"))
}

// TestActivateUser_MissingUser: an unknown target returns UserNotFound with no
// receipt.
func TestActivateUser_MissingUser(t *testing.T) {
	h := newHarness(t)

	_, err := activate(t, h, "00000000-0000-0000-0000-00000000b006", 999999, 1)
	require.Error(t, err)
	assert.ErrorIs(t, err, iam.ErrUserNotFound)
	assert.Equal(t, int64(0), h.countRows(t, "iam_command_receipts WHERE command_name = 'activate_user'"))
}

// TestActivateUser_DifferentRequestOnSameOperation: a replay whose expected
// version differs from the committed evidence is an operation conflict and
// leaves the committed transition untouched.
func TestActivateUser_DifferentRequestOnSameOperation(t *testing.T) {
	h := newHarness(t)
	result := provision(t, h, "00000000-0000-0000-0000-00000000a015", "activate_conflict", []int64{})

	_, err := activate(t, h, "00000000-0000-0000-0000-00000000b007", result.UserID, 1)
	require.NoError(t, err)

	_, err = activate(t, h, "00000000-0000-0000-0000-00000000b007", result.UserID, 99)
	require.Error(t, err)
	assert.ErrorIs(t, err, iam.ErrOperationConflict)

	user, err := postgres.NewStore(h.pool).GetUserByID(context.Background(), result.UserID)
	require.NoError(t, err)
	assert.Equal(t, iam.LifecycleActive, user.LifecycleState, "committed transition untouched")
	assert.Equal(t, int64(2), user.Version)
}
