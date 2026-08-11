// DisableUser + CompensateProvisioningUser (task T049;
// contracts/iam-application.md DisableUser / CompensateProvisioningUser,
// contracts/domain-events.md iam.user.deleted).
//
// Pins the lifecycle CAS pair on a real PostgreSQL through the public
// constructors: DisableUser transitions active -> disabled with expected-version
// CAS, revokes every live session and commits the receipt atomically;
// CompensateProvisioningUser deletes exactly a still-provisioning subject —
// never disable — so a failed create can neither surface as a visible disabled
// user nor permanently occupy its username. Both are replayable through their
// receipts, emit no events on miss, and escalate non-compensable states to
// typed conflicts requiring reconciliation.
package iam_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hdw/vue-element-plus-admin/backend/internal/iam"
	"github.com/hdw/vue-element-plus-admin/backend/internal/iam/postgres"
)

// disable runs the DisableUser command with the given operation context.
func disable(t *testing.T, h *harness, opID string, userID, expectedVersion int64, reasonCode string) (int64, error) {
	t.Helper()
	return h.svc.DisableUser(context.Background(), iam.OperationContext{
		OperationID:   opID,
		ActorUserID:   1,
		CorrelationID: "disable-test",
	}, userID, expectedVersion, reasonCode)
}

// compensate runs the CompensateProvisioningUser command with the given
// operation context.
func compensate(t *testing.T, h *harness, opID string, userID, expectedVersion int64) error {
	t.Helper()
	return h.svc.CompensateProvisioningUser(context.Background(), iam.OperationContext{
		OperationID:   opID,
		ActorUserID:   1,
		CorrelationID: "compensate-test",
	}, userID, expectedVersion)
}

// userState reads the current lifecycle/version pair straight from the DB.
func (h *harness) userState(t *testing.T, userID int64) (string, int64) {
	t.Helper()
	var state string
	var version int64
	require.NoError(t, h.db.QueryRow(context.Background(),
		"SELECT lifecycle_state, version FROM users WHERE id = $1", userID).Scan(&state, &version))
	return state, version
}

func (h *harness) liveSessions(t *testing.T, userID int64) int64 {
	t.Helper()
	return h.countRows(t, fmt.Sprintf("sessions WHERE user_id = %d AND revoked_at IS NULL", userID))
}

func (h *harness) revokedSessions(t *testing.T, userID int64) int64 {
	t.Helper()
	return h.countRows(t, fmt.Sprintf("sessions WHERE user_id = %d AND revoked_at IS NOT NULL", userID))
}

// TestDisableUser_RevokesSessionsAndCommits: active v2 -> disabled v3, every
// live session revoked in the same transaction, the old session stops
// authenticating, login fails, and the receipt records the safe result.
func TestDisableUser_RevokesSessionsAndCommits(t *testing.T) {
	h := newHarness(t)
	store := postgres.NewStore(h.pool)
	result := provision(t, h, "00000000-0000-0000-0000-00000000e001", "disable_me", nil)
	_, err := activate(t, h, "00000000-0000-0000-0000-00000000e002", result.UserID, 1)
	require.NoError(t, err)
	session, err := h.svc.Login(context.Background(), "disable_me", "initial-secret", opCtx())
	require.NoError(t, err)
	require.Equal(t, int64(1), h.liveSessions(t, result.UserID))

	version, err := disable(t, h, "00000000-0000-0000-0000-00000000e003", result.UserID, 2, "security.incident")
	require.NoError(t, err)
	assert.Equal(t, int64(3), version)

	state, currentVersion := h.userState(t, result.UserID)
	assert.Equal(t, "disabled", state)
	assert.Equal(t, int64(3), currentVersion)
	assert.Equal(t, int64(0), h.liveSessions(t, result.UserID), "no live session survives")
	assert.Equal(t, int64(1), h.revokedSessions(t, result.UserID), "the session is revoked, not deleted")

	// the revoked session no longer authenticates
	_, err = h.svc.Authenticate(context.Background(), session.Token, opCtx())
	assert.ErrorIs(t, err, iam.ErrInvalidToken)
	// the disabled subject cannot log in anymore
	_, err = h.svc.Login(context.Background(), "disable_me", "initial-secret", opCtx())
	assert.ErrorIs(t, err, iam.ErrInvalidCredentials)

	receipt, err := store.GetCommandReceipt(ctx, "00000000-0000-0000-0000-00000000e003", "disable_user")
	require.NoError(t, err)
	require.NotNil(t, receipt)
	assert.Equal(t, iam.CommandSucceeded, receipt.Status)
	assert.Equal(t, float64(result.UserID), receipt.SafeResult["user_id"])
	assert.Equal(t, float64(3), receipt.SafeResult["resulting_version"])
	_, ok := receipt.SafeResult["reason_code"]
	assert.False(t, ok, "reason_code is a safe classification, never echoed")
}

