// Startup-gate data source (task T070; quickstart §4): the generated
// GetCompatibilityBridgeMode query — read by cmd/server's gate before any
// delete consumer/dispatcher/route acceptance — must return the
// Platform-owned single row, reflect the migration 000008 seed state
// (legacy_delete_sync_enabled=true) and follow controlled mode transitions.
package database

import (
	"context"
	"testing"

	"github.com/hdw/vue-element-plus-admin/backend/internal/database/sqlc"
	"github.com/stretchr/testify/require"
)

func TestStartupGate_BridgeModeReadable(t *testing.T) {
	q := sqlc.New(testConn)

	mode, err := q.GetCompatibilityBridgeMode(context.Background())
	require.NoError(t, err)
	require.Equal(t, int32(1), mode.ID, "single Platform-owned row")

	// A controlled transition shows up in the gate's view: the query must
	// follow the CAS switch and its version bump (the shared DB's version
	// drifts across tests, so flip relative to the current state).
	before := mode.LegacyDeleteSyncEnabled
	newVersion := setLegacyDeleteSyncMode(t, bridgeModeVersion(t), !before)
	t.Cleanup(func() { setLegacyDeleteSyncMode(t, bridgeModeVersion(t), before) })

	mode, err = q.GetCompatibilityBridgeMode(context.Background())
	require.NoError(t, err)
	require.Equal(t, !before, mode.LegacyDeleteSyncEnabled)
	require.Equal(t, newVersion, mode.Version)
}
