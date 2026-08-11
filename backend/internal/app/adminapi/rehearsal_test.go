// T079 rollout rehearsal tests (quickstart §9, plan Phase 5.9 step 4): before
// deploy the operator pins the exact 004-compatible rollback artifact
// (image/Git SHA/config manifest) and runs the extended compatibility suite
// against it — the frozen pre-004 baseline is characterization only, never an
// eligible rollback binary. This file rehearses the staged granular
// enablement order at the capability-manifest level (each stage flips distinct
// flags; create/update and delete-route remain independent) and drives the
// real composition root with a pinned artifact so the rollout-gate evidence
// records the pinned manifest hash + artifact identity + suite result. The
// 72h window reset/approval math itself is owned by the migration-000012
// SECURITY DEFINER functions and verified in internal/database/
// rollout_gate_test.go (T077); this file adds the rehearsal-level confirmation
// that a freshly recorded window cannot yet be approved.
package adminapi

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hdw/vue-element-plus-admin/backend/internal/database"
	"github.com/hdw/vue-element-plus-admin/backend/internal/server"
)

// stagedManifest builds a full capability-manifest flag set with the given
// keys forced true and every other flag false, using exactly the key names
// cmd/server/capabilityManifest emits (the T077 manifest hash is computed over
// these sorted FLAG=value lines).
func stagedManifest(trues ...string) map[string]bool {
	m := map[string]bool{
		"ADMIN_BFF_ROUTES_ENABLED":              false,
		"ADMIN_BFF_SHADOW_READS":                false,
		"ADMIN_BFF_AUTH_PROFILE_READS_ENABLED":  false,
		"ADMIN_BFF_USER_LIST_READS_ENABLED":     false,
		"ADMIN_BFF_DEPARTMENT_WRITES_ENABLED":   false,
		"ADMIN_BFF_ROLE_WRITES_ENABLED":         false,
		"ADMIN_BFF_MANAGED_USER_WRITES_ENABLED": false,
		"ADMIN_BFF_USER_DELETE_ROUTE_ENABLED":   false,
		"LEGACY_DELETE_IAM_DELEGATION_ENABLED":  false,
		"IAM_DELETE_EVENT_CONSUMER_ENABLED":     false,
		"OUTBOX_DISPATCHER_ENABLED":             false,
	}
	for _, k := range trues {
		m[k] = true
	}
	return m
}

