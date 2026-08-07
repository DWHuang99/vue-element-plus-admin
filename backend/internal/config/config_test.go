package config

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_ValidConfig(t *testing.T) {
	// Set required env vars
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db?sslmode=disable")

	cfg, err := Load()

	require.NoError(t, err)
	assert.Equal(t, "0.0.0.0", cfg.Server.Host)
	assert.Equal(t, 8080, cfg.Server.Port)
	assert.Equal(t, "postgres://user:pass@localhost:5432/db?sslmode=disable", cfg.Database.URL)
	assert.Equal(t, int32(25), cfg.Database.MaxConns)
	assert.Equal(t, int32(5), cfg.Database.MinConns)
	assert.Equal(t, "info", cfg.Log.Level)
	assert.Equal(t, "text", cfg.Log.Format)
	// Rate limit defaults
	assert.True(t, cfg.RateLimit.Enabled)
	assert.Equal(t, 10, cfg.RateLimit.RegisterIPHour)
	assert.Equal(t, 10, cfg.RateLimit.LoginIP15Min)
	assert.Equal(t, 5, cfg.RateLimit.LoginUser15Min)
}

func TestLoad_MissingDatabaseURL(t *testing.T) {
	// Ensure DATABASE_URL is not set
	os.Unsetenv("DATABASE_URL")
	t.Setenv("SERVER_PORT", "8080") // valid non-DB config

	_, err := Load()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "DATABASE_URL")
	// Verify the error message does NOT contain any secret-like value
	assert.NotContains(t, err.Error(), "postgres://")
	assert.NotContains(t, err.Error(), "password")
}

func TestLoad_InvalidPort(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db?sslmode=disable")
	t.Setenv("SERVER_PORT", "99999")

	_, err := Load()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "SERVER_PORT")
}

func TestLoad_InvalidDatabaseURLFormat(t *testing.T) {
	t.Setenv("DATABASE_URL", "not-a-valid-url")

	_, err := Load()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "DATABASE_URL")
	assert.Contains(t, err.Error(), "PostgreSQL connection URL")
	// Secret safety: error should never contain the value itself
	assert.NotContains(t, err.Error(), "not-a-valid-url")
}

func TestLoad_MaxConnsOutOfRange(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db?sslmode=disable")
	t.Setenv("DATABASE_MAX_CONNS", "200")

	_, err := Load()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "DATABASE_MAX_CONNS")
}

func TestLoad_MinConnsGreaterThanMax(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db?sslmode=disable")
	t.Setenv("DATABASE_MAX_CONNS", "10")
	t.Setenv("DATABASE_MIN_CONNS", "20")

	_, err := Load()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "DATABASE_MIN_CONNS")
}

func TestLoad_InvalidLogLevel(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db?sslmode=disable")
	t.Setenv("LOG_LEVEL", "verbose")

	_, err := Load()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "LOG_LEVEL")
}

func TestLoad_InvalidLogFormat(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db?sslmode=disable")
	t.Setenv("LOG_FORMAT", "xml")

	_, err := Load()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "LOG_FORMAT")
}

func TestLoad_CustomServerConfig(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db?sslmode=disable")
	t.Setenv("SERVER_HOST", "127.0.0.1")
	t.Setenv("SERVER_PORT", "9090")

	cfg, err := Load()

	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1", cfg.Server.Host)
	assert.Equal(t, 9090, cfg.Server.Port)
}

func TestLoad_Addr(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db?sslmode=disable")

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, "0.0.0.0:8080", cfg.Addr())
}

func TestLoad_BlankDatabaseURL(t *testing.T) {
	os.Unsetenv("DATABASE_URL")
	t.Setenv("DATABASE_URL", "   ")

	_, err := Load()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "DATABASE_URL")
	// Should not contain the whitespace-only value
	assert.NotContains(t, strings.ToLower(err.Error()), "postgres")
}

func TestLoad_RateLimitDisabled(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db?sslmode=disable")
	t.Setenv("RATE_LIMIT_ENABLED", "false")

	cfg, err := Load()
	require.NoError(t, err)
	assert.False(t, cfg.RateLimit.Enabled)
}

func TestLoad_RateLimitCustomValues(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db?sslmode=disable")
	t.Setenv("RATE_LIMIT_REGISTER_IP_HOUR", "20")
	t.Setenv("RATE_LIMIT_LOGIN_IP_15MIN", "30")
	t.Setenv("RATE_LIMIT_LOGIN_USER_15MIN", "3")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, 20, cfg.RateLimit.RegisterIPHour)
	assert.Equal(t, 30, cfg.RateLimit.LoginIP15Min)
	assert.Equal(t, 3, cfg.RateLimit.LoginUser15Min)
}

func TestLoad_RateLimitInvalidValue(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db?sslmode=disable")
	t.Setenv("RATE_LIMIT_REGISTER_IP_HOUR", "0")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "RATE_LIMIT_REGISTER_IP_HOUR")
}

func TestConfig_Validate_PortBoundaries(t *testing.T) {
	// Valid low boundary
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db?sslmode=disable")
	t.Setenv("SERVER_PORT", "1")
	_, err := Load()
	assert.NoError(t, err)

	// Valid high boundary
	t.Setenv("SERVER_PORT", "65535")
	_, err = Load()
	assert.NoError(t, err)
}
