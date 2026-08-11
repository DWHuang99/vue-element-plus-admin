// CreateProvisioningUser (task T045; contracts/iam-application.md
// CreateProvisioningUser).
//
// Pins the provisioning lifecycle on a real PostgreSQL through the public
// constructors: provisioning state at version 1 with role assignment and
// receipt committed atomically, same-operation retry replay, no-login and
// excluded-from-lists guarantees, username conflicts, and the no-secrets
// receipt invariant.
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

// provision creates a provisioning user through the service and returns the
// harness plus the result.
func provision(t *testing.T, h *harness, opID, username string, roleIDs []int64) iam.CreateUserResult {
	t.Helper()
	result, err := h.svc.CreateProvisioningUser(context.Background(), iam.OperationContext{
		OperationID:   opID,
		ActorUserID:   1,
		CorrelationID: "provisioning-test",
	}, username, strPtr("acct"), strPtr("mail@example.com"), "initial-secret", roleIDs)
	require.NoError(t, err)
	return result
}

// TestCreateProvisioningUser_CommittedAtomically: the user is provisioning at
// version 1, the roles are assigned, and the receipt records the safe result
// in the same transaction.
func TestCreateProvisioningUser_CommittedAtomically(t *testing.T) {
	h := newHarness(t)
	store := postgres.NewStore(h.pool)
	ctx := context.Background()

	userRole := seedRole(t, h, "prov_user", "prov_user_role")
	adminRole := seedRole(t, h, "prov_admin", "prov_admin_role")

	result := provision(t, h, "00000000-0000-0000-0000-00000000a001", "provisioned_one", []int64{userRole, adminRole})

	// identity row: provisioning, version 1, hash persisted not the password
	user, err := store.GetUserByID(ctx, result.UserID)
	require.NoError(t, err)
	assert.Equal(t, "provisioned_one", user.Username)
	assert.Equal(t, iam.LifecycleProvisioning, user.LifecycleState)
	assert.Equal(t, int64(1), user.Version)
	assert.NotEqual(t, "initial-secret", user.PasswordHash, "never plaintext")

	// identity profile never carries the hash (compile-time shape, asserted here)
	profile, err := h.svc.GetIdentity(ctx, result.UserID)
	require.NoError(t, err)
	assert.Equal(t, "provisioned_one", profile.Username)
	assert.Equal(t, iam.LifecycleProvisioning, profile.LifecycleState)

	// roles assigned
	roles, err := store.ListRolesByUserID(ctx, result.UserID)
	require.NoError(t, err)
	assert.Len(t, roles, 2)
	ids := []int64{roles[0].ID, roles[1].ID}
	assert.ElementsMatch(t, []int64{userRole, adminRole}, ids)

	// receipt committed with the safe result
	receipt, err := store.GetCommandReceipt(ctx, "00000000-0000-0000-0000-00000000a001", "create_provisioning_user")
	require.NoError(t, err)
	require.NotNil(t, receipt)
	assert.Equal(t, iam.CommandSucceeded, receipt.Status)
	assert.Equal(t, result.UserID, *receipt.SubjectID)
	assert.Equal(t, float64(result.UserID), receipt.SafeResult["user_id"])
	assert.Equal(t, float64(1), receipt.SafeResult["resulting_version"])
	// the receipt never carries credential material
	for key := range receipt.SafeResult {
		assert.NotContains(t, key, "password")
		assert.NotContains(t, key, "hash")
	}
}

// TestCreateProvisioningUser_SameOperationReplays: retry with the same
// operation ID returns the same user ID/version and does not duplicate the
// user or its roles.
func TestCreateProvisioningUser_SameOperationReplays(t *testing.T) {
	h := newHarness(t)
	role := seedRole(t, h, "replay_role", "replay_role_code")

	first := provision(t, h, "00000000-0000-0000-0000-00000000a002", "replay_user", []int64{role})
	second := provision(t, h, "00000000-0000-0000-0000-00000000a002", "replay_user", []int64{role})

	assert.Equal(t, first, second, "same operation replays the committed result")

	// exactly one user and exactly one role link exist
	count := h.countRows(t, "users WHERE username = 'replay_user'")
	assert.Equal(t, int64(1), count)
	links := h.countRows(t, fmt.Sprintf("user_roles WHERE user_id = %d", first.UserID))
	assert.Equal(t, int64(1), links)
}

