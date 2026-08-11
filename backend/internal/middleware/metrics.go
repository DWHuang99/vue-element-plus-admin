// Metrics middleware (T074): the gin adapter for the platform observability
// registry. Both the legacy and Admin BFF routers mount it so every HTTP
// request records http_requests_total / 401 / 403 / 5xx classes and latency
// through observability.RecordHTTP. The registry is also stashed in the
// request context under metricsRegistryKey so the error-envelope path can
// count per-code failures without threading a field through every handler.
package middleware

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hdw/vue-element-plus-admin/backend/internal/platform/observability"
)

// metricsRegistryKey is the gin context key for the T074 registry.
const metricsRegistryKey = "admin_metrics_registry"

// Metrics wraps every request: it records the completed request (status and
// latency) after the chain runs. A nil registry is a pass-through (tests and
// routers without metrics enabled).
func Metrics(reg *observability.Registry) gin.HandlerFunc {
	if reg == nil {
		return func(c *gin.Context) { c.Next() }
	}
	return func(c *gin.Context) {
		c.Set(metricsRegistryKey, reg)
		start := time.Now()
		c.Next()
		ms := float64(time.Since(start).Microseconds()) / 1000.0
		observability.RecordHTTP(reg, c.Writer.Status(), ms)
	}
}

// MetricsEndpoint exposes the registry at GET /metrics as plain-text
// `name value` lines (observability.Registry.Handler is gin-free; this is the
// one-line gin adapter). Nil registry leaves the route unmatched.
func MetricsEndpoint(reg *observability.Registry) gin.HandlerFunc {
	if reg == nil {
		return func(c *gin.Context) { c.AbortWithStatus(404) }
	}
	return func(c *gin.Context) {
		reg.Handler().ServeHTTP(c.Writer, c.Request)
	}
}

// MetricsFromContext returns the registry stashed by Metrics middleware.
func MetricsFromContext(c *gin.Context) *observability.Registry {
	if v, ok := c.Get(metricsRegistryKey); ok {
		if reg, ok := v.(*observability.Registry); ok {
			return reg
		}
	}
	return nil
}
