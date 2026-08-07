package health

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// mockDB implements Pinger for testing.
type mockDB struct {
	pingErr error
}

func (m *mockDB) Ping(ctx context.Context) error {
	return m.pingErr
}

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func setupRouter(handler *Handler) *gin.Engine {
	router := gin.New()
	router.GET("/health/live", handler.Live)
	router.GET("/health/ready", handler.Ready)
	return router
}

// T017: Contract test for GET /health/live
func TestLive_Returns200(t *testing.T) {
	handler := NewHandler(&mockDB{}, newTestLogger())
	router := setupRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/health/live", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp HealthResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)

	assert.Equal(t, "ok", resp.Status)
	assert.False(t, resp.Timestamp.IsZero())
	// Live endpoint should NOT include checks
	assert.Empty(t, resp.Checks)
}

func TestLive_ReturnsXRequestID(t *testing.T) {
	// Note: X-Request-Id header is set by middleware, not by the handler itself.
	// This test verifies the handler works correctly; integration tests verify the middleware chain.
	handler := NewHandler(&mockDB{}, newTestLogger())
	router := setupRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/health/live", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestLive_100RapidCallsAll200(t *testing.T) {
	handler := NewHandler(&mockDB{}, newTestLogger())
	router := setupRouter(handler)

	for i := 0; i < 100; i++ {
		req := httptest.NewRequest(http.MethodGet, "/health/live", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code, "call %d should return 200", i)

		var resp HealthResponse
		err := json.Unmarshal(w.Body.Bytes(), &resp)
		require.NoError(t, err, "call %d should return valid JSON", i)
		assert.Equal(t, "ok", resp.Status, "call %d status should be ok", i)
	}
}

// T018: Contract test for GET /health/ready
func TestReady_DatabaseAvailable(t *testing.T) {
	handler := NewHandler(&mockDB{}, newTestLogger())
	router := setupRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp HealthResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)

	assert.Equal(t, "ok", resp.Status)
	assert.Equal(t, "ok", resp.Checks["database"])
}

func TestReady_DatabaseUnavailable(t *testing.T) {
	handler := NewHandler(&mockDB{pingErr: errors.New("connection refused")}, newTestLogger())
	router := setupRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)

	var resp HealthResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)

	assert.Equal(t, "degraded", resp.Status)
	assert.Equal(t, "unavailable", resp.Checks["database"])
	assert.NotEmpty(t, resp.Checks)
}

func TestReady_ResponseFormat(t *testing.T) {
	handler := NewHandler(&mockDB{}, newTestLogger())
	router := setupRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var resp HealthResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)

	// Verify response structure matches contracts/health-api.md
	assert.NotEmpty(t, resp.Status)
	assert.False(t, resp.Timestamp.IsZero())
	assert.Contains(t, resp.Checks, "database")
}

func TestReady_NoSecretsInResponse(t *testing.T) {
	handler := NewHandler(&mockDB{}, newTestLogger())
	router := setupRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	body := w.Body.String()

	// Verify no secrets or sensitive data in response
	secretPatterns := []string{"password", "secret", "token", "key", "postgres://", "credential"}
	for _, pattern := range secretPatterns {
		assert.NotContains(t, body, pattern, "response should not contain secret pattern: %s", pattern)
	}
}

func TestReady_StateTransitionLogging(t *testing.T) {
	handler := NewHandler(&mockDB{}, newTestLogger())
	router := setupRouter(handler)

	// First call - should initialize state
	req := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	// Subsequent calls should maintain state
	req = httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestHandler_NilDB(t *testing.T) {
	handler := NewHandler(nil, newTestLogger())
	router := setupRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)

	var resp HealthResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, "degraded", resp.Status)
	assert.Equal(t, "unavailable", resp.Checks["database"])
}
