// Platform rollout-gate operations (T077): the append-only evidence table
// compatibility_rollout_gates (migration 000008) is written only through the
// two migration-000012 SECURITY DEFINER functions — record_rollout_gate_sample
// (executed by the deployed app's writer loop, once per sample cadence) and
// approve_route_disable (a short-lived authenticated Platform operation). The
// functions own the window math: any mismatch or missing sample closes the
// current record and resets the 72h window; approval requires one complete
// continuous passing window. Runtime code never writes the table directly.
package database

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hdw/vue-element-plus-admin/backend/internal/database/sqlc"
)

// tsToPg adapts a *time.Time to the sqlc pgx driver's nullable timestamp.
func tsToPg(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}

// tsPg adapts a required timestamp.
func tsPg(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

// RolloutGateSample is one cadence observation. The window math (extension
// vs. close-and-reset) lives in the SQL function; this struct carries the
// evidence fields the plan (Phase 5.9 step 6) enumerates. The cumulative
// mismatch counter and the newest last-mismatch timestamp are what the
// function compares against the previous sample to detect a mismatch.
type RolloutGateSample struct {
	Phase                    string
	CapabilityManifestHash   string
	BridgeModeVersion        int64
	BridgeLegacyDeleteSyncOn bool
	LegacyRows               int64
	NewRows                  int64
	RowVersionChecksum       string
	MismatchCount            int64
	LastMismatchAt           *time.Time
	RollbackArtifactID       string
	RollbackSuiteResult      string
	PrincipalID              string
	ObservedAt               time.Time
	MaxGapInterval           time.Duration
}

// RecordRolloutGateSample appends one evidence row via the SECURITY DEFINER
// function and returns the new gate_id. A passing sample extends the open
// window; a mismatch or a missing sample (gap beyond MaxGapInterval) closes
// the current record and resets the window.
func RecordRolloutGateSample(ctx context.Context, pool *pgxpool.Pool, s RolloutGateSample) (int64, error) {
	gateID, err := sqlc.New(pool).RecordRolloutGateSample(ctx, sqlc.RecordRolloutGateSampleParams{
		PPhase:                         s.Phase,
		PCapabilityManifestHash:        s.CapabilityManifestHash,
		PBridgeModeVersion:             s.BridgeModeVersion,
		PBridgeLegacyDeleteSyncEnabled: s.BridgeLegacyDeleteSyncOn,
		PLegacyRows:                    s.LegacyRows,
		PNewRows:                       s.NewRows,
		PRowVersionChecksum:            s.RowVersionChecksum,
		PMismatchCount:                 s.MismatchCount,
		PLastMismatchAt:                tsToPg(s.LastMismatchAt),
		PRollbackArtifactID:            s.RollbackArtifactID,
		PRollbackSuiteResult:           s.RollbackSuiteResult,
		PPrincipalID:                   s.PrincipalID,
		PObservedAt:                    tsPg(s.ObservedAt),
		PMaxGapInterval:                pgtype.Interval{Microseconds: s.MaxGapInterval.Microseconds(), Valid: true},
	})
	if err != nil {
		return 0, fmt.Errorf("record rollout gate sample: %w", err)
	}
	return gateID, nil
}

// RouteDisableApproval carries the trusted Platform-operation fields of an
// approve_route_disable call. Identity fields come from the trusted Platform
// operations context — never from caller-supplied display text.
type RouteDisableApproval struct {
	Phase       string
	ApprovalID  string
	PrincipalID string
	ObservedAt  time.Time
}

// ApproveRouteDisable records the route-disable approval for a phase, but
// only when one complete continuous passing 72h window exists. It returns
// the approval record's gate_id; an incomplete/failed window raises.
func ApproveRouteDisable(ctx context.Context, pool *pgxpool.Pool, a RouteDisableApproval) (int64, error) {
	gateID, err := sqlc.New(pool).ApproveRouteDisable(ctx, sqlc.ApproveRouteDisableParams{
		PPhase:       a.Phase,
		PApprovalID:  a.ApprovalID,
		PPrincipalID: a.PrincipalID,
		PObservedAt:  tsPg(a.ObservedAt),
	})
	if err != nil {
		return 0, fmt.Errorf("approve route disable: %w", err)
	}
	return gateID, nil
}
