// T075 shadow-read unit tests: the replay middleware compares BFF vs legacy
// responses and counts/logs only divergences; jsonEqual is order/whitespace
// insensitive; readChain drops the shadow stage when disabled.
package adminapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/hdw/vue-element-plus-admin/backend/internal/middleware"
	"github.com/hdw/vue-element-plus-admin/backend/internal/platform/observability"
)

func init() { gin.SetMode(gin.TestMode) }

// shadowHarness builds a BFF-style router (RequestID + shadow middleware)
// and a legacy engine replaying the same route.
func shadowHarness(t *testing.T, legacy, bff gin.HandlerFunc) (*gin.Engine, *observability.Registry, *bytes.Buffer) {
	t.Helper()
	legacyEngine := gin.New()
	legacyEngine.GET("/api/v1/roles", legacy)

	reg := observability.NewRegistry()
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	bffEngine := gin.New()
	bffEngine.Use(middleware.RequestID())
	bffEngine.GET("/api/v1/roles", shadowReadsMiddleware(legacyEngine, logger, reg), bff)
	return bffEngine, reg, &logBuf
}

// count finds a snapshot metric by name (0 when absent).
func count(t *testing.T, reg *observability.Registry, name string) float64 {
	t.Helper()
	for _, m := range reg.Snapshot() {
		if m.Name == name {
			return m.Value
		}
	}
	return 0
}

func TestShadowReadsMiddleware_Match(t *testing.T) {
	// The BFF and legacy handlers return the same document with different key
	// orders — jsonEqual must treat them as equal, log nothing, and count the
	// shadow without a mismatch.
	router, reg, logBuf := shadowHarness(t,
		func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"data": []gin.H{{"code": "admin", "name": "管理员"}}})
		},
		func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"data": []gin.H{{"name": "管理员", "code": "admin"}}})
		},
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/roles", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, float64(1), count(t, reg, "shadow_reads_total"))
	require.Equal(t, float64(0), count(t, reg, "shadow_reads_mismatches_total"))
	require.Empty(t, logBuf.String(), "a matching shadow must not log")
	// The client still receives the BFF response untouched.
	require.Equal(t, "admin", responseData(t, rec.Body.Bytes()))
}

func TestShadowReadsMiddleware_BodyMismatch(t *testing.T) {
	router, reg, logBuf := shadowHarness(t,
		func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"data": []gin.H{{"code": "admin", "name": "管理员"}}})
		},
		func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"data": []gin.H{{"code": "user", "name": "普通用户"}}})
		},
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/roles", nil)
	req.Header.Set(middleware.RequestIDHeader, "req-123")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, float64(1), count(t, reg, "shadow_reads_mismatches_total"))
	logged := logBuf.String()
	require.Contains(t, logged, "shadow read mismatch")
	require.Contains(t, logged, "req-123", "log carries the correlation request_id")
	require.Contains(t, logged, "path=/api/v1/roles")
	require.Contains(t, logged, "bodies_equal=false")
}

func TestShadowReadsMiddleware_StatusMismatch(t *testing.T) {
	// A 200 vs 403 divergence is exactly what shadow reads exist to catch.
	router, reg, logBuf := shadowHarness(t,
		func(c *gin.Context) { c.JSON(http.StatusForbidden, gin.H{"error": "denied"}) },
		func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"data": []gin.H{{"code": "admin"}}}) },
	)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/roles", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, float64(1), count(t, reg, "shadow_reads_mismatches_total"))
	require.Contains(t, logBuf.String(), "bff_status=200")
	require.Contains(t, logBuf.String(), "legacy_status=403")
}

func TestJSONEqual(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{"identical", `{"a":1,"b":[1,2]}`, `{"a":1,"b":[1,2]}`, true},
		{"key order", `{"a":1,"b":2}`, `{"b":2,"a":1}`, true},
		{"whitespace", `{ "a" : 1 }`, `{"a":1}`, true},
		{"numeric form", `{"a":1}`, `{"a":1.0}`, true},
		{"nested array element keys", `{"d":[{"x":1,"y":2}]}`, `{"d":[{"y":2,"x":1}]}`, true},
		{"array order is significant", `{"d":[{"x":1},{"y":2}]}`, `{"d":[{"y":2},{"x":1}]}`, false},
		{"different values", `{"a":1}`, `{"a":2}`, false},
		{"different shape", `{"a":1}`, `{"a":1,"b":2}`, false},
		{"invalid json falls back to bytes", `not json`, `not json`, true},
		{"invalid json byte mismatch", `not json`, `not json!`, false},
		{"one side invalid", `{"a":1}`, `nope`, false},
		{"empty", ``, ``, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, jsonEqual([]byte(tc.a), []byte(tc.b)))
		})
	}
}

// responseData extracts the first role code from a {"data":[...]} body.
func responseData(t *testing.T, body []byte) string {
	t.Helper()
	var resp struct {
		Data []struct {
			Code string `json:"code"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &resp))
	require.NotEmpty(t, resp.Data, "expected at least one role")
	return resp.Data[0].Code
}
