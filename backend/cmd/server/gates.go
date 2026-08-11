// Startup gates for the US5 microservice-split rollout (plan Phase 5.9 step
// 4, quickstart §4): before the process accepts the delete-event
// consumer/dispatcher or the Admin BFF /users/delete route, the flag
// combination and the live bridge-mode state must be safe. Every error
// references configuration key names and reason codes only — never values,
// URLs or DB state that could leak secrets.
//
// Gate order (from plan step 4):
//  1. Legacy delete delegation opens first; the legacy /users/delete handler
//     delegates to IAM while the DELETE bridge may still be on (mode=true is
//     the phase-1 state).
//  2. After confirming no direct-delete entry remains, a short-lived
//     authorized Platform operation sets bridge mode=false — and only then is
//     consumer/dispatcher acceptance allowed.
//  3. ADMIN_BFF_USER_DELETE_ROUTE_ENABLED additionally requires the
//     consumer and dispatcher to be ready. The "delete compatibility tests
//     pass" condition is a CI/rehearsal gate (T079), not a runtime check.
//
// The same-physical-database gate lives in config.Validate (T069): all three
// module URLs must resolve to the physical database named by DATABASE_URL,
// and schema readiness is established by the migration runner before these
// gates run.
package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/hdw/vue-element-plus-admin/backend/internal/config"
	"github.com/hdw/vue-element-plus-admin/backend/internal/database"
	"github.com/hdw/vue-element-plus-admin/backend/internal/database/sqlc"
)

// bridgeModeSnapshot is the startup-relevant projection of the Platform-owned
// compatibility_bridge_mode row. Kept as a plain struct so the gate logic is
// decoupled from pgx/sqlc types and fully unit-testable.
type bridgeModeSnapshot struct {
	LegacyDeleteSyncEnabled bool
	Version                 int64
}

// readBridgeMode loads the single bridge-mode row. A missing row means the
// bridge schema is not in place — the gate reports it as not ready.
func readBridgeMode(ctx context.Context, db *database.DB) (*bridgeModeSnapshot, error) {
	row, err := sqlc.New(db.Pool).GetCompatibilityBridgeMode(ctx)
	if err != nil {
		return nil, fmt.Errorf("startup gate: compatibility bridge mode unreadable (schema/row missing): %w", err)
	}
	return &bridgeModeSnapshot{
		LegacyDeleteSyncEnabled: row.LegacyDeleteSyncEnabled,
		Version:                 row.Version,
	}, nil
}

// checkStartupGates validates the delete-path flag combination and the bridge
// mode snapshot. mode may be nil when no delete-path component is enabled
// (the default); when a delete-path component IS enabled, a nil mode fails
// the gate (bridge state must be readable).
func checkStartupGates(cfg *config.Config, mode *bridgeModeSnapshot) error {
	f := cfg.Features
	consumerOrDispatcher := f.IAMDeleteEventConsumerEnabled || f.OutboxDispatcherEnabled

	if consumerOrDispatcher && !f.LegacyDeleteIAMDelegationEnabled {
		return fmt.Errorf("startup gate rejected: IAM_DELETE_EVENT_CONSUMER_ENABLED/OUTBOX_DISPATCHER_ENABLED require LEGACY_DELETE_IAM_DELEGATION_ENABLED=true (delegation must be live before any consumer/dispatcher acceptance)")
	}
	if consumerOrDispatcher {
		if mode == nil {
			return fmt.Errorf("startup gate rejected: delete consumer/dispatcher acceptance requires the compatibility bridge mode row (schema not ready)")
		}
		if mode.LegacyDeleteSyncEnabled {
			return fmt.Errorf("startup gate rejected: delete consumer/dispatcher acceptance requires legacy_delete_sync_enabled=false (bridge mode still true)")
		}
	}
	if cfg.AdminBFF.UserDeleteRouteEnabled {
		if !f.LegacyDeleteIAMDelegationEnabled {
			return fmt.Errorf("startup gate rejected: ADMIN_BFF_USER_DELETE_ROUTE_ENABLED requires LEGACY_DELETE_IAM_DELEGATION_ENABLED=true")
		}
		if !f.IAMDeleteEventConsumerEnabled || !f.OutboxDispatcherEnabled {
			return fmt.Errorf("startup gate rejected: ADMIN_BFF_USER_DELETE_ROUTE_ENABLED requires IAM_DELETE_EVENT_CONSUMER_ENABLED=true and OUTBOX_DISPATCHER_ENABLED=true (consumer/dispatcher must be ready first)")
		}
		if mode == nil {
			return fmt.Errorf("startup gate rejected: ADMIN_BFF_USER_DELETE_ROUTE_ENABLED requires the compatibility bridge mode row (schema not ready)")
		}
		if mode.LegacyDeleteSyncEnabled {
			return fmt.Errorf("startup gate rejected: ADMIN_BFF_USER_DELETE_ROUTE_ENABLED requires legacy_delete_sync_enabled=false (bridge mode still true)")
		}
	}
	return nil
}

// runStartupGates executes the full gate sequence and logs the outcome. The
// bridge-mode read is skipped entirely while no delete-path component is
// enabled (the default all-off configuration).
func runStartupGates(ctx context.Context, cfg *config.Config, db *database.DB, logger *slog.Logger) error {
	var mode *bridgeModeSnapshot
	needMode := cfg.Features.IAMDeleteEventConsumerEnabled ||
		cfg.Features.OutboxDispatcherEnabled ||
		cfg.AdminBFF.UserDeleteRouteEnabled
	if needMode {
		var err error
		mode, err = readBridgeMode(ctx, db)
		if err != nil {
			return err
		}
	}
	if err := checkStartupGates(cfg, mode); err != nil {
		return err
	}
	if needMode {
		logger.Info("startup gates passed",
			"delegation", cfg.Features.LegacyDeleteIAMDelegationEnabled,
			"consumer", cfg.Features.IAMDeleteEventConsumerEnabled,
			"dispatcher", cfg.Features.OutboxDispatcherEnabled,
			"user_delete_route", cfg.AdminBFF.UserDeleteRouteEnabled,
			"bridge_mode_legacy_delete_sync_enabled", mode.LegacyDeleteSyncEnabled,
			"bridge_mode_version", mode.Version,
		)
	} else {
		logger.Info("startup gates passed", "delete_path", "disabled")
	}
	return nil
}
