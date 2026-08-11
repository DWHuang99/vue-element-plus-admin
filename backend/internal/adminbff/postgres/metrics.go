// Metrics reads (T074): durable workflow state counts for the observability
// collectors. Pure additive read on the BFF-owned workflow table.
package postgres

import (
	"context"

	"github.com/hdw/vue-element-plus-admin/backend/internal/adminbff"
)

// CountWorkflowsByState counts durable workflow rows in one state (failed
// retryable/manual, compensating — the failure gauges). adminbff.WorkflowStore
// stays untouched: the composition root asserts this additive read on the
// concrete adapter.
func (s *Store) CountWorkflowsByState(ctx context.Context, state adminbff.WorkflowState) (int64, error) {
	return s.q.CountWorkflowsByState(ctx, string(state))
}
