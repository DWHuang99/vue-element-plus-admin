// Polled DB-derived collectors (T074). Each collector is one read of durable
// state that a /metrics scrape triggers through observability.Collector; no
// background loop exists. Collectors assert only the additive read methods on
// the concrete adapters (anonymous interface assertions done in wire.go) so
// the iam/organization/adminbff port interfaces and their test fakes stay
// untouched. A failed read bumps a cumulative collect_*_failures_total
// counter and leaves the gauges stale rather than fabricating zeros.
package adminapi

import (
	"context"
	"time"

	"github.com/hdw/vue-element-plus-admin/backend/internal/adminbff"
	"github.com/hdw/vue-element-plus-admin/backend/internal/iam"
	"github.com/hdw/vue-element-plus-admin/backend/internal/iam/postgres"
	"github.com/hdw/vue-element-plus-admin/backend/internal/platform/observability"
)

// outboxCollector exposes the IAM outbox backlog (T066 GetOutboxBacklog).
type outboxCollector struct {
	store interface {
		GetOutboxBacklog(ctx context.Context) (iam.OutboxBacklog, error)
	}
}

func (c outboxCollector) Collect(ctx context.Context, reg *observability.Registry) {
	b, err := c.store.GetOutboxBacklog(ctx)
	if err != nil {
		reg.AddCounter("collect_outbox_failures_total", 1)
		return
	}
	reg.SetGauge("outbox_pending", float64(b.PendingCount))
	reg.SetGauge("outbox_leased", float64(b.LeasedCount))
	reg.SetGauge("outbox_blocked", float64(b.BlockedCount))
	reg.SetGauge("outbox_published", float64(b.PublishedCount))
	reg.SetGauge("outbox_lease_recoveries_total", float64(b.LeaseRecoveryCount))
	reg.SetGauge("outbox_stale_claim_rejections_total", float64(b.StaleClaimRejectionCount))
	if b.OldestPendingAt != nil {
		reg.SetGauge("outbox_oldest_pending_age_seconds", time.Since(*b.OldestPendingAt).Seconds())
	}
	if b.OldestLeasedUntil != nil {
		reg.SetGauge("outbox_oldest_lease_age_seconds", time.Since(*b.OldestLeasedUntil).Seconds())
	}
}

// inboxCollector exposes the Organization inbox cumulative counters (T066
// organization_inbox_metrics); absent keys default to zero gauges.
type inboxCollector struct {
	store interface {
		GetInboxMetrics(ctx context.Context) (map[string]int64, error)
	}
}

func (c inboxCollector) Collect(ctx context.Context, reg *observability.Registry) {
	m, err := c.store.GetInboxMetrics(ctx)
	if err != nil {
		reg.AddCounter("collect_inbox_failures_total", 1)
		return
	}
	reg.SetGauge("inbox_processed_total", float64(m["inbox_processed"]))
	reg.SetGauge("inbox_duplicates_total", float64(m["inbox_duplicates"]))
	reg.SetGauge("inbox_handler_failures_total", float64(m["inbox_handler_failures"]))
}

// workflowCollector exposes the durable BFF workflow failure population —
// the states an operator must act on.
type workflowCollector struct {
	store interface {
		CountWorkflowsByState(ctx context.Context, state adminbff.WorkflowState) (int64, error)
	}
}

func (c workflowCollector) Collect(ctx context.Context, reg *observability.Registry) {
	for _, st := range []adminbff.WorkflowState{
		adminbff.WorkflowFailedRetryable,
		adminbff.WorkflowFailedManual,
		adminbff.WorkflowCompensating,
	} {
		n, err := c.store.CountWorkflowsByState(ctx, st)
		if err != nil {
			reg.AddCounter("collect_workflow_failures_total", 1)
			return
		}
		reg.SetGauge("workflow_"+string(st), float64(n))
	}
}

// provisioningCollector exposes the IAM provisioning population (count plus
// the age of the oldest still-provisioning identity).
type provisioningCollector struct {
	store interface {
		GetProvisioningSnapshot(ctx context.Context) (postgres.ProvisioningSnapshot, error)
	}
}

func (c provisioningCollector) Collect(ctx context.Context, reg *observability.Registry) {
	s, err := c.store.GetProvisioningSnapshot(ctx)
	if err != nil {
		reg.AddCounter("collect_provisioning_failures_total", 1)
		return
	}
	reg.SetGauge("provisioning_count", float64(s.Count))
	if s.OldestCreatedAt != nil {
		reg.SetGauge("provisioning_oldest_age_seconds", time.Since(*s.OldestCreatedAt).Seconds())
	}
}
