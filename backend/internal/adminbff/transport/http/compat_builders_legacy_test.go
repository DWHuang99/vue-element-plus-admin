//go:build rollback

// Rollback-window router list for the public compatibility suite (T082): the
// legacy monolith router is still supported through the rollback window, so
// the suite proves the frozen public contract against both wirings — the
// legacy router and the Admin BFF router. Built only with `-tags rollback`;
// the shipped build uses compat_builders_default_test.go.
package http

import (
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hdw/vue-element-plus-admin/backend/internal/server"
)

// buildLegacyRouter wires the frozen baseline router (rollback build only).
func buildLegacyRouter(t *testing.T, env *testEnv) (*gin.Engine, func()) {
	t.Helper()
	router, closeFn := server.NewLegacyRouter(server.LegacyRouterDeps{
		DB:          env.db,
		Logger:      testLogger(),
		CORSOrigins: nil,                            // allow all (dev convenience)
		RateLimit:   server.LegacyRateLimitConfig{}, // disabled in tests
	})
	return router, closeFn
}

// builders returns both router constructors so the compatibility suite stays
// proven against the legacy wiring through the rollback support window.
func builders() []routerBuilder {
	return []routerBuilder{
		{name: "legacy", build: buildLegacyRouter},
		{name: "admin_bff", build: buildBFFRouter},
	}
}