// TestDisableUser_StaleVersionConflicts: an expected version behind the current
// row leaves the user active, sessions live and no receipt behind.
func TestDisableUser_StaleVersionConflicts(t *testing.T) {
	h := newHarness(t)
	result := provision(t, h, "00000000-0000-0000-0000-00000000e011", "disable_stale", nil)
	_, err := activate(t, h, "00000000-0000-0000-0000-00000000e012", result.UserID, 1)
	require.NoError(t, err)
	_, err = h.svc.Login(context.Background(), "disable_stale", "initial-secret", opCtx())
	require.NoError(t, err)

	_, err = disable(t, h, "00000000-0000-0000-0000-00000000e013", result.UserID, 1, "security.incident")
	require.Error(t, err)
	assert.ErrorIs(t, err, iam.ErrVersionConflict)

	state, currentVersion := h.userState(t, result.UserID)
	assert.Equal(t, "active", state, "row untouched by the missed CAS")
	assert.Equal(t, int64(2), currentVersion)
	assert.Equal(t, int64(1), h.liveSessions(t, result.UserID), "sessions survive a failed disable")
	assert.Equal(t, int64(0), h.countRows(t, "iam_command_receipts WHERE command_name = 'disable_user'"))
}

// TestDisableUser_AlreadyDisabledIsInvalidTransition: a second disable on an
// already-disabled subject is a lifecycle conflict, not a no-op.
func TestDisableUser_AlreadyDisabledIsInvalidTransition(t *testing.T) {
	h := newHarness(t)
	result := provision(t, h, "00000000-0000-0000-0000-00000000e021", "disable_twice", nil)
	_, err := activate(t, h, "00000000-0000-0000-0000-00000000e022", result.UserID, 1)
	require.NoError(t, err)
	_, err = disable(t, h, "00000000-0000-0000-0000-00000000e023", result.UserID, 2, "security.incident")
	require.NoError(t, err)

	_, err = disable(t, h, "00000000-0000-0000-0000-00000000e024", result.UserID, 3, "security.incident")
	require.Error(t, err)
	assert.ErrorIs(t, err, iam.ErrInvalidLifecycleTransition)
	state, currentVersion := h.userState(t, result.UserID)
	assert.Equal(t, "disabled", state)
	assert.Equal(t, int64(3), currentVersion)
}

// TestDisableUser_ProvisioningIsInvalidTransition: provisioning subjects are
// not disabled — they belong to CompensateProvisioningUser.
func TestDisableUser_ProvisioningIsInvalidTransition(t *testing.T) {
	h := newHarness(t)
	result := provision(t, h, "00000000-0000-0000-0000-00000000e031", "disable_prov", nil)

	_, err := disable(t, h, "00000000-0000-0000-0000-00000000e032", result.UserID, 1, "security.incident")
	require.Error(t, err)
	assert.ErrorIs(t, err, iam.ErrInvalidLifecycleTransition)
	state, _ := h.userState(t, result.UserID)
	assert.Equal(t, "provisioning", state)
}

