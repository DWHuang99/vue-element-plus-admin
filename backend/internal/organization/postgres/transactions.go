// Organization transaction support.
//
// Store is the pool-bound adapter handed to organization.NewService; RunInTx
// gives the service one Organization-local transaction per call (commit on
// nil error, rollback otherwise), keeping full-batch operations such as
// DeleteDepartments atomic. The transaction-bound txStore shares the same
// query set but refuses nesting.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hdw/vue-element-plus-admin/backend/internal/organization"
	"github.com/hdw/vue-element-plus-admin/backend/internal/organization/postgres/sqlc"
)

// Store is the pool-bound organization.Store implementation.
type Store struct {
	pool *pgxpool.Pool
	queries
}

// NewStore wires the adapter to a connection pool.
func NewStore(pool *pgxpool.Pool) organization.Store {
	return &Store{pool: pool, queries: queries{q: sqlc.New(pool)}}
}

// RunInTx executes fn on a Store bound to one Organization-local transaction.
func (s *Store) RunInTx(ctx context.Context, fn func(organization.Store) error) error {
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

// txStore is a transaction-bound organization.Store.
type txStore struct {
	queries
}

// RunInTx on a transaction-bound store is unsupported: the service never
// nests transactions, and a txStore must never escape its fn.
func (t *txStore) RunInTx(context.Context, func(organization.Store) error) error {
	return errors.New("organization: nested transactions are not supported")
}