// TestRolloutRehearsal_StagedEnablementOrder walks the quickstart §9 staged
// granular enablement sequence at the capability-manifest level. Each stage
// is a strict superset of the previous one and produces a distinct manifest
// hash; the ADMIN_BFF_USER_DELETE_ROUTE_ENABLED flag is only flipped in the
// final stage, and it stays independent of the create/update flags (they are
// separate manifest lines, never merged).
func TestRolloutRehearsal_StagedEnablementOrder(t *testing.T) {
	// §9 staged order (each stage = previously enabled flags plus the new one):
	//   reads: auth/profile → user-list
	//   writes: department → role → managed-user create/update
	//   delete chain: legacy-delete delegation → consumer/dispatcher →
	//                 ADMIN_BFF_USER_DELETE_ROUTE_ENABLED (LAST)
	stages := []struct {
		name     string
		manifest map[string]bool
	}{
		{
			name:     "auth/profile reads",
			manifest: stagedManifest("ADMIN_BFF_ROUTES_ENABLED", "ADMIN_BFF_AUTH_PROFILE_READS_ENABLED"),
		},
		{
			name:     "user-list reads",
			manifest: stagedManifest("ADMIN_BFF_ROUTES_ENABLED", "ADMIN_BFF_AUTH_PROFILE_READS_ENABLED", "ADMIN_BFF_USER_LIST_READS_ENABLED"),
		},
		{
			name:     "department writes",
			manifest: stagedManifest("ADMIN_BFF_ROUTES_ENABLED", "ADMIN_BFF_AUTH_PROFILE_READS_ENABLED", "ADMIN_BFF_USER_LIST_READS_ENABLED", "ADMIN_BFF_DEPARTMENT_WRITES_ENABLED"),
		},
		{
			name:     "role writes",
			manifest: stagedManifest("ADMIN_BFF_ROUTES_ENABLED", "ADMIN_BFF_AUTH_PROFILE_READS_ENABLED", "ADMIN_BFF_USER_LIST_READS_ENABLED", "ADMIN_BFF_DEPARTMENT_WRITES_ENABLED", "ADMIN_BFF_ROLE_WRITES_ENABLED"),
		},
		{
			name:     "managed-user create/update",
			manifest: stagedManifest("ADMIN_BFF_ROUTES_ENABLED", "ADMIN_BFF_AUTH_PROFILE_READS_ENABLED", "ADMIN_BFF_USER_LIST_READS_ENABLED", "ADMIN_BFF_DEPARTMENT_WRITES_ENABLED", "ADMIN_BFF_ROLE_WRITES_ENABLED", "ADMIN_BFF_MANAGED_USER_WRITES_ENABLED"),
		},
		{
			name:     "legacy-delete delegation",
			manifest: stagedManifest("ADMIN_BFF_ROUTES_ENABLED", "ADMIN_BFF_AUTH_PROFILE_READS_ENABLED", "ADMIN_BFF_USER_LIST_READS_ENABLED", "ADMIN_BFF_DEPARTMENT_WRITES_ENABLED", "ADMIN_BFF_ROLE_WRITES_ENABLED", "ADMIN_BFF_MANAGED_USER_WRITES_ENABLED", "LEGACY_DELETE_IAM_DELEGATION_ENABLED"),
		},
		{
			name:     "consumer/dispatcher",
			manifest: stagedManifest("ADMIN_BFF_ROUTES_ENABLED", "ADMIN_BFF_AUTH_PROFILE_READS_ENABLED", "ADMIN_BFF_USER_LIST_READS_ENABLED", "ADMIN_BFF_DEPARTMENT_WRITES_ENABLED", "ADMIN_BFF_ROLE_WRITES_ENABLED", "ADMIN_BFF_MANAGED_USER_WRITES_ENABLED", "LEGACY_DELETE_IAM_DELEGATION_ENABLED", "IAM_DELETE_EVENT_CONSUMER_ENABLED", "OUTBOX_DISPATCHER_ENABLED"),
		},
		{
			name:     "delete route (LAST)",
			manifest: stagedManifest("ADMIN_BFF_ROUTES_ENABLED", "ADMIN_BFF_AUTH_PROFILE_READS_ENABLED", "ADMIN_BFF_USER_LIST_READS_ENABLED", "ADMIN_BFF_DEPARTMENT_WRITES_ENABLED", "ADMIN_BFF_ROLE_WRITES_ENABLED", "ADMIN_BFF_MANAGED_USER_WRITES_ENABLED", "LEGACY_DELETE_IAM_DELEGATION_ENABLED", "IAM_DELETE_EVENT_CONSUMER_ENABLED", "OUTBOX_DISPATCHER_ENABLED", "ADMIN_BFF_USER_DELETE_ROUTE_ENABLED"),
		},
	}

	var prevHash string
	for i, stage := range stages {
		t.Run(stage.name, func(t *testing.T) {
			hash := CapabilityManifestHash(stage.manifest)
			// Deterministic: the same set always hashes identically.
			assert.Equal(t, hash, CapabilityManifestHash(stage.manifest),
				"manifest hash is a pure function of the flag set")
			if i > 0 {
				assert.NotEqual(t, prevHash, hash,
					"each staged flip must change the deployed capability manifest")
			}
			prevHash = hash

			// The delete route flag is only ever true in the final stage.
			deleteRoute := stage.manifest["ADMIN_BFF_USER_DELETE_ROUTE_ENABLED"]
			if i < len(stages)-1 {
				assert.False(t, deleteRoute,
					"delete route is not enabled before the final stage")
			} else {
				assert.True(t, deleteRoute,
					"delete route is the last granular capability enabled")
			}
		})
	}
}