// TestCreateProvisioningUser_DifferentRequestOnSameOperation: a replay whose
// request differs from the committed evidence is an operation conflict.
func TestCreateProvisioningUser_DifferentRequestOnSameOperation(t *testing.T) {
	h := newHarness(t)
	role := seedRole(t, h, "conflict_role", "conflict_role_code")

	provision(t, h, "00000000-0000-0000-0000-00000000a003", "conflict_user", []int64{role})

	_, err := h.svc.CreateProvisioningUser(context.Background(), iam.OperationContext{
		OperationID: "00000000-0000-0000-0000-00000000a003",
		ActorUserID: 1,
	}, "conflict_user", nil, nil, "different-secret", []int64{role})
	require.Error(t, err)
	assert.ErrorIs(t, err, iam.ErrOperationConflict)
}

// TestCreateProvisioningUser_CannotLogin: provisioning credentials never
// authenticate.
func TestCreateProvisioningUser_CannotLogin(t *testing.T) {
	h := newHarness(t)
	role := seedRole(t, h, "nologin_role", "nologin_role_code")
	provision(t, h, "00000000-0000-0000-0000-00000000a004", "nologin_user", []int64{role})

	_, err := h.svc.Login(context.Background(), "nologin_user", "initial-secret", opCtx())
	require.Error(t, err)
	assert.ErrorIs(t, err, iam.ErrInvalidCredentials)
}

// TestCreateProvisioningUser_ExcludedFromManagedLists: provisioning users do
// not appear in the normal managed-user list.
func TestCreateProvisioningUser_ExcludedFromManagedLists(t *testing.T) {
	h := newHarness(t)
	role := seedRole(t, h, "list_role", "list_role_code")
	provision(t, h, "00000000-0000-0000-0000-00000000a005", "invisible_user", []int64{role})
	h.register(t, "visible_user", "pw-123456")

	page, err := h.svc.BatchGetManagedIdentities(context.Background(), nil, nil, nil, 1, 10)
	require.NoError(t, err)
	for _, item := range page.Items {
		assert.NotEqual(t, "invisible_user", item.Username, "provisioning users are excluded")
	}
	assert.True(t, page.Total >= 1, "the registered user still appears")
}

// TestCreateProvisioningUser_UsernameConflict: a username conflict from a
// different operation returns NameTaken, even against a provisioning user.
func TestCreateProvisioningUser_UsernameConflict(t *testing.T) {
	h := newHarness(t)
	role := seedRole(t, h, "taken_role", "taken_role_code")
	provision(t, h, "00000000-0000-0000-0000-00000000a006", "taken_user", []int64{role})

	_, err := h.svc.CreateProvisioningUser(context.Background(), iam.OperationContext{
		OperationID: "00000000-0000-0000-0000-00000000a007",
		ActorUserID: 1,
	}, "taken_user", nil, nil, "other-secret", []int64{role})
	require.Error(t, err)
	assert.ErrorIs(t, err, iam.ErrUsernameTaken)
}

// TestCreateProvisioningUser_MissingRoleRejectsWholeBatch: one missing role
// leaves no user, no links and no receipt behind.
func TestCreateProvisioningUser_MissingRoleRejectsWholeBatch(t *testing.T) {
	h := newHarness(t)
	role := seedRole(t, h, "partial_role", "partial_role_code")

	_, err := h.svc.CreateProvisioningUser(context.Background(), iam.OperationContext{
		OperationID: "00000000-0000-0000-0000-00000000a008",
		ActorUserID: 1,
	}, "partial_user", nil, nil, "secret", []int64{role, 999999})
	require.Error(t, err)
	assert.ErrorIs(t, err, iam.ErrRoleNotFound)

	assert.Equal(t, int64(0), h.countRows(t, "users WHERE username = 'partial_user'"))
	assert.Equal(t, int64(0), h.countRows(t, "iam_command_receipts"))
}

// --- helpers -----------------------------------------------------------------

// seedRole creates a role directly on the store and returns its id.
func seedRole(t *testing.T, h *harness, name, code string) int64 {
	t.Helper()
	role, err := postgres.NewStore(h.pool).CreateRole(context.Background(), name, code)
	require.NoError(t, err)
	return role.ID
}

func strPtr(s string) *string { return &s }
