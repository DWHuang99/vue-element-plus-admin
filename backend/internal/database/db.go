package database

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Config holds database connection configuration.
type Config struct {
	URL             string
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
}

// DB wraps the pgxpool connection pool.
type DB struct {
	Pool *pgxpool.Pool
}

// Connect creates a new database connection pool and verifies connectivity.
func Connect(ctx context.Context, cfg Config) (*DB, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("database connection failed: unable to parse DATABASE_URL: %w", err)
	}

	poolCfg.MaxConns = cfg.MaxConns
	poolCfg.MinConns = cfg.MinConns
	poolCfg.MaxConnLifetime = cfg.MaxConnLifetime
	poolCfg.MaxConnIdleTime = cfg.MaxConnIdleTime

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("database connection failed: unable to create connection pool: %w", err)
	}

	// Verify connectivity
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("database connection failed: unable to ping database: %w", err)
	}

	return &DB{Pool: pool}, nil
}

// Ping checks if the database is reachable.
func (db *DB) Ping(ctx context.Context) error {
	return db.Pool.Ping(ctx)
}

// Close gracefully shuts down the connection pool.
func (db *DB) Close() {
	db.Pool.Close()
}
