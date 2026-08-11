// Package platform — evidence-cleanup coordinator (T078, data-model.md
// "Retention and cleanup matrix" line 350).
//
// The coordinator is deliberately thin and module-agnostic: it evaluates the
// requested cutoff against every evidence owner (IAM, Organization) and the
// Admin BFF's unresolved-workflow watermark, refuses to delete while ANY owner
// reports unresolved/replay risk, raises the cutoff to the most conservative
// protective floor across all owners, records the dry-run counts + approval in
// the immutable audit, then invokes each owner's local purge transaction.
// If any owner is unavailable/uncertain the coordinator performs NO deletion.
// It never touches pgx/sqlc or runs cross-owner SQL — every owner-local purge
// is the owner's own adapter transaction. TrustedCleanupContext is a
// module-agnostic mirror of the recovery contexts; the composition root's
// owner adapters convert it per module, and only the app layer (after
// verifying an operator session) mints it.
package platform

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// newCleanupAuditID mints the audit row identity. uuid is not pgx/sqlc — the
// coordinator stays HTTP/pgx/sqlc-free; the SQL-backed audit adapter owns the
// pgx side.
func newCleanupAuditID() string {
	return uuid.NewString()
}

// TrustedCleanupContext mirrors the module recovery contexts so the
// coordinator never imports a module. The app-level owner adapters convert it
// into each module's TrustedRecoveryContext. It is minted only from a verified
// operator session; ApprovalID is the explicit operational approval for this
// cleanup.
type TrustedCleanupContext struct {
	PrincipalID         int64
	AuthorizationSource string
	ApprovalID          string
	CorrelationID       string
	RequestID           string
}

// EvidenceCleanupEvaluation is the dry-run result mirrored from the module
// ports. EligibleCounts is per-table eligible rows at the cutoff (the
// row-level 30-day predicates are the owner-local safety net);
// BlockingUnresolvedCount>0 means the owner has unresolved evidence and
// cleanup must not delete; OldestReplayableAt is the earliest cutoff at which
// that owner's evidence could become eligible.
type EvidenceCleanupEvaluation struct {
	EligibleCounts          map[string]int64
	BlockingUnresolvedCount int64
	OldestReplayableAt      *time.Time
}

// WorkflowWatermark mirrors the Admin BFF cleanup watermark: unresolved
// workflows keep evidence alive, and receipts are never purged shorter than
// the oldest referencing workflow.
type WorkflowWatermark struct {
	UnresolvedCount    int64
	OldestUnresolvedAt *time.Time
	OldestWorkflowAt   *time.Time
}

// EvidenceCleanupOwner is one owner's local cleanup surface. Each module
// adapter (composition root) implements it; the port calls stay owner-local.
type EvidenceCleanupOwner interface {
	EvaluateEvidenceCleanup(ctx context.Context, cutoff time.Time) (EvidenceCleanupEvaluation, error)
	PurgeEligibleEvidence(ctx context.Context, cutoff time.Time, trustedCtx TrustedCleanupContext) (map[string]int64, error)
}

// WorkflowWatermarkSource exposes the Admin BFF unresolved-workflow watermark.
type WorkflowWatermarkSource interface {
	WorkflowEvidenceWatermark(ctx context.Context) (WorkflowWatermark, error)
}

