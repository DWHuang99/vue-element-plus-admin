package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// LegacyRateLimitConfig mirrors config.RateLimitConfig for router construction.
// It is shared (not gated behind the rollback build tag) because the
// composition root's NewWire signature carries it for both wirings: the Admin
// BFF router maps its fields into adminbfhttp.RateLimitConfig, and the legacy
// monolith router (rollback build only) consumes it directly. The name keeps
// the pre-split contract so callers and tests read identically in every build.
type LegacyRateLimitConfig struct {
	Enabled        bool
	RegisterIPHour int
	LoginIP15Min   int
	LoginUser15Min int
}

// Config holds HTTP server configuration.
type Config struct {
	Host         string
	Port         int
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	IdleTimeout  time.Duration
}

// Server wraps the HTTP server and its dependencies.
type Server struct {
	httpServer *http.Server
	router     *gin.Engine
	logger     *slog.Logger
}

// New creates a new Server with the given configuration and dependencies.
func New(cfg Config, logger *slog.Logger) *Server {
	return newWithRouter(cfg, logger, nil)
}

// NewWithRouter creates a Server serving the given router (e.g. the legacy
// monolith router from NewLegacyRouter, or the Admin BFF router after cutover).
// The server layer still owns Recovery, NoRoute and NoMethod wiring so the
// external router keeps identical error-handler behavior.
func NewWithRouter(cfg Config, logger *slog.Logger, router *gin.Engine) *Server {
	return newWithRouter(cfg, logger, router)
}

func newWithRouter(cfg Config, logger *slog.Logger, router *gin.Engine) *Server {
	gin.SetMode(gin.ReleaseMode)

	if router == nil {
		router = gin.New()
	}

	// Recovery middleware logs panics but doesn't expose stack traces to clients
	router.Use(gin.Recovery())

	srv := &Server{
		router: router,
		logger: logger,
		httpServer: &http.Server{
			Addr:         fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
			Handler:      router,
			ReadTimeout:  cfg.ReadTimeout,
			WriteTimeout: cfg.WriteTimeout,
			IdleTimeout:  cfg.IdleTimeout,
		},
	}

	// Register error handlers
	router.NoRoute(notFoundHandler)
	router.NoMethod(methodNotAllowedHandler)

	return srv
}

// Router returns the Gin engine for registering routes.
func (s *Server) Router() *gin.Engine {
	return s.router
}

// Start begins listening for HTTP requests in a goroutine.
func (s *Server) Start() error {
	s.logger.Info("server starting", "addr", s.httpServer.Addr)
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully stops the server, waiting for active requests to finish
// or the context deadline to expire.
func (s *Server) Shutdown(ctx context.Context) error {
	s.logger.Info("server shutting down")
	return s.httpServer.Shutdown(ctx)
}

// Addr returns the server's listen address.
func (s *Server) Addr() string {
	return s.httpServer.Addr
}

// notFoundHandler returns a consistent JSON 404 response.
func notFoundHandler(c *gin.Context) {
	c.JSON(http.StatusNotFound, gin.H{
		"error": gin.H{
			"code":    "NOT_FOUND",
			"message": "The requested path was not found",
		},
	})
}

// methodNotAllowedHandler returns a consistent JSON 405 response.
func methodNotAllowedHandler(c *gin.Context) {
	c.JSON(http.StatusMethodNotAllowed, gin.H{
		"error": gin.H{
			"code":    "METHOD_NOT_ALLOWED",
			"message": fmt.Sprintf("The %s method is not allowed for this path", c.Request.Method),
		},
	})
}

// ListenAddr returns the address string without the host prefix.
func (cfg Config) Addr() string {
	return fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
}
