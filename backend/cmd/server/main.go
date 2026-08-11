package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hdw/vue-element-plus-admin/backend/internal/app/adminapi"
	"github.com/hdw/vue-element-plus-admin/backend/internal/config"
	"github.com/hdw/vue-element-plus-admin/backend/internal/database"
	"github.com/hdw/vue-element-plus-admin/backend/internal/logging"
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

	// 5. Startup gates (US5, plan Phase 5.9 step 4): reject unsafe flag/mode
	// combinations before any delete consumer/dispatcher/route acceptance.
	// Same-physical-database consistency is enforced inside config.Validate;
	// the bridge-mode check reads the live Platform-owned row.
	if err := runStartupGates(ctx, cfg, db, logger); err != nil {
		logger.Error("server failed to start", "error", err.Error())
		os.Exit(1)
	}

	// 6. Assemble the composition root and activate the route switch
	// (legacy monolith wiring by default; Admin BFF after T032).
	app := adminapi.NewWire(db, logger, adminapi.Config{
		AdminBFFRoutesEnabled: cfg.AdminBFF.RoutesEnabled,
		// OUTBOX_DISPATCHER_ENABLED only reaches this point if the startup
		// gates above accepted it (delegation + bridge mode=false).
		OutboxDispatcherEnabled: cfg.Features.OutboxDispatcherEnabled,
		// Shadow reads AND with the master switch (CapabilityEnabled); the
		// BFF router replays pure reads through the legacy router (T075).
		ShadowReadsEnabled: cfg.AdminBFF.CapabilityEnabled(cfg.AdminBFF.ShadowReadsEnabled),
		// Legacy delete delegation (T076): the legacy /users/delete route
		// delegates to IAM instead of direct-deleting. The startup gates
		// already enforced delegation=true before this point for any
		// delete-path component.
		LegacyDeleteIAMDelegationEnabled: cfg.Features.LegacyDeleteIAMDelegationEnabled,
		// Rollout gate (T077): the capability manifest hash fingerprints the
		// deployed capability set (sorted FLAG=value), proving every gate
		// sample corresponds to the deployed manifest (plan Phase 5.9 step 6).
		RolloutGate: adminapi.RolloutGateRuntime{
			Enabled:                cfg.RolloutGate.Enabled,
			SampleCadence:          cfg.RolloutGate.SampleCadence,
			MaxGapInterval:         cfg.RolloutGate.MaxGapInterval,
			Phase:                  cfg.RolloutGate.Phase,
			PrincipalID:            cfg.RolloutGate.PrincipalID,
			RollbackArtifactID:     cfg.RolloutGate.RollbackArtifactID,
			RollbackSuiteResult:    cfg.RolloutGate.RollbackSuiteResult,
			CapabilityManifestHash: adminapi.CapabilityManifestHash(capabilityManifest(cfg)),
		},
	}, cfg.CORS.AllowedOrigins, server.LegacyRateLimitConfig{
		Enabled:        cfg.RateLimit.Enabled,
		RegisterIPHour: cfg.RateLimit.RegisterIPHour,
		LoginIP15Min:   cfg.RateLimit.LoginIP15Min,
		LoginUser15Min: cfg.RateLimit.LoginUser15Min,
	})
	router, closeRatelimiters := app.Router()
	defer closeRatelimiters()

	// 7. Start background loops (outbox dispatcher) once gates have passed.
	app.StartBackground(ctx)

	// 6. Create HTTP server wrapping the router.
	srvCfg := server.Config{
		Host:         cfg.Server.Host,
		Port:         cfg.Server.Port,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  cfg.Server.IdleTimeout,
	}
	srv := server.NewWithRouter(srvCfg, logger, router)

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

// capabilityManifest builds the deployed capability set for the T077
// rollout-gate manifest hash: the BFF master switch, every granular BFF
// capability and the module feature toggles (quickstart §9 staged order).
// The hash is computed over sorted FLAG=value lines by
// adminapi.CapabilityManifestHash.
func capabilityManifest(cfg *config.Config) map[string]bool {
	return map[string]bool{
		"ADMIN_BFF_ROUTES_ENABLED":              cfg.AdminBFF.RoutesEnabled,
		"ADMIN_BFF_SHADOW_READS":                cfg.AdminBFF.ShadowReadsEnabled,
		"ADMIN_BFF_AUTH_PROFILE_READS_ENABLED":  cfg.AdminBFF.AuthProfileReadsEnabled,
		"ADMIN_BFF_USER_LIST_READS_ENABLED":     cfg.AdminBFF.UserListReadsEnabled,
		"ADMIN_BFF_DEPARTMENT_WRITES_ENABLED":   cfg.AdminBFF.DepartmentWritesEnabled,
		"ADMIN_BFF_ROLE_WRITES_ENABLED":         cfg.AdminBFF.RoleWritesEnabled,
		"ADMIN_BFF_MANAGED_USER_WRITES_ENABLED": cfg.AdminBFF.ManagedUserWritesEnabled,
		"ADMIN_BFF_USER_DELETE_ROUTE_ENABLED":   cfg.AdminBFF.UserDeleteRouteEnabled,
		"LEGACY_DELETE_IAM_DELEGATION_ENABLED":  cfg.Features.LegacyDeleteIAMDelegationEnabled,
		"IAM_DELETE_EVENT_CONSUMER_ENABLED":     cfg.Features.IAMDeleteEventConsumerEnabled,
		"OUTBOX_DISPATCHER_ENABLED":             cfg.Features.OutboxDispatcherEnabled,
	}
}