// TestDisableUser_MissingUser: disabling a nonexistent subject is UserNotFound.
func TestDisableUser_MissingUser(t *testing.T) {
	h := newHarness(t)
	_, err := disable(t, h, "00000000-0000-0000-0000-00000000e041", 999999, 1, "security.incident")
	require.Error(t, err)
	assert.ErrorIs(t, err, iam.ErrUserNotFound)
	assert.Equal(t, int64(0), h.countRows(t, "iam_command_receipts WHERE command_name = 'disable_user'"))
}

// TestDisableUser_InvalidReasonCode: reason codes are safe classifications —
// free-form text is rejected before any write.
func TestDisableUser_InvalidReasonCode(t *testing.T) {
	h := newHarness(t)
	result := provision(t, h, "00000000-0000-0000-0000-00000000e051", "disable_badcode", nil)
	_, err := activate(t, h, "00000000-0000-0000-0000-00000000e052", result.UserID, 1)
	require.NoError(t, err)

	_, err = disable(t, h, "00000000-0000-0000-0000-00000000e053", result.UserID, 2, "user said: leak!")
	require.Error(t, err)
	assert.ErrorIs(t, err, iam.ErrInvalidInput)
	_, err = disable(t, h, "00000000-0000-0000-0000-00000000e054", result.UserID, 2, "")
	require.Error(t, err)
	assert.ErrorIs(t, err, iam.ErrInvalidInput)

	state, currentVersion := h.userState(t, result.UserID)
	assert.Equal(t, "active", state, "no lifecycle change on invalid reason codes")
	assert.Equal(t, int64(2), currentVersion)
	assert.Equal(t, int64(0), h.countRows(t, "iam_command_receipts WHERE command_name = 'disable_user'"))
}

// TestDisableUser_SameOperationReplays: a retry with the same operation ID
// returns the committed version without a second receipt or side effect.
func TestDisableUser_SameOperationReplays(t *testing.T) {
	h := newHarness(t)
	result := provision(t, h, "00000000-0000-0000-0000-00000000e061", "disable_replay", nil)
	_, err := activate(t, h, "00000000-0000-0000-0000-00000000e062", result.UserID, 1)
	require.NoError(t, err)

	first, err := disable(t, h, "00000000-0000-0000-0000-00000000e063", result.UserID, 2, "security.incident")
	require.NoError(t, err)
	second, err := disable(t, h, "00000000-0000-0000-0000-00000000e063", result.UserID, 2, "security.incident")
	require.NoError(t, err)
	assert.Equal(t, first, second)

	state, currentVersion := h.userState(t, result.UserID)
	assert.Equal(t, "disabled", state)
	assert.Equal(t, int64(3), currentVersion)
	assert.Equal(t, int64(1), h.countRows(t, "iam_command_receipts WHERE command_name = 'disable_user'"))
}

// TestDisableUser_DifferentRequestOnSameOperation: a replay whose reason code
// differs from the committed evidence is an operation conflict.
func TestDisableUser_DifferentRequestOnSameOperation(t *testing.T) {
	h := newHarness(t)
	result := provision(t, h, "00000000-0000-0000-0000-00000000e071", "disable_conflict", nil)
	_, err := activate(t, h, "00000000-0000-0000-0000-00000000e072", result.UserID, 1)
	require.NoError(t, err)

	_, err = disable(t, h, "00000000-0000-0000-0000-00000000e073", result.UserID, 2, "security.incident")
	require.NoError(t, err)
	_, err = disable(t, h, "00000000-0000-0000-0000-00000000e073", result.UserID, 2, "policy.violation")
	require.Error(t, err)
	assert.ErrorIs(t, err, iam.ErrOperationConflict)
	state, _ := h.userState(t, result.UserID)
	assert.Equal(t, "disabled", state, "committed disable untouched")
}

