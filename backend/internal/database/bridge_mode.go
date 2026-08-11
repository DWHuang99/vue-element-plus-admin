// Platform-owned bridge-mode operations (T076): the controlled transition of
// compatibility_bridge_mode.legacy_delete_sync_enabled is a short-lived
// authenticated Platform operation — it runs the migration 000008 SECURITY
// DEFINER function (owner platform_owner), which CAS-bumps the mode row and
// appends an immutable compatibility_bridge_mode_changes audit row in the
// same transaction. Runtime code never writes the mode row directly.
package database

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hdw/vue-element-plus-admin/backend/internal/database/sqlc"
)

// BridgeModeTransition carries the Platform-operation fields recorded in the
// immutable audit row. Caller identity fields come from the trusted Platform
// operations context — never from caller-supplied display text. Empty
// approval/request/correlation strings are recorded as empty values (the SQL
// layer's NULL equivalent for those columns).
type BridgeModeTransition struct {
	ExpectedVersion int64
	Enabled         bool
	PrincipalID     string
	// AuthorizationSource names the trusted mechanism that authenticated the
	// operation (e.g. a Platform ops credential), never a raw credential.
	AuthorizationSource string
	ApprovalID          string
	ReasonCode          string
	RequestID           string
	CorrelationID       string
}

// SetLegacyDeleteSyncMode runs the Platform mode switch. It returns the
// resulting mode version: the bumped version on a real transition, the
// current version on an idempotent replay whose target is already achieved.
// A stale CAS with a different target raises (version conflict).
func SetLegacyDeleteSyncMode(ctx context.Context, pool *pgxpool.Pool, t BridgeModeTransition) (int64, error) {
	version, err := sqlc.New(pool).SetLegacyDeleteSyncMode(ctx, sqlc.SetLegacyDeleteSyncModeParams{
		PExpectedVersion:     t.ExpectedVersion,
		PEnabled:             t.Enabled,
		PPrincipalID:         t.PrincipalID,
		PAuthorizationSource: t.AuthorizationSource,
		PApprovalID:          t.ApprovalID,
		PReasonCode:          t.ReasonCode,
		PRequestID:           t.RequestID,
		PCorrelationID:       t.CorrelationID,
	})
	if err != nil {
		return 0, fmt.Errorf("set legacy delete sync mode: %w", err)
	}
	return version, nil
}
