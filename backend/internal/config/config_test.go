package config

import (
	"os"
	"strings"
	"testing"
	"time"

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

// --- US5 module config (T069) -------------------------------------------------

func TestLoad_ModuleDatabaseURLsFallBackToBase(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db?sslmode=disable")

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, cfg.Database.URL, cfg.Database.IAMURL)
	assert.Equal(t, cfg.Database.URL, cfg.Database.OrganizationURL)
	assert.Equal(t, cfg.Database.URL, cfg.Database.AdminBFFURL)
}

func TestLoad_ModuleDatabaseURLsExplicitSamePhysicalDB(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db?sslmode=disable")
	t.Setenv("IAM_DATABASE_URL", "postgres://iam:iam@localhost:5432/db")
	t.Setenv("ORGANIZATION_DATABASE_URL", "postgres://org:org@localhost:5432/db?sslmode=disable")
	t.Setenv("ADMIN_BFF_DATABASE_URL", "postgres://bff:bff@localhost:5432/db")

	cfg, err := Load()
	require.NoError(t, err)

	// Different credentials and query params, same physical database: valid.
	assert.Equal(t, "postgres://iam:iam@localhost:5432/db", cfg.Database.IAMURL)
	assert.Equal(t, "postgres://org:org@localhost:5432/db?sslmode=disable", cfg.Database.OrganizationURL)
	assert.Equal(t, "postgres://bff:bff@localhost:5432/db", cfg.Database.AdminBFFURL)
}

func TestLoad_ModuleDatabaseURLDefaultPortNormalized(t *testing.T) {
	// Base URL spells the default port explicitly; the module URL omits it —
	// both resolve to the same physical database.
	t.Setenv("DATABASE_URL", "postgres://user:pass@db.internal:5432/appdb")
	t.Setenv("IAM_DATABASE_URL", "postgres://user:pass@db.internal/appdb")

	cfg, err := Load()
	require.NoError(t, err)
	baseKey, err := physicalDatabaseKey(cfg.Database.URL)
	require.NoError(t, err)
	iamKey, err := physicalDatabaseKey(cfg.Database.IAMURL)
	require.NoError(t, err)
	assert.Equal(t, baseKey, iamKey, "omitted default port must normalize to the same physical database")

	// And a genuinely different physical database is rejected.
	t.Setenv("IAM_DATABASE_URL", "postgres://user:pass@db.internal:5432/otherdb")
	_, err = Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "IAM_DATABASE_URL")
	assert.Contains(t, err.Error(), "same physical database")
	assert.NotContains(t, err.Error(), "postgres://", "error must not leak URL values")
	assert.NotContains(t, err.Error(), "otherdb")
}

func TestLoad_ModuleDatabaseDifferentHostRejected(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db")
	t.Setenv("ORGANIZATION_DATABASE_URL", "postgres://user:pass@other-host:5432/db")

	_, err := Load()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "ORGANIZATION_DATABASE_URL")
	assert.NotContains(t, err.Error(), "other-host")
}

func TestLoad_ModuleDatabaseInvalidURLRejected(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db")
	t.Setenv("ADMIN_BFF_DATABASE_URL", "not-a-postgres-url")

	_, err := Load()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "ADMIN_BFF_DATABASE_URL")
	assert.Contains(t, err.Error(), "PostgreSQL connection URL")
	assert.NotContains(t, err.Error(), "not-a-postgres-url")
}

