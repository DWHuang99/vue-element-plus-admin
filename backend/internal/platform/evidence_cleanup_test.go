// Evidence-cleanup coordinator tests (T078). The coordinator is a pure policy
// boundary: it merges owner dry-runs + the BFF watermark into the most
// conservative cutoff, refuses to delete on ANY unresolved evidence or an
// unavailable owner ("uncertain → delete nothing"), and records the approved
// cleanup in the audit before invoking each owner's local purge. All owners,
// the watermark source and the audit store are fakes here — the SQL-backed
// behavior lives in the database/module adapter tests.
package platform

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeCleanupOwner returns a canned dry-run; when purgeRefuse is set its purge
// refuses with that error. purgeRecords captures per-call cutoff/trusted ctx.
type fakeCleanupOwner struct {
	name          string
	eval          EvidenceCleanupEvaluation
	evalErr       error
	purgeCounts   map[string]int64
	purgeErr      error
	mu            sync.Mutex
	evalCalls     int
	purgeCutoffs  []time.Time
	purgedTrusted []TrustedCleanupContext
}

func (f *fakeCleanupOwner) EvaluateEvidenceCleanup(ctx context.Context, cutoff time.Time) (EvidenceCleanupEvaluation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.evalCalls++
	return f.eval, f.evalErr
}

func (f *fakeCleanupOwner) PurgeEligibleEvidence(ctx context.Context, cutoff time.Time, trusted TrustedCleanupContext) (map[string]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.purgeCutoffs = append(f.purgeCutoffs, cutoff)
	f.purgedTrusted = append(f.purgedTrusted, trusted)
	if f.purgeErr != nil {
		return nil, f.purgeErr
	}
	return f.purgeCounts, nil
}

// fakeWatermark returns a canned watermark.
type fakeWatermark struct {
	wm  WorkflowWatermark
	err error
}

func (f *fakeWatermark) WorkflowEvidenceWatermark(ctx context.Context) (WorkflowWatermark, error) {
	return f.wm, f.err
}

// fakeAuditStore records the audit lifecycle.
type fakeAuditStore struct {
	mu          sync.Mutex
	recorded    []CleanupAudit
	finalized   []string
	finalizeErr error
	recordErr   error
}

func (f *fakeAuditStore) RecordCleanup(ctx context.Context, a CleanupAudit) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recordErr != nil {
		return f.recordErr
	}
	f.recorded = append(f.recorded, a)
	return nil
}

func (f *fakeAuditStore) FinalizeCleanup(ctx context.Context, auditID, status string, purgedCounts map[string]int64, blockedReason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.finalizeErr != nil {
		return f.finalizeErr
	}
	f.finalized = append(f.finalized, auditID+":"+status)
	return nil
}

func newCoordinator(owners []CleanupOwner, wm WorkflowWatermarkSource, audit CleanupAuditStore) *CleanupCoordinator {
	return NewCleanupCoordinator(owners, wm, audit, slog.New(slog.DiscardHandler))
}

func at(t time.Time) *time.Time { return &t }

func TestCleanupEvaluate_ConservativeCutoffAndMerge(t *testing.T) {
	requested := time.Now().Add(-40 * 24 * time.Hour)
	iamFloor := time.Now().Add(-25 * 24 * time.Hour) // later than requested → raises cutoff
	orgFloor := time.Now().Add(-60 * 24 * time.Hour) // earlier → ignored
	oldestWF := time.Now().Add(-20 * 24 * time.Hour) // watermark → raises cutoff further

	iam := &fakeCleanupOwner{name: "iam", eval: EvidenceCleanupEvaluation{
		EligibleCounts:          map[string]int64{"iam.command_receipts": 3},
		BlockingUnresolvedCount: 0,
		OldestReplayableAt:      at(iamFloor),
	}}
	org := &fakeCleanupOwner{name: "org", eval: EvidenceCleanupEvaluation{
		EligibleCounts:          map[string]int64{"organization.command_receipts": 5},
		BlockingUnresolvedCount: 0,
		OldestReplayableAt:      at(orgFloor),
	}}
	c := newCoordinator([]CleanupOwner{
		{Name: "iam", Port: iam}, {Name: "org", Port: org},
	}, &fakeWatermark{wm: WorkflowWatermark{UnresolvedCount: 0, OldestWorkflowAt: at(oldestWF)}}, &fakeAuditStore{})

	ev, err := c.Evaluate(context.Background(), requested)
	require.NoError(t, err)
	assert.Equal(t, oldestWF, ev.EffectiveCutoff, "most conservative watermark wins (max of requested + floors + workflow)")
	assert.Equal(t, map[string]int64{
		"iam.command_receipts":          3,
		"organization.command_receipts": 5,
	}, ev.EligibleCounts)
	assert.Equal(t, 1, iam.evalCalls)
	assert.Equal(t, 1, org.evalCalls)
}

