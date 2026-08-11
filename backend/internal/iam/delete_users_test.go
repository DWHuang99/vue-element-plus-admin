// DeleteUsers (task T048; contracts/iam-application.md DeleteUsers,
// contracts/domain-events.md iam.user.deleted).
//
// Pins the authoritative batch deletion on a real PostgreSQL through the
// public constructors: normalized full-batch precheck (any missing/stale
// target rejects everything), cascade cleanup, one iam.user.deleted v1 outbox
// event per user with the tombstone version, and one batch receipt carrying
// the same per-user results — all atomic, replayable, and never duplicating
// events.
package iam_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hdw/vue-element-plus-admin/backend/internal/iam"
	"github.com/hdw/vue-element-plus-admin/backend/internal/iam/postgres"
)

// deleteUsers runs the DeleteUsers command with the given operation context.
func deleteUsers(t *testing.T, h *harness, opID string, targets []iam.DeleteTarget) ([]iam.BatchDeleteResultItem, error) {
	t.Helper()
	return h.svc.DeleteUsers(context.Background(), iam.OperationContext{
		OperationID:   opID,
		ActorUserID:   1,
		CorrelationID: "delete-users-test",
	}, targets)
}

// outboxRow is one DB-truth outbox row for assertions.
type outboxRow struct {
	EventID          string
	EventType        string
	EventVersion     int
	Producer         string
	AggregateType    string
	AggregateID      string
	AggregateVersion int64
	Status           string
	CorrelationID    string
	Payload          map[string]any
}

func (h *harness) outboxRows(t *testing.T) []outboxRow {
	t.Helper()
	rows, err := h.db.Query(context.Background(), `
		SELECT event_id::text, event_type, event_version, producer, aggregate_type,
		       aggregate_id, aggregate_version, status, correlation_id, payload
		FROM iam_outbox_events ORDER BY event_id`)
	require.NoError(t, err)
	defer rows.Close()
	var out []outboxRow
	for rows.Next() {
		var r outboxRow
		var payload []byte
		require.NoError(t, rows.Scan(&r.EventID, &r.EventType, &r.EventVersion, &r.Producer,
			&r.AggregateType, &r.AggregateID, &r.AggregateVersion, &r.Status, &r.CorrelationID, &payload))
		require.NoError(t, json.Unmarshal(payload, &r.Payload))
		out = append(out, r)
	}
	return out
}

// TestDeleteUsers_CommitsAtomically: both users are deleted (sessions and
// roles cascade), each emits one iam.user.deleted v1 event with the tombstone
// version, and the batch receipt carries the same per-user results.
func TestDeleteUsers_CommitsAtomically(t *testing.T) {
	h := newHarness(t)
	store := postgres.NewStore(h.pool)
	u1 := h.register(t, "del_one", "pw-123456").User.UserID
	u2 := h.register(t, "del_two", "pw-123456").User.UserID
	// one session each
	require.Equal(t, int64(2), h.countRows(t, "sessions"))

	results, err := deleteUsers(t, h, "00000000-0000-0000-0000-00000000d001",
		[]iam.DeleteTarget{{UserID: u2, ExpectedVersion: 1}, {UserID: u1, ExpectedVersion: 1}})
	require.NoError(t, err)
	assert.Equal(t, []iam.BatchDeleteResultItem{
		{UserID: u1, TombstoneVersion: 2},
		{UserID: u2, TombstoneVersion: 2},
	}, results, "normalized ascending by user id, tombstone = prior version + 1")

	// users, sessions and role links are gone
	_, err = store.GetUserByID(ctx, u1)
	assert.ErrorIs(t, err, iam.ErrUserNotFound)
	_, err = store.GetUserByID(ctx, u2)
	assert.ErrorIs(t, err, iam.ErrUserNotFound)
	assert.Equal(t, int64(0), h.countRows(t, "sessions"))
	assert.Equal(t, int64(0), h.countRows(t, fmt.Sprintf("user_roles WHERE user_id = %d", u1)))
	assert.Equal(t, int64(0), h.countRows(t, fmt.Sprintf("user_roles WHERE user_id = %d", u2)))

	// one pending iam.user.deleted v1 event per user with the tombstone version
	events := h.outboxRows(t)
	require.Len(t, events, 2)
	for _, e := range events {
		assert.Equal(t, "iam.user.deleted", e.EventType)
		assert.Equal(t, 1, e.EventVersion)
		assert.Equal(t, "iam", e.Producer)
		assert.Equal(t, "user", e.AggregateType)
		assert.Equal(t, "pending", e.Status)
		assert.Equal(t, "delete-users-test", e.CorrelationID)
		assert.Equal(t, int64(2), e.AggregateVersion)
		assert.Equal(t, e.AggregateID, fmt.Sprintf("%.0f", e.Payload["user_id"]), "aggregate id equals payload user id")
	}
	ids := []string{events[0].AggregateID, events[1].AggregateID}
	assert.ElementsMatch(t, []string{fmt.Sprintf("%d", u1), fmt.Sprintf("%d", u2)}, ids)

	// batch receipt carries the same normalized per-user results
	receipt, err := store.GetCommandReceipt(ctx, "00000000-0000-0000-0000-00000000d001", "delete_users")
	require.NoError(t, err)
	require.NotNil(t, receipt)
	assert.Equal(t, iam.CommandSucceeded, receipt.Status)
	targets, ok := receipt.SafeResult["targets"].([]any)
	require.True(t, ok)
	require.Len(t, targets, 2)
	first := targets[0].(map[string]any)
	assert.Equal(t, float64(u1), first["user_id"])
	assert.Equal(t, float64(2), first["tombstone_version"])
}

