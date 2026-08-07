package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config holds all configuration for the application.
type Config struct {
	Server     ServerConfig
	Database   DatabaseConfig
	Log        LogConfig
	RateLimit  RateLimitConfig
	CORS       CORSConfig
}

// CORSConfig holds cross-origin request configuration.
type CORSConfig struct {
	// AllowedOrigins is a comma-separated origin list; empty means allow all (dev).
	AllowedOrigins []string
}

// RateLimitConfig holds rate limiting configuration.
type RateLimitConfig struct {
	Enabled              bool
	RegisterIPHour       int
	LoginIP15Min         int
	LoginUser15Min       int
}

// ServerConfig holds HTTP server configuration.
type ServerConfig struct {
	Host         string
	Port         int
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	IdleTimeout  time.Duration
}

// DatabaseConfig holds PostgreSQL connection configuration.
type DatabaseConfig struct {
	URL             string
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

	cfg := &Config{
		Server: ServerConfig{
			Host:         v.GetString("SERVER_HOST"),
			Port:         v.GetInt("SERVER_PORT"),
			ReadTimeout:  readTimeout,
			WriteTimeout: writeTimeout,
			IdleTimeout:  idleTimeout,
		},
		Database: DatabaseConfig{
			URL:             v.GetString("DATABASE_URL"),
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
