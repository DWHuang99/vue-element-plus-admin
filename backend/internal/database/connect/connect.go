package dbconnect

import (
	"context"
	"database/sql"
	"fmt"
	"vue-element-plus-admin/backend/internal/config"
	db "vue-element-plus-admin/backend/internal/database/generated"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func Connect(ctx context.Context, DbConfig *config.DbConfig) (*db.Queries, *sql.DB) {
	// Implement the database connection logic here
	database, err := sql.Open(DbConfig.Dbtype, DbConfig.DatabaseURL)
	if err != nil {
		panic(err)
	}
	// defer database.Close() // Do not close the database here, as it will be used by the returned queries object

	if err := database.PingContext(ctx); err != nil {
		panic(err)
	}

	fmt.Println("Database connected")
	queries := db.New(database)
	return queries, database
}
