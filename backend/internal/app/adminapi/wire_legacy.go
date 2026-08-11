//go:build rollback

// Legacy monolith router wiring (T005/T031/T075/T076), preserved for the
// rollback support window (plan Phase 8 step 3: 回滚 binary/route 仍在支持
// 窗口保留). Built only with `-tags rollback`; the shipped binary never
// compiles this file, so it physically cannot serve legacy routes.
package adminapi

import (
	"github.com/gin-gonic/gin"

	"github.com/hdw/vue-element-plus-admin/backend/internal/logging"
	"github.com/hdw/vue-element-plus-admin/backend/internal/rbac"
	"github.com/hdw/vue-element-plus-admin/backend/internal/server"
)

// Router returns the active router and a close func that stops the rate
// limiter goroutines. With ADMIN_BFF_ROUTES_ENABLED=false (the default) the
// legacy monolith router serves; with it enabled the Admin BFF router serves
// the same public contract (T031: the compat suite passes against both
// wirings, so the switch is safe at this checkpoint). Both mount the same
// per-module readiness handler.
func (w *Wire) Router() (*gin.Engine, func()) {
	if w.cfg.AdminBFFRoutesEnabled {
		return w.adminBFFRouter()
	}
	return server.NewLegacyRouter(server.LegacyRouterDeps{
		DB:                  w.db,
		Logger:              w.logger,
		CORSOrigins:         w.cors,
		RateLimit:           w.rate,
		Health:              w.health,
		Metrics:             w.metrics,
		UsersDeleteDelegate: w.legacyDeleteDelegate(),
	})
}

// legacyDeleteDelegate returns the rbac delete delegation target (T076) when
// LEGACY_DELETE_IAM_DELEGATION_ENABLED is set, nil otherwise (pre-split direct
// delete). A nil delegate must also be handed to the shadow legacy router —
// shadow traffic is read-only and must never reach a delete path.
func (w *Wire) legacyDeleteDelegate() rbac.UsersDeleteDelegate {
	if !w.cfg.LegacyDeleteIAMDelegationEnabled {
		return nil
	}
	return &legacyUsersDeleteDelegate{iamSvc: w.iamSvc, pool: w.db.Pool}
}

// buildShadowReads builds the T075 replay middleware against a legacy router
// built alongside the Admin BFF router (rollback build). The shadow router
// mounts no metrics registry — shadow traffic is internal verification and
// must not pollute the production HTTP series.
func (w *Wire) buildShadowReads() (gin.HandlerFunc, func()) {
	if !w.cfg.ShadowReadsEnabled {
		return nil, func() {}
	}
	legacyRouter, legacyClose := server.NewLegacyRouter(server.LegacyRouterDeps{
		DB:          w.db,
		Logger:      w.logger,
		CORSOrigins: w.cors,
		RateLimit:   w.rate,
		Health:      w.health,
	})
	shadowReads := shadowReadsMiddleware(legacyRouter, logging.WithModule(w.logger, "adminbff", "shadow_reads"), w.metrics)
	w.logger.Info("shadow reads enabled: pure RBAC reads replay through the legacy router")
	return shadowReads, legacyClose
}