// TestCompensateProvisioningUser_DeletesProvisioningSubject: the compensating
// delete removes only the still-provisioning subject, emits the normal
// iam.user.deleted v1 event (tombstone = 2) and frees the username.
func TestCompensateProvisioningUser_DeletesProvisioningSubject(t *testing.T) {
	h := newHarness(t)
	store := postgres.NewStore(h.pool)
	result := provision(t, h, "00000000-0000-0000-0000-00000000f001", "comp_me", nil)

	err := compensate(t, h, "00000000-0000-0000-0000-00000000f002", result.UserID, 1)
	require.NoError(t, err)

	_, err = store.GetUserByID(ctx, result.UserID)
	assert.ErrorIs(t, err, iam.ErrUserNotFound, "compensation deletes, never disables")
	assert.Equal(t, int64(0), h.countRows(t, "sessions"), "no orphaned sessions")
	assert.Equal(t, int64(0), h.countRows(t, fmt.Sprintf("user_roles WHERE user_id = %d", result.UserID)))

	// the normal deletion event with the tombstone version
	events := h.outboxRows(t)
	require.Len(t, events, 1)
	e := events[0]
	assert.Equal(t, "iam.user.deleted", e.EventType)
	assert.Equal(t, 1, e.EventVersion)
	assert.Equal(t, "iam", e.Producer)
	assert.Equal(t, "user", e.AggregateType)
	assert.Equal(t, "pending", e.Status)
	assert.Equal(t, "compensate-test", e.CorrelationID)
	assert.Equal(t, int64(2), e.AggregateVersion, "tombstone = prior version + 1")
	assert.Equal(t, e.AggregateID, fmt.Sprintf("%.0f", e.Payload["user_id"]))

	receipt, err := store.GetCommandReceipt(ctx, "00000000-0000-0000-0000-00000000f002", "compensate_provisioning_user")
	require.NoError(t, err)
	require.NotNil(t, receipt)
	assert.Equal(t, float64(result.UserID), receipt.SafeResult["user_id"])
	assert.NotContains(t, receipt.SafeResult, "resulting_version")

	// the username is not permanently occupied: re-provisioning succeeds
	again := provision(t, h, "00000000-0000-0000-0000-00000000f003", "comp_me", nil)
	require.NotEqual(t, result.UserID, again.UserID)
}

// TestCompensateProvisioningUser_ActiveSubjectNeedsReconciliation: an activated
// subject is no longer compensable — typed lifecycle conflict, row intact.
func TestCompensateProvisioningUser_ActiveSubjectNeedsReconciliation(t *testing.T) {
	h := newHarness(t)
	store := postgres.NewStore(h.pool)
	result := provision(t, h, "00000000-0000-0000-0000-00000000f011", "comp_active", nil)
	_, err := activate(t, h, "00000000-0000-0000-0000-00000000f012", result.UserID, 1)
	require.NoError(t, err)

	err = compensate(t, h, "00000000-0000-0000-0000-00000000f013", result.UserID, 2)
	require.Error(t, err)
	assert.ErrorIs(t, err, iam.ErrInvalidLifecycleTransition)
	assert.Contains(t, err.Error(), "reconciliation", "non-compensable states escalate to reconciliation")

	user, err := store.GetUserByID(ctx, result.UserID)
	require.NoError(t, err)
	assert.Equal(t, iam.LifecycleActive, user.LifecycleState)
	assert.Len(t, h.outboxRows(t), 0)
	assert.Equal(t, int64(0), h.countRows(t, "iam_command_receipts WHERE command_name = 'compensate_provisioning_user'"))
}

