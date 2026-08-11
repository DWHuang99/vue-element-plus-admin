//go:build rollback

// Legacy monolith router constructor (pre-split route wiring).
//
// This is the frozen baseline composition extracted from cmd/server/main.go
// so the public HTTP compatibility suite (Checkpoint A) can run the same
// table-driven assertions against both this router and the Admin BFF router.
package server

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hdw/vue-element-plus-admin/backend/internal/auth"
	"github.com/hdw/vue-element-plus-admin/backend/internal/authorization"
	"github.com/hdw/vue-element-plus-admin/backend/internal/database"
	"github.com/hdw/vue-element-plus-admin/backend/internal/health"
	"github.com/hdw/vue-element-plus-admin/backend/internal/middleware"
	"github.com/hdw/vue-element-plus-admin/backend/internal/platform/observability"
	"github.com/hdw/vue-element-plus-admin/backend/internal/ratelimit"
	"github.com/hdw/vue-element-plus-admin/backend/internal/rbac"
)

// LegacyRateLimitConfig lives in server.go (shared across both build variants;
// the composition root passes it to both wirings). This file carries the legacy
// router constructor only — rollback build: `-tags rollback`.

// LegacyRouterDeps supplies the legacy monolith router's dependencies.
type LegacyRouterDeps struct {
	DB          *database.DB
	Logger      *slog.Logger
	CORSOrigins []string
	RateLimit   LegacyRateLimitConfig
	// Health overrides the default DB-only handler with the composition
	// root's per-module probe handler (T071); nil builds the default.
	Health *health.Handler
	// Metrics (T074) is the shared observability registry; nil disables the
	// recorder middleware and the /metrics route.
	Metrics *observability.Registry
	// UsersDeleteDelegate (T076) is the US5 delete-delegation target: when
	// set, the legacy POST /users/delete route calls the IAM DeleteUsers port
	// (outbox + receipt) instead of the legacy direct delete, per
	// LEGACY_DELETE_IAM_DELEGATION_ENABLED. nil keeps the pre-split behavior.
	UsersDeleteDelegate rbac.UsersDeleteDelegate
}

// NewLegacyRouter builds the pre-split monolith router: /health, /api/v1/auth
// and the /api/v1 RBAC routes (departments/roles/users) with the frozen
// middleware order (RequestID, CORS, auth, authorization). It returns the
// router and a close func that stops the rate limiter goroutines.
func NewLegacyRouter(deps LegacyRouterDeps) (*gin.Engine, func()) {
	router := gin.New()
	router.Use(middleware.Metrics(deps.Metrics))
	router.Use(middleware.RequestID())
	router.Use(middleware.CORS(deps.CORSOrigins))

	healthHandler := deps.Health
	if healthHandler == nil {
		healthHandler = health.NewHandler(deps.DB, deps.Logger)
	}
	router.GET("/health/live", healthHandler.Live)
	router.GET("/health/ready", healthHandler.Ready)
	if deps.Metrics != nil {
		router.GET("/metrics", middleware.MetricsEndpoint(deps.Metrics))
	}

	// Auth routes under /api/v1/auth.
	authorizationSvc := authorization.NewService(deps.DB.Pool)
	authSvc := auth.NewAuthServiceWithAuthorization(deps.DB.Pool, authorizationSvc)
	authHandler := auth.NewHandler(authSvc, deps.Logger)

	// Rate limiters (config-controlled; disabled when Enabled=false).
	registerLimiter := ratelimit.New(ratelimit.Config{
		Enabled:  deps.RateLimit.Enabled,
		Limit:    deps.RateLimit.RegisterIPHour,
		Duration: time.Hour,
	})
	loginIPLimiter := ratelimit.New(ratelimit.Config{
		Enabled:  deps.RateLimit.Enabled,
		Limit:    deps.RateLimit.LoginIP15Min,
		Duration: 15 * time.Minute,
	})
	loginUserLimiter := ratelimit.New(ratelimit.Config{
		Enabled:  deps.RateLimit.Enabled,
		Limit:    deps.RateLimit.LoginUser15Min,
		Duration: 15 * time.Minute,
	})

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

	// RBAC routes under /api/v1 (departments/roles/users), auth-protected.
	rbacSvc := rbac.NewRBACService(deps.DB.Pool)
	rbacHandler := rbac.NewHandler(rbacSvc, deps.Logger)
	rbacHandler.SetUsersDeleteDelegate(deps.UsersDeleteDelegate)

	rbacGroup := router.Group("/api/v1")
	rbacGroup.Use(middleware.Auth(authSvc))
	rbac.RegisterRoutes(rbacGroup, rbacHandler, authorizationSvc)

	return router, func() {
		registerLimiter.Close()
		loginIPLimiter.Close()
		loginUserLimiter.Close()
	}
}
