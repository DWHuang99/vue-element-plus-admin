// Evidence-cleanup composition-root adapters (T078): the platform coordinator
// is module-agnostic, so the only package allowed to import every concrete
// adapter converts its mirrored types to each module's ports. Each owner
// adapter is a thin wrapper over the module's EvidenceCleanup adapter; the
// watermark source wraps the Admin BFF workflow store; the audit store wraps
// the SECURITY DEFINER-backed database functions. No cross-owner SQL runs
// anywhere — every purge is the owner's own local transaction.
package adminapi

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hdw/vue-element-plus-admin/backend/internal/adminbff"
	"github.com/hdw/vue-element-plus-admin/backend/internal/database"
	"github.com/hdw/vue-element-plus-admin/backend/internal/iam"
	iampostgres "github.com/hdw/vue-element-plus-admin/backend/internal/iam/postgres"
	"github.com/hdw/vue-element-plus-admin/backend/internal/organization"
	orgpostgres "github.com/hdw/vue-element-plus-admin/backend/internal/organization/postgres"
	"github.com/hdw/vue-element-plus-admin/backend/internal/platform"
)

// iamCleanupOwner adapts the IAM owner-local cleanup port.
type iamCleanupOwner struct {
	inner *iampostgres.EvidenceCleanup
}

func (o *iamCleanupOwner) EvaluateEvidenceCleanup(ctx context.Context, cutoff time.Time) (platform.EvidenceCleanupEvaluation, error) {
	eval, err := o.inner.EvaluateEvidenceCleanup(ctx, cutoff)
	if err != nil {
		return platform.EvidenceCleanupEvaluation{}, err
	}
	return platform.EvidenceCleanupEvaluation{
		EligibleCounts:          eval.EligibleCounts,
		BlockingUnresolvedCount: eval.BlockingUnresolvedCount,
		OldestReplayableAt:      eval.OldestReplayableAt,
	}, nil
}

func (o *iamCleanupOwner) PurgeEligibleEvidence(ctx context.Context, cutoff time.Time, trusted platform.TrustedCleanupContext) (map[string]int64, error) {
	counts, err := o.inner.PurgeEligibleEvidence(ctx, cutoff, iam.TrustedRecoveryContext{
		PrincipalID:         trusted.PrincipalID,
		AuthorizationSource: trusted.AuthorizationSource,
		ApprovalID:          trusted.ApprovalID,
		CorrelationID:       trusted.CorrelationID,
		RequestID:           trusted.RequestID,
	})
	// A concurrent unresolved outbox event refused the purge inside IAM's own
	// transaction. Translate the module refusal to the platform sentinel so the
	// coordinator classifies it as 'blocked' (no deletion) rather than 'failed'.
	if err != nil && errors.Is(err, iam.ErrEvidenceCleanupBlocked) {
		return counts, fmt.Errorf("%w: %v", platform.ErrCleanupBlocked, err)
	}
	return counts, err
}

// orgCleanupOwner adapts the Organization owner-local cleanup port.
type orgCleanupOwner struct {
	inner *orgpostgres.EvidenceCleanup
}

func (o *orgCleanupOwner) EvaluateEvidenceCleanup(ctx context.Context, cutoff time.Time) (platform.EvidenceCleanupEvaluation, error) {
	eval, err := o.inner.EvaluateEvidenceCleanup(ctx, cutoff)
	if err != nil {
		return platform.EvidenceCleanupEvaluation{}, err
	}
	return platform.EvidenceCleanupEvaluation{
		EligibleCounts:          eval.EligibleCounts,
		BlockingUnresolvedCount: eval.BlockingUnresolvedCount,
		OldestReplayableAt:      eval.OldestReplayableAt,
	}, nil
}

func (o *orgCleanupOwner) PurgeEligibleEvidence(ctx context.Context, cutoff time.Time, trusted platform.TrustedCleanupContext) (map[string]int64, error) {
	return o.inner.PurgeEligibleEvidence(ctx, cutoff, organization.TrustedRecoveryContext{
		PrincipalID:         trusted.PrincipalID,
		AuthorizationSource: trusted.AuthorizationSource,
		ApprovalID:          trusted.ApprovalID,
		CorrelationID:       trusted.CorrelationID,
		RequestID:           trusted.RequestID,
	})
}

// workflowWatermarkSource adapts the Admin BFF workflow store to the
// coordinator's watermark port.
type workflowWatermarkSource struct {
	store adminbff.WorkflowStore
}

func (s *workflowWatermarkSource) WorkflowEvidenceWatermark(ctx context.Context) (platform.WorkflowWatermark, error) {
	wm, err := s.store.WorkflowEvidenceWatermark(ctx)
	if err != nil {
		return platform.WorkflowWatermark{}, err
	}
	return platform.WorkflowWatermark{
		UnresolvedCount:    wm.UnresolvedCount,
		OldestUnresolvedAt: wm.OldestUnresolvedAt,
		OldestWorkflowAt:   wm.OldestWorkflowAt,
	}, nil
}

// cleanupAuditStore adapts the SECURITY DEFINER-backed audit functions to the
// coordinator's audit port.
type cleanupAuditStore struct {
	pool *pgxpool.Pool
}

func (s *cleanupAuditStore) RecordCleanup(ctx context.Context, a platform.CleanupAudit) error {
	return database.RecordCleanupAudit(ctx, s.pool, database.CleanupAuditRecord{
		AuditID:             a.AuditID,
		ApprovalID:          a.ApprovalID,
		PrincipalID:         a.PrincipalID,
		AuthorizationSource: a.AuthorizationSource,
		RequestID:           a.RequestID,
		CorrelationID:       a.CorrelationID,
		RequestedCutoff:     a.RequestedCutoff,
		EffectiveCutoff:     a.EffectiveCutoff,
		DryRunCounts:        a.DryRunCounts,
	})
}

func (s *cleanupAuditStore) FinalizeCleanup(ctx context.Context, auditID, status string, purgedCounts map[string]int64, blockedReason string) error {
	return database.FinalizeCleanupAudit(ctx, s.pool, auditID, status, purgedCounts, blockedReason)
}

// compile-time assertions: the adapters satisfy the coordinator ports.
var (
	_ platform.EvidenceCleanupOwner    = (*iamCleanupOwner)(nil)
	_ platform.EvidenceCleanupOwner    = (*orgCleanupOwner)(nil)
	_ platform.WorkflowWatermarkSource = (*workflowWatermarkSource)(nil)
	_ platform.CleanupAuditStore       = (*cleanupAuditStore)(nil)
)
