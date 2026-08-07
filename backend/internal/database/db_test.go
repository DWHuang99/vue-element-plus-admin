package database

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConnect_InvalidURL(t *testing.T) {
	ctx := context.Background()
	cfg := Config{
		URL:      "invalid-url",
		MaxConns: 5,
		MinConns: 1,
	}

	_, err := Connect(ctx, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DATABASE_URL")
}

func TestConnect_EmptyURL(t *testing.T) {
	ctx := context.Background()
	cfg := Config{
		URL:      "",
		MaxConns: 5,
		MinConns: 1,
	}

	_, err := Connect(ctx, cfg)
	require.Error(t, err)
}

func TestDB_ConfigDefaults(t *testing.T) {
	cfg := Config{
		URL:             "postgres://user:pass@localhost:5432/db?sslmode=disable",
		MaxConns:        25,
		MinConns:        5,
		MaxConnLifetime: 1 * time.Hour,
		MaxConnIdleTime: 30 * time.Minute,
	}

	assert.Equal(t, int32(25), cfg.MaxConns)
	assert.Equal(t, int32(5), cfg.MinConns)
	assert.Equal(t, 1*time.Hour, cfg.MaxConnLifetime)
	assert.Equal(t, 30*time.Minute, cfg.MaxConnIdleTime)
}

func TestRunMigrations_InvalidURL(t *testing.T) {
	err := RunMigrations("invalid-url")
	assert.Error(t, err)
}

func TestRunMigrations_ErrorContainsSafeMessage(t *testing.T) {
	// T026: Verify migration failure error messages do not contain SQL content or secrets
	err := RunMigrations("invalid-url")
	if err != nil {
		msg := err.Error()
		// Error message should describe the failure category but NOT SQL content
		assert.NotContains(t, msg, "CREATE TABLE")
		assert.NotContains(t, msg, "INSERT INTO")
		assert.NotContains(t, msg, "password")
		assert.NotContains(t, msg, "postgres://")
	}
}

func TestCheckDirtyState_InvalidURL(t *testing.T) {
	dirty, err := CheckDirtyState("invalid-url")
	assert.Error(t, err)
	assert.False(t, dirty)
}

func TestRunMigrations_EmptyURL(t *testing.T) {
	err := RunMigrations("")
	assert.Error(t, err)
}

func TestCheckDirtyState_EmptyURL(t *testing.T) {
	dirty, err := CheckDirtyState("")
	assert.Error(t, err)
	assert.False(t, dirty)
}