func TestCleanupEvaluate_BlocksOnAnyUnresolved(t *testing.T) {
	requested := time.Now().Add(-40 * 24 * time.Hour)
	cases := []struct {
		name     string
		iamBlock int64
		wmCount  int64
	}{
		{name: "owner unresolved", iamBlock: 1},
		{name: "workflow watermark unresolved", wmCount: 2},
		{name: "both unresolved", iamBlock: 3, wmCount: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			iam := &fakeCleanupOwner{name: "iam", eval: EvidenceCleanupEvaluation{
				EligibleCounts:          map[string]int64{"iam.command_receipts": 1},
				BlockingUnresolvedCount: tc.iamBlock,
			}}
			org := &fakeCleanupOwner{name: "org", eval: EvidenceCleanupEvaluation{BlockingUnresolvedCount: 0}}
			c := newCoordinator([]CleanupOwner{{Name: "iam", Port: iam}, {Name: "org", Port: org}},
				&fakeWatermark{wm: WorkflowWatermark{UnresolvedCount: tc.wmCount}}, &fakeAuditStore{})

			_, err := c.Evaluate(context.Background(), requested)
			require.ErrorIs(t, err, ErrCleanupBlocked)
		})
	}
}

func TestCleanupEvaluate_UnavailableOwnerPerformsNoDeletion(t *testing.T) {
	iam := &fakeCleanupOwner{name: "iam", eval: EvidenceCleanupEvaluation{BlockingUnresolvedCount: 0}}
	org := &fakeCleanupOwner{name: "org", eval: EvidenceCleanupEvaluation{}, evalErr: errors.New("org down")}
	c := newCoordinator([]CleanupOwner{{Name: "iam", Port: iam}, {Name: "org", Port: org}},
		&fakeWatermark{}, &fakeAuditStore{})

	_, err := c.Evaluate(context.Background(), time.Now())
	require.ErrorIs(t, err, ErrCleanupUnavailable)
	assert.Contains(t, err.Error(), "org")
}

func TestCleanupEvaluate_WatermarkUnavailablePerformsNoDeletion(t *testing.T) {
	iam := &fakeCleanupOwner{name: "iam", eval: EvidenceCleanupEvaluation{BlockingUnresolvedCount: 0}}
	c := newCoordinator([]CleanupOwner{{Name: "iam", Port: iam}},
		&fakeWatermark{err: errors.New("bff down")}, &fakeAuditStore{})

	_, err := c.Evaluate(context.Background(), time.Now())
	require.ErrorIs(t, err, ErrCleanupUnavailable)
}

func TestCleanupPurge_RequiresApproval(t *testing.T) {
	iam := &fakeCleanupOwner{name: "iam", eval: EvidenceCleanupEvaluation{BlockingUnresolvedCount: 0}}
	audit := &fakeAuditStore{}
	c := newCoordinator([]CleanupOwner{{Name: "iam", Port: iam}}, &fakeWatermark{}, audit)

	_, err := c.Purge(context.Background(), time.Now(), TrustedCleanupContext{ApprovalID: ""})
	require.ErrorIs(t, err, ErrCleanupMissingApproval)
	assert.Empty(t, audit.recorded, "no audit row without an approval")
	assert.Equal(t, 0, iam.evalCalls, "no dry-run without an approval")
}

