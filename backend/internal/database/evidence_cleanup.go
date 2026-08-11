// Platform evidence-cleanup audit (T078): the immutable
// platform_evidence_cleanup_audit table (migration 000013) is written only
// through the two SECURITY DEFINER functions — record_cleanup_audit (approval
// + dry-run counts recorded before any purge) and finalize_cleanup_audit
// (succeeded/blocked/failed outcome with purged counts or the block reason).
// Runtime code never writes the table directly. Caller identity fields come
// from the trusted Platform operations context, never from caller-supplied
// display text.
package database

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hdw/vue-element-plus-admin/backend/internal/database/sqlc"
)

// CleanupAuditRecord is the approval + dry-run evidence recorded before the
// Platform coordinator invokes owner-local purges.
type CleanupAuditRecord struct {
	AuditID             string
	ApprovalID          string
	PrincipalID         string
	AuthorizationSource string
	RequestID           string
	CorrelationID       string
	RequestedCutoff     time.Time
	EffectiveCutoff     time.Time
	DryRunCounts        map[string]int64
}

// RecordCleanupAudit durably records an approved evidence-cleanup before any
// owner-local purge runs. DryRunCounts is the per-table eligibility snapshot
// the coordinator evaluated.
func RecordCleanupAudit(ctx context.Context, pool *pgxpool.Pool, r CleanupAuditRecord) error {
	auditID, err := uuid.Parse(r.AuditID)
	if err != nil {
		return fmt.Errorf("invalid cleanup audit id: %w", err)
	}
	dryRun, err := json.Marshal(r.DryRunCounts)
	if err != nil {
		return fmt.Errorf("marshal cleanup dry-run counts: %w", err)
	}
	_, err = sqlc.New(pool).RecordCleanupAudit(ctx, sqlc.RecordCleanupAuditParams{
		Column1:              pgtype.UUID{Bytes: auditID, Valid: true},
		PApprovalID:          r.ApprovalID,
		PPrincipalID:         r.PrincipalID,
		PAuthorizationSource: r.AuthorizationSource,
		PRequestID:           r.RequestID,
		PCorrelationID:       r.CorrelationID,
		Column7:              tsPg(r.RequestedCutoff),
		Column8:              tsPg(r.EffectiveCutoff),
		Column9:              dryRun,
	})
	if err != nil {
		return fmt.Errorf("record cleanup audit: %w", err)
	}
	return nil
}

// FinalizeCleanupAudit transitions an approved_pending audit row to its
// terminal outcome: succeeded (with the per-table purged counts), blocked (no
// deletion, with the reason) or failed (owner-local purge error, with the
// partial counts purged so far). A row not in approved_pending state raises
// cleanup_audit_not_pending.
func FinalizeCleanupAudit(ctx context.Context, pool *pgxpool.Pool, auditID, status string, purgedCounts map[string]int64, blockedReason string) error {
	id, err := uuid.Parse(auditID)
	if err != nil {
		return fmt.Errorf("invalid cleanup audit id: %w", err)
	}
	var purged []byte
	if purgedCounts != nil {
		purged, err = json.Marshal(purgedCounts)
		if err != nil {
			return fmt.Errorf("marshal cleanup purged counts: %w", err)
		}
	}
	_, err = sqlc.New(pool).FinalizeCleanupAudit(ctx, sqlc.FinalizeCleanupAuditParams{
		Column1:        pgtype.UUID{Bytes: id, Valid: true},
		PStatus:        status,
		Column3:        purged,
		PBlockedReason: blockedReason,
	})
	if err != nil {
		return fmt.Errorf("finalize cleanup audit: %w", err)
	}
	return nil
}