// TestRolloutRehearsal_CreateUpdateIndependentOfDeleteRoute pins the flag
// independence that §9 demands: managed-user create/update and the delete
// route are separate capability lines, so toggling one never implies the
// other and their manifest hashes are both distinct from each other and from
// a shared-off baseline.
func TestRolloutRehearsal_CreateUpdateIndependentOfDeleteRoute(t *testing.T) {
	createUpdate := stagedManifest("ADMIN_BFF_MANAGED_USER_WRITES_ENABLED")
	deleteRoute := stagedManifest("ADMIN_BFF_USER_DELETE_ROUTE_ENABLED")
	both := stagedManifest("ADMIN_BFF_MANAGED_USER_WRITES_ENABLED", "ADMIN_BFF_USER_DELETE_ROUTE_ENABLED")
	off := stagedManifest()

	assert.False(t, createUpdate["ADMIN_BFF_USER_DELETE_ROUTE_ENABLED"],
		"create/update alone must not imply the delete route")
	assert.False(t, deleteRoute["ADMIN_BFF_MANAGED_USER_WRITES_ENABLED"],
		"delete route alone must not imply create/update")

	hCU := CapabilityManifestHash(createUpdate)
	hDel := CapabilityManifestHash(deleteRoute)
	hBoth := CapabilityManifestHash(both)
	hOff := CapabilityManifestHash(off)

	assert.NotEqual(t, hOff, hCU, "create/update flips the manifest")
	assert.NotEqual(t, hOff, hDel, "delete route flips the manifest")
	assert.NotEqual(t, hCU, hDel, "create/update and delete route hash differently")
	assert.NotEqual(t, hCU, hBoth, "adding the delete route changes the manifest")
	assert.NotEqual(t, hDel, hBoth, "adding create/update changes the manifest")
}

