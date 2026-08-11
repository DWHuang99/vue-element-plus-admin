//go:build rollback

// Legacy delete delegation (T076): the composition root builds the
// rbac.UsersDeleteDelegate that turns the legacy POST /users/delete route
// into an IAM DeleteUsers operation (outbox event + receipt), per
// LEGACY_DELETE_IAM_DELEGATION_ENABLED. The legacy contract carries no
// expected versions, so the delegate snapshots each target's current version
// first and hands the CAS to IAM: a concurrent version change rejects the
// whole batch rather than deleting blindly (fail-safe direction).
package adminapi

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hdw/vue-element-plus-admin/backend/internal/iam"
)

// legacyUsersDeleteDelegate adapts the IAM DeleteUsers port to the legacy rbac
// delete interface. The acting principal and correlation ID come from the
// legacy auth middleware (handler layer) and become the IAM operation audit
// fields — raw tokens never travel here.
type legacyUsersDeleteDelegate struct {
	iamSvc *iam.Service
	pool   *pgxpool.Pool
}

// DeleteUsers snapshots current versions and runs the IAM batch delete. A
// missing target rejects the whole batch (ErrUserNotFound, matching the
// legacy direct delete); a version changed between snapshot and delete
// rejects with the IAM version conflict — never a silent delete.
func (d *legacyUsersDeleteDelegate) DeleteUsers(ctx context.Context, actorID int64, correlationID string, ids []int64) error {
	targets := make([]iam.DeleteTarget, 0, len(ids))
	rows, err := d.pool.Query(ctx, "SELECT id, version FROM users WHERE id = ANY($1)", ids)
	if err != nil {
		return fmt.Errorf("snapshot versions for legacy delete delegation: %w", err)
	}
	defer rows.Close()
	seen := make(map[int64]bool, len(ids))
	for rows.Next() {
		var id, version int64
		if err := rows.Scan(&id, &version); err != nil {
			return fmt.Errorf("scan version snapshot: %w", err)
		}
		seen[id] = true
		targets = append(targets, iam.DeleteTarget{UserID: id, ExpectedVersion: version})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate version snapshot: %w", err)
	}
	for _, id := range ids {
		if !seen[id] {
			return fmt.Errorf("%w: user %d", iam.ErrUserNotFound, id)
		}
	}

	_, err = d.iamSvc.DeleteUsers(ctx, iam.OperationContext{
		OperationID:   uuid.NewString(),
		ActorUserID:   actorID,
		CorrelationID: correlationID,
	}, targets)
	if err != nil {
		return fmt.Errorf("delegated user delete: %w", err)
	}
	return nil
}
