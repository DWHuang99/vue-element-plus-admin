package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hdw/vue-element-plus-admin/backend/internal/auth"
	"github.com/hdw/vue-element-plus-admin/backend/internal/config"
	"github.com/hdw/vue-element-plus-admin/backend/internal/database"
	"github.com/hdw/vue-element-plus-admin/backend/internal/health"
	"github.com/hdw/vue-element-plus-admin/backend/internal/logging"
	"github.com/hdw/vue-element-plus-admin/backend/internal/middleware"
	"github.com/hdw/vue-element-plus-admin/backend/internal/ratelimit"
	"github.com/hdw/vue-element-plus-admin/backend/internal/server"
)

func main() {
	// 1. Load configuration
	cfg, err := config.Load()
	if err != nil {
		// Use a basic logger before the configured one is available
		basicLogger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
		basicLogger.Error("server failed to start", "error", err.Error())
		os.Exit(1)
	}

	// 2. Initialize logger
	logger := logging.New(logging.Config{
		Level:  cfg.Log.Level,
		Format: cfg.Log.Format,
	})

	logger.Info("configuration loaded")

	// 3. Connect to database
	ctx := context.Background()
	db, err := database.Connect(ctx, database.Config{
		URL:             cfg.Database.URL,
		MaxConns:        cfg.Database.MaxConns,
		MinConns:        cfg.Database.MinConns,
		MaxConnLifetime: cfg.Database.MaxConnLifetime,
		MaxConnIdleTime: cfg.Database.MaxConnIdleTime,
	})
	if err != nil {
		logger.Error("server failed to start", "error", err.Error())
		os.Exit(1)
	}
	defer db.Close()

	logger.Info("database connection established",
		"max_conns", cfg.Database.MaxConns,
		"min_conns", cfg.Database.MinConns,
	)

	// 4. Run database migrations
	if err := database.RunMigrations(cfg.Database.URL); err != nil {
		logger.Error("server failed to start", "error", err.Error())
		os.Exit(1)
	}

	logger.Info("database migrations completed")

	// 5. Create HTTP server
	srvCfg := server.Config{
		Host:         cfg.Server.Host,
		Port:         cfg.Server.Port,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  cfg.Server.IdleTimeout,
	}

	srv := server.New(srvCfg, logger)

	// 6. Register middleware and routes
	router := srv.Router()
	router.Use(middleware.RequestID())
	router.Use(middleware.CORS(cfg.CORS.AllowedOrigins))

	healthHandler := health.NewHandler(db, logger)
	router.GET("/health/live", healthHandler.Live)
	router.GET("/health/ready", healthHandler.Ready)

	// 6b. Auth routes under /api/v1/auth
	authSvc := auth.NewAuthService(db.Pool)
	authHandler := auth.NewHandler(authSvc, logger)

	// Rate limiters (config-controlled; disabled when RATE_LIMIT_ENABLED=false).
	registerLimiter := ratelimit.New(ratelimit.Config{
		Enabled:  cfg.RateLimit.Enabled,
		Limit:    cfg.RateLimit.RegisterIPHour,
		Duration: time.Hour,
	})
	defer registerLimiter.Close()

	loginIPLimiter := ratelimit.New(ratelimit.Config{
		Enabled:  cfg.RateLimit.Enabled,
		Limit:    cfg.RateLimit.LoginIP15Min,
		Duration: 15 * time.Minute,
	})
	defer loginIPLimiter.Close()

	loginUserLimiter := ratelimit.New(ratelimit.Config{
		Enabled:  cfg.RateLimit.Enabled,
		Limit:    cfg.RateLimit.LoginUser15Min,
		Duration: 15 * time.Minute,
	})
	defer loginUserLimiter.Close()

	authGroup := router.Group("/api/v1/auth")
	{
		authGroup.POST("/register", ratelimit.GinIPRateLimit(registerLimiter), authHandler.Register)
		authGroup.POST("/login",
			ratelimit.GinIPRateLimit(loginIPLimiter),
			ratelimit.GinUsernameRateLimit(loginUserLimiter),
			authHandler.Login)
		authGroup.POST("/logout", middleware.BearerToken(), authHandler.Logout)
		authGroup.GET("/me", middleware.Auth(authSvc), authHandler.Me)
	}

	// 7. Start server (blocking call in goroutine)
	go func() {
		logger.Info("server ready", "addr", srv.Addr())
		if err := srv.Start(); err != nil && err.Error() != "http: Server closed" {
			logger.Error("server failed unexpectedly", "error", err.Error())
			os.Exit(1)
		}
	}()

	// 8. Wait for shutdown signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit

	logger.Info("shutting down", "signal", sig.String())

	// 9. Graceful shutdown with timeout
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("server forced to shutdown", "error", err.Error())
	} else {
		logger.Info("server stopped")
	}
}