// TestCompensateProvisioningUser_DisabledSubjectNeedsReconciliation: a disabled
// subject (e.g. via SQL intervention) is equally not compensable.
func TestCompensateProvisioningUser_DisabledSubjectNeedsReconciliation(t *testing.T) {
	h := newHarness(t)
	store := postgres.NewStore(h.pool)
	result := provision(t, h, "00000000-0000-0000-0000-00000000f021", "comp_disabled", nil)
	h.setLifecycle(t, result.UserID, "disabled")

	err := compensate(t, h, "00000000-0000-0000-0000-00000000f022", result.UserID, 1)
	require.Error(t, err)
	assert.ErrorIs(t, err, iam.ErrInvalidLifecycleTransition)

	user, err := store.GetUserByID(ctx, result.UserID)
	require.NoError(t, err)
	assert.Equal(t, iam.LifecycleDisabled, user.LifecycleState)
	assert.Len(t, h.outboxRows(t), 0)
}

// TestCompensateProvisioningUser_StaleVersionConflicts: a provisioning subject
// at a newer version is a version conflict — nothing is deleted, no event.
func TestCompensateProvisioningUser_StaleVersionConflicts(t *testing.T) {
	h := newHarness(t)
	store := postgres.NewStore(h.pool)
	result := provision(t, h, "00000000-0000-0000-0000-00000000f031", "comp_stale", nil)
	_, err := h.db.Exec(context.Background(),
		"UPDATE users SET version = version + 1 WHERE id = $1", result.UserID)
	require.NoError(t, err)

	err = compensate(t, h, "00000000-0000-0000-0000-00000000f032", result.UserID, 1)
	require.Error(t, err)
	assert.ErrorIs(t, err, iam.ErrVersionConflict)

	_, err = store.GetUserByID(ctx, result.UserID)
	require.NoError(t, err, "stale subject survives compensation")
	assert.Len(t, h.outboxRows(t), 0)
	assert.Equal(t, int64(0), h.countRows(t, "iam_command_receipts WHERE command_name = 'compensate_provisioning_user'"))
}

// TestCompensateProvisioningUser_MissingUser: compensating a nonexistent
// subject is UserNotFound.
func TestCompensateProvisioningUser_MissingUser(t *testing.T) {
	h := newHarness(t)
	err := compensate(t, h, "00000000-0000-0000-0000-00000000f041", 999999, 1)
	require.Error(t, err)
	assert.ErrorIs(t, err, iam.ErrUserNotFound)
	assert.Len(t, h.outboxRows(t), 0)
}

// TestCompensateProvisioningUser_SameOperationReplays: a retry with the same
// operation ID resolves through the receipt and never duplicates the event.
func TestCompensateProvisioningUser_SameOperationReplays(t *testing.T) {
	h := newHarness(t)
	result := provision(t, h, "00000000-0000-0000-0000-00000000f051", "comp_replay", nil)

	err := compensate(t, h, "00000000-0000-0000-0000-00000000f052", result.UserID, 1)
	require.NoError(t, err)
	err = compensate(t, h, "00000000-0000-0000-0000-00000000f052", result.UserID, 1)
	require.NoError(t, err)

	assert.Len(t, h.outboxRows(t), 1, "no duplicated events on replay")
	assert.Equal(t, int64(1), h.countRows(t, "iam_command_receipts WHERE command_name = 'compensate_provisioning_user'"))
	assert.Equal(t, int64(0), h.countRows(t, "users"), "subject stays deleted")
}

// TestCompensateProvisioningUser_DifferentRequestOnSameOperation: a replay with
// a different expected version is an operation conflict.
func TestCompensateProvisioningUser_DifferentRequestOnSameOperation(t *testing.T) {
	h := newHarness(t)
	result := provision(t, h, "00000000-0000-0000-0000-00000000f061", "comp_conflict", nil)

	err := compensate(t, h, "00000000-0000-0000-0000-00000000f062", result.UserID, 1)
	require.NoError(t, err)
	err = compensate(t, h, "00000000-0000-0000-0000-00000000f062", result.UserID, 2)
	require.Error(t, err)
	assert.ErrorIs(t, err, iam.ErrOperationConflict)
	assert.Len(t, h.outboxRows(t), 1, "committed compensate untouched")
}