func TestLoad_AllUS5FeatureTogglesDefaultOff(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db")

	cfg, err := Load()
	require.NoError(t, err)

	assert.False(t, cfg.AdminBFF.RoutesEnabled)
	assert.False(t, cfg.AdminBFF.ShadowReadsEnabled)
	assert.False(t, cfg.AdminBFF.AuthProfileReadsEnabled)
	assert.False(t, cfg.AdminBFF.UserListReadsEnabled)
	assert.False(t, cfg.AdminBFF.DepartmentWritesEnabled)
	assert.False(t, cfg.AdminBFF.RoleWritesEnabled)
	assert.False(t, cfg.AdminBFF.ManagedUserWritesEnabled)
	assert.False(t, cfg.AdminBFF.UserDeleteRouteEnabled)
	assert.False(t, cfg.Features.LegacyDeleteIAMDelegationEnabled)
	assert.False(t, cfg.Features.IAMDeleteEventConsumerEnabled)
	assert.False(t, cfg.Features.OutboxDispatcherEnabled)
}

func TestLoad_AdminBFFGranularFlagsParse(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db")
	t.Setenv("ADMIN_BFF_ROUTES_ENABLED", "true")
	t.Setenv("ADMIN_BFF_SHADOW_READS", "true")
	t.Setenv("ADMIN_BFF_AUTH_PROFILE_READS_ENABLED", "true")
	t.Setenv("ADMIN_BFF_USER_LIST_READS_ENABLED", "true")
	t.Setenv("ADMIN_BFF_DEPARTMENT_WRITES_ENABLED", "true")
	t.Setenv("ADMIN_BFF_ROLE_WRITES_ENABLED", "true")
	t.Setenv("ADMIN_BFF_MANAGED_USER_WRITES_ENABLED", "true")
	t.Setenv("ADMIN_BFF_USER_DELETE_ROUTE_ENABLED", "true")

	cfg, err := Load()
	require.NoError(t, err)

	assert.True(t, cfg.AdminBFF.ShadowReadsEnabled)
	assert.True(t, cfg.AdminBFF.AuthProfileReadsEnabled)
	assert.True(t, cfg.AdminBFF.UserListReadsEnabled)
	assert.True(t, cfg.AdminBFF.DepartmentWritesEnabled)
	assert.True(t, cfg.AdminBFF.RoleWritesEnabled)
	assert.True(t, cfg.AdminBFF.ManagedUserWritesEnabled)
	assert.True(t, cfg.AdminBFF.UserDeleteRouteEnabled)
}

func TestLoad_FeaturesFlagsParse(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db")
	t.Setenv("LEGACY_DELETE_IAM_DELEGATION_ENABLED", "true")
	t.Setenv("IAM_DELETE_EVENT_CONSUMER_ENABLED", "true")
	t.Setenv("OUTBOX_DISPATCHER_ENABLED", "true")

	cfg, err := Load()
	require.NoError(t, err)

	assert.True(t, cfg.Features.LegacyDeleteIAMDelegationEnabled)
	assert.True(t, cfg.Features.IAMDeleteEventConsumerEnabled)
	assert.True(t, cfg.Features.OutboxDispatcherEnabled)
}

func TestAdminBFF_MasterKillSwitchForcesGranularOff(t *testing.T) {
	// Master on + granular on → enabled.
	on := AdminBFFConfig{RoutesEnabled: true, UserListReadsEnabled: true, DepartmentWritesEnabled: true}
	assert.True(t, on.CapabilityEnabled(on.UserListReadsEnabled))
	assert.True(t, on.CapabilityEnabled(on.DepartmentWritesEnabled))

	// Master on + granular off → off.
	assert.False(t, on.CapabilityEnabled(on.ShadowReadsEnabled))

	// Master off forces EVERY granular capability off regardless of its flag.
	off := AdminBFFConfig{RoutesEnabled: false, UserListReadsEnabled: true, DepartmentWritesEnabled: true, UserDeleteRouteEnabled: true}
	assert.False(t, off.CapabilityEnabled(off.UserListReadsEnabled))
	assert.False(t, off.CapabilityEnabled(off.DepartmentWritesEnabled))
	assert.False(t, off.CapabilityEnabled(off.UserDeleteRouteEnabled))
}

