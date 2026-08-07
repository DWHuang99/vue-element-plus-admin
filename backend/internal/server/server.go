package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

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
	gin.SetMode(gin.ReleaseMode)

	router := gin.New()

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
	router.NoRoute(srv.notFoundHandler)
	router.NoMethod(srv.methodNotAllowedHandler)

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
func (s *Server) notFoundHandler(c *gin.Context) {
	c.JSON(http.StatusNotFound, gin.H{
		"error": gin.H{
			"code":    "NOT_FOUND",
			"message": "The requested path was not found",
		},
	})
}

// methodNotAllowedHandler returns a consistent JSON 405 response.
func (s *Server) methodNotAllowedHandler(c *gin.Context) {
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
