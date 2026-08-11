// Metrics reads (T074): provisioning-population snapshot for the
// observability collectors. Pure additive reads — no delivery or lifecycle
// semantics change.
package postgres

import (
	"context"
	"time"
)

// ProvisioningSnapshot is the provisioning-population metrics read: how many
// identities are still provisioning and when the oldest one was created
// (nil when the population is empty).
type ProvisioningSnapshot struct {
	Count           int64
	OldestCreatedAt *time.Time
}

// GetProvisioningSnapshot reads the provisioning population for metrics
// exposition (T074). The lifecycle_state column is IAM-owned; NULL min()
// (empty population) surfaces as nil OldestCreatedAt.
func (s *Store) GetProvisioningSnapshot(ctx context.Context) (ProvisioningSnapshot, error) {
	row, err := s.q.GetProvisioningSnapshot(ctx)
	if err != nil {
		return ProvisioningSnapshot{}, err
	}
	// min(created_at) is NULL for an empty population; sqlc types the
	// nullable column as interface{} — decode to *time.Time.
	var oldest *time.Time
	if ts, ok := row.OldestCreatedAt.(time.Time); ok {
		oldest = &ts
	}
	return ProvisioningSnapshot{Count: row.Count, OldestCreatedAt: oldest}, nil
}