// TestWire_RolloutRehearsal_PinnedArtifactEvidence pins a 004-compatible
// rollback artifact (config-manifest hash + rollback artifact identity + suite
// result) into the real composition root and verifies the rollout-gate
// evidence records exactly that pinned identity alongside the live
// bridge-mode/parity surface. It also confirms a freshly recorded window
// cannot yet be approved — the 72h approval criterion is a rehearsal gate, not
// a runtime flag.
func TestWire_RolloutRehearsal_PinnedArtifactEvidence(t *testing.T) {
	ctx := context.Background()
	connStr := wireSmokeDB(t)

	db, err := database.Connect(ctx, database.Config{URL: connStr, MaxConns: 5, MinConns: 1})
	require.NoError(t, err)
	t.Cleanup(db.Close)

	// Seed one department + one legacy user: the 000008 insert bridge mirrors
	// the membership state (membership_version = users.version = 1), so the
	// parity surface is 1 == 1 with equal canonical checksums.
	var deptID int64
	require.NoError(t, db.Pool.QueryRow(ctx,
		"INSERT INTO departments (name) VALUES ('rehearsal-dept') RETURNING id").Scan(&deptID))
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO users (username, password_hash, lifecycle_state, version, department_id)
		VALUES ('rehearsal_user', 'not-a-real-hash', 'active', 1, $1)`, deptID)
	require.NoError(t, err)

	// The pinned artifact: the final staged manifest (§9) + a fixed rollback
	// artifact identity and the extended-suite verdict. This is what the
	// operator records before deploy (quickstart §9 step 1).
	pinnedHash := CapabilityManifestHash(stagedManifest(
		"ADMIN_BFF_ROUTES_ENABLED", "ADMIN_BFF_AUTH_PROFILE_READS_ENABLED",
		"ADMIN_BFF_USER_LIST_READS_ENABLED", "ADMIN_BFF_DEPARTMENT_WRITES_ENABLED",
		"ADMIN_BFF_ROLE_WRITES_ENABLED", "ADMIN_BFF_MANAGED_USER_WRITES_ENABLED",
		"LEGACY_DELETE_IAM_DELEGATION_ENABLED", "IAM_DELETE_EVENT_CONSUMER_ENABLED",
		"OUTBOX_DISPATCHER_ENABLED", "ADMIN_BFF_USER_DELETE_ROUTE_ENABLED"))
	const pinnedArtifact = "backend@sha256:beef0000dead0000rehearsal0000artifact"
	const phase = "rehearsal"

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := NewWire(db, logger, Config{
		RolloutGate: RolloutGateRuntime{
			Enabled:                true,
			SampleCadence:          50 * time.Millisecond,
			MaxGapInterval:         10 * time.Second,
			Phase:                  phase,
			PrincipalID:            "platform_operator_session",
			RollbackArtifactID:     pinnedArtifact,
			RollbackSuiteResult:    "passed",
			CapabilityManifestHash: pinnedHash,
		},
	}, nil, server.LegacyRateLimitConfig{})
	bgCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	w.StartBackground(bgCtx)

	// First evidence row: the full writer path completed once with the pinned
	// artifact identity.
	type evidence struct {
		manifestHash    string
		artifact        string
		suiteResult     string
		bridgeModeVer   int64
		bridgeDeleteSyn bool
		legacyRows      int64
		newRows         int64
		principal       string
	}
	var first evidence
	require.Eventually(t, func() bool {
		err := db.Pool.QueryRow(ctx, `
			SELECT capability_manifest_hash, rollback_artifact_id, rollback_suite_result,
			       bridge_mode_version, bridge_legacy_delete_sync_enabled,
			       legacy_rows, new_rows, approving_principal_id
			FROM compatibility_rollout_gates
			WHERE phase = $1
			ORDER BY gate_id LIMIT 1`, phase).
			Scan(&first.manifestHash, &first.artifact, &first.suiteResult,
				&first.bridgeModeVer, &first.bridgeDeleteSyn,
				&first.legacyRows, &first.newRows, &first.principal)
		return err == nil
	}, 10*time.Second, 50*time.Millisecond, "writer records the first pinned-artifact sample")

	assert.Equal(t, pinnedHash, first.manifestHash,
		"gate evidence carries the pinned config-manifest hash")
	assert.Equal(t, pinnedArtifact, first.artifact,
		"gate evidence carries the pinned rollback artifact identity")
	assert.Equal(t, "passed", first.suiteResult,
		"gate evidence carries the extended-suite verdict")
	assert.Equal(t, int64(1), first.bridgeModeVer, "bridge mode version mirrors the live row")
	assert.True(t, first.bridgeDeleteSyn,
		"pre-000008 seed keeps legacy delete sync on; the sample mirrors the live Platform row")
	assert.Equal(t, int64(1), first.legacyRows, "legacy side of the seeded parity row")
	assert.Equal(t, int64(1), first.newRows, "new side of the seeded parity row")
	assert.Equal(t, "platform_operator_session", first.principal,
		"principal is the trusted Platform operation identity, never caller text")

	// A freshly recorded window cannot be approved yet: the 72h continuous
	// passing window is the rollout-rehearsal approval gate (§9 step 10, §11),
	// and this window is seconds old.
	_, err = database.ApproveRouteDisable(ctx, db.Pool, database.RouteDisableApproval{
		Phase:       phase,
		ApprovalID:  "appr-rehearsal",
		PrincipalID: "platform_operator_session",
		ObservedAt:  time.Now(),
	})
	require.Error(t, err, "approval must reject an incomplete 72h window")
	require.Contains(t, err.Error(), "rollout_gate_window_incomplete",
		"the SQL gate names the incomplete window, not a runtime boolean")
}