// TestDeleteUsers_TombstoneTracksPriorVersion: an activated user (version 2)
// gets tombstone 3 — the aggregate version is prior version + 1, not 1.
func TestDeleteUsers_TombstoneTracksPriorVersion(t *testing.T) {
	h := newHarness(t)
	role := seedRole(t, h, "del_tomb_role", "del_tomb_role_code")
	result := provision(t, h, "00000000-0000-0000-0000-00000000a031", "del_tomb", []int64{role})
	_, err := activate(t, h, "00000000-0000-0000-0000-00000000b031", result.UserID, 1)
	require.NoError(t, err)

	results, err := deleteUsers(t, h, "00000000-0000-0000-0000-00000000d002",
		[]iam.DeleteTarget{{UserID: result.UserID, ExpectedVersion: 2}})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, int64(3), results[0].TombstoneVersion)

	events := h.outboxRows(t)
	require.Len(t, events, 1)
	assert.Equal(t, int64(3), events[0].AggregateVersion)
}

// TestDeleteUsers_StaleTargetRejectsWholeBatch: one stale expected version
// deletes nothing — no users, no events, no receipt.
func TestDeleteUsers_StaleTargetRejectsWholeBatch(t *testing.T) {
	h := newHarness(t)
	store := postgres.NewStore(h.pool)
	u1 := h.register(t, "del_stale_one", "pw-123456").User.UserID
	u2 := h.register(t, "del_stale_two", "pw-123456").User.UserID

	_, err := deleteUsers(t, h, "00000000-0000-0000-0000-00000000d003",
		[]iam.DeleteTarget{{UserID: u1, ExpectedVersion: 1}, {UserID: u2, ExpectedVersion: 2}})
	require.Error(t, err)
	assert.ErrorIs(t, err, iam.ErrVersionConflict)

	_, err = store.GetUserByID(ctx, u1)
	require.NoError(t, err, "whole batch rejected")
	_, err = store.GetUserByID(ctx, u2)
	require.NoError(t, err, "whole batch rejected")
	assert.Len(t, h.outboxRows(t), 0)
	assert.Equal(t, int64(0), h.countRows(t, "iam_command_receipts WHERE command_name = 'delete_users'"))
}

