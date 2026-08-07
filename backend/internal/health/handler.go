package health

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// Pinger is an interface for checking database connectivity.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Handler holds health check dependencies.
type Handler struct {
	db     Pinger
	logger *slog.Logger

	mu              sync.RWMutex
	dbAvailable     bool
	lastDBState     bool
	stateInitialized bool
}

// HealthResponse is the JSON response for health check endpoints.
type HealthResponse struct {
	Status    string            `json:"status"`
	Timestamp time.Time         `json:"timestamp"`
	Checks    map[string]string `json:"checks,omitempty"`
}

// NewHandler creates a new health check Handler.
func NewHandler(db Pinger, logger *slog.Logger) *Handler {
	return &Handler{
		db:     db,
		logger: logger,
	}
}

// Live handles the GET /health/live endpoint.
// It always returns 200 as long as the process is running.
func (h *Handler) Live(c *gin.Context) {
	c.JSON(http.StatusOK, HealthResponse{
		Status:    "ok",
		Timestamp: time.Now().UTC(),
	})
}

// Ready handles the GET /health/ready endpoint.
// It returns 200 if all critical dependencies (database) are available.
// It returns 503 if any dependency is unavailable.
func (h *Handler) Ready(c *gin.Context) {
	checks := make(map[string]string)

	dbAvailable := h.checkDatabase()
	checks["database"] = boolToStatus(dbAvailable)

	h.mu.Lock()
	prevState := h.lastDBState
	if h.stateInitialized && prevState != dbAvailable {
		if dbAvailable {
			h.logger.Warn("database connection restored, service is now ready")
		} else {
			h.logger.Warn("database connection lost, service is degraded")
		}
	}
	h.lastDBState = dbAvailable
	h.mu.Unlock()

	if !h.stateInitialized {
		h.mu.Lock()
		h.stateInitialized = true
		h.lastDBState = dbAvailable
		h.mu.Unlock()
	}

	status := "ok"
	httpStatus := http.StatusOK

	if !dbAvailable {
		status = "degraded"
		httpStatus = http.StatusServiceUnavailable
	}

	c.JSON(httpStatus, HealthResponse{
		Status:    status,
		Timestamp: time.Now().UTC(),
		Checks:    checks,
	})
}

// checkDatabase pings the database with a 5-second timeout.
func (h *Handler) checkDatabase() bool {
	if h.db == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return h.db.Ping(ctx) == nil
}

// boolToStatus converts a boolean to a health status string.
func boolToStatus(ok bool) string {
	if ok {
		return "ok"
	}
	return "unavailable"
}
