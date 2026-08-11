-- Platform-owned single-row bridge mode (migration 000008). The startup
-- gate (cmd/server) reads it to verify legacy_delete_sync_enabled=false
-- before accepting the delete consumer/dispatcher or the BFF /users/delete
-- route (plan Phase 5.9 step 4). Writes never happen through this package:
-- controlled transitions go through set_legacy_delete_sync_mode (T076).

-- name: GetCompatibilityBridgeMode :one
SELECT id, legacy_delete_sync_enabled, version, updated_at
FROM compatibility_bridge_mode
WHERE id = 1;

-- name: SetLegacyDeleteSyncMode :one
-- Controlled bridge-mode transition (T076): runs the Platform-owned SECURITY
-- DEFINER function which CAS-bumps the mode row, appends the immutable
-- compatibility_bridge_mode_changes audit row and returns the resulting
-- version. An idempotent replay whose target is already achieved returns the
-- current version; a stale CAS with a different target raises.
SELECT set_legacy_delete_sync_mode(
    $1, $2, $3, $4, $5, $6, $7, $8
) AS version;
