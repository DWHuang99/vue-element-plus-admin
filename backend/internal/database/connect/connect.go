package dbconnect

import (
	"context"
	"database/sql"
	"fmt"
	"vue-element-plus-admin/backend/internal/config"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func Connect(ctx context.Context, DbConfig *config.DbConfig) (*sql.DB, error) {
	database, err := sql.Open(DbConfig.Dbtype, DbConfig.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	if err := database.PingContext(ctx); err != nil {
		database.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return database, nil
}
