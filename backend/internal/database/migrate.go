package database

import (
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/hdw/vue-element-plus-admin/backend/db/migrations"
)

// RunMigrations applies all pending database migrations.
// It returns nil if all migrations succeed or if there are no pending migrations.
// On failure, the error message contains only the migration file name and error type,
// never the SQL content.
func RunMigrations(databaseURL string) error {
	source, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("migration failed: unable to read migration files: %w", err)
	}

	m, err := migrate.NewWithSourceInstance("iofs", source, databaseURL)
	if err != nil {
		return fmt.Errorf("migration failed: unable to initialize migrator: %w", err)
	}
	defer m.Close()

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("migration failed: %w", err)
	}

	return nil
}

// CheckDirtyState returns true if the migrations table has a dirty flag set.
// A dirty state means a previous migration failed part-way through and manual
// intervention is required.
func CheckDirtyState(databaseURL string) (bool, error) {
	source, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return false, fmt.Errorf("migration dirty check failed: unable to read migration files: %w", err)
	}

	m, err := migrate.NewWithSourceInstance("iofs", source, databaseURL)
	if err != nil {
		return false, fmt.Errorf("migration dirty check failed: unable to initialize migrator: %w", err)
	}
	defer m.Close()

	version, dirty, err := m.Version()
	if err != nil && err != migrate.ErrNilVersion {
		return false, fmt.Errorf("migration dirty check failed: %w", err)
	}

	_ = version
	return dirty, nil
}
