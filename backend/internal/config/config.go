package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config holds all configuration for the application.
type Config struct {
	Server      ServerConfig
	Database    DatabaseConfig
	Log         LogConfig
	RateLimit   RateLimitConfig
	CORS        CORSConfig
	AdminBFF    AdminBFFConfig
	Features    FeaturesConfig
	RolloutGate RolloutGateConfig
}

// RolloutGateConfig controls the in-process rollout-gate writer (T077): one
// evidence sample per cadence, appended through the Platform SECURITY
// DEFINER function (migration 000012) which owns the 72h window math. The
// rollback-artifact identity and extended-suite result are pinned before
// deploy (plan Phase 5.9 step 1/6, quickstart §9 step 10).
type RolloutGateConfig struct {
	// Enabled mirrors ROLLOUT_GATE_ENABLED; the writer loop runs only when
	// set.
	Enabled bool
	// SampleCadence mirrors ROLLOUT_GATE_SAMPLE_CADENCE (default 60s).
	SampleCadence time.Duration
	// MaxGapInterval mirrors ROLLOUT_GATE_MAX_GAP_INTERVAL (default 5m):
	// a sample arriving later than this after the previous one closes the
	// window as a monitoring gap.
	MaxGapInterval time.Duration
	// Phase labels the rollout stage the samples belong to (default
	// "bff-rollout"); the window math is per-phase.
	Phase string
	// PrincipalID identifies the trusted writer identity recorded on every
	// sample (default "app_runtime").
	PrincipalID string
	// RollbackArtifactID pins the 004-compatible rollback artifact
	// (image/Git SHA/config manifest hash).
	RollbackArtifactID string
	// RollbackSuiteResult records the extended compatibility suite result
	// for that artifact.
	RollbackSuiteResult string
}

// AdminBFFConfig controls the Admin BFF route switch (plan Phase 5.9 master
// kill switch). The master switch forces all granular BFF capabilities off
// when disabled; granular capability flags (US5) are each ANDed with it via
// CapabilityEnabled.
type AdminBFFConfig struct {
	// RoutesEnabled mirrors ADMIN_BFF_ROUTES_ENABLED; false keeps the legacy
	// monolith router active and disables every granular capability.
	RoutesEnabled bool

	// Granular capability flags (US5 rollout order). None of them has any
	// effect while the master switch is off.
	ShadowReadsEnabled       bool // ADMIN_BFF_SHADOW_READS
	AuthProfileReadsEnabled  bool // ADMIN_BFF_AUTH_PROFILE_READS_ENABLED
	UserListReadsEnabled     bool // ADMIN_BFF_USER_LIST_READS_ENABLED
	DepartmentWritesEnabled  bool // ADMIN_BFF_DEPARTMENT_WRITES_ENABLED
	RoleWritesEnabled        bool // ADMIN_BFF_ROLE_WRITES_ENABLED
	ManagedUserWritesEnabled bool // ADMIN_BFF_MANAGED_USER_WRITES_ENABLED (create/update only)
	// UserDeleteRouteEnabled mirrors ADMIN_BFF_USER_DELETE_ROUTE_ENABLED and
	// stays independent of create/update writes (delete must be the last BFF
	// capability to roll out).
	UserDeleteRouteEnabled bool
}

// CapabilityEnabled reports whether a granular BFF capability is active:
// the master switch forces every granular capability off when disabled.
func (c AdminBFFConfig) CapabilityEnabled(granular bool) bool {
	return c.RoutesEnabled && granular
}

// FeaturesConfig holds module-level feature toggles for the microservice
// split rollout (US5). All default to false — nothing turns on unless it is
// explicitly enabled.
type FeaturesConfig struct {
	// LegacyDeleteIAMDelegationEnabled mirrors
	// LEGACY_DELETE_IAM_DELEGATION_ENABLED: the legacy /users/delete handler
	// delegates to IAM DeleteUsers + outbox instead of direct deletion.
	LegacyDeleteIAMDelegationEnabled bool
	// IAMDeleteEventConsumerEnabled mirrors IAM_DELETE_EVENT_CONSUMER_ENABLED:
	// the Organization inbox consumer accepts iam.user.deleted events.
	IAMDeleteEventConsumerEnabled bool
	// OutboxDispatcherEnabled mirrors OUTBOX_DISPATCHER_ENABLED: the
	// in-process outbox dispatcher loop runs.
	OutboxDispatcherEnabled bool
}