func TestCleanupPurge_FullPipelineRecordsAndPurges(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	requested := now.Add(-45 * 24 * time.Hour)
	iamFloor := now.Add(-30 * 24 * time.Hour) // later than requested → effective cutoff raised
	iam := &fakeCleanupOwner{name: "iam", eval: EvidenceCleanupEvaluation{
		EligibleCounts: map[string]int64{"iam.command_receipts": 3}, BlockingUnresolvedCount: 0,
		OldestReplayableAt: at(iamFloor),
	}, purgeCounts: map[string]int64{"iam.command_receipts": 3}}
	org := &fakeCleanupOwner{name: "org", eval: EvidenceCleanupEvaluation{
		EligibleCounts: map[string]int64{"organization.inbox_messages": 2}, BlockingUnresolvedCount: 0,
	}, purgeCounts: map[string]int64{"organization.inbox_messages": 2}}
	audit := &fakeAuditStore{}
	trusted := TrustedCleanupContext{PrincipalID: 7, AuthorizationSource: "session", ApprovalID: "appr-1", RequestID: "req-1", CorrelationID: "corr-1"}
	c := newCoordinator([]CleanupOwner{{Name: "iam", Port: iam}, {Name: "org", Port: org}}, &fakeWatermark{}, audit)

	purged, err := c.Purge(context.Background(), requested, trusted)
	require.NoError(t, err)
	assert.Equal(t, map[string]int64{"iam.command_receipts": 3, "organization.inbox_messages": 2}, purged)

	require.Len(t, audit.recorded, 1)
	rec := audit.recorded[0]
	assert.Equal(t, "appr-1", rec.ApprovalID)
	assert.Equal(t, "7", rec.PrincipalID)
	assert.Equal(t, "session", rec.AuthorizationSource)
	assert.Equal(t, "req-1", rec.RequestID)
	assert.Equal(t, "corr-1", rec.CorrelationID)
	assert.Equal(t, requested, rec.RequestedCutoff)
	assert.Equal(t, iamFloor, rec.EffectiveCutoff, "most conservative cutoff is the IAM replay floor")
	assert.Equal(t, map[string]int64{"iam.command_receipts": 3, "organization.inbox_messages": 2}, rec.DryRunCounts)
	assert.NotEmpty(t, rec.AuditID)

	assert.Len(t, audit.finalized, 1)
	assert.Equal(t, rec.AuditID+":succeeded", audit.finalized[0])

	// Both owners purged at the SAME effective cutoff with the SAME identity.
	assert.Len(t, iam.purgeCutoffs, 1)
	assert.Len(t, org.purgeCutoffs, 1)
	assert.Equal(t, iam.purgeCutoffs[0], org.purgeCutoffs[0])
	for _, oc := range []*fakeCleanupOwner{iam, org} {
		require.Len(t, oc.purgedTrusted, 1)
		assert.Equal(t, trusted, oc.purgedTrusted[0])
	}
}

func TestCleanupPurge_OwnerRefusalFinalizesBlocked(t *testing.T) {
	requested := time.Now().Add(-45 * 24 * time.Hour)
	iam := &fakeCleanupOwner{name: "iam", eval: EvidenceCleanupEvaluation{
		EligibleCounts: map[string]int64{"iam.command_receipts": 3}, BlockingUnresolvedCount: 0,
	}, purgeCounts: map[string]int64{"iam.command_receipts": 3}}
	// The owner adapter maps a module refusal (concurrent unresolved surfaced in
	// its own transaction) to the platform sentinel; the coordinator must
	// finalize the audit as 'blocked', not 'failed'.
	org := &fakeCleanupOwner{name: "org", eval: EvidenceCleanupEvaluation{BlockingUnresolvedCount: 0},
		purgeErr: ErrCleanupBlocked,
	}
	audit := &fakeAuditStore{}
	c := newCoordinator([]CleanupOwner{{Name: "iam", Port: iam}, {Name: "org", Port: org}}, &fakeWatermark{}, audit)

	purged, err := c.Purge(context.Background(), requested, TrustedCleanupContext{ApprovalID: "appr-1", PrincipalID: 7})
	require.ErrorIs(t, err, ErrCleanupBlocked)
	assert.Equal(t, map[string]int64{"iam.command_receipts": 3}, purged, "IAM's purge already committed; org's refusal stops the rest")
	assert.Len(t, audit.recorded, 1)
	assert.Len(t, audit.finalized, 1)
	assert.Equal(t, audit.recorded[0].AuditID+":blocked", audit.finalized[0], "owner refusal finalizes as blocked")
}

func TestCleanupPurge_OwnerErrorFinalizesFailed(t *testing.T) {
	requested := time.Now().Add(-45 * 24 * time.Hour)
	iam := &fakeCleanupOwner{name: "iam", eval: EvidenceCleanupEvaluation{BlockingUnresolvedCount: 0},
		purgeErr: errors.New("iam tx failed"),
	}
	audit := &fakeAuditStore{}
	c := newCoordinator([]CleanupOwner{{Name: "iam", Port: iam}}, &fakeWatermark{}, audit)

	_, err := c.Purge(context.Background(), requested, TrustedCleanupContext{ApprovalID: "appr-1"})
	require.Error(t, err)
	assert.Len(t, audit.finalized, 1)
	assert.Equal(t, audit.recorded[0].AuditID+":failed", audit.finalized[0], "owner failure finalizes as failed")
}
