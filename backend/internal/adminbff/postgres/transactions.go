// Admin BFF workflow-store transaction support.
//
// Store is the pool-bound adapter handed to the BFF service composition;
// RunInTx gives the workflow service one BFF-local transaction per call
// (commit on nil error, rollback otherwise), keeping multi-statement units
// such as "insert all delete-batch subjects" or "persist tombstones + complete
// the workflow" atomic. The transaction-bound txStore shares the same query
// set but refuses nesting.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hdw/vue-element-plus-admin/backend/internal/adminbff"
	"github.com/hdw/vue-element-plus-admin/backend/internal/adminbff/postgres/sqlc"
)

// Store is the pool-bound adminbff.WorkflowStore implementation.
type Store struct {
	pool *pgxpool.Pool
	queries
}

// NewStore wires the adapter to a connection pool.
func NewStore(pool *pgxpool.Pool) adminbff.WorkflowStore {
	return &Store{pool: pool, queries: queries{q: sqlc.New(pool)}}
}

// RunInTx executes fn on a Store bound to one BFF-local transaction.
func (s *Store) RunInTx(ctx context.Context, fn func(adminbff.WorkflowStore) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(&txStore{queries: queries{q: sqlc.New(tx)}}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// txStore is a transaction-bound adminbff.WorkflowStore.
type txStore struct {
	queries
}

// RunInTx on a transaction-bound store is unsupported: the service never
// nests transactions, and a txStore must never escape its fn.
func (t *txStore) RunInTx(context.Context, func(adminbff.WorkflowStore) error) error {
	return errors.New("adminbff: nested transactions are not supported")
}