// CORSConfig holds cross-origin request configuration.
type CORSConfig struct {
	// AllowedOrigins is a comma-separated origin list; empty means allow all (dev).
	AllowedOrigins []string
}

// RateLimitConfig holds rate limiting configuration.
type RateLimitConfig struct {
	Enabled        bool
	RegisterIPHour int
	LoginIP15Min   int
	LoginUser15Min int
}

// ServerConfig holds HTTP server configuration.
type ServerConfig struct {
	Host         string
	Port         int
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	IdleTimeout  time.Duration
}

// DatabaseConfig holds PostgreSQL connection configuration. IAMURL,
// OrganizationURL and AdminBFFURL mirror the module-scoped connection
// strings and fall back to URL (DATABASE_URL) — during the microservice
// split every module runs against the same physical database, and Validate
// enforces that (a mixed setup would split bridge/outbox state with no
// cross-database transaction to repair it).
type DatabaseConfig struct {
	URL             string
	IAMURL          string
	OrganizationURL string
	AdminBFFURL     string
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
}

// LogConfig holds logging configuration.
type LogConfig struct {
	Level  string
	Format string
}

// Load reads configuration from environment variables using viper.
// Environment variables take precedence over .env file values.
func Load() (*Config, error) {
	v := viper.New()

	// Viper automatically reads from .env file if present
	v.SetConfigFile(".env")
	v.SetConfigType("env")
	_ = v.ReadInConfig() // Ignore error: .env is optional

	v.AutomaticEnv()

	// Set defaults
	v.SetDefault("SERVER_HOST", "0.0.0.0")
	v.SetDefault("SERVER_PORT", 8080)
	v.SetDefault("SERVER_READ_TIMEOUT", "30s")
	v.SetDefault("SERVER_WRITE_TIMEOUT", "30s")
	v.SetDefault("SERVER_IDLE_TIMEOUT", "60s")
	v.SetDefault("DATABASE_MAX_CONNS", 25)
	v.SetDefault("DATABASE_MIN_CONNS", 5)
	v.SetDefault("DATABASE_MAX_CONN_LIFETIME", "1h")
	v.SetDefault("DATABASE_MAX_CONN_IDLE_TIME", "30m")
	v.SetDefault("LOG_LEVEL", "info")
	v.SetDefault("LOG_FORMAT", "text")
	v.SetDefault("CORS_ALLOWED_ORIGINS", "") // empty = allow all (dev)
	v.SetDefault("RATE_LIMIT_ENABLED", true)
	v.SetDefault("RATE_LIMIT_REGISTER_IP_HOUR", 10)
	v.SetDefault("RATE_LIMIT_LOGIN_IP_15MIN", 10)
	v.SetDefault("RATE_LIMIT_LOGIN_USER_15MIN", 5)
	v.SetDefault("ADMIN_BFF_ROUTES_ENABLED", false)
	// US5 module feature toggles — everything defaults to off; nothing turns
	// on unless explicitly enabled (safety default for the split rollout).
	v.SetDefault("ADMIN_BFF_SHADOW_READS", false)
	v.SetDefault("ADMIN_BFF_AUTH_PROFILE_READS_ENABLED", false)
	v.SetDefault("ADMIN_BFF_USER_LIST_READS_ENABLED", false)
	v.SetDefault("ADMIN_BFF_DEPARTMENT_WRITES_ENABLED", false)
	v.SetDefault("ADMIN_BFF_ROLE_WRITES_ENABLED", false)
	v.SetDefault("ADMIN_BFF_MANAGED_USER_WRITES_ENABLED", false)
	v.SetDefault("ADMIN_BFF_USER_DELETE_ROUTE_ENABLED", false)
	v.SetDefault("LEGACY_DELETE_IAM_DELEGATION_ENABLED", false)
	v.SetDefault("IAM_DELETE_EVENT_CONSUMER_ENABLED", false)
	v.SetDefault("OUTBOX_DISPATCHER_ENABLED", false)
	// Rollout gate (T077) — disabled by default; cadence/gap parsed below.
	v.SetDefault("ROLLOUT_GATE_ENABLED", false)
	v.SetDefault("ROLLOUT_GATE_SAMPLE_CADENCE", "60s")
	v.SetDefault("ROLLOUT_GATE_MAX_GAP_INTERVAL", "5m")
	v.SetDefault("ROLLOUT_GATE_PHASE", "bff-rollout")
	v.SetDefault("ROLLOUT_GATE_PRINCIPAL_ID", "app_runtime")
	v.SetDefault("ROLLOUT_GATE_ROLLBACK_ARTIFACT_ID", "")
	v.SetDefault("ROLLOUT_GATE_ROLLBACK_SUITE_RESULT", "")

	// Parse durations
	readTimeout, err := time.ParseDuration(v.GetString("SERVER_READ_TIMEOUT"))
	if err != nil {
		return nil, fmt.Errorf("configuration invalid: SERVER_READ_TIMEOUT must be a valid duration")
	}
	writeTimeout, err := time.ParseDuration(v.GetString("SERVER_WRITE_TIMEOUT"))
	if err != nil {
		return nil, fmt.Errorf("configuration invalid: SERVER_WRITE_TIMEOUT must be a valid duration")
	}
	idleTimeout, err := time.ParseDuration(v.GetString("SERVER_IDLE_TIMEOUT"))
	if err != nil {
		return nil, fmt.Errorf("configuration invalid: SERVER_IDLE_TIMEOUT must be a valid duration")
	}
	maxConnLifetime, err := time.ParseDuration(v.GetString("DATABASE_MAX_CONN_LIFETIME"))
	if err != nil {
		return nil, fmt.Errorf("configuration invalid: DATABASE_MAX_CONN_LIFETIME must be a valid duration")
	}
	maxConnIdleTime, err := time.ParseDuration(v.GetString("DATABASE_MAX_CONN_IDLE_TIME"))
	if err != nil {
		return nil, fmt.Errorf("configuration invalid: DATABASE_MAX_CONN_IDLE_TIME must be a valid duration")
	}
	rolloutCadence, err := time.ParseDuration(v.GetString("ROLLOUT_GATE_SAMPLE_CADENCE"))
	if err != nil {
		return nil, fmt.Errorf("configuration invalid: ROLLOUT_GATE_SAMPLE_CADENCE must be a valid duration")
	}
	rolloutMaxGap, err := time.ParseDuration(v.GetString("ROLLOUT_GATE_MAX_GAP_INTERVAL"))
	if err != nil {
		return nil, fmt.Errorf("configuration invalid: ROLLOUT_GATE_MAX_GAP_INTERVAL must be a valid duration")
	}

	// Module-scoped database URLs fall back to the base DATABASE_URL: during
	// the split every module runs against the same physical database.
	baseURL := v.GetString("DATABASE_URL")
	iamURL := v.GetString("IAM_DATABASE_URL")
	if iamURL == "" {
		iamURL = baseURL
	}
	orgURL := v.GetString("ORGANIZATION_DATABASE_URL")
	if orgURL == "" {
		orgURL = baseURL
	}
	bffURL := v.GetString("ADMIN_BFF_DATABASE_URL")
	if bffURL == "" {
		bffURL = baseURL
	}

	cfg := &Config{
		Server: ServerConfig{
			Host:         v.GetString("SERVER_HOST"),
			Port:         v.GetInt("SERVER_PORT"),
			ReadTimeout:  readTimeout,
			WriteTimeout: writeTimeout,
			IdleTimeout:  idleTimeout,
		},
		Database: DatabaseConfig{
			URL:             baseURL,
			IAMURL:          iamURL,
			OrganizationURL: orgURL,
			AdminBFFURL:     bffURL,
			MaxConns:        v.GetInt32("DATABASE_MAX_CONNS"),
			MinConns:        v.GetInt32("DATABASE_MIN_CONNS"),
			MaxConnLifetime: maxConnLifetime,
			MaxConnIdleTime: maxConnIdleTime,
		},
		Log: LogConfig{
			Level:  v.GetString("LOG_LEVEL"),
			Format: v.GetString("LOG_FORMAT"),
		},
		RateLimit: RateLimitConfig{
			Enabled:        v.GetBool("RATE_LIMIT_ENABLED"),
			RegisterIPHour: v.GetInt("RATE_LIMIT_REGISTER_IP_HOUR"),
			LoginIP15Min:   v.GetInt("RATE_LIMIT_LOGIN_IP_15MIN"),
			LoginUser15Min: v.GetInt("RATE_LIMIT_LOGIN_USER_15MIN"),
		},
		CORS: CORSConfig{
			AllowedOrigins: splitList(v.GetString("CORS_ALLOWED_ORIGINS")),
		},
		AdminBFF: AdminBFFConfig{
			RoutesEnabled:            v.GetBool("ADMIN_BFF_ROUTES_ENABLED"),
			ShadowReadsEnabled:       v.GetBool("ADMIN_BFF_SHADOW_READS"),
			AuthProfileReadsEnabled:  v.GetBool("ADMIN_BFF_AUTH_PROFILE_READS_ENABLED"),
			UserListReadsEnabled:     v.GetBool("ADMIN_BFF_USER_LIST_READS_ENABLED"),
			DepartmentWritesEnabled:  v.GetBool("ADMIN_BFF_DEPARTMENT_WRITES_ENABLED"),
			RoleWritesEnabled:        v.GetBool("ADMIN_BFF_ROLE_WRITES_ENABLED"),
			ManagedUserWritesEnabled: v.GetBool("ADMIN_BFF_MANAGED_USER_WRITES_ENABLED"),
			UserDeleteRouteEnabled:   v.GetBool("ADMIN_BFF_USER_DELETE_ROUTE_ENABLED"),
		},
		Features: FeaturesConfig{
			LegacyDeleteIAMDelegationEnabled: v.GetBool("LEGACY_DELETE_IAM_DELEGATION_ENABLED"),
			IAMDeleteEventConsumerEnabled:    v.GetBool("IAM_DELETE_EVENT_CONSUMER_ENABLED"),
			OutboxDispatcherEnabled:          v.GetBool("OUTBOX_DISPATCHER_ENABLED"),
		},
		RolloutGate: RolloutGateConfig{
			Enabled:             v.GetBool("ROLLOUT_GATE_ENABLED"),
			SampleCadence:       rolloutCadence,
			MaxGapInterval:      rolloutMaxGap,
			Phase:               v.GetString("ROLLOUT_GATE_PHASE"),
			PrincipalID:         v.GetString("ROLLOUT_GATE_PRINCIPAL_ID"),
			RollbackArtifactID:  v.GetString("ROLLOUT_GATE_ROLLBACK_ARTIFACT_ID"),
			RollbackSuiteResult: v.GetString("ROLLOUT_GATE_ROLLBACK_SUITE_RESULT"),
		},
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// Validate checks that all required configuration values are present and valid.
// Error messages reference configuration key names only — never their values.
func (c *Config) Validate() error {
	// Server validation
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("configuration invalid: SERVER_PORT must be between 1 and 65535")
	}

	// Database validation
	if c.Database.URL == "" || strings.TrimSpace(c.Database.URL) == "" {
		return fmt.Errorf("required configuration DATABASE_URL is missing or empty")
	}
	if !strings.HasPrefix(c.Database.URL, "postgres://") && !strings.HasPrefix(c.Database.URL, "postgresql://") {
		return fmt.Errorf("configuration invalid: DATABASE_URL must be a valid PostgreSQL connection URL")
	}
	for name, raw := range map[string]string{
		"IAM_DATABASE_URL":          c.Database.IAMURL,
		"ORGANIZATION_DATABASE_URL": c.Database.OrganizationURL,
		"ADMIN_BFF_DATABASE_URL":    c.Database.AdminBFFURL,
	} {
		if !strings.HasPrefix(raw, "postgres://") && !strings.HasPrefix(raw, "postgresql://") {
			return fmt.Errorf("configuration invalid: %s must be a valid PostgreSQL connection URL", name)
		}
	}
	// The module URLs (after fallback) must point at the same physical
	// database as DATABASE_URL. A mixed setup would split bridge/outbox
	// state across databases with no cross-database transaction to repair
	// it. The error references key names only — never URL values.
	baseKey, err := physicalDatabaseKey(c.Database.URL)
	if err != nil {
		return fmt.Errorf("configuration invalid: DATABASE_URL must be a valid PostgreSQL connection URL")
	}
	for name, raw := range map[string]string{
		"IAM_DATABASE_URL":          c.Database.IAMURL,
		"ORGANIZATION_DATABASE_URL": c.Database.OrganizationURL,
		"ADMIN_BFF_DATABASE_URL":    c.Database.AdminBFFURL,
	} {
		key, kerr := physicalDatabaseKey(raw)
		if kerr != nil {
			return fmt.Errorf("configuration invalid: %s must be a valid PostgreSQL connection URL", name)
		}
		if key != baseKey {
			return fmt.Errorf("configuration invalid: %s must point at the same physical database as DATABASE_URL", name)
		}
	}
	if c.Database.MaxConns < 1 || c.Database.MaxConns > 100 {
		return fmt.Errorf("configuration invalid: DATABASE_MAX_CONNS must be between 1 and 100")
	}
	if c.Database.MinConns < 0 || c.Database.MinConns > int32(c.Database.MaxConns) {
		return fmt.Errorf("configuration invalid: DATABASE_MIN_CONNS must be between 0 and DATABASE_MAX_CONNS")
	}

	// Log validation
	validLevels := map[string]bool{"debug": true, "info": true, "warn": true, "error": true}
	if !validLevels[c.Log.Level] {
		return fmt.Errorf("configuration invalid: LOG_LEVEL must be one of debug, info, warn, error")
	}
	validFormats := map[string]bool{"text": true, "json": true}
	if !validFormats[c.Log.Format] {
		return fmt.Errorf("configuration invalid: LOG_FORMAT must be text or json")
	}

	// Rate limit validation
	if c.RateLimit.RegisterIPHour < 1 || c.RateLimit.RegisterIPHour > 1000 {
		return fmt.Errorf("configuration invalid: RATE_LIMIT_REGISTER_IP_HOUR must be between 1 and 1000")
	}
	if c.RateLimit.LoginIP15Min < 1 || c.RateLimit.LoginIP15Min > 1000 {
		return fmt.Errorf("configuration invalid: RATE_LIMIT_LOGIN_IP_15MIN must be between 1 and 1000")
	}
	if c.RateLimit.LoginUser15Min < 1 || c.RateLimit.LoginUser15Min > 1000 {
		return fmt.Errorf("configuration invalid: RATE_LIMIT_LOGIN_USER_15MIN must be between 1 and 1000")
	}

	return nil
}

// Addr returns the server listen address.
func (c *Config) Addr() string {
	return fmt.Sprintf("%s:%d", c.Server.Host, c.Server.Port)
}

// physicalDatabaseKey extracts the physical database identity (host:port and
// database name) from a PostgreSQL connection URL. Credentials and query
// parameters are ignored — they do not change which physical database the
// URL targets.
func physicalDatabaseKey(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("missing host")
	}
	port := u.Port()
	if port == "" {
		port = "5432"
	}
	db := strings.TrimPrefix(u.Path, "/")
	if db == "" {
		return "", fmt.Errorf("missing database name")
	}
	return net.JoinHostPort(host, port) + "/" + db, nil
}

// splitList splits a comma-separated list, trimming whitespace and dropping empties.
func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