func TestLoad_UserDeleteRouteIndependentOfManagedUserWrites(t *testing.T) {
	// Delete is an independent flag: create/update on, delete off.
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db")
	t.Setenv("ADMIN_BFF_ROUTES_ENABLED", "true")
	t.Setenv("ADMIN_BFF_MANAGED_USER_WRITES_ENABLED", "true")

	cfg, err := Load()
	require.NoError(t, err)
	assert.True(t, cfg.AdminBFF.ManagedUserWritesEnabled)
	assert.False(t, cfg.AdminBFF.UserDeleteRouteEnabled, "delete must stay independent of create/update")
	assert.False(t, cfg.AdminBFF.CapabilityEnabled(cfg.AdminBFF.UserDeleteRouteEnabled))

	// Delete on, create/update off.
	t.Setenv("ADMIN_BFF_MANAGED_USER_WRITES_ENABLED", "false")
	t.Setenv("ADMIN_BFF_USER_DELETE_ROUTE_ENABLED", "true")
	cfg, err = Load()
	require.NoError(t, err)
	assert.False(t, cfg.AdminBFF.ManagedUserWritesEnabled)
	assert.True(t, cfg.AdminBFF.CapabilityEnabled(cfg.AdminBFF.UserDeleteRouteEnabled))
}

func TestLoad_RolloutGateDefaults(t *testing.T) {
	// The rollout gate is disabled by default — nothing records evidence
	// unless explicitly turned on.
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db")

	cfg, err := Load()
	require.NoError(t, err)
	assert.False(t, cfg.RolloutGate.Enabled)
	assert.Equal(t, 60*time.Second, cfg.RolloutGate.SampleCadence)
	assert.Equal(t, 5*time.Minute, cfg.RolloutGate.MaxGapInterval)
	assert.Equal(t, "bff-rollout", cfg.RolloutGate.Phase)
	assert.Equal(t, "app_runtime", cfg.RolloutGate.PrincipalID)
	assert.Equal(t, "", cfg.RolloutGate.RollbackArtifactID)
	assert.Equal(t, "", cfg.RolloutGate.RollbackSuiteResult)
}

func TestLoad_RolloutGateOverride(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db")
	t.Setenv("ROLLOUT_GATE_ENABLED", "true")
	t.Setenv("ROLLOUT_GATE_SAMPLE_CADENCE", "90s")
	t.Setenv("ROLLOUT_GATE_MAX_GAP_INTERVAL", "7m")
	t.Setenv("ROLLOUT_GATE_PHASE", "wire-test")
	t.Setenv("ROLLOUT_GATE_PRINCIPAL_ID", "ops-writer")
	t.Setenv("ROLLOUT_GATE_ROLLBACK_ARTIFACT_ID", "artifact-004")
	t.Setenv("ROLLOUT_GATE_ROLLBACK_SUITE_RESULT", "passed")

	cfg, err := Load()
	require.NoError(t, err)
	assert.True(t, cfg.RolloutGate.Enabled)
	assert.Equal(t, 90*time.Second, cfg.RolloutGate.SampleCadence)
	assert.Equal(t, 7*time.Minute, cfg.RolloutGate.MaxGapInterval)
	assert.Equal(t, "wire-test", cfg.RolloutGate.Phase)
	assert.Equal(t, "ops-writer", cfg.RolloutGate.PrincipalID)
	assert.Equal(t, "artifact-004", cfg.RolloutGate.RollbackArtifactID)
	assert.Equal(t, "passed", cfg.RolloutGate.RollbackSuiteResult)
}

func TestLoad_InvalidRolloutGateCadence(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db")
	t.Setenv("ROLLOUT_GATE_SAMPLE_CADENCE", "not-a-duration")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ROLLOUT_GATE_SAMPLE_CADENCE")
}

func TestLoad_InvalidRolloutGateMaxGap(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db")
	t.Setenv("ROLLOUT_GATE_MAX_GAP_INTERVAL", "soon")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ROLLOUT_GATE_MAX_GAP_INTERVAL")
}
