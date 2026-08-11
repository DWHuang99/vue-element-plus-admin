// Metrics reads (T074): the Organization-owned cumulative inbox counters
// (000011_delivery_observability) for the observability collectors. Pure
// additive read — never affects handler semantics.
package postgres

import "context"

// GetInboxMetrics reads the cumulative inbox counters (processed /
// duplicates / handler-failures) as key → count. Absent keys simply don't
// appear; the collector defaults them to 0.
func (s *Store) GetInboxMetrics(ctx context.Context) (map[string]int64, error) {
	rows, err := s.q.GetInboxMetrics(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(rows))
	for _, r := range rows {
		out[r.MetricKey] = r.Count
	}
	return out, nil
}
