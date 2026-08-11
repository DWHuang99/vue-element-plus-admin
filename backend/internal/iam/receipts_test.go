// IAM command receipt storage + ResolveCommand (task T044;
// contracts/consistency-and-compensation.md).
//
// These tests pin the storage contract on a real PostgreSQL through the public
// constructors (postgres.NewStore / iam.NewService): receipt committed in the
// same transaction as its side effect, conflict-free evidence, and resolution
// semantics (absent -> nil, fingerprint mismatch -> OperationConflict).
package iam_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hdw/vue-element-plus-admin/backend/internal/iam"
	"github.com/hdw/vue-element-plus-admin/backend/internal/iam/postgres"
)

// receiptFixture returns a store-backed harness plus a sample receipt.
func receiptFixture(t *testing.T, h *harness) iam.Store {
	t.Helper()
	return postgres.NewStore(h.pool)
}

func sampleReceipt() iam.IAMCommandReceipt {
	return iam.IAMCommandReceipt{
		OperationID:        "00000000-0000-0000-0000-00000000cafe",
		CommandName:        "create_provisioning_user",
		RequestFingerprint: "fp-1234",
		Status:             iam.CommandSucceeded,
		SubjectID:          int64Ptr(7),
		ResultingVersion:   int64Ptr(1),
		SafeResult:         map[string]any{"user_id": float64(7), "resulting_version": float64(1)},
		ErrorCode:          nil,
	}
}

// TestReceipt_RoundTrip: what the service commits is what resolution sees.
func TestReceipt_RoundTrip(t *testing.T) {
	h := newHarness(t)
	store := receiptFixture(t, h)

	receipt := sampleReceipt()
	require.NoError(t, store.SaveCommandReceipt(context.Background(), receipt))

	got, err := store.GetCommandReceipt(context.Background(), receipt.OperationID, receipt.CommandName)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, receipt.OperationID, got.OperationID)
	assert.Equal(t, receipt.CommandName, got.CommandName)
	assert.Equal(t, receipt.RequestFingerprint, got.RequestFingerprint)
	assert.Equal(t, receipt.Status, got.Status)
	assert.Equal(t, receipt.SubjectID, got.SubjectID)
	assert.Equal(t, receipt.SafeResult, got.SafeResult, "transport-neutral result survives JSONB round trip")
	assert.Equal(t, int64(1), *got.ResultingVersion, "version extracted from the safe result")
	assert.Nil(t, got.ErrorCode)
}

// TestReceipt_CommittedWithSideEffect: a receipt written inside a transaction
// whose side effect rolls back must not survive — evidence is never orphaned.
func TestReceipt_CommittedWithSideEffect(t *testing.T) {
	h := newHarness(t)
	store := receiptFixture(t, h)
	ctx := context.Background()

	username := fmt.Sprintf("receipt_atomic_%d", time.Now().UnixNano())
	err := store.RunInTx(ctx, func(tx iam.Store) error {
		user, err := tx.CreateUser(ctx, username, "hash-not-committed")
		require.NoError(t, err)
		receipt := sampleReceipt()
		receipt.SubjectID = &user.ID
		require.NoError(t, tx.SaveCommandReceipt(ctx, receipt))
		return errors.New("rollback after side effect") // simulated failure
	})
	require.Error(t, err, "the fn error rolls the transaction back")

	// neither the user nor the receipt may exist
	_, err = store.GetUserByUsername(ctx, username)
	assert.Error(t, err, "user rolled back with the receipt")
	got, err := store.GetCommandReceipt(ctx, sampleReceipt().OperationID, "create_provisioning_user")
	require.NoError(t, err)
	assert.Nil(t, got, "receipt rolled back with the side effect")
}

// TestReceipt_SaveConflictDoesNotOverwrite: a second save of the same
// (operation_id, command_name) is a no-op, never a silent overwrite of the
// committed evidence.
func TestReceipt_SaveConflictDoesNotOverwrite(t *testing.T) {
	h := newHarness(t)
	store := receiptFixture(t, h)
	ctx := context.Background()

	first := sampleReceipt()
	require.NoError(t, store.SaveCommandReceipt(ctx, first))

	second := first
	second.RequestFingerprint = "fp-different"
	second.Status = iam.CommandRejected
	require.NoError(t, store.SaveCommandReceipt(ctx, second), "conflict is swallowed, not raised")

	got, err := store.GetCommandReceipt(ctx, first.OperationID, first.CommandName)
	require.NoError(t, err)
	assert.Equal(t, "fp-1234", got.RequestFingerprint, "original evidence is immutable")
	assert.Equal(t, iam.CommandSucceeded, got.Status)
}

// TestResolveCommand_AbsentReturnsNil: no committed evidence resolves to
// (nil, nil) — resolution never fabricates success or failure.
func TestResolveCommand_AbsentReturnsNil(t *testing.T) {
	h := newHarness(t)
	receipt, err := h.svc.ResolveCommand(context.Background(),
		"00000000-0000-0000-0000-00000000cafe", "create_provisioning_user", "fp-1234")
	require.NoError(t, err)
	assert.Nil(t, receipt)
}

// TestResolveCommand_FingerprintMismatch: a resolved receipt whose request
// differs from the committed evidence is an operation conflict, never a
// re-application.
func TestResolveCommand_FingerprintMismatch(t *testing.T) {
	h := newHarness(t)
	store := receiptFixture(t, h)
	ctx := context.Background()
	require.NoError(t, store.SaveCommandReceipt(ctx, sampleReceipt()))

	_, err := h.svc.ResolveCommand(ctx,
		sampleReceipt().OperationID, sampleReceipt().CommandName, "fp-other")
	require.Error(t, err)
	assert.ErrorIs(t, err, iam.ErrOperationConflict)
}

// TestResolveCommand_ReplaysCommittedReceipt: matching fingerprint resolves to
// the committed evidence for safe replay.
func TestResolveCommand_ReplaysCommittedReceipt(t *testing.T) {
	h := newHarness(t)
	store := receiptFixture(t, h)
	ctx := context.Background()
	require.NoError(t, store.SaveCommandReceipt(ctx, sampleReceipt()))

	got, err := h.svc.ResolveCommand(ctx,
		sampleReceipt().OperationID, sampleReceipt().CommandName, "fp-1234")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, int64(7), *got.SubjectID)
	assert.Equal(t, iam.CommandSucceeded, got.Status)
}

// TestReceipt_InvalidOperationID: a malformed operation id is an adapter error
// surfaced immediately, never a confusing query failure.
func TestReceipt_InvalidOperationID(t *testing.T) {
	h := newHarness(t)
	store := receiptFixture(t, h)
	ctx := context.Background()

	bad := sampleReceipt()
	bad.OperationID = "not-a-uuid"
	err := store.SaveCommandReceipt(ctx, bad)
	assert.Error(t, err, "malformed operation id is rejected")

	_, err = store.GetCommandReceipt(ctx, "not-a-uuid", "create_provisioning_user")
	assert.Error(t, err)
}

func int64Ptr(v int64) *int64 { return &v }
