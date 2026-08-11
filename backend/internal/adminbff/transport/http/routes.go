// BFF router (T030): the /api/v1 surface with the frozen middleware order
// (RequestID, CORS, authentication, per-route authorization) and the same
// rate limiting profile as the legacy router (register per-IP/hour; login
// per-IP + per-username per-15min). The /api prefix is preserved verbatim —
// the proxy and frontend rely on it.
//
// The transport layer stays infrastructure-free (architecture rule): the
// router receives the composed application service and ports; the
// composition root (internal/app/adminapi) builds the adapters.
package http

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hdw/vue-element-plus-admin/backend/internal/adminbff"
	"github.com/hdw/vue-element-plus-admin/backend/internal/iam"
	"github.com/hdw/vue-element-plus-admin/backend/internal/middleware"
	"github.com/hdw/vue-element-plus-admin/backend/internal/organization"
	"github.com/hdw/vue-element-plus-admin/backend/internal/platform/observability"
	"github.com/hdw/vue-element-plus-admin/backend/internal/ratelimit"
)

// RateLimitConfig mirrors the legacy limiter configuration for the BFF router.
type RateLimitConfig struct {
	Enabled        bool
	RegisterIPHour int
	LoginIP15Min   int
	LoginUser15Min int
}

// HealthRoutes is the optional operational health surface (built by the
// composition root, which owns the DB readiness probe).
type HealthRoutes struct {
	Live  gin.HandlerFunc
	Ready gin.HandlerFunc
}

// readChain assembles the read-route middleware. The shadow stage must be a
// chain member BEFORE authorization: RequirePermission calls c.Next()
// internally, so a shadow that ran after it would replace the writer after
// the handler already wrote — the capture would be empty. With shadow first,
// the writer capture wraps the whole authz+handler span, and an authz
// rejection (which aborts the remaining chain) still completes inside the
// capture — so a 401/403 on one side and 200 on the other is exactly the
// divergence shadow exists to catch. nil shadows drop out.
func readChain(authz, shadow gin.HandlerFunc) []gin.HandlerFunc {
	if shadow == nil {
		return []gin.HandlerFunc{authz}
	}
	return []gin.HandlerFunc{shadow, authz}
}

// RouterDeps supplies the BFF router's application dependencies.
type RouterDeps struct {
	Service     *adminbff.Service
	Auth        iam.AuthService
	Identity    iam.IdentityService
	Roles       iam.RoleService
	Departments organization.DepartmentService

	Logger      *slog.Logger
	CORSOrigins []string
	RateLimit   RateLimitConfig
	Health      HealthRoutes
	// Metrics (T074) is the shared observability registry; nil disables the
	// recorder middleware and the /metrics route.
	Metrics *observability.Registry
	// ShadowReads (T075) is the shadow-read middleware applied to the pure
	// RBAC read routes (GET /roles, /departments, /users) — never the auth
	// group. nil disables shadow reads.
	ShadowReads gin.HandlerFunc
}

// NewRouter builds the Admin BFF router and a close func that stops the rate
// limiter goroutines.
func NewRouter(deps RouterDeps) (*gin.Engine, func()) {
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

	router := gin.New()
	router.Use(middleware.Metrics(deps.Metrics))
	router.Use(middleware.RequestID())
	router.Use(middleware.CORS(deps.CORSOrigins))

	if deps.Health.Live != nil {
		router.GET("/health/live", deps.Health.Live)
	}
	if deps.Health.Ready != nil {
		router.GET("/health/ready", deps.Health.Ready)
	}
	if deps.Metrics != nil {
		router.GET("/metrics", middleware.MetricsEndpoint(deps.Metrics))
	}

	handler := NewHandler(deps.Service, deps.Auth, deps.Identity, deps.Roles, deps.Departments, deps.Logger)

	authGroup := router.Group("/api/v1/auth")
	{
		authGroup.POST("/register", ratelimit.GinIPRateLimit(registerLimiter), handler.Register)
		authGroup.POST("/login",
			ratelimit.GinIPRateLimit(loginIPLimiter),
			ratelimit.GinUsernameRateLimit(loginUserLimiter),
			handler.Login)
		authGroup.POST("/logout", BearerTokenFormat(), handler.Logout)
		authGroup.GET("/me", Authenticate(deps.Auth), handler.Me)
	}

	// RBAC read + department routes under /api/v1 with the per-route
	// permission matrix. Users write endpoints run the managed-user workflow
	// sagas (T056) with the contract Idempotency-Key header: authentication
	// and route authorization run BEFORE the idempotency middleware so a
	// revoked actor receives current 403 even for a previously succeeded key.
	rbacGroup := router.Group("/api/v1")
	rbacGroup.Use(Authenticate(deps.Auth))
	{
		// T075: shadow runs as a chain member BEFORE the authorization check
		// (readChain) so its writer capture spans the authz + handler execution
		// — RequirePermission's internal c.Next() would otherwise let the
		// handler write before the capture exists. Only the three pure GETs
		// are shadowed — the auth group never is (sessions must not slide
		// from shadow traffic).
		rbacGroup.GET("/roles", append(readChain(RequirePermission(deps.Identity, PermissionRolesRead), deps.ShadowReads), handler.ListRoles)...)
		rbacGroup.POST("/roles", RequirePermission(deps.Identity, PermissionRolesWrite), handler.SaveRole)
		rbacGroup.POST("/roles/delete", RequirePermission(deps.Identity, PermissionRolesWrite), handler.DeleteRoles)

		rbacGroup.GET("/departments", append(readChain(RequirePermission(deps.Identity, PermissionDepartmentsRead), deps.ShadowReads), handler.ListDepartments)...)
		rbacGroup.POST("/departments", RequirePermission(deps.Identity, PermissionDepartmentsWrite), handler.SaveDepartment)
		rbacGroup.POST("/departments/delete", RequirePermission(deps.Identity, PermissionDepartmentsWrite), handler.DeleteDepartments)

		rbacGroup.GET("/users", append(readChain(RequirePermission(deps.Identity, PermissionUsersRead), deps.ShadowReads), handler.ListUsers)...)
		rbacGroup.POST("/users", RequirePermission(deps.Identity, PermissionUsersWrite), IdempotencyKey(), handler.SaveUserWorkflow)
		rbacGroup.POST("/users/delete", RequirePermission(deps.Identity, PermissionUsersWrite), IdempotencyKey(), handler.DeleteUsersWorkflow)
	}

	return router, func() {
		registerLimiter.Close()
		loginIPLimiter.Close()
		loginUserLimiter.Close()
	}
}
