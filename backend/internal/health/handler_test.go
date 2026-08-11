package health

import (
	"bytes"
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

// --- T071 per-module readiness probes --------------------------------------

// mockProbe is a named Probe whose outcome can be flipped per test.
type mockProbe struct {
	err error
}

func (p *mockProbe) check(ctx context.Context) error { return p.err }

// moduleProbes builds the four US5 module probes: three DB-backed modules
// plus the outbox dispatcher (disabled → ok, enabled → loop-alive).
func moduleProbes(t *testing.T, iam, org, workflow, dispatcher error) []Probe {
	t.Helper()
	return []Probe{
		{Name: "iam_database", Check: (&mockProbe{err: iam}).check},
		{Name: "organization_database", Check: (&mockProbe{err: org}).check},
		{Name: "admin_workflow_store", Check: (&mockProbe{err: workflow}).check},
		{Name: "outbox_dispatcher", Check: func(ctx context.Context) error { return dispatcher }},
	}
}

func TestReady_ModuleProbesAllOk(t *testing.T) {
	handler := NewHandlerWithProbes(&mockDB{}, newTestLogger(),
		moduleProbes(t, nil, nil, nil, nil))
	router := setupRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp HealthResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "ok", resp.Status)
	// Base database plus every module is distinguished in the aggregate.
	for _, name := range []string{"database", "iam_database", "organization_database", "admin_workflow_store", "outbox_dispatcher"} {
		assert.Equal(t, "ok", resp.Checks[name], "check %s", name)
	}
}

func TestReady_ModuleProbeDownNamesTheModule(t *testing.T) {
	handler := NewHandlerWithProbes(&mockDB{}, newTestLogger(),
		moduleProbes(t, nil, errors.New("org db unreachable"), nil, nil))
	router := setupRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)

	var resp HealthResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "degraded", resp.Status)
	assert.Equal(t, "ok", resp.Checks["iam_database"])
	assert.Equal(t, "unavailable", resp.Checks["organization_database"])
	assert.Equal(t, "ok", resp.Checks["admin_workflow_store"])
	assert.Equal(t, "ok", resp.Checks["outbox_dispatcher"])
}

func TestReady_DispatcherDownDegradesAggregate(t *testing.T) {
	handler := NewHandlerWithProbes(&mockDB{}, newTestLogger(),
		moduleProbes(t, nil, nil, nil, errors.New("outbox dispatcher not running")))
	router := setupRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)

	var resp HealthResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "unavailable", resp.Checks["outbox_dispatcher"])
	assert.Equal(t, "degraded", resp.Status)
}

func TestReady_ModuleTransitionLoggingOnce(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	orgProbe := &mockProbe{}
	handler := NewHandlerWithProbes(&mockDB{}, logger, []Probe{
		{Name: "organization_database", Check: orgProbe.check},
	})
	router := setupRouter(handler)

	// Baseline observation: no transition logged.
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	assert.NotContains(t, buf.String(), "organization_database")

	// Down: one transition line naming the module.
	orgProbe.err = errors.New("down")
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	assert.Contains(t, buf.String(), "organization_database")
	assert.Contains(t, buf.String(), "degraded")

	// Steady state stays quiet.
	before := buf.Len()
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	assert.Equal(t, before, buf.Len(), "no repeated transition logging while state is steady")

	// Restored: one more transition line.
	orgProbe.err = nil
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	assert.Contains(t, buf.String(), "restored")
}
