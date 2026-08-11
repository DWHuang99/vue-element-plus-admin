package logging

import (
	"log/slog"
	"os"
	"strings"
)

// Config holds logging configuration.
type Config struct {
	Level  string
	Format string
}

// New creates a new slog.Logger based on the provided configuration.
func New(cfg Config) *slog.Logger {
	var level slog.Level
	switch strings.ToLower(cfg.Level) {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{
		Level: level,
	}

	var handler slog.Handler
	switch strings.ToLower(cfg.Format) {
	case "json":
		handler = slog.NewJSONHandler(os.Stdout, opts)
	default:
		handler = slog.NewTextHandler(os.Stdout, opts)
	}

	return slog.New(handler)
}

// WithModule scopes a logger to one module and service (T072, plan Phase
// 7.5): every log line a service emits carries its ownership context, so a
// BFF aggregate request can be traced across iam/organization/integration
// log streams. The base logger is never mutated — the scoped child shares
// the same handler.
func WithModule(logger *slog.Logger, module, service string) *slog.Logger {
	return logger.With("module", module, "service", service)
}