// CleanupAudit is the approved-cleanup evidence recorded before any purge.
type CleanupAudit struct {
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

// CleanupAuditStore durably records the approval + dry-run counts before the
// owner purges and finalizes the outcome afterwards. The SQL-backed adapter
// writes only through SECURITY DEFINER functions (migration 000013).
type CleanupAuditStore interface {
	RecordCleanup(ctx context.Context, a CleanupAudit) error
	FinalizeCleanup(ctx context.Context, auditID, status string, purgedCounts map[string]int64, blockedReason string) error
}

// Rejection sentinels. Owner-local refusals keep their module error identity
// (e.g. iam.ErrEvidenceCleanupBlocked) via %w.
var (
	ErrCleanupMissingApproval = errors.New("evidence cleanup requires an operational approval")
	ErrCleanupBlocked         = errors.New("evidence cleanup blocked by unresolved evidence")
	ErrCleanupUnavailable     = errors.New("evidence cleanup aborted: an owner is unavailable/uncertain")
)

// CleanupOwner names one owner for diagnostics.
type CleanupOwner struct {
	Name string
	Port EvidenceCleanupOwner
}

// CleanupCoordinator orchestrates the approved cross-owner evidence cleanup.
type CleanupCoordinator struct {
	owners    []CleanupOwner
	watermark WorkflowWatermarkSource
	audit     CleanupAuditStore
	logger    *slog.Logger
}

// NewCleanupCoordinator wires the coordinator to its owners, the BFF watermark
// source and the audit store.
func NewCleanupCoordinator(owners []CleanupOwner, watermark WorkflowWatermarkSource, audit CleanupAuditStore, logger *slog.Logger) *CleanupCoordinator {
	return &CleanupCoordinator{owners: owners, watermark: watermark, audit: audit, logger: logger}
}

// CleanupEvaluation is the coordinator's merged dry-run view.
type CleanupEvaluation struct {
	EffectiveCutoff time.Time
	EligibleCounts  map[string]int64
	BlockedReason   string
}

// Evaluate runs the conservative dry-run across every owner and the BFF
// watermark at the requested cutoff. No rows are modified and nothing is
// recorded. When any owner or the BFF reports unresolved evidence the
// coordinator returns ErrCleanupBlocked with a BlockedReason; when an owner is
// unavailable/uncertain it returns ErrCleanupUnavailable — in both cases no
// deletion may follow. The returned EffectiveCutoff is the most conservative
// cutoff (max of requested, each owner's replay floor, and the oldest
// referencing workflow).
func (c *CleanupCoordinator) Evaluate(ctx context.Context, requested time.Time) (CleanupEvaluation, error) {
	effective := requested
	eligible := map[string]int64{}
	unresolvedTotal := int64(0)

	for _, owner := range c.owners {
		eval, err := owner.Port.EvaluateEvidenceCleanup(ctx, requested)
		if err != nil {
			return CleanupEvaluation{}, fmt.Errorf("%w: %s: %v", ErrCleanupUnavailable, owner.Name, err)
		}
		for table, count := range eval.EligibleCounts {
			eligible[table] = count
		}
		unresolvedTotal += eval.BlockingUnresolvedCount
		if eval.OldestReplayableAt != nil && eval.OldestReplayableAt.After(effective) {
			effective = *eval.OldestReplayableAt
		}
	}

	watermark, err := c.watermark.WorkflowEvidenceWatermark(ctx)
	if err != nil {
		return CleanupEvaluation{}, fmt.Errorf("%w: admin bff watermark: %v", ErrCleanupUnavailable, err)
	}
	unresolvedTotal += watermark.UnresolvedCount
	if watermark.OldestUnresolvedAt != nil && watermark.OldestUnresolvedAt.After(effective) {
		effective = *watermark.OldestUnresolvedAt
	}
	if watermark.OldestWorkflowAt != nil && watermark.OldestWorkflowAt.After(effective) {
		effective = *watermark.OldestWorkflowAt
	}

	if unresolvedTotal > 0 {
		return CleanupEvaluation{}, ErrCleanupBlocked
	}
	return CleanupEvaluation{
		EffectiveCutoff: effective,
		EligibleCounts:  eligible,
	}, nil
}

// Purge runs the approved cleanup pipeline: re-evaluate conservatively (fresh
// dry-run immediately before any deletion — "uncertain → delete nothing"),
// require an explicit operational approval, record the dry-run counts +
// approval in the immutable audit, invoke each owner's local purge
// transaction, and finalize the audit. A too-early requested cutoff simply
// matches nothing (row-level 30-day predicates). Returns merged per-table
// purged counts.
func (c *CleanupCoordinator) Purge(ctx context.Context, requested time.Time, trusted TrustedCleanupContext) (map[string]int64, error) {
	if trusted.ApprovalID == "" {
		return nil, ErrCleanupMissingApproval
	}
	eval, err := c.Evaluate(ctx, requested)
	if err != nil {
		return nil, err
	}

	auditID := newCleanupAuditID()
	audit := CleanupAudit{
		AuditID:             auditID,
		ApprovalID:          trusted.ApprovalID,
		PrincipalID:         fmt.Sprintf("%d", trusted.PrincipalID),
		AuthorizationSource: trusted.AuthorizationSource,
		RequestID:           trusted.RequestID,
		CorrelationID:       trusted.CorrelationID,
		RequestedCutoff:     requested,
		EffectiveCutoff:     eval.EffectiveCutoff,
		DryRunCounts:        eval.EligibleCounts,
	}
	if err := c.audit.RecordCleanup(ctx, audit); err != nil {
		return nil, fmt.Errorf("record cleanup audit: %w", err)
	}
	c.logger.Info("cleanup approved", "audit_id", auditID, "effective_cutoff", eval.EffectiveCutoff.String())

	purged := map[string]int64{}
	for _, owner := range c.owners {
		counts, err := owner.Port.PurgeEligibleEvidence(ctx, eval.EffectiveCutoff, trusted)
		if err != nil {
			// An owner refused (e.g. concurrent unresolved surfaced in its own
			// transaction) or failed: no further deletion, audit finalized as
			// blocked/failed with the partial counts purged so far.
			if errors.Is(err, ErrCleanupBlocked) {
				finalizeErr := c.audit.FinalizeCleanup(ctx, auditID, "blocked", purged, err.Error())
				if finalizeErr != nil {
					return purged, fmt.Errorf("finalize blocked cleanup audit: %w", finalizeErr)
				}
				return purged, err
			}
			finalizeErr := c.audit.FinalizeCleanup(ctx, auditID, "failed", purged, err.Error())
			if finalizeErr != nil {
				return purged, fmt.Errorf("finalize failed cleanup audit: %w", finalizeErr)
			}
			return purged, fmt.Errorf("%s purge: %w", owner.Name, err)
		}
		for table, count := range counts {
			purged[table] = count
		}
	}

	if err := c.audit.FinalizeCleanup(ctx, auditID, "succeeded", purged, ""); err != nil {
		return purged, fmt.Errorf("finalize cleanup audit: %w", err)
	}
	return purged, nil
}