// TestDeleteUsers_MissingUserRejectsWholeBatch: one nonexistent target leaves
// every other user untouched with no events and no receipt.
func TestDeleteUsers_MissingUserRejectsWholeBatch(t *testing.T) {
	h := newHarness(t)
	store := postgres.NewStore(h.pool)
	u1 := h.register(t, "del_missing_one", "pw-123456").User.UserID

	_, err := deleteUsers(t, h, "00000000-0000-0000-0000-00000000d004",
		[]iam.DeleteTarget{{UserID: u1, ExpectedVersion: 1}, {UserID: 999999, ExpectedVersion: 1}})
	require.Error(t, err)
	assert.ErrorIs(t, err, iam.ErrUserNotFound)

	_, err = store.GetUserByID(ctx, u1)
	require.NoError(t, err, "existing user survives the rejected batch")
	assert.Len(t, h.outboxRows(t), 0)
	assert.Equal(t, int64(0), h.countRows(t, "iam_command_receipts WHERE command_name = 'delete_users'"))
}

// TestDeleteUsers_DuplicateTargetsDedupe: the same (user, version) target
// twice yields one result, one event and one receipt entry.
func TestDeleteUsers_DuplicateTargetsDedupe(t *testing.T) {
	h := newHarness(t)
	u1 := h.register(t, "del_dupe", "pw-123456").User.UserID

	results, err := deleteUsers(t, h, "00000000-0000-0000-0000-00000000d005",
		[]iam.DeleteTarget{{UserID: u1, ExpectedVersion: 1}, {UserID: u1, ExpectedVersion: 1}})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, u1, results[0].UserID)

	events := h.outboxRows(t)
	require.Len(t, events, 1, "one event per logical deletion")
	receipt, err := postgres.NewStore(h.pool).GetCommandReceipt(ctx, "00000000-0000-0000-0000-00000000d005", "delete_users")
	require.NoError(t, err)
	assert.Len(t, receipt.SafeResult["targets"].([]any), 1)
}

// TestDeleteUsers_SameOperationReplays: retry with the same operation ID
// returns the committed per-user results without duplicating events or
// receipts.
func TestDeleteUsers_SameOperationReplays(t *testing.T) {
	h := newHarness(t)
	u1 := h.register(t, "del_replay_one", "pw-123456").User.UserID
	u2 := h.register(t, "del_replay_two", "pw-123456").User.UserID

	targets := []iam.DeleteTarget{{UserID: u1, ExpectedVersion: 1}, {UserID: u2, ExpectedVersion: 1}}
	first, err := deleteUsers(t, h, "00000000-0000-0000-0000-00000000d006", targets)
	require.NoError(t, err)
	second, err := deleteUsers(t, h, "00000000-0000-0000-0000-00000000d006", targets)
	require.NoError(t, err)
	assert.Equal(t, first, second, "same operation replays the committed result")

	assert.Len(t, h.outboxRows(t), 2, "no duplicated events")
	assert.Equal(t, int64(1), h.countRows(t, "iam_command_receipts WHERE command_name = 'delete_users'"))
	assert.Equal(t, int64(0), h.countRows(t, "users"), "users stay deleted")
}

// TestDeleteUsers_DifferentRequestOnSameOperation: a replay whose targets
// differ from the committed evidence is an operation conflict; the other user
// survives.
func TestDeleteUsers_DifferentRequestOnSameOperation(t *testing.T) {
	h := newHarness(t)
	store := postgres.NewStore(h.pool)
	u1 := h.register(t, "del_conflict_one", "pw-123456").User.UserID
	u2 := h.register(t, "del_conflict_two", "pw-123456").User.UserID

	_, err := deleteUsers(t, h, "00000000-0000-0000-0000-00000000d007",
		[]iam.DeleteTarget{{UserID: u1, ExpectedVersion: 1}})
	require.NoError(t, err)

	_, err = deleteUsers(t, h, "00000000-0000-0000-0000-00000000d007",
		[]iam.DeleteTarget{{UserID: u2, ExpectedVersion: 1}})
	require.Error(t, err)
	assert.ErrorIs(t, err, iam.ErrOperationConflict)

	_, err = store.GetUserByID(ctx, u2)
	require.NoError(t, err, "committed delete untouched, other user survives")
	_, err = store.GetUserByID(ctx, u1)
	assert.ErrorIs(t, err, iam.ErrUserNotFound)
}
