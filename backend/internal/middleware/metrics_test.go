package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hdw/vue-element-plus-admin/backend/internal/platform/observability"
)

func TestMetricsMiddleware_RecordsStatusAndLatency(t *testing.T) {
	gin.SetMode(gin.TestMode)
	reg := observability.NewRegistry()

	router := gin.New()
	router.Use(Metrics(reg))
	router.GET("/ok", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	router.GET("/unauthorized", func(c *gin.Context) { c.Status(http.StatusUnauthorized) })

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ok", nil))
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/unauthorized", nil))

	assert.Equal(t, int64(2), reg.CounterValue("http_requests_total"))
	assert.Equal(t, int64(1), reg.CounterValue("http_401_total"))
	for _, m := range reg.Snapshot() {
		if m.Name == "http_401_ratio" {
			assert.Equal(t, float64(0.5), m.Value)
		}
	}
}

func TestMetricsMiddleware_StashesRegistryForErrorPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	reg := observability.NewRegistry()

	router := gin.New()
	router.Use(Metrics(reg))
	router.GET("/x", func(c *gin.Context) {
		assert.Same(t, reg, MetricsFromContext(c))
		c.Status(http.StatusBadRequest)
	})
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
}

func TestMetricsMiddleware_NilRegistryIsPassThrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(Metrics(nil))
	router.GET("/x", func(c *gin.Context) {
		assert.Nil(t, MetricsFromContext(c))
		c.Status(http.StatusOK)
	})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestMetricsEndpoint_ServesSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	reg := observability.NewRegistry()
	reg.AddCounter("http_requests_total", 3)

	router := gin.New()
	router.GET("/metrics", MetricsEndpoint(reg))

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	assert.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "http_requests_total 3\n")
}

func TestMetricsEndpoint_NilRegistryLeavesRouteUnmatched(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/metrics", MetricsEndpoint(nil))

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	assert.Equal(t, http.StatusNotFound, w.Code)
}
