package adminapi

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hdw/vue-element-plus-admin/backend/internal/adminbff"
	"github.com/hdw/vue-element-plus-admin/backend/internal/iam"
	"github.com/hdw/vue-element-plus-admin/backend/internal/iam/postgres"
	"github.com/hdw/vue-element-plus-admin/backend/internal/platform/observability"
)

// Fake stores only need the exact additive read the collector asserts; the
// collectors take the anonymous interface, so these small structs satisfy it.
type fakeOutbox struct {
	backlog iam.OutboxBacklog
	err     error
}

func (f *fakeOutbox) GetOutboxBacklog(ctx context.Context) (iam.OutboxBacklog, error) {
	return f.backlog, f.err
}

type fakeProvisioning struct {
	snap postgres.ProvisioningSnapshot
	err  error
}

func (f *fakeProvisioning) GetProvisioningSnapshot(ctx context.Context) (postgres.ProvisioningSnapshot, error) {
	return f.snap, f.err
}

type fakeInboxMetrics struct {
	m   map[string]int64
	err error
}

func (f *fakeInboxMetrics) GetInboxMetrics(ctx context.Context) (map[string]int64, error) {
	return f.m, f.err
}

type fakeWorkflowCounts struct {
	counts map[adminbff.WorkflowState]int64
	err    error
}

func (f *fakeWorkflowCounts) CountWorkflowsByState(ctx context.Context, state adminbff.WorkflowState) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	return f.counts[state], nil
}

func gauge(t *testing.T, reg *observability.Registry, name string) (float64, bool) {
	t.Helper()
	for _, m := range reg.Snapshot() {
		if m.Name == name {
			return m.Value, true
		}
	}
	return 0, false
}

func TestOutboxCollector_MapsBacklogToGauges(t *testing.T) {
	now := time.Now()
	backlog := iam.OutboxBacklog{
		PendingCount:             3,
		LeasedCount:              1,
		BlockedCount:             2,
		PublishedCount:           41,
		OldestPendingAt:          &now,
		OldestLeasedUntil:        &now,
		LeaseRecoveryCount:       7,
		StaleClaimRejectionCount: 9,
	}
	reg := observability.NewRegistry()
	c := outboxCollector{store: &fakeOutbox{backlog: backlog}}
	c.Collect(context.Background(), reg)

	for name, want := range map[string]float64{
		"outbox_pending": 3, "outbox_leased": 1, "outbox_blocked": 2, "outbox_published": 41,
		"outbox_lease_recoveries_total": 7, "outbox_stale_claim_rejections_total": 9,
	} {
		v, ok := gauge(t, reg, name)
		require.Truef(t, ok, "gauge %s present", name)
		assert.Equal(t, want, v)
	}
	for _, age := range []string{"outbox_oldest_pending_age_seconds", "outbox_oldest_lease_age_seconds"} {
		v, ok := gauge(t, reg, age)
		require.Truef(t, ok, "gauge %s present", age)
		assert.GreaterOrEqual(t, v, float64(0))
	}
}

func TestOutboxCollector_NilAgesOmitted(t *testing.T) {
	reg := observability.NewRegistry()
	c := outboxCollector{store: &fakeOutbox{backlog: iam.OutboxBacklog{PendingCount: 1}}}
	c.Collect(context.Background(), reg)

	_, ok := gauge(t, reg, "outbox_oldest_pending_age_seconds")
	assert.False(t, ok, "nil age must not create a gauge")
}

func TestOutboxCollector_ErrorBumpsFailureCounter(t *testing.T) {
	reg := observability.NewRegistry()
	c := outboxCollector{store: &fakeOutbox{err: errors.New("db down")}}
	c.Collect(context.Background(), reg)

	assert.Equal(t, int64(1), reg.CounterValue("collect_outbox_failures_total"))
	_, ok := gauge(t, reg, "outbox_pending")
	assert.False(t, ok, "failed read must not fabricate zero gauges")
}

func TestProvisioningCollector(t *testing.T) {
	now := time.Now()
	reg := observability.NewRegistry()
	c := provisioningCollector{store: &fakeProvisioning{
		snap: postgres.ProvisioningSnapshot{Count: 2, OldestCreatedAt: &now},
	}}
	c.Collect(context.Background(), reg)

	assert.Equal(t, float64(2), mustGauge(t, reg, "provisioning_count"))
	assert.GreaterOrEqual(t, mustGauge(t, reg, "provisioning_oldest_age_seconds"), float64(0))
}

func TestProvisioningCollector_EmptyPopulation(t *testing.T) {
	reg := observability.NewRegistry()
	c := provisioningCollector{store: &fakeProvisioning{snap: postgres.ProvisioningSnapshot{Count: 0}}}
	c.Collect(context.Background(), reg)

	assert.Equal(t, float64(0), mustGauge(t, reg, "provisioning_count"))
	_, ok := gauge(t, reg, "provisioning_oldest_age_seconds")
	assert.False(t, ok, "nil oldest must not create an age gauge")
}

func TestInboxCollector_DefaultsAbsentKeysToZero(t *testing.T) {
	reg := observability.NewRegistry()
	c := inboxCollector{store: &fakeInboxMetrics{m: map[string]int64{"inbox_processed": 5}}}
	c.Collect(context.Background(), reg)

	assert.Equal(t, float64(5), mustGauge(t, reg, "inbox_processed_total"))
	assert.Equal(t, float64(0), mustGauge(t, reg, "inbox_duplicates_total"))
	assert.Equal(t, float64(0), mustGauge(t, reg, "inbox_handler_failures_total"))
}

func TestWorkflowCollector_CountsFailureStates(t *testing.T) {
	reg := observability.NewRegistry()
	c := workflowCollector{store: &fakeWorkflowCounts{counts: map[adminbff.WorkflowState]int64{
		adminbff.WorkflowFailedRetryable: 2,
		adminbff.WorkflowFailedManual:    1,
		adminbff.WorkflowCompensating:    3,
	}}}
	c.Collect(context.Background(), reg)

	assert.Equal(t, float64(2), mustGauge(t, reg, "workflow_failed_retryable"))
	assert.Equal(t, float64(1), mustGauge(t, reg, "workflow_failed_manual"))
	assert.Equal(t, float64(3), mustGauge(t, reg, "workflow_compensating"))
}

func TestWorkflowCollector_ErrorStopsPass(t *testing.T) {
	reg := observability.NewRegistry()
	c := workflowCollector{store: &fakeWorkflowCounts{err: errors.New("db down")}}
	c.Collect(context.Background(), reg)

	assert.Equal(t, int64(1), reg.CounterValue("collect_workflow_failures_total"))
	_, ok := gauge(t, reg, "workflow_failed_retryable")
	assert.False(t, ok)
}

func mustGauge(t *testing.T, reg *observability.Registry, name string) float64 {
	t.Helper()
	v, ok := gauge(t, reg, name)
	require.Truef(t, ok, "gauge %s present", name)
	return v
}
